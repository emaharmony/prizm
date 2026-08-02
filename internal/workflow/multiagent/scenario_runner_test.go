package multiagent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------
// ScriptedRoleRunner: direct unit tests
// ---------------------------------------------------------------------

func TestScriptedRoleRunnerSequencing(t *testing.T) {
	runner := NewScriptedRoleRunner([]ScriptedRoleResult{
		{Role: "planner", Outcome: "plan_ready"},
		{Role: "developer", Outcome: "implementation_ready"},
	})

	res1, err := runner.RunRole(context.Background(), RoleRunRequest{Run: RunView{CurrentRole: RolePlanner}})
	if err != nil {
		t.Fatalf("step 1: unexpected error: %v", err)
	}
	if res1.Outcome != OutcomePlanReady {
		t.Errorf("step 1: outcome = %q, want %q", res1.Outcome, OutcomePlanReady)
	}
	if res1.LocalIterations != 1 {
		t.Errorf("step 1: localIterations = %d, want 1 (default)", res1.LocalIterations)
	}
	if res1.OutgoingHandoff == nil {
		t.Error("step 1: expected an outgoing handoff (no graph wired, so every step is treated as non-terminal)")
	}

	res2, err := runner.RunRole(context.Background(), RoleRunRequest{Run: RunView{CurrentRole: RoleDeveloper}})
	if err != nil {
		t.Fatalf("step 2: unexpected error: %v", err)
	}
	if res2.Outcome != OutcomeImplementationReady {
		t.Errorf("step 2: outcome = %q, want %q", res2.Outcome, OutcomeImplementationReady)
	}
}

func TestScriptedRoleRunnerScriptExhausted(t *testing.T) {
	runner := NewScriptedRoleRunner([]ScriptedRoleResult{
		{Role: "planner", Outcome: "plan_ready"},
	})
	if _, err := runner.RunRole(context.Background(), RoleRunRequest{Run: RunView{CurrentRole: RolePlanner}}); err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}

	_, err := runner.RunRole(context.Background(), RoleRunRequest{Run: RunView{CurrentRole: RoleDeveloper}})
	if err == nil {
		t.Fatal("expected an error once the script is exhausted, got nil")
	}
	if !strings.Contains(err.Error(), "script exhausted") {
		t.Errorf("error = %q, want it to mention the script being exhausted", err.Error())
	}
}

func TestScriptedRoleRunnerRoleMismatch(t *testing.T) {
	runner := NewScriptedRoleRunner([]ScriptedRoleResult{
		{Role: "planner", Outcome: "plan_ready"},
	})

	_, err := runner.RunRole(context.Background(), RoleRunRequest{Run: RunView{CurrentRole: RoleDeveloper}})
	if err == nil {
		t.Fatal("expected a role-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error = %q, want it to mention a script mismatch", err.Error())
	}
}

func TestScriptedRoleRunnerFailWith(t *testing.T) {
	runner := NewScriptedRoleRunner([]ScriptedRoleResult{
		{Role: "planner", Outcome: "plan_ready", FailWith: "simulated planner failure"},
	})

	_, err := runner.RunRole(context.Background(), RoleRunRequest{Run: RunView{CurrentRole: RolePlanner}})
	if err == nil {
		t.Fatal("expected an error from FailWith, got nil")
	}
	if err.Error() != "simulated planner failure" {
		t.Errorf("error = %q, want %q", err.Error(), "simulated planner failure")
	}
}

