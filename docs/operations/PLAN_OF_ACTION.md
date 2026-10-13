# Prizm Plan of Action

## Current Handoff: R1 fan-out recovery and worker lifecycle (2026-10-13)

The review candidate is `codex/r1-parallel-acceptance-staging`, based on the
current `origin/staging` snapshot `e2a4421018acca7940f4d6806d5a3c2cf59a92b9`.
It contains the R1 implementation and regression tests; the dirty primary
`codex/r1-parallel-acceptance` checkout and separate Recall worktree remain
untouched. The source change covers trusted Planner fan-out capability,
durable child recovery and retry-attempt scoping, fail-closed approval-bound
fan-out adoption, immutable validation-profile binding, safe structured-output
feedback, and worker close/drain synchronization.

Candidate verification on this tree passed `go build ./...`, `go vet ./...`,
default-parallel `go test ./...`, `staticcheck ./...`, `git diff --check`,
the graph-worker concurrency regression at 20 repetitions, and a focused race
detector run for close/drain. The Linux CI cleanup race was traced to worker
callbacks and command/deferred-resume work outliving `Close`; the worker now
closes admission and drains owned callbacks/work before returning. This is a
candidate for draft review, not evidence that the live Factory acceptance is
complete.

The latest real-provider Factory run, `run_9544229c6a3b2158bf82ddf6e6`, proved
three overlapping worker intervals but failed at fan-in: research succeeded,
implementation ended with a safe unknown/nonretryable failure, and review
exhausted structured-output recovery after typed field feedback. It recorded
no proposal, apply, or test result and emitted one terminal failure notice.
Earlier failed/cancelled records remain unchanged. The selected Factory source
snapshot is not full-game or live Studio evidence. Keep R1 open; do not claim a
9/10 gate or successful coding acceptance. PR #86 is now part of staging; the
separate Recall API-only test proof does not verify Prizm-to-Recall recovery.

**Next action:** review this exact candidate diff and validation packet, then
open a draft PR against staging if approved. Keep the full Factory acceptance
gate open and diagnose only from safe typed evidence. Do not merge the PR.

This is the current execution plan. It is updated at the end of every project
work session and must cite the active [roadmap](../architecture/PRIZM_ROADMAP.md)
items.

> Prizm is source-available under an all-rights-reserved [license](../../LICENSE)
> and is preview-stage. This plan does not change approval or policy requirements.

## Current Handoff (2026-10-13)

**Active roadmap IDs:** R1 remains the platform priority; FW-001 contract
groundwork is proceeding in parallel on branch `codex/fw001-intention-contracts`
from staging base `e2a4421018acca7940f4d6806d5a3c2cf59a92b9`.

PR [#91](https://github.com/emaharmony/prizm/pull/91) is an open draft R1
checkpoint. Its latest real-provider Factory run had three parallel child
lanes but failed during fan-in; it produced no implementation proposal, apply,
configured validation, or final review. R1 acceptance and the roadmap 9/10 gate
remain open. Do not start FW-P2 or describe the R1 gate as complete.

FW-001 adds only pure intention/candidate contracts, structural lifecycle
validation, disabled-by-default `freewill` flags, and ADR 0003 documentation.
Its config flags have no runtime consumer and do not enable scheduling or
execution. Focused tests and full repository `go test ./...`, `go build ./...`,
`go vet ./...`, and `staticcheck ./...` passed; coordinator review approved the
implementation. PR [#92](https://github.com/emaharmony/prizm/pull/92) is open,
ready for review, and initially mergeable; CI is running/queued on head
`8ca67fbc9e9b3f2a896b53a81c96244951830a21`. FW-002 is the next eligible
Freewill task after this review checkpoint, subject to provider/scheduler
prerequisites. FW-P2 remains blocked on R1 acceptance and FW-G2.

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

PR #91 remains a draft checkpoint; the real Factory acceptance run `run_9544229c6a3b2158bf82ddf6e6` failed before proposal, apply, configured validation, or final review. Its immutable report remains unchanged. The R1 review worktree was merged with current staging `31a6ee783691c7a59005d6afc2b064ad73859258`; only seven reviewed diagnostic/classifier paths were ported. Terminal child failures now preserve trusted retry counters and allowlisted failure/schema details through durable join failure, child event, and revalidated report projection. Four known safe nonretryable tool codes remain distinct; unknown errors stay unknown. Focused multiagent/event tests pass; full build, vet, and default-parallel suite are pending on the merged candidate.

Next: finish current-tree gates and review the exact seven-file diagnostic diff before committing/pushing an update to PR #91. Then address the live acceptance failure from the new bounded evidence; do not claim R1 complete or change immutable run history. FW-P1 (FW-002/003/004) remains separate follow-on work after this R1 checkpoint.
