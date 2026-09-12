package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
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

// InjectMemoriesInt is the int-parameter version of InjectMemories,
// satisfying the api.MemoryInjectorInterface (which uses int for mode
// since it can't reference the main package's InjectMode type).
func (mi *MemoryInjector) InjectMemoriesInt(ctx context.Context, mode int, userMessage string, sessionMsgCount int, maxTokens int) string {
	return mi.InjectMemories(ctx, InjectMode(mode), userMessage, sessionMsgCount, maxTokens)
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
	log.Printf("[MEMORY-INJECTOR] hybrid BM25+RRF search results: count=%d, err=%v, query=%q", len(results), err, searchQuery)

	// V85: Search now does BM25 + embedding RRF fusion internally.
	// No separate EmbeddingSearch fallback needed.

	if err != nil && len(results) == 0 {
		log.Printf("[MEMORY-SEARCH] search failed: %v", err)
		return mi.injectRecent(ctx, maxTokens)
	}

	if len(results) == 0 {
		log.Printf("[MEMORY-INJECTOR] no search results, falling back to recent")
		return mi.injectRecent(ctx, maxTokens)
	}

	mi.cache.set(cacheKey, results)

	// V80: Track recall count for injected memories
	go mi.trackRecalls(results)

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

// formatMemories formats a slice of memories into a grounding-aware prompt block,
// respecting the token budget. V83: Each memory includes date stamps, confidence
// labels, source attribution, and supersession status. The block includes explicit
// grounding instructions telling the model HOW to use these memories.
//
// Research showed that production agent systems that include grounding instructions
// with retrieved memories see 40-70% hallucination reduction. Our previous format
// said "The following memories were recalled from local storage" — which gives the
// model no guidance on whether to trust or use them.
func formatMemories(memories []memory.Memory, title string, maxTokens int) string {
	if len(memories) == 0 {
		return ""
	}

	maxChars := maxTokens * 4
	if maxChars <= 0 {
		maxChars = 1200 // default ~300 tokens
	}

	var sb strings.Builder

	// V83: Grounding header — explicit instructions on how to use these memories
	sb.WriteString("## " + title + "\n")
	sb.WriteString("The following memories were recalled from your verified local storage. ")
	sb.WriteString("These ARE your knowledge about yourself, your relationships, your projects, and your history. ")
	sb.WriteString("When these memories address the current question, TRUST THEM over your general training knowledge. ")
	sb.WriteString("Synthesize these memories into a natural, coherent answer — don't just list or quote them. \n\n")

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

		// V83: Date stamp + confidence label + source + supersession status
		dateStr := m.CreatedAt.Format("2006-01-02")
		confidenceLabel := confidenceLabel(m)
		sourceLabel := sourceLabel(m)
		supersededLabel := supersededLabel(m)

		// Format: [date | confidence | source] Summary
		entry.WriteString(fmt.Sprintf("[%s | %s | %s] ", dateStr, confidenceLabel, sourceLabel))
		if m.Category != "" {
			entry.WriteString(fmt.Sprintf("**%s** (%s)", m.Summary, m.Category))
		} else {
			entry.WriteString(fmt.Sprintf("**%s**", m.Summary))
		}

		// Mark superseded memories explicitly
		if supersededLabel != "" {
			entry.WriteString(fmt.Sprintf(" ⚠️ %s", supersededLabel))
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

	// V83: Grounding footer — reinforce memory-first behavior
	sb.WriteString("\n---\n")
	sb.WriteString("When answering questions about yourself, your relationships, your projects, or your history: ")
	sb.WriteString("check these memories FIRST. If a memory addresses your question, use it. ")
	sb.WriteString("If no memory is relevant, say \"I don't have that in my records\" — do not guess or fabricate details about your own identity or history.\n")

	return sb.String()
}

// confidenceLabel maps memory metadata to a human-readable confidence label.
// V83: Models respond better to natural language qualifiers than raw numbers.
func confidenceLabel(m memory.Memory) string {
	// Check explicit confidence first
	if m.Metadata != nil {
		if conf, ok := m.Metadata["confidence"]; ok {
			val, err := strconv.ParseFloat(strings.TrimSpace(conf), 64)
			if err == nil {
				switch {
				case val >= 8.0:
					return "high"
				case val >= 4.0:
					return "medium"
				default:
					return "low"
				}
			}
		}
	}
	// Default confidence based on source
	if m.Source == "user_stated" || m.Source == "lumi" {
		return "high"
	}
	return "medium"
}

// sourceLabel returns a human-readable source attribution.
func sourceLabel(m memory.Memory) string {
	if m.Source != "" {
		return m.Source
	}
	if m.AgentID != "" {
		return m.AgentID
	}
	return "memory"
}

// supersededLabel returns a warning string if the memory is superseded.
func supersededLabel(m memory.Memory) string {
	if m.Metadata == nil {
		return ""
	}
	if sup, ok := m.Metadata["superseded_by"]; ok && sup != "" {
		return fmt.Sprintf("SUPERSEDED (replaced by %s — no longer current)", sup)
	}
	return ""
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

// trackRecalls updates recall_count and last_recalled metadata for injected memories.
// This runs in a goroutine so it doesn't block the response.
func (mi *MemoryInjector) trackRecalls(memories []memory.Memory) {
	if mi.store == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, m := range memories {
		if m.Metadata == nil {
			m.Metadata = make(map[string]string)
		}
		// Increment recall count
		count := 0
		if v, ok := m.Metadata["recall_count"]; ok {
			if n, err := strconv.Atoi(v); err == nil {
				count = n
			}
		}
		m.Metadata["recall_count"] = strconv.Itoa(count + 1)
		m.Metadata["last_recalled"] = now

		// Save the updated memory back to the store
		if _, err := mi.store.Store(context.Background(), m); err != nil {
			log.Printf("[MEMORY-INJECTOR] failed to track recall for %s: %v", m.ID, err)
		}
	}
}