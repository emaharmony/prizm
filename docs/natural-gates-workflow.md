# Natural Gates Workflow System (V2)

> **Status:** Phase 1-6 Complete
> **Designed by:** Mango
> **Implemented by:** Lumi

## Overview

The Natural Gates Workflow System replaces hardcoded iteration-based loops with a phase-based architecture where each phase has a **natural gate** — a real condition that must be met before the phase exits, not just an iteration counter.

## The 7 Phases

| Phase | Gate | What Happens |
|-------|------|-------------|
| **PROBE** | Assumption threshold < 2.0 | Identify unknowns, ask questions, search for answers |
| **RESEARCH** | All confidence domains ≥ 0.7 | Search web, memory, codebase, consult agents |
| **PLAN** | Plan completeness ≥ 0.9 | Break work into tasks, assign to agents, define success criteria |
| **FEEDBACK_PRE** | Lumi/Ema approval | Plan posted to Discord, workflow pauses until approved |
| **EXECUTION** | All tasks complete + verification passes | Execute plan with V36 enforcement (branch protection, commit-push gate) and V35 objective build/test verification (e.g. `go test ./...`) before the phase can complete |
| **FEEDBACK_POST** | Lumi + Mango review | Post-execution review, Mango ALWAYS required |
| **REPORT** | All 5 sections present | Final report with proof of work (commits, files, PRs) |

## Natural Gates

### Assumption Tracking
Prizm declares assumptions during PROBE:
```
ASSUMPTION: The API supports pagination | confidence: 0.3 | criticality: high
```
Gate exits when weighted score < 2.0 (blocker=4x, high=2x, medium=1x, low=0.5x).

### Confidence Tracking
Prizm declares confidence during RESEARCH:
```
CONFIDENCE: codebase_understanding | 0.8 | reason: read all relevant files
```
7 domains. Gate uses weakest-link principle — overall = minimum across all domains.

### Plan Completeness
Prizm declares tasks during PLAN:
```
TASK: T1 | description: Fix auth flow | agent: prizm | depends_on: [] | success: sign-in works end-to-end
```
Gate evaluates: tasks identified (30%), resources assigned (30%), dependencies (20%), success criteria (10%), risk mitigation (10%).

## Multi-Agent Delegation

Prizm can delegate tasks to:
- **Mango** (deepseek-v4-pro) — code review, data structuring, computation
- **Junie** (JetBrains) — refactoring, test writing, debugging
- **Lumi** (OpenClaw) — architecture, creative direction, consultation
- **Custom agents** — created during PLAN phase if needed (policy-gated)

Delegation happens via NATS task packets:
```json
{
  "type": "task_delegation",
  "target_agent": "mango",
  "task_id": "T1",
  "description": "Review auth flow",
  "expected_deliverable": "Code review with scores",
  "validation_checklist": ["build passes", "no broken refs"]
}
```

## Feedback Gates

### Pre-Execution (FEEDBACK_PRE)
- Plan posted to Discord #manager-room
- Lumi OR Ema approves (configurable: require_any or require_both)
- Workflow PAUSES — state saved to disk
- Next wake cycle resumes from saved state
- Commands: `approve {id}`, `changes {id}: {notes}`, `reject {id}: {reason}`

### Post-Execution (FEEDBACK_POST)
- Review package posted to Discord
- Mango ALWAYS required (hardcoded in gate)
- 6 review dimensions: code_quality, task_completion, regression_check, test_coverage, documentation, git_hygiene
- Commands: `review_approve {id}`, `review_changes {id}: {issues}`

## Fast Path
Low-risk tasks (marked `risk: low` in PROJECT_STATE.md) skip PROBE and RESEARCH, going straight to PLAN → EXECUTION.

## Risk Levels
Tasks in PROJECT_STATE.md can have risk levels:
- `low` — fast path, Lumi approval only
- `medium` — full workflow, Lumi approval only
- `high` — full workflow, Lumi AND Ema approval

## Resumability
- State persisted to `runs/natural-gates/current_workflow.json`
- Auto-save every 30 seconds during execution
- Paused workflows resume on next wake cycle
- All assumptions, confidence, delegations, and feedback preserved

## Configuration
Default config: [examples/workflows/natural-gates-default.yaml](../examples/workflows/natural-gates-default.yaml)

```yaml
phases:
  - name: PROBE
    max_iterations: 10
    gate:
      type: assumption_threshold
      threshold: 2.0
```

## CLI
```bash
# View current workflow status
prizm workflow v2 status

# List all workflow runs
prizm workflow v2 list

# Export state as JSON
prizm workflow v2 export
```

## V36 Enforcement (Preserved in EXECUTION)
- Branch protection: mutations denied on main/master
- Commit-push gate: can't finish until committed AND pushed
- Self-review: system auto-injects git diff before commit
- Task assignment: system parses PROJECT_STATE.md for tasks

## Mango Involvement
Per Ema's requirement, Mango is involved in every fix:
- Mango is a required reviewer in FEEDBACK_POST (hardcoded)
- If Mango finds issues, Mango reviews the fixes
- Mango is involved end-to-end in the fix cycle

## See Also

- [Architecture](architecture/ARCHITECTURE.md)
- [Version History](history/VERSION_HISTORY.md)
- [Documentation Hub](README.md)
