// schema_types.go defines the developer-authored workflow graph schema: the
// YAML/JSON contract a developer writes by hand to describe a role/outcome
// workflow, before it is validated (validator.go, PR2) and compiled into an
// immutable CompiledGraph (compiler.go, PR3). This file is purely a set of
// data types plus the two schema-identity constants; it performs no
// validation and no execution.
//
// WorkflowDefinition is distinct from the existing Definition type in
// config.go: Definition is Phase 1's fixed, four-role runtime contract.
// WorkflowDefinition is the open, developer-authored source format that a
// later PR compiles down to a shape the supervisor can run. Both types are
// kept alive side by side; see the Phase 3 plan's compatibility section.
package multiagent

// SchemaAPIVersion is the only apiVersion value LoadDefinition accepts in
// this pass. A mismatch is rejected immediately by the loader, before any
// further decoding is attempted.
const SchemaAPIVersion = "prism.dev/v1alpha1"

// SchemaKind is the only kind value LoadDefinition accepts in this pass.
const SchemaKind = "MultiAgentWorkflow"

// WorkflowDefinition is the root of an authored workflow graph document.
type WorkflowDefinition struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Metadata   WorkflowMetadata `json:"metadata"`
	Spec       WorkflowSpec     `json:"spec"`
}

// WorkflowMetadata identifies and describes a workflow definition.
type WorkflowMetadata struct {
	Name string `json:"name"`
	// ID defaults to Name when empty. That defaulting is applied by the
	// loader's normalizeDefaults, not here — this struct only describes the
	// decoded shape.
	ID string `json:"id,omitempty"`
	// Version is a user-facing string (e.g. "1.0.0"). It is stored as-is in
	// this pass and is not validated as semver; the registry (PR6) assigns
	// its own internal monotonic version separately.
	Version     string            `json:"version"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// WorkflowSpec is the executable body of a workflow definition: its entry
// point, budgets, cascading defaults, and the node/edge graph itself.
type WorkflowSpec struct {
	EntryNode string           `json:"entryNode"`
	Budgets   SchemaBudgets    `json:"budgets"`
	Defaults  WorkflowDefaults `json:"defaults,omitempty"`
	Nodes     []SchemaNode     `json:"nodes"`
	Edges     []SchemaEdge     `json:"edges"`
}

// WorkflowDefaults holds workflow-wide fallback policy that individual nodes
// and edges may omit and inherit. Applying these cascades onto specific
// nodes/edges is validator territory (PR2), not the loader's.
type WorkflowDefaults struct {
	Cancellation CancellationPolicy `json:"cancellation,omitempty"`
	Failure      FailurePolicy      `json:"failure,omitempty"`
	Approval     ApprovalDefaults   `json:"approval,omitempty"`
	Validation   ValidationDefaults `json:"validation,omitempty"`
}

// CancellationPolicy is a deliberately empty placeholder — no fields yet.
// It matches Phase 1's always-immediate cancellation behavior and exists so
// the schema has a stable extension point if per-workflow cancellation
// policy is ever needed, without a breaking schema change.
type CancellationPolicy struct{}

// FailurePolicy describes what happens when a node produces an outcome no
// edge handles. Only "fail" is meaningful in this pass; the validator (PR2)
// checks the value, the loader just decodes it.
type FailurePolicy struct {
	DefaultOnUnhandledOutcome string `json:"defaultOnUnhandledOutcome,omitempty"`
}

// ApprovalDefaults names workflow-wide default approvers, inherited by any
// node whose own approval policy does not set its own list.
type ApprovalDefaults struct {
	Approvers []string `json:"approvers,omitempty"`
}

// ValidationDefaults names workflow-wide default validation profiles,
// inherited by any node whose own validation policy does not set its own.
type ValidationDefaults struct {
	Profiles []string `json:"profiles,omitempty"`
}

// SchemaBudgets bounds the whole run. Per-node limits on SchemaNode may be
// stricter; whether a stricter node limit and a looser global one is an
// error, a warning, or fine is the validator's decision, not this type's.
type SchemaBudgets struct {
	MaxTransitions     *SchemaLimit `json:"maxTransitions,omitempty"`
	MaxTokens          *SchemaLimit `json:"maxTokens,omitempty"`
	MaxLocalIterations *SchemaLimit `json:"maxLocalIterations,omitempty"`
	MaxRetries         *SchemaLimit `json:"maxRetries,omitempty"`
	// MaxDuration is a Go duration string (e.g. "30m"). "-1" means
	// unlimited. It is stored as a string and left unparsed here; parsing
	// and range-checking belong to the validator.
	MaxDuration string `json:"maxDuration,omitempty"`
}

// SchemaLimit decodes a JSON/YAML integer budget value. -1 means unlimited,
// mirroring the sentinel convention Limit already establishes in types.go.
// This is a distinct schema-layer type, not a reuse of Limit: Limit is a
// runtime/compiled-layer concept, and keeping the authored-schema type
// separate means a future change to one does not silently change the other's
// wire shape.
type SchemaLimit int

// SchemaNode is one node (a role step or a terminal) in the authored graph.
type SchemaNode struct {
	ID string `json:"id"`
	// Type is "role" (the default when empty) or "terminal".
	Type            string   `json:"type,omitempty"`
	Role            string   `json:"role,omitempty"`
	DisplayName     string   `json:"displayName,omitempty"`
	Description     string   `json:"description,omitempty"`
	AgentProfile    string   `json:"agentProfile,omitempty"` // maps 1:1 to the existing RoleConfig.AgentRef
	AllowedTools    []string `json:"allowedTools,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	AllowedOutcomes []string `json:"allowedOutcomes,omitempty"`

	MaxVisits       *SchemaLimit `json:"maxVisits,omitempty"`
	LocalIterations *SchemaLimit `json:"localIterations,omitempty"`
	TokenBudget     *SchemaLimit `json:"tokenBudget,omitempty"`
	DurationBudget  string       `json:"durationBudget,omitempty"`

	Retry      *SchemaRetryPolicy      `json:"retry,omitempty"`
	Approval   *SchemaApprovalPolicy   `json:"approval,omitempty"`
	Validation *SchemaValidationPolicy `json:"validation,omitempty"`

	// TerminalCondition is only meaningful when Type == "terminal".
	TerminalCondition string `json:"terminalCondition,omitempty"`

	InputContract  *SchemaIOContract `json:"inputContract,omitempty"`
	OutputContract *SchemaIOContract `json:"outputContract,omitempty"`
}

