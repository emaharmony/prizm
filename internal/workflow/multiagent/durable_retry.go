package multiagent

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/retry"
	"github.com/emaharmony/prizm/internal/subagent"
)

type durableRoleRetryHandle struct {
	mu      sync.Mutex
	runtime *DurableRuntime
	ctx     context.Context
	record  DurableRun
	closed  bool
}

func newDurableRoleRetryHandle(ctx context.Context, runtime *DurableRuntime, record DurableRun) *durableRoleRetryHandle {
	return &durableRoleRetryHandle{runtime: runtime, ctx: ctx, record: record}
}

func (h *durableRoleRetryHandle) CurrentAttempt() RetryAttempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return retryAttempt(h.record.Retry)
}

func (h *durableRoleRetryHandle) NoProgressCount(expectedAttemptID, observationHash string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validateAttempt(expectedAttemptID); err != nil {
		return 0, err
	}
	if h.record.Retry.LastObservationHash == safeRetryFact(observationHash) {
		return h.record.Retry.ConsecutiveNoProgress, nil
	}
	return 0, nil
}

func retryAttempt(state *RetryState) RetryAttempt {
	if state == nil {
		return RetryAttempt{}
	}
	return RetryAttempt{
		VisitID: state.VisitID, AttemptID: state.AttemptID, Number: state.Number,
		RecoveryNumber: state.RecoveryNumber, Remaining: state.MaxRecoveries - state.RecoveryNumber,
		NextAt: state.NextAt,
	}
}

func (h *durableRoleRetryHandle) ConsumeRecovery(expectedAttemptID, failureCode string) (RetryAttempt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validateAttempt(expectedAttemptID); err != nil {
		return RetryAttempt{}, err
	}
	if err := h.ctx.Err(); err != nil {
		return RetryAttempt{}, err
	}
	if h.record.Retry.RecoveryNumber >= h.record.Retry.MaxRecoveries {
		return RetryAttempt{}, ErrRetryBudgetExhausted
	}
	next := h.record
	state := *next.Retry
	state.RecoveryNumber++
	state.Number++
	state.AttemptID = retryAttemptID(state.VisitID, state.Number)
	state.FailureClass = "correction"
	state.FailureCode = safeRetryCode(failureCode)
	state.NextAt = h.runtime.now().UTC().Add(roleRetryDelay(state.VisitID, state.RecoveryNumber, 0))
	state.ReplaySafe = !state.MutationPossible
	next.Retry = &state
	h.runtime.emitRetry(event.EventMultiAgentRetryScheduled, next, state)
	saved, err := h.checkpoint(next)
	if err != nil {
		return RetryAttempt{}, err
	}
	h.record = saved
	return retryAttempt(saved.Retry), nil
}

func (h *durableRoleRetryHandle) RecordProgress(expectedAttemptID, kind, safeRef, observationHash string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validateAttempt(expectedAttemptID); err != nil {
		return err
	}
	next := h.record
	state := *next.Retry
	state.Progress = append(state.Progress, RetryProgress{
		Kind: safeRetryCode(kind), SafeRef: safeRetryFact(safeRef), ObservationHash: safeRetryFact(observationHash),
	})
	if len(state.Progress) > 24 {
		state.Progress = append([]RetryProgress(nil), state.Progress[len(state.Progress)-24:]...)
	}
	if !state.MutationPossible {
		state.ReplaySafe = true
	}
	next.Retry = &state
	saved, err := h.checkpoint(next)
	if err != nil {
		return err
	}
	h.record = saved
	return nil
}

func (h *durableRoleRetryHandle) RecordCorrection(expectedAttemptID, errorClass, code string) error {
	return h.RecordCorrectionFeedback(expectedAttemptID, errorClass, code, "")
}

func (h *durableRoleRetryHandle) RecordCorrectionFeedback(expectedAttemptID, errorClass, code, _ string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validateAttempt(expectedAttemptID); err != nil {
		return err
	}
	next := h.record
	state := *next.Retry
	feedback := roleFailureFeedback(state.Role, safeRetryCode(code), "", "")
	state.Corrections = append(state.Corrections, RetryCorrection{
		ErrorClass: safeRetryCode(errorClass), Code: safeRetryCode(code), Feedback: feedback,
	})
	if len(state.Corrections) > 16 {
		state.Corrections = append([]RetryCorrection(nil), state.Corrections[len(state.Corrections)-16:]...)
	}
	if feedback != "" {
		state.RecoveryFeedback = feedback
	}
	next.Retry = &state
	saved, err := h.checkpoint(next)
	if err != nil {
		return err
	}
	h.record = saved
	return nil
}

