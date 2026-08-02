// compiler.go implements PR3 of the Phase 3 "Developer-Authored Workflow
// Graph Runtime" initiative: turning a WorkflowDefinition (schema_types.go)
// that has already passed ValidateDefinition (validator.go, PR2) into an
// immutable CompiledGraph — the runtime representation the supervisor will
// execute starting in PR4.
//
// Compile is the *only* constructor for CompiledGraph. Every CompiledGraph
// field is unexported and there are no setters, so a CompiledGraph is
// structurally impossible to mutate after construction; every accessor that
// exposes a slice or map returns an independent defensive copy.
//
// The single most important reuse requirement in this file: node/edge IDs
// are produced by calling graph_projection.go's existing unexported
// nodeID/terminalNodeID/edgeID functions directly (same package), not by
// reimplementing the "node:<role>" / "node:terminal:<condition>" /
// "edge:<from>:<outcome>" string format locally. Phase 2's dashboard read
// model depends on the compiled path producing byte-identical IDs to what
// graph_projection.go already produces for the legacy Definition path.
package multiagent

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/emaharmony/prism/internal/checksum"
	"github.com/emaharmony/prism/internal/retry"
)

// Default CompileOptions size caps, used whenever the corresponding
// CompileOptions field is zero.
const (
	defaultMaxNodes       = 500
	defaultMaxEdges       = 2000
	defaultMaxDiagnostics = 1000
)

// transitionKey indexes CompiledGraph.outgoing (and, before PR4, indexed
// resolver.go's now-deleted TransitionResolver.routes). Moved here from the
// deleted resolver.go: CompiledGraph absorbed TransitionResolver entirely in
// PR4, so this is the only remaining place that needs the type. Deliberately
// typed; free-form runner content never participates in route selection.
type transitionKey struct {
	role    Role
	outcome TransitionOutcome
}

// CompiledGraph is the immutable runtime representation the supervisor
// executes (starting in PR4). It is never re-derived from YAML at run time:
// Compile is the only constructor, all fields are unexported, and there are
// no setters — structurally impossible to mutate after construction.
type CompiledGraph struct {
	workflowID      string
	workflowVersion string // metadata.version, user-facing string
	schemaVersion   string // apiVersion
	fingerprint     string
	entryNodeID     string

	// registryVersion is the definition registry's internally assigned
	// integer version (the plan's "RunState.WorkflowVersion becomes a
	// registry-assigned internal version" repurposing). No definition
	// registry exists yet in this PR (that is PR6's job — definition_store.go
	// in the Phase 3 plan), so a graph built directly by Compile (the new
	// authored-YAML path, not yet registered) leaves this at its zero value,
	// 0, as an explicit placeholder. CompatAdaptDefinition (compat.go) is the
	// one exception: it sets this to the legacy Definition's own Version
	// field, so RunState.Validate's workflow-version-equality check continues
	// to agree exactly with every already-persisted historical run (which
	// stamped RunState.WorkflowVersion from Definition.Version before this
	// PR) — this is a small, deliberate addition PR3 did not anticipate,
	// added here because PR4's backward-compat requirement cannot be met
	// without it. See RegistryVersion's accessor doc and PR4's report for the
	// full rationale.
	registryVersion int

	// nodeOrder/edgeOrder preserve source declaration order, independent of
	// Go's randomized map iteration order, so accessors (and the fingerprint
	// computation) are deterministic.
	nodeOrder []string
	edgeOrder []string
	loopOrder []LoopKind

	nodes map[string]CompiledNode // keyed by stable node ID (node:<role> / node:terminal:<condition>)
	edges map[string]CompiledEdge // keyed by stable edge ID (edge:<from>:<outcome>)

	// outgoing is the "outgoing transition table" — reuses transitionKey
	// from resolver.go (same package, not redefined here).
	outgoing map[transitionKey]ResolvedTransition

	// outcomeIndex records which outcomes each role may legally return,
	// derived from each role node's declared AllowedOutcomes.
	outcomeIndex map[Role]map[TransitionOutcome]bool

	terminalNodeIDs []string

	// loops is keyed by the owning edge's stable ID (used AS the LoopKind
	// value), per the plan's "LoopKind for a developer-defined loop equals
	// that edge's own stable ID" convention.
	loops map[LoopKind]CompiledLoop

	budgets BudgetLimits       // reuses the existing BudgetLimits struct from config.go, unmodified
	source  WorkflowDefinition // retained for later CLI `inspect` use (PR5) — not consulted by the supervisor
}

