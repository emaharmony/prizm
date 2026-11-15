package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/freewill/telemetry"
	"github.com/emaharmony/prizm/internal/memory"
)

const (
	freewillTaskSurpriseSource = "prizm:freewill-task-surprise"
	freewillTaskSurpriseID     = "freewill-task-surprise/v1"
)

type freewillTaskSurpriseRequest struct {
	Task             freewillTaskAuditRequest `json:"task"`
	SelfReportDigest string                   `json:"self_report_digest"`
}

type freewillTaskSurpriseSink struct {
	appraisals  *freewillTaskAppraisalSink
	base        *freewillTaskContradictionAuditSink
	proposition string
	scope       memory.Scope
}

func newFreewillTaskSurpriseSink(appraisals *freewillTaskAppraisalSink, binding freewillTaskAuditBinding) (*freewillTaskSurpriseSink, error) {
	base, err := newFreewillTaskContradictionAuditSink(appraisals, binding)
	if err != nil {
		return nil, err
	}
	proposition := freewillBindingDigest(freewillTaskSurpriseID, base.proposition)
	scopeID := "fw-task-surprise-" + proposition
	return &freewillTaskSurpriseSink{appraisals: appraisals, base: base, proposition: proposition,
		scope: memory.Scope{UserID: base.binding.OwnerID, IncludeUserScope: true, ProjectID: base.binding.ProjectID, TaskID: scopeID, AgentID: base.binding.AgentID, CorrelationID: scopeID}}, nil
}

func (s *freewillTaskSurpriseSink) loadCycle(ctx context.Context, req freewillTaskSurpriseRequest) (memory.Memory, freewillTaskOutcome, memory.Memory, freewillTaskAppraisal, telemetry.Source, telemetry.Source, error) {
	if s == nil || s.base == nil || len(req.SelfReportDigest) != 64 || strings.ToLower(req.SelfReportDigest) != req.SelfReportDigest {
		return memory.Memory{}, freewillTaskOutcome{}, memory.Memory{}, freewillTaskAppraisal{}, telemetry.Source{}, telemetry.Source{}, errors.New("freewill: surprise source request is invalid")
	}
	outcomeEntry, outcome, receipt, err := s.base.loadReceipt(ctx, req.Task)
	if err != nil {
		return memory.Memory{}, freewillTaskOutcome{}, memory.Memory{}, freewillTaskAppraisal{}, telemetry.Source{}, telemetry.Source{}, err
	}
	taskID := freewillTaskScopeID(s.base.binding.ProjectID, s.base.workspace, req.Task.ChildRunID)
	rows, err := s.appraisals.ReadOwnerTask(ctx, s.base.binding.OwnerID, s.base.binding.ProjectID, s.base.workspace, taskID, s.base.binding.AgentID, req.Task.ParentRunID, req.Task.ChildRunID)
	if err != nil {
		return memory.Memory{}, freewillTaskOutcome{}, memory.Memory{}, freewillTaskAppraisal{}, telemetry.Source{}, telemetry.Source{}, err
	}
	var reportEntry memory.Memory
	var report freewillTaskAppraisal
	for _, row := range rows {
		if row.Source != freewillTaskAppraisalSource || row.Category != "appraisal" || row.UserID != s.base.binding.OwnerID || row.ProjectID != s.base.binding.ProjectID || row.TaskID != taskID || row.AgentID != s.base.binding.AgentID {
			continue
		}
		var candidate freewillTaskAppraisal
		if json.Unmarshal([]byte(row.Content), &candidate) != nil || candidate.Kind != "model_task_self_report" || candidate.Confidence != "model_self_report" || candidate.OutcomeID != outcomeEntry.ID || candidate.ProjectID != s.base.binding.ProjectID || candidate.WorkspaceID != s.base.workspaceID || candidate.TaskID != taskID || candidate.ParentRunID != req.Task.ParentRunID || candidate.ChildRunID != req.Task.ChildRunID || candidate.Uncertainty == "" || (candidate.PerceivedOutcome != "completed" && candidate.PerceivedOutcome != "not_completed" && candidate.PerceivedOutcome != "unknown") {
			continue
		}
		if reportEntry.ID != "" {
			return memory.Memory{}, freewillTaskOutcome{}, memory.Memory{}, freewillTaskAppraisal{}, telemetry.Source{}, telemetry.Source{}, errors.New("freewill: surprise self-report is ambiguous")
		}
		reportEntry, report = row, candidate
	}
	if reportEntry.ID == "" {
		return memory.Memory{}, freewillTaskOutcome{}, memory.Memory{}, freewillTaskAppraisal{}, telemetry.Source{}, telemetry.Source{}, errors.New("freewill: exact task self-report is unavailable")
	}
	reportDigest := freewillBindingDigest(reportEntry.ID, reportEntry.Content, reportEntry.Source)
	if reportDigest != req.SelfReportDigest {
		return memory.Memory{}, freewillTaskOutcome{}, memory.Memory{}, freewillTaskAppraisal{}, telemetry.Source{}, telemetry.Source{}, errors.New("freewill: surprise self-report digest is stale")
	}
	reported := telemetry.Outcome(report.PerceivedOutcome)
	if !telemetry.ValidOutcome(reported) {
		return memory.Memory{}, freewillTaskOutcome{}, memory.Memory{}, freewillTaskAppraisal{}, telemetry.Source{}, telemetry.Source{}, errors.New("freewill: self-report outcome is not a typed appraisal")
	}
	reportSource := telemetry.Source{ID: reportEntry.ID, Digest: reportDigest, Outcome: reported, ParentRunID: report.ParentRunID, ChildRunID: report.ChildRunID, RecordedAt: report.RecordedAt.UTC()}
	hostOutcome := telemetry.Outcome(receipt.Claim)
	if !telemetry.ValidOutcome(hostOutcome) {
		return memory.Memory{}, freewillTaskOutcome{}, memory.Memory{}, freewillTaskAppraisal{}, telemetry.Source{}, telemetry.Source{}, errors.New("freewill: host outcome is not typed")
	}
	return outcomeEntry, outcome, reportEntry, report, telemetry.Source{ID: receipt.MemoryID, Digest: receipt.Digest, Outcome: hostOutcome, ParentRunID: receipt.ParentRunID, ChildRunID: receipt.ChildRunID, RecordedAt: receipt.RecordedAt}, reportSource, nil
}

