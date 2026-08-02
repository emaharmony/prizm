package multiagent

import (
	"errors"
	"testing"
)

// graph_resolve_test.go replaces the deleted resolver_test.go: it exercises
// the exact same test intent and assertions TestTransitionResolver /
// TestTransitionResolverRejectsInvalidOutcome had against the now-deleted
// *TransitionResolver, retargeted to CompiledGraph.Resolve via
// CompatAdaptDefinition — CompiledGraph absorbed TransitionResolver entirely
// in PR4 (see compiler.go's Resolve doc).
func TestCompiledGraphResolve(t *testing.T) {
	graph, err := CompatAdaptDefinition(validDefinition())
	if err != nil {
		t.Fatalf("adapt definition: %v", err)
	}

	tests := []struct {
		name     string
		role     Role
		outcome  TransitionOutcome
		wantTo   Role
		terminal TerminalCondition
	}{
		{
			name:    "planner to developer",
			role:    RolePlanner,
			outcome: OutcomePlanReady,
			wantTo:  RoleDeveloper,
		},
		{
			name:    "tester correction loop",
			role:    RoleTester,
			outcome: OutcomeTestsFailed,
			wantTo:  RoleDeveloper,
		},
		{
			name:     "review completes",
			role:     RoleReviewer,
			outcome:  OutcomeReviewApproved,
			terminal: TerminalConditionCompleted,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, resolveErr := graph.Resolve(test.role, test.outcome)
			if resolveErr != nil {
				t.Fatalf("resolve: %v", resolveErr)
			}
			if got.To != test.wantTo || got.Terminal != test.terminal {
				t.Fatalf("resolve = %#v, want to=%q terminal=%q", got, test.wantTo, test.terminal)
			}
		})
	}
}

func TestCompiledGraphResolveRejectsInvalidOutcome(t *testing.T) {
	graph, err := CompatAdaptDefinition(validDefinition())
	if err != nil {
		t.Fatalf("adapt definition: %v", err)
	}

	_, err = graph.Resolve(RolePlanner, OutcomeTestsPassed)
	var transitionErr *InvalidTransitionError
	if !errors.As(err, &transitionErr) {
		t.Fatalf("expected InvalidTransitionError, got %T: %v", err, err)
	}
}
