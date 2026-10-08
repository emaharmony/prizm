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
| Active roadmap IDs | R1 (event contract and delegation); R2/R6 Recall work is parked. |
| Reviewed baseline | `origin/staging` at `dca2156` includes merged R1 PR #84 and R2 PR #85; no 9/10 gate is met. |
| Current state | R1 now has a canonical typed command/outcome envelope and a SQLite outbox with accept-once idempotency, leases, bounded retry, restart replay, deadline failure, and correlated reports. `workflow/v2` delegation commands and worker outcomes use this durable path. Outcomes are bound to the stored command, replayed until workflow state is persisted, and carry stable delegation/delivery identity through accepted, progress, and one terminal result. Paused v2 parents process and persist delegation facts without leaving their approval/review pause. The graph retains its separate atomic run/event outbox. |
| PR handoff | Draft PR #86 on `codex/r6-memory-reconciliation` is parked without further Recall implementation, review, merge, or claims of live Recall reconciliation. |
| Next implementation decision | Define the canonical graph's child/delegation state contract, then connect a bounded delegation waiting checkpoint to this outcome intake before parallel research/code/review fan-out/fan-in. |
| Evidence required before advancing | A real-provider task and an embedded-NATS failure matrix must prove one execution and one terminal outcome across duplicate delivery, worker loss, deadline, retry, and restart. Then prove parallel research/code/review fan-in with a complete event-derived report. R3 and R4 follow that R1 gate. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)
