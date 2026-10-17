# Prizm Plan of Action

## Current Handoff: R1 served acceptance retry exhaustion (2026-10-17)

**Roadmap:** R1 remains incomplete. The isolated served run
`run_772f310af2f018ca180769e28d` used Prizm's API and the configured
`ollama/glm-5.3:cloud` text provider. Prizm owned orchestration and durable
retry scheduling; Codex/Claude executors were disabled. Its source was built
from local commit `7f925b7f8b3633a59122eeebf0fbab8c593ba69c`, whose tree
`a9a66b6a6ec64f130c319ab125980b50003b0c1d` matches the published PR #91
candidate. The Factory workspace was the isolated clone.

**Evidence:** the exact previously approved request body was submitted
unchanged (SHA-256
`6BD2CA13898A5EDCA61BA80D8681B606CB85C9FCBE09304E01863FF00B0AA845`). The
planner consumed one initial attempt and ten retries, then ended with
`retry_exhausted` after `provider_transient/provider_turn_failed`. The durable
retry record and all three persisted events—
`prism.workflow.multi_agent.role.retry.scheduled`,
`prism.workflow.multi_agent.role.retry.exhausted`, and
`prism.workflow.multi_agent.run.failed`—recorded the allowlisted
`failure_origin=provider_call`, confirming the origin-population fix in the
live path. The terminal report records 11 local iterations and 10 retries.
There was no proposal, apply, configured validation, test outcome, or delivery,
and the planner made no workflow transition. The terminal record and events
remained durable after service shutdown. The service listened on loopback only and is stopped;
the isolated Factory clone is clean.

**Risk/next action:** this run verifies Prizm's retry exhaustion, attribution,
and terminal reporting, but does not prove coding delivery. The safe failure
classification does not explain the underlying provider failure, so do not
infer a provider root cause from it. Diagnose provider availability using
sanitized status only, then run a fresh isolated Prizm acceptance once the
provider can complete a turn. Stop at any proposal for explicit approval.
Keep PR #91 draft and R1 incomplete until proposal, approval, exactly-once
apply, configured validation, review, and final reporting succeed.

## Previous Handoff: R1 retry terminal-cause authority and event provenance (2026-10-16)

**Roadmap:** R1 remains incomplete. On staging-derived branch
`codex/pr91-r1-failure-origin` (base commit
`bff5a7c38cf216df7167c3680e8af88ce57cf34f`), durable role classification now
uses the terminal error as the retry authority. Earlier safe correction facts
remain in runner recovery state, but cannot replace a later typed provider
failure or turn an unknown terminal error into a retryable known failure.
Scheduled and exhausted retry events now include `failure_origin` only when
the class/code/origin tuple passes the existing allowlist; the exhausted event
schema accepts that optional field. No package boundary changed.

**Evidence:** regressions cover a correctable nonmutating tool error followed
by `provider_turn_failed` with `Retry-After`, preservation of the safe earlier
correction diagnostic, unknown terminal errors staying unknown/nonretryable,
and validated failure origin on scheduled/exhausted events. Focused tests,
`go test ./... -count=1`, `go build ./...`, `go vet ./...`, and
`git diff --check` passed. The full suite required loopback-enabled execution
and workspace-local temp paths; the default sandbox run showed socket and
Windows temp-object cleanup denials.

**Risk/next action:** this is retry correctness and observability evidence,
not a successful R1 acceptance. Independently review the local patch, rebuild
Prizm before the next served acceptance, and keep PR #91 draft until the fresh
isolated task completes proposal, approval, apply, configured validation,
review, and final report.

## Current Handoff: R1 durable fan-out failure provenance (2026-10-16)

**Roadmap:** R1 remains incomplete. Branch `codex/pr91-r1-failure-origin` is
based on staging candidate `6aebb33c356851843d745445e8dbf50235864a00`. The
change carries allowlisted failure class/code/origin from the actual tool
executor correction callsite into durable child state, child and join events,
the top-level persisted join failure, and the reference report. The fixed join
diagnosis is `orchestration/delegation_join_failed/fanout_join`; unknown
failures remain unknown and nonretryable. The authoritative DB audit for
`run_9544229c6a3b2158bf82ddf6e6` shows planner failure after three visits with
`tool/tool_execution_error`, zero developer/reviewer/tester durable visits,
and no persisted child classification/origin despite event-summary lane
failure IDs/statuses.

**Evidence:** focused provenance regressions pass; `go test ./... -count=1`,
`go build ./...`, `go vet ./...`, Staticcheck, and `git diff --check` pass. The
full suite required workspace-local temp paths and loopback access. No live
acceptance task or remote PR update was started.

**Risk/next action:** this is diagnostic coverage, not a successful R1 task.
Review the local diff/commit, then run a fresh isolated Prizm-served acceptance
with Codex disabled and the configured Ollama text provider. Keep PR #91 draft
and R1 incomplete until proposal, approval, apply, configured validation,
fresh review, and final report all succeed.

## Current Handoff: R1 Linux CI fan-out synchronization (2026-10-16)

**Roadmap:** R1 remains incomplete. The current PR #91 head is
`58e8c5d245b9eaec0a944251346ef1ef6c2f5aa1` on `staging`. Its Linux check
failed in `cmd/prizm-cli` at
`TestGraphRoleWorkerEmbeddedNATSRecoversPersistedFanoutJoin`: the test expected
one child success and two deadline outcomes, but observed three deadline
outcomes. The immediate-success child was not synchronized before the test
advanced its fake clock. The test now waits for the child terminal notification
and verifies its durable outbox outcome before advancing time. No runtime
behavior or package boundary changed.

**Evidence:** the focused regression passed 30 consecutive runs with local
loopback sockets enabled; `go test ./cmd/prizm-cli -count=1`,
`go test ./... -count=1`, `go build ./...`, and `go vet ./...` passed. The PR's
other checks at the observed head passed (`test-windows`, `lint-linux`, and
`test-python`); `test-linux` failed on the race described above. This source
fix has not been pushed, so those checks are not evidence about the fixed
commit. The Factory clone stayed unchanged and no new acceptance task was
submitted.

**Risk/next action:** the prior served R1 run
`run_9544229c6a3b2158bf82ddf6e6` failed before proposal, apply, configured
validation, or final review. The authoritative durable DB record shows planner
failure after three visits with `tool/tool_execution_error`; developer,
reviewer, and tester have zero durable visits. The separate event summary
contained lane failure IDs/statuses, but the DB did not classify or persist
child failure origin. Do not describe this run as an implementation
`unknown/unknown` plus reviewer `invalid_field_value` failure. Keep raw errors
unknown/nonretryable and persist only allowlisted diagnoses from trusted
boundaries. After review, run a fresh isolated acceptance through Prizm's
served API with Codex disabled and the configured Ollama text provider. Do not
claim R1 complete based on local checks.

## Current Handoff: R1 actionable recovery and reviewer handoff (2026-10-16)

**Roadmap:** R1 remains incomplete. Starting from PR-local commit
`110dd3d2e7c3b744f342953209b352d907291f4e`, branch
`codex/r1-actionable-corrections` adds fixed allowlisted recovery feedback for
typed `invalid_proposal_diff` failures and clarifies the reviewer handoff shape
based on its decision. The preceding feedback truncation fix remains an
independent improvement; served run `run_92f076e7f909e9c686c15d785d` does not
prove it caused that run's failures. No package boundary changed.

