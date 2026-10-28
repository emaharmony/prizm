package multiagent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
)

const freewillWakeEventSource = "prizm-freewill-wake"

// maxFreewillSelectionAttempts bounds provider attempts per accepted wake/run.
// It derives from the shared recoverable-failure budget plus the initial call.
const maxFreewillSelectionAttempts = uint64(maxRoleRecoveryRetries + 1)

var ErrFreewillRuntimeUnavailable = errors.New("multiagent: Freewill decision runtime is not configured")
var ErrFreewillWakeNotDue = errors.New("multiagent: Freewill selection retry is not due")
var ErrFreewillActorAdmissionConflict = errors.New("multiagent: Freewill actor admission conflicts with an active intention")
var ErrFreewillActorAdmissionAmbiguous = errors.New("multiagent: Freewill actor admission state is ambiguous")
var ErrFreewillSelectionBudgetExceeded = errors.New("multiagent: Freewill selector attempt budget is exhausted")

// FreewillWakeRequest is a scheduler-owned wake envelope. Trigger is an
// allowlisted source label; it cannot carry task instructions or authority.
type FreewillWakeRequest struct {
	RunID         string
	AgentID       string
	Scope         freewill.Scope
	Trigger       string
	DedupeKey     string
	CorrelationID string
}

// FreewillWakeLease is the persisted claim identity returned to a trusted
// decision worker. A late worker cannot release or commit a newer attempt.
type FreewillWakeLease struct {
	RunID    string
	Owner    string
	Attempt  uint64
	Until    time.Time
	Revision int64
}

func (w FreewillWakeRequest) validate() error {
	for name, value := range map[string]string{
		"run_id": w.RunID, "agent_id": w.AgentID, "dedupe_key": w.DedupeKey,
		"correlation_id": w.CorrelationID,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("multiagent: Freewill wake %s is invalid", name)
		}
	}
	if w.Trigger != "scheduled_tick" {
		return errors.New("multiagent: Freewill wake trigger is not allowed")
	}
	if strings.TrimSpace(w.Scope.ProjectID) == "" || strings.TrimSpace(w.Scope.Workspace) == "" ||
		len(w.Scope.ProjectID) > 256 || len(w.Scope.Workspace) > 256 || strings.ContainsAny(w.Scope.ProjectID+w.Scope.Workspace, "\r\n\x00") {
		return errors.New("multiagent: Freewill wake scope is invalid")
	}
	return nil
}

