# Prizm YAML Reference

Field-by-field reference for the YAML files Prizm reads. Use it as a manual: find
your file type in the index, copy the example, and edit fields with confidence.

## Overview

Prizm uses YAML for workflows, policies, adapters, and the main runtime config
(`prizm.yaml`, which contains agent, provider, channel, scheduler, and
remembrance sections). Each section below shows a real example and documents its
fields. Status labels:

- **Preview/Stable** — implemented and exercised by demos/tests.
- **Preview** — implemented; interface may still change.
- **Experimental** — implemented but advanced/optional; expect rough edges.

## YAML File Types

| YAML Type | Example File | Purpose | Status |
|---|---|---|---|
| Workflow YAML | `examples/workflows/demo-echo.yaml` | Ordered workflow steps | Preview/Stable |
| Policy YAML | `policies/default.yaml` | Allow/deny/approval rules | Preview |
| Adapter step (workflow) | `examples/workflows/demo-adapter.yaml` | Dispatch to an adapter | Preview |
| Agent config | `prizm.yaml` (`agents:`) | Agent roles/providers | Experimental |
| Provider config | `prizm.yaml` (`agents[].provider/model`) | Model providers | Experimental |
| Scheduler config | `prizm.yaml` (`prizm.scheduler`) | Scheduled runs | Experimental |
| MCP config | `prizm.yaml` (`mcp_servers`) | MCP tool servers | Experimental |
| Remembrance config | `prizm.yaml` (`remembrance`) | Memory service client | Experimental |

## Workflow YAML

### Purpose

Defines a named workflow that runs Prizm steps in order. Workflows are loaded
from `examples/workflows/`.

### Example

```yaml
name: demo.echo_tool
description: Demo workflow that runs an echo tool.
version: 1

steps:
  - id: echo
    type: tool.execute
    tool: echo
    input:
      text: "hello from workflow"
```

### Fields

| Field | Type | Required? | Description |
|---|---|---:|---|
| `name` | string | Yes | Unique workflow name (used by `workflow run <name>`) |
| `description` | string | No | Human-readable description |
| `version` | number | Recommended | Workflow version |
| `steps` | list | Yes | Ordered workflow steps |

### Natural Gates Token Budgets

Natural Gates workflow configs (`version: 2`, such as `examples/workflows/gated-loop.yaml`) use `global.max_total_tokens` as the run-wide prompt+completion ceiling:

| Value | Meaning |
|---:|---|
| `-1` | Explicitly unlimited. Use only when an external guardrail bounds the run. |
| `0` or omitted | Built-in default ceiling (`2,000,000` tokens). |
| Positive integer | Explicit run ceiling in tokens. |
| Less than `-1` | Invalid; config loading rejects it. |

Project entries in `prizm.yaml` may set `projects[].token_budget` with the same `-1` / `0` / positive semantics. A project token budget overrides the workflow's `global.max_total_tokens` for that project.

### Step Fields

| Field | Type | Required? | Description |
|---|---|---:|---|
| `id` | string | Yes | Unique step ID inside the workflow |
| `type` | string | Yes | Step type, e.g. `tool.execute`, `dispatch.run`, `delegate` |
| `when` | string | No | Condition guarding step execution |
| `input` | object | No | Step-specific input |
| `tool` | string | For `tool.execute` | Tool name |
| `adapter` | string | For `dispatch.run` | Dispatch adapter name |
| `action` | string | For `dispatch.run` | Adapter action name |
| `agent` | string | For `delegate` | Target agent id |

### Adapter Dispatch Example

```yaml
name: demo.adapter_echo
description: Demo workflow that uses the echo adapter
version: 1
steps:
  - id: echo
    type: dispatch.run
    adapter: echo
    action: echo
    input:
      message: "hello from adapter"
```

### Multi-Agent Delegation Example

