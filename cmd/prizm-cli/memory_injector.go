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
// conversation context. It uses the QueryPlanner to analyze the user's
// message and generate structured search keywords, then searches using
// those keywords instead of the raw user message.
//
// If the QueryPlanner is unavailable or times out, it falls back to
// heuristic keyword extraction.
type MemoryInjector struct {
	store        *memory.MarkdownStore
	planner      *memory.QueryPlanner
	cache        *memorySearchCache
	plannerCache *plannerResultCache
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

// plannerResultCache caches query planner results to avoid redundant LLM calls.
type plannerResultCache struct {
	mu      sync.RWMutex
	entries map[string]*memory.QueryPlanResult
	ttl     time.Duration
}

func newPlannerResultCache() *plannerResultCache {
	return &plannerResultCache{
		entries: make(map[string]*memory.QueryPlanResult),
		ttl:     5 * time.Minute,
	}
}

func (c *plannerResultCache) get(key string) *memory.QueryPlanResult {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.entries[key]
}

func (c *plannerResultCache) set(key string, result *memory.QueryPlanResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = result
}

// NewMemoryInjector creates a memory injector backed by the given store and
// optional query planner. If planner is nil, heuristic keyword extraction is used.
func NewMemoryInjector(store *memory.MarkdownStore, planner *memory.QueryPlanner) *MemoryInjector {
	return &MemoryInjector{
		store:        store,
		planner:      planner,
		cache:        &memorySearchCache{entries: make(map[string]*memCacheEntry), ttl: 5 * time.Minute, maxSlots: 50},
		plannerCache: newPlannerResultCache(),
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
// It uses the QueryPlanner (if available) to generate search keywords from the
// user's message, then searches using those keywords instead of the raw text.
//
// Parameters:
//   - ctx: context for cancellation
//   - mode: search (fresh question) or continuation (ongoing conversation)
//   - userMessage: the user's message text (used for search queries)
//   - sessionMsgCount: number of messages in the current session
//   - maxTokens: approximate token budget for memories (rough: 1 token ≈ 4 chars)
func (mi *MemoryInjector) InjectMemories(ctx context.Context, mode InjectMode, userMessage string, sessionMsgCount int, maxTokens int) string {
	switch mode {
	case ModeContinuation:
		return mi.injectRecent(ctx, maxTokens)
	default:
		return mi.injectPlannedSearch(ctx, userMessage, sessionMsgCount, maxTokens)
	}
}

// injectPlannedSearch uses the QueryPlanner to generate keywords from the
// user's message, then searches using those keywords.
func (mi *MemoryInjector) injectPlannedSearch(ctx context.Context, userMessage string, sessionMsgCount int, maxTokens int) string {
	if mi.store == nil || userMessage == "" {
		log.Printf("[MEMORY-INJECTOR] injectPlannedSearch skipped: store_nil=%v, msg_empty=%v", mi.store == nil, userMessage == "")
		return ""
	}

	// Step 1: Get search plan from QueryPlanner
	var plan *memory.QueryPlanResult
	if mi.planner != nil {
		plan = mi.planner.Plan(ctx, userMessage, sessionMsgCount, 0)
	}

	// Build the search query from planner keywords or raw message
	var searchQuery string
	if plan != nil && len(plan.Keywords) > 0 {
		searchQuery = strings.Join(plan.Keywords, " ")
		log.Printf("[MEMORY-INJECTOR] query planner: intent=%q keywords=%v search_type=%s query=%q",
			plan.Intent, plan.Keywords, plan.SearchType, searchQuery)
	} else {
		// Fallback: heuristic keyword extraction
		keywords := memory.ExtractKeywords(userMessage)
		if len(keywords) > 0 {
			searchQuery = strings.Join(keywords, " ")
			log.Printf("[MEMORY-INJECTOR] heuristic fallback: keywords=%v query=%q", keywords, searchQuery)
		} else {
			searchQuery = userMessage
			log.Printf("[MEMORY-INJECTOR] no keywords extracted, using raw message")
		}
	}

	// Step 2: Keyword search using the planned query
	cacheKey := queryKey(searchQuery)
	if cached := mi.cache.get(cacheKey); cached != nil {
		log.Printf("[MEMORY-INJECTOR] cache hit for key=%s", cacheKey)
		return formatMemories(cached, "Relevant Memories", maxTokens)
	}

	results, err := mi.store.Search(ctx, searchQuery, 20)
	log.Printf("[MEMORY-INJECTOR] keyword search results: count=%d, err=%v, query=%q", len(results), err, searchQuery)

	// Step 3: If keyword results are weak, try embedding search
	if (len(results) < 3 || err != nil) && mi.store != nil {
		embResults, embErr := mi.store.EmbeddingSearch(ctx, searchQuery, 10)
		if embErr == nil && len(embResults) > 0 {
			log.Printf("[MEMORY-INJECTOR] embedding search returned %d results, supplementing keyword results", len(embResults))
			seen := make(map[string]bool)
			for _, m := range results {
				seen[m.ID] = true
			}
			for _, m := range embResults {
				if !seen[m.ID] {
					results = append(results, m)
					seen[m.ID] = true
				}
			}
		} else if embErr != nil {
			log.Printf("[MEMORY-INJECTOR] embedding search failed: %v (keyword-only fallback)", embErr)
		}
	}

	if err != nil && len(results) == 0 {
		log.Printf("[MEMORY-SEARCH] search failed: %v", err)
		return mi.injectRecent(ctx, maxTokens)
	}

	if len(results) == 0 {
		log.Printf("[MEMORY-INJECTOR] no search results, falling back to recent")
		return mi.injectRecent(ctx, maxTokens)
	}

	mi.cache.set(cacheKey, results)

	log.Printf("[MEMORY-INJECTOR] search results detail: query=%q, count=%d", searchQuery, len(results))
	for i, m := range results {
		if i < 5 {
			log.Printf("[MEMORY-INJECTOR]   result[%d]: id=%s category=%s summary=%q", i, m.ID, m.Category, memory.TruncateStr(m.Summary, 80))
		}
	}

	return formatMemories(results, "Relevant Memories", maxTokens)
}

// injectSearch is the legacy search path (kept for backward compatibility).
// It searches using the raw user message without query planning.
func (mi *MemoryInjector) injectSearch(ctx context.Context, query string, maxTokens int) string {
	return mi.injectPlannedSearch(ctx, query, 0, maxTokens)
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
// NOTE: With QueryPlanner, this is less important since the planner decides
// the search strategy. It's kept for the continuation path.
func ChooseMode(sessionMsgCount int, sessionAge time.Duration) InjectMode {
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

		alloc := maxChars / minInt(len(memories), 10)
		if i < 3 {
			alloc = alloc * 2
		}

		var entry strings.Builder
		if m.Category != "" {
			entry.WriteString(fmt.Sprintf("- **%s** (%s)", m.Summary, m.Category))
		} else {
			entry.WriteString(fmt.Sprintf("- **%s**", m.Summary))
		}

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
			sb.WriteString("- *(...additional memories omitted for context budget)*\n")
			break
		}

		sb.WriteString(entryStr)
		charsUsed += len(entryStr)
	}

	return sb.String()
}

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

	now := time.Now()
	for k, v := range c.entries {
		if now.After(v.expires) {
			delete(c.entries, k)
		}
	}

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