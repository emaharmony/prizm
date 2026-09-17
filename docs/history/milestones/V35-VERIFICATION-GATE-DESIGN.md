# V35 — Objective Verification in the Gated Loop

**Status:** Source-current
**Last Updated:** 2026-06-29

## Problem

The gated autonomous-development loop (`PROBE → RESEARCH → PLAN → FEEDBACK_PRE →
EXECUTION → FEEDBACK_POST → REPORT`) could write files, `git_add`, `git_commit`,
and `git_push`, but nothing in the loop ever **built or tested** the code it
produced. The `task_completion` gate counts tasks the model marked done; the
`review_pass` gate relies on a human or sub-agent reviewer. Neither is an
*objective* signal that the committed code compiles and its tests pass.

Prizm already had the machinery to run that signal safely: the V5 `validation`
package runs **allowlisted** command profiles (e.g. `go_test_all` → `go test
./...`) with a timeout, a minimal environment, and artifact capture — the model
never controls what executes. It was simply never wired into the loop.

## Design

A phase may declare a **verification** block:

```yaml
- name: EXECUTION
  type: execution
  gate: { type: task_completion, mode: partial_allowed }
  verification:
    profile: go_test_all   # a registered V5 validation profile
    blocking: true         # phase cannot complete until it passes
```

### Flow

When a phase signals completion (`ActionFinal`/`ActionPhaseComplete`) and any
commit/push enforcement has been satisfied, the engine runs the configured
profile **before** evaluating the phase gate:

1. **Pass** → record the result, proceed to the normal gate.
2. **Blocking fail** → feed the captured stdout/stderr tail back to the model as
   `VERIFICATION FAILED: …`, reset the commit/push flags (so the fix is
   re-committed and re-pushed), and keep iterating so the model fixes the real
   failure. Each attempt consumes the phase's iteration budget, so a perpetually
   failing build cannot loop forever — it falls through to the phase fallback.
3. **Non-blocking fail** → record the result, surface it, but allow the gate to
   proceed.
4. **Could not run** (unknown profile, safety rejection, executor error) → never
   blocks; logged and skipped. Verification misconfiguration must not deadlock a
   run.

The outcome is stored on `WorkflowState.Verification`
(`VerificationRecord{Profile, Passed, ExitCode, Summary, Attempts, RanAt}`),
emitted as a `phase.verification` event, and surfaced in the FEEDBACK_POST review
package so reviewers see objective proof rather than the model's say-so.

### Wiring

- **Config:** `PhaseConfig.Verification *VerificationConfig` (`config.go`).
  `ValidateConfig` rejects a verification block with an empty profile.
- **Engine:** `Engine.SetVerificationRunner(VerificationFunc)` injects the
  runner. When unset (tests, projects without a profile) verification is a no-op,
  so the change is fully backward-compatible.
- **Driver:** `Engine.runVerification` (`driver.go`) implements the flow above.
- **Runtime:** `RunGatedLoop` (`wake_handler.go`) binds the runner to a
  `validation.Executor` rooted at the project repo, mapping a profile result to a
  `VerificationOutcome` and tailing the captured artifacts for the summary.

`DefaultConfig()` and `examples/workflows/gated-loop.yaml` enable
`go_test_all` (blocking) on EXECUTION out of the box. Override `profile` to match
a project's toolchain, or register additional profiles in
`validation.RegisterBuiltins`.

## Loop-correctness fix (shipped with V35)

While wiring this in, a latent bug surfaced in `Engine.Drive`: a `break` inside
the action `switch` only broke the switch, not the phase iteration loop, so a
phase that passed its gate on iteration 1 still ran to `MaxIterations`. That was
harmless for idempotent gates but caused verification to re-run the test command
every iteration. The inner loop is now labeled (`iterLoop`) and the gate-pass
paths `break iterLoop`, so a phase exits as soon as its gate is satisfied.

## Tests

`internal/workflow/v2/verification_test.go` covers: blocking failure → retry →
pass; non-blocking failure records-but-proceeds; no-runner skip; non-runnable
skip; default-config presence; YAML round-trip; and validation of an empty
profile.

## Run budgets (shipped with V35)

The loop tracked tokens and parsed `max_total_time` but enforced neither. `Drive`
now computes a wall-clock deadline from `global.max_total_time` and enforces a
`global.max_total_tokens` ceiling (`-1` = unlimited, `0` = default, positive = explicit cap). Both are checked before each phase and before
each iteration; on exceed the loop emits `workflow.budget_exhausted` and finalizes
gracefully (a soft stop, not a hard error). Either dimension is disabled by a zero
value. `DefaultConfig` ships a 2,000,000-token ceiling so a runaway or stuck run
cannot burn tokens without bound.

## Stuck-loop detection (shipped with V35)

A model can burn its whole budget repeating one action that never makes progress.
`Drive` now counts identical tool calls per phase (signature = tool name + input,
rendered with `fmt %v` so map ordering is stable). At the halfway mark it injects a
"NO PROGRESS — change your approach" nudge; at `global.max_repeated_tool_calls`
(default 6) it emits `phase.stuck` and aborts the phase (which then takes its normal
fallback). The counter resets on every phase entry, so legitimate re-entry after
"changes requested" starts clean.

## Model self-check: the `run_validation` tool (shipped with V35)

The verification gate runs *after* the model commits. To let the model catch
failures *before* a commit, EXECUTION exposes a `run_validation` tool. When the
model calls it, the engine runs a validation profile through the same injected
runner the gate uses (profile from the call's `profile` input, else the phase's
configured profile), records the result on `state.Verification`, emits
`phase.verification` with `source: "tool"`, and feeds the pass/fail summary back.
It is handled inside the engine (not delegated to the external tool executor), so
it reuses the safe allowlisted runner and is unit-testable. EXECUTION guidance
nudges the model to self-check before declaring done.

## Idempotent tool retry (shipped with V35)

Transient tool failures (timeout, connection reset, 503) used to waste a whole
loop iteration. `Engine.executeTool` now wraps read-only tools (`read_file`,
`list_dir`, `search_files`, `git_status/log/diff/branch_list`, `project_overview`,
`web_search`, `memory_search`) in `internal/retry`'s exponential backoff, retrying
only errors `IsRetryable` recognises as transient. **Mutations are never wrapped**
— retrying a `git_commit` could double-commit — and deterministic errors (file not
found) are not retried. Each retry emits `tool.retry`.

## Follow-ups

- Per-phase token budgets (in addition to the per-run ceiling).
- Wire `EventStore` into the one-shot `run` lifecycle for queryable cross-run
  history (events currently persist to JSONL only).
- Delegation retry-on-timeout and capability-aware `delegate` targeting (see V37).
