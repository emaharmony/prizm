# ADR 0003: Freewill intention contracts remain proposals

Status: Accepted for FW-001 contract groundwork

## Context

Freewill is an experimental agent-originated intention flow. It is distinct
from the existing user-directed Freedom / Free Mode. An agent can describe a
goal and request capabilities; it cannot grant itself capabilities, approve a
request, or execute a tool. Prizm's existing runtime remains authoritative for
identity, scope enforcement, policy, human approval, budgets, persistence,
execution, and audit, as required by [ADR 0001](0001-runtime-owns-authority.md).

FW-001 adds only typed contracts, structural validation, lifecycle vocabulary,
and off-by-default configuration. It does not connect those contracts to a
provider, event bus, scheduler, tool, approval store, persistence layer, or
execution path. Setting either configuration flag does not activate a loop.

## Decision

The `internal/freewill` package owns the shape and structural invariants of
agent-originated intention requests and the legality of descriptive lifecycle
edges. It is deliberately pure. A valid request is not an authorized request;
a valid transition is not a policy decision. Runtime integrations must resolve
identity and scope from trusted server-owned context and pass every effect
through the existing policy and approval path.

Requests require an agent identity and origin, candidate source, title and
goal, a brief reason summary, one or more unique requested capability names,
project and workspace scope, idempotency and correlation identifiers, and an
optional resource budget. Unknown JSON fields are rejected. In particular,
permission or approval fields such as `approved`, `allow`, or `granted_scope`
are not part of this contract. Budget fields are pointers: absent means
unspecified, and an explicitly supplied value must be positive. FW-001 adds no
default limits and reserves or consumes no resources.

`freewill.enabled` and `freewill.scheduling.enabled` are independent booleans,
both false by default. They are schema only in this milestone. They do not
enable legacy `shell`/Freedom configuration, create a scheduler, or authorize
any action.

### Lifecycle edge contract

`ValidateTransition` checks only whether the following edge is part of the
documented state graph. It does not establish that an external condition,
approval, or permission has been met.

| From | Allowed next state | Additional authority required outside this validator |
| --- | --- | --- |
| `disabled` | `sleeping` | Runtime configuration and feature lifecycle |
| `sleeping` | `waking` | A future trusted wake source and deduplication |
| `waking` | `selecting`, `sleeping`, `halted` | Runtime-owned wake and halt checks |
| `selecting` | `proposed`, `sleeping`, `halted` | Candidate choice remains non-executable |
| `proposed` | `approval_pending`, `denied`, `paused`, `halted` | Existing policy and authenticated approval flow |
| `approval_pending` | `running`, `denied`, `paused`, `halted` | For `running`, current exact approval, policy, scope, budget, and execution gates |
| `paused` | `waking`, `halted` | Revalidation before any future resume |
| `running` | `paused`, `halted` | Runtime checkpoint, cancellation, and audit rules |
| `denied` | none | Terminal for this request version |
| `halted` | none | Terminal until a separately specified lifecycle exists |

New lifecycles begin in `disabled`. No edge is an implicit grant. Any future
change that adds transitions or behavior requires its own reviewed milestone.

### Threat model and required controls

| Threat | FW-001 contract response | Control required before a later feature uses it |
| --- | --- | --- |
| Cross-project or cross-user scope access | Scope is explicit and structurally required; request scope is untrusted | Bind to authenticated principal/project and enforce allowed roots in runtime |
| Agent-originated privilege escalation | Capabilities are requests only; unknown approval/permission keys fail decoding | Runtime policy computes effective capabilities; approvals bind exact immutable request and scope |
| Shell, path, or network escape | No executor, command, path resolver, or network API exists in this package | OS/worktree isolation and tool-specific path/network policy with escape tests |
| Fake or replayed user approval | No approval field or approval transition input exists in the request | Existing authenticated approval domain validates principal, exact request, expiry, and replay identity |
| Duplicate wake or intention delivery | Request carries idempotency and correlation identifiers | A future scheduler/store must enforce durable uniqueness and reconcile restart/replay |
| Runaway wake/resource use | FW-001 creates no wake source or work loop; omitted budget values remain unspecified and no spending defaults are invented | A later wake/runtime domain must apply explicit existing policy and hard ceilings before work; an agent-supplied budget is never authority |
| Crash or ambiguous side effect | This package has no persistence or effect API | Future runtime must checkpoint and reconcile; unknown effects must not be blindly retried |
| Privacy or prompt leakage | Only a bounded reason summary is represented; it is not private chain of thought | Apply existing access controls, retention, redaction, and consent rules to stored/audited data |

These entries identify risks and ownership; they do not claim the later controls
are implemented by FW-001.

## Package justification

Package: `internal/freewill`

Domain statement: owns the typed proposal contracts and structural lifecycle
invariants for Freewill intention requests.

Invariants owned:

- Required request identity, origin, requested scope, capability list, and
  correlation/idempotency identity;
- Strict contract decoding and rejection of unknown authority-bearing fields;
- Explicitly unspecified optional budgets and rejection of invalid supplied
  values; and
- The descriptive lifecycle state vocabulary and legal transition edges.

Primary callers: FW-001 contract/config tests; future policy or presentation
adapters may consume the DTO after separately validating trusted context.

Allowed dependencies: Go standard library only.

Responsibilities explicitly excluded: permission decisions, authorization,
approval, persistence, transport, scheduling, event delivery, provider/model
calls, tool selection, execution, budget reservation/consumption, and audit
storage.

Existing packages considered: `workflow`, `policy`, `approval`, `event`, and
`orchestrator`.

Why those packages do not own this domain: `workflow` owns executable workflow
transitions, `policy` owns authorization decisions, `approval` owns human
decisions, `event` owns envelopes, and `orchestrator` composes services. None
owns the distinct pre-execution vocabulary and structural invariants of an
agent-originated intention proposal. `internal/freewill` does not duplicate
those packages' authority.

Test boundary: pure JSON contract and transition tests prove malformed,
scope-less, unknown-authority, and illegal-edge inputs fail closed; config tests
prove both feature flags default off and round-trip independently. Integration
with policy, persistence, wake sources, or executors is intentionally absent.

## Consequences

- Later work has a stable, testable vocabulary without making autonomous
  behavior reachable in this release.
- Any future execution feature must use existing runtime gates and prove its
  additional safety prerequisites independently.
- The values and names in the DTO are an initial repository contract; they do
  not imply persisted wire compatibility until a later versioning decision.
