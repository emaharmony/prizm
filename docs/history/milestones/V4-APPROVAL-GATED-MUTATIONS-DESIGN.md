# V4 — Approval-Gated Mutations

## Mission

Give agents the ability to propose file changes, but ensure Prizm never applies
them without explicit human operator approval. Every step in the proposal →
approval → application pipeline is evented and auditable.

**No LLM self-approval. The model proposes; the human decides.**

## What Changed

### Approval System (`internal/approval`)
- `Approval` model: ID, status, requested_by, project, mutation_type, target_path,
  content, preview, timestamps, policy decision
- State machine: `pending → approved | denied | expired`
- `NewApproval()` with ULID generation, content preview (first 500 chars)
- `Approve(by)` and `Deny(by, reason)` with state validation
- File-based persistence: `runs/<run_id>/approvals/<approval_id>.json`
- `ApprovalStore` with RWMutex for concurrent access

### Mutation Executor (`internal/mutation`)
- `Executor` applies approved mutations to disk
- Safety checks: workspace-scoped, path traversal protection, size limits
- No destructive deletes — mutations are additive or replacement only
- `write_file` mutation type with content validation

### write_file_proposal Tool
- Replaces `write_file_dry_run` as the file mutation path
- Validates path and content, creates pending approval
- Returns `approval_id` to the CLI operator
- Emits `prizm.mutation.proposed` + `prizm.approval.requested`
- Does NOT write to disk — only creates the approval record

### V4 Event Types
- `prizm.approval.requested` — approval created, awaiting decision
- `prizm.approval.granted` — human approved the mutation
- `prizm.approval.denied` — human denied the mutation
- `prizm.approval.expired` — approval timed out
- `prizm.mutation.proposed` — agent proposed a change
- `prizm.mutation.validated` — safety checks passed
- `prizm.mutation.applied` — file was written to disk
- `prizm.mutation.failed` — mutation execution failed

### CLI Approval Commands
- `prizm approval list` — list all approvals for a project
- `prizm approval show <id>` — view approval details and content preview
- `prizm approval approve <id>` — approve and apply the mutation
- `prizm approval deny <id> --reason "..."` — deny with reason

### Runner Integration
- `write_file_proposal` integrated into agent tool request parsing
- Approval flow hooked into the runner's post-agent-execution pipeline
- `summary.json` extended with `approvals` and `mutations` arrays

### Policy Updates
- `write_file_proposal` → `requires_approval` (default policy)
- `write_file` (direct) → `denied` (prevents bypassing approval)
- Updated prompt builder with V4 mutation instructions for the agent

## Key Packages/Files

| Package / File | Purpose |
|---|---|
| `internal/approval/approval.go` | Approval model + state machine |
| `internal/approval/store.go` | File-based approval persistence |
| `internal/mutation/executor.go` | Disk-level mutation application with safety |
| `internal/tool/builtins.go` | write_file_proposal tool implementation |
| `internal/tool/policy.go` | Updated policy for V4 mutation types |
| `cmd/prizm-cli/main.go` | Approval CLI commands |
| `internal/run/runner.go` | Approval flow integration into lifecycle |
| `internal/event/event.go` | 8 new approval + mutation event types |
| `internal/agent/parser.go` | Tool request parsing updated for V4 |

## Design Decisions

1. **Propose-then-approve, never auto-apply** — The agent proposes a mutation.
   The proposal creates an approval record in `pending` status. Nothing is
   written to disk until a human runs `prizm approval approve`.

2. **Approvals are file-based, not in-memory** — Approval records persist as
   JSON files under `runs/<run_id>/approvals/`. This means approvals survive
   restarts, are transferable (copy the directory), and are auditable without
   querying a database.

3. **Policy layers: V4 policy decides approval requirement, not permission** —
   The V3 policy layer says "this tool is allowed/denied." V4 adds the
   `requires_approval` decision — the tool is allowed to run but cannot apply
   mutations without human approval.

4. **Content preview in approvals** — The first 500 characters of proposed
   content are stored as `preview` in the approval record. This lets the
   operator see what will be written without opening a separate file.

5. **State machine with validation** — You cannot approve a denied approval,
   deny an approved one, or re-approve an expired one. The state machine
   enforces valid transitions and returns clear errors for invalid ones.

6. **Safety by isolation** — The mutation executor only writes within the
   project workspace root. Path traversal and absolute path escape are blocked
   at the executor level, not just the policy layer. Defense in depth.

7. **Not included (by design)**: apply_patch (V5 candidate), shell execution,
   multi-agent, approval UI, background workers. Each requires its own design
   iteration.

## Safety Model

| Threat | Protection |
|---|---|
| LLM self-approval | Approval required for ALL file writes |
| Bypass approval with direct `write_file` | Direct write denied by policy |
| Write outside workspace | Path containment check in executor |
| Path traversal | Blocked at executor + policy |
| Destructive deletes | Mutation executor does not support delete |
| Concurrent writes | RWMutex in ApprovalStore |

## Test Coverage

- **134 tests** across 8 packages (up from 104)
- Approval: creation, approve, deny, state transitions, invalid transitions, preview
- Store: save, load, list by run, update status
- Mutation executor: valid write, path traversal, outside workspace, empty content
- Tool: write_file_proposal validation, approval creation
- Policy: write_file_proposal requires_approval, direct write_file denied
- Runner: V4 approval lifecycle integration, approval approve/deny flows

## Approval Flow

```
Agent tool_request = write_file_proposal
  → Tool.validateInput(path, content)
  → ApprovalStore.NewApproval(runID, project, path, content)
  → emit(prizm.mutation.proposed)
  → emit(prizm.approval.requested)
  → approval.pending ← WAIT FOR HUMAN ←

[Human runs: prizm approval approve <id>]
  → ApprovalStore.Load(id)
  → approval.Approve(operator)
  → MutationExecutor.Apply(approval)
  → safety checks pass
  → file written to disk
  → emit(prizm.mutation.applied)
  → emit(prizm.approval.granted)
  → approval.approved

[OR Human runs: prizm approval deny <id> --reason "..." ]
  → approval.Deny(operator, reason)
  → emit(prizm.approval.denied)
  → approval.denied
```