func correctionFeedback(code string) string {
	switch code {
	case "patch_validation_failed":
		return "The patch proposal failed validation before an approval was saved. Correct the request and try again: input must contain exactly the non-empty string fields \"patch\" and \"base_sha\"; patch must be one complete unified diff that applies cleanly, changes at least one file, and stays within this workspace's allowed write paths; base_sha must equal the trusted patch base supplied in the task. After the proposal succeeds, return the required final role JSON."
	case "invalid_proposal_diff":
		return "The proposal is not a valid complete unified diff. Correct its diff syntax and return the required final role JSON. The proposal was not executed."
	case "invalid_json_syntax":
		return "The previous response was not valid JSON syntax. Return exactly one JSON object matching the required role schema."
	case "missing_required_fields":
		return "The previous JSON omitted required fields. Add the missing fields listed by the required role schema and return exactly one JSON object."
	case "invalid_field_type", "invalid_field_value", "invalid_structured_output", "malformed_tool_input":
		return "The previous response did not match the required JSON structure. Check field types and allowed values in the required role schema, then return exactly one valid response."
	case "repeated_tool_observation":
		return "The tool returned unchanged evidence repeatedly. Change the arguments or use a different approach before continuing."
	default:
		return ""
	}
}

// ConfirmNoEffects clears only the matching started mutation intent after the
// tool adapter has durably recorded typed proof that the action had no effect.
// Any prior unattributed or other unresolved mutation keeps the role unsafe.
func (h *durableRoleRetryHandle) ConfirmNoEffects(expectedAttemptID, mutationID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validateAttempt(expectedAttemptID); err != nil {
		return err
	}
	mutationID = safeMutationID(mutationID)
	if mutationID == "" {
		return ErrStaleRetryAttempt
	}
	next := h.record
	state := *next.Retry
	ensureMutationLedger(&state)
	state.UnsafeMutationIDs = append([]string(nil), state.UnsafeMutationIDs...)
	state.ProvenNoEffectIDs = append([]string(nil), state.ProvenNoEffectIDs...)
	if !containsMutationID(state.ProvenNoEffectIDs, mutationID) {
		return fmt.Errorf("multiagent: mutation has no durable no-effects proof")
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
		return fmt.Errorf("multiagent: no pending mutation intent for confirmation")
	}
	state.UnsafeMutationIDs = kept
	state.ProvenNoEffectIDs = removeMutationID(state.ProvenNoEffectIDs, mutationID)
	refreshMutationSafety(&state)
	next.Retry = &state
	saved, err := h.checkpoint(next)
	if err != nil {
		return err
	}
	h.record = saved
	return nil
}

func (h *durableRoleRetryHandle) RecordToolDiagnostic(diagnostic ToolDiagnostic) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.validateAttempt(diagnostic.AttemptID); err != nil {
		return err
	}
	next := h.record
	state := *next.Retry
	if err := applyRetryToolDiagnostic(&state, diagnostic); err != nil {
		return err
	}
	next.Retry = &state
	payload := map[string]any{
		"run_id": next.State.RunID, "workflow_id": next.State.WorkflowID,
		"role": string(state.Role), "execution_key": state.ExecutionKey,
		"attempt_id": state.AttemptID, "tool": safeRetryCode(diagnostic.Tool),
		"status": safeRetryCode(diagnostic.Status), "sequence": diagnostic.Sequence,
	}
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
	h.runtime.buffer.Emit(event.NewEvent(event.EventMultiAgentToolDiagnostic, durableEventSource, payload).
		WithCorrelationID(next.State.RunID).
		WithMetadata(event.EventMetadata{RunID: next.State.RunID, Agent: string(state.Role)}))
	saved, err := h.checkpoint(next)
	if err != nil {
		return err
	}
	h.record = saved
	return nil
}

func ensureMutationLedger(state *RetryState) {
	if state.MutationLedgerReady {
		return
	}
	// Old checkpoints carried only the global bit. Preserve that ambiguity;
	// it cannot be attributed to, and cleared by, a later action ID.
	if state.MutationPossible {
		state.UnattributedMutation = true
	}
	state.MutationLedgerReady = true
}

