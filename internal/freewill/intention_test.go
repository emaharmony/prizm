package freewill

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func validRequest() IntentionRequest {
	return IntentionRequest{
		RequestID: "req-1", AgentID: "agent-1", Origin: OriginAgent,
		CandidateSource: SourceNew, Title: "Explore", Goal: "Write a bounded report",
		ReasonSummary:         "Related to the current project",
		RequestedCapabilities: []string{"memory.read"},
		Scope:                 Scope{ProjectID: "project-1", Workspace: "workspace-1"},
		IdempotencyKey:        "idem-1", CorrelationID: "corr-1",
	}
}

func TestDecodeIntentionRequestAcceptsUntrustedScopedProposal(t *testing.T) {
	want := validRequest()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeIntentionRequest(data)
	if err != nil {
		t.Fatalf("DecodeIntentionRequest() error = %v", err)
	}
	if got.RequestID != want.RequestID || got.Scope != want.Scope || got.Origin != OriginAgent {
		t.Fatalf("decoded request lost identity or scope: %#v", got)
	}
	if got.Budget.RuntimeMinutes != nil || got.Budget.MaxTokens != nil {
		t.Fatalf("unspecified budget must remain unspecified: %#v", got.Budget)
	}
}

func TestCandidateIntentionRequiresEnvelopeAndValidRequest(t *testing.T) {
	candidate := CandidateIntention{CandidateID: "candidate-1", Request: validRequest()}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}
	candidate.CandidateID = " "
	if err := candidate.Validate(); err == nil {
		t.Fatal("candidate without identity was accepted")
	}
}

func TestSelectionRequiresThreeScopedFreshCandidatesAndServerVerifiedResume(t *testing.T) {
	scope := Scope{ProjectID: "project-a", Workspace: "workspace-a"}
	candidates := make([]CandidateIntention, 0, 4)
	for i := 0; i < 3; i++ {
		req := validRequest()
		req.RequestID = fmt.Sprintf("request-%d", i)
		req.AgentID = "agent-a"
		req.Scope = scope
		req.CandidateSource = SourceNew
		candidates = append(candidates, CandidateIntention{CandidateID: fmt.Sprintf("candidate-%d", i), Request: req})
	}
	resume := validRequest()
	resume.RequestID, resume.AgentID, resume.Scope, resume.CandidateSource = "resume-request", "agent-a", scope, SourceResume
	candidates = append(candidates, CandidateIntention{CandidateID: "resume-1", Request: resume})
	selection := Selection{Candidates: candidates, ChoiceID: "candidate-1", ReasonSummary: "Fits the current scope."}
	if err := selection.Validate("agent-a", scope, map[string]struct{}{"resume-1": {}}); err != nil {
		t.Fatalf("valid selection rejected: %v", err)
	}
	if err := selection.Validate("agent-a", scope, nil); err == nil {
		t.Fatal("unverified resume candidate accepted")
	}

	selection.Candidates = selection.Candidates[:2]
	if err := selection.Validate("agent-a", scope, nil); err == nil {
		t.Fatal("selection with fewer than three fresh candidates accepted")
	}
}

func TestSelectionRejectsScopeDriftAndSleepWithoutReason(t *testing.T) {
	scope := Scope{ProjectID: "project-a", Workspace: "workspace-a"}
	candidates := make([]CandidateIntention, 0, 3)
	for i := 0; i < 3; i++ {
		req := validRequest()
		req.RequestID = fmt.Sprintf("request-%d", i)
		req.AgentID, req.Scope, req.CandidateSource = "agent-a", scope, SourceNew
		candidates = append(candidates, CandidateIntention{CandidateID: fmt.Sprintf("candidate-%d", i), Request: req})
	}
	selection := Selection{Candidates: candidates, ChoiceID: "candidate-1", ReasonSummary: "Scoped choice."}
	selection.Candidates[0].Request.Scope.Workspace = "other-workspace"
	if err := selection.Validate("agent-a", scope, nil); err == nil {
		t.Fatal("scope-drifted candidate accepted")
	}
	selection.Candidates[0].Request.Scope = scope
	selection.ChoiceID, selection.ReasonSummary, selection.SleepReason = "", "", " "
	if err := selection.Validate("agent-a", scope, nil); err == nil {
		t.Fatal("sleep without a reason accepted")
	}
}

