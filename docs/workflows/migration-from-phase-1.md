# Migrating from the Phase 1 Built-In Workflow

The short version: **you don't have to.** `CompatAdaptDefinition`
(`internal/workflow/multiagent/compat.go`) converts the Phase 1 built-in
`Definition`/`RoleConfig`/`TransitionRule` shape into the exact same
`CompiledGraph` the new authored-YAML path produces, so
`prism workflow run multi-agent-software-task` keeps working exactly as
before, with zero changes required, forever — see
[ADR 0002](../architecture/decisions/0002-graph-defined-workflows.md).
`DefaultReferenceDefinition()`/`ApplyReferenceOverrides()`
(`reference_workflow.go`) are kept unmodified.

## If you want to move to an authored graph anyway

[`templates/software-delivery.yaml`](../../internal/workflow/multiagent/templates/software-delivery.yaml)
**is** the migration path — a hand-authored YAML definition describing the
same shape as `DefaultReferenceDefinition()`: the same four roles
(`planner`/`developer`/`tester`/`reviewer`), the same outcome vocabulary
(`plan_ready`/`implementation_ready`/`tests_passed`/`tests_failed`/
`review_approved`/`changes_requested`), and similar budgets. It is
**behaviorally** equivalent, not byte-identical — the two compilation paths
(`Compile` for authored YAML, `CompatAdaptDefinition` for the legacy Go
struct) have no principled reason to serialize identically, and there is a
dedicated test (`compat_equivalence_test.go`) asserting behavioral
equivalence (same `Resolve()`/`LoopFor()` results for every input) between
them, not fingerprint equality.

One structural difference worth knowing about: `templates/software-delivery.yaml`
adds an explicit `loop:` policy to two edges
(`developer`→`tester`, `tester`→`reviewer`) that the legacy `Definition`
shape never needed a concept for at all. This is not a behavioral
difference — it's the SCC-wide cycle-validation rule that only applies to
the *new* authored schema. See [Loops and cycles](loops-and-cycles.md) for
why, and note it if you're comparing the two side by side and wondering why
the YAML has more loop annotations than you'd expect from just reading the
Phase 1 Go source.

## What actually changes if you migrate

Nothing about execution — the same supervisor, the same persistence, the
same dashboard, the same budget/approval/validation enforcement. What you
gain:

- Your own role/outcome vocabulary, instead of the fixed four
  roles/eight outcomes.
- Local, file:line:column validation (`prism graph validate`) before you
  ever run anything.
- Scripted, no-model-call testing (`prism graph test`) instead of needing a
  real run to check routing behavior.
- Immutable, versioned registration (`prism graph run --file`, or the
  `POST /api/v1/multiagent/definitions` API) instead of one fixed built-in
  definition.

What you do **not** gain yet: dashboard UI for browsing/selecting
registered definitions or versions — Phase 2's dashboard keeps working
against a compiled-graph-derived `RunGraph` with zero dashboard code
changes, but there is no definition browser or version selector. See
[Current limitations](limitations.md).

## Side-by-side

| | Phase 1 built-in | Authored YAML |
| --- | --- | --- |
| Definition source | Go literal (`reference_workflow.go`) | YAML/JSON file |
| Role/outcome vocabulary | Fixed (4 roles, 8 outcomes, Go enum) | Open, per-definition string |
| Validated by | `Definition.Validate()` | `ValidateDefinition()` |
| Compiled via | `CompatAdaptDefinition` | `Compile` |
| Run via | `prism workflow run multi-agent-software-task` | `prism graph run --file`/`--workflow` |
| Versioning | `Definition.Version int`, no history | `DefinitionStore`, immutable version history |
| Testable without a model call | No | Yes (`prism graph test`) |
