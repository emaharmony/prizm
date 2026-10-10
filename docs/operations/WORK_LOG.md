# Prizm Work Log

This append-only log records work sessions against the active
[roadmap](../architecture/PRIZM_ROADMAP.md). It provides a concise handoff for
the next contributor.

> Prizm is source-available under an all-rights-reserved [license](../../LICENSE)
> and is preview-stage.

## 2026-10-02 — R2 Scope Isolation and Serve Wiring

**Roadmap IDs:** R2, R6
**Branch/baseline:** `codex/r2-scoped-memory`, rebased onto `origin/staging` at `a2a5622`

- Rebased the single R2 commit directly onto merged R1 staging; obsolete stacked
  R1 history was not replayed. Explicit capture keys now derive a distinct
  local identity from canonical user/project/task scope, preventing a producer
  key from colliding across authorization boundaries.
- Scoped capture and search require a correlation ID. `serve` injects trusted
  workspace/run/agent identity for memory tools and overrides model input;
  operations without those real identities fail closed. Scoped events persist
  their correlated lifecycle facts in the existing SQLite event store.
- Recall search now requests project/task/user/session/agent scope and rejects
  every missing or mismatched result before local/primary merge. The adapter
  maps available topics, timestamps, and supersession metadata.

**Verification:** focused memory, Remembrance, tool, and CLI tests; `go build
./...`, `go vet ./...`, `staticcheck ./...`, `go test ./... -count=1`, and
`git diff --check` passed.

**Open risks:** CLI chat remains on legacy memory composition and therefore
does not invent a scoped task identity. Local fallback is immediate, but
durable retry/reconciliation needs a local-store contract and is deferred to
R6. The 10-case real Recall score gate remains unproven.

**Next action:** run the seeded scoped-retrieval acceptance set and a real
Recall outage/recovery trace, then design durable reconciliation from evidence.

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

## 2026-10-02 — R2 Autonomous Prompt Scope and Sync-Pending Evidence

**Roadmap IDs:** R2, R6
**Branch/baseline:** `codex/r2-scoped-memory` based on merged staging `a2a5622`

- Replaced autonomous serve prompt injection's unscoped local search and
  `ListRecent` fallback. The prompt path derives canonical workspace project,
  active run, session, agent, and authorized owner identity, and sends the same
  identifiers to Recall context building.
- Recall context is injected only when it echoes all trusted scope metadata.
  Its cache key includes the complete scope and a task-text digest. A missing or
  mismatched field fails closed and the scoped memory facade supplies the local
  fallback.
- Added a durable `prizm.memory.capture.sync_pending` lifecycle fact for primary
  capture outage or absence. A duplicate delivery reuses the local memory ID and
  retries the primary call; this is evidence for a future R6 reconciliation
  worker, not a claim that reconciliation exists.
- Focused scoped-memory, Remembrance, and serve prompt-scope tests pass.
  Full `go test ./... -count=1`, build, vet, staticcheck, and diff checks pass
  when Go uses its normal host cache; the restricted sandbox alone denies that
  cache's metadata writes.

**Open risks:** A live Recall service must implement and echo task/session scope
metadata before its context packs can be used. R6 still needs a durable replay
consumer that drains pending sync facts after recovery.

**Next action:** implement the R6 idempotent reconciliation consumer and run a
live outage/recovery trace against a scope-aware Recall service.

## 2026-10-02 — R2 Scoped Memory Hardening and Score Gate

**Roadmap IDs:** R2, R6
**Branch/baseline:** `codex/r2-scoped-memory` at `6179a36`, based on merged staging `a2a5622`

- Made serve-chat memory authority explicit: model-supplied project, task, user,
  session, agent, correlation, and user-scope fields are removed before the
  trusted workspace and run identity are applied. Missing either trusted value
  fails closed.
- Made scoped lifecycle events strict. An event-store failure returns an error;
  a retry finds the existing local capture and does not duplicate it. Capture
  keys now reject changed content or metadata unless an explicit superseding
  relation identifies the prior memory.
- Repaired Markdown persistence for project, session, and agent scope fields;
  those fields now round-trip through the local fallback store.
- The deterministic R2 seeded retrieval gate passed **10/10** with zero scope
  leakage. Focused memory/remembrance/tool/serve tests, `go build ./...`,
  `go vet ./...`, and `staticcheck ./...` passed.
