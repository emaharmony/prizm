# V13 — Multi-Agent Orchestration

## Mission

Give Prizm the ability to coordinate multiple AI agents through the event
stream — not through direct messaging, not through shared filesystem state,
but through the same canonical event log that already powers everything else.

V13 answers: *what happens when more than one intelligence operates on the
same event stream, with policy controlling who can do what?*

## Why This Matters for the Thesis

Prizm's thesis: "What happens to an intelligence system when context does not
get wiped?" V1-V12 answered this for a single agent. V13 extends the answer
to multiple agents sharing the same context.

If Agent A writes a file and Agent B reads it, they should be doing that
through events — not through shared filesystem state. If Agent A proposes a
mutation and Agent B reviews it, that's a V4 approval flow — but between
agents, not just agent↔human.

The event stream is the shared context. Policy is the shared guardrail.
Projections give each agent a view of the current state. This is the
architecture that makes multi-agent safe and observable.

## What V13 Builds

### 1. Agent Registry

A registry of named agents with declared roles and capabilities.

```go
// internal/agent/registry.go

type Agent struct {
    Name         string            // e.g., "coder", "reviewer", "planner"
    Version      string            // e.g., "1.0.0"
    Role         string            // e.g., "implementation", "review", "planning"
    Capabilities []AgentCapability // what this agent can do
    Provider     provider.Provider // how to call this agent's LLM
    Model        string            // model name
}

type AgentCapability struct {
    Action      string // e.g., "write_code", "review_code", "plan_task"
    Description string // human-readable
}

type Registry struct {
    agents map[string]*Agent
    mu     sync.RWMutex
}
```

Methods: `Register`, `Resolve`, `List`, `Capabilities`.

The registry is similar to the adapter registry (V9) but for LLM-backed
agents. Key difference: agents have a Provider for making LLM calls, while
adapters have an Execute method for domain actions.

### 2. Delegation Steps

A new workflow step type: `delegate`. This lets one agent delegate a subtask
to another agent.

```yaml
# example workflow with delegation
name: code-and-review
version: 1
steps:
  - id: plan
    type: delegate
    agent: planner
    input:
      task: "{{ .task }}"
  - id: implement
    type: delegate
    agent: coder
    when: "step.plan.status == completed"
    input:
      task: "{{ step.plan.output.plan }}"
  - id: review
    type: delegate
    agent: reviewer
    when: "step.implement.status == completed"
    input:
      task: "{{ step.implement.output }}"
```

When a delegation step runs:
1. The workflow runner resolves the agent from the registry
2. The runner creates a subtask event (new event type: `agent.delegated`)
3. The agent's LLM provider is called with the subtask
4. The agent's response and any tool calls are recorded as events
5. The delegation result (success/failure/output) becomes the step output

All of this goes through the same events.jsonl. Every agent action is
observable, replayable, and auditable.

### 3. Shared Run Context

Multiple agents in one run share:
- **events.jsonl** — the canonical event log
- **projections** — derived state views (V10)
- **approvals** — pending mutations (V4)
- **policy decisions** — what's allowed (V8)

Each agent's events carry an `agent` field identifying which agent emitted
them. Projections can filter by agent. The dashboard (V11) shows which agent
did what.

This means we DON'T need:
- A separate event log per agent
- A messaging bus between agents
- Shared memory or shared filesystem state

The event stream IS the communication mechanism.

### 4. Inter-Agent Policy

V8 policy already gates actions. V13 extends this to agent-to-agent
delegation.

New policy action format:

```yaml
# Example: coder cannot review its own code
- id: deny_self_review
  action: agent.delegate
  resource:
    type: agent
    name: "reviewer"
  subject:
    type: agent
    name: "coder"  # only applies when coder delegates to reviewer
  decision: allowed
  description: "Allow coder to delegate review to reviewer"

# Example: no agent can delegate to itself
- id: deny_self_delegation
  action: agent.delegate
  resource:
    type: agent
    name: "{{ .subject.name }}"  # same as subject = self-delegation
  decision: denied
  description: "Agents cannot delegate to themselves"
```

The policy evaluator already supports subject and resource matching.
V13 just adds the `agent.delegate` action and agent-typed subjects/resources.

## New Event Types

