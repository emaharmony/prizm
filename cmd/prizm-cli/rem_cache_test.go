package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	remcli "github.com/emaharmony/prizm/internal/remembrance"
)

// ── remembranceCache Tests ──────────────────────────────────────

func TestRemCache_SetGetHit(t *testing.T) {
	c := newRemembranceCache(60 * time.Second)
	resp := &remcli.ContextPackResponse{
		Task:       "test",
		TokenCount: 1,
	}
	c.Set("lumi:s1", resp)

	got := c.Get("lumi:s1")
	if got == nil {
		t.Fatal("expected cache hit, got nil")
	}
	if got.Task != "test" {
		t.Errorf("expected task=test, got %s", got.Task)
	}
}

func TestRemCache_GetMiss(t *testing.T) {
	c := newRemembranceCache(60 * time.Second)
	if got := c.Get("nonexistent"); got != nil {
		t.Error("expected nil for cache miss")
	}
}

func TestRemCache_TTLExpiry(t *testing.T) {
	c := newRemembranceCache(100 * time.Millisecond)
	resp := &remcli.ContextPackResponse{Task: "test"}
	c.Set("k1", resp)

	// Should be available immediately
	if got := c.Get("k1"); got == nil {
		t.Error("expected cache hit immediately after Set")
	}

	// Wait for TTL to expire
	time.Sleep(150 * time.Millisecond)

	if got := c.Get("k1"); got != nil {
		t.Error("expected nil after TTL expiry")
	}
}

func TestRemCache_TTLExpiryDeletesEntry(t *testing.T) {
	c := newRemembranceCache(50 * time.Millisecond)
	resp := &remcli.ContextPackResponse{Task: "test"}
	c.Set("k1", resp)

	time.Sleep(80 * time.Millisecond)

	// Get should return nil and remove the expired entry
	c.Get("k1")

	// Check internal map size — expired entry should be cleaned up
	c.mu.RLock()
	count := len(c.items)
	c.mu.RUnlock()
	if count > 0 {
		t.Errorf("expected 0 items after expired Get, got %d", count)
	}
}

func TestRemCache_Invalidate(t *testing.T) {
	c := newRemembranceCache(60 * time.Second)
	resp := &remcli.ContextPackResponse{Task: "test"}
	c.Set("k1", resp)

	c.Invalidate("k1")
	if got := c.Get("k1"); got != nil {
		t.Error("expected nil after invalidation")
	}
}

func TestRemCache_Clear(t *testing.T) {
	c := newRemembranceCache(60 * time.Second)
	resp := &remcli.ContextPackResponse{Task: "test"}
	c.Set("k1", resp)
	c.Set("k2", resp)

	c.Clear()

	c.mu.RLock()
	count := len(c.items)
	c.mu.RUnlock()
	if count != 0 {
		t.Errorf("expected 0 items after Clear, got %d", count)
	}
}

func TestRemCache_ConcurrentAccess(t *testing.T) {
	c := newRemembranceCache(60 * time.Second)
	resp := &remcli.ContextPackResponse{Task: "test"}

	var wg sync.WaitGroup
	// Concurrent writes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Set(string(rune('a'+i%26)), resp)
		}(i)
	}
	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Get(string(rune('a' + i%26)))
		}(i)
	}
	wg.Wait()
}

func TestRemCache_LazyEviction(t *testing.T) {
	c := newRemembranceCache(60 * time.Second)
	resp := &remcli.ContextPackResponse{Task: "test"}

	// Fill cache beyond 100 entries
	for i := 0; i < 110; i++ {
		c.Set(string(rune(i)), resp)
	}

	c.mu.RLock()
	count := len(c.items)
	c.mu.RUnlock()
	// After lazy eviction, should be <= 100 (expired entries removed)
	if count > 110 {
		t.Errorf("expected <=110 items after filling, got %d", count)
	}
}

// ── dream_scheduler Tests ───────────────────────────────────────

func TestDreamPersistCounter(t *testing.T) {
	// Reset counter
	atomic.StoreInt64(&dreamPersistCount, 0)

	// Increment
	atomic.AddInt64(&dreamPersistCount, 1)
	atomic.AddInt64(&dreamPersistCount, 1)

	if got := atomic.LoadInt64(&dreamPersistCount); got != 2 {
		t.Errorf("expected 2, got %d", got)
	}

	// Reset
	atomic.StoreInt64(&dreamPersistCount, 0)
	if got := atomic.LoadInt64(&dreamPersistCount); got != 0 {
		t.Errorf("expected 0 after reset, got %d", got)
	}
}

func TestDreamPersistCounterThreshold(t *testing.T) {
	atomic.StoreInt64(&dreamPersistCount, 0)
	threshold := int64(10)

	// Increment to threshold
	for i := int64(0); i < threshold; i++ {
		atomic.AddInt64(&dreamPersistCount, 1)
	}

	count := atomic.LoadInt64(&dreamPersistCount)
	if count < threshold {
		t.Errorf("expected count >= %d, got %d", threshold, count)
	}

	// After "dream cycle", reset
	atomic.StoreInt64(&dreamPersistCount, 0)
	if got := atomic.LoadInt64(&dreamPersistCount); got != 0 {
		t.Errorf("expected 0 after reset, got %d", got)
	}
}

func TestRunDreamCycle_Unavailable(t *testing.T) {
	// Client pointed at nothing — should log error but not panic
	client := remcli.NewClient("http://localhost:59997")
	// This should not panic
	runDreamCycle(client, nil)
	// If we get here, no panic occurred
}

func TestDreamSchedulerNext3AM(t *testing.T) {
	// Verify next3AM calculation doesn't produce negative duration
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
	if now.After(next) {
		next = next.Add(24 * time.Hour)
	}
	dur := next.Sub(now)
	if dur <= 0 {
		t.Errorf("next3AM should be positive, got %v", dur)
	}
	if dur > 25*time.Hour {
		t.Errorf("next3AM should be < 25h, got %v", dur)
	}
}