- A deterministic primary-outage/recovery trace confirms local fallback remains
  readable after the primary returns. A real local Remembrance trace was not
  possible: the available Python launcher could not start and Ollama could not
  start because its local log rotation was denied. No remote reconciliation was
  claimed or implemented.

**Open risks:** local fallback captures still need an R6 idempotent
reconciliation path to Recall. Live service recovery remains unproven in this
environment. R1's event-outbox and delegation gates remain open.

**Next action:** add the R6 reconciliation record and run the live service
outage/recovery acceptance trace when Python and Ollama are available.

## 2026-10-02 — R2 Scoped Memory Review Handoff

**Roadmap IDs:** R2, R6
**Branch/baseline:** `codex/r2-scoped-memory` based on `origin/staging` `a2a5622`

- Prepared the R2 scoped-memory lifecycle and isolation slice for normal review.
  The branch carries canonical autonomous scope injection, strict scoped capture
  events, explicit supersession, scoped Recall-primary/local-fallback behavior,
  and the deterministic 10/10 seeded retrieval gate with zero leakage.
- Verification evidence for the branch includes `go build ./...`, `go vet
  ./...`, `staticcheck ./...`, and `go test ./... -count=1`.

**Open risks:** This slice does not provide a durable replay consumer for
`sync_pending` captures and has no live Recall outage/recovery proof. Those are
R6 follow-up work; the R2/R6 acceptance claims remain limited accordingly.

**Next action:** publish the branch for review, then implement idempotent
local-to-Recall reconciliation and perform the live outage/recovery trace when
the required local services are available.

## 2026-10-08 — R1 Event-Outbox Planning Handoff

**Roadmap IDs:** R1; R2/R6 parked
**Branch/baseline:** `codex/r1-event-outbox` from refreshed `origin/staging`
`dca2156`

- Parked draft PR #86 and `codex/r6-memory-reconciliation` without further
  Recall implementation, review, merge, or a claim of live Recall recovery.
- Reprioritized the next implementation slice to the roadmap's R1 event
  contract: one typed command intake and durable SQLite outbox shared by graph
  and NATS action delivery.
- Defined the required delegation lifecycle evidence: correlated idempotency
  keys, explicit accepted/progress/terminal acknowledgements, deadlines, and
  terminal failure for lost workers, while preserving `workflow/v2`.
- The slice is planned only. No event-outbox, delegation, or Recall code has
  been implemented or tested in this session.

**Open risks:** graph and NATS actions still have split delivery paths;
delegation remains short of durable acknowledgement and recovery. Parallel
fan-out/fan-in and R3/R4 remain dependent on this foundation.

**Next action:** implement the typed event contract and SQLite outbox, with
embedded NATS and fake-worker tests for duplicate delivery, restart, deadline,
lost-worker, and terminal-report behavior.

## 2026-10-08 — R1 Durable Event-Outbox Vertical Slice

**Roadmap IDs:** R1; R2/R6 parked
**Branch/baseline:** `codex/r1-event-outbox` from `origin/staging` `dca2156`

- Added the canonical typed command and outcome envelope in the event domain,
  including run/task/delegation, correlation/causation, idempotency, deadline,
  schema, and payload identity.
- Added a SQLite outbox with accept-once conflict detection, atomic claim and
  lease, bounded retry, terminal failure, expired-claim replay, deadline
  enforcement, one terminal outcome per delivery, and correlated reports.
- Routed `workflow/v2` delegation commands through the outbox in the serve wake
  path, including startup replay. The legacy TaskPacket remains the NATS wire
  payload. The canonical graph continues to use its existing atomic run/event
  outbox, so compatibility paths remain green.
- Added delegation and per-attempt delivery IDs, explicit accepted/progress
  transitions, retry identity, and rejection of late completion from an older
  attempt. Embedded-NATS tests prove the durable path preserves the legacy wire
  contract; deterministic tests cover publish failure, restart replay,
  duplicate intake, leases, retry bounds, deadline failure, terminal outcome,
  stale retry rejection, and correlated reporting.
- Verified `go build ./...`, `go vet ./...`, `staticcheck ./...`, `go test ./...
  -count=1`, and `git diff --check`.

**Open risks:** worker outcome intake is not yet composed end to end with the
outbox report, and graph parent pause/resume has not yet been driven by these
accepted/terminal outcome facts. Parallel fan-out/fan-in and the real-provider
R1 score gate remain open.

