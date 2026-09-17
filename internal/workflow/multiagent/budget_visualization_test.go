package multiagent

import (
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/cost"
)

func TestBuildBudgetVisualizationThresholds(t *testing.T) {
	limits := BudgetLimits{
		MaxTransitions:              100,
		MaxLocalIterations:          100,
		MaxRetries:                  100,
		MaxTokens:                   100,
		MaxRepeatedFailure:          100,
		MaxTesterToDeveloperLoops:   100,
		MaxReviewerToDeveloperLoops: 100,
		MaxDuration:                 100 * time.Second,
		MaxVisitsPerRole: map[Role]Limit{
			RolePlanner: 100,
		},
	}

	tests := []struct {
		name string
		used int
		want BudgetLevel
	}{
		{"well under threshold", 10, BudgetNormal},
		{"just under approaching", 74, BudgetNormal},
		{"exactly at approaching boundary", 75, BudgetApproaching},
		{"between approaching and critical", 80, BudgetApproaching},
		{"just under critical", 89, BudgetApproaching},
		{"exactly at critical boundary", 90, BudgetCritical},
		{"between critical and exhausted", 95, BudgetCritical},
		{"exactly at exhausted boundary", 100, BudgetExhaustedLevel},
		{"over the limit", 120, BudgetExhaustedLevel},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usage := BudgetUsage{
				Tokens:               cost.TokenUsage{TotalTokens: test.used},
				TotalLocalIterations: test.used,
				RepeatedFailures:     test.used,
			}
			loops := LoopTraversalCounts{
				TesterToDeveloper:   test.used,
				ReviewerToDeveloper: test.used,
			}
			elapsed := time.Duration(test.used) * time.Second

			viz := BuildBudgetVisualization(usage, limits, loops, elapsed, test.used, map[Role]int{RolePlanner: test.used})

			for name, dimension := range map[string]BudgetDimension{
				"tokens":            viz.Tokens,
				"local_iterations":  viz.LocalIterations,
				"repeated_failures": viz.RepeatedFailures,
				"transitions":       viz.Transitions,
				"elapsed":           viz.Elapsed,
				"role_visits":       viz.RoleVisits[RolePlanner],
				"loop_tester":       viz.LoopTraversals[LoopTesterToDeveloper],
				"loop_reviewer":     viz.LoopTraversals[LoopReviewerToDeveloper],
			} {
				if dimension.Level != test.want {
					t.Errorf("%s level = %s, want %s (used=%d)", name, dimension.Level, test.want, test.used)
				}
				if dimension.Used != test.used {
					t.Errorf("%s used = %d, want %d", name, dimension.Used, test.used)
				}
				if dimension.Limit == nil || *dimension.Limit != 100 {
					t.Errorf("%s limit = %v, want 100", name, dimension.Limit)
				}
			}
		})
	}
}

func TestBuildBudgetVisualizationUnlimitedNeverApproaches(t *testing.T) {
	limits := BudgetLimits{
		MaxTransitions:              Unlimited,
		MaxLocalIterations:          Unlimited,
		MaxRetries:                  Unlimited,
		MaxTokens:                   Unlimited,
		MaxRepeatedFailure:          Unlimited,
		MaxTesterToDeveloperLoops:   Unlimited,
		MaxReviewerToDeveloperLoops: Unlimited,
		MaxDuration:                 UnlimitedDuration,
		MaxVisitsPerRole: map[Role]Limit{
			RolePlanner: Unlimited,
		},
	}
	usage := BudgetUsage{
		Tokens:               cost.TokenUsage{TotalTokens: 1_000_000},
		TotalLocalIterations: 1_000_000,
		RepeatedFailures:     1_000_000,
	}
	loops := LoopTraversalCounts{
		TesterToDeveloper:   1_000_000,
		ReviewerToDeveloper: 1_000_000,
	}

	viz := BuildBudgetVisualization(usage, limits, loops, 1_000_000*time.Second, 1_000_000, map[Role]int{RolePlanner: 1_000_000})

	dimensions := map[string]BudgetDimension{
		"tokens":            viz.Tokens,
		"local_iterations":  viz.LocalIterations,
		"repeated_failures": viz.RepeatedFailures,
		"transitions":       viz.Transitions,
		"elapsed":           viz.Elapsed,
		"role_visits":       viz.RoleVisits[RolePlanner],
		"loop_tester":       viz.LoopTraversals[LoopTesterToDeveloper],
		"loop_reviewer":     viz.LoopTraversals[LoopReviewerToDeveloper],
	}
	for name, dimension := range dimensions {
		if dimension.Limit != nil {
			t.Errorf("%s limit = %v, want nil for an unlimited budget", name, *dimension.Limit)
		}
		if dimension.Level != BudgetNormal {
			t.Errorf("%s level = %s, want normal for an unlimited budget regardless of usage", name, dimension.Level)
		}
	}
}

func TestBuildBudgetVisualizationRoleVisitsUsesConfiguredLimitPerRole(t *testing.T) {
	limits := BudgetLimits{
		MaxVisitsPerRole: map[Role]Limit{
			RolePlanner:   1,
			RoleDeveloper: 10,
		},
	}
	roleVisits := map[Role]int{
		RolePlanner:   1,
		RoleDeveloper: 1,
		RoleTester:    5, // no configured limit; must not divide-by-zero or crash
	}

	viz := BuildBudgetVisualization(BudgetUsage{}, limits, LoopTraversalCounts{}, 0, 0, roleVisits)

	if viz.RoleVisits[RolePlanner].Level != BudgetExhaustedLevel {
		t.Fatalf("planner visits level = %s, want exhausted (1 of 1)", viz.RoleVisits[RolePlanner].Level)
	}
	if viz.RoleVisits[RoleDeveloper].Level != BudgetNormal {
		t.Fatalf("developer visits level = %s, want normal (1 of 10)", viz.RoleVisits[RoleDeveloper].Level)
	}
	tester := viz.RoleVisits[RoleTester]
	if tester.Limit != nil || tester.Level != BudgetNormal {
		t.Fatalf("tester visits (no configured limit) = %+v, want unlimited/normal", tester)
	}
}