// CompiledNode is one stable-ID vertex of a CompiledGraph: either a
// configured role step or a terminal condition.
type CompiledNode struct {
	ID                string              `json:"id"`
	Role              Role                `json:"role,omitempty"`
	Kind              string              `json:"kind"` // "role" | "terminal"
	DisplayName       string              `json:"display_name,omitempty"`
	RoleConfig        RoleConfig          `json:"role_config"`
	AllowedOutcomes   []TransitionOutcome `json:"allowed_outcomes,omitempty"`
	TerminalCondition TerminalCondition   `json:"terminal_condition,omitempty"`
}

// CompiledEdge is one stable-ID transition rule of a CompiledGraph.
type CompiledEdge struct {
	ID       string            `json:"id"`
	From     Role              `json:"from"`
	Outcome  TransitionOutcome `json:"outcome"`
	To       Role              `json:"to,omitempty"`
	Terminal TerminalCondition `json:"terminal,omitempty"` // set when the edge's destination node has Kind == "terminal"
	Priority int               `json:"priority,omitempty"`
	Label    string            `json:"label,omitempty"`
}

// LoopExhaustionBehavior is what happens once a CompiledLoop's MaxTraversals
// bound is reached.
type LoopExhaustionBehavior string

const (
	LoopExhaustedFail    LoopExhaustionBehavior = "fail"
	LoopExhaustedRouteTo LoopExhaustionBehavior = "route_to"
)

// CompiledLoop is the precomputed bound on one correction-loop edge.
type CompiledLoop struct {
	Kind          LoopKind               `json:"kind"`
	EdgeID        string                 `json:"edge_id"`
	MaxTraversals Limit                  `json:"max_traversals"`
	OnExhausted   LoopExhaustionBehavior `json:"on_exhausted"`
	RouteTo       Role                   `json:"route_to,omitempty"` // only meaningful when OnExhausted == LoopExhaustedRouteTo
}

// CompileOptions overrides Compile's default hard size caps. A zero-value
// CompileOptions{} uses the package defaults (500 nodes, 2000 edges, 1000
// diagnostics).
type CompileOptions struct {
	MaxNodes       int // 0 => default 500
	MaxEdges       int // 0 => default 2000
	MaxDiagnostics int // 0 => default 1000
}

// resolvedOptions fills in default values for every zero field.
func (o CompileOptions) resolved() CompileOptions {
	if o.MaxNodes <= 0 {
		o.MaxNodes = defaultMaxNodes
	}
	if o.MaxEdges <= 0 {
		o.MaxEdges = defaultMaxEdges
	}
	if o.MaxDiagnostics <= 0 {
		o.MaxDiagnostics = defaultMaxDiagnostics
	}
	return o
}

