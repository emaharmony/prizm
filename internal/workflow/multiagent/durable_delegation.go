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
	ChildID        string         `json:"child_id"`
	Role           Role           `json:"role"`
	Task           TaskReference  `json:"task"`
	ExecutionKey   string         `json:"execution_key"`
	RunID          string         `json:"run_id"`
	WorkspaceID    string         `json:"workspace_id"`
	DelegationID   string         `json:"delegation_id"`
	JoinID         string         `json:"join_id,omitempty"`
	Lane           FanOutLane     `json:"lane,omitempty"`
	DeliveryKey    string         `json:"delivery_key"`
	CorrelationID  string         `json:"correlation_id"`
	CommandEventID string         `json:"command_event_id"`
	Deadline       time.Time      `json:"deadline"`
	Request        RoleRunRequest `json:"request"`
}

// GraphRoleOutcome is the typed terminal payload returned by a graph worker.
type GraphRoleOutcome struct {
	Result RoleRunResult `json:"result"`
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
	correlationID := record.State.RunID + ":" + record.ActiveExecutionKey
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
	role := record.State.CurrentRole
	roleConfig, _ := r.supervisor.graph.RoleConfig(role)
	request := RoleRunRequest{Run: r.supervisor.runView(record.State), RoleConfig: cloneRoleConfig(roleConfig)}
	payload, err := json.Marshal(GraphRoleCommand{ChildID: waiting.ChildID, Role: role,
		Task: record.State.CurrentTask, ExecutionKey: record.ActiveExecutionKey,
		RunID: record.State.RunID, WorkspaceID: record.State.WorkspaceID,
		DelegationID: waiting.DelegationID, DeliveryKey: waiting.DeliveryKey,
		CorrelationID: waiting.CorrelationID, CommandEventID: waiting.CommandEventID,
		Deadline: waiting.Deadline, Request: request})
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
		if err := matchDelegationOutcome(record.State.RunID, waiting, outcome); err != nil {
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: err}
		}
		if outcome.OccurredAt.After(waiting.Deadline) {
			return r.timeoutDelegation(ctx, record, "delegated role outcome arrived after deadline")
		}
		if decoded, ok := decodeDelegationOutcome(outcome); ok && decoded.Result.FanOut != nil {
			return r.startDelegationJoin(ctx, record, decoded.Result, waiting.StartedAt, outcome.OccurredAt.UTC(), outcome.EventID)
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
		if outcome.Status != event.OutcomeSucceeded {
			record.PendingDelegationOutcomeAck = outcome.EventID
			reason := "delegated role " + string(outcome.Status)
			if len(outcome.Payload) != 0 {
				var failure struct {
					Message string `json:"message"`
				}
				if json.Unmarshal(outcome.Payload, &failure) == nil && strings.TrimSpace(failure.Message) != "" {
					reason += ": " + boundedDiagnostic(errors.New(failure.Message))
				}
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
			record, err = r.pauseForProposals(ctx, record, role, decoded.Result, waiting.StartedAt, outcome.OccurredAt.UTC())
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
		children = append(children, DelegationJoinChild{ChildID: childID, Lane: lane, Role: task.Role,
			Task: TaskReference{ID: childID, Description: task.Description}, ExecutionKey: executionKey,
			DelegationID: delegationID, JoinID: "join:" + record.ActiveExecutionKey, DeliveryKey: deliveryKey, CommandEventID: event.CommandEventID(deliveryKey),
			CorrelationID: record.State.RunID + ":" + executionKey, Deadline: deadline, DispatchPending: true})
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
		cfg, _ := r.supervisor.graph.RoleConfig(child.Role)
		view := r.supervisor.runView(record.State)
		view.CurrentRole = child.Role
		view.Task = child.Task
		view.ExecutionKey = child.ExecutionKey
		payload, err := json.Marshal(GraphRoleCommand{ChildID: child.ChildID, Role: child.Role, Task: child.Task,
			ExecutionKey: child.ExecutionKey, RunID: record.State.RunID, WorkspaceID: record.State.WorkspaceID,
			DelegationID: child.DelegationID, JoinID: child.JoinID, Lane: child.Lane, DeliveryKey: child.DeliveryKey, CorrelationID: child.CorrelationID,
			CommandEventID: child.CommandEventID, Deadline: child.Deadline, Request: RoleRunRequest{Run: view, RoleConfig: cloneRoleConfig(cfg)}})
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
		child.LastSequence = outcome.Sequence
		if outcome.Status.Terminal() {
			child.Status = outcome.Status
			if outcome.Status == event.OutcomeSucceeded {
				var decoded GraphRoleOutcome
				if json.Unmarshal(outcome.Payload, &decoded) != nil || decoded.Result.FanOut != nil {
					return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: errors.New("fan-out child result is invalid or nested")}
				}
				if err := validateRoleRunResult(r.supervisor.graph, decoded.Result); err != nil {
					return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: err}
				}
				cloned := cloneRoleRunResult(decoded.Result)
				child.Result = &cloned
			} else {
				child.Error = "child outcome " + string(outcome.Status)
			}
			r.supervisor.emit(event.EventMultiAgentFanoutChildCompleted, record.State, map[string]any{"run_id": record.State.RunID, "join_id": child.JoinID, "lane": string(child.Lane), "role": string(child.Role), "child_id": child.ChildID, "status": string(child.Status)})
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
			record.DelegationJoin = nil
			return r.failDelegation(ctx, record, "delegation_join_failed", errors.New("one or more fan-out lanes failed"))
		}
		return r.completeDelegationJoin(ctx, record)
	}
	// The caller decides whether an otherwise pending join has expired. A
	// durable child fact must be checkpointed and observed before deadline
	// policy turns its siblings into synthetic timeout outcomes.
	return record, nil
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
				r.supervisor.emit(event.EventMultiAgentFanoutChildCompleted, record.State, map[string]any{"run_id": record.State.RunID, "join_id": joinID, "lane": string(record.DelegationJoin.Children[i].Lane), "role": string(record.DelegationJoin.Children[i].Role), "child_id": record.DelegationJoin.Children[i].ChildID, "status": string(event.OutcomeTimedOut)})
			}
		}
		r.supervisor.emit(event.EventMultiAgentFanoutJoined, record.State, map[string]any{"run_id": record.State.RunID, "join_id": joinID, "status": string(event.OutcomeTimedOut), "reason": "deadline exceeded"})
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
	role := join.ParentRole
	cfg, _ := r.supervisor.graph.RoleConfig(role)
	record.DelegationJoin = nil
	r.supervisor.emit(event.EventMultiAgentFanoutJoined, record.State, map[string]any{"run_id": record.State.RunID, "join_id": join.JoinID, "status": "joined"})
	record.Waiting = nil
	record.State.Status = RunStatusRunning
	rs := record.State.RoleStates[role]
	rs.Status = RoleStatusRunning
	record.State.RoleStates[role] = rs
	record.Phase = CheckpointRoleRunning
	if len(merged.Proposals) > 0 {
		return r.pauseForProposals(ctx, record, role, merged, join.ParentStartedAt, join.ParentFinishedAt)
	}
	return r.finishPreparedRole(ctx, record, role, cfg, merged, join.ParentStartedAt, join.ParentFinishedAt)
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
	role := record.State.CurrentRole
	state, terminalErr := r.supervisor.failRole(record.State, role, cause)
	record.State = state
	record.Phase = CheckpointTerminal
	record.Failure = persistedFailure(kind, terminalErr, r.now())
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