```yaml
name: code-and-review
version: 1
description: "Plan, implement, and review code with multiple agents"
steps:
  - id: plan
    type: delegate
    agent: planner
    input:
      task: "Break down this task: {{ .task }}"
  - id: implement
    type: delegate
    agent: coder
    when: "step.plan.status == completed"
    input:
      task: "Implement the plan:\n{{ step.plan.output }}"
```

## Policy YAML

### Purpose

Defines allow/deny/approval rules. Policy decides permission; local validators
still enforce input safety.

### Example

```yaml
policies:
  - id: deny_shell_execution
    description: Block shell execution.
    match:
      action: tool.execute
      resource.name: run_command
    decision: denied
    reason: Shell execution is not supported.
    severity: critical
```

### Fields

| Field | Type | Required? | Description |
|---|---|---:|---|
| `policies` | list | Yes | List of policy rules |
| `id` | string | Yes | Unique policy rule ID |
| `description` | string | No | Human-readable explanation |
| `match` | object | Yes | Exact-match criteria (e.g. `action`, `resource.name`, `context.mode`) |
| `decision` | string | Yes | `allowed`, `denied`, or `requires_approval` |
| `reason` | string | Recommended | Explanation recorded in artifacts/events |
| `severity` | string | No | e.g. `warning`, `critical` |

## Adapter YAML

Adapter dispatch is expressed as a workflow step (`type: dispatch.run`) as shown
above. Inspect available adapters with `prizm adapter list|show|health`. The
`echo` adapter is safe and used by the adapter demo. Domain adapters (e.g.
trading) are gated by policy — see the `deny_live_trading_adapter` rule in
`policies/default.yaml`.

## Agent YAML

### Purpose

Agents are defined under `agents:` in `prizm.yaml`. Each agent's `id` becomes its
event namespace prefix.

### Example

```yaml
agents:
  - id: astraea
    role: orchestrator
    provider: ollama
    model: "llama3.2"
    primary: true
    context:
      - soul
      - agents
      - user
    capabilities:
      - plan
      - route
      - review
      - report
```

### Fields

| Field | Type | Required? | Description |
|---|---|---:|---|
| `id` | string | Yes | Unique agent id / event namespace prefix |
| `role` | string | Recommended | Agent role (e.g. `orchestrator`, `coder`, `reviewer`) |
| `provider` | string | Yes | Provider: `mock`, `ollama`, `openai`, `openai_responses`, `anthropic`, `gemini`, `claude_code` |
| `model` | string | Yes | Model name for the provider |
| `primary` | bool | No | Marks the default agent |
| `context` | list | No | Context layers to inject (e.g. `soul`, `agents`, `user`) |
| `capabilities` | list | No | Capability tags used for routing |
| `invocable_via_api` | bool | No | Opt into `POST /api/v1/agents/<id>/invoke` (requires API token) |

## Provider YAML

Providers are not a separate file; they are selected per agent via `provider:`
and `model:` (see Agent YAML) or via `--provider`/`--model` on `prizm run`.
API-backed providers require credentials in environment variables (e.g.
`OPENAI_API_KEY`).

## Scheduler YAML

### Example

```yaml
prizm:
  scheduler:
    enabled: false
    jobs:
      - name: "status-report"
        schedule: "0 */2 * * *"
        event: "prizm.task.scheduled"
        payload:
          action: "status_report"
        enabled: true
```

Full cron reference: [docs/SCHEDULER.md](../operations/SCHEDULER.md).

## MCP YAML

### Example

```yaml
mcp_servers:
  - name: "filesystem"
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-filesystem", "D:/projects"]
    env: []
    enabled: true

mcp_auto_approve: false
```

MCP tools register as `mcp_<name>_<tool>` and run through the same policy engine
as built-in tools. Probe before enabling: `prizm mcp probe`.

## Remembrance YAML

### Example

```yaml
remembrance:
  enabled: false
  url: "http://localhost:18790"
  timeout_seconds: 60
```

## Full Examples

