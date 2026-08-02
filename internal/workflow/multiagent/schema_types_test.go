package multiagent

import (
	"reflect"
	"testing"
)

// TestWorkflowDefinitionYAMLJSONRoundTripIdentical asserts that the YAML and
// JSON forms of the same reference workflow decode to identical
// WorkflowDefinition values, proving the YAML->generic-map->JSON bridge
// (loader.go's normalizeYAMLValue + decodeStrictWorkflowJSON) drives both
// formats off the single json:"..." tag set with no divergence.
func TestWorkflowDefinitionYAMLJSONRoundTripIdentical(t *testing.T) {
	yamlDef, _, yamlDiags, err := LoadDefinition("testdata/schema/valid/software-delivery.yaml")
	if err != nil {
		t.Fatalf("load yaml: %v", err)
	}
	if yamlDiags.HasErrors() {
		t.Fatalf("unexpected yaml diagnostics: %v", yamlDiags)
	}

	jsonDef, _, jsonDiags, err := LoadDefinition("testdata/schema/valid/software-delivery.json")
	if err != nil {
		t.Fatalf("load json: %v", err)
	}
	if jsonDiags.HasErrors() {
		t.Fatalf("unexpected json diagnostics: %v", jsonDiags)
	}

	if !reflect.DeepEqual(yamlDef, jsonDef) {
		t.Fatalf("yaml and json decoded to different values:\nyaml=%#v\njson=%#v", yamlDef, jsonDef)
	}

	// Sanity: the fixture actually exercises the fields we care about.
	if yamlDef.APIVersion != SchemaAPIVersion {
		t.Errorf("apiVersion = %q, want %q", yamlDef.APIVersion, SchemaAPIVersion)
	}
	if yamlDef.Kind != SchemaKind {
		t.Errorf("kind = %q, want %q", yamlDef.Kind, SchemaKind)
	}
	if yamlDef.Metadata.ID != "software-delivery" {
		t.Errorf("metadata.id = %q, want normalized-from-name %q", yamlDef.Metadata.ID, "software-delivery")
	}
	if len(yamlDef.Spec.Nodes) != 6 {
		t.Fatalf("nodes = %d, want 6", len(yamlDef.Spec.Nodes))
	}
	if len(yamlDef.Spec.Edges) != 6 {
		t.Fatalf("edges = %d, want 6", len(yamlDef.Spec.Edges))
	}

	tester := yamlDef.Spec.Nodes[2]
	if tester.ID != "tester" || tester.Role != "tester" {
		t.Errorf("nodes[2] = %+v, want tester node", tester)
	}
	if len(tester.AllowedOutcomes) != 2 || tester.AllowedOutcomes[0] != "tests_passed" || tester.AllowedOutcomes[1] != "tests_failed" {
		t.Errorf("tester.AllowedOutcomes = %v", tester.AllowedOutcomes)
	}

	loopEdge := yamlDef.Spec.Edges[2]
	if loopEdge.ID != "tester-to-dev" {
		t.Fatalf("edges[2] = %+v, want tester-to-dev", loopEdge)
	}
	if loopEdge.Loop == nil {
		t.Fatal("tester-to-dev edge expected a loop policy")
	}
	if loopEdge.Loop.MaxTraversals != 3 || loopEdge.Loop.OnExhausted != "fail" {
		t.Errorf("loop = %+v, want maxTraversals=3 onExhausted=fail", loopEdge.Loop)
	}
}

// TestWorkflowDefinitionOmittedOptionalFields asserts that fields absent from
// the source document decode to their Go zero values (nil pointers for
// *SchemaLimit/*SchemaRetryPolicy/etc, nil slices, zero structs) rather than
// some non-zero placeholder — important since callers (the validator, PR2)
// need to distinguish "not set" from "set to zero".
func TestWorkflowDefinitionOmittedOptionalFields(t *testing.T) {
	minimal := `apiVersion: prism.dev/v1alpha1
kind: MultiAgentWorkflow
metadata:
  name: minimal
  version: "0.1.0"
spec:
  entryNode: only
  budgets: {}
  nodes:
    - id: only
      type: role
      role: only
  edges: []
`
	parsed, _, parseDiags, loadErr := LoadDefinitionBytes([]byte(minimal), "yaml", "minimal.yaml")
	if loadErr != nil {
		t.Fatalf("load minimal: %v", loadErr)
	}
	if parseDiags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", parseDiags)
	}

	node := parsed.Spec.Nodes[0]
	if node.MaxVisits != nil {
		t.Errorf("MaxVisits = %v, want nil", node.MaxVisits)
	}
	if node.LocalIterations != nil {
		t.Errorf("LocalIterations = %v, want nil", node.LocalIterations)
	}
	if node.TokenBudget != nil {
		t.Errorf("TokenBudget = %v, want nil", node.TokenBudget)
	}
	if node.Retry != nil {
		t.Errorf("Retry = %v, want nil", node.Retry)
	}
	if node.Approval != nil {
		t.Errorf("Approval = %v, want nil", node.Approval)
	}
	if node.Validation != nil {
		t.Errorf("Validation = %v, want nil", node.Validation)
	}
	if node.InputContract != nil {
		t.Errorf("InputContract = %v, want nil", node.InputContract)
	}
	if node.OutputContract != nil {
		t.Errorf("OutputContract = %v, want nil", node.OutputContract)
	}
	if node.AllowedOutcomes != nil {
		t.Errorf("AllowedOutcomes = %v, want nil", node.AllowedOutcomes)
	}
	if parsed.Spec.Budgets.MaxTransitions != nil {
		t.Errorf("Budgets.MaxTransitions = %v, want nil", parsed.Spec.Budgets.MaxTransitions)
	}
	if parsed.Spec.Edges != nil && len(parsed.Spec.Edges) != 0 {
		t.Errorf("Edges = %v, want empty", parsed.Spec.Edges)
	}
	if !reflect.DeepEqual(parsed.Spec.Defaults, WorkflowDefaults{}) {
		t.Errorf("Defaults = %+v, want zero value", parsed.Spec.Defaults)
	}
}
