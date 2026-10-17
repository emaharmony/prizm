package multiagent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

// NewDelegationChildRetryHandle binds process-local hooks to a child visit
// after the worker has authenticated the persisted command and its delivery.
// The returned hooks are never serialized into model-visible command data.
func NewDelegationChildRetryHandle(
	ctx context.Context,
	store DurableRunStore,
	runID, joinID, childID, deliveryAttemptID, attemptID string,
) (RoleVisitRetryHandle, ToolEventSink, error) {
	if store == nil {
		return nil, nil, errors.New("multiagent: child retry store is required")
	}
	record, err := store.Load(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	child := findDelegationChild(record, joinID, childID)
	if child == nil || child.Retry == nil || child.DeliveryAttemptID != deliveryAttemptID || child.Retry.AttemptID != attemptID {
		return nil, nil, ErrStaleRetryAttempt
	}
	h := &delegationChildRetryHandle{ctx: ctx, store: store, runID: runID, joinID: joinID, childID: childID,
		deliveryAttemptID: deliveryAttemptID, attemptID: attemptID, state: *child.Retry}
	return h, h.recordToolDiagnostic, nil
}

type delegationChildRetryHandle struct {
	mu                sync.Mutex
	ctx               context.Context
	store             DurableRunStore
	runID             string
	joinID            string
	childID           string
	deliveryAttemptID string
	attemptID         string
	state             RetryState
}

func (h *delegationChildRetryHandle) CurrentAttempt() RetryAttempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return retryAttempt(&h.state)
}

func (h *delegationChildRetryHandle) NoProgressCount(expected, observationHash string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(expected); err != nil {
		return 0, err
	}
	if h.state.LastObservationHash == safeRetryFact(observationHash) {
		return h.state.ConsecutiveNoProgress, nil
	}
	return 0, nil
}

func (h *delegationChildRetryHandle) ConsumeRecovery(expected, failureCode string) (RetryAttempt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(expected); err != nil {
		return RetryAttempt{}, err
	}
	if err := h.ctx.Err(); err != nil {
		return RetryAttempt{}, err
	}
	var nextAttempt RetryAttempt
	_, err := h.update(expected, func(child *DelegationJoinChild) error {
		state := child.Retry
		if state.RecoveryNumber >= state.MaxRecoveries {
			return ErrRetryBudgetExhausted
		}
		if state.MutationPossible || state.UnresolvedMutationFailure || state.UnattributedMutation {
			return errors.New("multiagent: child retry refused with unresolved mutation")
		}
		state.RecoveryNumber++
		state.Number++
		state.AttemptID = retryAttemptID(state.VisitID, state.Number)
		if state.FailureClass == "" || state.FailureCode != safeRetryCode(failureCode) {
			state.FailureClass = "correction"
			state.FailureCode = safeRetryCode(failureCode)
			state.FailureOrigin = FailureOriginUnknown
		}
		state.RecoveryFeedback = correctionFeedback(state.FailureCode)
		state.NextAt = time.Now().UTC().Add(roleRetryDelay(state.VisitID, state.RecoveryNumber, 0))
		state.ReplaySafe = true
		nextAttempt = retryAttempt(state)
		return nil
	}, func(record DurableRun, child DelegationJoinChild) []event.Event {
		return []event.Event{childRetryEvent(record, child, event.EventMultiAgentRetryScheduled)}
	})
	return nextAttempt, err
}

func (h *delegationChildRetryHandle) RecordProgress(expected, kind, safeRef, observationHash string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(expected); err != nil {
		return err
	}
	_, err := h.update(expected, func(child *DelegationJoinChild) error {
		state := child.Retry
		state.Progress = append(state.Progress, RetryProgress{Kind: safeRetryCode(kind), SafeRef: safeRetryFact(safeRef), ObservationHash: safeRetryFact(observationHash)})
		if len(state.Progress) > 24 {
			state.Progress = append([]RetryProgress(nil), state.Progress[len(state.Progress)-24:]...)
		}
		state.ReplaySafe = !state.MutationPossible
		return nil
	}, nil)
	return err
}