func (s *freewillTaskSurpriseSink) rows(ctx context.Context) ([]memory.Memory, error) {
	lister, ok := s.appraisals.facts.store.Local.(interface {
		ListScopedRecords(context.Context, memory.Scope) ([]memory.Memory, error)
	})
	if !ok {
		return nil, errors.New("freewill: surprise ledger requires exact scoped reads")
	}
	rows, err := lister.ListScopedRecords(ctx, s.scope)
	if err != nil {
		return nil, err
	}
	filtered := rows[:0]
	for _, row := range rows {
		if row.UserID == s.base.binding.OwnerID && row.ProjectID == s.base.binding.ProjectID && row.TaskID == s.scope.TaskID && row.AgentID == s.base.binding.AgentID && row.Source == freewillTaskSurpriseSource {
			filtered = append(filtered, row)
		}
	}
	return filtered, nil
}

func (s *freewillTaskSurpriseSink) RecordPrediction(ctx context.Context, req freewillTaskSurpriseRequest, expected telemetry.Outcome, recordedAt time.Time) (memory.Memory, error) {
	if !telemetry.ValidOutcome(expected) || recordedAt.IsZero() {
		return memory.Memory{}, errors.New("freewill: expected task appraisal prediction is invalid")
	}
	claim, err := s.appraisals.facts.acquireCaptureClaim(ctx, freewillBindingDigest(freewillTaskSurpriseID, "proposition-lock", s.proposition))
	if err != nil {
		return memory.Memory{}, err
	}
	defer claim.Release()
	_, outcome, _, _, receipt, report, err := s.loadCycle(ctx, req)
	if err != nil {
		return memory.Memory{}, err
	}
	if !recordedAt.After(outcome.RecordedAt) || !recordedAt.After(report.RecordedAt) {
		return memory.Memory{}, errors.New("freewill: prediction must follow its source cycle")
	}
	forecast := telemetry.Prediction{Version: 1, Kind: "tentative_task_appraisal_prediction", PropositionKey: s.proposition, OwnerID: s.base.binding.OwnerID, ProjectID: s.base.binding.ProjectID, WorkspaceID: s.base.workspaceID, AgentID: s.base.binding.AgentID, TaskIdentity: s.base.binding.TaskIdentity, ManifestID: s.base.binding.ManifestID, ContextDigest: s.base.binding.ContextDigest, ExpectedOutcome: expected, SourceReceipt: receipt, SourceSelfReport: report, Attribution: "host_recorded_tentative_prediction", Uncertainty: "high; one source cycle does not establish stable interest", RecordedAt: recordedAt.UTC()}
	forecast.PredictionID = "prediction-" + freewillBindingDigest(s.proposition, receipt.ID, receipt.Digest, report.ID, report.Digest)
	rows, err := s.rows(ctx)
	if err != nil {
		return memory.Memory{}, err
	}
	for _, row := range rows {
		if row.Category != "hypothesis" {
			continue
		}
		var prior telemetry.Prediction
		if json.Unmarshal([]byte(row.Content), &prior) == nil && prior.Kind == forecast.Kind {
			if prior.PredictionID == forecast.PredictionID && prior.ExpectedOutcome == expected && prior.SourceReceipt.Digest == receipt.Digest && prior.SourceSelfReport.Digest == report.Digest {
				return row, nil
			}
			return memory.Memory{}, errors.New("freewill: exact task context already has a different original prediction")
		}
	}
	content, _ := json.Marshal(forecast)
	stored, _, err := s.appraisals.facts.store.Capture(ctx, memory.CaptureRequest{Scope: s.scope, Content: string(content), Summary: "Tentative task appraisal prediction", Category: "hypothesis", Tier: "persist", Source: freewillTaskSurpriseSource, CaptureKey: "prediction:" + forecast.PredictionID})
	return stored, err
}

