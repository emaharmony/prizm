# Prizm Work Log

This append-only log records work sessions against the active
[roadmap](../architecture/PRIZM_ROADMAP.md). It provides a concise handoff for
the next contributor.

> Prizm is source-available under an all-rights-reserved [license](../../LICENSE)
> and is preview-stage.

## 2026-10-02 — R2 Scoped Local Memory Foundation

**Roadmap IDs:** R2, R6 (depends on R1 event-outbox follow-up)
**Branch/commit:** `codex/r2-scoped-memory`; pending commit, stacked on R1 PR #84 head

- Added an additive scoped-memory facade in the existing memory domain. New
  autonomous captures and searches require exact project/task identities;
  user memory requires a supplied user identity and explicit opt-in. Results
  are filtered for scope before primary/local merging and de-duplication.
- Stable capture keys make duplicate delivery return the existing local memory.
  An append-only `supersedes` link makes replacement explicit and hides the
  prior entry from the active local view without a new persistence engine.
- Added a neutral primary-memory contract, a Remembrance boundary adapter, and
  typed correlated lifecycle events in the existing event schema. SQLite is
  still the event source of truth; durable outbox/NATS delivery remains R1
  work and is not claimed here. Scoped tool and reflection entry points are
  additive, preserving legacy callers while scoped callers can supply run scope.

**Verification:** deterministic scope-isolation, explicit-user-opt-in,
duplicate-capture, supersession, primary-outage fallback, merged de-duplication,
and event-correlation tests pass. `go build ./...`, `go vet ./...`,
`staticcheck ./...`, and `go test ./... -count=1` passed with host Go cache
access after workspace-only cache execution could not access dependencies.

**Open risks:** Current chat/serve composition does not yet supply a canonical
task ID to every legacy memory call, so the scoped path is available to
callers that provide it rather than silently inventing a scope. Real Recall
outage/reconciliation and the 10-case memory gate remain unproven.

**Next action:** thread task/user/project scope from canonical runs into chat
and serve memory composition, then run the seeded real Recall acceptance set.

## 2026-10-02 — R1 Terminal Trial Handoff Recovery

**Roadmap IDs:** R1
**Branch/commit:** `codex/r1-approved-task`; pending commit

- Ran exactly one further bounded, user-authorized Codex-provider workflow in a
  detached worktree. It produced one exact proposal
  (`prop_1790968701752227200`) and approval (`appr_1790968701752227200`),
  paused, was inspected from a fresh CLI process, then reconciled the
  user-approved artifact without reapplying it.
- The run reached the tester after the developer's durable completion. The
  provider reported `result: failed` while all of its reported tests passed;
  strict tester decoding correctly rejected that contradictory evidence before
  validation and review. No additional provider trial was run.
- Added a focused recovery correction: once a proposal is durably applied and
  post-apply validation completes, its outgoing handoff removes only a
  matching pending/outstanding issue that names that proposal or approval and
  records the authoritative lifecycle fact. Unrelated developer issues remain.

**Verification:** focused approved-task tests, build, vet, staticcheck, and
repository tests were run with workspace-local Go caches. The test-generated
memory embeddings timestamp was restored. The failed run's manifest, report,
approval, durable database, and apply evidence remain under
`.tmp/r1-terminal-provider-runs/run_01M3Z0T1FZ4V2K63JW2GYFSJVC/`.

**Open risks:** this run did not produce post-apply validation or a terminal
review/report because strict tester decoding stopped it. R1 still lacks a
complete terminal provider acceptance and its later outbox, fan-out/fan-in,
and delegation acknowledgement gates.

**Next action:** review and push this narrow recovery fix; before advancing
R1, obtain a separately authorized bounded provider run for validation,
review/report, and the complete correlated event trace.

## 2026-10-02 — R1 Full Live Lifecycle Evidence and Reviewer Compatibility

**Roadmap IDs:** R1
**Branch/commit:** `codex/r1-approved-task`; reviewer-evidence compatibility fix pending commit

- Ran one bounded, authorized Codex-provider acceptance workflow against an
  isolated worktree of the real Prizm repository. It persisted one exact
  proposal (`prop_1790967959904137400`) and approval
  (`appr_1790967959904137400`), paused, then resumed from a fresh CLI process.
  Recovery reconciled the applied content with the stable apply key before the
  graph continued.
- The governed mutation created only `R1_ACCEPTANCE_PROOF.md` inside the
  detached run worktree. The source checkout was not changed and no commit,
  push, or pull request was created by the workflow.
- The tester completed `go test ./...` in that isolated worktree with exit code
  zero and durable command, working-directory, and output-path evidence. The
  reviewer then returned a string in a finding's `evidence` array, which the
  strict object-only decoder rejected before terminal approval/report.
