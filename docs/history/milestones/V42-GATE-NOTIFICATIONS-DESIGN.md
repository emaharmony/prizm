# V42 — Gate-Needs-You Notifications

**Status:** Source-current
**Last Updated:** 2026-06-29

## Problem

When the gated loop pauses at FEEDBACK_PRE / FEEDBACK_POST, it posted the plan or
review package to the channel — but did nothing to actively get the approver's
attention. If they weren't watching the channel, the run sat blocked (up to the
8-hour wait) for no reason other than a missed message.

## Design

The feedback notifier now prepends an **action-needed banner** that @-mentions the
named approvers/reviewers, so Discord pings them. Entirely in the notifier
(presentation layer); the loop core is untouched. The pause event already carries
`approvers` and `required_reviewers`.

- `extractNames(payload, "approvers", "required_reviewers")` — pulls a
  deduplicated, ordered name list from the payload (arrays survive the NATS
  round-trip as `[]any`).
- `gateAlert(phase, names, resolve)` — builds
  `🔔 **ACTION NEEDED** at `<phase>` — <mentions>, your <approval|review> is needed.`
  `resolve` maps a name to a Discord user ID; resolvable names become `<@id>`
  pings, others fall back to a plain `@name`. Returns "" when there are no names.
- `discordIDResolver(cfg)` — resolves an approver name (owner `ID` or
  `DisplayName`) to its first `Aliases["discord"]` entry in `cfg.Users`, so no new
  config is required — it reuses the existing user identity map.

The banner is prepended to the message (above the plan/review package and the V41
diff preview) so the ping is the first thing seen.

## Testability

`extractNames`, `gateAlert`, and `discordIDResolver` are pure and unit-tested:
dedup/ordering, resolved-mention vs `@name` fallback, approval-vs-review wording,
empty-on-no-names, and owner→Discord-ID resolution (incl. nil config / missing
alias).

## Tests

`cmd/prizm-cli/workflow_feedback_notifier_test.go`: `TestExtractNamesDedupes`,
`TestGateAlertMentionsAndFallback`, `TestDiscordIDResolver` (alongside the V41 diff
tests).

## Follow-ups (UX roadmap)

Remaining brainstormed items: dry-run/plan preview, and rich Discord approval cards
(interactive buttons instead of typed `approve`/`changes` commands).
