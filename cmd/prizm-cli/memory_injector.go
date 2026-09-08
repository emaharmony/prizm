package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/memory"
)

// MemoryInjector decides how to inject memories into the prompt based on
// conversation context. It uses two modes:
//
//   - Search mode (default): When the user asks a question or starts a new topic,
//     it searches all memories by relevance to the query.
//   - Continuation mode: When the conversation is clearly continuing (same session,
//     recent activity), it injects recent context.
//
// A TTL-based cache avoids re-searching for the same query within a short window.
type MemoryInjector struct {
	store *memory.MarkdownStore
	cache *memorySearchCache
}

// memorySearchCache caches recent search results keyed by query hash.
type memorySearchCache struct {
	mu       sync.RWMutex
	entries  map[string]*memCacheEntry
	ttl      time.Duration
	maxSlots int
}

type memCacheEntry struct {
	results []memory.Memory
	query   string
	expires time.Time
}

// NewMemoryInjector creates a memory injector backed by the given store.
func NewMemoryInjector(store *memory.MarkdownStore) *MemoryInjector {
	return &MemoryInjector{
		store: store,
		cache: &memorySearchCache{
			entries:  make(map[string]*memCacheEntry),
			ttl:      5 * time.Minute,
			maxSlots: 50,
		},
	}
}

// InjectMode determines how memories should be injected.
type InjectMode int

const (
	// ModeSearch injects memories relevant to the user's message.
	// Used for fresh questions and topic shifts.
	ModeSearch InjectMode = iota

	// ModeContinuation injects recent memories for conversation continuity.
	// Used when the conversation is clearly continuing the same topic.
	ModeContinuation
)

// InjectMemories returns a formatted memory block for injection into the prompt.
// It uses the mode to decide between search and recent recall.
//
// Parameters:
//   - ctx: context for cancellation
//   - mode: search (fresh question) or continuation (ongoing conversation)
//   - userMessage: the user's message text (used for search queries)
//   - sessionMsgCount: number of messages in the current session (used for mode detection)
//   - maxTokens: approximate token budget for memories (rough: 1 token ≈ 4 chars)
func (mi *MemoryInjector) InjectMemories(ctx context.Context, mode InjectMode, userMessage string, sessionMsgCount int, maxTokens int) string {
	switch mode {
	case ModeSearch:
		return mi.injectSearch(ctx, userMessage, maxTokens)
	case ModeContinuation:
		return mi.injectRecent(ctx, maxTokens)
	default:
		return mi.injectSearch(ctx, userMessage, maxTokens)
	}
}

// injectSearch searches all memories for relevance to the user's message
// and returns a formatted block.
func (mi *MemoryInjector) injectSearch(ctx context.Context, query string, maxTokens int) string {
	if mi.store == nil || query == "" {
		log.Printf("[MEMORY-INJECTOR] injectSearch skipped: store_nil=%v, query_empty=%v", mi.store == nil, query == "")
		return ""
	}

	// Check cache first
	cacheKey := queryKey(query)
	if cached := mi.cache.get(cacheKey); cached != nil {
		log.Printf("[MEMORY-INJECTOR] cache hit for key=%s", cacheKey)
		return formatMemories(cached, "Relevant Memories", maxTokens)
	}

	// Search for relevant memories
	results, err := mi.store.Search(ctx, query, 20)
	log.Printf("[MEMORY-INJECTOR] search results: count=%d, err=%v", len(results), err)
	if err != nil {
		log.Printf("[MEMORY-SEARCH] search failed: %v", err)
		// Fall back to recent
		return mi.injectRecent(ctx, maxTokens)
	}

	if len(results) == 0 {
		log.Printf("[MEMORY-INJECTOR] no search results, falling back to recent")
		// No relevant memories — try recent as fallback
		return mi.injectRecent(ctx, maxTokens)
	}

	// Cache the results
	mi.cache.set(cacheKey, results)

	log.Printf("[MEMORY-INJECTOR] search results detail: query=%q, count=%d", query, len(results))
	for i, m := range results {
		if i < 5 {
			log.Printf("[MEMORY-INJECTOR]   result[%d]: id=%s category=%s summary=%q", i, m.ID, m.Category, truncate(m.Summary, 80))
		}
	}

	return formatMemories(results, "Relevant Memories", maxTokens)
}

