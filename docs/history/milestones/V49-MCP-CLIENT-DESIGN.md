# V49 — MCP Client Foundation

**Status:** Source-current (foundation; transport + wiring are follow-ups)
**Last Updated:** 2026-06-29

## Goal

Let Prizm consume external **Model Context Protocol (MCP)** tool servers
(filesystem, GitHub, Slack, databases, …) so the autonomous-dev loop can use the
whole MCP ecosystem — while keeping Prizm's safety model. Per the MCP analysis: the
high-leverage direction is Prizm-as-MCP-**client**, registering remote tools into
the existing policy-gated `tool.Registry`.

## Why it fits

Prizm's `tool.Tool` interface (`Name/Description/Schema/Execute`) maps almost 1:1
onto an MCP tool (`name/description/inputSchema/tools-call`). Crucially, **every**
tool runs through `tool.Executor.ExecuteWithPolicy` (policy → approval → execute →
events). So an MCP-backed tool registered in the `Registry` inherits policy gating,
approval, path/write enforcement, audit events, and the gated loop's per-phase
`allowed_tools` whitelist — with **no engine or core change**.

## This iteration (foundation)

`internal/tool/mcp`:

- **`Client` interface** — `ListTools(ctx)` / `CallTool(ctx, name, args)`. The
  package is transport-agnostic; a concrete stdio JSON-RPC / streamable-HTTP client
  is a separate, swappable implementation. This keeps the mapping logic
  unit-testable without spawning a subprocess or opening a socket.
- **`ToolDef` / `CallResult`** — the MCP shapes Prizm needs.
- **`ToolName(server, tool)`** — namespaces remote tools as
  `mcp_<server>_<tool>` (sanitized) so servers can't collide and the names are easy
  to policy-scope.
- **`SchemaFromJSON`** — maps an MCP JSON-Schema input object to a Prizm
  `ToolSchema` (properties → `ParamSpec`, honoring `required`), degrading gracefully
  on odd/empty schemas.
- **`mappedTool`** — adapts one MCP tool to `tool.Tool`; `Execute` proxies the
  `CallTool`, maps an MCP `isError` (or transport error) to a failed `ToolResult`
  rather than a Go error, so the agent can recover.
- **`RegisterServer(ctx, reg, server, client)`** — lists the server's tools and
  registers a wrapper per tool into a `tool.Registry`, returning the names and
  collecting per-tool registration errors without aborting the rest.

## Tests

`internal/tool/mcp/mcp_test.go`: namespacing/sanitize; schema mapping (types,
required, nil); **end-to-end** register-into-a-real-`tool.Registry` then
`reg.Execute(...)` proxies to a mock server (proving the remote tool is a
first-class Prizm tool); MCP-`isError` and transport-error → failed result; and
`RegisterServer` guards (nil registry, list error).

## Serve wiring (shipped)

`mcp_servers:` in `prizm.yaml` declares external servers
(`{name, command, args, env, enabled}` → `orchestrator.MCPServerConfig`).
`prizm serve` maps them (`mcpServerSpecs`) and calls
`mcp.RegisterServers(ctx, toolReg, specs, mcp.ProcessClientFactory)` after the
built-in tools are registered, so each enabled server's tools join the live
policy-gated registry as `mcp_<name>_<tool>`.

`RegisterServers` is robust and unit-testable: the **client factory is injected**
(production = `ProcessClientFactory` spawning a stdio subprocess; tests use a
fake), it performs the `initialize` handshake per server, and **one server failing
does not abort the others** — each `RegisterResult` carries its own error, logged
at startup.

## Policy default (shipped)

MCP tools (`mcp_<server>_<tool>`) are **approval-required by default** in the tool
policy engine: the `EvaluatePolicyForAgent` default branch matches the `mcp_`
prefix and returns `RequiresApproval` — never silently `Denied` (which would make
them unusable) and never auto-run. A dedicated `PolicyConfig.AutoApproveMCP` flag
opts into unattended execution; it is **deliberately separate from
`AutoApproveMutations`**, so the autonomous wake/gated loop (which sets
`AutoApproveMutations`) still gates MCP tools behind approval — remote, untrusted,
attacker-influenced schemas don't piggyback on the local-mutation opt-in.

### Config plumbing (shipped)

`mcp_auto_approve: true` in `prizm.yaml` sets `PolicyConfig.AutoApproveMCP` in serve
mode. Because the autonomous executor derives its policy by copying the base
(`newAutoExec`), the flag flows into the gated loop automatically — and stays off
by default, so unattended MCP execution is always an explicit operator choice. A
config round-trip test covers `mcp_servers` + `mcp_auto_approve` parsing.

## Inspection: `prizm mcp`

`prizm mcp [--config prizm.yaml] [--json]` lists the configured MCP servers (name,
command+args, enabled state) and the global approval posture
(approval-required vs `mcp_auto_approve`). It is read-only — it reports
configuration, not live connections — so a user can verify their setup before
`prizm serve` connects them. `mcpServerViews` and `renderMCPServers` are pure and
unit-tested.

`prizm mcp probe <name>` live-connects a configured server (spawns it via
`ProcessClientFactory`), runs the handshake, and lists its actual tools **without
registering them** — a connectivity check beyond config. `mcp.ProbeServer`
(injectable factory) and `renderProbedTools` are pure/unit-tested; the subprocess
path is the production wiring.

## Follow-ups (the rest of MCP client support)

1. **Transport:** streamable-HTTP transport implementing `Client` (stdio is done).
2. **Reverse direction (optional):** expose Prizm's `Registry` as an MCP **server**
   for interop with Claude Desktop / other hosts.