func (h *delegationChildRetryHandle) RecordCorrection(expected, class, code string) error {
	return h.RecordCorrectionWithOrigin(expected, class, code, "", "")
}

func (h *delegationChildRetryHandle) RecordCorrectionFeedback(expected, class, code, _ string) error {
	return h.RecordCorrectionWithOrigin(expected, class, code, "", "")
}

func (h *delegationChildRetryHandle) RecordCorrectionWithOrigin(expected, errorClass, code, origin, _ string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(expected); err != nil {
		return err
	}
	_, err := h.update(expected, func(child *DelegationJoinChild) error {
		state := child.Retry
		feedback := roleFailureFeedback(child.Role, safeRetryCode(code), "", "")
		class := correctionFailureClass(errorClass, code)
		safeOrigin := safeFailureOrigin(class, safeRetryCode(code), FailureOrigin(origin))
		state.FailureClass, state.FailureCode, state.FailureOrigin = class, safeRetryCode(code), safeOrigin
		state.Corrections = append(state.Corrections, RetryCorrection{ErrorClass: safeRetryCode(errorClass), Code: safeRetryCode(code), FailureOrigin: safeOrigin, Feedback: feedback})
		if len(state.Corrections) > 16 {
			state.Corrections = append([]RetryCorrection(nil), state.Corrections[len(state.Corrections)-16:]...)
		}
		if feedback != "" {
			state.RecoveryFeedback = feedback
		}
		return nil
	}, nil)
	return err
}

func (h *delegationChildRetryHandle) ConfirmNoEffects(expected, mutationID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(expected); err != nil {
		return err
	}
	mutationID = safeMutationID(mutationID)
	if mutationID == "" {
		return ErrStaleRetryAttempt
	}
	_, err := h.update(expected, func(child *DelegationJoinChild) error {
		state := child.Retry
		ensureMutationLedger(state)
		state.UnsafeMutationIDs = append([]string(nil), state.UnsafeMutationIDs...)
		state.ProvenNoEffectIDs = append([]string(nil), state.ProvenNoEffectIDs...)
		if !containsMutationID(state.ProvenNoEffectIDs, mutationID) {
			return errors.New("multiagent: mutation has no durable no-effects proof")
		}
		found := false
		kept := state.UnsafeMutationIDs[:0]
		for _, pending := range state.UnsafeMutationIDs {
			if pending == mutationID {
				found = true
				continue
			}
			kept = append(kept, pending)
		}
		if !found {
			return errors.New("multiagent: no pending mutation intent for confirmation")
		}
		state.UnsafeMutationIDs = kept
		state.ProvenNoEffectIDs = removeMutationID(state.ProvenNoEffectIDs, mutationID)
		refreshMutationSafety(state)
		return nil
	}, nil)
	return err
}

func (h *delegationChildRetryHandle) recordToolDiagnostic(diagnostic ToolDiagnostic) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(diagnostic.AttemptID); err != nil {
		return err
	}
	_, err := h.update(diagnostic.AttemptID, func(child *DelegationJoinChild) error {
		return applyRetryToolDiagnostic(child.Retry, diagnostic)
	}, func(record DurableRun, child DelegationJoinChild) []event.Event {
		payload := map[string]any{"run_id": record.State.RunID, "workflow_id": record.State.WorkflowID,
			"role": string(child.Role), "execution_key": child.ExecutionKey, "attempt_id": child.Retry.AttemptID,
			"tool": safeRetryCode(diagnostic.Tool), "status": safeRetryCode(diagnostic.Status), "sequence": diagnostic.Sequence}
		if mutationID := safeMutationID(diagnostic.MutationID); mutationID != "" {
			payload["mutation_id"] = mutationID
		}
		if code := safeRetryCode(diagnostic.ErrorCode); code != "" {
			payload["error_code"] = code
		}
		if diagnostic.Duration > 0 {
			payload["duration_ms"] = diagnostic.Duration.Milliseconds()
		}
		if ref := safeRetryFact(diagnostic.SafeRef); ref != "" {
			payload["safe_ref"] = ref
		}
		if hash := safeRetryFact(diagnostic.ObservationHash); hash != "" {
			payload["observation_hash"] = hash
		}
		if diagnostic.MutationPossible {
			payload["mutation_possible"] = true
		}
		if diagnostic.NoEffectsProven {
			payload["no_effects_proven"] = true
		}
		return []event.Event{event.NewEvent(event.EventMultiAgentToolDiagnostic, durableEventSource, payload).WithCorrelationID(child.CorrelationID).WithMetadata(event.EventMetadata{RunID: record.State.RunID, Agent: string(child.Role)})}
	})
	return err
}

