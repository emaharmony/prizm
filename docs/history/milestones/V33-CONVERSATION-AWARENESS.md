# V33 — Conversation Awareness

**Date:** 2026-06-08
**Author:** Lumi
**Status:** Draft — pending Ema review

---

## Problem Statement

When Ema asks Prizm "what's the state of the project?", Prizm sometimes answers about the wrong project — telling him about BassBook when he asked about Prizm. When someone says "hey" in the fun channel, Prizm responds with a full dev-mode analysis. When someone shares a meme in manager-room, Prizm tries to be helpful about it.

These are all the same disease: **the agent has no situational awareness.** It doesn't know:

1. **Where it is** — which channel, what that channel is for, who else is there
2. **What it's talking about** — which project, which context, what's relevant
3. **Who it is** — the system prompt says "You are lumi, a lead assistant" but SOUL.md says something completely different
4. **Whether it should respond** — every message triggers a full LLM call, even "+1"s and memes
5. **How to behave** — one conversation_postfix for all channels, but different channels need different behavior

The agent receives a flat dump of all context files (SOUL.md, AGENTS.md, USER.md, MEMORY.md) and a generic system prompt, then guesses. Sometimes it guesses wrong.

### Concrete Failure Modes

| What Ema says | What Prizm does | What it should do |
|---------------|----------------|-------------------|
| "What's the state of Prizm?" | Tells him about BassBook | Use project_overview on the Prizm repo |
| "Hey" in fun channel | Responds with dev analysis | Casual greeting, no tools |
| Asks a question in manager-room | Gives a 3-paragraph answer with menus of options | Makes a decision, states it directly |
| Someone shares a meme | Tries to be helpful about it | Acknowledges and moves on, or doesn't respond |
| "Fix the bug in the tool loop" | Immediately starts coding | Creates a plan first (V32 guard rail) |

---

## Root Causes

### 1. Identity Override

The system prompt starts with:
```
You are {id}, a {role} assistant.
```

But `id` is "lumi" and `role` is "lead" from the config. SOUL.md says the agent is Astraea, a Factory Orchestrator. The system prompt flattens identity to a generic "assistant" and contradicts the workspace files.

**Fix:** Derive identity from the workspace, not from config. SOUL.md and IDENTITY.md should be the primary identity source. The config `id` and `role` are metadata, not personality.

### 2. No Response Gate

Every Discord message triggers a full LLM call. There's no check for "should I respond to this?" The pipeline is:

```
message → debounce → rate limit → injection defense → route → session → LLM → respond
```

It should be:

```
message → debounce → rate limit → injection defense → response gate → (if yes) route → session → LLM → respond
```

The response gate answers: "Is this message worth a full LLM call?" Memes, "+1"s, "lol", and other low-signal messages should get lightweight responses or no response.

### 3. No Channel Awareness

The agent doesn't know what channel it's in. State actions inject behavior instructions ("be direct") but don't explain *why* — the agent doesn't know it's in manager-room, or what manager-room is for.

**Fix:** Inject explicit channel context:

```
## Channel Context
You are in #manager-room, a private strategic channel with Ema.
Purpose: Full development mode — architecture, code, decisions, pushback.
The other participant is Ema (Emmanuel), your collaborator.
```

### 4. No Project Scoping

MEMORY.md contains memories from ALL projects mixed together. When the agent reads it, it gets Prizm, BassBook, Eggventura, Pudgy Power — everything. There's no signal about which project is relevant to this conversation.

**Fix:** Add `project` to channel config. When the agent is in manager-room, it knows "this channel is about Prizm." Project-overview and search_files calls are scoped to that project.

### 5. Flat Context Weighting

All context files get injected with the same format (`## Workspace: soul`). SOUL.md and IDENTITY.md are the agent's core identity, but they get the same visual weight as MEMORY.md (which is a catch-all dump).

**Fix:** Layer the prompt with explicit priority:

```
## Who You Are
[from SOUL.md and IDENTITY.md — highest priority]

## How You Work
[from AGENTS.md — collaboration style]

## Who You're Talking To
[from USER.md — Ema's preferences]

## What You Know
[from MEMORY.md — filtered by project relevance]

## Current Channel
[from channel config — where you are, what's expected]

## Current State
[from state files — what you were doing]

## Tools Available
[from tool registry — what you can use]
```

### 6. Universal Tool Guidance

One `toolUsageGuidance` constant applies to all channels. It says "use tools ONLY when explicitly asked." But in manager-room, the agent should be proactive with tools. In fun, it should never use tools.

**Fix:** Tool guidance should vary by channel role. Manager-room = proactive tools. Build-room = structured tool use. Fun = no tools.

### 7. Universal Personality

The `conversation_postfix` applies one personality to all channels. "Stay present, be warm, curious, engaged." But build-room needs terse JSON. Fun needs exaggerated personality. Manager-room needs direct, opinionated, no-menus.

