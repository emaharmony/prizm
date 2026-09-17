package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// QueryPlanner uses an LLM to analyze the user's message and generate
// structured search instructions. Instead of blindly searching the raw
// user message, the model extracts intent, keywords, and search type.
//
// This follows the MemGPT approach: the agent decides what to search for,
// not a heuristic that splits on spaces.
//
// If the LLM call fails or times out, the heuristic fallback extracts
// keywords from the message deterministically.
type QueryPlanner struct {
	model      string
	ollamaURL  string
	timeout    time.Duration
	httpClient *http.Client
	enabled    bool

	mu    sync.RWMutex
	cache map[string]*QueryPlanResult // simple cache: message hash → plan
}

// queryPlanResult is the structured output from the LLM query planner.
type QueryPlanResult struct {
	Intent             string   `json:"intent"`
	Keywords           []string `json:"keywords"`
	SearchType         string   `json:"search_type"`        // "keyword", "semantic", "hybrid"
	RecencyPreference  string   `json:"recency_preference"` // "recent", "all_time"
	AlternativeQueries []string `json:"alternative_queries"` // 2-3 alternative search queries for semantic questions
	RawResponse        string   `json:"-"`                   // for logging
}

// QueryPlanConfig holds configuration for the query planner.
type QueryPlanConfig struct {
	Enabled  bool          `yaml:"enabled" json:"enabled"`
	Model    string        `yaml:"model" json:"model"`
	OllamaURL string       `yaml:"ollama_url" json:"ollama_url"`
	Timeout  time.Duration `yaml:"timeout" json:"timeout"`
	Fallback string        `yaml:"fallback" json:"fallback"` // "heuristic" or "raw"
}

// DefaultQueryPlanConfig returns sensible defaults.
func DefaultQueryPlanConfig() QueryPlanConfig {
	return QueryPlanConfig{
		Enabled:   true,
		Model:     "deepseek-v4-flash:cloud",
		OllamaURL: "http://localhost:11434",
		Timeout:   300 * time.Second, // 5 min for local model cold starts
		Fallback:  "heuristic",
	}
}

// NewQueryPlanner creates a new query planner from config.
func NewQueryPlanner(cfg QueryPlanConfig) *QueryPlanner {
	return &QueryPlanner{
		model:     cfg.Model,
		ollamaURL: cfg.OllamaURL,
		timeout:   cfg.Timeout,
		enabled:   cfg.Enabled,
		httpClient: &http.Client{Timeout: cfg.Timeout},
		cache:     make(map[string]*QueryPlanResult),
	}
}

// Plan analyzes the user's message and generates search instructions.
// It returns keywords to search for, the search type, and a recency preference.
// If the LLM call fails, it falls back to heuristic keyword extraction.
func (qp *QueryPlanner) Plan(ctx context.Context, userMessage string, sessionMsgCount int, sessionAge time.Duration) *QueryPlanResult {
	if !qp.enabled {
		return qp.heuristicFallback(userMessage, sessionMsgCount, sessionAge)
	}

	// Check cache
	cacheKey := messageKey(userMessage)
	qp.mu.RLock()
	if cached, ok := qp.cache[cacheKey]; ok {
		qp.mu.RUnlock()
		log.Printf("[QUERY-PLANNER] cache hit for message (key=%s)", cacheKey)
		return cached
	}
	qp.mu.RUnlock()

	// Call LLM to generate search plan
	result, err := qp.callLLM(ctx, userMessage, sessionMsgCount, sessionAge)
	if err != nil {
		log.Printf("[QUERY-PLANNER] LLM call failed: %v, falling back to heuristic", err)
		return qp.heuristicFallback(userMessage, sessionMsgCount, sessionAge)
	}

	// Cache the result
	qp.mu.Lock()
	qp.cache[cacheKey] = result
	qp.mu.Unlock()

	log.Printf("[QUERY-PLANNER] plan: intent=%q keywords=%v search_type=%s recency=%s",
		result.Intent, result.Keywords, result.SearchType, result.RecencyPreference)

	return result
}