// Compile validates def itself (via ValidateDefinition — callers never call
// both) and, only if there are zero error-severity diagnostics, builds an
// immutable CompiledGraph. Warnings do not block compilation but are always
// returned alongside the graph. idx may be nil. opts may be a zero-value
// CompileOptions{} to use defaults.
//
// Size caps (MaxNodes/MaxEdges) are enforced BEFORE ValidateDefinition is
// ever called: a definition exceeding either cap is rejected immediately
// with a single DefinitionTooLargeError, without running the validator's
// more expensive passes (in particular Tarjan SCC cycle detection) against
// pathologically large input.
func Compile(def WorkflowDefinition, idx *PositionIndex, opts CompileOptions) (*CompiledGraph, Diagnostics, error) {
	opts = opts.resolved()

	if len(def.Spec.Nodes) > opts.MaxNodes {
		err := &DefinitionTooLargeError{Dimension: "nodes", Limit: opts.MaxNodes, Got: len(def.Spec.Nodes)}
		return nil, Diagnostics{{
			Severity: SeverityError,
			Rule:     "compile.definition-too-large",
			Message:  err.Error(),
		}}, err
	}
	if len(def.Spec.Edges) > opts.MaxEdges {
		err := &DefinitionTooLargeError{Dimension: "edges", Limit: opts.MaxEdges, Got: len(def.Spec.Edges)}
		return nil, Diagnostics{{
			Severity: SeverityError,
			Rule:     "compile.definition-too-large",
			Message:  err.Error(),
		}}, err
	}

	diags := ValidateDefinition(def, idx)
	hasErrors := diags.HasErrors()
	if len(diags) > opts.MaxDiagnostics {
		diags = diags[:opts.MaxDiagnostics]
	}
	if hasErrors {
		return nil, diags, &CompilationFailedError{Diagnostics: diags}
	}

	graph := buildCompiledGraph(def)
	return graph, diags, nil
}

// ---------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------