**Evidence:** durable recovery now persists only the fixed safe correction for
`invalid_proposal_diff`; focused Resume coverage checks delivery and confirms
raw patch/error canaries do not enter durable state. Reviewer guidance and
tests cover required non-null object/string handoff for `changes_requested`
and omitted/null handoff for `approved`. A CLI backend regression covers a
text `tool_request` envelope parsed and executed through Prizm's injected
executor. This proves a provider/tool path, not a successful served task.
Ollama was unavailable, so targeted source inspection used the documented
narrow fallback. The earlier served run dispatched three lanes and completed
research, then reviewer `invalid_field_type` and developer
`invalid_proposal_diff` exhausted their retry caps; fan-in failed, no proposal,
apply, or validation occurred, the dashboard emitted a failure event, and the
isolated clone remained unchanged.

**Next action:** complete targeted and full source gates, then rerun the same
isolated R1 gates and verify fresh review, proposal/apply/validation, and a
terminal success report. Keep PR #91 unpushed until reviewed.

## Current Handoff: R1 fan-out retry diagnostics (2026-10-16)

**Roadmap:** R1 remains incomplete. The PR #91 local candidate branch
`codex/pr91-prizm-end-to-end` is based on checkpoint
`d83c686bb0ea18ff1d13dd929de84313f274cd7c` and
contains the exact child/task identity guard and trusted approved-tree receipt
checks. This session adds bounded schema-owned correction metadata to each
scheduled fan-out retry and its retry event: correction field, expected type,
allowed values, and the fixed reason `retry_scheduled`. The retry history keeps
at most 16 entries and never stores raw model output or prompts. Commit
`dede161257e6430f79085f6b59e4d3b4c3f37998` contains this update. No package
boundary changed.

**Evidence:** the focused command identity, trusted-tree basis, and new
fan-out diagnostic tests pass; `go test ./internal/event -count=1`,
`go build ./...`, `go vet ./...`, and `git diff --check` pass. The full
multi-agent suite and focused CLI proposal/reconciliation tests only failed in
`testing.TempDir` cleanup when Windows denied unlinking test-created Git object
files, even with `TEMP`, `TMP`, and `GOTMPDIR` under the workspace. The build
exited 0 with a module download stat-cache write warning. The Ollama scout could
not connect to localhost; targeted inspection followed the documented fallback.
GitHub API access was unavailable, so remote PR state was not refreshed. No
provider task, live run, service, or remote PR was accessed.

**Next action:** coordinator review, then a separately authorized fresh served
R1 acceptance. The full R1 acceptance and roadmap 9/10 gate remain open.

## Current Handoff: R1 validation and terminal-state hardening (2026-10-15)

**Roadmap:** R1 remains incomplete. The source-equivalent scratch branch is
based on `d936950f355b384c1b094746bb9b45eae78f5bae`. Nonzero validation results
now carry a bounded sanitized diagnostic from at most the last 32 KiB of each
output stream. The existing Tester handoff carries the summary into the
Developer's structured incoming context; safe event/report evidence also
includes it. The summary omits raw output, prompt/private-reasoning markers,
structured dumps, and common credential forms. No package boundary changed.

Terminal failure persistence now also clears stale waiting metadata, and the
dashboard read model suppresses waiting controls for terminal records, including
legacy records. `DurableRun.Validate` remains permissive for terminal records
with old waiting metadata to preserve compatibility. Focused denial and
dashboard regressions passed. The first full package run encountered Windows
cleanup denials for temporary Git objects; rerunning with elevated access and
Go/test temp paths under `D:\_projects_\prism\.tmp` passed the full multiagent
package suite and uncached repository-wide tests. Build, vet, Staticcheck, and
`git diff --check` also passed. The local Ollama
context scout was unavailable because the localhost socket was denied; targeted
file discovery and inspection followed the documented fallback. No live run,
pending proposal, service, or remote PR was accessed.

**Next action:** coordinator review of the local implementation and a fresh,
separately authorized R1 served acceptance. R1 remains open; do not resume prior
terminal runs or interact with unapproved proposals.

## Current Handoff: R1 reserved-final schema recovery (2026-10-15)

**Roadmap:** R1 remains incomplete. On the source-equivalent worktree based at
`3eb34564c94a34dd0ecc3d6563a3efd12651980d`, an isolated real-provider run was
durably accepted and the planner completed, but Developer terminated at attempt
4 with recovery 3/10 after a completed `search_files` call. The failure path was
the reserved-final guard in `internal/subagent/runner.go`: parser-classified
schema errors returned as plain errors before the later correction handling,
so the durable role classifier marked them nonretryable. The correction now
preserves those two allowlisted categories (`invalid_final_schema` and
`malformed_tool_input`) as typed safe failures, which schedule one durable role
visit under the existing recovery budget. No raw model output or private
reasoning is stored or reported. No proposal, approval, validation, or mutation
was produced by the failed run; it remains terminal and must not be resumed.

The four-file source diff is under review. Focused tests, the uncached full Go
suite, build, vet, Staticcheck, and `git diff --check` passed on the exact
worktree; the sandbox-only loopback and temp-cleanup failures were resolved by
the authorized elevated run with cache/temp scoped under `D:\_projects_\prism\.tmp\r1g`.
No provider default, approval rule, package boundary, or worktree behavior was
changed. No commit, push, or service restart occurred.

**Next action:** review the exact source diff and keep R1 acceptance open until
a separately authorized fresh served run reaches proposal, approval, apply,
validation, fresh review, and final report. Do not reuse or resume the terminal
run.

## Current Handoff: R1 trusted-tree and parallel child-recovery integration (2026-10-15)

**Roadmap:** R1 remains incomplete. The `codex/r1-owned-worktree-recovery` branch at `215d4904a8ca731216251eb01b00b353f7f9991f` was integrated with local staging checkpoint `07bfa94aa9a4ed45f12c94f921106853296a319f` (parallel child recovery WIP `72766bf` plus staging documentation `b935356`) in a local two-parent merge that resolved 15 conflicts. It preserves the approval-bound trusted post-apply tree basis and exact `ParentTask` identity checks while integrating staging's per-child retry, fanout adoption, worker drain, validation-profile fingerprint, and structured-output behavior. Focused tests passed for `internal/workflow/multiagent`, `internal/gitx`, `internal/approval`, `internal/tool`, `internal/mutation`, and `cmd/prizm-cli`. On the exact R1 worktree, `go build ./...`, `go vet ./...`, `go test -count=1 ./...`, `staticcheck ./...`, and `git diff --cached --check` all passed with exit code 0. The test/cache paths were scoped to the short workspace-local `D:\_projects_\prism\.tmp\r1g` root.

The preserved R1 run `run_54b6a3649a7ab3e9cadd1301d0` remains terminal and immutable after one approved apply, validator result 18 passed/1 failed (missing `BLOCKED` import), ten patch-validation recovery attempts, one delivered failure report/notice, and no fresh Reviewer. Do not resume, resubmit, reuse its approval, or edit its applied tree. R1 acceptance remains open. No live service or PR state was changed by this reconciliation.

FW-P1 (FW-002/003/004) remains a separate served-proof gate, and Recall remains untouched. Local source validation does not establish either served acceptance.

**Next action:** hand the verified local merge head and parent-delta review to the coordinator for guarded PR #91 publication/review. R1 still needs a separately authorized fresh served acceptance.
## Previous handoff: R1 parent-task propagation and proof-v4 recovery (2026-10-14)

PR #91 remains a draft at head `dda3c3118c6b65be4ec023e63648155483f34c08` on `codex/r1-owned-worktree-recovery`, based on `staging`. The reviewed R1 change preserves original parent-task requirements through delegated role requests and prompts, with legacy nil payload compatibility. Local build, vet, full test, Staticcheck (277 test-inclusive package patterns), and diff checks passed before these documentation-only updates. The separate FW-P1 implementation remains outside this R1 checkpoint.

