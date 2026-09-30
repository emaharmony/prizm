# Routing and Outcomes

A role finishes by returning a `RoleRunResult` carrying a single
`TransitionOutcome` — a plain, workflow-scoped string, not a fixed Go enum
(see [ADR 0002](../architecture/decisions/0002-graph-defined-workflows.md)).
The runtime resolves exactly one declared edge for `(current role, that
outcome)` and takes it. There is no "the model chooses the next node"
behavior anywhere in this system — an agent proposes an outcome, and the
compiled graph's routing table (`CompiledGraph.Resolve`) is the only thing
that ever decides where execution goes next. This is the same authority
model [ADR 0001](../architecture/decisions/0001-runtime-owns-authority.md)
already established for Phase 1; Phase 3 generalizes the vocabulary, not the
authority boundary.

## The routing model

- Each role node declares `allowedOutcomes: [...]` — the exhaustive set of
  outcome values that role's runner may legally return.
- Each edge declares `from`, `to`, and `when.outcome` — "when the node named
  `from` returns exactly this outcome, go to `to`."
- At most one edge may resolve for a given `(from, outcome)` pair unless
  edges are disambiguated by distinct `priority` values (see
  [File structure](file-structure.md#specedges-schemaedge)); an ambiguous,
  same-priority collision is a hard validation error
  (`routing.ambiguous-transition`).
- Every outcome a node declares in `allowedOutcomes` must either be routed
  by some edge, or `spec.defaults.failure.defaultOnUnhandledOutcome: fail`
  must be set — an unroutable declared outcome is otherwise a hard error
  (`routing.unhandled-outcome`), so a workflow can never silently strand a
  run at a role whose outcome nothing handles.

## The explicit non-goal: no expression language

`SchemaEdge.when` is `{outcome: string}` and nothing else. There is no
condition-term list, no `if`, no comparison operator, no access to task
metadata or role output content in a routing decision — a routing edge can
only ever be selected by an exact outcome match. This was a deliberate scope
decision for this pass (see the Phase 3 plan's decision #3): building an
expression-evaluation surface into the schema would mean interpreting
developer-supplied logic as part of routing, which is exactly the kind of
authority-boundary risk [ADR 0001](../architecture/decisions/0001-runtime-owns-authority.md)
exists to prevent, and none of the three shipped templates need it. See
[Current limitations](limitations.md) — this can be revisited later without
a breaking schema change (`SchemaCondition` is its own type specifically so
it can grow a second field later), but adding it is a real design decision,
not a small patch, and has not been made.

If you find yourself wanting a condition beyond "which outcome did the role
return," the usual answer is: add another outcome. A role that might succeed
in two meaningfully different ways for the purposes of routing should
return two different outcome values, not one outcome plus a side channel a
routing edge inspects.

## Worked example

From [`templates/security-review.yaml`](../../internal/workflow/multiagent/templates/security-review.yaml):

```yaml
- id: security-scanner
  type: role
  role: security-scanner
  allowedOutcomes:
    - findings_ready
    - critical_finding
```

```yaml
edges:
  - id: scanner-to-reviewer
    from: security-scanner
    to: security-reviewer
    when: { outcome: findings_ready }
  - id: scanner-to-human-review
    from: security-scanner
    to: needs-human-review
    when: { outcome: critical_finding }
```

The scanner role does not "decide to route to human review" — it returns
the outcome `critical_finding`, and the compiled graph's routing table is
what actually sends the run to the `needs-human-review` terminal.
