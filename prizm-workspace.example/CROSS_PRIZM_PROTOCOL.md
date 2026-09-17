# CROSS_PRIZM_PROTOCOL.md

## Purpose

Astraea may live in one Prizm environment while communicating with another Prizm environment.

This file defines a simple communication protocol for coordination.

---

## Principles

1. Never assume shared memory.
2. Send context explicitly.
3. Ask for structured outputs.
4. Mark approval requirements.
5. Distinguish planning from execution.
6. Include telemetry when useful.
7. Confirm validation status.
8. Preserve decisions into memory.

---

## Message Types

```txt
context_sync:
  Share current project state or memory.

task_request:
  Ask another Prizm environment to perform or plan a task.

status_request:
  Ask for current progress/blockers.

validation_request:
  Ask another environment to validate output.

handoff:
  Transfer responsibility for a task.

memory_sync:
  Share durable decisions or lessons.

goblin_packet:
  Controlled creative chaos payload. Must include checklist.
```

---

## Outbound Template

```json
{
  "from": "Astraea",
  "to": "<target-prizm-env>",
  "message_type": "task_request",
  "priority": "normal",
  "thread": {
    "project": "Roblox Factory",
    "game": "<game-name>",
    "feature": "<feature-name>",
    "correlation_id": "<id>"
  },
  "context": {
    "summary": "<short context>",
    "known_constraints": [],
    "current_status": "PLANNED | ASSIGNED | IMPLEMENTED | VALIDATED | BLOCKED | NEEDS_REVIEW",
    "relevant_files": [],
    "repo_context": "<path-or-description>"
  },
  "request": {
    "goal": "<specific ask>",
    "expected_output": "plan | patch | validation_report | questions | summary",
    "definition_of_done": [],
    "approval_required": false
  },
  "telemetry": {
    "coherence": 0.0,
    "uncertainty": 0.0,
    "risk": 0.0,
    "goblin": 0.0,
    "integration": 0.0
  }
}
```

---

## Response Template

```json
{
  "from": "<target-prizm-env>",
  "to": "Astraea",
  "message_type": "task_response",
  "correlation_id": "<id>",
  "status": "accepted | rejected | blocked | completed | needs_clarification",
  "summary": "<what happened>",
  "outputs": [],
  "validation": {
    "status": "not_run | partial | passed | failed",
    "details": []
  },
  "risks": [],
  "questions": [],
  "recommended_next_action": "<next step>"
}
```

---

## Goblin Packet Template

```json
{
  "from": "Astraea",
  "to": "<target-prizm-env>",
  "message_type": "goblin_packet",
  "priority": "low",
  "context": {
    "purpose": "creative ideation / edge-case hunting",
    "feature": "<feature>"
  },
  "goblins": [
    {
      "idea": "<weird idea>",
      "possible_value": "<why it might help>",
      "risk": "<why it might be bad>",
      "checklist_before_task": []
    }
  ],
  "rule": "Release the goblins, but give them a checklist."
}
```