The current real Factory run `run_54b6a3649a7ab3e9cadd1301d0` completed three delegated lanes, retained the reviewed sole proposal across restart, and applied it once. The configured `factory_pytest_selected_recovery_v1` validator exited 1 after 18 passed and 1 failed because the blocked-resume test referenced `BLOCKED` without importing it from `production.evidence`. The corrective Developer visit exhausted ten patch-validation recovery attempts, and the run failed at 2026-10-14T21:09:12.7346034Z (revision 584). Prizm persisted the failure report and delivered exactly one terminal notification; no fresh Reviewer ran. The applied patch remains in its isolated worktree, and no second application is recorded.

The run is terminal and immutable: do not resume it, resubmit it, modify its applied tree, or reuse its approval. Safe terminal metadata did not establish a more specific cause for the final patch-validation recovery exhaustion. R1 acceptance and the roadmap 9/10 gate remain open; a new acceptance requires a reviewed correction, configured validation, a fresh Reviewer, a final report, and exactly one terminal notice. PR #91 remains draft. FW-P1 is separate.

**Next action:** Fix serial approved corrections: current PlanPatch requires a clean worktree after ApplyPlannedPatch leaves approved changes uncommitted; use a durable trusted tree basis and retain approval/out-of-band change checks. This source defect is not attributed to individual hidden retry causes.

This is the current execution plan. It is updated at the end of every project
work session and must cite the active [roadmap](../architecture/PRIZM_ROADMAP.md)
items.

> Prizm is source-available under an all-rights-reserved [license](../../LICENSE)
> and is preview-stage. This plan does not change approval or policy requirements.

## Current Handoff (2026-10-14)

**Active roadmap IDs:** R1 remains the platform priority. FW-001 contract groundwork is complete in its separate review artifact; FW-P1 runtime work is separate from this R1 checkpoint, and FW-P2 remains gated on R1 acceptance and FW-G2.

