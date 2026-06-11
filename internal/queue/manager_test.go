package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoalescingCollapsesToOneRerun(t *testing.T) {
	started := make(chan string)
	release := make(chan struct{})
	var builds atomic.Int32

	m := NewManager([]string{"docs"}, 1, func(ctx context.Context, slug, reason string) {
		builds.Add(1)
		started <- reason
		<-release
	})
	defer m.Stop(time.Second)

	if _, err := m.Enqueue("docs", "webhook"); err != nil {
		t.Fatal(err)
	}
	<-started // build 1 running

	// While running: first extra trigger parks as pending, the rest coalesce.
	if c, _ := m.Enqueue("docs", "webhook"); c {
		t.Error("first trigger while running should park, not coalesce")
	}
	for i := 0; i < 3; i++ {
		if c, _ := m.Enqueue("docs", "webhook"); !c {
			t.Error("expected trigger to coalesce into pending run")
		}
	}

	release <- struct{}{} // finish build 1
	<-started             // build 2 (the coalesced rerun) starts
	release <- struct{}{}

	// No third build should start.
	select {
	case <-started:
		t.Fatal("unexpected third build")
	case <-time.After(100 * time.Millisecond):
	}
	if got := builds.Load(); got != 2 {
		t.Errorf("builds = %d, want 2", got)
	}
}

func TestNoConcurrentBuildsOfSameSite(t *testing.T) {
	var current, maxSeen, builds atomic.Int32

	m := NewManager([]string{"docs"}, 4, func(ctx context.Context, slug, reason string) {
		builds.Add(1)
		c := current.Add(1)
		for {
			max := maxSeen.Load()
			if c <= max || maxSeen.CompareAndSwap(max, c) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		current.Add(-1)
	})

	// Coalescing makes the total build count non-deterministic (1..N); space
	// the triggers out so several builds actually run, then drain via Stop.
	for i := 0; i < 6; i++ {
		m.Enqueue("docs", "webhook")
		time.Sleep(5 * time.Millisecond)
	}
	m.Stop(5 * time.Second)

	if builds.Load() < 1 {
		t.Fatal("no builds ran")
	}
	if maxSeen.Load() != 1 {
		t.Errorf("observed %d concurrent builds of one site", maxSeen.Load())
	}
}

func TestGlobalConcurrencyCap(t *testing.T) {
	var current, maxSeen atomic.Int32
	var wg sync.WaitGroup
	slugs := []string{"a", "b", "c", "d", "e"}
	wg.Add(len(slugs))

	m := NewManager(slugs, 2, func(ctx context.Context, slug, reason string) {
		defer wg.Done()
		c := current.Add(1)
		for {
			max := maxSeen.Load()
			if c <= max || maxSeen.CompareAndSwap(max, c) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		current.Add(-1)
	})
	defer m.Stop(time.Second)

	for _, s := range slugs {
		m.Enqueue(s, "startup")
	}
	wg.Wait()
	if maxSeen.Load() > 2 {
		t.Errorf("global cap exceeded: %d concurrent builds", maxSeen.Load())
	}
}

func TestEnqueueUnknownSite(t *testing.T) {
	m := NewManager([]string{"a"}, 1, func(context.Context, string, string) {})
	defer m.Stop(time.Second)
	if _, err := m.Enqueue("nope", "webhook"); err == nil {
		t.Error("expected error for unknown site")
	}
}

func TestStopCancelsAfterGrace(t *testing.T) {
	started := make(chan struct{})
	m := NewManager([]string{"a"}, 1, func(ctx context.Context, slug, reason string) {
		close(started)
		<-ctx.Done() // build only ends on cancellation
	})
	m.Enqueue("a", "startup")
	<-started

	done := make(chan struct{})
	go func() {
		m.Stop(50 * time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not cancel in-flight build after grace")
	}
}