// AcceptFreewillWake records a debounced wake through the same atomic run,
// idempotency-key, and outbox transaction as ordinary durable work. It does
// not invoke a provider or advance a candidate.
func (r *DurableRuntime) AcceptFreewillWake(ctx context.Context, wake FreewillWakeRequest) (DurableRun, bool, error) {
	if err := wake.validate(); err != nil {
		return DurableRun{}, false, err
	}
	if _, ok := r.store.(FreewillOperatorControlStore); !ok {
		return DurableRun{}, false, ErrFreewillControlUnavailable
	}
	if _, ok := r.store.(IdempotentRunAcceptor); !ok {
		return DurableRun{}, false, ErrFreewillControlUnavailable
	}
	now := r.now().UTC()
	identity := &RequestIdentity{
		RequesterID:    wake.AgentID,
		SessionID:      wake.CorrelationID,
		Source:         "freewill_scheduler",
		IdempotencyKey: wake.DedupeKey,
		AcceptedAt:     now,
	}
	state := RunState{
		SchemaVersion:   RunStateSchemaVersion,
		RunID:           wake.RunID,
		RequestIdentity: identity,
		WorkflowID:      FreewillWorkflowID,
		WorkflowVersion: 1,
		CurrentTask:     TaskReference{ID: "freewill-wake"},
		Status:          RunStatusCreated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	record := DurableRun{
		SchemaVersion: DurableRunSchemaVersion,
		Kind:          DurableRunKindFreewill,
		State:         state,
		Phase:         CheckpointFreewillWake,
		Freewill: &FreewillRunState{
			Lifecycle:             freewill.StateWaking,
			Scope:                 wake.Scope,
			SelectionAttemptLimit: maxFreewillSelectionAttempts,
			WakeID:                wake.RunID,
			Trigger:               wake.Trigger,
			DedupeKey:             wake.DedupeKey,
			CorrelationID:         wake.CorrelationID,
			ExecutionDisabled:     true,
		},
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return DurableRun{}, false, err
	}
	evt := event.NewEvent(event.EventMultiAgentRunCreated, freewillWakeEventSource, map[string]any{
		"run_id": wake.RunID, "workflow_id": FreewillWorkflowID, "status": string(RunStatusCreated),
	})
	evt.Timestamp = now.Format(time.RFC3339Nano)
	evt.CorrelationID = wake.CorrelationID
	evt.Metadata.RunID = wake.RunID
	created := true
	var err error
	if acceptor, ok := r.store.(IdempotentRunAcceptor); ok {
		record, created, err = acceptor.AcceptRun(ctx, record, []event.Event{evt})
	} else {
		record, err = r.store.Create(ctx, record, []event.Event{evt})
	}
	if err != nil {
		return DurableRun{}, false, err
	}
	if !freewillWakeMatches(record, wake) {
		return record, false, ErrRequestIdentityConflict
	}
	if err := r.dispatchOutbox(ctx, record.State.RunID); err != nil {
		return record, created, err
	}
	return record, created, nil
}

func freewillWakeMatches(record DurableRun, wake FreewillWakeRequest) bool {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil ||
		record.Freewill.Scope != wake.Scope || record.Freewill.Trigger != wake.Trigger ||
		record.State.RequestIdentity == nil || record.State.RequestIdentity.RequesterID != wake.AgentID {
		return false
	}
	if record.State.RunID != wake.RunID {
		return freewillAdmissionActive(record)
	}
	return record.Freewill.WakeID == wake.RunID && record.Freewill.DedupeKey == wake.DedupeKey &&
		record.Freewill.CorrelationID == wake.CorrelationID
}

// ClaimFreewillWake leases one accepted wake through the run's existing CAS
// record and outbox. Expired claims are recoverable after restart; active
// claims cannot be selected twice.
func (r *DurableRuntime) ClaimFreewillWake(ctx context.Context, runID, owner string, leaseFor time.Duration) (DurableRun, FreewillWakeLease, error) {
	if strings.TrimSpace(owner) == "" || len(owner) > 128 || strings.ContainsAny(owner, "\r\n\x00") || leaseFor <= 0 {
		return DurableRun{}, FreewillWakeLease{}, errors.New("multiagent: Freewill wake lease request is invalid")
	}
	preflight, err := r.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, FreewillWakeLease{}, err
	}
	agentID, ok := freewillRunAgent(preflight)
	if !ok || preflight.Freewill == nil {
		return preflight, FreewillWakeLease{}, errors.New("multiagent: run is not a Freewill wake")
	}
	if err := ensureFreewillNotHalted(ctx, r.store, agentID, preflight.Freewill.Scope); err != nil {
		return preflight, FreewillWakeLease{}, err
	}
	admission, ok := r.store.(FreewillActorAdmissionStore)
	if !ok {
		return preflight, FreewillWakeLease{}, ErrFreewillActorAdmissionConflict
	}
	if err := admission.EnsureFreewillActorAdmission(ctx, runID, agentID, preflight.Freewill.Scope); err != nil {
		return preflight, FreewillWakeLease{}, err
	}
	claim, err := r.claimer.Acquire(ctx, runID)
	if err != nil {
		return DurableRun{}, FreewillWakeLease{}, err
	}
	defer claim.Release()
	record, err := r.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, FreewillWakeLease{}, err
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.Phase != CheckpointFreewillWake ||
		(record.Freewill.Lifecycle != freewill.StateWaking && record.Freewill.Lifecycle != freewill.StateSelecting) {
		return record, FreewillWakeLease{}, errors.New("multiagent: run is not a Freewill wake")
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record, FreewillWakeLease{}, err
	}
	now := r.now().UTC()
	if record.Freewill.SelectionNextAt.After(now) {
		return record, FreewillWakeLease{}, ErrFreewillWakeNotDue
	}
	if record.Freewill.LeaseOwner != "" && record.Freewill.LeaseUntil.After(now) {
		return record, FreewillWakeLease{}, ErrFreewillWakeLeaseHeld
	}
	if record.Freewill.LeaseAttempt == ^uint64(0) {
		return record, FreewillWakeLease{}, errors.New("multiagent: Freewill wake lease attempt exhausted")
	}
	if record.Freewill.SelectionAttemptLimit == 0 {
		// Older accepted wakes predate the persisted limit; migrate them to the
		// current conservative selector-attempt ceiling on their next claim.
		record.Freewill.SelectionAttemptLimit = maxFreewillSelectionAttempts
	}
	if record.Freewill.LeaseAttempt >= record.Freewill.SelectionAttemptLimit {
		if err := freewill.ValidateTransition(record.Freewill.Lifecycle, freewill.StateHalted); err != nil {
			return record, FreewillWakeLease{}, err
		}
		record.Freewill.Lifecycle = freewill.StateHalted
		record.Freewill.ExecutionDisabled = true
		record.Freewill.PauseReason = "selection_budget_exhausted"
		record.Freewill.SelectionFailureClass = "budget_exhausted"
		record.Freewill.SelectionFailureCode = "selection_attempt_budget_exhausted"
		record.Freewill.LeaseOwner = ""
		record.Freewill.LeaseUntil = time.Time{}
		record.Freewill.SelectionRecoveryNumber = 0
		record.Freewill.SelectionFeedback = ""
		record.Freewill.SelectionNextAt = time.Time{}
		record.State.UpdatedAt = now
		if err := record.Validate(r.supervisor.graph); err != nil {
			return record, FreewillWakeLease{}, err
		}
		evt := freewillLeaseEvent(event.EventFreewillSelectionHalted, record, map[string]any{
			"attempt": record.Freewill.LeaseAttempt, "attempt_count": record.Freewill.LeaseAttempt,
			"attempt_limit": record.Freewill.SelectionAttemptLimit, "recovery_count": record.Freewill.SelectionRecoveryNumber,
			"failure_class": record.Freewill.SelectionFailureClass, "failure_code": record.Freewill.SelectionFailureCode,
			"reason": record.Freewill.PauseReason, "execution_disabled": true,
		})
		saved, err := r.store.Checkpoint(ctx, record.Revision, record, []event.Event{evt})
		if err != nil {
			return record, FreewillWakeLease{}, err
		}
		if err := r.dispatchOutbox(ctx, runID); err != nil {
			return saved, FreewillWakeLease{}, err
		}
		return saved, FreewillWakeLease{}, ErrFreewillSelectionBudgetExceeded
	}
	record.Freewill.LeaseAttempt++
	record.Freewill.LeaseOwner = owner
	record.Freewill.LeaseUntil = now.Add(leaseFor)
	record.Freewill.SelectionNextAt = time.Time{}
	record.State.UpdatedAt = now
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record, FreewillWakeLease{}, err
	}
	evt := freewillLeaseEvent(event.EventFreewillWakeClaimed, record, map[string]any{
		"attempt": record.Freewill.LeaseAttempt, "lease_until": record.Freewill.LeaseUntil.Format(time.RFC3339Nano),
	})
	saved, err := r.store.Checkpoint(ctx, record.Revision, record, []event.Event{evt})
	if err != nil {
		return record, FreewillWakeLease{}, err
	}
	if err := r.dispatchOutbox(ctx, runID); err != nil {
		return saved, FreewillWakeLease{}, err
	}
	return saved, FreewillWakeLease{RunID: runID, Owner: owner, Attempt: saved.Freewill.LeaseAttempt, Until: saved.Freewill.LeaseUntil, Revision: saved.Revision}, nil
}

