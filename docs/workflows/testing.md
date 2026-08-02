# Testing Workflows

`prism graph test` runs scripted routing scenarios against a compiled graph
with **zero model calls** — a `ScriptedRoleRunner` consumes a fixed sequence
of canned outcomes instead of invoking any real agent/provider. Scenario
fixtures are plain YAML/JSON data, not Go code, so testing a workflow
requires no access to Prism's Go test package or toolchain at all — this is
the point: an external developer authoring a workflow template gets a real
test framework for free.

## Scenario fixture shape

A scenario fixture (`ScenarioFile`, `internal/workflow/multiagent/scenario_types.go`)
names the workflow it exercises and a list of scenarios:

```yaml
workflow: ../../templates/software-delivery.yaml   # relative to this file's own location
scenarios:
  - name: happy-path
    input:
      id: task-happy-path
      description: ship the feature with no correction loops
    script:
      - role: planner
        outcome: plan_ready
      - role: developer
        outcome: implementation_ready
      - role: tester
        outcome: tests_passed
      - role: reviewer
        outcome: review_approved
    expect:
      finalStatus: completed
      terminalNode: "node:terminal:completed"
      nodeVisits:
        planner: 1
        developer: 1
        tester: 1
        reviewer: 1
```

`workflow` is resolved relative to the **scenario file's own directory**,
not the process's working directory — see
[the note on directory layout](#why-scenario-fixtures-live-in-a-separate-directory-from-workflow-definitions)
below before you decide where to put your own fixtures.

### `script[]`

One entry consumed per role execution, in order. If the supervisor requests
a different role than the script's next entry names, that's a clear,
specific failure ("the scripted scenario has fallen out of sync with the
workflow's actual routing"), not a panic.

| Field | Meaning |
| --- | --- |
| `role` | Must match the role the supervisor actually requests next. |
| `outcome` | The canned `TransitionOutcome` this step returns. |
| `localIterations` | Defaults to 1 if omitted/zero. |
| `tokenUsage` | `{prompt, completion}` — optional, for budget-focused scenarios. |
| `validationStatus` / `approvalStatus` | Optional, for scenarios asserting on `ValidationFailed`. |
| `failWith` | If set, this step returns an error instead of an outcome (simulates a role execution failure). |
| `cancel` | If `true`, cancels the run's context before returning (simulates external cancellation). |

### `expect`

Every field is optional — a scenario only asserts on the dimensions it sets.
See `ScenarioExpectation`'s doc comment
(`internal/workflow/multiagent/scenario_types.go`) for the exact
field-to-signal mapping; the headline ones:

| Field | Compared against |
| --- | --- |
| `finalStatus` | `RunState.Status` (`"completed"`, `"failed"`, `"cancelled"`, `"budget_exhausted"`). |
| `terminalNode` | The resolved terminal node's stable ID, e.g. `"node:terminal:completed"` (uses the node's declared `terminalCondition`, not its `id`). |
| `transitions` | The exact ordered sequence of stable edge IDs traversed, e.g. `"edge:tester:tests_failed"`. |
| `nodeVisits` | Per-role visit counts. |
| `edgeTraversals` | Per-edge traversal counts. |
| `budgetExhausted` / `loopExhausted` | The `BudgetExceededError.Budget` name a run ended with — see the note on loop budget names below. |
| `invalidOutcome` | Whether the run ended with an `*InvalidTransitionError`. |
| `validationFailed` | Whether any role state has `validation_status: "failed"`. |
| `approvalWaiting` | **Best-effort.** `prism graph test` drives `Supervisor.Run` directly, which executes a role synchronously start-to-finish and never itself pauses mid-role for approval — that's a `DurableRuntime`-only concept this in-memory path cannot produce. |

### Loop budget names in `loopExhausted`

If your workflow's role/outcome vocabulary happens to exactly match Phase
1's built-in correction loops (`tester`→`developer` on `tests_failed`, or
`reviewer`→`developer` on `changes_requested`), the exhausted loop's budget
name is the same backward-compatible slug Phase 1 always used —
`max_tester_to_developer_loops` / `max_reviewer_to_developer_loops` — not a
generic edge-ID-shaped name. Any other loop uses the generic form:
`"max_" + <stable edge ID> + "_loops"`, e.g.
`max_edge:security-reviewer:issues_found_loops`. See
[`testdata/template-scenarios/software-delivery-loop-exhaustion.yaml`](../../internal/workflow/multiagent/testdata/template-scenarios/software-delivery-loop-exhaustion.yaml)
and
[`testdata/template-scenarios/security-review-loop-exhaustion.yaml`](../../internal/workflow/multiagent/testdata/template-scenarios/security-review-loop-exhaustion.yaml)
for one worked example of each form.

## Running scenarios

```text
$ prism graph test <file|dir>... [--json]
```

Each positional argument may be a scenario file or a directory (walked
recursively for `.yaml`/`.yml`/`.json` files). For each file: load it,
resolve+compile its referenced workflow **once**, then run every scenario in
the file against that one compiled graph. Exits non-zero if any scenario in
any file fails.

```text
$ prism graph test internal/workflow/multiagent/testdata/template-scenarios
[PASS] ...software-delivery-happy-path.yaml :: happy-path
[PASS] ...software-delivery-happy-path.yaml :: one-correction-loop-of-each-then-approved
[PASS] ...software-delivery-loop-exhaustion.yaml :: tester-loop-exhausted
[PASS] ...security-review-happy-path.yaml :: happy-path
[PASS] ...security-review-happy-path.yaml :: one-correction-loop-then-approved
[PASS] ...security-review-happy-path.yaml :: critical-finding-escapes-to-human-review
[PASS] ...security-review-loop-exhaustion.yaml :: reviewer-loop-exhausted
[PASS] ...documentation-change-happy-path.yaml :: happy-path
[PASS] ...documentation-change-happy-path.yaml :: technical-review-requests-revision
```

## Why scenario fixtures live in a separate directory from workflow definitions

`prism graph validate`/`prism graph test` both walk a given directory
**recursively**, and treat *every* `.yaml`/`.yml`/`.json` file found as,
respectively, a workflow definition or a scenario fixture — there is no
content-based disambiguation. This means you should never point `graph
validate` and `graph test` at the same directory tree if it contains both
kinds of file: `graph validate` will reject a scenario fixture as an invalid
workflow definition (it has no `apiVersion`/`kind`), and `graph test` will
reject a workflow definition as an invalid scenario fixture (it has unknown
top-level fields under strict decoding).

This is why this repository's own shipped templates keep the two file kinds
in **separate directory trees**:
[`internal/workflow/multiagent/templates/`](../../internal/workflow/multiagent/templates/)
holds only the three workflow definitions, and
[`internal/workflow/multiagent/testdata/template-scenarios/`](../../internal/workflow/multiagent/testdata/template-scenarios/)
holds only their scenario fixtures, each referencing its workflow via a
relative `../../templates/<file>.yaml` path. Structure your own workflows
and scenarios the same way — see [CI integration](ci-integration.md) for the
exact commands this repository runs.
