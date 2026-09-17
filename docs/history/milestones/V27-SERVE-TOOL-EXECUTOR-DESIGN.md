# V27 - Serve-Mode Tool Executor

## Mission

Wire the full tool executor into `prizm serve` so live Discord and chat-style interactions can inspect the workspace and execute policy-gated operations through the same runtime controls used by one-shot runs.

## What Changed

### Serve Tool Registry

`cmd/prizm-cli/cmd_serve.go` initializes a tool registry for the live conversation context. The registry includes:

- Basic read tools.
- Project comprehension tools.
- Git read tools.
- Git mutation tools.
- State and plan tools when their managers are available.

The serve context holds both the executor and `tool.PolicyConfig` so tool calls are not raw model actions.

### Workspace and Allowed Paths

Tool policy is rooted at `prizm.workspace`. If that field is empty, serve mode falls back to the current directory for the live tool registry.

`prizm.allowed_paths` adds extra roots. The workspace root is always implicitly allowed. Path validation resolves symlinks to prevent escape from allowed roots.

### Mutation Control

Read-only tools can execute directly when inputs pass policy. Mutation tools are treated separately:

- File write tools are proposal/dry-run oriented unless explicitly approved.
- Git mutation tools are registered but approval-aware.
- Guard and plan checks can block mutation tools when no valid plan exists.

### Native and Text Tool Paths

V27 is compatible with both tool-loop paths:

- ChatProvider path: structured native tool calls.
- Text fallback path: parsed JSON tool requests.

Both paths execute through the same `tool.Executor`.

## Public Interfaces

No new CLI command was added. The user-facing change is that `prizm serve` can safely expose tools to configured agents.

Relevant config:

```yaml
prizm:
  workspace: "D:/_projects_/prizm"
  allowed_paths: []

channel_roles:
  - id: "general"
    role: "manager-room"
    tools: "read-only"
```

## Testing

Coverage lives across serve, tool, policy, path safety, and guard tests:

- Tool registry includes the expected built-ins.
- Policy allows read tools inside allowed roots.
- Policy blocks paths outside allowed roots.
- Mutation tools are recognized and gated.

## Notes

This version made live agents useful inside a workspace without granting unrestricted filesystem access.
