package multiagent

import (
	"reflect"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

func TestBuildRunGraphFreshRun(t *testing.T) {
	definition := validDefinition()
	state := freshRunState(definition)

	graph := BuildRunGraph(definition, state, nil)

	if graph.WorkflowID != definition.ID || graph.WorkflowVersion != definition.Version {
		t.Fatalf("graph identity = %q/%d, want %q/%d", graph.WorkflowID, graph.WorkflowVersion, definition.ID, definition.Version)
	}
	if graph.CurrentNodeID != "node:planner" {
		t.Fatalf("current node = %q, want node:planner", graph.CurrentNodeID)
	}
	if graph.ActiveEdgeID != "" {
		t.Fatalf("active edge = %q, want none for a fresh run", graph.ActiveEdgeID)
	}
	if graph.RunPaused || graph.BudgetExhausted {
		t.Fatalf("fresh run overlays = paused:%v exhausted:%v, want both false", graph.RunPaused, graph.BudgetExhausted)
	}

	nodesByID := indexNodes(graph.Nodes)
	planner := nodesByID["node:planner"]
	if planner.Status != GraphNodeRunning || !planner.IsCurrent {
		t.Fatalf("planner node = %+v, want running/current", planner)
	}
	for _, role := range []Role{RoleDeveloper, RoleTester, RoleReviewer} {
		node := nodesByID[nodeID(role)]
		if node.Status != GraphNodeNotStarted || node.IsCurrent {
			t.Fatalf("%s node = %+v, want not_started/non-current", role, node)
		}
	}
	for _, condition := range []TerminalCondition{TerminalConditionCompleted, TerminalConditionFailed, TerminalConditionCancelled} {
		node, ok := nodesByID[terminalNodeID(condition)]
		if !ok {
			t.Fatalf("missing terminal node for %s", condition)
		}
		if node.Status != GraphNodeNotStarted || node.IsCurrent {
			t.Fatalf("terminal node %s = %+v, want not_started/non-current", condition, node)
		}
	}
	if _, ok := nodesByID[terminalNodeID(TerminalConditionBudgetExhausted)]; ok {
		t.Fatal("budget_exhausted must never be rendered as a node")
	}

	for _, edge := range graph.Edges {
		if edge.Status != GraphEdgeAvailable {
			t.Fatalf("edge %s = %s, want available on a fresh run", edge.ID, edge.Status)
		}
		if edge.TraversalCount != 0 {
			t.Fatalf("edge %s traversal count = %d, want 0", edge.ID, edge.TraversalCount)
		}
	}
}

func TestBuildRunGraphMidRunLoopbackTraversedAndActiveEdge(t *testing.T) {
	definition := validDefinition()
	state := freshRunState(definition)

	// planner -> developer -> tester(fail) -> developer -> tester(current)
	plannerState := state.RoleStates[RolePlanner]
	plannerState.Status = RoleStatusCompleted
	plannerState.Visits = 1
	state.RoleStates[RolePlanner] = plannerState

	developerState := state.RoleStates[RoleDeveloper]
	developerState.Status = RoleStatusCompleted
	developerState.Visits = 2
	state.RoleStates[RoleDeveloper] = developerState

	testerState := state.RoleStates[RoleTester]
	testerState.Status = RoleStatusRunning
	testerState.Visits = 2
	state.RoleStates[RoleTester] = testerState

	state.CurrentRole = RoleTester
	state.TransitionCount = 4
	state.LoopTraversals.TesterToDeveloper = 1

	events := []event.Event{
		transitionSelectedEvent(RolePlanner, OutcomePlanReady, RoleDeveloper, 1),
		transitionSelectedEvent(RoleDeveloper, OutcomeImplementationReady, RoleTester, 2),
		transitionSelectedEvent(RoleTester, OutcomeTestsFailed, RoleDeveloper, 3),
		transitionSelectedEvent(RoleDeveloper, OutcomeImplementationReady, RoleTester, 4),
	}

	graph := BuildRunGraph(definition, state, events)

	if graph.CurrentNodeID != "node:tester" {
		t.Fatalf("current node = %q, want node:tester", graph.CurrentNodeID)
	}
	wantActive := edgeID(RoleDeveloper, OutcomeImplementationReady)
	if graph.ActiveEdgeID != wantActive {
		t.Fatalf("active edge = %q, want %q", graph.ActiveEdgeID, wantActive)
	}

	edgesByID := indexEdges(graph.Edges)

	activeEdge := edgesByID[wantActive]
	if activeEdge.Status != GraphEdgeActive || activeEdge.TraversalCount != 2 {
		t.Fatalf("developer->tester edge = %+v, want active with traversal count 2", activeEdge)
	}

	loopEdge := edgesByID[edgeID(RoleTester, OutcomeTestsFailed)]
	if loopEdge.Status != GraphEdgeTraversed {
		t.Fatalf("tester->developer loop edge status = %s, want previously_traversed", loopEdge.Status)
	}
	if loopEdge.TraversalCount != 1 {
		t.Fatalf("tester->developer loop edge traversal count = %d, want 1", loopEdge.TraversalCount)
	}
	if loopEdge.MaxTraversals == nil || *loopEdge.MaxTraversals != int(definition.Budgets.MaxTesterToDeveloperLoops) {
		t.Fatalf("tester->developer loop edge max traversals = %v, want %d", loopEdge.MaxTraversals, definition.Budgets.MaxTesterToDeveloperLoops)
	}
	if !loopEdge.IsLoopback {
		t.Fatal("tester->developer edge must be flagged as a loopback")
	}

	untouched := edgesByID[edgeID(RoleTester, OutcomeTestsPassed)]
	if untouched.Status != GraphEdgeAvailable || untouched.TraversalCount != 0 {
		t.Fatalf("tester->reviewer edge = %+v, want untouched/available", untouched)
	}
}

func TestBuildRunGraphCompletedHappyPath(t *testing.T) {
	definition := validDefinition()
	state := freshRunState(definition)
	now := state.CreatedAt.Add(time.Hour)

	for _, role := range Phase1Roles() {
		roleState := state.RoleStates[role]
		roleState.Status = RoleStatusCompleted
		roleState.Visits = 1
		state.RoleStates[role] = roleState
	}
	state.CurrentRole = RoleReviewer
	state.Status = RunStatusCompleted
	state.TransitionCount = 4
	state.CompletedAt = &now
	state.TerminalOutcome = &TerminalOutcome{
		Condition: TerminalConditionCompleted,
		Reason:    "reviewer approved",
		At:        now,
	}

	events := []event.Event{
		transitionSelectedEvent(RolePlanner, OutcomePlanReady, RoleDeveloper, 1),
		transitionSelectedEvent(RoleDeveloper, OutcomeImplementationReady, RoleTester, 2),
		transitionSelectedEvent(RoleTester, OutcomeTestsPassed, RoleReviewer, 3),
		transitionSelectedTerminalEvent(RoleReviewer, OutcomeReviewApproved, TerminalConditionCompleted, 4),
	}

	graph := BuildRunGraph(definition, state, events)

	if graph.CurrentNodeID != terminalNodeID(TerminalConditionCompleted) {
		t.Fatalf("current node = %q, want the completed terminal node", graph.CurrentNodeID)
	}
	if graph.ActiveEdgeID != "" {
		t.Fatalf("active edge = %q, want none once the run is terminal", graph.ActiveEdgeID)
	}

	nodesByID := indexNodes(graph.Nodes)
	completedTerminal := nodesByID[terminalNodeID(TerminalConditionCompleted)]
	if completedTerminal.Status != GraphNodeCompleted || !completedTerminal.IsCurrent {
		t.Fatalf("completed terminal node = %+v, want completed/current", completedTerminal)
	}
	for _, role := range Phase1Roles() {
		node := nodesByID[nodeID(role)]
		if node.Status != GraphNodeCompleted {
			t.Fatalf("%s node = %+v, want completed", role, node)
		}
	}

	edgesByID := indexEdges(graph.Edges)
	for _, id := range []string{
		edgeID(RolePlanner, OutcomePlanReady),
		edgeID(RoleDeveloper, OutcomeImplementationReady),
		edgeID(RoleTester, OutcomeTestsPassed),
		edgeID(RoleReviewer, OutcomeReviewApproved),
	} {
		edge := edgesByID[id]
		if edge.TraversalCount != 1 || edge.Status != GraphEdgeTraversed {
			t.Fatalf("edge %s = %+v, want traversed once", id, edge)
		}
	}
}

func TestBuildRunGraphSkippedRolesOnEarlyCancellation(t *testing.T) {
	definition := validDefinition()
	state := freshRunState(definition)
	now := state.CreatedAt.Add(time.Minute)

	plannerState := state.RoleStates[RolePlanner]
	plannerState.Status = RoleStatusCancelled
	plannerState.Visits = 1
	state.RoleStates[RolePlanner] = plannerState

	state.CurrentRole = RolePlanner
	state.Status = RunStatusCancelled
	state.CancellationReason = "operator requested cancellation"
	state.CompletedAt = &now
	state.TerminalOutcome = &TerminalOutcome{
		Condition: TerminalConditionCancelled,
		Reason:    state.CancellationReason,
		At:        now,
	}

	graph := BuildRunGraph(definition, state, nil)

	nodesByID := indexNodes(graph.Nodes)
	if nodesByID[nodeID(RolePlanner)].Status != GraphNodeCancelled {
		t.Fatalf("planner node = %+v, want cancelled", nodesByID[nodeID(RolePlanner)])
	}
	for _, role := range []Role{RoleDeveloper, RoleTester, RoleReviewer} {
		node := nodesByID[nodeID(role)]
		if node.Status != GraphNodeSkipped {
			t.Fatalf("%s node = %+v, want skipped", role, node)
		}
	}
	cancelledTerminal := nodesByID[terminalNodeID(TerminalConditionCancelled)]
	if cancelledTerminal.Status != GraphNodeCancelled || !cancelledTerminal.IsCurrent {
		t.Fatalf("cancelled terminal node = %+v, want cancelled/current", cancelledTerminal)
	}
}

func TestBuildRunGraphBudgetExhaustedOverlayHasNoSyntheticNodeOrEdge(t *testing.T) {
	definition := validDefinition()
	state := freshRunState(definition)
	now := state.CreatedAt.Add(time.Minute)

	developerState := state.RoleStates[RoleDeveloper]
	developerState.Status = RoleStatusFailed
	state.RoleStates[RoleDeveloper] = developerState

	state.CurrentRole = RoleDeveloper
	state.Status = RunStatusBudgetExhausted
	state.CompletedAt = &now
	state.TerminalOutcome = &TerminalOutcome{
		Condition: TerminalConditionBudgetExhausted,
		Reason:    "max_transitions exhausted",
		At:        now,
	}

	graph := BuildRunGraph(definition, state, nil)

	if !graph.BudgetExhausted {
		t.Fatal("expected RunGraph.BudgetExhausted overlay to be true")
	}
	if graph.CurrentNodeID != "" {
		t.Fatalf("current node = %q, want empty (no node represents budget exhaustion)", graph.CurrentNodeID)
	}
	for _, node := range graph.Nodes {
		if node.Kind == "terminal" && node.Terminal == TerminalConditionBudgetExhausted {
			t.Fatalf("unexpected synthetic node for budget exhaustion: %+v", node)
		}
	}
	for _, edge := range graph.Edges {
		if edge.Terminal == TerminalConditionBudgetExhausted {
			t.Fatalf("unexpected synthetic edge for budget exhaustion: %+v", edge)
		}
	}
}

func TestBuildRunGraphStableIDsAreDeterministic(t *testing.T) {
	definition := validDefinition()
	state := freshRunState(definition)
	events := []event.Event{
		transitionSelectedEvent(RolePlanner, OutcomePlanReady, RoleDeveloper, 1),
	}

	first := BuildRunGraph(definition, cloneRunStateForTest(state), append([]event.Event(nil), events...))
	second := BuildRunGraph(definition, cloneRunStateForTest(state), append([]event.Event(nil), events...))

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("BuildRunGraph is not deterministic:\nfirst:  %+v\nsecond: %+v", first, second)
	}

	nodesByID := indexNodes(first.Nodes)
	for _, role := range Phase1Roles() {
		if _, ok := nodesByID[nodeID(role)]; !ok {
			t.Fatalf("missing expected stable node id for role %s", role)
		}
	}
	edgesByID := indexEdges(first.Edges)
	if _, ok := edgesByID["edge:tester:tests_failed"]; !ok {
		t.Fatal("missing expected stable edge id edge:tester:tests_failed")
	}
}