// injectRecent returns the most recent memories for conversation continuity.
func (mi *MemoryInjector) injectRecent(ctx context.Context, maxTokens int) string {
	if mi.store == nil {
		return ""
	}

	recent, err := mi.store.ListRecent(ctx, 5)
	if err != nil {
		log.Printf("[MEMORY-RECENT] list recent failed: %v", err)
		return ""
	}

	if len(recent) == 0 {
		return ""
	}

	return formatMemories(recent, "Recent Context", maxTokens)
}

// ChooseMode decides the injection mode based on session context.
// If the session has recent messages (continuing conversation), use continuation mode.
// If it's a fresh question or topic shift, use search mode.
func ChooseMode(sessionMsgCount int, sessionAge time.Duration) InjectMode {
	// If the session has more than 2 messages and is less than 30 minutes old,
	// this is likely a continuation.
	if sessionMsgCount > 2 && sessionAge < 30*time.Minute {
		return ModeContinuation
	}
	return ModeSearch
}

// formatMemories formats a slice of memories into a prompt block,
// respecting the token budget. Higher-scoring memories get more chars.
func formatMemories(memories []memory.Memory, title string, maxTokens int) string {
	if len(memories) == 0 {
		return ""
	}

	// Convert token budget to char budget (rough: 1 token ≈ 4 chars)
	maxChars := maxTokens * 4
	if maxChars <= 0 {
		maxChars = 1200 // default ~300 tokens
	}

	var sb strings.Builder
	sb.WriteString("## " + title + "\n")
	sb.WriteString("The following memories were recalled from local storage:\n\n")

	charsUsed := 0
	for i, m := range memories {
		if i >= 20 {
			break
		}

		// Allocate more chars to top results
		alloc := maxChars / minInt(len(memories), 10)
		if i < 3 {
			alloc = alloc * 2 // top 3 get double
		}

		// Format the memory entry
		var entry strings.Builder
		if m.Category != "" {
			entry.WriteString(fmt.Sprintf("- **%s** (%s)", m.Summary, m.Category))
		} else {
			entry.WriteString(fmt.Sprintf("- **%s**", m.Summary))
		}

		// Add content, truncated to allocation
		content := m.Content
		if len(content) > alloc {
			content = content[:alloc] + "..."
		}
		if content != "" && content != m.Summary {
			entry.WriteString("\n  " + content)
		}
		entry.WriteString("\n")

		entryStr := entry.String()
		if charsUsed+len(entryStr) > maxChars {
			// Budget exceeded — add a truncation note and stop
			sb.WriteString("- *(...additional memories omitted for context budget)*\n")
			break
		}

		sb.WriteString(entryStr)
		charsUsed += len(entryStr)
	}

	return sb.String()
}

// minInt returns the smaller of a and b.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- Cache methods ---

func (c *memorySearchCache) get(key string) []memory.Memory {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expires) {
		return nil
	}
	return entry.results
}

func (c *memorySearchCache) set(key string, results []memory.Memory) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict expired entries
	now := time.Now()
	for k, v := range c.entries {
		if now.After(v.expires) {
			delete(c.entries, k)
		}
	}

	// Evict oldest if at capacity
	if len(c.entries) >= c.maxSlots {
		oldest := ""
		oldestTime := time.Now()
		for k, v := range c.entries {
			if v.expires.Before(oldestTime) {
				oldestTime = v.expires
				oldest = k
			}
		}
		if oldest != "" {
			delete(c.entries, oldest)
		}
	}

	c.entries[key] = &memCacheEntry{
		results: results,
		query:   key,
		expires: now.Add(c.ttl),
	}
}

// queryKey normalizes a search query for cache keying.
func queryKey(query string) string {
	q := strings.ToLower(strings.TrimSpace(query))
	words := strings.Fields(q)
	if len(words) > 5 {
		words = words[:5]
	}
	return strings.Join(words, "_")
}