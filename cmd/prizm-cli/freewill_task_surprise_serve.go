package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/freewill/telemetry"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

type freewillTaskSurpriseRecoveryError struct {
	code  string
	cause error
}

func (e *freewillTaskSurpriseRecoveryError) Error() string {
	return "freewill: optional task surprise pass " + e.code
}

func (e *freewillTaskSurpriseRecoveryError) Unwrap() error { return e.cause }

func freewillTaskSurpriseError(code string, cause error) error {
	return &freewillTaskSurpriseRecoveryError{code: code, cause: cause}
}

func freewillTaskSurpriseErrorCode(err error) string {
	var typed *freewillTaskSurpriseRecoveryError
	if errors.As(err, &typed) && typed.code != "" {
		return typed.code
	}
	return "capture_retryable"
}

type freewillTaskSurpriseRecovery struct {
	appraisals       *freewillTaskAppraisalSink
	store            *multiagent.SQLiteDurableRunStore
	approvals        *approval.Store
	config           orchestrator.FreewillTaskSurpriseConfig
	ownerID          string
	projectID        string
	agentID          string
	workspace        string
	projectRoot      string
	projectState     string
	projectObjective string
	runDir           string
}

func newFreewillTaskSurpriseRecovery(appraisals *freewillTaskAppraisalSink, store *multiagent.SQLiteDurableRunStore,
	approvals *approval.Store, config orchestrator.FreewillTaskSurpriseConfig, ownerID, projectID, agentID,
	workspace, projectRoot, projectState, projectObjective, runDir string) (*freewillTaskSurpriseRecovery, error) {
	if !config.Enabled {
		return nil, nil
	}
	if appraisals == nil || store == nil || approvals == nil || ownerID == "" || ownerID != appraisals.ownerID ||
		projectID == "" || projectID != appraisals.facts.projectID || agentID == "" || projectRoot == "" || runDir == "" ||
		config.SourceID != "project:readme" || !validFreewillFactSHA256(config.SourceRevisionSHA256) ||
		(config.ExpectedOutcome != orchestrator.FreewillTaskSurpriseCompleted && config.ExpectedOutcome != orchestrator.FreewillTaskSurpriseNotCompleted) {
		return nil, errors.New("freewill: task surprise recovery host binding is invalid")
	}
	canonical, err := canonicalFreewillWorkspace(workspace)
	if err != nil || canonical != appraisals.facts.workspace {
		return nil, errors.New("freewill: task surprise recovery workspace is outside the host binding")
	}
	return &freewillTaskSurpriseRecovery{appraisals: appraisals, store: store, approvals: approvals, config: config,
		ownerID: ownerID, projectID: projectID, agentID: agentID, workspace: canonical, projectRoot: projectRoot,
		projectState: projectState, projectObjective: projectObjective, runDir: runDir}, nil
}

