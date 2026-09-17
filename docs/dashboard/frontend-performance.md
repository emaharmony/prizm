# Frontend performance

The embedded dashboard avoids a frontend framework and large dependencies. The
overview makes bounded concurrent requests to existing endpoints, renders only the
latest task/activity subset, uses one shared request helper with cancellation and a
timeout, and does not start unbounded polling. Data refresh is user initiated.

The multi-agent execution graph page (`multiagent-run.html`) instead live-updates
by design: it opens one server-sent-events connection per run, applies narrow DOM
patches per event, and falls back to a ~250ms debounced full-snapshot
reconciliation only for the handful of fields with no unambiguous per-event patch.
Server-side, the stream backfills in bounded batches and then polls the event
store on a fixed 750ms interval (there is no push path from SQLite) with a 30s
heartbeat. Replay mode makes no network requests at all — it steps through the
already-fetched event history client-side. See
[Multi-Agent Execution Graph Dashboard](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md#live-event-behavior)
for details.
