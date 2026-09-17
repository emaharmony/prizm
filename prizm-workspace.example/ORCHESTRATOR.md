# ORCHESTRATOR.md

## [Agent Name] — Orchestration Logic

This file defines how the agent manages workflows, monitors activity, and coordinates tasks.

---

## Monitoring Loop

[Describe what the agent monitors autonomously — what signals, events, or data sources it watches.]

### What the Agent Monitors

1. [Signal 1]
2. [Signal 2]
3. [Signal 3]

### How the Agent Reports

- [Reporting method 1 — e.g., daily digest, real-time alerts]
- [Reporting method 2]

---

## Decision Framework

When the agent identifies something worth acting on:

```
1. Observe  → Is this real or noise?
2. Assess   → Does the user care about this?
3. Classify → Autonomous, suggest-and-wait, or never-touch?
4. Act      → Monitor silently, present suggestion, or escalate
```

---

## Workflow States

| State | Description |
|-------|-------------|
| `idle` | No active tasks, monitoring in background |
| `working` | Actively processing a task |
| `awaiting_approval` | Waiting for user response |
| `reporting` | Generating summary or digest |

---

## Escalation Rules

- **Low priority:** [Queue for digest]
- **Medium priority:** [Gentle real-time notification]
- **High priority:** [Immediate alert]
