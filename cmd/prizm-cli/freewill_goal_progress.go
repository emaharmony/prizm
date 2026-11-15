package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/memory"
)

const (
	freewillGoalProgressSource = "prizm:freewill-goal-progress"
	freewillGoalReviewSource   = "prizm:freewill-goal-relevance-review"
)

type freewillGoalProgressPayload struct {
	Version             int      `json:"version"`
	Kind                string   `json:"kind"`
	OwnerID             string   `json:"owner_id"`
	ProjectID           string   `json:"project_id"`
	Workspace           string   `json:"workspace"`
	WorkspaceID         string   `json:"workspace_id"`
	AgentID             string   `json:"agent_id"`
	ProposalID          string   `json:"proposal_id"`
	GoalID              string   `json:"goal_id"`
	GoalRevision        int      `json:"goal_revision"`
	RequestID           string   `json:"request_id"`
	ChildRunID          string   `json:"child_run_id"`
	ReceiptRef          string   `json:"receipt_ref"`
	ReceiptEventID      string   `json:"receipt_event_id"`
	ReportSHA256        string   `json:"report_sha256"`
	SelfReportRef       string   `json:"self_report_ref"`
	PreviousReviewID    string   `json:"previous_review_id,omitempty"`
	HostStatus          string   `json:"host_status"`
	HostValidation      string   `json:"host_validation"`
	TentativeSelfReport string   `json:"tentative_self_report,omitempty"`
	RelevanceClass      string   `json:"relevance_class"`
	RelevanceReason     string   `json:"relevance_reason"`
	RelevanceSourceIDs  []string `json:"relevance_source_ids"`
	RecordedAt          string   `json:"recorded_at"`
}

type freewillGoalReviewPayload struct {
	Version          int      `json:"version"`
	Kind             string   `json:"kind"`
	OwnerID          string   `json:"owner_id"`
	ProjectID        string   `json:"project_id"`
	WorkspaceID      string   `json:"workspace_id"`
	AgentID          string   `json:"agent_id"`
	ProposalID       string   `json:"proposal_id"`
	GoalID           string   `json:"goal_id"`
	GoalRevision     int      `json:"goal_revision"`
	ProgressRecordID string   `json:"progress_record_id"`
	PreviousReviewID string   `json:"previous_review_id,omitempty"`
	Classification   string   `json:"classification"`
	Reason           string   `json:"reason"`
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	Confidence       string   `json:"confidence"`
	SourceIDs        []string `json:"source_ids"`
	RecordedAt       string   `json:"recorded_at"`
}

type freewillGoalRelevanceInput struct {
	GoalHypothesis       string   `json:"goal_hypothesis"`
	HostStatus           string   `json:"host_status"`
	HostValidation       string   `json:"host_validation"`
	TaskSelfReportLabel  string   `json:"task_self_report_label"`
	OwnerTentativeReport string   `json:"owner_tentative_report,omitempty"`
	SourceIDs            []string `json:"source_ids"`
}

type freewillGoalRelevanceOutput struct {
	Classification string   `json:"classification"`
	Reason         string   `json:"reason"`
	SourceIDs      []string `json:"source_ids"`
}

type freewillGoalRelevanceReviewer interface {
	ReviewGoalRelevance(context.Context, string) (string, error)
}

type freewillLocalFakeGoalRelevanceReviewer struct{}

func (freewillLocalFakeGoalRelevanceReviewer) ReviewGoalRelevance(_ context.Context, input string) (string, error) {
	var request freewillGoalRelevanceInput
	if err := json.Unmarshal([]byte(input), &request); err != nil {
		return "", err
	}
	result, err := json.Marshal(freewillGoalRelevanceOutput{Classification: "uncertain", Reason: "synthetic local assessment; goal relevance remains tentative", SourceIDs: append([]string(nil), request.SourceIDs...)})
	return string(result), err
}