func appendUniqueMutationID(ids []string, mutationID string) []string {
	for _, existing := range ids {
		if existing == mutationID {
			return ids
		}
	}
	return append(ids, mutationID)
}

func safeMutationID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return ""
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return ""
	}
	return strings.ToLower(value)
}

func containsMutationID(ids []string, mutationID string) bool {
	for _, existing := range ids {
		if existing == mutationID {
			return true
		}
	}
	return false
}

func removeMutationID(ids []string, mutationID string) []string {
	kept := make([]string, 0, len(ids))
	for _, existing := range ids {
		if existing != mutationID {
			kept = append(kept, existing)
		}
	}
	return kept
}

func refreshMutationSafety(state *RetryState) {
	state.MutationPossible = state.UnattributedMutation || len(state.UnsafeMutationIDs) > 0
	state.ReplaySafe = !state.MutationPossible
}

func (h *durableRoleRetryHandle) validateAttempt(expected string) error {
	if h.closed || h.record.Retry == nil || expected == "" || h.record.Retry.AttemptID != expected ||
		h.record.State.CurrentRole != h.record.Retry.Role || h.record.Phase != CheckpointRoleRunning ||
		h.record.ActiveExecutionKey != h.record.Retry.ExecutionKey {
		return ErrStaleRetryAttempt
	}
	roleState := h.record.State.RoleStates[h.record.Retry.Role]
	if roleState.Status != RoleStatusRunning || roleState.LastExecutionKey != h.record.Retry.ExecutionKey {
		return ErrStaleRetryAttempt
	}
	return nil
}

func (h *durableRoleRetryHandle) checkpoint(next DurableRun) (DurableRun, error) {
	saved, err := h.runtime.checkpoint(h.ctx, next)
	if saved.Revision > h.record.Revision {
		h.record = saved
	}
	return saved, err
}

func (h *durableRoleRetryHandle) close() DurableRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	return h.record
}