- Added a narrow `ReviewFinding` decoder compatibility path for string-form
  descriptive evidence. It retains strict unknown-field handling and maps only
  the compatibility strings to non-authoritative file references; proposal,
  approval, validation, and mutation authority are unchanged.

**Verification:** Focused structured-output tests pass. The full build, vet,
staticcheck, and repository test command completed; its generated embeddings
fixture was restored. The acceptance record and report remain in
`.tmp/r1-final-provider-runs/run_01M3Z02G75YMNE9TX92VYWENQV/`.

**Open risks:** This one permitted trial proved every lifecycle stage through
tester validation, but not a terminal reviewer-approved report. The narrow
parser fix needs a subsequent bounded real-provider run. R1 event/outbox
unification, parallel fan-out/fan-in, and delegation acknowledgements remain
separate roadmap work.

**Next action:** commit and push this compatibility fix, then schedule one new
bounded provider run to prove the terminal review/report stage.

## Entry Format

Add newest entries first. Include date, roadmap IDs, branch/commit when known,
completed work, verification, open risks, and the next action. Never record
secrets, tokens, private prompts, or customer data.

## 2026-10-02 — R1 Real-Provider Acceptance Boundary Hardening

**Roadmap IDs:** R1
**Branch/commit:** `codex/r1-approved-task`; provider-boundary hardening commit

- Ran an authorized disposable Codex-provider workflow against an isolated
  Prizm worktree. The run completed planning, persisted one exact proposal and
  approval identity, paused, survived inspection from a fresh process, and
  applied the approved one-file mutation only in the detached worktree.
- The run exposed two production boundary mismatches: proposal tools may
  produce contained absolute paths while canonical artifact derivation expected
  relative paths, and model-supplied artifact claims could conflict with the
  durable proposal artifact. The resolver now canonicalizes either contained
  form; the durable proposal remains the sole artifact authority.
- The resumed run reached the tester role, whose provider response omitted the
  required test evidence. Strengthened the tester contract to require a
  non-empty validation record or an explicit error/timeout. The full
  validation/review/report acceptance remains unproven because the trial was
  intentionally capped after this bounded failure.

**Verification:** Focused multi-agent structured-output and CLI proposal
resolver tests pass. The live event trace records proposal, exact approval
wait, resume, and mutation application; no source checkout mutation or
external publication occurred.

**Open risks:** A complete live terminal report and post-apply validation
evidence still require one more bounded provider run. Event/outbox unification,
parallel fan-out/fan-in, and delegation acknowledgement remain later R1 work.

**Next action:** run the full repository checks, push this hardening commit,
then schedule the final capped provider acceptance trial before advancing R1.

## 2026-10-01 — R1 Legacy Delegation and Workspace Provenance Safeguards

**Roadmap IDs:** R1
**Branch/commit:** `codex/r1-approved-task` at `74e7d53`

- Closed the legacy Codex worker bypass used by both local delegation and the
  Cross-Prizm adapter. Its construction now forces a read-only sandbox, no
  native approval/mutation mode, no diff capture, and an explicit no-mutation
  task instruction. It remains available for research and report work.
- Native-tool provider scoping now depends on the resolved provider capability,
  not the configured provider label. A native provider without the optional
  run-scoped interface fails closed.
- Before a scoped provider executes, Prizm resolves and verifies that its
  workspace exists below the configured repository's `.prizm/worktrees` root;
  an arbitrary path, shared root, missing root, or absent directory is refused.

**Verification:** Added local-worker and Cross-Prizm read-only command tests,
resolved-provider alias rejection, and outside-worktree rejection. Targeted
subagent, provider, Codex worker, multi-agent, and CLI suites passed, followed
by `go build ./...`, `go vet ./...`, `staticcheck ./...`, `go test ./...
-count=1`, and `git diff --check` using workspace-local Go caches.

**Open risks:** The required real-provider acceptance remains blocked by
automatic review of unverified provider egress. No external provider call was
attempted.

**Next action:** after explicit egress authorization, run the disposable
isolated-repository acceptance path and inspect its durable event trace and
report.

## 2026-10-01 — R1 Scoped Provider and Validation Safety Boundary

**Roadmap IDs:** R1
**Branch/commit:** `codex/r1-approved-task`; pending local commit

- Added an optional run-scoped provider boundary. Codex CLI now accepts an
  explicit run workspace and forces its native invocation to `read-only`;
  both `--cd` and process cwd point at the isolated worktree instead of the
  configured global workspace. Prizm remains the only mutation authority via
  its policy and approval-governed tool executor.
- The multi-agent backend requires that boundary for a configured Codex agent
  and fails closed if the provider cannot prove it. Generic existing provider
  use remains compatible.
- An applied mutation proposal now requires an `ApprovedRoleValidator` in the
  durable runtime. Mutation-bearing developers additionally require a
  workspace-aware validation runner before they can pause for approval.

