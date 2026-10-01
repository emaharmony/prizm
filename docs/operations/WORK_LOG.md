# Prizm Work Log

This append-only log records work sessions against the active
[roadmap](../architecture/PRIZM_ROADMAP.md). It provides a concise handoff for
the next contributor.

> Prizm is source-available under an all-rights-reserved [license](../../LICENSE)
> and is preview-stage.

## Entry Format

Add newest entries first. Include date, roadmap IDs, branch/commit when known,
completed work, verification, open risks, and the next action. Never record
secrets, tokens, private prompts, or customer data.

## 2026-10-01 — R1 Tool-First Provider Contract

**Roadmap IDs:** R1
**Branch/commits:** `codex/r1-approved-task`; `6cf33f3`

- Corrected the text-provider tool protocol: the parser now selects a valid `tool_request` before any final envelope, developer instructions require a separate proposal request before the developer role JSON, and the corrective path requests only the proposal action before accepting the final role JSON after its tool result.
- Reserved two remaining local iterations for a correction so a proposal action and its final role JSON can both complete inside the existing role budget. Added focused parser, mixed-response, proposal-tool-to-final-loop, corrective proposal, and budget tests.
- The bounded real-provider run used a disposable repository and persisted an approval record through `write_file_proposal`, proving the tool request reached the approved mutation boundary. The provider then returned a developer final object without the required `changed_artifacts` field. Strict decoding failed the run before approval pause, application, validation, and review; the terminal report and cleanup completed without external publication.

**Verification:** Focused protocol tests and `go build ./...`, `go vet ./...`, `staticcheck ./...`, `go test ./... -count=1`, and `git diff --check` passed before the real-provider run.

**Open risks:** The real provider can now create the approval artifact but does not yet reliably emit the required developer final schema after the tool result. Therefore the proposal → approval → apply → validation → review/report acceptance gate remains incomplete.

**Next action:** make the post-proposal developer final-schema contract more reliable, then repeat the disposable-repository run through approval, restart, application, workspace validation, and report.

## 2026-10-01 — R1 Proposal-Required Coding and Roster Binding

**Roadmap IDs:** R1
**Branch/commits:** `codex/r1-approved-task`; `be616e0`, `75c06df`

- Added distinct proposal and approval identities through the existing approval, tool, and graph lifecycle. The durable checkpoint now retains both identities, preserving schema-v1 compatibility when a historical approval lacks a proposal ID.
- A developer that returns without a persisted proposal receives exactly one corrective provider turn. That turn carries the prior output, shares the original deadline, and is limited to the remaining token and iteration budget. Exhaustion or a second omission fails closed with an inspectable governance outcome.
- Added validation lifecycle events, checked terminal worktree cleanup after report persistence, containment enforcement, and cleaned-workspace manifest recovery. Role-profile composition now selects configured agents by required capability while preserving explicit role-profile input overrides.
- A real provider run used a disposable repository and reached the configured planner and developer in an isolated worktree. The developer omitted the required proposal on both turns; the run terminated safely, emitted a terminal failure event, wrote an inspectable report, and reclaimed its worktree. No commit, push, or pull request was created by the run.

**Verification:** Focused corrective-turn, validation-event, worktree-cleanup, cleaned-manifest recovery, and roster-binding tests passed. `go build ./...`, `go vet ./...`, `staticcheck ./...`, `go test ./... -count=1`, and `git diff --check` passed using workspace-local Go caches.

**Open risks:** The real provider has not yet completed proposal → approval → apply → validation → review/report. The enforcement is proven to fail closed, but the coding-agent/tool prompt still needs to yield reliable proposal-tool use before the R1 real-provider acceptance gate can pass.

**Next action:** refine the coding-agent proposal-tool contract, then repeat the disposable-repository acceptance run including approval, restart, application, validation, and report evidence.

## 2026-09-30 — R1 Durable Approved-Task Execution

**Roadmap IDs:** R1
**Branch/commits:** `codex/r1-approved-task`; `2785552`, `0315378`

- Connected an exact proposal to durable approval, idempotent application,
  crash reconciliation, bounded retry, validation, resume, and terminal result
  retention. An unrelated approval cannot advance a waiting run, and an
  ambiguous partially applied change fails closed for review.
- New graph runs execute in persisted detached Git worktrees. Project-aware
  validation records the command, arguments, working directory, output, and
  worktree diff in the run evidence.
- Added correlated proposal lifecycle events and focused coverage for grants,
  denials, unrelated decisions, duplicate delivery, restart reconciliation,
  retry, lifecycle evidence, validation, and real temporary-repository
  worktree isolation. Existing adapters and `workflow/v2` remain green.
- A bounded real-provider run exposed and fixed strict role-response parsing,
  planner schema guidance, iteration budget, and proposal authority for coding
  agents. The latest run reaches the developer in an isolated real Prizm
  worktree but the provider returns a final response without calling the
  required proposal tool.

**Verification:** `go test ./... -count=1`, `go vet ./...`, `go build ./...`,
and `staticcheck ./...` pass. The generated memory fixture timestamp was
restored and is not part of the commits.

**Open risks:** the real-provider end-to-end acceptance gate is incomplete.
The developer role can currently finish without persisting a proposal, so the
run stops before approval, restart recovery, validation, review, and report.

