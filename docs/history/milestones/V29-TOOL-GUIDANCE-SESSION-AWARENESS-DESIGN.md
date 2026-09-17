# V29 - Tool Guidance and Session Awareness

## Mission

Make tool use more reliable by improving the instructions given to models and by making prompts aware of workspace and conversation state.

## What Changed

### Stronger Tool Guidance

The text-based fallback path uses explicit guidance that tells the model:

- Use tools only when the current user request requires filesystem, code, project, or git information.
- Do not call tools for simple social or explanatory messages.
- Actually emit a tool request when file/code information is needed.
- Prefer `read_file`, `search_files`, and `project_overview` for codebase questions.

This reduced cases where models said "I will search" without calling the tool.

### Tool Schemas in Prompts

Tool descriptions and input schemas are included so models know required arguments and available parameters.

### Workspace Root Awareness

The prompt includes the workspace root. Tool parameter descriptions also explain when to use relative paths and when absolute paths are required for allowed external projects.

### Session Awareness

Serve and chat prompt assembly includes session context, recent messages, active state, and active plans where available. This gives the model continuity without relying only on long-term memory.

### Shared Guidance Constant

Serve and chat paths share a `toolUsageGuidance` block so native and text tool paths stay aligned.

## Public Interfaces

No new user command was added. The behavior is visible through better tool calls in:

```bash
prizm serve --config prizm.yaml
prizm chat --config prizm.yaml
```

## Testing

Relevant scenarios:

- Tool prompt suffix lists available tools and schemas.
- Workspace root is included in tool guidance.
- Conversational messages do not trigger unnecessary tool use.
- File/project/git requests include the appropriate tool options.

## Notes

V29 does not grant new authority. It makes existing tool authority easier for the model to use correctly.
