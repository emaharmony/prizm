# Security Review (PR7 — Final Hardening Pass)

This page verifies every claim in the Phase 3 plan's "Security" section
against the **actual merged code**, not the plan's stated intent — the plan
explicitly asked for this, and two of the checks below found real gaps that
a re-assertion of intent would have missed. Each claim is checked
independently; findings that required action are marked and cross-linked to
what was actually done.

## Claims checked

### 1. No arbitrary code execution in the loader/validator/compiler/CLI path

**Verified — pass.** Scoped `grep` for `os/exec`, `exec.Command`,
`text/template`, `html/template`, the `plugin` package, and every
scripting-engine dependency named in the plan (`goja`, `otto`, `starlark`,
`cel-go`) across `internal/workflow/multiagent/*.go` returns **zero
matches**. The same grep against `cmd/prism-cli` finds matches only in six
pre-existing, unrelated files (`cmd_doctor.go`'s `gh auth status` check,
`cmd_panel.go`'s panel-binary launcher, `cmd_remembrance.go`'s Python
subprocess, `wake_handler.go`'s git/gh integration,
`workflow_feedback_notifier.go`'s `git log`, and `cmd_serve.go`'s import for
an unrelated feature) — **none** of the five `cmd_graph_*.go` files
(`cmd_graph_run.go`, `cmd_graph_validate.go`, `cmd_graph_compile.go`,
`cmd_graph_inspect.go`, `cmd_graph_testcmd.go`) or `cmd_graph_common.go`
match at all. The loader/validator/compiler path uses only
`yaml.Unmarshal`/`encoding/json`.

### 2. `SchemaCondition` is still outcome-only

**Verified — pass.** `schema_types.go`'s `SchemaCondition` is still exactly
`{Outcome string}`. No `If` field or condition-term list was added in any
later PR. See [Routing and outcomes](routing-and-outcomes.md).

### 3. `DisallowUnknownFields()` is still in effect on both decode paths

**Verified — pass.** `loader.go`'s `decodeStrictWorkflowJSON` (used by both
`loadYAML` and `loadJSON`) still calls
`json.NewDecoder(...).DisallowUnknownFields()`, unchanged since PR1.
`scenario_types.go`'s `decodeStrictScenarioJSON` does the same for scenario
fixtures. `internal/api/multiagent_definitions.go`'s `readDefinitionBody`
routes every request body through `LoadDefinitionBytes` → the same strict
path — no alternate, more permissive decoder was introduced for the HTTP
surface.

One nuance worth recording explicitly: `definition_store.go`'s `Register`
marshals an **already-decoded, already-validated** `WorkflowDefinition` Go
struct (`json.Marshal(wf)`) into `definition_json`, and `getWhere` reads it
back with a plain `json.Unmarshal` (no `DisallowUnknownFields`). This is
**not** a bypass: the bytes being unmarshaled were produced by marshaling a
Go struct with a fixed, known field set, not arbitrary user-supplied text —
there is no code path where the strict decode is skipped for content that
actually originated outside `LoadDefinition`/`LoadDefinitionBytes`. The
registry has no `UPDATE` path and no way to insert a hand-edited row through
its own API, so this stays inside the trusted boundary by construction, not
by convention.

### 4. No schema field holds or is documented as holding a literal secret