func applyRetryToolDiagnostic(state *RetryState, diagnostic ToolDiagnostic) error {
	ensureMutationLedger(state)
	state.UnsafeMutationIDs = append([]string(nil), state.UnsafeMutationIDs...)
	state.ProvenNoEffectIDs = append([]string(nil), state.ProvenNoEffectIDs...)
	if diagnostic.MutationPossible {
		mutationID := safeMutationID(diagnostic.MutationID)
		if mutationID == "" {
			state.UnattributedMutation = true
		} else if !containsMutationID(state.UnsafeMutationIDs, mutationID) && len(state.UnsafeMutationIDs) >= 128 {
			state.UnattributedMutation = true
		} else {
			state.UnsafeMutationIDs = appendUniqueMutationID(state.UnsafeMutationIDs, mutationID)
		}
	}
	if diagnostic.Status == "failed" && diagnostic.MutationPossible && !diagnostic.NoEffectsProven {
		state.UnresolvedMutationFailure = true
	}
	if diagnostic.NoEffectsProven {
		mutationID := safeMutationID(diagnostic.MutationID)
		if diagnostic.Status != "failed" || mutationID == "" || !containsMutationID(state.UnsafeMutationIDs, mutationID) {
			return errors.New("multiagent: invalid no-effects proof diagnostic")
		}
		state.ProvenNoEffectIDs = appendUniqueMutationID(state.ProvenNoEffectIDs, mutationID)
		if len(state.ProvenNoEffectIDs) > 128 {
			return errors.New("multiagent: no-effects proof ledger is full")
		}
	}
	refreshMutationSafety(state)
	if diagnostic.NoProgressEligible && diagnostic.ObservationHash != "" {
		hash := safeRetryFact(diagnostic.ObservationHash)
		if hash == state.LastObservationHash {
			state.ConsecutiveNoProgress++
		} else {
			state.LastObservationHash = hash
			state.ConsecutiveNoProgress = 1
		}
		updated := false
		for i := range state.Observations {
			if state.Observations[i].Hash == hash {
				state.Observations[i].Count++
				updated = true
				break
			}
		}
		if !updated {
			state.Observations = append(state.Observations, RetryObservation{Hash: hash, Count: 1})
		}
		if len(state.Observations) > 64 {
			state.Observations = append([]RetryObservation(nil), state.Observations[len(state.Observations)-64:]...)
		}
	}
	return nil
}

func (h *delegationChildRetryHandle) validate(expected string) error {
	if expected == "" || expected != h.attemptID || h.state.AttemptID != expected {
		return ErrStaleRetryAttempt
	}
	return h.ctx.Err()
}

