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

### Scoped Memory Lifecycle (R2) — `prizm.memory.*` (capture/search/sync)

The R2 scoped-memory pipeline emits these alongside the context events above.
Capture is idempotent, scoped to user/project/task, and falls back to local
storage when Recall is unavailable.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.memory.capture.started` | Scoped capture begins | `scope`, `content_key` |
| `prizm.memory.capture.pending` | Capture queued for Recall sync | `scope`, `intent_id` |
| `prizm.memory.capture.persisted` | Capture stored locally | `scope`, `memory_id` |
| `prizm.memory.capture.fallback` | Recall unavailable — local fallback used | `scope`, `reason` |
| `prizm.memory.capture.synced` | Capture accepted by Recall (idempotent) | `intent_id`, `capture_id` |
| `prizm.memory.capture.sync_pending` | Recall sync deferred for retry | `intent_id` |
| `prizm.memory.capture.sync_retry` | Sync retry scheduled | `intent_id`, `attempt` |
| `prizm.memory.capture.sync_failed` | Sync failed terminally | `intent_id`, `error` |
| `prizm.memory.search.requested` | Scoped retrieval begins | `scope`, `query` |
| `prizm.memory.search.completed` | Scoped retrieval finished | `scope`, `results_count` |
| `prizm.memory.search.fallback` | Search fell back to local store | `scope`, `reason` |
| `prizm.memory.extracted` | Candidate memory extracted from content | `content_key` |
| `prizm.memory.gate_passed` | Extraction passed the relevance gate | `memory_id`, `reasoning` |
| `prizm.memory.gate_rejected` | Extraction rejected | `reasoning` |
| `prizm.memory.persisted` | Memory persisted to the store | `memory_id` |
| `prizm.memory.superseded` | Older memory superseded by newer fact | `memory_id`, `superseded_by` |
| `prizm.memory.synced` | Store sync completed | `count` |
| `prizm.memory.sync_failed` | Store sync failed | `error` |

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

### Durable Proposal & Validation Lifecycle (R1) — `prizm.workflow.proposal.*`, `prizm.workflow.validation.*`

Emitted by the durable multi-agent runtime. Every proposal carries its exact
approval identity through apply and resume; commands travel through the
durable event outbox with idempotency keys (subjects under
`prizm.command.*`, e.g. `prizm.command.delegation` /
`prizm.command.graph_role_delegation`), so duplicate deliveries resolve as
`decision_already_recorded` instead of double-applying.

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.workflow.proposal.recorded` | Approved proposal durably recorded | `run_id`, `proposal_id`, `approval_id`, `apply_key` |
| `prizm.workflow.proposal.approval_pending` | Proposal waiting on approval | `run_id`, `proposal_id` |
| `prizm.workflow.proposal.approval_granted` | Exact approval matched and granted | `run_id`, `proposal_id`, `approval_id` |
| `prizm.workflow.proposal.approval_denied` | Proposal denied | `run_id`, `proposal_id`, `reason` |
| `prizm.workflow.proposal.apply_started` | Isolated worktree apply begins | `run_id`, `proposal_id`, `workspace_id` |
| `prizm.workflow.proposal.applied` | Patch applied to worktree | `run_id`, `proposal_id`, `target_path`, `diff_path` |
| `prizm.workflow.proposal.apply_failed` | Apply failed | `run_id`, `proposal_id`, `message` |
| `prizm.workflow.proposal.reconciled` | Interrupted run reconciled on restart | `run_id`, `proposal_id` |
| `prizm.workflow.validation.started` | Configured project validation begins | `run_id`, `profile` |
| `prizm.workflow.validation.completed` | Validation passed | `run_id`, `status` |
| `prizm.workflow.validation.failed` | Validation failed | `run_id`, `error` |

### Interaction Runtime — `prizm.interaction.*`

Live interaction-loop events (multi-agent runtime). Payloads always include
`run_id`, `adapter`, `step`, and — when caused by another run —
`parent_run_id`.

| Event | When |
|-------|------|
| `prizm.interaction.observation.recorded` | Observation captured |
| `prizm.interaction.decision.selected` | Action selection made |
| `prizm.interaction.action.validated` | Action passed validation |
| `prizm.interaction.action.rejected` | Action rejected by policy |
| `prizm.interaction.action.executed` | Action executed |
| `prizm.interaction.action.failed` | Action failed |
| `prizm.interaction.verification.completed` | Post-action verification done |
| `prizm.interaction.neutralized` | Run neutralized (halted) |
| `prizm.interaction.approval.paused` | Run paused at an approval gate |
| `prizm.interaction.resumed` | Run resumed after approval |

### Reflection — `prizm.reflection.*`

| Event | When | Key Payload |
|-------|------|-------------|
| `prizm.reflection.requested` | Bounded reflection requested | `run_id` |
| `prizm.reflection.completed` | Reflection produced a diagnosis | `run_id`, `diagnosis` |
| `prizm.reflection.failed` | Reflection errored | `run_id`, `error` |
| `prizm.reflection.memory_accepted` | Reflection stored | `memory_id` |
| `prizm.reflection.memory_rejected` | Reflection rejected by gate | `reason` |
| `prizm.reflection.replan_requested` | Reflection triggered a revised plan | `run_id` |

> **Note on coverage:** the canonical event constants live in
> `internal/event/` (`schema.go`, `multiagent.go`, `outbox.go`). Families not
> tabulated above — `prizm.cross.*` (Cross-Prizm bridge),
> `prizm.autopatch.*`, `prizm.architecture.*`/`prizm.vulnerability.*`
> (quality checks), `prizm.factory.*` (Factory monitor), `prizm.agent.status`
> / `prizm.agent.openclaw` (serve-mode agent liveness), and
> `prizm.dashboard.task.*` (dashboard workers) — are internal or
> experimental integration surfaces; consult the source before relying on
> them.

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
