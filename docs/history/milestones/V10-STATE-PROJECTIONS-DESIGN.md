# V10 — State Projections / Query Layer Design

## Mission

Turn Prizm's event history into queryable current state, without adding a database.

## Problem

Prizm emits events for everything. Runs produce `events.jsonl` files. Right now, to answer questions like "what's the status of this run?" or "how many approvals are pending?", you have to read the entire event stream and reconstruct the state yourself.

V10 introduces **projections** — functions that read events and produce queryable indexes. A projection is just: "given all events of type X, compute Y."

## Core Concept: Projections as Indexes Over Events

Think of it like a database view, but file-based. Each projection:

1. **Subscribes** to specific event types (e.g., `prizm.task.*`, `prizm.approval.*`)
2. **Accumulates** state from those events
3. **Writes** a snapshot to disk under `runs/<run_id>/projections/<name>.json`

Projections are **pure functions of events**. Given the same events, you always get the same projection. No side effects, no external state.

## Why "Projection" and Not "Query" or "View"

- **Projection** comes from CQRS/Event Sourcing — you project event streams into read models
- **View** implies read-only, which is true, but "projection" emphasizes the *computation* aspect
- **Query** implies a request-response pattern, which this isn't — projections are pre-computed

## Architecture

```
events.jsonl
    ↓ (scan)
ProjectionRunner
    ↓ (apply each projection)
┌──────────────┐  ┌──────────────┐  ┌──────────────┐
│ RunStatus    │  │ ApprovalState│  │ ToolHistory  │
│ projection   │  │ projection   │  │ projection   │
└──────┬───────┘  └──────┬───────┘  └──────┬───────┘
       ↓                  ↓                  ↓
  run_status.json  approval_state.json  tool_history.json
```

Each projection:
1. Reads all events from `events.jsonl`
2. Filters to its subscribed event types
3. Applies events in order, updating internal state
4. Writes the final state to a JSON file

## What V10 Adds

### 1. Projection Interface (`internal/projection/projection.go`)

```go
// Package projection provides state projections over Prizm event streams.
//
// A projection is a pure function of events: given a sequence of events,
// it computes a read-only snapshot of some aspect of system state.
// Projections are the "read side" of Prizm's event-sourced architecture.
//
// Why projections? Instead of querying a database, you project events
// into pre-computed indexes. Each projection subscribes to specific event
// types and accumulates state from them. Given the same events, you always
// get the same projection. No side effects, no external state.
//
// This is the CQRS/Event Sourcing pattern adapted for local-file-based
// systems. Prizm stores events; projections are derived views of those
// events. You can always rebuild a projection from scratch by replaying
// the event stream.
package projection

// Projection computes a read-only snapshot from a sequence of events.
// Implementations must be pure: given the same events in the same order,
// they must produce the same output. No side effects, no external state,
// no randomness.
type Projection interface {
    // Name returns the projection's unique identifier (e.g., "run_status")
    Name() string

    // Subscribe returns the event types this projection cares about.
    // Only events matching these types will be passed to Apply.
    // Use "*" to subscribe to all event types.
    Subscribe() []string

    // Apply processes a single event and updates the projection state.
    // Apply must be idempotent: applying the same event twice should
    // produce the same state as applying it once.
    Apply(event.Event) error

    // Snapshot returns the current projection state as a serializable map.
    // This is what gets written to disk.
    Snapshot() map[string]any
}
```

### 2. Projection Runner (`internal/projection/runner.go`)

The runner reads events, applies all registered projections, and writes snapshots.

```go
// Runner applies a set of projections to an event stream.
type Runner struct {
    projections []Projection
}

// NewRunner creates a runner with the given projections.
func NewRunner(projections ...Projection) *Runner

// Run reads events from an events.jsonl file and applies all projections.
// Each projection receives only the event types it subscribes to.
// After processing, snapshots are written to runs/<run_id>/projections/.
func (r *Runner) Run(eventsFile, runDir string) error

// RunFromEvents applies projections to an already-loaded event slice.
// Useful for testing and programmatic use.
func (r *Runner) RunFromEvents(events []event.Event, runDir string) error
```

### 3. Built-in Projections

#### RunStatus Projection (`internal/projection/builtin/runstatus/`)

Tracks the current status of a run. This is the simplest projection —
it reads task lifecycle events and produces a status snapshot.

```go
// RunStatusProjection tracks the lifecycle state of a run.
//
// Subscribed events:
//   - prizm.task.created    → status: "created"
//   - prizm.task.started    → status: "running"
//   - prizm.task.completed  → status: "completed"
//   - prizm.task.failed      → status: "failed"
//
// The snapshot includes:
//   - status: current lifecycle status
//   - task: the task description
//   - project: the project name
//   - agent: the agent name
//   - started_at: when the run started (ISO 8601)
//   - completed_at: when the run finished (if applicable)
//   - duration_ms: how long the run took
//   - error: error message if failed
type RunStatusProjection struct { ... }
```

#### ApprovalState Projection (`internal/projection/builtin/approval/`)

Tracks the state of all approvals in a run. This answers "what approvals
exist, and which ones are still pending?"

