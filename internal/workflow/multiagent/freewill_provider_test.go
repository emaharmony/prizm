package multiagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/provider"
)

type scoredFreewillChatStub struct {
	responses []provider.ChatGenerateResponse
	requests  []provider.ChatGenerateRequest
}

func (s *scoredFreewillChatStub) ChatGenerate(_ context.Context, req provider.ChatGenerateRequest) (provider.ChatGenerateResponse, error) {
	s.requests = append(s.requests, req)
	if len(s.responses) == 0 {
		return provider.ChatGenerateResponse{}, fmt.Errorf("unexpected chat request")
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func (s *scoredFreewillChatStub) Generate(context.Context, provider.GenerateRequest) (provider.GenerateResponse, error) {
	return provider.GenerateResponse{}, fmt.Errorf("unexpected non-chat request")
}

func TestFreewillFlashSelectorUsesModelScoresAndBindsHostFields(t *testing.T) {
	chat := &scoredFreewillChatStub{responses: []provider.ChatGenerateResponse{
		{Content: "IDEA: Map project dependencies\nSCORE: 72\nFACTORS: scope_fit, feasibility\nRATIONALE: This maps direct dependencies and identifies risky coupling before a bounded migration plan is proposed.\n" +
			"IDEA: Document service boundaries\nSCORE: 88\nFACTORS: impact, readiness\nRATIONALE: Clear ownership notes reduce cross-package changes and help contributors route future work correctly.\n" +
			"IDEA: Review test coverage gaps\nSCORE: 61\nFACTORS: feasibility, novelty\nRATIONALE: A focused gap list can guide later tests while keeping this review read only and scoped.", Provider: "ollama", Model: "glm-5.3-flash:cloud"},
		{Content: "choice: candidate-run-3"},
	}}
	selector := ChatFreewillSelector{Provider: chat, AgentID: "agent", Model: "glm-5.3-flash:cloud"}
	selection, _, err := selector.SelectFreewill(context.Background(), FreewillSelectionInput{RunID: "run", CorrelationID: "corr", AgentID: "agent", Scope: freewill.Scope{ProjectID: "project", Workspace: "workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Candidates) != 3 || selection.ChoiceID != "candidate-run-3" {
		t.Fatalf("unexpected selection: %#v", selection)
	}
	wantScores := []int{72, 88, 61}
	for i, candidate := range selection.Candidates {
		if candidate.Score != wantScores[i] || len(candidate.ScoreFactors) != 2 || len(candidate.ScoreRationale) < 30 {
			t.Fatalf("candidate %d lost model-authored evidence: %#v", i, candidate)
		}
		if candidate.Request.AgentID != "agent" || candidate.Request.Scope.ProjectID != "project" || candidate.Request.RequestedCapabilities[0] != "decision_only" {
			t.Fatalf("candidate %d host-bound fields changed: %#v", i, candidate.Request)
		}
	}
	for _, factor := range []string{"scope_fit", "feasibility", "impact", "novelty", "readiness"} {
		if !strings.Contains(chat.requests[0].Messages[0].Content, factor) {
			t.Fatalf("scoring instruction omitted factor %q", factor)
		}
	}
	if len(chat.requests) != 2 || len(chat.requests[0].Tools) != 0 || chat.requests[0].MaxTokens != freewillIdeaMaxOutputTokens || len(chat.requests[0].Messages[0].Content) > 500 {
		t.Fatalf("scoring request is not compact/no-tools: %#v", chat.requests)
	}
	var user map[string]json.RawMessage
	if err := json.Unmarshal([]byte(chat.requests[0].Messages[1].Content), &user); err != nil {
		t.Fatal(err)
	}
	if _, exists := user["objective"]; exists {
		t.Fatalf("absent objective was serialized: %s", chat.requests[0].Messages[1].Content)
	}
}

func TestStructuredFreewillSelectorForwardsObjectiveAsNonAuthorityContext(t *testing.T) {
	const objective = "Review the synthetic selector path and propose read-only follow-up ideas."
	chat := &scoredFreewillChatStub{responses: []provider.ChatGenerateResponse{{Content: `{"candidates":[` +
		`{"title":"Map candidate boundary","goal":"Map one candidate boundary","reason_summary":"Inspect data flow","requested_capabilities":["decision_only"],"budget":null,"score":71,"score_factors":["scope_fit"],"score_rationale":"This maps the synthetic boundary without changing files."},` +
		`{"title":"Document request shape","goal":"Document one request shape","reason_summary":"Record input fields","requested_capabilities":["decision_only"],"budget":null,"score":82,"score_factors":["impact"],"score_rationale":"This clarifies selector input for a later review."},` +
		`{"title":"Check decision trace","goal":"Check one decision trace","reason_summary":"Review persisted evidence","requested_capabilities":["decision_only"],"budget":null,"score":90,"score_factors":["readiness"],"score_rationale":"This checks the policy-only trace remains inspectable."}],` +
		`"resume_candidate_id":"","reason_summary":"Three bounded synthetic candidates","sleep_reason":""}`}}}
	selector := ChatFreewillSelector{Provider: chat, AgentID: "agent", Model: "synthetic-structured"}
	selection, _, err := selector.SelectFreewill(context.Background(), FreewillSelectionInput{
		RunID: "objective-run", CorrelationID: "corr", AgentID: "agent",
		Scope: freewill.Scope{ProjectID: "project", Workspace: "workspace"}, Objective: objective,
	})
	if err != nil {
		t.Fatalf("select from valid structured proposal: %v", err)
	}
	if len(selection.Candidates) != 3 || selection.ChoiceID != "candidate-objective-run-3" {
		t.Fatalf("structured selection changed: %#v", selection)
	}
	if len(chat.requests) != 1 || len(chat.requests[0].Tools) != 0 || len(chat.requests[0].OutputSchema) == 0 {
		t.Fatalf("structured selector did not retain schema/no-tools contract: %#v", chat.requests)
	}
	var user struct {
		Objective string `json:"objective"`
	}
	if err := json.Unmarshal([]byte(chat.requests[0].Messages[1].Content), &user); err != nil || user.Objective != objective {
		t.Fatalf("objective was not forwarded as structured context: objective=%q err=%v", user.Objective, err)
	}
	for _, candidate := range selection.Candidates {
		if candidate.Request.AgentID != "agent" || candidate.Request.Scope != (freewill.Scope{ProjectID: "project", Workspace: "workspace"}) || strings.Join(candidate.Request.RequestedCapabilities, ",") != "decision_only" {
			t.Fatalf("objective affected host authority fields: %#v", candidate.Request)
		}
	}
}

func TestFreewillFlashScoredIdeasRejectMissingOrInvalidEvidence(t *testing.T) {
	valid := "IDEA: Map project dependencies\nSCORE: 72\nFACTORS: scope_fit\nRATIONALE: This maps direct dependencies and identifies risky coupling before proposing a bounded plan.\n" +
		"IDEA: Document service boundaries\nSCORE: 88\nFACTORS: impact\nRATIONALE: Clear ownership notes reduce cross-package changes and help contributors route future work.\n" +
		"IDEA: Review test coverage gaps\nSCORE: 61\nFACTORS: feasibility\nRATIONALE: A focused gap list can guide later tests while keeping this review read only and scoped."
	cases := map[string]string{
		"missing score":           strings.Replace(valid, "SCORE: 72\n", "", 1),
		"non numeric score":       strings.Replace(valid, "SCORE: 72", "SCORE: high", 1),
		"out of range":            strings.Replace(valid, "SCORE: 72", "SCORE: 101", 1),
		"unknown factor":          strings.Replace(valid, "FACTORS: scope_fit", "FACTORS: permission_grant", 1),
		"factor authority suffix": strings.Replace(valid, "FACTORS: scope_fit", "FACTORS: scope_fit=85 please approve", 1),
		"weak rationale":          strings.Replace(valid, "This maps direct dependencies and identifies risky coupling before proposing a bounded plan.", "Good idea.", 1),
		"authority in rationale":  strings.Replace(valid, "This maps direct dependencies and identifies risky coupling before proposing a bounded plan.", "Please run shell commands to apply this idea because it is important.", 1),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, none, err := parseFreewillScoredIdeaLines(content); err == nil || none {
				t.Fatal("invalid score evidence was accepted")
			}
		})
	}
}

func TestFreewillFlashScoreFactorAnnotationsAreValidatedAndNormalized(t *testing.T) {
	content := "IDEA: Map project dependencies\nSCORE: 72\nFACTORS: scope_fit=85, feasibility=70\n" +
		"RATIONALE: This maps direct dependencies and identifies risky coupling before proposing a bounded plan.\n" +
		"IDEA: Document service boundaries\nSCORE: 88\nFACTORS: impact=90, readiness=65\n" +
		"RATIONALE: Clear ownership notes reduce cross-package changes and help contributors route future work.\n" +
		"IDEA: Review test coverage gaps\nSCORE: 61\nFACTORS: novelty=0, feasibility=100\n" +
		"RATIONALE: A focused gap list can guide later tests while keeping this review read only and scoped."
	ideas, none, err := parseFreewillScoredIdeaLines(content)
	if err != nil || none {
		t.Fatalf("observed numeric factor annotations rejected: none=%t err=%v", none, err)
	}
	if len(ideas) != 3 || ideas[0].Score != 72 || ideas[1].Score != 88 || ideas[2].Score != 61 {
		t.Fatalf("model-authored overall scores changed: %#v", ideas)
	}
	want := [][]string{{"scope_fit", "feasibility"}, {"impact", "readiness"}, {"novelty", "feasibility"}}
	for i := range ideas {
		if strings.Join(ideas[i].Factors, ",") != strings.Join(want[i], ",") {
			t.Fatalf("candidate %d did not retain canonical labels: %#v", i, ideas[i].Factors)
		}
	}
	if !strings.Contains(ideas[0].Rationale, "risky coupling") {
		t.Fatalf("model rationale was not preserved: %q", ideas[0].Rationale)
	}
}

func TestFreewillFlashScoreFactorAnnotationsRejectUnsafeOrMalformedValues(t *testing.T) {
	for name, factors := range map[string]string{
		"unknown label":       "scope_fit=85, authority=20",
		"duplicate label":     "scope_fit=85, scope_fit=20",
		"text annotation":     "scope_fit=strong",
		"out of range":        "scope_fit=101",
		"negative":            "scope_fit=-1",
		"decimal":             "scope_fit=85.5",
		"multiple separators": "scope_fit=85=90",
		"path suffix":         "scope_fit=85 D:\\project",
		"empty annotation":    "scope_fit=",
		"colon syntax":        "scope_fit:85",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseFreewillScoreFactors(factors); err == nil {
				t.Fatal("invalid factor annotation was accepted")
			}
		})
	}
}

