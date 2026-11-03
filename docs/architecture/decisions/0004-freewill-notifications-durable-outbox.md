# ADR 0004: Freewill lifecycle notifications via durable outbox

Status: Accepted (2026-11-03)

## Context

Freewill produces human-relevant lifecycle facts: a candidate proposal awaits
a decision, a decision was made, a policy rule denied a proposal, a selection
was halted, or a decision needed reconciliation after a restart. Operators
want these surfaced in real time (Discord), but the earlier integration had
three structural flaws this ADR closes permanently:

1. **Lossy transport.** A plain NATS subscription at serve startup missed every
   event emitted while the process was down (restarts, upgrades, crashes).
2. **Enrichment at the wrong layer.** Event payloads carried opaque candidate
   IDs; each consumer had to reopen the durable run store to render anything
   human-readable.
3. **Unauthenticated topic.** Anyone with NATS access could publish to the
   `prism.freewill.*` subjects and fabricate notification cards, because the
   relay trusted transport-level topics instead of canonical state.

## Decision

Notifications are a **durable, source-enriched pipeline**, not a transport
subscription:

1. **Enrichment at the emit point.** Freewill decision checkpoints
   (`freewillEventContext`, `internal/workflow/multiagent/freewill_approval.go`)
   embed human-meaningful fields — `selected_title`, `selected_score`,
   `agent_id`, `project_id`, `workspace`, `decision`, `policy_rule_id`,
   `approval_ref`, and the full candidate list — directly into the canonical
   event payloads at checkpoint time. Consumers are dumb renderers; no
   consumer ever re-reads the durable run store to draw a card.

2. **Durable delivery.** `FreewillNotificationService`
   (`internal/workflow/multiagent/freewill_notification.go`) owns a dedicated
   SQLite outbox (`<freewill.db>-notifications.db`) drained by the shared
   `event.Dispatcher` (retry with backoff, lease-claimed, terminal on
   repeated failure, idempotent by canonical event ID). A reconciler scans
   the canonical event store with a persisted cursor every 15 seconds and at
   startup, enqueuing any curated event the outbox has not seen. A crash
   between notify and mark-delivered replays the same event ID; consumers
   must be idempotent per event.

3. **Curated visibility.** Only five event types are notify-worthy
   (`FreewillNotifyWorthyEventTypes`): `approval.proposed`,
   `approval.decided`, `approval.reconciled`, `policy.denied`,
   `selection.halted`. Per-attempt chatter (wake claims, selection
   started/retries) is excluded by construction, not by consumer filtering.

4. **Discord sink, nothing more.** `freewillDiscordSink` (cmd/prizm-cli)
   formats enriched payloads into cards. Approve/Deny buttons render only
   when `freewill.notify_allow_user_ids` is configured; button clicks are
   authorized against that allowlist and execute through the same
   `FreewillApprovalService.DecideStatus` path as the dashboard and API, with
   the actor recorded as `discord:<user>`. Decisions made via Discord
   therefore satisfy the same audit trail as every other surface.

5. **Configuration stays local and explicit.** `freewill.notify_channel_id`
   (empty = off) and `freewill.notify_allow_user_ids` (empty = card-only)
   default to silence. Notification delivery is opt-in per deployment; the
   upstream defaults remain unchanged for other operators.

## Consequences

- Restarting `prizm serve` can no longer lose notifications: the canonical
  store plus outbox plus reconciler reconstruct the deliverable set.
- Fabricating a card now requires writing to the canonical event store —
  the same trust boundary as every other Prizm fact — not to a bus topic.
- Payload growth is negligible (a few enriched fields per checkpoint), and
  the enrichment moves with the record, benefiting future consumers without
  further integration work.
- The reconciler's cursor lives in the notification outbox database itself,
  so cursor and delivery state share SQLite durability and backup semantics.