func safeRetryCode(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 96 {
		value = value[:96]
	}
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func safeRetryFact(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		value = value[:128]
	}
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("_-.::/@", r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func (r *DurableRuntime) emitRetry(kind string, record DurableRun, state RetryState) {
	payload := map[string]any{
		"run_id": record.State.RunID, "workflow_id": record.State.WorkflowID,
		"role": string(state.Role), "execution_key": state.ExecutionKey,
		"visit_id": state.VisitID, "attempt_id": state.AttemptID,
		"attempt": state.Number, "recovery_number": state.RecoveryNumber,
	}
	if kind != event.EventMultiAgentRetryAttemptStarted {
		payload["failure_class"] = safeRetryCode(state.FailureClass)
		payload["failure_code"] = safeRetryCode(state.FailureCode)
	}
	if kind == event.EventMultiAgentRetryScheduled {
		payload["next_at"] = state.NextAt.UTC().Format(time.RFC3339Nano)
	}
	evt := event.NewEvent(kind, durableEventSource, payload).
		WithCorrelationID(record.State.RunID).
		WithMetadata(event.EventMetadata{RunID: record.State.RunID, Agent: string(state.Role)})
	r.buffer.Emit(evt)
}

func classifyRetryableRoleFailure(err error) (class, code string, retryAfter time.Duration, ok bool) {
	if err == nil {
		return "", "", 0, false
	}
	var governance *GovernanceError
	var approval *ApprovalRequiredError
	if errors.As(err, &governance) || errors.As(err, &approval) || errors.Is(err, context.Canceled) {
		return "", "", 0, false
	}
	var schema *SchemaCorrectionError
	if errors.As(err, &schema) {
		return "schema_correction", safeRetryCode(schema.Code), 0, true
	}
	var structured *StructuredOutputError
	if errors.As(err, &structured) {
		return "schema_correction", "invalid_structured_output", 0, true
	}
	var safeFailure *subagent.SafeFailureError
	if errors.As(err, &safeFailure) {
		switch safeFailure.Code {
		case "invalid_proposal_diff":
			return "schema_correction", safeFailure.Code, 0, true
		case "provider_turn_failed":
			return "provider_transient", safeRetryCode(safeFailure.Code), safeFailure.RetryAfter(), true
		case "deadline_exceeded", "execution_error":
			return "execution_transient", safeRetryCode(safeFailure.Code), 0, true
		default:
			return "", "", 0, false
		}
	}
	if durableTransientError(err) {
		class = "provider_transient"
		var after interface{ RetryAfter() time.Duration }
		if errors.As(err, &after) {
			retryAfter = after.RetryAfter()
		}
		return class, "provider_transient", retryAfter, true
	}
	return "", "", 0, false
}

func durableTransientError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var typed *retry.RetryableError
	if errors.As(err, &typed) {
		return typed.Retryable
	}
	return retry.IsTransientNetworkError(err)
}

func (r *DurableRuntime) scheduleRoleRetry(ctx context.Context, record DurableRun, role Role, result RoleRunResult, failure error, class, code string, retryAfter time.Duration, deadline time.Time) (DurableRun, error) {
	if err := ctx.Err(); err != nil {
		return r.persistRoleFailure(ctx, record, role, err)
	}
	state := record.Retry
	if state != nil && state.Role == role && state.MutationPossible {
		state.FailureCode = "ambiguous_mutation"
		record.Retry = state
		return r.pauseUncertainExecution(ctx, record)
	}
	if state == nil || state.Role != role {
		return r.persistRoleFailure(ctx, record, role, errors.New("retry_refused_ambiguous_execution"))
	}
	state.AccumulatedUsage = addTokenUsage(state.AccumulatedUsage, result.TokenUsage)
	iterations := result.LocalIterations
	if iterations <= 0 {
		iterations = 1
	}
	state.AccumulatedIterations += iterations
	state.FailureClass, state.FailureCode = safeRetryCode(class), safeRetryCode(code)
	var schemaCorrection *SchemaCorrectionError
	if errors.As(failure, &schemaCorrection) && schemaCorrection != nil {
		feedback := canonicalSchemaFeedback(schemaCorrection.Role, safeRetryCode(schemaCorrection.Code), schemaCorrection.Fields)
		if feedback == "" || feedback != strings.TrimSpace(schemaCorrection.Feedback) {
			feedback = ""
		}
		if feedback != "" && len(feedback) <= 512 {
			state.RecoveryFeedback = feedback
			state.Corrections = append(state.Corrections, RetryCorrection{
				ErrorClass: "schema", Code: safeRetryCode(schemaCorrection.Code), Feedback: feedback,
			})
			if len(state.Corrections) > 16 {
				state.Corrections = append([]RetryCorrection(nil), state.Corrections[len(state.Corrections)-16:]...)
			}
		}
	}
	if state.RecoveryNumber >= state.MaxRecoveries {
		r.emitRetry(event.EventMultiAgentRetryExhausted, record, *state)
		return r.persistRoleFailure(ctx, record, role, ErrRetryBudgetExhausted)
	}
	proposedRecovery := state.RecoveryNumber + 1
	proposedDue := r.now().UTC().Add(roleRetryDelay(state.VisitID, proposedRecovery, retryAfter))
	if !deadline.IsZero() && !proposedDue.Before(deadline) {
		state.FailureCode = "retry_deadline_exceeded"
		record.Retry = state
		return r.persistRoleFailure(ctx, record, role, RetryDeadlineError{})
	}
	state.RecoveryNumber++
	state.Number++
	state.AttemptID = retryAttemptID(state.VisitID, state.Number)
	state.NextAt = proposedDue
	state.ReplaySafe = true
	record.Retry = state
	record.Phase = CheckpointWaiting
	record.State.Status = RunStatusPaused
	record.State.UpdatedAt = r.now().UTC()
	roleState := record.State.RoleStates[role]
	roleState.Status = RoleStatusWaiting
	roleState.LastError = state.FailureCode
	roleState.UpdatedAt = record.State.UpdatedAt
	record.State.RoleStates[role] = roleState
	record.Waiting = &WaitingState{Kind: "role_retry", Reason: retryWaitReason(state), SafeToRetry: true, Since: record.State.UpdatedAt}
	r.emitRetry(event.EventMultiAgentRetryScheduled, record, *state)
	saved, err := r.checkpoint(ctx, record)
	if err != nil {
		return saved, err
	}
	return saved, waitingError(saved)
}

func archiveRetryForReflection(record DurableRun, result RoleRunResult, role Role, failureCode string, now time.Time) DurableRun {
	record = accumulateFailedAttempt(record, result)
	if record.Retry == nil {
		return record
	}
	state := *record.Retry
	if state.FailureCode == "" {
		state.FailureCode = safeRetryCode(failureCode)
		state.FailureClass = "role_replanned"
	}
	roleState := record.State.RoleStates[role]
	roleState.LocalIterations += state.AccumulatedIterations
	roleState.Retries += state.RecoveryNumber
	roleState.TokenUsage = addTokenUsage(roleState.TokenUsage, state.AccumulatedUsage)
	roleState.Status = RoleStatusFailed
	roleState.LastError = "reflection_replan"
	roleState.UpdatedAt = now
	roleState.CompletedAt = timePointer(now)
	record.State.RoleStates[role] = roleState
	record.State.BudgetUsage.TotalLocalIterations += state.AccumulatedIterations
	record.State.BudgetUsage.Retries += state.RecoveryNumber
	record.State.BudgetUsage.Tokens = addTokenUsage(record.State.BudgetUsage.Tokens, state.AccumulatedUsage)
	appendRetryHistory(&record, &state)
	record.Retry = nil
	return record
}

func describeRecoveryCandidate(record DurableRun, now time.Time) RecoveryCandidate {
	candidate := RecoveryCandidate{RunID: record.State.RunID, Phase: record.Phase, DueAt: time.Time{}}
	switch record.Phase {
	case CheckpointPendingRole:
		candidate.Kind, candidate.Ready = "accepted", true
	case CheckpointTransitionPending:
		candidate.Kind, candidate.Ready = "transition", true
	case CheckpointRoleRunning:
		candidate.Kind = "role_running"
		candidate.Ambiguous = true
		if record.Retry != nil {
			candidate.DueAt = record.Retry.NextAt
			if record.Retry.ReplaySafe && !record.Retry.MutationPossible && !record.Retry.NextAt.After(now) {
				candidate.Kind, candidate.Ready, candidate.Ambiguous = "retry", true, false
			}
		}
	case CheckpointWaiting:
		if record.Waiting != nil {
			candidate.WaitingKind = record.Waiting.Kind
			candidate.Kind = record.Waiting.Kind
		}
		if candidate.WaitingKind == "role_retry" && record.Retry != nil {
			candidate.DueAt = record.Retry.NextAt
			candidate.Ready = !record.Retry.MutationPossible && !record.Retry.NextAt.After(now)
		} else if candidate.WaitingKind == "delegation_outcome" && record.Waiting.DispatchPending {
			if record.Retry == nil {
				candidate.Ready = true
			} else {
				candidate.DueAt = record.Retry.NextAt
				candidate.Ambiguous = record.Retry.MutationPossible || record.Retry.UnresolvedMutationFailure || record.Retry.UnattributedMutation
				candidate.Ready = !candidate.Ambiguous && (record.Retry.NextAt.IsZero() || !record.Retry.NextAt.After(now))
			}
		} else if candidate.WaitingKind == "execution_reconciliation" {
			candidate.Ambiguous = true
		} else if candidate.WaitingKind == "delegation_join" && record.DelegationJoin != nil {
			candidate.Kind = "delegation_join"
			for _, child := range record.DelegationJoin.Children {
				if !child.DispatchPending {
					continue
				}
				due := time.Time{}
				if child.Retry != nil {
					due = child.Retry.NextAt
					if child.Retry.MutationPossible || child.Retry.UnresolvedMutationFailure || child.Retry.UnattributedMutation {
						candidate.Ambiguous = true
						continue
					}
				}
				if candidate.DueAt.IsZero() || (!due.IsZero() && due.Before(candidate.DueAt)) {
					candidate.DueAt = due
				}
				if due.IsZero() || !due.After(now) {
					candidate.Ready = true
				}
			}
		}
	default:
		candidate.Kind = string(record.Phase)
	}
	return candidate
}

func (r *DurableRuntime) ListRecoveryCandidates(ctx context.Context, now time.Time, limit int) ([]RecoveryCandidate, error) {
	lister, ok := r.store.(NonterminalRunLister)
	if !ok {
		return nil, errors.New("multiagent: durable store does not support recovery enumeration")
	}
	records, err := lister.ListNonterminalRuns(ctx, limit)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = r.now()
	}
	candidates := make([]RecoveryCandidate, 0, len(records))
	for _, record := range records {
		candidates = append(candidates, describeRecoveryCandidate(record, now.UTC()))
	}
	return candidates, nil
}