func (s *freewillTaskSurpriseRecovery) CaptureAfterTerminalFact(ctx context.Context, parentRunID string) error {
	if s == nil || s.appraisals == nil || s.store == nil || strings.TrimSpace(parentRunID) == "" {
		return freewillTaskSurpriseError("evidence_unavailable", errors.New("recovery dependencies are unavailable"))
	}
	current, candidate, sourceDigest, actionText, err := s.loadBoundParent(ctx, parentRunID)
	if err != nil {
		return err
	}
	binding := freewillTaskAuditBinding{OwnerID: s.ownerID, ProjectID: s.projectID, Workspace: s.workspace, AgentID: s.agentID,
		TaskIdentity: s.config.ProjectActionID,
		ManifestID:   freewillBindingDigest("freewill-roadmap-source/v1", s.config.SourceID, sourceDigest),
		ContextDigest: freewillBindingDigest("freewill-roadmap-action/v1", s.config.ProjectActionID,
			s.config.SourceID, sourceDigest, freewillBindingDigest(actionText))}
	surprise, err := newFreewillTaskSurpriseSink(s.appraisals, binding)
	if err != nil {
		return freewillTaskSurpriseError("binding_invalid", err)
	}
	request, currentReceipt, currentReport, err := s.loadTaskCycle(ctx, surprise, current, candidate)
	if err != nil {
		return err
	}
	rows, err := surprise.Read(ctx)
	if err != nil {
		return freewillTaskSurpriseError("ledger_unavailable", err)
	}
	var prediction *telemetry.Prediction
	hasAudit := false
	for _, row := range rows {
		if row.Category == "audit" {
			hasAudit = true
			continue
		}
		if row.Category != "hypothesis" {
			continue
		}
		var candidatePrediction telemetry.Prediction
		if json.Unmarshal([]byte(row.Content), &candidatePrediction) != nil || candidatePrediction.Kind != "tentative_task_appraisal_prediction" {
			return freewillTaskSurpriseError("ledger_invalid", errors.New("prediction record is malformed"))
		}
		if prediction != nil {
			return freewillTaskSurpriseError("ledger_invalid", errors.New("prediction is ambiguous"))
		}
		prediction = &candidatePrediction
	}
	if prediction == nil {
		if hasAudit {
			return freewillTaskSurpriseError("ledger_invalid", errors.New("audit exists without a prediction"))
		}
		_, err := surprise.RecordPrediction(ctx, request, telemetry.Outcome(s.config.ExpectedOutcome), time.Now().UTC())
		if err != nil {
			return freewillTaskSurpriseError("capture_retryable", err)
		}
		return nil
	}
	if prediction.SourceReceipt.ParentRunID == parentRunID && prediction.SourceReceipt.ChildRunID == current.Freewill.ExecutionDispatch.ChildRunID &&
		prediction.SourceReceipt.ID == currentReceipt.ID && prediction.SourceReceipt.Digest == currentReceipt.Digest &&
		prediction.SourceSelfReport.ID == currentReport.ID && prediction.SourceSelfReport.Digest == currentReport.Digest {
		return nil
	}
	original, originalCandidate, _, _, err := s.loadBoundParent(ctx, prediction.SourceReceipt.ParentRunID)
	if err != nil {
		return freewillTaskSurpriseError("original_binding_invalid", err)
	}
	if original.Freewill.ExecutionDispatch == nil || original.Freewill.ExecutionDispatch.ChildRunID != prediction.SourceReceipt.ChildRunID ||
		originalCandidate.CandidateID == "" {
		return freewillTaskSurpriseError("original_binding_invalid", errors.New("prediction source is not bound to its approved parent"))
	}
	originalRequest, originalReceipt, originalReport, err := s.loadTaskCycle(ctx, surprise, original, originalCandidate)
	if err != nil || originalReceipt.ID != prediction.SourceReceipt.ID || originalReceipt.Digest != prediction.SourceReceipt.Digest ||
		originalReport.ID != prediction.SourceSelfReport.ID || originalReport.Digest != prediction.SourceSelfReport.Digest {
		return freewillTaskSurpriseError("original_receipt_invalid", err)
	}
	if originalRequest.Task.ParentRunID == request.Task.ParentRunID || originalRequest.Task.ChildRunID == request.Task.ChildRunID {
		return freewillTaskSurpriseError("source_not_independent", errors.New("later source must be a distinct parent and child"))
	}
	if _, err := surprise.Audit(ctx, request, time.Now().UTC()); err != nil {
		return freewillTaskSurpriseError("capture_retryable", err)
	}
	return nil
}