```go
// ApprovalStateProjection tracks approval state transitions.
//
// Subscribed events:
//   - prizm.approval.requested → new approval, status: "pending"
//   - prizm.approval.granted   → approval granted, status: "approved"
//   - prizm.approval.denied     → approval denied, status: "denied"
//   - prizm.approval.expired    → approval expired, status: "expired"
//
// The snapshot includes:
//   - approvals: map of approval_id → {status, target_path, requested_by, ...}
//   - pending_count: how many approvals are still pending
//   - total_count: total approvals in this run
type ApprovalStateProjection struct { ... }
```

#### ToolHistory Projection (`internal/projection/builtin/toolhistory/`)

Tracks all tool calls in a run. This answers "what tools were called,
what did policy decide, and what were the results?"

```go
// ToolHistoryProjection tracks tool call history.
//
// Subscribed events:
//   - prizm.tool.requested → new tool call, status: "requested"
//   - prizm.tool.approved  → tool approved by policy
//   - prizm.tool.denied     → tool denied by policy
//   - prizm.tool.started   → tool execution started
//   - prizm.tool.completed → tool execution succeeded
//   - prizm.tool.failed    → tool execution failed
//
// The snapshot includes:
//   - calls: ordered list of tool calls with status, policy, result
//   - summary: {total, approved, denied, succeeded, failed}
type ToolHistoryProjection struct { ... }
```

### 4. Projection Events (`internal/projection/events.go`)

```go
const (
    EventTypeProjectionStarted   = "prizm.projection.started"
    EventTypeProjectionCompleted = "prizm.projection.completed"
    EventTypeProjectionFailed    = "prizm.projection.failed"
)
```

### 5. CLI Commands

```bash
# Rebuild all projections for a run (or all runs)
./prizm projection rebuild --run <run_id>
./prizm projection rebuild --all

# Query a specific projection
./prizm projection query run_status --run <run_id>
./prizm projection query approval_state --run <run_id>
./prizm projection query tool_history --run <run_id>

# List available projections
./prizm projection list
```

### 6. Auto-projection

When a run completes, projections are automatically computed as part of the
run lifecycle. This means `summary.json` and `projections/` are both written
at run completion. You can also rebuild projections manually with
`prizm projection rebuild`.

## File Layout

```
runs/<run_id>/
├── events.jsonl              # The source of truth (always)
├── summary.json              # V1 run summary (unchanged)
├── prompt.md                 # V1 prompt artifact
├── output.md                 # V1 output artifact
├── projections/              # V10: derived state
│   ├── run_status.json       # Current run lifecycle state
│   ├── approval_state.json   # Approval state transitions
│   └── tool_history.json    # Tool call history
├── approval/                 # V4 approval artifacts
├── validation/               # V5 validation artifacts
└── review/                   # V5 review artifacts
```

## What V10 Does NOT Include

- **Database** — projections are local JSON files, not PostgreSQL/SQLite
- **Real-time streaming** — projections are computed at run completion, not streamed
- **Complex queries** — no SQL-like query language, just projection snapshots
- **Dashboard** — that's V11
- **Multi-run aggregation** — V10 projects within a single run
- **Projection versioning** — snapshots are rebuilt from events, so version = events

## Key Design Principles

1. **Events are the source of truth.** Projections are derived. You can always
   rebuild a projection from scratch by replaying `events.jsonl`.

2. **Projections are pure functions.** Given the same events in the same order,
   a projection always produces the same snapshot. No side effects, no external
   state, no randomness.

3. **Projections are local files.** No database, no external service. The
   `projections/` directory is just JSON files in the run directory.

4. **Projections are additive.** Adding a new projection never changes existing
   ones. Removing a projection just means its file isn't written anymore.

5. **Rebuild is cheap.** Scanning a run's `events.jsonl` and applying projections
   takes milliseconds. You can rebuild all projections at any time.

6. **Projections don't replace summary.json.** The existing summary format is
   maintained. Projections provide structured, queryable state alongside it.

## Why This Matters

Right now, to answer "what's the status of run X?" or "which approvals are
still pending?", you have to parse `summary.json` (which is flat) or scan the
entire `events.jsonl` (which is verbose). Projections give you structured,
queryable state that's derived from the event stream but optimized for
reading.

This also sets up V11 (Dashboard) — the dashboard will query projections,
not raw events.

## Tests Required

- Projection interface compliance
- Runner: apply projections to events, write snapshots
- RunStatusProjection: lifecycle state transitions
- ApprovalStateProjection: approval state transitions
- ToolHistoryProjection: tool call history
- CLI: projection rebuild, query, list
- Idempotency: applying the same event twice produces same state
- All existing tests pass (289+)

## Acceptance Criteria

1. Projection interface exists with Name, Subscribe, Apply, Snapshot
2. ProjectionRunner can apply projections to events.jsonl
3. RunStatusProjection tracks lifecycle state
4. ApprovalStateProjection tracks approval state
5. ToolHistoryProjection tracks tool call history
6. Projections are written to runs/<run_id>/projections/
7. CLI: `prizm projection rebuild --run <run_id>`
8. CLI: `prizm projection query <name> --run <run_id>`
9. CLI: `prizm projection list`
10. Auto-projection runs at run completion
11. All existing tests pass (289+)
12. README documents V10 truthfully