**Next action:** compose durable worker outcome intake and parent resume, then
prove duplicate delivery, worker loss, retry, restart, and parallel fan-in with
one terminal event-derived report.

## 2026-10-08 — R1 Durable Delegation Outcome Intake

**Roadmap IDs:** R1; R2/R6 parked
**Branch/baseline:** `codex/r1-event-outbox` at `6381756`

- Replaced the wake path's lossy generic completion forwarding with canonical
  accepted/progress/terminal outcomes. Each fact is validated against the
  stored command, persisted before forwarding, replayed after restart while
  unconsumed, and handed to `workflow/v2` with blocking/context backpressure.
- The sub-agent worker now publishes an accepted fact before execution and one
  terminal fact with the full run, correlation, task, delegation, delivery,
  artifact, and result identity. Delegation IDs use sortable unique IDs rather
  than second-resolution timestamps.
- Terminal publish exhaustion and command deadlines now atomically add failed
  or timed-out facts to the correlated report. Forged identity, progress before
  acceptance, duplicate terminal facts, and late results from older retries
  fail closed.
- Tests cover restart before intake, pending-outcome replay, backpressure without
  loss, complete artifact forwarding, mismatch rejection, progress ordering,
  stale retry rejection, deadline reporting, and `workflow/v2` parent task
  advancement. `go build ./...`, `go vet ./...`, `staticcheck ./...`, and the
  repeated full `go test ./... -count=1` pass. One first full-suite attempt hit
  a pre-existing Windows temporary-directory cleanup race; the isolated test,
  CLI package, and complete rerun passed.

**Open risks:** the canonical multiagent graph still uses its own atomic event
outbox and does not yet consume delegation outcomes to drive a parent waiting
checkpoint. Parallel fan-out/fan-in and the real-provider R1 score gate remain
open.

**Next action:** implement deterministic parallel research/code/review
fan-out/fan-in and bind the graph parent wait/resume seam to canonical terminal
delegation outcomes.

## 2026-10-08 — R1 Outcome Acknowledgement Ordering

**Roadmap IDs:** R1; R2/R6 parked
**Branch/baseline:** `codex/r1-event-outbox` at `f521d03`

- Moved durable outcome acknowledgement from the wake intake to the workflow
  engine. `workflow/v2` now saves the run-specific and current state files after
  applying an external event and only then marks its outcome consumed.
- `WaitForResume` routes accepted, progress, and terminal delegation facts
  through the same handler while a parent is paused, persists and acknowledges
  them, and keeps waiting until an approval or review decision actually resumes
  the parent.
- Duplicate outcome event IDs are now content-addressed in practice: exact
  redelivery is idempotent, while changed status, sequence, payload, or identity
  under the same event ID fails closed.
- Tests cover crash after wake enqueue but before acknowledgement, replay after
  reopen, paused-parent delegation handling, persistence-before-acknowledgement,
  and changed duplicate rejection.

**Open risks:** this proves durable parent advancement for `workflow/v2`. The
canonical multiagent graph has no child/delegation identity in its durable run
schema and no delegation waiting checkpoint, so adding an outcome source alone
would create an unowned partial authority path. Its graph-owned child state,
waiting transition, checkpoint-before-dispatch, and checkpoint-before-ack
contract remain a separate coherent R1 slice. Parallel fan-out/fan-in and the
real-provider gate remain open.

**Next action:** add the bounded canonical-graph delegation state and waiting
checkpoint contract, adapt `event.Outcome` at the composition edge, and prove
restart, mismatch, deadline, terminal transition, and acknowledgement ordering.

## 2026-10-08 — R1 Compatibility Listener Backpressure

**Roadmap IDs:** R1
**Branch/baseline:** `codex/r1-event-outbox` at `7102370`

- Confirmed the standalone `workflow/v2` NATS listener has no production
  composition caller; production delegation outcomes use the durable wake
  intake and SQLite acknowledgement lifecycle.
- Marked the standalone listener's canonical outcome source as compatibility
  only and replaced its full-channel drop with blocking backpressure that is
  released by listener shutdown. It does not claim durable delivery.
- A focused embedded-NATS test fills the workflow event queue and proves the
  canonical outcome is delivered after capacity becomes available.

