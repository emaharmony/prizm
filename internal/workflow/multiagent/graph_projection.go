package multiagent

import (
	"strings"

	"github.com/emaharmony/prizm/internal/event"
)

// GraphNodeStatus is the dashboard-facing lifecycle state of one graph node.
// It is always derived, never persisted: BuildRunGraph recomputes it from
// RunState on every call.
type GraphNodeStatus string

const (
	GraphNodeNotStarted      GraphNodeStatus = "not_started"
	GraphNodeRunning         GraphNodeStatus = "running"
	GraphNodeWaitingApproval GraphNodeStatus = "waiting_approval"
	// GraphNodeWaitingValidation is reserved: no Phase 1 code path
	// distinguishes a validation wait from an approval wait (both surface as
	// RoleStatusWaiting), so this value is never produced today.
	GraphNodeWaitingValidation GraphNodeStatus = "waiting_validation"
	GraphNodeCompleted         GraphNodeStatus = "completed"
	GraphNodeFailed            GraphNodeStatus = "failed"
	// GraphNodeBlocked is reserved: RoleStatusBlocked is defined in types.go
	// but no Phase 1 code path in supervisor.go/durable_runtime.go ever sets
	// it, so this value is never produced today.
	GraphNodeBlocked   GraphNodeStatus = "blocked"
	GraphNodeCancelled GraphNodeStatus = "cancelled"
	// GraphNodeSkipped marks a role the run never entered before reaching a
	// terminal condition (e.g. planner -> terminal cancelled skips
	// developer/tester/reviewer).
	GraphNodeSkipped GraphNodeStatus = "skipped"
)

// Deliberately no "paused" node status: RunStatusPaused is a RunGraph-level
// overlay (RunGraph.RunPaused), applied to whichever node has
// IsCurrent == true. A paused run's current node keeps its underlying
// status (running or waiting_approval); it does not become a distinct node
// status.

// GraphNode is one stable-ID vertex in a run's execution graph: either a
// configured role or a terminal condition reachable by some TransitionRule.
type GraphNode struct {
	ID                 string            `json:"id"`
	Kind               string            `json:"kind"` // "role" or "terminal"
	Role               Role              `json:"role,omitempty"`
	Terminal           TerminalCondition `json:"terminal,omitempty"`
	Label              string            `json:"label"`
	Status             GraphNodeStatus   `json:"status"`
	IsCurrent          bool              `json:"is_current"`
	Visits             int               `json:"visits"`
	LocalIterations    int               `json:"local_iterations"`
	MaxVisits          *int              `json:"max_visits,omitempty"`
	MaxLocalIterations *int              `json:"max_local_iterations,omitempty"`
}

// GraphEdgeStatus is the dashboard-facing state of one graph edge.
type GraphEdgeStatus string

const (
	GraphEdgeAvailable GraphEdgeStatus = "available"
	GraphEdgeTraversed GraphEdgeStatus = "previously_traversed"
	GraphEdgeActive    GraphEdgeStatus = "active"
	GraphEdgeExhausted GraphEdgeStatus = "exhausted"
	// GraphEdgeDisabled and GraphEdgeInvalid are reserved for non-reference
	// definitions; the Phase 1 reference graph never produces them.
	GraphEdgeDisabled GraphEdgeStatus = "disabled"
	GraphEdgeInvalid  GraphEdgeStatus = "invalid"
)

// GraphEdge is one stable-ID transition rule rendered for the dashboard.
type GraphEdge struct {
	ID             string            `json:"id"`
	From           Role              `json:"from"`
	Outcome        TransitionOutcome `json:"outcome"`
	To             Role              `json:"to,omitempty"`
	Terminal       TerminalCondition `json:"terminal,omitempty"`
	Label          string            `json:"label"`
	Status         GraphEdgeStatus   `json:"status"`
	IsLoopback     bool              `json:"is_loopback"`
	TraversalCount int               `json:"traversal_count"`
	MaxTraversals  *int              `json:"max_traversals,omitempty"`
}

// RunGraph is the complete, deterministic execution-graph projection of one
// run: stable node/edge IDs, current position, and run-level overlays.
type RunGraph struct {
	WorkflowID      string      `json:"workflow_id"`
	WorkflowVersion int         `json:"workflow_version"`
	Nodes           []GraphNode `json:"nodes"`
	Edges           []GraphEdge `json:"edges"`
	CurrentNodeID   string      `json:"current_node_id,omitempty"`
	ActiveEdgeID    string      `json:"active_edge_id,omitempty"`
	RunPaused       bool        `json:"run_paused"`
	// BudgetExhausted is a run-level status overlay only.
	// TerminalConditionBudgetExhausted has no TransitionRule in
	// DefaultReferenceDefinition, so it is never rendered as a synthetic
	// node or edge here.
	BudgetExhausted bool `json:"budget_exhausted"`
}

// nodeID returns the stable identifier for a role node.
func nodeID(role Role) string {
	return "node:" + string(role)
}

// terminalNodeID returns the stable identifier for a terminal node.
func terminalNodeID(condition TerminalCondition) string {
	return "node:terminal:" + string(condition)
}

