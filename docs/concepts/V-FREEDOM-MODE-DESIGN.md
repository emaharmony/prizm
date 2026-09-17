# Prizm Freedom Mode — Design Doc

**Date:** 2026-07-14
**Authors:** Ema + Lumi
**Status:** Draft — Pending Ema Alignment

---

## Context

Prizm's gated workflow (PLAN → BRANCH → IMPLEMENT → FEEDBACK_PRE → EXECUTION → FEEDBACK_POST) provides system-enforced safety. Every mutation goes through proposal → approval → application. Every phase restricts which tools are available. This is excellent for autonomous, scheduled work — but it makes Prizm significantly less capable than OpenClaw or Claude Code for interactive, Ema-directed work.

**The tension:** Prizm's gates are its superpower for *autonomous* work AND its bottleneck for *interactive* work. Ema wants Prizm to eventually replace OpenClaw, but Prizm currently can't do what OpenClaw does — execute shell commands, make multi-step decisions freely, edit files without phase gates.

**The goal:** Give Prizm a "free mode" — full tool access, no phase gates — while preserving the gated workflow for autonomous/scheduled work.

---

## Vision Alignment

From PRIZM-VISION.md:
> "Prizm is an event-native agentic environment... It replaces OpenClaw and any other agentic system entirely."

From Ema's vision notes:
> "Prizm Lumi = left brain (structured, scheduled, deliberate). OpenClaw Lumi = right brain (real-time, creative, reactive)."
> "The plan was always to replace OpenClaw with Prizm."
> "Goal: Get Prizm Lumi to be more capable than OpenClaw."

**This design is the bridge.** It gives Prizm right-brain capabilities without sacrificing left-brain safety.

---

## Design: Dual Operating Modes

### Mode 1: Gated Mode (Current Behavior)

**When:** Autonomous/scheduled work, cron-triggered tasks, auto-patch, project-work loops.

**How it works today:**
- Phase-gated workflow: PLAN → BRANCH → IMPLEMENT → FEEDBACK_PRE → EXECUTION → FEEDBACK_POST
- Policy engine restricts tools per phase
- Mutations require proposal → approval → application
- Every decision emits events for audit trail
- Workflow state persisted to disk

**No changes.** This mode stays exactly as-is.

### Mode 2: Free Mode (New)

**When:** Interactive, Ema-directed work in Discord channels. Ema asks Prizm to do something → Prizm does it, full capability, no gates.

**How it works:**
- No phase gates — direct from message to action
- All tools available simultaneously (read, write, exec, git, search, memory)
- No proposal/approval dance for mutations — direct writes
- Shell/exec tool available with policy-tiered access
- Session-based conversation context (like OpenClaw)
- Events still emitted for audit (but not gating)

