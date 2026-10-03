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
			name: "reviewer string finding evidence",
			role: RoleReviewer,
			raw: `{
				"schema_version": 1,
				"decision": "approved",
				"findings": [{"severity": "info", "summary": "validated", "evidence": ["validation/go_test_all.stdout.txt"]}],
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

func TestDecodeDeveloperOutputForProposalsDerivesMissingArtifact(t *testing.T) {
	raw := `{"schema_version":1,"summary":"proposed","handoff":{"objective":"test","reason":"proposal recorded"}}`
	proposals := []ProposalReference{{ProposalID: "proposal-1", ApprovalID: "approval-1", Artifacts: []ArtifactRef{{Kind: ArtifactFile, URI: "feature.txt"}}}}
	decoded, err := decodeDeveloperOutputForProposals(raw, proposals)
	if err != nil || decoded.Handoff == nil || len(decoded.Handoff.Artifacts) != 1 || decoded.Handoff.Artifacts[0].URI != "feature.txt" {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
}

func TestDecodeDeveloperOutputForProposalsRequiresProposalAndUsesCanonicalSource(t *testing.T) {
	missing := `{"schema_version":1,"summary":"proposed","handoff":{"objective":"test","reason":"proposal recorded"}}`
	if _, err := decodeDeveloperOutputForProposals(missing, nil); err == nil {
		t.Fatal("missing proposal source was accepted")
	}
	conflict := `{"schema_version":1,"summary":"proposed","changed_artifacts":[{"kind":"file","uri":"model-claim.txt"}],"handoff":{"objective":"test","reason":"proposal recorded"}}`
	proposals := []ProposalReference{{ProposalID: "proposal-1", ApprovalID: "approval-1", Artifacts: []ArtifactRef{{Kind: ArtifactFile, URI: "canonical.txt"}}}}
	decoded, err := decodeDeveloperOutputForProposals(conflict, proposals)
	if err != nil || decoded.Handoff == nil || len(decoded.Handoff.Artifacts) != 1 || decoded.Handoff.Artifacts[0].URI != "canonical.txt" {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
}

func TestDecodeDeveloperOutputForProposalsIsRecoveryStable(t *testing.T) {
	raw := `{"schema_version":1,"summary":"proposed","handoff":{"objective":"test","reason":"proposal recorded"}}`
	proposals := []ProposalReference{{ProposalID: "proposal-1", ApprovalID: "approval-1", Artifacts: []ArtifactRef{{Kind: ArtifactFile, URI: "feature.txt"}}}}
	first, firstErr := decodeDeveloperOutputForProposals(raw, proposals)
	second, secondErr := decodeDeveloperOutputForProposals(raw, proposals)
	if firstErr != nil || secondErr != nil || first.Handoff == nil || second.Handoff == nil || !sameArtifactRefs(first.Handoff.Artifacts, second.Handoff.Artifacts) {
		t.Fatalf("first=%#v firstErr=%v second=%#v secondErr=%v", first, firstErr, second, secondErr)
	}
}

func TestDecodeReflectionOutputStrictContract(t *testing.T) {
	raw := `{"schema_version":1,"verdict":"partial","confidence":0.75,"failure_class":"verification","evidence":[],"lesson_candidate":{"summary":"keep the check","content":"Run verification before routing.","category":"decision","topics":["verification"]},"replan_requested":true,"replan_reason":"verification evidence changed"}`
	result, err := decodeReflectionOutput(raw)
	if err != nil {
		t.Fatalf("decodeReflectionOutput() error = %v", err)
	}
	if result.Verdict != "partial" || result.Confidence != 0.75 || !result.ReplanRequested || result.LessonCandidate == nil {
		t.Fatalf("result = %#v", result)
	}
	for _, invalid := range []string{
		`{"schema_version":1,"verdict":"bad","confidence":0.5,"failure_class":"none"}`,
		`{"schema_version":1,"verdict":"success","confidence":1.5,"failure_class":"none"}`,
		`{"schema_version":1,"verdict":"success","confidence":0.5,"failure_class":"none","replan_requested":true}`,
	} {
		if _, err := decodeReflectionOutput(invalid); err == nil {
			t.Errorf("decodeReflectionOutput(%s) accepted invalid input", invalid)
		}
	}
}
