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

## Experimental Freewill Program (FW) — Integrated Delivery Plan

**Status: planned; feature disabled by default.** This FW track extends, and does
not replace, priorities **R1–R8** above. R1 remains the current platform work
priority; these FW slices are not proof that R1 or any 9/10 gate has passed.

[Detailed Freewill implementation roadmap](FREEWILL_IMPLEMENTATION_ROADMAP.md)
and [locked Freewill design specification](../concepts/PRIZM-FREEWILL-MODE-DESIGN.md)
are the companion planning documents. Follow
[Freewill agent handoff](../operations/FREEWILL_AGENT_HANDOFF.md) for the
next eligible task and verification checklist.

**Invariant:** Freewill can originate an `IntentionRequest`; the existing Prizm
runtime alone owns permission, approvals, budget, execution and audit. The
existing interactive **Freedom / Free Mode** is *not* Freewill, and its trusted
owner privileges may never be inherited by an agent-originated request.

### Unified development order: work lanes and eligibility

Tasks are ordered **within each dependency lane**, not as a mandatory global
serial queue. An agent may work on independent FW read-only planning while R1
is still being closed; it must not start write-capable Freewill until the named
gates are **verified** on the implementation branch.

| Order / lane | Roadmap IDs | Deliverable | Can begin when | Completion evidence / blocking gate |
| --- | --- | --- | --- | --- |
| **1 — Platform priority** | **R1** | Exact approval identity, canonical durable command/outcome, idempotent outbox, governed worktree execution, tested recovery and delegation | Active now | Real provider/repo trace; approval → apply → verify → resume; restart/duplicate/lost-worker injection; no unauthorized effects. Merging a PR is not the whole R1 acceptance gate. |
| **2 — Parallel safe start** | **FW-P0 / FW-001** | Freewill ADR, threat model, state machine, typed intention and candidate contracts, feature flag off by default | Now, independent of R1 finish | Schema/state tests; no change to existing gated or Freedom behavior; runtime-owned authority documented. |
| **3 — Parallel safe prototype** | **FW-P1 / FW-002–004** | Allowed wake sources, three new candidates plus optional resume, sleep, read-only selection and *policy-only* proposal evaluation | FW-P0 accepted; existing provider/scheduler paths available | `dry-run` never writes or calls side-effect tools; duplicate wakes do not double-select; full decision trace. Command name is proposed. |
| **4 — Platform memory foundation** | **R2 → R6** | Scoped capture/retrieval/supersession and eventual Recall reconciliation | Existing R1 event contract and Recall reliability, according to R2/R6 gates | Scope-leak regression tests, local fallback, durable reconciliation test. Local-first FW factual memory may proceed after R2 scope integrity before full R6. |
| **5 — Execution safety gate** | **R1 + FW-G2** | Validate governed execution, workspace isolation, authorization, budgets, halt/revocation, network/secret boundaries | R1 acceptance evidence plus FW-P1 | Negative security tests and real isolated run; no shell-string-only sandbox; interactive Free Mode cannot elevate Freewill. **Hard blocker before FW-P2.** |
| **6 — First autonomous work** | **FW-P2** | Authorized small intention → existing governed workflow → validated evidence-based report | Step 5 complete | Approval/edited-scope checks; denied intent never executes; exactly one authorized mutation in an isolated workspace. |
| **7 — Durable autonomy** | **FW-P3** | Multi-cycle intention lifecycle, checkpoints, leases, restart reconciliation, graceful disable/emergency halt | FW-P2 + verified R1 recovery | Restart at every transition; ambiguous side effects reconciled rather than blindly retried. |
| **8 — Learning foundation** | **FW-P4 → FW-P5** | Distinct facts/self-reports/versioned diary, scoped local memory, enjoyment signals, bounded reflection and evidence-calibrated preferences | FW-P1 + trustworthy scope/memory evidence (R2); FW-P4 precedes P5 | Contradictions preserved; weak preferences stay tentative; no claims of subjective experience; telemetry never grants tool authority. |
| **9 — Goals and teamwork** | **FW-P6** | Adaptive priorities, preemption, commitments, learned goals, authorized collaboration | FW-P3 + FW-P5; R1 delegation evidence for collaborative tasks | Promises require acceptance to change; paused work resumes; delegation cannot widen permissions. |
| **10 — Social security/privacy** | **FW-P7** | Verified roles/identity, scoped diary sharing, relationship consent and forgiveness | FW-P4 + audited identity/privacy gates | Role spoof, cross-user leakage, access expiry, consent withdrawal and deletion boundaries tested. |
| **11 — Research evaluation** | **FW-P8** | Controlled longitudinal comparisons, snapshots, metrics, reproducibility | FW-P5 minimum; P6/P7 for richer social studies | Repeatable control/experimental runs, provenance, uncertainty reports and privacy review. |
| **Parallel existing work** | **R3/R4, R5/R7, R8** | Remote agents/adapters, skill versioning, champion/challenger | Keep each original R1/R2/R6/R7 dependency; **not global blockers** for decision-only Freewill | Original R1–R8 acceptance criteria above; R8 PR creation always separately approved. |

**Earliest unblocked Freewill task: `FW-001` (architecture and schemas).** Before
selecting it, verify whether a current developer/PR already owns it. Do not
replace active R1 work or start a second competing implementation.

### Freewill gate rules for automated task selection

1. Read `AGENTS.md`, this roadmap, `PLAN_OF_ACTION.md`, latest `WORK_LOG.md`,
   package-boundary policy, and the companion Freewill spec at the **same target
   SHA**. Detect stale status and existing feature branches/PRs first.
2. Select the **first eligible incomplete** task whose stated dependencies are
   supported by recorded evidence; consider R1 active priority before new work.
   A design-only slice may proceed in parallel when it touches separate files.
3. Make one narrowly scoped PR per testable vertical slice. Keep Freewill off by
   default. The agent must **never** infer policy authorization from a checklist
   or from its own generated intentions.
4. Pass targeted tests and project-wide applicable checks; record exact commands,
   outcomes and limitations. A merged code PR is not sufficient to mark an R1,
   R2/R6 or FW safety gate completed.
5. If a prerequisite fails, **stop at that gate**, capture evidence and work on
   the prerequisite rather than skipping ahead. For uncertain effects, fail
   closed and request human review.
6. Update `PLAN_OF_ACTION.md` and append `WORK_LOG.md` with roadmap IDs, SHA,
   branch, evidence, blockers and next eligible task at each implementation
   handoff. Do not backdate work or claim tests not executed.

### Freewill release boundaries

- **Decision-only milestone:** FW-P0 + FW-P1; no external/FS mutations, including
  from triggers, memory injection or model-generated tool requests.
- **Owner-controlled MVP:** FW-P2 + FW-P3 after R1 and sandbox gates; bounded,
  governed autonomous tasks and safe recovery, still opt-in and experimental.
- **Developmental agent milestone:** FW-P4 + FW-P5 + FW-P6; subjective reports
  remain separate from verified observations and learned interests affect choice
  only, never authority.
- **Multi-user / research milestone:** FW-P7 + FW-P8; only after consent, RBAC,
  privacy and experimental measurement protocols pass review.

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
- [Freewill Implementation Roadmap](FREEWILL_IMPLEMENTATION_ROADMAP.md)
- [Freewill Agent Handoff](../operations/FREEWILL_AGENT_HANDOFF.md)
- [Work Log](../operations/WORK_LOG.md)
- [Multi-Agent Execution Graph](MULTI_AGENT_EXECUTION_GRAPH.md)
- [Historical Roadmap](../history/ROADMAP.md)