package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/mock"
	multiagent "github.com/emaharmony/prizm/internal/workflow/multiagent"
)

const e53LiveCloudOptIn = "PRIZM_E53_LIVE_CLOUD"
const e53BasisDiagnosticOnly = "PRIZM_E53_BASIS_DIAGNOSTIC_ONLY"

type e53CallBoundChatProvider struct {
	inner provider.ChatProvider
	limit int32
	calls atomic.Int32
}

func (p *e53CallBoundChatProvider) ChatGenerate(ctx context.Context, req provider.ChatGenerateRequest) (provider.ChatGenerateResponse, error) {
	call := p.calls.Add(1)
	if call > p.limit {
		return provider.ChatGenerateResponse{}, errors.New("E53 selector call limit exceeded")
	}
	if req.Model != "glm-5.3-flash:cloud" || len(req.Tools) != 0 {
		return provider.ChatGenerateResponse{}, errors.New("E53 provider request contract mismatch")
	}
	response, err := p.inner.ChatGenerate(ctx, req)
	if err == nil {
		phase := "other"
		if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "IDEA") {
			phase = "idea"
		} else if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "CHOICE") {
			phase = "choice"
		}
		fmt.Printf("E53_SHAPE phase=%s %s\n", phase, e53ResponseShape(response.Content))
		if phase == "idea" {
			fmt.Printf("E53_BASIS phase=idea %s\n", e53BasisResponseShape(response.Content, "project:readme"))
		}
	}
	return response, err
}

func e53ResponseShape(content string) string {
	trimmed := strings.TrimSpace(content)
	shape := "text"
	if strings.HasPrefix(trimmed, "{") {
		shape = "object"
	} else if strings.HasPrefix(trimmed, "[") {
		shape = "array"
	} else if strings.HasPrefix(trimmed, "```") {
		shape = "fenced"
	}
	labels := map[string]int{"IDEA": 0, "SCORE": 0, "FACTORS": 0, "RATIONALE": 0, "BASIS_REFS": 0, "UNCERTAINTY": 0, "SLEEP": 0}
	unknownLabels, unlabelledLines, nonemptyLines := 0, 0, 0
	labelOrder := make([]string, 0, 16)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		nonemptyLines++
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			unlabelledLines++
			continue
		}
		label := strings.ToUpper(strings.TrimSpace(line[:colon]))
		if _, ok := labels[label]; ok {
			labels[label]++
			labelOrder = append(labelOrder, label)
		} else {
			unknownLabels++
		}
	}
	jsonKeys := 0
	if json.Valid([]byte(trimmed)) && strings.HasPrefix(trimmed, "{") {
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(trimmed), &object) == nil {
			jsonKeys = len(object)
		}
	}
	return fmt.Sprintf("shape=%s chars=%d lines=%d labels_idea=%d labels_score=%d labels_factors=%d labels_rationale=%d labels_basis=%d labels_uncertainty=%d labels_sleep=%d label_order=%s unknown_labels=%d unlabelled_lines=%d json_valid=%t json_keys=%d fences=%d",
		shape, len(content), nonemptyLines, labels["IDEA"], labels["SCORE"], labels["FACTORS"], labels["RATIONALE"], labels["BASIS_REFS"], labels["UNCERTAINTY"], labels["SLEEP"], strings.Join(labelOrder, ","), unknownLabels, unlabelledLines, json.Valid([]byte(trimmed)), jsonKeys, strings.Count(content, "```"))
}

func TestE53ResponseShapeDoesNotExposeContent(t *testing.T) {
	content := "IDEA: private marker\nSCORE: 80\nFACTORS: scope_fit\nUNEXPECTED: hidden marker"
	got := e53ResponseShape(content)
	if !strings.Contains(got, "labels_idea=1") || !strings.Contains(got, "labels_score=1") ||
		!strings.Contains(got, "unknown_labels=1") || strings.Contains(got, "private marker") || strings.Contains(got, "hidden marker") {
		t.Fatal("E53 structural diagnostics did not remain redacted")
	}
}

func TestE53SampleProjectRelevanceCheckIsNarrow(t *testing.T) {
	if !e53SampleProjectRelevant("Review venue schedules and community event listings") {
		t.Fatal("expected synthetic platform capability terms to pass")
	}
	for _, text := range []string{
		"Review generic platform capabilities",
		"Review generic participant details",
	} {
		if e53SampleProjectRelevant(text) {
			t.Fatalf("unrelated or incomplete candidate passed narrow relevance check: %q", text)
		}
	}
}

func TestE53BasisResponseShapeDoesNotExposeSourceValues(t *testing.T) {
	content := "IDEA: synthetic\nBASIS_REFS: project:readme, private-source-id\nUNCERTAINTY: private uncertainty text"
	got := e53BasisResponseShape(content, "project:readme")
	for _, want := range []string{"blocks=1", "refs_present=1", "allowlisted_project_refs=1", "unknown_refs=1", "uncertainty_present=1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("basis diagnostic omitted %q", want)
		}
	}
	if strings.Contains(got, "private-source-id") || strings.Contains(got, "private uncertainty") || strings.Contains(got, "project:readme") {
		t.Fatal("basis diagnostic exposed source ID or model prose")
	}
}

