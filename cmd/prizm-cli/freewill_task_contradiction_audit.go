package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

const (
	freewillTaskContradictionAuditSource = "prizm:freewill-task-contradiction-audit"
	freewillTaskContradictionAuditID     = "freewill-task-contradiction-audit/v1"
)

// This binding is created from host configuration after the task manifest and
// context revision have been validated. Request fields are checked against it;
// they never define proposition equivalence.
type freewillTaskAuditBinding struct {
	OwnerID       string
	ProjectID     string
	Workspace     string
	AgentID       string
	TaskIdentity  string
	ManifestID    string
	ContextDigest string
}

type freewillTaskAuditRequest struct {
	OwnerID       string `json:"owner_id"`
	ProjectID     string `json:"project_id"`
	Workspace     string `json:"workspace"`
	AgentID       string `json:"agent_id"`
	TaskIdentity  string `json:"task_identity"`
	ManifestID    string `json:"manifest_id"`
	ContextDigest string `json:"context_digest"`
	Proposition   string `json:"proposition_key"`
	ParentRunID   string `json:"parent_run_id"`
	ChildRunID    string `json:"child_run_id"`
	ReceiptDigest string `json:"receipt_digest"`
}

type freewillTaskAuditReceipt struct {
	MemoryID    string    `json:"memory_id"`
	Digest      string    `json:"digest"`
	Status      string    `json:"status"`
	Claim       string    `json:"typed_outcome"`
	ParentRunID string    `json:"parent_run_id"`
	ChildRunID  string    `json:"child_run_id"`
	RecordedAt  time.Time `json:"recorded_at"`
}

type freewillTaskContradictionForecast struct {
	Version         int                      `json:"version"`
	Kind            string                   `json:"kind"`
	ForecastID      string                   `json:"forecast_id"`
	PropositionKey  string                   `json:"proposition_key"`
	OwnerID         string                   `json:"owner_id"`
	ProjectID       string                   `json:"project_id"`
	WorkspaceID     string                   `json:"workspace_id"`
	AgentID         string                   `json:"agent_id"`
	TaskIdentity    string                   `json:"task_identity"`
	ManifestID      string                   `json:"manifest_id"`
	ContextDigest   string                   `json:"context_digest"`
	ForecastAbout   string                   `json:"forecast_about"`
	ExpectedOutcome string                   `json:"expected_outcome"`
	Uncertainty     string                   `json:"uncertainty"`
	Attribution     string                   `json:"attribution"`
	Source          freewillTaskAuditReceipt `json:"source_receipt"`
	RecordedAt      time.Time                `json:"recorded_at"`
}

type freewillTaskContradictionAudit struct {
	Version                int                      `json:"version"`
	Kind                   string                   `json:"kind"`
	AuditID                string                   `json:"audit_id"`
	PropositionKey         string                   `json:"proposition_key"`
	OriginalForecastID     string                   `json:"original_forecast_id"`
	OriginalForecastDigest string                   `json:"original_forecast_digest"`
	PreviousAuditID        string                   `json:"previous_audit_id,omitempty"`
	PreviousAuditDigest    string                   `json:"previous_audit_digest,omitempty"`
	NewSource              freewillTaskAuditReceipt `json:"new_source_receipt"`
	Result                 string                   `json:"result"`
	RevisionReason         string                   `json:"revision_reason,omitempty"`
	RevisedConclusion      string                   `json:"revised_conclusion,omitempty"`
	Attribution            string                   `json:"attribution"`
	Uncertainty            string                   `json:"uncertainty"`
	RecordedAt             time.Time                `json:"recorded_at"`
}

type freewillTaskContradictionAuditSink struct {
	appraisals  *freewillTaskAppraisalSink
	binding     freewillTaskAuditBinding
	workspace   string
	workspaceID string
	proposition string
	scope       memory.Scope
}

