# Prizm Plan of Action

This is the current execution plan. It is updated at the end of every project
work session and must cite the active [roadmap](../architecture/PRIZM_ROADMAP.md)
items.

> Prizm is source-available under an all-rights-reserved [license](../../LICENSE)
> and is preview-stage. This plan does not change approval or policy requirements.

## Current Focus: R2 Scoped Memory Contract

The next slice makes memory retrieval and capture safe for autonomous agents:
every new operation carries an exact project and task scope, user context is
opt-in, and Recall failure falls back to local durable memory without leakage.

| Order | Work | Done when |
| --- | --- | --- |
| 1 | Require exact project and task scope on new autonomous memory reads and writes. | Done for serve chat: model-supplied scope fields are cleared; prompt injection derives canonical workspace, run, session, agent, and owner scope, rejects Recall context packs that do not echo it, and falls back only through scoped local retrieval. |
| 2 | Make capture idempotent and replacements explicit. | Done: delivery keys are namespaced by scope; conflicting content or metadata is rejected, while an explicit superseding replacement receives a distinct stable ID. |
| 3 | Make Recall primary with safe local fallback. | Partial: scoped Recall requests and local fallback pass. Primary capture failure now persists a `sync_pending` event with an idempotent retry key; durable replay/reconciliation remains R6 work. |
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
| Active roadmap IDs | R2 (primary), with R6 integration dependency; R1 acceptance and event-outbox work remain open. |
| Reviewed baseline | `origin/staging` at `a2a5622` includes merged R1 PR #84; no 9/10 gate is met. |
| Current state | Autonomous prompt memory now uses a canonical workspace/run/session/agent/owner scope. Recall context packs that omit or mismatch that scope are skipped; scoped local search is the only fallback, with cache keys including the full scope. Capture conflicts fail closed unless they explicitly supersede the prior memory, and failed primary sync records an inspectable pending state without claiming reconciliation. Legacy APIs remain compatible outside the autonomous serve path. |
| PR handoff | R2 work is on `codex/r2-scoped-memory`, based on `a2a5622` and ready for normal review against `staging`. Its scope is the R2 scoped-memory contract and deterministic isolation evidence; it does not claim R6 live reconciliation or full R2/R6 completion. |
| Next implementation decision | Implement R6 durable local-to-Recall reconciliation with an idempotent delivery record, then run a live outage/recovery trace in an environment with a working Remembrance service and embedding provider. |
| Evidence required before advancing | The seeded gate is 10/10 with no leakage. Still required: a live Recall outage/recovery trace and proof that deferred local captures reconcile once without duplicate remote writes. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)
