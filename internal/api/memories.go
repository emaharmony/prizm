// Package api provides the Prizm HTTP API server.
// This file implements the memory visualizer API endpoints:
//
//	GET /api/v1/memories           — list/search memories (local MarkdownStore)
//	GET /api/v1/memories/stats     — aggregate memory stats
//	GET /api/v1/memories/categories — distinct categories
//	GET /api/v1/memories/{id}      — single memory lookup
package api

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/memory"
)

// memoryResponse is the JSON envelope returned by GET /api/v1/memories.
type memoryResponse struct {
	Memories []memoryEntry `json:"memories"`
	Total    int           `json:"total"`
	Query    string         `json:"query,omitempty"`
	Source   string         `json:"source"` // "local" (MarkdownStore only)
}

// memoryEntry is a single memory in the API response.
type memoryEntry struct {
	ID         string            `json:"id"`
	Content    string            `json:"content"`
	Category   string            `json:"category"`
	Tier       string            `json:"tier"`
	Summary    string            `json:"summary"`
	KeyTopics  []string          `json:"key_topics"`
	Source     string            `json:"source"`
	AgentID    string            `json:"agent_id"`
	SessionID  string            `json:"session_id,omitempty"`
	ProjectID  string            `json:"project_id,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedAt  string            `json:"created_at"`
	AccessedAt string            `json:"accessed_at,omitempty"`
}

// handleMemories handles GET /api/v1/memories.
//
// Query parameters:
//
//	q      — search query (keyword match, optional)
//	limit  — max memories to return (default 100)
//	sort   — sort order: "newest" (default), "oldest", "category"
//	source — "local" (default, only option now)
func (s *Server) handleMemories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query().Get("q")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	sortOrder := r.URL.Query().Get("sort")
	if sortOrder == "" {
		sortOrder = "newest"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	entries := s.fetchLocalMemories(ctx, query, limit)

	// Sort
	sortMemories(entries, sortOrder)

	// Apply limit
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}

	writeJSON(w, memoryResponse{
		Memories: entries,
		Total:    len(entries),
		Query:    query,
		Source:   "local",
	})
}

// fetchLocalMemories reads memories from the MarkdownStore.
func (s *Server) fetchLocalMemories(ctx context.Context, query string, limit int) []memoryEntry {
	if s.memStore == nil {
		return nil
	}

	var memories []memory.Memory
	var err error

	if query != "" {
		memories, err = s.memStore.Search(ctx, query, limit)
	} else {
		memories, err = s.memStore.ListRecent(ctx, limit)
	}

	if err != nil {
		log.Printf("[API] memory store error: %v", err)
		return nil
	}

	entries := make([]memoryEntry, 0, len(memories))
	for _, m := range memories {
		entries = append(entries, memoryToEntry(m))
	}
	return entries
}

// sortMemories sorts entries by the given order.
func sortMemories(entries []memoryEntry, order string) {
	switch order {
	case "oldest":
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].CreatedAt < entries[j].CreatedAt
		})
	case "category":
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Category != entries[j].Category {
				return entries[i].Category < entries[j].Category
			}
			return entries[i].CreatedAt > entries[j].CreatedAt
		})
	default: // "newest"
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].CreatedAt > entries[j].CreatedAt
		})
	}
}

// memoryToEntry converts a memory.Memory to an API memoryEntry.
func memoryToEntry(m memory.Memory) memoryEntry {
	e := memoryEntry{
		ID:         m.ID,
		Content:    m.Content,
		Category:   m.Category,
		Tier:       m.Tier,
		Summary:    m.Summary,
		KeyTopics:  m.KeyTopics,
		Source:     m.Source,
		AgentID:    m.AgentID,
		SessionID:  m.SessionID,
		ProjectID:  m.ProjectID,
		Metadata:   m.Metadata,
		CreatedAt:  m.CreatedAt.Format(time.RFC3339),
		AccessedAt: m.AccessedAt.Format(time.RFC3339),
	}
	if e.Source == "" {
		e.Source = "local"
	}
	return e
}

// handleMemoriesDetail handles GET /api/v1/memories/{id} — single memory lookup.
func (s *Server) handleMemoriesDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/memories/")
	if id == "" {
		http.Error(w, "memory id required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if s.memStore != nil {
		mem, err := s.memStore.Get(ctx, id)
		if err == nil && mem != nil {
			writeJSON(w, memoryToEntry(*mem))
			return
		}
	}

	writeJSONError(w, "memory not found", http.StatusNotFound)
}

// handleMemoriesCategories returns the list of distinct categories.
func (s *Server) handleMemoriesCategories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.memStore == nil {
		writeJSON(w, []string{})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	memories, err := s.memStore.ListRecent(ctx, 0) // get all
	if err != nil {
		log.Printf("[API] memory store error: %v", err)
		writeJSON(w, []string{})
		return
	}

	seen := make(map[string]bool)
	categories := make([]string, 0)
	for _, m := range memories {
		if m.Category != "" && !seen[m.Category] {
			seen[m.Category] = true
			categories = append(categories, m.Category)
		}
	}
	sort.Strings(categories)
	writeJSON(w, categories)
}

// handleMemoriesStats returns aggregate stats about memories.
func (s *Server) handleMemoriesStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.memStore == nil {
		writeJSON(w, map[string]any{"total": 0, "categories": []string{}, "tiers": map[string]int{}})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	memories, err := s.memStore.ListRecent(ctx, 0) // get all
	if err != nil {
		log.Printf("[API] memory store error: %v", err)
		writeJSON(w, map[string]any{"total": 0, "categories": []string{}, "tiers": map[string]int{}})
		return
	}

	categoryCounts := make(map[string]int)
	tierCounts := make(map[string]int)
	agentCounts := make(map[string]int)
	for _, m := range memories {
		categoryCounts[m.Category]++
		tierCounts[m.Tier]++
		if m.AgentID != "" {
			agentCounts[m.AgentID]++
		}
	}

	categories := make([]string, 0, len(categoryCounts))
	for c := range categoryCounts {
		categories = append(categories, c)
	}
	sort.Strings(categories)

	writeJSON(w, map[string]any{
		"total":        len(memories),
		"categories":   categoryCounts,
		"tiers":        tierCounts,
		"agents":       agentCounts,
		"categoryList": categories,
	})
}