func indexNodes(nodes []GraphNode) map[string]GraphNode {
	index := make(map[string]GraphNode, len(nodes))
	for _, node := range nodes {
		index[node.ID] = node
	}
	return index
}

func indexEdges(edges []GraphEdge) map[string]GraphEdge {
	index := make(map[string]GraphEdge, len(edges))
	for _, edge := range edges {
		index[edge.ID] = edge
	}
	return index
}

// freshRunState builds a minimal, valid-shaped RunState for a brand new run
// of definition: planner running, all other roles pending, no transitions
// yet. Tests mutate the returned value further for mid-run/terminal cases.
func freshRunState(definition Definition) RunState {
	createdAt := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	plannerEnteredAt := createdAt
	roleStates := make(map[Role]RoleState, len(definition.Roles))
	for _, cfg := range definition.Roles {
		roleStates[cfg.Role] = RoleState{
			Role:      cfg.Role,
			Status:    RoleStatusPending,
			UpdatedAt: createdAt,
		}
	}
	plannerState := roleStates[RolePlanner]
	plannerState.Status = RoleStatusRunning
	plannerState.EnteredAt = &plannerEnteredAt
	plannerState.UpdatedAt = createdAt
	roleStates[RolePlanner] = plannerState

	return RunState{
		SchemaVersion:   RunStateSchemaVersion,
		RunID:           "run-graph-projection-test",
		WorkflowID:      definition.ID,
		WorkflowVersion: definition.Version,
		CurrentRole:     RolePlanner,
		WorkspaceID:     "workspace-graph-projection-test",
		CurrentTask: TaskReference{
			ID:          "task-graph-projection-test",
			Description: "graph projection fixture",
		},
		Status:     RunStatusRunning,
		RoleStates: roleStates,
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
	}
}

