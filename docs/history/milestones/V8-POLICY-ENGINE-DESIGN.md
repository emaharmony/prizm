# V8 — Core Policy Engine

## Mission

Centralize all policy decisions across Prizm into a single, consistent, declarative
policy engine. Before V8, policy lived in multiple places: tool policy (V3),
approval gates (V4), validation profiles (V5). V8 brings them under one roof with
a unified rule model, YAML-based configuration, and first-match evaluation.

**Policy decides permission. Local validators still enforce input safety.**

## What Changed

### Policy Decision Model (`internal/policy/decision.go`)
- `PolicyDecision` struct: decision (allowed/denied/requires_approval), reason, rule_id, severity
- `Decision` constants: `DecisionAllowed`, `DecisionDenied`, `DecisionRequiresApproval`
- `Severity` levels: info, warning, critical

### Policy Rule Model (`internal/policy/rule.go`)
- `PolicyRule`: ID, Description, Match (MatchSpec), Decision, Reason, Severity
- `MatchSpec`: Action, ResourceType, ResourceName, ContextMode — field-based matching
- Missing match fields act as wildcards (match anything)
- YAML-based declarative rules

### Policy Request Model (`internal/policy/request.go`)
- `PolicyRequest`: Action, Resource (type + name), Subject (user/agent ID), Context (mode, metadata)
- Standardized request format for all Prizm policy decisions

### Policy Evaluator (`internal/policy/evaluator.go`)
- `Evaluator`: registry-based, first-match evaluation
- `Evaluate(req)` → `PolicyDecision` with default deny
- Event emission at every evaluation: `prizm.policy.requested/evaluated/allowed/denied/approval_required`
- Configurable default decision (default: deny)

### Policy Registry + Loader (`internal/policy/registry.go`, `loader.go`)
- `Registry`: register, list, resolve rules by ID
- `Loader`: load rules from YAML files and directories
- `LoadFromDir()` — scan directory for `.yaml` policy files

### Policy Events (`internal/policy/events.go`)
- `prizm.policy.requested` — evaluation requested
- `prizm.policy.evaluated` — evaluation complete
- `prizm.policy.allowed` — rule matched with allow decision
- `prizm.policy.denied` — rule matched with deny decision
- `prizm.policy.approval_required` — rule matched with approval requirement

### Policy Artifacts (`internal/policy/artifacts.go`)
- `runs/policy/<eval_id>.json` — persisted evaluation result
- Captures request, matching rule, decision, reason

### Default Policies (`policies/default.yaml`)
- `allow_echo` — echo tool is safe and allowlisted
- `allow_read_file` — read_file with workspace path enforcement
- `allow_list_files` — list_files with workspace scope
- `deny_shell_execution` — run_command blocked (critical severity)
- `require_approval_for_file_write` — file mutations require operator approval
- `block_live_trading_dispatch` — live trading dispatch denied (critical severity)

### Tool Integration
- V8 policy evaluates **before** local tool validation (V3 policy)
- Policy decides permission; local validators enforce input safety
- `PolicyDenied` from V8 blocks even if V3 local policy would allow
- `PolicyAllowed` from V8 still lets local validators block path traversal etc.
- Backward compatible: no `PolicyEvaluator` = local V3 policy only

### CLI Commands
- `prizm policy list` — list all registered policy rules
- `prizm policy evaluate --input <file.json>` — evaluate a policy request against rules

## Key Packages/Files

| Package / File | Purpose |
|---|---|
| `internal/policy/rule.go` | PolicyRule + MatchSpec structs, matching logic |
| `internal/policy/decision.go` | PolicyDecision, Decision constants, severity |
| `internal/policy/request.go` | Standardized PolicyRequest format |
| `internal/policy/evaluator.go` | First-match evaluation engine |
| `internal/policy/registry.go` | Rule registration, listing, resolution |
| `internal/policy/loader.go` | YAML file/directory loading |
| `internal/policy/events.go` | 5 policy event type constants |
| `internal/policy/artifacts.go` | Evaluation result persistence |
| `internal/tool/executor.go` | Integrated V8 policy before local validation |
| `internal/tool/policy_integration_test.go` | V8 ↔ V3 policy integration tests |
| `policies/default.yaml` | Default policy rules for core operations |
| `cmd/prizm-cli/main.go` | Policy CLI commands |

