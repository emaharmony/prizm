# Debugging Validation Errors

`prism graph validate` prints one line per diagnostic in the form:

```text
file:line:col: rule: message (did you mean "X"?)
```

- `file:line:col` is omitted when no position is available (an in-memory
  definition with no backing source file); if a filename is known but no
  line is, only the filename prefixes the message.
- `rule` is a stable, dotted rule ID (`<pass>.<check>`, e.g.
  `routing.outcome-not-allowed`, `cycle.unbounded-loop-edge`) — grep for it,
  don't parse the message text, if you're scripting against this output.
  `--json` gives you the same data as structured `Diagnostic` objects.
- The `(did you mean "X"?)` suffix appears when a small Levenshtein-distance
  helper found a close match among the definition's own known node/edge
  IDs, outcomes, or profile names — no network calls, no fuzzy matching
  beyond simple edit distance.

## Worked example

Given this minimal (deliberately broken) definition:

```yaml
apiVersion: prism.dev/v1alpha1
kind: MultiAgentWorkflow
metadata:
  name: diag-example
  version: "1.0.0"
spec:
  entryNode: writer
  budgets:
    maxTransitions: 5
  nodes:
    - id: writer
      type: role
      role: writer
      agentProfile: writer-default
      allowedOutcomes:
        - draft_ready
    - id: done
      type: terminal
      terminalCondition: completed
  edges:
    - id: writer-to-done
      from: writer
      to: done
      when:
        outcome: draft_reddy   # typo
```

```text
$ prism graph validate diag-example.yaml
11:7: routing.outcome-not-allowed: edge "writer-to-done" routes outcome "draft_reddy" from node "writer", which is not in that node's allowedOutcomes (did you mean "draft_ready"?)
11:7: routing.unhandled-outcome: node "writer" declares allowed outcome "draft_ready" with no routing edge, and spec.defaults.failure.defaultOnUnhandledOutcome is not "fail"
Error: graph validate: 1 of 1 file(s) failed validation
```

Two diagnostics from one typo, and both point at line 11 — the `writer`
node's own declaration line, since these are `NodeID`-keyed diagnostics and
the position index resolves a node ID to where that node was declared:

1. **`routing.outcome-not-allowed`** — the edge routes an outcome
   (`draft_reddy`) that isn't in the source node's `allowedOutcomes`. The
   suggestion (`draft_ready`) is exactly what you'd guess: fix the typo in
   the edge's `when.outcome`.
2. **`routing.unhandled-outcome`** — a *consequence* of the same typo: with
   the edge's outcome misspelled, the node's real declared outcome
   (`draft_ready`) has no edge routing it at all, so the graph could
   deadlock the moment `writer` actually returns it.

Fixing the typo (`outcome: draft_ready`) resolves both diagnostics at once
— they were never two independent problems, just two consequences of the
same one. `prism graph validate` never stops at the first diagnostic
(matching the existing "collect everything, never short-circuit" precedent
elsewhere in Prism's config validation), so you always see the full picture
in one pass rather than fixing errors one at a time across repeated runs.

## Severity: error vs. warning

Only `error`-severity diagnostics fail `prism graph validate`'s exit code
and block `prism graph compile`/`Compile`. `warning`-severity diagnostics
(e.g. `structural.unreachable-node`, `routing.tie-break-used`,
`budget.contradictory-limit`) are always printed but never block — they
flag something worth a second look, not something that's actually wrong.

## `--json` output

```text
$ prism graph validate --json diag-example.yaml
{
  "diag-example.yaml": [
    {
      "severity": "error",
      "rule": "routing.outcome-not-allowed",
      "message": "edge \"writer-to-done\" routes outcome \"draft_reddy\" ...",
      "line": 11,
      "column": 7,
      "node_id": "writer",
      "edge_id": "writer-to-done",
      "suggestion": "draft_ready"
    },
    ...
  ]
}
```

Grouped by file path (not a flat array): each `Diagnostic` already carries
its own `file` field, but grouping up front means a caller (CI, an editor
extension) can answer "did this specific file pass" without re-grouping a
flat list first.