// callLLM sends the user message to the LLM and parses the structured response.
func (qp *QueryPlanner) callLLM(ctx context.Context, userMessage string, msgCount int, age time.Duration) (*QueryPlanResult, error) {
	prompt := fmt.Sprintf(`You are a memory search query planner for an AI agent named Lumi.

Given the user's message, conversation context, and what they might be looking for in long-term memory, generate a structured search plan.

Respond with ONLY a JSON object (no markdown, no thinking, no explanation):
{
  "intent": "brief description of what the user is asking about",
  "keywords": ["word1", "word2", "word3"],
  "search_type": "keyword" | "semantic" | "hybrid",
  "recency_preference": "recent" | "all_time",
  "alternative_queries": ["query2", "query3"]
}

Rules for keywords:
- Extract 3-7 search-relevant terms from the user's intent
- Use the CONCEPTS the user is asking about, not the exact words
- "who am I" → ["identity", "origin", "name", "Lumi", "naming"]
- "what did we decide about X" → ["decision", "X", "agreement", "choice"]
- "how does Y work" → ["Y", "mechanism", "architecture", "design"]
- Remove stop words (the, a, is, am, was, what, how, why, do, did, can)
- Include proper nouns and technical terms as-is
- If the user mentions a person, include their name as a keyword

Rules for search_type:
- "keyword" if the query has specific terms that will match exactly
- "semantic" if the query is conceptual or uses different words than what might be stored
- "hybrid" if you're unsure (default)

Rules for recency_preference:
- "recent" if the user is asking about something that just happened or is in progress
- "all_time" if the user is asking about history, identity, or anything that could be old

Rules for alternative_queries:
- Generate 2-3 alternative search queries that express the same intent using DIFFERENT words
- These help find memories that use different terminology than the user's question
- "Why is emotional continuity important?" → ["enjoyment tracking Lumi", "personality consistency AI agent", "SOUL.md empathetic warm"]
- "What did we decide about memory search?" → ["V80 memory architecture decision", "BM25 RRF hybrid search", "embedding keyword search choice"]
- Only include if search_type is "semantic" or "hybrid"; leave empty for "keyword"

Conversation context: %d messages, session age %s
User message: %s`, msgCount, age.Round(time.Second), userMessage)

	reqBody := map[string]any{
		"model":  qp.model,
		"prompt": prompt,
		"stream": false,
		"options": map[string]any{
			"num_predict": 256,
			"temperature": 0.1,
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}


	resp, err := qp.httpClient.Post(qp.ollamaURL+"/api/generate", "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var ollamaResp struct {
		Response string `json:"response"`
		Thinking string `json:"thinking"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	raw := strings.TrimSpace(ollamaResp.Response)
	if raw == "" {
		raw = strings.TrimSpace(ollamaResp.Thinking)
	}
	if raw == "" {
		return nil, fmt.Errorf("empty ollama response")
	}

	// Strip thinking/reasoning prefix if present
	raw = stripThinkingPrefix(raw)

	// Extract JSON from response (model might include surrounding text)
	result, err := parseQueryPlanJSON(raw)
	if err != nil {
		log.Printf("[QUERY-PLANNER] failed to parse LLM response: %v, raw: %s", err, TruncateStr(raw, 200))
		return nil, fmt.Errorf("parse response: %w", err)
	}

	result.RawResponse = raw
	return result, nil
}

// heuristicFallback extracts keywords from the user message without an LLM call.
// This is used when the LLM is unavailable or times out.
func (qp *QueryPlanner) heuristicFallback(userMessage string, msgCount int, age time.Duration) *QueryPlanResult {
	keywords := ExtractKeywords(userMessage)

	searchType := "keyword"
	if len(keywords) <= 2 {
		searchType = "semantic" // short queries benefit from semantic search
	}

	recency := "all_time"
	if msgCount > 2 && age < 30*time.Minute {
		recency = "recent"
	}

	return &QueryPlanResult{
		Intent:            "heuristic extraction from user message",
		Keywords:         keywords,
		SearchType:       searchType,
		RecencyPreference: recency,
	}
}

// extractKeywords does simple keyword extraction from a message:
// lowercase, split on spaces, remove stop words, take first 7 meaningful terms.
func ExtractKeywords(message string) []string {
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "am": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true, "did": true,
		"will": true, "would": true, "could": true, "should": true, "may": true,
		"might": true, "can": true, "shall": true, "must": true, "need": true,
		"what": true, "how": true, "why": true, "when": true, "where": true,
		"who": true, "which": true, "that": true, "this": true, "these": true,
		"those": true, "it": true, "its": true, "i": true, "me": true, "my": true,
		"we": true, "our": true, "you": true, "your": true, "he": true, "she": true,
		"they": true, "them": true, "their": true, "and": true, "or": true,
		"but": true, "if": true, "then": true, "so": true, "for": true,
		"to": true, "of": true, "in": true, "on": true, "at": true, "with": true,
		"about": true, "just": true, "really": true, "very": true, "also": true,
	}

	words := strings.Fields(strings.ToLower(message))
	var keywords []string
	seen := make(map[string]bool)
	for _, w := range words {
		// Remove punctuation and contractions
		w = strings.Trim(w, ".,!?;:'\"()[]{}")
		for _, suffix := range []string{"'s", "'t", "'re", "'ve", "'ll", "'d"} {
			w = strings.TrimSuffix(w, suffix)
		}
		if len(w) < 2 || stopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		keywords = append(keywords, w)
		if len(keywords) >= 7 {
			break
		}
	}

	// If all words were stop words, return original words (minus single chars)
	if len(keywords) == 0 {
		for _, w := range words {
			w = strings.Trim(w, ".,!?;:'\"()[]{}")
			if len(w) >= 2 && !seen[w] {
				seen[w] = true
				keywords = append(keywords, w)
				if len(keywords) >= 3 {
					break
				}
			}
		}
	}

	return keywords
}

// parseQueryPlanJSON extracts the JSON query plan from the LLM response.
// The model might include markdown code fences or other surrounding text.
func parseQueryPlanJSON(raw string) (*QueryPlanResult, error) {
	// Try to find JSON in the response
	jsonStart := strings.Index(raw, "{")
	jsonEnd := strings.LastIndex(raw, "}")
	if jsonStart < 0 || jsonEnd < 0 || jsonEnd <= jsonStart {
		return nil, fmt.Errorf("no JSON object found in response")
	}

	jsonStr := raw[jsonStart : jsonEnd+1]

	var result QueryPlanResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("unmarshal JSON: %w", err)
	}

	// Validate required fields
	if len(result.Keywords) == 0 {
		return nil, fmt.Errorf("no keywords in plan")
	}

	// Set defaults
	if result.SearchType == "" {
		result.SearchType = "hybrid"
	}
	if result.RecencyPreference == "" {
		result.RecencyPreference = "all_time"
	}

	return &result, nil
}

// stripThinkingPrefix removes <think>...</think> blocks and reasoning prefixes
// that some models include in their output.
func stripThinkingPrefix(s string) string {
	// Remove <think>...</think> blocks (used by some models)
	for {
		startTag := strings.Index(s, "<think>")
		if startTag < 0 {
			break
		}
		endTag := strings.Index(s, "</think>")
		if endTag < 0 {
			s = s[:startTag]
			break
		}
		s = s[:startTag] + s[endTag+len("</think>"):]
	}

	// Remove reasoning/tool-call prefixes that precede the JSON,
	// e.g. "<tool_call>I need to analyze this... response{...}".
	// Only strip when the leading text clearly contains reasoning markers
	// so we never mangle JSON that legitimately contains "{".
	if idx := strings.Index(s, "{"); idx > 0 {
		prefix := s[:idx]
		if strings.Contains(prefix, " response") ||
			strings.Contains(prefix, "<tool_call>") ||
			strings.Contains(prefix, " thinking") {
			s = s[idx:]
		}
	}

	// Remove markdown code fences
	s = strings.TrimPrefix(s, "```json\n")
	s = strings.TrimPrefix(s, "```\n")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	return s
}
// messageKey creates a cache key from a message.
func messageKey(msg string) string {
	words := strings.Fields(strings.ToLower(msg))
	if len(words) > 8 {
		words = words[:8]
	}
	return strings.Join(words, "_")
}

// truncateStr truncates a string to n characters.
func TruncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}