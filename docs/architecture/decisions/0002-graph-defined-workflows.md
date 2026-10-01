# ADR 0002: Graph-defined workflows

Status: Accepted

Phase 1 hardcoded exactly four roles and eight outcomes as Go enum switches,
with one fixed routing table and two fixed correction loops. Phase 3 opens
that up: `Role` and `TransitionOutcome` stay `type X string`, but validity is
no longer a property of the Go type system — it is computed once, per
compiled definition, from each node's declared `allowedOutcomes`
(`CompiledGraph.HasRole`/`HasOutcome`/`OutcomeValidFor`). A developer authors
a `WorkflowDefinition` in YAML/JSON with whatever role and outcome
vocabulary its workflow needs; `ValidateDefinition` and `Compile` are what
now enforce well-formedness, not a closed switch statement.

`CompiledGraph` is the single immutable representation the supervisor
executes. It is the only thing `Supervisor`/`DurableRuntime` know how to run,
produced by exactly one of two paths: `Compile` (new, authored YAML/JSON) or
`CompatAdaptDefinition` (existing, the legacy `Definition`/`RoleConfig`/
`TransitionRule` shape). Those legacy types are kept alive permanently, not
deleted or migrated — they remain the shape embedded in every historical run
manifest, and `CompatAdaptDefinition` is the only place that will ever read
`Definition`'s two Phase-1-specific loop-limit fields
(`MaxTesterToDeveloperLoops`/`MaxReviewerToDeveloperLoops`) again. This is
what makes Phase 1 compatibility unconditional: an already-persisted run
resumes through the exact same `CompiledGraph` machinery a brand-new
authored workflow runs through, with zero data migration.

This stays inside `internal/workflow/multiagent`, not a new package. The
package already owns the multi-agent role/outcome execution graph end to
end — its definition, validation, compilation, and supervised execution —
and the new authored-schema path is an extension of that same domain, not a
different one. `internal/workflow`'s linear step-sequence model has no
typed-outcome routing or bounded correction loops; `internal/workflow/v2` is
a distinct, Discord-facing approval-gating product surface with its own
event/state model. Neither owns this invariant. The definitions registry
(`definition_store.go`) lives here too, for the same reason
`durable_sqlite_store.go` already does: a SQLite representation of domain
state belongs at the storage boundary, but the domain meaning stays with the
domain that owns it.

One deliberate, load-bearing tradeoff: cycle validation uses Tarjan's
strongly-connected-components algorithm, not enumeration of every simple
cycle. Tarjan finds every cycle in linear time; enumerating simple cycles is
combinatorially worse and unnecessary for the actual rule being enforced
("can this edge participate in unbounded repetition"). The consequence is
SCC-wide, not edge-wide: every edge inside a cyclic strongly-connected
component must carry a bounded `loop:` policy, including edges a human would
never call a "loop" — a straight-line "developer finishes, so we move on to
testing" edge is swept in in a Planner→Developer→Tester→Reviewer graph with
two correction loops back to Developer, because Developer/Tester/Reviewer
all end up in one strongly-connected component together. This is recorded
here as a deliberate design decision, not a footnote: it directly shapes how
a developer must author a bounded workflow, and every shipped template
(`templates/software-delivery.yaml`, `templates/security-review.yaml`)
carries an explicit comment at the point this applies. See
[Loops and cycles](../../workflows/loops-and-cycles.md) for the developer-facing
explanation and a worked example.
