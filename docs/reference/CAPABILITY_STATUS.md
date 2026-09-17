# Prizm Capability Status

Prizm is a preview-stage, source-available project. This page summarizes which
capabilities are safe to rely on versus experimental. It is a guide, not a
guarantee — always confirm against the source and `prizm --help`.

> Prizm is not open source and is not production-ready. Do not treat any
> capability as production-grade.

## Status Legend

- **Preview/Stable** — implemented and exercised by demos/tests; interface is
  fairly settled.
- **Preview** — implemented and usable; interface may still change.
- **Experimental** — implemented but advanced/optional; expect rough edges and
  changes.

## Capability Summary

| Area | Status | Notes |
|---|---|---|
| Build & test (`cmd/prizm-cli`, `go test ./...`) | Preview/Stable | Core developer workflow |
| Workflow runtime (`workflow list/show/run/status`) | Preview/Stable | Echo demo is the canonical first run |
| Built-in tools (`tool list/run`) | Preview | Local validators enforce input safety |
| Policy engine (`policy list/evaluate`) | Preview | Default rules in `policies/default.yaml` |
| Approval-gated mutations (`approval ...`) | Preview | File writes require approval |
| Adapters (`adapter ...`, echo adapter) | Preview | Domain adapters gated by policy |
| Serve mode (API, SSE, dashboard) | Preview | Loopback by default; token required off-loopback |
| Run inspection (`runs`, `cost`, `trace`, `watch`) | Preview | Artifacts: events.jsonl, prompt.md, output.md, summary.json |
| Config tooling (`config`, `doctor`, `config wizard`) | Preview | Validate/summarize `prizm.yaml` |
| Multi-agent delegation | Experimental | Requires configured agents + provider |
| Providers (Ollama/OpenAI/Anthropic/Gemini/Claude Code) | Experimental | API-backed providers need credentials |
| Scheduler (cron jobs) | Experimental | Disabled by default |
| MCP tool servers | Experimental | Disabled by default; probe before enabling |
| Remembrance memory service | Experimental | Separate Python service, disabled by default |
| Autopatch / self-patching (`scan`) | Experimental | Disabled by default |
| Cross-Prizm bridge / Factory handoff | Experimental | Disabled by default |
| Vector search / projections | Experimental | Advanced/optional |
| Free Mode / shell tool (V60) | Experimental | Disabled by default; single-owner Discord bypass of the approval gate — see [Safety Model](../concepts/SAFETY.md#free-mode-owner-authorized-mutation-mode) |

## Safe Defaults

- HTTP API binds to loopback (`127.0.0.1`) by default.
- File mutations require operator approval, **except** for the one Discord
  user configured as `shell.master_user_id` in a channel set to
  `mode: free` — see [Safety Model](../concepts/SAFETY.md#free-mode-owner-authorized-mutation-mode).
- The shell tool is registered but requires approval by default (gated
  mode); it is auto-approved only under Free Mode for the configured master
  user, and its working directory is not restricted to the workspace even
  then.
- Optional integrations (autopatch, MCP, bridge, scheduler, remembrance,
  Free Mode) are disabled until explicitly enabled.

## See Also

- [Getting Started](../getting-started/GETTING_STARTED.md)
- [Configuration Guide](../operations/CONFIGURATION.md)
- [Roadmap](../history/ROADMAP.md)