func (h *delegationChildRetryHandle) update(expected string, change func(*DelegationJoinChild) error, buildEvents func(DurableRun, DelegationJoinChild) []event.Event) (DurableRun, error) {
	if err := h.ctx.Err(); err != nil {
		return DurableRun{}, err
	}
	saved, err := updateDelegationJoinChild(h.ctx, h.store, h.runID, h.joinID, h.childID, h.deliveryAttemptID, expected,
		change, func(record DurableRun) []event.Event {
			if buildEvents == nil {
				return nil
			}
			child := findDelegationChild(record, h.joinID, h.childID)
			if child == nil {
				return nil
			}
			return buildEvents(record, *child)
		})
	if err != nil {
		return DurableRun{}, err
	}
	child := findDelegationChild(saved, h.joinID, h.childID)
	if child == nil || child.Retry == nil {
		return DurableRun{}, errors.New("multiagent: child retry state disappeared")
	}
	h.state, h.attemptID = *child.Retry, child.Retry.AttemptID
	return saved, nil
}

func findDelegationChild(record DurableRun, joinID, childID string) *DelegationJoinChild {
	if record.DelegationJoin == nil || record.DelegationJoin.JoinID != joinID {
		return nil
	}
	for i := range record.DelegationJoin.Children {
		if record.DelegationJoin.Children[i].ChildID == childID {
			return &record.DelegationJoin.Children[i]
		}
	}
	return nil
}

// ValidateDelegatedRoleCommand binds every host-visible identity to the
// durable execution before retry hooks or project tools are constructed.
func ValidateDelegatedRoleCommand(ctx context.Context, store DurableRunStore, command GraphRoleCommand) error {
	if store == nil {
		return errors.New("multiagent: delegated command store is required")
	}
	record, err := store.Load(ctx, command.RunID)
	if err != nil {
		return err
	}
	if record.State.RunID != command.RunID || record.State.WorkspaceID != command.WorkspaceID ||
		record.State.Status != RunStatusPaused || record.Phase != CheckpointWaiting {
		return errors.New("multiagent: delegated command does not match active run scope")
	}
	request := command.Request
	if request.Run.RunID != command.RunID || request.Run.Task != command.Task || request.Run.CurrentRole != command.Role ||
		request.Run.ExecutionKey != command.ExecutionKey || request.Run.WorkspaceID != command.WorkspaceID {
		return errors.New("multiagent: delegated role request does not match command identity")
	}
	if expected := trustedTreeBasisForRole(record, command.Role); !reflect.DeepEqual(request.TrustedTreeBasis, expected) {
		return errors.New("multiagent: delegated role trusted patch basis does not match durable applied receipt")
	}
	if command.JoinID != "" {
		child := findDelegationChild(record, command.JoinID, command.ChildID)
		if child == nil || child.Role != command.Role || child.Task != command.Task || child.ExecutionKey != command.ExecutionKey ||
			!sameOptionalTaskReference(request.ParentTask, record.DelegationJoin.ParentTask) ||
			child.DelegationID != command.DelegationID || child.Lane != command.Lane || child.DeliveryKey != command.DeliveryKey ||
			child.CorrelationID != command.CorrelationID || child.CommandEventID != command.CommandEventID || !child.Deadline.Equal(command.Deadline) ||
			child.DeliveryAttemptID != command.DeliveryAttemptID || child.Retry == nil || child.Retry.AttemptID != command.AttemptID {
			return ErrStaleRetryAttempt
		}
		return nil
	}
	if request.ParentTask != nil {
		return ErrStaleRetryAttempt
	}
	w := record.Waiting
	if record.ActiveExecutionKey != command.ExecutionKey || w == nil || w.Kind != "delegation_outcome" || record.State.CurrentRole != command.Role || record.State.CurrentTask != command.Task ||
		w.ChildID != command.ChildID || w.DelegationID != command.DelegationID || w.DeliveryKey != command.DeliveryKey ||
		w.CorrelationID != command.CorrelationID || w.CommandEventID != command.CommandEventID || !w.Deadline.Equal(command.Deadline) ||
		w.DeliveryAttemptID != command.DeliveryAttemptID || w.AttemptID != command.AttemptID || record.Retry == nil || record.Retry.AttemptID != command.AttemptID {
		return ErrStaleRetryAttempt
	}
	return nil
}