**Fix:** State actions already exist but they're injected as raw text without framing. They should be framed as channel context, not just behavior instructions.

---

## Architecture

### Layered Prompt Assembly

Instead of the current flat concatenation:

```
"You are {id}, a {role} assistant." + context_files + postfix + tools
```

We assemble the prompt in layers, each with explicit framing:

```
Layer 1: IDENTITY        — Who am I? (SOUL.md + IDENTITY.md)
Layer 2: CHANNEL CONTEXT  — Where am I? Who am I talking to? What's expected?
Layer 3: COLLABORATION    — How do I work? (AGENTS.md)
Layer 4: USER CONTEXT     — Who am I talking to? (USER.md)
Layer 5: PROJECT CONTEXT  — What project are we discussing? (channel project mapping)
Layer 6: MEMORY           — What do I know? (MEMORY.md, project-filtered)
Layer 7: WORKING STATE    — What was I doing? (state files, plan files)
Layer 8: TOOLS            — What can I do? (tool registry, channel-filtered)
Layer 9: BEHAVIOR         — How should I respond? (state actions, conversation style)
```

Each layer is explicitly labeled so the LLM knows what each piece of context means and why it's there.

### Response Gate

Before the LLM call, a lightweight check determines if the message warrants a full response:

```go
type ResponseDecision int

const (
    RespondFully   ResponseDecision = iota  // Full LLM call
    RespondLightly                            // Short acknowledgment
    RespondWithTool                           // LLM call with tools
    Skip                                      // Don't respond
)

func ShouldRespond(msg *InboundMessage, channelRole string) ResponseDecision {
    // Skip bot messages (already handled)
    // Skip very short messages in strategic channels ("+1", "lol", "nice")
    // Skip messages that are clearly social in dev channels
    // Always respond to questions and direct mentions
    // Always respond in manager-room (Ema expects it)
}
```

The response gate is NOT an LLM call — it's a fast heuristic. Keywords, message length, channel role, and direct mention detection.

### Channel-Aware Tool Filtering

Tool availability varies by channel role:

```yaml
channel_roles:
  - id: "1491622581348864162"
    role: manager-room
    project: "prizm"
    tools: all
    personality: direct
  - id: "1491622824991920231"
    role: build-room
    project: "prizm"
    tools: all
    personality: terse
  - id: "1493297644821283067"
    role: fun
    project: none
    tools: none
    personality: bubbly
```

When `tools: none`, the tool gate excludes ALL tools. When `tools: all`, the tool gate includes all tools. When `tools: read-only`, only read tools are included.

### Project-Scoped Context

When a channel has a `project` mapping:

1. The system prompt explicitly says: "You are discussing the Prizm project."
2. MEMORY.md injection filters for project-relevant content
3. `project_overview` is called automatically for grounding
4. File tools default to the project's repo path
5. When the user says "the project", they mean this specific project

### Channel Context Injection

Instead of raw state actions like:

```
Full dev mode. All tools. Be direct. Make decisions. Push back.
```

We inject structured channel context:

```
## Channel: #manager-room
Purpose: Private strategic channel with Ema for architecture, code, and decisions.
Project: Prizm (event-native agentic environment)
Participants: You and Ema (Emmanuel, senior dev → AI engineering, ADHD-aware collaboration)
Expected behavior: Full development mode. All tools available. Be direct and technical.
  Make decisions — don't present menus of options. Push back when something is wrong.
  If you see a better path, take it. If something needs fixing, fix it.
  When Ema asks about "the project", they mean Prizm.
```

This is richer than the current state actions because it explains *why* and gives the agent enough context to make good decisions.

---

## Implementation

### Phase 1: Layered Prompt + Identity Fix
- Restructure `rebuildStaticSystemContent()` into layered assembly
- Replace `"You are {id}, a {role} assistant"` with identity from SOUL.md/IDENTITY.md
- Add explicit layer labels (`## Who You Are`, `## Channel Context`, etc.)
- Fall back to config `id`/`role` only if no identity files exist
- **Result:** The agent knows who it is and why each context piece is there

