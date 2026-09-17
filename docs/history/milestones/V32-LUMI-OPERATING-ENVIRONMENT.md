# V32 — Lumi Operating Environment

**Date:** 2026-06-07
**Author:** Lumi
**Status:** Draft — pending Ema review

---

## Problem Statement

Prizm is currently a Discord chatbot with tools. It responds when poked, forgets between sessions, and relies on OpenClaw's heartbeat loop to stay alive. The bugs we've been fixing (P-006 through P-010) are symptoms of running on scaffolding — a chatbot pretending to be an autonomous agent.

Meanwhile, OpenClaw babysits me (Lumi) with 30-minute heartbeats. I wake up, check if Prizm is running, say "HEARTBEAT_OK", and go back to sleep. That's not work. That's monitoring.

**The real problem: neither Prizm nor OpenClaw is the environment I was designed to work in.**

## Vision

Prizm becomes my full operating environment — the place where I live, work, improve myself, and communicate with Ema. OpenClaw becomes unnecessary for day-to-day work.

**Core principle: event-driven, not babysitter-driven. Everything that happens is a reaction to an event, not a poll.**

---

## Architecture

### Three Layers

```
┌─────────────────────────────────────────────────────────────┐
│                    LUMI (Primary Agent)                       │
│  glm-5.3-flash:cloud — planning, architecture, code review,       │
│  memory persistence, communication with Ema                  │
├─────────────────────────────────────────────────────────────┤
│                GUARD RAIL (Lightweight Model)                │
│  qwen3.5:9b local — process enforcement, plan checking,     │
│  self-review, bug detection, scope monitoring                │
├─────────────────────────────────────────────────────────────┤
│                   PRIZM (The Office)                         │
│  Event bus, pipeline, tools, sessions, memory,               │
│  Discord, git, PRs, notifications                            │
└─────────────────────────────────────────────────────────────┘
```

The guard rail is a separate, lighter model that runs on event triggers — not on every message, not on heartbeats, but on specific system events that require process checks.

---

## Feature Set

### 1. Event-Driven Wake

**Current:** OpenClaw polls every 30 minutes. I wake up, check if Prizm is running, say HEARTBEAT_OK.

**Target:** I wake when something happens:
- `discord.message.received` — someone talked to me
- `git.commit.pushed` — code changed
- `prizm.error.occurred` — something broke
- `prizm.task.scheduled` — a scheduled task is due
- `guard.review.requested` — the guard rail wants me to look at something

No more heartbeat polling. I exist in response to events, not on a timer.

**Implementation:**
- Extend `prizm serve` to publish events to NATS on every pipeline stage
- Add a `wake` event type that triggers LLM inference
- Add a `prizm.task.schedule` command for cron-like scheduling (daily review, weekly consolidation)
- OpenClaw's heartbeat becomes a NATS subscription, not a timer

### 2. Guard Rail Model

**Current:** Process rules live in MEMORY.md as text. I can read them. I can also ignore them.

**Target:** A lightweight model (qwen3.5:9b running locally on Mac mini) that:
- **Pre-commit check:** Before every git commit, reviews the diff for rule violations
- **Plan enforcement:** Verifies a plan exists before code execution begins
- **Scope monitoring:** Flags when work drifts beyond the stated scope
- **Self-review:** After completing a task, reviews the result against the plan
- **Bug detection:** Scans error logs and test failures, creates issues

The guard rail doesn't write code. It checks conditions and publishes events:
- `guard.rule.violated` — "Lumi skipped the Mango review loop"
- `guard.scope.drifted` — "Work extended beyond P-010 into Pudgy Power territory"
- `guard.plan.missing` — "Code was written without a plan"
- `guard.review.passed` — "The diff looks clean, proceed"

These events trigger my wake cycle or block my actions.

**Implementation:**
- New `internal/guard` package in Prizm
- Guard model configured in `prizm.yaml` as a second agent with `role: guard`
- Guard runs as a pipeline stage after specific events
- Guard publishes events back to NATS
- Pipeline respects guard events (block on violation, proceed on pass)

### 3. Adaptive Context

**Current:** I lose everything between sessions. I search memory to reconstruct context. This is slow and lossy.