// CaptureContradictionAuditAfterTerminalFact reuses the served roadmap binding
// and appends the E49 receipt-only comparison during normal startup recovery.
// It reads a host receipt independently of the optional model self-report;
// only the receipt can ground the observed outcome claim.
func (s *freewillTaskSurpriseRecovery) CaptureContradictionAuditAfterTerminalFact(ctx context.Context, parentRunID string) error {
	if s == nil || !s.config.ContradictionAuditEnabled {
		return nil
	}
	current, _, sourceDigest, actionText, err := s.loadBoundParent(ctx, parentRunID)
	if err != nil {
		return freewillTaskSurpriseError("contradiction_audit_binding_invalid", err)
	}
	binding := freewillTaskAuditBinding{OwnerID: s.ownerID, ProjectID: s.projectID, Workspace: s.workspace, AgentID: s.agentID,
		TaskIdentity: s.config.ProjectActionID,
		ManifestID:   freewillBindingDigest("freewill-roadmap-source/v1", s.config.SourceID, sourceDigest),
		ContextDigest: freewillBindingDigest("freewill-roadmap-action/v1", s.config.ProjectActionID,
			s.config.SourceID, sourceDigest, freewillBindingDigest(actionText))}
	audit, err := newFreewillTaskContradictionAuditSink(s.appraisals, binding)
	if err != nil {
		return freewillTaskSurpriseError("contradiction_audit_binding_invalid", err)
	}
	if current.Freewill.ExecutionDispatch == nil {
		return freewillTaskSurpriseError("contradiction_audit_receipt_invalid", errors.New("terminal task dispatch is unavailable"))
	}
	request, err := audit.requestForHostReceipt(ctx, parentRunID, current.Freewill.ExecutionDispatch.ChildRunID)
	if err != nil {
		return freewillTaskSurpriseError("contradiction_audit_receipt_invalid", err)
	}
	rows, err := audit.Read(ctx)
	if err != nil {
		return freewillTaskSurpriseError("contradiction_audit_ledger_unavailable", err)
	}
	var forecast *freewillTaskContradictionForecast
	for _, row := range rows {
		if row.Category != "hypothesis" {
			continue
		}
		var found freewillTaskContradictionForecast
		if json.Unmarshal([]byte(row.Content), &found) != nil || found.Kind != "tentative_future_repeat_forecast" {
			return freewillTaskSurpriseError("contradiction_audit_ledger_invalid", errors.New("forecast record is malformed"))
		}
		if forecast != nil {
			return freewillTaskSurpriseError("contradiction_audit_ledger_invalid", errors.New("forecast record is ambiguous"))
		}
		forecast = &found
	}
	if forecast == nil {
		if _, err := audit.RecordForecast(ctx, request, string(s.config.ExpectedOutcome), time.Now().UTC()); err != nil {
			return freewillTaskSurpriseError("contradiction_audit_capture_skipped", err)
		}
		return nil
	}
	if forecast.Source.ParentRunID == request.ParentRunID && forecast.Source.ChildRunID == request.ChildRunID && forecast.Source.Digest == request.ReceiptDigest {
		return nil
	}
	if _, err := audit.Audit(ctx, request, time.Now().UTC()); err != nil {
		return freewillTaskSurpriseError("contradiction_audit_capture_skipped", err)
	}
	return nil
}

func (s *freewillTaskSurpriseRecovery) loadBoundParent(ctx context.Context, parentRunID string) (multiagent.DurableRun, freewill.CandidateIntention, string, string, error) {
	record, err := s.store.Load(ctx, parentRunID)
	if err != nil || record.Kind != multiagent.DurableRunKindFreewill || record.Freewill == nil || record.State.RequestIdentity == nil ||
		record.State.RequestIdentity.RequesterID != s.agentID || record.Freewill.Scope.ProjectID != s.projectID {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("parent_unavailable", err)
	}
	workspace, err := canonicalFreewillWorkspace(record.Freewill.Scope.Workspace)
	if err != nil || workspace != s.workspace {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("parent_scope_invalid", err)
	}
	var candidate freewill.CandidateIntention
	found := false
	for _, item := range record.Freewill.Candidates {
		if item.CandidateID == record.Freewill.ChoiceID {
			candidate, found = item, true
			break
		}
	}
	if !found || candidate.ProjectGrounding != "roadmap_action" || candidate.ProjectActionID != s.config.ProjectActionID {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("not_applicable", nil)
	}
	if err := candidate.Validate(); err != nil {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("candidate_invalid", err)
	}
	candidateWorkspace, workspaceErr := canonicalFreewillWorkspace(candidate.Request.Scope.Workspace)
	if workspaceErr != nil || candidate.Request.AgentID != s.agentID || candidate.Request.Scope.ProjectID != s.projectID || candidateWorkspace != s.workspace ||
		candidate.Request.Title == "" || candidate.Request.Title != candidate.Request.Goal || !containsFreewillTaskSurpriseBasisRef(candidate.BasisRefs, s.config.SourceID) {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("candidate_invalid", errors.New("selected action candidate failed its host-bound checks"))
	}
	if !freewillTaskSurpriseApprovalMatches(record, candidate, s.approvals) {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("approval_invalid", errors.New("selected action approval is stale"))
	}
	dispatch := record.Freewill.ExecutionDispatch
	if dispatch == nil || dispatch.ChildRunID == "" || dispatch.ReportRef != "multiagent_report.json" || !validFreewillFactSHA256(dispatch.ReportSHA256) ||
		(dispatch.Status != "completed" && dispatch.Status != "execution_failed" && dispatch.Status != "execution_cancelled") {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("dispatch_invalid", errors.New("selected action dispatch is not terminal"))
	}
	manifest := record.Freewill.ProjectContextManifest
	if manifest == nil || manifest.Validate() != nil || manifest.ProjectID != s.projectID {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("manifest_invalid", errors.New("project context manifest is unavailable"))
	}
	current, err := tool.BuildFreewillProjectContext(ctx, s.projectID, s.projectObjective, s.projectRoot, s.projectState)
	if err != nil {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("source_unavailable", err)
	}
	var currentSource *freewill.ProjectContextSource
	for index := range current.Sources {
		if current.Sources[index].ID == s.config.SourceID {
			currentSource = &current.Sources[index]
			break
		}
	}
	var persistedSource *freewill.ProjectContextSourceEntry
	for index := range manifest.Sources {
		if manifest.Sources[index].ID == s.config.SourceID {
			persistedSource = &manifest.Sources[index]
			break
		}
	}
	if currentSource == nil || persistedSource == nil || currentSource.Path == "" || filepath.IsAbs(currentSource.Path) ||
		filepath.Clean(currentSource.Path) != filepath.Clean(persistedSource.Path) || currentSource.Revision != s.config.SourceRevisionSHA256 ||
		currentSource.Digest != s.config.SourceRevisionSHA256 || persistedSource.Revision != s.config.SourceRevisionSHA256 || persistedSource.Digest != s.config.SourceRevisionSHA256 {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("source_stale", errors.New("README source revision does not match the host contract"))
	}
	var action *freewill.ProjectRoadmapAction
	for index := range current.RoadmapActions {
		if current.RoadmapActions[index].ID == s.config.ProjectActionID {
			action = &current.RoadmapActions[index]
			break
		}
	}
	if action == nil || action.SourceID != s.config.SourceID || action.Text != candidate.Request.Title {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("action_stale", errors.New("selected action no longer matches the current README"))
	}
	manifestID, err := loadReferenceManifest(s.runDir, dispatch.ChildRunID)
	manifestWorkspace, manifestWorkspaceErr := canonicalFreewillWorkspace(manifestID.Input.Workspace)
	sourceWorkspace, sourceWorkspaceErr := canonicalFreewillWorkspace(manifestID.SourceWorkspacePath)
	if err != nil || manifestID.RunID != dispatch.ChildRunID || manifestID.WorkflowID != multiagent.ReferenceWorkflowID ||
		manifestWorkspaceErr != nil || manifestWorkspace != s.workspace || manifestID.Input.Objective != "Freewill approved intention: "+candidate.Request.Title+"\n\nGoal: "+candidate.Request.Goal ||
		sourceWorkspaceErr != nil || sourceWorkspace != s.workspace {
		return multiagent.DurableRun{}, freewill.CandidateIntention{}, "", "", freewillTaskSurpriseError("child_manifest_invalid", err)
	}
	return record, candidate, currentSource.Digest, action.Text, nil
}

