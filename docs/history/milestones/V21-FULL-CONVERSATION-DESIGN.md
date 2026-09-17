# V21 — Full Conversation Pipeline Design

**Date:** 2026-05-21
**Status:** Complete (merged to main)
**PR:** #32

---

## Thesis

Prizm gains a full conversation pipeline that processes Discord messages through debounce → session → routing → LLM → response → Discord delivery. The pipeline is hybrid: the handler is the adapter (Discord-specific concerns), and the pipeline stages handle the domain-agnostic core.

---

## What Changed

### V21-1: Conversation Context
- `conversationContext` struct holds all state for the Discord message handler
- Debounce tracker, session manager, agent router, bot, event log
- Context builder for workspace injection (SOUL.md, AGENTS.md, USER.md)

### V21-2: Debounce Integration
- Per-user 3-second debounce window
- Prevents duplicate processing from Discord message events
- Uses `internal/debounce/` package

### V21-3: Session Integration
- Messages routed through session manager
- Session maintains conversation history
- Compaction when context exceeds budget (truncate strategy)

### V21-4: Agent Router Integration
- Route to the primary agent (or configured agent)
- `router.Route()` determines which agent handles the message

### V21-5: Stage Pipeline
- Hybrid approach: Handler as adapter, Pipeline for core
- `StreamCallbackFunc` pattern for token-by-token delivery
- Pipeline: LLMStage → DelegationStage → PersistenceStage → EventPublishStage
- RemembranceStage exists but only emits events (enhanced in V26)

### V21-6: End-to-End Test
- 5 E2E tests covering the full conversation pipeline
- Session creation, message routing, LLM calls, Discord delivery

---

## Key Decisions

- **Handler as adapter**: Discord-specific concerns (debounce, typing, message delivery) stay in the handler. Pipeline stages are domain-agnostic.
- **StreamCallback pattern**: `type StreamCallbackFunc func(token string, index int, finished bool) error` — handler creates a closure that delivers tokens to Discord via placeholder message edits.
- **PersistenceStage skips gracefully**: Empty RunDir = conversation mode (no file artifacts).
- **Remembrance goroutine limit**: Semaphore with 4 slots to prevent capture from overwhelming the system.
- **publishEvent copies payload**: No mutation of caller's map.

---

## Conversation Flow

```
Discord message arrives
    │
    ▼
Debounce (3s per user)
    │
    ▼
Session lookup/create
    │
    ▼
Agent Router → find agent
    │
    ▼
Build prompt (session history + workspace context)
    │
    ▼
LLM call with StreamCallback
    │
    ▼
Edit placeholder message with tokens
    │
    ▼
Final message delivery
    │
    ▼
Save to session + event log
    │
    ▼
Remembrance capture (async, fire-and-forget)
```

---

## StreamCallback Pattern

The handler creates a closure that:
1. Accumulates tokens into `accumulatedText`
2. Batches edits every 900ms (Discord rate limit: 5 edits/5s)
3. On `finished=true`, does a final edit with the complete response
4. Falls back to direct send if placeholder creation fails