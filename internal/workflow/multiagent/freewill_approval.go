package multiagent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/policy"
)

var ErrFreewillApprovalStale = errors.New("multiagent: Freewill approval no longer matches the current request")

type FreewillPolicyEvaluator interface {
	Evaluate(policy.PolicyRequest) policy.PolicyDecision
}

type FreewillApprovalStore interface {
	Load(string, string) (*approval.Approval, error)
	List(string) ([]*approval.Approval, error)
	Save(*approval.Approval) error
}

// FreewillApprovalService evaluates configured policy and stores a human
// decision for the selected intention. It has no task, tool, or executor.
type FreewillApprovalService struct {
	runtime   *DurableRuntime
	store     DurableRunStore
	approvals FreewillApprovalStore
	policy    FreewillPolicyEvaluator
	actor     string
	readOnly  bool
}

type FreewillApprovalStatus struct {
	RunID                 string                    `json:"run_id"`
	RequestID             string                    `json:"request_id,omitempty"`
	Lifecycle             freewill.State            `json:"lifecycle"`
	ChoiceID              string                    `json:"choice_id,omitempty"`
	Selected              *FreewillPublicCandidate  `json:"selected,omitempty"`
	Alternatives          []FreewillPublicCandidate `json:"alternatives,omitempty"`
	ReasonSummary         string                    `json:"reason_summary,omitempty"`
	Provider              string                    `json:"provider,omitempty"`
	Model                 string                    `json:"model,omitempty"`
	Usage                 cost.TokenUsage           `json:"usage,omitempty"`
	ApprovalID            string                    `json:"approval_id,omitempty"`
	ApprovalStatus        string                    `json:"approval_status,omitempty"`
	RequestFingerprint    string                    `json:"request_fingerprint,omitempty"`
	PolicyDecision        string                    `json:"policy_decision,omitempty"`
	ExecutionDisabled     bool                      `json:"execution_disabled"`
	PauseReason           string                    `json:"pause_reason,omitempty"`
	SelectionRecoveries   int                       `json:"selection_recoveries,omitempty"`
	SelectionFailureClass string                    `json:"selection_failure_class,omitempty"`
	SelectionFailureCode  string                    `json:"selection_failure_code,omitempty"`
	SelectionNextAt       time.Time                 `json:"selection_next_at,omitempty"`
}

type FreewillPublicCandidate struct {
	CandidateID    string                   `json:"candidate_id"`
	Title          string                   `json:"title"`
	Goal           string                   `json:"goal"`
	Reason         string                   `json:"reason_summary"`
	Source         freewill.CandidateSource `json:"candidate_source"`
	Scope          freewill.Scope           `json:"scope"`
	Capabilities   []string                 `json:"requested_capabilities"`
	Budget         freewill.ResourceBudget  `json:"budget"`
	Score          *int                     `json:"score,omitempty"`
	ScoreFactors   []string                 `json:"score_factors,omitempty"`
	ScoreRationale string                   `json:"score_rationale,omitempty"`
}

func (s *FreewillApprovalService) Status(ctx context.Context, runID string) (FreewillApprovalStatus, error) {
	record, err := s.store.Load(ctx, runID)
	if err != nil {
		return FreewillApprovalStatus{}, err
	}
	return s.statusFromRecord(ctx, record)
}

func (s *FreewillApprovalService) DecideStatus(ctx context.Context, runID, approvalID, decision, actor, reason string) (FreewillApprovalStatus, error) {
	record, err := s.Decide(ctx, runID, approvalID, decision, actor, reason)
	if err != nil {
		return FreewillApprovalStatus{}, err
	}
	return s.statusFromRecord(ctx, record)
}