// edgeID returns the stable identifier for a transition rule. This is safe
// because Definition.Validate() already guarantees (From, Outcome)
// uniqueness across a definition's transitions (config.go's seenTransitions
// dedup check).
func edgeID(from Role, outcome TransitionOutcome) string {
	return "edge:" + string(from) + ":" + string(outcome)
}

// BuildRunGraph derives the complete execution graph for one run. It is a
// pure function: no I/O, deterministic given the same inputs, and fully
// unit-testable without a database. events is expected in ascending
// chronological (insertion) order, matching event.Store.Query's contract.
func BuildRunGraph(definition Definition, state RunState, events []event.Event) RunGraph {
	graph := RunGraph{
		WorkflowID:      definition.ID,
		WorkflowVersion: definition.Version,
		RunPaused:       state.Status == RunStatusPaused,
		BudgetExhausted: state.Status == RunStatusBudgetExhausted,
	}

	currentNodeID := currentGraphNodeID(state)
	graph.CurrentNodeID = currentNodeID

	for _, cfg := range definition.Roles {
		graph.Nodes = append(graph.Nodes, buildRoleNode(definition, state, cfg, currentNodeID))
	}
	for _, condition := range terminalConditions(definition) {
		graph.Nodes = append(graph.Nodes, buildTerminalNode(state, condition, currentNodeID))
	}

	traversals, lastTransition := scanTransitionEvents(events)
	activeEdgeID := ""
	if lastTransition != nil && !state.Status.Terminal() &&
		lastTransition.destination == string(state.CurrentRole) {
		activeEdgeID = edgeID(Role(lastTransition.source), TransitionOutcome(lastTransition.outcome))
	}
	graph.ActiveEdgeID = activeEdgeID

	for _, rule := range definition.Transitions {
		graph.Edges = append(graph.Edges, buildEdge(definition, state, rule, traversals, activeEdgeID))
	}
	return graph
}

func currentGraphNodeID(state RunState) string {
	if state.Status.Terminal() {
		// TerminalConditionBudgetExhausted deliberately has no corresponding
		// node (see terminalConditions): budget exhaustion is a run-level
		// overlay (RunGraph.BudgetExhausted), not a graph vertex. Pointing
		// CurrentNodeID at a node ID that never appears in graph.Nodes would
		// be a dangling reference, so leave it empty in that case.
		if state.TerminalOutcome != nil && state.TerminalOutcome.Condition != TerminalConditionBudgetExhausted {
			return terminalNodeID(state.TerminalOutcome.Condition)
		}
		return ""
	}
	if state.CurrentRole == "" {
		return ""
	}
	return nodeID(state.CurrentRole)
}

func buildRoleNode(definition Definition, state RunState, cfg RoleConfig, currentNodeID string) GraphNode {
	id := nodeID(cfg.Role)
	roleState := state.RoleStates[cfg.Role]
	node := GraphNode{
		ID:                 id,
		Kind:               "role",
		Role:               cfg.Role,
		Label:              humanizeIdentifier(string(cfg.Role)),
		Status:             roleNodeStatus(roleState, state.Status),
		IsCurrent:          id == currentNodeID,
		Visits:             roleState.Visits,
		LocalIterations:    roleState.LocalIterations,
		MaxLocalIterations: limitPointer(cfg.MaxLocalIterations),
	}
	if limit, ok := definition.Budgets.MaxVisitsPerRole[cfg.Role]; ok {
		node.MaxVisits = limitPointer(limit)
	}
	return node
}

// roleNodeStatus maps a role's persisted execution status to a graph node
// status, applying the "skipped" derivation: a role that never left
// RoleStatusPending before the run reached a terminal condition was never
// entered.
func roleNodeStatus(roleState RoleState, runStatus RunStatus) GraphNodeStatus {
	if roleState.Status == RoleStatusPending && runStatus.Terminal() {
		return GraphNodeSkipped
	}
	switch roleState.Status {
	case RoleStatusPending:
		return GraphNodeNotStarted
	case RoleStatusRunning:
		return GraphNodeRunning
	case RoleStatusWaiting:
		// Phase 1 only ever pauses a role for approval; the rarer
		// execution_reconciliation wait (crash recovery) also surfaces as
		// RoleStatusWaiting and renders the same way today.
		return GraphNodeWaitingApproval
	case RoleStatusCompleted:
		return GraphNodeCompleted
	case RoleStatusFailed:
		return GraphNodeFailed
	case RoleStatusBlocked:
		return GraphNodeBlocked
	case RoleStatusCancelled:
		return GraphNodeCancelled
	default:
		return GraphNodeNotStarted
	}
}

func buildTerminalNode(state RunState, condition TerminalCondition, currentNodeID string) GraphNode {
	id := terminalNodeID(condition)
	status := GraphNodeNotStarted
	if state.TerminalOutcome != nil && state.TerminalOutcome.Condition == condition {
		status = terminalNodeStatus(condition)
	}
	return GraphNode{
		ID:        id,
		Kind:      "terminal",
		Terminal:  condition,
		Label:     humanizeIdentifier(string(condition)),
		Status:    status,
		IsCurrent: id == currentNodeID,
	}
}

