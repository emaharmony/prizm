# Prizm Event Manual

> Complete reference for all canonical Prizm events, their namespaces, payloads, and causal chains.

Every meaningful action in Prizm becomes a **canonical event** — an immutable, timestamped record that flows through the event bus. Events are Prizm's universal language. They enable observability, replay, auditing, and extensibility.

---

## Event Structure

```json
{
  "id": "evt_01JXXXXXXXXXXXX",
  "type": "prizm.task.started",
  "source": "runner",
  "timestamp": "2026-05-19T17:00:00.000Z",
  "correlation_id": "corr_01JXXXXXXXXXXXX",
  "parent_id": "evt_01JYYYYYYYYYYYY",
  "payload": { "key": "value" },
  "metadata": {
    "run_id": "run_01JXXXXXXXXXXXX",
    "session_id": "sess_01JXXXXXXXXXXXX",
    "project": "my-project",
    "agent": "analyst",
    "model": "llama3",
    "token_cost": 0,
    "latency_ms": 0
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Unique ULID-based ID (`evt_`) |
| `type` | string | Namespace-qualified event type (e.g., `prizm.task.started`) |
| `source` | string | Component that emitted the event |
| `timestamp` | string | RFC 3339 Nano timestamp |
| `correlation_id` | string | Groups all events from the same logical request |
| `parent_id` | string | Points to the event that caused this one (causal chain) |
| `payload` | object | Event-specific data |
| `metadata` | object | Runtime context (run ID, agent, model, cost) |

---

## Event Namespaces

Prizm events are organized into **15 namespaces**, each representing a domain:

| Namespace | Domain | Since |
|-----------|--------|-------|
| `prizm.task` | Task lifecycle | V1 |
| `prizm.agent` | Agent lifecycle | V1 |
| `prizm.tool` | Tool execution | V1/V3 |
| `prizm.memory` | Memory context | V1 |
| `prizm.llm` | LLM calls | V2 |
| `prizm.context` | Context injection | 14 (V2 had `prizm.context.requested/injected/failed`, V19 adds `prizm.context.file_read` and redefines `prizm.context.injected` with richer payload) |
| `prizm.approval` | Approval gates | V4 |
| `prizm.mutation` | File mutations | V4 |
| `prizm.validation` | Validation profiles | V5 |
| `prizm.review` | Deterministic review | V5 |
| `prizm.policy` | Policy decisions | V8 |
| `prizm.adapter` | Adapter lifecycle | V9 |
| `prizm.projection` | State projections | V10 |
| `prizm.workflow` | Workflow orchestration | V7 |
| `prizm.cost` | Cost tracking | V16 |
| `prizm.context` | Context injection | V19 |
| `prizm.system` | System health | V1 |
| `prizm.persistence` | WAL checkpoint | V14d |
| `prizm.output` | Output artifacts | V2 |

---

## Complete Event Reference

### Task Lifecycle (`prizm.task.*`)

The root of every run. A task is the top-level unit of work.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.task.created` | Run initialized | `task`, `provider`, `model` |
| `prizm.task.started` | Execution begins | `task`, `run_id` |
| `prizm.task.completed` | Run finished successfully | `status`, `duration_ms` |
| `prizm.task.failed` | Run failed | `error`, `status` |

**Causal chain:** `task.created → task.started → ... → task.completed | task.failed`

---

### Agent Lifecycle (`prizm.agent.*`)

An agent processes a task. Events track when the agent starts, produces output, and finishes.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.agent.started` | Agent begins processing | `agent`, `model` |
| `prizm.agent.output` | Agent produces intermediate output | `content`, `tokens` |
| `prizm.agent.completed` | Agent finishes | `status`, `duration_ms` |
| `prizm.agent.failed` | Agent errors out | `error` |

**Causal chain:** `task.started → agent.started → [agent.output...] → agent.completed | agent.failed`

---

### LLM Calls (`prizm.llm.*`)

Each LLM call produces a request/completion pair. Streaming responses produce intermediate chunks via SSE.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.llm.requested` | LLM API call initiated | `provider`, `model`, `prompt_tokens` |
| `prizm.llm.completed` | LLM response received | `completion_tokens`, `latency_ms` |
| `prizm.llm.failed` | LLM call failed | `error`, `provider` |

