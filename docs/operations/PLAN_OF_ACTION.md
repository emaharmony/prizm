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
| 5 | Prove the slice. | Deterministic grant, deny, unrelated approval, restart, duplicate, reconciliation, retry, failure, event, worktree, corrective-turn, terminal-cleanup, canonical-artifact, post-apply-validation, scoped-provider, legacy-worker, and fail-closed-validator tests pass. The authorized disposable Codex run proved planner handoff, an exact durable proposal/approval pause, process restart inspection, and isolated mutation application. It exposed and closed absolute-target canonicalization and model-claimed-artifact conflicts. Its tester then omitted required evidence, so the terminal test/review/report proof remains open. Every Codex delegation path is read-only: the legacy local/Cross-Prizm worker forcibly disables native mutation, and graph subagents require a contained, owned run worktree before scoped native execution. |

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
| Reviewed baseline | `codex/r1-approved-task` at the legacy-delegation safety commit, based on merged PR #83 plus CI repair `8232f7e`; no 9/10 gate is met. |
| Current state | Exact proposal approval, durable idempotent apply, crash reconciliation, isolated worktree execution, post-apply workspace-aware validation, diff evidence, correlated lifecycle events, proposal-required developer completion, and terminal worktree cleanup are integrated. The first real-provider run reached exact approval, process-restart inspection, isolated apply, and tester entry; its provider-output edge cases are now hardened, but a complete provider-backed terminal report still needs to be repeated. Legacy local and Cross-Prizm Codex worker delegation is read-only and carries a no-mutation prompt. Graph subagents scope any resolved native-tool provider based on capability rather than provider label, reject providers without scoped execution, and verify the workspace is an extant child of the configured repository's owned worktree root. Mutation actions remain exclusively in Prizm's governed tool executor. Status-only approvals and `workflow/v2` remain compatible. |
| PR handoff | Current R1 foundation and provider-boundary hardening commits are pushed to `origin/codex/r1-approved-task`. They are not merged. |
| Next implementation decision | Repeat the disposable real-provider run after the tester-output hardening, then inspect validation, review/report, and the complete correlated event trace. |
| Evidence required before advancing | A complete real-provider/repository run through proposal, approval, restart recovery, apply, validation, review/report, and the full correlated event trace. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)
