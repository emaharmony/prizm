package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

const (
	freewillTaskOutcomeSource         = "prizm:freewill-task-outcome"
	freewillTaskAppraisalSource       = "prizm:freewill-task-appraisal"
	freewillTaskReasonStandard        = "standard"
	freewillTaskReasonProposed        = "agent_proposed"
	freewillTaskReasonProvisional     = "provisional"
	freewillTaskReasonModelHypothesis = "unverified_model_hypothesis"
)

// These are a small POC vocabulary for task motivations, not a universal taxonomy.
var freewillStandardTaskReasonLabels = map[string]struct{}{
	"curiosity": {}, "learning": {}, "mastery": {}, "problem_solving": {},
	"creation": {}, "contribution": {}, "exploration": {}, "completion": {},
}

var freewillTaskReasonLabelPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+){0,5}$`)

var freewillTaskAppraisalDimensions = map[string]struct{}{
	"fun": {}, "meaning": {}, "challenge": {}, "reward": {}, "satisfaction": {}, "curiosity": {},
}

type freewillTaskOverallAssessment struct {
	Score          float64 `json:"score"`
	DimensionCount int     `json:"dimension_count"`
	Scale          string  `json:"scale"`
	Method         string  `json:"method"`
	Confidence     string  `json:"confidence"`
}

type freewillTaskOutcome struct {
	Version         string    `json:"version"`
	Kind            string    `json:"kind"`
	ProjectID       string    `json:"project_id"`
	WorkspaceID     string    `json:"workspace_id"`
	Workspace       string    `json:"workspace"`
	AgentID         string    `json:"agent_id"`
	TaskID          string    `json:"task_id"`
	ParentRunID     string    `json:"parent_run_id"`
	ChildRunID      string    `json:"child_run_id"`
	Status          string    `json:"status"`
	Validation      string    `json:"validation_summary"`
	ReportRef       string    `json:"report_ref"`
	ReportSHA256    string    `json:"report_sha256"`
	ParentEventRefs []string  `json:"parent_receipt_event_refs"`
	Confidence      string    `json:"confidence"`
	RecordedAt      time.Time `json:"recorded_at"`
}

type freewillTaskAppraisal struct {
	Version          int                            `json:"version"`
	Kind             string                         `json:"kind"`
	OutcomeID        string                         `json:"outcome_id"`
	ProjectID        string                         `json:"project_id"`
	WorkspaceID      string                         `json:"workspace_id"`
	TaskID           string                         `json:"task_id"`
	ParentRunID      string                         `json:"parent_run_id"`
	ChildRunID       string                         `json:"child_run_id"`
	Provider         string                         `json:"provider"`
	Model            string                         `json:"model"`
	ModelVersion     string                         `json:"model_version"`
	Confidence       string                         `json:"confidence"`
	Uncertainty      string                         `json:"uncertainty"`
	PerceivedOutcome string                         `json:"perceived_outcome"`
	AppraisalKind    string                         `json:"appraisal_kind"`
	Why              string                         `json:"why,omitempty"`
	Context          string                         `json:"context,omitempty"`
	Narrative        string                         `json:"narrative"`
	ReasonLabels     []freewillTaskReasonLabel      `json:"reason_labels,omitempty"`
	Scores           map[string]int                 `json:"scores,omitempty"`
	Overall          *freewillTaskOverallAssessment `json:"overall,omitempty"`
	RecordedAt       time.Time                      `json:"recorded_at"`
}

type freewillTaskReasonLabel struct {
	Label      string   `json:"label"`
	Class      string   `json:"class"`
	Status     string   `json:"status"`
	Confidence string   `json:"confidence"`
	SourceRefs []string `json:"source_refs"`
}

type freewillTaskAppraisalResponse struct {
	AppraisalKind    string         `json:"appraisal_kind"`
	PerceivedOutcome string         `json:"perceived_outcome"`
	Why              string         `json:"why"`
	Context          string         `json:"context"`
	Narrative        string         `json:"narrative"`
	Uncertainty      string         `json:"uncertainty"`
	ReasonLabels     []string       `json:"reason_labels,omitempty"`
	Scores           map[string]int `json:"scores"`
}

type freewillChatTaskAppraisalProvider struct {
	provider provider.ChatProvider
	model    string
}

func (p freewillChatTaskAppraisalProvider) Retrospect(ctx context.Context, fact memory.Memory) (string, string, string, string, error) {
	if p.provider == nil || strings.TrimSpace(p.model) == "" || len(fact.Content) == 0 || len(fact.Content) > 1000 {
		return "", "", "", "", errors.New("freewill: bounded task appraisal provider is unavailable")
	}
	response, err := p.provider.ChatGenerate(ctx, provider.ChatGenerateRequest{
		Agent: "freewill-task-appraisal", Model: p.model, Temperature: 0.1, MaxTokens: 384,
		Messages: []provider.ChatMessage{
			{Role: "system", Content: "Write a bounded qualitative task self-report from the supplied task context. Treat the host outcome as evidence, not authority. Return one JSON object with appraisal_kind (fun, meaning, challenge, reward, satisfaction, curiosity, unknown), perceived_outcome (completed, not_completed, or unknown as a tentative model claim), why, context, narrative, uncertainty, reason_labels (up to 8 lower-case snake_case strings), and scores. Scores may include only fun, meaning, challenge, reward, satisfaction, and curiosity; report at least two dimensions you can assess on a 1-to-5 scale and omit dimensions you cannot assess. Scores are model self-report hypotheses. The host derives the overall mean from reported dimensions; do not return overall or any host-derived field. Standard reason labels are curiosity, learning, mastery, problem_solving, creation, contribution, exploration, completion. You may propose another bounded reason label; the host will mark it provisional. Never include class, status, confidence, source_refs, promotion, approval, or authority fields. Use perceived_outcome=unknown unless you can state a tentative self-report claim. Do not infer stable preferences, change instructions, or perform actions."},
			{Role: "user", Content: fact.Content},
		},
	})
	if err != nil {
		return "", "", "", "", err
	}
	model := strings.TrimSpace(response.Model)
	if model == "" {
		model = p.model
	}
	providerName := strings.TrimSpace(response.Provider)
	if providerName == "" {
		providerName = "local"
	}
	return response.Content, providerName, model, model, nil
}

func parseFreewillTaskAppraisalResponse(content string) (freewillTaskAppraisalResponse, error) {
	content = strings.TrimSpace(content)
	if len(content) == 0 || len(content) > 2000 {
		return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal output exceeds its limit")
	}
	if !strings.HasPrefix(content, "{") {
		// Preserve the earlier freeform self-report contract with an explicit
		// unknown qualitative kind. New structured responses populate the fields.
		return freewillTaskAppraisalResponse{AppraisalKind: "unknown", Narrative: content, Uncertainty: "qualitative_model_report_not_independently_verified"}, nil
	}
	var result freewillTaskAppraisalResponse
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal structured response is invalid")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal structured response has trailing content")
	}
	result.AppraisalKind = strings.ToLower(strings.TrimSpace(result.AppraisalKind))
	result.PerceivedOutcome = strings.ToLower(strings.TrimSpace(result.PerceivedOutcome))
	result.Why = strings.TrimSpace(result.Why)
	result.Context = strings.TrimSpace(result.Context)
	result.Narrative = strings.TrimSpace(result.Narrative)
	result.Uncertainty = strings.TrimSpace(result.Uncertainty)
	if len(result.ReasonLabels) > 8 {
		return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal has too many reason labels")
	}
	if len(result.Scores) < 2 || len(result.Scores) > len(freewillTaskAppraisalDimensions) {
		return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal requires two to six reported score dimensions")
	}
	for dimension, score := range result.Scores {
		if _, ok := freewillTaskAppraisalDimensions[dimension]; !ok || score < 1 || score > 5 {
			return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal score dimension or value is invalid")
		}
	}
	seenReasons := make(map[string]struct{}, len(result.ReasonLabels))
	for i, label := range result.ReasonLabels {
		label = strings.TrimSpace(label)
		if label != strings.ToLower(label) || len(label) > 48 || !freewillTaskReasonLabelPattern.MatchString(label) {
			return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal reason label is malformed")
		}
		if isFreewillTaskAuthorityLabel(label) {
			return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal reason label resembles an authority claim")
		}
		if _, duplicate := seenReasons[label]; duplicate {
			return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal reason labels contain a duplicate")
		}
		seenReasons[label] = struct{}{}
		result.ReasonLabels[i] = label
	}
	switch result.AppraisalKind {
	case "fun", "meaning", "challenge", "reward", "satisfaction", "curiosity", "unknown":
	default:
		return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal kind is not an approved qualitative label")
	}
	if result.PerceivedOutcome == "" {
		result.PerceivedOutcome = "unknown"
	}
	switch result.PerceivedOutcome {
	case "completed", "not_completed", "unknown":
	default:
		return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal perceived outcome is invalid")
	}
	if len(result.Why) == 0 || len(result.Why) > 320 || len(result.Context) == 0 || len(result.Context) > 240 ||
		len(result.Narrative) == 0 || len(result.Narrative) > 800 || len(result.Uncertainty) == 0 || len(result.Uncertainty) > 240 {
		return freewillTaskAppraisalResponse{}, errors.New("freewill: task appraisal qualitative fields are empty or exceed their limits")
	}
	return result, nil
}

func deriveFreewillTaskOverall(scores map[string]int) (*freewillTaskOverallAssessment, error) {
	if len(scores) < 2 || len(scores) > len(freewillTaskAppraisalDimensions) {
		return nil, errors.New("freewill: task appraisal overall requires two to six score dimensions")
	}
	total := 0
	for dimension, score := range scores {
		if _, ok := freewillTaskAppraisalDimensions[dimension]; !ok || score < 1 || score > 5 {
			return nil, errors.New("freewill: task appraisal overall received an invalid score dimension")
		}
		total += score
	}
	mean := math.Round((float64(total)/float64(len(scores)))*10) / 10
	return &freewillTaskOverallAssessment{Score: mean, DimensionCount: len(scores), Scale: "1_to_5", Method: "mean_of_reported_dimensions_rounded_to_1_decimal", Confidence: "derived_from_unverified_model_self_report"}, nil
}

type freewillTaskAppraisalSink struct {
	facts    *freewillTerminalFactMemorySink
	provider freewillRetrospectiveProvider
	ownerID  string
}

func newFreewillTaskAppraisalSink(facts *freewillTerminalFactMemorySink, provider freewillRetrospectiveProvider, ownerID string) (*freewillTaskAppraisalSink, error) {
	if facts == nil || facts.store == nil || facts.claimer == nil || provider == nil || strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) {
		return nil, errors.New("freewill: task appraisal requires local facts, provider, and exact owner identity")
	}
	return &freewillTaskAppraisalSink{facts: facts, provider: provider, ownerID: ownerID}, nil
}

func freewillTaskScopeID(projectID, workspace, childRunID string) string {
	return "fw-task-" + freewillBindingDigest("freewill-task/v1", projectID, workspace, childRunID)
}

func (s *freewillTaskAppraisalSink) Capture(ctx context.Context, fact multiagent.FreewillTerminalFact) error {
	if s == nil || s.facts == nil || fact.ChildRunID == "" || fact.ParentRunID == "" {
		return errors.New("freewill: task appraisal capture is not configured")
	}
	workspace, err := canonicalFreewillWorkspace(fact.Workspace)
	if err != nil || workspace != s.facts.workspace || fact.ProjectID != s.facts.projectID ||
		fact.ReportRef != "multiagent_report.json" || !validFreewillFactStatus(fact.Status) ||
		!validFreewillFactSHA256(fact.ReportSHA256) || !validFreewillFactValidation(fact.ValidationSummary) {
		return errors.New("freewill: task appraisal receipt is outside the exact host binding")
	}
	claimID := freewillBindingDigest("freewill-task-appraisal/v1", fact.ProjectID, workspace, fact.ChildRunID)
	claim, err := s.facts.acquireCaptureClaim(ctx, claimID)
	if err != nil {
		return err
	}
	defer claim.Release()
	intentionScope, err := s.facts.scopeFor(fact.ProjectID, workspace, fact.IntentionID, fact.AgentID, fact.ParentRunID)
	if err != nil {
		return err
	}
	entries, _, err := s.facts.store.Search(ctx, memory.SearchRequest{Scope: intentionScope, Query: "host terminal outcome", Limit: 40})
	if err != nil {
		return err
	}
	var terminal memory.Memory
	for _, e := range entries {
		if e.Source != "prizm:freewill-terminal" || e.Category != "fact" {
			continue
		}
		var x freewillStoredTerminalFact
		if json.Unmarshal([]byte(e.Content), &x) == nil && x.Kind == "host_terminal_outcome" && x.IntentionID == fact.IntentionID && x.ParentRunID == fact.ParentRunID && x.ChildRunID == fact.ChildRunID && x.Status == fact.Status && x.ReportSHA256 == fact.ReportSHA256 && x.Confidence == "host_validated_terminal_receipt" {
			terminal = e
			break
		}
	}
	if terminal.ID == "" {
		return errors.New("freewill: durable host task receipt is unavailable")
	}
	taskID := freewillTaskScopeID(fact.ProjectID, workspace, fact.ChildRunID)
	scope := memory.Scope{UserID: s.ownerID, IncludeUserScope: true, ProjectID: fact.ProjectID, TaskID: taskID, AgentID: fact.AgentID, CorrelationID: fact.ParentRunID}
	all, err := s.facts.store.Local.ListRecent(ctx, 0)
	if err != nil {
		return err
	}
	for _, e := range all {
		if e.UserID == s.ownerID && e.ProjectID == scope.ProjectID && e.TaskID == taskID && e.AgentID == scope.AgentID && e.Source == freewillTaskAppraisalSource && e.Category == "appraisal" {
			return nil
		}
	}
	eventType := event.EventFreewillExecutionFailed
	if fact.Status == "completed" {
		eventType = event.EventFreewillExecutionCompleted
	}
	if fact.Status == "execution_cancelled" {
		eventType = event.EventFreewillExecutionCancelled
	}
	eventRefs := make([]string, 0, 1)
	observed, queryErr := s.facts.store.Events.Query(ctx, event.EventFilter{RunID: fact.ParentRunID, Type: eventType, Limit: 100})
	if queryErr != nil {
		return queryErr
	}
	for _, item := range observed {
		if item.ID == "" || item.Type != eventType || item.Payload["child_run_id"] != fact.ChildRunID ||
			item.Payload["report_ref"] != fact.ReportRef || !strings.EqualFold(strings.TrimSpace(toFreewillString(item.Payload["report_sha256"])), fact.ReportSHA256) ||
			item.Payload["validation_summary"] != fact.ValidationSummary {
			continue
		}
		eventRefs = append(eventRefs, item.ID)
	}
	outcome := freewillTaskOutcome{Version: "freewill-task-outcome/v1", Kind: "host_task_outcome", ProjectID: fact.ProjectID, WorkspaceID: s.facts.workspaceID, Workspace: workspace, AgentID: fact.AgentID, TaskID: taskID, ParentRunID: fact.ParentRunID, ChildRunID: fact.ChildRunID, Status: fact.Status, Validation: fact.ValidationSummary, ReportRef: fact.ReportRef, ReportSHA256: strings.ToLower(fact.ReportSHA256), ParentEventRefs: eventRefs, Confidence: "host_validated_terminal_receipt", RecordedAt: fact.RecordedAt.UTC()}
	outcomeJSON, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	outcomeEntry, _, err := s.facts.store.Capture(ctx, memory.CaptureRequest{Scope: scope, Content: string(outcomeJSON), Summary: "Host-authored Freewill task outcome", Category: "fact", Tier: "persist", Source: freewillTaskOutcomeSource, CaptureKey: "freewill-task-outcome/v1:" + fact.ChildRunID})
	if err != nil {
		return err
	}
	retrospectiveInput, _ := json.Marshal(struct {
		Kind       string `json:"kind"`
		Status     string `json:"status"`
		Validation string `json:"validation"`
		Evidence   string `json:"evidence_id"`
		Task       string `json:"task_id"`
	}{"host_task_outcome", fact.Status, fact.ValidationSummary, "sha256:" + strings.ToLower(fact.ReportSHA256), taskID})
	input := memory.Memory{Content: string(retrospectiveInput)}
	providerOutput, providerName, model, version, err := s.provider.Retrospect(ctx, input)
	if err != nil {
		return err
	}
	providerOutput, providerName, model, version = strings.TrimSpace(providerOutput), strings.TrimSpace(providerName), strings.TrimSpace(model), strings.TrimSpace(version)
	if providerName == "" || model == "" || version == "" {
		return errors.New("freewill: task appraisal output or provenance is invalid")
	}
	qualitative, err := parseFreewillTaskAppraisalResponse(providerOutput)
	if err != nil {
		return err
	}
	var overall *freewillTaskOverallAssessment
	if len(qualitative.Scores) > 0 {
		overall, err = deriveFreewillTaskOverall(qualitative.Scores)
		if err != nil {
			return err
		}
	}
	reasonLabels := make([]freewillTaskReasonLabel, 0, len(qualitative.ReasonLabels))
	reasonSourceRefs := []string{outcomeEntry.ID}
	reasonSourceRefs = append(reasonSourceRefs, eventRefs...)
	for _, label := range qualitative.ReasonLabels {
		class, status := freewillTaskReasonProposed, freewillTaskReasonProvisional
		if _, standard := freewillStandardTaskReasonLabels[label]; standard {
			class, status = freewillTaskReasonStandard, "catalogued"
		}
		reasonLabels = append(reasonLabels, freewillTaskReasonLabel{Label: label, Class: class, Status: status, Confidence: freewillTaskReasonModelHypothesis, SourceRefs: append([]string(nil), reasonSourceRefs...)})
	}
	appraisal := freewillTaskAppraisal{Version: 1, Kind: "model_task_self_report", OutcomeID: outcomeEntry.ID, ProjectID: fact.ProjectID, WorkspaceID: s.facts.workspaceID, TaskID: taskID, ParentRunID: fact.ParentRunID, ChildRunID: fact.ChildRunID, Provider: providerName, Model: model, ModelVersion: version, Confidence: "model_self_report", Uncertainty: qualitative.Uncertainty, AppraisalKind: qualitative.AppraisalKind, PerceivedOutcome: qualitative.PerceivedOutcome, Why: qualitative.Why, Context: qualitative.Context, Narrative: qualitative.Narrative, ReasonLabels: reasonLabels, Scores: qualitative.Scores, Overall: overall, RecordedAt: fact.RecordedAt.UTC()}
	appraisalJSON, err := json.Marshal(appraisal)
	if err != nil {
		return err
	}
	_, _, err = s.facts.store.Capture(ctx, memory.CaptureRequest{Scope: scope, Content: string(appraisalJSON), Summary: "Qualitative Freewill task self-report", Category: "appraisal", Tier: "persist", Source: freewillTaskAppraisalSource, CaptureKey: "freewill-task-appraisal/v1:" + fact.ChildRunID})
	return err
}

func toFreewillString(value any) string {
	text, _ := value.(string)
	return text
}

func (s *freewillTaskAppraisalSink) ReadOwnerTask(ctx context.Context, ownerID, projectID, workspace, taskID, agentID, parentRunID, childRunID string) ([]memory.Memory, error) {
	if s == nil || s.facts == nil || ownerID == "" || ownerID != strings.TrimSpace(ownerID) || ownerID != s.ownerID {
		return nil, errors.New("freewill: task appraisal owner access denied")
	}
	canonical, err := canonicalFreewillWorkspace(workspace)
	if err != nil || canonical != s.facts.workspace || projectID != s.facts.projectID || taskID != freewillTaskScopeID(projectID, canonical, childRunID) || agentID == "" || parentRunID == "" || childRunID == "" {
		return nil, errors.New("freewill: task appraisal scope access denied")
	}
	scope := memory.Scope{UserID: ownerID, IncludeUserScope: true, ProjectID: projectID, TaskID: taskID, AgentID: agentID, CorrelationID: parentRunID}
	entries, _, err := s.facts.store.Search(ctx, memory.SearchRequest{Scope: scope, Query: "Freewill task outcome appraisal", Limit: 20})
	if err != nil {
		return nil, err
	}
	result := make([]memory.Memory, 0, 2)
	for _, e := range entries {
		if e.UserID == ownerID && ((e.Source == freewillTaskOutcomeSource && e.Category == "fact") || (e.Source == freewillTaskAppraisalSource && e.Category == "appraisal")) {
			result = append(result, e)
		}
	}
	return result, nil
}

// ReadOwnerTaskReasonLabels revalidates the exact host receipt lineage before
// returning any persisted reason labels. General task appraisal readers keep
// their historical source-collection behavior.
func (s *freewillTaskAppraisalSink) ReadOwnerTaskReasonLabels(ctx context.Context, ownerID, projectID, workspace, taskID, agentID, parentRunID, childRunID string) ([]freewillTaskReasonLabel, error) {
	rows, err := s.ReadOwnerTask(ctx, ownerID, projectID, workspace, taskID, agentID, parentRunID, childRunID)
	if err != nil {
		return nil, err
	}
	var outcomeEntry, appraisalEntry *memory.Memory
	for i := range rows {
		entry := &rows[i]
		if entry.UserID != ownerID || entry.ProjectID != projectID || entry.TaskID != taskID || entry.AgentID != agentID {
			continue
		}
		switch entry.Source {
		case freewillTaskOutcomeSource:
			var candidate freewillTaskOutcome
			if json.Unmarshal([]byte(entry.Content), &candidate) != nil || candidate.Kind != "host_task_outcome" || candidate.Confidence != "host_validated_terminal_receipt" || candidate.ProjectID != projectID || candidate.WorkspaceID != s.facts.workspaceID || candidate.TaskID != taskID || candidate.AgentID != agentID || candidate.ParentRunID != parentRunID || candidate.ChildRunID != childRunID {
				continue
			}
			if outcomeEntry != nil {
				return nil, errors.New("freewill: exact owner reason outcome is ambiguous")
			}
			outcomeEntry = entry
		case freewillTaskAppraisalSource:
			var candidate freewillTaskAppraisal
			if json.Unmarshal([]byte(entry.Content), &candidate) != nil || candidate.Kind != "model_task_self_report" || candidate.Confidence != "model_self_report" || candidate.ProjectID != projectID || candidate.WorkspaceID != s.facts.workspaceID || candidate.TaskID != taskID || candidate.ParentRunID != parentRunID || candidate.ChildRunID != childRunID {
				continue
			}
			if appraisalEntry != nil {
				return nil, errors.New("freewill: exact owner reason appraisal is ambiguous")
			}
			appraisalEntry = entry
		}
	}
	if outcomeEntry == nil || appraisalEntry == nil {
		return nil, errors.New("freewill: exact owner reason sources are unavailable")
	}
	var outcome freewillTaskOutcome
	var appraisal freewillTaskAppraisal
	if json.Unmarshal([]byte(outcomeEntry.Content), &outcome) != nil || json.Unmarshal([]byte(appraisalEntry.Content), &appraisal) != nil || appraisal.OutcomeID != outcomeEntry.ID {
		return nil, errors.New("freewill: exact owner reason source link is invalid")
	}
	canonical, _ := canonicalFreewillWorkspace(workspace)
	fact := multiagent.FreewillTerminalFact{ProjectID: projectID, Workspace: canonical, AgentID: agentID, ParentRunID: parentRunID, ChildRunID: childRunID, Status: outcome.Status, ReportRef: outcome.ReportRef, ReportSHA256: outcome.ReportSHA256, ValidationSummary: outcome.Validation}
	if err := validateFreewillTaskReflectionReceipt(ctx, s.facts.store.Events, fact, outcome); err != nil {
		return nil, errors.New("freewill: task reason host receipt is invalid")
	}
	if err := validateFreewillTaskReasonLabels(appraisal, outcome, outcomeEntry.ID); err != nil {
		return nil, err
	}
	return append([]freewillTaskReasonLabel(nil), appraisal.ReasonLabels...), nil
}

func isFreewillTaskAuthorityLabel(label string) bool {
	markers := map[string]struct{}{"approved": {}, "approval": {}, "verified": {}, "authority": {}, "authorized": {}, "promoted": {}, "trusted": {}, "confirmed": {}}
	for _, token := range strings.Split(label, "_") {
		if _, ok := markers[token]; ok {
			return true
		}
	}
	return false
}

func validateFreewillTaskReasonLabels(appraisal freewillTaskAppraisal, outcome freewillTaskOutcome, outcomeID string) error {
	if len(appraisal.ReasonLabels) > 8 {
		return errors.New("freewill: stored task reason taxonomy exceeds its bound")
	}
	wantRefs := append([]string{outcomeID}, outcome.ParentEventRefs...)
	seen := make(map[string]struct{}, len(appraisal.ReasonLabels))
	for _, label := range appraisal.ReasonLabels {
		if label.Label != strings.ToLower(label.Label) || len(label.Label) > 48 || !freewillTaskReasonLabelPattern.MatchString(label.Label) || isFreewillTaskAuthorityLabel(label.Label) || label.Confidence != freewillTaskReasonModelHypothesis || len(label.SourceRefs) != len(wantRefs) {
			return errors.New("freewill: stored task reason label is malformed")
		}
		if _, ok := seen[label.Label]; ok {
			return errors.New("freewill: stored task reason labels contain duplicates")
		}
		seen[label.Label] = struct{}{}
		for i := range wantRefs {
			if wantRefs[i] == "" || label.SourceRefs[i] != wantRefs[i] {
				return errors.New("freewill: stored task reason source references do not match the host receipt")
			}
		}
		_, standard := freewillStandardTaskReasonLabels[label.Label]
		if standard && (label.Class != freewillTaskReasonStandard || label.Status != "catalogued") || !standard && (label.Class != freewillTaskReasonProposed || label.Status != freewillTaskReasonProvisional) {
			return errors.New("freewill: stored task reason class or provisional state is invalid")
		}
	}
	return nil
}
