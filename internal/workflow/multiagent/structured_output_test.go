package multiagent

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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

func TestHandoffSummaryCompatibilityKeepsStrictRoutingAndReviewSemantics(t *testing.T) {
	t.Run("string summary becomes descriptive handoff", func(t *testing.T) {
		got, err := decodeRoleOutput(RolePlanner, `{"schema_version":1,"understanding":"plan","implementation_plan":["inspect"],"task_breakdown":["work"],"acceptance_criteria":["pass"],"handoff":"Ready for implementation"}`)
		if err != nil {
			t.Fatal(err)
		}
		if got.Outcome != OutcomePlanReady || got.Handoff == nil || got.Handoff.Objective != "Ready for implementation" || got.Handoff.Reason != "role handoff summary" || got.Handoff.Notes != "Ready for implementation" {
			t.Fatalf("summary handoff normalization = %#v", got)
		}
	})
	t.Run("summary object remains strict", func(t *testing.T) {
		got, err := decodeRoleOutput(RoleDeveloper, `{"schema_version":1,"summary":"changed","changed_artifacts":[{"kind":"file","uri":"a.go"}],"handoff":{"summary":"Verify the change"}}`)
		if err != nil || got.Handoff == nil || got.Handoff.Objective != "Verify the change" || got.Handoff.Reason != "role handoff summary" {
			t.Fatalf("summary object handoff = %#v, err=%v", got, err)
		}
		_, err = decodeRoleOutput(RoleDeveloper, `{"schema_version":1,"summary":"changed","changed_artifacts":[{"kind":"file","uri":"a.go"}],"handoff":{"summary":"Verify","destination":"reviewer"}}`)
		var malformed *StructuredOutputError
		if !errors.As(err, &malformed) || malformed.Cause == nil || !strings.Contains(malformed.Cause.Error(), "unknown field") {
			t.Fatalf("unknown handoff routing field accepted: %v", err)
		}
		_, err = decodeRoleOutput(RoleDeveloper, `{"schema_version":1,"summary":"changed","changed_artifacts":[{"kind":"file","uri":"a.go"}],"handoff":{"summary":"x"} {}}`)
		if err == nil {
			t.Fatal("handoff accepted trailing JSON value")
		}
	})
	t.Run("empty shorthand is not a valid handoff", func(t *testing.T) {
		for _, value := range []string{`""`, `"   "`, `null`} {
			raw := `{"schema_version":1,"understanding":"plan","implementation_plan":["inspect"],"task_breakdown":["work"],"acceptance_criteria":["pass"],"handoff":` + value + `}`
			if _, err := decodeRoleOutput(RolePlanner, raw); err == nil {
				t.Errorf("accepted empty handoff value %s", value)
			}
		}
	})
	t.Run("review decision and routing remain strict", func(t *testing.T) {
		got, err := decodeRoleOutput(RoleReviewer, `{"schema_version":1,"decision":"changes_requested","findings":[{"severity":"high","summary":"fix it"}],"required_corrections":["fix it"],"handoff":"Return to implementation","destination":"developer"}`)
		if err == nil || got.Outcome != "" {
			t.Fatalf("reviewer routing field accepted: %#v err=%v", got, err)
		}
		_, err = decodeRoleOutput(RoleReviewer, `{"schema_version":1,"decision":"approved","handoff":"Do more work"}`)
		var malformed *StructuredOutputError
		if !errors.As(err, &malformed) || malformed.Cause == nil || !strings.Contains(malformed.Cause.Error(), "must not include a handoff") {
			t.Fatalf("approved review accepted handoff summary: %v", err)
		}
		_, err = decodeRoleOutput(RoleReviewer, `{"schema_version":1,"decision":"changes_requested","required_corrections":["fix"],"handoff":null}`)
		if err == nil {
			t.Fatal("changes-requested review accepted null handoff")
		}
	})
}

