# Accessibility

The shared shell provides a skip link, semantic primary navigation, active-route
indication, visible keyboard focus, a responsive menu button with `aria-expanded`,
and a live service-health status. Overview uses labeled filters, table headers,
mobile labels, and explicit loading, empty, and error copy. Reduced-motion users
receive disabled decorative animation.

The multi-agent execution graph page (`multiagent-run.html`) follows the same
conventions at graph scale: every async region (loading, run overlays, replay
banner, operator status, budget-near-exhaustion warnings) is an
`aria-live="polite"` region rather than color/animation alone; graph nodes and
edges are individually focusable with descriptive `aria-label`s and respond to
`Enter`/`Space` like a click; and `prefers-reduced-motion: reduce` disables the
current-node pulse and active-edge marching-ants animations. See
[Multi-Agent Execution Graph Dashboard](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md#accessibility)
for the full accessibility notes.
