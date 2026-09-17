# V24 — Visual Workflow Representations Design

**Date:** 2026-05-25
**Author:** Lumi
**Status:** ✅ Complete (merged to main)

## Overview

V24 generates visual representations of Prizm's workflows — agents, delegation flows, approval gates, feedback loops, event streams. These are auto-generated from the running Prizm config and state, rendered as SVG diagrams that can be viewed in the dashboard or exported.

V25 will make these editable (diagrams become config), but V24 focuses on **accurate, beautiful, read-only visualizations**.

## Workflow Types

### 1. Agent Topology Diagram
Shows all configured agents and their relationships:
- Boxes for each agent (Lumi, Mango, Junie)
- Role badges (lead, coder, researcher)
- Capability tags inside each box
- Primary agent highlighted
- Arrows showing delegation paths (who can delegate to whom)

### 2. Feedback Loop Diagram
The Three Feedback Loops (Lumi×Mango):
- **Pre-Dev Architecture Check**: Lumi → Mango (plan review)
- **Mid-Dev Correctness Check**: Mango → Lumi (implementation review)
- **Post-Dev Vulnerability Analysis**: Mango → Lumi (security review)
- Different line styles for each loop type

### 3. Delegation Flow Diagram
Shows the task delegation pipeline:
- Lumi (lead) → Mango (coder) → Junie (small tasks)
- DelegationStage in the pipeline
- Task lifecycle: created → assigned → in_progress → completed/failed
- Capability enforcement at each delegation point

### 4. Approval Gate Diagram
Shows approval flows:
- Agent requests approval → User approves/denies
- Discord reaction → approval event → task status update
- Different approval types (push, deploy, delete, merge)

### 5. Event Flow Diagram
Shows the event bus topology:
- Per-agent namespaces (lumi.*, mango.*, remembrance.*)
- System events (prizm.*)
- Adapter events (adapter.discord.*)
- Registered actions (event → action triggers)

## Architecture

### SVG Generation
- Go package `internal/workflow/` generates SVG diagrams
- Uses `github.com/ajstarks/svgo` for SVG rendering
- Each diagram type is a separate function
- Config-driven: reads from `prizm.yaml` and runtime state
- API endpoint: `GET /api/v1/workflows/{type}/svg`

### Diagram Conventions
- **Solid arrows**: Direct delegation (Lumi → Mango: "code this")
- **Dashed arrows**: Async events (`mango.task.completed`)
- **Dotted arrows**: Review loops (Mango → Lumi: review request)
- **Bold lines**: Approval gates (requires human ✅/❌)
- **Rounded boxes**: Agents
- **Diamond shapes**: Decision/approval points
- **Color coding**: Lead=blue, Coder=green, Researcher=orange, System=purple

### Rendering
- SVG output with embedded CSS
- Responsive viewBox for scaling
- Hover states for interactivity (V25 foundation)
- Dark theme matching dashboard aesthetic

## Milestones

| ID | Description | Status |
|----|-------------|--------|
| M5.1 | Agent Topology SVG generator | ⬜ |
| M5.2 | Feedback Loop SVG generator | ⬜ |
| M5.3 | Delegation Flow SVG generator | ⬜ |
| M5.4 | Approval Gate SVG generator | ⬜ |
| M5.5 | Event Flow SVG generator | ⬜ |
| M5.6 | Dashboard integration (workflow tab) | ⬜ |
| M5.7 | API endpoint for SVG retrieval | ⬜ |