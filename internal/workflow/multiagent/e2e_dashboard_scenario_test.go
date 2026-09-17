//go:build ignore

// This test references RunLocator and writeLocatorManifest which are not yet
// implemented. Skipped until the persistence/recovery PR lands.

package multiagent

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

// TestReferenceWorkflowEndToEndDashboardScenario is the Milestone 9c
// deterministic, no-external-model-calls end-to-end test for Phase 2 of the
// Multi-Agent Loop Orchestration initiative. It drives DurableRuntime.Run
// through one full lifecycle of the reference workflow (planner ->
// developer -> tester correction loop -> tester -> reviewer correction loop
// -> developer -> tester -> reviewer -> completed) using a scripted
// RoleRunner double (no network or model calls), then proves that:
//
//  1. the terminal RunState's counters are exactly what the scripted
//     transition sequence implies;
//  2. BuildRunGraph's projection of that terminal state is correct,
//     including both correction-loop edges;
//  3. re-opening the run from disk via a fresh RunLocator/DurableRuntime/
//     event store -- exactly what a real dashboard "refresh" (a browser's
//     GET .../snapshot request hitting a process that did not run the
//     workflow) does via RunLocator.OpenInspection -- reconstructs an
//     IDENTICAL graph/budget projection to the one built from the original
//     in-process run. This is the backend half of the
//     refresh-reconstructs-identical-state design invariant; and
//  4. the persisted event log is a complete, correctly ordered record of
//     the run, matching what the frontend replay module (Milestone 8)
//     consumes.
func TestReferenceWorkflowEndToEndDashboardScenario(t *testing.T) {
	root := t.TempDir()
	runID := "run-e2e-dashboard-scenario"

	// DefaultReferenceDefinition's MaxReviewerToDeveloperLoops ceiling is 1
	// (reference_workflow.go). This scenario's single reviewer->developer
	// correction traversal (1 out of 1) would make BuildRunGraph's edge
	// display rule -- "TraversalCount >= MaxTraversals" in
	// graph_projection.go's buildEdge -- render that loop edge as
	// GraphEdgeExhausted rather than GraphEdgeTraversed, even though the
	// budget engine itself (which uses a strict ">" comparison in
	// supervisor.go's exceeded()) does not consider the loop exhausted at
	// exactly its limit. ApplyReferenceOverrides can only narrow the
	// shipped safety ceiling, never widen it (see its applyLimitOverride
	// helper and TestReferenceOverridesOnlyNarrowAuthority in
	// reference_workflow_test.go), so it cannot be used to raise this
	// ceiling. Raising it by direct field mutation on a cloned Definition
	// is the established idiom this package's existing tests already use
	// for the same purpose (e.g. supervisor_test.go's
	// "definition.Budgets.MaxTesterToDeveloperLoops = 1" and
	// durable_runtime_test.go's per-role MaxLocalIterations mutation).
	definition := DefaultReferenceDefinition()
	definition.Budgets.MaxReviewerToDeveloperLoops = 2

	withWorkspace := func(outcome TransitionOutcome) RoleRunResult {
		result := scriptedResult(outcome)
		result.Metadata.WorkspaceID = "workspace-e2e-dashboard"
		result.Metadata.ValidationStatus = "passed"
		return result
	}

	// The exact scripted transition sequence (9 role visits, 9 transitions):
	//   1. planner:   plan_ready            -> developer          (visit 1)
	//   2. developer: implementation_ready  -> tester              (visit 1)
	//   3. tester:    tests_failed          -> developer            (visit 1, loop tester->developer #1)
	//   4. developer: implementation_ready  -> tester                (visit 2)
	//   5. tester:    tests_passed          -> reviewer               (visit 2)
	//   6. reviewer:  changes_requested     -> developer                (visit 1, loop reviewer->developer #1)
	//   7. developer: implementation_ready  -> tester                   (visit 3)
	//   8. tester:    tests_passed          -> reviewer                  (visit 3)
	//   9. reviewer:  review_approved       -> terminal completed         (visit 2)
	//
	// Role visit totals (recounted directly against this script, not
	// assumed): planner=1, developer=3, tester=3, reviewer=2.
	// Transition count = 9: one transition per scripted role result above,
	// including the final terminal transition out of reviewer's second
	// visit.
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner: {
			{result: withWorkspace(OutcomePlanReady)},
		},
		RoleDeveloper: {
			{result: withWorkspace(OutcomeImplementationReady)},
			{result: withWorkspace(OutcomeImplementationReady)},
			{result: withWorkspace(OutcomeImplementationReady)},
		},
		RoleTester: {
			{result: withWorkspace(OutcomeTestsFailed)},
			{result: withWorkspace(OutcomeTestsPassed)},
			{result: withWorkspace(OutcomeTestsPassed)},
		},
		RoleReviewer: {
			{result: withWorkspace(OutcomeChangesRequested)},
			{result: withWorkspace(OutcomeReviewApproved)},
		},
	}}

	dbPath := filepath.Join(root, runID, "multiagent.db")
	store, err := NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatalf("new durable store: %v", err)
	}
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	runtime := newDurableRuntimeForTest(
		t, definition, runner, store, FileRunClaimer{Root: root}, events)

	request := RunRequest{
		RunID: runID,
		Task: TaskReference{
			ID:          "task-e2e-dashboard-scenario",
			Description: "exercise the full dashboard-facing backend stack through one deterministic run",
		},
	}

	finalState, err := runtime.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("run reference workflow: %v", err)
	}

	// -- 1. Final state correctness -------------------------------------

	if finalState.Status != RunStatusCompleted {
		t.Fatalf("final status = %q, want %q", finalState.Status, RunStatusCompleted)
	}
	if finalState.TerminalOutcome == nil ||
		finalState.TerminalOutcome.Condition != TerminalConditionCompleted ||
		finalState.TerminalOutcome.Reason != "review approved" {
		t.Fatalf("terminal outcome = %#v, want completed via review approved", finalState.TerminalOutcome)
	}
	if finalState.TransitionCount != 9 {
		t.Fatalf("transition count = %d, want 9", finalState.TransitionCount)
	}
	if finalState.LoopTraversals.Get(LoopTesterToDeveloper) != 1 || finalState.LoopTraversals.Get(LoopReviewerToDeveloper) != 1 {
		t.Fatalf("loop traversals = %#v, want exactly 1 of each", finalState.LoopTraversals)
	}
	wantVisits := map[Role]int{
		RolePlanner:   1,
		RoleDeveloper: 3,
		RoleTester:    3,
		RoleReviewer:  2,
	}
	for role, want := range wantVisits {
		if got := finalState.RoleStates[role].Visits; got != want {
			t.Fatalf("%s visits = %d, want %d", role, got, want)
		}
	}

	// -- capture the "original", in-process view for later comparison ---

	originalRecord, err := runtime.Inspect(context.Background(), runID)
	if err != nil {
		t.Fatalf("inspect original record: %v", err)
	}
	originalEvents, err := events.Query(context.Background(), event.EventFilter{RunID: runID, Limit: 1000})
	if err != nil {
		t.Fatalf("query original events: %v", err)
	}
	if len(originalEvents) != 41 {
		t.Fatalf("original event count = %d, want 41", len(originalEvents))
	}

	// -- 2. Graph projection correctness --------------------------------

	graph := BuildRunGraph(definition, finalState, originalEvents)
	assertGraphMatchesDashboardScenario(t, definition, graph)

	// -- 4. Replay reproduction: canonical event ordering ----------------

	wantEventTypes := expectedDashboardScenarioEventTypes()
	gotEventTypes := make([]string, len(originalEvents))
	for i, evt := range originalEvents {
		gotEventTypes[i] = evt.Type
	}
	if !reflect.DeepEqual(gotEventTypes, wantEventTypes) {
		t.Fatalf("event type sequence mismatch:\n got  %v\n want %v", gotEventTypes, wantEventTypes)
	}

	// Write the on-disk manifest a real CLI run would have written, then
	// close every store this process opened -- simulating the process that
	// ran the workflow moving on before a browser ever asks for the
	// dashboard snapshot.
	writeLocatorManifest(t, root, runID, definition)
	if err := store.Close(); err != nil {
		t.Fatalf("close original store: %v", err)
	}
	if err := events.Close(); err != nil {
		t.Fatalf("close original events: %v", err)
	}

	// -- 3. Refresh-reconstruction correctness ---------------------------
	//
	// From this point on, nothing from the original runtime/store/events
	// above is reused. A brand new RunLocator opens the same on-disk run
	// directory exactly as RunLocator.OpenInspection (used by the API's
	// snapshot handler) would for a fresh HTTP request.

	locator := RunLocator{Root: root}
	freshRuntime, freshStore, freshEvents, err := locator.OpenInspection(context.Background(), runID)
	if err != nil {
		t.Fatalf("open fresh inspection: %v", err)
	}
	defer func() {
		if err := freshStore.Close(); err != nil {
			t.Errorf("close fresh store: %v", err)
		}
		if err := freshEvents.Close(); err != nil {
			t.Errorf("close fresh events: %v", err)
		}
	}()

	freshRecord, err := freshRuntime.Inspect(context.Background(), runID)
	if err != nil {
		t.Fatalf("fresh inspect: %v", err)
	}
	freshAllEvents, err := freshEvents.Query(context.Background(), event.EventFilter{RunID: runID, Limit: 1000})
	if err != nil {
		t.Fatalf("query fresh events: %v", err)
	}
	if len(freshAllEvents) != len(originalEvents) {
		t.Fatalf("fresh event count = %d, want %d", len(freshAllEvents), len(originalEvents))
	}

	freshDefinition, err := locator.Definition(runID)
	if err != nil {
		t.Fatalf("fresh definition: %v", err)
	}

	// Two independently built graph projections: "graph" was built above
	// from the original in-memory Definition and the original event store's
	// query result; "freshGraph" is built from a Definition round-tripped
	// through the on-disk manifest JSON and a brand new event store query
	// against the same database file. They must match exactly.
	freshGraph := BuildRunGraph(freshDefinition, freshRecord.State, freshAllEvents)
	if !reflect.DeepEqual(graph, freshGraph) {
		t.Fatalf("fresh graph != original graph:\n original %#v\n fresh    %#v", graph, freshGraph)
	}
	assertGraphMatchesDashboardScenario(t, freshDefinition, freshGraph)

	originalSnapshot := BuildDashboardSnapshot(definition, originalRecord, originalEvents)
	freshSnapshot := BuildDashboardSnapshot(freshDefinition, freshRecord, freshAllEvents)

	if !reflect.DeepEqual(originalSnapshot.Graph, freshSnapshot.Graph) {
		t.Fatalf("fresh snapshot graph != original:\n original %#v\n fresh    %#v",
			originalSnapshot.Graph, freshSnapshot.Graph)
	}
	if !reflect.DeepEqual(originalSnapshot.Budgets, freshSnapshot.Budgets) {
		t.Fatalf("fresh budget visualization != original:\n original %#v\n fresh    %#v",
			originalSnapshot.Budgets, freshSnapshot.Budgets)
	}
	if originalSnapshot.TransitionCount != freshSnapshot.TransitionCount {
		t.Fatalf("fresh transition count %d != original %d",
			freshSnapshot.TransitionCount, originalSnapshot.TransitionCount)
	}
	if !reflect.DeepEqual(originalSnapshot.LoopTraversals, freshSnapshot.LoopTraversals) {
		t.Fatalf("fresh loop traversals %#v != original %#v",
			freshSnapshot.LoopTraversals, originalSnapshot.LoopTraversals)
	}
	if originalSnapshot.WorkflowVersion != freshSnapshot.WorkflowVersion ||
		originalSnapshot.SchemaVersion != freshSnapshot.SchemaVersion {
		t.Fatalf("fresh snapshot versions = %d/%d, want %d/%d",
			freshSnapshot.WorkflowVersion, freshSnapshot.SchemaVersion,
			originalSnapshot.WorkflowVersion, originalSnapshot.SchemaVersion)
	}
	for role, want := range wantVisits {
		if got := freshRecord.State.RoleStates[role].Visits; got != want {
			t.Fatalf("fresh %s visits = %d, want %d (matching original)", role, got, want)
		}
	}
	if freshRecord.State.Status != RunStatusCompleted ||
		freshRecord.State.TransitionCount != 9 ||
		freshRecord.State.LoopTraversals.Get(LoopTesterToDeveloper) != 1 ||
		freshRecord.State.LoopTraversals.Get(LoopReviewerToDeveloper) != 1 {
		t.Fatalf("fresh reconstructed state = %#v", freshRecord.State)
	}

	// An inspection-only runtime opened by RunLocator must never be able to
	// re-execute the workflow: the "refresh" path is strictly read-only.
	if _, runErr := freshRuntime.Run(context.Background(), RunRequest{
		RunID: "run-e2e-dashboard-scenario-should-not-execute",
		Task:  TaskReference{ID: "task-should-not-execute"},
	}); runErr == nil {
		t.Fatal("expected inspection-only fresh runtime to refuse execution, got nil error")
	}
}

