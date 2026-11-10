package server

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestReplayCacheFirstSeen(t *testing.T) {
	c := NewReplayCache(time.Minute)
	now := time.Now()
	if c.SeenOrAdd("key1", now) {
		t.Fatal("expected false for unseen key")
	}
}

func TestReplayCacheReplay(t *testing.T) {
	c := NewReplayCache(time.Minute)
	now := time.Now()
	c.SeenOrAdd("key1", now)
	if !c.SeenOrAdd("key1", now.Add(time.Second)) {
		t.Fatal("expected true for replayed key")
	}
}

func TestReplayCacheDifferentKeysAreIndependent(t *testing.T) {
	c := NewReplayCache(time.Minute)
	now := time.Now()
	c.SeenOrAdd("key1", now)
	if c.SeenOrAdd("key2", now) {
		t.Fatal("expected false for different key")
	}
}

func TestReplayCacheFailsClosedAtCeiling(t *testing.T) {
	c := NewReplayCache(time.Minute)
	c.maxEntries = 2
	now := time.Now()

	if c.SeenOrAdd("k1", now) || c.SeenOrAdd("k2", now) {
		t.Fatal("expected first two keys to be admitted as unseen")
	}
	// Map is full; a new key must be rejected (treated as seen) rather than
	// admitted, so memory stays bounded.
	if !c.SeenOrAdd("k3", now) {
		t.Fatal("expected new key to be rejected at ceiling")
	}
	if c.Len() != 2 {
		t.Fatalf("expected cache capped at 2, got %d", c.Len())
	}
	// An already-tracked key is still detected as a replay.
	if !c.SeenOrAdd("k1", now) {
		t.Fatal("expected existing key to still register as replay at ceiling")
	}
}

func TestReplayCacheExpiredKeyIsEvicted(t *testing.T) {
	ttl := 50 * time.Millisecond
	c := NewReplayCache(ttl)
	now := time.Now()
	c.SeenOrAdd("key1", now)

	// Advance time past TTL and trigger a new call to force eviction
	future := now.Add(ttl + time.Millisecond)
	if c.SeenOrAdd("probe", future) {
		t.Fatal("expected probe to be unseen")
	}
	// key1 should have been evicted; re-adding it should return false
	if c.SeenOrAdd("key1", future) {
		t.Fatal("expected evicted key to be re-admissible")
	}
}

func TestReplayCacheExpiryBoundary(t *testing.T) {
	ttl := time.Minute
	c := NewReplayCache(ttl)
	now := time.Now()
	c.SeenOrAdd("key1", now)

	// One nanosecond before expiry: replay is detected.
	justBefore := now.Add(ttl - time.Nanosecond)
	if !c.SeenOrAdd("key1", justBefore) {
		t.Fatal("expected replay to be detected just before expiry")
	}

	// At exactly expiry (now == exp): !now.After(exp) is true, so the replay
	// IS still detected. An entry is valid for the closed interval [added, added+ttl].
	atExpiry := now.Add(ttl)
	if !c.SeenOrAdd("key1", atExpiry) {
		t.Fatal("expected replay to be detected at exact expiry boundary")
	}

	// One nanosecond after expiry: entry is evicted, key can be re-added.
	afterExpiry := now.Add(ttl + time.Nanosecond)
	c.SeenOrAdd("evict-probe", afterExpiry) // triggers eviction pass
	if c.SeenOrAdd("key1", afterExpiry) {
		t.Fatal("expected evicted key to be re-admissible after TTL")
	}
}

func TestReplayCacheConcurrent(t *testing.T) {
	c := NewReplayCache(time.Minute)
	now := time.Now()

	const goroutines = 50
	results := make([]bool, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			results[i] = c.SeenOrAdd("shared-key", now)
		}(i)
	}
	wg.Wait()

	seen := 0
	for _, r := range results {
		if r {
			seen++
		}
	}
	// Exactly one goroutine should have inserted; the rest should have seen a replay.
	if seen != goroutines-1 {
		t.Fatalf("expected %d replays, got %d", goroutines-1, seen)
	}
}

func TestReplayCacheSweepRemovesExpiredEntries(t *testing.T) {
	ttl := time.Minute
	c := NewReplayCache(ttl)
	now := time.Now()

	c.SeenOrAdd("fresh", now)
	c.SeenOrAdd("stale-1", now.Add(-2*ttl))
	c.SeenOrAdd("stale-2", now.Add(-3*ttl))

	removed := c.Sweep(now)
	if removed != 2 {
		t.Fatalf("expected 2 entries swept, got %d", removed)
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 entry left after sweep, got %d", c.Len())
	}
}

func TestReplayCacheStartSweeperExitsOnContextCancel(t *testing.T) {
	c := NewReplayCache(time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		c.RunSweeper(ctx, 5*time.Millisecond)
		close(done)
	}()

	// Let the sweeper tick at least once, then cancel.
	time.Sleep(15 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sweeper goroutine did not exit after context cancel")
	}
}

func TestReplayCacheConcurrentDistinctKeys(t *testing.T) {
	c := NewReplayCache(time.Minute)
	now := time.Now()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", i)
			if c.SeenOrAdd(key, now) {
				t.Errorf("expected false for unique key %s", key)
			}
		}(i)
	}
	wg.Wait()
}
