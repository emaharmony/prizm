# Prizm Configuration Guide

This guide is a map of Prizm configuration: where files live, how they relate,
and what is stable versus experimental. For field-by-field YAML documentation
see the [YAML Reference](../reference/YAML_REFERENCE.md).

## Configuration Philosophy

- **Local-first** — Prizm embeds NATS JetStream and uses SQLite. No external
  database or broker is required for basic operation.
- **Explicit config** — the main runtime config is a single `prizm.yaml`. Copy
  `prizm.yaml.example` and edit it.
- **Safe defaults** — the HTTP API binds to loopback (`127.0.0.1`) by default;
  file mutations require approval; shell execution is denied by policy.
- **Adapters are opt-in** — external adapters and integrations are disabled
  unless configured.
- **Experimental features are off by default** — autopatch, MCP servers,
  bridge, scheduler, remembrance, and factory monitoring are disabled until you
  enable them explicitly.

## Configuration File Locations

| File/Directory | Purpose | Required? |
|---|---|---:|
| `prizm.yaml` | Main Prizm runtime config (agents, channels, actions, serve) | For serve/chat |
| `prizm.yaml.example` | Annotated template to copy from | Reference |
| `policies/default.yaml` | Default allow/deny/approval policy rules | Recommended |
| `examples/workflows/*.yaml` | Workflow definitions loaded by `workflow` commands | Optional |
| `prizm-workspace/` | Workspace context (SOUL.md, AGENTS.md, USER.md, ...) | Optional |
| `prizm-workspace.example/` | Example workspace layout | Reference |
| `runs/` | Default run artifact output directory | Auto-created |
| `.env` | Local environment variables/secrets | Optional, do not commit |

> The example config points `workflow_config` at
> `examples/workflows/gated-loop.yaml`. Adjust paths to match your setup.

## Configuration Types

- **Runtime config** (`prizm.yaml`) — the primary file. Configures the instance,
  paths, agents, channels, actions, sessions, and optional integrations.
- **Policy config** (`policies/default.yaml`) — allow/deny/approval rules.
- **Workflow config** (`examples/workflows/*.yaml`) — named step pipelines and
  the gated-loop definition.
- **Workspace context** (`prizm-workspace/`) — Markdown context injected into
  prompts.

## Minimal Configuration

For the echo-workflow demo you need **no** config file. To run serve mode, start
from the example and keep defaults:

```bash
cp prizm.yaml.example prizm.yaml
```

A minimal `prizm.yaml` core block:

```yaml
prizm:
  instance_id: "my-prizm"
  nats_url: ""            # empty = embedded NATS
  data_dir: "~/.prizm/data"
  port: 8321             # HTTP API listens on port+1
  bind_host: "127.0.0.1"
  log_level: "info"
```

## Workflow Configuration

`prizm.workflow_config` points at a gated-loop workflow YAML (e.g.
`examples/workflows/gated-loop.yaml`). When set and loadable, it overrides the
built-in default phases. Named workflows for the `workflow` commands are loaded
from `examples/workflows/`. See the [YAML Reference](../reference/YAML_REFERENCE.md#workflow-yaml).

## Policy Configuration

Policy decides permission; local validators still enforce input safety. The
default rule set is in `policies/default.yaml`. See the
[YAML Reference](../reference/YAML_REFERENCE.md#policy-yaml).

## Provider / Model Configuration

Providers are selected per agent (`provider:` / `model:`) in `prizm.yaml`, or via
`--provider` / `--model` flags on `prizm run`. Supported providers include
`mock`, `ollama`, `openai`, `openai_responses`, `anthropic`, `gemini`,
`claude_code`. API-backed providers require credentials via environment
variables.

## Agent Configuration

Agents are defined under the `agents:` list in `prizm.yaml`. Each agent has an
`id` (which becomes its event namespace prefix), a `role`, a `provider`/`model`,
`context` layers, and `capabilities`. See the
[YAML Reference](../reference/YAML_REFERENCE.md#agent-yaml).

## Adapter Configuration

Adapters are opt-in external integrations. Inspect them with `prizm adapter
list|show|health`. The echo adapter is safe and used by the adapter demo
workflow. See the [YAML Reference](../reference/YAML_REFERENCE.md#adapter-yaml).

## Dashboard / Serve Configuration

`prizm serve` starts the API, SSE stream, and dashboard from a single process.
Relevant `prizm.yaml` fields:

- `prizm.port` — health port; the HTTP API/dashboard listen on `port+1`.
- `prizm.bind_host` — `127.0.0.1` (default) or `0.0.0.0` (requires
  `api.auth_token` / `api.auth_token_env`).
- `api.auth_token` / `api.auth_token_env` — bearer token for state-changing
  endpoints and the SSE stream.
- `api.allowed_origins` — CORS allowlist.

## Remembrance Configuration

Remembrance is a separate Python memory service, disabled by default:

```yaml
remembrance:
  enabled: false
  url: "http://localhost:18790"
  timeout_seconds: 60
```

## MCP Configuration

External MCP tool servers are configured under `mcp_servers:` (empty by
default). MCP tools require approval unless `mcp_auto_approve: true`. Probe a
server before enabling with `prizm mcp probe`.

## Scheduler Configuration

The built-in cron scheduler is under `prizm.scheduler` (disabled by default).
Each job fires a NATS event on a schedule. Full guide:
[docs/SCHEDULER.md](SCHEDULER.md).

## Environment Variables

- `OLLAMA_BASE_URL` — overrides `prizm.ollama_url`.
- `PRIZM_API_TOKEN` (or any name set in `api.auth_token_env`) — API bearer token.
- `DISCORD_BOT_TOKEN` — referenced as `${DISCORD_BOT_TOKEN}` in `channels`.
- `PRIZM_BRIDGE_SECRET` — cross-Prizm bridge secret (via `bridge.secret_env`).
- Provider API keys (e.g. `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`) for API-backed
  providers.

## Secrets Handling

> Do not commit `.env`, tokens, API keys, broker credentials, Discord tokens,
> OpenAI keys, Anthropic keys, or local run artifacts.

Prefer environment variables (e.g. `${DISCORD_BOT_TOKEN}`, `auth_token_env`,
`secret_env`) over inlining secrets in YAML.

## Configuration Loading Order

`prizm.yaml` is loaded from the path given by `--config` (defaulting to
`prizm.yaml` in the working directory). Environment variables such as
`OLLAMA_BASE_URL` and `api.auth_token_env` take precedence over the matching
YAML values. Beyond these documented overrides, prefer passing explicit config
paths through CLI flags where available.

## Example Config Sets

- **Local demo** — no config; run the echo workflow.
- **Serve mode** — `cp prizm.yaml.example prizm.yaml`, keep loopback defaults.
- **With memory** — enable `remembrance` and run the Remembrance service.

## Common Config Mistakes

- Binding `bind_host: "0.0.0.0"` without setting `api.auth_token` /
  `api.auth_token_env` — startup is rejected.
- Committing secrets directly in `prizm.yaml` instead of using env vars.
- Pointing `workflow_config` at a path that does not exist.
- Expecting external NATS when `nats_url` is empty (empty = embedded).
