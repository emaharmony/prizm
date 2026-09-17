# Prizm — The Complete Vision (v2)

**Date:** 2026-05-21
**Authors:** Ema + Lumi
**Status:** North Star — aligned on High Level → Architecture → Human Experience → Scope → Business → Timeline

---

## HIGH LEVEL — What Is Prizm?

Prizm is an **event-native agentic environment**. It tracks events end-to-end through the system's usage, similar to webhooks. Events trigger registered actions — call adapter logic, start memory processes, update trackers. It replaces OpenClaw and any other agentic system entirely.

The goal: **smooth, clean, end-to-end flow. No task dropped. No context lost.**

**What Prizm IS:**
- An event-driven orchestrator that spawns agents, routes tasks, and tracks everything
- A system where you give a task and it runs through to completion autonomously
- A platform where multiple agents work in parallel on different roles
- A system where Remembrance provides long-term, seamless context across all chats
- A customizable environment — set up two Prizms and they can communicate, send each other tasks and updates
- Something useful for business, personal assistance, even home IoT control

**What Prizm is NOT:**
- Not a "context forever" system (context has lifecycle)
- Not just a coding agent
- Not a standalone LLM

---

## ARCHITECTURE — How Does It Work?

### Separate Services Communicating Through Event Bus

Prizm is NOT a monolith. It's a collection of separate services that communicate through the NATS event bus:

```
┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐
│ Orchestrator │  │   Lumi      │  │   Mango     │  │ Remembrance  │
│  (Go)       │  │  Agent (Go)  │  │ Agent (Go)  │  │  (Python)   │
│             │  │              │  │             │  │             │
│ - Route     │  │ - Reason     │  │ - Code      │  │ - Persist   │
│ - Delegate  │  │ - Delegate   │  │ - Test      │  │ - Recall    │
│ - Schedule  │  │ - Report     │  │ - Report    │  │ - Extract   │
└──────┬──────┘  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘
       │                │                │                │
       └────────────────┴────────────────┴────────────────┘
                                  │
                    ┌─────────────▼─────────────┐
                    │     Event Bus (NATS)       │
                    │                           │
                    │  lumi.agent.started       │
                    │  mango.tool.completed     │
                    │  remembrance.memory.stored │
                    │  prizm.cost.tracked       │
                    │  prizm.channel.received   │
                    └─────────────┬─────────────┘
                                  │
       ┌──────────────┬───────────┴───────────┬──────────────┐
       ▼              ▼                       ▼              ▼
┌─────────────┐ ┌─────────────┐  ┌─────────────────┐ ┌─────────────┐
│   Discord   │ │  Tracker    │  │   Dashboard     │ │   Cron      │
│  Adapter    │ │ (Projections)│  │   (Web UI)     │ │  Scheduler  │
└─────────────┘ └─────────────┘  └─────────────────┘ └─────────────┘
```

### Per-Agent Namespaces (Dynamic)

Each agent publishes events under its own namespace. The namespace is **determined by the agent's ID in its configuration**, not hardcoded. The only hardcoded namespace is `prizm.*` for system-level events.

**No hardcoded agent names.** If no agent ID is provided in the config, the system auto-generates: `prizm1`, `prizm2`, `prizm3`, etc.

Agent definitions are in the config:

```yaml
agents:
  - id: lumi                # This becomes the event namespace
    role: lead               # Role: lead, coder, researcher, etc.
    model: glm-5.3-flash:cloud
    context: soul,agents     # V19 context injection

  - id: mango
    role: coder
    model: deepseek-v4-pro:cloud
    context: agents

  # No ID provided — auto-generated as prizm1
  - role: researcher
    model: gpt-4o
```

The `id` field becomes the event namespace prefix. If omitted, auto-generated as `prizm<N>`:

```
<agent-id>.agent.started          → Agent begins reasoning
<agent-id>.agent.output           → Agent produces output
<agent-id>.agent.completed       → Agent finishes
<agent-id>.agent.failed            → Agent encounters error
<agent-id>.llm.requested          → Agent calls an LLM
<agent-id>.llm.completed          → LLM call completes
<agent-id>.tool.requested         → Agent requests a tool
<agent-id>.tool.completed         → Tool call completes
<agent-id>.context.injected       → Context injected into agent
<agent-id>.channel.sent           → Agent sends message to user
```

