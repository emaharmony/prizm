# V19 — Smart Context Injection

**Status:** Implemented
**Date:** 2026-05-20

## Mission

Prizm can now inject OpenClaw workspace context into LLM prompts — personality, rules, project docs — without manual file duplication. Smart selection, token budgeting, and observability built in.

## What Changed

### 1. Context Builder (`internal/context/`)

New package that reads workspace files, applies selection rules, respects token budgets, and produces formatted context strings:

```go
builder := context.NewBuilder(workspaceRoot).
    WithNamedContexts([]string{"soul", "agents"}).
    WithAutoFiles(autoFiles).
    WithTokenBudget(4000)
result, err := builder.Build()
```

### 2. Named Context Sources

| Flag | File | ~Tokens | Priority |
|------|------|---------|----------|
| `soul` | SOUL.md | ~1,500 | 100 (never truncate) |
| `agents` | AGENTS.md | ~1,600 | 80 |
| `user` | USER.md | ~1,500 | 80 |
| `heartbeat` | HEARTBEAT.md | ~900 | 50 |
| `memory` | MEMORY.md | ~1,300 | 50 |
| `identity` | IDENTITY.md | ~800 | 50 |

### 3. Truncation Priority

When context exceeds the token budget (40% of model's context window from V18 `ModelInfo`):

1. **`--context-file` extras** truncated first (priority 60)
2. **`--context auto` matches** next (priority 30)
3. **`heartbeat`, `memory`** next (priority 50)
4. **`agents`, `user`** next (priority 80)
5. **`soul` never truncated** (priority 100)

Truncated sections get a header: `[4,200 tokens from SOUL.md — 800 tokens truncated]`

### 4. Auto-Discovery (`--context auto`)

Keyword matching against `docs/*.md`:
- Tokenizes task description, strips stopwords
- Counts keyword hits in each doc file
- Ranks by hit count, injects until budget exhausted
- Deterministic, testable, explainable

### 5. Pipeline Stage (`internal/stage/context.go`)

Context injection runs as a pipeline stage before Remembrance:

```
Connection → Context → Remembrance → LLM → Tool → Approval → Validation → Review
```

Emits two events per run:
- `prizm.context.file_read` — per file (file, size, tokens)
- `prizm.context.injected` — aggregate (files, total tokens, truncation, hash)

### 6. CLI Commands

```bash
# Preview what would be injected
prizm context show --context soul,agents --workspace-root ~/.openclaw/workspace

# With auto-discovery
prizm context show --context soul,agents --auto --task "fix the approval engine"

# With token budget
prizm context show --context soul,agents --budget 4000
```

### 7. V19 Event Types

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.context.file_read` | Workspace file read for context | `file`, `source`, `size_bytes`, `estimated_tokens` |
| `prizm.context.injected` | Context injection complete | `run_id`, `files`, `total_tokens`, `truncated`, `truncation_applied` |

### 8. Content Hash (Safety Net)

Every context injection computes a SHA-256 hash of the raw concatenated files. Stored in `prizm.context.injected` event for traceability. If the hash differs from a cached version, it's flagged in the trace — observability without blocking.

## Design Decisions

1. **Raw caching, no compilation** — Mango's safety concern: compilation risks silent semantic loss. Raw files are lossless. Token savings come from selectivity (~45-55%), not compression.
2. **Pipeline stage, not CLI flag bolt-on** — Context injection runs as a proper stage with events, budgeting, and observability.
3. **Soul never truncated** — Personality is the last thing to cut. Period.
4. **Selectivity is the biggest win** — `--context soul,agents` cuts tokens ~45% by not reading files you don't need.
5. **Auto-discovery is deterministic** — keyword matching, not embeddings. No LLM calls needed.
6. **Content hash for observability** — SHA-256 of raw content in events, not a compilation diff gate.
7. **Read-only** — Prizm never writes to the workspace. Zero trust boundary violation.

## Packages

| Package | Purpose | Files |
|---------|---------|-------|
| `internal/context/` | Context builder, selection, budgeting, formatting | `builder.go`, `builder_test.go` |
| `internal/stage/context.go` | Pipeline stage: read → budget → inject → emit events | New |
| `cmd/prizm-cli/cmd_context.go` | `prizm context show` CLI command | New |
| `cmd/prizm-cli/main.go` | `--context`, `--context-auto`, `--context-file`, `--workspace-root` flags | Modified |
| `internal/event/event.go` | V19 event types | Modified |
| `internal/event/schema.go` | V19 schema validation | Modified |

## Test Coverage

- Named context reading and selection
- Missing file graceful skipping
- Token budget enforcement
- Soul never truncated (even at tiny budget)
- Auto-discovery keyword matching and ranking
- Explicit file injection
- Content hash computation
- Truncation message formatting
- Extract keywords stopword filtering
- Concurrent access (via builder)

## What's NOT in V19

- **Compilation/compression** — Deferred to V20. Raw caching + selectivity gives ~45% token savings with zero risk.
- **RAG retrieval** — Deferred to V20. Uses V17 HNSW infrastructure.
- **Frontmatter parsing** — Raw markdown injection is safe and lossless.
- **Write-back to workspace** — Out of scope. Prizm reads, never writes.
- **Remembrance composition** — Workspace context and memory context are separate stages; they compose naturally.