func newFreewillTaskContradictionAuditSink(appraisals *freewillTaskAppraisalSink, binding freewillTaskAuditBinding) (*freewillTaskContradictionAuditSink, error) {
	if appraisals == nil || appraisals.facts == nil || binding.OwnerID != appraisals.ownerID || binding.ProjectID != appraisals.facts.projectID ||
		binding.OwnerID == "" || binding.OwnerID != strings.TrimSpace(binding.OwnerID) || binding.AgentID == "" || binding.AgentID != strings.TrimSpace(binding.AgentID) ||
		binding.TaskIdentity == "" || binding.TaskIdentity != strings.TrimSpace(binding.TaskIdentity) || binding.ManifestID == "" || binding.ManifestID != strings.TrimSpace(binding.ManifestID) ||
		!validFreewillFactSHA256(binding.ContextDigest) || strings.ToLower(binding.ContextDigest) != binding.ContextDigest {
		return nil, errors.New("freewill: contradiction audit requires exact host task configuration")
	}
	workspace, err := canonicalFreewillWorkspace(binding.Workspace)
	if err != nil || workspace != appraisals.facts.workspace {
		return nil, errors.New("freewill: contradiction audit workspace is outside the host binding")
	}
	binding.Workspace = workspace
	workspaceID := appraisals.facts.workspaceID
	proposition := freewillBindingDigest(freewillTaskContradictionAuditID, binding.OwnerID, binding.ProjectID, workspaceID, binding.AgentID, binding.TaskIdentity, binding.ManifestID, binding.ContextDigest)
	scopeID := "fw-task-audit-" + proposition
	return &freewillTaskContradictionAuditSink{appraisals: appraisals, binding: binding, workspace: workspace, workspaceID: workspaceID,
		proposition: proposition, scope: memory.Scope{UserID: binding.OwnerID, IncludeUserScope: true, ProjectID: binding.ProjectID, TaskID: scopeID, AgentID: binding.AgentID, CorrelationID: scopeID}}, nil
}

func (s *freewillTaskContradictionAuditSink) validateRequest(req freewillTaskAuditRequest) error {
	if s == nil || s.appraisals == nil || s.appraisals.facts == nil || req.OwnerID != s.binding.OwnerID || req.ProjectID != s.binding.ProjectID ||
		req.AgentID != s.binding.AgentID || req.TaskIdentity != s.binding.TaskIdentity || req.ManifestID != s.binding.ManifestID || req.ContextDigest != s.binding.ContextDigest ||
		req.Proposition != s.proposition || req.ParentRunID == "" || req.ChildRunID == "" || req.ParentRunID != strings.TrimSpace(req.ParentRunID) || req.ChildRunID != strings.TrimSpace(req.ChildRunID) {
		return errors.New("freewill: contradiction audit request is outside the exact configured proposition")
	}
	workspace, err := canonicalFreewillWorkspace(req.Workspace)
	if err != nil || workspace != s.workspace {
		return errors.New("freewill: contradiction audit request workspace is outside the exact configured proposition")
	}
	if len(req.ReceiptDigest) != 64 || strings.ToLower(req.ReceiptDigest) != req.ReceiptDigest {
		return errors.New("freewill: contradiction audit source digest is invalid")
	}
	return nil
}

