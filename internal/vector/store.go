// Package vector provides Prizm's vector search capability (V15).
//
// Vector search enables semantic similarity queries over events, runs, and
// artifacts. "Find runs similar to this task", "what events are related to X",
// "show me artifacts closest to this description" — all powered by
// embedding vectors and cosine similarity.
//
// Architecture:
//   - VectorStore interface for upsert/search/delete
//   - EmbeddingProvider interface for pluggable embedding sources
//   - SQLiteVectorStore for persistence + in-memory HNSW-like index
//   - Built-in providers: Mock (deterministic test), OpenAI, Ollama
//
// The pure-Go index maintains Prizm's single-binary deployment story.
// No external vector database required. For V17+, a pgvector adapter
// can be added for horizontal scale.
package vector

import (
	"context"
	"time"
)

// VectorEntry represents a document with its embedding vector and metadata.
type VectorEntry struct {
	ID        string         `json:"id"`
	Content   string         `json:"content"`
	Vector    []float64      `json:"vector"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Source    string         `json:"source"`    // "event", "run_summary", "artifact"
	SourceID  string         `json:"source_id"` // ID of the source object
	CreatedAt time.Time      `json:"created_at"`
}

// SearchResult is a vector search result with similarity score.
type SearchResult struct {
	Entry VectorEntry `json:"entry"`
	Score float64     `json:"score"` // Cosine similarity [0, 1]
}

// SearchOptions controls vector search behavior.
type SearchOptions struct {
	TopK         int            `json:"top_k"`         // Number of results (default 10)
	MinScore     float64        `json:"min_score"`     // Minimum similarity (default 0.5)
	SourceFilter string         `json:"source_filter"` // Filter by source type
	Metadata     map[string]any `json:"metadata"`      // Filter by metadata
}

// DefaultSearchOptions returns sensible defaults.
func DefaultSearchOptions() SearchOptions {
	return SearchOptions{
		TopK:     10,
		MinScore: 0.5,
	}
}

// VectorFilter controls which entries to delete.
type VectorFilter struct {
	IDs    []string // Delete by specific IDs
	Source string   // Delete by source type
}

// VectorStore is the interface for vector storage and retrieval.
type VectorStore interface {
	// Upsert adds or updates a vector entry.
	Upsert(ctx context.Context, entry VectorEntry) error

	// UpsertBatch adds or updates multiple entries atomically.
	UpsertBatch(ctx context.Context, entries []VectorEntry) error

	// Search finds the top-K entries most similar to the query vector.
	Search(ctx context.Context, query []float64, opts SearchOptions) ([]SearchResult, error)

	// Get retrieves a single entry by ID.
	Get(ctx context.Context, id string) (*VectorEntry, error)

	// Delete removes vectors matching the filter.
	Delete(ctx context.Context, filter VectorFilter) error

	// Close releases resources.
	Close() error
}

// EmbeddingProvider generates vector embeddings from text.
type EmbeddingProvider interface {
	// Embed generates an embedding for a single text.
	Embed(ctx context.Context, text string) ([]float64, error)

	// EmbedBatch generates embeddings for multiple texts.
	EmbedBatch(ctx context.Context, texts []string) ([][]float64, error)

	// Dimension returns the embedding dimension.
	Dimension() int

	// Name returns the provider name.
	Name() string
}

// ValidateDimension checks that a vector has the expected dimension.
func ValidateDimension(vector []float64, expected int) bool {
	return len(vector) == expected
}

// CosineSimilarity computes cosine similarity between two vectors.
// Returns a value in [-1, 1], where 1 = identical direction.
func CosineSimilarity(a, b []float64) float64 {
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
	return dotProduct / (sqrt(normA) * sqrt(normB))
}

func sqrt(x float64) float64 {
	// Simple Newton's method for sqrt — avoids importing math
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 20; i++ {
		z = (z + x/z) / 2
	}
	return z
}
