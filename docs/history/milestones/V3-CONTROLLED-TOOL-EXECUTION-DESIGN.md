# V3 — Controlled Tool Execution

## Mission

Give agents safe, observable, policy-gated hands. V3 introduces a tool registry,
deterministic permission policy, four safe built-in tools, and full tool lifecycle
events. The agent can now request tool execution in its output, and Prizm decides
whether to allow, deny, or escalate before running anything.

**Tools are the only way an agent can affect the outside world.**

## What Changed

### Tool Registry (`internal/tool`)
- `Tool` interface: `Name()`, `Description()`, `Schema()`, `Execute(ctx, input) → result`
- `Registry`: register, list, resolve, validate, execute
- `Policy`: deterministic permission decisions — `approved`, `denied`, `requires_approval`
- `ToolSchema`: input/output parameter specs with types, descriptions, required flags
- `ToolResult`: universal result type (`Success`, `Output`, `Error`)
- Path traversal protection: `..` and absolute paths blocked at the policy layer

### Four Built-in Tools (`internal/tool/builtins.go`)
- **echo**: returns the input as-is (safe passthrough for testing)
- **list_dir**: lists files in the project directory (workspace-scoped only)
- **read_file**: reads file contents (workspace-scoped only)
- **write_file_dry_run**: validates path + content, returns preview — does NOT write to disk

### Agent Tool Parsing (`internal/agent/parser.go`)
- `ParseAgentOutput(output)` — extracts structured tool requests from LLM output
- Supports JSON tool_request and final answer parsing
- Handles markdown code fence extraction
- `BuildToolPromptSuffix()` — injects tool usage instructions into the prompt
- One tool call per run in V3 (sequential execution)

### Tool Lifecycle Events
- `prizm.tool.requested` — agent requested a tool call
- `prizm.tool.approved` — policy approved the tool call
- `prizm.tool.denied` — policy denied the tool call
- `prizm.tool.started` — tool execution has begun
- `prizm.tool.completed` — tool execution finished successfully
- `prizm.tool.failed` — tool execution returned an error

### CLI Commands
- `prizm tool list` — list all registered tools with descriptions
- `prizm tool run <name> --input '{...}' --project <name>` — execute a tool directly

### Artifacts
- `tool_result.json` — persisted per tool execution
- `tool_calls` array in `summary.json` — records all tool invocations

### Mock Provider Update
- Tool-aware prompt suffix added to mock provider output
- Enables end-to-end tool testing without real LLM calls

## Key Packages/Files

| Package / File | Purpose |
|---|---|
| `internal/tool/types.go` | Tool interface, registry, policy, schema types |
| `internal/tool/registry.go` | Registration, listing, resolution, validation |
| `internal/tool/policy.go` | Deterministic permission decisions, path safety |
| `internal/tool/builtins.go` | 4 built-in tools: echo, list_dir, read_file, write_file_dry_run |
| `internal/tool/executor.go` | Tool execution context, event emission, artifact persistence |
| `internal/agent/parser.go` | Parse LLM output for tool requests + final answers |
| `internal/event/event.go` | Extended with 6 tool event types |
| `internal/run/runner.go` | Integrated tool lifecycle into runner pipeline |

## Design Decisions

1. **Policy is deterministic, not probabilistic** — No LLM evaluates policy. Permission
   decisions are hardcoded rules that can be reasoned about and audited. If unclear,
   the policy denies.

2. **Tools are registered, not dynamically loaded** — Every tool must be explicitly
   registered. No runtime code generation or dynamic imports. This prevents injection
   and ensures the tool surface is always known.

3. **Path safety at the policy layer** — `..` path traversal checks and absolute path
   blocks are enforced before execution. The policy layer is the gatekeeper; even if
   a registered tool is exploitable, the policy layer catches path attacks.

4. **write_file_dry_run, not write_file** — In V3, file writes are preview-only.
   No disk mutation occurs. Actual file writes come in V4 as approval-gated mutations.
   This is intentional: V3 proves the tool execution pipeline is safe before V4 adds
   the write capability.

5. **One tool per run (for now)** — V3 supports sequential single-tool calls. Multi-step
   tool orchestration (chain tool A then tool B) is deferred to workflow runtime (V7+).

6. **Agent output parsing is permissive but structured** — The parser handles JSON in
   markdown fences, plain JSON, and gracefully falls back on `tool_request` vs `final`
   key detection. This maximizes compatibility with different LLM output styles.

7. **Events at every step** — Tool approval and denial are separate events before
   execution starts or is skipped. This makes the audit trail complete: you can verify
   that a tool was denied (not just not run).

## Safety Model

| Threat | Protection |
|---|---|
| Path traversal (`..`) | Blocked in `validateInput()` |
| Absolute path escape | Blocked in `validateInput()` |
| Unknown tool execution | Registry rejects unregistered names |
| Shell access | `run_command` not registered — denied by absence |
| File mutation | `write_file_dry_run` only — preview, no disk write |
| Recursive tool calls | Not supported in V3 architecture |

## Test Coverage

- **74+ tests** across 6 packages (up from 46)
- Registry: register, list, resolve, unknown tool, duplicate, validate, execute
- Policy: echo approved, list_dir allowed/denied, read_file allowed/denied, shell denied, path traversal
- Execution: echo, list_dir, read_file, write_file_dry_run, execution failure
- Events: lifecycle, correlation_id, parent chain
- Parser: final, tool_request, invalid JSON, markdown fence extraction
- Runner: V3 tool lifecycle integration tests

## Tool Execution Flow

```
Agent output → ParseAgentOutput()
  → ToolRequest? Yes → emit(prizm.tool.requested)
    → Policy.Evaluate(tool, input)
    → Approved → emit(prizm.tool.approved)
      → emit(prizm.tool.started)
      → Tool.Execute(ctx, input)
      → Success → emit(prizm.tool.completed) → tool_result.json
      → Failure → emit(prizm.tool.failed)
    → Denied → emit(prizm.tool.denied) → agent output = denial reason
  → Final answer → emit(prizm.agent.output) → output.md
```