**Open risks:** legacy feedback, agent-status, and TaskCompletion messages in
the compatibility listener retain their historical best-effort behavior. They
are outside the canonical outcome path and are not used by production serve
composition.

**Next action:** continue the canonical graph delegation checkpoint work; keep
the standalone listener compatibility-only unless a production caller adopts
it with a durable intake.

## 2026-10-09 — R1 Canonical Graph Delegation Safety Gate

**Roadmap IDs:** R1; R4 remains dependent; R2/R6 Recall work remains parked

- Completed the canonical graph's durable child/delegation checkpoint and
  outcome-consumption seam with deterministic injected-outcome coverage. The
  typed command/outcome envelope and SQLite outbox remain the durable path for
  `workflow/v2` delegation delivery and outcome acknowledgement.
- Kept production canonical-graph delegation disabled. A production request via
  `PRIZM_GRAPH_ROLE_DELEGATION=1` now fails fast because no durable graph-role
  publisher and worker are composed. Deterministic composition tests inject
  correlated outcomes only through an unexported package-test helper; no
  environment switch can activate no-worker graph delegation in production.
- The existing sub-agent worker cannot safely consume graph commands: it accepts
  legacy task packets, while graph roles require the parent-owned profile,
  workspace/worktree, authorization, proposal lifecycle, validation, and
  graph-role result contract.
- Evidence: `go test ./cmd/prizm-cli -count=1` passed, including default inline
  graph execution, production enablement fail-fast, and test-only durable
  checkpoint/restart outcome consumption. No Recall changes were made.

**Open risks:** no live graph-role worker, real-provider proof, embedded-NATS
delivery matrix, or parallel research/code/review fan-out/fan-in exists yet.
Graph delegation deadline checks run only when an API/operator/outcome resume
path executes; there is no graph wake/recovery scheduler at wall-clock expiry.
R4 adapters remain blocked on that verified command-consumer contract.

**Next action:** compose a graph-owned worker and durable publisher/outcome
intake, then prove the R1 failure matrix and parallel fan-in with an
event-derived report.

## 2026-10-09 — R1 Production Graph-Role Worker and Recovery

**Roadmap IDs:** R1; R4 remains dependent; R2/R6 Recall work remains parked

- Added the typed graph-role command/result contract. Commands bind the exact
  prepared role request to trusted run, role, task, execution, workspace,
  delegation, delivery, correlation, command-event, and deadline identity.
- Composed an opt-in `prizm serve` NATS publisher/worker. The worker validates
  the command against its persisted manifest and the immutable SQLite outbox
  row, including exact payload bytes. NATS carries notifications only. Outcome
  intake likewise requires exact bytes already persisted by the trusted worker
  ledger before it can resume the parent.
- Added a SQLite execution ledger. Concurrent duplicates do not re-execute,
  terminal results replay after restart, and a new worker that finds an
  interrupted in-flight owner records an ambiguous terminal failure instead of
  blindly repeating role execution.
- Routed delegated mutation proposals into the parent runtime's existing exact
  approval, application, and post-apply validation lifecycle. The worker never
  applies a proposal or supplies approval authority; the parent checkpoints the
  proposal identities before acknowledging the terminal worker fact.
- Added autonomous deadline scanning and embedded-NATS coverage for duplicate
  delivery, forged outcome rejection, malformed-command terminal rejection,
  bounded publisher retry, terminal replay after restart, interrupted worker
  failure, and wall-clock deadline wake. Local wake serialization and the
  durable run claimer arbitrate scanner races.
- Evidence: focused failure-matrix tests passed; `go test ./... -count=1`
  passed with workspace-local `TMP`, `TEMP`, and `GOCACHE`; `git diff --check`
  passed.

**Open risks:** no configured real-provider graph task was run in this session.
The separate bounded parallel research/code/review fan-out/fan-in acceptance
and its complete event-derived report remain open, so the full R1 score gate is
not claimed. The worker requires a shared run database; authenticated remote
workers remain R3 scope.

**Next action:** run one opt-in serve task with a configured provider, review
its command/outcome/proposal trace, then implement and prove bounded parallel
fan-out/fan-in.

## 2026-10-09 — R1 Live-Provider Outcome and Resume Recovery

**Roadmap IDs:** R1; R4 remains dependent; R2/R6 Recall work remains parked

