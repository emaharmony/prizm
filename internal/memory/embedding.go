package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EmbeddingIndex manages vector embeddings for memory entries.
// It generates embeddings via Ollama, stores them on disk, and provides
// cosine similarity search.
//
// Architecture:
//   - At parse time, each Memory entry gets an embedding generated from its Summary + Content
//   - Embeddings are stored in memory/embeddings.json
//   - Only changed entries are re-embedded (incremental updates)
//   - Search embeds the query and finds top-N by cosine similarity
//   - If Ollama is unavailable, search returns nil (fallback to keyword)
type EmbeddingIndex struct {
	mu         sync.RWMutex
	entries    map[string]*EmbeddingEntry // keyed by Memory.ID
	filePath   string                     // path to embeddings.json
	model      string                     // Ollama model name
	url        string                     // Ollama API URL
	dims       int                        // embedding dimensions
	httpClient EmbeddingClient
}

// EmbeddingEntry stores a single memory's embedding.
type EmbeddingEntry struct {
	ID          string    `json:"id"`
	SummaryHash string    `json:"summary_hash"` // fingerprint for change detection
	Embedding   []float64 `json:"embedding"`
	UpdatedAt   string    `json:"updated_at"` // RFC3339
}

// EmbeddingIndexFile is the on-disk format.
type EmbeddingIndexFile struct {
	Version int              `json:"version"`
	Model   string           `json:"model"`
	Dims    int              `json:"dimensions"`
	Entries []*EmbeddingEntry `json:"entries"`
}

// EmbeddingConfig holds configuration for the embedding index.
type EmbeddingConfig struct {
	Enabled          bool   `yaml:"enabled" json:"enabled"`
	Model            string `yaml:"model" json:"model"`
	URL              string `yaml:"url" json:"url"`
	Dimensions       int    `yaml:"dimensions" json:"dimensions"`
	IndexPath        string `yaml:"index_path" json:"index_path"`
	ReindexOnStartup bool   `yaml:"reindex_on_startup" json:"reindex_on_startup"`
}

// DefaultEmbeddingConfig returns sensible defaults.
func DefaultEmbeddingConfig() EmbeddingConfig {
	return EmbeddingConfig{
		Enabled:          true,
		Model:           "nomic-embed-text",
		URL:             "http://localhost:11434",
		Dimensions:      768,
		IndexPath:       "memory/embeddings.json",
		ReindexOnStartup: true,
	}
}

// EmbeddingClient is the interface for generating embeddings.
type EmbeddingClient interface {
	Embed(ctx context.Context, model, text string) ([]float64, error)
}

// OllamaEmbeddingClient is the real Ollama client.
type OllamaEmbeddingClient struct {
	URL       string
	TimeoutMs int
}

