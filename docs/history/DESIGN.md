# Prizm — System Design

**Last Updated:** 2026-05-21
**Based On:** PRIZM-VISION.md v2 (Ema + Lumi aligned)

---

## System Overview

Prizm is an event-native agentic environment. Separate services communicate through a NATS event bus. Events trigger registered actions (webhook-style). Agents are first-class bus citizens with their own event namespaces. The orchestrator routes, delegates, and tracks tasks end-to-end.

---

## Service Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Prizm Runtime                                │
│                                                                   │
│  ┌───────────────────────────────────────────────────────────────┐ │
│  │                    Orchestrator (Go)                          │ │
│  │                                                              │ │
│  │  ┌─────────────┐  ┌──────────────┐  ┌──────────────────┐   │ │
│  │  │  Session    │  │   Agent      │  │   Task           │   │ │
│  │  │  Manager    │  │   Router     │  │   Tracker         │   │ │
│  │  └──────┬──────┘  └──────┬───────┘  └────────┬─────────┘   │ │
│  │         │                │                    │              │ │
│  └─────────┼────────────────┼────────────────────┼─────────────┘ │
│            │                │                    │               │
│  ┌─────────▼────────────────▼────────────────────▼─────────────┐ │
│  │                    Event Bus (NATS JetStream)                │ │
│  │                                                             │ │
│  │  Namespaces:                                                │ │
│  │    lumi.*          — Lumi agent events                     │ │
│  │    mango.*         — Mango agent events                    │ │
│  │    remembrance.*   — Memory events                         │ │
│  │    prizm.*         — System events (cost, policy, etc.)    │ │
│  │    adapter.discord.* — Discord adapter events              │ │
│  │    adapter.telegram.* — Telegram adapter events            │ │
│  │    cron.*          — Scheduled task events                  │ │
│  │                                                             │ │
│  │  Registered Actions:                                        │ │
│  │    lumi.agent.output     → remembrance.gate.extract        │ │
│  │    mango.agent.output    → remembrance.gate.extract        │ │
│  │    *.tool.completed      → prizm.cost.track                │ │
│  │    *.approval.requested  → adapter.discord.notify          │ │
│  │    cron.triggered        → orchestrator.spawn_agent         │ │
│  │                                                             │ │
│  └─────────────────────────┬───────────────────────────────────┘ │
│                            │                                     │
│  ┌─────────────┬───────────┴───────────┬──────────────────┐    │
│  ▼             ▼                       ▼                  ▼    │
│  ┌─────────┐  ┌──────────┐  ┌──────────────┐  ┌─────────────┐ │
│  │ Lumi    │  │  Mango   │  │ Remembrance  │  │   Adapters   │ │
│  │ Agent   │  │  Agent   │  │  (Python)    │  │  (Discord,   │ │
│  │         │  │          │  │              │  │  Telegram,   │ │
│  │ - Plan  │  │  - Code  │  │  - Gate     │  │  Webchat,     │ │
│  │ - Route  │  │  - Test │  │  - Extract  │  │  HTTP)       │ │
│  │ - Review │  │  - Report│  │  - Persist  │  │              │ │
│  │ - Report │  │          │  │  - Recall   │  │              │ │
│  └────┬─────┘  └────┬─────┘  └──────┬──────┘  └──────┬──────┘ │
│       │              │               │                │        │
│  ┌────▼──────────────▼───────────────▼────────────────▼────┐   │
│  │                    Core Services (Go)                   │   │
│  │                                                        │   │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ │   │
│  │  │  Policy  │ │  Cost    │ │ Context  │ │ Approval │ │   │
│  │  │  Engine  │ │ Tracking │ │ Injection│ │  Gates   │ │   │
│  │  └──────────┘ └──────────┘ └──────────┘ └──────────┘ │   │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ │   │
│  │  │   Tool   │ │ Workflow │ │ Projections│ │  Vector  │ │   │
│  │  │ Executor │ │  Engine  │ │           │ │  Search  │ │   │
│  │  └──────────┘ └──────────┘ └──────────┘ └──────────┘ │   │
│  └────────────────────────────────────────────────────────┘   │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐   │
│  │                    Storage Layer                        │   │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────────────────┐  │   │
│  │  │  SQLite  │ │ events   │ │ Remembrance DB       │  │   │
│  │  │ (events, │ │ .jsonl   │ │ (entities, facts,    │  │   │
│  │  │  runs,   │ │ (append- │ │  aliases, vectors)    │  │   │
│  │  │  sessions│ │  only    │ │                      │  │   │
│  │  │  costs)  │ │  log)    │ │                      │  │   │
│  │  └──────────┘ └──────────┘ └──────────────────────┘  │   │
│  └────────────────────────────────────────────────────────┘   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## Event Namespace Design

