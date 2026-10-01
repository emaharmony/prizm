# Prizm Plan of Action

This is the current execution plan. It is updated at the end of every project
work session and must cite the active [roadmap](../architecture/PRIZM_ROADMAP.md)
items.

> Prizm is source-available under an all-rights-reserved [license](../../LICENSE)
> and is preview-stage. This plan does not change approval or policy requirements.

## Current Focus: R1 Reliable Coding and Delegation Foundation

The next slice closes the path from a developer proposal to verified code. It
must be complete before adding broader autonomy triggers or remote delegation.

| Order | Work | Done when |
| --- | --- | --- |
| 1 | Carry the exact proposal approval ID into durable waiting state and the approval decision. | A run pauses for its own proposal, rejects an unrelated approval, and resumes only after that proposal is granted or denied. |
| 2 | Apply the granted proposal before continuing the graph. | The proposal changes are applied once, emit an applied event, and a restart or duplicate delivery cannot apply them twice. |
| 3 | Verify in an isolated worktree with a project-aware validation profile. | The selected project's configured validation runs in the worktree; its command, result, and diff are stored in the run evidence. |
| 4 | Complete the durable transition. | The run resumes to review/report on success; failure produces a bounded diagnosis, retry or escalation, and a terminal event. |
| 5 | Prove the slice. | Tests cover grant, deny, unrelated approval, restart before/after apply, duplicate delivery, validation failure, and one real-provider/real-repository task. |

## Follow-on R1 Work

After the approval slice passes, unify the graph and NATS action paths behind one
typed intake and durable outbox. Define command, acceptance, progress, and
terminal-outcome events with idempotency keys and correlation IDs. Then add
delegation acknowledgement, worker deadlines, fan-out/fan-in, and event-derived
task/review reports.

## Session Handoff

| Field | Current value |
| --- | --- |
| Active roadmap IDs | R1 (primary); R2, R3, R4, R5, R6, and R8 have partial foundations; R7 is missing in Prizm. |
| Reviewed baseline | Pre-push review snapshot: `codex/adapter-driven-runtime` at `059fb4f`, with refreshed `origin/staging` at `9d0833d`; no 9/10 gate is met. |
| Current state | Foundational and partial work already exists: R1 durable graph and existing approval components, delegation, worktree, and validation components; R4 adapter contracts and NATS/JetStream paths; and partial R2/R3/R5/R6/R8 capabilities. These remain disconnected from the required end-to-end evidence paths. |
| PR handoff | A ready-for-review PR body is staged locally at `.tmp/pr-body.md`. GitHub rejected the branch push with HTTP 403 because no configured credential has write access; no PR, push, or merge occurred. |
| Next implementation decision | Carry the exact proposal approval identity through durable waiting state, apply, verify, and resume before modifying the live graph composition further. |
| Evidence required before advancing | Focused unit/integration tests, a real-provider real-repo run, and review of the event trace and generated report. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)
