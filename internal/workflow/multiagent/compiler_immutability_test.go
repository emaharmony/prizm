package multiagent

import "testing"

// CompiledGraph has no exported fields and no setters (see compiler.go's
// package doc comment) — that alone makes it structurally impossible for a
// caller to mutate its internal state directly. What remains to verify at
// runtime is that every accessor returning a slice or map hands back an
// *independent copy*, not a reference into the graph's internal storage:
// mutating what an accessor returns must never be observable through a
// second, independent call to the same accessor (or a related one).

func TestNodesReturnsIndependentCopy(t *testing.T) {
	graph, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	nodes := graph.Nodes()
	for i := range nodes {
		if nodes[i].ID == nodeID(RoleDeveloper) {
			nodes[i].DisplayName = "MUTATED"
			nodes[i].RoleConfig.AgentRef = "MUTATED"
			if nodes[i].RoleConfig.AllowedTools == nil {
				nodes[i].RoleConfig.AllowedTools = []string{"MUTATED"}
			} else {
				nodes[i].RoleConfig.AllowedTools[0] = "MUTATED"
			}
		}
	}

	refetched := graph.Nodes()
	for _, n := range refetched {
		if n.ID != nodeID(RoleDeveloper) {
			continue
		}
		if n.DisplayName == "MUTATED" {
			t.Error("mutating a returned CompiledNode.DisplayName leaked into the graph's internal state")
		}
		if n.RoleConfig.AgentRef == "MUTATED" {
			t.Error("mutating a returned CompiledNode.RoleConfig.AgentRef leaked into the graph's internal state")
		}
		for _, tool := range n.RoleConfig.AllowedTools {
			if tool == "MUTATED" {
				t.Error("mutating a returned CompiledNode.RoleConfig.AllowedTools leaked into the graph's internal state")
			}
		}
	}

	rc, ok := graph.RoleConfig(RoleDeveloper)
	if !ok {
		t.Fatal("expected RoleConfig for developer")
	}
	if rc.AgentRef == "MUTATED" {
		t.Error("mutation via Nodes() leaked into RoleConfig()'s independently returned copy")
	}
}

func TestEdgesReturnsIndependentCopy(t *testing.T) {
	graph, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	edges := graph.Edges()
	if len(edges) == 0 {
		t.Fatal("expected at least one edge")
	}
	originalLabel := edges[0].Label
	edges[0].Label = "MUTATED"

	refetched := graph.Edges()
	if refetched[0].Label != originalLabel {
		t.Errorf("mutating a returned CompiledEdge leaked into the graph's internal state: got %q, want %q", refetched[0].Label, originalLabel)
	}
}

func TestTerminalNodeIDsReturnsIndependentCopy(t *testing.T) {
	graph, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	ids := graph.TerminalNodeIDs()
	if len(ids) == 0 {
		t.Fatal("expected at least one terminal node id")
	}
	ids[0] = "MUTATED"

	refetched := graph.TerminalNodeIDs()
	for _, id := range refetched {
		if id == "MUTATED" {
			t.Error("mutating a returned TerminalNodeIDs() slice leaked into the graph's internal state")
		}
	}
}

func TestBudgetsReturnsIndependentCopy(t *testing.T) {
	graph, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	budgets := graph.Budgets()
	if budgets.MaxVisitsPerRole == nil {
		t.Fatal("expected a non-nil MaxVisitsPerRole map")
	}
	budgets.MaxVisitsPerRole[RoleDeveloper] = Limit(999999)

	refetched := graph.Budgets()
	if refetched.MaxVisitsPerRole[RoleDeveloper] == Limit(999999) {
		t.Error("mutating a returned Budgets().MaxVisitsPerRole map leaked into the graph's internal state")
	}
}

func TestRoleConfigReturnsIndependentCopy(t *testing.T) {
	graph, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	rc, ok := graph.RoleConfig(RoleDeveloper)
	if !ok {
		t.Fatal("expected RoleConfig for developer")
	}
	rc.AgentRef = "MUTATED"

	refetched, ok := graph.RoleConfig(RoleDeveloper)
	if !ok {
		t.Fatal("expected RoleConfig for developer on second fetch")
	}
	if refetched.AgentRef == "MUTATED" {
		t.Error("mutating a returned RoleConfig() leaked into the graph's internal state")
	}
}