**Causal chain:** `agent.started → llm.requested → llm.completed | llm.failed`

---

### Tool Execution (`prizm.tool.*`)

V1 defined basic tool events. V3 added the full tool lifecycle with policy gates.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.tool.called` | Tool invoked (V1 basic) | `tool_name`, `args` |
| `prizm.tool.result` | Tool returned result (V1 basic) | `result` |
| `prizm.tool.failed` | Tool errored (V1 basic) | `error` |
| `prizm.tool.requested` | Tool execution requested (V3) | `tool_name`, `args`, `policy_decision` |
| `prizm.tool.approved` | Policy allows tool (V3) | `tool_name`, `policy` |
| `prizm.tool.denied` | Policy blocks tool (V3) | `tool_name`, `reason` |
| `prizm.tool.started` | Tool execution begins (V3) | `tool_name` |
| `prizm.tool.completed` | Tool execution succeeds (V3) | `tool_name`, `result` |

**Causal chain (V3):** `tool.requested → [tool.approved | tool.denied] → tool.started → tool.completed | tool.failed`

---

### Context Injection (`prizm.context.*`)

Context events track memory/context injection before LLM calls.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.context.requested` | Context lookup initiated | `task`, `project` |
| `prizm.context.injected` | Context added to prompt | `context_size`, `source` |
| `prizm.context.failed` | Context lookup failed | `error` |

---

### Memory Context (`prizm.memory.*`)

Remembrance integration events. Emitted when Prizm connects to the Remembrance memory service.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.memory.context_requested` | Memory lookup started | `task`, `project`, `agent` |
| `prizm.memory.context_built` | Memory context assembled | `memories_count`, `entities_count` |
| `prizm.memory.context_failed` | Memory lookup failed | `error` |

---

### Approval Gates (`prizm.approval.*`)

V4 approval-gated mutations. Human-in-the-loop for file writes and other risky operations.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.approval.requested` | Mutation needs approval | `approval_id`, `mutation_type`, `target_path` |
| `prizm.approval.granted` | Human approved | `approval_id`, `approved_by` |
| `prizm.approval.denied` | Human denied | `approval_id`, `reason` |
| `prizm.approval.expired` | Approval timed out | `approval_id` |

**Causal chain:** `mutation.proposed → approval.requested → [approval.granted | approval.denied | approval.expired]`

---

### Mutations (`prizm.mutation.*`)

File mutations after approval.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.mutation.proposed` | File change proposed | `mutation_type`, `target_path`, `diff` |
| `prizm.mutation.validated` | Validation passed | `approval_id`, `validation_status` |
| `prizm.mutation.applied` | File change written | `target_path`, `success` |
| `prizm.mutation.failed` | File write failed | `target_path`, `error` |

---

### Validation (`prizm.validation.*`)

V5 validation pipeline. Allowlisted command profiles run after mutations.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.validation.requested` | Validation profile requested | `profile`, `command` |
| `prizm.validation.started` | Command execution begins | `profile`, `working_dir` |
| `prizm.validation.completed` | Command finished | `profile`, `exit_code`, `duration_ms` |
| `prizm.validation.failed` | Command errored | `profile`, `error` |
| `prizm.validation.skipped` | Profile skipped (not allowlisted) | `profile`, `reason` |
| `prizm.validation.timeout` | Command exceeded timeout | `profile`, `timeout_seconds` |

---

### Review (`prizm.review.*`)