**Verified — pass.** Re-scanned every field in `schema_types.go`:
`agentProfile`, `allowedTools`, `capabilities`, `approvers`,
`validation.profiles` are all **names/references**, resolved against
Prism's existing registries at run time — never a literal credential value.
This is consistent with the plan's own stated rationale for omitting
env-var substitution entirely (see
[Current limitations](limitations.md#no-environment-variable-substitution)).

### 5. Registration is genuinely gated by the existing bearer-token auth mechanism

**Re-verified independently — pass** (not just trusted from PR6's report).
`internal/api/server.go`'s `requiresAuth(r)` returns `true` for
`http.MethodPost` (among other mutating methods) unconditionally, and both
`POST /api/v1/multiagent/definitions` (register) and
`POST /api/v1/multiagent/definitions/{id}/versions/{n}/run` (start a run)
are `POST` routes — `authMiddleware` gates them behind
`s.authToken != "" && requiresAuth(r) && !s.authorized(r)` exactly like
every other mutating route, with zero special-casing for the definitions
prefix. Read-only `GET` routes (list workflows, list versions, get
definition/compiled/fingerprint) remain open, matching the existing,
documented convention for read-only inspection endpoints elsewhere in this
API.

### 6. Diagnostics never leak sensitive data from a malformed definition

**Verified — pass**, with the caveat that this claim is almost vacuous
given claim 4: since no schema field can hold a secret in the first place,
there is nothing sensitive for a diagnostic message to leak even in the
worst case. Spot-checked several `Diagnostic` construction sites in
`validator.go` and `loader.go`: messages reference node/edge/outcome/field
**names** the author themselves supplied (their own IDs and identifiers),
never a dump of full node/edge content, and `diagnosticFromDecodeError`
surfaces Go's own `DisallowUnknownFields` error text (`json: unknown field
"foo"`), which names only the offending field, not its value.

### 7. `DefinitionTooLargeError` fires before expensive validation work

**Verified — pass.** `compiler.go`'s `Compile` checks `len(def.Spec.Nodes) >
opts.MaxNodes` and `len(def.Spec.Edges) > opts.MaxEdges` and returns
immediately on either before `ValidateDefinition` (and therefore before
Tarjan SCC cycle detection) is ever called — confirmed by reading the
function top to bottom, not by re-asserting the plan's description of it.

## Findings requiring action

### Finding: `loop.onExhausted: route_to` is schema-validated but not runtime-enforced

**Status: reported, not silently patched — see rationale below.**

While building `templates/security-review.yaml`'s bounded correction loop,
authoring `onExhausted: route_to` compiled and validated cleanly, but
`Supervisor.checkLoopBudget` (`supervisor.go`) never reads
`loop.OnExhausted`/`loop.RouteTo` — confirmed by reading the function and by
an exhaustive grep for `RouteTo`/`route_to` across every `*.go` file in
`internal/workflow/multiagent`, which turns up references only in
`compiler.go` (compiling the field into `CompiledLoop`), `validator.go`
(checking it), and `cycle_detection_test.go` (validator-only tests) — zero
references in `supervisor.go` or `durable_runtime.go`. Every loop exhaustion
ends the run with a budget-exhausted status today, identical to
`onExhausted: fail`, regardless of the declared value.

This is a genuine gap, not a small fix: implementing it correctly requires
deciding what happens to the in-flight handoff on a forced exit, what event
fires, and how a `routeTo` target that is a **terminal** node (not just
another role) should be represented — `CompiledLoop.RouteTo` is currently
typed as a plain `Role`, and `compiler.go`'s `roleForNode` would silently
fabricate a bogus `Role` from a terminal node's ID if a compiled loop's
`RouteTo` were ever actually consulted against one. That is a design
question spanning the compiler and the supervisor, not something this PR's
scope covers safely. **Both shipped templates were changed to use
`onExhausted: fail`** instead of relying on unimplemented behavior — see
[Loops and cycles](loops-and-cycles.md#onexhausted-route_to-is-validated-but-not-yet-runtime-enforced)
and [Current limitations](limitations.md#looponexhausted-route_to-is-not-runtime-enforced).

### Finding: custom terminal conditions could never actually finish a run — found and fixed

**Status: fixed in this PR.**

`Supervisor.finishTerminal` hardcoded a switch over exactly Phase 1's three
legacy terminal conditions (`completed`/`cancelled`/`failed`); anything
else fell into a `default` case that rejected the transition with a
spurious `*InvalidTransitionError`. This directly contradicts the schema's
own design — `SchemaNode.TerminalCondition` has no enum anywhere in the
validator or compiler, and is documented as an open, developer-chosen
value — and was discovered concretely, not hypothetically:
`templates/documentation-change.yaml`'s very first scripted scenario
(`editor` → `published` on `ready_to_publish`) failed with
`"multiagent: no transition from role \"editor\" for outcome
\"ready_to_publish\""`, even though `prism graph inspect` on the same file
showed the transition compiled correctly.

**Fix applied**: `finishTerminal`'s `default` case now calls a new
`completeRunWithCondition(state, transition.Terminal)`, which completes the
run successfully (`RunStatusCompleted`) carrying the workflow author's
**actual** declared terminal condition string (not a hardcoded
`"completed"`) and a generic, condition-derived reason. The three existing
legacy cases (`completed`/`cancelled`/`failed`) are **completely
untouched** — `completeRun()` (used only by the literal
`TerminalConditionCompleted` case) still produces byte-identical output,
which matters because `e2e_dashboard_scenario_test.go` asserts its exact
`Reason` string (`"review approved"`) literally.

This qualified as a small, safe, additive fix rather than a design question
to merely report, for three reasons: (1) the prior behavior was
**unconditionally broken** for every custom terminal condition — there was
no existing behavior to regress, only a new default case added where one
previously always errored; (2) the chosen semantics (reaching a
declared, validated, routed terminal via a real edge is a successful,
designed outcome) is the least surprising available default, and a workflow
author who wants failure semantics for a given terminal already has the
tool for it — name that terminal's condition `"failed"`; (3) it is required
for this PR's own shipped templates to work at all —
`templates/security-review.yaml`'s `needs-human-review` terminal and
`templates/documentation-change.yaml`'s `published`/`needs-revision`
terminals all depend on it. All three shipped templates and their scenario
fixtures now exercise this path, including a dedicated regression scenario
proving it (`security-review-happy-path.yaml`'s
`critical-finding-escapes-to-human-review`, which reaches
`node:terminal:needs_human_review`).

See `supervisor.go`'s `finishTerminal`/`completeRunWithCondition` doc
comments for the in-code version of this rationale, and
[Current limitations](limitations.md#custom-terminal-conditions-previously-could-not-finish-a-run--found-and-fixed-in-this-pr).

## What this review did not re-litigate

Every claim above was checked against code that PR1–PR6 already wrote and
merged; this review did not re-derive Phase 3's overall security posture
from scratch, and it does not cover general Prism security topics (auth
token generation/rotation, TLS termination, SQLite file permissions) that
are unrelated to this feature and already documented elsewhere
(`SECURITY.md`, [Safety & Policy](../concepts/SAFETY.md)).