// SchemaIOContract documents (but, in this pass, does not enforce) the
// expected shape of a node's input or output artifacts.
type SchemaIOContract struct {
	Description string   `json:"description,omitempty"`
	Artifacts   []string `json:"artifacts,omitempty"`
}

// SchemaRetryPolicy configures per-node retry behavior. Duration fields are
// Go duration strings, parsed and range-checked by the validator, not here.
type SchemaRetryPolicy struct {
	MaxRetries int    `json:"maxRetries"`
	BaseDelay  string `json:"baseDelay,omitempty"`
	MaxDelay   string `json:"maxDelay,omitempty"`
	Jitter     string `json:"jitter,omitempty"`
}

// SchemaApprovalPolicy configures whether a node requires human approval
// before it may proceed, and who may grant it.
type SchemaApprovalPolicy struct {
	Required  bool     `json:"required"`
	Approvers []string `json:"approvers,omitempty"`
}

// SchemaValidationPolicy names the validation profiles a node's output must
// pass. Profile existence/resolution is a runtime concern, not the loader's.
type SchemaValidationPolicy struct {
	Profiles []string `json:"profiles,omitempty"`
}

// SchemaEdge is one routing rule in the authored graph: from a node, on a
// given outcome, to another node (or implicitly terminal via the target
// node's own type).
type SchemaEdge struct {
	ID       string            `json:"id"`
	From     string            `json:"from"`
	To       string            `json:"to,omitempty"`
	When     SchemaCondition   `json:"when"`
	Priority int               `json:"priority,omitempty"`
	Loop     *SchemaLoopPolicy `json:"loop,omitempty"`
	Label    string            `json:"label,omitempty"`
}

// SchemaCondition is outcome-only in this pass. Routing decision #3 (see the
// Phase 3 plan) deliberately cut deterministic condition guards beyond a
// plain outcome match to keep the schema free of anything resembling an
// expression language. Do not add an "If" field or a condition-term list
// here without revisiting that decision.
type SchemaCondition struct {
	Outcome string `json:"outcome"`
}

// SchemaLoopPolicy declares that an edge may be traversed repeatedly (i.e.
// participates in a correction loop) and bounds how many times.
type SchemaLoopPolicy struct {
	MaxTraversals int `json:"maxTraversals"`
	// OnExhausted is "fail" or "route_to". The validator (PR2) checks the
	// enum and, for "route_to", that RouteTo escapes the loop's cycle.
	OnExhausted string `json:"onExhausted"`
	RouteTo     string `json:"routeTo,omitempty"`
}