### Per-Agent Namespaces (Dynamic)

Each agent publishes events under its own namespace. The namespace is **determined by the agent's ID in its configuration**, not hardcoded. The only hardcoded namespace is `prizm.*` for system-level events. If no agent ID is provided, auto-generate as `prizm1`, `prizm2`, etc.

Agent definitions are in the config:

```yaml
agents:
  - id: lumi                # Custom ID → lumi.* namespace
    role: lead
    model: glm-5.3-flash:cloud
    context: soul,agents

  - id: mango
    role: coder
    model: deepseek-v4-pro:cloud
    context: agents

  # No ID → auto-generated as prizm1
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

Service namespaces (Remembrance, adapters) use their own prefixes:

```
remembrance.memory.stored    → Context saved
remembrance.memory.recalled  → Context retrieved
remembrance.gate.extract    → Memory extraction triggered
```

### System Namespaces (shared)

```
prizm.task.created           → Task created
prizm.task.completed         → Task finished
prizm.cost.tracked           → Token cost recorded
prizm.cost.reported          → Cost report generated
prizm.policy.evaluated        → Policy decision made
prizm.approval.requested     → Approval needed
prizm.channel.received       → Message from Discord/Telegram
prizm.channel.sent           → Message sent to Discord/Telegram
prizm.session.created        → New session started
prizm.session.ended          → Session closed
prizm.cron.triggered         → Scheduled task fired
```

### Registered Actions

Events can trigger actions automatically — this is the webhook-style behavior:

```yaml
# Example registered actions
- trigger: lumi.agent.output
  action: remembrance.gate.extract    # Save Lumi's output to memory

- trigger: mango.agent.output
  action: remembrance.gate.extract    # Save Mango's output to memory

- trigger: "*.tool.completed"
  action: prizm.cost.track           # Track token costs

- trigger: "*.approval.requested"
  action: adapter.discord.notify     # Send approval request to Discord

- trigger: cron.triggered
  action: orchestrator.spawn_agent    # Start an agent for scheduled task
```

---

## Session Manager Design

Sessions track conversations across messages, channels, and time.

```
Session {
    id:           string        // ULID, unique per session
    agent:        string        // Which agent (lumi, mango, etc.)
    channel:      string        // discord, telegram, webchat
    channel_id:   string        // Discord channel ID, etc.
    user_id:      string        // Who's talking
    started_at:   timestamp     // When this session started
    last_active:  timestamp     // Last user message
    context:      []string      // Recent message IDs (for compaction)
    remembrance:  string        // Remembrance context ID
}
```

### Session Lifecycle

1. **Created:** First message from a user creates a session
2. **Active:** Messages flow in and out, context grows
3. **Compaction:** When context exceeds budget, Remembrance summarizes older messages
4. **Idle reset:** After N minutes of inactivity, session resets (configurable)
5. **Daily reset:** At 4am local time, new session starts fresh
6. **Ended:** User types `/new` or session expires

### Session → Remembrance Bridge

Every `lumi.agent.output` and `mango.agent.output` event triggers Remembrance's gate pipeline:
1. **Gate** — Is this worth remembering? (DistilBERT)
2. **Extract** — What entities, facts, decisions are in this output? (Nemotron)
3. **Persist** — Store in Remembrance DB with embeddings
4. **Recall** — Next session, Remembrance provides relevant context automatically

---

## Orchestrator Design

The orchestrator is the brain of Prizm. It runs as a persistent Go service (`prizm serve`).

### Responsibilities

1. **Route incoming messages** to the right agent
2. **Track sessions** — who's talking, which agent, conversation state
3. **Manage agent lifecycle** — spawn, monitor, kill agents
4. **Track tasks** — every task from start to finish, no dropped tasks
5. **Trigger registered actions** — events → actions automatically
6. **Schedule cron tasks** — recurring tasks, wake events
7. **Enforce policies** — allow/deny/approve actions
8. **Track costs** — token usage, estimated spend

### API

```bash
# Start the orchestrator
prizm serve --from-config --workspace ~/.openclaw/workspace

