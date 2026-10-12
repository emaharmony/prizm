# Prizm Plan of Action

This is the current execution plan. It is updated at the end of every project
work session and must cite the active [roadmap](../architecture/PRIZM_ROADMAP.md)
items.

> Prizm is source-available under an all-rights-reserved [license](../../LICENSE)
> and is preview-stage. This plan does not change approval or policy requirements.

## Current Focus: R1 Durable Event Outbox and Delegation Acknowledgements

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
| Reviewed baseline | `origin/staging` at `dca2156` includes merged R1 PR #84 and R2 PR #85; no 9/10 gate is met. |
| Current state | R1 now has a canonical typed command/outcome envelope and SQLite outbox with accept-once idempotency, leases, bounded publish retry, restart replay, deadline failure, and correlated reports. `workflow/v2` remains on its compatible durable path. The canonical graph now publishes a command containing the exact prepared role request and trusted run, role, execution, workspace, delegation, delivery, correlation, and deadline identity. `prizm serve` can compose a graph-owned NATS worker behind `PRIZM_GRAPH_ROLE_DELEGATION=1`; NATS is only a notification channel, and the worker requires the wire command to match the canonical SQLite outbox row byte-for-byte before executing through the governed `AgentRoleRunner`. Mutation proposals now support one bounded atomic multi-file unified diff under one exact approval. The approval persists the trusted base SHA/tree, expected tree, original patch hash, and all canonical paths; proposal and apply both enforce configured write roots, and restart recovery classifies the complete worktree tree as applied, not applied, or ambiguous. Governed Git patch interpretation uses `--recount` consistently for path inspection, precheck, expected-tree calculation, apply, rollback, and direction checks so incorrect model-generated hunk counts do not invalidate otherwise complete diffs; the exact original bytes remain approval-bound and no reject, three-way, unsafe-path, or content normalization mode is enabled. Governed developers expose only the atomic proposal mutation tool and receive the trusted base SHA from the runtime. The governed worker decodes proposal and typed-final envelopes as one complete JSON object, independent of member order and whitespace; atomic proposal input requires exactly non-empty string `patch` and `base_sha` fields, and embedded/fenced/multiple objects fail closed. Legacy embedded-envelope parsing remains compatible only for existing `workflow/v2` callers. A supervised wall-clock scanner wakes expired graph delegations autonomously; local resume serialization plus the durable run claimer prevents competing wake transitions. |
| PR handoff | Draft PR #87 on `codex/r1-event-outbox` includes atomic proposal checkpoint `fefc753`, reviewed SQLite outbox serialization fix `a5a054d`, canonical envelope repair `fa1dd94`, and governed hunk recount repair `ef4808b`. Local build, vet, and the complete Go suite are green. Documentation correction `648aa70` is committed locally but its push remains pending explicit remote-ownership approval after automatic review rejected that push. Draft PR #86 on `codex/r6-memory-reconciliation` remains parked without further Recall implementation, review, merge, or claims of live Recall reconciliation. |
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