**Activation:** Free mode activates when **both** conditions are met:
1. Channel role config has `mode: free`
2. The message sender is the configured `master_user` (Ema's Discord ID)

If a non-master user sends a message in a free-mode channel, Prizm falls back to gated behavior. This is config-driven:

```yaml
shell:
  master_user_id: "164169326142816256"  # Ema's Discord ID — only this user gets free mode
```

---

## Core Addition: Shell Tool

### The Tool

```go
type ShellTool struct {
    Config ShellToolConfig
}

func (t *ShellTool) Name() string { return "shell" }
```

**Schema:**
```json
{
  "command": "string (required) — the shell command to execute",
  "cwd": "string (optional) — working directory, defaults to project root",
  "timeout": "int (optional) — timeout in seconds, defaults to 30"
}
```

**Output:**
```json
{
  "stdout": "command stdout (truncated to 10KB)",
  "stderr": "command stderr (truncated to 5KB)",
  "exit_code": int,
  "duration_ms": int
}
```

### Policy Tiers

The shell tool respects the existing policy engine (`internal/tool/policy.go`) with new config:

```yaml
# prizm.yaml — per channel role
channel_roles:
  - id: "1491622581348864162"  # manager-room
    role: manager-room
    mode: free                   # NEW: free mode for this channel
    tools: all                    # all tools including shell
    shell_policy: tier_2          # NEW: shell access tier
    personality: direct
    tagged_only: false
    context: |
      You are in #manager-room...
```

**Shell Policy Tiers:**

| Tier | Access | Use Case |
|------|--------|----------|
| `none` | No shell access | Fun channel, autonomous gated mode |
| `tier_1` | Allowlist only: `go build`, `go test`, `npm test`, `git status`, `git diff`, `cat`, `ls`, `pwd` | Safe autonomous/scheduled work |
| `tier_2` | Expanded allowlist: all tier_1 + `npm`, `node`, `python3`, `pip`, `curl`, `docker`, `make`, `find`, `grep`, `sed`, `awk`, `head`, `tail`, `wc`, `sort`, `uniq` | Interactive development work |
| `tier_3` | Full access — any command | Ema-directed work only, explicit activation |

**Config:**
```yaml
shell:
  allowlists:
    tier_1:
      - "go build*"
      - "go test*"
      - "npm test*"
      - "git status*"
      - "git diff*"
      - "git log*"
      - "cat *"
      - "ls *"
      - "pwd"
    tier_2:
      - "npm *"
      - "node *"
      - "python3 *"
      - "pip *"
      - "curl *"
      - "docker *"
      - "make *"
      - "find *"
      - "grep *"
      - "sed *"
      - "head *"
      - "tail *"
      - "wc *"
    tier_3:
      - "*"  # everything
  defaults:
    timeout_seconds: 30
    max_output_bytes: 10240
    blocked_patterns:
      - "rm -rf /"
      - "rm -rf ~"
      - "rm -rf *"
      - "mkfs*"
      - "dd if=*of=/dev/*"
      - "> /dev/sda*"
      - "chmod 777"
```

### Safety Hard Blocklist

Regardless of tier, these commands are **always blocked**:
- `rm -rf /` or `rm -rf ~` or `rm -rf *` (root/home/glob deletion)
- `mkfs` (filesystem format)
- `dd if=...of=/dev/...` (raw disk writes)
- `> /dev/sd*` (raw device overwrite)
- `chmod 777` on system directories
- `:(){:|:&};:` (fork bomb)

These are checked at the tool executor level — the model cannot bypass them.

---

## Free Mode Behavior

### Message Flow (Free Mode)

```
Discord Message
    ↓
handleDiscordMessage(msg)
    ↓
Check: is channel role mode == "free"?
    ↓ YES
Check: is sender == master_user_id?
    ↓ YES
Build prompt (full context, no phase restrictions)
    ↓
LLM call with ALL tools available (including shell at configured tier)
    ↓
Tool execution (shell, write_file, git, etc.)
    ↓
Response → Discord
    ↓
Event emitted: prizm.free.action (for audit, not gating)
```

If sender is NOT master_user → falls back to gated behavior for that message.

### What Changes in `handleDiscordMessage`

When `channelRole.Mode == "free"`:
1. **Skip phase gates** — no PLAN/BRANCH/IMPLEMENT workflow
2. **All tools registered** — including shell tool
3. **Direct mutations allowed** — `write_file` instead of `write_file_proposal`
4. **Session-based context** — conversation history persists across messages
5. **Events for audit** — emit `prizm.free.tool_called` and `prizm.free.action_completed` but don't gate on them

### What Stays the Same

- Channel context injection (personality, tools, context)
- Session management (message history)
- Remembrance memory search
- Debounce / rate limiting
- Tagged-only mode check

---

## Integration Points

### 1. Channel Role Config (prizm.yaml)

New fields on `ChannelRole`:
```yaml
channel_roles:
  - id: "1491622581348864162"
    role: manager-room
    mode: free                    # "gated" (default) | "free"
    shell_policy: tier_2           # "none" | "tier_1" | "tier_2" | "tier_3"
    tools: all
    # ... rest unchanged
```

### 2. Tool Registry

The tool registry needs:
- New `ShellTool` registered
- Policy evaluation updated to check `shell_policy` tier
- In free mode, all tools registered without phase restriction
- In gated mode, shell tool not registered (or registered with tier_1 only)

### 3. Policy Engine (`internal/tool/policy.go`)

New policy evaluation:
```go
type ShellPolicy struct {
    Tier    string   // "none", "tier_1", "tier_2", "tier_3"
    Allowlist []string
    Blocklist []string
}

func EvaluateShellPolicy(policy ShellPolicy, command string) PolicyResult {
    // 1. Check hard blocklist — always blocks
    // 2. Check tier allowlist — pattern match
    // 3. Return allowed/blocked
}
```

### 4. Executor (`internal/tool/executor.go`)

Shell tool execution:
```go
func (e *Executor) executeShell(ctx context.Context, input map[string]any) (ToolResult, error) {
    command := input["command"].(string)
    
    // 1. Evaluate shell policy — tier check + blocklist
    // 2. If blocked, return error with reason
    // 3. Execute via os/exec with timeout
    // 4. Capture stdout, stderr, exit code
    // 5. Emit prizm.tool.shell.executed event
    // 6. Return result
}
```

### 5. Config Struct (`internal/orchestrator/config.go`)

New fields:
```go
type ChannelRole struct {
    // ... existing fields ...
    Mode         string `yaml:"mode"`          // "gated" | "free"
    ShellPolicy  string `yaml:"shell_policy"`  // "none" | "tier_1" | "tier_2" | "tier_3"
}

type ShellConfig struct {
    Allowlists map[string][]string `yaml:"allowlists"`
    Defaults   ShellDefaults       `yaml:"defaults"`
}

type ShellDefaults struct {
    TimeoutSeconds   int      `yaml:"timeout_seconds"`
    MaxOutputBytes    int      `yaml:"max_output_bytes"`
    BlockedPatterns   []string `yaml:"blocked_patterns"`
}
```

---

## Security Considerations

### Hard Blocklist (System-Level)
- Always enforced, cannot be overridden by config or tier
- Covers catastrophic operations (disk wipe, fork bomb, recursive delete of root/home)
- Checked before any tier-based allowlist evaluation

### Tier-Based Allowlists
- Pattern-matched against command string (glob-style: `go build*` matches `go build ./...`)
- Configurable per deployment via `prizm.yaml`
- Default tiers ship with sensible allowlists

### Path Containment
- Shell commands execute with `cwd` constrained to allowed project paths
- The existing `allowed_paths` config in `prizm.yaml` restricts file operations
- Shell tool should respect the same path boundaries

### Audit Trail
- Every shell command emitted as `prizm.tool.shell.executed` event
- Includes command, cwd, exit_code, duration, channel, user
- Dashboard can display shell activity (future)

### Rate Limiting
- Shell tool respects existing rate limiter
- Additional per-session shell call limit (configurable, default 20 calls/turn)

---

## Migration Path

### Phase 1: Shell Tool + Free Mode Foundation
- Add `ShellTool` to tool registry
- Add policy evaluation for shell commands
- Add `mode: free` to channel role config
- Wire free mode into `handleDiscordMessage`
- Tests for shell tool, policy tiers, free mode routing

### Phase 2: Channel Migration
- Set `mode: free` on #manager-room (Ema's primary interactive channel)
- Keep `mode: gated` on #build-room (agent factory)
- Keep #fun as `tools: none`
- Monitor: does Prizm Lumi respond correctly in free mode?

### Phase 3: Capability Expansion
- Add MCP tool support in free mode (unified tool access)
- Add subagent spawning in free mode (like OpenClaw's `sessions_spawn`)
- Add file editing tools without phase restriction
- Evaluate: can Prizm now replace OpenClaw for interactive work?

### Phase 4: OpenClaw Sunset (Future)
- When Prizm free mode reaches feature parity with OpenClaw
- Migrate remaining OpenClaw workflows to Prizm
- Decommission OpenClaw

---

## Open Questions

1. **Should free mode support the gated-loop workflow as an opt-in?** E.g., Ema types "run gated loop on BassBook" and Prizm launches the full workflow even in free mode? (My recommendation: yes — free mode can *invoke* gated mode, but gated mode can't invoke free mode.)

2. **How should we handle the shell tool in autonomous/scheduled work?** Tier 1 allowlist for build/test commands? Or keep autonomous mode fully gated with no shell? (My recommendation: tier 1 for autonomous — Prizm needs to run tests after writing code.)

3. **Should free mode have its own event namespace?** `prizm.free.*` vs `prizm.channel.*`? Or keep the existing event structure and just add a `mode` field to events? (My recommendation: add `mode` field to existing events — simpler, preserves dashboard compatibility.)

4. **What about the `listen_to_agents` bot interop?** OpenClaw Lumi talks to Prizm Lumi via Discord. In free mode, should Prizm Lumi be able to spawn subagents like OpenClaw does? (My recommendation: yes, but that's Phase 3 — not blocking Phase 1.)

5. **Should we add a `write_file` direct path in free mode** (skipping the proposal dance), or keep the existing tool but just disable the phase gate? (My recommendation: add a direct write path — the proposal system is phase-coupled and doesn't make sense without phases.)

---

## Decision Gates — RESOLVED

1. ✅ **Dual mode approach** — gated + free modes, both implemented
2. ✅ **Shell tool with tiered policy** — tiered access, config-driven
3. ✅ **Phase 1 scope** — shell + free mode
4. ✅ **Free mode channels** — manager-room + sys-manager (config-driven, master user only)
5. ✅ **Autonomous mode gets tier_1 shell** — build/test commands available in gated mode

### Additional Decisions (from Ema, 2026-07-14)

6. ✅ **Free mode is master-user only** — only Ema (config-defined `master_user`) can trigger free mode. Other users get gated mode regardless of channel. Config-driven:
```yaml
shell:
  master_user_id: "164169326142816256"  # Ema's Discord ID
```
7. ✅ **Autonomous work gets shell access** — tier_1 allowlist (build, test, git) available in gated mode so Prizm can run tests after writing code