V5 deterministic review. Generates a review artifact from mutation + validation results.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.review.requested` | Review initiated | `run_id`, `mutation_id` |
| `prizm.review.started` | Review generation begins | `reviewer` |
| `prizm.review.completed` | Review artifact written | `recommendation`, `artifact_path` |
| `prizm.review.failed` | Review generation failed | `error` |

---

### Policy Engine (`prizm.policy.*`)

V8 core policy engine. Determines whether actions are allowed, denied, or require approval.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.policy.requested` | Policy evaluation requested | `action`, `resource` |
| `prizm.policy.evaluated` | Policy rules evaluated | `rules_matched`, `decision` |
| `prizm.policy.allowed` | Action allowed | `action`, `resource`, `rule` |
| `prizm.policy.denied` | Action denied | `action`, `resource`, `reason` |
| `prizm.policy.approval_required` | Action needs human approval | `action`, `resource`, `rule` |
| `prizm.policy.failed` | Policy evaluation errored | `error` |

---

### Adapter Lifecycle (`prizm.adapter.*`)

V9 adapter contract system. Events emitted by built-in and custom adapters.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.adapter.registered` | Adapter registered with bus | `adapter_id`, `capabilities` |
| `prizm.adapter.health` | Health check response | `adapter_id`, `status` |
| `prizm.adapter.execute` | Adapter executing action | `adapter_id`, `action`, `payload` |
| `prizm.adapter.success` | Adapter action succeeded | `adapter_id`, `action`, `result` |
| `prizm.adapter.failed` | Adapter action failed | `adapter_id`, `action`, `error` |

---

### State Projections (`prizm.projection.*`)

V10 CQRS query layer. Projections maintain derived state from the event stream.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.projection.started` | Projection update started | `projection_id`, `event_type` |
| `prizm.projection.completed` | Projection update finished | `projection_id`, `state` |
| `prizm.projection.failed` | Projection update failed | `projection_id`, `error` |

**Built-in projections:** RunStatus, AgentActivity, ToolHistory, ApprovalState

---

### Workflow Orchestration (`prizm.workflow.*`)

V7 workflow runtime. Named workflows compose Prizm capabilities.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.workflow.started` | Workflow begins | `workflow_id`, `name`, `steps` |
| `prizm.workflow.step.started` | Step execution begins | `step_id`, `step_name` |
| `prizm.workflow.step.completed` | Step finished successfully | `step_id`, `result` |
| `prizm.workflow.step.failed` | Step errored | `step_id`, `error` |
| `prizm.workflow.step.skipped` | Step skipped (condition not met) | `step_id`, `reason` |
| `prizm.workflow.paused` | Workflow paused at approval gate | `workflow_id`, `pending_approval_id` |
| `prizm.workflow.resumed` | Workflow resumed after approval | `workflow_id`, `approval_id` |
| `prizm.workflow.completed` | Workflow finished | `workflow_id`, `status` |
| `prizm.workflow.failed` | Workflow failed | `workflow_id`, `error` |

---

### System & Infrastructure (`prizm.system.*`, `prizm.persistence.*`, `prizm.output.*`)

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.system.health` | Health check | `status`, `version`, `uptime` |
| `prizm.persistence.completed` | WAL checkpoint completed | `run_id`, `stage`, `checkpoint_id` |
| `prizm.output.written` | Output artifact written | `path`, `size_bytes` |

### Cost Tracking (`prizm.cost.*`) — V16

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.cost.tracked` | Token usage recorded for an LLM call | `provider`, `model`, `prompt_tokens`, `completion_tokens`, `estimated_cost_usd` |
| `prizm.cost.reported` | Cost report generated for a run | `run_id`, `total_tokens`, `estimated_cost_usd` |

### Context Injection (`prizm.context.*`) — V19

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.context.file_read` | Workspace file read for context injection | `file`, `source`, `size_bytes`, `estimated_tokens` |
| `prizm.context.injected` | Context injection complete for a run | `run_id`, `files`, `total_tokens`, `truncated`, `truncation_applied` |