- Diagnosed the disposable configured-provider failure as two independent
  compatibility/recovery defects. The accepted outcome event ID is stable by
  delivery key and status, but acceptance reconstruction could replace its
  timestamped bytes before transport replay; the dashboard `RunLocator` also
  rejected the CLI's current schema-v2 registry-backed manifest.
- Made acceptance replay publish the first ledger-persisted bytes. The event
  outbox keeps its strict conflicting-content rejection: a changed body for a
  stable event ID still fails closed.
- When an outcome reaches the worker before the originating `Run` releases
  its durable claim, resume is retried through the existing per-run guard.
  The durable claim remains the sole cross-process execution arbiter.
- Added regression coverage for immutable acceptance replay, temporary claim
  contention retry, and current CLI v2 manifest inspection.
- Evidence: focused `go test ./cmd/prizm-cli ./internal/workflow/multiagent -count=1`
  and `go test ./... -count=1` passed with workspace-local `TMP`, `TEMP`, and
  `GOCACHE`; `git diff --check` passed.

**Open risks:** the disposable real-provider run was executed against the
baseline before this repair. The complete configured-provider acceptance rerun
and the bounded parallel fan-out/fan-in report remain required before claiming
the R1 score gate. Authenticated remote workers remain R3 scope.

**Next action:** rebuild the CLI and rerun the isolated opt-in provider task;
verify one immutable accepted fact, one terminal fact, successful dashboard
snapshot, and eventual resume after an initial claim conflict.

## 2026-10-09 — R1 Graph Workspace Binding and Terminal-Fact Convergence

**Roadmap IDs:** R1; R4 remains dependent; R2/R6 Recall work remains parked

- Seeded `RunState.WorkspaceID` from the trusted reference-runtime manifest at
  run creation. Graph commands now carry that immutable workspace identity in
  both their envelope and prepared role request before the first delegated
  role runs; worker-side identity validation remains unchanged.
- Identified the remaining event-ID conflict as an uncertain publisher result
  racing a terminal fact already persisted by the worker. The dispatcher had
  attempted to create a second terminal failure for the same delivery key,
  with a different diagnostic body. It now preserves the worker terminal fact
  and marks transport delivery complete when that fact proves receipt.
- Added deterministic coverage for workspace-bound command construction and
  an embedded-NATS worker terminal followed by a simulated publisher failure.
  The strict outbox conflicting-content rejection remains covered separately.

**Evidence:** focused CLI, event, and multiagent package tests passed with
workspace-local `TMP`, `TEMP`, and `GOCACHE`.

**Open risks:** the live provider must still be rerun from this commit. The
parallel fan-out/fan-in acceptance report remains required before the R1 score
gate can be claimed.

**Next action:** rebuild the isolated acceptance binary, run one opt-in graph
task, then inspect the outbox trace for a workspace-bound command, exactly one
accepted fact, one terminal fact, and a successful snapshot.

## 2026-10-09 — R1 Persistence-Before-Notification Outcome Closure

**Roadmap IDs:** R1; R4 remains dependent; R2/R6 Recall work remains parked

- Moved trusted worker outcome insertion ahead of NATS publication. Accepted,
  terminal, ambiguous, and replayed outcomes now enter `command_outcomes` from
  their exact graph-role ledger bytes before transport notification; NATS is
  notification-only and duplicate intake remains byte-identical.
- Production graph delegation now explicitly requires the composition-seeded
  workspace identity. Test-only injected delegation remains available without
  that production invariant.
- A fresh acceptance run exposed a second dispatcher race: it attempted a
  synthetic terminal at sequence one after the worker had already persisted
  acceptance at sequence one. Publisher uncertainty now treats accepted or
  terminal worker facts as delivery proof; the rare no-worker fallback derives
  its sequence from the durable maximum.
- Added coverage for persistence before notification, production workspace
  requirement, worker-terminal publisher failure, and accepted worker receipt
  during publisher failure.

**Open risks:** the real-provider process also reported external Codex CLI
plugin-cache and PowerShell snapshot errors; those are provider-environment
failures outside the durable event contract. The graph worker should be rerun
from this repair before R1 acceptance is claimed.

**Next action:** rebuild the isolated binary and rerun one opt-in provider
task. Confirm the snapshot, command workspace fields, and one accepted plus
one terminal outbox fact before investigating any provider-environment error.