func (s *freewillTaskSurpriseSink) Audit(ctx context.Context, req freewillTaskSurpriseRequest, recordedAt time.Time) (memory.Memory, error) {
	if recordedAt.IsZero() {
		return memory.Memory{}, errors.New("freewill: surprise audit timestamp is required")
	}
	claim, err := s.appraisals.facts.acquireCaptureClaim(ctx, freewillBindingDigest(freewillTaskSurpriseID, "proposition-lock", s.proposition))
	if err != nil {
		return memory.Memory{}, err
	}
	defer claim.Release()
	rows, err := s.rows(ctx)
	if err != nil {
		return memory.Memory{}, err
	}
	var originalRow memory.Memory
	var prediction telemetry.Prediction
	for _, row := range rows {
		if row.Category != "hypothesis" {
			continue
		}
		var candidate telemetry.Prediction
		if json.Unmarshal([]byte(row.Content), &candidate) == nil && candidate.Kind == "tentative_task_appraisal_prediction" {
			if originalRow.ID != "" {
				return memory.Memory{}, errors.New("freewill: original task prediction is ambiguous")
			}
			originalRow, prediction = row, candidate
		}
	}
	if originalRow.ID == "" || !validFreewillTaskSurprisePrediction(prediction, s) {
		return memory.Memory{}, errors.New("freewill: original task prediction is unavailable or invalid")
	}
	predictionHash := freewillBindingDigest(originalRow.ID, originalRow.Content, originalRow.Source)
	originalRequest := s.sourceRequest(prediction.SourceReceipt, prediction.SourceSelfReport.Digest)
	_, originalOutcome, _, _, originalReceipt, originalReport, err := s.loadCycle(ctx, originalRequest)
	if err != nil || !sameSurpriseSource(originalReceipt, prediction.SourceReceipt) || !sameSurpriseSource(originalReport, prediction.SourceSelfReport) {
		return memory.Memory{}, errors.New("freewill: original prediction sources are stale")
	}
	_, outcome, _, _, receipt, report, err := s.loadCycle(ctx, req)
	if err != nil {
		return memory.Memory{}, err
	}
	if receipt.ID == prediction.SourceReceipt.ID || report.ID == prediction.SourceSelfReport.ID || !prediction.RecordedAt.After(originalOutcome.RecordedAt) || !outcome.RecordedAt.After(prediction.RecordedAt) || !report.RecordedAt.After(prediction.RecordedAt) || !recordedAt.After(outcome.RecordedAt) || !recordedAt.After(report.RecordedAt) {
		return memory.Memory{}, errors.New("freewill: surprise audit requires a later independent evidence cycle")
	}
	linked, err := s.verifiedRevisions(ctx, rows, originalRow, prediction, predictionHash)
	if err != nil {
		return memory.Memory{}, err
	}
	for _, prior := range linked {
		if prior.Record.SourceReceipt.ID == receipt.ID && prior.Record.SourceSelfReport.ID == report.ID {
			for _, row := range rows {
				if row.ID == prior.ContentID {
					return row, nil
				}
			}
		}
	}
	if len(linked) > 0 && (!receipt.RecordedAt.After(linked[len(linked)-1].Record.SourceReceipt.RecordedAt) || !report.RecordedAt.After(linked[len(linked)-1].Record.SourceSelfReport.RecordedAt)) {
		return memory.Memory{}, errors.New("freewill: surprise evidence is not later than the preceding audit cycle")
	}
	relation := telemetry.Compare(prediction.ExpectedOutcome, report.Outcome, receipt.Outcome)
	previousID, previousHash, revision := prediction.PredictionID, predictionHash, 1
	if len(linked) > 0 {
		last := linked[len(linked)-1]
		previousID, previousHash, revision = last.Record.RevisionID, last.ContentHash, last.Record.Revision+1
	}
	key := "revision:" + prediction.PredictionID + ":" + predictionHash + ":" + receipt.ID + ":" + receipt.Digest + ":" + report.ID + ":" + report.Digest
	record := telemetry.SurpriseRevision{Version: 1, Kind: "preference_surprise_revision", Revision: revision, PropositionKey: s.proposition, OriginalPredictionID: prediction.PredictionID, OriginalPredictionHash: predictionHash, PreviousRevisionID: previousID, PreviousRevisionHash: previousHash, ExpectedOutcome: prediction.ExpectedOutcome, ReportedOutcome: report.Outcome, HostOutcome: receipt.Outcome, Relation: relation, SourceReceipt: receipt, SourceSelfReport: report, Attribution: "host_compared_typed_prediction_to_tentative_report_after_receipt_validation", Uncertainty: "high; one surprise record does not establish durable interest", FurtherEvidenceRequired: true, DurableInterest: false, RecordedAt: recordedAt.UTC()}
	record.RevisionID = "revision-" + freewillBindingDigest(key)
	content, _ := json.Marshal(record)
	stored, _, err := s.appraisals.facts.store.Capture(ctx, memory.CaptureRequest{Scope: s.scope, Content: string(content), Summary: "Typed task preference surprise audit", Category: "audit", Tier: "persist", Source: freewillTaskSurpriseSource, CaptureKey: key})
	return stored, err
}