// buildCompiledGraph assumes def has already passed ValidateDefinition with
// zero error-severity diagnostics: every node/edge reference it dereferences
// here is guaranteed to resolve.
func buildCompiledGraph(def WorkflowDefinition) *CompiledGraph {
	nodeByID := buildNodeByID(def) // reused from validator.go, same package

	nodeOrder := make([]string, 0, len(def.Spec.Nodes))
	nodes := make(map[string]CompiledNode, len(def.Spec.Nodes))
	outcomeIndex := make(map[Role]map[TransitionOutcome]bool, len(def.Spec.Nodes))
	var terminalNodeIDs []string
	visitLimits := make(map[Role]Limit, len(def.Spec.Nodes))

	for _, n := range def.Spec.Nodes {
		kind := normalizedNodeType(n) // reused from validator.go
		var id string
		var role Role
		var terminalCond TerminalCondition
		var roleConfig RoleConfig

		if kind == "terminal" {
			terminalCond = TerminalCondition(n.TerminalCondition)
			id = terminalNodeID(terminalCond) // graph_projection.go's existing function
			terminalNodeIDs = append(terminalNodeIDs, id)
		} else {
			role = roleForNode(n)
			id = nodeID(role) // graph_projection.go's existing function
			roleConfig = buildRoleConfig(role, n)
			outcomeIndex[role] = outcomeSet(n.AllowedOutcomes)
			if n.MaxVisits != nil {
				visitLimits[role] = Limit(*n.MaxVisits)
			} else {
				visitLimits[role] = Unlimited
			}
		}

		allowedOutcomes := make([]TransitionOutcome, len(n.AllowedOutcomes))
		for i, oc := range n.AllowedOutcomes {
			allowedOutcomes[i] = TransitionOutcome(oc)
		}

		nodeOrder = append(nodeOrder, id)
		nodes[id] = CompiledNode{
			ID:                id,
			Role:              role,
			Kind:              kind,
			DisplayName:       n.DisplayName,
			RoleConfig:        roleConfig,
			AllowedOutcomes:   allowedOutcomes,
			TerminalCondition: terminalCond,
		}
	}

	edgeOrder := make([]string, 0, len(def.Spec.Edges))
	edges := make(map[string]CompiledEdge, len(def.Spec.Edges))
	outgoing := make(map[transitionKey]ResolvedTransition, len(def.Spec.Edges))
	loopOrder := make([]LoopKind, 0)
	loops := make(map[LoopKind]CompiledLoop)

	for _, e := range def.Spec.Edges {
		fromNode := nodeByID[e.From]
		fromRole := roleForNode(fromNode)
		outcome := TransitionOutcome(e.When.Outcome)
		id := edgeID(fromRole, outcome) // graph_projection.go's existing function

		destNode := nodeByID[e.To]
		var to Role
		var terminal TerminalCondition
		if normalizedNodeType(destNode) == "terminal" {
			terminal = TerminalCondition(destNode.TerminalCondition)
		} else {
			to = roleForNode(destNode)
		}

		edgeOrder = append(edgeOrder, id)
		edges[id] = CompiledEdge{
			ID:       id,
			From:     fromRole,
			Outcome:  outcome,
			To:       to,
			Terminal: terminal,
			Priority: e.Priority,
			Label:    e.Label,
		}
		outgoing[transitionKey{role: fromRole, outcome: outcome}] = ResolvedTransition{
			From:     fromRole,
			Outcome:  outcome,
			To:       to,
			Terminal: terminal,
		}

		if e.Loop != nil {
			kind := LoopKind(id)
			var routeTo Role
			onExhausted := LoopExhaustedFail
			if e.Loop.OnExhausted == string(LoopExhaustedRouteTo) {
				onExhausted = LoopExhaustedRouteTo
				routeTo = roleForNode(nodeByID[e.Loop.RouteTo])
			}
			loopOrder = append(loopOrder, kind)
			loops[kind] = CompiledLoop{
				Kind:          kind,
				EdgeID:        id,
				MaxTraversals: Limit(e.Loop.MaxTraversals),
				OnExhausted:   onExhausted,
				RouteTo:       routeTo,
			}
		}
	}

	budgets := buildBudgets(def.Spec.Budgets, visitLimits)

	workflowID := strings.TrimSpace(def.Metadata.ID)
	if workflowID == "" {
		// The loader's normalizeDefaults ordinarily fills Metadata.ID from
		// Metadata.Name before validation ever runs; this fallback only
		// matters for a hand-built WorkflowDefinition that skipped the
		// loader entirely (e.g. constructed directly by a test).
		workflowID = def.Metadata.Name
	}

	entryNode := nodeByID[def.Spec.EntryNode]
	var entryNodeID string
	if normalizedNodeType(entryNode) == "terminal" {
		entryNodeID = terminalNodeID(TerminalCondition(entryNode.TerminalCondition))
	} else {
		entryNodeID = nodeID(roleForNode(entryNode))
	}

	graph := &CompiledGraph{
		workflowID:      workflowID,
		workflowVersion: def.Metadata.Version,
		schemaVersion:   def.APIVersion,
		entryNodeID:     entryNodeID,
		nodeOrder:       nodeOrder,
		edgeOrder:       edgeOrder,
		loopOrder:       loopOrder,
		nodes:           nodes,
		edges:           edges,
		outgoing:        outgoing,
		outcomeIndex:    outcomeIndex,
		terminalNodeIDs: terminalNodeIDs,
		loops:           loops,
		budgets:         budgets,
		source:          def,
	}
	graph.fingerprint = computeFingerprint(graph)
	return graph
}

// roleForNode returns the node's declared Role, falling back to its ID when
// Role is empty. Neither PR1 nor PR2 established an explicit convention for
// this defaulting (SchemaNode.Role's doc comment does not mention a
// fallback), so this compiler establishes one: an omitted Role defaults to
// the node's own ID, mirroring the same "ID doubles as the identifier when a
// more specific name is omitted" pattern WorkflowMetadata.ID already uses
// relative to WorkflowMetadata.Name.
func roleForNode(n SchemaNode) Role {
	if n.Role != "" {
		return Role(n.Role)
	}
	return Role(n.ID)
}

func outcomeSet(outcomes []string) map[TransitionOutcome]bool {
	set := make(map[TransitionOutcome]bool, len(outcomes))
	for _, oc := range outcomes {
		set[TransitionOutcome(oc)] = true
	}
	return set
}

