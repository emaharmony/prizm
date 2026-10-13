package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

// GraphRoleDelegationCommandType identifies the canonical graph-role command.
const GraphRoleDelegationCommandType = "prizm.command.graph_role_delegation"

// GraphRoleCommand is the complete read-only execution context for one prepared
// role visit. Identity is repeated inside the payload because transports publish
// payload bytes; workers must bind it back to the durable envelope identities.
type GraphRoleCommand struct {
	ChildID           string         `json:"child_id"`
	Role              Role           `json:"role"`
	Task              TaskReference  `json:"task"`
	ExecutionKey      string         `json:"execution_key"`
	RunID             string         `json:"run_id"`
	WorkspaceID       string         `json:"workspace_id"`
	DelegationID      string         `json:"delegation_id"`
	JoinID            string         `json:"join_id,omitempty"`
	Lane              FanOutLane     `json:"lane,omitempty"`
	DeliveryKey       string         `json:"delivery_key"`
	CorrelationID     string         `json:"correlation_id"`
	CommandEventID    string         `json:"command_event_id"`
	Deadline          time.Time      `json:"deadline"`
	DeliveryAttemptID string         `json:"delivery_attempt_id"`
	AttemptID         string         `json:"attempt_id"`
	RecoveryFeedback  string         `json:"recovery_feedback,omitempty"`
	Request           RoleRunRequest `json:"request"`
}

// GraphRoleOutcome is the typed terminal payload returned by a graph worker.
type GraphRoleOutcome struct {
	Result  RoleRunResult         `json:"result,omitempty"`
	Failure *RoleExecutionFailure `json:"failure,omitempty"`
}

// Retain the package-local name used by the existing deterministic tests.
type delegatedRoleOutcome = GraphRoleOutcome

func (r *DurableRuntime) dispatchDelegatedRole(ctx context.Context, record DurableRun) (DurableRun, error) {
	if r.delegation.RequireWorkspaceID && strings.TrimSpace(record.State.WorkspaceID) == "" {
		return record, errors.New("multiagent: delegated graph role requires a persisted workspace identity")
	}
	role := record.State.CurrentRole
	roleState := record.State.RoleStates[role]
	childID := fmt.Sprintf("%s:%s:%d", record.State.CurrentTask.ID, role, roleState.Visits)
	delegationID := "graph:" + record.ActiveExecutionKey
	deliveryKey := delegationID + ":0"
	deliveryAttemptID := record.ActiveExecutionKey + ":delivery:0"
	correlationID := fmt.Sprintf("%s:%s:delivery:0", record.State.RunID, record.ActiveExecutionKey)
	now := r.now().UTC()
	roleState.Status = RoleStatusWaiting
	roleState.UpdatedAt = now
	record.State.RoleStates[role] = roleState
	record.State.Status = RunStatusPaused
	record.State.UpdatedAt = now
	record.Phase = CheckpointWaiting
	record.Waiting = &WaitingState{Kind: "delegation_outcome", Reason: "waiting for delegated role outcome",
		SafeToRetry: false, Since: now, ChildID: childID, DelegationID: delegationID,
		DeliveryKey: deliveryKey, CommandEventID: event.CommandEventID(deliveryKey), CorrelationID: correlationID,
		DeliveryAttemptID: deliveryAttemptID, DeliveryNumber: 0, AttemptID: record.Retry.AttemptID,
		StartedAt: now, Deadline: now.Add(r.delegation.Deadline), DispatchPending: true}
	r.supervisor.emitRun(event.EventMultiAgentRunPaused, record.State, record.Waiting.Reason)
	record, err := r.checkpoint(ctx, record)
	if err != nil {
		return record, err
	}
	record, err = r.dispatchPendingDelegation(ctx, record)
	if err != nil {
		return record, err
	}
	return record, waitingError(record)
}

