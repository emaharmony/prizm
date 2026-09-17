# V16 — Intelligence Arc

**Status:** Implemented
**Date:** 2026-05-19
**Design Doc:** [V16-INTELLIGENCE-ARC-DESIGN.md](./V16-INTELLIGENCE-ARC-DESIGN.md)

## What Changed

V16 enriches every Prizm event with intelligent metadata — how long it took, what it cost, whether it succeeded, and what should happen next. Two new CLI commands (`prizm cost` and `prizm trace`) make this data immediately useful.

### New Features

1. **Enriched EventMetadata** — `DurationMs`, `Outcome`, `TokenUsage` added to event metadata
2. **Cost Tracking** — `internal/cost/` package tracks per-provider, per-model, per-agent token usage
3. **Event Schema Validation** — `internal/event/schema.go` validates event payloads at ingestion
4. **CLI: `prizm cost`** — Aggregates and displays token usage and cost for a run
5. **CLI: `prizm trace`** — Reconstructs causal DAG from parent_id chains and prints human-readable trace

### New Event Types

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.cost.tracked` | Token usage recorded for an LLM call | `provider`, `model`, `prompt_tokens`, `completion_tokens`, `estimated_cost_usd` |
| `prizm.cost.reported` | Cost report generated for a run | `run_id`, `total_tokens`, `estimated_cost_usd` |

### New Packages

| Package | Files | Purpose |
|---------|-------|---------|
| `internal/cost/` | `tracker.go`, `pricing.go`, `tracker_test.go` | Token cost aggregation and reporting |
| `internal/event/schema.go` | `schema.go`, `schema_test.go` | Lightweight event payload validation |

### New CLI Commands

- `prizm cost <run_id>` — Show token usage and cost report
- `prizm trace <run_id>` — Show event trace (causal DAG)

### Design Decisions

- **Enrichment is additive** — All V16 fields are optional (`omitempty`). V1 consumers continue to work.
- **Outcome is explicit** — Every event can carry `success`, `failure`, `timeout`, or `skipped`.
- **TokenUsage replaces TokenCost** — `TokenCost` (int) is deprecated but not removed. Both serialize.
- **Cost aggregation is event-driven** — `CostTracker` subscribes to LLM events. No polling.
- **Trace is read-only** — `prizm trace` reads `events.jsonl`. No new storage.
- **Schema validation is opt-in** — Call `event.Validate(evt)` in development to catch bugs early.
- **No new external dependencies** — Cost estimation uses hardcoded pricing tables.

### What's NOT in V16

- **Data governance / PII scrubbing** — Owned by Remembrance
- **Security audit adapter** — V8 policy events already provide this
- **Agent inbox (subscription filtering)** — V9 adapters already subscribe to specific subjects
- **Resource governor (rate limiting)** — Deferred to a future version
- **Event replay** — Partially covered by projections