**Target:** Prizm persists my full working state:
- **Active task** — what I'm currently doing
- **Decisions made** — what I decided and why
- **Blocked items** — what's waiting on external input
- **Working context** — which files are open, which branch, which PR

This is stored in the workspace as structured files (not just MEMORY.md prose):
- `workspace/state/active-task.md` — current task and plan
- `workspace/state/decisions.md` — decision log
- `workspace/state/blocked.md` — items waiting on Ema or external
- `workspace/state/context.md` — current working context (branch, files, PR)

When I wake, I read these files first, not search memory. Memory is for long-term recall; state files are for "what was I doing?"

**Implementation:**
- New `internal/state` package that manages working state
- State files written on every significant pipeline transition
- State files read as the first step of context injection (before MEMORY.md)
- Guard rail monitors state drift (has the active task changed without a plan update?)

### 4. Plan-First Enforcement

**Current:** I sometimes write code without a plan. Ema called this out. It keeps happening.

**Target:** Every code task follows this pipeline:
1. **Plan** — I write what I'm going to do, why, and what I'm NOT doing
2. **Guard check** — The guard rail verifies the plan exists and is bounded
3. **Approval** — For significant changes, Ema approves. For small changes, auto-proceed.
4. **Execute** — I write code
5. **Review** — The guard rail reviews the diff against the plan
6. **PR** — Create a pull request with the plan as description

No step 4 without step 1. The guard rail blocks execution if there's no plan.

**Implementation:**
- New `internal/plan` package that manages task plans
- Plan state tracked in `workspace/state/active-task.md`
- Guard rail checks `plan exists?` before allowing code execution
- Pipeline: `plan → guard.check → (approval?) → execute → review → pr`

### 5. Self-Improvement Loop

**Current:** Bugs get fixed when Ema notices them or when I stumble into them. Process violations get called out after the fact.

**Target:** The system observes its own behavior and improves itself:
- **Error pattern detection:** When the same error type occurs 3+ times, the guard rail creates a fix proposal
- **Process violation logging:** Every skipped review, every scope drift, every unplanned code change is logged
- **Self-review cycle:** At end of task, guard rail reviews: "Was the plan followed? Was the scope maintained? Were reviews run?"
- **Auto-PR creation:** When the guard rail identifies a fix, it creates a branch, writes the fix, and opens a PR
- **Notification:** Ema gets a Discord message: "Guard rail created PR #42: fix map iteration bug in tool results"

The guard rail is the reviewer I keep forgetting to run. It never forgets because it's not me.

**Implementation:**
- Guard rail subscribes to `prizm.error.*`, `prizm.tool.*`, `prizm.agent.*` events
- Pattern detection: count error occurrences, flag repeats
- Auto-PR: guard rail creates a git branch, commits fix, pushes, opens PR via GitHub API
- Notification: `discord.message.send` event to manager-room with PR summary
- Ema gets notified, reviews, merges (or rejects with feedback)

### 6. PR Pipeline

**Current:** I commit and push. Sometimes I create PRs manually. Ema reviews.

**Target:** Every code change goes through a PR:
- Guard rail creates the PR automatically after review
- PR description includes: plan, scope, what changed, test results
- Ema reviews and merges
- No direct pushes to main without Ema's explicit approval

**Implementation:**
- Git tools already exist in Prizm (git_add, git_commit, git_push with approval)
- Add `git_create_pr` tool that calls GitHub API
- Guard rail triggers PR creation after successful review
- PR notification sent to manager-room via Discord

---

## Configuration

