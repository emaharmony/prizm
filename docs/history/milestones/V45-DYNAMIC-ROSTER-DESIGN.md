# V45 — De-Hardcoded Gate Personas

**Status:** Source-current
**Last Updated:** 2026-06-29

## Problem

Some gate-facing text strictly named specific personas regardless of how a project
configured its roster. The FEEDBACK_POST review message always said
`Reviewers needed: **Mango** (required) and **Lumi**`, and plan/report formatting
used a hardcoded `switch` over `prizm|mango|junie|lumi` for agent decoration. A
project with a different roster (or just different names) saw the wrong people
named.

## Change

Persona-facing strings are now driven by config, not literals.

- **`FormatReviewPackage(state, gitDiff, requiredReviewers)`** — the reviewer line
  is built by `formatReviewerList(requiredReviewers)`, which names whichever
  reviewers the gate config specifies (first marked *required*), falling back to
  "the configured reviewers" when none are set.
- **`FormatPlanForApproval(state, approvers)`** — adds an **Approvers:** line naming
  the configured approvers (omitted when none).
- The driver passes `cfg.Gate.RequiredReviewers` / `cfg.Gate.Approvers` (already in
  the pause payload) into these formatters — the names now come from the same
  config source the gates enforce.
- **`AgentGlyphs`** — the persona `switch` for task decoration is replaced by an
  overridable `map[string]string` (`agentGlyph` defaults to 🤖 for any unlisted
  agent), so no persona is baked into formatting logic; a project can populate the
  map to match its roster.

These are the gate/role definitions that were strict in code. Other persona
mentions are either illustrative comments (router/namespace examples) or
config-overridable defaults (bridge `LeaderInstance`, per-agent delegation timeouts,
which already fall back to a generic default for any agent), so the roster is not
strict there.

## Tests

`internal/workflow/v2/discord_format_dynamic_test.go`: reviewer-list rendering
(empty / single / multiple), review package names configured reviewers and contains
no hardcoded personas, plan approval names approvers (and omits the line when none),
and `agentGlyph` default + override.

## Delegation timeouts (follow-up, same theme)

`DelegationManager` previously baked per-persona timeouts into its constructor
(`prizm 30m, mango 15m, junie 20m, lumi 10m, custom 20m`). These are now:

- A single `defaultDelegationTimeout` (20m) fallback, applied to any agent without
  a specific override (`timeoutFor`).
- Per-agent overrides set via `SetAgentTimeout`, `SetDefaultTimeout`, or
  `ApplyTimeoutConfig(map[string]string)` — the latter driven by the workflow
  config's `global.delegation_timeouts` (agent → duration string; special key
  `default`). `RunGatedLoop` applies it when building the manager.

So delegation deadlines are now controlled by config for a project's own roster,
with no persona names in code. See `examples/workflows/gated-loop.yaml` for the
(commented) shape.

## CLI default (follow-up)

`prizm run --agent` previously defaulted to the hardcoded persona `lumi`, so a run
without `--agent` referenced an agent that may not exist in the user's config. It
now defaults to the neutral instance name `prizm` (matching `--project`), and a
usage test (`TestCommandUsageNoHardcodedPersona`) guards the grouped help against
reintroducing persona names.

## Compatibility

The gated-loop `DefaultConfig` / example YAML still set `approvers: [ema]` and
`required_reviewers: [ema]` — those are user-editable config, not code literals, so
changing the roster is now purely a config edit. Delegation timeouts default to 20m
for every agent unless `global.delegation_timeouts` says otherwise.