func sameOptionalTaskReference(left, right *TaskReference) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func childRetryEvent(record DurableRun, child DelegationJoinChild, kind string) event.Event {
	state := child.Retry
	payload := map[string]any{"run_id": record.State.RunID, "workflow_id": record.State.WorkflowID,
		"role": string(child.Role), "execution_key": child.ExecutionKey, "visit_id": state.VisitID,
		"attempt_id": state.AttemptID, "attempt": state.Number,
		"recovery_number": state.RecoveryNumber, "max_recoveries": state.MaxRecoveries,
		"failure_class": safeRetryCode(state.FailureClass), "failure_code": safeRetryCode(state.FailureCode),
		"retryable": true}
	if !state.NextAt.IsZero() {
		payload["next_at"] = state.NextAt.UTC().Format(time.RFC3339Nano)
	}
	return event.NewEvent(kind, durableEventSource, payload).WithCorrelationID(child.CorrelationID).WithMetadata(event.EventMetadata{RunID: record.State.RunID, Agent: string(child.Role)})
}

var _ RoleVisitRetryHandle = (*delegationChildRetryHandle)(nil)

// NewDelegatedRoleRetryHandle binds trusted retry hooks to one outer graph-role
// delivery. The run remains owned by the controller; worker writes are limited
// to its RetryState and the current Waiting attempt identity.
func NewDelegatedRoleRetryHandle(ctx context.Context, store DurableRunStore, runID, deliveryAttemptID, attemptID string) (RoleVisitRetryHandle, ToolEventSink, error) {
	if store == nil {
		return nil, nil, errors.New("multiagent: delegated retry store is required")
	}
	record, err := store.Load(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	if !delegatedRoleRetryMatches(record, deliveryAttemptID, attemptID) {
		return nil, nil, ErrStaleRetryAttempt
	}
	handle := &delegatedRoleRetryHandle{ctx: ctx, store: store, runID: runID, deliveryAttemptID: deliveryAttemptID, attemptID: attemptID, state: *record.Retry}
	return handle, handle.recordToolDiagnostic, nil
}

type delegatedRoleRetryHandle struct {
	mu                                  sync.Mutex
	ctx                                 context.Context
	store                               DurableRunStore
	runID, deliveryAttemptID, attemptID string
	state                               RetryState
}

func delegatedRoleRetryMatches(record DurableRun, deliveryAttemptID, attemptID string) bool {
	w := record.Waiting
	return record.Phase == CheckpointWaiting && w != nil && w.Kind == "delegation_outcome" && !w.DispatchPending &&
		record.Retry != nil && w.DeliveryAttemptID == deliveryAttemptID && w.AttemptID == attemptID &&
		record.Retry.AttemptID == attemptID && record.Retry.ExecutionKey == record.ActiveExecutionKey &&
		record.Retry.Role == record.State.CurrentRole && record.Retry.VisitID != ""
}

func (h *delegatedRoleRetryHandle) CurrentAttempt() RetryAttempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return retryAttempt(&h.state)
}

func (h *delegatedRoleRetryHandle) NoProgressCount(expected, observationHash string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(expected); err != nil {
		return 0, err
	}
	if h.state.LastObservationHash == safeRetryFact(observationHash) {
		return h.state.ConsecutiveNoProgress, nil
	}
	return 0, nil
}

func (h *delegatedRoleRetryHandle) ConsumeRecovery(expected, failureCode string) (RetryAttempt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(expected); err != nil {
		return RetryAttempt{}, err
	}
	var result RetryAttempt
	_, err := h.update(expected, func(record *DurableRun) error {
		state := record.Retry
		if state.RecoveryNumber >= state.MaxRecoveries {
			return ErrRetryBudgetExhausted
		}
		if state.MutationPossible || state.UnresolvedMutationFailure || state.UnattributedMutation {
			return errors.New("multiagent: delegated retry refused with unresolved mutation")
		}
		state.RecoveryNumber++
		state.Number++
		state.AttemptID = retryAttemptID(state.VisitID, state.Number)
		state.FailureClass = "correction"
		state.FailureCode = safeRetryCode(failureCode)
		state.RecoveryFeedback = correctionFeedback(state.FailureCode)
		state.NextAt = time.Now().UTC().Add(roleRetryDelay(state.VisitID, state.RecoveryNumber, 0))
		record.Waiting.AttemptID = state.AttemptID
		result = retryAttempt(state)
		return nil
	}, func(record DurableRun) []event.Event {
		return []event.Event{delegatedRetryEvent(record, event.EventMultiAgentRetryScheduled)}
	})
	return result, err
}

