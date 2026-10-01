package multiagent

import (
	"context"
	"testing"
)

// This file is the regression coverage for the PR4 entry-role bug:
// Supervisor.newRunState used to hardcode RunState.CurrentRole to
// RolePlanner regardless of what the compiled graph's actual entry node
// was, which broke every authored workflow whose entry role isn't literally
// "planner" (e.g. the Security Review template's
// Implementer -> Security Scanner -> Security Reviewer shape, or the
// Documentation Change template's Writer -> Technical Reviewer -> Editor
// shape). The fix derives CurrentRole from graph.EntryNodeID() (resolved
// once, in NewSupervisor, into Supervisor.entryRole) instead.

// TestSupervisorEntryRoleDerivedFromGraphNotHardcodedPlanner compiles a
// synthetic WorkflowDefinition whose entry node's role is "implementer" —
// mimicking the Security Review template's shape, which has no node
// named/roled "planner" at all — and asserts Run() starts by invoking the
// scripted runner for role "implementer", not RolePlanner. Before the fix,
// this would fail immediately: newRunState would set
// CurrentRole = RolePlanner, RoleConfig(RolePlanner) would look up a node
// that does not exist in this graph, and the scripted runner would receive
// an unexpected role.
func TestSupervisorEntryRoleDerivedFromGraphNotHardcodedPlanner(t *testing.T) {
	implementerRole := Role("implementer")

	def := WorkflowDefinition{
		APIVersion: SchemaAPIVersion,
		Kind:       SchemaKind,
		Metadata:   WorkflowMetadata{Name: "security-review", ID: "security-review", Version: "1.0.0"},
		Spec: WorkflowSpec{
			EntryNode: "implementer",
			Nodes: []SchemaNode{
				roleNode("implementer", "implementation_ready"),
				terminalNode("completed", "completed"),
			},
			Edges: []SchemaEdge{
				edge("implementer-to-completed", "implementer", "completed", "implementation_ready"),
			},
		},
	}

	graph, diags, err := Compile(def, nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile returned error: %v (diags: %v)", err, diags)
	}
	if got, want := graph.EntryNodeID(), nodeID(implementerRole); got != want {
		t.Fatalf("EntryNodeID() = %q, want %q", got, want)
	}

	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		implementerRole: {{result: RoleRunResult{
			Outcome:         TransitionOutcome("implementation_ready"),
			LocalIterations: 1,
		}}},
	}}
	sink := &captureEventSink{}
	supervisor, err := NewSupervisor(graph, runner, sink, SupervisorOptions{})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	state, err := supervisor.Run(context.Background(), testRunRequest())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(runner.calls) == 0 || runner.calls[0] != implementerRole {
		t.Fatalf("runner.calls = %v, want first call to be role %q", runner.calls, implementerRole)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("state.Status = %q, want %q", state.Status, RunStatusCompleted)
	}
	if state.TerminalOutcome == nil || state.TerminalOutcome.Condition != TerminalConditionCompleted {
		t.Fatalf("state.TerminalOutcome = %#v", state.TerminalOutcome)
	}
}

// TestSupervisorPhase1CompatEntryRoleStillPlanner confirms the fix does not
// regress the Phase 1 compat path: CompatAdaptDefinition's entry node
// genuinely IS the RolePlanner node, so CurrentRole must still resolve to
// RolePlanner for the reference workflow — now derived from
// graph.EntryNodeID() instead of hardcoded, but producing the exact same
// observed behavior as before.
func TestSupervisorPhase1CompatEntryRoleStillPlanner(t *testing.T) {
	definition := validDefinition()
	graph := mustAdaptGraph(t, definition)

	if got, want := graph.EntryNodeID(), nodeID(RolePlanner); got != want {
		t.Fatalf("EntryNodeID() = %q, want %q", got, want)
	}

	runner := newScriptedRunner(map[Role][]TransitionOutcome{
		RolePlanner:   {OutcomePlanReady},
		RoleDeveloper: {OutcomeImplementationReady},
		RoleTester:    {OutcomeTestsPassed},
		RoleReviewer:  {OutcomeReviewApproved},
	})
	sink := &captureEventSink{}
	supervisor := newTestSupervisor(t, definition, runner, sink)

	state, err := supervisor.Run(context.Background(), testRunRequest())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(runner.calls) == 0 || runner.calls[0] != RolePlanner {
		t.Fatalf("runner.calls = %v, want first call to be %q", runner.calls, RolePlanner)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("state.Status = %q, want %q", state.Status, RunStatusCompleted)
	}
}

// TestNewSupervisorRejectsGraphWithInvalidEntryRole defends the
// should-be-unreachable-per-PR2-validation case: a *CompiledGraph whose
// entry node is not a role-kind node (e.g. a terminal entry node) must make
// NewSupervisor fail loudly with a clear error, not panic and not silently
// fall back to RolePlanner.
func TestNewSupervisorRejectsGraphWithInvalidEntryRole(t *testing.T) {
	def := WorkflowDefinition{
		APIVersion: SchemaAPIVersion,
		Kind:       SchemaKind,
		Metadata:   WorkflowMetadata{Name: "terminal-entry", ID: "terminal-entry", Version: "1.0.0"},
		Spec: WorkflowSpec{
			EntryNode: "completed",
			Nodes: []SchemaNode{
				terminalNode("completed", "completed"),
			},
		},
	}

	// This shape is deliberately unusual enough that ValidateDefinition may
	// itself reject it (an entry node with no role is not something PR2's
	// validator is designed to accept); Compile is expected to fail here in
	// the normal case. This test's real assertion is the fallback branch
	// below: even if Compile somehow produced a graph, NewSupervisor must
	// still reject a non-role entry node rather than accept it.
	graph, _, err := Compile(def, nil, CompileOptions{})
	if err != nil {
		t.Skipf("Compile rejected the terminal-entry fixture before NewSupervisor could be exercised (expected/acceptable): %v", err)
	}

	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{}}
	sink := &captureEventSink{}
	_, err = NewSupervisor(graph, runner, sink, SupervisorOptions{})
	if err == nil {
		t.Fatal("NewSupervisor: expected an error for a graph whose entry node is not a role node, got nil")
	}
}
