package multiagent

import (
	"sort"
	"testing"
)

// sortedSCCs returns a copy of sccs with each component's members sorted,
// and the components themselves sorted by their (now-sorted) first member,
// so test assertions don't depend on Tarjan's or map iteration's incidental
// ordering.
func sortedSCCs(sccs [][]string) [][]string {
	out := make([][]string, len(sccs))
	for i, scc := range sccs {
		cp := append([]string(nil), scc...)
		sort.Strings(cp)
		out[i] = cp
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func edge(id, from, to, outcome string) SchemaEdge {
	return SchemaEdge{ID: id, From: from, To: to, When: SchemaCondition{Outcome: outcome}}
}

func TestTarjanSCCLinearChainNoCycle(t *testing.T) {
	nodeIDs := []string{"a", "b", "c"}
	edges := []SchemaEdge{
		edge("a-b", "a", "b", "next"),
		edge("b-c", "b", "c", "next"),
	}
	sccs := sortedSCCs(tarjanSCC(nodeIDs, edges))
	if len(sccs) != 3 {
		t.Fatalf("got %d SCCs, want 3 (one per node): %v", len(sccs), sccs)
	}
	for _, scc := range sccs {
		if len(scc) != 1 {
			t.Errorf("expected every SCC in a linear chain to be a singleton, got %v", scc)
		}
	}
}

func TestTarjanSCCSelfLoop(t *testing.T) {
	nodeIDs := []string{"a", "b"}
	edges := []SchemaEdge{
		edge("a-a", "a", "a", "retry"),
		edge("a-b", "a", "b", "done"),
	}
	sccs := tarjanSCC(nodeIDs, edges)
	// Tarjan itself reports a self-loop node as a singleton SCC (its "cycle"
	// status is derived separately in validateCycles by checking for a
	// self-edge, not by SCC size) — this test only asserts every node is
	// still accounted for exactly once.
	total := 0
	for _, scc := range sccs {
		total += len(scc)
	}
	if total != len(nodeIDs) {
		t.Fatalf("SCCs cover %d node slots, want %d", total, len(nodeIDs))
	}
}

func TestTarjanSCCTwoNodeCycle(t *testing.T) {
	nodeIDs := []string{"a", "b", "c"}
	edges := []SchemaEdge{
		edge("a-b", "a", "b", "next"),
		edge("b-a", "b", "a", "retry"),
		edge("a-c", "a", "c", "done"),
	}
	sccs := sortedSCCs(tarjanSCC(nodeIDs, edges))
	found := false
	for _, scc := range sccs {
		if len(scc) == 2 && scc[0] == "a" && scc[1] == "b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a 2-node SCC {a,b}, got %v", sccs)
	}
}

func TestTarjanSCCThreeNodeCycle(t *testing.T) {
	nodeIDs := []string{"a", "b", "c", "d"}
	edges := []SchemaEdge{
		edge("a-b", "a", "b", "next"),
		edge("b-c", "b", "c", "next"),
		edge("c-a", "c", "a", "retry"),
		edge("a-d", "a", "d", "done"),
	}
	sccs := sortedSCCs(tarjanSCC(nodeIDs, edges))
	found := false
	for _, scc := range sccs {
		if len(scc) == 3 {
			want := []string{"a", "b", "c"}
			if scc[0] == want[0] && scc[1] == want[1] && scc[2] == want[2] {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected a 3-node SCC {a,b,c}, got %v", sccs)
	}
}

// buildLinearThenCycleDef constructs a small workflow: entry -> a -> b -> a
// (a 2-node cycle between a and b), with c as a reachable terminal off of a.
// Both edges of the {a,b} cycle need a bounded loop: policy for this
// validator's SCC-wide rule (a-b's is fixed here; loopEdge — normally b->a —
// is the one each test case varies).
func buildLinearThenCycleDef(loopEdge SchemaEdge) WorkflowDefinition {
	aToB := edge("a-b", "a", "b", "retry")
	aToB.Loop = &SchemaLoopPolicy{MaxTraversals: 999, OnExhausted: "fail"}

	return WorkflowDefinition{
		APIVersion: SchemaAPIVersion,
		Kind:       SchemaKind,
		Metadata:   WorkflowMetadata{Name: "cycle-fixture", ID: "cycle-fixture", Version: "1.0.0"},
		Spec: WorkflowSpec{
			EntryNode: "entry",
			Nodes: []SchemaNode{
				{ID: "entry", Type: "role", AgentProfile: "p", AllowedOutcomes: []string{"go"}},
				{ID: "a", Type: "role", AgentProfile: "p", AllowedOutcomes: []string{"retry", "done"}},
				{ID: "b", Type: "role", AgentProfile: "p", AllowedOutcomes: []string{"back"}},
				{ID: "escape", Type: "role", AgentProfile: "p", AllowedOutcomes: []string{"finish"}},
				{ID: "c", Type: "terminal", TerminalCondition: "completed"},
			},
			Edges: []SchemaEdge{
				edge("entry-a", "entry", "a", "go"),
				aToB,
				loopEdge, // b -> a, outcome "back"
				edge("a-c", "a", "c", "done"),
				edge("escape-c", "escape", "c", "finish"),
			},
		},
	}
}

func TestValidateCyclesBoundedCycleWithValidRouteToExit(t *testing.T) {
	loop := edge("b-a", "b", "a", "back")
	loop.Loop = &SchemaLoopPolicy{MaxTraversals: 3, OnExhausted: "route_to", RouteTo: "escape"}
	def := buildLinearThenCycleDef(loop)

	diags := validateCycles(def, nil)
	for _, d := range diags {
		if d.Severity == SeverityError {
			t.Errorf("unexpected error diagnostic for a validly-bounded cycle with a valid escape route: %v", d)
		}
	}
}

func TestValidateCyclesUnboundedCycleMissingLoop(t *testing.T) {
	loop := edge("b-a", "b", "a", "back") // no Loop field at all
	def := buildLinearThenCycleDef(loop)

	diags := validateCycles(def, nil)
	if !hasDiagnostic(diags, "cycle.unbounded-loop-edge", "", "b-a") {
		t.Fatalf("expected cycle.unbounded-loop-edge for edge b-a, got %v", diags)
	}
}

func TestValidateCyclesRouteToInsideSameCycleRejected(t *testing.T) {
	loop := edge("b-a", "b", "a", "back")
	// RouteTo "a" is a member of the very cycle {a,b} this edge belongs to,
	// so it is not a real escape.
	loop.Loop = &SchemaLoopPolicy{MaxTraversals: 3, OnExhausted: "route_to", RouteTo: "a"}
	def := buildLinearThenCycleDef(loop)

	diags := validateCycles(def, nil)
	if !hasDiagnostic(diags, "cycle.invalid-exhaustion-route", "", "b-a") {
		t.Fatalf("expected cycle.invalid-exhaustion-route for edge b-a routing back inside its own cycle, got %v", diags)
	}
}
