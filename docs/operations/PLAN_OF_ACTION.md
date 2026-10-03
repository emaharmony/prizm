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
| 5 | Prove the slice. | Deterministic grant, deny, unrelated approval, restart, duplicate, reconciliation, retry, failure, event, worktree, corrective-turn, terminal-cleanup, canonical-artifact, post-apply-validation, scoped-provider, legacy-worker, and fail-closed-validator tests pass. The final bounded Codex run proved planner handoff, one exact durable proposal/approval pause, fresh-process recovery, isolated application reconciliation, and developer completion. The tester then supplied a contradictory failed result with only passing test entries; strict decoding stopped the run before validation/review. The durable resume path now removes only an identity-bearing stale pending-approval issue from the pre-approval handoff and adds the authoritative applied/validated lifecycle fact. No second provider run was made. Every Codex delegation path is read-only: the legacy local/Cross-Prizm worker forcibly disables native mutation, and graph subagents require a contained, owned run worktree before scoped native execution. |

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
| Reviewed baseline | `codex/r1-approved-task` includes the current `origin/staging` integration through merge `94224bc`, based on merged PR #83 plus CI repair `8232f7e`; no 9/10 gate is met. |
| Current state | Exact proposal approval, durable idempotent apply, crash reconciliation, isolated worktree execution, post-apply workspace-aware validation, diff evidence, correlated lifecycle events, proposal-required developer completion, and terminal worktree cleanup are integrated. The latest bounded real-provider run completed planner, developer, fresh-process recovery, and isolated application. Its tester returned an inconsistent failed result with only passing test entries, so strict decoding stopped before validation/review. The runtime now replaces only an exact, settled proposal's stale pending-approval issue in the outgoing handoff with the durable lifecycle fact; unrelated developer issues remain. Legacy local and Cross-Prizm Codex worker delegation is read-only and carries a no-mutation prompt. Graph subagents scope any resolved native-tool provider based on capability rather than provider label, reject providers without scoped execution, and verify the workspace is an extant child of the configured repository's owned worktree root. Mutation actions remain exclusively in Prizm's governed tool executor. Status-only approvals and `workflow/v2` remain compatible. |
| PR handoff | Current R1 foundation, provider-boundary hardening, and the current staging integration are ready to push to `origin/codex/r1-approved-task`. They are not merged. |
| Next implementation decision | A future explicitly authorized bounded provider run must prove validation, review/report, and its full correlated event trace. |
| Evidence required before advancing | A complete real-provider/repository run through proposal, approval, restart recovery, apply, validation, review/report, and the full correlated event trace. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)