func (r *DurableRuntime) dispatchPendingDelegation(ctx context.Context, record DurableRun) (DurableRun, error) {
	waiting := record.Waiting
	if waiting == nil || waiting.Kind != "delegation_outcome" || !waiting.DispatchPending {
		return record, errors.New("multiagent: no pending graph delegation dispatch")
	}
	if record.Retry != nil && !record.Retry.NextAt.IsZero() && r.now().UTC().Before(record.Retry.NextAt) {
		return record, nil
	}
	if record.Retry != nil {
		record.Retry.NextAt = time.Time{}
	}
	role := record.State.CurrentRole
	roleConfig, _ := r.supervisor.graph.RoleConfig(role)
	request := RoleRunRequest{Run: r.supervisor.runView(record.State), RoleConfig: cloneRoleConfig(roleConfig),
		FanOutAvailable: role == RolePlanner}
	payload, err := json.Marshal(GraphRoleCommand{ChildID: waiting.ChildID, Role: role,
		Task: record.State.CurrentTask, ExecutionKey: record.ActiveExecutionKey,
		RunID: record.State.RunID, WorkspaceID: record.State.WorkspaceID,
		DelegationID: waiting.DelegationID, DeliveryKey: waiting.DeliveryKey,
		CorrelationID: waiting.CorrelationID, CommandEventID: waiting.CommandEventID,
		Deadline: waiting.Deadline, DeliveryAttemptID: waiting.DeliveryAttemptID, AttemptID: waiting.AttemptID,
		RecoveryFeedback: func() string {
			if record.Retry != nil {
				return record.Retry.RecoveryFeedback
			}
			return ""
		}(), Request: request})
	if err != nil {
		return record, fmt.Errorf("multiagent: marshal delegated role: %w", err)
	}
	cmd := event.Command{EventID: waiting.CommandEventID, Type: GraphRoleDelegationCommandType,
		RunID: record.State.RunID, TaskID: waiting.ChildID, DelegationID: waiting.DelegationID,
		CorrelationID: waiting.CorrelationID, IdempotencyKey: waiting.DeliveryKey, Deadline: waiting.Deadline,
		SchemaVersion: event.CommandSchemaVersion, Payload: payload}
	if err := r.delegation.Dispatcher.Dispatch(ctx, r.delegation.Subject, cmd); err != nil {
		return record, fmt.Errorf("multiagent: dispatch delegated role: %w", err)
	}
	waiting.DispatchPending = false
	record, err = r.checkpoint(ctx, record)
	if err != nil {
		return record, fmt.Errorf("multiagent: checkpoint delegated role dispatch: %w", err)
	}
	return record, nil
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
		return r.expireDelegation(ctx, record)
	}
	waiting := record.Waiting
	for _, outcome := range outcomes {
		if obsoleteDelegatedRoleOutcome(record, outcome) {
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
		if err := matchDelegationOutcome(record.State.RunID, waiting, outcome); err != nil {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: err}
		}
		if outcome.OccurredAt.After(waiting.Deadline) {
			return r.timeoutDelegation(ctx, record, "delegated role outcome arrived after deadline")
		}
		if decoded, ok := decodeDelegationOutcome(outcome); ok && decoded.Result.FanOut != nil {
			if record.State.CurrentRole != RolePlanner {
				record, terminalErr := r.failDelegation(ctx, record, "fanout_unsupported", errors.New("only the planner may request fan-out"))
				if record.PendingDelegationOutcomeAck == outcome.EventID {
					if acked, ackErr := r.ackDelegationOutcome(ctx, record, outcome.EventID); ackErr == nil {
						record = acked
					} else {
						return record, ackErr
					}
				}
				return record, terminalErr
			}
			return r.startDelegationJoin(ctx, record, decoded.Result, waiting.StartedAt, outcome.OccurredAt.UTC(), outcome.EventID)
		}
		if outcome.Sequence <= waiting.LastOutcomeSequence {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: fmt.Errorf("stale or duplicate delegation outcome sequence %d", outcome.Sequence)}
		}
		if outcome.Status.Terminal() && outcome.Status != event.OutcomeSucceeded && record.Retry != nil {
			var stale GraphRoleOutcome
			if json.Unmarshal(outcome.Payload, &stale) == nil && stale.Failure != nil &&
				stale.Failure.DeliveryAttemptID == waiting.DeliveryAttemptID && stale.Failure.AttemptID != waiting.AttemptID {
				waiting.LastOutcomeSequence = outcome.Sequence
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
		if outcome.Status != event.OutcomeSucceeded {
			var decoded GraphRoleOutcome
			failureScheduled := false
			failureValid := record.Retry != nil && json.Unmarshal(outcome.Payload, &decoded) == nil && decoded.Failure != nil &&
				decoded.Failure.DeliveryAttemptID == waiting.DeliveryAttemptID && decoded.Failure.AttemptID == waiting.AttemptID
			if failureValid && decoded.Failure.Retryable && record.Retry.RecoveryNumber < record.Retry.MaxRecoveries &&
				record.Retry.ReplaySafe && !record.Retry.MutationPossible && !record.Retry.UnresolvedMutationFailure && !record.Retry.UnattributedMutation &&
				!r.now().UTC().Add(roleRetryDelay(record.Retry.VisitID, record.Retry.RecoveryNumber+1, decoded.Failure.RetryAfter)).After(waiting.Deadline) {
				record.Retry.RecoveryNumber++
				record.Retry.AccumulatedUsage = addTokenUsage(record.Retry.AccumulatedUsage, decoded.Failure.Usage)
				record.Retry.Number++
				record.Retry.AttemptID = retryAttemptID(record.Retry.VisitID, record.Retry.Number)
				record.Retry.FailureClass = safeRetryCode(decoded.Failure.Class)
				record.Retry.FailureCode = safeRetryCode(decoded.Failure.Code)
				record.Retry.RecoveryFeedback = roleFailureFeedback(record.State.CurrentRole, record.Retry.FailureCode,
					decoded.Failure.CorrectionField, decoded.Failure.ExpectedType, decoded.Failure.AllowedValues)
				record.Retry.NextAt = r.now().UTC().Add(roleRetryDelay(record.Retry.VisitID, record.Retry.RecoveryNumber, decoded.Failure.RetryAfter))
				waiting.DeliveryNumber++
				deliveryNumber := waiting.DeliveryNumber
				waiting.DeliveryAttemptID = fmt.Sprintf("%s:delivery:%d", record.ActiveExecutionKey, deliveryNumber)
				waiting.DeliveryKey = fmt.Sprintf("%s:%d", waiting.DelegationID, deliveryNumber)
				waiting.CommandEventID = event.CommandEventID(waiting.DeliveryKey)
				waiting.CorrelationID = fmt.Sprintf("%s:%s:delivery:%d", record.State.RunID, record.ActiveExecutionKey, deliveryNumber)
				waiting.AttemptID = record.Retry.AttemptID
				waiting.LastOutcomeSequence = 0
				waiting.DispatchPending = true
				waiting.Reason = "waiting for delegated role recovery"
				failureScheduled = true
				record.PendingDelegationOutcomeAck = outcome.EventID
				r.supervisor.emit(event.EventMultiAgentRetryScheduled, record.State, map[string]any{
					"run_id": record.State.RunID, "workflow_id": record.State.WorkflowID, "role": string(record.Retry.Role),
					"execution_key": record.Retry.ExecutionKey, "visit_id": record.Retry.VisitID,
					"attempt_id": record.Retry.AttemptID, "attempt": record.Retry.Number,
					"recovery_number": record.Retry.RecoveryNumber, "next_at": record.Retry.NextAt.UTC().Format(time.RFC3339Nano),
					"failure_class": record.Retry.FailureClass, "failure_code": record.Retry.FailureCode})
				record, err = r.checkpoint(ctx, record)
				if err != nil {
					return record, err
				}
				return r.ackDelegationOutcome(ctx, record, outcome.EventID)
			}
			if failureValid && !failureScheduled {
				record.Retry.AccumulatedUsage = addTokenUsage(record.Retry.AccumulatedUsage, decoded.Failure.Usage)
			}
			record.PendingDelegationOutcomeAck = outcome.EventID
			reason := "delegated role " + string(outcome.Status)
			if failureValid {
				reason = "delegated role failure: " + safeRetryCode(decoded.Failure.Class) + "/" + safeRetryCode(decoded.Failure.Code)
			}
			record, terminalErr := r.failDelegation(ctx, record, "delegation_"+string(outcome.Status), errors.New(reason))
			if record.PendingDelegationOutcomeAck == outcome.EventID {
				if acked, ackErr := r.ackDelegationOutcome(ctx, record, outcome.EventID); ackErr == nil {
					record = acked
				} else {
					return record, ackErr
				}
			}
			return record, terminalErr
		}

		var decoded GraphRoleOutcome
		if len(outcome.Payload) == 0 || json.Unmarshal(outcome.Payload, &decoded) != nil {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: errors.New("terminal delegation outcome requires a valid graph role result")}
		}
		if err := validateRoleRunResult(r.supervisor.graph, decoded.Result); err != nil {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: fmt.Errorf("invalid delegated role result: %w", err)}
		}
		if record.Retry != nil {
			decoded.Result.TokenUsage = addTokenUsage(record.Retry.AccumulatedUsage, decoded.Result.TokenUsage)
			decoded.Result.Retries += record.Retry.RecoveryNumber
			decoded.Result.LocalIterations += record.Retry.AccumulatedIterations
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
		if len(decoded.Result.Proposals) != 0 {
			record, err = r.pauseForProposals(ctx, record, role, decoded.Result, waiting.StartedAt, outcome.OccurredAt.UTC(), nil)
			if err != nil {
				// Waiting is an expected result. The role result and exact proposal
				// identities are durable before the worker fact is acknowledged.
				var waitingErr *RunWaitingError
				if !errors.As(err, &waitingErr) {
					return record, err
				}
			}
			record, err = r.ackDelegationOutcome(ctx, record, outcome.EventID)
			if err != nil {
				return record, err
			}
			return record, waitingError(record)
		}
		record, err = r.finishPreparedRole(ctx, record, role, roleConfig, decoded.Result, waiting.StartedAt, outcome.OccurredAt.UTC())
		if err != nil {
			return record, err
		}
		return r.ackDelegationOutcome(ctx, record, outcome.EventID)
	}
	return r.expireDelegation(ctx, record)
}