func TestDecodeCandidateIntentionRejectsNestedUnknownAuthority(t *testing.T) {
	data, err := json.Marshal(CandidateIntention{CandidateID: "candidate-1", Request: validRequest()})
	if err != nil {
		t.Fatal(err)
	}
	input := strings.TrimSuffix(string(data), "}}") + `,"approved":true}}`
	if _, err := DecodeCandidateIntention([]byte(input)); err == nil {
		t.Fatal("candidate decoder accepted an unknown nested request")
	}
}

func TestDecodeIntentionRequestRejectsMalformedOrAuthorityFields(t *testing.T) {
	base, err := json.Marshal(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	valid := string(base)
	cases := map[string]string{
		"unknown authorization field": strings.TrimSuffix(valid, "}") + `,"approved":true}`,
		"unknown permission field":    strings.TrimSuffix(valid, "}") + `,"permissions":["shell"]}`,
		"scope-less":                  strings.Replace(valid, `"scope":{"project_id":"project-1","workspace":"workspace-1"}`, `"scope":{}`, 1),
		"blank agent identity":        strings.Replace(valid, `"agent_id":"agent-1"`, `"agent_id":" "`, 1),
		"wrong origin":                strings.Replace(valid, `"origin":"agent"`, `"origin":"user"`, 1),
		"empty capability":            strings.Replace(valid, `"memory.read"`, `" "`, 1),
		"duplicate capability":        strings.Replace(valid, `"memory.read"]`, `"memory.read","memory.read"]`, 1),
		"negative explicit budget":    strings.Replace(valid, `"budget":{}`, `"budget":{"max_tokens":-1}`, 1),
		"wrong field type":            strings.Replace(valid, `"goal":"Write a bounded report"`, `"goal":{"secret":"sentinel"}`, 1),
		"trailing JSON":               valid + ` {}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeIntentionRequest([]byte(input)); err == nil {
				t.Fatal("DecodeIntentionRequest() accepted invalid input")
			} else if strings.Contains(err.Error(), "sentinel") {
				t.Fatal("decode error exposed request content")
			}
		})
	}
}

func TestValidateTransitionDocumentsEdgesWithoutGrantingAuthority(t *testing.T) {
	allowed := [][2]State{
		{StateDisabled, StateSleeping}, {StateSleeping, StateWaking},
		{StateWaking, StateSelecting}, {StateWaking, StateSleeping}, {StateWaking, StateHalted},
		{StateSelecting, StateProposed}, {StateSelecting, StateSleeping}, {StateSelecting, StateHalted},
		{StateProposed, StateApprovalPending}, {StateProposed, StateDenied}, {StateProposed, StatePaused}, {StateProposed, StateHalted},
		{StateApprovalPending, StateRunning}, {StateApprovalPending, StateDenied}, {StateApprovalPending, StatePaused}, {StateApprovalPending, StateHalted},
		{StatePaused, StateWaking}, {StatePaused, StateHalted},
		{StateRunning, StatePaused}, {StateRunning, StateHalted},
	}
	for _, edge := range allowed {
		if err := ValidateTransition(edge[0], edge[1]); err != nil {
			t.Errorf("ValidateTransition(%q, %q): %v", edge[0], edge[1], err)
		}
	}
	for _, edge := range [][2]State{
		{StateDisabled, StateRunning}, {StateProposed, StateRunning},
		{StateDenied, StateSelecting}, {StateHalted, StateWaking},
		{StateRunning, StateSelecting}, {"unknown", StateDisabled},
	} {
		if err := ValidateTransition(edge[0], edge[1]); err == nil {
			t.Errorf("ValidateTransition(%q, %q) unexpectedly accepted", edge[0], edge[1])
		}
	}
	if InitialState != StateDisabled {
		t.Fatalf("InitialState = %q, want disabled", InitialState)
	}
}