func (s *freewillTaskSurpriseSink) sourceRequest(receipt telemetry.Source, reportDigest string) freewillTaskSurpriseRequest {
	return freewillTaskSurpriseRequest{Task: freewillTaskAuditRequest{OwnerID: s.base.binding.OwnerID, ProjectID: s.base.binding.ProjectID, Workspace: s.base.workspace, AgentID: s.base.binding.AgentID, TaskIdentity: s.base.binding.TaskIdentity, ManifestID: s.base.binding.ManifestID, ContextDigest: s.base.binding.ContextDigest, Proposition: s.base.proposition, ParentRunID: receipt.ParentRunID, ChildRunID: receipt.ChildRunID, ReceiptDigest: receipt.Digest}, SelfReportDigest: reportDigest}
}

func validFreewillTaskSurprisePrediction(prediction telemetry.Prediction, s *freewillTaskSurpriseSink) bool {
	return prediction.Version == 1 && prediction.Kind == "tentative_task_appraisal_prediction" && prediction.PredictionID == "prediction-"+freewillBindingDigest(s.proposition, prediction.SourceReceipt.ID, prediction.SourceReceipt.Digest, prediction.SourceSelfReport.ID, prediction.SourceSelfReport.Digest) && prediction.PropositionKey == s.proposition && prediction.OwnerID == s.base.binding.OwnerID && prediction.ProjectID == s.base.binding.ProjectID && prediction.WorkspaceID == s.base.workspaceID && prediction.AgentID == s.base.binding.AgentID && prediction.TaskIdentity == s.base.binding.TaskIdentity && prediction.ManifestID == s.base.binding.ManifestID && prediction.ContextDigest == s.base.binding.ContextDigest && telemetry.ValidOutcome(prediction.ExpectedOutcome) && prediction.Uncertainty != "" && prediction.Attribution == "host_recorded_tentative_prediction" && prediction.RecordedAt.After(prediction.SourceReceipt.RecordedAt) && prediction.RecordedAt.After(prediction.SourceSelfReport.RecordedAt)
}

