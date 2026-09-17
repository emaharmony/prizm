# Multi-Agent Execution Graph Dashboard

A read-only, live-updating, replayable execution-graph view of one durable
multi-agent run, served from Prizm's embedded dashboard. This document covers
the dashboard-facing read model, the live/replay/operator behavior of the run
page, and the operational limits of the current implementation. It is for
operators and contributors, not for the runtime contracts themselves — those
are defined in [Multi-Agent Loop Runtime Contracts](MULTI_AGENT_LOOP_RUNTIME.md)
and [Multi-Agent Software Task Workflow](../MULTI_AGENT_WORKFLOW.md).

> Prizm is source-available and preview-stage. See
> [Capability Status](../reference/CAPABILITY_STATUS.md) before relying on any
> capability described here.

## Where this fits

The multi-agent runtime (`internal/workflow/multiagent`) already persists
durable run state, canonical events, and CLI-facing inspection (`prizm
workflow status|cancel|resume|report`). This feature adds a second, read-only
consumer of that same durable state: a dashboard read model
(`DashboardRunSnapshot`), a run-scoped SSE event tail, and three narrow
operator-control endpoints, all served by `prizm serve`. It does not change
routing, budgets, persistence, policy, approval, or validation authority —
those remain exactly as described in the runtime contracts doc. The dashboard
is a projection, never a second source of truth.

## Pages and API surface

| Page | Purpose |
| --- | --- |
| `/multiagent-runs.html` | Lists every durable multi-agent run discoverable under the configured run root. |
| `/multiagent-run.html?run=<id>` | The execution graph, budget panel, run/node/edge/handoff inspector, event timeline, replay mode, and operator controls for one run. |

