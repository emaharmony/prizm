# V43 — `prizm preview`: Static Gated-Loop Preview

**Status:** Source-current
**Last Updated:** 2026-06-29

## Problem

Before launching a gated-loop run, a user had no way to see *what the loop would
do* — its phase sequence, the gate on each phase, iteration/token/time budgets, and
whether EXECUTION verification is on. The brainstormed "dry-run / plan preview"
addresses this; a true dynamic dry-run (run PROBE→PLAN then stop) would require a
stop-after-phase flag in the engine. A **static** preview delivers the same
"see the approach before spending budget" value as a pure config consumer, with no
change to the hardened core.

## Design

```
prizm preview [--config prizm.yaml] [--workflow <file>]
```

`resolvePreviewConfig` picks the effective config: an explicit `--workflow` file
wins; otherwise the project's `prizm.workflow_config`; otherwise the built-in
`DefaultConfig` (so it works with zero setup). `renderWorkflowPreview` (pure, I/O
free) formats it:

- **Budgets:** max iterations, max time, max tokens, stuck-cap (repeated tool
  calls).
- **Phases:** numbered, each with its gate type + threshold, max iterations,
  allowed-tool count, verification profile (+ blocking), and a ⛔ marker when the
  phase blocks on fallback.
- **Confidence domains** tracked during RESEARCH.

The command also runs `ValidateConfig` and prints any structural issues to stderr,
so a malformed custom workflow surfaces problems before a run. It runs no LLM and
starts no work — a read-only explainer.

## Tests

`cmd/prizm-cli/cmd_preview_test.go`: the default config renders all sections
(budgets, phases, verification, confidence domains, the "no LLM ran" footer); gate
types and blocking flags appear; `resolvePreviewConfig` falls back to the built-in
default when no config exists and errors on a missing explicit `--workflow` file.

## Follow-ups (UX roadmap)

The last brainstormed item: rich Discord approval cards (interactive buttons
instead of typed `approve` / `changes` commands). A future dynamic dry-run could
add a `--stop-after PLAN` engine option to also show the *generated* plan + a token
estimate, building on this static preview.
