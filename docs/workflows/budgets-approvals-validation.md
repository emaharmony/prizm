# Budgets, Approvals, and Validation Gates

These three mechanisms bound *how* a workflow is allowed to run — costs,
time, human sign-off, and output quality — independently of *what path*
routing selects. All three flow through unchanged into Prism's existing
`internal/tool`/`internal/policy`/`internal/approval`/`internal/validation`
enforcement authorities: a workflow definition only ever *declares* budgets,
approval requirements, and validation profiles — it never bypasses those
authorities itself (see
[ADR 0001](../architecture/decisions/0001-runtime-owns-authority.md)).

## Budgets

`spec.budgets` bounds the whole run; `spec.nodes[].maxVisits` /
`localIterations` / `tokenBudget` / `durationBudget` bound one node. See
[File structure](file-structure.md) for the full field list.

- Every numeric limit follows the existing `Limit` sentinel convention:
  a positive integer, or exactly `-1` for "unlimited." Zero is invalid
  (`budget.invalid-limit`) — an omitted or zero value can never silently
  mean "no bound."
- A node-level limit stricter than the matching global ceiling is fine (it
  just binds first). A node-level limit *looser* than a finite global
  ceiling is a warning (`budget.contradictory-limit`) — it can never
  actually bind, since the global ceiling always triggers first.
- Loop `maxTraversals` is the one dimension that can never be `-1` — see
  [Loops and cycles](loops-and-cycles.md).
- Size caps (`CompileOptions{MaxNodes: 500, MaxEdges: 2000,
  MaxDiagnostics: 1000}` by default) are enforced **before** validation
  ever runs, so a pathologically large definition fails fast with a single
  `DefinitionTooLargeError` rather than paying for a full Tarjan SCC pass
  first. See [Security review](security.md).

## Approvals

```yaml
- id: developer
  type: role
  approval:
    required: true
    approvers: [reviewer]
```

- `spec.defaults.approval.approvers` sets a workflow-wide default list,
  inherited by any node whose own `approval.approvers` is unset.
- **No self-approval**: a node cannot appear in its own `approvers` list
  (`governance.self-approval`, a hard error) — this generalizes the same
  check `Definition.Validate()` already enforces for the Phase 1 built-in
  workflow (`config.go`), so a mutating role can never approve its own
  work under either schema.
- An approver name that doesn't match any declared node is only a warning
  (`governance.unknown-approver`), since an approver may legitimately name
  an external human identity that was never modeled as a graph node.
- Approval *enforcement* itself (waiting for a decision, granting/denying)
  is `internal/approval`'s existing authority, reached through
  `DurableRuntime` exactly as it is for the Phase 1 built-in workflow — this
  schema only declares the requirement.

## Validation gates

```yaml
- id: tester
  type: role
  validation:
    profiles: [go_test_all]
```

`validation.profiles` names the allowlisted validation profiles
(`internal/validation`) a role's output must pass. `spec.defaults.validation.profiles`
sets a workflow-wide default. Profile **names** are checked for shape only
by default; checking that every named profile actually exists in a live
`validation.Registry` is opt-in
(`ValidateDefinitionWithProfiles(def, idx, knownProfiles)`) rather than the
default `ValidateDefinition` path — this is deliberate, so `prism graph
validate` keeps working with zero running services, matching the CI
requirement that validation never needs a live process to check syntax and
structure. Profile resolution against a live registry happens at run time.

## Agent profiles

```yaml
- id: developer
  type: role
  agentProfile: developer-default
```

`agentProfile` maps 1:1 onto the existing `RoleConfig.AgentRef` field and is
resolved against Prism's existing agent registry via
`AgentProfileResolver`/`RegistryProfileResolver`
(`internal/workflow/multiagent/agent_adapter_types.go`) — the same
resolution mechanism the Phase 1 built-in workflow already uses. A role
node's `agentProfile` is required and checked for non-emptiness at validate
time (`governance.missing-agent-profile`), but **whether that name actually
resolves to a registered agent is a run-time concern, not a load-time
one** — `prism graph validate`/`prism graph compile` never need a live agent
registry, matching every other "no running services required for CI" design
choice in this schema. An unresolvable `agentProfile` surfaces as a run-time
error the first time that role is entered, not as a validation diagnostic.
