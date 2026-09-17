# V16 — Intelligence Arc: Event Enrichment, Cost Tracking, and Trace Visualization

**Status:** In Progress
**Date:** 2026-05-19

## Mission

V16 makes Prizm events *intelligent*. Every event now carries enriched metadata — how long it took, what it cost, whether it succeeded, and what should happen next. This transforms Prizm from a passive event logger into an active observability system that can answer: "What happened? How long did it take? What did it cost? What went wrong? What should we try next?"

## What Changed

### 1. Enriched Event Metadata

The `EventMetadata` struct gains three new fields:

```go
type EventMetadata struct {
    RunID       string `json:"run_id,omitempty"`
    SessionID   string `json:"session_id,omitempty"`
    Project     string `json:"project,omitempty"`
    Agent       string `json:"agent,omitempty"`
    Model       string `json:"model,omitempty"`
    TokenCost   int    `json:"token_cost,omitempty"`     // V1: total tokens
    LatencyMs   int    `json:"latency_ms,omitempty"`    // V1: LLM latency

    // V16: Enriched metadata
    DurationMs  int64  `json:"duration_ms,omitempty"`   // Wall-clock duration
    Outcome     string `json:"outcome,omitempty"`        // "success" | "failure" | "timeout" | "skipped"
    TokenUsage  TokenUsage `json:"token_usage,omitempty"` // Detailed token breakdown
}
```

**DurationMs** — Wall-clock time for this event's action, in milliseconds. Already scattered in payloads as `duration_ms`; now promoted to first-class metadata.

**Outcome** — Categorizes the result: `success`, `failure`, `timeout`, `skipped`. Previously you had to infer outcome from the event type (e.g., `prizm.task.failed` = failure). Now every completion event carries an explicit outcome.

**TokenUsage** — Detailed token breakdown replacing the flat `TokenCost`:

```go
type TokenUsage struct {
    PromptTokens     int `json:"prompt_tokens,omitempty"`
    CompletionTokens int `json:"completion_tokens,omitempty"`
    TotalTokens      int `json:"total_tokens,omitempty"`
    EstimatedCostUsd float64 `json:"estimated_cost_usd,omitempty"` // microdollars
}
```

### 2. Cost Aggregation

New package `internal/cost/` aggregates token usage across a run:

```go
type CostReport struct {
    RunID           string            `json:"run_id"`
    TotalTokens     int               `json:"total_tokens"`
    PromptTokens    int               `json:"prompt_tokens"`
    CompletionTokens int              `json:"completion_tokens"`
    EstimatedCostUsd float64           `json:"estimated_cost_usd"`
    ByProvider      map[string]float64 `json:"by_provider"` // cost per provider
    ByModel         map[string]float64 `json:"by_model"`    // cost per model
    ByAgent         map[string]int     `json:"by_agent"`     // tokens per agent
    EventCount      int               `json:"event_count"`
    DurationMs      int64             `json:"duration_ms"`
}
```

- `CostTracker` listens to `prizm.llm.completed` and `prizm.llm.failed` events
- Accumulates token counts and estimated costs
- Writes `cost_report.json` to the run directory
- Exposes via CLI: `prizm cost <run_id>`

### 3. Event Trace Visualization

New CLI command `prizm trace <run_id>` reconstructs the causal DAG from parent_id chains and prints a human-readable trace:

```
task.created (evt_01JXA) run_abc
  └─ task.started (evt_01JXB) [12ms]
       └─ agent.started (evt_01JXC) analyst [5ms]
            └─ llm.requested (evt_01JXD) ollama/llama3 [2ms]
            └─ llm.completed (evt_01JXE) 423 tokens, 1847ms [1.847s]
                 └─ tool.requested (evt_01JXF) read_file [1ms]
                 └─ tool.approved (evt_01JXG) [1ms]
                 └─ tool.completed (evt_01JXH) [45ms]
            └─ agent.completed (evt_01JXI) [2.3s]
  └─ task.completed (evt_01JXJ) [2.5s]

Total: 2.5s | 423 tokens | $0.0000
```

This is built from the existing `ParentID` field — no new event types needed.

### 4. V16 Event Types

