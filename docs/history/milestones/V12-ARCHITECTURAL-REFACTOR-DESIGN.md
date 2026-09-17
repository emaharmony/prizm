# V12 — Architectural Refactor

## Mission

Fix the foundation before building higher. Multi-agent orchestration needs a clean
extensible codebase, not a 1500-line CLI monolith and a 1200-line god object.

## Problem

Prizm works. V1-V11 all pass. But the codebase has structural problems that will
block real extensibility:

1. **CLI monolith** — `cmd/prizm-cli/main.go` is 1581 lines with 32 functions.
   Every new version adds more functions to this single file. It's not sustainable.

2. **Runner god object** — `internal/run/runner.go` is 1199 lines. It handles
   the entire V1-V5 lifecycle, tool execution, approval, validation, review,
   event emission, and artifact writing. It does too much.

3. **Path safety duplication** — `isWithinRoot` exists in `tool/policy.go` and
   `tool/builtins.go`. `sanitizePath` exists in `dashboard/server.go`. The same
   path containment logic is implemented 4+ times with slight variations.

4. **Test bloat** — `runner_test.go` is 2191 lines. It tests everything about
   the runner because the runner does everything.

## What V12 Does

This is NOT a feature version. V12 is a structural refactor that:
- Splits the CLI monolith into command packages
- Extracts safety utilities into a shared package
- Decomposes the runner into focused components
- Makes the codebase genuinely extensible for V13 (Multi-Agent)

## Design Decisions

### D1: CLI Commands as Separate Files

Instead of one 1581-line `main.go`, split each command group into its own file:

```
cmd/prizm-cli/
├── main.go              # Entry point, subcommand dispatch (~100 lines)
├── cmd_run.go           # prizm run
├── cmd_tool.go          # prizm tool list/run
├── cmd_approval.go      # prizm approval list/show/approve/deny
├── cmd_validation.go    # prizm validation list/run
├── cmd_policy.go        # prizm policy list/evaluate
├── cmd_workflow.go      # prizm workflow list/show/run/status
├── cmd_adapter.go       # prizm adapter list/show/health
├── cmd_projection.go    # prizm projection list/rebuild/query
├── cmd_dashboard.go     # prizm dashboard
└── cmd_health.go        # prizm health
```

Each file is in package `main`. They share no state — each creates its own
registries and dependencies. This is the simplest split that removes the
monolith without requiring complex dependency injection.

### D2: Shared Safety Package

Create `internal/safety/path.go` with the single path containment implementation:

```go
package safety

// IsWithinRoot checks that absPath is within absRoot.
// Both must be absolute paths. Returns true if absPath is contained
// within absRoot, accounting for symlinks and path traversal.
func IsWithinRoot(absPath, absRoot string) bool

// ResolveAndContain resolves a relative path against a root and
// verifies it stays within root. Returns the absolute safe path
// or an error if path traversal is detected.
func ResolveAndContain(root, relPath string) (string, error)
```

Then replace all 4+ implementations with calls to this single one.

### D3: Runner Decomposition

The runner currently handles:
1. V1 task lifecycle
2. V2 LLM provider calls
3. V3 tool execution
4. V4 approval + mutation
5. V5 validation + review
6. Event emission throughout

Decompose into focused structs:

```go
// RunOrchestrator coordinates the overall run lifecycle.
// It delegates to phase-specific handlers.
type RunOrchestrator struct {
    config    RunConfig
    emitter   EventEmitter     // shared event emitter
    tools     *tool.Registry
    approvals *approval.Store
    validator *validation.Registry
    reviewer  *review.Reviewer
}

// EventEmitter centralizes event creation and publishing.
// Every component uses this instead of building events directly.
type EventEmitter struct {
    correlationID string
    runID         string
    metadata      event.EventMetadata
    js            nats.JetStreamContext  // nil is fine (guard exists)
}
```

The orchestrator calls phases in sequence, each emitting events through the
shared emitter. This keeps the runner under 300 lines.

### D4: No Behavior Changes

This refactor must NOT change any behavior. All 326 tests must pass unchanged.
The refactored code must produce the same events, the same artifacts, the same
summary.json. If anything changes, it's a bug, not an improvement.

## What V12 Does NOT Include

- **New features** — no new capabilities, just better structure
- **Multi-agent** — that's V13, built on the clean foundation
- **API changes** — public interfaces stay the same
- **Performance changes** — this is about structure, not speed
- **New tests** — existing tests prove behavior is preserved

## Acceptance Criteria

1. CLI split into command files (main.go under 200 lines)
2. `internal/safety/` package with single `IsWithinRoot` + `ResolveAndContain`
3. All path containment checks use `safety.IsWithinRoot`
4. Runner decomposed into orchestrator + phase handlers
5. Shared `EventEmitter` replaces scattered event creation
6. All 326+ existing tests pass unchanged
7. No behavior changes (same events, artifacts, summaries)
8. README documents V12 truthfully