// requestForHostReceipt derives a scoped audit request from the stored host
// outcome alone. The terminal receipt and durable event remain the authority;
// no model appraisal is required to identify or validate that source.
func (s *freewillTaskContradictionAuditSink) requestForHostReceipt(ctx context.Context, parentRunID, childRunID string) (freewillTaskAuditRequest, error) {
	request := freewillTaskAuditRequest{OwnerID: s.binding.OwnerID, ProjectID: s.binding.ProjectID, Workspace: s.workspace,
		AgentID: s.binding.AgentID, TaskIdentity: s.binding.TaskIdentity, ManifestID: s.binding.ManifestID,
		ContextDigest: s.binding.ContextDigest, Proposition: s.proposition, ParentRunID: parentRunID, ChildRunID: childRunID}
	taskID := freewillTaskScopeID(s.binding.ProjectID, s.workspace, childRunID)
	rows, err := s.appraisals.ReadOwnerTask(ctx, s.binding.OwnerID, s.binding.ProjectID, s.workspace, taskID, s.binding.AgentID, parentRunID, childRunID)
	if err != nil {
		return freewillTaskAuditRequest{}, err
	}
	for _, row := range rows {
		if row.Source != freewillTaskOutcomeSource || row.Category != "fact" || row.UserID != s.binding.OwnerID || row.ProjectID != s.binding.ProjectID || row.TaskID != taskID || row.AgentID != s.binding.AgentID {
			continue
		}
		var outcome freewillTaskOutcome
		if json.Unmarshal([]byte(row.Content), &outcome) != nil || outcome.Kind != "host_task_outcome" || outcome.Confidence != "host_validated_terminal_receipt" ||
			outcome.ProjectID != s.binding.ProjectID || outcome.Workspace != s.workspace || outcome.AgentID != s.binding.AgentID || outcome.ParentRunID != parentRunID || outcome.ChildRunID != childRunID {
			continue
		}
		if request.ReceiptDigest != "" {
			return freewillTaskAuditRequest{}, errors.New("freewill: contradiction audit source receipt is ambiguous")
		}
		request.ReceiptDigest = freewillBindingDigest(row.ID, row.Content, row.Source)
	}
	if request.ReceiptDigest == "" {
		return freewillTaskAuditRequest{}, errors.New("freewill: contradiction audit host receipt is unavailable")
	}
	if _, _, _, err := s.loadReceipt(ctx, request); err != nil {
		return freewillTaskAuditRequest{}, err
	}
	return request, nil
}

func (s *freewillTaskContradictionAuditSink) loadReceipt(ctx context.Context, req freewillTaskAuditRequest) (memory.Memory, freewillTaskOutcome, freewillTaskAuditReceipt, error) {
	if err := s.validateRequest(req); err != nil {
		return memory.Memory{}, freewillTaskOutcome{}, freewillTaskAuditReceipt{}, err
	}
	taskID := freewillTaskScopeID(s.binding.ProjectID, s.workspace, req.ChildRunID)
	rows, err := s.appraisals.ReadOwnerTask(ctx, s.binding.OwnerID, s.binding.ProjectID, s.workspace, taskID, s.binding.AgentID, req.ParentRunID, req.ChildRunID)
	if err != nil {
		return memory.Memory{}, freewillTaskOutcome{}, freewillTaskAuditReceipt{}, err
	}
	var receipt memory.Memory
	var outcome freewillTaskOutcome
	for _, row := range rows {
		if row.Source != freewillTaskOutcomeSource || row.Category != "fact" || row.UserID != s.binding.OwnerID || row.ProjectID != s.binding.ProjectID || row.TaskID != taskID || row.AgentID != s.binding.AgentID {
			continue
		}
		var candidate freewillTaskOutcome
		if json.Unmarshal([]byte(row.Content), &candidate) != nil || candidate.Kind != "host_task_outcome" || candidate.Confidence != "host_validated_terminal_receipt" || candidate.ParentRunID != req.ParentRunID || candidate.ChildRunID != req.ChildRunID {
			continue
		}
		if receipt.ID != "" {
			return memory.Memory{}, freewillTaskOutcome{}, freewillTaskAuditReceipt{}, errors.New("freewill: contradiction audit source receipt is ambiguous")
		}
		receipt, outcome = row, candidate
	}
	if receipt.ID == "" {
		return memory.Memory{}, freewillTaskOutcome{}, freewillTaskAuditReceipt{}, errors.New("freewill: contradiction audit host receipt is unavailable")
	}
	digest := freewillBindingDigest(receipt.ID, receipt.Content, receipt.Source)
	if digest != req.ReceiptDigest {
		return memory.Memory{}, freewillTaskOutcome{}, freewillTaskAuditReceipt{}, errors.New("freewill: contradiction audit host receipt digest is stale")
	}
	fact := multiagent.FreewillTerminalFact{ProjectID: outcome.ProjectID, Workspace: outcome.Workspace, AgentID: outcome.AgentID, ParentRunID: outcome.ParentRunID, ChildRunID: outcome.ChildRunID, Status: outcome.Status, ReportRef: outcome.ReportRef, ReportSHA256: outcome.ReportSHA256, ValidationSummary: outcome.Validation, RecordedAt: outcome.RecordedAt}
	if err := validateFreewillTaskReflectionReceipt(ctx, s.appraisals.facts.store.Events, fact, outcome); err != nil {
		return memory.Memory{}, freewillTaskOutcome{}, freewillTaskAuditReceipt{}, err
	}
	claim := freewillTaskAuditClaim(outcome)
	return receipt, outcome, freewillTaskAuditReceipt{MemoryID: receipt.ID, Digest: digest, Status: outcome.Status, Claim: claim, ParentRunID: outcome.ParentRunID, ChildRunID: outcome.ChildRunID, RecordedAt: outcome.RecordedAt.UTC()}, nil
}

