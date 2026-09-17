# V22 Multi-Agent Orchestration — Design Document

**Status:** ✅ Complete (merged to main, PR #34)
**Date:** 2026-05-22

## Goal
Lumi delegates to Mango through the event bus. Parallel agents work on separate tasks. Approval works in Discord.

**Exit Criteria:** Lumi can delegate to Mango through the event bus. Parallel agents work on separate tasks. Approval works in Discord.

## Milestones

| # | Milestone | Description | Depends |
|---|-----------|-------------|---------|
| M3.1 | Agent Delegation | Lumi → `mango.task.created` → Mango → `mango.task.completed` | V21 |
| M3.2 | Parallel Agents | Multiple agents work on different tasks simultaneously | M3.1 |
| M3.3 | Role Assignment | Orchestrator, Developer, Researcher role definitions | M3.1 |
| M3.4 | Task Tracking | Every task tracked end-to-end, no dropped tasks | M3.1, M1.7 |
| M3.5 | Approval Gates | "Approve this push?" → "yes" → push | M3.1 |
| M3.6 | E2E Delegation Test | Lumi delegates to Mango, Mango codes, Lumi reviews, PR created | M3.1–M3.4 |

## Architecture

### M3.1: Agent Delegation — Core Primitive

**Flow:**
```
User message arrives
  → Lumi (primary agent) processes
  → Lumi decides: "Mango should code this"
  → Lumi publishes mango.task.created event on NATS
  → Mango (second Prizm agent) subscribes to mango.task.created
  → Mango processes the task
  → Mango publishes mango.task.completed event
  → Lumi receives result via mango.task.completed subscription
  → Lumi reviews result → responds to user
```

### Delegation via Action Registry

The existing `internal/action` package already supports event-triggered actions. We extend it:

```yaml
# prizm.yaml
agents:
  - id: lumi
    role: lead
    provider: ollama
    model: glm-5.3-flash:cloud
    primary: true
    subscriptions:
      - "mango.task.completed"  # Lumi receives Mango's results

  - id: mango
    role: coder
    provider: ollama
    model: deepseek-v4-pro:cloud
    subscriptions:
      - "mango.task.created"    # Mango receives delegated tasks
```

### New Components

1. **`internal/delegation`** — Delegation engine
   - `Delegate(agentID, task, context) → taskID`
   - `Subscribe(agentID, subject, handler)`
   - `Track(taskID) → TaskStatus`

2. **`internal/task`** — Task tracking (create → assign → track → complete)
   - `Task` struct: ID, Type, ParentID, AgentID, Status, CreatedAt, CompletedAt, Result
   - `Store` interface: Create, Get, Update, ListByAgent, ListByStatus
   - SQLite-backed, event-sourced

3. **DelegationStage** — New pipeline stage
   - Publishes `<agent>.task.created` event
   - Tracks delegation in task store
   - Does NOT block pipeline — delegation is async

4. **Agent Subscription Handler** — Watches NATS subjects per agent
   - Each agent subscribes to its configured subjects
   - Dispatches to agent's pipeline (reuse existing stage pipeline)
   - Publishes completion events

5. **Approval Gate** — Discord interaction pattern
   - `<agent>.approval.requested` event
   - Discord sends message with "Approve/Reject" buttons (reactions)
   - User reacts → `<agent>.approval.granted` or `<agent>.approval.denied`
   - Pipeline resumes or cancels

### Event Schema (V22 additions)

```json
// <agent>.task.created
{
  "v": 1,
  "task_id": "task-abc123",
  "parent_task_id": "task-xyz789",
  "delegated_by": "lumi",
  "delegated_to": "mango",
  "task_type": "code_implementation",
  "description": "Implement the X feature",
  "context": { ... },
  "priority": "high",
  "created_at": "2026-05-23T03:00:00Z"
}

// <agent>.task.completed
{
  "v": 1,
  "task_id": "task-abc123",
  "completed_by": "mango",
  "status": "completed",
  "result": { ... },
  "output_length": 1234,
  "duration_ms": 5000
}

// <agent>.approval.requested
{
  "v": 1,
  "task_id": "task-abc123",
  "agent_id": "lumi",
  "approval_type": "push",
  "description": "Push to feature branch?",
  "target": "origin/v22-multi-agent"
}

// <agent>.approval.granted | <agent>.approval.denied
{
  "v": 1,
  "task_id": "task-abc123",
  "approved_by": "user-123456",
  "approved_at": "2026-05-23T03:05:00Z"
}
```

### Pipeline Extension

Current V21 pipeline:
```
LLMStage → PersistenceStage → EventPublishStage
```

V22 pipeline (after delegation):
```
LLMStage → DelegationStage → PersistenceStage → EventPublishStage
```

DelegationStage:
- If LLM response contains a delegation intent → publish `<agent>.task.created`
- If no delegation intent → no-op (pass through)
- Task ID added to RunContext for tracking

### Task Store Schema (SQLite)

```sql
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    parent_id TEXT,
    type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'created',
    delegated_by TEXT,
    delegated_to TEXT,
    description TEXT,
    context TEXT,  -- JSON
    result TEXT,   -- JSON
    priority TEXT DEFAULT 'normal',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    completed_at DATETIME
);

CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_tasks_agent ON tasks(delegated_to);
CREATE INDEX idx_tasks_parent ON tasks(parent_id);
```

### Parallel Agent Execution (M3.2)

Multiple delegation events can be in-flight simultaneously. Each agent processes independently:
- Lumi delegates Task A to Mango
- Lumi delegates Task B to a Researcher agent
- Both agents work in parallel
- Lumi receives both results and synthesizes

Implementation: NATS handles this naturally — each agent subscribes to its own subjects. No coordination needed beyond task tracking.

### Role Assignment (M3.3)

```yaml
agents:
  - id: lumi
    role: orchestrator     # Plans, delegates, reviews
    capabilities: [plan, delegate, review, approve]

  - id: mango
    role: developer        # Codes, tests, reports
    capabilities: [code, test, report]

  - id: researcher
    role: researcher       # Searches, summarizes
    capabilities: [search, summarize]
```

Roles constrain what an agent can do. The policy engine checks `capabilities` before allowing delegation.

### Approval Gates (M3.5)

Discord-based approval:
1. Agent sends approval request via `publishEvent`
2. Discord adapter renders message with ✅/❌ reactions
3. User clicks reaction → Discord gateway event → Prizm
4. Prizm publishes approval result
5. Delegated task resumes or cancels

## File Structure

```
internal/
  delegation/
    engine.go          # Delegate(), Subscribe(), Track()
    engine_test.go
    handler.go         # Agent subscription handler
    handler_test.go
  task/
    store.go           # TaskStore interface + SQLite impl
    store_test.go
    task.go            # Task struct
    task_test.go
cmd/prizm-cli/
  cmd_serve.go         # Wire delegation engine, subscription handlers
```

## Implementation Order

1. **M3.1a: Task Store** — `internal/task` package, SQLite schema, CRUD
2. **M3.1b: Delegation Engine** — `internal/delegation` package, NATS pub/sub
3. **M3.1c: Agent Subscriptions** — Wire subscriptions from `prizm.yaml` config
4. **M3.1d: DelegationStage** — Pipeline stage that detects delegation intent
5. **M3.1e: Integration test** — Full delegation flow through NATS

Then:
6. **M3.2: Parallel Agents** — Multiple agents, concurrent task tracking
7. **M3.3: Role Assignment** — Config-driven capabilities
8. **M3.4: Task Tracking** — End-to-end task lifecycle
9. **M3.5: Approval Gates** — Discord reactions → approval events
10. **M3.6: E2E Test** — Full Lumi → Mango → review flow