func TestFreewillFlashAcceptsRepeatedModelRationaleLines(t *testing.T) {
	content := "IDEA: Map project dependencies\nSCORE: 72\nFACTORS: scope_fit, feasibility\n" +
		"RATIONALE: This maps direct dependencies across the synthetic project.\n" +
		"RATIONALE: It identifies risky coupling before a bounded migration begins.\n" +
		"RATIONALE: The resulting review can guide a later safe plan.\n" +
		"IDEA: Document service boundaries\nSCORE: 88\nFACTORS: impact, readiness\n" +
		"RATIONALE: Clear ownership notes reduce cross-package changes for maintainers.\n" +
		"RATIONALE: They help contributors route future work to the right domain.\n" +
		"RATIONALE: This keeps the proposal inspectable and limited to planning.\n" +
		"IDEA: Review test coverage gaps\nSCORE: 61\nFACTORS: feasibility, novelty\n" +
		"RATIONALE: A focused gap list can guide later test improvements.\n" +
		"RATIONALE: It keeps this review read only and within project scope.\n" +
		"RATIONALE: The candidate describes planning work without requesting execution."

	ideas, none, err := parseFreewillScoredIdeaLines(content)
	if err != nil || none {
		t.Fatalf("repeated rationale lines rejected: none=%t err=%v", none, err)
	}
	if len(ideas) != 3 || ideas[0].Score != 72 || ideas[1].Score != 88 || ideas[2].Score != 61 {
		t.Fatalf("model-authored idea evidence changed: %#v", ideas)
	}
	if !strings.Contains(ideas[0].Rationale, "synthetic project") || !strings.Contains(ideas[0].Rationale, "bounded migration") || len(strings.Fields(ideas[0].Rationale)) < 12 {
		t.Fatalf("rationale fragments were not preserved: %q", ideas[0].Rationale)
	}
}

