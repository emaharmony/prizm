# P-008: Tool Relevance Gate

**Date:** 2026-06-07
**Status:** Pre-Dev Architecture Check
**Author:** Lumi (planner)
**Reviewer:** Mango (architecture review)

## Problem

Prizm agents call tools inappropriately — e.g., reading files when asked "say hello." Current mitigation is prompt text ("don't use tools for conversation"), which is:
- Model-dependent (glm-5.3-flash:cloud ignores it under context pressure)
- Fragile (topic bleed from conversation history overrides guidance)
- Not enforceable (no code-level guard)

## Root Cause

Tools are always included in the LLM request regardless of message intent. The model sees 14 tools and defaults to using them, especially when prior conversation context makes file/project topics salient.

## Proposed Solution: ToolRelevanceGate

A new pipeline stage that decides **whether to include tools** in the LLM request, based on the user's current message.

### Architecture

```
InboundMessage → Debounce → RateLimit → InjectionCheck → Route
  → Session → ToolRelevanceGate → LLM (with or without tools)
```

The gate sits between session management and LLM invocation. It analyzes the **current user message only** (not history) and returns a `ToolDecision`:

```go
type ToolDecision int

const (
    ToolDecisionInclude   ToolDecision = iota  // Include all tools
    ToolDecisionExclude                         // No tools — conversational
    ToolDecisionSubset                           // Only relevant subset
)
```

### Heuristic Rules (V1 — no LLM needed)

Simple keyword + pattern matching. Fast, deterministic, no extra LLM call.

**Exclude tools when:**
- Message length < 20 chars AND no tool keywords
- Message matches conversational patterns (greetings, thanks, bye, "lol", emojis)
- No tool-relevant nouns (file, code, project, repo, git, branch, diff, search, read, directory, folder, path)
- No question about specific filesystem/code content

**Include tools when:**
- Message contains tool-trigger words: read, search, find, list, show, open, check, git, branch, status, diff, log, commit, push, project, file, directory, folder, path, code, repo, overview
- Message asks a factual question about code/files/projects ("what does X do?", "how is Y structured?")
- Message contains a file path pattern (`/path/to/file`, `./relative`, `~/home`)

**Subset tools when:**
- Message mentions git but not files → only git tools
- Message mentions files but not git → only file/project tools
- Message is ambiguous → include all tools (safe default)

### Implementation

```go
package stage

type ToolRelevanceGate struct{}

type GateResult struct {
    Decision   ToolDecision
    Reason     string           // For logging/debugging
    ToolFilter []string         // Tool names to include (for Subset)
}

func (g *ToolRelevanceGate) Evaluate(message string, availableTools []string) GateResult
```

### Key Design Decisions

1. **V1 is heuristic, not LLM-based** — zero latency cost, deterministic, testable
2. **Gate applies per-message** — not per-session. Each message is evaluated independently.
3. **Conservative default** — when uncertain, include tools (false negatives are worse than false positives here; missing a tool the user needs > unnecessary tool call)
4. **Logging** — every gate decision is logged with reason for observability
5. **Bypass for agent messages** — agent-to-agent messages always include tools (they're task-oriented by nature)
6. **Configurable** — `tool_gate: enabled: true/false` in prizm.yaml

### Test Cases

| Input | Decision | Reason |
|-------|----------|--------|
| "Hey Lumi!" | Exclude | Short greeting, no tool keywords |
| "Say hello" | Exclude | Conversational, no tool keywords |
| "What do you think about Rust?" | Exclude | Opinion question, no file/code reference |
| "Read /Users/ema/projects/repos/prizm/main.go" | Include | Contains "Read" + file path |
| "What's the project structure?" | Include | Contains "project" + structure question |
| "git status" | Subset | Git keyword → only git tools |
| "Search for TODO in the codebase" | Include | Contains "Search" + code reference |

### Edge Cases

- **Context bleed**: User discussed architecture earlier, now says "thanks" → Gate sees "thanks" → Exclude. Correct behavior.
- **Ambiguous**: "Check that" → Could be conversational or tool-relevant. Include all tools (conservative).
- **Follow-up**: "Now do the same for bassbook" → Previous context was tool-relevant, but current message has "bassbook" (project name). Include tools. Correct.
- **Multi-intent**: "Hey! Also can you read the config?" → Contains tool keywords → Include. Correct.

### Future Enhancements (V2+)

- Small local model (DistilBERT) for intent classification — handles ambiguity better
- Per-tool relevance scoring with confidence thresholds
- Session-level tool frequency tracking (if tools were used in last 3 messages, likely needed again)

## Mango Review Required

Mango must evaluate:
1. Is the heuristic approach sound, or should V1 use a small LLM?
2. Is the gate placement correct (after session, before LLM)?
3. Should the gate also work for the text-based tool loop?
4. Is the conservative default (include when uncertain) correct?
5. Any vulnerability: can the gate be bypassed via injection?
6. Score: must be ≥ 8.75