func (h *delegatedRoleRetryHandle) RecordProgress(expected, kind, ref, hash string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(expected); err != nil {
		return err
	}
	_, err := h.update(expected, func(record *DurableRun) error {
		state := record.Retry
		state.Progress = append(state.Progress, RetryProgress{Kind: safeRetryCode(kind), SafeRef: safeRetryFact(ref), ObservationHash: safeRetryFact(hash)})
		if len(state.Progress) > 24 {
			state.Progress = append([]RetryProgress(nil), state.Progress[len(state.Progress)-24:]...)
		}
		state.ReplaySafe = !state.MutationPossible
		return nil
	}, nil)
	return err
}

func (h *delegatedRoleRetryHandle) RecordCorrection(expected, class, code string) error {
	return h.RecordCorrectionFeedback(expected, class, code, "")
}
func (h *delegatedRoleRetryHandle) RecordCorrectionFeedback(expected, class, code, _ string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(expected); err != nil {
		return err
	}
	_, err := h.update(expected, func(record *DurableRun) error {
		state := record.Retry
		fb := roleFailureFeedback(record.State.CurrentRole, safeRetryCode(code), "", "")
		state.Corrections = append(state.Corrections, RetryCorrection{ErrorClass: safeRetryCode(class), Code: safeRetryCode(code), Feedback: fb})
		if len(state.Corrections) > 16 {
			state.Corrections = append([]RetryCorrection(nil), state.Corrections[len(state.Corrections)-16:]...)
		}
		if fb != "" {
			state.RecoveryFeedback = fb
		}
		return nil
	}, nil)
	return err
}
func (h *delegatedRoleRetryHandle) ConfirmNoEffects(expected, mutationID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(expected); err != nil {
		return err
	}
	id := safeMutationID(mutationID)
	if id == "" {
		return ErrStaleRetryAttempt
	}
	_, err := h.update(expected, func(record *DurableRun) error {
		state := record.Retry
		ensureMutationLedger(state)
		state.UnsafeMutationIDs = append([]string(nil), state.UnsafeMutationIDs...)
		state.ProvenNoEffectIDs = append([]string(nil), state.ProvenNoEffectIDs...)
		if !containsMutationID(state.ProvenNoEffectIDs, id) {
			return errors.New("multiagent: mutation has no durable no-effects proof")
		}
		found := false
		kept := state.UnsafeMutationIDs[:0]
		for _, pending := range state.UnsafeMutationIDs {
			if pending == id {
				found = true
				continue
			}
			kept = append(kept, pending)
		}
		if !found {
			return errors.New("multiagent: no pending mutation intent for confirmation")
		}
		state.UnsafeMutationIDs = kept
		state.ProvenNoEffectIDs = removeMutationID(state.ProvenNoEffectIDs, id)
		refreshMutationSafety(state)
		return nil
	}, nil)
	return err
}
func (h *delegatedRoleRetryHandle) recordToolDiagnostic(d ToolDiagnostic) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validate(d.AttemptID); err != nil {
		return err
	}
	_, err := h.update(d.AttemptID, func(record *DurableRun) error { return applyRetryToolDiagnostic(record.Retry, d) }, func(record DurableRun) []event.Event { return []event.Event{delegatedToolDiagnosticEvent(record, d)} })
	return err
}
func (h *delegatedRoleRetryHandle) validate(expected string) error {
	if expected == "" || expected != h.attemptID || h.state.AttemptID != expected {
		return ErrStaleRetryAttempt
	}
	return h.ctx.Err()
}
func (h *delegatedRoleRetryHandle) update(expected string, change func(*DurableRun) error, build func(DurableRun) []event.Event) (DurableRun, error) {
	for i := 0; i < 16; i++ {
		record, err := h.store.Load(h.ctx, h.runID)
		if err != nil {
			return DurableRun{}, err
		}
		if !delegatedRoleRetryMatches(record, h.deliveryAttemptID, expected) {
			return DurableRun{}, ErrStaleRetryAttempt
		}
		before := record
		if err := change(&record); err != nil {
			return DurableRun{}, err
		}
		if record.State.RunID != before.State.RunID || record.State.CurrentRole != before.State.CurrentRole || record.State.CurrentTask != before.State.CurrentTask || record.State.WorkspaceID != before.State.WorkspaceID || record.ActiveExecutionKey != before.ActiveExecutionKey || record.Waiting.DeliveryKey != before.Waiting.DeliveryKey || record.Waiting.CommandEventID != before.Waiting.CommandEventID || record.Waiting.CorrelationID != before.Waiting.CorrelationID || record.Waiting.DeliveryAttemptID != before.Waiting.DeliveryAttemptID || record.Retry.AttemptID != record.Waiting.AttemptID || record.Retry.ExecutionKey != before.Retry.ExecutionKey || record.Retry.VisitID != before.Retry.VisitID || record.Retry.Role != before.Retry.Role {
			return DurableRun{}, errors.New("multiagent: delegated retry changed immutable run scope")
		}
		var events []event.Event
		if build != nil {
			events = build(record)
		}
		saved, err := h.store.Checkpoint(h.ctx, before.Revision, record, events)
		if err == nil {
			h.state = *saved.Retry
			h.attemptID = saved.Retry.AttemptID
			return saved, nil
		}
		if !errors.Is(err, ErrRevisionConflict) {
			return DurableRun{}, err
		}
	}
	return DurableRun{}, errors.New("multiagent: delegated retry exceeded revision conflict retries")
}