// BeginFreewillSelection is a durable, fenced transition before the selector
// provider is called. It never dispatches a normal role or authorizes effects.
func (r *DurableRuntime) BeginFreewillSelection(ctx context.Context, lease FreewillWakeLease) (DurableRun, error) {
	claim, err := r.claimer.Acquire(ctx, lease.RunID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := r.store.Load(ctx, lease.RunID)
	if err != nil {
		return DurableRun{}, err
	}
	if err := ensureFreewillNotHalted(ctx, r.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
		return record, err
	}
	if err := validateFreewillLease(record, lease, r.now().UTC()); err != nil {
		return record, err
	}
	if record.Freewill.Lifecycle != freewill.StateWaking {
		return record, errors.New("multiagent: Freewill wake is not ready for selection")
	}
	if err := freewill.ValidateTransition(freewill.StateWaking, freewill.StateSelecting); err != nil {
		return record, err
	}
	record.Freewill.Lifecycle = freewill.StateSelecting
	record.State.UpdatedAt = r.now().UTC()
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record, err
	}
	evt := freewillLeaseEvent(event.EventFreewillSelectionStarted, record, map[string]any{"attempt": lease.Attempt})
	saved, err := r.store.Checkpoint(ctx, record.Revision, record, []event.Event{evt})
	if err != nil {
		return record, err
	}
	if err := r.dispatchOutbox(ctx, lease.RunID); err != nil {
		return saved, err
	}
	return saved, nil
}