func obsoleteDelegatedRoleOutcome(record DurableRun, outcome event.Outcome) bool {
	w := record.Waiting
	if w == nil || record.Retry == nil || w.DeliveryNumber <= 0 || outcome.Validate() != nil {
		return false
	}
	for n := 0; n < w.DeliveryNumber; n++ {
		key := fmt.Sprintf("%s:%d", w.DelegationID, n)
		if outcome.DeliveryKey == key && outcome.CommandEventID == event.CommandEventID(key) &&
			outcome.TaskID == w.ChildID && outcome.DelegationID == w.DelegationID &&
			outcome.CorrelationID == fmt.Sprintf("%s:%s:delivery:%d", record.State.RunID, record.ActiveExecutionKey, n) {
			return true
		}
	}
	return false
}

func decodeDelegationOutcome(outcome event.Outcome) (GraphRoleOutcome, bool) {
	if outcome.Status != event.OutcomeSucceeded || len(outcome.Payload) == 0 {
		return GraphRoleOutcome{}, false
	}
	var decoded GraphRoleOutcome
	if json.Unmarshal(outcome.Payload, &decoded) != nil || decoded.Result.FanOut == nil {
		return GraphRoleOutcome{}, false
	}
	return decoded, true
}

func (r *DurableRuntime) startDelegationJoin(ctx context.Context, record DurableRun, parent RoleRunResult, startedAt, finishedAt time.Time, parentEventID string) (DurableRun, error) {
	if record.State.CurrentRole != RolePlanner {
		return record, errors.New("multiagent: only the planner may start a fan-out join")
	}
	if parent.FanOut == nil {
		return record, errors.New("multiagent: fan-out plan is missing")
	}
	if err := parent.FanOut.Validate(); err != nil {
		return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: err}
	}
	now := r.now().UTC()
	deadline := now.Add(r.delegation.Deadline)
	children := make([]DelegationJoinChild, 0, len(parent.FanOut.Tasks))
	lanes := []FanOutLane{FanOutResearch, FanOutImplementation, FanOutReview}
	byLane := make(map[FanOutLane]FanOutTask, len(parent.FanOut.Tasks))
	for _, task := range parent.FanOut.Tasks {
		byLane[task.Lane] = task
	}
	for _, lane := range lanes {
		task := byLane[lane]
		childID := fmt.Sprintf("%s:%s", record.State.CurrentTask.ID, lane)
		executionKey := record.ActiveExecutionKey + ":fanout:" + string(lane)
		delegationID := "graph:join:" + record.ActiveExecutionKey + ":" + string(lane)
		deliveryKey := delegationID + ":0"
		visitID := executionKey
		retryState := &RetryState{VisitID: visitID, ExecutionKey: executionKey, Role: task.Role,
			AttemptID: retryAttemptID(visitID, 1), Number: 1, MaxRecoveries: maxRoleRecoveryRetries,
			ReplaySafe: true, MutationLedgerReady: true}
		children = append(children, DelegationJoinChild{ChildID: childID, Lane: lane, Role: task.Role,
			Task: TaskReference{ID: childID, Description: task.Description}, ExecutionKey: executionKey,
			DelegationID: delegationID, JoinID: "join:" + record.ActiveExecutionKey, DeliveryKey: deliveryKey, CommandEventID: event.CommandEventID(deliveryKey),
			CorrelationID: record.State.RunID + ":" + executionKey + ":delivery:0", Deadline: deadline,
			DeliveryAttemptID: executionKey + ":delivery:0", DeliveryNumber: 0, Retry: retryState, DispatchPending: true})
	}
	record.DelegationJoin = &DelegationJoinState{JoinID: "join:" + record.ActiveExecutionKey,
		ParentExecutionKey: record.ActiveExecutionKey, ParentRole: record.State.CurrentRole,
		ParentResult: cloneRoleRunResult(parent), ParentStartedAt: startedAt, ParentFinishedAt: finishedAt,
		StartedAt: now, Deadline: deadline, FailurePolicy: "wait_for_all_then_fail", Children: children}
	role := record.State.CurrentRole
	roleState := record.State.RoleStates[role]
	roleState.Status = RoleStatusWaiting
	roleState.UpdatedAt = now
	record.State.RoleStates[role] = roleState
	record.State.Status = RunStatusPaused
	record.State.UpdatedAt = now
	record.Phase = CheckpointWaiting
	record.Waiting = &WaitingState{Kind: "delegation_join", Reason: "waiting for parallel delegated roles", SafeToRetry: false,
		Since: now, JoinID: record.DelegationJoin.JoinID, StartedAt: now, Deadline: deadline}
	record.PendingDelegationOutcomeAck = parentEventID
	r.supervisor.emitRun(event.EventMultiAgentRunPaused, record.State, record.Waiting.Reason)
	r.supervisor.emit(event.EventMultiAgentFanoutStarted, record.State, map[string]any{"run_id": record.State.RunID, "join_id": record.DelegationJoin.JoinID, "status": "started"})
	var err error
	if record, err = r.checkpoint(ctx, record); err != nil {
		return record, err
	}
	if record, err = r.ackDelegationOutcome(ctx, record, parentEventID); err != nil {
		return record, err
	}
	if record, err = r.dispatchPendingDelegationJoin(ctx, record); err != nil {
		return record, err
	}
	return record, waitingError(record)
}

