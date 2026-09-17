# V5 — Validation + Review Pipeline

## Mission

Add post-mutation validation and deterministic code review. After a mutation is
applied, V5 runs validation profiles (e.g., tests, linters) and generates a
deterministic review summary. The reviewer can advise but never approve or apply
mutations — it's read-only by design.

**Commands come only from named profiles. No LLM input to the shell.**

## What Changed

### Validation System (`internal/validation`)
- `Profile` struct: Name, Description, Command, Args, WorkingDir, Timeout, AllowedExitCodes
- `Registry`: register, list, resolve profiles by name
- `Executor`: run profiles with context-based timeout, capture stdout/stderr
- Built-in profiles: `echo_test` (safe noop), `go_test_all` (runs `go test ./...`)
- Safety checks: no pipes, redirects, command chaining, or arbitrary execution
- Path containment via `EvalSymlinks` on working_dir
- Validation events: `prizm.validation.requested/started/completed/failed/skipped/timeout`
- Artifacts: `<profile>.json`, `<profile>.stdout.txt`, `<profile>.stderr.txt`

### Deterministic Review (`internal/review`)
- `Reviewer` generates reviews from mutation + validation results — no LLM
- `Review` struct: recommendation, summary, files_changed, validation_results, reviewer_notes
- Recommendations: `approved_for_human_review`, `needs_fix`, `validation_failed`, `no_mutation_detected`
- Review events: `prizm.review.requested/started/completed/failed`
- Review artifact: `review.md` with human-readable recommendation
- Reviewer CANNOT approve or apply mutations (read-only by design)

### CLI Commands
- `prizm validation list` — list registered validation profiles
- `prizm validation run <profile>` — run a validation profile directly
- `prizm approval approve --validate` — approve AND run validation pipeline

### Runner Integration
- Validation runs after mutation is applied
- Review is generated from validation + mutation results
- `appendEventsToFile()` for post-run event persistence (events written after initial lifecycle)
- `summary.json` updated with `validations` and `reviews` arrays

### Fixes
- `TestV5ApprovalApproveWithValidate` rewritten as component integration test
- `TestV5RunValidation/Review` use `echo_test` profile (avoids recursive `go test`)
- `NewEmptyRegistry()` added for test-specific profile sets

## Key Packages/Files

| Package / File | Purpose |
|---|---|
| `internal/validation/profile.go` | Profile definition and registry |
| `internal/validation/executor.go` | Safe command execution with timeout |
| `internal/validation/safety.go` | Path containment, command safety checks |
| `internal/review/review.go` | Deterministic reviewer + recommendation logic |
| `internal/review/artifact.go` | Review artifact generation (review.md) |
| `internal/event/event.go` | 10 validation + 4 review event types |
| `internal/run/runner.go` | Post-mutation validation + review pipeline |
| `cmd/prizm-cli/main.go` | Validation and approval commands |

## Design Decisions

1. **Commands from profiles only, never from LLM output** — The LLM cannot
   request arbitrary shell commands. Validation profiles are registered in
   code (hardcoded command strings). This eliminates the entire class of
   command injection attacks.

2. **Deterministic review, no LLM** — The reviewer is pure logic: check if
   validations passed, check mutation status, produce a recommendation.
   No LLM subjectivity. The reviewer is fast, predictable, and auditable.

3. **Read-only reviewer** — The reviewer cannot approve or apply mutations.
   It only advises. The decision to merge remains with the human operator.
   This distinction is enforced by the code structure (Reviewer has no
   mutation state, only read access to results).

4. **Timeout required for every profile** — No unbounded execution. Every
   profile has a `timeout_seconds` field. Context-based timeout enforcement
   via `context.WithTimeout`.

5. **Safety by command structure** — The executor checks for pipes, redirects,
   command chaining (`&&`, `||`, `;`). Only single-command execution with
   static args is allowed.

6. **Integration testing avoids recursion** — Test profiles use `echo_test`,
   not `go test`, to prevent the tests from recursively running themselves.
   `NewEmptyRegistry()` allows tests to create isolated profile sets.

7. **Validation runs post-mutation** — The pipeline is: `mutation applied → validation → review`.
   If validation fails, the review recommends `needs_fix` or `validation_failed`.
   The operator can then fix and re-approve.

## Safety Model

| Threat | Protection |
|---|---|
| LLM requesting arbitrary commands | Only profile-defined commands allowed |
| Shell injection via args | Args are static strings, not templated |
| Pipe/redirect/chain injection | Blocked at executor level |
| Working directory escape | EvalSymlinks containment check |
| Unbounded execution | Timeout required per profile |
| Reviewer self-approval | Reviewer has no mutation authority |

## Test Coverage

- **180 tests** across 10 packages (up from 154)
- Validation: profile registration, resolve, validation, empty name, missing command
- Executor: echo success, test failure, timeout, path traversal blocked
- Safety: pipe blocked, redirect blocked, command chain blocked, EvalSymlinks containment
- Review: all four recommendations, validation passed/failed, no mutation, reviewer notes
- Runner: V5 validation pipeline, V5 review generation, approval with validate
- CLI: validation list, validation run, approval approve --validate

## Pipeline Flow (Post-Mutation)

```
Mutation applied → emit(prizm.mutation.applied)

For each validation profile:
  → emit(prizm.validation.requested)
  → emit(prizm.validation.started)
  → cmd.Run(profile.Command, profile.Args)
  → Success → emit(prizm.validation.completed)
  → Failure → emit(prizm.validation.failed)
  → Timeout → emit(prizm.validation.timeout)
  → Error   → emit(prizm.validation.skipped)
  → Artifact: <profile>.json, <profile>.stdout.txt, <profile>.stderr.txt

Reviewer.Generate(mutation_status, files_changed, validation_results)
  → emit(prizm.review.requested)
  → emit(prizm.review.started)
  → Determine recommendation
  → emit(prizm.review.completed)
  → Artifact: review.md
```