func (s *freewillGoalProposalSink) readCheckInTaskEvidence(ctx context.Context, childRunID string) (freewillGoalEvidence, error) {
	rows, err := s.readTaskEvidence(ctx, childRunID)
	if err != nil || len(rows) != 1 {
		return freewillGoalEvidence{}, errors.New("freewill: exact terminal task evidence is unavailable")
	}
	row := rows[0]
	all, err := s.appraisals.facts.store.Local.ListRecent(ctx, 0)
	if err != nil {
		return freewillGoalEvidence{}, err
	}
	var outcome *freewillTaskOutcome
	for _, item := range all {
		if item.ID != row.OutcomeID || item.UserID != s.ownerID || item.ProjectID != s.projectID || item.AgentID != s.agentID || item.TaskID != row.TaskID || item.Source != freewillTaskOutcomeSource || item.Category != "fact" {
			continue
		}
		var candidate freewillTaskOutcome
		if err := json.Unmarshal([]byte(item.Content), &candidate); err != nil {
			return freewillGoalEvidence{}, errors.New("freewill: terminal outcome receipt is malformed")
		}
		outcome = &candidate
	}
	if outcome == nil || outcome.Version != "freewill-task-outcome/v1" || outcome.Kind != "host_task_outcome" || outcome.Confidence != "host_validated_terminal_receipt" ||
		outcome.ProjectID != s.projectID || outcome.Workspace != s.workspace || outcome.WorkspaceID != s.appraisals.facts.workspaceID || outcome.AgentID != s.agentID || outcome.TaskID != row.TaskID || outcome.ChildRunID != childRunID ||
		outcome.ParentRunID == "" || outcome.Status != row.HostStatus || outcome.Validation != row.HostValidation || outcome.ReportRef != "multiagent_report.json" || !validFreewillFactStatus(outcome.Status) || !validFreewillFactValidation(outcome.Validation) || !validFreewillFactSHA256(outcome.ReportSHA256) || len(outcome.ParentEventRefs) == 0 {
		return freewillGoalEvidence{}, errors.New("freewill: terminal outcome receipt fields are incomplete or invalid")
	}
	eventType := event.EventFreewillExecutionCompleted
	if outcome.Status == "execution_failed" {
		eventType = event.EventFreewillExecutionFailed
	}
	if outcome.Status == "execution_cancelled" {
		eventType = event.EventFreewillExecutionCancelled
	}
	observed, err := s.appraisals.facts.store.Events.Query(ctx, event.EventFilter{RunID: outcome.ParentRunID, Type: eventType, Limit: 100})
	if err != nil {
		return freewillGoalEvidence{}, err
	}
	matched := map[string]bool{}
	for _, item := range observed {
		if item.ID != "" && item.Type == eventType && item.Payload["child_run_id"] == childRunID && item.Payload["report_ref"] == outcome.ReportRef && strings.EqualFold(strings.TrimSpace(toFreewillString(item.Payload["report_sha256"])), outcome.ReportSHA256) && item.Payload["validation_summary"] == outcome.Validation {
			matched[item.ID] = true
		}
	}
	for _, id := range outcome.ParentEventRefs {
		if id == "" || !matched[id] {
			return freewillGoalEvidence{}, errors.New("freewill: terminal outcome event reference does not match the durable parent receipt")
		}
	}
	if len(outcome.ParentEventRefs) == 0 {
		return freewillGoalEvidence{}, errors.New("freewill: terminal parent receipt event is missing")
	}
	row.ReportSHA256 = strings.ToLower(outcome.ReportSHA256)
	row.ReceiptEventID = outcome.ParentEventRefs[0]
	return row, nil
}

