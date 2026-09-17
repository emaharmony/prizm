# V28 - Project and Git Tools

## Mission

Give agents enough read-only project visibility to answer codebase questions accurately, and add approval-aware git tools for controlled handoff work.

## What Changed

### Project Comprehension Tools

V28 added higher-level project tools in addition to basic file reads:

- `read_project` recursively reads a bounded project tree.
- `search_files` searches text across project files.
- `project_overview` reads common project entry points, manifests, configs, README files, and selected architecture files.

These tools accept relative paths inside the configured workspace or absolute paths under allowed roots.

### Git Tools

Read-only git tools:

- `git_status`
- `git_log`
- `git_diff`
- `git_branch_list`

Mutation git tools:

- `git_add`
- `git_commit`
- `git_push`

Mutation tools require policy/approval handling and are classified separately from read tools.

### Rate Limiting

Serve mode includes per-user rate limiting in addition to debounce. This protects the live bot from rapid repeated messages and runaway tool-triggering conversations.

### Tool Policy Updates

The policy layer recognizes project and git tools:

- Project read/search/overview tools are read operations.
- Git status/log/diff/branch are read operations.
- Git add/commit/push are mutation operations.

## Public Interfaces

Tools are visible through:

```bash
prizm tool list
prizm tool run project_overview --input '{"path":"."}' --workspace .
prizm tool run git_status --input '{}' --workspace .
```

In serve/chat mode, tools are exposed to agents according to channel role:

```yaml
channel_roles:
  - id: "dev"
    role: "build-room"
    tools: "all"
```

## Testing

Relevant scenarios:

- Built-in registry includes project and git tools.
- Read-only git tools are policy-approved.
- Git mutation tools require approval.
- Project paths resolve against workspace and allowed roots.
- Symlink and path traversal attempts are blocked.

## Notes

V28 is the point where Prizm agents became practical for codebase inspection rather than only single-file reads.
