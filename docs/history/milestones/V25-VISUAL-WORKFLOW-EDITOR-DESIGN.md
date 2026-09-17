# V25 — Visual Workflow Editor Design

**Date:** 2026-05-25
**Author:** Lumi
**Status:** ✅ Complete (merged to main)

## Overview

V24 generates static SVG diagrams from Prizm config. V25 makes them **editable** — drag agents, connect delegation paths, add/remove nodes, and the changes write back to `prizm.yaml`.

The key insight: the diagram IS the config. Edit the diagram, edit the system.

## Architecture

### Data Flow

```
prizm.yaml → ConfigLoader → AgentConfig[] → EditorState → SVG Renderer
                                                     ↕
                                              Drag/Drop/Edit
                                                     ↕
                                          EditorState → ConfigWriter → prizm.yaml
```

### Editor State Model

```go
type EditorNode struct {
    ID           string         `json:"id"`
    Type         string         `json:"type"`    // "agent", "approval", "process"
    Label        string         `json:"label"`
    Role         string         `json:"role"`    // lead, coder, researcher, orchestrator
    Model        string         `json:"model"`
    Primary      bool           `json:"primary"`
    Capabilities []string       `json:"capabilities"`
    Position     Position       `json:"position"` // x, y for layout
    Subscriptions []string      `json:"subscriptions"`
}

type EditorEdge struct {
    ID       string `json:"id"`
    From    string `json:"from"`    // node ID
    To      string `json:"to"`      // node ID
    Type    string `json:"type"`    // "delegation", "review", "approval", "event"
    Label   string `json:"label"`   // optional label
    Style   string `json:"style"`   // "solid", "dashed", "dotted", "bold"
}

type Position struct {
    X int `json:"x"`
    Y int `json:"y"`
}

type EditorState struct {
    Nodes []EditorNode `json:"nodes"`
    Edges []EditorEdge  `json:"edges"`
}
```

### API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | /api/v1/workflows/editor | Get current config as EditorState |
| PUT | /api/v1/workflows/editor | Update config from EditorState |
| GET | /api/v1/workflows/editor/nodes | List all nodes |
| POST | /api/v1/workflows/editor/nodes | Add a node |
| PUT | /api/v1/workflows/editor/nodes/{id} | Update a node (position, role, etc.) |
| DELETE | /api/v1/workflows/editor/nodes/{id} | Remove a node |
| GET | /api/v1/workflows/editor/edges | List all edges |
| POST | /api/v1/workflows/editor/edges | Add an edge |
| DELETE | /api/v1/workflows/editor/edges/{id} | Remove an edge |

### Config Writer

The ConfigWriter takes an EditorState and produces a valid `prizm.yaml`:

- Agent nodes → `agents:` section
- Delegation edges → `subscriptions:` on agents
- Approval edges → approval config
- Role, model, capabilities → agent fields

### Dashboard Integration

Replace the static V24 workflow tab with an interactive editor:

1. **Canvas** — SVG with draggable nodes
2. **Toolbar** — Add agent, add edge, delete, save, reset
3. **Properties panel** — Click node to edit role, model, capabilities
4. **Edge drawing** — Click source node → drag to target node → creates edge
5. **Save** — PUT to /api/v1/workflows/editor → writes prizm.yaml

### Layout Algorithm

Auto-layout uses a simple directed graph layout:
1. Primary agent at top center
2. Delegation targets spread below
3. Review edges loop back
4. Approval diamonds offset to the side

Manual layout: nodes store position, edges route automatically.

## Milestones

| ID | Description | Status |
|----|-------------|--------|
| M6.1 | EditorState model + ConfigWriter | ✅ |
| M6.2 | API endpoints for editor CRUD | ✅ |
| M6.3 | Dashboard interactive editor | ✅ |
| M6.4 | Config round-trip test | ✅ |
| M6.5 | Edge drawing + deletion | ✅ |
| M6.6 | Save/write-back to prizm.yaml | ⬜ |