func (s *freewillTaskSurpriseSink) verifiedRevisions(ctx context.Context, rows []memory.Memory, originalRow memory.Memory, prediction telemetry.Prediction, predictionHash string) ([]telemetry.LinkedRevision, error) {
	linked := make([]telemetry.LinkedRevision, 0)
	for _, row := range rows {
		if row.Category != "audit" {
			continue
		}
		var record telemetry.SurpriseRevision
		if json.Unmarshal([]byte(row.Content), &record) != nil {
			return nil, telemetry.ErrInvalidRevisionChain
		}
		linked = append(linked, telemetry.LinkedRevision{Record: record, ContentID: row.ID, ContentHash: freewillBindingDigest(row.ID, row.Content, row.Source)})
	}
	ordered, err := telemetry.ValidateRevisionChain(prediction.PredictionID, predictionHash, s.proposition, linked)
	if err != nil {
		return nil, err
	}
	for _, item := range ordered {
		req := s.sourceRequest(item.Record.SourceReceipt, item.Record.SourceSelfReport.Digest)
		_, _, _, _, receipt, report, sourceErr := s.loadCycle(ctx, req)
		if sourceErr != nil || !sameSurpriseSource(receipt, item.Record.SourceReceipt) || !sameSurpriseSource(report, item.Record.SourceSelfReport) || report.Outcome != item.Record.ReportedOutcome || receipt.Outcome != item.Record.HostOutcome || item.Record.Relation != telemetry.Compare(item.Record.ExpectedOutcome, report.Outcome, receipt.Outcome) || item.Record.ExpectedOutcome != prediction.ExpectedOutcome {
			return nil, errors.New("freewill: prior surprise revision sources are stale or inconsistent")
		}
	}
	_ = originalRow
	return ordered, nil
}

func sameSurpriseSource(actual, stored telemetry.Source) bool {
	return actual.ID == stored.ID && actual.Digest == stored.Digest && actual.Outcome == stored.Outcome && actual.ParentRunID == stored.ParentRunID && actual.ChildRunID == stored.ChildRunID && actual.RecordedAt.UTC().Equal(stored.RecordedAt.UTC())
}

func (s *freewillTaskSurpriseSink) Read(ctx context.Context) ([]memory.Memory, error) {
	rows, err := s.rows(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Category != "hypothesis" && row.Category != "audit" {
			return nil, fmt.Errorf("freewill: surprise record has invalid category %q", row.Category)
		}
	}
	return rows, nil
}
