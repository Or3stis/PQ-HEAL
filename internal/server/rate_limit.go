package server

import (
	"context"
	"sync"
	"time"
)

// RateLimiter is a per-key token bucket. Allow returns true and consumes one
// token when capacity is available; false otherwise. A zero rate disables
// limiting entirely (Allow always returns true).
// defaultMaxBuckets is a safety ceiling on the bucket map so a flood of unique
// keys cannot grow it without bound between sweeps. Sized far above any
// legitimate client count.
const defaultMaxBuckets = 50_000

// sampleSize is how many buckets are examined when freeing a slot at the ceiling.
const sampleSize = 8

type RateLimiter struct {
	rate       float64 // tokens per second; 0 disables
	burst      float64 // maximum tokens per bucket
	maxBuckets int
	mu         sync.Mutex
	buckets    map[string]*bucket
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

func NewRateLimiter(rate, burst float64) *RateLimiter {
	return &RateLimiter{
		rate:       rate,
		burst:      burst,
		maxBuckets: defaultMaxBuckets,
		buckets:    make(map[string]*bucket),
	}
}

func (r *RateLimiter) Allow(key string, now time.Time) bool {
	if r == nil || r.rate <= 0 || r.burst <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	b, ok := r.buckets[key]
	if !ok {
		if r.maxBuckets > 0 && len(r.buckets) >= r.maxBuckets {
			// At the safety ceiling, make room rather than denying the caller.
			// Denying unknown keys would bound the map but lock out every new
			// legitimate client until the next sweep, while doing nothing to the
			// flooder that filled it — it was getting a fresh bucket either way.
			r.evictIdlestLocked(sampleSize)
		}
		r.buckets[key] = &bucket{tokens: r.burst - 1, lastRefill: now}
		return true
	}
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * r.rate
		if b.tokens > r.burst {
			b.tokens = r.burst
		}
		b.lastRefill = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// evictIdlestLocked frees one slot by removing the least recently refilled of a
// small random sample. Go randomises map iteration order, so the first n entries
// are such a sample. Sampling rather than scanning the whole map is deliberate:
// this runs while the map is at its ceiling, which is exactly when a flood is in
// progress, and a full scan of maxBuckets entries per request under the lock
// would turn the safety ceiling into an amplification vector. Must be called
// with r.mu held and only when the map is non-empty.
func (r *RateLimiter) evictIdlestLocked(n int) {
	var idlestKey string
	var idlest time.Time
	seen := 0
	for k, b := range r.buckets {
		if seen == 0 || b.lastRefill.Before(idlest) {
			idlestKey, idlest = k, b.lastRefill
		}
		if seen++; seen >= n {
			break
		}
	}
	delete(r.buckets, idlestKey)
}

// Sweep removes buckets whose last refill is older than idleAfter. Returns
// the number of buckets removed.
func (r *RateLimiter) Sweep(now time.Time, idleAfter time.Duration) int {
	if r == nil || idleAfter <= 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for k, b := range r.buckets {
		if now.Sub(b.lastRefill) > idleAfter {
			delete(r.buckets, k)
			removed++
		}
	}
	return removed
}

// Len returns the current number of tracked buckets.
func (r *RateLimiter) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buckets)
}

// RunSweeper blocks calling Sweep on a ticker until ctx is done.
func (r *RateLimiter) RunSweeper(ctx context.Context, interval, idleAfter time.Duration) {
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
			r.Sweep(now.UTC(), idleAfter)
		}
	}
}
