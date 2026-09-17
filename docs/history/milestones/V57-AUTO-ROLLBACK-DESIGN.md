# V57 — Automated Rollback for Failed Loops

Status: source-current
Date: 2026-07-04

## Goal

Before V57, a gated loop that ended failing could only fix forward: a blocking
verification failure kept iterating until phase/budget limits, and the run then
finalized "completed" with a red build sitting on the branch. Rollback was
explicitly a human decision. That is the right default — so auto-rollback is
**opt-in** (`global.auto_rollback: false`) — but an unattended loop platform
needs the option to discard failing work automatically instead of accumulating
verification debt.

This closes the "Automated rollback" gap from the loop-engineering readiness
assessment (see `docs/LOOP-READINESS-STATE.md`).

## Design

### Config (`internal/workflow/v2/config.go`)

- `global.auto_rollback: bool` (default false)
- `global.max_verification_attempts: int` (0 = default 3) — blocking
  verification failures allowed before rollback fires mid-run
- `ValidateConfig` rejects `auto_rollback` without at least one phase with
  **blocking** verification: rollback needs an objective failure signal.

### Engine hook (`engine.go`)

`Engine.SetRollbackRunner(fn RollbackFunc)` — mirrors `SetVerificationRunner`.
`RollbackFunc(ctx, reason) error`. Nil runner ⇒ rollback skipped even when
configured (tests, callers that can't roll back).

### Trigger points (`driver.go`)

All gated on `Global.AutoRollback`, all funnel through idempotent
`doRollback(ctx, reason)` which emits `workflow.rollback` and records a
`RollbackRecord` on the state:

1. **Verification attempts exhausted** — in the `ActionFinal` verification
   check: blocking failure with `Verification.Attempts >= max` stops iterating
   (no more budget burned on a doomed run), marks the phase fallback, rolls back.
2. **Blocking-phase fallback** — the run ends here anyway; work is discarded
   first, and state is persisted before the error return.
3. **End-of-run failing state** — at finalization, a run whose last
   verification is still red (e.g. budget exhausted mid-fix) rolls back.

A rolled-back run finalizes with the new status **`rolled_back`** (never
"completed"), stops executing later phases, and its EXECUTION phase reads
`fallback` (re-stamped after `ExecutionPhase.Exit`, which unconditionally marks
completed). A rollback that itself fails records the error and does NOT report
`rolled_back` — the branch may still hold bad commits.

### Rollback runner (`cmd/prizm-cli/wake_handler.go`)

Wired in `RunGatedLoop` when the workflow config enables it:

- Start SHA + original branch captured before the loop starts (via
  `gitx.CurrentSHA` / `gitx.CurrentBranch`); if the SHA can't be resolved,
  rollback is disabled for the run with a log line.
- Rollback = `gitx.ResetHard(workRoot, startSHA)`, then checkout the original
  branch if the model wandered off it (non-isolated mode).
- Under V56 worktree isolation: the reset happens inside the worktree, and the
  run branch `prizm/<runID>` is deleted after worktree cleanup (defer ordering:
  branch deletion registered before the worktree-removal defer, so it runs
  after — a checked-out branch can't be deleted).
- **Pushed commits are never force-removed.** The remote run branch remains
  for forensics; PR/branch review stays the remote safety net.

### Surfacing

- `prizm runs <id> --json` gains `"rollback": {reason, error?, at}` and shows
  `"status": "rolled_back"`.
- Config knobs documented in `examples/workflows/gated-loop.yaml`.

## Tests (`internal/workflow/v2/rollback_test.go`)

- attempts-exhausted → exactly N verification runs, one rollback,
  `workflow.rollback` emitted, status `rolled_back`, EXECUTION `fallback`
- disabled ⇒ pre-V57 fix-forward behavior, runner never fires
- no runner wired ⇒ no deadlock, no rollback record
- end-of-run red verification ⇒ finalization rollback
- failing rollback ⇒ error recorded, status NOT `rolled_back`
- passing run ⇒ runner never fires
- ValidateConfig: `auto_rollback` without blocking verification rejected

Git mechanics (`ResetHard` to a captured SHA, branch deletion) are covered by
`internal/gitx/gitx_test.go` against real temp repos (V56).