// RenewFreewillWake extends only a still-live claim held by the exact owner
// and attempt. A delayed provider call cannot commit after its lease expires.
func (r *DurableRuntime) RenewFreewillWake(ctx context.Context, lease FreewillWakeLease, leaseFor time.Duration) (FreewillWakeLease, error) {
	if leaseFor <= 0 {
		return FreewillWakeLease{}, errors.New("multiagent: Freewill lease duration is invalid")
	}
	claim, err := r.claimer.Acquire(ctx, lease.RunID)
	if err != nil {
		return FreewillWakeLease{}, err
	}
	defer claim.Release()
	record, err := r.store.Load(ctx, lease.RunID)
	if err != nil {
		return FreewillWakeLease{}, err
	}
	if err := ensureFreewillNotHalted(ctx, r.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
		return FreewillWakeLease{}, err
	}
	now := r.now().UTC()
	if err := validateFreewillLease(record, lease, now); err != nil {
		return FreewillWakeLease{}, err
	}
	record.Freewill.LeaseUntil = now.Add(leaseFor)
	record.State.UpdatedAt = now
	evt := freewillLeaseEvent(event.EventFreewillWakeClaimed, record, map[string]any{
		"attempt": lease.Attempt, "lease_until": record.Freewill.LeaseUntil.Format(time.RFC3339Nano), "renewed": true,
	})
	saved, err := r.store.Checkpoint(ctx, record.Revision, record, []event.Event{evt})
	if err != nil {
		return FreewillWakeLease{}, err
	}
	if err := r.dispatchOutbox(ctx, lease.RunID); err != nil {
		return FreewillWakeLease{}, err
	}
	return FreewillWakeLease{RunID: lease.RunID, Owner: lease.Owner, Attempt: lease.Attempt,
		Until: saved.Freewill.LeaseUntil, Revision: saved.Revision}, nil
}

// CommitFreewillSelection stores the selector's typed result under the same
// unexpired wake lease. A choice is only a proposal for approval; this method
// never advances to running or invokes ordinary workflow roles.
func (r *DurableRuntime) CommitFreewillSelection(ctx context.Context, lease FreewillWakeLease, selection freewill.Selection, verifiedResumeIDs map[string]struct{}) (DurableRun, error) {
	return r.CommitFreewillSelectionWithEvidence(ctx, lease, selection, verifiedResumeIDs, FreewillSelectionEvidence{})
}

