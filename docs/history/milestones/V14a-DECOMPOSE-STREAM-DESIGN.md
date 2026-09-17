# V14a — Decompose the God Object + Add Streaming

## Mission

Split `runner.go` (1199 lines) from a monolithic function into a composable
Stage pipeline. Each stage is an event producer. The pipeline IS the event-driven
architecture. Add streaming LLM responses so events are visible as they happen.

**Prizm is React for AI.** When state changes (events), actions fire automatically.
The event bus IS the render loop. React::setState → re-render :: Prizm::emitEvent → runActions.

## Why V14a First

Everything in V14b-V14e depends on the decomposed pipeline:
- WAL crash recovery (V14b) needs stage boundaries to checkpoint
- Retry (V14b) needs per-stage retry configuration
- Provider chaining (V14c) needs the LLM stage to be swappable
- Concurrent runs (V14d) need isolated RunContext per run
- SQLite (V14d) needs stages to write events as they happen, not at the end

If we don't decompose first, we're building all of this on a 1200-line god object.
That creates MORE complexity, not less.

## Change 1: Stage Pipeline Decomposition

### Current State

`runner.go` has a single `Run()` method that does 15 sequential steps:
1. Generate correlation ID
2. Create run directory
3. Connect to NATS
4. Create V1 task event
5. Create V2 task event
6. Build prompt from task + context
7. Call Remembrance for context (optional)
8. Call LLM provider
9. Parse LLM response
10. Execute tools (if LLM requested tools)
11. Create approval for file writes (if tools requested)
12. Run validation profiles
13. Run deterministic review
14. Write artifacts (events, summary, output, prompt)
15. Publish final event

All of this in ONE function. It's untestable, unextendable, and has race conditions
(the events slice is mutated from multiple code paths).

### Target State

A composable Stage pipeline where each stage implements a simple interface:

```go
// internal/stage/stage.go

// RunContext carries immutable state through the pipeline.
// Copy-on-write: each stage receives a copy, returns a new copy.
// No shared mutable state = no data races.
type RunContext struct {
    RunID          string
    CorrelationID  string
    Task           string
    Project        string
    Agent          string
    Provider       provider.Provider
    ProviderName   string
    Model          string
    Events         []event.Event  // immutable after construction
    Config         RunConfig
    // Stage results accumulated as the pipeline runs
    Results        map[string]*StageResult
}

// StageResult is the output of a single stage.
type StageResult struct {
    StageName string
    Success   bool
    Data      map[string]any  // stage-specific output
    Error     error
}

// Stage is a single step in the pipeline.
// Each stage is independently testable, mockable, and replaceable.
type Stage interface {
    // Name returns the stage identifier for logging and events.
    Name() string

    // Validate checks that the RunContext has everything this stage needs.
    // Returns an error if prerequisites are missing.
    Validate(ctx *RunContext) error

    // Execute runs the stage. Returns a new RunContext (copy-on-write)
    // and a StageResult. Errors are returned in StageResult, not as Go errors,
    // so the pipeline can decide whether to continue or abort.
    Execute(ctx context.Context, ctx *RunContext) (*RunContext, *StageResult, error)

    // Rollback undoes side effects if a later stage fails.
    // Not all stages need rollback (e.g., NATS publish can't be undone).
    // Return nil if rollback is not applicable.
    Rollback(ctx context.Context, ctx *RunContext) error
}
```

### Stage Breakdown

The 15-step monolith becomes 6 stages:

1. **ConnectionStage** — Connect to NATS, validate config, create run directory
2. **RemembranceStage** — Fetch context from Remembrance (optional, skip if disabled)
3. **LLMStage** — Build prompt, call provider, parse response. This is where streaming plugs in.
4. **ToolStage** — Execute tool calls requested by the LLM, apply policy
5. **ApprovalStage** — Create approval requests for mutations (V4)
6. **PersistenceStage** — Write artifacts (events, summary, output, prompt)

Each stage:
- Is ~200 lines (vs. 1200 for the whole runner)
- Has its own test file
- Emits events through the pipeline, not directly to NATS
- Returns a new RunContext (copy-on-write, no shared mutation)

### Pipeline Execution

