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
| 1 | Require exact project and task scope on new autonomous memory reads and writes. | Done for serve: trusted workspace/run identities override model input; missing identity fails closed. |
| 2 | Make capture idempotent and replacements explicit. | Done: capture IDs namespace delivery keys by user/project/task; append-only replacement links suppress superseded memories. |
| 3 | Make Recall primary with safe local fallback. | Partial: scoped Recall requests and local fallback are wired; durable retry/reconciliation remains R6 work. |
| 4 | Emit inspectable memory lifecycle facts. | Done for scoped serve operations: canonical correlation, capture/search/fallback events persist in SQLite. |
| 5 | Prove the memory score gate. | Pending: seed and measure 10 scoped preference/task cases, exercise a real Recall outage and later reconciliation, and prove no denied or stale context crosses a boundary. |

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
| Current state | Scoped memory is wired into `serve`: trusted workspace/run identities supply project/task/correlation, model scope is overridden, and absent identity fails closed. Recall receives exact scope parameters and incomplete or mismatched response metadata is discarded before merge. Legacy APIs remain compatible. |
| PR handoff | R2 work is on `codex/r2-scoped-memory`, rebased onto `a2a5622`; it is not pushed or merged. |
| Next implementation decision | Prove ten seeded cases and a real Recall outage/recovery trace; add durable reconciliation only after the local store contract supports it. |
| Evidence required before advancing | Ten seeded scoped-memory cases with 9/10 correct recall and no cross-scope return; one real Recall outage and recovery trace. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)