Examples for different setups:

```
# Ema's setup (custom IDs)
lumi.agent.started          → Lumi begins
mango.tool.completed        → Mango finishes a tool call

# Setup with no custom IDs (auto-generated)
prizm1.agent.started        → First agent begins
prizm2.tool.completed      → Second agent finishes a tool call

# Business setup
support-bot.agent.output   → Support bot responds
sales-agent.agent.started → Sales agent begins
```

This means agents are first-class citizens on the bus, not anonymous workers. The namespace is fully dynamic — configured by the user, not hardcoded by the system.

### Languages: Go + Python

- **Go:** Orchestrator, event bus, adapters, agent runtime, policy engine, cost tracking
- **Python:** Remembrance (memory pipeline), future agents that need Python LLM libraries
- **Communication:** Both Go and Python services publish/subscribe to NATS events

### Memory: Embedded Remembrance

Remembrance is embedded into Prizm, not a separate service you run independently. It hooks directly into the event bus:
- Every `lumi.agent.output` → Remembrance gates and extracts
- Every `mango.agent.output` → Remembrance gates and extracts
- Context is persistent, seamless, long-term across all chats
- No loss of context, no repeat work, no loss of direction

### LLM Calls: Prizm Owns Them

Prizm calls Ollama/OpenAI/Anthropic directly. No delegation to a separate LLM service. The model routing, chaining, and fallback are Prizm's job.

---

## HUMAN EXPERIENCE — What Does It Feel Like?

### The Flow

1. **You give Prizm a task** (e.g., "Build the auth system for BassBook")
2. **Orchestrator breaks it down** into subtasks, assigns roles
3. **Agents work in parallel** — Lumi plans, Mango codes, a researcher gathers specs
4. **Events flow through the bus** — every action, decision, tool call, cost tracked
5. **Registered actions trigger automatically** — memory is saved after every run, tracker updates, notifications sent
6. **You can talk to any agent** if you want, or let the orchestrator handle it
7. **Context persists** — Remembrance saves key context after every run, seamlessly across all chats
8. **No task dropped** — the orchestrator tracks every task end-to-end

### Key Feelings

- **Autonomous:** You give a task, the system runs it through. You don't babysit.
- **Clean:** No model running commands on top of doing the task. The system handles persistence, memory, tracking.
- **Customizable:** Set up two Prizm environments, they communicate and send each other tasks.
- **Observable:** Every event is tracked. Reports, traces, costs — all visible.
- **Persistent:** Context is seamless. No loss between sessions. Remembrance handles long-term memory.

### When Things Go Wrong

- **Reports, not panic.** A clear report of what happened, what failed, and what to do next.
- **No overcomplication.** Error handling should be simple and transparent.
- **Undo where possible.** Approval gates prevent irreversible actions.

---

## SCOPE — What's In and What's Out

### IN Scope
- Event bus with per-agent namespaces
- Orchestrator that routes, delegates, and tracks tasks end-to-end
- Agent spawning with full bus integration (per-agent events)
- Registered actions triggered by events (webhook-style)
- Remembrance embedded for long-term, seamless context
- Multi-agent parallel work with role assignment
- Real-time chat with all agents (Discord, Telegram, webchat)
- Customizable environments that can communicate with each other
- Cost tracking, policy engine, approval gates (already built)
- Dashboard for monitoring and control

