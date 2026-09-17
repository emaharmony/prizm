# Prizm Command Reference

Every major `prizm` CLI command grouped by purpose, with what it does and an
example. Commands are shown as `prizm <command>`; when running from source use
`go run ./cmd/prizm-cli <command>`.

> This list mirrors `prizm --help` (the grouped usage in `cmd/prizm-cli`). If a
> command differs from your build, prefer the output of `prizm --help`.

## Overview

Run the built-in help to see the grouped command list at any time:

```bash
go run ./cmd/prizm-cli --help
```

Prizm has no single top-level "global flags" set; most flags are per-command
(e.g. `--config`, `--json`, `--project`, `--agent`).

## Run & Interact

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm run --task <desc>` | Run a one-shot task lifecycle | Preview | `prizm run --task "Explain event-driven architecture" --provider ollama --model llama3.2` |
| `prizm chat` | Interactive terminal chat | Preview | `prizm chat --config prizm.yaml --agent astraea` |
| `prizm serve` | Start the persistent daemon (API, dashboard, bot) | Preview | `prizm serve --config prizm.yaml` |

`prizm run` expects a NATS bus at `nats://localhost:4222`; use `serve` for
embedded NATS or start `cmd/prizm-bus` separately. Key `run` flags:
`--provider`, `--model`, `--temperature`, `--max-tokens`, `--timeout`,
`--dry-run-prompt`, `--project`, `--agent`, `--run-dir`, `--memory-enabled`.

## Set Up Config

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm config wizard` | Interactive setup — generate a `prizm.yaml` | Preview | `prizm config wizard --out prizm.yaml` |
| `prizm config import <file>` | Convert an OpenClaw JSON to `prizm.yaml` | Preview | `prizm config import openclaw.json --out prizm.yaml` |

## Inspect & Validate

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm config` | Validate `prizm.yaml` and summarize it | Preview | `prizm config --config prizm.yaml --json` |
| `prizm doctor` | Preflight check of run dependencies | Preview | `prizm doctor --config prizm.yaml --json` |
| `prizm preview` | Static preview of the gated-loop workflow | Preview | `prizm preview --workflow examples/workflows/gated-loop.yaml` |
| `prizm agent list\|show <name>` | List / show registered agents | Preview | `prizm agent list` |
| `prizm tool list\|run <name>` | List / run built-in tools | Preview | `prizm tool run echo --input '{"text":"hi"}'` |
| `prizm validation list\|run <name>` | List / run validation profiles | Preview | `prizm validation list` |
| `prizm context show` | Show context that would be injected | Preview | `prizm context show --context soul,agents` |

## Workflow Commands

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm workflow list` | List registered workflows | Preview | `prizm workflow list` |
| `prizm workflow show <name>` | Show a workflow's steps | Preview | `prizm workflow show demo.echo_tool` |
| `prizm workflow run <name>` | Run a named workflow | Preview | `prizm workflow run demo.echo_tool` |
| `prizm workflow status <run_id>` | Show a workflow run's status | Preview | `prizm workflow status <run_id>` |
| `prizm workflow cancel <run_id>` | Request durable multi-agent cancellation | Phase 1 | `prizm workflow cancel <run_id>` |
| `prizm workflow resume <run_id>` | Safely resume a durable multi-agent run | Phase 1 | `prizm workflow resume <run_id>` |
| `prizm workflow report <run_id>` | Show a terminal multi-agent report | Phase 1 | `prizm workflow report <run_id> --json` |
| `prizm workflow start` | Start a gated-loop run for a project | Preview | `prizm workflow start --project example --prompt "..."` |

Generic `workflow run` accepts optional `--input <file.json>` and
`--run-dir <dir>`. The `multi-agent-software-task` workflow requires a strict
JSON input and also accepts `--config <prizm.yaml>`. Its status and report
commands support `--json`. See the
[Multi-Agent Workflow](../MULTI_AGENT_WORKFLOW.md) for its input and artifacts.

## Policy Commands

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm policy list` | List policy rules | Preview | `prizm policy list` |
| `prizm policy evaluate --input <file>` | Evaluate a JSON policy request | Preview | `prizm policy evaluate --input request.json` |