func freewillTaskAuditClaim(outcome freewillTaskOutcome) string {
	switch {
	case outcome.Status == "completed" && strings.EqualFold(strings.TrimSpace(outcome.Validation), "passed"):
		return "completed"
	case outcome.Status == "execution_failed" && strings.EqualFold(strings.TrimSpace(outcome.Validation), "failed"):
		return "not_completed"
	case outcome.Status == "execution_cancelled":
		return "unknown"
	default:
		return "incomparable"
	}
}

// RecordForecast persists one explicitly tentative prediction about a future
// repeat. It does not infer a contradiction from earlier outcomes.
func (s *freewillTaskContradictionAuditSink) RecordForecast(ctx context.Context, req freewillTaskAuditRequest, expectedOutcome string, recordedAt time.Time) (memory.Memory, error) {
	if expectedOutcome != "completed" && expectedOutcome != "not_completed" || recordedAt.IsZero() {
		return memory.Memory{}, errors.New("freewill: contradiction audit forecast must be a typed, timestamped tentative outcome")
	}
	claim, err := s.appraisals.facts.acquireCaptureClaim(ctx, freewillBindingDigest(freewillTaskContradictionAuditID, "proposition-lock", s.proposition))
	if err != nil {
		return memory.Memory{}, err
	}
	defer claim.Release()
	_, outcome, source, err := s.loadReceipt(ctx, req)
	if err != nil {
		return memory.Memory{}, err
	}
	if source.Claim != "completed" && source.Claim != "not_completed" {
		return memory.Memory{}, errors.New("freewill: forecast requires a typed host-verified source receipt")
	}
	record := freewillTaskContradictionForecast{Version: 1, Kind: "tentative_future_repeat_forecast", PropositionKey: s.proposition, OwnerID: s.binding.OwnerID, ProjectID: s.binding.ProjectID, WorkspaceID: s.workspaceID, AgentID: s.binding.AgentID, TaskIdentity: s.binding.TaskIdentity, ManifestID: s.binding.ManifestID, ContextDigest: s.binding.ContextDigest, ForecastAbout: "future_repeat_of_same_configured_task_context", ExpectedOutcome: expectedOutcome, Uncertainty: "high; one prior host receipt does not establish a stable outcome", Attribution: "host_recorded_tentative_forecast", Source: source, RecordedAt: recordedAt.UTC()}
	if outcome.RecordedAt.After(record.RecordedAt) {
		return memory.Memory{}, errors.New("freewill: forecast timestamp precedes its source receipt")
	}
	record.ForecastID = "forecast-" + freewillBindingDigest(s.proposition, source.MemoryID, source.Digest)
	rows, err := s.auditRows(ctx)
	if err != nil {
		return memory.Memory{}, err
	}
	for _, row := range rows {
		var prior freewillTaskContradictionForecast
		if json.Unmarshal([]byte(row.Content), &prior) == nil && prior.Kind == record.Kind {
			if prior.PropositionKey == s.proposition && prior.ExpectedOutcome == expectedOutcome && prior.Source.Digest == source.Digest {
				return row, nil
			}
			return memory.Memory{}, errors.New("freewill: configured proposition already has a different original forecast")
		}
	}
	content, _ := json.Marshal(record)
	reqKey := "forecast:" + record.ForecastID
	stored, _, err := s.appraisals.facts.store.Capture(ctx, memory.CaptureRequest{Scope: s.scope, Content: string(content), Summary: "Tentative future repeat forecast", Category: "hypothesis", Tier: "persist", Source: freewillTaskContradictionAuditSource, CaptureKey: reqKey})
	if err != nil {
		return memory.Memory{}, err
	}
	return stored, nil
}

