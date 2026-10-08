package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/emaharmony/prizm/internal/event"
)

const delegatedRoleCommandType = "prizm.command.graph_role_delegation"

type delegatedRolePayload struct {
	ChildID      string        `json:"child_id"`
	Role         Role          `json:"role"`
	Task         TaskReference `json:"task"`
	ExecutionKey string        `json:"execution_key"`
}

type delegatedRoleOutcome struct {
	Result RoleRunResult `json:"result"`
}

func (r *DurableRuntime) dispatchDelegatedRole(ctx context.Context, record DurableRun) (DurableRun, error) {
	if r.delegation.Dispatcher == nil || r.delegation.Outcomes == nil || strings.TrimSpace(r.delegation.Subject) == "" {
		return record, errors.New("multiagent: delegated role execution requires subject, dispatcher, and outcome source")
	}
	role := record.State.CurrentRole
	roleState := record.State.RoleStates[role]
	childID := fmt.Sprintf("%s:%s:%d", record.State.CurrentTask.ID, role, roleState.Visits)
	delegationID := "graph:" + record.ActiveExecutionKey
	deliveryKey := delegationID + ":0"
	correlationID := record.State.RunID + ":" + record.ActiveExecutionKey
	payload, err := json.Marshal(delegatedRolePayload{ChildID: childID, Role: role, Task: record.State.CurrentTask, ExecutionKey: record.ActiveExecutionKey})
	if err != nil {
		return record, fmt.Errorf("multiagent: marshal delegated role: %w", err)
	}
	cmd := event.Command{EventID: event.CommandEventID(deliveryKey), Type: delegatedRoleCommandType,
		RunID: record.State.RunID, TaskID: childID, DelegationID: delegationID,
		CorrelationID: correlationID, IdempotencyKey: deliveryKey,
		SchemaVersion: event.CommandSchemaVersion, Payload: payload}

	now := r.now().UTC()
	roleState.Status = RoleStatusWaiting
	roleState.UpdatedAt = now
	record.State.RoleStates[role] = roleState
	record.State.Status = RunStatusPaused
	record.State.UpdatedAt = now
	record.Phase = CheckpointWaiting
	record.Waiting = &WaitingState{Kind: "delegation_outcome", Reason: "waiting for delegated role outcome",
		SafeToRetry: false, Since: now, ChildID: childID, DelegationID: delegationID,
		DeliveryKey: deliveryKey, CommandEventID: cmd.EventID, CorrelationID: correlationID, StartedAt: now}
	r.supervisor.emitRun(event.EventMultiAgentRunPaused, record.State, record.Waiting.Reason)
	record, err = r.checkpoint(ctx, record)
	if err != nil {
		return record, err
	}
	if err := r.delegation.Dispatcher.Dispatch(ctx, r.delegation.Subject, cmd); err != nil {
		return record, fmt.Errorf("multiagent: dispatch delegated role: %w", err)
	}
	return record, waitingError(record)
}

func (r *DurableRuntime) resumeDelegation(ctx context.Context, record DurableRun) (DurableRun, error) {
	if r.delegation == nil || r.delegation.Outcomes == nil {
		return record, errors.New("multiagent: delegation outcome source is unavailable")
	}
	outcomes, err := r.delegation.Outcomes.PendingOutcomes(ctx, record.State.RunID)
	if err != nil {
		return record, fmt.Errorf("multiagent: load delegation outcomes: %w", err)
	}
	if len(outcomes) == 0 {
		return record, nil
	}
	waiting := record.Waiting
	for _, outcome := range outcomes {
		if err := matchDelegationOutcome(record.State.RunID, waiting, outcome); err != nil {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: err}
		}
		if outcome.Sequence <= waiting.LastOutcomeSequence {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: fmt.Errorf("stale or duplicate delegation outcome sequence %d", outcome.Sequence)}
		}
		if !outcome.Status.Terminal() {
			if outcome.Status != event.OutcomeAccepted && outcome.Status != event.OutcomeProgress {
				return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: fmt.Errorf("unsupported delegation outcome status %q", outcome.Status)}
			}
			waiting.LastOutcomeSequence = outcome.Sequence
			waiting.Reason = "delegated role " + string(outcome.Status)
			record.PendingDelegationOutcomeAck = outcome.EventID
			record, err = r.checkpoint(ctx, record)
			if err != nil {
				return record, err
			}
			record, err = r.ackDelegationOutcome(ctx, record, outcome.EventID)
			if err != nil {
				return record, err
			}
			continue
		}

		var decoded delegatedRoleOutcome
		if len(outcome.Payload) == 0 || json.Unmarshal(outcome.Payload, &decoded) != nil {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: errors.New("terminal delegation outcome requires a valid graph role result")}
		}
		if err := validateRoleRunResult(r.supervisor.graph, decoded.Result); err != nil {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: fmt.Errorf("invalid delegated role result: %w", err)}
		}
		role := record.State.CurrentRole
		roleConfig, _ := r.supervisor.graph.RoleConfig(role)
		record.State.Status = RunStatusRunning
		roleState := record.State.RoleStates[role]
		roleState.Status = RoleStatusRunning
		record.State.RoleStates[role] = roleState
		record.Waiting = nil
		record.Phase = CheckpointRoleRunning
		record.PendingDelegationOutcomeAck = outcome.EventID
		record, err = r.finishPreparedRole(ctx, record, role, roleConfig, decoded.Result, waiting.StartedAt, outcome.OccurredAt.UTC())
		if err != nil {
			return record, err
		}
		return r.ackDelegationOutcome(ctx, record, outcome.EventID)
	}
	return record, nil
}

func (r *DurableRuntime) ackDelegationOutcome(ctx context.Context, record DurableRun, eventID string) (DurableRun, error) {
	if r.delegation == nil || r.delegation.Outcomes == nil {
		return record, errors.New("multiagent: delegation outcome source is unavailable")
	}
	if err := r.delegation.Outcomes.MarkOutcomeConsumed(ctx, eventID); err != nil {
		return record, fmt.Errorf("multiagent: acknowledge delegation outcome: %w", err)
	}
	record.PendingDelegationOutcomeAck = ""
	record, err := r.checkpoint(ctx, record)
	if err != nil {
		return record, fmt.Errorf("multiagent: checkpoint delegation acknowledgement: %w", err)
	}
	return record, nil
}

func matchDelegationOutcome(runID string, waiting *WaitingState, outcome event.Outcome) error {
	if err := outcome.Validate(); err != nil {
		return err
	}
	if waiting == nil || outcome.RunID != runID || outcome.TaskID != waiting.ChildID ||
		outcome.DelegationID != waiting.DelegationID || outcome.DeliveryKey != waiting.DeliveryKey ||
		outcome.CommandEventID != waiting.CommandEventID || outcome.CorrelationID != waiting.CorrelationID ||
		outcome.CausationID != waiting.CommandEventID {
		return errors.New("delegation outcome identity does not match checkpoint")
	}
	return nil
}