## Approval Commands

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm approval list` | List approvals | Preview | `prizm approval list` |
| `prizm approval show <id>` | Show one approval | Preview | `prizm approval show <id> --run <run_id>` |
| `prizm approval approve <id>` | Approve a mutation | Preview | `prizm approval approve <id> --by ema` |
| `prizm approval deny <id>` | Deny a mutation | Preview | `prizm approval deny <id> --by ema` |

## Observe Runs

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm watch` | Live view of a running gated-loop workflow | Preview | `prizm watch --config prizm.yaml` |
| `prizm runs [<id>\|latest]` | List runs / show a run's report | Preview | `prizm runs latest --json` |
| `prizm cost <run_id>` | Show token usage and cost report | Preview | `prizm cost <run_id>` |
| `prizm trace <run_id>` | Show event trace (causal DAG) | Preview | `prizm trace <run_id>` |
| `prizm dashboard` | Start the local read-only dashboard | Preview | `prizm dashboard --port 8080` |
| `prizm status` | Show live service status | Preview | `prizm status` |
| `prizm health` | Check bus health | Preview | `prizm health` |

## Adapter Commands

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm adapter list\|show\|health [<name>]` | Inspect adapters | Preview | `prizm adapter list` |

## MCP Commands

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm mcp` | List configured MCP servers | Experimental | `prizm mcp --json` |
| `prizm mcp probe <name>` | Live-probe a server's tools | Experimental | `prizm mcp probe filesystem` |

## Skills Commands

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm skills` | List SKILL.md skills | Experimental | `prizm skills --json` |
| `prizm skills show <name>` | Show a skill's instructions | Experimental | `prizm skills show <name>` |

## Self-Patching Commands

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm scan` | Scan for issues (`--start` fixes the top one) | Experimental | `prizm scan --severity high --json` |

## Advanced Commands

| Command | Action | Status | Example |
|---|---|---|---|
| `prizm search --query <text>` | Search the vector store | Experimental | `prizm search --query "event bus"` |
| `prizm projection list\|rebuild\|query` | Manage CQRS projection snapshots | Experimental | `prizm projection list` |
| `prizm remembrance health\|status\|serve` | Manage the Remembrance memory service | Experimental | `prizm remembrance health` |
| `prizm version` | Print version | Preview | `prizm version` |

## Command Cheat Sheet

### Run tests

```bash
go test ./...
```

### See available workflows

```bash
go run ./cmd/prizm-cli workflow list
```

### Run the first workflow

```bash
go run ./cmd/prizm-cli workflow run demo.echo_tool
```

### Inspect run status

```bash
go run ./cmd/prizm-cli workflow status <run_id>
```

### Start serve mode (API + dashboard)

```bash
go run ./cmd/prizm-cli serve --config prizm.yaml
```

### Open the read-only dashboard

```bash
go run ./cmd/prizm-cli dashboard
```

## Common Command Flows

### First local demo

1. Run tests: `go test ./...`
2. List workflows: `workflow list`
3. Run the demo workflow: `workflow run demo.echo_tool`
4. Inspect artifacts: `runs latest --json`

### Policy test flow

1. List policies: `policy list`
2. Evaluate a JSON request: `policy evaluate --input request.json`
3. Inspect the written artifact under `runs/policy/`.

### Approval flow

1. Run a workflow that creates an approval (file mutation).
2. List pending approvals: `approval list`
3. Approve or deny: `approval approve <id> --by <name>`

### Debug a failed run

1. Check status: `workflow status <run_id>` or `runs <run_id>`
2. Inspect `events.jsonl` in the run directory.
3. Inspect `summary.json` for the final status.
4. Inspect `output.md` / `prompt.md` for detail.
