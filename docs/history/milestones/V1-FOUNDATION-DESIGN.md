# V1 — Foundation

## Mission

Prizm V1 establishes the foundational architecture: an event-driven agent runtime
with a shared event schema, NATS-powered bus, deterministic placeholder agent,
and a semantic memory system (Remembrance) for context injection.

Every action in Prizm is an event. The event schema is canonical — all components
share a single definition. The bus routes events; agents react.

## What Changed

### Prizm CLI (`cmd/prizm-cli`)
- `prizm run` command orchestrates full agent lifecycle
- `prizm health` for system health checks
- CLI flags: `--task`, `--project`, `--agent`, `--bus-url`, `--memory-enabled`,
  `--require-memory`, `--memory-url`, `--run-dir`
- Event persistence: `runs/<run_id>/events.jsonl` + `summary.json`

### Shared Event Package (`internal/event`)
- Canonical `Event` struct with ULID IDs, correlation/parent ID propagation
- 15 V1 event types covering the full lifecycle:
  - Task: `task.created`, `task.started`, `task.completed`, `task.failed`
  - Memory: `memory.context_requested`, `memory.context_built`, `memory.context_failed`
  - Agent: `agent.started`, `agent.output`, `agent.completed`, `agent.failed`
  - Tool: `tool.called`, `tool.result`, `tool.failed`
  - System: `system.health`

### Runner (`internal/run`)
- `Runner` orchestrator manages the full V1 lifecycle
- Lifecycle flow: `task.created → task.started → [memory hooks] → agent.started → agent.output → agent.completed → task.completed`
- Memory context injection via `RemembranceClient` with graceful fallback
- `--require-memory` flag to fail if memory is unavailable

### Placeholder Agent (`internal/agent`)
- Deterministic, always-succeeds agent for testing
- Stub for real LLM providers planned in V2

### Agent Runtime (`cmd/prizm-agent`)
- Separate binary that subscribes to events via NATS
- Decoupled from the CLI — agents react to events independently

### Event Bus (`cmd/prizm-bus`)
- Embedded NATS JetStream for event routing
- All components share the same event schema

### Remembrance V1 — Semantic Memory (`remembrance/`)
- Complete V1 implementation of the framework-native memory system
- SQLite metadata store (projects, users, memories, events, audit)
- LanceDB vector store (768-dim `nomic-embed-text` embeddings)
- Ollama embedding provider (nomic-embed-text, pluggable architecture)
- Hybrid ranker: vector 0.65 + keyword 0.15 + importance 0.10 + project_match 0.05 + recency 0.05
- Context builder: search → rank → filter → format
- Context formatter: markdown + JSON output, token budget
- FastAPI REST endpoints: `/v1/memory/ingest`, `search`, `context/build`
- CLI: `init`, `ingest`, `search`, `build-context`, `list`
- Go client interface (`RemembranceClient`) for Prizm integration
- 15 seed memories covering architecture decisions
- Full audit logging on all mutations
- Stack: Python 3.11+, Pydantic, FastAPI, LanceDB, SQLite, Ollama

## Key Packages/Files

| Package / File | Purpose |
|---|---|
| `cmd/prizm-cli/main.go` | CLI entry point, `prizm run` command |
| `cmd/prizm-bus/main.go` | NATS JetStream event bus |
| `cmd/prizm-agent/main.go` | Agent subscriber binary |
| `internal/event/event.go` | Canonical event schema + 15 V1 types |
| `internal/run/runner.go` | Full lifecycle orchestrator |
| `internal/agent/placeholder.go` | Deterministic test agent |
| `internal/remembrance/client.go` | Go HTTP client for Remembrance API |
| `remembrance/src/` | Python memory system (20+ files) |
| `remembrance/go/` | Go client interface |

## Design Decisions

1. **All events, all the time** — No implicit state changes. Every action is an event.
   Events are the source of truth; artifacts are derived from them.

2. **ULID IDs** — Sorted, URL-safe, timestamp-embeddable IDs for deterministic ordering.
   No UUIDs.

3. **Parent/propagation** — Every event has a parent ID and correlation ID. The causal
   chain is reconstructible from the event log alone.

4. **Memory is optional but integrated** — Remembrance is a separate service that Prizm
   calls over HTTP. If unavailable, Prizm degrades gracefully (unless `--require-memory`
   is set). The memory system is framework-agnostic with pluggable embedding providers.

5. **Python for memory, Go for runtime** — Remembrance is Python for the rich ML/AI
   ecosystem (LanceDB, Ollama embeddings). Prizm runtime is Go for performance,
   concurrency, and single-binary deployment.

6. **Hybrid ranking over pure vector** — Pure vector search misses keyword matches
   and recency signals. The weighted ranker (65% vector, 35% structured signals)
   produces more relevant results for agent context.

7. **Events.jsonl + summary.json** — Every run produces a line-delimited event log
   and a human-readable summary. The event log is the audit trail.

8. **NATS JetStream** — Persistent, at-least-once delivery for events. Embedded in
   the bus binary (no external server required).

## Test Coverage

- **22 integration tests** (event schema, placeholder agent, full lifecycle,
  memory failure graceful handling, require-memory failure, correlation ID
  propagation, event type ordering, NATS pub/sub)
- **Remembrance**: unit tests across API, memory, embeddings, stores
- All V1 tests passing

## Event Lifecycle (Happy Path)

```
task.created → task.started → agent.started → agent.output → agent.completed → task.completed
                                    ↑
                         memory.context_requested (optional)
                              → memory.context_built
```

## Artifacts per Run

```
runs/<run_id>/
├── events.jsonl          # Line-delimited event log
├── summary.json          # Human-readable run summary
└── (memory context injected into prompt)
```

## Remembrance Architecture

```
Prizm Runner
  → RemembranceClient.GetContext(task, project, agent)
    → HTTP POST /v1/context/build
      → MemorySearcher (vector search in LanceDB)
      → HybridRanker (weighted fusion)
      → ContextBuilder (filter + format)
      → ContextPack (markdown or JSON)
  → injected into agent prompt
```

## V2 Roadmap (as shipped)

V1 was built with these V2 targets:
- Replace placeholder agent with real LLM providers
- Add `provider.Provider` interface
- Add `prompt.Builder` for prompt assembly
- Add CLI flags for model selection, temperature, timeout
