# Developer-Authored Workflow Graphs

Status: Phase 3 — Developer-Authored Workflow Graph Runtime

Phase 1 shipped one fixed, hardcoded multi-agent workflow
(`multi-agent-software-task`: planner → developer → tester → reviewer, two
built-in correction loops). Phase 3 lets you author your own role/outcome
workflow graph in YAML or JSON, validate it locally with real
file:line:column diagnostics, compile it into an immutable runtime
representation, test it with scripted (no-model-call) scenarios, register
immutable versions of it, and run it through the exact same
supervisor/persistence/observability machinery Phase 1 already built. It is
not a second workflow engine, a no-code editor, or a scripting surface — see
[Current limitations](limitations.md) for what is deliberately out of scope.

> Prism is source-available and preview-stage. See
> [Capability Status](../reference/CAPABILITY_STATUS.md) before relying on
> any capability described here.

## Where to start

| If you want to... | Read |
| --- | --- |
| Understand the YAML/JSON schema field by field | [Workflow file structure](file-structure.md) |
| Understand outcome-based routing (and what it deliberately does not do) | [Routing and outcomes](routing-and-outcomes.md) |
| Add a correction loop, or understand why an edge needs a `loop:` policy you didn't expect | [Loops and cycles](loops-and-cycles.md) |
| Bound how long/expensive/large a run can get, or gate a role on human approval | [Budgets, approvals, and validation](budgets-approvals-validation.md) |
| Test a workflow without any model calls | [Testing workflows](testing.md) |
| Register immutable versions and understand what "pinning" guarantees | [Registry and versioning](registry-and-versioning.md) |
| Actually run a workflow, or inspect a compiled graph as text/Mermaid/JSON | [Running and inspecting workflows](running-and-inspecting.md) |
| Fix a `prism graph validate` error you don't understand | [Debugging validation errors](debugging-validation-errors.md) |
| Wire `prism graph validate`/`prism graph test` into CI | [CI integration](ci-integration.md) |
| Move off the Phase 1 built-in `multi-agent-software-task` flow | [Migrating from Phase 1](migration-from-phase-1.md) |
| Know what is NOT supported yet | [Current limitations](limitations.md) |
| See this PR's security/hardening review findings | [Security review](security.md) |

See also: [ADR 0002: Graph-defined workflows](../architecture/decisions/0002-graph-defined-workflows.md)
for the architectural decisions behind this feature, and
[Package Boundaries](../architecture/PACKAGE_BOUNDARIES.md) for why this all
lives inside `internal/workflow/multiagent` rather than a new package.

## A complete, runnable example

[`templates/software-delivery.yaml`](../../internal/workflow/multiagent/templates/software-delivery.yaml)
is a full, shipped, working example: the same planner → developer → tester →
reviewer shape as Phase 1's built-in reference workflow, authored by hand.
Two more shipped templates cover a security-review pipeline
([`templates/security-review.yaml`](../../internal/workflow/multiagent/templates/security-review.yaml))
and a purely linear, acyclic documentation pipeline
([`templates/documentation-change.yaml`](../../internal/workflow/multiagent/templates/documentation-change.yaml)).
Every shipped template has a matching, passing scenario fixture under
[`internal/workflow/multiagent/testdata/template-scenarios/`](../../internal/workflow/multiagent/testdata/template-scenarios/)
and a Go regression test
([`template_compat_test.go`](../../internal/workflow/multiagent/template_compat_test.go))
that loads, validates, compiles, and runs every one of them in CI.

Validate it:

```text
$ prism graph validate internal/workflow/multiagent/templates/software-delivery.yaml
internal\workflow\multiagent\templates\software-delivery.yaml: ok
```

Compile it and inspect the compiled graph:

```text
$ prism graph compile internal/workflow/multiagent/templates/software-delivery.yaml
{
  "workflow_id": "software-delivery",
  "workflow_version": "1.0.0",
  "schema_version": "prism.dev/v1alpha1",
  "fingerprint": "…64 hex chars…",
  "entry_node_id": "node:planner",
  ...
}
```

Render it as a diagram (see [Running and inspecting workflows](running-and-inspecting.md)
for the full text/JSON output too):

```text
$ prism graph inspect --file internal/workflow/multiagent/templates/software-delivery.yaml --format mermaid
```

```mermaid
flowchart TD
  node_planner["Planner"]
  node_developer["Developer"]
  node_tester["Tester"]
  node_reviewer["Reviewer"]
  node_terminal_completed["Completed"]
  node_terminal_failed["Failed"]
  node_planner -->|plan_ready| node_developer
  node_developer -.->|implementation_ready (loop)| node_tester
  node_developer -->|blocked| node_terminal_failed
  node_tester -.->|tests_failed (loop)| node_developer
  node_tester -.->|tests_passed (loop)| node_reviewer
  node_reviewer -.->|changes_requested (loop)| node_developer
  node_reviewer -->|review_approved| node_terminal_completed
```

Run its scripted test scenarios (no model calls, see [Testing workflows](testing.md)):

```text
$ prism graph test internal/workflow/multiagent/testdata/template-scenarios
[PASS] ...template-scenarios\software-delivery-happy-path.yaml :: happy-path
[PASS] ...template-scenarios\software-delivery-happy-path.yaml :: one-correction-loop-of-each-then-approved
[PASS] ...template-scenarios\software-delivery-loop-exhaustion.yaml :: tester-loop-exhausted
...
```

Run it for real (local-dev mode, validates + compiles + registers + runs a
local file in one step — see [Running and inspecting workflows](running-and-inspecting.md)
for registry-backed `--workflow/--version` mode):

```text
$ prism graph run --file internal/workflow/multiagent/templates/software-delivery.yaml \
    --input task.json --workspace ./my-project --config prism.yaml
```