func TestStructuredOutputCorrectionFeedbackIsSpecificAndSafe(t *testing.T) {
	t.Run("syntax", func(t *testing.T) {
		_, err := decodeRoleOutput(RolePlanner, `{"provider_private_canary":`)
		var correction *SchemaCorrectionError
		if !errors.As(err, &correction) || correction.Code != "invalid_json_syntax" || !strings.Contains(correction.Feedback, "valid JSON syntax") {
			t.Fatalf("correction=%#v err=%v", correction, err)
		}
		if strings.Contains(correction.Feedback, "provider_private_canary") {
			t.Fatalf("feedback leaked response content: %q", correction.Feedback)
		}
	})
	t.Run("missing required fields", func(t *testing.T) {
		_, err := decodeRoleOutput(RolePlanner, `{"schema_version":1,"understanding":"PRIVATE_CANARY"}`)
		var correction *SchemaCorrectionError
		if !errors.As(err, &correction) || correction.Code != "missing_required_fields" || !strings.Contains(correction.Feedback, "implementation_plan") {
			t.Fatalf("correction=%#v err=%v", correction, err)
		}
		if strings.Contains(correction.Feedback, "PRIVATE_CANARY") {
			t.Fatalf("feedback leaked response content: %q", correction.Feedback)
		}
	})
}

func TestRoleExecutionFailureCarriesOnlyAllowlistedFieldTypeFeedback(t *testing.T) {
	const privateCanary = "PRIVATE_MODEL_VALUE_CANARY"
	_, err := decodeRoleOutput(RoleReviewer, `{"schema_version":1,"decision":"approved","findings":{"summary":"`+privateCanary+`"},"evidence":[]}`)
	var correction *SchemaCorrectionError
	if !errors.As(err, &correction) || correction.Code != "invalid_field_type" {
		t.Fatalf("expected typed schema correction, got %T: %v", err, err)
	}
	failure := ClassifyRoleExecutionFailure(err)
	if failure.CorrectionField != "findings" || failure.ExpectedType != "a JSON array of objects" {
		t.Fatalf("safe type detail = %q/%q", failure.CorrectionField, failure.ExpectedType)
	}
	feedback := roleFailureFeedback(RoleReviewer, failure.Code, failure.CorrectionField, failure.ExpectedType, failure.AllowedValues)
	if !strings.Contains(feedback, "findings") || !strings.Contains(feedback, "a JSON array of objects") {
		t.Fatalf("feedback lacks actionable field/type: %q", feedback)
	}
	wire, marshalErr := json.Marshal(failure)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(wire), privateCanary) || strings.Contains(feedback, privateCanary) {
		t.Fatalf("schema feedback leaked model value: wire=%s feedback=%q", wire, feedback)
	}
	if got := roleFailureFeedback(RoleReviewer, "invalid_field_type", "private_model_field", privateCanary); strings.Contains(got, privateCanary) || strings.Contains(got, "private_model_field") {
		t.Fatalf("untrusted field/type pair escaped schema allowlist: %q", got)
	}
}

func TestEnumCorrectionFeedbackIsAllowlistedAndSpecific(t *testing.T) {
	const privateValue = "PRIVATE_BAD_DECISION_CANARY"
	raw := `{"schema_version":1,"decision":"` + privateValue + `","findings":[],"evidence":[]}`
	_, err := decodeRoleOutput(RoleReviewer, raw)
	var correction *SchemaCorrectionError
	if !errors.As(err, &correction) || correction.Code != "invalid_field_value" {
		t.Fatalf("expected typed enum correction, got %T: %v", err, err)
	}
	failure := ClassifyRoleExecutionFailure(err)
	if failure.CorrectionField != "decision" || !reflect.DeepEqual(failure.AllowedValues, []string{"approved", "changes_requested"}) {
		t.Fatalf("enum detail = %q %v", failure.CorrectionField, failure.AllowedValues)
	}
	feedback := roleFailureFeedback(RoleReviewer, failure.Code, failure.CorrectionField, failure.ExpectedType, failure.AllowedValues)
	if !strings.Contains(feedback, "decision") || !strings.Contains(feedback, "approved, changes_requested") {
		t.Fatalf("feedback lacks allowed enum values: %q", feedback)
	}
	wire, marshalErr := json.Marshal(failure)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(wire), privateValue) || strings.Contains(feedback, privateValue) {
		t.Fatalf("private model value leaked: wire=%s feedback=%q", wire, feedback)
	}
	if got := roleFailureFeedback(RoleReviewer, "invalid_field_value", "private_field", "", []string{privateValue}); strings.Contains(got, privateValue) || strings.Contains(got, "private_field") {
		t.Fatalf("untrusted enum detail escaped allowlist: %q", got)
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