## 2026-10-09 — R1 Accepted-Worker Recovery Arbitration

**Roadmap IDs:** R1; R4 remains dependent; R2/R6 Recall work remains parked

- Tightened publisher-failure arbitration: a terminal worker fact completes
  delivery, while an accepted-only worker fact returns the command to pending
  delivery for redelivery or deadline recovery. Acceptance is no longer
  mistaken for a terminal acknowledgement.
- Fallback terminal outcomes allocate the next persisted sequence, preserving
  `UNIQUE(delivery_key, sequence)` without relaxing it.
- Moved manifest workspace verification into the durable rejection path, so a
  mismatch is rejected before acceptance while still producing a trusted
  terminal rejection. Added bounded SQLite-busy retry around exact-byte worker
  outcome insertion for concurrent fan-out writers.

**Evidence:** focused CLI and event tests passed with workspace-local cache
paths. Full-suite validation remains the next execution step.

**Next action:** run the full Go suite, then rerun the isolated provider task
only after this recovery path is committed.

## 2026-10-09 — R1 Expired Accepted-Worker Closure

**Roadmap IDs:** R1; R4 remains dependent; R2/R6 Recall work remains parked

- The graph-worker deadline scanner now reads only the canonical outbox command
  and its trusted accepted ledger fact before producing one durable timed-out
  terminal outcome for a worker that died after acceptance. The parent is then
  woken to consume the terminal fact.
- Resume consumes durable delegation outcomes before evaluating an interrupted
  pending-dispatch checkpoint or deadline. A terminal worker outcome therefore
  cannot be discarded by recovery ordering.
- Dispatcher publication failures now return success when the outbox already
  proves a terminal worker result; idempotency and strict terminal bytes remain
  unchanged.

**Evidence:** deterministic focused tests cover accepted-worker expiry,
terminal-before-expired-pending-dispatch, and terminal proof after publisher
failure using workspace-local Go cache paths.

**Open risks:** the full CLI package currently has an unrelated ambient
`TestCoreIdentityBlock_Build` model assertion failure. The configured provider
acceptance run remains required before claiming the R1 gate.

**Next action:** run the full suite in the provider acceptance environment,
then inspect a live graph task for one accepted fact, one terminal fact, and a
valid snapshot.

## 2026-10-09 — R1 User-Approved Ollama Cloud Default

**Roadmap IDs:** R1; R4 remains dependent; R2/R6 Recall work remains parked

- The user approved `ollama/glm-5.3:cloud` as the default across all agent
  profiles and examples while preserving capabilities and a 1,048,576-token
  model-window declaration.
- An isolated Ollama 0.34.4 service at `http://127.0.0.1:11435` completed a
  real cloud-provider graph task with `PRIZM_OK`. Configuration validation and
  doctor passed apart from the pre-existing Remembrance offline warning.

**Evidence:** provider/CLI-focused tests passed on the default-provider commit;
the runtime lifecycle regressions are committed separately as `016a3b6`.

**Next action:** preserve the provider default for the next live graph run and
inspect the durable outbox and snapshot as part of the R1 acceptance record.

## 2026-10-09 — R1 Fan-Out Expiry Fact-First Closure

**Roadmap IDs:** R1; R4 remains dependent; R2/R6 Recall work remains parked

- Expired fan-out recovery persists every trusted child terminal fact before
  waking the parent; NATS notification cannot interleave a partial recovery.
- Join expiry consumes all durable child outcomes first, then marks only
  missing siblings timed out in the same recovery pass.

**Evidence:** focused join deadline and terminal-before-expiry tests pass;
full build, vet, and test passed on combined head before this narrow repair.

**Next action:** add the live three-child accepted-worker-crash acceptance
trace before starting another paid provider run.

**Scanner contract:** timeout synthesis is owned by the running graph-worker
scanner in `prizm serve`; direct manual `Resume` consumes durable outcomes but
does not synthesize missing command outcomes.

## 2026-10-09 — R1 Integrated Fan-Out Scanner Recovery

**Roadmap IDs:** R1; R4 remains dependent; R2/R6 Recall work remains parked

- Added an integrated regression using embedded NATS, the shared canonical
  SQLite outbox/event/run database, and a persisted `DurableRuntime`. The real
  parent command produces a three-child `delegation_join`; one child records a
  successful terminal while two simulate worker loss after durable acceptance.