func (r *DurableRuntime) dispatchPendingDelegationJoin(ctx context.Context, record DurableRun) (DurableRun, error) {
	if record.Waiting == nil || record.Waiting.Kind != "delegation_join" || record.DelegationJoin == nil {
		return record, errors.New("multiagent: no pending delegation join")
	}
	if r.delegation.RequireWorkspaceID && strings.TrimSpace(record.State.WorkspaceID) == "" {
		return record, errors.New("multiagent: delegated graph role requires a persisted workspace identity")
	}
	for i := range record.DelegationJoin.Children {
		child := &record.DelegationJoin.Children[i]
		if !child.DispatchPending {
			continue
		}
		if child.Retry != nil && !child.Retry.NextAt.IsZero() && r.now().UTC().Before(child.Retry.NextAt) {
			continue
		}
		// Once due, clear the delay in the same in-memory checkpoint that marks
		// this delivery dispatched. The delivery key remains stable across a
		// restart, so the transport can deduplicate a publish/checkpoint race.
		if child.Retry != nil {
			child.Retry.NextAt = time.Time{}
		}
		cfg, _ := r.supervisor.graph.RoleConfig(child.Role)
		view := r.supervisor.runView(record.State)
		view.CurrentRole = child.Role
		view.Task = child.Task
		view.ExecutionKey = child.ExecutionKey
		attemptID, recoveryFeedback := "", ""
		if child.Retry != nil {
			attemptID, recoveryFeedback = child.Retry.AttemptID, child.Retry.RecoveryFeedback
		}
		payload, err := json.Marshal(GraphRoleCommand{ChildID: child.ChildID, Role: child.Role, Task: child.Task,
			ExecutionKey: child.ExecutionKey, RunID: record.State.RunID, WorkspaceID: record.State.WorkspaceID,
			DelegationID: child.DelegationID, JoinID: child.JoinID, Lane: child.Lane, DeliveryKey: child.DeliveryKey, CorrelationID: child.CorrelationID,
			CommandEventID: child.CommandEventID, Deadline: child.Deadline, DeliveryAttemptID: child.DeliveryAttemptID,
			AttemptID: attemptID, RecoveryFeedback: recoveryFeedback,
			Request: RoleRunRequest{Run: view, RoleConfig: cloneRoleConfig(cfg), FanOutAvailable: false}})
		if err != nil {
			return record, err
		}
		cmd := event.Command{EventID: child.CommandEventID, Type: GraphRoleDelegationCommandType, RunID: record.State.RunID,
			TaskID: child.ChildID, DelegationID: child.DelegationID, CorrelationID: child.CorrelationID,
			JoinID: record.DelegationJoin.JoinID, Lane: string(child.Lane),
			IdempotencyKey: child.DeliveryKey, Deadline: child.Deadline, SchemaVersion: event.CommandSchemaVersion, Payload: payload}
		if err := r.delegation.Dispatcher.Dispatch(ctx, r.delegation.Subject, cmd); err != nil {
			return record, fmt.Errorf("multiagent: dispatch fan-out lane %s: %w", child.Lane, err)
		}
		child.DispatchPending = false
		r.supervisor.emit(event.EventMultiAgentFanoutChildDispatched, record.State, map[string]any{"run_id": record.State.RunID, "join_id": record.DelegationJoin.JoinID, "lane": string(child.Lane), "role": string(child.Role), "child_id": child.ChildID, "delivery_key": child.DeliveryKey, "status": "dispatched"})
		if record, err = r.checkpoint(ctx, record); err != nil {
			return record, err
		}
	}
	return record, nil
}