New event types for cost tracking:

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.cost.tracked` | Token usage recorded for an LLM call | `provider`, `model`, `prompt_tokens`, `completion_tokens`, `estimated_cost_usd` |
| `prizm.cost.reported` | Cost report generated for a run | `run_id`, `total_tokens`, `estimated_cost_usd` |

### 5. Event Schema Validation (Lightweight)

New package `internal/event/schema.go` validates event payloads against expected schemas at ingestion time:

```go
var Schemas = map[string]Schema{
    "prizm.task.started": {
        Required: []string{"task", "provider"},
        Optional: []string{"model", "agent"},
    },
    "prizm.llm.completed": {
        Required: []string{"provider", "model"},
        Optional: []string{"prompt_tokens", "completion_tokens", "latency_ms"},
    },
    // ... all event types
}
```

This is lightweight — just required/optional field checks, not JSON Schema validation. Catches typos and missing fields early without runtime overhead.

## Design Decisions

1. **Enrichment is additive** — All V16 fields are optional (`omitempty`). V1 consumers continue to work unchanged. No breaking changes.

2. **Outcome is explicit** — Instead of inferring success/failure from event type suffix, every event carries `outcome`. This makes filtering and querying straightforward: `prizm events --outcome failure`.

3. **TokenUsage replaces TokenCost** — `TokenCost` (int) is deprecated but not removed. New code uses `TokenUsage` (struct). Both are serialized; `TokenCost` is derived from `TokenUsage.TotalTokens` for backward compat.

4. **Cost aggregation is event-driven** — `CostTracker` subscribes to LLM events and accumulates. No polling, no database queries. The cost report is written when `prizm.task.completed` is emitted.

5. **Trace visualization is read-only** — `prizm trace` reads `events.jsonl` and reconstructs the DAG. No new storage, no projections. Pure computation from existing data.

6. **Schema validation is opt-in** — Call `event.Validate(evt)` before persisting. In production, this can be disabled for performance. In development, it catches bugs early.

7. **No new external dependencies** — Cost estimation uses hardcoded pricing tables (no API calls). Schema validation uses Go struct tags (no JSON Schema library).

## Packages

| Package | Purpose | Files |
|---------|---------|-------|
| `internal/cost/` | Token cost aggregation and reporting | `tracker.go`, `report.go`, `pricing.go`, `tracker_test.go` |
| `internal/event/schema.go` | Lightweight event payload validation | `schema.go`, `schema_test.go` |
| `internal/event/event.go` | Enriched EventMetadata + TokenUsage | Modified |
| `cmd/prizm-cli/cmd_cost.go` | `prizm cost` CLI command | New |
| `cmd/prizm-cli/cmd_trace.go` | `prizm trace` CLI command | New |

## Test Coverage

- TokenUsage serialization/deserialization
- CostTracker accumulation from LLM events
- CostReport aggregation by provider/model/agent
- Pricing table lookup accuracy
- Event schema validation (required fields present, unknown fields allowed)
- Trace reconstruction from parent_id chains
- Backward compatibility (V1 events without V16 fields still parse)

## What's NOT in V16

These were considered but deferred:

- **Data governance / PII scrubbing** — Owned by Remembrance, not Prizm core. Mango score: Impact 5, Feasibility 5, Uniqueness 3.
- **Security audit adapter** — V8 policy events already provide this. Mango score: Impact 5, Feasibility 6, Uniqueness 3.
- **Agent inbox (subscription filtering)** — V9 adapters already subscribe to specific subjects.
- **Resource governor (rate limiting)** — Complex, needs separate design.
- **Event replay** — Partially covered by projections; full replay needs separate V.
- **Next-suggestion heuristics** — Fuzzy, deferred. The enriched metadata foundation makes this possible later.

## Mango's Scoring (deepseek-v4-pro:cloud)

| Feature | Impact | Feasibility | Uniqueness | Decision |
|---------|--------|-------------|------------|----------|
| Event enrichment | 9 | 8 | 9 | ✅ Included |
| Cost tracking | 8 | 9 | 7 | ✅ Included |
| Event trace (causal DAG) | 7 | 9 | 8 | ✅ Included |
| Schema validation | 6 | 9 | 5 | ✅ Included (lightweight) |
| Data governance | 5 | 5 | 3 | ❌ Deferred (Remembrance) |
| Security audit | 5 | 6 | 3 | ❌ Deferred (V8 policy events) |
| Agent inbox | 4 | 7 | 2 | ❌ Deferred (V9 adapters) |
| Resource governor | 7 | 4 | 6 | ❌ Deferred (complex) |
| Event replay | 6 | 5 | 4 | ❌ Deferred (projections cover) |
| Next-suggestion | 6 | 3 | 8 | ❌ Deferred (fuzzy) |