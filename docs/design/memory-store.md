# Prizm Memory Store — Design Plan

> Phase 1: Local Markdown Memory Layer  
> Date: 2026-08-10  
> Status: Draft — pending Ema approval

## Problem

Prizm agents currently depend on the Recall HTTP API for all memory operations. If Recall is down or unavailable, agents lose all memory capability — they can't read or write memories. Memory is essential for agent continuity across sessions.

## Goals

1. **Autonomous memory** — Prizm can read/write memories without any external service
2. **Recall as enhancement** — Recall adds vector search and cross-agent sharing, but is optional
3. **Minimal cloud tokens** — Local model handles gate + extraction; cloud LLM does the thinking
4. **Event-driven** — All memory operations emit events on the Prizm bus
5. **Shared format** — Prizm writes the same markdown format OpenClaw reads (via symlink)

## Architecture

```
┌─────────────────────────────────────────────────┐
│                  Prizm Agent                     │
│                                                  │
│  memory_search()  ──►  MemoryStore interface     │
│  memory_write()   ──►        │                   │
│                              ▼                   │
│                    ┌─────────────────┐           │
│                    │  MarkdownStore   │           │
│                    │  (local, always) │           │
│                    └────────┬────────┘           │
│                             │                    │
│                    ┌────────▼────────┐           │
│                    │  LocalGate      │           │
│                    │  (nemotron-3-nano│           │
│                    │   → qwen3.5:4b  │           │
│                    │   → session LLM)│           │
│                    └────────┬────────┘           │
│                             │                    │
│                    ┌────────▼────────┐           │
│                    │  Event Bus       │           │
│                    │  (NATS JetStream) │           │
│                    └────────┬────────┘           │
│                             │                    │
│                    ┌────────▼────────┐           │
│                    │  Recall Sync     │           │
│                    │  (if available)  │           │
│                    └─────────────────┘           │
└─────────────────────────────────────────────────┘
```

## MemoryStore Interface

```go
// internal/memory/store.go

// Memory is a single stored memory entry.
type Memory struct {
    ID          string            // ULID
    Content     string            // The memory text
    Category   string            // e.g., "decision", "preference", "fact"
    Tier       string            // "ephemeral", "active", "persist"
    Summary    string            // Short summary
    KeyTopics  []string          // Tags/topics
    Source     string            // e.g., "prizm:lumi", "recall"
    AgentID    string            // Agent that created it
    SessionID  string            // Session context
    ProjectID  string            // Project context
    Metadata   map[string]string // Extensible
    CreatedAt  time.Time
    AccessedAt time.Time
}

// MemoryStore is the abstract interface. Phase 1 = MarkdownStore.
// Phase 2 = SQLiteStore (swap-in replacement).
type MemoryStore interface {
    // Read operations
    Search(ctx context.Context, query string, limit int) ([]Memory, error)
    Get(ctx context.Context, id string) (*Memory, error)
    ListRecent(ctx context.Context, limit int) ([]Memory, error)
    
    // Write operations
    Store(ctx context.Context, mem Memory) (string, error)
    
    // Lifecycle
    Close() error
}
```

## MarkdownStore Implementation

### File Format

Memories are stored in `memory/YYYY-MM-DD.md` (same format OpenClaw uses):

```markdown
## 14:32 — Decision: Use local models for memory extraction

- **Category:** decision
- **Tier:** active
- **Source:** prizm:lumi
- **Agent:** lumi
- **Session:** sess_abc123
- **Project:** prizm
- **Key Topics:** memory, local-models, architecture

Decided to use nemotron-3-nano as the primary memory gate/extract model.
Falls back to qwen3.5:4b, then to the current session LLM. This saves
cloud tokens and keeps memory working offline.
```

### Search Strategy

MarkdownStore search uses a layered approach:

1. **Exact keyword match** — grep-style on content, key topics, summary
2. **Recency boost** — newer files score higher
3. **Category filter** — optional category parameter

No semantic search in Phase 1 (that comes with SQLite + sqlite-vec in Phase 2).

### Write Strategy

```go
func (s *MarkdownStore) Store(ctx context.Context, mem Memory) (string, error) {
    // 1. Generate ULID as ID
    // 2. Format memory as markdown
    // 3. Append to today's memory/YYYY-MM-DD.md
    // 4. Emit prizm.memory.persisted event
    // 5. (Async) Push to Recall if available
}
```

## Gate + Extract Pipeline

Memory writes go through a two-stage pipeline using the local model:

### Stage 1: Gate (Should this be remembered?)

```
Input: conversation turn (user message + agent response)
Local model prompt: "Is this worth persisting as a memory? Reply YES or NO and why."
Model: nemotron-3-nano:4b → qwen3.5:4b → session LLM (fallback chain)
Output: decision (YES/NO) + reasoning
```

### Stage 2: Extract (Structure the memory)

```
Input: the conversation turn that passed the gate
Local model prompt: "Extract key facts as structured memory. Category, tier, summary, key topics."
Model: nemotron-3-nano:4b → qwen3.5:4b → session LLM (fallback chain)
Output: Memory struct (category, tier, summary, key topics, content)
```

### Fallback Chain

```go
// internal/memory/gate.go

type GateExtractor struct {
    models    []string    // Ordered preference: ["nemotron-3-nano:4b", "qwen3.5:4b"]
    fallback  string      // Session model (set at runtime)
    ollama    *ollama.Client
}

func (g *GateExtractor) callModel(ctx context.Context, prompt string) (string, error) {
    // Try each model in order
    for _, model := range g.models {
        resp, err := g.ollama.Chat(ctx, model, prompt)
        if err == nil {
            return resp, nil
        }
        log.Printf("[MEMORY] model %s failed: %v, trying next", model, err)
    }
    // Last resort: use session model via the cloud provider
    if g.fallback != "" {
        return g.callCloudModel(ctx, prompt)
    }
    return "", ErrNoModelAvailable
}
```

