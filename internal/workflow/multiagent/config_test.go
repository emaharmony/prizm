package multiagent

import (
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prism/internal/retry"
)

func TestDefinitionValidate(t *testing.T) {
	definition := validDefinition()
	if err := definition.Validate(); err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}
}

func TestDefinitionRejectsDuplicateRoles(t *testing.T) {
	definition := validDefinition()
	definition.Roles = append(definition.Roles, definition.Roles[0])

	err := definition.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate role") {
		t.Fatalf("expected duplicate role error, got %v", err)
	}
}

func TestDefinitionRejectsUnknownRoleReferences(t *testing.T) {
	definition := validDefinition()
	definition.Transitions[0].To = Role("operator")

	err := definition.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown destination role") {
		t.Fatalf("expected unknown role reference error, got %v", err)
	}
}

func TestDefinitionRejectsInvalidBudgets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Definition)
	}{
		{
			name: "zero transitions",
			mutate: func(d *Definition) {
				d.Budgets.MaxTransitions = 0
			},
		},
		{
			name: "invalid unlimited sentinel",
			mutate: func(d *Definition) {
				d.Budgets.MaxTokens = -2
			},
		},
		{
			name: "missing role visit limit",
			mutate: func(d *Definition) {
				delete(d.Budgets.MaxVisitsPerRole, RoleReviewer)
			},
		},
		{
			name: "zero local iterations",
			mutate: func(d *Definition) {
				d.Roles[0].MaxLocalIterations = 0
			},
		},
		{
			name: "zero time budget",
			mutate: func(d *Definition) {
				d.Roles[0].TimeBudget = 0
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition()
			test.mutate(&definition)
			if err := definition.Validate(); err == nil {
				t.Fatal("expected invalid budget to be rejected")
			}
		})
	}
}

func TestDefinitionRejectsSelfApproval(t *testing.T) {
	definition := validDefinition()
	definition.Roles[1].Approval = ApprovalRequirement{
		Required:  true,
		Approvers: []Role{RoleDeveloper},
	}

	err := definition.Validate()
	if err == nil || !strings.Contains(err.Error(), "cannot approve its own work") {
		t.Fatalf("expected self-approval error, got %v", err)
	}
}

func TestDefinitionRejectsUnsupportedOutcome(t *testing.T) {
	definition := validDefinition()
	definition.Transitions[0].Outcome = TransitionOutcome("looks_good")

	err := definition.Validate()
	if err == nil || !strings.Contains(err.Error(), "unsupported outcome") {
		t.Fatalf("expected unsupported outcome error, got %v", err)
	}
}

func TestDefinitionValidateTransition(t *testing.T) {
	definition := validDefinition()
	if err := definition.ValidateTransition(RoleTester, OutcomeTestsFailed, RoleDeveloper, ""); err != nil {
		t.Fatalf("declared transition rejected: %v", err)
	}
	if err := definition.ValidateTransition(RolePlanner, OutcomeTestsPassed, RoleReviewer, ""); err == nil {
		t.Fatal("expected invalid role/outcome combination to be rejected")
	}
	if err := definition.ValidateTransition(RoleTester, OutcomeTestsPassed, RoleDeveloper, ""); err == nil {
		t.Fatal("expected undeclared destination to be rejected")
	}
}

func validDefinition() Definition {
	roleConfig := func(role Role, agent string, capabilities ...string) RoleConfig {
		return RoleConfig{
			Role:               role,
			AgentRef:           agent,
			AllowedTools:       []string{"read_file"},
			Capabilities:       capabilities,
			MaxLocalIterations: 3,
			TokenBudget:        10_000,
			TimeBudget:         10 * time.Minute,
			Retry:              retry.DefaultRetryConfig(),
		}
	}

	planner := roleConfig(RolePlanner, "astraea", "plan")
	developer := roleConfig(RoleDeveloper, "lumi", "code", "test")
	developer.Approval = ApprovalRequirement{
		Required:  true,
		Approvers: []Role{RoleReviewer},
	}
	tester := roleConfig(RoleTester, "verifier", "test")
	tester.ValidationProfiles = []string{"go_test_all"}
	reviewer := roleConfig(RoleReviewer, "reviewer", "review")

	return Definition{
		ID:      "phase1-reference",
		Version: 1,
		Roles:   []RoleConfig{planner, developer, tester, reviewer},
		Transitions: []TransitionRule{
			{From: RolePlanner, Outcome: OutcomePlanReady, To: RoleDeveloper},
			{From: RoleDeveloper, Outcome: OutcomeImplementationReady, To: RoleTester},
			{From: RoleTester, Outcome: OutcomeTestsPassed, To: RoleReviewer},
			{From: RoleTester, Outcome: OutcomeTestsFailed, To: RoleDeveloper},
			{From: RoleReviewer, Outcome: OutcomeChangesRequested, To: RoleDeveloper},
			{From: RoleReviewer, Outcome: OutcomeReviewApproved, Terminal: TerminalConditionCompleted},
			{From: RoleDeveloper, Outcome: OutcomeTerminalFailure, Terminal: TerminalConditionFailed},
			{From: RolePlanner, Outcome: OutcomeCancelled, Terminal: TerminalConditionCancelled},
		},
		Budgets: BudgetLimits{
			MaxTransitions: 20,
			MaxVisitsPerRole: map[Role]Limit{
				RolePlanner:   2,
				RoleDeveloper: 6,
				RoleTester:    6,
				RoleReviewer:  4,
			},
			MaxLocalIterations: 20,
			MaxRetries:         8,
			MaxTokens:          100_000,
			MaxRepeatedFailure: 3,
			MaxDuration:        time.Hour,
		},
	}
}