### OUT of Scope
- Context forever system (context has lifecycle — Remembrance manages it)
- Just a coding agent (Prizm is broader — any task, any role)
- Standalone LLM (Prizm orchestrates LLMs, it doesn't just call them)

### Minimum Viable "Replace OpenClaw"
1. **Discord adapter** (inbound + outbound) — you can talk to Prizm on Discord
2. **Session continuity** — Prizm remembers your conversation
3. **Agent routing** — messages go to the right agent
4. **Task end-to-end** — give a task, it runs through to completion

---

## BUSINESS — Who Is This For?

- **Designed for outside use**, not just for Ema
- **Restrictive license for now** — will open up closer to release
- **Third parties can extend Prizm** — write adapters, add agents, create plugins
- **Useful for:** business automation, personal assistance, IoT control, development teams, solo developers
- **Not just a dev tool** — the event-driven architecture makes it useful anywhere actions need to be triggered by events

---

## TIMELINE — When Does This Need To Be Real?

- **ASAP.** Ema is feeling the pain with OpenClaw and wants to transition.
- **Priority:** Make it usable for the current workflow — whatever that looks like.
- **The one thing that would make the biggest difference right now:** Making Prizm usable in the current workflow. The exact form is TBD — but the pain is real and the urgency is high.

---

## What Already Exists (V1–V19)

| Component | Status | Notes |
|-----------|--------|-------|
| Event Bus (NATS) | ✅ Built | 60+ event types, causal DAG |
| LLM Providers | ✅ Built | Ollama, OpenAI, Anthropic, Gemini, chaining |
| Tool Execution | ✅ Built | Read/write/shell with policy gates |
| Approval Gates | ✅ Built | Human-in-the-loop for mutations |
| Policy Engine | ✅ Built | YAML-based allow/deny/approve |
| Cost Tracking | ✅ Built | Per-run, per-model, per-agent |
| Context Injection | ✅ Built | Workspace personality/rules/docs |
| OpenClaw Config Transfer | ✅ Built | Providers, models, API keys |
| Adapters (Discord, Echo, Remembrance) | ✅ Built | V9 adapter contract |
| Multi-Agent | ✅ Built | V13 agent registry and lifecycle |
| Workflow Engine | ✅ Built | V7 multi-step with conditions |
| HNSW Vector Search | ✅ Built | V17 semantic queries |

## What's Still Needed

| Component | Priority | Notes |
|-----------|----------|-------|
| **Orchestrator** (`prizm serve`) | 🔴 P0 | Persistent daemon, heartbeat, agent lifecycle |
| **Discord Adapter (inbound)** | 🔴 P0 | Receive messages → event bus |
| **Discord Adapter (outbound)** | 🔴 P0 | Event bus → send messages back |
| **Session Manager** | 🔴 P0 | Conversation continuity, compaction |
| **Agent Router** | 🔴 P0 | Route messages to right agent |
| **Registered Actions** | 🔴 P0 | Events trigger actions (webhook-style) |
| **Per-Agent Event Namespaces** | 🔴 P0 | Dynamic `<agent-id>.*` based on config, not hardcoded |
| **Streaming Responses** | 🟡 P1 | Token-by-token delivery to chat |
| **Remembrance Embedding** | 🟡 P1 | Memory as a Prizm service, not external |
| **Cron/Scheduling** | 🟡 P1 | Recurring tasks, wake events |
| **Migration Tool** | 🟡 P1 | `prizm migrate --from-openclaw` |
| **Multi-Prizm Communication** | 🟢 P2 | Two Prizm environments sending tasks to each other |
| **Dashboard** | 🟢 P2 | Real-time event stream, cost tracking, agent status |

---

## Key Design Principles

1. **Events are per-agent and dynamic.** `<agent-id>.llm.called`, not hardcoded. The agent ID comes from config — "lumi" and "mango" are examples. If no ID is given, auto-generate as `prizm1`, `prizm2`, etc. The only hardcoded namespace is `prizm.*` for system events.
2. **Registered actions, not model commands.** Events trigger actions automatically. The model doesn't run save-memory commands — the system does it.
3. **Separate services, one bus.** Orchestrator, agents, Remembrance, adapters — all communicate through NATS.
4. **Go + Python.** Go for performance and concurrency, Python for LLM ecosystem and Remembrance.
5. **End-to-end flow.** No task dropped. The orchestrator tracks everything from start to finish.
6. **Customizable environments.** Two Prizms can talk to each other. Not siloed.
7. **Designed for everyone.** Not just Ema. Business, personal, IoT — the architecture supports it.
8. **Restrictive for now, open later.** License will open up as the product matures.

---

*"One beam of light. One event. A spectrum of reactions."*

## See Also

- [Roadmap](../history/ROADMAP.md)
- [Architecture](../architecture/ARCHITECTURE.md)
- [Documentation Hub](../README.md)