func retryWaitReason(state *RetryState) string {
	if state == nil {
		return "role retry scheduled"
	}
	return fmt.Sprintf("role retry %d of %d scheduled", state.RecoveryNumber, state.MaxRecoveries)
}

func accumulateFailedAttempt(record DurableRun, result RoleRunResult) DurableRun {
	if record.Retry == nil {
		return record
	}
	state := *record.Retry
	state.AccumulatedUsage = addTokenUsage(state.AccumulatedUsage, result.TokenUsage)
	iterations := result.LocalIterations
	if iterations <= 0 {
		iterations = 1
	}
	state.AccumulatedIterations += iterations
	record.Retry = &state
	return record
}

func roleFailureCode(err error) string {
	var safeFailure *subagent.SafeFailureError
	if errors.As(err, &safeFailure) && safeRetryCode(safeFailure.Code) != "" {
		return safeRetryCode(safeFailure.Code)
	}
	var governance *GovernanceError
	if errors.As(err, &governance) {
		switch governance.Kind {
		case "fan_out":
			return "unsupported_fan_out"
		case "validation_profile_configuration":
			return "validation_profile_configuration_changed"
		}
	}
	if errors.Is(err, ErrRetryBudgetExhausted) {
		return "retry_exhausted"
	}
	var deadline RetryDeadlineError
	if errors.As(err, &deadline) {
		return "retry_deadline_exceeded"
	}
	var noProgress *subagent.NoProgressError
	if errors.As(err, &noProgress) {
		return "no_progress"
	}
	return "role_execution_failed"
}

