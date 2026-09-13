package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/memory"
)

// research_tools.go provides the read-only RESEARCH-phase tools: web_search and
// memory_search. Both are PolicyApproved (no filesystem mutation). They let the
// gated loop's PROBE/RESEARCH phases reach outside the codebase — the web and
// Prizm's Remembrance memory — instead of only grepping local files.

// MemorySearcher is the minimal surface the memory_search tool needs. The
// *remembrance.Client satisfies it; using an interface here avoids a
// tool→remembrance import cycle.
type MemorySearcher interface {
	Search(query, mode, category, tier string, limit int) (map[string]any, error)
}

// LocalMemoryStore is the minimal surface for local markdown memory fallback.
type LocalMemoryStore interface {
	Search(ctx context.Context, query string, limit int) ([]memory.Memory, error)
}

// MemorySearchTool queries Prizm's long-term memory mid-loop.
// It tries Recall (Remembrance) first, then falls back to local MarkdownStore.
type MemorySearchTool struct {
	Searcher    MemorySearcher   // Recall client (nil = disabled)
	LocalStore  LocalMemoryStore  // Local fallback (nil = no fallback)
}

func (t *MemorySearchTool) Name() string { return "memory_search" }
func (t *MemorySearchTool) Description() string {
	return "Searches Prizm's long-term memory (Remembrance) for relevant past context, decisions, and facts."
}
func (t *MemorySearchTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"query": {Type: "string", Description: "What to search memory for", Required: true},
			"limit": {Type: "number", Description: "Max results (default 5)", Required: false},
		},
		Output: ParamSpec{Type: "object", Description: "Matching memories with scores and snippets"},
	}
}
func (t *MemorySearchTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	query, ok := input["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return ToolResult{Success: false, Error: "required parameter 'query' must be a non-empty string"}, nil
	}
	limit := 5
	if l, ok := input["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	log.Printf("[MEMORY-SEARCH] query=%q limit=%d searcher=%v localStore=%v", query, limit, t.Searcher != nil, t.LocalStore != nil)

	// Try Recall first. Use "keyword" mode for fast, reliable results.
	// "balanced" and "hybrid" modes can hang due to FTS5 WAL issues.
	if t.Searcher != nil {
		results, err := t.Searcher.Search(query, "keyword", "", "", limit)
		if err == nil && results != nil {
			return ToolResult{Success: true, Output: results}, nil
		}
		log.Printf("[MEMORY-SEARCH] Recall search failed: err=%v, trying local fallback", err)
	}

	// Fallback to local store
	if t.LocalStore != nil {
		memories, err := t.LocalStore.Search(ctx, query, limit)
		if err != nil {
			log.Printf("[MEMORY-SEARCH] local store search error: %v", err)
		} else if len(memories) > 0 {
			output := map[string]any{
				"source":  "local",
				"results": memories,
				"count":   len(memories),
			}
			return ToolResult{Success: true, Output: output}, nil
		} else {
			// Empty search grounding: when search returns no results, tell the agent
			// to admit ignorance rather than fabricating details.
			output := map[string]any{
				"source":  "local",
				"results": []any{},
				"count":   0,
				"guidance": "No memories found for this query. If you don't have relevant memories about this topic, say 'I don't have that in my records' — do NOT fabricate or guess specific details about your own identity, project history, or relationships.",
			}
			return ToolResult{Success: true, Output: output}, nil
		}
	}

	return ToolResult{Success: false, Error: "memory search is not configured (both Remembrance and local store are unavailable)"}, nil
}

// WebSearchConfig configures the web_search tool. It targets a generic JSON
// search endpoint so it stays provider-agnostic: any service that accepts a
// query string and returns JSON works (Brave, Serper, SearXNG, etc.).
type WebSearchConfig struct {
	// Endpoint is the search API base URL. The query is added as the QueryParam.
	// Read from PRIZM_WEBSEARCH_URL when empty.
	Endpoint string
	// QueryParam is the query string parameter name (default "q").
	QueryParam string
	// APIKey, when set, is sent as a Bearer token. Read from PRIZM_WEBSEARCH_KEY.
	APIKey string
	// AuthHeader overrides the header name for the key (default "Authorization").
	AuthHeader string
}

// WebSearchTool performs a web search against a configured JSON endpoint.
type WebSearchTool struct {
	Config WebSearchConfig
	Client *http.Client
}

func (t *WebSearchTool) Name() string { return "web_search" }
func (t *WebSearchTool) Description() string {
	return "Searches the web for up-to-date information. Returns search results as JSON."
}
func (t *WebSearchTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"query": {Type: "string", Description: "The web search query", Required: true},
		},
		Output: ParamSpec{Type: "object", Description: "Search results from the configured provider"},
	}
}
func (t *WebSearchTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	query, ok := input["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return ToolResult{Success: false, Error: "required parameter 'query' must be a non-empty string"}, nil
	}

	endpoint := t.Config.Endpoint
	if endpoint == "" {
		endpoint = os.Getenv("PRIZM_WEBSEARCH_URL")
	}
	if endpoint == "" {
		return ToolResult{Success: false, Error: "web search is not configured — set PRIZM_WEBSEARCH_URL (and PRIZM_WEBSEARCH_KEY if required)"}, nil
	}
	apiKey := t.Config.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("PRIZM_WEBSEARCH_KEY")
	}
	queryParam := t.Config.QueryParam
	if queryParam == "" {
		queryParam = "q"
	}
	authHeader := t.Config.AuthHeader
	if authHeader == "" {
		authHeader = "Authorization"
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("invalid PRIZM_WEBSEARCH_URL: %v", err)}, nil
	}
	q := u.Query()
	q.Set(queryParam, query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		if authHeader == "Authorization" {
			req.Header.Set(authHeader, "Bearer "+apiKey)
		} else {
			req.Header.Set(authHeader, apiKey)
		}
	}

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("web search request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ToolResult{Success: false, Error: fmt.Sprintf("web search returned %d: %s", resp.StatusCode, truncateStr(string(body), 500))}, nil
	}

	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		// Non-JSON response — return raw text.
		return ToolResult{Success: true, Output: map[string]any{"results": string(body)}}, nil
	}
	return ToolResult{Success: true, Output: map[string]any{"results": parsed}}, nil
}

// RegisterResearchTools adds web_search and memory_search to the registry.
// Pass a nil searcher to register memory_search in a disabled state.
func RegisterResearchTools(registry *Registry, searcher MemorySearcher, localStore LocalMemoryStore, webCfg WebSearchConfig) *Registry {
	registry.Register(&MemorySearchTool{Searcher: searcher, LocalStore: localStore})
	registry.Register(&WebSearchTool{Config: webCfg})
	return registry
}

// truncateStr shortens a string for inclusion in an error message.
func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
