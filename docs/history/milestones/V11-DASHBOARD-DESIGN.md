# V11 — Dashboard / Event Explorer Design

## Mission

Give Prizm a visual interface for exploring runs, events, approvals, policies, projections, and artifacts — served locally from the CLI, not hosted in the cloud.

## Problem

Right now, understanding what happened in a Prizm run means reading JSON files.
`summary.json` gives you the overview. `events.jsonl` gives you the full stream.
`projections/` gives you derived state. But there's no way to *see* it — you have
to parse files yourself.

V11 adds `prizm dashboard` — a local HTTP server that serves a single-page HTML
dashboard. Open it in your browser, see your runs, drill into events, inspect
approvals, check projections, all without leaving your machine.

## Core Constraint

**V11 is CLI-served.** `prizm dashboard` starts a local HTTP server (default
`localhost:8080`) and serves a self-contained HTML page. No cloud hosting, no
external dependencies, no build step. The dashboard is a single HTML file with
embedded CSS and JavaScript. No React, no Webpack, no npm.

## Architecture

```
prizm dashboard [--port 8080] [--run-dir ./runs]
    ↓
Local HTTP Server (net/http)
    ↓
┌──────────────────────────────────────┐
│  GET /           → index.html        │
│  GET /api/runs    → list all runs     │
│  GET /api/runs/:id → run detail       │
│  GET /api/events/:id → events.jsonl   │
│  GET /api/projections/:id/:name       │
│  GET /api/policies  → policy list     │
└──────────────────────────────────────┘
    ↓
Browser (http://localhost:8080)
```

The dashboard has two layers:
1. **API layer** — Go HTTP handlers that read local files and return JSON
2. **UI layer** — Single HTML file with embedded CSS/JS that calls the API

## What V11 Adds

### 1. Dashboard Server (`internal/dashboard/server.go`)

```go
// Package dashboard provides a local web dashboard for Prizm.
//
// Start with: prizm dashboard
// Opens at: http://localhost:8080
//
// The dashboard reads local run data (events, summaries, projections,
// policies) and serves it through a simple HTTP API. The UI is a
// self-contained HTML page with embedded CSS and JavaScript.
// No external dependencies, no build step, no cloud hosting.
package dashboard

// Server serves the Prizm dashboard over HTTP.
type Server struct {
    addr    string     // listen address (e.g., ":8080")
    runDir  string     // path to runs/ directory
    policyDir string   // path to policies/ directory
    mux     *http.ServeMux
}

// NewServer creates a dashboard server.
func NewServer(addr, runDir, policyDir string) *Server

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error
```

### 2. API Handlers (`internal/dashboard/api.go`)

```
GET /api/runs              → List all runs with status
GET /api/runs/:id          → Run detail (summary + projection names)
GET /api/events/:id        → Events for a run (parsed from events.jsonl)
GET /api/projections/:id/:name → Projection snapshot
GET /api/policies          → List policy rules from YAML
GET /api/adapters          → List registered adapters
```

Each handler reads local files and returns JSON. No database, no caching,
no external services.

### 3. Dashboard UI (`internal/dashboard/ui.go`)

A single HTML file embedded in the Go binary using `embed.FS`.
Contains all CSS and JavaScript inline. No external assets.

**Dashboard sections:**
1. **Runs List** — All runs with status, task, agent, timing
2. **Run Detail** — Click a run → see summary, events timeline, projections
3. **Events Timeline** — Visual event stream with type coloring
4. **Approval Status** — Pending/approved/denied approvals
5. **Tool Calls** — Tool history with policy decisions
6. **Policy Rules** — Current policy configuration
7. **Adapters** — Registered adapters with health

### 4. CLI Command

```bash
./prizm dashboard                    # Start on :8080
./prizm dashboard --port 3000        # Custom port
./prizm dashboard --run-dir ./runs   # Custom run directory
```

## UI Design Principles

1. **Self-contained.** One HTML file. No build step. No npm. The CSS and JS
   are embedded directly. This means we can't use Tailwind or React — and
   that's intentional. The dashboard should feel like a Go tool, not a web app.

2. **Read-only.** The dashboard shows data. It does NOT approve, deny, execute,
   or mutate anything. All write operations go through the CLI.

3. **Local-first.** The server binds to localhost. No remote access. The data
   never leaves your machine.

4. **Minimal.** Clean, functional, not flashy. The dashboard is a developer
   tool, not a marketing page.

5. **Auto-refresh.** The dashboard polls `/api/runs` every 5 seconds to show
   new runs without requiring a page refresh.

## What V11 Does NOT Include

- **Write operations** — no approve/deny/execute from the dashboard
- **User authentication** — localhost only, no auth needed
- **Real-time WebSocket** — polling is sufficient for a local tool
- **Multi-user** — single developer, single browser tab
- **Cloud hosting** — `prizm dashboard` serves locally, period
- **Build pipeline** — no npm, no Webpack, no TypeScript
- **Mobile-responsive** — desktop-first, not mobile-first
- **Embeddable** — not designed to be embedded in other apps

## File Layout

```
internal/dashboard/
├── server.go          # HTTP server setup, ListenAndServe
├── api.go             # API handlers (/api/*)
├── ui.go              # Embedded HTML/CSS/JS via embed.FS
├── static/
│   └── index.html     # Dashboard HTML (embedded at compile time)
└── dashboard_test.go  # API handler tests

cmd/prizm-cli/main.go  # Updated with "dashboard" subcommand
```

## Tests Required

- Server starts and responds on configured port
- API handlers return correct JSON for runs, events, projections, policies
- API returns 404 for missing runs/projections
- HTML is served at GET /
- All existing tests pass (319+)

## Acceptance Criteria

1. `prizm dashboard` starts a local HTTP server
2. Dashboard shows runs list at http://localhost:8080
3. Clicking a run shows events timeline and projections
4. API endpoints return JSON for runs, events, projections, policies, adapters
5. Dashboard is self-contained (one HTML file, embedded in binary)
6. Dashboard is read-only (no write operations)
7. Server binds to localhost only
8. Auto-refresh shows new runs
9. All existing tests pass (319+)
10. README documents V11 truthfully