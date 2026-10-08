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
| 3 | Make Recall primary with safe local fallback. | Partial: failed primary capture persists `sync_pending`; the R6 consumer startup-scans and event-notifies bounded reconciliation, but the current Recall API cannot prove remote idempotency and is therefore rejected for replay. |
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
| Current state | Scoped capture persists `sync_pending` facts. The R6 consumer startup-scans them, responds to new pending notifications, bounds retry with durable evidence, revalidates local scope, and writes deterministic terminal events. It only replays through `IdempotentPrimaryBackend`; the current Recall `source_ref` is stored but not deduplicated, so its adapter fails closed rather than risk duplicate remote writes. |
| PR handoff | R6 work is on `codex/r6-memory-reconciliation`, based on merged staging `dca2156`. It delivers the local reconciliation contract and deterministic proof, not a claim that the current Recall service supports idempotent replay. |
| Next implementation decision | Add an atomic Recall idempotency-key contract that returns the original primary ID, implement it in the Remembrance adapter, then run a live outage/recovery trace. |
| Evidence required before advancing | The seeded gate is 10/10 with no leakage; deterministic reconciliation covers restart, concurrency, crash recovery, scope rejection, and retry exhaustion. Still required: a live Recall outage/recovery trace using a primary that guarantees idempotent delivery. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)