# Check status
prizm status

# Manage sessions
prizm session list
prizm session show <id>

# Manage agents
prizm agent list
prizm agent spawn mango --task "Fix the auth bug"
prizm agent kill <id>

# Manage tasks
prizm task list
prizm task show <id>

# Manage registered actions
prizm action list
prizm action register --trigger "lumi.agent.output" --action "remembrance.gate.extract"
```

---

## Agent Router Design

The router decides which agent handles each message.

### Routing Logic

1. **Direct address:** "Lumi, fix this" → route to Lumi
2. **@mention:** "@Mango write the tests" → route to Mango
3. **Intent detection:** "Write code for..." → route to Mango
4. **Default:** Route to the primary agent (Lumi)
5. **Delegation:** Lumi delegates to Mango, Mango reports back through events

### Delegation Flow

```
You → "Lumi, fix the auth bug"
  → Lumi reasons: "I need to look at the code, write a fix, and run tests"
  → Lumi delegates to Mango: mango.task.created {parent: lumi.agent.output}
    → Mango codes
    → Mango reports: mango.agent.completed {parent: mango.task.created}
  → Lumi reviews Mango's output
  → Lumi reports to you: lumi.channel.sent
```

---

## Remembrance Integration

Remembrance hooks directly into the event bus as a Prizm service.

### Event Flow

```
lumi.agent.output
  → remembrance.gate.extract (DistilBERT: worth remembering?)
    → remembrance.extract (Nemotron: extract entities/facts)
      → remembrance.persist (save to DB with embeddings)

Next session:
  → lumi.agent.started
    → remembrance.recall (retrieve relevant context)
      → lumi.context.injected (context from memory)
```

### Key Properties

- **Zero-cost local** — all LLM calls use Ollama, no API keys required
- **Universal** — not Prizm-specific, works with any event namespace
- **Seamless** — no model commands needed, registered actions handle it
- **Long-term** — context persists across sessions, no loss of direction
- **Lifecycle-aware** — context has a lifecycle (gate → extract → persist → recall → decay)

---

## Storage Architecture

| Store | What | Engine | Size |
|-------|------|--------|------|
| Events | All events (append-only) | NATS JetStream + SQLite | Grows with usage |
| Sessions | Active conversations | SQLite | MB |
| Remembrance | Entities, facts, vectors | SQLite + HNSW | GB |
| Runs | Task artifacts | Filesystem | Per-run |
| Costs | Token usage | SQLite | MB |
| Policies | Allow/deny/approve rules | YAML files | KB |

---

## Deployment

### Single Machine (Dev/Personal)

```bash
# One command to start everything
prizm serve --from-config --workspace ~/.openclaw/workspace
```

All services (orchestrator, agents, Remembrance, adapters) run in one process with goroutines. NATS is embedded.

### Multi-Instance (Production)

```bash
# Orchestrator
prizm serve --role orchestrator --nats nats://nats-cluster:4222

# Agent node
prizm serve --role agent --nats nats://nats-cluster:4222

# Remembrance node
prizm serve --role remembrance --nats nats://nats-cluster:4222
```

External NATS cluster, services scale independently.

---

## Compatibility with V1–V19

All existing `prizm.*` events continue to work. Per-agent namespaces are dynamic — configured by the user in their agent definitions. The namespace prefix comes from the agent's `id` field, not hardcoded. The orchestrator translates between them:

- `lumi.agent.output` with `correlation_id: X` is also published as `prizm.agent.output` with the same correlation ID
- This means existing V1–V19 code (cost tracking, policy, projections) continues to work without modification

## See Also

- [Architecture](../architecture/ARCHITECTURE.md)
- [Version History](VERSION_HISTORY.md)
- [Documentation Hub](README.md)