## Design Decisions

1. **First-match evaluation** — Rules are evaluated in registration order. The
   first rule whose `MatchSpec` matches the request wins. No rule ordering
   ambiguity. No partial credit. Place most specific rules first.

2. **Wildcard matching** — Missing fields in `MatchSpec` are wildcards. A rule
   with only `action: tool.execute` matches all tool executions. A rule with
   `action: tool.execute + resource.name: run_command` matches only run_command.
   This enables both broad defaults and narrow overrides.

3. **Policy decides permission; validators enforce safety** — The policy engine
   decides "should this be allowed?" Local validators enforce "is the input
   safe?" This two-layer approach prevents policy bugs from creating safety gaps.
   Even if policy allows `read_file`, the local validator still blocks `../`.

4. **Default deny** — If no rule matches, the operation is denied. This is the
   safest default. Explicit allow rules are required for every permitted operation.

5. **YAML configuration, not Go code** — Policies are data, not compiled code.
   They can be versioned, audited, distributed, and modified without recompilation.
   The `policies/` directory is the single source of truth for what Prizm allows.

6. **Backward compatible** — If no `PolicyEvaluator` is configured, the V3 local
   policy system still works. V8 is opt-in at the evaluator level. Once an
   evaluator is configured, V8 policy runs first, then local validation.

7. **Not included (by design)**: OPA/Rego integration, CEL expressions, remote
   policy service, RBAC, dashboard, LLM-authored policies. The YAML matching
   model is intentionally simple until complexity is needed.

## Test Coverage

- **43 new tests** (256 total, all passing across 12 packages)
- Rule: matching (exact, wildcard, mismatched action, mismatched resource type, all fields)
- Evaluator: allow rule, deny rule, requires_approval, first-match wins, default deny
- Registry: register, list, resolve, duplicate
- Loader: single file, directory, empty directory
- Events: requested, evaluated, allowed, denied, approval_required
- Integration: V8 deny overrides V3 allow, V8 allow still validates locally,
  tool execution through full V8+V3 chain

## Policy Evaluation Flow

```
Tool.Execute(request)
  → V8 PolicyEvaluator?.Evaluate(request)
    → emit(prizm.policy.requested)
    → iterate rules in registration order
    → first match wins:
      → allowed → emit(prizm.policy.allowed)
      → denied → emit(prizm.policy.denied) → return error
      → requires_approval → emit(prizm.policy.approval_required)
    → no match → default deny → emit(prizm.policy.denied)
  → V3 local policy validation (path safety, input checks)
  → Tool.Execute()
```

## Two-Layer Policy Architecture

```
Request: tool.execute, resource=read_file, path="../etc/passwd"

V8 Policy Engine:   "allow_read_file" rule matches → DecisionAllowed ✓
V3 Local Validator:  path contains ".." → blocked ✗
Result: DENIED (local validator overrides V8 allow for safety)

Request: tool.execute, resource=run_command, args=["ls"]

V8 Policy Engine:   "deny_shell_execution" rule matches → DecisionDenied ✗
V3 Local Validator:  (never reached)
Result: DENIED (V8 policy blocks before local validation)
```

## Policy Rule Format

```yaml
policies:
  - id: allow_echo
    description: Allow the echo tool.
    match:
      action: tool.execute
      resource.name: echo
    decision: allowed
    reason: "Echo tool is safe and allowlisted."

  - id: require_approval_for_file_write
    description: File mutations require approval.
    match:
      action: mutation.apply
      resource.type: file
    decision: requires_approval
    reason: "File mutations require operator approval."
```

## Status

**Merged** (PR #14)
**Date:** 2026-05-17
**Version:** prizm v0.8.0
