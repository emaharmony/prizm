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
| 1 | Require exact project and task scope on new autonomous memory reads and writes. | Done in this slice: user scope requires an explicit opt-in and scope filtering happens before primary/local result merge. |
| 2 | Make capture idempotent and replacements explicit. | Done in this slice: stable capture keys return the original local record; append-only replacement links suppress superseded memories. |
| 3 | Make Recall primary with safe local fallback. | Done in this slice: the memory facade owns a neutral primary-backend contract, local persistence, fallback, and de-duplication; Remembrance stays at the integration edge. |
| 4 | Emit inspectable memory lifecycle facts. | Done in this slice: correlated capture, fallback, supersession, and retrieval events use the existing SQLite event store schema. |
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
| Reviewed baseline | `codex/r1-approved-task` includes the current `origin/staging` integration through merge `94224bc`, based on merged PR #83 plus CI repair `8232f7e`; no 9/10 gate is met. |
| Current state | Scoped memory now has an additive local facade: project/task are mandatory, user context is rejected without opt-in, capture keys are stable, replacements are explicit, and primary/local results are filtered and deduplicated before return. Typed correlated events are persisted to the current SQLite event store. The Remembrance adapter translates its HTTP contract at the integration edge; workflow need not know that protocol. Legacy memory APIs remain compatible. |
| PR handoff | R1 is in PR #84. R2 work is on stacked branch `codex/r2-scoped-memory`; it is not pushed or merged. |
| Next implementation decision | Wire scoped context from run/chat identity into the composition roots, then prove real Recall capture/search, outage fallback, and reconciliation. |
| Evidence required before advancing | Ten seeded scoped-memory cases with 9/10 correct recall and no cross-scope return; one real Recall outage and recovery trace. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)
