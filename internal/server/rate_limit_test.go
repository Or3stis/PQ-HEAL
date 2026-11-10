package server

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRateLimiterDisabled(t *testing.T) {
	rl := NewRateLimiter(0, 0)
	now := time.Now()
	for i := range 100 {
		if !rl.Allow("client-1", now) {
			t.Fatalf("disabled limiter must always allow (i=%d)", i)
		}
	}
}

func TestRateLimiterBurstThenDeny(t *testing.T) {
	rl := NewRateLimiter(10, 3) // 10 rps, burst 3
	now := time.Now()
	for i := range 3 {
		if !rl.Allow("client-1", now) {
			t.Fatalf("expected token %d to be allowed", i)
		}
	}
	if rl.Allow("client-1", now) {
		t.Fatalf("expected 4th token at same instant to be denied")
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	rl := NewRateLimiter(10, 1) // 10 rps, burst 1
	t0 := time.Now()
	if !rl.Allow("client-1", t0) {
		t.Fatalf("expected first call to be allowed")
	}
	if rl.Allow("client-1", t0) {
		t.Fatalf("expected immediate retry to be denied")
	}
	// At 10 rps, one token refills in 100ms.
	if !rl.Allow("client-1", t0.Add(110*time.Millisecond)) {
		t.Fatalf("expected refill after 110ms to allow")
	}
}

func TestRateLimiterIndependentKeys(t *testing.T) {
	rl := NewRateLimiter(10, 1)
	now := time.Now()
	if !rl.Allow("a", now) || !rl.Allow("b", now) {
		t.Fatalf("expected independent buckets to each allow first call")
	}
	if rl.Allow("a", now) || rl.Allow("b", now) {
		t.Fatalf("expected each bucket to be exhausted independently")
	}
}

func TestRateLimiterSweepDropsIdleBuckets(t *testing.T) {
	rl := NewRateLimiter(10, 1)
	t0 := time.Now()
	rl.Allow("idle", t0)
	rl.Allow("active", t0.Add(time.Minute))

	removed := rl.Sweep(t0.Add(time.Minute), 30*time.Second)
	if removed != 1 {
		t.Fatalf("expected 1 idle bucket swept, got %d", removed)
	}
	if rl.Len() != 1 {
		t.Fatalf("expected 1 bucket left after sweep, got %d", rl.Len())
	}
}

// At the ceiling the limiter must make room for a new client rather than deny
// it. Denying would lock out every new legitimate client until the next sweep
// while leaving the id-rotating flooder that filled the map unaffected.
func TestRateLimiterEvictsIdlestAtCeiling(t *testing.T) {
	rl := NewRateLimiter(1000, 1000)
	rl.maxBuckets = 2
	now := time.Now()

	if !rl.Allow("a", now) || !rl.Allow("b", now.Add(time.Second)) {
		t.Fatalf("expected first two keys to be admitted")
	}
	// Map is full; the new key is admitted and the idlest bucket makes way.
	if !rl.Allow("c", now.Add(2*time.Second)) {
		t.Fatalf("expected new key to be admitted at bucket ceiling")
	}
	if rl.Len() != 2 {
		t.Fatalf("expected bucket map capped at 2, got %d", rl.Len())
	}
	rl.mu.Lock()
	_, keptA := rl.buckets["a"]
	_, keptC := rl.buckets["c"]
	rl.mu.Unlock()
	if keptA {
		t.Error("expected the least recently refilled bucket to be evicted")
	}
	if !keptC {
		t.Error("expected the new key to be tracked")
	}
	// Already-tracked keys still work while there's budget.
	if !rl.Allow("b", now.Add(3*time.Second)) {
		t.Fatalf("expected existing key to still be served at ceiling")
	}
}

// A key-rotation flood must not grow the bucket map past its ceiling.
func TestRateLimiterCeilingHoldsUnderKeyRotation(t *testing.T) {
	rl := NewRateLimiter(1000, 1000)
	rl.maxBuckets = 16
	now := time.Now()
	for i := range 5000 {
		rl.Allow(fmt.Sprintf("flood-%d", i), now.Add(time.Duration(i)*time.Millisecond))
		if got := rl.Len(); got > rl.maxBuckets {
			t.Fatalf("bucket map grew to %d, past the ceiling of %d", got, rl.maxBuckets)
		}
	}
}

func TestRateLimiterRunSweeperExitsOnContextCancel(t *testing.T) {
	rl := NewRateLimiter(10, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rl.RunSweeper(ctx, 5*time.Millisecond, time.Hour)
		close(done)
	}()
	time.Sleep(15 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunSweeper did not exit after context cancel")
	}
}