- Workflow: [`examples/workflows/demo-echo.yaml`](../../examples/workflows/demo-echo.yaml)
- Adapter step: [`examples/workflows/demo-adapter.yaml`](../../examples/workflows/demo-adapter.yaml)
- Multi-agent: [`examples/workflows/demo-agents.yaml`](../../examples/workflows/demo-agents.yaml)
- Gated loop: [`examples/workflows/gated-loop.yaml`](../../examples/workflows/gated-loop.yaml)
- Policy: [`policies/default.yaml`](../../policies/default.yaml)
- Full runtime config: [`prizm.yaml.example`](../../prizm.yaml.example)

## Validation Rules

- Workflow names should be unique.
- Step IDs must be unique inside a workflow.
- Unknown step types should fail clearly.
- Policy rule IDs should be unique.
- `decision` must be one of `allowed`, `denied`, `requires_approval`.
- Secrets should not be stored in YAML — use environment variables for API keys.

Validate your `prizm.yaml` with:

```bash
go run ./cmd/prizm-cli config --config prizm.yaml
```

## Common Mistakes

### Duplicate step IDs

Bad:

```yaml
steps:
  - id: run
    type: tool.execute
  - id: run
    type: validation.run
```

Good:

```yaml
steps:
  - id: run_tool
    type: tool.execute
  - id: run_validation
    type: validation.run
```

### Storing secrets in YAML

Bad:

```yaml
openai_api_key: sk-...
```

Good — reference an environment variable instead:

```yaml
# set OPENAI_API_KEY in your environment; do not inline the key
auth_token_env: "PRIZM_API_TOKEN"
```

## Agent Loop Config

### V71: Dual-Mode Agent Loop

The agent loop controls how Prizm executes multi-turn tool calls. Two modes are available:

- **`classic`** (default): Capped iteration loop with nudge at 50, tool stripping at 80, hard cap at 100.
- **`agentic`**: While-true loop with doom detection (pauses on 3 identical calls), nudge at 50, tool strip at 80, hard limit at 200.

```yaml
prizm:
  # Global agent loop mode (classic or agentic)
  agent_loop: "classic"

agents:
  - id: lumi
    # Per-agent override (empty = use global setting)
    agent_loop: "agentic"
```

### Doom Detection (agentic mode only)

When the agentic loop detects 3 consecutive identical tool calls (same tool + same arguments), it:
1. Injects a system message telling the model to break out
2. Resets the doom detector
3. Gives the model one more chance to self-correct

This prevents infinite loops without hard-stopping the agent mid-task.

### When to Use Agentic Mode

- Use **classic** for simple conversations and bounded tasks
- Use **agentic** for complex multi-step work (research, coding, plan execution)
- Agentic mode allows the model to continue executing after plan creation instead of stopping

## Context Mode Config (V72)

### Open Book Context Strategy

Controls how workspace context (SOUL.md, AGENTS.md, MEMORY.md, etc.) is injected into the system prompt.

- **`full`** (default): All context files are loaded into the system prompt. ~150K tokens.
- **`open_book`**: Only file summaries (first 20 lines) and a structured index are injected. ~2K tokens. The model uses `read_file` to load specific files on demand.

```yaml
prizm:
  # Global context mode (full or open_book)
  context_mode: "open_book"

agents:
  - id: lumi
    # Per-agent override (empty = use global setting)
    context_mode: "full"  # Override: this agent gets full context
```

### Why Open Book?

In agentic mode, the model makes many tool calls. Each tool result adds to the conversation context. Starting with 150K tokens of workspace context means the model hits its context window after just 2-3 tool calls, causing failover or empty responses.

Open book mode reduces the initial prompt to ~2K tokens, leaving room for 50+ tool calls before hitting context limits.

### When to Use Which?

- **`full`**: Simple conversations, few tool calls, or when the model needs all context upfront
- **`open_book`**: Complex tasks, many tool calls, agentic mode, or when using models with smaller context windows
