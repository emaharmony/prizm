# V44 — Rich Discord Approval Cards

**Status:** Source-current
**Last Updated:** 2026-06-29

## Problem

Feedback gates were actioned by typing commands (`approve <id>`,
`changes <id> : …`). That's error-prone (wrong id, wrong syntax) and unfriendly.
Interactive buttons make the high-stakes human-in-the-loop moment one click.

## Design

The correctness-critical core is pure and unit-tested; the discordgo wiring is thin
glue over it.

### Button codec (`cmd/prizm-cli/approval_buttons.go`)

- `encodeFeedbackButtonID(gate, action, runID)` → `prizmfb:<gate>:<action>:<runID>`;
  `decodeFeedbackButtonID` parses it back and rejects foreign/malformed IDs.
- `buildFeedbackButtons(phase, runID)` returns the gate's buttons: approve /
  changes / reject for FEEDBACK_PRE, approve / changes for FEEDBACK_POST (a
  rejected review just loops back via changes).
- `feedbackButtonPayload(customID, reviewer)` maps a clicked button to the exact
  feedback-response payload the typed commands publish — `feedback_response` for
  the pre gate, `review_response` for the post gate — rejecting non-`gl-` runs and
  unknown actions.

### Adapter (`internal/adapter/builtin/discordbot/bot.go`)

- `OutboundMessage.Buttons []MessageButton` — a discordgo-free button description,
  so callers request buttons without importing discordgo. Rendered as one action
  row via `ChannelMessageSendComplex` (attached to the final chunk of a split
  message).
- `OnButton(ButtonHandler)` + `onInteractionCreate` — routes
  `InteractionMessageComponent` events to handlers and acknowledges with a deferred
  update so the client never shows "interaction failed".

### Wiring

- The feedback notifier attaches `buildFeedbackButtons(phase, runID)` to the gate
  message (the typed commands still work in parallel).
- `prizm serve` registers an `OnButton` handler that runs `feedbackButtonPayload`
  and publishes to `prizm.workflow.feedback.response` — the same subject the typed
  path uses, so the engine resume logic is unchanged.

## Tests

`cmd/prizm-cli/approval_buttons_test.go`: id encode/decode + foreign/malformed
rejection; button sets per gate with round-tripping ids; payload mapping for
pre/post × approve/changes/reject; and guards (foreign id, non-`gl-` run, unknown
action). The discordgo send/interaction glue is covered by build + the existing
discordbot tests (a live Discord session can't be unit-tested).

## Status

This completes the brainstormed UX roadmap (V38–V44): live `watch`, `doctor`,
report artifact, diff preview, gate notifications, static preview, and approval
cards.
