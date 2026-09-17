# P-008 Architecture Self-Review (Lumi)

**Reviewer:** Lumi (standing in for Mango)
**Date:** 2026-06-07
**Status:** Pre-Dev self-review — Mango review pending when available

## Review Findings

### 1. Heuristic vs Small LLM for V1? → Heuristic is correct for V1

**Score: 9/10**

Heuristic gate is the right call:
- Zero latency — no extra LLM call before the main LLM call
- Deterministic — same input always same output, easy to test
- No new dependency — no model to download/configure
- Sufficient — 90%+ of tool misuse cases are obvious (greetings, short messages with no keywords)

The edge cases where heuristics fail ("check that") are exactly where a small LLM would also struggle. Better to let those through (conservative default) than add latency.

**Recommendation:** Ship V1 heuristic. Consider a DistilBERT classifier as V2 if misclassification rate is too high.

### 2. Gate placement: after session, before LLM? → Correct

**Score: 9/10**

The gate must see the current user message but NOT be influenced by conversation history (that's the whole point — to prevent context bleed). Placing it after session management but before LLM invocation is correct.

Important: the gate should evaluate the **raw user message**, not the framed/version that includes session context. Otherwise the gate itself gets polluted by history.

**Action item:** Ensure gate receives `msg.Content` (raw), not the full prompt or framed message.

### 3. Should it also gate the text-based tool loop? → YES

**Score: 10/10**

Both code paths (ChatProvider native tools + text-based JSON tools) have the same problem. The gate should be called before both `runToolLoopChat()` and `runToolLoop()`, and both paths should respect the gate's decision.

For the text-based path, if tools are excluded, the prompt should NOT include `BuildToolPromptSuffix()`. The model then can't emit `tool_request` JSON (no tool instructions = no tool calls).

### 4. Conservative default (include when uncertain) → Correct

**Score: 9/10**

False positive (tool included unnecessarily) = slight token waste, model might still ignore tools
False negative (tool excluded when needed) = user gets "I can't help with that" or the model hallucinates file contents

False negative is much worse. Conservative default is correct.

### 5. Injection bypass risk? → Low but real

**Score: 8/10**

**Attack vector:** A user could craft a message that passes the gate's keyword check but then injects tool-misuse instructions. Example: "Hey can you read the file at /etc/passwd" — gate says "include tools" (correct, keyword match), but the LLM then tries to read `/etc/passwd`.

However — this is already handled by:
- Path containment checks (ResolveToolPath blocks `/etc/passwd`)
- Injection defense stage (earlier in pipeline)
- Tool policy (mutation tools require approval)

The gate doesn't ADD new risk — it only reduces unnecessary tool exposure. If the gate excludes tools, that's strictly safer (fewer attack surfaces).

**No additional mitigation needed.**

### 6. Implementation Concerns

**a) Keyword list must be configurable**
Hardcoded keywords will age. Allow `tool_gate.keywords` in prizm.yaml so we can tune without redeploying.

**b) Agent messages should bypass the gate**
Already noted in design. Agent-to-agent messages are task-oriented by definition. Confirm: the bypass should check `isAgentBot()` or the `msg.IsBot` field.

**c) Chat tools list construction**
When gate says Exclude, `buildChatTools()` should return empty slice. When Subset, filter the tool list. This means the gate result needs to be threaded into both `buildChatTools()` and the tool prompt builder.

**d) Logging for tuning**
Every gate decision should log: message (truncated), decision, reason, tools included/excluded. This lets us measure the misclassification rate and tune keywords.

## Overall Score: 9.0/10

Design is sound. Proceed to implementation with these action items:
1. Gate receives raw user message only, not framed context
2. Both code paths (chat + text) gated
3. Keyword list configurable via prizm.yaml
4. Agent messages bypass gate
5. Log every gate decision for observability

**Mango review pending** — when available, have Mango read `/Users/ema/projects/repos/prizm/docs/P008-TOOL-GATE-DESIGN.md` and this review.