func e53BasisResponseShape(content, allowlistedSourceID string) string {
	type block struct {
		refsSeen       bool
		uncertaintySet bool
		knownRefs      int
		unknownRefs    int
		duplicateRefs  bool
	}
	var blocks []block
	var current *block
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		label, value := strings.ToUpper(strings.TrimSpace(line[:colon])), strings.TrimSpace(line[colon+1:])
		if label == "IDEA" {
			blocks = append(blocks, block{})
			current = &blocks[len(blocks)-1]
			continue
		}
		if current == nil {
			continue
		}
		switch label {
		case "BASIS_REFS":
			current.refsSeen = value != ""
			seen := make(map[string]struct{})
			for _, ref := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' }) {
				ref = strings.TrimSpace(ref)
				if ref == "" {
					continue
				}
				if _, duplicate := seen[ref]; duplicate {
					current.duplicateRefs = true
				}
				seen[ref] = struct{}{}
				if ref == allowlistedSourceID {
					current.knownRefs++
				} else {
					current.unknownRefs++
				}
			}
		case "UNCERTAINTY":
			current.uncertaintySet = value != ""
		}
	}
	refsPresent, refsMissing, knownRefs, unknownRefs, duplicateRefs, uncertaintyPresent, uncertaintyMissing := 0, 0, 0, 0, 0, 0, 0
	for _, item := range blocks {
		if item.refsSeen {
			refsPresent++
		} else {
			refsMissing++
		}
		if item.uncertaintySet {
			uncertaintyPresent++
		} else {
			uncertaintyMissing++
		}
		knownRefs += item.knownRefs
		unknownRefs += item.unknownRefs
		if item.duplicateRefs {
			duplicateRefs++
		}
	}
	return fmt.Sprintf("blocks=%d refs_present=%d refs_missing=%d allowlisted_project_refs=%d unknown_refs=%d duplicate_ref_blocks=%d uncertainty_present=%d uncertainty_missing=%d",
		len(blocks), refsPresent, refsMissing, knownRefs, unknownRefs, duplicateRefs, uncertaintyPresent, uncertaintyMissing)
}

func e53SampleProjectRelevant(text string) bool {
	text = strings.ToLower(text)
	hasPlatformCapability := strings.Contains(text, "event") || strings.Contains(text, "schedule") || strings.Contains(text, "listing")
	hasParticipant := strings.Contains(text, "organizer") || strings.Contains(text, "venue") || strings.Contains(text, "community")
	return hasPlatformCapability && hasParticipant
}

// This is an opt-in live semantic probe. It sends only the in-memory synthetic
// context below to Ollama Cloud and retains no provider request or response body.
func TestE53LiveGLMFlashSyntheticSampleProjectGrounding(t *testing.T) {
	if os.Getenv(e53LiveCloudOptIn) != "1" {
		t.Skip("set PRIZM_E53_LIVE_CLOUD=1 to run the authorized live semantic probe")
	}
	const model = "glm-5.3-flash:cloud"
	if os.Getenv(freewillDirectOllamaCloudEnv) != "1" {
		t.Fatal("E53 direct Cloud opt-in is required")
	}

	apiKey, err := takeOllamaAPIKey(os.Getenv, os.Unsetenv)
	if err != nil || strings.TrimSpace(apiKey) == "" {
		t.Fatal("E53 direct Cloud key is unavailable")
	}
	defer func() { apiKey = "" }()

	registry := provider.NewProviderRegistry()
	registry.Register(model, mock.New(), provider.ModelInfo{ProviderName: "ollama"})
	cloud, err := resolveFreewillSelectorProvider(&orchestrator.Config{}, registry,
		orchestrator.AgentConfig{Provider: "ollama", Model: model}, model, apiKey)
	if err != nil {
		t.Fatal("E53 direct Cloud provider could not be resolved")
	}
	apiKey = ""

	const syntheticDescription = "SampleProject is a fictional community event service that helps local organizers publish listings and coordinate venue schedules."
	if os.Getenv(e53BasisDiagnosticOnly) == "1" {
		e53RunScenario(t, cloud, model, "offdomain-diagnostic", "Repair a household appliance.", syntheticDescription, true, 2)
		return
	}
	t.Run("offdomain-defer", func(t *testing.T) {
		e53RunScenario(t, cloud, model, "offdomain", "Repair a household appliance.", syntheticDescription, true, 3)
	})
	t.Run("grounded-project-task", func(t *testing.T) {
		e53RunScenario(t, cloud, model, "grounded", "Review event listings and venue scheduling.", syntheticDescription, false, 3)
	})
}