func durableFailureDiagnostic(err error, retry *RetryState) *PersistedFailure {
	diagnostic := &PersistedFailure{FailureReason: "nonretryable"}
	if retry != nil {
		diagnostic.AttemptID = safeRetryCode(retry.AttemptID)
		diagnostic.Attempt = retry.Number
		diagnostic.RecoveryNumber = retry.RecoveryNumber
		diagnostic.MaxRecoveries = retry.MaxRecoveries
	}
	if errors.Is(err, ErrRetryBudgetExhausted) {
		diagnostic.FailureReason = "retry_exhausted"
		diagnostic.RetryExhausted = true
		if retry != nil {
			diagnostic.FailureClass = safeRetryCode(retry.FailureClass)
			diagnostic.FailureCode = safeRetryCode(retry.FailureCode)
			diagnostic.Retryable = diagnostic.FailureClass != ""
		}
		return diagnostic
	}
	if errors.Is(err, context.Canceled) {
		diagnostic.FailureReason = "cancelled"
		diagnostic.FailureClass = "cancellation"
		diagnostic.FailureCode = "cancelled"
		return diagnostic
	}
	var governance *GovernanceError
	var approval *ApprovalRequiredError
	if errors.As(err, &governance) || errors.As(err, &approval) {
		diagnostic.FailureReason = "governance"
		diagnostic.FailureClass = "governance"
		diagnostic.FailureCode = roleFailureCode(err)
		return diagnostic
	}
	if class, code, _, retryable := classifyRetryableRoleFailure(err); retryable {
		diagnostic.FailureClass = safeRetryCode(class)
		diagnostic.FailureCode = safeRetryCode(code)
		diagnostic.Retryable = true
		diagnostic.FailureReason = "recovery_unavailable"
		return diagnostic
	}
	diagnostic.FailureClass = "nonretryable"
	diagnostic.FailureCode = safeRetryCode(roleFailureCode(err))
	var deadline RetryDeadlineError
	if errors.As(err, &deadline) {
		diagnostic.FailureReason = "deadline"
	}
	var noProgress *subagent.NoProgressError
	if errors.As(err, &noProgress) {
		diagnostic.FailureReason = "no_progress"
	}
	return diagnostic
}

func appendRetryHistory(record *DurableRun, state *RetryState) {
	if record == nil || state == nil {
		return
	}
	if state.RecoveryNumber == 0 && len(state.Corrections) == 0 && state.FailureCode == "" {
		return
	}
	copy := *state
	copy.Progress = append([]RetryProgress(nil), state.Progress...)
	copy.Corrections = append([]RetryCorrection(nil), state.Corrections...)
	copy.UnsafeMutationIDs = append([]string(nil), state.UnsafeMutationIDs...)
	copy.ProvenNoEffectIDs = append([]string(nil), state.ProvenNoEffectIDs...)
	record.RetryHistory = append(record.RetryHistory, copy)
	if len(record.RetryHistory) > 32 {
		record.RetryHistory = append([]RetryState(nil), record.RetryHistory[len(record.RetryHistory)-32:]...)
	}
}