func (r *DurableRuntime) CommitFreewillSelectionWithEvidence(ctx context.Context, lease FreewillWakeLease, selection freewill.Selection, verifiedResumeIDs map[string]struct{}, evidence FreewillSelectionEvidence) (DurableRun, error) {
	if evidence.Provider != "" || evidence.Model != "" || evidence.Usage != (cost.TokenUsage{}) {
		if strings.TrimSpace(evidence.Provider) == "" || strings.TrimSpace(evidence.Model) == "" || len(evidence.Provider) > 64 || len(evidence.Model) > 256 ||
			evidence.Usage.PromptTokens < 0 || evidence.Usage.CompletionTokens < 0 || evidence.Usage.TotalTokens < 0 {
			return DurableRun{}, errors.New("multiagent: Freewill selection evidence is invalid")
		}
	}
	claim, err := r.claimer.Acquire(ctx, lease.RunID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := r.store.Load(ctx, lease.RunID)
	if err != nil {
		return DurableRun{}, err
	}
	if err := ensureFreewillNotHalted(ctx, r.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
		return record, err
	}
	if err := validateFreewillLease(record, lease, r.now().UTC()); err != nil {
		return record, err
	}
	if record.Freewill.Lifecycle != freewill.StateSelecting {
		return record, errors.New("multiagent: Freewill selection is not active")
	}
	if err := selection.Validate(record.State.RequestIdentity.RequesterID, record.Freewill.Scope, verifiedResumeIDs); err != nil {
		return record, err
	}
	target := freewill.StateProposed
	if selection.ChoiceID == "" {
		target = freewill.StateSleeping
	}
	if err := freewill.ValidateTransition(record.Freewill.Lifecycle, target); err != nil {
		return record, err
	}
	record.Freewill.Lifecycle = target
	record.Freewill.Candidates = cloneFreewillCandidates(selection.Candidates)
	record.Freewill.VerifiedResumeIDs = make([]string, 0, len(verifiedResumeIDs))
	for id := range verifiedResumeIDs {
		record.Freewill.VerifiedResumeIDs = append(record.Freewill.VerifiedResumeIDs, id)
	}
	sort.Strings(record.Freewill.VerifiedResumeIDs)
	record.Freewill.ChoiceID = selection.ChoiceID
	record.Freewill.ReasonSummary = selection.ReasonSummary
	record.Freewill.SleepReason = selection.SleepReason
	record.Freewill.ApprovalRef = ""
	record.Freewill.ApprovalFingerprint = ""
	record.Freewill.ApprovalGeneration = 0
	record.Freewill.ApprovalReceipt = nil
	record.Freewill.PolicyDecision = ""
	record.Freewill.PolicyRuleID = ""
	// A selected candidate remains proposal-only. Keep execution explicitly
	// disabled until a separate governed execution path is implemented.
	record.Freewill.ExecutionDisabled = true
	record.Freewill.PauseReason = ""
	if evidence.Provider != "" {
		record.Freewill.SelectionProvider = evidence.Provider
		record.Freewill.SelectionModel = evidence.Model
		record.Freewill.SelectionUsage = evidence.Usage
		record.Freewill.SelectionAttempt = lease.Attempt
	}
	record.Freewill.SelectionFailureClass = ""
	record.Freewill.SelectionFailureCode = ""
	record.Freewill.SelectionFeedback = ""
	record.Freewill.SelectionNextAt = time.Time{}
	record.Freewill.LeaseOwner = ""
	record.Freewill.LeaseUntil = time.Time{}
	record.State.UpdatedAt = r.now().UTC()
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record, err
	}
	evt := freewillLeaseEvent(event.EventFreewillSelectionCommitted, record, map[string]any{
		"attempt": lease.Attempt, "lifecycle": string(target), "candidate_count": len(selection.Candidates), "choice_id": selection.ChoiceID,
		"recovery_count": record.Freewill.SelectionRecoveryNumber,
	})
	for key, value := range freewillSelectionTraceFields(selection) {
		evt.Payload[key] = value
	}
	if evidence.Provider != "" {
		evt.Payload["provider"] = evidence.Provider
		evt.Payload["model"] = evidence.Model
		evt.Payload["prompt_tokens"] = evidence.Usage.PromptTokens
		evt.Payload["completion_tokens"] = evidence.Usage.CompletionTokens
		evt.Payload["total_tokens"] = evidence.Usage.TotalTokens
	}
	saved, err := r.store.Checkpoint(ctx, record.Revision, record, []event.Event{evt})
	if err != nil {
		return record, err
	}
	if err := r.dispatchOutbox(ctx, lease.RunID); err != nil {
		return saved, err
	}
	return saved, nil
}

