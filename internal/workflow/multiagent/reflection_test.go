package multiagent

import "testing"

func TestValidateReflectionPolicyRequiresTypedRoles(t *testing.T) {
	definition := baseDef()
	definition.Spec.Reflection = &SchemaReflectionPolicy{Enabled: true, Role: "missing", ReplanRole: "start", MaxReplans: 3, MemoryScope: "global", Triggers: []string{"unknown"}}
	diagnostics := ValidateDefinition(definition, nil)
	for _, rule := range []string{"reflection.missing-reflector", "reflection.invalid-replan-budget", "reflection.invalid-memory-scope", "reflection.invalid-trigger"} {
		found := false
		for _, diagnostic := range diagnostics {
			if diagnostic.Rule == rule {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing diagnostic %q in %#v", rule, diagnostics)
		}
	}
}