func (r *DurableRuntime) resumeDelegationJoin(ctx context.Context, record DurableRun) (DurableRun, error) {
	if r.delegation == nil || r.delegation.Outcomes == nil || record.DelegationJoin == nil {
		return record, errors.New("multiagent: delegation join source is unavailable")
	}
	outcomes, err := r.delegation.Outcomes.PendingOutcomes(ctx, record.State.RunID)
	if err != nil {
		return record, err
	}
	if len(outcomes) == 0 {
		return r.expireDelegationJoin(ctx, record)
	}
	for _, outcome := range outcomes {
		idx := -1
		for i := range record.DelegationJoin.Children {
			if record.DelegationJoin.Children[i].DeliveryKey == outcome.DeliveryKey {
				idx = i
				break
			}
		}
		if idx < 0 {
			if obsoleteDelegationChildOutcome(record, outcome) {
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
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: errors.New("fan-out outcome delivery key does not match join")}
		}
		child := &record.DelegationJoin.Children[idx]
		if err := matchDelegationJoinOutcome(record.State.RunID, child, outcome); err != nil {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: err}
		}
		if outcome.OccurredAt.After(child.Deadline) {
			return r.expireDelegationJoin(ctx, record)
		}
		if outcome.Sequence <= child.LastSequence {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: fmt.Errorf("stale fan-out outcome sequence %d", outcome.Sequence)}
		}
		if outcome.Status.Terminal() && child.Retry != nil && outcome.Status != event.OutcomeSucceeded {
			var stale GraphRoleOutcome
			if json.Unmarshal(outcome.Payload, &stale) == nil && stale.Failure != nil &&
				stale.Failure.DeliveryAttemptID == child.DeliveryAttemptID && stale.Failure.AttemptID != child.Retry.AttemptID {
				child.LastSequence = outcome.Sequence
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
		}
		child.LastSequence = outcome.Sequence
		if outcome.Status.Terminal() {
			scheduled := false
			if outcome.Status == event.OutcomeSucceeded {
				var decoded GraphRoleOutcome
				if json.Unmarshal(outcome.Payload, &decoded) != nil || decoded.Failure != nil || decoded.Result.FanOut != nil {
					return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: errors.New("fan-out child result is invalid or nested")}
				}
				if err := validateRoleRunResult(r.supervisor.graph, decoded.Result); err != nil {
					return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: err}
				}
				cloned := cloneRoleRunResult(decoded.Result)
				if child.Retry != nil {
					cloned.TokenUsage = addTokenUsage(child.Retry.AccumulatedUsage, cloned.TokenUsage)
					cloned.Retries += child.Retry.RecoveryNumber
					cloned.LocalIterations += child.Retry.AccumulatedIterations
				}
				child.Result = &cloned
				child.Status = outcome.Status
			} else {
				var decoded GraphRoleOutcome
				failureValid := child.Retry != nil && json.Unmarshal(outcome.Payload, &decoded) == nil && decoded.Failure != nil &&
					decoded.Failure.DeliveryAttemptID == child.DeliveryAttemptID && decoded.Failure.AttemptID == child.Retry.AttemptID
				if failureValid && decoded.Failure.Retryable && child.Retry.RecoveryNumber < child.Retry.MaxRecoveries &&
					child.Retry.ReplaySafe && !child.Retry.MutationPossible && !child.Retry.UnresolvedMutationFailure && !child.Retry.UnattributedMutation &&
					!r.now().UTC().Add(roleRetryDelay(child.ExecutionKey, child.Retry.RecoveryNumber+1, decoded.Failure.RetryAfter)).After(child.Deadline) {
					child.Retry.RecoveryNumber++
					child.Retry.AccumulatedUsage = addTokenUsage(child.Retry.AccumulatedUsage, decoded.Failure.Usage)
					child.Retry.Number++
					child.Retry.AttemptID = retryAttemptID(child.Retry.VisitID, child.Retry.Number)
					child.Retry.FailureClass = safeRetryCode(decoded.Failure.Class)
					child.Retry.FailureCode = safeRetryCode(decoded.Failure.Code)
					child.Retry.RecoveryFeedback = roleFailureFeedback(child.Role, child.Retry.FailureCode,
						decoded.Failure.CorrectionField, decoded.Failure.ExpectedType, decoded.Failure.AllowedValues)
					child.Retry.NextAt = r.now().UTC().Add(roleRetryDelay(child.ExecutionKey, child.Retry.RecoveryNumber, decoded.Failure.RetryAfter))
					child.DeliveryNumber++
					child.DeliveryAttemptID = fmt.Sprintf("%s:delivery:%d", child.ExecutionKey, child.DeliveryNumber)
					child.DeliveryKey = fmt.Sprintf("%s:%d", child.DelegationID, child.DeliveryNumber)
					child.CommandEventID = event.CommandEventID(child.DeliveryKey)
					child.CorrelationID = fmt.Sprintf("%s:%s:delivery:%d", record.State.RunID, child.ExecutionKey, child.DeliveryNumber)
					child.LastSequence = 0
					child.Status, child.Result, child.Error, child.DispatchPending = "", nil, "", true
					r.supervisor.emit(event.EventMultiAgentRetryScheduled, record.State, map[string]any{
						"run_id": record.State.RunID, "workflow_id": record.State.WorkflowID, "role": string(child.Role),
						"execution_key": child.ExecutionKey, "visit_id": child.Retry.VisitID,
						"attempt_id": child.Retry.AttemptID, "attempt": child.Retry.Number,
						"delivery_attempt_id": child.DeliveryAttemptID, "recovery_number": child.Retry.RecoveryNumber,
						"max_recoveries": child.Retry.MaxRecoveries, "failure_class": child.Retry.FailureClass,
						"failure_code": child.Retry.FailureCode, "retryable": true,
						"next_at": child.Retry.NextAt.UTC().Format(time.RFC3339Nano)})
					scheduled = true
				} else {
					child.Status = outcome.Status
					child.Error = "child outcome " + string(outcome.Status)
					if failureValid {
						child.Retry.AccumulatedUsage = addTokenUsage(child.Retry.AccumulatedUsage, decoded.Failure.Usage)
						child.Retry.FailureClass = safeRetryCode(decoded.Failure.Class)
						child.Retry.FailureCode = safeRetryCode(decoded.Failure.Code)
					}
					child.TerminalFailure = terminalChildFailure(child, decoded.Failure, failureValid, r.now().UTC())
				}
			}
			if child.Status.Terminal() && !scheduled {
				r.supervisor.emit(event.EventMultiAgentFanoutChildCompleted, record.State, fanOutChildCompletionPayload(record.State.RunID, *child))
			}
		} else if outcome.Status != event.OutcomeAccepted && outcome.Status != event.OutcomeProgress {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: fmt.Errorf("unsupported fan-out outcome status %q", outcome.Status)}
		}
		record.PendingDelegationOutcomeAck = outcome.EventID
		if record, err = r.checkpoint(ctx, record); err != nil {
			return record, err
		}
		if record, err = r.ackDelegationOutcome(ctx, record, outcome.EventID); err != nil {
			return record, err
		}
	}
	allTerminal := true
	failed := false
	for _, child := range record.DelegationJoin.Children {
		if !child.Status.Terminal() {
			allTerminal = false
		}
		if child.Status.Terminal() && child.Status != event.OutcomeSucceeded {
			failed = true
		}
	}
	if allTerminal {
		if failed {
			terminalFailures := terminalFanOutFailures(record.DelegationJoin.Children)
			record.DelegationJoin = nil
			return r.failDelegationWithFanOut(ctx, record, "delegation_join_failed", errors.New("one or more fan-out lanes failed"), terminalFailures)
		}
		return r.completeDelegationJoin(ctx, record)
	}
	// All durable child facts have now been checkpointed. Apply deadline policy
	// only after that pass so missing siblings are finalized without discarding
	// a trusted terminal child result.
	return r.expireDelegationJoin(ctx, record)
}

func obsoleteDelegationChildOutcome(record DurableRun, outcome event.Outcome) bool {
	if record.DelegationJoin == nil || outcome.Validate() != nil {
		return false
	}
	for _, child := range record.DelegationJoin.Children {
		for n := 0; n < child.DeliveryNumber; n++ {
			key := fmt.Sprintf("%s:%d", child.DelegationID, n)
			if outcome.DeliveryKey == key && outcome.CommandEventID == event.CommandEventID(key) &&
				outcome.TaskID == child.ChildID && outcome.DelegationID == child.DelegationID && outcome.JoinID == child.JoinID &&
				outcome.Lane == string(child.Lane) && outcome.CorrelationID == fmt.Sprintf("%s:%s:delivery:%d", record.State.RunID, child.ExecutionKey, n) {
				return true
			}
		}
	}
	return false
}

func (r *DurableRuntime) expireDelegationJoin(ctx context.Context, record DurableRun) (DurableRun, error) {
	if record.Waiting != nil && r.now().UTC().Before(record.Waiting.Deadline) {
		return record, nil
	}
	if record.DelegationJoin != nil {
		joinID := record.DelegationJoin.JoinID
		for i := range record.DelegationJoin.Children {
			if !record.DelegationJoin.Children[i].Status.Terminal() {
				record.DelegationJoin.Children[i].Status = event.OutcomeTimedOut
				record.DelegationJoin.Children[i].Error = "fan-out deadline exceeded"
				r.supervisor.emit(event.EventMultiAgentFanoutChildCompleted, record.State, fanOutChildCompletionPayload(record.State.RunID, record.DelegationJoin.Children[i]))
			}
		}
		r.supervisor.emit(event.EventMultiAgentFanoutJoined, record.State, map[string]any{"run_id": record.State.RunID, "join_id": joinID, "status": string(event.OutcomeTimedOut), "reason": "deadline exceeded", "child_summaries": fanOutChildSummaries(record.DelegationJoin.Children)})
		record.DelegationJoin = nil
	}
	return r.failDelegation(ctx, record, "delegation_timeout", errors.New("fan-out deadline exceeded"))
}