func (s *freewillTaskContradictionAuditSink) auditRows(ctx context.Context) ([]memory.Memory, error) {
	lister, ok := s.appraisals.facts.store.Local.(interface {
		ListScopedRecords(context.Context, memory.Scope) ([]memory.Memory, error)
	})
	if !ok {
		return nil, errors.New("freewill: contradiction audit requires exact scoped local reads")
	}
	rows, err := lister.ListScopedRecords(ctx, s.scope)
	if err != nil {
		return nil, err
	}
	result := rows[:0]
	for _, row := range rows {
		if row.UserID == s.binding.OwnerID && row.ProjectID == s.binding.ProjectID && row.TaskID == s.scope.TaskID && row.AgentID == s.binding.AgentID && row.Source == freewillTaskContradictionAuditSource {
			result = append(result, row)
		}
	}
	return result, nil
}

func freewillTaskAuditResult(expected, observed string) string {
	switch observed {
	case "unknown":
		return "unknown"
	case "incomparable":
		return "incomparable"
	case "completed", "not_completed":
		if observed == expected {
			return "equal"
		}
		return "contradiction"
	default:
		return "incomparable"
	}
}

func (s *freewillTaskContradictionAuditSink) Audit(ctx context.Context, req freewillTaskAuditRequest, recordedAt time.Time) (memory.Memory, error) {
	if recordedAt.IsZero() {
		return memory.Memory{}, errors.New("freewill: contradiction audit timestamp is required")
	}
	claim, err := s.appraisals.facts.acquireCaptureClaim(ctx, freewillBindingDigest(freewillTaskContradictionAuditID, "proposition-lock", s.proposition))
	if err != nil {
		return memory.Memory{}, err
	}
	defer claim.Release()
	rows, err := s.auditRows(ctx)
	if err != nil {
		return memory.Memory{}, err
	}
	var forecastEntry memory.Memory
	var forecast freewillTaskContradictionForecast
	for _, row := range rows {
		var candidate freewillTaskContradictionForecast
		if json.Unmarshal([]byte(row.Content), &candidate) == nil && candidate.Kind == "tentative_future_repeat_forecast" {
			if forecastEntry.ID != "" {
				return memory.Memory{}, errors.New("freewill: original configured forecast is ambiguous")
			}
			forecastEntry, forecast = row, candidate
		}
	}
	if forecastEntry.ID == "" || forecast.Version != 1 || forecast.PropositionKey != s.proposition || forecast.OwnerID != s.binding.OwnerID || forecast.ProjectID != s.binding.ProjectID || forecast.WorkspaceID != s.workspaceID || forecast.AgentID != s.binding.AgentID || forecast.TaskIdentity != s.binding.TaskIdentity || forecast.ManifestID != s.binding.ManifestID || forecast.ContextDigest != s.binding.ContextDigest || forecast.ForecastAbout != "future_repeat_of_same_configured_task_context" || forecast.Uncertainty == "" || forecast.Attribution != "host_recorded_tentative_forecast" || (forecast.ExpectedOutcome != "completed" && forecast.ExpectedOutcome != "not_completed") {
		return memory.Memory{}, errors.New("freewill: original future-repeat forecast is unavailable or invalid")
	}
	forecastDigest := freewillBindingDigest(forecastEntry.ID, forecastEntry.Content, forecastEntry.Source)
	if forecast.ForecastID != "forecast-"+freewillBindingDigest(s.proposition, forecast.Source.MemoryID, forecast.Source.Digest) || forecast.Source.MemoryID == "" || forecast.Source.Digest == "" {
		return memory.Memory{}, errors.New("freewill: original forecast lineage is invalid")
	}
	_, outcome, source, err := s.loadReceipt(ctx, req)
	if err != nil {
		return memory.Memory{}, err
	}
	if source.MemoryID == forecast.Source.MemoryID || source.Digest == forecast.Source.Digest {
		return memory.Memory{}, errors.New("freewill: contradiction audit requires a later independent receipt")
	}
	initialRequest := freewillTaskAuditRequest{OwnerID: s.binding.OwnerID, ProjectID: s.binding.ProjectID, Workspace: s.workspace, AgentID: s.binding.AgentID, TaskIdentity: s.binding.TaskIdentity, ManifestID: s.binding.ManifestID, ContextDigest: s.binding.ContextDigest, Proposition: s.proposition, ParentRunID: forecast.Source.ParentRunID, ChildRunID: forecast.Source.ChildRunID, ReceiptDigest: forecast.Source.Digest}
	_, initialOutcome, initialSource, err := s.loadReceipt(ctx, initialRequest)
	if err != nil || initialSource.MemoryID != forecast.Source.MemoryID {
		return memory.Memory{}, errors.New("freewill: original forecast source receipt is stale or invalid")
	}
	if !forecast.RecordedAt.After(initialOutcome.RecordedAt) || !outcome.RecordedAt.After(forecast.RecordedAt) || !recordedAt.After(outcome.RecordedAt) {
		return memory.Memory{}, errors.New("freewill: contradiction audit chronology does not follow forecast and receipts")
	}
	type priorAuditEntry struct {
		row    memory.Memory
		record freewillTaskContradictionAudit
	}
	nextByPrevious := make(map[string]priorAuditEntry)
	auditCount := 0
	for _, row := range rows {
		if row.Category != "audit" {
			continue
		}
		var prior freewillTaskContradictionAudit
		if json.Unmarshal([]byte(row.Content), &prior) != nil || prior.Kind != "cross_cycle_contradiction_audit" || prior.PreviousAuditID == "" {
			return memory.Memory{}, errors.New("freewill: prior contradiction audit record is malformed")
		}
		if _, exists := nextByPrevious[prior.PreviousAuditID]; exists {
			return memory.Memory{}, errors.New("freewill: prior contradiction audit chain branches")
		}
		nextByPrevious[prior.PreviousAuditID] = priorAuditEntry{row: row, record: prior}
		auditCount++
	}
	previousID, previousDigest := forecast.ForecastID, forecastDigest
	previousAt := forecast.RecordedAt
	var replay memory.Memory
	visited := 0
	for {
		entry, ok := nextByPrevious[previousID]
		if !ok {
			break
		}
		prior, row := entry.record, entry.row
		if prior.Version != 1 || prior.AuditID == "" || prior.PropositionKey != s.proposition || prior.OriginalForecastID != forecast.ForecastID || prior.OriginalForecastDigest != forecastDigest || prior.PreviousAuditDigest != previousDigest || prior.NewSource.MemoryID == "" || prior.NewSource.Digest == "" || prior.NewSource.ParentRunID == "" || prior.NewSource.ChildRunID == "" || prior.RecordedAt.IsZero() || prior.RecordedAt.Before(previousAt) {
			return memory.Memory{}, errors.New("freewill: prior contradiction audit chain link is invalid")
		}
		priorKey := "audit:" + forecast.ForecastID + ":" + forecastDigest + ":" + prior.NewSource.MemoryID + ":" + prior.NewSource.Digest
		if prior.AuditID != "audit-"+freewillBindingDigest(priorKey) || row.ID == "" || row.Source != freewillTaskContradictionAuditSource || row.Category != "audit" {
			return memory.Memory{}, errors.New("freewill: prior contradiction audit identity or source digest is invalid")
		}
		priorReq := freewillTaskAuditRequest{OwnerID: s.binding.OwnerID, ProjectID: s.binding.ProjectID, Workspace: s.workspace, AgentID: s.binding.AgentID, TaskIdentity: s.binding.TaskIdentity, ManifestID: s.binding.ManifestID, ContextDigest: s.binding.ContextDigest, Proposition: s.proposition, ParentRunID: prior.NewSource.ParentRunID, ChildRunID: prior.NewSource.ChildRunID, ReceiptDigest: prior.NewSource.Digest}
		_, _, verifiedPriorSource, verifyErr := s.loadReceipt(ctx, priorReq)
		if verifyErr != nil || verifiedPriorSource.MemoryID != prior.NewSource.MemoryID || verifiedPriorSource.Claim != prior.NewSource.Claim || prior.Result != freewillTaskAuditResult(forecast.ExpectedOutcome, verifiedPriorSource.Claim) {
			return memory.Memory{}, errors.New("freewill: prior contradiction audit source or outcome is stale")
		}
		if prior.NewSource.MemoryID == source.MemoryID && prior.NewSource.Digest == source.Digest {
			replay = row
		}
		previousID, previousDigest, previousAt = prior.AuditID, freewillBindingDigest(row.ID, row.Content, row.Source), prior.RecordedAt
		visited++
	}
	if visited != auditCount {
		return memory.Memory{}, errors.New("freewill: prior contradiction audit chain contains an unlinked record")
	}
	if replay.ID != "" {
		return replay, nil
	}
	if !source.RecordedAt.After(previousAt) {
		return memory.Memory{}, errors.New("freewill: audit source is not later than the previous audited cycle")
	}
	result := freewillTaskAuditResult(forecast.ExpectedOutcome, source.Claim)
	reason, revision := "later_receipt_validation_not_comparable_to_typed_forecast", ""
	switch result {
	case "unknown":
		reason = "later_receipt_does_not_resolve_the_forecast"
	case "equal":
		reason = "later_receipt_matches_tentative_forecast"
	case "contradiction":
		reason, revision = "later_independent_host_receipt_opposes_tentative_future_repeat_forecast", source.Claim
	}
	record := freewillTaskContradictionAudit{Version: 1, Kind: "cross_cycle_contradiction_audit", PropositionKey: s.proposition, OriginalForecastID: forecast.ForecastID, OriginalForecastDigest: forecastDigest, PreviousAuditID: previousID, PreviousAuditDigest: previousDigest, NewSource: source, Result: result, RevisionReason: reason, RevisedConclusion: revision, Attribution: "host_decided_from_typed_receipts_only", Uncertainty: "high; this audit compares one forecast with one later receipt and does not establish a durable preference", RecordedAt: recordedAt.UTC()}
	captureKey := "audit:" + forecast.ForecastID + ":" + forecastDigest + ":" + source.MemoryID + ":" + source.Digest
	record.AuditID = "audit-" + freewillBindingDigest(captureKey)
	content, _ := json.Marshal(record)
	stored, _, err := s.appraisals.facts.store.Capture(ctx, memory.CaptureRequest{Scope: s.scope, Content: string(content), Summary: "Cross-cycle task contradiction audit", Category: "audit", Tier: "persist", Source: freewillTaskContradictionAuditSource, CaptureKey: captureKey})
	if err != nil {
		return memory.Memory{}, err
	}
	return stored, nil
}

func (s *freewillTaskContradictionAuditSink) Read(ctx context.Context) ([]memory.Memory, error) {
	rows, err := s.auditRows(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Category != "hypothesis" && rows[i].Category != "audit" {
			return nil, fmt.Errorf("freewill: contradiction audit record category is invalid")
		}
	}
	return rows, nil
}
