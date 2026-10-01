package multiagent

import "testing"

func TestExecutionPolicyValidation(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].Execution = &SchemaExecutionPolicy{
		Lane:         "warp",
		Cadence:      "not-a-duration",
		MaxActions:   schemaLimitPtr(0),
		Interrupts:   []string{""},
		Verification: []string{""},
	}
	diags := ValidateDefinition(def, nil)
	want := map[string]bool{
		"execution.invalid-lane":       false,
		"execution.invalid-cadence":    false,
		"budget.invalid-limit":         false,
		"execution.empty-interrupt":    false,
		"execution.empty-verification": false,
	}
	for _, d := range diags {
		if _, ok := want[d.Rule]; ok {
			want[d.Rule] = true
		}
	}
	for rule, found := range want {
		if !found {
			t.Errorf("expected diagnostic %q, got %#v", rule, diags)
		}
	}
}

func schemaLimitPtr(v SchemaLimit) *SchemaLimit { return &v }
