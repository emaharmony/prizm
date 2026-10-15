# Prizm Architecture

A concise map of how Prizm works internally. For the full design rationale and
per-version design notes, see [DESIGN.md](../history/DESIGN.md) and the `V*-DESIGN.md`
documents in this directory.

## Overview

Prizm is a Go, event-native AI agent platform that runs as a persistent service.
Agents communicate through a NATS event bus, maintain conversation sessions,
remember context through Remembrance, use tools under policy, and expose a local
API and dashboard.

The framework controls lifecycle, safety, context, routing, and persistence. The
model generates outputs inside that lifecycle.

## Runtime Components

```text
Prizm Runtime
  - Config + orchestrator
  - Embedded or external NATS JetStream
  - SQLite-backed sessions, tasks, approvals, events, and run artifacts
  - Agent router, tool executor, policy/guard checks, plan/state managers
  - Sub-agent worker: bounded tool-loop delegation with worktree isolation
  - Durable multi-agent runtime: event outbox (idempotent commands, leases,
    claim/retry), exact approval identity through apply/resume, restart
    reconciliation (R1)
  - Scoped memory lifecycle: capture/search/supersede scoped to
    user/project/task, idempotent Recall sync with local fallback (R2)
  - Remembrance HTTP client and cache
  - Freewill intention contracts (policy-only, flag-gated off)
  - HTTP API, SSE event stream, dashboard, visual workflow editor
```

## Key Concepts

- **Event-driven** — the canonical event types live in `internal/event`; agents
  and components communicate over NATS subjects.
- **Providers** — LLM provider implementations live in `internal/provider/`
  (Mock, Ollama, OpenAI, OpenAI Responses, Anthropic, Gemini, Claude Code CLI).
- **Tools under policy** — the tool executor runs built-in tools; the policy
  engine and guard checks decide permission; local validators enforce input
  safety.
- **Workflows** — named step pipelines (`internal/workflow`) run steps in order
  and emit events; the gated loop drives longer autonomous runs.
- **Persistence** — SQLite stores sessions, tasks, approvals, events, and run
  artifacts. No external database or broker is required for basic operation.
- **Durable commands (outbox)** — delegation and proposal commands go through
  a SQLite-backed outbox (`internal/event/outbox.go`): idempotency keys make
  duplicate deliveries resolve as already-recorded, leases make crash-retry
  safe, and acknowledgement happens only after state persistence (R1).
- **Scoped memory (R2)** — the memory pipeline (`internal/memory`) scopes
  capture, retrieval, and supersession to user/project/task; Recall sync is
  idempotent and falls back to local storage on outage, reconciling later
  without scope leakage.
- **Run artifacts** — each run writes `events.jsonl`, `prompt.md`, `output.md`,
  and `summary.json`.

## Package Layout

All application code lives under `internal/` as focused domains (e.g.
`internal/event`, `internal/session`, `internal/tool`, `internal/policy`,
`internal/workflow`, `internal/provider`). The CLI is in `cmd/prizm-cli/`. See
the README "Core Packages" table for the full list.

## Related Docs

- [Multi-Agent Loop Runtime Contracts](MULTI_AGENT_LOOP_RUNTIME.md)
- [Multi-Agent Workflow Operations](../MULTI_AGENT_WORKFLOW.md)
- [Package Boundaries](PACKAGE_BOUNDARIES.md)
- [Multi-Agent Runtime Roadmap](MULTI_AGENT_RUNTIME_ROADMAP.md)
- [Getting Started](../getting-started/GETTING_STARTED.md)
- [Configuration Guide](../operations/CONFIGURATION.md)
- [Capability Status](../reference/CAPABILITY_STATUS.md)
- [Design Notes](../history/DESIGN.md)