```go
// internal/stage/pipeline.go

type Pipeline struct {
    Stages []Stage
}

func (p *Pipeline) Run(ctx context.Context, initial *RunContext) (*RunContext, error) {
    current := initial

    for _, stage := range p.Stages {
        // Validate prerequisites
        if err := stage.Validate(current); err != nil {
            return nil, fmt.Errorf("stage %s validation failed: %w", stage.Name(), err)
        }

        // Execute the stage
        next, result, err := stage.Execute(ctx, current)
        if err != nil {
            // Infrastructure error (not a stage failure) — abort pipeline
            return nil, fmt.Errorf("stage %s infrastructure error: %w", stage.Name(), err)
        }

        // Store the result
        if current.Results == nil {
            current.Results = make(map[string]*StageResult)
        }
        current.Results[stage.Name()] = result

        // Check for stage failure
        if !result.Success {
            // Stage failed — write WAL checkpoint, emit failure event, return
            return current, nil
        }

        // Advance to next stage with updated context
        current = next
    }

    return current, nil
}
```

### RunContext is Immutable

The key design decision: RunContext uses copy-on-write. Each stage receives a
copy and returns a new copy. No stage can mutate another stage's state.

This eliminates ALL data races because there IS no shared mutable state.
The events slice grows as the pipeline runs, but each stage appends to its own
copy and returns the new copy.

### Event Emission

Stages don't emit events directly to NATS. They append events to the RunContext:

```go
// RunContext has a method to append events (returns a new RunContext)
func (rc *RunContext) WithEvent(evt event.Event) *RunContext {
    newEvents := make([]event.Event, len(rc.Events), len(rc.Events)+1)
    copy(newEvents, rc.Events)
    newEvents = append(newEvents, evt)
    return &RunContext{
        RunID:         rc.RunID,
        CorrelationID: rc.CorrelationID,
        // ... all other fields
        Events:        newEvents,
        Results:       rc.Results,
    }
}
```

The PersistenceStage writes all accumulated events at the end. The LLM streaming
stage writes token events to a separate channel (see Change 2).

### WAL Integration (V14b Preview)

The stage boundaries naturally create WAL checkpoint opportunities:

```go
// Before each stage, write WAL entry
walEntry := event.NewEvent("wal.stage.entered", "pipeline", map[string]any{
    "run_id":     current.RunID,
    "stage":      stage.Name(),
    "stage_index": i,
})
// fsync the WAL entry
```

This means crash recovery (V14b) can resume from the last completed stage,
not from the beginning of the run.

### Backward Compatibility

The `run.Runner` struct still exists. It becomes a thin wrapper:

```go
func (r *Runner) Run() (*RunResult, error) {
    pipeline := stage.NewPipeline([]stage.Stage{
        &stage.ConnectionStage{Config: r.config},
        &stage.RemembranceStage{Config: r.config},
        &stage.LLMStage{Config: r.config},
        &stage.ToolStage{Config: r.config},
        &stage.ApprovalStage{Config: r.config},
        &stage.PersistenceStage{Config: r.config},
    })

    initialCtx := &stage.RunContext{...}
    finalCtx, err := pipeline.Run(context.Background(), initialCtx)
    ...
}
```

All existing `runner_test.go` tests should pass with minimal changes.
The Runner API is unchanged — `Run()` still returns `(*RunResult, error)`.

## Change 2: Streaming LLM Responses

### Current State

The Ollama provider calls `Generate()` and waits for the full response.
For large outputs (2K+ tokens), this means 30-60 seconds of silence.
The CLI appears to hang. The user has no idea what's happening.

### Target State

A `StreamingProvider` interface that extends `Provider`:

```go
// internal/provider/streaming.go

// TokenChunk represents a single token from a streaming LLM response.
type TokenChunk struct {
    Token     string
    Index     int
    Finished bool
    Error     error
}

// StreamingProvider extends Provider with streaming generation.
// Providers that support streaming implement this interface.
// Providers that don't fall back to the synchronous Generate() path.
type StreamingProvider interface {
    Provider
    GenerateStream(ctx context.Context, req GenerateRequest) (<-chan TokenChunk, error)
}
```

### How Streaming Works in the Pipeline

The LLMStage checks if the provider implements StreamingProvider:

```go
func (s *LLMStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
    if sp, ok := s.Config.Provider.(StreamingProvider); ok {
        return s.executeStreaming(ctx, rc, sp)
    }
    return s.executeSync(ctx, rc)
}
```

