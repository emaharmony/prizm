package memory

import (
	"context"
	"testing"
)

// mockEmbeddingClient is a test double for EmbeddingClient.
type mockEmbeddingClient struct {
	embeddings map[string][]float64
	err        error
	callCount  int
	dims       int // if 0, uses default 768
}

func (m *mockEmbeddingClient) Embed(ctx context.Context, model, text string) ([]float64, error) {
	m.callCount++
	if m.err != nil {
		return nil, m.err
	}
	if emb, ok := m.embeddings[text]; ok {
		return emb, nil
	}
	// Return a simple embedding based on text length
	dims := m.dims
	if dims == 0 {
		dims = 768
	}
	emb := make([]float64, dims)
	for i := range emb {
		emb[i] = float64(len(text)) / 1000.0
	}
	return emb, nil
}

func TestEmbeddingIndexNew(t *testing.T) {
	cfg := DefaultEmbeddingConfig()
	idx := NewEmbeddingIndex(cfg)
	if idx == nil {
		t.Fatal("expected non-nil index")
	}
	if idx.model != "nomic-embed-text" {
		t.Errorf("expected model nomic-embed-text, got %s", idx.model)
	}
	if idx.dims != 768 {
		t.Errorf("expected 768 dims, got %d", idx.dims)
	}
}

func TestEmbeddingIndexIndexMemories(t *testing.T) {
	cfg := DefaultEmbeddingConfig()
	idx := NewEmbeddingIndex(cfg)
	idx.httpClient = &mockEmbeddingClient{}

	memories := []Memory{
		{ID: "mem1", Summary: "Lumi was named after luminescent light", Content: "Kirbii asked what to name the AI and Lumi chose her own name."},
		{ID: "mem2", Summary: "Ema prefers direct collaboration", Content: "Ema wants Lumi to push back when she sees a better path."},
	}

	count, err := idx.IndexMemories(context.Background(), memories)
	if err != nil {
		t.Fatalf("IndexMemories failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 embeddings, got %d", count)
	}
	if len(idx.entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(idx.entries))
	}
}

func TestEmbeddingIndexIncrementalUpdate(t *testing.T) {
	cfg := DefaultEmbeddingConfig()
	idx := NewEmbeddingIndex(cfg)
	idx.httpClient = &mockEmbeddingClient{}

	memories := []Memory{
		{ID: "mem1", Summary: "First memory", Content: "Content 1"},
	}

	// First indexing
	count, _ := idx.IndexMemories(context.Background(), memories)
	if count != 1 {
		t.Errorf("expected 1 new embedding, got %d", count)
	}

	// Second indexing with same memories — should be 0 (no changes)
	count, _ = idx.IndexMemories(context.Background(), memories)
	if count != 0 {
		t.Errorf("expected 0 new embeddings (unchanged), got %d", count)
	}

	// Update a memory's summary
	memories[0].Summary = "Updated memory"
	count, _ = idx.IndexMemories(context.Background(), memories)
	if count != 1 {
		t.Errorf("expected 1 new embedding (changed), got %d", count)
	}
}

func TestEmbeddingIndexSearch(t *testing.T) {
	cfg := DefaultEmbeddingConfig()
	idx := NewEmbeddingIndex(cfg)
	idx.dims = 4 // override for test with short vectors

	mock := &mockEmbeddingClient{dims: 4}
	idx.httpClient = mock

	// Manually add entries with 4-dim vectors
	idx.entries["mem1"] = &EmbeddingEntry{
		ID:          "mem1",
		SummaryHash: "abc",
		Embedding:   []float64{0.9, 0.1, 0.0, 0.0},
	}
	idx.entries["mem2"] = &EmbeddingEntry{
		ID:          "mem2",
		SummaryHash: "def",
		Embedding:   []float64{0.1, 0.9, 0.0, 0.0},
	}

	results := idx.Search(context.Background(), "identity origin", 5)
	if len(results) == 0 {
		t.Fatal("expected search results, got none")
	}
	// mem1 should rank higher for a query closer to its vector direction
	if results[0].ID != "mem1" {
		t.Errorf("expected mem1 as top result, got %s (score=%.4f)", results[0].ID, results[0].Score)
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name  string
		a, b  []float64
		want  float64
		delta float64
	}{
		{
			name:  "identical vectors",
			a:     []float64{1.0, 0.0, 0.0},
			b:     []float64{1.0, 0.0, 0.0},
			want:  1.0,
			delta: 0.001,
		},
		{
			name:  "orthogonal vectors",
			a:     []float64{1.0, 0.0},
			b:     []float64{0.0, 1.0},
			want:  0.0,
			delta: 0.001,
		},
		{
			name:  "opposite vectors",
			a:     []float64{1.0, 0.0},
			b:     []float64{-1.0, 0.0},
			want:  -1.0,
			delta: 0.001,
		},
		{
			name:  "different length",
			a:     []float64{1.0, 0.0},
			b:     []float64{1.0},
			want:  0.0,
			delta: 0.001,
		},
		{
			name:  "zero vector",
			a:     []float64{0.0, 0.0},
			b:     []float64{1.0, 0.0},
			want:  0.0,
			delta: 0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosineSimilarity(tt.a, tt.b)
			if got < tt.want-tt.delta || got > tt.want+tt.delta {
				t.Errorf("cosineSimilarity() = %f, want %f (±%f)", got, tt.want, tt.delta)
			}
		})
	}
}

func TestSummaryHash(t *testing.T) {
	tests := []struct {
		name string
		input string
		wantLen int
	}{
		{"short", "hello", 0}, // just checking it doesn't crash
		{"long", string(make([]byte, 100)), 0},
		{"empty", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summaryHash(tt.input)
			if result == "" {
				t.Error("expected non-empty hash")
			}
		})
	}
}

func TestEmbeddingSearchFallback(t *testing.T) {
	cfg := DefaultEmbeddingConfig()
	idx := NewEmbeddingIndex(cfg)
	// No entries — search should return nil
	results := idx.Search(context.Background(), "test", 5)
	if results != nil {
		t.Errorf("expected nil for empty index, got %v", results)
	}
}

func TestOllamaEmbeddingClientFormat(t *testing.T) {
	client := &OllamaEmbeddingClient{
		URL:       "http://localhost:11434",
		TimeoutMs: 300000,
	}
	if client.URL != "http://localhost:11434" {
		t.Errorf("expected URL http://localhost:11434, got %s", client.URL)
	}
	if client.TimeoutMs != 300000 {
		t.Errorf("expected timeout 300000, got %d", client.TimeoutMs)
	}
}