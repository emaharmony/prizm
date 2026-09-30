# Current Limitations

An honest, specific list of what this feature does not do yet — compiled
across all seven PRs of the Phase 3 effort plus this final PR's own
findings. None of these are secret or accidental; each was either a
deliberate scope decision or a gap found during this PR's final
security/hardening review.

## Deliberate scope decisions

### Outcome-only routing — no expression language

`SchemaEdge.when` is `{outcome: string}` only. There is no condition-term
list, no comparison against task metadata or role output, no `if`. See
[Routing and outcomes](routing-and-outcomes.md#the-explicit-non-goal-no-expression-language).
This was cut deliberately (Phase 3 plan decision #3) to keep the schema free
of anything resembling an expression language — an authority-boundary
concern, not just a "we didn't get to it" gap. `SchemaCondition` is its own
type specifically so a second field could be added later without a breaking
schema change, but that has not happened and is not a small addition when
it does.

### No environment-variable substitution

No schema field is expected to ever hold a secret or credential — agent and
validation profiles are resolved by *name* through Prism's existing
registries, never embedded as literal credential values (re-verified
directly against `schema_types.go`'s field list in this PR's
[security review](security.md)). Building unused substitution surface area
was judged unjustified scope.

### Dashboard UI integration — explicitly out of scope for the whole Phase 3 effort

Phase 2's dashboard (`internal/dashboard`) keeps working exactly as before
against a compiled-graph-derived `RunGraph` — zero dashboard code changed
during Phase 3 — but there is:

- No definition browser (list registered `workflow_id`s from the
  dashboard).
- No version selector (pick a specific registered version to inspect or
  run from the browser).
- No compiled-graph visualization in the dashboard UI (the CLI's
  `prism graph inspect --format mermaid` is the only visualization surface
  today).

The graph renderer (`multiagent-graph.js`) still only has a hand-tuned
layout for the fixed Phase 1 reference topology
(planner → developer → tester → reviewer); a run of a genuinely different,
authored workflow shape renders via a plain overflow-row fallback rather
than a meaningful layout. See
[Multi-Agent Execution Graph Dashboard](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md#phase-2-limitations)
for the pre-existing dashboard-specific gaps this inherits unchanged.

## Gaps found during PR7's final hardening pass

### `loop.onExhausted: route_to` is not runtime-enforced

The schema accepts it, `prism graph validate`/`prism graph compile` check
it (must name a known node, must escape the cycle it's declared on), and
`CompiledLoop.RouteTo`/`LoopExhaustionBehavior` represent it in the compiled
graph — but `Supervisor.checkLoopBudget` (`supervisor.go`) never actually
reads `loop.OnExhausted`/`loop.RouteTo` at run time: **every** loop
exhaustion ends the run with a budget-exhausted status, identical to
`onExhausted: fail`, regardless of what was declared. See
[Loops and cycles](loops-and-cycles.md#onexhausted-route_to-is-validated-but-not-yet-runtime-enforced)
and [Security review](security.md) for the full finding and why it was
reported rather than silently patched (implementing it correctly is a
supervisor-level design decision — what happens to the in-flight handoff,
what event fires, how a terminal-node `routeTo` target should even be
represented internally, since `CompiledLoop.RouteTo` is currently typed as
a `Role`, not a general node ID — not a one-line fix). Neither shipped
template relies on `route_to` actually firing at runtime.

### Custom terminal conditions previously could not finish a run — found and fixed in this PR

Before this PR, `Supervisor.finishTerminal` only recognized Phase 1's three
legacy terminal condition values (`completed`/`cancelled`/`failed`) at run
time, even though the schema, validator, and compiler all already treat
`terminalCondition` as an open, developer-chosen string with no enum. Any
workflow using its own terminal condition name (e.g. `published`,
`needs_revision`, `needs_human_review`) validated and compiled cleanly but
could never actually finish a run through that edge — the run failed with a
spurious `*InvalidTransitionError` instead. This was found while building
`templates/documentation-change.yaml`'s first scenario and **was fixed** in
this PR (`Supervisor.completeRunWithCondition`, `supervisor.go`) — see
[Security review](security.md) for the full before/after and why this
qualified as a safe, additive fix rather than a design question to merely
report. All three shipped templates and their scenario fixtures now cover
this path, including a dedicated regression scenario
(`security-review-happy-path.yaml`'s `critical-finding-escapes-to-human-review`).

### `prism graph inspect`/`prism graph run` registry-backed inspection is partial

`prism graph inspect` only supports `--file` (a local definition) in this
release; `--workflow`/`--version` (inspecting an already-registered
version) is not wired up. `prism graph run` *does* support both modes
(`--file` and `--workflow`/`--version`) — the gap is specific to `inspect`,
not the whole CLI.

### `WorkflowRunStarter` is single-workspace-per-server for API-started runs

`POST /api/v1/multiagent/definitions/{id}/versions/{n}/run` starts a run
against the workspace `prism serve` was configured with at startup — there
is no per-request workspace override in this release. A `--file`/CLI-started
run via `prism graph run` does accept an explicit `--workspace`, since it's
a fresh process invocation each time; the API's long-lived server process
has no equivalent per-request parameter yet. Flagged originally in PR6's
own report, carried forward here unchanged (not addressed by this PR).

### Directory-scan-based CLI commands cannot mix workflow definitions and scenario fixtures in one scanned path

`prism graph validate`/`prism graph test` both walk a given directory
recursively and classify every `.yaml`/`.yml`/`.json` file found purely by
*where it is asked to scan*, not by its content — there is no
disambiguation between "this is a workflow definition" and "this is a
scenario fixture." Pointing either command at a directory containing both
file kinds fails (see
[Testing workflows](testing.md#why-scenario-fixtures-live-in-a-separate-directory-from-workflow-definitions)).
This is confirmed, pre-existing, deliberate CLI behavior (documented in
PR5's own `TestExecuteGraphTestWalksDirectoryRecursively` test) that this PR
worked around by directory layout rather than by changing shared CLI
directory-walk logic — changing that behavior (e.g. having each command
silently skip files that don't match its expected shape) is a real design
question about whether skipping should ever be silent, not this PR's call
to make unilaterally.

## Pre-existing, unchanged limitations inherited from Phase 1/2

- **The SCC-wide cycle-validation rule's forward-edge consequence** — see
  [Loops and cycles](loops-and-cycles.md). A deliberate design tradeoff
  (Tarjan over simple-cycle enumeration), not a bug, but genuinely
  surprising the first time you hit it.
- **The dashboard's auth gap on mutating requests and its SSE stream** — no
  bearer-token header is attached by the dashboard's own JS for
  Pause/Resume/Cancel or the live event stream. Pre-existing, dashboard-wide
  (not introduced by Phase 3). See
  [Multi-Agent Execution Graph Dashboard](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md#auth-gap-on-mutating-requests-and-the-sse-stream).
- **`waiting_validation` and `blocked` node statuses are reserved but
  unreachable** — exist in the type system, no code path produces them yet.
