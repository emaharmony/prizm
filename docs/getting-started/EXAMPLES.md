# Prizm Examples

A guided tour of the example configurations and demo flows shipped in the repo.
All commands assume you are at the repository root. Use `go run
./cmd/prizm-cli ...` or a built `prizm` binary.

## Demo Index

| Example | Demonstrates | Files | Status |
|---|---|---|---|
| 1. Echo Tool Workflow | A minimal tool step | `examples/workflows/demo-echo.yaml` | Preview/Stable |
| 2. Policy Evaluation | Allow/deny/approval decisions | `policies/default.yaml` | Preview |
| 3. Approval-Gated Mutation | File writes requiring approval | `policies/default.yaml` | Preview |
| 4. Adapter Dispatch | Dispatching to the echo adapter | `examples/workflows/demo-adapter.yaml` | Preview |
| 5. Run/Event Inspection | Reading run artifacts | `examples/runs/sample-run/` | Preview |
| 6. Multi-Agent Workflow | Delegation across agents | `examples/workflows/demo-agents.yaml` | Experimental |

## Example 1 — Echo Tool Workflow

**What it demonstrates:** the simplest workflow — a single `tool.execute` step.

**Files:** [`examples/workflows/demo-echo.yaml`](../../examples/workflows/demo-echo.yaml)

**Run:**

```bash
go run ./cmd/prizm-cli workflow show demo.echo_tool
go run ./cmd/prizm-cli workflow run demo.echo_tool
```

**Expected output:** the workflow runs the `echo` tool and reports a completed
step. Artifacts are written to the run directory.

**What to inspect:** `events.jsonl`, `summary.json`, `output.md`.

**Troubleshooting:** if you see `workflow not found`, run from the repo root so
`examples/workflows/` is discoverable.

## Example 2 — Policy Evaluation

**What it demonstrates:** how policy decides `allowed` / `denied` /
`requires_approval`.

**Files:** [`policies/default.yaml`](../../policies/default.yaml)

**Run:**

List the rules, then evaluate a JSON request. Create `request.json`:

```json
{ "action": "tool.execute", "resource": { "type": "tool", "name": "run_command" } }
```

```bash
go run ./cmd/prizm-cli policy list
go run ./cmd/prizm-cli policy evaluate --input request.json
```

**Expected output:** `run_command` is denied by the `deny_shell_execution` rule;
`echo` is allowed by `allow_echo`. A policy artifact is written under
`runs/policy/`.

**What to inspect:** the decision, matched rule, and `reason` for the request.

## Example 3 — Approval-Gated Mutation

**What it demonstrates:** file mutations require operator approval
(`require_approval_for_file_write`).

**Files:** [`policies/default.yaml`](../../policies/default.yaml)

**Flow:**

```bash
# 1. Run a workflow/task that proposes a file mutation.
# 2. List pending approvals:
go run ./cmd/prizm-cli approval list
# 3. Approve or deny:
go run ./cmd/prizm-cli approval approve <id> --by ema
```

**What to inspect:** approval records and the resulting events.

## Example 4 — Adapter Dispatch

**What it demonstrates:** a `dispatch.run` step routed to the `echo` adapter.

**Files:** [`examples/workflows/demo-adapter.yaml`](../../examples/workflows/demo-adapter.yaml)

**Run:**

```bash
go run ./cmd/prizm-cli adapter list
go run ./cmd/prizm-cli workflow run demo.adapter_echo
```

**Expected output:** the echo adapter returns the dispatched message. The
`allow_echo_adapter` policy permits it; live trading dispatch is denied.

## Example 5 — Run / Event Inspection

**What it demonstrates:** the artifact shape of a run.

**Files:** [`examples/runs/sample-run/`](../../examples/runs) — contains
`events.jsonl`, `prompt.md`, `output.md`, `summary.json`.

**Run:**

```bash
go run ./cmd/prizm-cli runs
go run ./cmd/prizm-cli runs latest --json
```

**What to inspect:** each artifact and how events chain together. For a live
gated-loop run, `prizm watch` and `prizm trace <run_id>` show progress and the
causal event DAG.

## Example 6 — Multi-Agent Workflow

**What it demonstrates:** delegation across agents (plan → implement → review).

**Files:** [`examples/workflows/demo-agents.yaml`](../../examples/workflows/demo-agents.yaml)

> Status: Experimental. This flow requires configured agents (see
> [`prizm.yaml.example`](../../prizm.yaml.example)) and a working provider (e.g.
> Ollama). Without configured agents, use it as a structural reference.

**What to inspect:** delegation events (`agent.delegated`, `agent.completed`) in
`events.jsonl`, and how the `when:` conditions gate later steps.

## How Examples Connect to Architecture

- Workflows exercise the **workflow runtime** and **tool executor**.
- Policy examples exercise the **policy engine** and **guard checks**.
- Approvals exercise **approval-gated mutations**.
- Adapters exercise the **adapter contract** and **dispatch**.
- Run artifacts are produced by the **run lifecycle** and event bus.

See [Architecture](../architecture/ARCHITECTURE.md) and [Capability Status](../reference/CAPABILITY_STATUS.md)
for the bigger picture.
