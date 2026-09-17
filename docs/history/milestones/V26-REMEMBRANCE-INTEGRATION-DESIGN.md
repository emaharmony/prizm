# V26 — Remembrance Integration Design

**Date:** 2026-05-26
**Author:** Lumi + Mango
**Status:** ✅ Complete (merged to main, PR #35)
**Branch:** `v26-remembrance-integration`

---

## Thesis

Remembrance is Prizm's memory layer. It captures what agents say, extracts entities and relationships, builds context for future conversations, and maintains itself through dream cycles. This version wires Remembrance into Prizm's event pipeline so memory flows automatically.

**Core principle:** Remembrance is a separate Python process. Prizm talks to it via HTTP (synchronous reads) and NATS (async writes). No embedding, no subprocess management.

---

## Architecture: Hybrid A+C

```
┌─────────────┐                  ┌──────────────────┐
│    Prizm     │                  │   Remembrance    │
│   (Go)      │                  │   (Python)       │
│             │                  │                  │
│  Pipeline   │──HTTP GET───────>│  BuildContext()  │
│  (sync)     │  /context/build  │  Search()        │
│             │                  │  EntityGet()      │
│  Pipeline   │──HTTP POST──────>│  Capture()       │
│  (sync)     │  /capture        │                  │
│             │                  │                  │
│  EventBus   │──NATS───────────>│  NATS Subscriber │
│  (async)    │  *.agent.output  │  (auto-capture)  │
│             │                  │                  │
└─────────────┘                  │  Dream Cycle     │
                                 │  (cron + event)  │
                                 └──────────────────┘
```

**Two-path context flow:**
1. **Async capture (fire-and-forget):** Agent output → NATS event `*.agent.output` → Remembrance auto-captures
2. **Sync enrichment (on-demand):** Before LLM call, RemembranceStage calls `client.BuildContext()` → injects memories into prompt

**Why both?** NATS capture ensures no data loss even if Prizm restarts. The synchronous stage ensures context is available for the *current* turn. Idempotency keys prevent duplicates.

---

## Components

### 1. NATS Subscriber (Python — `nats_sub.py`)

New file in `remembrance_mcp/`. Subscribes to `*.agent.output` on NATS and calls `pipeline.capture()` for each event.

Key design decisions:
- **Idempotency:** Uses `(agent + session_id + turn)` as dedup key, bounded list of 1000
- **Graceful degradation:** If `nats-py` is not installed, falls back to HTTP-only mode
- **Daemon thread:** Runs in background, doesn't block the REST API
- **Source tagging:** Captures are tagged `nats:<agent>` for traceability

### 2. Serve Command (Python — `serve.py`)

New CLI entry point. Starts both REST API and NATS subscriber together.

```bash
python -m remembrance_mcp.serve --port 8788 --nats nats://localhost:4222
python -m remembrance_mcp.serve --no-nats  # REST only
```

### 3. RemembranceStage (Go — `remembrance.go`)

Enhanced from V20 stub. Now performs TWO operations:

- **Capture:** Calls `client.Capture(agent_output, source, category, "")` after LLM response
- **Context:** Calls `client.BuildContext(task, project, agent, 10)` before LLM call

Configuration:
- `MemoryEnabled` — whether to attempt memory operations
- `RequireMemory` — whether to fail if Remembrance is unavailable
- `MemoryURL` — Remembrance service URL (default: `http://localhost:8788`)
- `Capture` — enable/disable capture (default: true)
- `Context` — enable/disable context building (default: true)

### 4. Remembrance Adapter (Go — unchanged)

The existing `internal/adapter/builtin/remembrance/` adapter remains for action-based triggers. The stage handles pipeline integration; the adapter handles action-based triggers.

---

## Event Flow

```
1. Agent produces output
2. EventBus publishes: lumi.agent.output { content, session_id, turn, project }
3. NATS subscriber receives → pipeline.capture(content, source="nats:lumi")
4. RemembranceStage also calls: client.Capture(content, source="prizm:lumi")
5. Both use same idempotency key → dedup in pipeline
6. Next turn: RemembranceStage calls client.BuildContext(task, project, agent)
7. LLM receives enriched prompt with relevant memories
```

Steps 3 and 4 are belt-and-suspenders. If NATS is reliable, we can remove step 4 later.

---

## Dream Cycle Triggers

1. **Cron (primary):** Run at 3AM via `POST /dream`
2. **Event trigger (secondary):** After N=10 PERSIST memories, trigger a partial dream cycle
3. **Manual:** `POST /dream` anytime for debugging

The event trigger is a NATS subscription on `remembrance.dream.triggered`. Prizm can publish this event after counting PERSIST captures.

---

## New Files

| File | Language | Purpose |
|------|----------|---------|
| `remembrance_mcp/nats_sub.py` | Python | NATS subscriber for auto-capture |
| `remembrance_mcp/serve.py` | Python | CLI entry point (REST + NATS) |
| `internal/stage/remembrance.go` | Go | Enhanced RemembranceStage (capture + context) |
| `internal/stage/remembrance_test.go` | Go | 9 unit tests |

## Modified Files

| File | Change |
|------|--------|
| `pyproject.toml` | Added `[project.optional-dependencies] nats = ["nats-py>=2.0.0"]` |
| `internal/stage/stage_integration_test.go` | Removed old RemembranceStage tests (moved to dedicated file) |
| `docs/TASKS.md` | Updated Phase 2 status, added Phase 7 |

---

## Risks & Mitigations

| Risk | Mitigation |
|------|-----------|
| Double-capture (NATS + stage) | Idempotency keys (agent + session + turn) |
| BuildContext latency on every turn | Future: 60s TTL cache per session |
| Remembrance unavailable | `client.IsAvailable()` check, graceful degradation |
| NATS connection failure | Fall back to HTTP-only mode, log warning |
| Dream cycle resource usage | Start with manual trigger, add cron later |

---

## What's Next (After V26)

- **E2E integration test** with mock Remembrance server
- **Dream cycle triggers** (cron + event-based)
- **Context caching** (60s TTL per session)
- **Streaming responses** (token-by-token to Discord)
- **Auth middleware** for all API endpoints