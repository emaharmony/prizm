# P-010: Per-Channel State Actions

## Problem
Prizm has no way to configure different behaviors per channel or state. Right now:
- Channel routing (fun vs dev) lives only in workspace AGENTS.md as personality instructions
- `conversation_postfix` is a single string on the agent config — applies everywhere
- The pipeline has no concept of "state actions" — instructions that apply when in a specific context

## Design

### State Actions in Config
Add a `state_actions` map to `AgentConfig` in prizm.yaml:

```yaml
agents:
  - id: lumi
    role: "lead"
    # ...
    conversation_postfix: "..."  # default behavior
    state_actions:
      manager-room:
        inject: |
          You are in the manager room — a private strategic channel.
          Full dev mode: all tools available, be direct and technical.
          No fluff, no hedging. Make decisions. Push back when something is wrong.
      build-room:
        inject: |
          You are in the build room — agent factory channel.
          You are collaborating with other AI agents. Keep messages concise and technical.
          Frame responses as structured data when appropriate. No pleasantries.
      fun:
        inject: |
          You are in the fun channel — casual social space.
          NO tools, NO code, NO technical responses. You are purely conversational.
          Exaggerated personality: bubbly, playful, enthusiastic about everything.
          React to what people say with warmth and humor. Ask follow-ups. Be present.
      agent:
        inject: |
          This message is from another AI agent. Treat it as peer input.
          Respond with structured information. No pleasantries or personality overlay.
          If the agent is reporting an error or asking for help, provide a direct answer.
```

### How It Works

1. **Config Loading**: `AgentConfig.StateActions` is a `map[string]StateAction` loaded from YAML
2. **Pipeline Integration**: After routing + session lookup, before prompt building, the handler resolves which state action applies based on channel ID → channel role mapping
3. **Prompt Injection**: The `inject` text is appended to the system prompt AFTER `conversation_postfix` but BEFORE tool instructions
4. **Channel Role Map**: The `ChannelConfig` already has channel IDs. We add a `role` field that maps channel IDs to state action keys.

### Architecture

```
Config Layer:
  AgentConfig.StateActions map[string]StateAction  // key = state name
  ChannelConfig.Role     string                     // "manager-room", "build-room", "fun"

Pipeline Layer:
  Message arrives → debounce → rate limit → injection check → route → session
    → resolve state (channel role + agent-to-agent override)
    → build prompt: identity + context + conversation_postfix + STATE_ACTION + tools
    → LLM → tool loop → response
```

### State Resolution Rules
- If the message channel has a `role` matching a state action key → inject that action
- If the message is from an agent (listen_to_agents) → use the "agent" state action
- If no match → only `conversation_postfix` applies (current behavior)
- Multiple matches: exact channel role wins over "agent" state

### StateAction Type
```go
type StateAction struct {
    Inject string `yaml:"inject"`  // Text to append to system prompt
    // Future: Tools   []string `yaml:"tools"`  // Tool whitelist/blacklist
    // Future: Model   string   `yaml:"model"`    // Override model for this state
}
```

### Implementation Plan
1. Add `StateActions` to `AgentConfig` and `Role` to `ChannelConfig`
2. Add `state_actions` to `prizm.yaml`
3. In `handleDiscordMessage`, resolve state action from channel role
4. In `handleAgentMessage`, resolve state action as "agent"
5. Inject state action text into prompt after conversation_postfix
6. Same for `prizm chat` CLI — use default state action or none
7. Tests for state resolution and prompt injection

### Why Not Just Workspace Files?
- Workspace files (AGENTS.md) are static — they can't change per-channel
- The `conversation_postfix` applies everywhere — can't specialize per context
- State actions are config-driven: changing a channel's behavior is a YAML edit, not a workspace file edit
- This makes the system portable: different Prizm instances can have different state actions without changing workspace files

### Why Inject Into Prompt (Not Code Logic)?
- LLMs respond well to clear behavioral instructions in the system prompt
- This avoids hard-coding behavior in Go — the same agent can be chatty in fun and technical in dev
- It's the same pattern as `conversation_postfix` but scoped to a context
- Future state actions could include tool overrides, model overrides, etc.