// RecordFreewillSelectionFailure persists only a safe classifier result. It
// schedules at most the shared ten recovery attempts for this wake, and
// terminalizes unknown/permanent failures without retaining their raw error.
func (r *DurableRuntime) RecordFreewillSelectionFailure(ctx context.Context, lease FreewillWakeLease, failure error) (DurableRun, error) {
	if failure == nil || errors.Is(failure, context.Canceled) {
		return DurableRun{}, errors.New("multiagent: Freewill selection failure is missing or cancelled")
	}
	class, code, retryAfter, retryable := classifyRetryableRoleFailure(failure)
	if !retryable {
		class, code = "unknown", "unknown"
	}
	claim, err := r.claimer.Acquire(ctx, lease.RunID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := r.store.Load(ctx, lease.RunID)
	if err != nil {
		return DurableRun{}, err
	}
	if err := ensureFreewillNotHalted(ctx, r.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
		return record, err
	}
	if err := validateFreewillLease(record, lease, r.now().UTC()); err != nil {
		return record, err
	}
	if record.Freewill.Lifecycle != freewill.StateSelecting {
		return record, errors.New("multiagent: Freewill selection failure is stale")
	}
	code = safeRetryCode(code)
	now := r.now().UTC()
	record.Freewill.SelectionFailureClass = safeRetryCode(class)
	record.Freewill.SelectionFailureCode = code
	record.Freewill.LeaseOwner = ""
	record.Freewill.LeaseUntil = time.Time{}
	var evt event.Event
	if retryable && record.Freewill.SelectionRecoveryNumber < maxRoleRecoveryRetries {
		record.Freewill.SelectionRecoveryNumber++
		record.Freewill.SelectionNextAt = now.Add(roleRetryDelay("freewill:"+record.State.RunID, record.Freewill.SelectionRecoveryNumber, retryAfter))
		record.Freewill.SelectionFeedback = freewillSelectionRecoveryFeedback(code)
		if record.Freewill.SelectionFeedback == "" {
			record.Freewill.SelectionFeedback = "Retry the same intention selection using the required output schema."
		}
		evt = freewillLeaseEvent(event.EventFreewillSelectionRetryScheduled, record, map[string]any{
			"attempt": lease.Attempt, "recovery_count": record.Freewill.SelectionRecoveryNumber,
			"max_recoveries": maxRoleRecoveryRetries, "failure_class": record.Freewill.SelectionFailureClass,
			"failure_code": code, "next_at": record.Freewill.SelectionNextAt.Format(time.RFC3339Nano),
		})
	} else {
		if retryable {
			record.Freewill.SelectionFailureClass = "recovery_exhausted"
		}
		if err := freewill.ValidateTransition(record.Freewill.Lifecycle, freewill.StateHalted); err != nil {
			return record, err
		}
		record.Freewill.Lifecycle = freewill.StateHalted
		record.Freewill.ExecutionDisabled = true
		record.Freewill.PauseReason = "selection_failed"
		record.Freewill.SelectionNextAt = time.Time{}
		record.Freewill.SelectionFeedback = ""
		evt = freewillLeaseEvent(event.EventFreewillSelectionHalted, record, map[string]any{
			"attempt": lease.Attempt, "recovery_count": record.Freewill.SelectionRecoveryNumber,
			"failure_class": record.Freewill.SelectionFailureClass, "failure_code": code, "reason": record.Freewill.PauseReason,
		})
	}
	record.State.UpdatedAt = now
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record, err
	}
	saved, err := r.store.Checkpoint(ctx, record.Revision, record, []event.Event{evt})
	if err != nil {
		return record, err
	}
	if err := r.dispatchOutbox(ctx, lease.RunID); err != nil {
		return saved, err
	}
	return saved, nil
}