Both pages consume the existing `/api/v1` API, plus these multi-agent-specific
routes (`internal/api/server.go`):

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/multiagent/runs` | List runs (`multiagent.RunSummary[]`: run ID, workflow ID, status, created/updated timestamps). |
| `GET` | `/api/v1/multiagent/runs/{id}/snapshot` | The complete dashboard read model for one run (`multiagent.DashboardRunSnapshot`). |
| `GET` | `/api/v1/multiagent/runs/{id}/events/stream` | Server-sent events: backfill of the run's canonical event history, then a live tail. |
| `POST` | `/api/v1/multiagent/runs/{id}/resume` | Re-invoke the role runner for a paused/waiting run. |
| `POST` | `/api/v1/multiagent/runs/{id}/cancel` | Persist an operator cancellation request. Optional JSON body `{"reason": "..."}`. |
| `POST` | `/api/v1/multiagent/runs/{id}/pause` | Persist an operator pause request. Optional JSON body `{"reason": "..."}`. |

`GET /api/v1/multiagent/runs` and the snapshot/stream routes require
`multiagent.RunLocator` to be configured (`503` otherwise). The three `POST`
routes additionally require a `MultiAgentController` to be wired (`503`
otherwise) — in `prizm serve` this reuses `cmd/prizm-cli`'s existing,
already-tested run-opening helpers (`openLiveReferenceRuntime` for Resume,
`RunLocator.OpenInspection` for Cancel/Pause), not a separate code path.

## Execution graph concepts

`multiagent.BuildRunGraph` (`internal/workflow/multiagent/graph_projection.go`)
is a pure function: given a workflow `Definition`, a `RunState`, and the run's
event history, it deterministically derives a `RunGraph` — nothing about it is
persisted, and calling it twice on the same inputs always produces the same
result.

A `RunGraph` has:

- **Nodes** (`GraphNode`) — one per configured role, plus one per terminal
  condition reachable by some transition rule. Each carries a stable ID,
  status, whether it's the run's current position, visit/local-iteration
  counts, and configured ceilings.
- **Edges** (`GraphEdge`) — one per declared `TransitionRule`. Each carries a
  stable ID, status, traversal count, whether it's a loopback (a correction
  edge back to an earlier role), and configured traversal ceilings.
- `CurrentNodeID` / `ActiveEdgeID` — the run's current position and the edge
  most recently traversed into it.
- `RunPaused` / `BudgetExhausted` — run-level overlay flags (see
  [Status semantics](#status-semantics)).

Stable IDs are constructed directly from domain identifiers, not database
row IDs, so they are the same across snapshot fetches, SSE patches, and replay
steps:

| ID kind | Format | Example |
| --- | --- | --- |
| Role node | `node:<role>` | `node:developer` |
| Terminal node | `node:terminal:<condition>` | `node:terminal:failed` |
| Edge | `edge:<from-role>:<outcome>` | `edge:tester:tests_failed` |

This is safe because `Definition.Validate()` already guarantees `(From,
Outcome)` uniqueness across a definition's transitions.

`TerminalConditionBudgetExhausted` deliberately has **no** graph node: no
transition rule in the Phase 1 reference definition declares it, so budget
exhaustion is represented only as the `RunGraph.BudgetExhausted` overlay flag,
never as a synthetic node or a dangling `CurrentNodeID` reference.

The frontend renderer (`multiagent-graph.js`) knows the *fixed* Phase 1
topology (planner → developer → tester → reviewer, with `tests_failed` /
`changes_requested` correction loops back to developer, and three terminal
outcomes) and lays it out by hand. It does not implement a generic graph
layout algorithm: an unrecognized node ID (e.g. from a future, non-reference
workflow definition) falls back to a plain overflow row rather than crashing,
but only the reference topology gets a hand-tuned layout.

## Delegation graph vs execution graph

This dashboard renders the **execution graph** only — "what happened in this
run": role visits, transition outcomes, correction loops, and terminal state.
It does not render, and has no relationship to, the **delegation graph**,
which governs permission ("who may delegate to whom") and is enforced
entirely server-side before a role ever runs. See
[Delegation graph and execution graph](MULTI_AGENT_LOOP_RUNTIME.md#delegation-graph-and-execution-graph)
in the runtime contracts doc for the full distinction. In short: an execution
edge on this page is only ever rendered because delegation and governance
already permitted the underlying assignment; the graph on this page cannot
express permission, only outcome.

## Status semantics

### Node status (`GraphNodeStatus`)

| Value | Meaning |
| --- | --- |
| `not_started` | Role has not yet been entered. |
| `running` | Role is currently executing. |
| `waiting_approval` | Role is paused pending approval, or (rarer) crash-recovery reconciliation — both surface as `RoleStatusWaiting` and render identically today. |
| `waiting_validation` | **Reserved, currently unreachable.** No Phase 1 code path distinguishes a validation wait from an approval wait, so this value is never produced. |
| `completed` | Role finished successfully. |
| `failed` | Role failed. |
| `blocked` | **Reserved, currently unreachable.** `RoleStatusBlocked` exists in the type system but no Phase 1 code path in the supervisor or durable runtime ever sets it. |
| `cancelled` | Role was cancelled. |
| `skipped` | The run reached a terminal condition without ever entering this role (e.g. planner → terminal `cancelled` skips developer/tester/reviewer). |

There is deliberately no `"paused"` node status. A paused run is represented
as `RunGraph.RunPaused = true`, applied as an overlay; the node at
`CurrentNodeID` keeps its own underlying status (`running` or
`waiting_approval`) rather than becoming a distinct fourth thing.

### Edge status (`GraphEdgeStatus`)

| Value | Meaning |
| --- | --- |
| `available` | Never traversed, not currently active. |
| `previously_traversed` | Traversed at least once, not the currently-active edge. |
| `active` | The most recently selected transition into the run's current node. |
| `exhausted` | Traversal count has reached its configured ceiling (correction loops only). |
| `disabled`, `invalid` | **Reserved for non-reference definitions.** The Phase 1 reference graph never produces either. |

### Waiting state (`WaitingState.Kind`)

A paused run (`RunStatus = "paused"`) always carries a `Waiting` object with
one of these kinds, surfaced in the Run Inspector:

| Kind | Meaning | Introduced |
| --- | --- | --- |
| `approval` | Waiting on a policy/tool approval decision. | Phase 1 |
| `execution_reconciliation` | A process crash left an execution outcome unknown; requires operator reconciliation, `safe_to_retry = false`. | Phase 1 |
| `operator_pause` | An operator paused the run from this dashboard (or the API). | Phase 2 Milestone 9 |

`RunStatus` itself has no separate "waiting" value — every wait kind above is
represented as `RunStatus = "paused"` (`durable_runtime.go`'s
`pauseForApproval` and the operator-pause path both set
`record.State.Status = RunStatusPaused` before setting `record.Waiting`), so
the dashboard's Resume button gates purely on `status === "paused"` and never
needs to branch on `waiting.kind`.

## Counter semantics

The dashboard distinguishes four counting concepts that are easy to conflate;
each names exactly one thing and only one thing (`multiagent-graph.js`'s
`COUNTER_LABELS`):

| Counter | Scope | Meaning |
| --- | --- | --- |
| **Visits** | Per role node, all-time | Number of times the run has entered this role (correction loops create additional visits). |
| **Local iterations** | Per role node, current visit only | Bounded attempts performed within the role's *current* visit. Shown only on the current node. |
| **Traversals** | Per edge | Number of times this specific transition (role + outcome) has been selected across the run's full history. |
| **Total transitions** | Run-level | `RunState.TransitionCount` — every transition selected across the whole run, regardless of edge. |

Per-edge traversal counts (for edges that are not one of the two
budget-tracked correction loops) have no other source of truth than scanning
the full `EventMultiAgentTransitionSelected` history — `BuildRunGraph`
deliberately does not truncate the event slice it is given for this reason.

### Budget dimensions and levels

`multiagent.BuildBudgetVisualization` (`budget_visualization.go`) renders each
budget dimension (transitions, tokens, local iterations, repeated failures,
elapsed time, per-role visits, per-loop traversals) as a `BudgetDimension`:
`{used, limit, level}`. `limit` is `null` when the dimension is explicitly
unlimited, and in that case `level` is always `"normal"`.

Severity thresholds are fixed, integer-exact percentages of the configured
limit (no floating-point rounding at the boundary):

| Level | Condition |
| --- | --- |
| `normal` | `used < 75%` of limit |
| `approaching` | `used ≥ 75%` of limit |
| `critical` | `used ≥ 90%` of limit |
| `exhausted` | `used ≥ 100%` of limit |

These thresholds (75% / 90%) are product defaults, deliberately unexported
from the `multiagent` package — the dashboard never re-derives its own
threshold from raw `used`/`limit` numbers; it always trusts the
server-computed `level` field verbatim.

## Live event behavior

While viewing a run in live mode, the page opens
`GET .../events/stream` (`multiagent-live.js` + inline script in
`multiagent-run.html`). Each canonical event is mapped to a small set of
narrow, unambiguous DOM patches by the pure function `derivePatches`
(`multiagent-live.js`). The guiding rule: this module never invents a
severity/threshold judgement or re-derives a status the backend already
computed — it only ever restates a field the event payload carries verbatim,
or a structural fact implied by the event type (e.g. "this edge is now THE
active edge").

Most event types get an immediate, precise patch. A few do not, because no
unambiguous per-event mapping exists (as of this writing, per
`multiagent-live.js`'s own comments):

| Event type | Live behavior |
| --- | --- |
| `handoff.created` | No immediate patch — no handoff UI exists to patch narrowly. Reconciliation-only. |
| `budget.warning` (tester/reviewer correction-loop dimensions) | Immediate patch: near-exhaustion styling + remaining-attempts text on the affected edge (Milestone 9b). |
| `budget.warning` (every other budget dimension — role visits, local iterations, etc.) | No immediate patch — no documented mapping from `BudgetExceededError.Budget` values to a `BudgetVisualization` dimension key. Reconciliation-only. |
| `loop.traversal.recorded` | No immediate patch for the edge's new traversal count/level — the sibling `transition.selected` event (same event batch) already covers the edge-active highlight. Reconciliation-only for the exact count. |

For all of these, a **~250ms debounced full-snapshot reconciliation** (a
fresh `GET .../snapshot` fetch, re-rendering the whole graph and budget panel)
is the safety net: the UI never permanently drifts from backend-derived
truth, it just briefly lags on these specific fields. Confirm the current
list against `multiagent-live.js`'s `derivePatches` switch statement, since it
may be refined in later milestones.

Every applied event is deduplicated and cursor-tracked by ID (`shouldApply`/
`markApplied`) — ULIDs sort correctly as plain strings, so a lexicographic
comparison against the last-applied cursor is sufficient; framing events
without an `id:` line (`connected`, `heartbeat`) are never applied as
patches.

## Refresh and reconnect behavior

1. On page load, the run page fetches the snapshot once
   (`GET .../snapshot`) and renders it.
2. It then opens the SSE stream with `?after=<snapshot.event_cursor>` — the
   snapshot's own cursor — so there is no gap between what the snapshot
   reflects and where the stream picks up, and no re-delivery of events
   already reflected in the snapshot.
3. Reconnection is handled entirely by the browser's native `EventSource`
   auto-reconnect plus `Last-Event-ID` (which the server prefers over the
   `?after=` query param on any subsequent connection). The only
   page-specific behavior is: every `onopen` *after* the first one triggers
   an extra full reconciliation — a reconnect could follow a backend restart
   with partially-durable in-flight events, so this closes that gap.
4. Server-side, the stream first pages through the run's complete event
   history after the cursor (500-event batches) before switching to a live
   tail that polls the event store every **750ms** (there is no push path
   from the SQLite event store). A 30-second heartbeat frame keeps the
   connection alive through idle periods.
5. If the run becomes genuinely unreachable (the manifest or database no
   longer exist — the "deleted/unavailable run" case), `reconcile()`
   accumulates consecutive 404s. Once that streak crosses a threshold, the
   page **latches** into an "unavailable" state: it tears down the SSE
   connection and reconcile timer, shows a banner, and disables operator
   controls. This latch never clears itself — only a fresh page load does. A
   single failed fetch (network blip, `500`, `503`) does not trip this; the
   page just keeps showing its last good state and retries on the next
   debounced burst or reconnect.

## Replay mode

Replay is offered only for a run that has reached a terminal `RunStatus`
(`completed`, `failed`, `cancelled`, `budget_exhausted`). It works entirely
from data the page already fetched once on load —
`DashboardRunSnapshot.recent_events` (the run's full, untruncated event
history for a dashboard snapshot, unlike the CLI's own 20-event-truncated
inspection view) — sorted chronologically and stepped through locally.
**Replay never re-executes an agent or tool and never makes a network
request.**

Each replay step re-applies the exact same pure `derivePatches` function the
live SSE reducer uses, against an isolated, plain-object replay state — never
against the live DOM or the page's live snapshot. Controls: jump to start,
step back, play/pause, step forward, jump to end.

**Known, deliberate imprecision:** the same handful of event types that are
reconciliation-only in live mode (`handoff.created`, `budget.warning`'s
non-loop dimensions, `loop.traversal.recorded`'s exact count) return an empty
patch list during replay too — but replay has no per-step "ask the backend
for ground truth at this point in history" endpoint to fall back on. So
mid-replay state for the fields those events would affect (budget dimension
usage/level, the handoff panel, exact edge traversal counts) can lag behind
what actually happened at that historical instant. This **self-corrects
exactly at "jump to end"**, which renders the run's real, authoritative final
snapshot rather than the patch-accumulated state, by design.

## Operator controls

Three fire-and-forget mutating buttons on the run page: **Pause**, **Resume**,
**Cancel** (`multiagent-operator.js` for the pure gating logic;
`multiagent-run.html` for the DOM wiring). A successful `POST` only confirms
the request was *accepted* — never that the run has actually paused, resumed,
or cancelled. The button re-enables and the graph updates only once the real
`run.paused` / `run.resumed` / `run.cancelled` event arrives via the live
event stream (or the next debounced reconciliation).

### Button gating

Purely a function of the snapshot's `status` field plus whether the page is in
replay mode or has latched "unavailable":

| Button | Visible when | Also requires |
| --- | --- | --- |
| Pause | `status === "running"` | Not terminal, not replay mode, run reachable |
| Resume | `status === "paused"` | Not terminal, not replay mode, run reachable (covers all three `Waiting.Kind` values uniformly) |
| Cancel | not terminal | Not replay mode, run reachable |

All three are disabled while viewing replay mode ("Operator controls are
unavailable while viewing replay mode — actions only ever apply to the live
run, never to a historical replay view") and once the page has latched
"unavailable."

### Approve/Reject is not implemented here

A run waiting on `Waiting.Kind === "approval"` renders clearly in the Run
Inspector, but this page does **not** offer Approve/Reject buttons. The
underlying `WaitingState` data model (`durable_types.go`) carries only
`kind`/`reason`/`safe_to_retry`/`since` — no approval ID anywhere — and the
separate `ApprovalRequiredError`/`ApprovalCheck` types this package uses carry
no ID either. That's a different, unrelated abstraction from the legacy
`POST /api/v1/approvals/{id}/grant|deny` endpoint the existing dashboard's
Operations page (`v2.html`) uses. Rather than fabricate an approval ID or
guess a URL, the page shows an explanatory note and directs the operator to
resolve the approval through whatever mechanism this deployment has
configured, then use **Resume** here once it has been decided.

### Auth gap on mutating requests (and the SSE stream)

Prizm's dashboard shares one HTTP client helper (`app.js`'s `request()`) and
none of the three operator POSTs, nor this page's `EventSource` connection,
attach an `Authorization: Bearer <token>` header or a `?token=` query
parameter. `internal/api/server.go`'s `authMiddleware` gates every `POST`
request and (separately) this run's own SSE stream path
(`/api/v1/multiagent/runs/{id}/events/stream`) behind the configured bearer
token whenever one is set (`Config.AuthToken`); plain `GET` snapshot/list
requests remain open. So:

- If the Prizm server has an auth token configured, **Pause/Resume/Cancel
  will 401.**
- The **live SSE stream will also 401** under the same condition — this page
  follows the same no-token convention as the dashboard's only other SSE
  consumer (`workflow-editor.html`), and the server does accept `?token=` for
  SSE specifically (browsers can't set headers on `EventSource`), but neither
  page appends it today.

This is a **pre-existing, dashboard-wide gap**, not something Phase 2
introduced or fixed — the same issue affects other existing mutating
dashboard actions (e.g. approve/deny on the Operations page). Workaround:
run without an auth token for local/loopback use of this dashboard, or use
the equivalent CLI commands (`prizm workflow pause|resume|cancel <run-id>`)
which pass credentials through the normal CLI auth path. See
[Recommended Phase 3 roadmap](#recommended-phase-3-roadmap).

### Resume and live agent wiring

`prizm serve` composes a real, fully-wired multi-agent role runner on demand
to support Resume from the dashboard — the same tested composition function
the CLI uses (`openLiveReferenceRuntime`), not a separate or lighter-weight
path. Because Resume is fire-and-forget and a resume may execute one or more
further roles (potentially for minutes), an async failure during a
dashboard-triggered Resume is only visible via the SSE event stream (a
`run.failed` / `recovery.failed` event) or the server logs — the initiating
HTTP request only confirms the request was accepted, never that Resume
ultimately succeeded.

## Accessibility

- A skip link and a single `<main tabindex="-1">` landmark on every page.
- Every async region reports state through `aria-live="polite"` (loading,
  run overlays, replay banner, operator status message, loop-budget
  near-exhaustion announcements) — never color or animation alone.
- The graph canvas itself is `role="group"` with a descriptive
  `aria-label` (workflow ID, node count, edge count). Each node and edge is
  individually focusable (`tabindex="0"`) with its own `aria-label`, and
  responds to `Enter`/`Space` the same as a click — opening the corresponding
  inspector panel. Edge label text is `aria-hidden` (redundant with the
  edge's own label) rather than read twice.
- Event timeline rows are `role="button"`, `tabindex="0"`, and respond to
  `Enter`/`Space` identically to a click.
- `prefers-reduced-motion: reduce` disables the current-node pulse animation
  and the active-edge marching-ants animation; a near-exhausted loop edge
  never animates in the first place, in either motion preference.
- Focus is visible (`:focus-visible` outline + drop-shadow) on graph edges,
  matching the site-wide focus treatment elsewhere.
- Status is always paired with text, not just an icon or color — node/edge
  status renders as a humanized label string alongside its glyph and CSS
  class.

## Troubleshooting

| Symptom | Likely cause | What to do |
| --- | --- | --- |
| `503 multi-agent run locator not configured` on the runs list or a run page | `prizm serve` was started without a configured multi-agent run root. | Configure the run locator root (the same directory `prizm workflow run` writes to) and restart `prizm serve`. |
| `503 multi-agent controller not configured` on Pause/Resume/Cancel | `prizm serve` was started without a `MultiAgentController` wired. | This is a `cmd/prizm-cli` composition concern, not something fixable from the dashboard; confirm the serve command includes multi-agent control wiring. |
| Pause/Resume/Cancel returns `401 unauthorized` | Server has an auth token configured; the dashboard's operator buttons don't send it. | See [Auth gap on mutating requests](#auth-gap-on-mutating-requests-and-the-sse-stream). Use the equivalent CLI command, or run without an auth token for local use. |
| Live graph/timeline stop updating, no error banner | SSE stream 401'd (auth token configured) or dropped silently. | Check the browser console/network tab for the `events/stream` request status. Reload the page; if it recurs, see the auth-gap note above. |
| "This run is no longer available" banner, live updates stopped | `reconcile()` saw enough consecutive 404s to latch "unavailable" — the run's manifest/database is genuinely gone (removed or relocated). | If the run should still exist, verify the run root/ID; otherwise this is expected. Reload the page only after confirming the run is back. |
| Resume click succeeds ("Resuming…") but nothing happens afterward | Resume is fire-and-forget; the actual role execution can fail asynchronously. | Watch the live event stream (or server logs) for `run.failed`/`recovery.failed` — the HTTP response only confirmed acceptance, not success. |
| A waiting-for-approval run has no Approve/Reject button | Not a bug — see [Approve/Reject is not implemented here](#approvereject-is-not-implemented-here). | Resolve the approval through this deployment's existing approval mechanism, then click Resume. |
| Budget/loop numbers look briefly stale right after a `budget.warning` or `loop.traversal.recorded` event | Expected — those fields are reconciliation-only, see [Live event behavior](#live-event-behavior). | Wait ~250ms for the debounced reconciliation; no action needed. |
| Mid-replay numbers don't match the final report for a handoff or a non-loop budget dimension | Expected — see [Replay mode](#replay-mode)'s documented imprecision. | Use "jump to end" for authoritative final state; treat mid-replay values for those specific fields as approximate. |
| `node:*` status stuck on `waiting_validation` or `blocked` | Should not happen today. | These are reserved-but-unreachable values (see [Status semantics](#status-semantics)); if observed, it indicates either a data/version mismatch or a genuine bug — file an issue rather than assuming it's expected. |

## Phase 2 limitations

- **Operator-control `POST` requests (pause/resume/cancel), and this page's
  SSE stream, carry no bearer-token auth header.** A server with an auth
  token configured will 401 all four. Pre-existing, dashboard-wide gap — not
  introduced by, or fixed in, Phase 2.
- **Approve/Reject are not offered from this page.** `WaitingState` has no
  approval-ID field to wire a button to; approval must go through Prizm's
  existing, separate approvals mechanism, then Resume from here.
- **Resume composes a real, fully-wired role runner** on demand in `prizm
  serve`; async failures during a dashboard-triggered Resume are only
  observable via the SSE stream or server logs, not the initiating HTTP
  response.
- **A few event types are reconciliation-only in live mode**:
  `handoff.created`, `budget.warning`'s non-loop dimensions, and
  `loop.traversal.recorded`'s exact new count. A ~250ms debounced
  full-snapshot reconciliation is the safety net; verify the current list in
  `multiagent-live.js`'s `derivePatches` comments, as it may be refined.
- **Replay mode has the same imprecision** for the same event types, without
  live mode's automatic reconciliation to correct it mid-replay — it
  self-corrects only at "jump to end," which renders the real final
  snapshot.
- **`waiting_validation` and `blocked` node statuses are reserved but
  currently unreachable.** They exist in the type system for
  forward-compatibility; no Phase 1 code path produces them today.
  `disabled` and `invalid` edge statuses are similarly reserved for
  non-reference workflow definitions.
- **The graph renderer only has a hand-tuned layout for the fixed Phase 1
  reference topology** (planner → developer → tester → reviewer). A
  hypothetical non-reference workflow definition would render nodes/edges via
  a plain overflow fallback rather than a meaningful layout.
- **A real ULID-entropy ordering bug in `internal/event` was found and fixed
  during Phase 2's final hardening pass.** `event.NewID()` now explicitly uses
  `ulid.DefaultEntropy()` (a thread-safe, per-process, monotonically
  increasing source) rather than plain `crypto/rand` entropy — plain
  `crypto/rand` made two ULIDs minted within the same millisecond sort in
  effectively random relative order, which would silently break the
  `ORDER BY id ASC` chronological-ordering guarantee this dashboard's event
  cursor, live patch dedup, and replay ordering all depend on. This is
  reflected in the current code (`internal/event/event.go`); it is called out
  here because it's foundational to the correctness of everything in
  [Live event behavior](#live-event-behavior) and [Replay mode](#replay-mode).
- **Frontend tests have no build step.** The 7 test files under
  `internal/dashboard/static/tests/` (`multiagent-graph.test.js`,
  `multiagent-live.test.js`, `multiagent-animation.test.js`,
  `multiagent-inspector.test.js`, `multiagent-replay.test.js`,
  `multiagent-resilience.test.js`, `multiagent-operator-controls.test.js`)
  run via plain `node <file>.test.js` — no `package.json`, no bundler — by
  deliberate design, matching this dashboard's zero-dependency philosophy.
- **One reference workflow definition.** Like the CLI control plane this
  dashboard sits on top of, it observes the single Phase 1
  `multi-agent-software-task` definition; there is no multi-workflow-
  definition browsing or comparison.

## Recommended Phase 3 roadmap

Based on the gaps actually observed while building and documenting this
dashboard (not speculative feature requests):

1. **Dashboard-wide auth-token support for mutating requests and SSE.** Give
   `app.js`'s shared `request()` helper (and the two pages that open
   `EventSource` connections) a stored-token mechanism, so Pause/Resume/
   Cancel and the live event stream work correctly once a server auth token
   is configured. This is the single highest-value fix: today, enabling auth
   silently breaks this feature's mutating controls and live updates.
2. **An approval-ID-carrying data model.** Add an approval identifier to
   `WaitingState` (or a way to resolve one from a `Waiting.Kind === "approval"`
   run), so a future Approve/Reject control can be wired here instead of
   directing operators to a separate mechanism.
3. **A ground-truth lookup for replay's reconciliation-only fields.** A
   point-in-time snapshot endpoint (or embedding the missing fields directly
   in `handoff.created`/`budget.warning`/`loop.traversal.recorded` payloads)
   would remove replay's one documented imprecision without waiting on
   "jump to end."
4. **Multi-workflow-definition support**, once the runtime itself supports
   more than the single Phase 1 reference definition — a generic graph
   layout (not just the current hand-tuned reference topology) and node/edge
   ID handling that doesn't fall back to a plain overflow row.
5. **Promote `waiting_validation` and `blocked` from reserved to real**, if
   and when a Phase 1+ code path actually produces `RoleStatusBlocked` or a
   distinguishable validation-wait — until then, leave them exactly as
   documented rather than fabricating a path just to exercise them.

## See Also

- [Developer-Authored Workflow Graphs](../workflows/README.md) — Phase 3's
  YAML/JSON workflow authoring, validation, testing, and registry, which
  this dashboard observes via the exact same `CompiledGraph`-derived
  `RunGraph` with zero dashboard code changes (see
  [Current limitations](../workflows/limitations.md) for what dashboard
  integration still does not cover — definition browsing and a version
  selector, in particular).
- [Multi-Agent Loop Runtime Contracts](MULTI_AGENT_LOOP_RUNTIME.md) — the
  runtime this dashboard observes, including the delegation-graph vs
  execution-graph distinction.
- [Multi-Agent Software Task Workflow](../MULTI_AGENT_WORKFLOW.md) — the CLI
  control plane (`prizm workflow status|cancel|resume|report`) this
  dashboard complements.
- [Multi-Agent Runtime Roadmap](MULTI_AGENT_RUNTIME_ROADMAP.md) — the larger,
  multi-year architectural staging this feature sits within (Graph Runtime,
  Visual Graph Editor).
- [Dashboard Guide](../dashboard/README.md)
- [Accessibility](../dashboard/accessibility.md)
- [Frontend Performance](../dashboard/frontend-performance.md)
- [Package Boundaries](PACKAGE_BOUNDARIES.md)