func delegatedRetryEvent(record DurableRun, kind string) event.Event {
	s := record.Retry
	payload := map[string]any{"run_id": record.State.RunID, "workflow_id": record.State.WorkflowID, "role": string(s.Role), "execution_key": s.ExecutionKey, "visit_id": s.VisitID, "attempt_id": s.AttemptID, "attempt": s.Number, "recovery_number": s.RecoveryNumber, "failure_class": s.FailureClass, "failure_code": s.FailureCode, "next_at": s.NextAt.UTC().Format(time.RFC3339Nano)}
	return event.NewEvent(kind, durableEventSource, payload).WithCorrelationID(record.State.RunID).WithMetadata(event.EventMetadata{RunID: record.State.RunID, Agent: string(s.Role)})
}
func delegatedToolDiagnosticEvent(record DurableRun, d ToolDiagnostic) event.Event {
	s := record.Retry
	payload := map[string]any{"run_id": record.State.RunID, "workflow_id": record.State.WorkflowID, "role": string(s.Role), "execution_key": s.ExecutionKey, "attempt_id": s.AttemptID, "tool": safeRetryCode(d.Tool), "status": safeRetryCode(d.Status), "sequence": d.Sequence}
	if code := safeRetryCode(d.ErrorCode); code != "" {
		payload["error_code"] = code
	}
	if d.MutationPossible {
		payload["mutation_possible"] = true
	}
	if d.NoEffectsProven {
		payload["no_effects_proven"] = true
	}
	if id := safeMutationID(d.MutationID); id != "" {
		payload["mutation_id"] = id
	}
	return event.NewEvent(event.EventMultiAgentToolDiagnostic, durableEventSource, payload).WithCorrelationID(record.State.RunID).WithMetadata(event.EventMetadata{RunID: record.State.RunID, Agent: string(s.Role)})
}

var _ RoleVisitRetryHandle = (*delegatedRoleRetryHandle)(nil)
