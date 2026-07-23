package multiagent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prism/internal/cost"
)

func TestRunStateValidateAndJSONRoundTrip(t *testing.T) {
	definition := validDefinition()
	state := validRunState()
	if err := state.Validate(definition); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	var decoded RunState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if !reflect.DeepEqual(state, decoded) {
		t.Fatalf("state round-trip mismatch:\nwant: %#v\n got: %#v", state, decoded)
	}
	if err := decoded.Validate(definition); err != nil {
		t.Fatalf("round-tripped state rejected: %v", err)
	}
}

func TestRunStateRepresentsBudgetExhaustion(t *testing.T) {
	definition := validDefinition()
	state := validRunState()
	terminalAt := state.UpdatedAt.Add(time.Minute)
	state.Status = RunStatusBudgetExhausted
	state.CompletedAt = &terminalAt
	state.TerminalOutcome = &TerminalOutcome{
		Condition: TerminalConditionBudgetExhausted,
		Reason:    "maximum total transitions reached",
		At:        terminalAt,
	}
	if err := state.Validate(definition); err != nil {
		t.Fatalf("budget-exhausted state rejected: %v", err)
	}
}

func TestRunStateRejectsInvariantViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RunState)
		want   string
	}{
		{
			name: "negative accounting",
			mutate: func(s *RunState) {
				role := s.RoleStates[RoleDeveloper]
				role.LocalIterations = -1
				s.RoleStates[RoleDeveloper] = role
			},
			want: "local_iterations must be non-negative",
		},
		{
			name: "unknown status",
			mutate: func(s *RunState) {
				s.Status = RunStatus("lost")
			},
			want: "unknown run status",
		},
		{
			name: "terminal outcome on active run",
			mutate: func(s *RunState) {
				s.TerminalOutcome = &TerminalOutcome{
					Condition: TerminalConditionFailed,
					Reason:    "unexpected",
					At:        s.UpdatedAt,
				}
			},
			want: "non-terminal state must not have terminal_outcome",
		},
		{
			name: "mismatched handoff run",
			mutate: func(s *RunState) {
				s.LatestHandoff.RunID = "other-run"
			},
			want: "latest_handoff.run_id does not match",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := validRunState()
			test.mutate(&state)
			err := state.Validate(validDefinition())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func validRunState() RunState {
	createdAt := time.Date(2026, 7, 23, 18, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	plannerCompletedAt := updatedAt.Add(-30 * time.Second)
	plannerEnteredAt := createdAt
	developerEnteredAt := plannerCompletedAt
	handoff := validHandoff()
	handoff.CreatedAt = plannerCompletedAt

	return RunState{
		SchemaVersion:   RunStateSchemaVersion,
		RunID:           "run-001",
		WorkflowID:      "phase1-reference",
		WorkflowVersion: 1,
		CurrentRole:     RoleDeveloper,
		CurrentTask: TaskReference{
			ID:          "task-001",
			Description: "Build the Phase 1 reference feature.",
		},
		Status: RunStatusRunning,
		RoleStates: map[Role]RoleState{
			RolePlanner: {
				Role:            RolePlanner,
				Status:          RoleStatusCompleted,
				Visits:          1,
				LocalIterations: 1,
				LastOutcome:     OutcomePlanReady,
				EnteredAt:       &plannerEnteredAt,
				UpdatedAt:       plannerCompletedAt,
				CompletedAt:     &plannerCompletedAt,
			},
			RoleDeveloper: {
				Role:      RoleDeveloper,
				Status:    RoleStatusRunning,
				Visits:    1,
				EnteredAt: &developerEnteredAt,
				UpdatedAt: updatedAt,
			},
			RoleTester: {
				Role:      RoleTester,
				Status:    RoleStatusPending,
				UpdatedAt: createdAt,
			},
			RoleReviewer: {
				Role:      RoleReviewer,
				Status:    RoleStatusPending,
				UpdatedAt: createdAt,
			},
		},
		TransitionCount: 1,
		BudgetUsage: BudgetUsage{
			Tokens: cost.TokenUsage{
				PromptTokens:     120,
				CompletionTokens: 80,
				TotalTokens:      200,
				EstimatedCostUsd: 0.001,
			},
			TotalLocalIterations: 1,
			Elapsed:              time.Minute,
		},
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		LatestHandoff: &handoff,
	}
}
