# Prizm Developer Onboarding

This guide gets a contributor from clone to a working local Prizm setup.

---

## What Prizm Is

Prizm is a Go event-native AI agent platform. It can run as a persistent service, a one-shot CLI, or an interactive terminal chat. The runtime owns event flow, sessions, tools, approvals, prompt context, memory integration, and API/dashboard access.

Primary entry points:

1. `prizm serve` - persistent daemon with Discord, sessions, embedded NATS, API, and dashboard.
2. `prizm run` - one-shot run lifecycle for a single task.
3. `prizm chat` - interactive terminal chat using configured agents and tools.
4. HTTP API + SSE - local integration surface on serve-mode port `8322` by default.

---

## Quick Start

```bash
git clone https://github.com/emaharmony/prizm.git
cd prizm
go build -o prizm ./cmd/prizm-cli
go test ./... -count=1
cp prizm.yaml.example prizm.yaml
./prizm serve --config prizm.yaml
```

Open:

- Health: `http://localhost:8321/health`
- API status: `http://localhost:8322/api/v1/status`
- Event stream: `http://localhost:8322/api/v1/events/stream`

Windows users should follow [WINDOWS_SETUP.md](./WINDOWS_SETUP.md).

---

## Remembrance

Remembrance is a separate Python service used for memory capture and context building.

```bash
cd remembrance
python -m venv .venv
source .venv/bin/activate
python -m pip install --upgrade pip
pip install -e .
uvicorn remembrance.app:app --host 127.0.0.1 --port 18790
```

Enable it in the repo root `prizm.yaml`:

```yaml
remembrance:
  enabled: true
  url: "http://localhost:18790"
  timeout_seconds: 60
```

---

## CLI Basics

One-shot run mode:

```bash
./prizm run --task "Explain event-driven architecture" --provider ollama --model llama3.2
./prizm run --task "Build prompt and artifacts only" --dry-run-prompt
./prizm run --task "Use memory" --memory-enabled --memory-url http://localhost:18790
```

Interactive chat:

```bash
./prizm chat --config prizm.yaml
./prizm chat --config prizm.yaml --agent astraea
```

Useful management commands:

```bash
./prizm status --config prizm.yaml
./prizm health
./prizm tool list
./prizm context show --context soul,agents --workspace-root .
./prizm search --query "memory pipeline" --provider mock
./prizm trace <run_id>
./prizm cost <run_id>
```

`prizm run` expects a NATS bus at `nats://localhost:4222`. Use `prizm serve` for embedded NATS or run `cmd/prizm-bus` in another terminal.

---

## Key Concepts

### Events

Everything meaningful becomes an event: agent output, tool calls, approvals, session changes, scheduled wake events, cross-Prizm messages, and memory captures. Agent IDs become namespaces such as `astraea.agent.output`.

### Sessions

Sessions are SQLite-backed conversations with idle reset, daily reset configuration, and max-message compaction.

### Agents

Agents are configured in `prizm.yaml`:

- `id` - event namespace and lookup key.
- `role` - lead, coder, reviewer, orchestrator, guard, etc.
- `provider` and `model` - `ollama`, `openai`, `openai_responses`, `anthropic`, or `gemini`.
- `context` - named workspace files to inject.
- `capabilities` - routing/delegation/tool policy metadata.
- `state_actions` and `channel_roles` - channel-aware behavior.

### Tools and Plans

The runtime exposes file, project, git, state, and plan tools. Read-only tools are policy-approved inside allowed roots. Mutation tools require approval or plan/guard checks depending on context.

Plan tools track task plans in workspace state. Guard checks block mutation tools when the required plan state is missing or not approved.

### Remembrance

Remembrance captures agent output, extracts useful facts, and builds context for later prompts. Serve mode can use it both asynchronously after agent output and synchronously before the next LLM call.

### Cross-Prizm and Factory

The bridge handles signed cross-Prizm subjects. The optional Factory handoff writes validated task requests into a Roblox Factory queue. See [CROSS-PRIZM-FACTORY-SETUP.md](../history/milestones/CROSS-PRIZM-FACTORY-SETUP.md).

---

## Architecture Tour

```text
prizm serve
  - load prizm.yaml
  - start or connect to NATS
  - initialize SQLite stores
  - register agents, tools, state, plans, guard, scheduler
  - connect Discord channels
  - start health server on 8321
  - start API/SSE/dashboard server on 8322

Discord message path
  debounce/rate limit
  response gate
  route to agent
  load session
  build layered prompt and workspace/memory context
  choose ChatProvider native tools or text-tool fallback
  execute tools through policy
  persist session and events
  capture memory when enabled

run path
  create run context
  call LLM stage
  handle delegation/persistence/events/remembrance
  write run artifacts under ./runs
```

---

## API Surface

Serve mode exposes these route groups:

- Status: `/api/v1/status`
- Agents: `/api/v1/agents`, `/api/v1/agents/{id}`
- Sessions: `/api/v1/sessions`, `/api/v1/sessions/{id}`
- Tasks: `/api/v1/tasks`, `/api/v1/tasks/{id}`
- Approvals: `/api/v1/approvals`, `/api/v1/approvals/{id}/grant`, `/api/v1/approvals/{id}/deny`
- Events: `/api/v1/events/stream`
- Workflows: `/api/v1/workflows`, `/api/v1/workflows/{type}`
- Editor: `/api/v1/editor`, `/api/v1/editor/nodes`, `/api/v1/editor/edges`, `/api/v1/editor/save`
- Costs: `/api/v1/costs`

---

## Development Patterns

- Keep core behavior deterministic; use models for generation, not authorization.
- Prefer structured events and SQLite-backed state over ad hoc process memory.
- Pass `context.Context` through I/O paths.
- Keep tool access scoped to `workspace` and `allowed_paths`.
- Run focused package tests during development, then `go test ./... -count=1` before handing off.
- Historical `docs/V*-...` design docs are snapshots. Update living docs when behavior changes.

---

## Architecture Evolution

See [ROADMAP.md](../history/ROADMAP.md) for current status and [TASKS.md](../history/milestones/TASKS.md) for the active tracker.

Recent source-backed milestones after V26:

| Version | What | Design Doc |
|---------|------|------------|
| V27 | Serve-mode tool executor | [V27](../history/milestones/V27-SERVE-TOOL-EXECUTOR-DESIGN.md) |
| V28 | Project and git tools | [V28](../history/milestones/V28-PROJECT-GIT-TOOLS-DESIGN.md) |
| V29 | Tool guidance and session awareness | [V29](../history/milestones/V29-TOOL-GUIDANCE-SESSION-AWARENESS-DESIGN.md) |
| V30 | Native Ollama tool calling | [V30](../history/milestones/V30-NATIVE-TOOL-CALLING-DESIGN.md) |
| V31 | Native chat streaming gap | [V31](../history/milestones/V31-CHAT-STREAMING-GAP.md) |
| V32 | Operating environment | [V32](../history/milestones/V32-LUMI-OPERATING-ENVIRONMENT.md) |
| V33 | Conversation awareness | [V33](../history/milestones/V33-CONVERSATION-AWARENESS.md) |
| V34 | OpenAI Responses provider | [V34](../history/milestones/V34-OPENAI-RESPONSES-DESIGN.md) |

## See Also

- [Getting Started](GETTING_STARTED.md)
- [Architecture](../architecture/ARCHITECTURE.md)
- [Documentation Hub](../README.md)