// buildRoleConfig maps a SchemaNode's role-level fields onto the existing
// RoleConfig shape (config.go).
//
// Field-mapping note: RoleConfig has no per-node "max visits" field of its
// own — that concept lives on the legacy BudgetLimits.MaxVisitsPerRole map,
// keyed by Role. SchemaNode.MaxVisits is therefore NOT mapped into
// RoleConfig here; buildCompiledGraph instead folds it into
// CompiledGraph.budgets.MaxVisitsPerRole (see buildBudgets), matching where
// the equivalent concept already lives in the existing model.
// SchemaNode.LocalIterations and SchemaNode.TokenBudget, by contrast, DO
// have a direct RoleConfig home (MaxLocalIterations/TokenBudget) and are
// mapped directly.
func buildRoleConfig(role Role, n SchemaNode) RoleConfig {
	rc := RoleConfig{
		Role:               role,
		AgentRef:           n.AgentProfile,
		AllowedTools:       cloneStrings(n.AllowedTools),
		Capabilities:       cloneStrings(n.Capabilities),
		MaxLocalIterations: schemaLimitToLimit(n.LocalIterations),
		TokenBudget:        schemaLimitToLimit(n.TokenBudget),
		TimeBudget:         parseDurationOrUnlimited(n.DurationBudget),
	}

	if n.Approval != nil {
		approvers := make([]Role, len(n.Approval.Approvers))
		for i, a := range n.Approval.Approvers {
			approvers[i] = Role(a)
		}
		rc.Approval = ApprovalRequirement{Required: n.Approval.Required, Approvers: approvers}
	}

	if n.Validation != nil {
		rc.ValidationProfiles = cloneStrings(n.Validation.Profiles)
	}

	if n.Retry != nil {
		rc.Retry = retry.RetryConfig{
			MaxRetries: n.Retry.MaxRetries,
			BaseDelay:  parseDurationOrZero(n.Retry.BaseDelay),
			MaxDelay:   parseDurationOrZero(n.Retry.MaxDelay),
			Jitter:     parseDurationOrZero(n.Retry.Jitter),
		}
	}

	return rc
}

// buildBudgets maps SchemaBudgets onto the existing BudgetLimits shape
// (config.go).
//
// Per the plan, the two Phase-1-specific fields
// MaxTesterToDeveloperLoops/MaxReviewerToDeveloperLoops are deliberately
// NOT populated here — they stay at their zero value for every
// new-schema-compiled graph. Only compatAdaptDefinition (PR4/legacy path)
// ever reads or writes those two fields; loop limits for a compiled
// WorkflowDefinition graph live per-edge in CompiledGraph.loops instead.
//
// MaxRepeatedFailure has no SchemaBudgets equivalent in this pass (the
// authored schema has no concept of "repeated identical failures" as a
// distinct budget dimension), so it is set to Unlimited rather than left at
// the zero value: Limit's own convention treats 0 as an invalid sentinel
// (see Limit.Valid in types.go), and defaulting an unpopulated dimension to
// "no bound" is safer than defaulting it to a value that reads as "already
// exhausted."
func buildBudgets(b SchemaBudgets, visitLimits map[Role]Limit) BudgetLimits {
	maxVisitsPerRole := make(map[Role]Limit, len(visitLimits))
	for role, limit := range visitLimits {
		maxVisitsPerRole[role] = limit
	}
	return BudgetLimits{
		MaxTransitions:              schemaLimitToLimit(b.MaxTransitions),
		MaxVisitsPerRole:            maxVisitsPerRole,
		MaxLocalIterations:          schemaLimitToLimit(b.MaxLocalIterations),
		MaxRetries:                  schemaLimitToLimit(b.MaxRetries),
		MaxTokens:                   schemaLimitToLimit(b.MaxTokens),
		MaxRepeatedFailure:          Unlimited,
		MaxTesterToDeveloperLoops:   0,
		MaxReviewerToDeveloperLoops: 0,
		MaxDuration:                 parseDurationOrUnlimited(b.MaxDuration),
	}
}