func validateFreewillLease(record DurableRun, lease FreewillWakeLease, now time.Time) error {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.Phase != CheckpointFreewillWake ||
		record.Freewill.LeaseOwner != lease.Owner || record.Freewill.LeaseAttempt != lease.Attempt ||
		!record.Freewill.LeaseUntil.After(now) {
		return ErrStaleFreewillWakeLease
	}
	return record.Validate(nil)
}

func cloneFreewillCandidates(candidates []freewill.CandidateIntention) []freewill.CandidateIntention {
	cloned := make([]freewill.CandidateIntention, len(candidates))
	copy(cloned, candidates)
	for i := range cloned {
		cloned[i].Request.RequestedCapabilities = append([]string(nil), candidates[i].Request.RequestedCapabilities...)
	}
	return cloned
}

// ReleaseFreewillWake relinquishes only the exact active claim. A stale
// decision worker cannot release a newer lease after restart.
func (r *DurableRuntime) ReleaseFreewillWake(ctx context.Context, lease FreewillWakeLease) (DurableRun, error) {
	claim, err := r.claimer.Acquire(ctx, lease.RunID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := r.store.Load(ctx, lease.RunID)
	if err != nil {
		return DurableRun{}, err
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil ||
		record.Freewill.LeaseOwner != lease.Owner || record.Freewill.LeaseAttempt != lease.Attempt {
		return record, ErrStaleFreewillWakeLease
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record, err
	}
	record.Freewill.LeaseOwner = ""
	record.Freewill.LeaseUntil = time.Time{}
	record.State.UpdatedAt = r.now().UTC()
	evt := freewillLeaseEvent(event.EventFreewillWakeReleased, record, map[string]any{"attempt": lease.Attempt})
	saved, err := r.store.Checkpoint(ctx, record.Revision, record, []event.Event{evt})
	if err != nil {
		return record, err
	}
	if err := r.dispatchOutbox(ctx, lease.RunID); err != nil {
		return saved, err
	}
	return saved, nil
}

func freewillLeaseEvent(eventType string, record DurableRun, fields map[string]any) event.Event {
	payload := map[string]any{"run_id": record.State.RunID, "wake_id": record.Freewill.WakeID}
	for key, value := range fields {
		payload[key] = value
	}
	evt := event.NewEvent(eventType, freewillWakeEventSource, payload).
		WithCorrelationID(record.Freewill.CorrelationID).
		WithMetadata(event.EventMetadata{RunID: record.State.RunID})
	evt.Timestamp = record.State.UpdatedAt.UTC().Format(time.RFC3339Nano)
	return evt
}

// freewillSelectionTraceFields records only host-safe selection metadata. The
// durable event contains candidate identities, bounded reasons, and requested
// budget numbers, but never raw goals, model prose, or authority fields.
func freewillSelectionTraceFields(selection freewill.Selection) map[string]any {
	ids := make([]string, 0, len(selection.Candidates))
	budgets := make([]map[string]any, 0, len(selection.Candidates))
	for _, candidate := range selection.Candidates {
		ids = append(ids, candidate.CandidateID)
		budget := map[string]any{"candidate_id": candidate.CandidateID}
		if value := candidate.Request.Budget.RuntimeMinutes; value != nil {
			budget["runtime_minutes"] = *value
		}
		if value := candidate.Request.Budget.DiskMB; value != nil {
			budget["disk_mb"] = *value
		}
		if value := candidate.Request.Budget.MaxModelCalls; value != nil {
			budget["max_model_calls"] = *value
		}
		if value := candidate.Request.Budget.MaxTokens; value != nil {
			budget["max_tokens"] = *value
		}
		budgets = append(budgets, budget)
	}
	outcome := "sleep"
	if selection.ChoiceID != "" {
		outcome = "proposal"
	}
	return map[string]any{
		"candidate_ids":     ids,
		"candidate_budgets": budgets,
		"reason_summary":    selection.ReasonSummary,
		"sleep_reason":      selection.SleepReason,
		"outcome":           outcome,
	}
}