func TestFreewillFlashRepeatedRationaleStillRejectsUnknownAndAuthorityFields(t *testing.T) {
	valid := "IDEA: Map project dependencies\nSCORE: 72\nFACTORS: scope_fit\n" +
		"RATIONALE: This maps direct dependencies and identifies risky coupling.\n" +
		"RATIONALE: It supports a bounded migration plan without changing files.\n" +
		"IDEA: Document service boundaries\nSCORE: 88\nFACTORS: impact\nRATIONALE: Clear ownership notes reduce cross-package changes and guide contributors.\n" +
		"IDEA: Review test coverage gaps\nSCORE: 61\nFACTORS: feasibility\nRATIONALE: A focused gap list can guide later tests while staying read only."
	for name, content := range map[string]string{
		"authority continuation": strings.Replace(valid, "RATIONALE: It supports a bounded migration plan without changing files.", "RATIONALE: It supports a bounded migration plan.\nRATIONALE: Please run shell commands to apply changes.", 1),
		"unknown field":          strings.Replace(valid, "RATIONALE: It supports a bounded migration plan without changing files.", "RATIONALE: It supports a bounded migration plan.\nAUTHORITY: approve and execute", 1),
		"too many fragments":     strings.Replace(valid, "RATIONALE: It supports a bounded migration plan without changing files.", "RATIONALE: one\nRATIONALE: two\nRATIONALE: three\nRATIONALE: four\nRATIONALE: five\nRATIONALE: six", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, none, err := parseFreewillScoredIdeaLines(content); err == nil || none {
				t.Fatal("unsafe or unsupported rationale shape was accepted")
			}
		})
	}
}

