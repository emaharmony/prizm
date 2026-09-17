# V23 — Platform Design

**Date:** 2026-05-25
**Author:** Lumi
**Status:** ✅ Complete (merged to main)

## Overview

V23 transforms Prizm from a CLI + Discord tool into a platform with an HTTP API, real-time event streaming, and multi-Prizm communication. This is the foundation for V24 (Visual Representations) and V25 (Visual Workflow Editor).

## Milestones

### M4.1: HTTP API (REST + SSE)

REST endpoints for external integrations. SSE for real-time event streaming.

**Endpoints:**
- `GET /api/v1/status` — System status (agents, sessions, bus)
- `GET /api/v1/agents` — List agents with status
- `GET /api/v1/agents/{id}` — Agent detail
- `GET /api/v1/sessions` — List active sessions
- `GET /api/v1/sessions/{id}` — Session detail
- `GET /api/v1/tasks` — List tasks (all statuses)
- `GET /api/v1/tasks/{id}` — Task detail
- `GET /api/v1/approvals` — List pending approvals
- `POST /api/v1/approvals/{id}/grant` — Grant approval
- `POST /api/v1/approvals/{id}/deny` — Deny approval
- `GET /api/v1/events/stream` — SSE real-time event stream
- `GET /api/v1/workflows` — List workflows
- `GET /api/v1/costs` — Cost summary

**SSE Event Stream:**
- Subscribes to all NATS subjects (or filtered by query param)
- Streams events as JSON with `event:` and `data:` fields
- Heartbeat every 30s to keep connection alive
- Reconnect support with `Last-Event-ID`

### M4.2: Dashboard v2

Upgrade the existing dashboard to use the new API. Add real-time event stream, cost tracking, agent status, and task/approval views.

**Features:**
- Real-time event stream panel (SSE)
- Agent status cards (online/idle/offline)
- Task tracker view (active, stuck, completed)
- Approval queue (grant/deny from dashboard)
- Cost tracking summary
- Session list with last active time

### M4.3: Multi-Prizm Communication

Two Prizm environments can discover each other and exchange events/tasks.

**Design:**
- NATS leafnode or gateway for cross-Prizm communication
- Each Prizm advertises its agents on a shared subject
- Tasks can be delegated to agents on remote Prizms
- Events propagate across the mesh with origin tagging

**Implementation:**
- `internal/bridge/bridge.go` — Bridge manages connections to remote Prizms
- Bridge config in `prizm.yaml`: remote endpoints, shared secrets
- Events published with `origin` field to prevent loops
- Health checks on remote connections

### M4.4: Adapter SDK

Development kit for third-party adapters.

**Design:**
- `Adapter` interface with lifecycle methods: `Init`, `Start`, `Stop`, `Handle`
- `EventBus` interface adapters use to publish/subscribe
- Manifest YAML for metadata (name, version, events)
- Built-in test harness for adapter testing

### M4.5–M4.7: IoT, Business Template, License

Deferred to after V24/V25 scope is clearer.

## Architecture Decisions

1. **HTTP API is separate from dashboard** — API server can run without dashboard
2. **SSE for real-time** — simpler than WebSocket, works with proxies, native browser support
3. **API versioning** — `/api/v1/` prefix for future compatibility
4. **Dashboard is still self-contained** — no React, no npm, inline HTML+CSS+JS
5. **Multi-Prizm uses NATS gateway** — not HTTP, to preserve event bus semantics
6. **Bridge events tagged with origin** — prevents loops in mesh topology
7. **Adapter SDK uses Go interfaces** — no plugins, no CGO, compile-time safety

## Pipeline Extension

No pipeline changes. HTTP API and dashboard are adapters that subscribe to the event bus. Bridge is a new component that connects event buses.