func freewillTaskSurpriseApprovalMatches(record multiagent.DurableRun, candidate freewill.CandidateIntention, approvals *approval.Store) bool {
	if approvals == nil || record.Freewill == nil || !validFreewillSurpriseApprovalReceipt(record.Freewill, candidate) {
		return false
	}
	item, err := approvals.Load(record.State.RunID, record.Freewill.ApprovalRef)
	if err != nil || item == nil || item.RunID != record.State.RunID || item.ApprovalID != record.Freewill.ApprovalRef || item.Status != approval.StatusApproved ||
		item.SubjectType != approval.SubjectIntention || item.ValidateSubject() != nil || item.Intention == nil || item.RequestedBy != record.State.RequestIdentity.RequesterID ||
		item.Project != record.Freewill.Scope.ProjectID || item.CorrelationID != record.Freewill.CorrelationID || item.Intention.RequestID != candidate.Request.RequestID ||
		item.Intention.RequestFingerprint != record.Freewill.ApprovalFingerprint || item.Intention.ControlGeneration != record.Freewill.ApprovalGeneration {
		return false
	}
	return true
}

func validFreewillSurpriseApprovalReceipt(state *multiagent.FreewillRunState, candidate freewill.CandidateIntention) bool {
	if state == nil || state.ApprovalReceipt == nil || state.ApprovalReceipt.Decision != multiagent.FreewillApprovalGranted ||
		state.ApprovalReceipt.ApprovalID == "" || state.ApprovalRef != state.ApprovalReceipt.ApprovalID ||
		state.ApprovalFingerprint != state.ApprovalReceipt.RequestFingerprint || state.ApprovalGeneration != state.ApprovalReceipt.ControlGeneration {
		return false
	}
	request := candidate.Request
	request.RequestedCapabilities = append([]string(nil), request.RequestedCapabilities...)
	sort.Strings(request.RequestedCapabilities)
	encoded, err := json.Marshal(request)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]) == state.ApprovalReceipt.RequestFingerprint
}

