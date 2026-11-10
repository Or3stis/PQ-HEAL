package server

import (
	"context"
	"sync"
	"time"
)

// defaultMaxReplayEntries is a safety ceiling on the replay-cache map so an
// adversarial flood of unique keys cannot grow it without bound between sweeps.
// Sized far above any legitimate working set (~10MB at this count).
const defaultMaxReplayEntries = 100_000

type ReplayCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	set        map[string]time.Time
}

func NewReplayCache(ttl time.Duration) *ReplayCache {
	return &ReplayCache{ttl: ttl, maxEntries: defaultMaxReplayEntries, set: make(map[string]time.Time)}
}

// SeenOrAdd records the key and returns true if it was already present within
// its TTL window. Expired entries for the same key are treated as fresh.
// O(1) per call — bulk garbage collection of unrelated expired keys is done
// out-of-band by Sweep / RunSweeper.
func (c *ReplayCache) SeenOrAdd(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	exp, ok := c.set[key]
	if ok && !now.After(exp) {
		return true
	}
	if !ok && c.maxEntries > 0 && len(c.set) >= c.maxEntries {
		// At the safety ceiling: fail closed (reject as a potential replay)
		// rather than admit a new entry and grow the map unbounded.
		return true
	}
	c.set[key] = now.Add(c.ttl)
	return false
}

// Sweep removes all entries whose TTL has elapsed at `now` and returns the
// count removed. Intended to be called periodically by RunSweeper.
func (c *ReplayCache) Sweep(now time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	removed := 0
	for k, exp := range c.set {
		if now.After(exp) {
			delete(c.set, k)
			removed++
		}
	}
	return removed
}

// Len returns the current number of entries (test/observability aid).
func (c *ReplayCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.set)
}

// RunSweeper blocks, calling Sweep on the given interval until ctx is done.
// Intended to run in its own goroutine for the server's lifetime.
func (c *ReplayCache) RunSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.Sweep(now.UTC())
		}
	}
}