// cloneRunStateForTest returns a deep-enough copy for determinism
// assertions: a fresh RoleStates map so mutating one graph build's inputs
// cannot be mistaken for aliasing between the two calls under test.
func cloneRunStateForTest(state RunState) RunState {
	cloned := state
	cloned.RoleStates = make(map[Role]RoleState, len(state.RoleStates))
	for role, roleState := range state.RoleStates {
		cloned.RoleStates[role] = roleState
	}
	return cloned
}

func transitionSelectedEvent(from Role, outcome TransitionOutcome, to Role, transitionCount int) event.Event {
	return event.Event{
		ID:   event.NewID(),
		Type: event.EventMultiAgentTransitionSelected,
		Payload: map[string]any{
			"source_role":      string(from),
			"outcome":          string(outcome),
			"destination_role": string(to),
			"transition_count": transitionCount,
		},
	}
}

func transitionSelectedTerminalEvent(from Role, outcome TransitionOutcome, terminal TerminalCondition, transitionCount int) event.Event {
	return event.Event{
		ID:   event.NewID(),
		Type: event.EventMultiAgentTransitionSelected,
		Payload: map[string]any{
			"source_role":        string(from),
			"outcome":            string(outcome),
			"terminal_condition": string(terminal),
			"transition_count":   transitionCount,
		},
	}
}