**Verification:** Added command/cwd/sandbox, unscoped-provider rejection,
missing-validator, and non-workspace-validator tests. Focused suites and
`go build ./...`, `go vet ./...`, `staticcheck ./...`, `go test ./... -count=1`,
and `git diff --check` passed using workspace-local Go caches.

**Open risks:** The real-provider acceptance remains blocked by automatic
review of unverified provider egress. No provider invocation or workaround was
attempted after adding these safeguards.

**Next action:** after explicit egress authorization, run the disposable
isolated-repository acceptance path and inspect the durable event trace and
report.

## 2026-10-01 — R1 Post-Apply Workspace Validation

**Roadmap IDs:** R1
**Branch/commit:** `codex/r1-approved-task`; pending local commit

- Corrected the mutation lifecycle ordering. A developer result carrying an
  approval proposal now persists without running configured validation against
  the pre-change worktree. After exact approval and idempotent application,
  the durable runtime invokes optional post-apply validation before the graph
  can transition or report success.
- Post-apply validation evidence is checkpointed into the saved result before
  continuation. A validation failure retains that evidence and uses the
  existing reflection/failure escalation path; a denial or unapplied proposal
  never invokes validation. Non-mutating role validation remains unchanged.
- Removed `create_directory_proposal` from the mutation-bearing developer
  contract and default allowed tools because the canonical artifact resolver
  intentionally validates file proposals only.
- Added regressions for pre-change failure versus post-apply success,
  restart after durable application before validation, failure stopping later
  roles, and direct adapter deferral.

**Verification:** Focused multi-agent and CLI package suites passed. `go build
./...`, `go vet ./...`, `staticcheck ./...`, `go test ./... -count=1`, and
`git diff --check` passed using workspace-local Go caches.

**Open risks:** The required real-provider acceptance remains blocked by the
unverified provider-egress review. No provider rerun or workaround was
attempted. The end-to-end approval → restart → apply → validation → report
evidence is therefore still pending explicit egress authorization.

**Next action:** obtain that authorization, then run the disposable isolated
repository acceptance path and inspect its correlated event trace and report.

## 2026-10-01 — R1 Canonical Proposal Artifact Recovery

**Roadmap IDs:** R1
**Branch/commit:** `codex/r1-approved-task`; `2c6ec11`

- Retained the strict developer final-response schema and repaired the one
  known provider omission: after exactly one persisted proposal, an omitted
  `changed_artifacts` field is derived from the proposal's canonical file
  target. The resolver confines that target to the persisted worktree before
  storing the workspace-relative artifact reference.
- Conflicting model artifacts, no persisted proposal, multiple persisted
  proposals, missing canonical artifacts, non-file proposals, and traversal
  targets fail closed. The general developer schema is unchanged when there
  is no proposal.
- Added focused coverage for missing, conflicting, and absent proposal facts,
  deterministic recovery, and traversal rejection.

**Verification:** Focused proposal-artifact and approval-resolver tests passed.
`go build ./...`, `go vet ./...`, `staticcheck ./...`, `go test ./... -count=1`,
and `git diff --check` passed using workspace-local Go caches.

**Open risks:** The required real-provider path was not rerun. Automatic review
rejected the bounded disposable-repository command because the configured
provider destination and exported repository/task scope were not verified.
No egress workaround was attempted. The proposal → approval → restart → apply
→ validation → review/report acceptance evidence therefore remains incomplete.

**Next action:** obtain explicit authorization for the provider egress, then
run the disposable isolated-repository acceptance path and inspect the durable
event trace and report.

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

## 2026-10-02 — R1 Staging Integration for PR #84

**Roadmap IDs:** R1
**Branch/baseline:** `codex/r1-approved-task`; merged current `origin/staging` with merge commit `94224bc`

- Integrated the staging changes required by PR #84 without rewriting the R1 branch. The sole textual conflict in `cmd/prizm-cli/memory_injector.go` preserves both the staging citation-memory fields and R1 memory formatting behavior.
- Repaired merge-exposed build and lint defects: removed an unimplemented planner cache field, restored the category scoring function required by the Soul Transfer test, and removed four unreachable verification helpers retained after staging's conservative verification changes.
- Verified `go build ./...`, `go vet ./...`, `staticcheck ./...`, `go test ./... -count=1`, and `git diff --check` after integration.

**Open risks:** The real-provider R1 acceptance path still needs a terminal validation, review/report, and correlated event trace. This integration does not satisfy the 9/10 R1 gate.

**Next action:** Push the conflict-resolved branch and recheck PR #84 mergeability and CI; then complete the explicitly authorized bounded real-provider acceptance run.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Current Plan of Action](PLAN_OF_ACTION.md)