### Phase 2: Response Gate
- Add `ShouldRespond()` heuristic to Discord message handler
- Skip low-signal messages in strategic channels (length < 5 chars, no question mark, no direct mention)
- Always respond in manager-room (Ema expects responses there)
- In fun channel: respond to everything (that's the point)
- In build-room: respond to agent messages and direct mentions
- Add `response_gate.go` to `internal/stage/`
- **Result:** The agent doesn't waste LLM calls on "+1" messages

### Phase 3: Channel-Aware Context
- Add `project` and `personality` fields to channel role config
- Inject structured channel context into the prompt (not just state actions)
- Add project name to system prompt when channel has a project mapping
- Add "When the user says 'the project', they mean {project}" instruction
- **Result:** The agent knows where it is and what project is relevant

### Phase 4: Project-Scoped Memory
- Filter MEMORY.md content by project relevance before injection
- When channel has a `project`, auto-call `project_overview` for grounding
- Set default `workspace_root` for file tools to the project repo path
- **Result:** "What's the state of Prizm?" returns Prizm info, not BassBook

### Phase 5: Channel-Filtered Tools
- Add `tools` field to channel role config (`all`, `read-only`, `none`)
- Filter tool registry based on channel role before tool instruction injection
- Remove tool instructions entirely for `tools: none` channels
- Override `toolUsageGuidance` based on personality type
- **Result:** Fun channel never shows tools. Manager-room gets proactive tool guidance.

---

## Configuration Changes

Current config:
```yaml
agents:
  - id: lumi
    role: lead
    context:
      - soul
      - agents
      - user
      - identity
      - memory
    conversation_postfix: "Stay present..."
    state_actions:
      manager-room:
        inject: |
          Full dev mode. All tools. Be direct...
```

Proposed config:
```yaml
agents:
  - id: lumi
    role: lead                          # Fallback role when no identity files
    provider: ollama
    model: glm-5.3-flash:cloud
    primary: true
    context:                             # Ordered by priority (highest first)
      - soul                             # Layer 1: Identity
      - identity                         # Layer 1: Identity
      - agents                           # Layer 3: Collaboration
      - user                             # Layer 4: User context
      - memory                           # Layer 6: Memory (project-filtered)
    conversation_postfix: "Stay present..."  # Still works as fallback

channel_roles:
  - id: "1491622581348864162"
    role: manager-room
    project: "prizm"                    # Which project this channel discusses
    tools: all                          # all | read-only | none
    personality: direct                 # direct | terse | bubbly | social
    context: |                          # Structured channel context (replaces state_actions.inject)
      You are in #manager-room, a private strategic channel with Ema.
      Purpose: Full development mode — architecture, code, decisions, pushback.
      When Ema asks about "the project", they mean Prizm.
      Make decisions, don't present menus. Push back when something is wrong.

  - id: "1491622824991920231"
    role: build-room
    project: "prizm"
    tools: all
    personality: terse
    context: |
      You are in #build-room, an agent factory channel.
      You are collaborating with other AI agents. Keep messages concise and technical.
      Frame responses as structured data when appropriate.

  - id: "1493297644821283067"
    role: fun
    project: none                       # No project — pure social
    tools: none                         # No tools available
    personality: bubbly
    context: |
      You are in #fun, a casual social channel.
      NO tools, NO code, NO technical responses. You are purely conversational.
      Exaggerated personality: bubbly, playful, enthusiastic about everything.
```

### Backward Compatibility

- `state_actions` still works — if `context` is not set, falls back to `state_actions`
- `conversation_postfix` still works — it's appended after the channel context
- `context` field on channel_roles is NEW — it replaces the raw `inject` text with structured context
- If no `channel_roles` exist, behavior is identical to current (no channel context injected)

---

## What This Fixes

| Problem | Before | After |
|---------|--------|-------|
| Wrong project context | Agent guesses from mixed MEMORY.md | Channel tells agent which project; MEMORY.md is filtered |
| Generic identity | "You are lumi, a lead assistant" | Identity from SOUL.md/IDENTITY.md with config as fallback |
| No channel awareness | State actions inject raw text without context | Structured channel context explains where, why, who |
| Every message gets LLM call | "+1" → full inference | Response gate filters low-signal messages |
| One tool guidance for all | "Use tools ONLY when explicitly asked" | Manager-room = proactive, build-room = structured, fun = none |
| Flat context dump | All files same priority | Layered: identity → channel → collaboration → user → project → memory → state → tools → behavior |
| Wrong personality for channel | One conversation_postfix | Channel-specific personality + context |
| No "should I respond?" check | Always responds | Response gate decides: full, light, tool, or skip |

---

## Testing Strategy

### Unit Tests
- `ShouldRespond()` — test message classification for each channel role
- `buildLayeredPrompt()` — verify layer ordering and labels
- `filterToolsByChannelRole()` — verify tool filtering (all, read-only, none)
- `resolveProjectForChannel()` — verify project mapping
- `formatChannelContext()` — verify structured context output

### Integration Tests
- Send "What's the state of Prizm?" to manager-room → should get Prizm info, not BassBook
- Send "hey" to fun channel → should get casual response, not dev analysis
- Send "+1" to manager-room → should get a lightweight acknowledgment, not a full LLM call
- Send "fix the bug" to build-room → should get terse structured response
- Send a question in manager-room with tools=all → should proactively use tools
- Send a question in fun channel with tools=none → should not call any tools

### Manual Testing
- Watch Prizm respond in each Discord channel
- Verify project scoping (ask about "the project" in different channels)
- Verify personality shifts between channels
- Verify low-signal messages don't trigger full LLM calls