func (r *DurableRuntime) completeDelegationJoin(ctx context.Context, record DurableRun) (DurableRun, error) {
	join := record.DelegationJoin
	if join == nil {
		return record, errors.New("multiagent: missing delegation join")
	}
	merged := cloneRoleRunResult(join.ParentResult)
	merged.FanOut = nil
	for _, child := range join.Children {
		if child.Result == nil {
			return record, errors.New("multiagent: incomplete delegation join")
		}
		merged.Proposals = append(merged.Proposals, child.Result.Proposals...)
		if child.Result.OutgoingHandoff != nil {
			if merged.OutgoingHandoff == nil {
				merged.OutgoingHandoff = &HandoffDraft{}
			}
			h := child.Result.OutgoingHandoff
			merged.OutgoingHandoff.Artifacts = append(merged.OutgoingHandoff.Artifacts, h.Artifacts...)
			merged.OutgoingHandoff.Evidence = append(merged.OutgoingHandoff.Evidence, h.Evidence...)
			merged.OutgoingHandoff.UnresolvedIssues = append(merged.OutgoingHandoff.UnresolvedIssues, h.UnresolvedIssues...)
			merged.OutgoingHandoff.Notes += "\n" + string(child.Lane) + ": " + h.Notes
		}
	}
	adoption, adoptionErr := r.fanOutAdoptionCandidate(record, join, merged)
	if adoptionErr != nil {
		record.DelegationJoin = nil
		return r.failDelegation(ctx, record, "fanout_adoption_scope", adoptionErr)
	}
	role := join.ParentRole
	cfg, _ := r.supervisor.graph.RoleConfig(role)
	record.DelegationJoin = nil
	r.supervisor.emit(event.EventMultiAgentFanoutJoined, record.State, map[string]any{"run_id": record.State.RunID, "join_id": join.JoinID, "status": "joined", "child_summaries": fanOutChildSummaries(join.Children)})
	record.Waiting = nil
	record.State.Status = RunStatusRunning
	rs := record.State.RoleStates[role]
	rs.Status = RoleStatusRunning
	record.State.RoleStates[role] = rs
	record.Phase = CheckpointRoleRunning
	if len(merged.Proposals) > 0 {
		appendRetryHistory(&record, record.Retry)
		record.Retry = nil
		return r.pauseForProposals(ctx, record, role, merged, join.ParentStartedAt, join.ParentFinishedAt, adoption)
	}
	if adoption != nil {
		return r.failDelegation(ctx, record, "fanout_adoption_without_proposal", errors.New("implementation adoption requires an exact approved proposal"))
	}
	return r.finishPreparedRole(ctx, record, role, cfg, merged, join.ParentStartedAt, join.ParentFinishedAt)
}

func terminalChildFailure(child *DelegationJoinChild, failure *RoleExecutionFailure, valid bool, now time.Time) *TerminalRoleFailure {
	if child == nil {
		return nil
	}
	classified := RoleExecutionFailure{Class: "unknown", Code: "unknown"}
	if valid && failure != nil {
		classified = *failure
	}
	summary := sanitizeTerminalRoleFailure(child.Role, classified)
	if child.Retry != nil {
		summary.AttemptID = child.Retry.AttemptID
		summary.RecoveryNumber = child.Retry.RecoveryNumber
		summary.MaxRecoveries = child.Retry.MaxRecoveries
	}
	switch {
	case !valid:
		summary.Reason = "unmatched_outcome"
	case !summary.Retryable:
		summary.Reason = "not_retryable"
	case child.Retry == nil:
		summary.Reason = "retry_state_unavailable"
	case child.Retry.RecoveryNumber >= child.Retry.MaxRecoveries:
		summary.Reason = "recovery_limit_reached"
	case child.Retry.MutationPossible || child.Retry.UnresolvedMutationFailure || child.Retry.UnattributedMutation || !child.Retry.ReplaySafe:
		summary.Reason = "unsafe_replay_state"
	case !child.Deadline.IsZero() && now.Add(roleRetryDelay(child.ExecutionKey, child.Retry.RecoveryNumber+1, classified.RetryAfter)).After(child.Deadline):
		summary.Reason = "deadline_would_expire"
	default:
		summary.Reason = "retry_not_scheduled"
	}
	return &summary
}

func terminalFanOutFailures(children []DelegationJoinChild) []TerminalFanOutFailure {
	var failures []TerminalFanOutFailure
	for _, child := range children {
		if child.TerminalFailure == nil {
			continue
		}
		failures = append(failures, TerminalFanOutFailure{
			JoinID: child.JoinID, ChildID: child.ChildID, Lane: child.Lane, Role: child.Role,
			Failure: *child.TerminalFailure,
		})
	}
	return failures
}

func fanOutChildCompletionPayload(runID string, child DelegationJoinChild) map[string]any {
	payload := map[string]any{
		"run_id": runID, "join_id": child.JoinID, "lane": string(child.Lane),
		"role": string(child.Role), "child_id": child.ChildID, "status": string(child.Status),
	}
	if failure := child.TerminalFailure; failure != nil {
		payload["attempt_id"] = failure.AttemptID
		payload["failure_class"] = failure.Class
		payload["failure_code"] = failure.Code
		payload["failure_reason"] = failure.Reason
		payload["retryable"] = failure.Retryable
		payload["recovery_number"] = failure.RecoveryNumber
		payload["max_recoveries"] = failure.MaxRecoveries
		if failure.CorrectionField != "" {
			payload["correction_field"] = failure.CorrectionField
		}
		if failure.ExpectedType != "" {
			payload["expected_type"] = failure.ExpectedType
		}
		if len(failure.AllowedValues) > 0 {
			payload["allowed_values"] = append([]string(nil), failure.AllowedValues...)
		}
	}
	return payload
}

