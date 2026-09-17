# V38 — `prizm watch`: Live Run Visibility

**Status:** Source-current
**Last Updated:** 2026-06-29

## Problem

The gated loop emits rich events (`phase.entered`, `phase.gate_check`,
`phase.verification`, `tool.retry`, `delegation.*`, `workflow.budget_exhausted`,
…), but a human watching a run had no good live view — they were buried in logs or
the read-only dashboard. This is the single biggest perceived-UX gap: the loop does
a lot of objective work and none of it was visible as it happened.

## Design

`prizm watch` is a **pure consumer** of events the engine already emits, so it can
never affect a run — it only observes. It connects to the running daemon's SSE
endpoint and renders a continuously-updated terminal view.

```
prizm watch [--config prizm.yaml] [--subject prizm.workflow.>] [--url <base>]
```

- **Transport:** `GET /api/v1/events/stream?subject=prizm.workflow.>` (the existing
  SSE endpoint), parsed with the shared `internal/sse` decoder. Base URL and bearer
  token are derived from `prizm.yaml` (API on health `port + 1`), or overridden
  with `--url`.
- **Model/render split:** `watchModel.apply(evType, payload)` folds each event into
  in-memory state; `watchModel.render()` produces the screen. Both are I/O-free and
  unit-tested, so the parsing/rendering logic is verified without a live server.
- **Render loop:** `runWatch` repaints (ANSI clear + home) on each event, throttled
  to ~80ms so an event burst doesn't thrash the terminal. Ctrl-C exits cleanly via
  a signal-cancelled context.

### What it shows

- **Header:** workflow name + overall status (running / paused / completed /
  blocked / budget exhausted).
- **Budget meter:** a 20-cell burn-down bar of total tokens against
  `max_total_tokens`, fed by the new `phase.tokens` telemetry event (see below).
- **Phase tree:** each phase with a status glyph (▶️ current, ✅ passed, ⏸️ paused,
  ⚠️ fallback, ❌ stuck/blocked), its gate score, verification result, and current
  tool (with retry count).
- **Delegations:** task → `agent:status` (sent / retrying / timed_out).

## Supporting change: `phase.tokens` event

The loop tracked cumulative tokens but never emitted them, so a live budget meter
was impossible. `Engine.Drive` now emits `phase.tokens`
(`{phase, prompt, completion, total, max}`) after each LLM call. This is purely
observational — no behaviour change — and makes the existing budget enforcement
visible.

## Tests

`cmd/prizm-cli/cmd_watch_test.go`: phase lifecycle (entered → tokens → gate →
verification) folds into the model and renders; delegation sent→retry→timeout;
paused/completed transitions; and `tokenMeter` (full / half / uncapped /
over-budget-clamp).

## Follow-ups (UX roadmap)

Next brainstormed UX items, in rough priority: rich Discord approval cards
(buttons), diff preview at feedback gates, `prizm doctor` preflight, dry-run/plan
preview, a durable `runs/<id>/REPORT.md` artifact, and gate-needs-you
notifications.
