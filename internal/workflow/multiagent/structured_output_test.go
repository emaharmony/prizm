package multiagent

import (
	"errors"
	"testing"
)

func TestDecodeRoleOutput(t *testing.T) {
	tests := []struct {
		name        string
		role        Role
		raw         string
		wantOutcome TransitionOutcome
		wantHandoff bool
	}{
		{
			name: "planner",
			role: RolePlanner,
			raw: `{
				"schema_version": 1,
				"understanding": "add a bounded supervisor adapter",
				"implementation_plan": ["inspect", "implement", "test"],
				"task_breakdown": ["adapter", "decoder"],
				"acceptance_criteria": ["strict JSON"],
				"risks": ["ambiguous output"],
				"assumptions": [],
				"handoff": {"objective": "implement", "reason": "plan ready"}
			}`,
			wantOutcome: OutcomePlanReady,
			wantHandoff: true,
		},
		{
			name: "developer",
			role: RoleDeveloper,
			raw: `{
				"schema_version": 1,
				"summary": "implemented adapter",
				"changed_artifacts": [{"kind": "file", "uri": "internal/adapter.go"}],
				"commands_executed": ["go test ./..."],
				"known_limitations": [],
				"handoff": {"objective": "verify", "reason": "implementation ready"}
			}`,
			wantOutcome: OutcomeImplementationReady,
			wantHandoff: true,
		},
		{
			name: "tester passed",
			role: RoleTester,
			raw: `{
				"schema_version": 1,
				"result": "passed",
				"tests_executed": [{"name": "go test", "status": "passed"}],
				"handoff": {"objective": "review", "reason": "tests passed"}
			}`,
			wantOutcome: OutcomeTestsPassed,
			wantHandoff: true,
		},
		{
			name: "tester failed",
			role: RoleTester,
			raw: `{
				"schema_version": 1,
				"result": "failed",
				"tests_executed": [{"name": "go test", "status": "failed"}],
				"reproduction": ["go test ./internal/workflow/multiagent"],
				"handoff": {"objective": "fix tests", "reason": "tests failed"}
			}`,
			wantOutcome: OutcomeTestsFailed,
			wantHandoff: true,
		},
		{
			name: "reviewer approved",
			role: RoleReviewer,
			raw: `{
				"schema_version": 1,
				"decision": "approved",
				"findings": [],
				"required_corrections": [],
				"evidence": []
			}`,
			wantOutcome: OutcomeReviewApproved,
		},
		{
			name: "reviewer requests changes",
			role: RoleReviewer,
			raw: `{
				"schema_version": 1,
				"decision": "changes_requested",
				"findings": [{"severity": "high", "summary": "missing fail-closed check"}],
				"required_corrections": ["add the check"],
				"evidence": [],
				"handoff": {"objective": "correct implementation", "reason": "review finding"}
			}`,
			wantOutcome: OutcomeChangesRequested,
			wantHandoff: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeRoleOutput(test.role, test.raw)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Outcome != test.wantOutcome {
				t.Errorf("outcome = %q, want %q", got.Outcome, test.wantOutcome)
			}
			if (got.Handoff != nil) != test.wantHandoff {
				t.Errorf("handoff present = %t, want %t", got.Handoff != nil, test.wantHandoff)
			}
		})
	}
}

func TestDecodeRoleOutputRejectsMalformedOrAmbiguousResults(t *testing.T) {
	tests := []struct {
		name string
		role Role
		raw  string
	}{
		{name: "not json", role: RolePlanner, raw: "the plan is ready"},
		{
			name: "unknown field",
			role: RolePlanner,
			raw:  `{"schema_version":1,"understanding":"x","implementation_plan":["x"],"task_breakdown":["x"],"acceptance_criteria":["x"],"destination":"developer","handoff":{"objective":"x","reason":"x"}}`,
		},
		{
			name: "ambiguous tester result",
			role: RoleTester,
			raw:  `{"schema_version":1,"result":"probably passed","tests_executed":[{"name":"go test","status":"passed"}],"handoff":{"objective":"review","reason":"looks okay"}}`,
		},
		{
			name: "approval with handoff",
			role: RoleReviewer,
			raw:  `{"schema_version":1,"decision":"approved","handoff":{"objective":"developer","reason":"contradiction"}}`,
		},
		{
			name: "multiple json values",
			role: RoleReviewer,
			raw:  `{"schema_version":1,"decision":"approved"} {"decision":"changes_requested"}`,
		},
		{
			name: "passed result with failed test",
			role: RoleTester,
			raw:  `{"schema_version":1,"result":"passed","tests_executed":[{"name":"go test","status":"failed"}],"handoff":{"objective":"review","reason":"contradictory"}}`,
		},
		{
			name: "failed result with all tests passing",
			role: RoleTester,
			raw:  `{"schema_version":1,"result":"failed","tests_executed":[{"name":"go test","status":"passed"}],"reproduction":["none"],"handoff":{"objective":"fix","reason":"contradictory"}}`,
		},
		{
			name: "approval with required correction",
			role: RoleReviewer,
			raw:  `{"schema_version":1,"decision":"approved","required_corrections":["change code"]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeRoleOutput(test.role, test.raw)
			var outputErr *StructuredOutputError
			if !errors.As(err, &outputErr) {
				t.Fatalf("expected StructuredOutputError, got %T: %v", err, err)
			}
		})
	}
}
