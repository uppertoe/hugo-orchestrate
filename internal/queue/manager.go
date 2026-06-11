// Package queue provides per-site build coalescing and a global concurrency
// cap. Each site has one worker goroutine fed by a buffered channel of depth
// one: a trigger arriving while a build runs parks in the buffer ("run once
// more"), and further triggers collapse into it. Two builds of the same site
// can never run concurrently.
package queue

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// BuildFunc runs one build for a site. Errors are handled by the callee
// (logged, recorded in state); the queue only schedules.
type BuildFunc func(ctx context.Context, slug, reason string)

// Manager owns the site workers and the global build semaphore.
type Manager struct {
	build   BuildFunc
	sem     chan struct{}
	workers map[string]*worker
	stopCh  chan struct{}
	stopped sync.Once
	wg      sync.WaitGroup

	runCtx    context.Context
	cancelRun context.CancelFunc
}

type worker struct {
	slug    string
	trigger chan string // buffered, depth 1: the coalesced pending run
}

// NewManager creates a Manager for the given site slugs.
func NewManager(slugs []string, maxConcurrent int, build BuildFunc) *Manager {
	runCtx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		build:     build,
		sem:       make(chan struct{}, maxConcurrent),
		workers:   make(map[string]*worker, len(slugs)),
		stopCh:    make(chan struct{}),
		runCtx:    runCtx,
		cancelRun: cancel,
	}
	for _, slug := range slugs {
		w := &worker{slug: slug, trigger: make(chan string, 1)}
		m.workers[slug] = w
		m.wg.Add(1)
		go m.runWorker(w)
	}
	return m
}

// Enqueue requests a build of slug. If a run is already pending the trigger
// coalesces into it and Enqueue reports coalesced=true.
func (m *Manager) Enqueue(slug, reason string) (coalesced bool, err error) {
	w, ok := m.workers[slug]
	if !ok {
		return false, fmt.Errorf("unknown site %q", slug)
	}
	select {
	case w.trigger <- reason:
		return false, nil
	default:
		return true, nil
	}
}

func (m *Manager) runWorker(w *worker) {
	defer m.wg.Done()
	for {
		select {
		case <-m.stopCh:
			return
		case reason := <-w.trigger:
			select {
			case m.sem <- struct{}{}:
			case <-m.stopCh:
				return
			}
			m.build(m.runCtx, w.slug, reason)
			<-m.sem
		}
	}
}

// Stop drains: workers stop picking up new triggers, in-flight builds get up
// to grace to finish, then their contexts are cancelled. Blocks until all
// workers exit.
func (m *Manager) Stop(grace time.Duration) {
	m.stopped.Do(func() {
		close(m.stopCh)
		done := make(chan struct{})
		go func() {
			m.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(grace):
			m.cancelRun()
			<-done
		}
		m.cancelRun()
	})
}
