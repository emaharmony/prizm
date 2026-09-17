# Prizm - Event-Native AI Agent Platform

[![Go 1.26+](https://img.shields.io/badge/go-1.26%2B-blue)](https://go.dev/)
[![CI](https://github.com/emaharmony/prizm/actions/workflows/ci.yml/badge.svg)](https://github.com/emaharmony/prizm/actions/workflows/ci.yml)
[![Packages: 85](https://img.shields.io/badge/packages-85-green)]()
[![License: All Rights Reserved](https://img.shields.io/badge/license-all%20rights%20reserved-red)](./LICENSE)

> **Public preview, source-available.** Prizm is local-first and **not
> production-ready**. Use, modification, distribution, or incorporation
> requires written permission — see [LICENSE](./LICENSE). Full status:
> [Stability Matrix](./docs/reference/stability-matrix.md) ·
> [QUALITY.md](./QUALITY.md) ·
> [Public Preview Checklist](./docs/operations/PUBLIC_PREVIEW_CHECKLIST.md).

Prizm is a Go event-native AI agent runtime: a persistent service where
agents communicate over a NATS event bus, tool use is policy-gated and
approval-gated, and every mutation is validated and recorded before it
counts as done.

## The Thesis

> The runtime controls lifecycle, policy, approvals, tools, budgets,
> context, validation, and persistence. Models generate outputs **inside**
> those boundaries — they do not decide their own permissions.

Most agent frameworks start with a model and bolt on control afterward.
Prizm starts with the runtime: a deterministic policy engine decides what a
tool call is allowed to do before any model output can act on the world,
and every mutation passes through the same approval → execution →
validation → persistence pipeline regardless of which model proposed it.

**Dashboard:** `docs/assets/dashboard.png` (screenshot pending — see the
[Public Preview Checklist](./docs/operations/PUBLIC_PREVIEW_CHECKLIST.md));
run `prizm serve` and open `http://localhost:8322/` to see it live in the
meantime.

## Why Prizm Exists

Interactive coding assistants are optimized for one human driving one
session. Prizm is built for the case beyond that: agents that keep running
when you're not watching — scheduled work, multi-agent delegation,
autonomous loops against a real repository — where "trust the model" is not
an acceptable safety story. Prizm's answer is to keep the framework, not
the model, in charge of what is allowed to happen, and to make every
mutation auditable after the fact.

## Execution Lifecycle

```text
Task
  → Route        (agent registry + capability-aware routing)
  → Policy       (deterministic allow / deny / requires-approval — no LLM vote)
  → Approval     (human or configured reviewer gate on mutations)
  → Execute      (tool runs under the resolved policy decision)
  → Validate     (allowlisted validation profiles, e.g. go_test_all)
  → Persist      (canonical events + run artifacts: events.jsonl, summary.json)
  → Observe      (dashboard, SSE stream, `prizm watch`/`prizm trace`/`prizm cost`)
```

Supporting systems: embedded NATS JetStream (event bus), SQLite (sessions,
tasks, approvals, run state), the tool registry + policy/guard checks,
Remembrance (external memory service, opt-in), and the dashboard/API that
reads the same event and artifact stream operators see. Approval and
validation are not optional steps a diagram can skip — a mutation that
fails either one does not reach "persisted."

One explicit, opt-in exception exists to the approval step: **Free Mode**
auto-approves mutations for a single configured Discord owner. See
[Safety Model](./docs/concepts/SAFETY.md#free-mode-owner-authorized-mutation-mode)
before enabling it.

## Five-Minute, Model-Free Demo

The fastest way to understand Prizm is to run a workflow and inspect the
generated events and artifacts. The built-in echo workflow needs no model,
no API key, and no external services — it runs fully locally:

```bash
go test ./...                                   # verify the build
go run ./cmd/prizm-cli workflow list            # see available workflows
go run ./cmd/prizm-cli workflow show demo.echo_tool
go run ./cmd/prizm-cli workflow run demo.echo_tool
go run ./cmd/prizm-cli workflow status <run_id>
```

What to expect:

- workflow and tool events are emitted for the run
- artifacts are written under `runs/<run_id>/`
- `events.jsonl` shows the full event trail
- `summary.json` shows the run result

A sanitized sample run is checked in at
[`examples/runs/sample-run/`](./examples/runs/sample-run). For a step-by-step
walkthrough see [Getting Started](./docs/getting-started/GETTING_STARTED.md) and
[Examples](./docs/getting-started/EXAMPLES.md).

## Key Capabilities

- **Policy-gated tool execution** — read/write/git/shell tools resolved
  through a deterministic policy engine; mutations default to
  approval-gated.
- **Verified gated dev loop** — `PROBE → RESEARCH → PLAN → FEEDBACK_PRE →
  EXECUTION → FEEDBACK_POST → REPORT`, with objective build/test
  verification before a phase can complete.
- **Multi-agent delegation and sub-agents** — capability-based routing,
  bounded tool loops, worktree-per-subagent isolation.
- **Local-first persistence** — embedded NATS JetStream + SQLite, no
  external broker or database required.
- **Dashboard and API** — same-origin dashboard served by `prizm serve`,
  REST/SSE API, run browser, cost/usage tracking.
- **Optional integrations** — Remembrance memory, MCP tool servers,
  Discord, cross-Prizm bridge — all opt-in and disabled by default.

## Safety Model

- The policy engine decides tool permission deterministically; the model
  never grants itself permission.
- File mutations require an approval gate by default — resolved by a human
  or an explicitly configured reviewer, never by the acting model.
- **Free Mode** is the one documented exception: a single, config-selected
  Discord owner can bypass the approval gate for that owner's own messages.
  It is off by default, cannot be triggered remotely or by any other user,
  and some — but not all — safety boundaries still apply while it's active.
  Read [Safety Model → Free Mode](./docs/concepts/SAFETY.md#free-mode-owner-authorized-mutation-mode)
  in full before enabling it.
- Full model: [docs/concepts/SAFETY.md](./docs/concepts/SAFETY.md). Security
  reporting: [SECURITY.md](./SECURITY.md).

## Stability and Quality Evidence

Prizm is in **public-preview** development. It is source-available,
local-first, and **not production-ready**.

- [Stability Matrix](./docs/reference/stability-matrix.md) — per-feature
  stable / preview / experimental classification and production
  recommendations.
- [QUALITY.md](./QUALITY.md) — verified commit, test/coverage counts, CI
  scope, and known limitations as of the last verification pass.
- [Capability Status](./docs/reference/CAPABILITY_STATUS.md) — safe
  defaults and what to confirm before relying on a given capability.

## Architecture

```text
Prizm Runtime
  - Config + orchestrator
  - Embedded or external NATS JetStream
  - SQLite-backed sessions, tasks, approvals, events, and run artifacts
  - Agent router, tool executor, policy/guard checks, plan/state managers
  - Sub-agent worker: bounded tool-loop delegation with worktree isolation (V58)
  - Remembrance HTTP client and cache
  - HTTP API, SSE event stream, dashboard, visual workflow editor
  - Optional cross-Prizm bridge and Roblox Factory handoff
  - Idle guard + schedule optimization (zero tokens when nothing to do)

Ingress/Egress
  - Discord bot (interactive approval buttons, channel roles)
  - CLI run/chat commands
  - REST API clients
  - NATS subjects and scheduled wake events
```

### Core Packages

| Package | Purpose |
|---------|---------|
| `bus/`, `event/`, `run/`, `runtrack/` | Embedded NATS, canonical events, run lifecycle, WAL-style artifacts, run tracking |
| `provider/` | Mock, Ollama, OpenAI chat completions, OpenAI Responses, Anthropic, Gemini, Claude Code CLI, Codex CLI |
| `agent/`, `agentns/`, `router/`, `orchestrator/` | Agent registry, agent namespaces, routing, config loading, live service lifecycle |
| `session/`, `task/`, `approval/`, `delegation/`, `subagent/` | Conversation state, task tracking, approvals, multi-agent delegation, autonomous sub-agent worker |
| `tool/`, `policy/`, `safety/`, `mutation/`, `guard/` | Tool registry, policy gates, path safety, mutations, plan-aware guard checks |
| `state/`, `plan/`, `prompt/`, `context/` | Working state, task plans, prompt layering, workspace context injection |
| `stage/`, `workstart/` | Gated-loop phase stages, work-start lifecycle |
| `skill/` | SKILL.md skill-use capabilities (Claude Code / OpenClaw) |
| `remembrance/` | Go HTTP client and cache for the separate Remembrance memory service |
| `api/`, `dashboard/`, `editor/`, `workflow/` | REST/SSE API, dashboard, visual editor, SVG workflow diagrams |
| `bridge/`, `crossprizm/`, `factory/`, `factorymonitor/` | Cross-Prizm protocol, Factory handoff, Factory queue monitoring |
| `autopatch/`, `improve/` | Self-patching PR mode, issue scanner, improvement proposals |
| `claudecli/`, `claudeworker/`, `codexworker/` | Claude Code CLI resolution, Claude reviewer worker, Codex subscription worker |
| `gitx/` | Shared git worktree helpers (used by sub-agents, autopatch, gated loop) |
| `codesummary/` | Codebase summary generation |
| `vector/`, `sse/`, `cost/`, `projection/`, `validation/`, `review/` | Search, streaming helpers, cost tracking, CQRS projections, checks, review artifacts |
| `action/`, `debounce/`, `retry/`, `checksum/`, `scheduler/` | Event actions, debounce, retry logic, checksums, cron scheduler |
| `config/`, `sqlite/`, `integration/` | Configuration loading, SQLite persistence, integration tests |

---

## Documentation

New to Prizm? Start here:

1. [Getting Started](docs/getting-started/GETTING_STARTED.md) — install, build, test, and run your first workflow.
2. [Configuration Guide](docs/operations/CONFIGURATION.md) — where config files live and how Prizm loads them.
3. [YAML Reference](docs/reference/YAML_REFERENCE.md) — workflow, policy, adapter, provider, and agent YAML.
4. [Command Reference](docs/reference/COMMANDS.md) — all major CLI commands and what they do.
5. [Examples](docs/getting-started/EXAMPLES.md) — guided demo flows.
6. [Troubleshooting](docs/operations/TROUBLESHOOTING.md) — common setup and runtime issues.
7. [Architecture](docs/architecture/ARCHITECTURE.md) — how Prizm works internally.
8. [Capability Status](docs/reference/CAPABILITY_STATUS.md) — stable vs experimental features.
9. [Safety Model](docs/concepts/SAFETY.md) — human-in-the-loop, policy vs validators, autopatch risks.
10. [Roadmap](docs/history/ROADMAP.md) — project direction.
11. [Version History](docs/history/VERSION_HISTORY.md) — the full V1–V58+ development story.
12. [Public Preview Checklist](docs/operations/PUBLIC_PREVIEW_CHECKLIST.md) — release-prep status.
13. [Dashboard Guide](docs/dashboard/README.md) — dashboard architecture, navigation, and UI development.
14. [Multi-Agent Workflow](docs/MULTI_AGENT_WORKFLOW.md) — operate the supported bounded software-task flow.

---

## Requirements

- **Go 1.26+** (module requires 1.26.2)
- **Git** (for project tools, worktree isolation, autopatch)
- **Ollama** — if using local Ollama models
- **Python 3.11+** — only if running the Remembrance memory service
- **Claude CLI** — only if using the `claude_code` provider or Claude reviewer (subscription-based, no API key)
- **Codex CLI** — only if using the Codex worker (`codex login` required)
- **`gh` CLI** — only if using autopatch in `"pr"` mode (GitHub pull requests)

No external database or message broker is required for basic operation — Prizm embeds NATS JetStream and uses SQLite.

---

## Quick Start

### Build

```bash
git clone https://github.com/emaharmony/prizm.git
cd prizm
go build -o prizm ./cmd/prizm-cli
```

On Windows PowerShell:

```powershell
$env:GOTELEMETRY = "off"
go build -o .\prizm-current.exe .\cmd\prizm-cli
go build -o .\prizm-bus-current.exe .\cmd\prizm-bus
```

For a full Windows walkthrough, see [docs/WINDOWS_SETUP.md](./docs/getting-started/WINDOWS_SETUP.md).

### Test

```bash
go test ./... -count=1 -race
```

### Start Serve Mode

```bash
cp prizm.yaml.example prizm.yaml
./prizm serve --config prizm.yaml
```

Default serve-mode URLs:

- Health: `http://localhost:8321/health`
- API status: `http://localhost:8322/api/v1/status`
- SSE events: `http://localhost:8322/api/v1/events/stream`
- **Dashboard: `http://localhost:8322/`** — served by `prizm serve` itself (no separate process, no CORS setup)

The dashboard pages (all same-origin on the API port):

| Page | Purpose |
|---|---|
| `/config.html` | **Settings** editor — instance/paths, feature toggles, autopatch, remembrance, workflow run-behavior |
| `/scheduler.html` | **Cron Jobs** editor — add/edit jobs with live cron validation + action presets |
| `/index.html` | Runs browser · `/v2.html` Status · `/editor.html` Agent graph · `/workflow-editor.html` Workflow |

Settings and cron edits are written back to `prizm.yaml` surgically (comments and untouched sections preserved) and **apply on the next `prizm serve` restart**. `prizm serve` starts an embedded NATS server when `prizm.nats_url` is empty.

### Start Remembrance

Remembrance is a separate Python service. From the repo's `remembrance` directory:

```bash
python -m venv .venv
source .venv/bin/activate
python -m pip install --upgrade pip
pip install -e .
uvicorn remembrance.app:app --host 127.0.0.1 --port 18790
```

Enable it in `prizm.yaml`:

```yaml
remembrance:
  enabled: true
  url: "http://localhost:18790"
  timeout_seconds: 60
```

### One-Shot CLI Mode

`prizm run` is a one-shot lifecycle command. It expects a NATS bus at `nats://localhost:4222`; use `prizm serve` for embedded NATS or start `cmd/prizm-bus` separately.

```bash
./prizm run --task "Explain event-driven architecture" --provider ollama --model llama3.2
./prizm run --task "Build prompt and artifacts only" --dry-run-prompt
./prizm run --task "Use Remembrance context" --memory-enabled --memory-url http://localhost:18790
```

### Interactive Chat

```bash
./prizm chat --config prizm.yaml
./prizm chat --config prizm.yaml --agent astraea
```

`chat` uses the same config, workspace context, tool registry, state tools, plan tools, and provider setup as serve mode, but runs in the terminal.

---

## Use Cases

### Persistent AI Assistant with Discord

Run Prizm as a daemon connected to Discord. Agents maintain sessions, use channel-aware context, call tools through policy, and capture memory through Remembrance.

```yaml
agents:
  - id: astraea
    role: orchestrator
    provider: ollama
    model: "llama3.2"
    primary: true
    context: [soul, agents, user]
    capabilities: [plan, route, review, validate, report]

channels:
  - type: discord
    token: "<bot-token>"
    channels: ["general", "dev"]
```

### Multi-Agent Delegation

Agents can delegate work through task events. Capabilities decide which agents can receive which task types, and approvals can gate risky operations.

```yaml
agents:
  - id: lumi
    role: lead
    capabilities: [plan, delegate, review, approve]
  - id: forge
    role: coder
    capabilities: [code, test, report]
```

### Autonomous Sub-Agent Worker (V58)

Sub-agents run independently with bounded tool loops, per-agent tool scoping (capability-based gating), worktree-per-subagent isolation, and deadline enforcement. Enable with `PRIZM_SUBAGENT_WORKER=1`. Code-capable agents get isolated git worktrees; read-only agents skip isolation. Failed or timed-out sub-agents always emit a `status:"failed"` completion — they never silently hold a gate.

### Plan-Aware Tool Execution

Serve and chat modes register read, project, git, state, and plan tools. Read-only tools can run directly inside allowed paths; mutation tools such as `git_add`, `git_commit`, and `git_push` are policy-gated.

### Verified Gated Loop

The gated dev loop (`PROBE → RESEARCH → PLAN → FEEDBACK_PRE → EXECUTION → FEEDBACK_POST → REPORT`) enforces objective build/test verification in EXECUTION: after the model commits, Prizm runs an allowlisted V5 validation profile (e.g. `go_test_all` → `go test ./...`). With `blocking: true` the phase cannot complete until it passes — the failing output is fed back so the model fixes the real problem and re-commits. The model can also call the `run_validation` tool to self-check before committing. The loop is bounded by run budgets (`max_total_time`, `max_total_tokens`; `-1` explicitly means unlimited, `0` uses the default token ceiling) and stuck-loop detection (`max_repeated_tool_calls`), each emitting an event and stopping gracefully. Configure it per phase under `verification` (see [docs/history/milestones/V35-VERIFICATION-GATE-DESIGN.md](./docs/history/milestones/V35-VERIFICATION-GATE-DESIGN.md)).

### Scout Sub-Agent

A lightweight local model (e.g. qwen3:8b, gemma3:4b) gathers codebase context before the cloud model runs, reducing token cost. The scout can also collect reference images via the `collect_reference_images` tool (with Firecrawl support for real image downloads).

### Cross-Prizm and Factory Handoff

The bridge verifies signed cross-Prizm messages over shared NATS, stores generic delegated tasks, and can route selected target profiles into adapters such as Roblox Factory. Discord can issue `/prizm delegate`, `/prizm status`, and `/prizm stop` commands, but autonomous Prizm-to-Prizm communication stays on NATS so Discord bot greeting loops are avoided. See [docs/history/milestones/CROSS-PRIZM-FACTORY-SETUP.md](./docs/history/milestones/CROSS-PRIZM-FACTORY-SETUP.md).

### Roblox Game-Dev Team

A multi-agent studio (orchestrator, researcher, game planner, Factory master, asset maker) that designs and builds Roblox games — with native reference-image tools, a Blender-MCP asset pipeline, and a cross-Prizm rubric handshake. See [docs/history/milestones/ROBLOX-TEAM.md](./docs/history/milestones/ROBLOX-TEAM.md).

### Codex Subscription Worker

Prizm can delegate selected tasks to the local OpenAI Codex CLI through a `codex` worker. This uses the user's existing `codex login` session, so Prizm does not handle ChatGPT OAuth tokens and does not route this path through `OPENAI_API_KEY`.

```yaml
codex:
  enabled: true
  sandbox: "workspace-write"
  approval_policy: "on-request"
  timeout_minutes: 30
```

After enabling it, local agents can emit `[DELEGATE: codex | code] ...`, and cross-Prizm commands can target `/prizm delegate target:codex task:...`.

### Claude Code Provider

Use the Claude CLI as an orchestrator brain for the gated loop (no API key — uses your subscription). Define an agent with `provider: claude_code` and point a project at it with `project.orchestrator`. The provider shells out to `claude -p` with tools disabled, so Prizm still owns tool execution, gates, and policy. Claude Code can also serve as a reviewer at feedback gates.

```yaml
claude_code:
  enabled: true
  reviewer_name: "claude"
  timeout_minutes: 10
```

### Auto-Patching

Autopatch turns explicit bug reports or validation failures into reviewable patch proposals. It creates an isolated git worktree, tries configured patch workers in order, runs allowlisted validation profiles, and stores artifacts under `.prizm/data/autopatch/<task-id>/`. Two modes: `"propose"` (default — patch artifact only, never touches the main worktree) and `"pr"` (V50 — pushes a branch and opens a GitHub pull request via the `gh` CLI; `prizm doctor` preflights `gh` auth).

```yaml
autopatch:
  enabled: true
  mode: "propose"            # or "pr" to open pull requests
  require_clean_worktree: true
  max_attempts: 2
  validation_profiles: ["go_test_all"]
  worker_order: ["codex", "local_agent"]
  local_agent: "forge"
  worktree_root: ".prizm/worktrees"
  base_branch: ""            # PR base in "pr" mode; empty = repo default
```

Start a task with `POST /api/v1/autopatch` using `{"description":"tests are failing, fix this bug"}`. In Discord, explicit requests such as "auto patch this bug" or "tests are failing, fix this bug" start the same tracked `auto_patch` task.

### Skill-Use Capabilities (V54)

Agents can consume SKILL.md files (Claude Code / OpenClaw format) via the `use_skill` tool, enabling structured skill injection into prompts.

### MCP Client (V49)

Consume external MCP (Model Context Protocol) tool servers. Tools register as `mcp_<name>_<tool>` and run through the same policy engine as built-in tools. Probe a server before enabling:

```bash
prizm mcp probe --command npx --args "-y,@modelcontextprotocol/server-filesystem,D:/_projects_"
```

```yaml
mcp_servers:
  - name: "filesystem"
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-filesystem", "D:/_projects_"]
    enabled: true
mcp_auto_approve: false
```

---

## CLI Reference

### Daemon and Chat

```bash
prizm serve [--config prizm.yaml] [--port 8321]
prizm chat [--config prizm.yaml] [--agent <id>]
prizm status [--config prizm.yaml]
prizm dashboard [--port 8080] [--run-dir ./runs] [--policy-dir policies]  # optional; serve already hosts the UI on the API port
```

### One-Shot Runs

```bash
prizm run --task "..." [--provider mock|ollama|openai|anthropic|gemini]
prizm run --task "..." --model llama3.2 --ollama-url http://localhost:11434
prizm run --task "..." --memory-enabled --memory-url http://localhost:18790
prizm run --task "..." --dry-run-prompt
```

### Workflows and Projects

```bash
prizm workflow list
prizm workflow show <name>
prizm workflow run <name> --input input.json
prizm workflow start --project <id> --prompt "..."
prizm workflow status <run_id>
prizm workflow run multi-agent-software-task --input task.json --config prizm.yaml
prizm workflow cancel <run_id>
prizm workflow resume <run_id> --config prizm.yaml
prizm workflow report <run_id> [--json]
prizm preview [--config prizm.yaml]          # static gated-loop preview
prizm watch [--config prizm.yaml]            # live SSE run visibility
prizm runs [--json]                          # browse past runs & reports
prizm runs latest                            # latest run shortcut
```

### Management

```bash
prizm health [--bus-url nats://localhost:4222]
prizm doctor [--json]                        # preflight health check
prizm config validate                        # validate prizm.yaml
prizm config summarize                       # summarize config
prizm config wizard                          # interactive config setup
prizm config import                          # OpenClaw→prizm.yaml import
prizm agent list
prizm agent show <id>
prizm approval list [--run <run_id>]
prizm approval approve <id> --by <name> --run <run_id> [--validate]
prizm approval deny <id> --by <name> --run <run_id>
prizm tool list
prizm tool run <name> --input '{"key":"value"}' --workspace .
prizm skills list                            # list available SKILL.md files
prizm validation list
prizm validation run <profile>
prizm policy list
prizm policy evaluate --input request.json
prizm adapter list
prizm adapter show <name>
prizm adapter health <name>
prizm projection list
prizm projection rebuild --run <id>
prizm projection query <name> --run <id>
prizm context show --context soul,agents --workspace-root .
prizm cost <run_id>
prizm trace <run_id>
prizm search --query "text" [--top-k 10] [--provider mock|openai|ollama]
prizm scan [--start]                         # issue-discovery scanner
prizm mcp probe --command <cmd> --args <args> # probe MCP server
prizm remembrance status                     # check Remembrance connection
```

---

## Configuration

`prizm.yaml.example` is the current reference. Important fields:

```yaml
prizm:
  instance_id: "prizm"
  nats_url: ""                       # empty = embedded NATS in serve mode
  data_dir: ".prizm/data"
  workspace: "D:/_projects_/prizm"
  ollama_url: "http://localhost:11434"
  context_token_budget: 4000
  llm_timeout_seconds: 1200
  port: 8321
  log_level: "info"
  bind_host: "127.0.0.1"            # "0.0.0.0" to expose on network (requires api.auth_token)
  read_roots:
    - "D:/"
    - "C:/Users/emaha"
  write_roots:
    - "D:/_projects_"
    - "D:/Projects"
  allowed_paths: []                  # legacy alias if split roots are omitted
  scheduler:                         # built-in cron; see docs/operations/SCHEDULER.md
    enabled: false
    jobs: []

api:
  auth_token: ""                     # bearer token for state-changing endpoints
  auth_token_env: ""                 # e.g. "PRIZM_API_TOKEN" (takes priority)
  allowed_origins: []                # CORS origin allowlist

bridge:
  enabled: false
  mode: "shared_nats"
  secret_env: "PRIZM_BRIDGE_SECRET"
  allowed_subjects:
    - "prizm.cross.context_sync"
    - "prizm.cross.task_request"
    - "prizm.cross.status_request"
    - "prizm.cross.validation_request"
    - "prizm.cross.task_response"
  factory:
    enabled: false
    root: "D:/_projects_/roblox-factory"
    project: "eggventura"
    project_path: "D:/Projects/Roblox/eggventura"
    approval_mode: "report_only"
    run_codex: false
    vision_review: "none"
    playtest_mode: "none"
    enable_ui_generation: false
    ui_generation_dry_run: true

projects:
  - id: my-project
    repo_path: "/path/to/your/repo"
    state_file: "PROJECT_STATE.md"
    default_branch: "main"
    channel: ""
    workflow_config: ""
    orchestrator: ""                 # agent ID (e.g. "claude_code"); empty = primary agent
    worktree_isolation: false        # V56: per-run git worktree
    default: true

agents:
  - id: astraea
    role: orchestrator
    provider: ollama                  # ollama, openai, openai_responses, anthropic, gemini, claude_code
    model: "llama3.2"
    primary: true
    context: [soul, agents, user]
    conversation_postfix: ""
    capabilities: [plan, route, review, validate, report]
    listen_to_agents: []
    subscriptions: []
    state_actions:
      manager-room:
        inject: "Prefer explicit status and concrete decisions."

channels:
  - type: discord
    token: "<bot-token>"
    channels: []

channel_roles:
  - id: "general"
    role: "manager-room"
    tools: "read-only"                # all, read-only, none
    personality: "direct"             # direct, terse, bubbly, social — shapes "How You Respond";
                                       # an agent's own conversation_postfix always takes priority
    context: "Coordination room for status and task triage."

actions:
  - trigger: "*.agent.output"
    action: "remembrance.gate.extract"
    enabled: true

sessions:
  max_context_messages: 100
  idle_timeout_minutes: 30
  persistence: true
  resume_after_idle: true
  compaction_strategy: "summarize"
  keep_archived_messages: true
  daily_reset_hour: 4
  continuity_scope: "owner_agent"    # or "channel_user" for per-channel
  recall_window_mode: "calendar_week"
  recall_timezone: "Local"
  short_term_window_days: 7
  verbatim_recent_messages: 40

remembrance:
  enabled: false
  url: "http://localhost:18790"
  timeout_seconds: 60

autopatch:
  enabled: false
  mode: "propose"
  require_clean_worktree: true
  max_attempts: 2
  validation_profiles: ["go_test_all"]
  worker_order: ["codex", "local_agent"]
  local_agent: "forge"

codex:
  enabled: false
  sandbox: "workspace-write"
  approval_policy: "on-request"
  timeout_minutes: 30

claude_code:
  enabled: false
  reviewer_name: "claude"
  timeout_minutes: 10
  allowed_tools: ""                  # empty = read-only default

mcp_servers: []
mcp_auto_approve: false

factory_monitor:
  enabled: false
  poll_seconds: 30
  stuck_after_minutes: 30
```

Windows notes:

- The current config loader does not expand `~` in YAML paths; use repo-relative or absolute paths.
- The current config loader does not expand `${ENV_VAR}` inside YAML. Put real local values in `prizm.yaml` or remove optional channels while testing.

### Environment Variables

| Variable | Description |
|----------|-------------|
| `OPENAI_API_KEY` | Required by `openai` and `openai_responses` providers |
| `ANTHROPIC_API_KEY` | Required by the Anthropic provider |
| `GEMINI_API_KEY` | Required by the Gemini provider |
| `OPENAI_EMBEDDING_MODEL` | Optional model override for `prizm search --provider openai` |
| `OLLAMA_BASE_URL` | Optional embedding base URL for `prizm search --provider ollama` |
| `OLLAMA_EMBEDDING_MODEL` | Optional Ollama embedding model override |
| `PRIZM_BRIDGE_SECRET` | Shared HMAC secret for cross-Prizm bridge when enabled |
| `PRIZM_SUBAGENT_WORKER` | Set to `1` to enable the V58 autonomous sub-agent worker in serve mode |
| `PRIZM_API_TOKEN` | Bearer token for API authentication (via `api.auth_token_env`) |
| `DISCORD_BOT_TOKEN` | Discord bot token (referenced in channel config) |

---

## Setting Up a Project Loop

Prizm can autonomously work on a project in a scheduled loop — reading a state file for tasks, implementing changes, self-reviewing, and pushing branches. Here's how to set it up:

### 1. Add a project to `prizm.yaml`

```yaml
projects:
  - id: my-project
    repo_path: "/path/to/your/repo"
    state_file: "PROJECT_STATE.md"        # task assignment file (relative to repo_path)
    default_branch: "main"               # protected: git_commit/git_push refuse direct writes here (see docs/concepts/SAFETY.md)
    channel: "1234567890123456789"       # Discord channel ID for feedback/reports
    workflow_config: "examples/workflows/fast-loop.yaml"  # optional per-project workflow
    worktree_isolation: false             # V56: per-run git worktree (parallel runs)
    default: true                         # used when no --project is specified
```

### 2. Create a `PROJECT_STATE.md` in the repo

```markdown
# Project State

## FEATURE PRIORITY — DO THESE IN ORDER

- [ ] Task 1: Brief description (risk: low)
- [ ] Task 2: Brief description (risk: medium)
- [x] Task 3: Completed task (strikethrough for done items)
```

Prizm reads this file at the start of each cycle, picks the topmost unchecked task, and works on it. When done, she marks it `[x]`.

### 3. Configure the scheduler job

```yaml
scheduler:
  enabled: true
  jobs:
    - name: "project-work"
      schedule: "*/10 * * * *"          # every 10 minutes (cron expression)
      event: "prizm.task.scheduled"
      payload:
        action: "project_work"
      enabled: true

    - name: "status-report"
      schedule: "0 */2 * * *"           # every 2 hours
      event: "prizm.task.scheduled"
      payload:
        action: "status_report"
      enabled: true
```

- **project-work**: Runs the gated loop — discovers tasks, implements, reviews, pushes
- **status-report**: Reads recent run summaries + PROJECT_STATE.md, posts a report to Discord

Full cron reference (all fields, actions, examples): [docs/SCHEDULER.md](./docs/operations/SCHEDULER.md).

### 4. Choose a workflow config

| Config | Best for | Key settings |
|---|---|---|
| `gated-loop.yaml` (default) | Human-in-the-loop | 60m budget, approval gates, full iteration caps |
| `fast-loop.yaml` | Autonomous loops | 15m budget, `auto_approve: true`, reduced iteration caps |

Point your project at either one via `workflow_config`. Or omit it to use the built-in default.

### 5. Workflow config options

```yaml
global:
  auto_approve: true          # skip FEEDBACK_PRE/FEEDBACK_POST gates (no human needed)
  auto_rollback: false         # V57: auto git revert on failed verification
  max_total_time: "15m"       # hard time budget per run
  max_total_tokens: 500000    # token ceiling (-1 = unlimited, 0 = default)
  max_repeated_tool_calls: 3  # stuck-loop detection

phases:
  - name: PROBE
    max_iterations: 8         # how many LLM calls before forcing phase advance
    gate:
      type: assumption_threshold
      threshold: 1.0
    fallback:
      on_max_iterations: proceed_with_open_assumptions  # don't block
      blocks: false
```

### 6. Start immediately (optional)

Don't want to wait for the next cron tick?

```bash
./prizm workflow start --project my-project --prompt "Work on the next task in PROJECT_STATE.md"
```

Or via the API:

```bash
curl -X POST http://localhost:8322/api/v1/workflows/start \
  -H "Content-Type: application/json" \
  -d '{"project":"my-project","prompt":"Work on the next task in PROJECT_STATE.md"}'
```

### 7. Discord approval buttons

When a workflow pauses at a feedback gate, Prizm posts a message with interactive buttons to the configured channel:

- **Approve** — resume the workflow
- **Request changes** — loop back to EXECUTION
- **Reject** — end the run (FEEDBACK_PRE only)

Buttons are automatically disabled after one is clicked. With `auto_approve: true`, feedback gates are skipped entirely and no buttons are sent.

## API Surface

Serve mode exposes the live API on `port + 1`, so the default is `8322`.

Current routes include:

- `GET /api/v1/status`
- `GET /api/v1/agents`, `GET /api/v1/agents/{id}`
- `GET /api/v1/sessions`, `GET /api/v1/sessions/{id}`
- `GET /api/v1/tasks`, `GET /api/v1/tasks/{id}`
- `GET /api/v1/approvals`
- `POST /api/v1/approvals/{id}/grant`, `POST /api/v1/approvals/{id}/deny`
- `GET /api/v1/events/stream`
- `GET /api/v1/workflows`, `GET /api/v1/workflows/{type}`
- `POST /api/v1/workflows/start`
- `GET/PUT /api/v1/editor`
- `GET/POST /api/v1/editor/nodes`, `PUT/DELETE /api/v1/editor/nodes/{id}`
- `GET/POST /api/v1/editor/edges`, `DELETE /api/v1/editor/edges/{id}`
- `POST /api/v1/editor/save`
- `GET /api/v1/costs`
- `POST /api/v1/autopatch`

---

## Makefile Targets

| Target | Command | Description |
|--------|---------|-------------|
| `make dev` | `go run ./cmd/prizm-cli run ...` | Run in development mode |
| `make test` | `go test ./... -count=1 -race` | Run all tests with race detector |
| `make test-short` | `go test ./... -count=1 -short` | Run tests without race detector (faster) |
| `make test-coverage` | Generates `coverage.html` | Tests with coverage report |
| `make lint` | `go vet ./...` | Run linters |
| `make ci` | vet → build → test | Full CI gate |
| `make build` | `CGO_ENABLED=0 go build ...` | Build binary for current platform |
| `make build-all` | Cross-compile | Linux amd64, Darwin arm64, Windows amd64 |
| `make docker-build` | `docker build -t prizm:latest .` | Build Docker image |
| `make docker-run` | `docker-compose up -d` | Run in Docker |
| `make docker-stop` | `docker-compose down` | Stop Docker containers |
| `make clean` | Remove build artifacts | Clean up binaries and coverage files |

---

## Docker

```bash
docker build -t prizm:latest .
docker-compose up -d
```

The Docker setup exposes port 8080 and mounts `./runs` and `./policies` as volumes.

---

## Key Design Decisions

- Events are the source of truth; meaningful actions become canonical events.
- Agent IDs define namespaces, for example `astraea.agent.output`.
- Policies and guard checks are deterministic; the model does not decide its own write permissions.
- SQLite and embedded NATS keep local development self-contained.
- Remembrance is a separate HTTP service, not embedded.
- ChatProvider-native tool calling is preferred where supported; text-based tool requests remain as fallback.
- Working state and plans are explicit files managed by state/plan tools.
- Cross-Prizm and Factory integrations are optional adapters around the core runtime.
- Sub-agents fail closed: unknown agent, runner error, or timeout always yields a failed completion — never silently holds a gate.
- Idle guard prevents token waste when no work is scheduled.

---

## Project Structure

```text
prizm/
├── cmd/
│   ├── prizm-cli/          # Main CLI binary (serve, run, chat, doctor, config, workflow, etc.)
│   ├── prizm-bus/          # Standalone NATS bus binary
│   └── prizm-agent/        # Standalone agent binary
├── internal/               # All core packages (~57 domains)
│   ├── action/             # Event-triggered actions
│   ├── adapter/            # Adapter contract
│   ├── agent/              # Agent registry and lifecycle
│   ├── agentns/            # Agent namespace helpers
│   ├── api/                # REST/SSE HTTP API
│   ├── approval/           # Approval gates
│   ├── autopatch/          # Self-patching (propose + PR modes)
│   ├── bridge/             # Cross-Prizm bridge protocol
│   ├── bus/                # Embedded NATS JetStream
│   ├── checksum/           # Deterministic checksums
│   ├── claudecli/          # Claude Code CLI executable resolution
│   ├── claudeworker/       # Claude reviewer worker
│   ├── codesummary/        # Codebase summary generation
│   ├── codexworker/        # Codex CLI subscription worker
│   ├── config/             # Configuration loading
│   ├── context/            # Workspace context injection
│   ├── cost/               # Token/cost tracking
│   ├── crossprizm/         # Cross-Prizm messaging
│   ├── dashboard/          # Dashboard UI
│   ├── debounce/           # Debounce helpers
│   ├── delegation/         # Multi-agent delegation
│   ├── editor/             # Visual workflow editor
│   ├── event/              # Canonical event types
│   ├── factory/            # Roblox Factory adapter
│   ├── factorymonitor/     # Factory queue monitoring
│   ├── gitx/               # Shared git worktree helpers
│   ├── guard/              # Plan-aware guard checks
│   ├── improve/            # Improvement proposals
│   ├── integration/        # Integration tests
│   ├── mutation/           # Mutation tracking
│   ├── orchestrator/       # Live orchestrator lifecycle
│   ├── plan/               # Task plan management
│   ├── policy/             # Policy engine
│   ├── projection/         # CQRS state projections
│   ├── prompt/             # Prompt layering
│   ├── provider/           # LLM providers (Mock, Ollama, OpenAI, Anthropic, Gemini, Claude Code, Codex)
│   ├── remembrance/        # Remembrance HTTP client + cache
│   ├── retry/              # Retry logic
│   ├── review/             # Review artifacts
│   ├── router/             # Agent routing
│   ├── run/                # Run lifecycle + WAL artifacts
│   ├── runtrack/           # Run tracking
│   ├── safety/             # Path safety checks
│   ├── scheduler/          # Built-in cron scheduler
│   ├── session/            # Conversation sessions
│   ├── skill/              # SKILL.md parsing and injection
│   ├── sqlite/             # SQLite persistence
│   ├── sse/                # SSE streaming helpers
│   ├── stage/              # Gated-loop phase stages
│   ├── state/              # Working state management
│   ├── subagent/           # Autonomous sub-agent worker (V58)
│   ├── task/               # Task tracking
│   ├── tool/               # Tool registry and execution
│   ├── validation/         # Validation profiles
│   ├── vector/             # Vector search
│   ├── workflow/           # Workflow runtime
│   └── workstart/          # Work-start lifecycle
├── adapters/               # External adapter implementations
│   ├── echo/               # Echo adapter (example)
│   └── remembrance/        # Remembrance adapter
├── sdk/                    # SDK for external consumers
│   ├── examples/
│   └── prizm/
├── remembrance/            # Separate Python memory service
│   ├── configs/
│   ├── src/
│   └── tests/
├── policies/               # Policy definition files
├── examples/               # Example configs and workflows
│   ├── runs/
│   └── workflows/
├── docs/                   # Design documents (V1–V58)
├── scripts/                # Utility scripts
├── Makefile                # Build, test, lint, CI, Docker targets
├── Dockerfile              # Multi-stage Docker build
├── docker-compose.yaml     # Docker Compose setup
├── go.mod / go.sum         # Go module (github.com/emaharmony/prizm)
├── prizm.yaml.example      # Reference configuration
└── LICENSE                 # All rights reserved
```

---

## Testing

```bash
go test ./... -count=1           # all tests
go test ./... -count=1 -race     # all tests with race detector
go test ./internal/stage/... -race
go test ./internal/vector/... -bench=.
go test ./internal/checksum/ -v -count=1  # single package
```

Tests use the external test package pattern (`package foo_test`), stdlib `testing` only (no third-party frameworks), and assertions via `t.Errorf` / `t.Fatalf`.

---

## Dependencies

Direct runtime dependencies (from `go.mod`):

| Dependency | Purpose |
|------------|---------|
| `modernc.org/sqlite` | Pure-Go SQLite (no CGO required) |
| `nats-io/nats-server/v2`, `nats-io/nats.go` | Embedded NATS JetStream server and client |
| `gopkg.in/yaml.v3` | YAML configuration parsing |
| `bwmarrin/discordgo` | Discord bot integration |
| `ajstarks/svgo` | SVG workflow diagram generation |
| `gofrs/flock` | File locking |
| `oklog/ulid/v2`, `rs/xid` | ULID and XID generation |

No external database requirement for local development.

---

## Version History

Prizm grew through many incremental versions (V1–V58+). The full development
story — with links to each design document — lives in
[docs/VERSION_HISTORY.md](./docs/history/VERSION_HISTORY.md).

At a glance:

- **Foundations (V1–V13):** event spine, LLM execution, tool execution,
  approvals, validation, workflow runtime, policy engine, adapters,
  projections, dashboard, multi-agent orchestration.
- **Platform expansion (V14–V34):** vector search, providers, persistent serve
  mode, Discord, sessions, Remembrance, git/project tools.
- **Gated loop & observability (V35–V48):** verification gates, `prizm watch`,
  `prizm doctor`, run artifacts, approval cards, `prizm runs`.
- **Advanced / experimental (V49–V58):** MCP client, self-patching autopatch,
  skills, config wizard, worktree isolation, auto-rollback, sub-agent worker.

---

## Project Status

Prizm is source-available and in **public-preview** development — see the
[Status](#status) section above and the full
[Capability Status](./docs/reference/CAPABILITY_STATUS.md) matrix for what is stable
versus experimental. It is not production-ready.

Current focus is stabilization: keeping runtime behavior, docs, and
configuration aligned; a clear golden-path demo; repo hygiene; and hardening the
experimental sub-agent worker, idle-guard optimization, and cross-Prizm/Factory
handoff.

See [docs/ROADMAP.md](./docs/history/ROADMAP.md) and [docs/design/TASKS.md](./docs/history/milestones/TASKS.md).

---

## License

All rights reserved. Copyright (c) 2025–2026 Emmanuel Vinas. See [LICENSE](./LICENSE) for details.