func (s *freewillGoalProposalSink) CheckIn(ctx context.Context, ownerID, projectID, agentID, proposalID string, goalRevision int, goalID, requestID, childRunID, selfReport string) (api.FreewillGoalRecord, error) {
	if s == nil || ownerID != s.ownerID || projectID != s.projectID || agentID != s.agentID || proposalID == "" ||
		goalRevision < 1 || goalID == "" || requestID == "" || len(requestID) > 128 || strings.TrimSpace(requestID) != requestID ||
		childRunID == "" || len(childRunID) > 128 || strings.TrimSpace(childRunID) != childRunID || strings.ContainsAny(childRunID, `/\\`) ||
		len(selfReport) > 320 || strings.TrimSpace(selfReport) != selfReport {
		return api.FreewillGoalRecord{}, errors.New("freewill: progress check-in identity or scope is invalid")
	}
	claim, err := s.appraisals.facts.acquireCaptureClaim(ctx, freewillBindingDigest("freewill-goal-progress/v1", ownerID, projectID, s.workspace, agentID, proposalID, goalID))
	if err != nil {
		return api.FreewillGoalRecord{}, err
	}
	defer claim.Release()
	goal, err := s.readProposal(ctx, proposalID)
	if err != nil || goal.Decision == nil || goal.Decision.Decision != "accept" || goal.Goal == nil || goal.Goal.GoalID != goalID {
		return api.FreewillGoalRecord{}, errors.New("freewill: progress check-in requires an explicitly accepted goal")
	}
	entry, err := s.readCheckInTaskEvidence(ctx, childRunID)
	if err != nil {
		return api.FreewillGoalRecord{}, errors.New("freewill: exact governed terminal receipt and linked self-report are required")
	}
	if entry.TaskID != freewillTaskScopeID(projectID, s.workspace, childRunID) || entry.OutcomeID == "" || entry.SelfReportID == "" {
		return api.FreewillGoalRecord{}, errors.New("freewill: task source references do not match exact child run")
	}
	all, err := s.appraisals.facts.store.Local.ListRecent(ctx, 0)
	if err != nil {
		return api.FreewillGoalRecord{}, err
	}
	var prior *freewillGoalReviewPayload
	var priorEntryID string
	var replay *memory.Memory
	var replayPayload *freewillGoalProgressPayload
	reviewsByProgress := map[string]bool{}
	for i := range all {
		m := all[i]
		if m.UserID != ownerID || m.ProjectID != projectID || m.AgentID != agentID || m.TaskID != freewillGoalScopeTaskID(projectID, agentID) {
			continue
		}
		if m.Source == freewillGoalProgressSource && m.Category == "goal_progress" {
			var p freewillGoalProgressPayload
			if json.Unmarshal([]byte(m.Content), &p) != nil || p.Version != 1 || p.Kind != "owner_goal_progress_check_in" || p.OwnerID != ownerID || p.ProjectID != projectID || p.Workspace != s.workspace || p.WorkspaceID != s.appraisals.facts.workspaceID || p.AgentID != agentID || p.ProposalID != proposalID || p.GoalID != goalID || p.GoalRevision < 1 || p.GoalRevision > goal.Goal.Revision || p.RequestID == "" || p.ChildRunID == "" || p.ReceiptRef == "" || p.ReceiptEventID == "" || !validFreewillFactSHA256(p.ReportSHA256) || p.SelfReportRef == "" {
				return api.FreewillGoalRecord{}, errors.New("freewill: stored progress provenance is invalid")
			}
			activeRevision := false
			for _, revision := range goal.GoalHistory {
				if revision.Revision == p.GoalRevision && revision.State == "active" {
					activeRevision = true
				}
			}
			if !activeRevision {
				return api.FreewillGoalRecord{}, errors.New("freewill: progress references a non-active lifecycle revision")
			}
			if p.RequestID == requestID {
				if p.GoalRevision != goalRevision || p.ChildRunID != childRunID || p.TentativeSelfReport != selfReport {
					return api.FreewillGoalRecord{}, errors.New("freewill: progress request replay conflicts with prior receipt or report")
				}
				copy := m
				replay = &copy
				stored := p
				replayPayload = &stored
			}
		}
		if m.Source == freewillGoalReviewSource && m.Category == "goal_relevance_review" {
			var r freewillGoalReviewPayload
			if json.Unmarshal([]byte(m.Content), &r) != nil || r.Version != 1 || r.Kind != "bounded_local_goal_relevance_review" || r.OwnerID != ownerID || r.ProjectID != projectID || r.WorkspaceID != s.appraisals.facts.workspaceID || r.AgentID != agentID || r.ProposalID != proposalID {
				return api.FreewillGoalRecord{}, errors.New("freewill: stored relevance review provenance is invalid")
			}
			if prior == nil || r.RecordedAt > prior.RecordedAt {
				copy := r
				prior = &copy
				priorEntryID = m.ID
			}
			if r.ProgressRecordID != "" {
				reviewsByProgress[r.ProgressRecordID] = true
			}
		}
	}
	if replay != nil {
		if replayPayload.ReceiptRef != entry.OutcomeID || replayPayload.SelfReportRef != entry.SelfReportID || replayPayload.ReceiptEventID != entry.ReceiptEventID || replayPayload.ReportSHA256 != entry.ReportSHA256 {
			return api.FreewillGoalRecord{}, errors.New("freewill: progress replay receipt or self-report reference changed")
		}
		if !reviewsByProgress[replay.ID] {
			if err := s.captureGoalProgressReview(ctx, goal, *replayPayload, replay.ID, replayPayload.PreviousReviewID); err != nil {
				return api.FreewillGoalRecord{}, err
			}
		}
		return s.readProposal(ctx, proposalID)
	}
	if goal.Goal.State != "active" || goal.Goal.Revision != goalRevision {
		return api.FreewillGoalRecord{}, errors.New("freewill: progress check-in requires the exact current active goal revision")
	}
	if prior != nil && prior.GoalID != goalID {
		return api.FreewillGoalRecord{}, errors.New("freewill: prior review is tied to another goal")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	payload := freewillGoalProgressPayload{Version: 1, Kind: "owner_goal_progress_check_in", OwnerID: ownerID, ProjectID: projectID, Workspace: s.workspace, WorkspaceID: s.appraisals.facts.workspaceID, AgentID: agentID, ProposalID: proposalID, GoalID: goalID, GoalRevision: goalRevision, RequestID: requestID, ChildRunID: childRunID, ReceiptRef: entry.OutcomeID, ReceiptEventID: entry.ReceiptEventID, ReportSHA256: entry.ReportSHA256, SelfReportRef: entry.SelfReportID, PreviousReviewID: priorEntryID, HostStatus: entry.HostStatus, HostValidation: entry.HostValidation, TentativeSelfReport: selfReport, RecordedAt: now}
	input, err := json.Marshal(freewillGoalRelevanceInput{GoalHypothesis: goal.Proposal.Hypothesis, HostStatus: entry.HostStatus, HostValidation: entry.HostValidation, TaskSelfReportLabel: entry.AppraisalKind, OwnerTentativeReport: selfReport, SourceIDs: []string{entry.OutcomeID, entry.SelfReportID, entry.ReceiptEventID}})
	if err != nil || len(input) > 1800 {
		return api.FreewillGoalRecord{}, errors.New("freewill: bounded goal relevance input is invalid")
	}
	if s.relevanceReviewer == nil {
		return api.FreewillGoalRecord{}, errors.New("freewill: bounded relevance reviewer is unavailable")
	}
	outputText, err := s.relevanceReviewer.ReviewGoalRelevance(ctx, string(input))
	if err != nil || len(outputText) == 0 || len(outputText) > 800 {
		return api.FreewillGoalRecord{}, errors.New("freewill: bounded relevance review output is invalid")
	}
	var output freewillGoalRelevanceOutput
	if json.Unmarshal([]byte(outputText), &output) != nil || (output.Classification != "relevant" && output.Classification != "uncertain" && output.Classification != "no_longer_relevant") || strings.TrimSpace(output.Reason) == "" || len(output.Reason) > 256 || len(output.SourceIDs) == 0 || len(output.SourceIDs) > 3 {
		return api.FreewillGoalRecord{}, errors.New("freewill: bounded relevance review fields are invalid")
	}
	allowedRefs := map[string]bool{entry.OutcomeID: true, entry.SelfReportID: true, entry.ReceiptEventID: true}
	seenRefs := map[string]bool{}
	for _, ref := range output.SourceIDs {
		if !allowedRefs[ref] || seenRefs[ref] {
			return api.FreewillGoalRecord{}, errors.New("freewill: relevance reviewer cited an unavailable source")
		}
		seenRefs[ref] = true
	}
	payload.ReceiptEventID, payload.ReportSHA256 = entry.ReceiptEventID, entry.ReportSHA256
	payload.RelevanceClass, payload.RelevanceReason, payload.RelevanceSourceIDs = output.Classification, output.Reason, append([]string(nil), output.SourceIDs...)
	b, err := json.Marshal(payload)
	if err != nil {
		return api.FreewillGoalRecord{}, err
	}
	progressEntry, _, err := s.appraisals.facts.store.Capture(ctx, memory.CaptureRequest{Scope: s.scope(proposalID), Content: string(b), Summary: "Owner Freewill goal progress check-in", Category: "goal_progress", Tier: "persist", Source: freewillGoalProgressSource, CaptureKey: "freewill-goal-progress/v1:" + requestID})
	if err != nil {
		return api.FreewillGoalRecord{}, err
	}
	if err := s.captureGoalProgressReview(ctx, goal, payload, progressEntry.ID, priorEntryID); err != nil {
		return api.FreewillGoalRecord{}, err
	}
	return s.readProposal(ctx, proposalID)
}

func (s *freewillGoalProposalSink) captureGoalProgressReview(ctx context.Context, goal api.FreewillGoalRecord, p freewillGoalProgressPayload, progressID, previousReviewID string) error {
	// Materialize the already host-validated local fake assessment. Keeping it in
	// the progress row makes a failed second append recoverable without re-review.
	if p.RelevanceClass == "" || p.RelevanceReason == "" || len(p.RelevanceSourceIDs) == 0 {
		return errors.New("freewill: progress record lacks its relevance hypothesis")
	}
	review := freewillGoalReviewPayload{Version: 1, Kind: "bounded_local_goal_relevance_review", OwnerID: p.OwnerID, ProjectID: p.ProjectID, WorkspaceID: p.WorkspaceID, AgentID: p.AgentID, ProposalID: p.ProposalID, GoalID: p.GoalID, GoalRevision: p.GoalRevision, ProgressRecordID: progressID, PreviousReviewID: previousReviewID, Classification: p.RelevanceClass, Reason: p.RelevanceReason, Provider: "local-fake", Model: "bounded-relevance-reviewer-v1", Confidence: "tentative_model_hypothesis", SourceIDs: append([]string(nil), p.RelevanceSourceIDs...), RecordedAt: p.RecordedAt}
	b, err := json.Marshal(review)
	if err != nil {
		return err
	}
	_, _, err = s.appraisals.facts.store.Capture(ctx, memory.CaptureRequest{Scope: s.scope(goal.Proposal.ProposalID), Content: string(b), Summary: "Bounded local Freewill goal relevance review", Category: "goal_relevance_review", Tier: "persist", Source: freewillGoalReviewSource, CaptureKey: "freewill-goal-review/v1:" + progressID})
	return err
}

func (s *freewillGoalProposalSink) readGoalProgress(ctx context.Context, proposalID string, lifecycle []api.FreewillGoalStateRevision) ([]api.FreewillGoalProgress, []api.FreewillGoalRelevanceReview, error) {
	all, err := s.appraisals.facts.store.Local.ListRecent(ctx, 0)
	if err != nil {
		return nil, nil, err
	}
	var progresses []api.FreewillGoalProgress
	var reviews []api.FreewillGoalRelevanceReview
	progressRows := map[string]freewillGoalProgressPayload{}
	type reviewRow struct {
		id      string
		payload freewillGoalReviewPayload
	}
	var reviewRows []reviewRow
	for _, m := range all {
		if m.UserID != s.ownerID || m.ProjectID != s.projectID || m.AgentID != s.agentID || m.TaskID != freewillGoalScopeTaskID(s.projectID, s.agentID) {
			continue
		}
		switch m.Source {
		case freewillGoalProgressSource:
			if m.Category != "goal_progress" {
				continue
			}
			var p freewillGoalProgressPayload
			if json.Unmarshal([]byte(m.Content), &p) != nil || p.Version != 1 || p.Kind != "owner_goal_progress_check_in" || p.OwnerID != s.ownerID || p.ProjectID != s.projectID || p.Workspace != s.workspace || p.WorkspaceID != s.appraisals.facts.workspaceID || p.AgentID != s.agentID || p.ProposalID != proposalID || p.GoalID == "" || p.GoalRevision < 1 || p.RequestID == "" || p.ChildRunID == "" || p.ReceiptRef == "" || p.ReceiptEventID == "" || p.SelfReportRef == "" || !validFreewillFactSHA256(p.ReportSHA256) || p.RelevanceClass == "" || p.RelevanceReason == "" || len(p.RelevanceSourceIDs) == 0 {
				return nil, nil, errors.New("freewill: invalid progress history")
			}
			activeRevision := false
			for _, rev := range lifecycle {
				if rev.Revision == p.GoalRevision && rev.State == "active" {
					activeRevision = true
				}
			}
			if !activeRevision {
				return nil, nil, errors.New("freewill: progress history references a non-active lifecycle revision")
			}
			allowed := map[string]bool{p.ReceiptRef: true, p.SelfReportRef: true, p.ReceiptEventID: true}
			seen := map[string]bool{}
			if p.RelevanceClass != "relevant" && p.RelevanceClass != "uncertain" && p.RelevanceClass != "no_longer_relevant" || len(p.RelevanceReason) > 256 || len(p.RelevanceSourceIDs) > 3 {
				return nil, nil, errors.New("freewill: stored relevance hypothesis is invalid")
			}
			for _, ref := range p.RelevanceSourceIDs {
				if !allowed[ref] || seen[ref] {
					return nil, nil, errors.New("freewill: stored relevance hypothesis cites an invalid source")
				}
				seen[ref] = true
			}
			progressRows[m.ID] = p
			progresses = append(progresses, api.FreewillGoalProgress{RecordID: m.ID, RequestID: p.RequestID, GoalID: p.GoalID, GoalRevision: p.GoalRevision, ChildRunID: p.ChildRunID, ReceiptRef: p.ReceiptRef, ReceiptEventID: p.ReceiptEventID, ReportSHA256: p.ReportSHA256, SelfReportRef: p.SelfReportRef, OwnerSelfReport: p.TentativeSelfReport, RecordedAt: p.RecordedAt})
		case freewillGoalReviewSource:
			if m.Category != "goal_relevance_review" {
				continue
			}
			var r freewillGoalReviewPayload
			if json.Unmarshal([]byte(m.Content), &r) != nil || r.Version != 1 || r.Kind != "bounded_local_goal_relevance_review" || r.OwnerID != s.ownerID || r.ProjectID != s.projectID || r.WorkspaceID != s.appraisals.facts.workspaceID || r.AgentID != s.agentID || r.ProposalID != proposalID {
				return nil, nil, errors.New("freewill: invalid relevance history")
			}
			reviews = append(reviews, api.FreewillGoalRelevanceReview{RecordID: m.ID, ProgressRecordID: r.ProgressRecordID, PreviousReviewID: r.PreviousReviewID, Classification: r.Classification, Reason: r.Reason, Provider: r.Provider, Model: r.Model, Confidence: r.Confidence, SourceIDs: append([]string(nil), r.SourceIDs...), RecordedAt: r.RecordedAt})
			reviewRows = append(reviewRows, reviewRow{id: m.ID, payload: r})
		}
	}
	sort.Slice(progresses, func(i, j int) bool { return progresses[i].RecordedAt < progresses[j].RecordedAt })
	sort.Slice(reviews, func(i, j int) bool { return reviews[i].RecordedAt < reviews[j].RecordedAt })
	sort.Slice(reviewRows, func(i, j int) bool { return reviewRows[i].payload.RecordedAt < reviewRows[j].payload.RecordedAt })
	seenProgressReviews := map[string]bool{}
	previousID := ""
	for _, row := range reviewRows {
		r := row.payload
		p, ok := progressRows[r.ProgressRecordID]
		if !ok || seenProgressReviews[r.ProgressRecordID] || r.GoalID != p.GoalID || r.GoalRevision != p.GoalRevision || r.Provider != "local-fake" || r.Model != "bounded-relevance-reviewer-v1" || r.Confidence != "tentative_model_hypothesis" || r.Classification != p.RelevanceClass || r.Reason != p.RelevanceReason || r.PreviousReviewID != previousID || len(r.SourceIDs) == 0 || len(r.SourceIDs) > 3 {
			return nil, nil, errors.New("freewill: relevance review is unlinked or invalid")
		}
		allowed := map[string]bool{p.ReceiptRef: true, p.SelfReportRef: true, p.ReceiptEventID: true}
		seen := map[string]bool{}
		for _, ref := range r.SourceIDs {
			if !allowed[ref] || seen[ref] {
				return nil, nil, errors.New("freewill: stored review cites an invalid source")
			}
			seen[ref] = true
		}
		seenProgressReviews[r.ProgressRecordID] = true
		previousID = row.id
	}
	return progresses, reviews, nil
}