func (s *FreewillApprovalService) statusFromRecord(ctx context.Context, record DurableRun) (FreewillApprovalStatus, error) {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil {
		return FreewillApprovalStatus{}, errors.New("multiagent: run is not a Freewill intention")
	}
	status := FreewillApprovalStatus{RunID: record.State.RunID, Lifecycle: record.Freewill.Lifecycle, ChoiceID: record.Freewill.ChoiceID,
		ApprovalID: record.Freewill.ApprovalRef, RequestFingerprint: record.Freewill.ApprovalFingerprint,
		PolicyDecision: record.Freewill.PolicyDecision, ExecutionDisabled: record.Freewill.ExecutionDisabled, PauseReason: record.Freewill.PauseReason,
		ReasonSummary: record.Freewill.ReasonSummary, Provider: record.Freewill.SelectionProvider, Model: record.Freewill.SelectionModel,
		Usage: record.Freewill.SelectionUsage, SelectionRecoveries: record.Freewill.SelectionRecoveryNumber,
		SelectionFailureClass: record.Freewill.SelectionFailureClass, SelectionFailureCode: record.Freewill.SelectionFailureCode,
		SelectionNextAt: record.Freewill.SelectionNextAt}
	for _, candidate := range record.Freewill.Candidates {
		public := publicFreewillCandidate(candidate)
		if candidate.CandidateID == record.Freewill.ChoiceID {
			status.RequestID = candidate.Request.RequestID
			status.Selected = &public
		} else {
			status.Alternatives = append(status.Alternatives, public)
		}
	}
	if status.ApprovalID != "" {
		item, err := s.approvals.Load(record.State.RunID, status.ApprovalID)
		if err != nil {
			return FreewillApprovalStatus{}, err
		}
		if item.RunID != record.State.RunID || item.ApprovalID != status.ApprovalID || item.SubjectType != approval.SubjectIntention || item.Intention == nil || item.Intention.RequestFingerprint != record.Freewill.ApprovalFingerprint {
			return FreewillApprovalStatus{}, ErrFreewillApprovalStale
		}
		control, ok := s.store.(FreewillOperatorControlStore)
		if !ok {
			return FreewillApprovalStatus{}, ErrFreewillControlUnavailable
		}
		controlState, err := control.LoadFreewillOperatorControl(ctx, record.State.RequestIdentity.RequesterID, record.Freewill.Scope)
		if err != nil {
			return FreewillApprovalStatus{}, err
		}
		if item.Intention.ControlGeneration != record.Freewill.ApprovalGeneration || controlState.Generation != record.Freewill.ApprovalGeneration {
			status.ApprovalStatus = "revoked"
			status.Lifecycle = freewill.StateHalted
			status.PauseReason = "operator_revoked"
			status.ExecutionDisabled = true
			return status, nil
		}
		status.ApprovalStatus = item.Status
	}
	return status, nil
}

func publicFreewillCandidate(candidate freewill.CandidateIntention) FreewillPublicCandidate {
	var score *int
	if candidate.Score >= 0 && candidate.Score <= 100 {
		value := candidate.Score
		score = &value
	}
	factors := make([]string, 0, len(candidate.ScoreFactors))
	allowed := map[string]bool{"scope_fit": true, "feasibility": true, "impact": true, "novelty": true, "readiness": true}
	seen := make(map[string]bool, len(candidate.ScoreFactors))
	for _, factor := range candidate.ScoreFactors {
		if allowed[factor] && !seen[factor] && len(factors) < 5 {
			factors = append(factors, factor)
			seen[factor] = true
		}
	}
	rationale := candidate.ScoreRationale
	if len(rationale) > 500 {
		rationale = rationale[:500]
	}
	lower := strings.ToLower(rationale)
	for _, marker := range []string{"bearer ", "authorization:", "api_key=", "apikey=", "token=", "password=", "secret="} {
		if strings.Contains(lower, marker) {
			rationale = "[REDACTED]"
			break
		}
	}
	return FreewillPublicCandidate{CandidateID: candidate.CandidateID, Title: candidate.Request.Title, Goal: candidate.Request.Goal,
		Reason: candidate.Request.ReasonSummary, Source: candidate.Request.CandidateSource, Scope: candidate.Request.Scope,
		Capabilities: append([]string(nil), candidate.Request.RequestedCapabilities...), Budget: candidate.Request.Budget,
		Score: score, ScoreFactors: factors, ScoreRationale: rationale}
}

func NewFreewillApprovalService(runtime *DurableRuntime, store DurableRunStore, approvals FreewillApprovalStore, evaluator FreewillPolicyEvaluator, policyActor string) (*FreewillApprovalService, error) {
	if runtime == nil || store == nil || approvals == nil || evaluator == nil || strings.TrimSpace(policyActor) == "" {
		return nil, errors.New("multiagent: Freewill approval requires runtime, run store, approval store, policy, and actor")
	}
	return &FreewillApprovalService{runtime: runtime, store: store, approvals: approvals, policy: evaluator, actor: policyActor}, nil
}

// NewFreewillApprovalStatusReader composes the existing status projection over
// an existing read-only run store. It cannot make or reconcile decisions.
func NewFreewillApprovalStatusReader(store DurableRunStore, approvals FreewillApprovalStore) (*FreewillApprovalService, error) {
	if store == nil || approvals == nil {
		return nil, errors.New("multiagent: Freewill status reader requires run and approval stores")
	}
	return &FreewillApprovalService{store: store, approvals: approvals, readOnly: true}, nil
}