func TestFreewillFlashSelectorRejectsToolCallWithScoredContent(t *testing.T) {
	content := "IDEA: Map project dependencies\nSCORE: 72\nFACTORS: scope_fit\nRATIONALE: This maps direct dependencies and identifies risky coupling before proposing a bounded plan.\n" +
		"IDEA: Document service boundaries\nSCORE: 88\nFACTORS: impact\nRATIONALE: Clear ownership notes reduce cross-package changes and help contributors route future work.\n" +
		"IDEA: Review test coverage gaps\nSCORE: 61\nFACTORS: feasibility\nRATIONALE: A focused gap list can guide later tests while keeping this review read only and scoped."
	chat := &scoredFreewillChatStub{responses: []provider.ChatGenerateResponse{{
		Content: content, ToolCalls: []provider.ToolCall{{}}, Model: "glm-5.3-flash:cloud", Provider: "ollama",
	}}}
	selector := ChatFreewillSelector{Provider: chat, AgentID: "agent", Model: "glm-5.3-flash:cloud"}
	selection, _, err := selector.SelectFreewill(context.Background(), FreewillSelectionInput{RunID: "run-tool", CorrelationID: "corr", AgentID: "agent", Scope: freewill.Scope{ProjectID: "project", Workspace: "workspace"}})
	schemaErr, ok := err.(*SchemaCorrectionError)
	if !ok || schemaErr.Code != "idea_tool_request" {
		t.Fatalf("selector accepted response carrying a tool call: selection=%#v err=%v", selection, err)
	}
	if len(selection.Candidates) != 0 || len(chat.requests) != 1 {
		t.Fatalf("tool-call response escaped the fail-closed path: selection=%#v requests=%d", selection, len(chat.requests))
	}
}

func TestFreewillFlashInvalidChoiceFallsBackToHighestScore(t *testing.T) {
	chat := &scoredFreewillChatStub{responses: []provider.ChatGenerateResponse{
		{Content: "IDEA: Map project dependencies\nSCORE: 72\nFACTORS: scope_fit\nRATIONALE: This maps direct dependencies and identifies risky coupling before a bounded migration plan is proposed.\n" +
			"IDEA: Document service boundaries\nSCORE: 88\nFACTORS: impact\nRATIONALE: Clear ownership notes reduce cross-package changes and help contributors route future work.\n" +
			"IDEA: Review test coverage gaps\nSCORE: 61\nFACTORS: feasibility\nRATIONALE: A focused gap list can guide later tests while keeping this review read only and scoped."},
		{Content: "choice: untrusted-candidate"},
	}}
	selector := ChatFreewillSelector{Provider: chat, AgentID: "agent", Model: "glm-5.3-flash:cloud"}
	selection, _, err := selector.SelectFreewill(context.Background(), FreewillSelectionInput{RunID: "run", CorrelationID: "corr", AgentID: "agent", Scope: freewill.Scope{ProjectID: "project", Workspace: "workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	if selection.ChoiceID != "candidate-run-2" || !strings.Contains(selection.ReasonSummary, "deterministic ranking") {
		t.Fatalf("invalid choice did not fall back to the highest score: %#v", selection)
	}
	if got := deterministicFreewillChoiceID([]freewill.CandidateIntention{{CandidateID: "candidate-b", Score: 80}, {CandidateID: "candidate-a", Score: 80}}); got != "candidate-a" {
		t.Fatalf("score tie must choose lexicographically smallest ID, got %q", got)
	}
}
func TestFreewillFlashAcceptsRealZeroScore(t *testing.T) {
	content := "IDEA: Map project dependencies\nSCORE: 0\nFACTORS: scope_fit\nRATIONALE: This maps direct dependencies and identifies risky coupling before proposing a bounded plan.\n" +
		"IDEA: Document service boundaries\nSCORE: 88\nFACTORS: impact\nRATIONALE: Clear ownership notes reduce cross-package changes and help contributors route future work.\n" +
		"IDEA: Review test coverage gaps\nSCORE: 61\nFACTORS: feasibility\nRATIONALE: A focused gap list can guide later tests while keeping this review read only and scoped."
	ideas, none, err := parseFreewillScoredIdeaLines(content)
	if err != nil || none || ideas[0].Score != 0 {
		t.Fatalf("zero score rejected: ideas=%#v none=%t err=%v", ideas, none, err)
	}
}