```go
// V13 agent events
const (
    EventAgentRegistered = "agent.registered"   // agent added to registry
    EventAgentDelegated  = "agent.delegated"    // subtask delegated to agent
    EventAgentCompleted   = "agent.completed"    // agent finished subtask
    EventAgentFailed      = "agent.failed"       // agent subtask failed
)
```

Each delegation event carries:
- `agent_name`: which agent was delegated to
- `delegated_by`: which agent (or workflow) did the delegation
- `subtask`: the task description given to the agent
- `run_id`: the parent run ID
- `correlation_id`: links the delegation to its completion

## New CLI Commands

```
prizm agent list              — List registered agents
prizm agent show <name>       — Show agent details and capabilities
```

No new workflow commands — delegation is a step type within existing workflows.

## New Projection: `agent_activity`

A built-in projection that tracks which agents have done what:

```json
{
  "agents": {
    "planner": { "tasks_delegated": 3, "tasks_completed": 2, "tasks_failed": 1 },
    "coder":   { "tasks_delegated": 5, "tasks_completed": 4, "tasks_failed": 1 },
    "reviewer": { "tasks_delegated": 2, "tasks_completed": 2, "tasks_failed": 0 }
  },
  "total_delegations": 10,
  "total_completions": 8,
  "total_failures": 2
}
```

This projection subscribes to all agent events and maintains a running
count per agent.

## Package Structure

```
internal/
├── agent/
│   ├── agent.go          # Agent struct, AgentCapability
│   ├── registry.go       # Registry: Register, Resolve, List, Capabilities
│   └── events.go         # V13 agent event types
├── workflow/
│   ├── delegate.go       # Delegation step handler (new)
│   └── ...               # existing workflow files
├── projection/
│   └── builtin/
│       └── agentactivity/ # Agent activity projection (new)
│           └── agentactivity.go
└── policy/
    └── ...               # existing policy files (agent.delegate action added)
```

Note: `internal/agent/` currently has a `placeholder.go` file. V13 replaces
this with real agent functionality.

## What V13 Does NOT Include

- **No autonomous swarms** — agents only act within workflow steps (roadmap constraint)
- **No agent-to-agent direct messaging** — use the event stream
- **No new LLM provider types** — reuse the existing `provider.Provider` interface
- **No agent memory** — each agent's context comes from the event stream and projections
- **No agent spawning** — agents are registered, not spawned on demand
- **No inter-agent negotiation** — delegation is one-directional (delegator → delegate)
- **No human-in-the-loop for delegation** — policy decides, not a human approval step

The last point is important and worth discussing. Should agent-to-agent
delegation require human approval? My design says no — policy decides.
But the policy CAN require approval:

```yaml
- id: require_approval_for_production_deploy
  action: agent.delegate
  resource:
    type: agent
    name: deployer
  decision: requires_approval
  description: "Deploying to production needs human sign-off"
```

So the capability is there. The default is policy-gated, not approval-gated.

## Acceptance Criteria

1. `internal/agent/` has real Agent struct, Registry, and event types
2. Workflow `delegate` step type works end-to-end
3. Agent delegation emits canonical events (delegated → completed/failed)
4. Inter-agent policy gates delegation with `agent.delegate` action
5. `agent_activity` projection tracks per-agent activity counts
6. CLI: `prizm agent list`, `prizm agent show <name>`
7. Demo workflow: `examples/workflows/demo-agents.yaml`
8. All 334+ existing tests pass unchanged
9. Design doc: `docs/V13-MULTI-AGENT-DESIGN.md`
10. Version: `prizm v0.13.0`

## Version History Context

| Version | What | Key Insight |
|---------|------|-------------|
| V1-V5 | Single agent lifecycle | Task → LLM → tools → approval → validation → review |
| V6 | Gate systems | Domain-specific decision gates |
| V7 | Workflows | Named sequences of steps |
| V8 | Policy | Declarative rules for what's allowed |
| V9 | Adapters | How Prizm talks to the outside world |
| V10 | Projections | Derived state from events |
| V11 | Dashboard | Visual interface for runs |
| V12 | Refactor | Clean foundation for what comes next |
| **V13** | **Multi-Agent** | **Multiple agents, one event stream, policy-controlled** |

The through-line: every version since V7 has been building toward V13.
Workflows provide the orchestration. Policy provides the guardrails.
Adapters provide the external actions. Projections provide the shared state.
Now V13 connects them all — multiple agents operating through the same
infrastructure, with the event stream as the single source of truth.