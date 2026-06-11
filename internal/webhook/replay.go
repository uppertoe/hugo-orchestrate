package webhook

import (
	"sync"
	"time"
)

// replayCache is an in-memory delivery-ID dedupe window. A process restart
// clears it (documented and accepted). Entries older than the window are
// pruned on each check.
type replayCache struct {
	window time.Duration
	now    func() time.Time
	mu     sync.Mutex
	seen   map[string]time.Time
}

func newReplayCache(window time.Duration, now func() time.Time) *replayCache {
	if now == nil {
		now = time.Now
	}
	return &replayCache{window: window, now: now, seen: make(map[string]time.Time)}
}

// CheckAndRecord returns true if id was already seen inside the window,
// recording it otherwise.
func (c *replayCache) CheckAndRecord(id string) (replayed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	cutoff := now.Add(-c.window)
	for k, t := range c.seen {
		if t.Before(cutoff) {
			delete(c.seen, k)
		}
	}
	if _, ok := c.seen[id]; ok {
		return true
	}
	c.seen[id] = now
	return false
}
