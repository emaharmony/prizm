package main

import (
	"sync"
	"time"

	remcli "github.com/emaharmony/prizm/internal/remembrance"
)

// remembranceCache provides a TTL-based cache for Remembrance BuildContext results.
// This avoids calling BuildContext on every message within the same session
// when the context hasn't changed (60s TTL).
//
// Cache key format: "<agent_id>:<session_id>"
type remembranceCache struct {
	mu    sync.RWMutex
	ttl   time.Duration
	items map[string]*cacheEntry
}

type cacheEntry struct {
	resp      *remcli.ContextPackResponse
	expiresAt time.Time
}

func newRemembranceCache(ttl time.Duration) *remembranceCache {
	return &remembranceCache{
		ttl:   ttl,
		items: make(map[string]*cacheEntry),
	}
}

// Get returns a cached BuildContext result if it exists and hasn't expired.
// Expired entries are removed on read (lazy cleanup).
// Returns nil if not found or expired.
func (c *remembranceCache) Get(key string) *remcli.ContextPackResponse {
	c.mu.Lock() // Need write lock to delete expired entries
	defer c.mu.Unlock()

	entry, ok := c.items[key]
	if !ok {
		return nil
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.items, key) // Remove expired entry on read
		return nil
	}
	return entry.resp
}

// Set stores a BuildContext result with the configured TTL.
func (c *remembranceCache) Set(key string, resp *remcli.ContextPackResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = &cacheEntry{
		resp:      resp,
		expiresAt: time.Now().Add(c.ttl),
	}

	// Lazy eviction: remove expired entries when cache grows
	if len(c.items) > 100 {
		now := time.Now()
		for k, v := range c.items {
			if now.After(v.expiresAt) {
				delete(c.items, k)
			}
		}
	}
}

// Invalidate removes a cached entry (e.g., after a new capture).
func (c *remembranceCache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// Clear removes all cached entries.
func (c *remembranceCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*cacheEntry)
}
