# Multi-Agent Runtime Roadmap

Status: Architectural direction

This document describes the sequence by which Prizm can evolve from an
event-native workflow runtime into a workflow-first multi-agent operating
system. It defines goals and architectural outcomes. It intentionally does not
select package names, APIs, event fields, storage schemas, scheduling
algorithms, protocols, UI frameworks, or deployment technology.

Each stage builds on the guarantees of the stages before it. A later milestone
must not bypass policy, approval, validation, audit, or other runtime-owned
authority in order to achieve greater autonomy or scale.

```mermaid
flowchart LR
  current["Current architecture"] --> supervisor["Multi-Agent Supervisor"]
  supervisor --> loop["Loop Runtime"]
  loop --> graph["Graph Runtime"]
  graph --> editor["Visual Graph Editor"]
  editor --> parallel["Parallel Execution"]
  parallel --> distributed["Distributed Runtime"]
```

## Architectural invariants

The following remain true throughout the roadmap:

- Workflows are the primary expression of coordinated work.
- The framework, not a model, owns lifecycle and effects.
- Models and agents may propose actions but may not grant themselves authority.
- Policy, approval, validation, execution, and persistence remain explicit and
  auditable.
- Events provide an observable system record; transport does not define domain
  meaning.
- Local operation remains a complete and supported mode as more advanced
  execution models become available.
- New code follows the bounded-domain rules in
  [Package Boundaries](PACKAGE_BOUNDARIES.md).

## 1. Current architecture

Prizm today is a persistent, event-native Go runtime with:

- named workflows and gated execution;
- canonical events delivered through NATS;
- SQLite-backed sessions, tasks, approvals, events, and run artifacts;
- policy-governed and approval-gated tool use;
- allowlisted validation;
- providers and tool adapters;
- Remembrance integration for memory;
- API, dashboard, and workflow editor surfaces; and
- bounded sub-agent delegation with capability scoping and worktree isolation.

This is the foundation, not a legacy phase to bypass. It establishes the
authority, audit, persistence, and workflow semantics on which multi-agent
coordination must rely.

Architectural outcome: one runtime can execute and observe governed workflows,
including bounded delegated work, with effects controlled by the framework.

## 2. Multi-Agent Supervisor

Goal: make a team of agents a first-class, governed runtime concern.

The supervisor stage establishes a coherent coordination boundary for agent
identity, roles, capabilities, assignments, delegation relationships, progress,
and results. It enables the runtime to understand a unit of work that spans
multiple agents without treating each delegation as an unrelated task.

The supervisor must preserve a traceable chain from the originating workflow to
every assignment and result. Delegation may narrow authority but may not widen
it. Human decisions remain human decisions, and the supervisor remains subject
to the same policy, validation, persistence, and audit rules as any other
runtime path.

Architectural outcome: Prizm can supervise a bounded multi-agent team as one
observable unit of work with explicit responsibility and authority.

## 3. Loop Runtime

Goal: make iterative agent work an explicit, reusable lifecycle.

The loop runtime gives repeated reasoning and action a common architectural
meaning across workflows and supervised agents. A loop has a declared
objective, observable progress, bounded resources, completion criteria, and
terminal outcomes. It can distinguish useful continuation from completion,
failure, cancellation, exhaustion, or lack of progress.

This stage separates the concept of iteration from any one workflow,
provider, agent type, or user interface. A supervisor can observe and govern a
loop without becoming the owner of its execution semantics.

Architectural outcome: iterative work is bounded, resumable where promised,
auditable, and governed consistently rather than being embedded as ad hoc
control flow in individual features.

## 4. Graph Runtime

Goal: represent workflow structure beyond a single ordered sequence.

The graph runtime gives Prizm a canonical model for work with dependencies,
branches, joins, nested coordination, and explicit terminal states. Graph
meaning is independent of how a graph is authored, displayed, or scheduled.
The model must make authority gates and observable state transitions part of
the workflow rather than optional annotations.

Loops and supervised agent work become composable workflow capabilities. The
graph defines their relationships without absorbing the semantics owned by the
workflow, agents, policy, approval, or validation domains.

Architectural outcome: Prizm can validate, execute, inspect, and explain the
same workflow graph with unambiguous lifecycle and dependency semantics.

## 5. Visual Graph Editor

Goal: make the canonical graph understandable and authorable by operators.

The editor presents the same graph contract the runtime executes. It supports
round-trip fidelity: a valid workflow does not change meaning when viewed and
saved, and an invalid workflow cannot appear executable merely because it is
visually well formed.

The visual surface exposes dependencies, agent responsibilities, loops,
governance gates, validation, progress, and terminal states. It improves human
control and comprehension; it does not become a separate source of runtime
truth or authority.

Architectural outcome: operators can create, review, validate, and inspect
workflow graphs without divergence between the visual and runtime models.

## 6. Parallel Execution

Goal: allow independent graph work to progress concurrently while preserving
deterministic workflow meaning.

Parallelism must respect declared dependencies, resource and budget limits,
cancellation, failure propagation, approvals, validation, and audit. The
observable result of a join must be defined by graph semantics rather than by
incidental completion timing.

This stage follows the graph and editor stages because concurrency magnifies
ambiguity. Prizm must first be able to express and inspect independence,
coordination, and terminal behavior clearly.

Architectural outcome: Prizm can increase throughput for independent work
without weakening reproducibility, governance, or operator understanding.

## 7. Distributed Runtime

Goal: preserve Prizm's runtime guarantees when coordinated work spans multiple
runtime instances.

The distributed stage establishes clear ownership of work, agent identity,
authority, state, and recovery across failure and connectivity boundaries.
Operators can trace a distributed workflow as one governed execution even when
its work is placed in different runtime locations.

Distribution must not create a trusted bypass around local policy or conceal
partial failure. Local-first operation remains valid; distribution is a scale
and placement capability, not a prerequisite for correctness.

Architectural outcome: multiple Prizm runtimes can participate in one coherent,
observable workflow while maintaining explicit authority and failure semantics.

## Stage gates

Progression is architectural, not calendar-driven. A stage is ready to support
the next stage when its outcome is stable enough that the next stage does not
need to invent a competing model.

| Stage | Required architectural clarity |
| --- | --- |
| Multi-Agent Supervisor | Agent responsibility, delegation authority, and team-level lifecycle are explicit |
| Loop Runtime | Progress, bounds, termination, cancellation, and recovery have common meanings |
| Graph Runtime | Dependencies, branches, joins, nesting, gates, and terminal states are canonical |
| Visual Graph Editor | Visual and runtime representations have semantic parity |
| Parallel Execution | Independence, coordination, failure propagation, and observable results are deterministic |
| Distributed Runtime | Work ownership, identity, authority, state, and recovery survive location boundaries |

## Decisions deliberately deferred

This roadmap does not decide:

- whether a stage requires a new package;
- concrete Go interfaces or types;
- event subjects or payload schemas;
- database or artifact formats;
- scheduling, concurrency, or consensus mechanisms;
- network topology or service boundaries;
- editor technology;
- compatibility and migration mechanics; or
- milestone dates.

Those decisions belong in later design proposals after the preceding
architectural stage is understood. Any proposal that introduces a package must
include the justification required by
[Package Boundaries](PACKAGE_BOUNDARIES.md).