---

## Causal Chains

The most common event flows through Prizm:

### Basic LLM Call
```
task.created → task.started → agent.started → llm.requested → llm.completed
  → agent.completed → task.completed
```

### With Tool Execution (V3)
```
task.created → task.started → agent.started → llm.requested → llm.completed
  → tool.requested → tool.approved → tool.started → tool.completed
  → agent.completed → task.completed
```

### With Approval Gate (V4)
```
task.created → task.started → agent.started → llm.requested → llm.completed
  → mutation.proposed → approval.requested → [human approves]
  → approval.granted → mutation.validated → mutation.applied
  → agent.completed → task.completed
```

### With Validation + Review (V5)
```
task.created → ... → mutation.applied → validation.requested → validation.started
  → validation.completed → review.requested → review.started → review.completed
  → task.completed
```

### With Workflow (V7)
```
task.created → task.started → workflow.started → workflow.step.started
  → workflow.step.completed → workflow.step.started → workflow.step.completed
  → workflow.completed → task.completed
```

### With Policy Gate (V8)
```
tool.requested → policy.requested → policy.evaluated
  → [policy.allowed | policy.denied | policy.approval_required]
```

---

## Subscribing to Events

### Go (Embedded NATS)
```go
bus := bus.NewEmbeddedBus()
sub, _ := bus.Subscribe("prizm.task.*")
for msg := range sub.C {
    fmt.Printf("Event: %s\n", msg.Subject)
}
```

### CLI
```bash
# Watch all events
prizm events --watch

# Filter by type
prizm events --type "prizm.task.*"

# Filter by run
prizm events --run run_01JXXXXXXXXXXXX

# Export to JSONL
prizm events --export events.jsonl
```

### Dashboard
```bash
prizm dashboard
# Opens http://localhost:8080 with live event stream
```

---

## Version Compatibility

| Version | Added Events |
|---------|-------------|
| V1 | `task.*`, `agent.*`, `tool.called/result/failed`, `memory.*`, `system.health` |
| V2 | `llm.*`, `context.*`, `output.written` |
| V3 | `tool.requested/approved/denied/started/completed` |
| V4 | `approval.*`, `mutation.*` |
| V5 | `validation.*`, `review.*` |
| V7 | `workflow.*` |
| V8 | `policy.*` |
| V9 | `adapter.*` |
| V10 | `projection.*` |
| V14d | `persistence.completed` |
| V16 | `cost.tracked`, `cost.reported`, enriched `EventMetadata` (`duration_ms`, `outcome`, `token_usage`) |
| V17 | HNSW vector index, connection pooling, event store indexes |
| V18 | OpenClaw config transfer (`--from-config`), ProviderRegistry, model metadata |
| V19 | Smart context injection (pipeline stage, token budgeting, auto-discovery, `prizm context show`) |

All V1 events remain backward compatible. Newer versions add events alongside V1, never replacing them.

---

## Event Storage

Events are persisted to SQLite (WAL mode) and optionally exported to JSONL:

```
runs/<run_id>/
├── events.jsonl       # Canonical event log (one JSON per line)
├── summary.json       # Run summary with all event IDs
├── prompt.md          # Assembled prompt
├── output.md          # LLM output
├── validation/        # Validation results
│   └── <profile>.json
└── review.md          # Deterministic review artifact
```

Query events from SQLite:
```go
store, _ := event.NewSQLiteEventStore("prizm-data/events.db")
events, _ := store.Query(ctx, event.EventFilter{
    RunID: "run_01JXXX",
    Type:  "prizm.task.",
    Limit: 100,
})
```

Or use the CLI:
```bash
prizm db export <run_id>   # SQLite → JSONL
prizm db query --type "prizm.approval.*" --limit 50
```

## See Also

- [Architecture](../architecture/ARCHITECTURE.md)
- [Documentation Hub](../README.md)
