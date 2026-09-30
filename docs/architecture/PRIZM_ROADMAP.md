# Prizm Roadmap

This is the authoritative current roadmap for the coding and autonomous-agent
harness. It translates the product goals into sequenced, observable work.

> Prizm is source-available under an all-rights-reserved [license](../../LICENSE)
> and is preview-stage. Current capability maturity is recorded in
> [Capability Status](../reference/CAPABILITY_STATUS.md).

## Target

Prizm must let a user state a task, clarify and gather authorized context, plan,
execute bounded work, and report evidence. It must coordinate specialist agents
and remote Prizm workers through durable, observable events.

"9/10" means each score below is demonstrated on a real provider and real Git
repository, not inferred from mocks or design documents.

| Dimension | Audited baseline | 9/10 gate |
| --- | ---: | --- |
| Reasoning | 5/10 | Clarify, context, plan, execute, verify, and evidence-backed report complete across 10 representative tasks; at least 9 complete without an operator correcting an internal transition. |
| Reflection | 3/10 | A failed verification produces a bounded diagnosis and revised plan; the revised plan passes or escalates with evidence in at least 9 of 10 injected failures. |
| Autonomous delivery | 3/10 | 9 of 10 approved code tasks complete in isolated worktrees with project-aware validation, an inspectable diff, and deterministic pause/retry/resume behavior. |
| User memory | 5/10 | Scoped preferences and task context are recalled correctly in 9 of 10 seeded cases; stale or denied context is never returned across scope boundaries. |
| Recovery | 7/10 | Restart, duplicate delivery, worker loss, and approval interruption recover or fail closed with a terminal, queryable event in 9 of 10 trials. |

The final gate additionally requires a two-node Prizm run over Tailscale with
failure injection (lost worker, duplicate event, restart during approval, and
unreachable remote node) and a complete event trace for every outcome.

## Current Audit

| Area | Status | Evidence and gap |
| --- | --- | --- |
| Durable role graph and observable run state | Partial | Persisted graph state, events, checkpoints, and operator controls exist. The primary proposal approval does not carry the exact approval through apply and resume. |
| Coding execution | Partial | Proposal tools and validation exist, but the primary graph can advance without applying the approved proposal; its default validation is not project-aware. |
| Event-driven autonomy | Partial | Graph events and NATS event paths are separate; there is no single durable outbox/consumer contract for autonomous actions. |
| Local delegation | Partial | NATS delegation exists, but plain delivery can drop or fail silently and lacks the required end-to-end acknowledgement/recovery contract. |
| Interaction policy | Missing for production autonomy | Production defaults use `FirstLegal` action selection and `Allow` authorization; they need task-aware routing and explicit policy gates. |
| Recall integration | Partial | Prizm has V1 context and no proven local-memory fallback. Recall/Remembrance has Context Pack V2, scoped retrieval, approved versioned skills, and JetStream ingestion; its Phase 0 reliability and idempotency work remains pending. |
| Skills | Partial | A skill registry and daily-use capability exist, but the registry is unversioned and cannot consume Recall skill versions. |
| Self-improvement | Partial | Autopatch/auto-PR capability exists, but its policy permits categories that must instead require an explicit user approval before a PR is created. |

## Priorities

| ID | Priority | Status | Dependency | Acceptance |
| --- | --- | --- | --- | --- |
| R1 | Multi-agent coding, parallel delegation, and event-observable reports | Partial | Durable approval identity, worktrees, project validation, event contract | A planner delegates parallel research/code/review tasks; every lifecycle transition and final report is queryable; an approved code task completes pause → apply → verify → resume in an isolated worktree. |
| R2 | Local memory system integration | Partial | Scoped memory contract and event producers | Local capture, retrieval, supersession, and fallback are event-driven, scoped to user/project/task, and pass the memory score gate. |
| R3 | Prizm-to-Prizm communication and delegation over Tailscale | Partial | R1 event/ack contract; node identity and trust policy | A coordinator assigns bounded work to a specialized remote node, receives an authenticated completion, and recovers from node loss without silent timeout. |
| R4 | Easy adapters and MCP actions connected to the event bus | Partial | Unified event/outbox contract and typed intake | An adapter or MCP tool can declare an event trigger, execute only through policy, deduplicate delivery, and emit an auditable outcome. |
| R5 | Daily skill discovery and use | Partial | R6 context and R7 version metadata | Agents discover, select, apply, and report the approved skill version for routine work. |
| R6 | Recall as primary memory with local fallback and shared scoped task context | Partial | Recall Phase 0 reliability/idempotency; R2 memory contract | Recall supplies Context Pack V2 and scoped shared task context; an outage automatically uses local memory and later reconciles without scope leakage. |
| R7 | Recall skill versioning | Missing in Prizm | R5 skill-use path; Recall approved version API | Prizm resolves an approved immutable skill version, records it in run evidence, and supports a safe upgrade/rollback policy. |
| R8 | Champion/challenger self-improvement | Partial | R1 evidence, R6/R7 provenance, approval policy | Evaluations detect a material regression, compare champion and challenger, generate a report and proposed patch, and wait for explicit user approval before creating a PR. |

## Delivery Sequence

1. **R1 foundation:** Connect each proposal to its exact approval ID and durable
   pause → apply → verify → resume path. Make runs worktree-isolated and select
   validation from project configuration.
2. **R1 event contract:** Replace split graph/NATS action paths with a typed,
   durable outbox and idempotent consumers; add acknowledgements, terminal
   failure events, and an event-derived run report.
3. **R1 delegation:** Add task routing, parallel fan-out/fan-in, capability
   matching, deadlines, and failure-safe completion before expanding to R3.
4. **R2 and R6:** Define a scoped memory event schema, integrate Recall as the
   primary backend, retain local fallback, and expose a shared task-context
   search surface to authorized agents.
5. **R3 and R4:** Add authenticated node registration and remote execution over
   Tailscale, then allow adapters/MCPs to subscribe and publish typed event
   actions under policy.
6. **R5 and R7:** Consume approved Recall skill versions in the skill runtime
   and retain version provenance in every run.
7. **R8:** Build the champion/challenger evaluation loop on the evidence,
   memory, and skill provenance created above. PR creation always remains a
   separately approved action.

## Operating Rules

- Events describe facts; commands request work. Consumers must be idempotent and
  publish a terminal outcome for every accepted command.
- Every autonomous mutation is bounded by task, workspace, budget, policy,
  approval, and validation evidence.
- Shared context is scoped by user, project, task, and authorization. It is not a
  global prompt dump.
- A current session updates the plan and appends a log entry that cites these
  roadmap IDs.

## See Also

- [Current Plan of Action](../operations/PLAN_OF_ACTION.md)
- [Work Log](../operations/WORK_LOG.md)
- [Multi-Agent Execution Graph](MULTI_AGENT_EXECUTION_GRAPH.md)
- [Historical Roadmap](../history/ROADMAP.md)