// schemaLimitToLimit converts an optional SchemaLimit into the existing
// Limit sentinel convention: an unset (nil) SchemaLimit means "no bound was
// authored," which maps to Unlimited, not 0 (0 is Limit's own invalid
// sentinel — see Limit.Valid in types.go).
func schemaLimitToLimit(l *SchemaLimit) Limit {
	if l == nil {
		return Unlimited
	}
	return Limit(*l)
}

// parseDurationOrUnlimited parses a Go duration string using the same "-1
// means unlimited" convention SchemaBudgets.MaxDuration's doc comment
// establishes. An empty string (never authored) and the literal "-1" both
// map to UnlimitedDuration. validateSchemaBudgets (validator.go) does not
// itself check maxDuration/durationBudget's format — by the time Compile
// reaches this helper, def has already passed validation, but a malformed
// string is not actually impossible (this dimension is unchecked), so a
// parse failure falls back to UnlimitedDuration rather than panicking or
// silently producing a zero (invalid, per validDuration in types.go)
// duration.
func parseDurationOrUnlimited(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" || s == "-1" {
		return UnlimitedDuration
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return UnlimitedDuration
	}
	return d
}

// parseDurationOrZero parses a retry-policy duration field
// (SchemaRetryPolicy.BaseDelay/MaxDelay/Jitter). Unlike budget/time-budget
// durations, retry.RetryConfig has no "-1 means unlimited" sentinel — its
// zero value already means "no delay" — so an empty or unparseable string
// simply maps to 0, matching retry.RetryConfig's own zero-value semantics.
func parseDurationOrZero(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

func cloneOutcomes(s []TransitionOutcome) []TransitionOutcome {
	if s == nil {
		return nil
	}
	out := make([]TransitionOutcome, len(s))
	copy(out, s)
	return out
}

// cloneRoleConfig (supervisor.go) already provides the exact independent-copy
// semantics needed here — every slice field gets a fresh backing array — so
// it is reused as-is rather than redefined.

// cloneCompiledNode returns an independent copy of n: every slice field
// (directly, and within its embedded RoleConfig) gets a fresh backing array.
func cloneCompiledNode(n CompiledNode) CompiledNode {
	n.AllowedOutcomes = cloneOutcomes(n.AllowedOutcomes)
	n.RoleConfig = cloneRoleConfig(n.RoleConfig)
	return n
}

// cloneBudgetLimits returns an independent copy of b: MaxVisitsPerRole gets
// a fresh backing map.
func cloneBudgetLimits(b BudgetLimits) BudgetLimits {
	if b.MaxVisitsPerRole != nil {
		cloned := make(map[Role]Limit, len(b.MaxVisitsPerRole))
		for role, limit := range b.MaxVisitsPerRole {
			cloned[role] = limit
		}
		b.MaxVisitsPerRole = cloned
	}
	return b
}

// ---------------------------------------------------------------------
// Fingerprint
// ---------------------------------------------------------------------

// fingerprintPayload is the canonical, deterministic encoding
// computeFingerprint hashes. It is built exclusively from the graph's
// already-ordered node/edge/loop slices (nodeOrder/edgeOrder/loopOrder), not
// by ranging graph.nodes/graph.edges/graph.loops directly — Go map
// iteration order is randomized per-process, which would make the
// fingerprint non-deterministic across repeated compiles of identical
// input. BudgetLimits.MaxVisitsPerRole is the one remaining map in this
// payload; encoding/json sorts map keys when marshaling (guaranteed since
// Go 1.7 for string-keyed maps, which Role — an underlying string type —
// qualifies as), so it does not need special handling to stay deterministic.
type fingerprintPayload struct {
	WorkflowID      string         `json:"workflow_id"`
	WorkflowVersion string         `json:"workflow_version"`
	SchemaVersion   string         `json:"schema_version"`
	EntryNodeID     string         `json:"entry_node_id"`
	Nodes           []CompiledNode `json:"nodes"`
	Edges           []CompiledEdge `json:"edges"`
	Loops           []CompiledLoop `json:"loops"`
	Budgets         BudgetLimits   `json:"budgets"`
}

func computeFingerprint(g *CompiledGraph) string {
	payload := fingerprintPayload{
		WorkflowID:      g.workflowID,
		WorkflowVersion: g.workflowVersion,
		SchemaVersion:   g.schemaVersion,
		EntryNodeID:     g.entryNodeID,
		Nodes:           make([]CompiledNode, 0, len(g.nodeOrder)),
		Edges:           make([]CompiledEdge, 0, len(g.edgeOrder)),
		Loops:           make([]CompiledLoop, 0, len(g.loopOrder)),
		Budgets:         g.budgets,
	}
	for _, id := range g.nodeOrder {
		payload.Nodes = append(payload.Nodes, g.nodes[id])
	}
	for _, id := range g.edgeOrder {
		payload.Edges = append(payload.Edges, g.edges[id])
	}
	for _, kind := range g.loopOrder {
		payload.Loops = append(payload.Loops, g.loops[kind])
	}

	data, err := json.Marshal(payload)
	if err != nil {
		// json.Marshal can only fail here for a cyclic value or an
		// unsupported type (channel/func/complex) — none of which
		// fingerprintPayload's fields can ever contain. Panicking would be
		// a disproportionate response to a theoretical impossibility; fall
		// back to hashing a fixed sentinel so Compile never itself panics.
		data = []byte("fingerprint-marshal-error")
	}
	return checksum.ComputeChecksum(data)
}

// ---------------------------------------------------------------------
// Accessors
// ---------------------------------------------------------------------

// Fingerprint returns the SHA-256 hex fingerprint of the compiled graph's
// content.
func (g *CompiledGraph) Fingerprint() string {
	if g == nil {
		return ""
	}
	return g.fingerprint
}

// WorkflowID returns the compiled graph's workflow identifier.
func (g *CompiledGraph) WorkflowID() string {
	if g == nil {
		return ""
	}
	return g.workflowID
}

// WorkflowVersion returns the user-facing metadata.version string.
func (g *CompiledGraph) WorkflowVersion() string {
	if g == nil {
		return ""
	}
	return g.workflowVersion
}

// SchemaVersion returns the source definition's apiVersion.
func (g *CompiledGraph) SchemaVersion() string {
	if g == nil {
		return ""
	}
	return g.schemaVersion
}

// RegistryVersion returns the definition registry's internally assigned
// integer version — see the registryVersion field doc above. It is 0 for any
// graph that has not been through a definition registry (every Compile()
// output in this PR, since PR6 has not shipped yet) and the legacy
// Definition's own Version field for a CompatAdaptDefinition-derived graph.
func (g *CompiledGraph) RegistryVersion() int {
	if g == nil {
		return 0
	}
	return g.registryVersion
}

// Nodes returns an independent copy of every compiled node, in source
// declaration order.
func (g *CompiledGraph) Nodes() []CompiledNode {
	if g == nil {
		return nil
	}
	out := make([]CompiledNode, 0, len(g.nodeOrder))
	for _, id := range g.nodeOrder {
		out = append(out, cloneCompiledNode(g.nodes[id]))
	}
	return out
}

// Edges returns an independent copy of every compiled edge, in source
// declaration order.
func (g *CompiledGraph) Edges() []CompiledEdge {
	if g == nil {
		return nil
	}
	out := make([]CompiledEdge, 0, len(g.edgeOrder))
	for _, id := range g.edgeOrder {
		out = append(out, g.edges[id])
	}
	return out
}

// EntryNodeID returns the stable ID of the graph's entry node.
func (g *CompiledGraph) EntryNodeID() string {
	if g == nil {
		return ""
	}
	return g.entryNodeID
}

// Node returns the compiled node with the given stable ID (as produced by
// nodeID/terminalNodeID in graph_projection.go, and returned by
// EntryNodeID/Nodes/Edges' ID fields), and whether a node with that ID
// exists. Added alongside PR4's supervisor entry-role fix (Node(g.EntryNodeID())
// is how the supervisor now derives RunState.CurrentRole instead of
// hardcoding RolePlanner) — PR5/PR6/PR7 will likely also want by-ID lookup
// for CLI inspect / API responses. Like every other CompiledGraph accessor
// that exposes node/edge data, it returns an independent copy so a caller
// cannot mutate the graph's internal state through slice fields it shares
// (CompiledNode.AllowedOutcomes, CompiledNode.RoleConfig's slice fields).
func (g *CompiledGraph) Node(id string) (CompiledNode, bool) {
	if g == nil {
		return CompiledNode{}, false
	}
	n, ok := g.nodes[id]
	if !ok {
		return CompiledNode{}, false
	}
	return cloneCompiledNode(n), true
}

// TerminalNodeIDs returns an independent copy of every terminal node's
// stable ID, in source declaration order.
func (g *CompiledGraph) TerminalNodeIDs() []string {
	if g == nil {
		return nil
	}
	return cloneStrings(g.terminalNodeIDs)
}

// RoleConfig returns the configuration for a logical role, and whether that
// role has a corresponding node in the graph.
func (g *CompiledGraph) RoleConfig(role Role) (RoleConfig, bool) {
	if g == nil {
		return RoleConfig{}, false
	}
	n, ok := g.nodes[nodeID(role)]
	if !ok || n.Kind != "role" {
		return RoleConfig{}, false
	}
	return cloneRoleConfig(n.RoleConfig), true
}

// Resolve returns the single declared transition for role and outcome.
// Identical signature and error type to TransitionResolver.Resolve
// (resolver.go), so PR4 can swap s.resolver.Resolve(...) for
// s.graph.Resolve(...) with zero other changes.
func (g *CompiledGraph) Resolve(role Role, outcome TransitionOutcome) (ResolvedTransition, error) {
	if g == nil {
		return ResolvedTransition{}, &InvalidTransitionError{Role: role, Outcome: outcome}
	}
	t, ok := g.outgoing[transitionKey{role: role, outcome: outcome}]
	if !ok {
		return ResolvedTransition{}, &InvalidTransitionError{Role: role, Outcome: outcome}
	}
	return t, nil
}

// LoopFor looks up the bounded-loop policy (if any) governing a resolved
// transition. It reconstructs the owning edge's stable ID from transition's
// From/Outcome (ResolvedTransition carries no edge-ID field of its own) and
// looks that ID up in the graph's loop table — PR4 will call this to
// replace the hardcoded correctionLoop() function in supervisor.go.
func (g *CompiledGraph) LoopFor(transition ResolvedTransition) (CompiledLoop, bool) {
	if g == nil {
		return CompiledLoop{}, false
	}
	loop, ok := g.loops[LoopKind(edgeID(transition.From, transition.Outcome))]
	return loop, ok
}

// Budgets returns an independent copy of the graph's run-level budgets.
func (g *CompiledGraph) Budgets() BudgetLimits {
	if g == nil {
		return BudgetLimits{}
	}
	return cloneBudgetLimits(g.budgets)
}

// HasRole reports whether r has a corresponding role node in the graph.
func (g *CompiledGraph) HasRole(r Role) bool {
	if g == nil {
		return false
	}
	n, ok := g.nodes[nodeID(r)]
	return ok && n.Kind == "role"
}

// HasOutcome reports whether o is a legal outcome for at least one role in
// the graph.
func (g *CompiledGraph) HasOutcome(o TransitionOutcome) bool {
	if g == nil {
		return false
	}
	for _, outcomes := range g.outcomeIndex {
		if outcomes[o] {
			return true
		}
	}
	return false
}

// OutcomeValidFor reports whether role may legally produce outcome,
// according to that role node's declared allowedOutcomes.
func (g *CompiledGraph) OutcomeValidFor(role Role, outcome TransitionOutcome) bool {
	if g == nil {
		return false
	}
	outcomes, ok := g.outcomeIndex[role]
	return ok && outcomes[outcome]
}
