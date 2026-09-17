package multiagent

import (
	"errors"
	"testing"
)

func TestTransitionResolver(t *testing.T) {
	resolver, err := NewTransitionResolver(validDefinition())
	if err != nil {
		t.Fatalf("new resolver: %v", err)
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
			got, resolveErr := resolver.Resolve(test.role, test.outcome)
			if resolveErr != nil {
				t.Fatalf("resolve: %v", resolveErr)
			}
			if got.To != test.wantTo || got.Terminal != test.terminal {
				t.Fatalf("resolve = %#v, want to=%q terminal=%q", got, test.wantTo, test.terminal)
			}
		})
	}
}

func TestTransitionResolverRejectsInvalidOutcome(t *testing.T) {
	resolver, err := NewTransitionResolver(validDefinition())
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}

	_, err = resolver.Resolve(RolePlanner, OutcomeTestsPassed)
	var transitionErr *InvalidTransitionError
	if !errors.As(err, &transitionErr) {
		t.Fatalf("expected InvalidTransitionError, got %T: %v", err, err)
	}
}