// assertGraphMatchesDashboardScenario asserts every graph-projection
// invariant this scenario is designed to exercise: all four roles were
// genuinely visited (no not_started/skipped node), both correction-loop
// edges are traversed exactly once without being misreported as exhausted,
// and every other edge's traversal count matches the scripted sequence.
func assertGraphMatchesDashboardScenario(t *testing.T, definition Definition, graph RunGraph) {
	t.Helper()

	nodesByID := make(map[string]GraphNode, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodesByID[node.ID] = node
	}
	wantNodeVisits := map[Role]int{
		RolePlanner:   1,
		RoleDeveloper: 3,
		RoleTester:    3,
		RoleReviewer:  2,
	}
	for role, wantVisits := range wantNodeVisits {
		node, ok := nodesByID[nodeID(role)]
		if !ok {
			t.Fatalf("missing graph node for role %s", role)
		}
		if node.Status == GraphNodeNotStarted || node.Status == GraphNodeSkipped {
			t.Fatalf("role %s node status = %s, want a genuinely-visited status", role, node.Status)
		}
		if node.Status != GraphNodeCompleted {
			t.Fatalf("role %s node status = %s, want %s", role, node.Status, GraphNodeCompleted)
		}
		if node.Visits != wantVisits {
			t.Fatalf("role %s node visits = %d, want %d", role, node.Visits, wantVisits)
		}
	}

	terminalNode, ok := nodesByID[terminalNodeID(TerminalConditionCompleted)]
	if !ok || terminalNode.Status != GraphNodeCompleted || !terminalNode.IsCurrent {
		t.Fatalf("terminal completed node = %#v, ok=%v", terminalNode, ok)
	}
	if graph.CurrentNodeID != terminalNodeID(TerminalConditionCompleted) {
		t.Fatalf("current node id = %q, want %q", graph.CurrentNodeID, terminalNodeID(TerminalConditionCompleted))
	}
	if graph.ActiveEdgeID != "" {
		t.Fatalf("active edge id = %q, want empty on a terminal run", graph.ActiveEdgeID)
	}
	if graph.RunPaused || graph.BudgetExhausted {
		t.Fatalf("run overlays = paused:%v budget_exhausted:%v, want both false", graph.RunPaused, graph.BudgetExhausted)
	}

	edgesByID := make(map[string]GraphEdge, len(graph.Edges))
	for _, edge := range graph.Edges {
		edgesByID[edge.ID] = edge
	}

	testerLoop := edgesByID[edgeID(RoleTester, OutcomeTestsFailed)]
	if testerLoop.TraversalCount != 1 || testerLoop.Status != GraphEdgeTraversed {
		t.Fatalf("tester->developer loop edge = %#v, want 1 traversal / previously_traversed", testerLoop)
	}
	if testerLoop.MaxTraversals == nil || *testerLoop.MaxTraversals != int(definition.Budgets.MaxTesterToDeveloperLoops) {
		t.Fatalf("tester->developer loop max traversals = %v, want %d",
			testerLoop.MaxTraversals, definition.Budgets.MaxTesterToDeveloperLoops)
	}

	reviewerLoop := edgesByID[edgeID(RoleReviewer, OutcomeChangesRequested)]
	if reviewerLoop.TraversalCount != 1 || reviewerLoop.Status != GraphEdgeTraversed {
		t.Fatalf("reviewer->developer loop edge = %#v, want 1 traversal / previously_traversed", reviewerLoop)
	}
	if reviewerLoop.MaxTraversals == nil || *reviewerLoop.MaxTraversals != int(definition.Budgets.MaxReviewerToDeveloperLoops) {
		t.Fatalf("reviewer->developer loop max traversals = %v, want %d",
			reviewerLoop.MaxTraversals, definition.Budgets.MaxReviewerToDeveloperLoops)
	}

	plannerEdge := edgesByID[edgeID(RolePlanner, OutcomePlanReady)]
	if plannerEdge.TraversalCount != 1 || plannerEdge.Status != GraphEdgeTraversed {
		t.Fatalf("planner->developer edge = %#v, want 1 traversal / previously_traversed", plannerEdge)
	}

	developerEdge := edgesByID[edgeID(RoleDeveloper, OutcomeImplementationReady)]
	if developerEdge.TraversalCount != 3 || developerEdge.Status != GraphEdgeTraversed {
		t.Fatalf("developer->tester edge = %#v, want 3 traversals / previously_traversed", developerEdge)
	}

	testerPassedEdge := edgesByID[edgeID(RoleTester, OutcomeTestsPassed)]
	if testerPassedEdge.TraversalCount != 2 || testerPassedEdge.Status != GraphEdgeTraversed {
		t.Fatalf("tester->reviewer edge = %#v, want 2 traversals / previously_traversed", testerPassedEdge)
	}

	reviewApprovedEdge := edgesByID[edgeID(RoleReviewer, OutcomeReviewApproved)]
	if reviewApprovedEdge.TraversalCount != 1 || reviewApprovedEdge.Status != GraphEdgeTraversed {
		t.Fatalf("reviewer->completed edge = %#v, want 1 traversal / previously_traversed", reviewApprovedEdge)
	}

	failedEdge := edgesByID[edgeID(RoleDeveloper, OutcomeTerminalFailure)]
	if failedEdge.TraversalCount != 0 || failedEdge.Status != GraphEdgeAvailable {
		t.Fatalf("never-taken developer->failed edge = %#v, want untouched/available", failedEdge)
	}
	cancelledEdge := edgesByID[edgeID(RolePlanner, OutcomeCancelled)]
	if cancelledEdge.TraversalCount != 0 || cancelledEdge.Status != GraphEdgeAvailable {
		t.Fatalf("never-taken planner->cancelled edge = %#v, want untouched/available", cancelledEdge)
	}
}

