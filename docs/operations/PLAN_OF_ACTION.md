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