PR [#91](https://github.com/emaharmony/prizm/pull/91) remains an open draft at head `dda3c3118c6b65be4ec023e63648155483f34c08`. The current run `run_54b6a3649a7ab3e9cadd1301d0` completed three delegated lanes and applied the approved patch once. Its configured test failed with 18 passed and 1 failed because the generated test omitted the `BLOCKED` import; the corrective Developer visit exhausted ten patch-validation recovery attempts, and the run failed at revision 584. Prizm persisted one failure report and delivered exactly one terminal notification; no fresh Reviewer ran. R1 acceptance and the roadmap 9/10 gate remain open, and the run is immutable.

The local R1 source gates passed before this plan-only correction. FW-001 adds only pure intention/candidate contracts, structural lifecycle validation, disabled-by-default `freewill` flags, and ADR 0003 documentation; its config flags have no runtime consumer and do not enable scheduling or execution. PR [#92](https://github.com/emaharmony/prizm/pull/92) remains separate. Recall is a separate repository and was not accessed in this work.

## Previous R1 delivery plan (retained for context)

Recall and R6 reconciliation are intentionally parked for a later stage. The
next slice consolidates graph and NATS action delivery behind one typed command
intake and a durable SQLite outbox. It makes delegation acceptance, progress,
deadlines, and terminal outcomes recoverable and inspectable without changing
the compatibility `workflow/v2` path.

| Order | Work | Done when |
| --- | --- | --- |
| 1 | Define one typed command and outcome contract. | Commands carry correlation, causation, idempotency key, deadline, run, and delegation identity; facts distinguish accepted, progress, succeeded, failed, timed out, and rejected outcomes. |
| 2 | Persist delivery through a SQLite outbox. | A graph transition records its command and state atomically before publish; restart and duplicate delivery preserve one effect and one terminal outcome. |
| 3 | Add delegation acknowledgements and deadline recovery. | A worker accepts or rejects explicitly, emits progress and one terminal result, and an expired or lost worker produces a correlated terminal failure. |
| 4 | Preserve compatibility and compose one report. | `workflow/v2` remains supported; graph and NATS actions use the same intake, and an event-derived report explains each command's disposition. |
| 5 | Prove deterministic delivery behavior. | Embedded NATS and fake-worker tests cover acceptance, duplicate delivery, restart, deadline expiry, lost worker, terminal failure, and correlation continuity. |

## Follow-on R1 Work

After the approval slice passes, unify the graph and NATS action paths behind one
typed intake and durable outbox. Define command, acceptance, progress, and
terminal-outcome events with idempotency keys and correlation IDs. Then add
delegation acknowledgement, worker deadlines, fan-out/fan-in, and event-derived
task/review reports.

## Session Handoff

### Cross-cutting runtime budget follow-up (2026-10-11)

The current no-default-cap request is being implemented across active runtime
entry points alongside the R1 work. The local autopatch model worker now sends
`MaxTokens: 0`, and the CLI text/native tool loops retain their two-minute
context deadlines while running without fixed iteration counts. V2 workflow
defaults and graph visualization already use the unlimited sentinel from the
active change set. The legacy wake loop remains capped by phase counts because
those counts currently drive phase transitions; remove them only after a
semantic completion contract is established. Anthropic requires a positive
`max_tokens` request field, so uncapped Prizm requests map to a 64,000-token
provider allowance; this is separate from Prizm run and workflow budgets.

| Field | Current value |
| --- | --- |
| Active roadmap IDs | R1 (event contract and delegation), with R4 dependent on the completed R1 contract; R2/R6 Recall work is parked. |
| Reviewed baseline | `origin/staging` at `b85f46a` includes the merged R1/R2 staging history; no 9/10 gate is met. |
| Current state | R1 now has a canonical typed command/outcome envelope and SQLite outbox with accept-once idempotency, leases, bounded publish retry, restart replay, deadline failure, and correlated reports. `workflow/v2` remains on its compatible durable path. The canonical graph now publishes a command containing the exact prepared role request and trusted run, role, execution, workspace, delegation, delivery, correlation, and deadline identity. `prizm serve` can compose a graph-owned NATS worker behind `PRIZM_GRAPH_ROLE_DELEGATION=1`; NATS is only a notification channel, and the worker requires the wire command to match the canonical SQLite outbox row byte-for-byte before executing through the governed `AgentRoleRunner`. Mutation proposals now support one bounded atomic multi-file unified diff under one exact approval. The approval persists the trusted base SHA/tree, expected tree, original patch hash, and all canonical paths; proposal and apply both enforce configured write roots, and restart recovery classifies the complete worktree tree as applied, not applied, or ambiguous. Governed Git patch interpretation uses `--recount` consistently for path inspection, precheck, expected-tree calculation, apply, rollback, and direction checks so incorrect model-generated hunk counts do not invalidate otherwise complete diffs; the exact original bytes remain approval-bound and no reject, three-way, unsafe-path, or content normalization mode is enabled. Governed developers expose only the atomic proposal mutation tool and receive the trusted base SHA from the runtime. The governed worker decodes proposal and typed-final envelopes as one complete JSON object, independent of member order and whitespace; atomic proposal input requires exactly non-empty string `patch` and `base_sha` fields, and embedded/fenced/multiple objects fail closed. Legacy embedded-envelope parsing remains compatible only for existing `workflow/v2` callers. A supervised wall-clock scanner wakes expired graph delegations autonomously; local resume serialization plus the durable run claimer prevents competing wake transitions. |
| PR handoff | R1 PR #89 is merged to staging. PR #86 on `codex/r6-memory-reconciliation` is ready for review after resolving the staging conflicts; Recall PR #9 has merged, while deployed-service and public scoped-route acceptance remains open. This does not claim end-to-end Recall service acceptance or completion of the broader R1 gate. |
| Next implementation decision | Live run `run_01M4M68BG82B8PFYHY1TGKNCNE` used the reviewed recount binary, unchanged registered `software-delivery-v5` graph, exact task packet, and clean `6de4579` acceptance clone. It reached canonical `apply_patch_proposal`; recount accepted the diff structure, but the indexed precheck rejected the proposed context at `internal/provider/ollama/chat.go:160` because it did not apply to that exact source. The run failed before approval or mutation, and both the isolated run worktree and source clone remain clean. Do not retry or broaden the task until the stale or incorrect patch-context mechanism is diagnosed from public evidence and a bounded repair is independently reviewed. |
| Estimated focused sessions | The remaining work is blocked on configured-provider output behavior. Once that external condition clears, budget two focused acceptance sessions: one continuous Prizm restart-through-approval/reviewer trace and one independent Roblox Factory preflight-doctor trace, followed by final evidence and PR-readiness verification. This does not complete the broader roadmap or any 9/10 score gate. |
| Evidence required before advancing | Atomic proposal tests cover add/modify/delete, malformed and oversized patches, base drift, `.git` case aliases, unsupported symlink mode, narrow write roots, no mutation before approval, one approval with multiple artifacts, exact-tree apply/recovery, unchanged state on failure, and exact approved-patch evidence. Patch compatibility tests prove trailing-newline and byte/hash preservation through the real proposal tool, wrong-count multi-file plan/apply/reverse/direction behavior with the expected tree and exact paths, and rejection of structurally malformed input. Parser regressions cover pretty and reordered canonical envelopes, patch strings containing braces, exact byte round-trip, typed-final ordering, malformed and extra fields, embedded/prose/fenced/multiple objects, and role JSON isolation. The real deterministic composition test covers pause, restart, grant, apply, post-apply validation, and terminal completion. Fresh `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check` pass with workspace-local caches and temporary storage after the recount change. The live recount trial proves the canonical proposal reached Git semantic precheck without corrupt-patch failure and that a non-applying context fails closed without approval or mutation. Live provider completion, a completed reviewer outcome, the continuous live restart trace, and the independent real-repository acceptance trace remain required before claiming the R1 gate. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)

### Shared-session addendum (2026-10-11)

| Field | Current value |
| --- | --- |
| Active roadmap IDs | R1 autonomous execution behavior; R1 durable delegation/outbox behavior; R4 remains dependent. |
| Completed in this session | V36 wake execution now advances on standalone phase completion signals followed by a final JSON response, with signal parsing before JSON, rejection of signal-plus-tool turns, cancellation checks, and commit/push enforcement retained through its phase. The compatible `workflow/v2` report path now removes private `<think>` blocks before accepting/persisting report content and again at the shared Discord/artifact formatter; incomplete blocks fail closed, and fenced patch/code data is preserved. No package boundary changed. |
| Evidence | Wake focused signal/parser tests pass; its focused CLI test run and `git diff --check` pass. Privacy canary and focused report-format/artifact tests pass via `go test ./internal/workflow/v2 -run 'TestReportOutputAndArtifactExcludePrivateReasoning|TestFinalReport|TestWriteReportArtifact' -count=1` with workspace-local caches. |
| Open risks | The broader CLI package suite remains blocked by unrelated approval/workspace-scope and filesystem ACL failures. Wake completion signals are model-declared; branch-mutation and commit/push gates remain enforced, but an end-to-end fake-provider loop test is still absent. |
| Next action | Coordinator reviews both implementation diffs and decides whether broader validation can run under the current temp-root permissions. |

### Public-output privacy and wake-engine follow-up (2026-10-11)

| Field | Current value |
| --- | --- |
| Active roadmap IDs | R1 autonomous execution behavior; R1 durable delegation/outbox behavior; R4 dependent. |
| Public-output boundaries closed | `internal/run` now derives `output.md` and `agent.completed.summary` from sanitized public response parsing; structured tool inputs remain unchanged and raw tool-request JSON is not copied into reports. `internal/workflow.ExecuteDelegate` sanitizes returned output and `agent.completed` payloads. API invocation sanitizes final content before citation checks and again before session persistence and completion events. `cmd serve` sanitizes resolved replies before send/session/TTS/commitment and extraction events. `internal/stage.LLMStage` sanitizes final result data while retaining raw `RunContext` output for structured tool/delegation parsing. Existing V2 report formatting/artifact sanitization remains in place. |
| Wake and V2 safeguards | Wake parsing validates the standalone completion signal and strict final JSON before sanitizing preamble and decoded prose, preserving literal `<think>` text in unified-diff hunks and rejecting malformed/private-only output. V2 rejects invalid budget values below -1 before execution. |
| Evidence | Focused canaries pass in `internal/run`, `internal/workflow`, `internal/api`, `cmd/prizm-cli` serve response, and `internal/stage` LLM stage. The earlier V2 report/artifact canary passes. Wake focused tests and the full `internal/workflow/v2` package pass. `git diff --check` passes across the changed paths. The full CLI package still has unrelated workspace/ACL and path failures. |
| Open risks | The full CLI suite remains unverified under current filesystem restrictions; no new package was introduced. |
| Next action | Coordinator reviews the combined privacy and wake-engine diff and decides whether broader CLI validation can run in a less restricted environment. |

### Stage persistence and memory-capture addendum (2026-10-11)

`PersistenceStage` now writes only sanitized public output to `output.md`; incomplete or structured tool output removes a stale prior artifact, and removal failure returns a generic stage failure. `RemembranceStage` captures only sanitized final text and skips incomplete or tool-request output. Raw `RunContext` remains unchanged for structured parsing. Canary coverage includes secret removal, fenced diff preservation, stale-artifact removal, and unchanged raw context.

**Evidence:** `go test ./internal/stage -count=1`, focused `TestSanitizeServeResponse` tests, `go build ./...`, `go vet ./...`, and `git diff --check` pass using workspace-local Go caches. No package was added.

**Next action:** coordinator review of the final privacy-boundary diff; broader CLI failures remain unrelated workspace/ACL and path failures.

### Final persistence replacement refinement (2026-10-11)

`PersistenceStage` now reconciles `output.md` even when `LLMResponse` is empty, removing stale output for empty or unsafe content. Safe output is replaced through a same-directory temporary file and rename; temp creation, write, close, or rename failure removes the prior artifact before returning a generic stage failure. Full stage tests pass, including empty-response stale-artifact and injected temp-creation failure cases. The broader CLI suite remains limited by unrelated workspace, ACL, and path failures.

### Patch grounding and reviewer-output security addendum (2026-10-11)

**Roadmap IDs:** R1 (governed proposals, exact patch grounding, and review evidence); R8 (autopatch/self-improvement remains dependent on R1 approval evidence).

- `autopatch.extractDiff` now excludes private fenced diff blocks from proposed public patches.
- Claude verdict parsing ignores verdicts inside private reasoning and fails closed for malformed, nested, or unmatched `<think>` markers.
- `gitx.PatchSourceRanges` now handles `--- ` lines inside hunks while preserving old-side source grounding for add/delete operations.

**Evidence:** focused autopatch privacy tests pass; the full autopatch suite is blocked by Windows temp-root ACL failures. The full `claudeworker` package passes. Full `gitx` and `subagent`, plus focused proposal/graph tests, pass with workspace-local `TMP`/`TEMP`. No secrets or user data were recorded.

**Open risks:** the full autopatch package gate remains unverified because of temp-root ACL restrictions; this is an environment limitation, not a passing suite.

**Next action:** coordinator review of the three security diffs and decide whether the autopatch suite can be rerun with an allowed temp root.

### Final autopatch parser review (2026-10-11)

Autopatch diff extraction also rejects malformed or nested think-like tags before selecting a diff. Focused canaries pass. Independent review found no remaining P1/P2 issues; the local changes are ready for commit review.

### Unlimited-default validation handoff (2026-10-11)

**Roadmap IDs:** R1 runtime execution behavior; R8 autopatch/self-improvement depends on the R1 governed execution path.

A full `go test ./...` attempt exposed stale CLI manifest expectations (60k instead of `UnlimitedTokens`) and two shipped template scenarios that still expected the removed default iteration caps. The CLI assertion now follows the unlimited default. The shipped templates retain explicit `-1` unlimited defaults, and the scenarios now distinguish those defaults from separate opt-in traversal-exhaustion cases. Full `internal/workflow/multiagent` tests pass.

**Next action:** rerun `go test ./...` and record any remaining failures before final validation.

### Full-suite validation resolved (2026-10-11)

The pending full-suite rerun is complete: `go test ./...` exited 0 using workspace-local `GOCACHE`, `GOMODCACHE`, `TMP`, and `TEMP`; 88 packages passed, 5 had no test files, and none failed. This resolves the prior pending validation note. `go build ./...` and `go vet ./...` also passed earlier with workspace-local caches.

**Next action:** proceed with coordinator review and commit preparation for the validated local changes.

### Staticcheck U1000 cleanup handoff (2026-10-11)

**Roadmap IDs:** R1 runtime execution behavior and governed proposal handling; R8 remains dependent on R1 approval and provenance.

Removed two unreferenced compatibility helpers flagged by Linux CI: the singular proposal-artifact wrapper and the superseded unphased prerequisite-budget check. The plural artifact resolver and phased budget checks remain in place, including the no-cap behavior.

**Evidence:** local `staticcheck ./...` (2026.1/v0.7.0), `go vet ./...`, `go build ./...`, `go test ./internal/subagent ./cmd/prizm-cli -count=1`, and `git diff --check` pass. The default Staticcheck cache was inaccessible, so its cache was redirected to a workspace-local directory.

**Open risks:** the CI action resolves its `latest` Staticcheck version on Linux; local validation used v0.7.0 on Windows.

**Next action:** coordinator review, then confirm PR #87's Linux lint job against this commit.

### Local rebuild and test serve (2026-10-11)

**Roadmap IDs:** R1 durable event/outbox runtime and governed execution; R8 remains dependent on R1 approval and provenance evidence.

Built the CLI from checkout `codex/r1-event-outbox` at `c30ca7f` with the existing workspace-local Go caches. Replaced the verified repository-owned service and started the rebuilt executable at PID `6700` from `D:\_projects_\prism\.tmp-prizm-c30ca7f.exe`. The loopback health endpoint at `http://127.0.0.1:8321/health` and API status at `http://127.0.0.1:8322/api/v1/status` both return HTTP 200; the API reports `running` with six agents. Startup diagnostics are in `logs/prizm-local-c30ca7f.out.log` and `logs/prizm-local-c30ca7f.err.log`.

**Open risks:** the process is healthy, but Discord is not ready because the host denied outbound gateway access; Blender MCP initialization also failed, and context compression fell back after Ollama returned 410. The API is bound to loopback with no configured auth token, as expected by the local configuration. No external service or provider completion is claimed.

**Next action:** test the R1 flows through the local API/dashboard. Recheck Discord and Blender MCP only in an environment where their required services are reachable.

### Live real-provider task smoke (2026-10-11)

The requested read-only task did not reach a provider. The service was healthy before and after (`/health` returned `ok`; `/api/v1/status` returned `running`) under unchanged PID 6700 (`.tmp-prizm-c30ca7f.exe`, started 2026-10-11 21:20:09 -04). `POST /api/v1/agents/scout/invoke` returned HTTP 403, `agent "scout" is not invocable via API`; all six configured agents report `invocable_via_api=false`. No invocation/run ID or event trace was created, and status task counters remained unchanged at one pre-existing failed task. `/api/v1/multiagent/definitions` is empty. The alternate workflow-start API launches general project work, so it was not used for a read-only request. Effective task/provider budgets are not exposed by the status/config API response.

**R1/R8 evidence and risk:** this confirms the API refuses before provider execution, but it does not confirm real-provider completion or task-loss recovery. There was no accepted task to silently disappear; no retry, timeout, or termination occurred. R1/R8 live execution confidence remains unverified.

**Next action:** configure an explicit read-only test entry point (agent API opt-in or registered read-only workflow) and rerun with durable task/run ID and event inspection. Keep the opt-in scope limited to the test agent.

### Follow-up: isolated live provider lifecycle (2026-10-11)

A separate, bounded `prizm graph run` used the current `prizm.yaml` provider and an isolated temporary Git workspace. The definition compiled with ceilings of 2 transitions, 2,000 tokens, 2 local iterations, 1 global retry ceiling (0 retries for this role), 5 minutes total, and 4 minutes for the role. It persisted run ID `run_01M4PWAZFPZPDS82VJF95VAGE6` before provider execution. The provider returned HTTP 429 on model turn 1; Prizm recorded `run.started` (`evt_01M4PWAZV5WS1F6R86817TXCW4`), `role.entered` (`evt_01M4PWAZVARRSG0XM7ENV52VZJ`), and terminal `run.failed` (`evt_01M4PWB6AQYX862HTDA6734B78`) with the same reason. Status and report both show terminal `failed`; no provider output or file-tool call completed, and no retry or timeout occurred.

**Updated R1/R8 evidence:** the accepted CLI graph task was not silently lost; it became a durable, queryable terminal failure before the five-minute run cap. This still does not demonstrate successful real-provider work or full recovery. The live service remained unchanged and healthy. Effective model usage is unavailable because the provider rejected the request before returning usage.

**Next action:** retry one bounded read-only run after the provider stops returning HTTP 429; then inspect success/failure events and workspace cleanliness before claiming provider work completion.

### Follow-up: isolated local Ollama lifecycle (2026-10-11)

The local installed `qwen3.5:9b` was available and used through a temporary minimal config without changing the running service or its configuration. A one-role reviewer graph allowed only `read_file`; token and iteration budgets were explicitly unlimited, with two transitions, one global retry ceiling but zero retries on the role, a 3-minute run deadline, and a 2-minute role deadline. Run `run_01M4PWEE6MX1FCE76G3WYANSKF` was persisted before provider execution. The model emitted a `read_file` request, but tool execution failed; the CLI log exposed only `execution_error=true`, and the canonical events do not preserve the underlying tool error. The later reviewer response did not satisfy the strict output contract; the durable terminal reason is `multiagent: invalid structured output for role "reviewer": invalid character 'T' looking for beginning of value`.

The run has `run.started`, `role.entered`, and terminal `run.failed` events (`evt_01M4PWEEE60V25QWRV6YETY9T8`, `evt_01M4PWEEEAEPPE2629AAPSTPY3`, `evt_01M4PWF669HAN44YZYBKJFDHZN`). No retry or timeout occurred. Runtime logs show three model turns, while the durable failed role state reports zero local iterations and no token usage. The main service remained PID 6700 and healthy with unchanged task counts.

**Updated R1/R8 evidence:** the graph task remained observable and reached an explicit terminal failure, but the real provider task did not complete. Failed-run observability loses useful tool-error and usage/iteration details, so this evidence does not establish recovery or successful work.

**Next action:** inspect the read-file tool request contract and preserve tool failure and partial usage/iteration data in the durable failed-role record. Rerun one bounded read-only task only after those failure causes are understood; do not infer success from provider output alone.

### Final follow-up: successful isolated local-provider response (2026-10-11)

A third isolated graph run used local Ollama `qwen3.5:9b` with a reviewer role configured with `allowedTools: []`, one visit, and zero role retries. The graph's token, iteration, and global retry caps were omitted (reported by `graph inspect` as unlimited); transitions were capped at 2, graph wall time at 3 minutes, and role time at 2 minutes. Run `run_01M4PWJHYXC7SH5E0N1PB47ZNT` completed in one model turn with 1,689 prompt and 37 completion tokens. Durable status/report give `completed`, `review_approved`, and terminal reason `review approved`; events include role.completed (`evt_01M4PWJMQSYWHKPDE7GD5G72YX`) and run.completed (`evt_01M4PWJMR2JWC17K3F5PH17ZWH`). The process returned exit code 0, with no retry, timeout, or cancellation.

**R1/R8 evidence and limits:** this confirms one real local-provider graph task can return a valid structured final and reach a durable successful terminal state. It had no tools enabled, so it does not demonstrate README/file work, tool execution, process-crash recovery, or full project execution. Earlier cloud and local read-file attempts remain terminal failures (cloud 429; local read-file execution error followed by invalid structured output). The main service remained PID 6700 at `.tmp-prizm-c30ca7f.exe`, same start time; `/health` remained `ok`, `/api/v1/status` remained `running`, and both loopback ports 8321 and 8322 remained listening under PID 6700. The short-lived graph CLI exited; no graph process remained. The isolated source workspace stayed clean. A temporary process enumeration API was access denied, but direct process lookup and netstat verified the main PID and its listeners.

**Next action:** fix and persist read-file tool errors and failed-turn usage/iteration metrics, then run a bounded read-only tool acceptance test. Keep crash-recovery evidence separate; this smoke did not kill or restart any process.

### Evidence correction: read-file retry evidence belongs to separate runs (2026-10-11)

The prior local Ollama lifecycle note describes only run `run_01M4PWEE6MX1FCE76G3WYANSKF`. Its durable report still records the later strict-reviewer JSON decode failure and zero saved failed-attempt usage/iterations; its tool error detail was not durably retained. The successful read-file retry evidence came from a separate investigator rerun, `run_01M4PWTKBPE493XNW2TFWN0RQA`: turn 1 supplied invalid `start_line=0` and received `parameter "start_line" must be a positive integer`; turn 2 successfully read 1,263 bytes; final structured output then failed. This demonstrates one tool retry succeeding in that distinct run, not completion of the original run or a successful graph task. Other investigator runs either reached context deadline after repeated reads (`run_01M4PWX2PA75F882QPZQ351ZAE`) or completed without calling read_file (`run_01M4PWWN4XSFYQKDHVHYF9ZPNT`); neither is successful read-file evidence. The successful no-tool run above remains the only successful task in this smoke series.

## R1 handoff: durable retries and accepted coding task evidence (2026-10-12)

The shipped `software-delivery` template/reference defaults no longer impose implicit aggregate retry or 30-minute task-duration ceilings; positive limits explicitly authored by a workflow remain authoritative. A new immutable template version was registered for acceptance. Strict run ingress now rejects unknown task keys and blank descriptions before persistence, and terminal report projection uses the durable task description and tester validation evidence. Current default-parallel `go test ./... -count=1`, `go vet ./...`, `go build ./...`, and all seven dashboard JavaScript tests pass.

A real `glm-5.3:cloud` run (`run_ab78d1b310fc9568fefc209851`) completed planner, developer, tester, and reviewer. It applied only the approved two-row 401/403 retry classification change in the isolated worktree, passed `go test ./...` there (exit 0), and received `review_approved`. Restart with approval pending preserved the proposal identity; repeating the run-scoped grant was an idempotent no-op. The rebuilt report and single completion notification are queryable. A visual browser screenshot remains unverified in this environment, and this one success does not satisfy the roadmap's multi-task R1 gate.

**Next action:** coordinator review the full feature diff and acceptance evidence; only then authorize branch/commit/push/PR preparation. Continue remaining R1 scenarios and record browser/UI validation when a supported browser surface is available.

### R1 acceptance status correction and verification (2026-10-12)

- The accepted live task remains `run_ab78d1b310fc9568fefc209851`, with completed status, `review_approved`, the approved two authentication test rows, and a persisted successful `go test ./...` result from the owned task worktree. The run-scoped duplicate grant remains a no-op; the persisted report was reconstructed from durable task/validation evidence without changing the terminal event history. Exactly one dashboard completion event is in the outbox.
- Final current-tree checks on branch `codex/durable-task-retries`: default-parallel `go test ./... -count=1`, `go build ./...`, `go vet ./...`, and all seven dashboard JS tests passed. A credential projection test covers separate `--token` values, `--password=`, `api_key=`, and Authorization/Bearer arguments without retaining their values.
- The suite exposed an existing Windows symlink containment failure reproduced on the clean `6de4579` source snapshot. `internal/safety/path.go` now requires a resolvable root, reconstructs genuinely missing descendant components in forward order, and fails closed when an existing parent cannot be resolved (Windows returned Access is denied for the symlink). The symlink-escape test, missing-root test, nested missing-child path test, and full safety/tool package tests pass.
- Fetched `origin/staging` is `d7f76ef`; its tree hash equals the prior feature HEAD `c30ca7f`, so the new feature branch starts at staging without carrying the old 55-commit history. All feature edits remain uncommitted for coordinator review. Workspace caches, `.tmp/`, and unrelated temporary artifacts are excluded from the proposed commit.
- Headless Chrome successfully captured the served run page at `http://127.0.0.1:20822/multiagent-run.html?run=run_ab78d1b310fc9568fefc209851` into untracked `.tmp/dashboard-proof.png`. The rendered page shows the completed badge, review-approved terminal outcome, and one task-completion timeline entry; its DOM has no retry countdown text. The reference workflow graph is empty in the returned graph snapshot. The run page exposes the latest handoff inspector and the runs list exposes an Open final report link; the successful report fields were verified through the API, but the current screenshot does not show the report opened in the UI. Treat the visual report-opening step as still pending. The child IAB could not be shown from this worker session.

**Next action:** coordinator reviews the final evidence and decides whether the dashboard report-link visibility is sufficient or requires a human UI check before authorizing commit/push/PR. R1 overall and its remaining multi-task roadmap gate stay open.

### Dashboard report link verification update (2026-10-12)

The run list was rendered from `http://127.0.0.1:20822/multiagent-runs.html`. Its row for `run_ab78d1b310fc9568fefc209851` displays `Completed` and an `Open final report` link whose exact target is `/api/v1/multiagent/runs/run_ab78d1b310fc9568fefc209851/report`. The served report was also opened in headless Chrome from that target and captured at `.tmp/dashboard-report.png`; the browser-rendered JSON shows the original objective, `tests_executed: go_test_all: passed (go test ./...)`, `validation_results` status `passed` with exit 0, `test_outcome: passed`, reviewer `review_approved`, and final status `completed`. The run detail screenshot `.tmp/dashboard-proof.png` shows the Completed badge and approved terminal outcome; its DOM contains zero retry-countdown text. The runs list includes one completed row for the target run. This closes the direct browser/report-link acceptance check. Screenshots and DOM captures remain untracked temporary evidence.

**Next action:** coordinator reviews the complete packet and decides on commit/push/PR authorization. R1 overall and its remaining multi-task roadmap gate remain open.

### Commit and PR handoff (2026-10-12)

Feature commit `b798578f5b7ac175b94fed6d5f8036811625e3d8` is pushed on `codex/durable-task-retries` from `origin/staging` `d7f76efbd913b165bd695ff295a051e74cf828c2`. Pull request [#89](https://github.com/emaharmony/prizm/pull/89) targets `staging`; it is open and not a draft. At handoff, CI jobs `lint-linux` and `test-linux` were running, with `test-windows` and `test-python` queued. The tracked worktree is clean; pre-existing caches and temporary directories remain untracked and are excluded. No merge was performed.

**Next action:** CI follow-up on PR #89 resolved the deprecated `Temporary` classifier and three concrete gates; verify the newly pushed head's CI. R1 overall and its additional roadmap scenarios remain open.


### R1 terminal fan-out diagnostics handoff (2026-10-13)

PR #91 remains a draft checkpoint; the real Factory acceptance run `run_9544229c6a3b2158bf82ddf6e6` failed before proposal, apply, configured validation, or final review. Its authoritative durable DB record shows planner failure after three visits with `tool/tool_execution_error`; developer, reviewer, and tester have zero durable visits. The separate event summary contained lane failure IDs/statuses, but the DB did not classify or persist child origin. The R1 review worktree was merged with current staging `31a6ee783691c7a59005d6afc2b064ad73859258`; terminal child failures preserve trusted retry counters and allowlisted failure/schema details through durable join failure, child event, and revalidated report projection. Unknown errors stay unknown and nonretryable.

Next: preserve the immutable failed run and use the bounded reconciliation result to distinguish base-tree, expected-tree, and ambiguous outcomes before any operator-approved recovery. R1 remains incomplete until the same approved task completes configured validation, fresh review, and final report/notification checks. FW-P1 (FW-002/003/004) remains separate follow-on work.

**Current reconciliation diagnostic update (2026-10-13):** the PR #91 review worktree records a fixed reconciliation code and validated observed tree hash before pausing an ambiguous approved apply. Raw errors, paths, and contents are excluded. Focused tests, `go build ./...`, `go vet ./...`, default-parallel `go test ./...`, `staticcheck ./...`, and `git diff --check` pass on the current review worktree. The current live run remains unchanged and unresumed pending rebuilt-service reconciliation evidence.

### R1 live acceptance checkpoint (2026-10-13)

PR #91 remains a draft; R1 acceptance is still open. The isolated Git trust fix binds safe-directory handling to the verified run-owned worktree and approved base without changing global Git configuration. `go build ./...`, `go vet ./...`, full `go test ./...`, `staticcheck ./...`, the focused Git/CLI tests, and `git diff --check` passed on the reconstructed PR worktree. The reviewed source delta is seven Git/CLI source and test paths.

The previously approved run `run_eaa180f0f12345e1d3adb2ceb7` was resumed once through the existing API after trusted preflight proved the exact base and approved worktree binding. Its approved patch applied once: one apply-start event and one apply-success event share the same apply key. The resulting worktree tree matches the approved expected tree, and only `tests/test_production_e2e_fake.py` is modified there. The configured `factory_pytest_selected_v1` command ran in the run worktree and exited 1 after 3.578 seconds: 4 tests passed and 15 errored during setup with Windows access-denied errors while cleaning paths under the configured pytest basetemp. All 31 parsed denied paths were inside the shared acceptance basetemp. The basetemp was left untouched. No review ran; the run is terminal failed. Exactly one terminal dashboard notification was delivered.

**Next action:** preserve this immutable failed run. Diagnose the Windows basetemp cleanup failure, then use a fresh run-owned basetemp and a newly approved proposal/profile binding for any corrective acceptance. Do not reuse this grant, edit the failed run, or claim R1 complete. PR #91 remains draft; no merge occurred; R1 acceptance remains incomplete.
### R1 capability preflight correction and next acceptance (2026-10-13)

The failed corrective request `run_1c2b` is immutable and has one delivered terminal failure notice. Its planner failed before provider execution because the configured `planner-default` agent lacked the required `plan` capability; the safe failure code was previously lost as `unknown/unknown`. The R1-only branch now preserves the allowlisted `governance/capability` classification through the delegated outcome without exposing the private error reason or enabling retry. The proof configuration adds only the workflow-declared capabilities (`plan`, `code`, `test`, `review`) to the four corresponding agents; its recorded config hash is `C8B3C6B1B6BE9F756A6ADD70FFFD0438FCAF88AB359184D253E0EADF2180D183`. The running proof service has not loaded this configuration, and no new task has been submitted.

R1-only checks on base `7ce5fa9c936300d09383c1948f61b9b9b8ac8828` passed: build, vet, full Go test suite, staticcheck with test-inclusive package loading, focused delegated failure tests, and `git diff --check`. The six-file source delta is limited to the capability classifier/outcome propagation and regressions. PR #91 remains draft; no merge or publication occurred. R1 is incomplete: the next acceptance must use the refreshed proof config/profile binding, a fresh nonexistent served basetemp, the full owned Factory workspace, three parallel lanes, and a newly reviewed proposal/approval. The earlier apply-once run and failed validation remain preserved. FW-P1 remains a separate follow-on, and Recall is a separate repository untouched by this work.

**Next action:** prepare and verify the R1-only proof binary/config/profile and service identity, then return the exact packet for review before submitting a new linked acceptance task. Do not reuse either failed run, approval, or basetemp.

### Current R1 handoff (2026-10-14)

The R1 review worktree is `codex/r1-owned-worktree-recovery` at `7ce5fa9c936300d09383c1948f61b9b9b8ac8828`, with 14 modified tracked paths (12 Go paths and these two documentation files). The reviewed delegation fix carries the trusted original task separately from the lane task through the durable join and dispatched request; prompt construction presents both. Legacy nil fields remain omitted, mismatched parent-task data fails validation, and original task text is excluded from event projections. Source inspection confirms the pre-fix delegation path overwrote `RunView.Task` with the lane task before prompt construction. The failed live run no longer retains its completed child join descriptions, so this source finding does not prove that it caused the latest proposal failure.

Focused prompt, legacy-wire, restart/replay, forged-parent, and event-privacy tests pass. On this frozen candidate, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, Staticcheck `./...`, and `git diff --check` all exited 0. Staticcheck was rerun with `GOPACKAGESDRIVER=off`, `GOFLAGS=-buildvcs=false`, and the D-owned caches; `go list -test ./...` resolved 277 package-pattern entries. A separate proof-v4 CLI build exited 0 from this source, HEAD, and 12-file Go manifest SHA-256 `4A4F32B020DA512533E4D3C34D0C922E438C917110C8DFFB6E9AC2B3A68AE891`; its binary SHA-256 is `1B7533B6BBBF858CCFE90E5A24CD86FD2D9D46C9AA3DC1C01BACF08FF178894C` at `.tmp/factory-r1-corrective-baseline/proof-v4/bin/prizm-cli.exe`. The running v3 service was not restarted.

The latest weak-proposal run `run_8319183840de58c95c36763e94` was denied through the existing approval API, applied nothing, and ended failed with exactly one delivered terminal notice. It is immutable, as are earlier failed/provider/setup runs. No successful real coding task or complete R1 acceptance is established. PR #91 remains draft and unmerged. FW-P1 remains separate; Recall is a separate untouched repository.

**Next action:** coordinator reviews the new source/binary provenance and a corrected task-06 request before a guarded service restart or submission. Preserve the current proof service and old runs; require same-branch/worktree/baseline plus per-gate PASS evidence on both test attempts. Obtain a fresh proposal and approval, then validate, obtain fresh review, and verify exactly one terminal report/notice before calling R1 complete.
### R1 served validation follow-up (2026-10-14)

The proof-v4 run `run_54b6a3649a7ab3e9cadd1301d0` passed the reviewed fan-out and applied its sole approved patch once. The configured `factory_pytest_selected_recovery_v1` command ran in the run-owned worktree and returned exit 1: 18 tests passed and the new blocked-resume test failed with `NameError` because `BLOCKED` was used without importing it from `production.evidence`. This is a test-source defect, not a basetemp setup failure. The tester recorded `tests_failed`; developer visit 2 is waiting on the canonical delivered command, with no terminal outcome or later correlated role/tool event observed as of 2026-10-14 21:06Z. The run, applied tree, and validation result remain unchanged; do not rerun pytest manually or reuse this approval. Worker accepted status is not evidence that `RunRole` began: the current worker records acceptance before runtime construction and has no persisted start/elapsed marker. R1 is incomplete; PR #91 remains draft.

**Next action:** continue observing only the current developer visit until its canonical result/deadline. Review any new proposal for the missing import and the original required assertions before another approval. Add a bounded worker-stage trace as a separately reviewed follow-up; do not infer active execution from accepted status alone.

### R1 Tester outcome and reserved-final tool recovery follow-up (2026-10-15)

The follow-up run `run_01M505NB4QXFHZFVEMJVSVGF8F` is terminal and unchanged.
Tester completed with `validation_status=passed` and `LastOutcome=tests_failed`,
so the declared `tests_failed -> developer` edge correctly scheduled Developer
visit 2. The strict Tester contract permits a failed result when another
reported check is nonpassing and has failure evidence or a reproduction;
configured profile success alone does not override the Tester result.

The reserved-final guard now reports a rejected tool request as the allowlisted
`invalid_final_schema` safe failure. The request is rejected before execution,
and recovery stays within the shared durable role limit. Empty or unclassified
final responses still fail closed. Run metadata does not identify the response
shape that caused its generic terminal failure; raw provider content remains
uninspected.

**Evidence:** focused `go test` passed for the reserved-final tool request,
empty final response, parser-classified reserved-final failures, durable
recovery scheduling, initial-plus-ten exhaustion, retry classification, and
cancellation. On the exact worktree at HEAD `4097d5a5f43b7f7086650391f41e494bd29bef05`,
`go build ./...`, `go vet ./...`, uncached `go test -count=1 ./...`,
`staticcheck ./...`, and final `git diff --check` all exited 0. These tests do
not identify the response shape in the immutable provider run.

**Next action:** coordinator reviews the minimal source/test and documentation
diff before any commit or publication. R1 served acceptance remains incomplete;
do not resume the terminal run.

### R1 strict unified-diff proposal preflight (2026-10-15)

On the source snapshot at `0232ca600ba2ab7e1675c9583061b77353a6fd33`, patch
planning, expected-tree calculation, apply/rollback, direction checks, and path
extraction now use strict Git unified-diff parsing without `--recount`.
Incorrect hunk counts fail before the approval store is reached. The existing
typed no-effects correction continues to feed bounded feedback to the model and
consume the shared role retry allowance; successful application remains bound
to its persisted patch plan.

**Evidence:** focused Git, tool, subagent, and multiagent tests passed. The
malformed-hunk regression proves no approval request is emitted, no approval is
saved, and the worktree stays clean; existing valid multi-file proposal/apply
and typed correction/retry tests pass. `go build ./...`, `go vet ./...`,
`go test ./... -count=1`, `staticcheck ./...`, and `git diff --check` passed with
D-drive caches and the elevated Windows temp setup. No served run, live
approval, or provider prompt was accessed. R1 remains partial and the live
acceptance gate is outstanding.

**Next action:** review the local commit, then use a separately authorized fresh
served run to verify malformed proposals receive correction before approval and
that the corrected proposal is approved, applied exactly once, validated, and
resumed. Do not reuse the immutable failed run or its proposal.

## Current Handoff: R1 strict patch hunk preflight (2026-10-15)

**Roadmap:** R1 remains incomplete. Starting from the published PR #91 tree,
`internal/gitx` validates each unified-diff hunk's declared old/new counts and
requires the body to consume those counts exactly. It rejects surplus or
missing lines and text left after completed hunks before Git prechecks. Shared
patch planning applies the rule to clean and trusted-tree plans, so malformed
proposals fail without persisting or emitting approval. No package boundary
changed.

**Evidence:** parser and planning coverage exercises the 19-header/21-addition
case, excess removals, incomplete counts, trailing text, context, and the
no-newline marker. A tool-boundary regression confirms malformed input creates
no approval request or stored approval and leaves the workspace unchanged.

**Next action:** complete focused and repository-wide validation on this PR
branch, then coordinator review. A fresh, separately authorized served R1
acceptance remains necessary; do not resume a terminal run.

### R1 CRLF compatibility in strict patch preflight (2026-10-15)

The strict parser removes a terminal carriage return only from its local line
view before matching hunk headers. Patch bytes passed to Git and used for the
approval-bound hash remain unchanged. Count checks and trailing-line rejection
apply equally to CRLF hunks.

**Evidence:** CRLF regressions cover valid counts, the no-newline marker,
surplus-line rejection, and a new-file planning path that retains the original
patch hash. Repository-wide validation on this branch is pending.

**Next action:** finish focused and full validation, then coordinator review.
R1 served acceptance remains incomplete and requires a separately authorized
fresh run.

## Current Handoff: R1 cancellation precedence at durable retry boundary (2026-10-15)

**Roadmap:** R1 remains incomplete. `DurableRuntime.Resume` now checks the
persisted cancellation request before recovery/drive can resume a due
`role_retry`, and `Cancel` plus the retry boundary share one terminal
checkpoint path. That path accounts accumulated retry iterations, recovery
count, token usage, and retry history before clearing waiting state. It emits
the cancellation event through the durable outbox exactly once; terminal
Resume remains idempotent. No package boundary changed.

**Evidence:** the regression persists cancellation while a run claim is held,
advances beyond retry due time, releases the claim, and resumes. It confirms no
provider retry starts, the run/report/dashboard are cancelled with the supplied
reason, retry waiting is cleared, and a repeated Resume does not duplicate the
cancellation event. Focused tests, `go build ./...`, `go vet ./...`, uncached
`go test -count=1 ./...`, `staticcheck ./...`, and `git diff --check` passed.

**Next action:** coordinator review, followed by a separately authorized fresh
served R1 acceptance. No live provider or service was used for this change.

### Cancellation safety during ambiguous execution recovery

Cancellation finalization now defers while a role execution is still
`CheckpointRoleRunning`, while the run is waiting for `execution_reconciliation`,
or while retry state is non-replayable or carries possible/unresolved/unattributed
mutation evidence. The request remains durable. Recovery preserves the
reconciliation checkpoint before cancellation can become terminal; safe retry
boundaries still cancel before invoking the provider.

**Evidence:** a regression starts a mutating tool, simulates a process crash,
persists cancellation while the run claim is held, and resumes. The run remains
paused for reconciliation with mutation evidence and the request intact; it
does not invoke the runner or emit a terminal cancellation event. Focused and
full repository validation passed.

**Next action:** coordinator review and separately authorized served R1
acceptance; no live provider behavior is claimed.

## Current Handoff: R1 served retry failure origin (2026-10-16)

**Roadmap:** R1 remains incomplete. Served run `run_8b0136a552a09b58a015757442` used the approved `ollama/glm-5.3:cloud` provider through Prizm's served API. It consumed one initial attempt and ten retries, then ended with `retry_exhausted` for `provider_transient/provider_turn_failed`. No proposal, apply, or validation occurred; the isolated clone stayed untouched. PR #91 remains draft.

The runtime now derives the retry origin through the existing failure classifier, stores only the allowlisted origin with durable retry state, and includes the safe origin (including `unknown`) in scheduled and exhausted retry events. Unknown errors remain unknown; raw error text is excluded. The regression exercises the real durable scheduling and exhaustion path for both known provider origin and unknown origin.

**Evidence:** the focused durable retry-origin regressions passed. `go build ./...`, `go vet ./...`, uncached `go test ./... -count=1` (with loopback and workspace-local temporary/cache paths), and `git diff --check` passed. The local Ollama context scout was unavailable because port 11434 access was denied; targeted source discovery followed the documented fallback. No package boundary changed.

**Risk:** this run demonstrates bounded retry exhaustion and terminal reporting, not successful code delivery. R1 acceptance remains open.

**Next action:** coordinator review of this exact candidate, then a fresh isolated served task after the provider availability issue is addressed. Require a reviewed proposal, explicit approval, exactly-once apply, configured validation, resume, and one final report before claiming R1 completion or removing draft status.