// Embed calls the Ollama /api/embeddings endpoint.
func (c *OllamaEmbeddingClient) Embed(ctx context.Context, model, text string) ([]float64, error) {
	reqBody := map[string]any{
		"model":  model,
		"prompt": text,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	client := &http.Client{Timeout: time.Duration(c.TimeoutMs) * time.Millisecond}
	resp, err := client.Post(c.URL+"/api/embeddings", "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var result struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}

	return result.Embedding, nil
}

// NewEmbeddingIndex creates a new embedding index.
func NewEmbeddingIndex(cfg EmbeddingConfig) *EmbeddingIndex {
	return &EmbeddingIndex{
		entries:    make(map[string]*EmbeddingEntry),
		filePath:   cfg.IndexPath,
		model:      cfg.Model,
		url:        cfg.URL,
		dims:       cfg.Dimensions,
		httpClient: &OllamaEmbeddingClient{URL: cfg.URL, TimeoutMs: 300000}, // 5 min
	}
}

// Load reads the embedding index from disk. Returns nil error if file doesn't exist.
func (ei *EmbeddingIndex) Load() error {
	ei.mu.Lock()
	defer ei.mu.Unlock()

	data, err := os.ReadFile(ei.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[EMBEDDING] no existing index at %s, starting fresh", ei.filePath)
			return nil
		}
		return fmt.Errorf("read index: %w", err)
	}

	var idx EmbeddingIndexFile
	if err := json.Unmarshal(data, &idx); err != nil {
		log.Printf("[EMBEDDING] corrupted index file, starting fresh: %v", err)
		return nil
	}

	for _, e := range idx.Entries {
		ei.entries[e.ID] = e
	}

	log.Printf("[EMBEDDING] loaded %d entries from %s (model=%s, dims=%d)",
		len(ei.entries), ei.filePath, idx.Model, idx.Dims)
	return nil
}

// Save writes the embedding index to disk.
func (ei *EmbeddingIndex) Save() error {
	ei.mu.RLock()
	entries := make([]*EmbeddingEntry, 0, len(ei.entries))
	for _, e := range ei.entries {
		entries = append(entries, e)
	}
	ei.mu.RUnlock()

	idx := EmbeddingIndexFile{
		Version: 1,
		Model:   ei.model,
		Dims:    ei.dims,
		Entries: entries,
	}

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(ei.filePath), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	if err := os.WriteFile(ei.filePath, data, 0644); err != nil {
		return fmt.Errorf("write index: %w", err)
	}

	log.Printf("[EMBEDDING] saved %d entries to %s", len(entries), ei.filePath)
	return nil
}

// IndexMemories generates embeddings for memories that are new or changed.
// Returns the number of new embeddings generated.
func (ei *EmbeddingIndex) IndexMemories(ctx context.Context, memories []Memory) (int, error) {
	ei.mu.RLock()

	var toEmbed []Memory
	for _, m := range memories {
		entry, exists := ei.entries[m.ID]
		hash := summaryHash(m.Summary)
		if !exists || entry.SummaryHash != hash {
			toEmbed = append(toEmbed, m)
		}
	}
	ei.mu.RUnlock()

	if len(toEmbed) == 0 {
		return 0, nil
	}

	log.Printf("[EMBEDDING] generating embeddings for %d memories (of %d total)", len(toEmbed), len(memories))

	generated := 0
	for _, m := range toEmbed {
		text := m.Summary
		if m.Content != "" && m.Content != m.Summary {
			text = m.Summary + ". " + TruncateStr(m.Content, 500)
		}

		emb, err := ei.httpClient.Embed(ctx, ei.model, text)
		if err != nil {
			log.Printf("[EMBEDDING] failed to embed %s: %v", m.ID, err)
			continue
		}

		if len(emb) != ei.dims {
			log.Printf("[EMBEDDING] dimension mismatch for %s: got %d, want %d", m.ID, len(emb), ei.dims)
			continue
		}

		ei.mu.Lock()
		ei.entries[m.ID] = &EmbeddingEntry{
			ID:          m.ID,
			SummaryHash: summaryHash(m.Summary),
			Embedding:   emb,
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
		}
		ei.mu.Unlock()
		generated++
	}

	if generated > 0 {
		if err := ei.Save(); err != nil {
			log.Printf("[EMBEDDING] failed to save index: %v", err)
		}
	}

	return generated, nil
}

// EmbeddingSearchResult is a memory ID with its similarity score.
type EmbeddingSearchResult struct {
	ID    string
	Score float64
}

// Search embeds the query and returns the top-N most similar memory IDs with scores.
// Returns nil if embeddings are unavailable (Ollama down, no entries, etc.).
func (ei *EmbeddingIndex) Search(ctx context.Context, query string, topN int) []EmbeddingSearchResult {
	if len(ei.entries) == 0 {
		return nil
	}

	queryEmb, err := ei.httpClient.Embed(ctx, ei.model, query)
	if err != nil {
		log.Printf("[EMBEDDING] query embedding failed: %v", err)
		return nil
	}

	if len(queryEmb) != ei.dims {
		log.Printf("[EMBEDDING] query dimension mismatch: got %d, want %d", len(queryEmb), ei.dims)
		return nil
	}

	ei.mu.RLock()
	defer ei.mu.RUnlock()

	type scored struct {
		id    string
		score float64
	}

	var results []scored
	for _, entry := range ei.entries {
		if len(entry.Embedding) != ei.dims {
			continue
		}
		score := cosineSimilarity(queryEmb, entry.Embedding)
		results = append(results, scored{id: entry.ID, score: score})
	}

	// Sort by score descending
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].score > results[i].score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if topN > len(results) {
		topN = len(results)
	}

	out := make([]EmbeddingSearchResult, topN)
	for i := 0; i < topN; i++ {
		out[i] = EmbeddingSearchResult{
			ID:    results[i].id,
			Score: results[i].score,
		}
	}

	return out
}

// cosineSimilarity computes the cosine similarity between two vectors.
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// summaryHash returns a fingerprint for change detection.
func summaryHash(s string) string {
	if len(s) > 64 {
		return fmt.Sprintf("%d:%s", len(s), s[:64])
	}
	return fmt.Sprintf("%d:%s", len(s), s)
}