// expectedDashboardScenarioEventTypes is the canonical event-type sequence
// this scenario's scripted transitions must produce, in ascending
// chronological (persisted) order. Derived directly from supervisor.go's
// emission order: enterRole emits role.entered; completeRole emits
// role.completed; checkLoopBudget then emits budget.warning exactly when a
// correction loop's traversal count is about to reach one below its
// configured limit (durable_budget_warning_test.go documents this
// "one-before-the-limit" rule) -- which both loop traversals in this
// scenario trigger, since each brings its loop to 1 of a 2-traversal
// ceiling; a non-terminal transition then emits handoff.created followed by
// transition.selected, with loop.traversal.recorded appended only when the
// transition is one of the two correction loops; a terminal transition
// emits transition.selected (no handoff) followed by the run-level terminal
// event (run.completed here). run.started is emitted once before the first
// role executes.
//
// Confirming this exact order end-to-end also exercises a real fix made
// during this milestone's final verification sweep: internal/event/event.go
// previously minted ULID event IDs using crypto/rand entropy directly,
// which does not guarantee monotonically increasing IDs for two events
// minted within the same millisecond. Since store.go's Query relies on
// "ORDER BY id ASC" for chronological replay, that made the persisted order
// of same-millisecond events (such as the budget.warning/handoff.created/
// transition.selected/loop.traversal.recorded cluster emitted here in quick
// succession) nondeterministic across runs. event.go now uses
// ulid.DefaultEntropy(), the library's thread-safe monotonic entropy
// source, which is what makes this assertion reliably reproducible.
func expectedDashboardScenarioEventTypes() []string {
	return []string{
		event.EventMultiAgentRunStarted,

		// 1. planner visit 1: plan_ready -> developer
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentHandoffCreated,
		event.EventMultiAgentTransitionSelected,

		// 2. developer visit 1: implementation_ready -> tester
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentHandoffCreated,
		event.EventMultiAgentTransitionSelected,

		// 3. tester visit 1: tests_failed -> developer (loop tester->developer #1 of 2)
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentBudgetWarning,
		event.EventMultiAgentHandoffCreated,
		event.EventMultiAgentTransitionSelected,
		event.EventMultiAgentLoopTraversal,

		// 4. developer visit 2: implementation_ready -> tester
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentHandoffCreated,
		event.EventMultiAgentTransitionSelected,

		// 5. tester visit 2: tests_passed -> reviewer
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentHandoffCreated,
		event.EventMultiAgentTransitionSelected,

		// 6. reviewer visit 1: changes_requested -> developer (loop reviewer->developer #1 of 2)
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentBudgetWarning,
		event.EventMultiAgentHandoffCreated,
		event.EventMultiAgentTransitionSelected,
		event.EventMultiAgentLoopTraversal,

		// 7. developer visit 3: implementation_ready -> tester
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentHandoffCreated,
		event.EventMultiAgentTransitionSelected,

		// 8. tester visit 3: tests_passed -> reviewer
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentHandoffCreated,
		event.EventMultiAgentTransitionSelected,

		// 9. reviewer visit 2: review_approved -> terminal completed
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentTransitionSelected,
		event.EventMultiAgentRunCompleted,
	}
}