## Event Schema

All memory operations emit events on the Prizm bus:

```go
// Existing events (already in internal/event/schema.go)
// prizm.memory.context_requested
// prizm.memory.context_built
// prizm.memory.context_failed

// New events for memory writes
prizm.memory.gate_passed      // Gate decided: worth remembering
prizm.memory.gate_rejected    // Gate decided: not worth remembering
prizm.memory.extracted        // Extract produced structured memory
prizm.memory.persisted        // Memory written to local store
prizm.memory.synced           // Memory pushed to Recall (if available)
prizm.memory.sync_failed      // Recall push failed (non-blocking)
```

## Config

```yaml
# prizm.yaml
memory:
  # Local store config
  store_type: "markdown"          # "markdown" (Phase 1) or "sqlite" (Phase 2)
  store_path: "memory"            # Relative to workspace root
  
  # Gate + Extract model chain
  gate_model: "nemotron-3-nano:4b"
  extract_model: "nemotron-3-nano:4b"
  model_fallback_chain:          # Tried in order
    - "nemotron-3-nano:4b"
    - "qwen3.5:4b"
  # Empty = use session cloud model as last resort
  
  # Recall integration (optional)
  recall_url: "http://localhost:18790"
  recall_enabled: true           # false = local-only mode
  recall_sync: "async"           # "async" or "sync" (sync blocks until Recall confirms)
  
  # Memory behavior
  auto_capture: true             # Automatically gate conversation turns
  min_importance: 0.5            # Gate threshold (0.0-1.0)
  max_memories_per_turn: 3       # Don't over-extract from a single turn
```

## memory_search Tool Integration

The existing `memory_search` tool (in `internal/tool/research_tools.go`) currently only calls Recall. It needs to:

1. Try Recall first (if `recall_enabled: true`)
2. Fall back to `MarkdownStore.Search()` if Recall is unavailable
3. Merge + deduplicate results if both sources return hits

```go
// Updated flow
func (t *MemorySearchTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
    var results []memory.Memory
    var recallErr error
    
    // Try Recall first
    if t.searcher != nil {
        results, recallErr = t.searcher.Search(ctx, query, limit)
    }
    
    // Fallback to local store
    if (recallErr != nil || len(results) == 0) && t.localStore != nil {
        localResults, err := t.localStore.Search(ctx, query, limit)
        if err == nil && len(localResults) > 0 {
            results = mergeAndDedup(results, localResults)
        }
    }
    
    return formatResults(results), nil
}
```

## memory_write Tool (New)

```go
// internal/tool/memory_tools.go

type MemoryWriteTool struct {
    Gate     *GateExtractor
    Store    memory.MemoryStore
    Emitter  event.Emitter
}

func (t *MemoryWriteTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
    // 1. Gate: should this be remembered?
    // 2. Extract: structure the memory
    // 3. Store: write to MarkdownStore
    // 4. Emit: prizm.memory.persisted event
    // 5. (Async) Sync to Recall
}
```

## File Structure

```
internal/memory/
├── store.go           # MemoryStore interface + Memory struct
├── markdown.go        # MarkdownStore implementation
├── markdown_test.go   # Tests for markdown read/write/search
├── gate.go            # GateExtractor (local model gate + extract)
├── gate_test.go       # Tests for gate pipeline
├── events.go          # Event definitions for memory operations
└── sync.go            # Recall sync (async push, retry logic)

cmd/prizm-cli/
└── (updated)          # Wire MemoryStore into session manager

internal/tool/
└── memory_tools.go    # memory_search (updated) + memory_write (new)
```

## Phase 1 Scope

- [ ] `MemoryStore` interface
- [ ] `MarkdownStore` with read/write/search
- [ ] `GateExtractor` with local model fallback chain
- [ ] `memory_write` tool
- [ ] `memory_search` tool update (local fallback)
- [ ] Event emission on memory operations
- [ ] Config schema in `prizm.yaml`
- [ ] Integration tests with embedded NATS
- [ ] Docs: `docs/design/memory-store.md` (this file)

## Phase 2 Preview (not now)

- `SQLiteStore` implementing `MemoryStore` interface
- sqlite-vec for semantic search
- Embedding generation via nomic-embed-text
- Bidirectional sync with Recall (pull + push)
- Markdown export from SQLite (human-readable view)

## Token Cost Analysis

| Operation | Cloud LLM | Local Model | Savings |
|-----------|-----------|-------------|---------|
| Gate (should remember?) | ~200 tokens | ~80 tokens | 60% |
| Extract (structure it) | ~400 tokens | ~150 tokens | 63% |
| **Per memory write** | **~600 tokens** | **~230 tokens** | **~62%** |

Over a 50-turn conversation with ~15 memory candidates:
- Cloud only: ~9,000 tokens on memory ops
- Local gate + extract: ~3,450 tokens on memory ops
- **Savings: ~5,550 tokens per session**

## Open Questions

1. **Markdown format versioning?** — If we change the format later, how do we migrate existing files? (Proposal: header comment with version)
2. **Concurrent writes?** — Multiple agents writing to same daily file. (Proposal: file-level mutex per daily file)
3. **MEMORY.md generation?** — Should we auto-generate MEMORY.md from the daily files, or keep it manual? (Proposal: auto-generate on `prizm memory brief`)
4. **Gate model warm-up?** — First local model call has cold-start latency (~3-5s). (Proposal: warm model on session start)