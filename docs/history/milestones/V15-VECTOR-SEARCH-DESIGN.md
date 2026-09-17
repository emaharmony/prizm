# V15 — Vector Search (sqlite-vss)

## Mission

Add vector similarity search to Prizm's existing SQLite event store. This
enables semantic queries like "find runs similar to this task" or "what events
are semantically related to X?" — all within the single-binary deployment story.

sqlite-vss keeps the deployment story clean: no external vector database
required. The vector index lives in a SQLite extension alongside the event
store. For V17+, if scale demands it, we can add a pgvector adapter.

## Architecture

### Core Interface: VectorStore

```go
type VectorStore interface {
    // Upsert adds or updates a vector embedding.
    Upsert(ctx context.Context, entry VectorEntry) error

    // Search finds the top-K entries most similar to the query vector.
    Search(ctx context.Context, query []float64, opts SearchOptions) ([]SearchResult, error)

    // Delete removes vectors matching the filter.
    Delete(ctx context.Context, filter VectorFilter) error

    // Close releases resources.
    Close() error
}
```

### VectorEntry

```go
type VectorEntry struct {
    ID       string    // Unique ID (ULID)
    Content  string    // Original text content
    Vector   []float64 // Embedding vector (dimension must be consistent)
    Metadata map[string]any // Arbitrary key-value metadata
    Source   string    // Source type: "event", "run_summary", "artifact"
    SourceID string    // ID of the source object
    CreatedAt time.Time
}
```

### SearchResult

```go
type SearchResult struct {
    Entry  VectorEntry
    Score  float64 // Cosine similarity [0, 1]
}
```

### SearchOptions

```go
type SearchOptions struct {
    TopK         int               // Number of results (default 10)
    MinScore     float64           // Minimum similarity threshold (default 0.5)
    SourceFilter string            // Filter by source type
    Metadata     map[string]any    // Filter by metadata
}
```

## Implementation: sqlite-vss

The SQLite VSS extension provides virtual table-based vector search.
We'll use the `sqlite-vss` Go bindings.

### Table Schema

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS vss_entries USING vss0(
    vss_vector(384)  -- 384-dim vectors (all-MiniLM-L6-v2 default)
);

CREATE TABLE IF NOT EXISTS vector_entries (
    id TEXT PRIMARY KEY,
    content TEXT NOT NULL,
    source TEXT NOT NULL,
    source_id TEXT NOT NULL,
    metadata JSON,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    rowid INTEGER  -- Matches vss_entries rowid
);
```

### Embedding Provider

Vector embeddings come from a pluggable EmbeddingProvider:

```go
type EmbeddingProvider interface {
    Embed(ctx context.Context, text string) ([]float64, error)
    EmbedBatch(ctx context.Context, texts []string) ([][]float64, error)
    Dimension() int
    Name() string
}
```

Built-in providers:
1. **MockProvider** — deterministic test embeddings (no external dependency)
2. **OpenAIProvider** — text-embedding-3-small (1536-dim default, supports 384/512/1024/1536)
3. **OllamaProvider** — local embeddings via /api/embeddings endpoint

### Integration Points

The vector store integrates with existing Prizm systems:

1. **Event Enrichment** (V16a precursor): When an event is stored in the
   EventStore, an async hook can generate an embedding and upsert it into
   the VectorStore.

2. **Run Summary Indexing**: After a run completes, the task description
   and summary get embedded for future similarity search.

3. **CLI Command**: `prizm search --query "find deployment failures" --top-k 5`

## New Files

- `internal/vector/store.go` — VectorStore interface + VectorEntry, SearchResult, SearchOptions
- `internal/vector/sqlite.go` — SQLite VSS implementation
- `internal/vector/embedding.go` — EmbeddingProvider interface + MockProvider
- `internal/vector/embedding_openai.go` — OpenAI embedding provider
- `internal/vector/embedding_ollama.go` — Ollama embedding provider
- `internal/vector/store_test.go` — VectorStore interface tests
- `internal/vector/sqlite_test.go` — SQLite implementation tests
- `internal/vector/embedding_test.go` — Embedding provider tests
- `cmd/prizm-cli/cmd_search.go` — `prizm search` CLI command

## Scorecard Impact

- **Scalability**: 6.5 → 8.5+ (vector search enables semantic queries at scale)
- **Usefulness**: 8.0 → 8.5+ (semantic search is a killer feature)
- **Reliability**: 8.5 → 8.5 (maintained, sqlite-vss is battle-tested)

## Dependencies

- `github.com/ngrok/sqlite-vss-go` — SQLite VSS extension for Go

## Risk Assessment

- **sqlite-vss CGO dependency**: Requires CGO and SQLite compilation.
  Prizm's core SQLite stores use a pure-Go driver, so sqlite-vss would need
  explicit CGO build support if this design is implemented.
- **Embedding dimension consistency**: Must enforce consistent dimensions.
  VectorStore.ValidateDimension() handles this.
- **Performance**: Vector search on <1M entries is sub-millisecond. 
  No optimization needed yet.

## Version Bump

prizm v0.15.0