**Next action:** enforce proposal-required completion for mutation-bearing
roles with a bounded corrective turn, then repeat the real-provider acceptance
run and inspect its run record and event trace.

## 2026-09-30 — Linux CI Repair After R1 Foundation Merge

**Roadmap IDs:** R1
**Branch/baseline:** `codex/r1-approved-task`, from `staging` at `5270739`

- Investigated the Linux failures from PR #83 merge workflow `36757367089`.
  Staticcheck found dormant private code and small lint issues, including a
  non-functional loop `break`; these were removed or corrected without adding
  a new package. A Slack dispatch test now covers the registered-handler path.
- Corrected `TestCancelRegistryConcurrent`: its 100 goroutines reused only 26
  session IDs, allowing one registration to replace another before cancellation
  and leaving a goroutine blocked. The test now uses unique session IDs.
- Verified focused package tests, `go vet ./...`, `staticcheck ./...`, and
  `go test ./... -count=1`. The test suite regenerated a timestamp in the
  tracked memory embedding fixture; it was restored and is not part of this
  change.

**Open risks:** this repairs CI reliability only. The R1 approval → apply →
verify → resume lifecycle, isolated worktree validation, and durable event
contract remain unimplemented.

**Next action:** commit and review the CI repair, then implement R1's exact
proposal approval identity through durable apply, verify, and resume.

## 2026-09-30 — Roadmap-Alignment Review

**Roadmap IDs:** R1, R2, R3, R4, R5, R6, R7, R8
**Branch/baseline:** `codex/adapter-driven-runtime` at `73fac4e`; documentation
changes were uncommitted at review time.

- Performed read-only code and documentation analysis. No code feature
  implementation, tests, builds, or Git history operations were performed; this
  session made documentation-only updates.
- Corrected the plan handoff to reflect existing foundations instead of claiming
  no roadmap work has started. Evidence includes durable approval and graph
  components (`internal/approval`, workflow graph paths), isolated-worktree and
  validation machinery (`internal/autopatch`, `internal/validation`), NATS and
  JetStream bus paths (`internal/bus`, `cmd/prizm-bus`), adapter contracts
  (`internal/adapter`), remote NATS bridge groundwork (`internal/bridge`), and
  existing memory, skill, and autopatch subsystems.
- The evidence supports partial R1 and R4 foundations, plus partial R2, R3, R5,
  R6, and R8. R7 remains missing in Prizm, and the roadmap's 9/10 gates remain
  unproven because the required integrated approval/apply/verify/resume,
  durable event contract, real-provider/repository trials, and two-node failure
  injection evidence do not yet exist.

**Open risks:** the exact proposal approval is not yet proven to control apply
and resume; graph and NATS paths remain split; project-aware validation is not
the primary graph default; delegation lacks the required acknowledgement and
recovery proof; Recall fallback and skill-version provenance remain incomplete.

**Next action:** implement R1 approval identity → apply → verify → resume, then
add worktree isolation and project-aware validation to the primary path before
the unified event/outbox work.

## 2026-09-30 — Harness Audit and Roadmap Controls

**Roadmap IDs:** R1, R2, R3, R4, R5, R6, R7, R8
**Branch/base commit:** `codex/adapter-driven-runtime`, based on `0d04d0f`

- Recorded the audited baseline: reasoning 5/10, reflection 3/10, autonomous
  delivery 3/10, user memory 5/10, and recovery 7/10.
- Established the authoritative roadmap, current plan, and this persistent log.
  The immediate sequence is R1 proposal approval → apply → verify → resume,
  isolated worktrees/project-aware validation, then the unified event/outbox and
  typed intake contract.
- Branch inspection reported the CI fix commit with tests and `go vet` passing.
  No merge or push was performed in this session.

**Open risks:** proposal approval is not connected to durable apply/resume;
graph and NATS event stacks are split; local delegation needs reliable terminal
acknowledgement; Recall reliability/idempotency remains a dependency.

**Next action:** design and implement R1's approval identity and application
boundary, then prove it through the R1 verification scenarios in the current
plan.

## 2026-09-30 — PR Setup Handoff Blocked by GitHub Authorization

**Roadmap IDs:** R1, R4
**Pre-push review baseline:** `codex/adapter-driven-runtime` at `059fb4f`;
refreshed `origin/staging` at `9d0833d`

- The pre-push review found a clean working tree and the branch 17 commits
  ahead of refreshed `origin/staging` and zero commits behind.
- Confirmed no existing GitHub pull request or remote branch existed for
  `codex/adapter-driven-runtime`.
- Prepared a ready-for-review PR body in ignored workspace path
  `.tmp/pr-body.md`, covering the adapter-driven workflow runtime, validation,
  and known R1/R4 limitations.
- GitHub rejected the push with HTTP 403 because the available GitHub
  credentials lack write access to `emaharmony/prizm`. No PR was created, no
  push completed, and no merge was attempted.

**Open risks:** the R1 approval → apply → verify → resume path and the R4
unified graph/NATS event-outbox contract remain incomplete; remote review is
blocked until an authorized GitHub credential is available.

**Next action:** after GitHub authentication with write access is restored,
push `codex/adapter-driven-runtime`, create the prepared PR against `staging`,
then complete the R1 approval-to-verified-resume slice.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Current Plan of Action](PLAN_OF_ACTION.md)