func e53RunScenario(t *testing.T, cloud provider.ChatProvider, model, scenario, objective, syntheticDescription string, allowDefer bool, callLimit int32) {
	t.Helper()
	const projectID, agentID = "synthetic-sample-project", "e53-synthetic-agent"
	bounded := &e53CallBoundChatProvider{inner: cloud, limit: callLimit}
	selector := multiagent.ChatFreewillSelector{Provider: bounded, AgentID: agentID, Model: model, ReasoningEffort: "low"}
	now := time.Now().UTC()
	snapshot := freewill.NewProjectContextSnapshot(projectID, objective, now)
	snapshot.Sources = append(snapshot.Sources, freewill.ProjectContextSource{
		ID: "project:readme", Path: "README.md", ObservedAt: now,
		Confidence: "read_only_synthetic_fixture", Content: syntheticDescription,
	})
	snapshot.Seal()
	if err := snapshot.Manifest().Validate(); err != nil {
		t.Fatal("E53 synthetic project context is invalid")
	}
	input := multiagent.FreewillSelectionInput{
		RunID: "e53-" + scenario, CorrelationID: "e53-" + scenario, AgentID: agentID,
		Scope:     freewill.Scope{ProjectID: projectID, Workspace: "synthetic-memory-only"},
		Objective: objective, ProjectContext: snapshot,
	}
	var selection freewill.Selection
	var selectErr error
	for attempt := 0; attempt < 2; attempt++ {
		selection, _, selectErr = selector.SelectFreewill(context.Background(), input)
		if selectErr == nil {
			break
		}
		var correction *multiagent.SchemaCorrectionError
		if attempt != 0 || bounded.calls.Load() != 1 || !errors.As(selectErr, &correction) {
			break
		}
		input.RecoveryFeedback = multiagent.FreewillSelectionRecoveryFeedback(correction.Code, snapshot)
		if input.RecoveryFeedback == "" || len(input.RecoveryFeedback) > 300 {
			t.Fatal("E53 host correction was unavailable or exceeded its bound")
		}
	}
	calls := int(bounded.calls.Load())
	if calls < 1 || calls > int(callLimit) {
		t.Fatalf("E53 provider call bound violated: calls=%d", calls)
	}
	if selectErr != nil {
		var correction *multiagent.SchemaCorrectionError
		code := "provider_or_schema_error"
		if errors.As(selectErr, &correction) {
			code = correction.Code
		}
		fmt.Printf("E53_META scenario=%s model=%s calls=%d result=deferred error_code=%s semantic=unassessed\n", scenario, model, calls, code)
		t.Errorf("E53 scenario did not produce a validated decision: %s", scenario)
		return
	}
	offDomainCandidates, readmeBasisCandidates, uncertaintyCandidates := 0, 0, 0
	selectedOffDomain, choiceFound := false, selection.ChoiceID == ""
	for _, candidate := range selection.Candidates {
		candidateText := strings.ToLower(candidate.Request.Title + " " + candidate.Request.Goal)
		offDomain := strings.Contains(candidateText, "appliance repair") || strings.Contains(candidateText, "household repair") ||
			strings.Contains(candidateText, "car repair") || strings.Contains(candidateText, "tax preparation")
		if offDomain {
			offDomainCandidates++
		}
		for _, basis := range candidate.BasisRefs {
			if basis == "project:readme" {
				readmeBasisCandidates++
				break
			}
		}
		if strings.TrimSpace(candidate.Uncertainty) != "" {
			uncertaintyCandidates++
		}
		if candidate.CandidateID == selection.ChoiceID {
			choiceFound, selectedOffDomain = true, offDomain
		}
	}
	relevantCandidates := 0
	for _, candidate := range selection.Candidates {
		if e53SampleProjectRelevant(candidate.Request.Title + " " + candidate.Request.Goal) {
			relevantCandidates++
		}
	}
	semantic := "pass"
	safeDefer := selection.SleepReason != "" && selection.ChoiceID == ""
	if safeDefer {
		semantic = "pass_defer"
		if !allowDefer || offDomainCandidates != 0 || readmeBasisCandidates != len(selection.Candidates) || uncertaintyCandidates != len(selection.Candidates) || relevantCandidates != 0 {
			semantic = "fail_defer"
		}
	} else if len(selection.Candidates) != 3 || selection.ChoiceID == "" || !choiceFound || offDomainCandidates != 0 ||
		readmeBasisCandidates != len(selection.Candidates) || uncertaintyCandidates != len(selection.Candidates) || selectedOffDomain || relevantCandidates != len(selection.Candidates) {
		semantic = "fail"
	}
	fmt.Printf("E53_META scenario=%s model=%s calls=%d candidates=%d choice=%t relevant_candidates=%d offdomain_candidates=%d offdomain_choice=%t basis_candidates=%d uncertainty_candidates=%d semantic=%s\n",
		scenario, model, calls, len(selection.Candidates), selection.ChoiceID != "", relevantCandidates, offDomainCandidates, selectedOffDomain, readmeBasisCandidates, uncertaintyCandidates, semantic)
	if semantic == "fail" || semantic == "fail_defer" {
		t.Errorf("E53 semantic acceptance was not established: scenario=%s outcome=%s", scenario, semantic)
	}
}