- The graph-worker deadline scanner persists both missing trusted terminal
  facts before invoking the actual durable resume callback. The parent reaches
  a terminal failed state, and its event-derived report contains all three
  fan-out children.
- Re-running the scanner preserves each accepted and terminal event identity
  and does not append duplicate outcomes.

**Evidence:**
`go test ./cmd/prizm-cli -run TestGraphRoleWorkerEmbeddedNATSRecoversPersistedFanoutJoin -count=1`
passed. The full CLI package requires workspace-local `TMP`/`TEMP`; a first
sandbox run without those settings failed existing worktree-safety tests before
the corrected validation run.

**Open risks:** a successful configured-provider graph task remains required
before claiming the complete R1 score gate. Authenticated remote workers remain
R3 scope.

**Next action:** complete the configured-provider acceptance trace, then assess
the remaining R1 gate evidence before starting R3/R4 work.

## 2026-10-09 — R1 Live GLM Approval and Terminal Notification Recovery

**Roadmap IDs:** R1; R4 remains dependent; R2/R6 Recall work remains parked

- Fixed the persistence-before-notification wake seam. A graph worker commits
  trusted outcome bytes before NATS publication, so the subscriber normally
  observes an idempotent insert; a verified terminal notification now resumes
  the parent even when that insert reports the row already exists.
- Added a regression that creates the canonical command, persists accepted and
  terminal worker facts, delivers the exact terminal bytes, and verifies the
  parent resume callback. Untrusted or conflicting bytes still fail before the
  callback.
- Live `ollama/glm-5.3:cloud` run `run_01M4HTQVKXRTHH4VN8PNK29CV0` completed
  planner and developer roles, recorded and exactly approved proposal
  `appr_1791599842692901700`, reconciled the applied file in the isolated
  worktree, and completed tester validation. The allowlisted `go_test_all`
  profile passed with exit code 0. The reviewer then failed closed because its
  response encoded `evidence` as a string rather than an `ArtifactRef` object.
- Clarified the reviewer output prompt with concrete `{kind, uri}` evidence
  objects while retaining strict decoding; no string coercion or authority
  bypass was added.

**Evidence:** focused graph-worker resume tests passed. Full `go build ./...`
and `go vet ./...` passed. The first full `go test ./... -count=1` run passed all
packages except an existing Windows temp-directory cleanup race in
`TestScopedPromptInjectionDoesNotCrossScopeOrCache`; focused rerun evidence is
recorded with the final handoff.

**Open risks:** the live task did not reach a completed reviewer terminal, and
daemon restart recovery plus approval/application were demonstrated in separate
runs rather than one continuous run. Authenticated remote workers remain R3
scope.

**Next action:** rerun the bounded configured-provider task with the clarified
reviewer contract, including a daemon restart while paused for exact approval,
then require a completed terminal report before claiming the R1 gate.

## 2026-10-09 — R1/R4 Remaining Acceptance Handoff

**Roadmap IDs:** R1; R4 remains dependent; the broader 9/10 roadmap gates are
not achieved

- Audited the handoff at `fdc38dc` without changing runtime code. Draft PR #87
  retains its latest verified green Linux, race, Windows, Python, vet, and
  staticcheck evidence.
- Three acceptance items remain: obtain a completed live reviewer result with
  the corrected artifact-evidence prompt; prove one continuous daemon restart
  while paused for exact approval through apply, validation, and terminal
  completion; and repeat the acceptance task in a second independent real
  repository.
- The reviewer and restart checks can be combined in one controlled run. A
  second focused session can cover the independent repository, followed by a
  final evidence and PR-readiness pass. The working estimate is two to three
  focused sessions, with one or two additional fix sessions only if a live
  provider run exposes a concrete defect.

**Evidence:** documentation-only audit of the roadmap, current plan, latest
work-log entry, and repository head. No provider call, build, test, or fresh CI
query was performed in this handoff session.

**Open risks:** a transient resume error other than `ErrRunClaimed` relies on a
later durable wake; this remains P2 hardening. The session estimate applies to
the current R1/R4 acceptance handoff and must not be read as completion of the
full multi-roadmap 9/10 program.

**Next action:** run the corrected reviewer contract and restart-through-
approval recovery as one bounded acceptance trace, then execute the same task
in a second independent repository.
