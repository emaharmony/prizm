package multiagent

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// ReconcileChildMutationNoEffectFailure uses the existing exclusive run claim
// to fence a child before consuming its exact durable no-effect ledger. A
// running child is terminalized as failed without invoking a role runner. The
// returned opaque receipt can then be projected by the approved parent.
func (r *DurableRuntime) ReconcileChildMutationNoEffectFailure(ctx context.Context, childRunID string) (FreewillMutationNoEffectReceipt, error) {
	if r == nil || r.store == nil || r.claimer == nil || childRunID == "" {
		return FreewillMutationNoEffectReceipt{}, errors.New("multiagent: child mutation reconciliation is unavailable")
	}
	claim, err := r.claimer.Acquire(ctx, childRunID)
	if err != nil {
		return FreewillMutationNoEffectReceipt{}, err
	}
	defer claim.Release()
	record, err := r.store.Load(ctx, childRunID)
	if err != nil {
		return FreewillMutationNoEffectReceipt{}, err
	}
	if record.State.RunID != childRunID {
		return FreewillMutationNoEffectReceipt{}, ErrMutationNoEffectUnproven
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return FreewillMutationNoEffectReceipt{}, fmt.Errorf("multiagent: child mutation reconciliation source is invalid: %w", err)
	}
	if record.Phase == CheckpointTerminal {
		return receiptFromTerminalNoEffectFailure(record)
	}
	if record.Phase != CheckpointRoleRunning || record.State.Status != RunStatusRunning || record.Retry == nil ||
		record.State.CurrentRole != record.Retry.Role || record.ActiveExecutionKey != record.Retry.ExecutionKey ||
		record.State.RoleStates[record.Retry.Role].Status != RoleStatusRunning {
		return FreewillMutationNoEffectReceipt{}, ErrMutationNoEffectUnproven
	}
	if _, err := receiptFromRetryLedger(childRunID, record.Retry); err != nil {
		return FreewillMutationNoEffectReceipt{}, err
	}
	saved, failureErr := r.persistRoleFailureWithKind(ctx, record, record.Retry.Role,
		errors.New("child_mutation_no_effect_proven"), "mutation_no_effect_reconciled", "child_mutation_no_effect_proven")
	if saved.Revision <= record.Revision || saved.Phase != CheckpointTerminal || saved.State.Status != RunStatusFailed {
		if failureErr != nil {
			return FreewillMutationNoEffectReceipt{}, failureErr
		}
		return FreewillMutationNoEffectReceipt{}, errors.New("multiagent: child no-effect failure could not be durably terminalized")
	}
	if failureErr != nil && failureErr.Error() != "child_mutation_no_effect_proven" {
		return FreewillMutationNoEffectReceipt{}, failureErr
	}
	return receiptFromTerminalNoEffectFailure(saved)
}

func receiptFromTerminalNoEffectFailure(record DurableRun) (FreewillMutationNoEffectReceipt, error) {
	if record.Phase != CheckpointTerminal || record.State.Status != RunStatusFailed || record.Failure == nil ||
		record.Failure.Kind != "mutation_no_effect_reconciled" || record.Failure.Message != "child_mutation_no_effect_proven" {
		return FreewillMutationNoEffectReceipt{}, ErrMutationNoEffectUnproven
	}
	var match *RetryState
	for i := range record.RetryHistory {
		state := &record.RetryHistory[i]
		if state.AttemptID == record.Failure.AttemptID {
			if match != nil {
				return FreewillMutationNoEffectReceipt{}, ErrMutationNoEffectUnproven
			}
			match = state
		}
	}
	if match == nil {
		return FreewillMutationNoEffectReceipt{}, ErrMutationNoEffectUnproven
	}
	return receiptFromRetryLedger(record.State.RunID, match)
}

func receiptFromRetryLedger(childRunID string, state *RetryState) (FreewillMutationNoEffectReceipt, error) {
	if state == nil || !state.MutationLedgerReady || !state.MutationPossible || state.UnresolvedMutationFailure ||
		state.UnattributedMutation || state.AttemptID == "" || !validAttemptIdentity(state.AttemptID) ||
		len(state.UnsafeMutationIDs) == 0 || len(state.UnsafeMutationIDs) != len(state.ProvenNoEffectIDs) {
		return FreewillMutationNoEffectReceipt{}, ErrMutationNoEffectUnproven
	}
	unsafe := make(map[string]struct{}, len(state.UnsafeMutationIDs))
	for _, id := range state.UnsafeMutationIDs {
		if !validSHA256Hex(id) {
			return FreewillMutationNoEffectReceipt{}, ErrMutationNoEffectUnproven
		}
		if _, exists := unsafe[id]; exists {
			return FreewillMutationNoEffectReceipt{}, ErrMutationNoEffectUnproven
		}
		unsafe[id] = struct{}{}
	}
	for _, id := range state.ProvenNoEffectIDs {
		if _, exists := unsafe[id]; !exists {
			return FreewillMutationNoEffectReceipt{}, ErrMutationNoEffectUnproven
		}
		delete(unsafe, id)
	}
	if len(unsafe) != 0 {
		return FreewillMutationNoEffectReceipt{}, ErrMutationNoEffectUnproven
	}
	ids := append([]string(nil), state.UnsafeMutationIDs...)
	sort.Strings(ids)
	return FreewillMutationNoEffectReceipt{childRunID: childRunID, attemptID: state.AttemptID, mutationIDs: ids}, nil
}
