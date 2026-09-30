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
| 1 | Carry the exact proposal approval ID into durable waiting state and the approval decision. | Done in `2785552`: proposal and approval identities are checkpointed, and unrelated decisions cannot advance the run. |
| 2 | Apply the granted proposal before continuing the graph. | Done in `2785552`: stable apply keys, reconciliation, bounded retry, denial, and duplicate delivery are durable and event-visible. |
| 3 | Verify in an isolated worktree with a project-aware validation profile. | Done in `2785552`: new runs persist a detached worktree; validation evidence includes command, arguments, working directory, output, and diff. |
| 4 | Complete the durable transition. | Done in `2785552`: the saved role result resumes without another model call, then continues to review/report or fails closed. |
| 5 | Prove the slice. | Deterministic grant, deny, unrelated approval, restart, duplicate, reconciliation, retry, failure, event, and worktree tests pass. The real-provider acceptance run remains open because the developer can finish without creating its required proposal. |

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
| Reviewed baseline | `codex/r1-approved-task` at `0315378`, based on merged PR #83 plus CI repair `8232f7e`; no 9/10 gate is met. |
| Current state | Exact proposal approval, durable idempotent apply, crash reconciliation, isolated worktree execution, workspace-aware validation, diff evidence, and correlated lifecycle events are integrated. Status-only approvals and `workflow/v2` remain compatible. |
| PR handoff | Local commits `2785552` and `0315378` implement and harden the slice. They are not pushed or merged. |
| Next implementation decision | Require a mutation-bearing developer role to persist a proposal before it may complete, with one bounded corrective turn when the provider returns a final response without the proposal. |
| Evidence required before advancing | Repeat the real-provider/repository run through approval, restart recovery, validation, review/report, and inspect the complete event trace. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)