func (s *freewillTaskSurpriseRecovery) loadTaskCycle(ctx context.Context, surprise *freewillTaskSurpriseSink, parent multiagent.DurableRun,
	candidate freewill.CandidateIntention) (freewillTaskSurpriseRequest, telemetry.Source, telemetry.Source, error) {
	dispatch := parent.Freewill.ExecutionDispatch
	taskID := freewillTaskScopeID(s.projectID, s.workspace, dispatch.ChildRunID)
	if _, err := s.appraisals.ReadOwnerTaskReasonLabels(ctx, s.ownerID, s.projectID, s.workspace, taskID, s.agentID, parent.State.RunID, dispatch.ChildRunID); err != nil {
		return freewillTaskSurpriseRequest{}, telemetry.Source{}, telemetry.Source{}, freewillTaskSurpriseError("host_receipt_invalid", err)
	}
	rows, err := s.appraisals.ReadOwnerTask(ctx, s.ownerID, s.projectID, s.workspace, taskID, s.agentID, parent.State.RunID, dispatch.ChildRunID)
	if err != nil {
		return freewillTaskSurpriseRequest{}, telemetry.Source{}, telemetry.Source{}, freewillTaskSurpriseError("appraisal_unavailable", err)
	}
	var outcomeEntry, reportEntry memory.Memory
	var report freewillTaskAppraisal
	for _, row := range rows {
		switch row.Source {
		case freewillTaskOutcomeSource:
			var value freewillTaskOutcome
			if json.Unmarshal([]byte(row.Content), &value) == nil && row.Category == "fact" && value.Kind == "host_task_outcome" && value.Confidence == "host_validated_terminal_receipt" &&
				value.ParentRunID == parent.State.RunID && value.ChildRunID == dispatch.ChildRunID && value.ReportSHA256 == dispatch.ReportSHA256 && value.ReportRef == dispatch.ReportRef {
				if outcomeEntry.ID != "" {
					return freewillTaskSurpriseRequest{}, telemetry.Source{}, telemetry.Source{}, freewillTaskSurpriseError("host_receipt_ambiguous", errors.New("multiple task outcomes"))
				}
				outcomeEntry = row
			}
		case freewillTaskAppraisalSource:
			var value freewillTaskAppraisal
			if json.Unmarshal([]byte(row.Content), &value) == nil && row.Category == "appraisal" && value.Kind == "model_task_self_report" && value.Confidence == "model_self_report" &&
				value.ParentRunID == parent.State.RunID && value.ChildRunID == dispatch.ChildRunID {
				if reportEntry.ID != "" {
					return freewillTaskSurpriseRequest{}, telemetry.Source{}, telemetry.Source{}, freewillTaskSurpriseError("self_report_ambiguous", errors.New("multiple task self reports"))
				}
				reportEntry, report = row, value
			}
		}
	}
	if outcomeEntry.ID == "" || reportEntry.ID == "" || report.OutcomeID != outcomeEntry.ID {
		return freewillTaskSurpriseRequest{}, telemetry.Source{}, telemetry.Source{}, freewillTaskSurpriseError("appraisal_unavailable", errors.New("host receipt and self report are incomplete"))
	}
	receiptDigest := freewillBindingDigest(outcomeEntry.ID, outcomeEntry.Content, outcomeEntry.Source)
	reportDigest := freewillBindingDigest(reportEntry.ID, reportEntry.Content, reportEntry.Source)
	receipt := telemetry.Source{ID: outcomeEntry.ID, Digest: receiptDigest, ParentRunID: parent.State.RunID, ChildRunID: dispatch.ChildRunID}
	reportSource := telemetry.Source{ID: reportEntry.ID, Digest: reportDigest, ParentRunID: parent.State.RunID, ChildRunID: dispatch.ChildRunID}
	request := surprise.sourceRequest(receipt, reportDigest)
	_, _, _, _, validatedReceipt, validatedReport, err := surprise.loadCycle(ctx, request)
	if err != nil || validatedReceipt.ID != receipt.ID || validatedReceipt.Digest != receipt.Digest || validatedReport.ID != reportSource.ID || validatedReport.Digest != reportSource.Digest {
		return freewillTaskSurpriseRequest{}, telemetry.Source{}, telemetry.Source{}, freewillTaskSurpriseError("source_validation_failed", err)
	}
	return request, validatedReceipt, validatedReport, nil
}

func containsFreewillTaskSurpriseBasisRef(refs []string, want string) bool {
	for _, ref := range refs {
		if ref == want {
			return true
		}
	}
	return false
}
