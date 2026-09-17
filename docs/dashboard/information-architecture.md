# Information architecture

Primary navigation is Overview, Operations, Workflow, Agents, Scheduler, and
Settings. Overview answers health, active work, approvals, failures, and recent
activity. Editing remains in the existing Workflow, Scheduler, and Settings
surfaces; the dashboard does not invent new runtime controls.

Multi-Agent Runs (`multiagent-runs.html`, reachable from the nav) lists every
durable multi-agent run and links to a per-run execution graph page
(`multiagent-run.html`). That page is read-only observability plus three
narrow, existing-authority operator actions (pause/resume/cancel) — it does
not add a graph editor or a second source of runtime truth. See
[Multi-Agent Execution Graph Dashboard](../architecture/MULTI_AGENT_EXECUTION_GRAPH.md).