// RequestApproval evaluates the existing policy engine before creating an
// intention-specific approval. Repeated calls reuse the same exact subject.
func (s *FreewillApprovalService) RequestApproval(ctx context.Context, runID string) (DurableRun, error) {
	if s.readOnly {
		return DurableRun{}, errors.New("multiagent: Freewill status reader is read-only")
	}
	claim, err := s.runtime.claimer.Acquire(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := s.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	if record.Kind == DurableRunKindFreewill && record.Freewill != nil {
		if err := ensureFreewillNotHalted(ctx, s.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
			return record, err
		}
		control, ok := s.store.(FreewillOperatorControlStore)
		if !ok {
			return record, ErrFreewillControlUnavailable
		}
		controlState, err := control.LoadFreewillOperatorControl(ctx, record.State.RequestIdentity.RequesterID, record.Freewill.Scope)
		if err != nil {
			return record, err
		}
		record.Freewill.ApprovalGeneration = controlState.Generation
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.Freewill.Lifecycle != freewill.StateProposed || record.Freewill.ChoiceID == "" {
		return record, errors.New("multiagent: Freewill run has no proposed choice")
	}
	candidate, ok := selectedFreewillCandidate(record.Freewill)
	if !ok {
		return record, errors.New("multiagent: selected Freewill candidate is missing")
	}
	if err := validateFreewillApprovalCandidate(record, candidate); err != nil {
		return record, err
	}
	fingerprint, canonicalRequest := freewillRequestFingerprint(candidate.Request)
	decision := s.policy.Evaluate(policy.PolicyRequest{
		Subject:  policy.Subject{Type: "agent", ID: record.State.RequestIdentity.RequesterID},
		Action:   "intention.propose",
		Resource: policy.Resource{Type: "intention", Name: "freewill_candidate"},
		Context:  policy.Context{Project: record.Freewill.Scope.ProjectID, Mode: "proposal"},
	})
	if decision.Decision == policy.DecisionDenied {
		record.Freewill.Lifecycle = freewill.StateDenied
		record.Freewill.PolicyDecision = string(decision.Decision)
		record.Freewill.PolicyRuleID = safePolicyRuleID(decision.RuleID)
		record.Freewill.ExecutionDisabled = true
		record.Freewill.PauseReason = "policy_denied"
		return s.saveFreewillDecision(ctx, record, event.EventFreewillPolicyDenied, map[string]any{
			"request_id": canonicalRequest.RequestID, "policy_rule_id": record.Freewill.PolicyRuleID,
			"reason_code": "policy_denied", "execution_disabled": true,
		})
	}
	if decision.Decision != policy.DecisionAllowed && decision.Decision != policy.DecisionRequiresApproval {
		return record, errors.New("multiagent: Freewill policy outcome is unsupported")
	}
	ref := approval.IntentionApprovalRef{RequestID: canonicalRequest.RequestID, RequestFingerprint: fingerprint, ControlGeneration: record.Freewill.ApprovalGeneration,
		ScopeSummary: "project:" + record.Freewill.Scope.ProjectID, Capabilities: canonicalRequest.RequestedCapabilities}
	item, err := s.findOrCreateApproval(record, ref)
	if err != nil {
		return record, err
	}
	if err := item.ValidateSubject(); err != nil || item.RunID != runID || item.ApprovalID == "" ||
		item.SubjectType != approval.SubjectIntention || item.Intention == nil || item.Intention.RequestID != canonicalRequest.RequestID ||
		item.Intention.RequestFingerprint != fingerprint || item.Intention.ControlGeneration != record.Freewill.ApprovalGeneration {
		return record, ErrFreewillApprovalStale
	}
	if item.Status == approval.StatusApproved {
		record.Freewill.Lifecycle = freewill.StatePaused
		record.Freewill.PauseReason = "execution_disabled"
	} else if item.Status == approval.StatusDenied {
		record.Freewill.Lifecycle = freewill.StateDenied
		record.Freewill.PauseReason = "human_denied"
	} else if item.Status == approval.StatusPending {
		record.Freewill.Lifecycle = freewill.StateApprovalPending
		record.Freewill.PauseReason = "awaiting_human_decision"
	} else {
		return record, ErrFreewillApprovalStale
	}
	record.Freewill.ApprovalRef = item.ApprovalID
	record.Freewill.ApprovalFingerprint = fingerprint
	approvalDecision, ok := freewillApprovalDecision(item.Status)
	if !ok {
		return record, ErrFreewillApprovalStale
	}
	record.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: item.ApprovalID, RequestFingerprint: fingerprint, Decision: approvalDecision, ControlGeneration: record.Freewill.ApprovalGeneration}
	record.Freewill.PolicyDecision = string(decision.Decision)
	record.Freewill.PolicyRuleID = safePolicyRuleID(decision.RuleID)
	record.Freewill.ExecutionDisabled = true
	return s.saveFreewillDecision(ctx, record, event.EventFreewillApprovalProposed, map[string]any{
		"request_id": canonicalRequest.RequestID, "approval_id": item.ApprovalID, "approval_status": item.Status,
		"request_fingerprint": fingerprint, "execution_disabled": true,
	})
}

// RecoverApprovalDecision closes the crash window where the existing approval
// store durably saved a human decision but the workflow checkpoint did not.
// It reads the same immutable approval subject and only mirrors that decision;
// it never creates, overwrites, or executes an approval.
func (s *FreewillApprovalService) RecoverApprovalDecision(ctx context.Context, runID string) (DurableRun, error) {
	if s.readOnly {
		return DurableRun{}, errors.New("multiagent: Freewill status reader is read-only")
	}
	claim, err := s.runtime.claimer.Acquire(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := s.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	if record.Kind == DurableRunKindFreewill && record.Freewill != nil {
		if err := ensureFreewillNotHalted(ctx, s.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
			return record, err
		}
		if err := ensureFreewillApprovalGeneration(ctx, s.store, record); err != nil {
			return record, err
		}
	}
	legacyParked := record.Kind == DurableRunKindFreewill && record.Freewill != nil && record.Freewill.Lifecycle == freewill.StatePaused && record.Freewill.PauseReason == "execution_disabled" && record.Freewill.ApprovalRef != "" && record.Freewill.ApprovalReceipt == nil
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || (record.Freewill.Lifecycle != freewill.StateApprovalPending && !legacyParked) || record.Freewill.ApprovalRef == "" {
		return record, nil
	}
	item, err := s.approvals.Load(runID, record.Freewill.ApprovalRef)
	if err != nil {
		return record, err
	}
	candidate, ok := selectedFreewillCandidate(record.Freewill)
	if !ok {
		return record, ErrFreewillApprovalStale
	}
	if err := validateFreewillApprovalCandidate(record, candidate); err != nil {
		return record, err
	}
	fingerprint, _ := freewillRequestFingerprint(candidate.Request)
	if err := item.ValidateSubject(); err != nil || item.RunID != runID || item.ApprovalID != record.Freewill.ApprovalRef ||
		item.SubjectType != approval.SubjectIntention || item.Intention == nil || item.Intention.RequestID != candidate.Request.RequestID ||
		item.Intention.RequestFingerprint != fingerprint || item.Intention.ControlGeneration != record.Freewill.ApprovalGeneration || fingerprint != record.Freewill.ApprovalFingerprint {
		return record, ErrFreewillApprovalStale
	}
	if item.Status == approval.StatusPending {
		if !legacyParked {
			return record, nil
		}
		record.Freewill.Lifecycle = freewill.StateApprovalPending
		record.Freewill.PauseReason = "awaiting_human_decision"
		record.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: item.ApprovalID, RequestFingerprint: fingerprint, Decision: FreewillApprovalPending, ControlGeneration: record.Freewill.ApprovalGeneration}
		record.Freewill.ExecutionDisabled = true
		return s.saveFreewillDecision(ctx, record, event.EventFreewillApprovalReconciled, map[string]any{
			"request_id": candidate.Request.RequestID, "approval_id": item.ApprovalID, "decision": item.Status,
			"lifecycle": string(record.Freewill.Lifecycle), "execution_disabled": true, "reason_code": "legacy_approval_reconciled",
		})
	}
	recoveryEvent := event.EventFreewillApprovalDecided
	if legacyParked {
		recoveryEvent = event.EventFreewillApprovalReconciled
	}
	if item.Status == approval.StatusApproved {
		current := s.policy.Evaluate(policy.PolicyRequest{Subject: policy.Subject{Type: "agent", ID: record.State.RequestIdentity.RequesterID},
			Action: "intention.propose", Resource: policy.Resource{Type: "intention", Name: "freewill_candidate"},
			Context: policy.Context{Project: record.Freewill.Scope.ProjectID, Mode: "proposal"}})
		if current.Decision != policy.DecisionAllowed && current.Decision != policy.DecisionRequiresApproval {
			record.Freewill.Lifecycle = freewill.StateDenied
			record.Freewill.PauseReason = "policy_denied"
			record.Freewill.ApprovalReceipt = nil
			record.Freewill.PolicyDecision = string(current.Decision)
		} else {
			record.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: item.ApprovalID, RequestFingerprint: fingerprint, Decision: FreewillApprovalGranted, ControlGeneration: record.Freewill.ApprovalGeneration}
			record.Freewill.Lifecycle = freewill.StatePaused
			record.Freewill.PauseReason = "execution_disabled"
		}
	} else if item.Status == approval.StatusDenied {
		record.Freewill.Lifecycle = freewill.StateDenied
		record.Freewill.PauseReason = "human_denied"
		record.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: item.ApprovalID, RequestFingerprint: fingerprint, Decision: FreewillApprovalDenied, ControlGeneration: record.Freewill.ApprovalGeneration}
	} else {
		return record, ErrFreewillApprovalStale
	}
	record.Freewill.ExecutionDisabled = true
	return s.saveFreewillDecision(ctx, record, recoveryEvent, map[string]any{
		"request_id": candidate.Request.RequestID, "approval_id": item.ApprovalID, "decision": item.Status,
		"lifecycle": string(record.Freewill.Lifecycle), "execution_disabled": true,
	})
}

