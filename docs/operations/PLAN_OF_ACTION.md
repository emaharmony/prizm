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
| Active roadmap IDs | R1 (event contract and delegation), with R4 dependent on the completed R1 contract; R2/R6 Recall work is parked. |
| Reviewed baseline | `origin/staging` at `dca2156` includes merged R1 PR #84 and R2 PR #85; no 9/10 gate is met. |
| Current state | R1 now has a canonical typed command/outcome envelope and SQLite outbox with accept-once idempotency, leases, bounded publish retry, restart replay, deadline failure, and correlated reports. `workflow/v2` remains on its compatible durable path. The canonical graph now publishes a command containing the exact prepared role request and trusted run, role, execution, workspace, delegation, delivery, correlation, and deadline identity. `prizm serve` can compose a graph-owned NATS worker behind `PRIZM_GRAPH_ROLE_DELEGATION=1`; NATS is only a notification channel, and the worker requires the wire command to match the canonical SQLite outbox row byte-for-byte before executing through the governed `AgentRoleRunner`. The composition root seeds the persisted run workspace before the first command is dispatched, so both command workspace fields are stable and validated before any provider result. Outcome intake likewise accepts only facts already persisted by the trusted worker ledger. Acceptance replay uses the first persisted bytes, so a stable outcome ID never gains a new timestamp or body. A dispatcher that observes an already-persisted worker terminal fact preserves that fact instead of synthesizing a conflicting terminal body. A terminal fact is consumed before a recovered pending-dispatch deadline decision, and an accepted graph worker that expires without a terminal fact is durably closed as timed out before the parent is woken. Dashboard inspection accepts both the historical v1 and current CLI v2 manifests. The ledger provides one execution claim and terminal result, replays completed results after restart, and fails an interrupted ambiguous execution closed instead of rerunning it. Mutation proposals return to the parent runtime's exact approval, apply, and post-apply validation lifecycle before outcome acknowledgement. A supervised wall-clock scanner wakes expired graph delegations autonomously; local resume serialization plus the durable run claimer prevents competing wake transitions. An integrated embedded-NATS regression now drives the persisted parent into a three-child `delegation_join`, leaves two worker claims accepted-only beside one successful terminal, runs the expiry scanner, invokes the real durable resume callback, and verifies the terminal fan-out report plus stable replay identities. |
| PR handoff | Draft PR #87 on `codex/r1-event-outbox` is at `fdc38dc`; its latest verified CI run was green across Linux tests and race checks, Windows tests, Python tests, vet, and staticcheck. Draft PR #86 on `codex/r6-memory-reconciliation` remains parked without further Recall implementation, review, merge, or claims of live Recall reconciliation. |
| Next implementation decision | Hold the live R1 acceptance gate at the configured-provider boundary. Run `run_01M4JDQVRP089S8S9F6417WFA2` proved the bounded planner finalization phase, then `glm-5.3:cloud` exhausted its full 8,192-token reply allowance in private reasoning on developer turn 3 and returned no public content. The backend failed explicitly before proposal creation. Do not raise the cap or weaken the parser; resume acceptance only after the provider can return bounded public content reliably. Then prove the continuous restart-at-approval path and repeat it in the independent Roblox Factory repository snapshot. |
| Estimated focused sessions | The remaining work is blocked on configured-provider output behavior. Once that external condition clears, budget two focused acceptance sessions: one continuous Prizm restart-through-approval/reviewer trace and one independent Roblox Factory preflight-doctor trace, followed by final evidence and PR-readiness verification. This does not complete the broader roadmap or any 9/10 score gate. |
| Evidence required before advancing | The embedded-NATS matrix proves duplicate delivery executes once, forged transport outcomes are rejected, malformed canonical commands terminate as rejected, terminal restart replay does not re-execute, interrupted worker ownership fails closed, publish retry is bounded, and wall-clock expiry wakes and terminates a persisted three-child fan-out parent with a complete event-derived report. Timeout synthesis is owned by the `prizm serve` graph-worker scanner. Trusted terminal notifications now wake the parent even though persistence-before-notification makes the subscriber's insert idempotent. Live run `run_01M4JDQVRP089S8S9F6417WFA2` entered planner finalization with 21,031 tokens remaining, produced one valid strict planner result, and advanced to developer; the developer then failed closed when the provider consumed the complete 8,192-token reply allowance without public content. No proposal, approval, or apply occurred. Before claiming the complete R1 gate, still require a completed reviewer outcome, one continuous restart-while-awaiting-approval trace through apply/validation/terminal completion, and a second independent real-repository acceptance trace. A transient resume error other than `ErrRunClaimed` still waits for the next durable wake and remains tracked as P2 hardening. This is a shared-run-database local trust model; authenticated cross-machine workers remain R3 scope. |

## Update Format

At session end, update the current focus and handoff table, then append one entry
to [Work Log](WORK_LOG.md). Each entry states the roadmap IDs, changes, evidence,
open risks, and next action.

## See Also

- [Prizm Roadmap](../architecture/PRIZM_ROADMAP.md)
- [Work Log](WORK_LOG.md)
- [Multi-Agent Execution Graph](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md)
