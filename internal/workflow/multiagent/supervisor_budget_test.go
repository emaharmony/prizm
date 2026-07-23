package multiagent

import (
	"context"
	"errors"
	"testing"

	"github.com/emaharmony/prism/internal/event"
)

func TestSupervisorBudgetEnforcement(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*Definition)
		runner     func() *scriptedRunner
		wantBudget string
		wantRole   Role
	}{
		{
			name: "maximum total transitions",
			configure: func(definition *Definition) {
				definition.Budgets.MaxTransitions = 1
			},
			runner: func() *scriptedRunner {
				return newScriptedRunner(map[Role][]TransitionOutcome{
					RolePlanner: {OutcomePlanReady},
				})
			},
			wantBudget: "max_transitions",
		},
		{
			name: "maximum visits per role",
			configure: func(definition *Definition) {
				definition.Budgets.MaxVisitsPerRole[RoleDeveloper] = 1
			},
			runner: func() *scriptedRunner {
				return newScriptedRunner(map[Role][]TransitionOutcome{
					RolePlanner:   {OutcomePlanReady},
					RoleDeveloper: {OutcomeImplementationReady},
					RoleTester:    {OutcomeTestsFailed},
				})
			},
			wantBudget: "max_visits_per_role",
			wantRole:   RoleDeveloper,
		},
		{
			name: "maximum reviewer correction loops",
			configure: func(definition *Definition) {
				definition.Budgets.MaxReviewerToDeveloperLoops = 1
			},
			runner: func() *scriptedRunner {
				return newScriptedRunner(map[Role][]TransitionOutcome{
					RolePlanner:   {OutcomePlanReady},
					RoleDeveloper: {OutcomeImplementationReady, OutcomeImplementationReady},
					RoleTester:    {OutcomeTestsPassed, OutcomeTestsPassed},
					RoleReviewer:  {OutcomeChangesRequested, OutcomeChangesRequested},
				})
			},
			wantBudget: "max_reviewer_to_developer_loops",
			wantRole:   RoleReviewer,
		},
		{
			name: "maximum global local iterations",
			configure: func(definition *Definition) {
				definition.Budgets.MaxLocalIterations = 1
			},
			runner: func() *scriptedRunner {
				result := scriptedResult(OutcomePlanReady)
				result.LocalIterations = 2
				return &scriptedRunner{scripts: map[Role][]scriptedStep{
					RolePlanner: {{result: result}},
				}}
			},
			wantBudget: "max_local_iterations",
		},
		{
			name: "maximum role local iterations",
			configure: func(definition *Definition) {
				for i := range definition.Roles {
					if definition.Roles[i].Role == RolePlanner {
						definition.Roles[i].MaxLocalIterations = 1
					}
				}
			},
			runner: func() *scriptedRunner {
				result := scriptedResult(OutcomePlanReady)
				result.LocalIterations = 2
				return &scriptedRunner{scripts: map[Role][]scriptedStep{
					RolePlanner: {{result: result}},
				}}
			},
			wantBudget: "role_max_local_iterations",
			wantRole:   RolePlanner,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition()
			test.configure(&definition)
			sink := &captureEventSink{}
			supervisor := newTestSupervisor(t, definition, test.runner(), sink)

			state, err := supervisor.Run(context.Background(), testRunRequest())
			var budgetErr *BudgetExceededError
			if !errors.As(err, &budgetErr) {
				t.Fatalf("expected BudgetExceededError, got %T: %v", err, err)
			}
			if budgetErr.Budget != test.wantBudget {
				t.Errorf("budget = %q, want %q", budgetErr.Budget, test.wantBudget)
			}
			if budgetErr.Role != test.wantRole {
				t.Errorf("budget role = %q, want %q", budgetErr.Role, test.wantRole)
			}
			if state.Status != RunStatusBudgetExhausted {
				t.Errorf("status = %q, want %q", state.Status, RunStatusBudgetExhausted)
			}
			if got := sink.types()[len(sink.types())-1]; got != event.EventMultiAgentBudgetExhausted {
				t.Errorf("last event = %q, want %q", got, event.EventMultiAgentBudgetExhausted)
			}
			if err := state.Validate(definition); err != nil {
				t.Fatalf("budget state invalid: %v", err)
			}
		})
	}
}

func TestSupervisorCorrectionLoopEventOrdering(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := newScriptedRunner(map[Role][]TransitionOutcome{
		RolePlanner:   {OutcomePlanReady},
		RoleDeveloper: {OutcomeImplementationReady},
		RoleTester:    {OutcomeTestsFailed},
	})
	sink := &captureEventSink{}
	sink.onEmit = func(evt event.Event) {
		if evt.Type == event.EventMultiAgentLoopTraversal {
			cancel()
		}
	}
	supervisor := newTestSupervisor(t, validDefinition(), runner, sink)

	_, err := supervisor.Run(ctx, testRunRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation after loop event, got %v", err)
	}

	types := sink.types()
	if len(types) < 3 {
		t.Fatalf("not enough events: %v", types)
	}
	got := types[len(types)-3:]
	want := []string{
		event.EventMultiAgentTransitionSelected,
		event.EventMultiAgentLoopTraversal,
		event.EventMultiAgentRunCancelled,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event suffix = %v, want %v", got, want)
		}
	}
}