func fanOutChildSummaries(children []DelegationJoinChild) []event.MultiAgentFanoutChildSummary {
	summaries := make([]event.MultiAgentFanoutChildSummary, 0, len(children))
	for _, child := range children {
		summary := event.MultiAgentFanoutChildSummary{
			ChildID: child.ChildID, Lane: string(child.Lane), Role: string(child.Role),
			ExecutionKey: child.ExecutionKey, DeliveryKey: child.DeliveryKey, Status: string(child.Status),
		}
		if child.Result != nil {
			meta := child.Result.Metadata
			summary.Provider, summary.Model = boundedExecutionLabel(meta.Provider), boundedExecutionLabel(meta.Model)
			usage := child.Result.TokenUsage
			summary.TokenUsage = event.TokenUsage{
				PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
				TotalTokens: usage.TotalTokens, EstimatedCostUsd: usage.EstimatedCostUsd,
			}
			if !meta.StartedAt.IsZero() {
				summary.StartedAt = meta.StartedAt.UTC().Format(time.RFC3339Nano)
			}
			if !meta.FinishedAt.IsZero() {
				summary.FinishedAt = meta.FinishedAt.UTC().Format(time.RFC3339Nano)
			}
			if !meta.StartedAt.IsZero() && !meta.FinishedAt.IsZero() && !meta.FinishedAt.Before(meta.StartedAt) {
				summary.DurationMs = meta.FinishedAt.Sub(meta.StartedAt).Milliseconds()
			}
		} else if child.Retry != nil {
			usage := child.Retry.AccumulatedUsage
			summary.TokenUsage = event.TokenUsage{PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
				TotalTokens: usage.TotalTokens, EstimatedCostUsd: usage.EstimatedCostUsd}
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func boundedExecutionLabel(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func (r *DurableRuntime) fanOutAdoptionCandidate(record DurableRun, join *DelegationJoinState, merged RoleRunResult) (*FanOutAdoptionState, error) {
	var implementation *DelegationJoinChild
	for i := range join.Children {
		child := &join.Children[i]
		if child.Lane == FanOutImplementation {
			implementation = child
		}
	}
	if implementation == nil || implementation.Result == nil || len(implementation.Result.Proposals) == 0 {
		return nil, nil
	}
	transition, transitionErr := r.supervisor.graph.Resolve(join.ParentRole, merged.Outcome)
	if transitionErr != nil || transition.Terminal != "" || transition.To != RoleDeveloper {
		return nil, nil
	}
	fail := func(reason string) (*FanOutAdoptionState, error) {
		return nil, errors.New(reason)
	}
	if len(join.Children) != 3 || join.ParentRole != RolePlanner || record.State.CurrentRole != join.ParentRole ||
		join.ParentExecutionKey == "" || join.ParentExecutionKey != record.ActiveExecutionKey ||
		join.JoinID != "join:"+join.ParentExecutionKey || join.ParentExecutionKey != roleExecutionKey(record.State.RunID, join.ParentRole, record.State.RoleStates[join.ParentRole].Visits) ||
		record.State.CurrentTask.ID == "" || record.State.WorkflowID == "" || record.State.WorkflowFingerprint == "" ||
		record.State.WorkspaceID == "" || record.State.RoleStates[join.ParentRole].Visits < 1 {
		return fail("fan-out implementation proposal has incomplete parent scope")
	}
	if err := join.Validate(record.State.RunID, record.State.CurrentRole); err != nil {
		return fail("fan-out implementation proposal has an invalid canonical join")
	}
	if join.ParentResult.FanOut == nil || join.ParentResult.FanOut.Validate() != nil {
		return fail("fan-out implementation proposal has no trusted parent plan")
	}
	plan := make(map[FanOutLane]FanOutTask, len(join.ParentResult.FanOut.Tasks))
	for _, task := range join.ParentResult.FanOut.Tasks {
		plan[task.Lane] = task
	}
	for _, child := range join.Children {
		want := plan[child.Lane]
		wantChildID := fmt.Sprintf("%s:%s", record.State.CurrentTask.ID, child.Lane)
		wantExecutionKey := join.ParentExecutionKey + ":fanout:" + string(child.Lane)
		wantDelegationID := "graph:join:" + join.ParentExecutionKey + ":" + string(child.Lane)
		wantDeliveryKey := fmt.Sprintf("%s:%d", wantDelegationID, child.DeliveryNumber)
		if child.Status != event.OutcomeSucceeded || child.Result == nil || child.JoinID != join.JoinID ||
			child.ChildID != wantChildID || child.Role != want.Role || child.Task.Description != want.Description ||
			child.ExecutionKey != wantExecutionKey || child.DelegationID != wantDelegationID ||
			child.DeliveryKey != wantDeliveryKey || child.DeliveryAttemptID != fmt.Sprintf("%s:delivery:%d", wantExecutionKey, child.DeliveryNumber) ||
			child.Retry == nil || child.Retry.AttemptID != retryAttemptID(child.ExecutionKey, child.Retry.Number) ||
			child.CommandEventID != event.CommandEventID(wantDeliveryKey) ||
			child.CorrelationID != fmt.Sprintf("%s:%s:delivery:%d", record.State.RunID, wantExecutionKey, child.DeliveryNumber) ||
			child.Result.Metadata.WorkspaceID != record.State.WorkspaceID {
			return fail("fan-out implementation proposal has incomplete child execution identity")
		}
	}
	if implementation.Role != RoleDeveloper || implementation.ExecutionKey == "" ||
		implementation.Result.Metadata.StartedAt.IsZero() || implementation.Result.Metadata.FinishedAt.IsZero() {
		return fail("fan-out implementation result is not a complete Developer execution")
	}
	developerNodeID, ok := r.supervisor.graph.NodeIDForRole(RoleDeveloper)
	if !ok {
		return fail("fan-out implementation has no configured Developer successor node")
	}
	if !sameProposalSet(implementation.Result.Proposals, merged.Proposals) {
		return fail("fan-out implementation proposals do not exactly match the joined proposal set")
	}
	result := cloneRoleRunResult(*implementation.Result)
	return &FanOutAdoptionState{
		RunID: record.State.RunID, Task: record.State.CurrentTask, WorkflowID: record.State.WorkflowID,
		WorkflowFingerprint: record.State.WorkflowFingerprint, WorkspaceID: record.State.WorkspaceID,
		ParentRole: join.ParentRole, ParentExecutionKey: join.ParentExecutionKey,
		ParentVisit: record.State.RoleStates[join.ParentRole].Visits, JoinID: join.JoinID,
		ChildID: implementation.ChildID, ChildExecutionKey: implementation.ExecutionKey,
		DelegationID: implementation.DelegationID, DeliveryKey: implementation.DeliveryKey,
		CommandEventID: implementation.CommandEventID, CorrelationID: implementation.CorrelationID,
		DeliveryAttemptID: implementation.DeliveryAttemptID, DeliveryNumber: implementation.DeliveryNumber,
		AttemptID: implementation.Retry.AttemptID, AttemptNumber: implementation.Retry.Number,
		RecoveryNumber: implementation.Retry.RecoveryNumber, MaxRecoveries: implementation.Retry.MaxRecoveries,
		ExpectedRole: RoleDeveloper, ExpectedNodeID: developerNodeID,
		ExpectedVisit: record.State.RoleStates[RoleDeveloper].Visits + 1,
		Proposals:     cloneProposalReferences(implementation.Result.Proposals),
		Result:        result,
	}, nil
}

func sameProposalSet(left, right []ProposalReference) bool {
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	ids := make(map[string]struct{}, len(left))
	for _, proposal := range left {
		key := proposal.ProposalID + "\x00" + proposal.ApprovalID
		if proposal.ProposalID == "" || proposal.ApprovalID == "" {
			return false
		}
		if _, ok := ids[key]; ok {
			return false
		}
		ids[key] = struct{}{}
	}
	for _, proposal := range right {
		key := proposal.ProposalID + "\x00" + proposal.ApprovalID
		if _, ok := ids[key]; !ok {
			return false
		}
		delete(ids, key)
	}
	return len(ids) == 0
}

func matchDelegationJoinOutcome(runID string, child *DelegationJoinChild, outcome event.Outcome) error {
	if err := outcome.Validate(); err != nil {
		return err
	}
	if child == nil || outcome.RunID != runID || outcome.TaskID != child.ChildID || outcome.DelegationID != child.DelegationID || outcome.JoinID != child.JoinID || outcome.Lane != string(child.Lane) || outcome.DeliveryKey != child.DeliveryKey || outcome.CommandEventID != child.CommandEventID || outcome.CorrelationID != child.CorrelationID || outcome.CausationID != child.CommandEventID {
		return errors.New("fan-out outcome identity does not match checkpoint")
	}
	return nil
}

// updateDelegationJoinChild reloads and checkpoints one child in the active
// join. A conflicting run checkpoint is retried from the latest durable state
// so concurrent child workers cannot overwrite one another with stale copies.
func updateDelegationJoinChild(
	ctx context.Context,
	store DurableRunStore,
	runID, joinID, childID, deliveryAttemptID, attemptID string,
	update func(*DelegationJoinChild) error,
	events func(DurableRun) []event.Event,
) (DurableRun, error) {
	if store == nil || update == nil || strings.TrimSpace(runID) == "" ||
		strings.TrimSpace(joinID) == "" || strings.TrimSpace(childID) == "" {
		return DurableRun{}, errors.New("multiagent: child update identity is required")
	}
	for attempt := 0; attempt < 16; attempt++ {
		record, err := store.Load(ctx, runID)
		if err != nil {
			return DurableRun{}, err
		}
		if record.DelegationJoin == nil || record.DelegationJoin.JoinID != joinID ||
			record.Waiting == nil || record.Waiting.Kind != "delegation_join" {
			return DurableRun{}, errors.New("multiagent: child update does not match active join")
		}
		var child *DelegationJoinChild
		for i := range record.DelegationJoin.Children {
			if record.DelegationJoin.Children[i].ChildID == childID {
				child = &record.DelegationJoin.Children[i]
				break
			}
		}
		if child == nil || child.JoinID != joinID || child.DeliveryAttemptID != deliveryAttemptID ||
			child.Retry == nil || child.Retry.AttemptID != attemptID {
			return DurableRun{}, errors.New("multiagent: child update identity is stale")
		}
		before := *child
		if err := update(child); err != nil {
			return DurableRun{}, err
		}
		if child.ChildID != before.ChildID || child.Lane != before.Lane || child.Role != before.Role ||
			child.Task != before.Task || child.ExecutionKey != before.ExecutionKey ||
			child.DelegationID != before.DelegationID || child.JoinID != before.JoinID ||
			child.Deadline != before.Deadline {
			return DurableRun{}, errors.New("multiagent: child update cannot change execution identity")
		}
		if child.Retry == nil || child.Retry.Role != before.Role || child.Retry.ExecutionKey != before.ExecutionKey ||
			child.Retry.VisitID != before.ExecutionKey || child.Retry.AttemptID != retryAttemptID(child.Retry.VisitID, child.Retry.Number) ||
			child.Retry.Number != child.Retry.RecoveryNumber+1 || child.Retry.RecoveryNumber < 0 ||
			child.Retry.MaxRecoveries < 0 || child.Retry.MaxRecoveries > maxRoleRecoveryRetries ||
			child.Retry.RecoveryNumber > child.Retry.MaxRecoveries ||
			child.DeliveryAttemptID != fmt.Sprintf("%s:delivery:%d", child.ExecutionKey, child.DeliveryNumber) ||
			child.DeliveryKey != fmt.Sprintf("%s:%d", child.DelegationID, child.DeliveryNumber) ||
			child.CommandEventID != event.CommandEventID(child.DeliveryKey) ||
			child.CorrelationID != fmt.Sprintf("%s:%s:delivery:%d", runID, child.ExecutionKey, child.DeliveryNumber) {
			return DurableRun{}, errors.New("multiagent: child update produced invalid retry or delivery identity")
		}
		var checkpointEvents []event.Event
		if events != nil {
			checkpointEvents = events(record)
		}
		saved, err := store.Checkpoint(ctx, record.Revision, record, checkpointEvents)
		if err == nil {
			return saved, nil
		}
		if !errors.Is(err, ErrRevisionConflict) {
			return DurableRun{}, err
		}
	}
	return DurableRun{}, errors.New("multiagent: child update exceeded revision conflict retries")
}

func cloneRoleRunResult(result RoleRunResult) RoleRunResult {
	cloned := result
	cloned.OutgoingHandoff = cloneHandoffDraft(result.OutgoingHandoff)
	cloned.Proposals = append([]ProposalReference(nil), result.Proposals...)
	cloned.FanOut = cloneFanOutPlan(result.FanOut)
	return cloned
}

func (r *DurableRuntime) expireDelegation(ctx context.Context, record DurableRun) (DurableRun, error) {
	if record.Waiting != nil && !r.now().UTC().Before(record.Waiting.Deadline) {
		return r.timeoutDelegation(ctx, record, "delegated role deadline exceeded")
	}
	return record, nil
}

func (r *DurableRuntime) timeoutDelegation(ctx context.Context, record DurableRun, reason string) (DurableRun, error) {
	return r.failDelegation(ctx, record, "delegation_timeout", errors.New(reason))
}

func (r *DurableRuntime) failDelegation(ctx context.Context, record DurableRun, kind string, cause error) (DurableRun, error) {
	return r.failDelegationWithFanOut(ctx, record, kind, cause, nil)
}

func (r *DurableRuntime) failDelegationWithFanOut(ctx context.Context, record DurableRun, kind string, cause error, failures []TerminalFanOutFailure) (DurableRun, error) {
	role := record.State.CurrentRole
	state, terminalErr := r.supervisor.failRole(record.State, role, cause)
	record.State = state
	record.Phase = CheckpointTerminal
	record.Failure = persistedFailure(kind, terminalErr, r.now())
	record.Failure.FanOutFailures = append([]TerminalFanOutFailure(nil), failures...)
	record.PendingTransition = nil
	record.Waiting = nil
	record, checkpointErr := r.checkpoint(ctx, record)
	if checkpointErr != nil {
		return record, checkpointErr
	}
	return record, terminalErr
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