**Streaming path:**
1. Call `GenerateStream()` → get a `<-chan TokenChunk`
2. Read tokens from the channel
3. Every 50ms (or 5 tokens), emit a `prizm.llm.token` event with batched tokens
4. On completion, emit `prizm.llm.completed` with full response
5. Token events go to NATS subject `PRIZM_TOKENS` (1h retention, max 10000 messages)
6. Token events are NOT written to events.jsonl (only `llm.completed` is persisted)

**Synchronous path (backward compat):**
1. Call `Generate()` → get full response
2. Emit `prizm.llm.completed` immediately
3. No token events

### Mock Streaming Provider

```go
// internal/provider/mock.go

func (m *MockProvider) GenerateStream(ctx context.Context, req GenerateRequest) (<-chan TokenChunk, error) {
    ch := make(chan TokenChunk, len(m.Response))
    go func() {
        defer close(ch)
        words := strings.Fields(m.Response)
        for i, word := range words {
            ch <- TokenChunk{Token: word + " ", Index: i}
            time.Sleep(10 * time.Millisecond) // simulate streaming delay
        }
        ch <- TokenChunk{Finished: true, Index: len(words)}
    }()
    return ch, nil
}
```

### Ollama Streaming Provider

The Ollama provider's `GenerateStream()` method uses Ollama's `/api/generate`
endpoint with `stream: true`. It reads the streaming response and batches
tokens into 50ms windows.

### Dashboard Integration

The V11 dashboard already polls `/api/runs` every 5 seconds. For streaming,
we add a new SSE (Server-Sent Events) endpoint:

```
GET /api/runs/:id/stream → SSE stream of token events
```

This gives the dashboard real-time token display without WebSockets.

## File Structure

```
internal/
├── stage/
│   ├── stage.go              # Stage interface, RunContext, StageResult, Pipeline
│   ├── pipeline.go           # Pipeline execution logic
│   ├── connection.go         # ConnectionStage (NATS, run dir)
│   ├── remembrance.go        # RemembranceStage (context fetch)
│   ├── llm.go               # LLMStage (prompt build, provider call, streaming)
│   ├── tool.go              # ToolStage (policy-gated tool execution)
│   ├── approval.go          # ApprovalStage (mutation approval)
│   ├── persistence.go        # PersistenceStage (artifact writing)
│   ├── stage_test.go         # Interface tests
│   ├── pipeline_test.go      # Pipeline execution tests
│   ├── connection_test.go    # ConnectionStage tests
│   ├── remembrance_test.go   # RemembranceStage tests
│   ├── llm_test.go          # LLMStage tests (sync + streaming)
│   ├── tool_test.go          # ToolStage tests
│   ├── approval_test.go     # ApprovalStage tests
│   └── persistence_test.go   # PersistenceStage tests
├── provider/
│   ├── provider.go           # Existing Provider interface
│   ├── streaming.go          # StreamingProvider interface, TokenChunk
│   ├── mock.go               # Updated with GenerateStream
│   └── ollama.go             # Updated with streaming support
└── run/
    ├── runner.go             # Thin wrapper around Pipeline
    └── runner_test.go        # Existing tests (minimal changes)
```

## Acceptance Criteria

1. `internal/stage/` package with Stage interface, RunContext, StageResult, Pipeline
2. All 6 stages implemented with Validate/Execute/Rollback
3. RunContext is copy-on-write (no shared mutable state)
4. `go test -race ./...` passes with no data races
5. StreamingProvider interface with GenerateStream method
6. Mock streaming provider with simulated timing
7. Ollama streaming provider with 50ms token batching
8. Token events on separate NATS stream `PRIZM_TOKENS` (1h retention)
9. Token events excluded from events.jsonl (only llm.completed persisted)
10. All 351+ existing tests pass unchanged (Runner API unchanged)
11. `runner.go` reduced to ~200 lines (thin wrapper around Pipeline)
12. Each stage is independently testable with mock dependencies
13. Design doc: `docs/V14a-DECOMPOSE-STREAM-DESIGN.md`
14. Version: `prizm v0.14.0`

## What V14a Does NOT Include

- WAL crash recovery (V14b)
- Retry logic (V14b)
- Idempotency keys (V14b)
- OpenAI provider (V14c)
- Refract Track adapter (V14c)
- SQLite (V14d)
- Concurrent runs (V14d)
- Discord integration (V14e)

These are all planned for later versions. V14a is decomposition + streaming only.
The pipeline structure enables all of them, but we don't implement them here.