func TestScriptedRoleRunnerCancel(t *testing.T) {
	runner := NewScriptedRoleRunner([]ScriptedRoleResult{
		{Role: "planner", Outcome: "plan_ready", Cancel: true},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.SetCancelFunc(cancel)

	if _, err := runner.RunRole(ctx, RoleRunRequest{Run: RunView{CurrentRole: RolePlanner}}); err == nil {
		t.Fatal("expected a non-nil error for a Cancel step")
	}
	if ctx.Err() == nil {
		t.Error("expected the wired context to be cancelled after a Cancel step")
	}
}

// ---------------------------------------------------------------------
// RunScenario: assertion-diffing tests, against the same Phase 1 reference
// fixture (validDefinition/mustAdaptGraph, config_test.go) this package's
// existing supervisor tests already use.
// ---------------------------------------------------------------------

func happyPathScenario() Scenario {
	return Scenario{
		Name: "happy-path",
		Script: []ScriptedRoleResult{
			{Role: "planner", Outcome: "plan_ready"},
			{Role: "developer", Outcome: "implementation_ready"},
			{Role: "tester", Outcome: "tests_passed"},
			{Role: "reviewer", Outcome: "review_approved"},
		},
		Expect: ScenarioExpectation{
			FinalStatus:  "completed",
			TerminalNode: terminalNodeID(TerminalConditionCompleted),
			Transitions: []string{
				edgeID(RolePlanner, OutcomePlanReady),
				edgeID(RoleDeveloper, OutcomeImplementationReady),
				edgeID(RoleTester, OutcomeTestsPassed),
				edgeID(RoleReviewer, OutcomeReviewApproved),
			},
			NodeVisits: map[string]int{
				"planner": 1, "developer": 1, "tester": 1, "reviewer": 1,
			},
			EdgeTraversals: map[string]int{
				edgeID(RolePlanner, OutcomePlanReady):             1,
				edgeID(RoleDeveloper, OutcomeImplementationReady): 1,
				edgeID(RoleTester, OutcomeTestsPassed):            1,
				edgeID(RoleReviewer, OutcomeReviewApproved):       1,
			},
		},
	}
}

func TestRunScenarioPasses(t *testing.T) {
	graph := mustAdaptGraph(t, validDefinition())
	result, err := RunScenario(context.Background(), graph, happyPathScenario())
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected scenario to pass, failures: %v", result.Failures)
	}
}

func TestRunScenarioFinalStatusMismatch(t *testing.T) {
	graph := mustAdaptGraph(t, validDefinition())
	scenario := happyPathScenario()
	scenario.Expect.FinalStatus = "failed"

	result, err := RunScenario(context.Background(), graph, scenario)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if result.Passed {
		t.Fatal("expected scenario to fail on finalStatus mismatch")
	}
	if !containsSubstring(result.Failures, "finalStatus") {
		t.Errorf("failures = %v, want one mentioning finalStatus", result.Failures)
	}
}

func TestRunScenarioNodeVisitsMismatch(t *testing.T) {
	graph := mustAdaptGraph(t, validDefinition())
	scenario := happyPathScenario()
	scenario.Expect.NodeVisits["developer"] = 2

	result, err := RunScenario(context.Background(), graph, scenario)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if result.Passed {
		t.Fatal("expected scenario to fail on nodeVisits mismatch")
	}
	if !containsSubstring(result.Failures, "developer") {
		t.Errorf("failures = %v, want one mentioning the developer node", result.Failures)
	}
}

func TestRunScenarioEdgeTraversalsMismatch(t *testing.T) {
	graph := mustAdaptGraph(t, validDefinition())
	scenario := happyPathScenario()
	scenario.Expect.EdgeTraversals[edgeID(RolePlanner, OutcomePlanReady)] = 5

	result, err := RunScenario(context.Background(), graph, scenario)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if result.Passed {
		t.Fatal("expected scenario to fail on edgeTraversals mismatch")
	}
	if !containsSubstring(result.Failures, "traversals") {
		t.Errorf("failures = %v, want one mentioning traversals", result.Failures)
	}
}

// TestRunScenarioRoleScriptMismatch covers the scenario-author-error case: a
// script whose declared role order does not match the workflow's actual
// routing. RunScenario itself must not error or panic — the mismatch
// surfaces as a failed run (ScriptedRoleRunner.RunRole's error propagates
// through Supervisor.Run as a RoleExecutionError, ending the run
// RunStatusFailed), which a scenario asserting FinalStatus: "completed"
// correctly reports as a failed expectation.
func TestRunScenarioRoleScriptMismatch(t *testing.T) {
	graph := mustAdaptGraph(t, validDefinition())
	scenario := Scenario{
		Name: "role-mismatch",
		Script: []ScriptedRoleResult{
			{Role: "developer", Outcome: "implementation_ready"}, // wrong: planner runs first
		},
		Expect: ScenarioExpectation{FinalStatus: "completed"},
	}

	result, err := RunScenario(context.Background(), graph, scenario)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if result.Passed {
		t.Fatal("expected scenario to fail when the script's role order disagrees with actual routing")
	}
}

func TestRunScenarioBudgetExhausted(t *testing.T) {
	definition := validDefinition()
	definition.Budgets.MaxTesterToDeveloperLoops = 1
	graph := mustAdaptGraph(t, definition)

	scenario := Scenario{
		Name: "loop-exhaustion",
		Script: []ScriptedRoleResult{
			{Role: "planner", Outcome: "plan_ready"},
			{Role: "developer", Outcome: "implementation_ready"},
			{Role: "tester", Outcome: "tests_failed"},
			{Role: "developer", Outcome: "implementation_ready"},
			{Role: "tester", Outcome: "tests_failed"},
		},
		Expect: ScenarioExpectation{
			FinalStatus:   "budget_exhausted",
			LoopExhausted: "max_tester_to_developer_loops",
		},
	}

	result, err := RunScenario(context.Background(), graph, scenario)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected scenario to pass, failures: %v", result.Failures)
	}
}

func containsSubstring(list []string, substr string) bool {
	for _, s := range list {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// TestScenarioTestdataFixturesPass end-to-end exercises the shipped
// testdata/scenarios/*.yaml fixtures — the same files `prism graph test`
// reads — through LoadScenarioFile -> LoadDefinition -> Compile ->
// RunScenario, proving they are not just decodable but actually pass
// against the real compiled-from-YAML path (not the legacy
// CompatAdaptDefinition path every other test in this file uses).
func TestScenarioTestdataFixturesPass(t *testing.T) {
	fixtures := []string{
		"testdata/scenarios/software-delivery-happy-path.yaml",
		"testdata/scenarios/software-delivery-loop-exhaustion.yaml",
	}

	for _, fixturePath := range fixtures {
		t.Run(fixturePath, func(t *testing.T) {
			sf, err := LoadScenarioFile(fixturePath)
			if err != nil {
				t.Fatalf("LoadScenarioFile: %v", err)
			}

			workflowPath := filepath.Join(filepath.Dir(fixturePath), sf.Workflow)
			def, idx, diags, err := LoadDefinition(workflowPath)
			if err != nil {
				t.Fatalf("LoadDefinition(%s): %v", workflowPath, err)
			}
			if diags.HasErrors() {
				t.Fatalf("LoadDefinition(%s) diagnostics: %v", workflowPath, diags)
			}
			graph, compileDiags, err := Compile(def, idx, CompileOptions{})
			if err != nil {
				t.Fatalf("Compile(%s): %v (diags: %v)", workflowPath, err, compileDiags)
			}

			if len(sf.Scenarios) == 0 {
				t.Fatal("fixture declares zero scenarios")
			}
			for _, scenario := range sf.Scenarios {
				t.Run(scenario.Name, func(t *testing.T) {
					result, err := RunScenario(context.Background(), graph, scenario)
					if err != nil {
						t.Fatalf("RunScenario: %v", err)
					}
					if !result.Passed {
						t.Fatalf("scenario failed: %v", result.Failures)
					}
				})
			}
		})
	}
}