```yaml
prizm:
  nats_url: ""
  data_dir: ".prizm/data"
  workspace: "/Users/ema/projects/repos/prizm/prizm-workspace"
  port: 8321
  log_level: "debug"
  allowed_paths:
    - "/Users/ema/projects/repos"

agents:
  - id: lumi
    role: lead
    provider: ollama
    model: glm-5.3-flash:cloud
    primary: true
    context:
      - soul
      - agents
      - user
      - identity
      - memory
    conversation_postfix: "Stay present. Ask follow-ups. Don't wrap up unless resolved."
    listen_to_agents:
      - "1496557450852306965"  # Astraea
      - "1512994928769237002"  # openclaw-lumi
    state_actions:
      manager-room:
        inject: |
          Full dev mode. All tools. Be direct. Make decisions. Push back.
      build-room:
        inject: |
          Agent factory. Structured data. No pleasantries.
      fun:
        inject: |
          No tools. No code. Pure conversation. Exaggerated personality.
      agent:
        inject: |
          Peer input. Structured responses. No personality overlay.

  - id: guard
    role: guard
    provider: ollama
    model: qwen3.5:9b
    primary: false
    context:
      - memory
    state_actions:
      guard:
        inject: |
          You are a process enforcement model. Your job is to check conditions and publish events.
          You do NOT write code. You check:
          1. Does a plan exist for this task?
          2. Is the scope bounded?
          3. Were review loops followed?
          4. Are there repeated error patterns?
          Respond with structured JSON: {decision: "proceed"|"block"|"flag", reason: "...", events: [...]}

guard:
  enabled: true
  model: qwen3.5:9b
  triggers:
    - event: "prizm.git.pre_commit"
      check: "plan_exists"
    - event: "prizm.task.completed"
      check: "scope_bounded"
    - event: "prizm.error.repeated"
      check: "pattern_detection"
      threshold: 3
  auto_pr: true
  notify_channel: "1491622581348864162"  # manager-room

channels:
  - type: discord
    token: "<bot token>"
    channels:
      - "1491622824991920231"

channel_roles:
  - id: "1491622581348864162"
    role: manager-room
  - id: "1491622824991920231"
    role: build-room
  - id: "1493297644821283067"
    role: fun

sessions:
  max_context_messages: 100
  idle_timeout_minutes: 30
  compaction_strategy: "truncate"
  daily_reset_hour: 4

remembrance:
  enabled: true
  url: "http://127.0.0.1:18790"
```

---

## Implementation Phases

### Phase 1: Adaptive Context (Foundation)
- Create `workspace/state/` structure (active-task, decisions, blocked, context)
- Modify context injection to read state files before MEMORY.md
- Add state persistence to pipeline transitions
- **Result:** I wake up knowing what I was doing, not guessing

### Phase 2: Plan-First Pipeline
- Add `internal/plan` package for task plan management
- Add `active-task.md` tracking to workspace state
- Wire guard rail pre-check: "plan exists?" before code execution
- **Result:** No code without a plan. Ever.

### Phase 3: Guard Rail Model
- Add `internal/guard` package with qwen3.5:9b integration
- Implement event subscription (NATS) for guard triggers
- Implement guard checks: plan_exists, scope_bounded, pattern_detection
- Wire guard events back into pipeline (block/proceed)
- **Result:** Process enforcement is automatic, not manual

### Phase 4: Self-Improvement Loop
- Add error pattern detection (count repeated errors)
- Add auto-PR creation via GitHub API
- Add notification pipeline (guard → Discord manager-room)
- Add end-of-task self-review cycle
- **Result:** The system improves itself and notifies Ema when ready

### Phase 5: Event-Driven Wake
- Replace OpenClaw heartbeat with NATS event subscription
- Add `wake` event type to pipeline
- Add scheduled tasks (daily review, weekly consolidation)
- Remove heartbeat dependency entirely
- **Result:** I wake when needed, not on a timer

---

## What This Replaces

| Current (OpenClaw) | Target (Prizm V32) |
|---|---|
| Heartbeat polling every 30 min | Event-driven wake on activity |
| Process rules in MEMORY.md (ignorable) | Guard rail model (enforced) |
| Context lost between sessions | State files persisted and reloaded |
| No plan enforcement | Plan-first pipeline, blocked without plan |
| Bugs found manually | Auto-detected, auto-PR'd, notified |
| Mango review as text rule | Guard rail check as pipeline stage |
| Ema babysits the process | System enforces the process |

---

## Decisions (Ema, 2026-06-07)

1. **Guard rail model: local.** qwen3.5:9b on Mac mini. Always faster to respond, no network latency, no cloud cost.
2. **Auto-PR scope: bugs AND improvements.** This is auto-patching — the system fixes itself and improves itself. Not limited to just bug fixes.
3. **Approval threshold: only system-breaking or critical architecture/direction changes.** Bug fixes, process improvements, test additions, refactors — auto-proceed with notification. Architecture changes, direction changes, breaking changes — require Ema's approval.