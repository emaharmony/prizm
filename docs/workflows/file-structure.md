# Workflow File Structure

A workflow definition is a YAML or JSON document (format detected by file
extension — `.yaml`/`.yml` decode as YAML, everything else as JSON) matching
the `WorkflowDefinition` schema
(`internal/workflow/multiagent/schema_types.go`). This page documents every
field. For the routing/loop *semantics* (not just the shape), see
[Routing and outcomes](routing-and-outcomes.md) and
[Loops and cycles](loops-and-cycles.md).

## Top level

```yaml
apiVersion: prism.dev/v1alpha1
kind: MultiAgentWorkflow
metadata: { ... }
spec: { ... }
```

| Field | Required | Meaning |
| --- | --- | --- |
| `apiVersion` | yes | Must be exactly `prism.dev/v1alpha1` today. See [Schema versioning](#schema-versioning) below. |
| `kind` | yes | Must be exactly `MultiAgentWorkflow`. |
| `metadata` | yes | Identity and description — see below. |
| `spec` | yes | The executable graph — see below. |

Unknown top-level (or nested) fields are a **hard error**, not a silently
ignored typo — the loader decodes with `json.Decoder.DisallowUnknownFields()`
on both the YAML and JSON paths. See
[Debugging validation errors](debugging-validation-errors.md).

## `metadata`

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | Human-readable workflow name. |
| `id` | no | Defaults to `name` when empty. This is the identifier the [definition registry](registry-and-versioning.md) keys versions under. |
| `version` | yes | A user-facing string (e.g. `"1.0.0"`). Stored as-is, **not** validated as semver. Distinct from the registry's own internally-assigned monotonic integer version — see [Registry and versioning](registry-and-versioning.md). |
| `description` | no | Free text. |
| `labels` | no | `map[string]string` of free-form labels. |

## `spec`

| Field | Required | Meaning |
| --- | --- | --- |
| `entryNode` | yes | The node ID execution starts at. Must name a declared node. |
| `budgets` | yes | Run-wide ceilings — see [Budgets, approvals, and validation](budgets-approvals-validation.md). |
| `defaults` | no | Workflow-wide fallback policy nodes/edges may omit and inherit — see below. |
| `nodes` | yes | The graph's vertices — see below. |
| `edges` | yes | The graph's routing rules — see below. |

### `spec.defaults`

| Field | Meaning |
| --- | --- |
| `cancellation` | Reserved, currently no fields. Matches Phase 1's always-immediate cancellation behavior; exists as a stable extension point. |
| `failure.defaultOnUnhandledOutcome` | Set to `"fail"` to allow a role's declared `allowedOutcomes` entry to have no routing edge without `prism graph validate` rejecting it (`routing.unhandled-outcome`). See [Routing and outcomes](routing-and-outcomes.md). |
| `approval.approvers` | Workflow-wide default approver list, inherited by any node whose own `approval.approvers` is unset. |
| `validation.profiles` | Workflow-wide default validation profile list, inherited by any node whose own `validation.profiles` is unset. |

### `spec.nodes[]` (`SchemaNode`)

| Field | Required | Meaning |
| --- | --- | --- |
| `id` | yes | Unique within the definition. Also used as the node's `role` when `role` is omitted. |
| `type` | no | `"role"` (default) or `"terminal"`. |
| `role` | no (role nodes) | The logical role name a `RoleRunner` is asked to execute as. Defaults to `id`. |
| `displayName` | no | Human-readable label — used by `prism graph inspect`'s text/Mermaid output. |
| `description` | no | Free text. |
| `agentProfile` | required for role nodes | Maps 1:1 to `RoleConfig.AgentRef`; resolved against Prism's existing agent registry **at run time**, not at validate/compile time — see [Agent profiles](budgets-approvals-validation.md#agent-profiles). |
| `allowedTools` | no | Shape-checked only (non-empty, no duplicates) — not checked against a live tool registry at this layer. |
| `capabilities` | no | Shape-checked only, same as `allowedTools`. |
| `allowedOutcomes` | role nodes | Every outcome value this role's `RoleRunResult.Outcome` may legally return. |
| `maxVisits` | no | Per-node visit ceiling. Positive integer or `-1` (unlimited). |
| `localIterations` | no | Per-node bounded-attempt ceiling, maps to `RoleConfig.MaxLocalIterations`. |
| `tokenBudget` | no | Per-node token ceiling, maps to `RoleConfig.TokenBudget`. |
| `durationBudget` | no | Per-node Go duration string (e.g. `"10m"`), maps to `RoleConfig.TimeBudget`. |
| `retry` | no | `{maxRetries, baseDelay, maxDelay, jitter}` — per-node retry policy. |
| `approval` | no | `{required, approvers}` — see [Budgets, approvals, and validation](budgets-approvals-validation.md). |
| `validation` | no | `{profiles}` — validation profile names this role's output must pass. |
| `terminalCondition` | terminal nodes only | The condition string recorded on `RunState.TerminalOutcome` when a run ends here. Any non-empty string is legal — see the note on custom terminal conditions in [Current limitations](limitations.md#terminal-condition-runtime-support). |
| `inputContract` / `outputContract` | no | `{description, artifacts}` — documented, **not enforced**, in this release. |

### `spec.edges[]` (`SchemaEdge`)

| Field | Required | Meaning |
| --- | --- | --- |
| `id` | yes | Unique within the definition. Also the seed for the edge's stable compiled ID (`edge:<from-role>:<outcome>`). |
| `from` | yes | Source node ID. |
| `to` | yes | Destination node ID. There is no schema-level way to leave `to` empty and mean "ends the run" — route to a node whose `type: terminal` instead. |
| `when.outcome` | yes | The exact outcome value that selects this edge. See [Routing and outcomes](routing-and-outcomes.md) — this is the **only** condition surface; there is no expression language. |
| `priority` | no | Tie-break for two edges sharing the same `(from, outcome)` pair; higher wins. |
| `loop` | required inside a cycle | `{maxTraversals, onExhausted, routeTo}` — see [Loops and cycles](loops-and-cycles.md). |
| `label` | no | Human-readable edge label, shown by `prism graph inspect`. |

## Schema versioning

`apiVersion`/`kind` are checked against the raw decoded document **before**
any typed decode is attempted — a mismatch produces a single, immediate
diagnostic (`schema.unsupported-api-version` / `schema.unsupported-kind`)
and the loader stops there; it never attempts a partial decode of a document
it does not recognize. This is a deliberate fail-closed design: a future,
incompatible schema version cannot be silently misinterpreted as today's
shape.

Today `LoadDefinition` accepts exactly one `apiVersion`
(`prism.dev/v1alpha1`, `multiagent.SchemaAPIVersion`) and one `kind`
(`MultiAgentWorkflow`, `multiagent.SchemaKind`). There is no version
negotiation or multi-version support in this release — see
[Current limitations](limitations.md).
