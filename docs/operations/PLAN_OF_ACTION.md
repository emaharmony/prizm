# Prizm Plan of Action

This is the current execution plan. It is updated at the end of every project
work session and must cite the active [roadmap](../architecture/PRIZM_ROADMAP.md)
items.

> Prizm is source-available under an all-rights-reserved [license](../../LICENSE)
> and is preview-stage. This plan does not change approval or policy requirements.

## Current Focus: R6 Durable Recall Reconciliation

The next slice makes deferred local memory capture recover safely after Recall
returns. Every replay starts from a durable pending fact, revalidates the exact
scope against the local memory record, and requires a primary backend that
explicitly proves idempotent delivery.

| Order | Work | Done when |
| --- | --- | --- |
| 1 | Require exact project and task scope on new autonomous memory reads and writes. | Done for serve chat: model-supplied scope fields are cleared; prompt injection derives canonical workspace, run, session, agent, and owner scope, rejects Recall context packs that do not echo it, and falls back only through scoped local retrieval. |
| 2 | Make capture idempotent and replacements explicit. | Done: delivery keys are namespaced by scope; conflicting content or metadata is rejected, while an explicit superseding replacement receives a distinct stable ID. |
| 3 | Make Recall primary with safe local fallback. | Partial: every primary delivery persists `sync_pending` before any remote call and uses the idempotent contract for both initial delivery and replay. The R6 consumer counts only unresolved facts and retains valid pending captures when no eligible primary is configured; the current Recall API cannot prove remote idempotency and is therefore not used for mutation. |
| 4 | Emit inspectable memory lifecycle facts. | Done: lifecycle event persistence is strict for scoped operations; event-store failure surfaces and an idempotent retry retains the original local record. |
| 5 | Prove the memory score gate. | Done: deterministic seeded retrieval gate scores 10/10 with zero cross-scope leakage. |

## Follow-on R1 Work

After the approval slice passes, unify the graph and NATS action paths behind one
typed intake and durable outbox. Define command, acceptance, progress, and
terminal-outcome events with idempotency keys and correlation IDs. Then add
delegation acknowledgement, worker deadlines, fan-out/fan-in, and event-derived
task/review reports.

## Session Handoff

| Field | Current value |
| --- | --- |
| Active roadmap IDs | R2 and R6; R1 acceptance and event-outbox work remain open. |
| Reviewed baseline | `origin/staging` at `dca2156` includes merged R1 PR #84 and R2 PR #85; no 9/10 gate is met. |
| Current state | Scoped capture persists `sync_pending` before every primary attempt. Both initial delivery and replay require `IdempotentPrimaryBackend`, so a crash after remote success can replay the stable sync key without creating another record. The R6 consumer startup-scans unresolved facts, responds to notifications, revalidates local scope, and writes deterministic terminal events. Reconciliation reads every SQLite causal page by durable rowid while public queries retain their event-ID cursor, so deterministic terminal IDs cannot hide later facts and a large history cannot truncate recovery. Retry facts have deterministic IDs per pending generation and attempt; concurrent duplicate stores are idempotent and retry accounting uses distinct attempts. Recall migration 14 now binds an idempotency key to the exact owner/workspace/project/repository/task/session/agent scope and payload fingerprint in its transactional outbox; a matching replay returns the original remote ID. The Prizm Recall adapter implements the idempotent primary contract using the stable local memory ID. |
| PR handoff | R6 work is on `codex/r6-memory-reconciliation`, based on merged staging `dca2156`. It delivers the local reconciliation contract and deterministic proof, not a claim that the current Recall service supports idempotent replay. |
| Next implementation decision | Preserve the process-level outage/recovery trace with the paired Recall API review; use the shared serve-tool seam for deterministic scoped fallback/search checks while the public capture route remains absent. |
| Evidence required before advancing | The seeded gate is 10/10 with no leakage; deterministic reconciliation covers restart, concurrent duplicate failure/retry accounting, pending-before-delivery, crash recovery, sync-event write failure, scope rejection, causal SQLite pending → terminal → re-pending order, paged causal recovery beyond one bounded page, retry-generation reset, terminal-write health reporting, absent-primary startup, and retry exhaustion. A process-level trace covers Recall outage fallback, fresh-process reconciliation, Recall restart, duplicate replay, scope-separated delivery keys, and crash-after-remote-success recovery against a fresh SQLite database. Actual `prizm serve` startup was checked with Recall unavailable and the reconciliation consumer active. The shared serve registration seam now exercises the real `MemoryWriteTool` plus scoped facade fallback/search under exact scope; the HTTP API still has no public scoped capture/tool route. The Recall adapter rejects `FAILED` and other unrecognized terminal decisions even when a raw remote ID is present. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)