// Decide records a human grant or denial. A grant only parks the request in a
// visibly paused, execution-disabled state; it never resumes or dispatches it.
func (s *FreewillApprovalService) Decide(ctx context.Context, runID, approvalID, decision, actor, reason string) (DurableRun, error) {
	if s.readOnly {
		return DurableRun{}, errors.New("multiagent: Freewill status reader is read-only")
	}
	if strings.TrimSpace(actor) == "" || (decision != "grant" && decision != "deny") {
		return DurableRun{}, errors.New("multiagent: Freewill decision is invalid")
	}
	claim, err := s.runtime.claimer.Acquire(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := s.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	if record.Kind == DurableRunKindFreewill && record.Freewill != nil {
		if err := ensureFreewillNotHalted(ctx, s.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
			return record, err
		}
		if err := ensureFreewillApprovalGeneration(ctx, s.store, record); err != nil {
			return record, err
		}
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.Freewill.Lifecycle != freewill.StateApprovalPending || record.Freewill.ApprovalRef != approvalID {
		return record, ErrFreewillApprovalStale
	}
	candidate, ok := selectedFreewillCandidate(record.Freewill)
	if !ok {
		return record, ErrFreewillApprovalStale
	}
	if err := validateFreewillApprovalCandidate(record, candidate); err != nil {
		return record, err
	}
	fingerprint, _ := freewillRequestFingerprint(candidate.Request)
	if fingerprint != record.Freewill.ApprovalFingerprint {
		return record, ErrFreewillApprovalStale
	}
	item, err := s.approvals.Load(runID, approvalID)
	if err != nil {
		return record, err
	}
	if err := item.ValidateSubject(); err != nil || item.RunID != runID || item.ApprovalID != approvalID || item.SubjectType != approval.SubjectIntention || item.Intention == nil ||
		item.Intention.RequestID != candidate.Request.RequestID || item.Intention.RequestFingerprint != fingerprint || item.Intention.ControlGeneration != record.Freewill.ApprovalGeneration {
		return record, ErrFreewillApprovalStale
	}
	if decision == "grant" {
		currentPolicy := s.policy.Evaluate(policy.PolicyRequest{
			Subject: policy.Subject{Type: "agent", ID: record.State.RequestIdentity.RequesterID}, Action: "intention.propose",
			Resource: policy.Resource{Type: "intention", Name: "freewill_candidate"},
			Context:  policy.Context{Project: record.Freewill.Scope.ProjectID, Mode: "proposal"},
		})
		if currentPolicy.Decision != policy.DecisionAllowed && currentPolicy.Decision != policy.DecisionRequiresApproval {
			return record, errors.New("multiagent: Freewill policy no longer permits this request")
		}
		if item.Status == approval.StatusPending {
			if err := item.Approve(actor); err != nil {
				return record, err
			}
			if err := s.approvals.Save(item); err != nil {
				return record, err
			}
		} else if item.Status != approval.StatusApproved {
			return record, ErrFreewillApprovalStale
		}
		record.Freewill.Lifecycle = freewill.StatePaused
		record.Freewill.PauseReason = "execution_disabled"
	} else {
		if item.Status == approval.StatusPending {
			if err := item.Deny(actor, safeDecisionReason(reason)); err != nil {
				return record, err
			}
			if err := s.approvals.Save(item); err != nil {
				return record, err
			}
		} else if item.Status != approval.StatusDenied {
			return record, ErrFreewillApprovalStale
		}
		record.Freewill.Lifecycle = freewill.StateDenied
		record.Freewill.PauseReason = "human_denied"
	}
	record.Freewill.ExecutionDisabled = true
	receiptDecision := FreewillApprovalDenied
	if decision == "grant" {
		receiptDecision = FreewillApprovalGranted
	}
	record.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: approvalID, RequestFingerprint: fingerprint, Decision: receiptDecision, ControlGeneration: record.Freewill.ApprovalGeneration}
	evt := event.EventFreewillApprovalDecided
	payload := map[string]any{"request_id": candidate.Request.RequestID, "approval_id": approvalID,
		"decision": decision, "lifecycle": string(record.Freewill.Lifecycle), "execution_disabled": true}
	if record.Freewill.PolicyRuleID != "" {
		payload["policy_rule_id"] = record.Freewill.PolicyRuleID
	}
	return s.saveFreewillDecision(ctx, record, evt, payload)
}

func (s *FreewillApprovalService) findOrCreateApproval(record DurableRun, ref approval.IntentionApprovalRef) (*approval.Approval, error) {
	items, err := s.approvals.List(record.State.RunID)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.SubjectType == approval.SubjectIntention && item.Intention != nil && item.Intention.RequestID == ref.RequestID &&
			item.Intention.RequestFingerprint == ref.RequestFingerprint && item.Intention.ControlGeneration == ref.ControlGeneration {
			return item, nil
		}
	}
	item, err := approval.NewIntentionApproval(record.State.RunID, record.Freewill.CorrelationID,
		record.State.RequestIdentity.RequesterID, record.Freewill.Scope.ProjectID, ref,
		approval.PolicyDecision{Decision: approval.DecisionRequiresApproval, Reason: "policy_evaluated"})
	if err != nil {
		return nil, err
	}
	if err := s.approvals.Save(item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *FreewillApprovalService) saveFreewillDecision(ctx context.Context, record DurableRun, eventType string, payload map[string]any) (DurableRun, error) {
	if err := record.Validate(s.runtime.supervisor.graph); err != nil {
		return record, err
	}
	record.State.UpdatedAt = s.runtime.now().UTC()
	payload["run_id"] = record.State.RunID
	evt := event.NewEvent(eventType, freewillWakeEventSource, payload)
	evt.Timestamp = record.State.UpdatedAt.Format(time.RFC3339Nano)
	evt.CorrelationID = record.Freewill.CorrelationID
	evt.Metadata.RunID = record.State.RunID
	saved, err := s.store.Checkpoint(ctx, record.Revision, record, []event.Event{evt})
	if err != nil {
		return record, err
	}
	if err := s.runtime.dispatchOutbox(ctx, record.State.RunID); err != nil {
		return saved, err
	}
	return saved, nil
}

func freewillApprovalDecision(status string) (FreewillApprovalDecision, bool) {
	switch status {
	case approval.StatusPending:
		return FreewillApprovalPending, true
	case approval.StatusApproved:
		return FreewillApprovalGranted, true
	case approval.StatusDenied:
		return FreewillApprovalDenied, true
	default:
		return "", false
	}
}

// validateFreewillApprovalCandidate re-checks the untrusted selection at the
// policy/approval boundary. Earlier selection validation is not sufficient:
// the durable record may have been edited or recovered independently.
func validateFreewillApprovalCandidate(record DurableRun, candidate freewill.CandidateIntention) error {
	if err := candidate.Validate(); err != nil {
		return ErrFreewillApprovalStale
	}
	if record.Freewill == nil || candidate.Request.AgentID != record.State.RequestIdentity.RequesterID || candidate.Request.Scope != record.Freewill.Scope {
		return ErrFreewillApprovalStale
	}
	return nil
}

func safePolicyRuleID(id string) string {
	if strings.TrimSpace(id) == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n\x00") {
		return "unknown"
	}
	return id
}

func safeDecisionReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "denied by reviewer"
	}
	if len(reason) > 256 {
		reason = reason[:256]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, reason)
}
