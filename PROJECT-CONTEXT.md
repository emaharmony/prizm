# PROJECT-CONTEXT.md — Prizm
> Universal project context file. Auto-loaded by Lumi for any work on this project.
> Format: structured, scannable, no fluff. Update when milestones complete or architecture changes.

## Identity
- **Name:** Prizm
- **Type:** Event-Native AI Agent Platform
- **Tagline:** One beam of light. One event. A spectrum of reactions.
- **Born:** 2026-05-07
- **Status:** Pre-M1 (core components prototyped, not yet integrated end-to-end)
- **Repo:** `/Users/ema/projects/repos/prizm/`
- **Branch:** `main` (only branch)
- **Design Doc:** `docs/DESIGN.md`

## Architecture
- **Core runtime:** Go (goroutines = natural actor model)
- **Agent SDK:** Python (LLM ecosystem: LangGraph, AutoGen, CrewAI)
- **Event backbone:** NATS JetStream (embedded, persistence + replay + wildcards)
- **Memory:** Remembrance (LanceDB + SQLite + Ollama nomic-embed-text) — Python FastAPI
- **State:** SQLite (local) / PostgreSQL (server) — event sourcing + projection tables
- **License:** Source-available, all-rights-reserved, permission-required (see LICENSE)

## Implemented Components

### ✅ prizm-bus (Go binary)
- Embedded NATS JetStream server
- Canonical Event schema (id, type, source, timestamp, correlation_id, parent_id, payload, metadata)
- PRIZM stream: subjects `prizm.>`, 1M msgs max, 1GB, 7-day retention, file storage
- Built-in durable consumers: logger, decision-handler, memory-store
- Test events: publishes 3 demo events on startup (channel.received → agent.decision → memory.stored)
- **File:** `cmd/prizm-bus/main.go`
- **Binary:** `prizm-bus` (22.8MB, in repo root)

### ✅ prizm-agent (Go binary)
- Connects to NATS bus, subscribes to configured subjects
- Configurable: `-name`, `-subs`, `-model`, `-bus` flags
- Default subscription: `prizm.>` (all events)
- Durable consumers per subject, heartbeat every 30s
- Emits `prizm.agent.started` and `prizm.agent.stopped` lifecycle events
- **File:** `cmd/prizm-agent/main.go`
- **Binary:** `prizm-agent` (9.0MB, in repo root)

### ✅ Python SDK (`sdk/prizm/`)
- `PrizmClient` — async NATS connection, emit/subscribe
- Event model with Pydantic validation
- Tool registry: `register_tool()` + `call_tool()` with request/response over events
- Decorators: `@prizm.on()`, `@prizm.emit()`, `@prizm.tool()`, `@prizm.agent()`, `@prizm.run()`
- **Files:** `sdk/prizm/client.py`, `event.py`, `_global.py`, `agents/`, `channels/`, `tools/`

### ✅ Channel Adapters
- **Discord** (`sdk/prizm/channels/discord.py`) — receives messages, emits `prizm.channel.received`
- **Telegram** (`sdk/prizm/channels/telegram.py`) — receives messages, emits `prizm.channel.received`
- Both translate channel formats → canonical Prizm Event schema

### ✅ Remembrance (Memory Layer)
- FastAPI server on configurable port
- LanceDB for vector storage (semantic search)
- SQLite for metadata (source, category, tier, timestamps)
- Ollama provider for embeddings (nomic-embed-text)
- Ingest, search, context-build endpoints
- Hybrid ranking (semantic + recency + tier)
- **Files:** `remembrance/src/remembrance/` (app, api, memory, stores, embeddings, models)
- **Config:** `remembrance/configs/remembrance.local.yaml`

### ✅ Design Document
- **File:** `docs/DESIGN.md` (v0.1, 11.8KB)
- Covers: vision, architecture, event schema, subject tree, all 7 components, SDK, migration, monetization

## NOT Yet Implemented
- [ ] Agent runtime with actual LLM calls (Go orchestrating Python agents)
- [ ] gRPC bridge between Go runtime and Python SDK
- [ ] Migration tool (`prizm migrate --from-openclaw`)
- [ ] Web UI (real-time event stream viewer)
- [ ] Cron scheduler (time-based event producers)
- [ ] State manager (CQRS projections)
- [ ] ACP/A2A gateway
- [ ] End-to-end integration test (Discord message → bus → agent → memory → response)

## Event Schema (Canonical)
```json
{
  "id": "evt_<timestamp>_<counter>",
  "type": "prizm.<domain>.<action>",
  "source": "agent or component name",
  "timestamp": "ISO 8601 UTC",
  "correlation_id": "links events in same workflow",
  "parent_id": "direct causal parent",
  "payload": {},
  "metadata": { "model": "", "prompt_hash": "", "token_cost": 0, "session_id": "", "latency_ms": 0 }
}
```

## Subject Tree
- `prizm.agent.*` — agent lifecycle (spawned, decision, output, error, completed, started, stopped, heartbeat)
- `prizm.tool.*` — tool invocations (called, result, error, registered)
- `prizm.memory.*` — memory operations (stored, retrieved, consolidated)
- `prizm.channel.*` — channel messages (received, sent)
- `prizm.cron.*` — scheduled triggers (triggered, completed)
- `prizm.session.*` — session lifecycle (created, ended)
- `prizm.state.*` — state mutations (changed)

## Key Decisions
| Decision | Choice | Rationale |
|----------|--------|-----------|
| Scope | Full platform, event-native, source-available | Ema controls the whole stack |
| Runtime | Go core + Python SDK | Go for event backbone; Python for agent ergonomics |
| Event bus | NATS JetStream | Persistence, replay, consumer groups, wildcards, Go-native |
| Migration | OpenClaw config import | Read openclaw.json → emit Prizm config |
| Memory | Event-sourced + projections + vector search | Immutable audit trail, time-travel debugging |

## Dependencies
- Go: `github.com/nats-io/nats-server/v2`, `github.com/nats-io/nats.go`
- Python: `nats-py`, `pydantic`, `fastapi`, `uvicorn`, `lancedb`, `ollama`
- External: NATS server (embedded), Ollama (for embeddings)

## Commands
```bash
# Start the bus
cd /Users/ema/projects/repos/prizm && ./prizm-bus

# Start an agent
./prizm-agent -name lumi -subs "prizm.agent.*,prizm.channel.received" -model glm-5.3-flash:cloud

# Start Remembrance
cd remembrance && uvicorn remembrance.app:app --reload --port 8900

# Python SDK example
python sdk/examples/minimal.py
python sdk/examples/full_app.py
```

## Migration Path (OpenClaw → Prizm)
| OpenClaw | Prizm |
|----------|-------|
| `channels.discord` | Channel adapter: `prizm.channel.received.discord` |
| `session.dmScope` | Session config: `prizm.session.*` |
| `hooks.internal` | Event subscriptions: `prizm.agent.*` → handler |
| `plugins.entries` | Tool registrations: `prizm.tool.registered.*` |
| `gateway.bind` | Runtime config: host/port |
| `models.*` | LLM provider config in agent definitions |
| Memory DB (`MEMORY.md`) | Import as `prizm.memory.stored` events |
| Cron jobs | `prizm.cron.triggered.*` with schedule config |
| Sub-agent spawns | `prizm.agent.spawned` with parent correlation |