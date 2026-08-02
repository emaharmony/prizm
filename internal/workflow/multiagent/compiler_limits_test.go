package multiagent

import (
	"strconv"
	"testing"
)

// Compile enforces CompileOptions.MaxNodes/MaxEdges with a strict "greater
// than" comparison (len(...) > limit fails; len(...) == limit passes) —
// i.e. the limit itself is an inclusive ceiling, not an exclusive one. These
// tests pin that boundary behavior down explicitly at both edges.

// buildLinearDef returns a minimal, individually valid definition with
// exactly n role nodes chained start -> n2 -> n3 -> ... -> terminal, so
// tests can construct definitions of an exact node/edge count without
// tripping unrelated validator rules.
func buildLinearDef(roleCount int) WorkflowDefinition {
	nodes := make([]SchemaNode, 0, roleCount+1)
	edges := make([]SchemaEdge, 0, roleCount)

	for i := 0; i < roleCount; i++ {
		id := "n" + strconv.Itoa(i)
		nodes = append(nodes, roleNode(id, "next"))
	}
	nodes = append(nodes, terminalNode("end", "completed"))

	for i := 0; i < roleCount; i++ {
		from := "n" + strconv.Itoa(i)
		to := "end"
		if i+1 < roleCount {
			to = "n" + strconv.Itoa(i+1)
		}
		edges = append(edges, edge(from+"-edge", from, to, "next"))
	}

	return WorkflowDefinition{
		APIVersion: SchemaAPIVersion,
		Kind:       SchemaKind,
		Metadata:   WorkflowMetadata{Name: "linear", ID: "linear", Version: "1.0.0"},
		Spec: WorkflowSpec{
			EntryNode: "n0",
			Nodes:     nodes,
			Edges:     edges,
		},
	}
}

func TestDefinitionTooLargeNodesExactlyAtLimitPasses(t *testing.T) {
	def := buildLinearDef(3) // 3 role nodes + 1 terminal = 4 nodes total
	_, diags, err := Compile(def, nil, CompileOptions{MaxNodes: 4})
	if err != nil {
		t.Fatalf("expected success at exactly the node limit, got error: %v (diags: %v)", err, diags)
	}
}

func TestDefinitionTooLargeNodesOneOverLimitFails(t *testing.T) {
	def := buildLinearDef(3) // 4 nodes total
	graph, diags, err := Compile(def, nil, CompileOptions{MaxNodes: 3})
	if graph != nil {
		t.Errorf("expected nil graph, got %+v", graph)
	}
	if err == nil {
		t.Fatal("expected an error for a definition one node over the limit")
	}
	tooLarge, ok := err.(*DefinitionTooLargeError)
	if !ok {
		t.Fatalf("expected *DefinitionTooLargeError, got %T: %v", err, err)
	}
	if tooLarge.Dimension != "nodes" {
		t.Errorf("Dimension = %q, want %q", tooLarge.Dimension, "nodes")
	}
	if tooLarge.Limit != 3 || tooLarge.Got != 4 {
		t.Errorf("Limit/Got = %d/%d, want 3/4", tooLarge.Limit, tooLarge.Got)
	}
	if len(diags) != 1 {
		t.Errorf("expected exactly one diagnostic entry for a size-cap rejection, got %v", diags)
	}
	var ge GraphError = tooLarge
	if ge.Code() != "definition_too_large" {
		t.Errorf("Code() = %q, want %q", ge.Code(), "definition_too_large")
	}
}

func TestDefinitionTooLargeEdgesExactlyAtLimitPasses(t *testing.T) {
	def := buildLinearDef(3) // 3 edges total
	_, diags, err := Compile(def, nil, CompileOptions{MaxEdges: 3})
	if err != nil {
		t.Fatalf("expected success at exactly the edge limit, got error: %v (diags: %v)", err, diags)
	}
}

func TestDefinitionTooLargeEdgesOneOverLimitFails(t *testing.T) {
	def := buildLinearDef(3) // 3 edges total
	graph, _, err := Compile(def, nil, CompileOptions{MaxEdges: 2})
	if graph != nil {
		t.Errorf("expected nil graph, got %+v", graph)
	}
	tooLarge, ok := err.(*DefinitionTooLargeError)
	if !ok {
		t.Fatalf("expected *DefinitionTooLargeError, got %T: %v", err, err)
	}
	if tooLarge.Dimension != "edges" {
		t.Errorf("Dimension = %q, want %q", tooLarge.Dimension, "edges")
	}
	if tooLarge.Limit != 2 || tooLarge.Got != 3 {
		t.Errorf("Limit/Got = %d/%d, want 2/3", tooLarge.Limit, tooLarge.Got)
	}
}

// TestDefinitionTooLargeCheckedBeforeValidation asserts the size cap is
// enforced even against a definition that would ALSO fail validation for
// unrelated reasons (here, an unrouted outcome) — the point being that
// Compile must reject on size before ever calling the (potentially
// expensive) ValidateDefinition pass, not just before returning a graph.
func TestDefinitionTooLargeCheckedBeforeValidation(t *testing.T) {
	def := buildLinearDef(5)
	def.Spec.EntryNode = "does-not-exist" // would also fail structural validation

	_, diags, err := Compile(def, nil, CompileOptions{MaxNodes: 2})
	tooLarge, ok := err.(*DefinitionTooLargeError)
	if !ok {
		t.Fatalf("expected *DefinitionTooLargeError even though the definition is also invalid, got %T: %v", err, err)
	}
	if tooLarge.Dimension != "nodes" {
		t.Errorf("Dimension = %q, want %q", tooLarge.Dimension, "nodes")
	}
	// Exactly the one size-cap diagnostic, not the validator's own findings
	// (structural.entry-node-unknown etc.) — proof the validator never ran.
	if len(diags) != 1 || diags[0].Rule != "compile.definition-too-large" {
		t.Errorf("expected only the compile.definition-too-large diagnostic, got %v", diags)
	}
}

func TestDefaultOptionsUsedWhenZeroValue(t *testing.T) {
	// A zero-value CompileOptions{} must use the package defaults
	// (500/2000/1000), not reject everything as "too large."
	graph, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile with zero-value CompileOptions: %v", err)
	}
	if graph == nil {
		t.Fatal("expected a non-nil graph")
	}
}