func terminalNodeStatus(condition TerminalCondition) GraphNodeStatus {
	switch condition {
	case TerminalConditionCompleted:
		return GraphNodeCompleted
	case TerminalConditionFailed:
		return GraphNodeFailed
	case TerminalConditionCancelled:
		return GraphNodeCancelled
	default:
		// TerminalConditionBudgetExhausted never reaches here because no
		// transition rule declares it, so terminalConditions() never yields
		// it as a node in the first place.
		return GraphNodeNotStarted
	}
}

// terminalConditions returns the distinct terminal conditions reachable by
// some TransitionRule, in first-seen Transitions order (deterministic given
// a fixed Definition). TerminalConditionBudgetExhausted is intentionally
// absent from DefaultReferenceDefinition's transitions and therefore never
// appears here.
func terminalConditions(definition Definition) []TerminalCondition {
	seen := make(map[TerminalCondition]bool, len(definition.Transitions))
	var conditions []TerminalCondition
	for _, rule := range definition.Transitions {
		if rule.Terminal == "" || seen[rule.Terminal] {
			continue
		}
		seen[rule.Terminal] = true
		conditions = append(conditions, rule.Terminal)
	}
	return conditions
}

func buildEdge(
	definition Definition,
	state RunState,
	rule TransitionRule,
	traversals map[string]int,
	activeEdgeID string,
) GraphEdge {
	id := edgeID(rule.From, rule.Outcome)
	edge := GraphEdge{
		ID:       id,
		From:     rule.From,
		Outcome:  rule.Outcome,
		To:       rule.To,
		Terminal: rule.Terminal,
		Label:    humanizeIdentifier(string(rule.Outcome)),
	}
	if rule.To != "" {
		edge.IsLoopback = roleOrder(rule.To) <= roleOrder(rule.From)
	}

	resolved := ResolvedTransition{From: rule.From, Outcome: rule.Outcome, To: rule.To, Terminal: rule.Terminal}
	if loopKind, ok := correctionLoop(resolved); ok {
		edge.TraversalCount = state.LoopTraversals.Get(loopKind)
		edge.MaxTraversals = loopLimitPointer(definition, loopKind)
	} else {
		edge.TraversalCount = traversals[id]
	}

	switch {
	case id == activeEdgeID:
		edge.Status = GraphEdgeActive
	case edge.MaxTraversals != nil && edge.TraversalCount >= *edge.MaxTraversals:
		edge.Status = GraphEdgeExhausted
	case edge.TraversalCount > 0:
		edge.Status = GraphEdgeTraversed
	default:
		edge.Status = GraphEdgeAvailable
	}
	return edge
}

func loopLimitPointer(definition Definition, kind LoopKind) *int {
	switch kind {
	case LoopTesterToDeveloper:
		return limitPointer(definition.Budgets.MaxTesterToDeveloperLoops)
	case LoopReviewerToDeveloper:
		return limitPointer(definition.Budgets.MaxReviewerToDeveloperLoops)
	default:
		return nil
	}
}

// limitPointer converts a Limit into the nil-means-unlimited *int contract
// used throughout the dashboard read model. Limit == Unlimited (-1) becomes
// nil; any other valid Limit becomes a pointer to its int value.
func limitPointer(limit Limit) *int {
	if limit == Unlimited {
		return nil
	}
	value := int(limit)
	return &value
}

// transitionEventInfo is the minimal projection of one
// EventMultiAgentTransitionSelected payload needed to derive per-edge
// traversal counts and the active edge.
type transitionEventInfo struct {
	source      string
	outcome     string
	destination string
}

// scanTransitionEvents counts per-edge traversals (keyed by edgeID) across
// the full event history and returns the most recent transition-selected
// event, if any. It intentionally scans the full history rather than a
// truncated tail: per-edge traversal counts for non-loop edges have no
// other source of truth (only the two correction-loop edges are counted in
// RunState.LoopTraversals).
func scanTransitionEvents(events []event.Event) (map[string]int, *transitionEventInfo) {
	counts := make(map[string]int)
	var last *transitionEventInfo
	for i := range events {
		evt := events[i]
		if evt.Type != event.EventMultiAgentTransitionSelected {
			continue
		}
		source := payloadString(evt.Payload, "source_role")
		outcome := payloadString(evt.Payload, "outcome")
		if source == "" || outcome == "" {
			// Malformed or foreign event on this run's stream; skip rather
			// than corrupt traversal counts with an empty-role edge ID.
			continue
		}
		counts[edgeID(Role(source), TransitionOutcome(outcome))]++
		info := transitionEventInfo{
			source:      source,
			outcome:     outcome,
			destination: payloadString(evt.Payload, "destination_role"),
		}
		last = &info
	}
	return counts, last
}

// humanizeIdentifier renders a snake_case Role/Outcome/TerminalCondition
// identifier as a short display label, e.g. "tests_failed" -> "Tests
// failed".
func humanizeIdentifier(value string) string {
	if value == "" {
		return ""
	}
	spaced := strings.ReplaceAll(value, "_", " ")
	return strings.ToUpper(spaced[:1]) + spaced[1:]
}
