package multiagent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prism/internal/event"
	"github.com/emaharmony/prism/internal/validation"
)

const durableEventSource = "prism-multi-agent-durable-runtime"

// DurableRuntimeOptions contains deterministic supervisor seams.
type DurableRuntimeOptions struct {
	Clock        func() time.Time
	NewHandoffID func() string
	Logger       *slog.Logger
}

// DurableRuntime advances a run only through atomically persisted checkpoints.
type DurableRuntime struct {
	supervisor *Supervisor
	buffer     *durableEventBuffer
	store      DurableRunStore
	claimer    RunClaimer
	publisher  EventPublisher
	now        func() time.Time
}

// NewDurableRuntime creates the Phase 1 persistence and recovery boundary
// from an already-compiled graph (see NewSupervisor's doc for why this is no
// longer a legacy Definition).
func NewDurableRuntime(
	graph *CompiledGraph,
	runner RoleRunner,
	store DurableRunStore,
	claimer RunClaimer,
	publisher EventPublisher,
	options DurableRuntimeOptions,
) (*DurableRuntime, error) {
	if store == nil {
		return nil, errors.New("multiagent: durable run store is required")
	}
	if claimer == nil {
		return nil, errors.New("multiagent: run claimer is required")
	}
	if publisher == nil {
		return nil, errors.New("multiagent: canonical event publisher is required")
	}
	buffer := &durableEventBuffer{}
	supervisor, err := NewSupervisor(
		graph,
		runner,
		buffer,
		SupervisorOptions(options),
	)
	if err != nil {
		return nil, err
	}
	return &DurableRuntime{
		supervisor: supervisor,
		buffer:     buffer,
		store:      store,
		claimer:    claimer,
		publisher:  publisher,
		now:        supervisor.now,
	}, nil
}

// Run creates and durably advances a new run.
func (r *DurableRuntime) Run(
	ctx context.Context,
	request RunRequest,
) (RunState, error) {
	if strings.TrimSpace(request.RunID) == "" {
		return RunState{}, errors.New("multiagent: run id is required")
	}
	if strings.TrimSpace(request.Task.ID) == "" {
		return RunState{}, errors.New("multiagent: task id is required")
	}
	claim, err := r.claimer.Acquire(ctx, request.RunID)
	if err != nil {
		return RunState{}, err
	}
	defer claim.Release()

	state := r.supervisor.newRunState(request)
	r.supervisor.emitRun(event.EventMultiAgentRunStarted, state, "")
	record := DurableRun{
		SchemaVersion: DurableRunSchemaVersion,
		State:         state,
		Phase:         CheckpointPendingRole,
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return RunState{}, err
	}
	record, err = r.store.Create(ctx, record, r.buffer.snapshot())
	if err != nil {
		return RunState{}, err
	}
	r.buffer.reset()
	if err := r.dispatchOutbox(ctx, request.RunID); err != nil {
		return record.State, err
	}
	return r.drive(ctx, record, false)
}

// Resume loads the persisted source of truth, obtains exclusive ownership, and
// advances only from a safe checkpoint.
func (r *DurableRuntime) Resume(
	ctx context.Context,
	runID string,
) (RunState, error) {
	claim, err := r.claimer.Acquire(ctx, runID)
	if err != nil {
		return RunState{}, err
	}
	defer claim.Release()

	record, err := r.store.Load(ctx, runID)
	if err != nil {
		r.publishRecoveryFailure(ctx, runID, err)
		return RunState{}, &RecoveryFailedError{RunID: runID, Cause: err}
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		r.publishRecoveryFailure(ctx, runID, err)
		return record.State, &RecoveryFailedError{RunID: runID, Cause: err}
	}
	if err := r.dispatchOutbox(ctx, runID); err != nil {
		return record.State, err
	}
	if record.Phase == CheckpointTerminal {
		r.publishRecoveryCompleted(ctx, record.State, "terminal run unchanged")
		return record.State, nil
	}

	// An operator pause is a special CheckpointWaiting exit: it was taken
	// before the role was ever prepared (see prepareRole), so — unlike the
	// approval/execution-reconciliation waits drive's own CheckpointWaiting
	// case retries mid-role — resuming out of it must return to
	// CheckpointPendingRole so the next drive() loop prepares the role from
	// scratch (enterRole, a fresh execution key, a RoleEntered event). The
	// pause request is cleared here, before the run can reach another role
	// boundary, so it does not immediately re-pause.
	if record.Phase == CheckpointWaiting &&
		record.Waiting != nil &&
		record.Waiting.Kind == "operator_pause" {
		if err := r.store.ClearPauseRequest(ctx, runID); err != nil {
			return record.State, err
		}
		now := r.now().UTC()
		record.State.Status = RunStatusRunning
		record.State.UpdatedAt = now
		record.Waiting = nil
		record.Phase = CheckpointPendingRole
		r.supervisor.emitRun(event.EventMultiAgentRunResumed, record.State, "operator pause cleared")
	}

	r.emitRecovery(event.EventMultiAgentRecoveryStarted, record.State, "")
	record, err = r.checkpoint(ctx, record)
	if err != nil {
		return record.State, err
	}
	return r.drive(ctx, record, true)
}

// Inspect loads and validates the durable source of truth without advancing it.
func (r *DurableRuntime) Inspect(ctx context.Context, runID string) (DurableRun, error) {
	record, err := r.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record, &RecoveryFailedError{RunID: runID, Cause: err}
	}
	return record, nil
}

// Cancel persists an operator request before attempting the execution claim.
// An active owner observes it at the next role boundary; an idle run becomes
// terminal immediately. A terminal run is returned unchanged.
func (r *DurableRuntime) Cancel(
	ctx context.Context,
	runID string,
	reason string,
) (RunState, error) {
	if reason = strings.TrimSpace(reason); reason == "" {
		reason = "cancelled by user"
	}
	reason = boundedDiagnostic(errors.New(reason))
	if err := r.store.RequestCancellation(ctx, runID, reason); err != nil {
		return RunState{}, err
	}
	claim, err := r.claimer.Acquire(ctx, runID)
	if err != nil {
		if errors.Is(err, ErrRunClaimed) {
			record, loadErr := r.store.Load(ctx, runID)
			if loadErr != nil {
				return RunState{}, loadErr
			}
			return record.State, nil
		}
		return RunState{}, err
	}
	defer claim.Release()

	record, err := r.store.Load(ctx, runID)
	if err != nil {
		return RunState{}, err
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record.State, &RecoveryFailedError{RunID: runID, Cause: err}
	}
	if record.Phase == CheckpointTerminal {
		return record.State, nil
	}
	now := r.now().UTC()
	roleState := record.State.RoleStates[record.State.CurrentRole]
	if roleState.Status == RoleStatusRunning {
		roleState.Status = RoleStatusCancelled
		roleState.UpdatedAt = now
		roleState.CompletedAt = timePointer(now)
		record.State.RoleStates[record.State.CurrentRole] = roleState
	}
	record.State, _ = r.supervisor.cancelRun(record.State, errors.New(reason))
	record.Phase = CheckpointTerminal
	record.PendingTransition = nil
	record.ActiveExecutionKey = ""
	record.Waiting = nil
	record.Failure = nil
	record, err = r.checkpoint(ctx, record)
	if err != nil {
		return record.State, err
	}
	return record.State, nil
}

// Pause persists an operator pause request without acquiring the execution
// claim and without forcing any state transition itself: an active owner
// observes it at the next role boundary (see prepareRole); an idle run only
// observes it the next time Resume is called. Unlike Cancel, Pause never
// makes the run terminal — a paused run must remain resumable. A run that
// has already reached a terminal status cannot be paused; ErrRunAlreadyTerminal
// is returned in that case without persisting a request.
func (r *DurableRuntime) Pause(
	ctx context.Context,
	runID string,
	reason string,
) (RunState, error) {
	if reason = strings.TrimSpace(reason); reason == "" {
		reason = "paused by user"
	}
	reason = boundedDiagnostic(errors.New(reason))

	record, err := r.store.Load(ctx, runID)
	if err != nil {
		return RunState{}, err
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record.State, &RecoveryFailedError{RunID: runID, Cause: err}
	}
	if record.Phase == CheckpointTerminal {
		return record.State, fmt.Errorf("%w: %s", ErrRunAlreadyTerminal, runID)
	}
	if err := r.store.RequestPause(ctx, runID, reason); err != nil {
		return record.State, err
	}
	return record.State, nil
}

func (r *DurableRuntime) drive(
	ctx context.Context,
	record DurableRun,
	recovering bool,
) (RunState, error) {
	canExecutePrepared := false
	for {
		switch record.Phase {
		case CheckpointPendingRole:
			var err error
			record, err = r.prepareRole(ctx, record)
			if err != nil {
				return record.State, err
			}
			if record.Phase == CheckpointTerminal {
				if recovering {
					r.publishRecoveryCompleted(ctx, record.State, "run reached terminal state")
				}
				return record.State, terminalError(record.State)
			}
			if record.Phase == CheckpointWaiting {
				// An operator pause observed at this role boundary: return
				// immediately rather than falling through to the
				// CheckpointWaiting case below, whose SafeToRetry
				// auto-continue is built for waits that resume mid-role
				// (approval/execution-reconciliation), not a pause taken
				// before the role was ever prepared.
				return record.State, waitingError(record)
			}
			canExecutePrepared = true

		case CheckpointRoleRunning:
			if !canExecutePrepared {
				var err error
				record, err = r.pauseUncertainExecution(ctx, record)
				if err != nil {
					return record.State, err
				}
				return record.State, waitingError(record)
			}
			canExecutePrepared = false
			var err error
			record, err = r.executePreparedRole(ctx, record)
			if err != nil {
				return record.State, err
			}
			if record.Phase == CheckpointWaiting {
				return record.State, waitingError(record)
			}
			if record.Phase == CheckpointTerminal {
				if recovering {
					r.publishRecoveryCompleted(ctx, record.State, "run reached terminal state")
				}
				return record.State, terminalError(record.State)
			}

		case CheckpointTransitionPending:
			var err error
			record, err = r.applyPendingTransition(ctx, record, recovering)
			if err != nil {
				return record.State, err
			}
			if record.Phase == CheckpointTerminal {
				return record.State, terminalError(record.State)
			}

		case CheckpointWaiting:
			if record.Waiting == nil || !record.Waiting.SafeToRetry {
				return record.State, waitingError(record)
			}
			record.State.Status = RunStatusRunning
			roleState := record.State.RoleStates[record.State.CurrentRole]
			roleState.Status = RoleStatusRunning
			roleState.LastError = ""
			roleState.UpdatedAt = r.now().UTC()
			record.State.RoleStates[record.State.CurrentRole] = roleState
			record.State.UpdatedAt = roleState.UpdatedAt
			record.Waiting = nil
			record.Phase = CheckpointRoleRunning
			r.supervisor.emitRun(
				event.EventMultiAgentRunResumed,
				record.State,
				"external waiting condition will be rechecked",
			)
			var err error
			record, err = r.checkpoint(ctx, record)
			if err != nil {
				return record.State, err
			}
			canExecutePrepared = true

		case CheckpointTerminal:
			return record.State, terminalError(record.State)

		default:
			err := fmt.Errorf("unsupported checkpoint phase %q", record.Phase)
			r.publishRecoveryFailure(ctx, record.State.RunID, err)
			return record.State, &RecoveryFailedError{
				RunID: record.State.RunID,
				Cause: err,
			}
		}
	}
}

func (r *DurableRuntime) prepareRole(
	ctx context.Context,
	record DurableRun,
) (DurableRun, error) {
	if err := ctx.Err(); err != nil {
		state, cancelErr := r.supervisor.cancelRun(record.State, err)
		record.State = state
		record.Phase = CheckpointTerminal
		record.Failure = persistedFailure("cancelled", cancelErr, r.now())
		record, checkpointErr := r.checkpoint(context.WithoutCancel(ctx), record)
		if checkpointErr != nil {
			return record, checkpointErr
		}
		return record, cancelErr
	}
	reason, requested, err := r.store.CancellationRequest(ctx, record.State.RunID)
	if err != nil {
		return record, err
	}
	if requested {
		state, cancelErr := r.supervisor.cancelRun(record.State, errors.New(reason))
		record.State = state
		record.Phase = CheckpointTerminal
		record.PendingTransition = nil
		record.ActiveExecutionKey = ""
		record.Waiting = nil
		record.Failure = nil
		record, checkpointErr := r.checkpoint(ctx, record)
		if checkpointErr != nil {
			return record, checkpointErr
		}
		return record, cancelErr
	}
	pauseReason, pauseRequested, err := r.store.PauseRequest(ctx, record.State.RunID)
	if err != nil {
		return record, err
	}
	if pauseRequested {
		now := r.now().UTC()
		record.State.Status = RunStatusPaused
		record.State.UpdatedAt = now
		record.Phase = CheckpointWaiting
		record.Waiting = &WaitingState{
			Kind:        "operator_pause",
			Reason:      pauseReason,
			SafeToRetry: true,
			Since:       now,
		}
		r.supervisor.emitRun(event.EventMultiAgentRunPaused, record.State, pauseReason)
		return r.checkpoint(ctx, record)
	}
	if budgetErr := r.supervisor.checkBeforeRole(record.State); budgetErr != nil {
		state, terminalErr := r.supervisor.exhaustRun(record.State, budgetErr)
		record.State = state
		record.Phase = CheckpointTerminal
		record.Failure = persistedFailure("budget_exhausted", terminalErr, r.now())
		record, err := r.checkpoint(ctx, record)
		if err != nil {
			return record, err
		}
		return record, terminalErr
	}

	role := record.State.CurrentRole
	r.supervisor.enterRole(&record.State, role)
	roleState := record.State.RoleStates[role]
	executionKey := roleExecutionKey(record.State.RunID, role, roleState.Visits)
	roleState.LastExecutionKey = executionKey
	record.State.RoleStates[role] = roleState
	record.ActiveExecutionKey = executionKey
	record.PendingTransition = nil
	record.Waiting = nil
	record.Phase = CheckpointRoleRunning
	return r.checkpoint(ctx, record)
}

func (r *DurableRuntime) executePreparedRole(
	ctx context.Context,
	record DurableRun,
) (DurableRun, error) {
	role := record.State.CurrentRole
	roleConfig, _ := r.supervisor.graph.RoleConfig(role)
	startedAt := r.now().UTC()
	result, runErr := r.supervisor.runner.RunRole(ctx, RoleRunRequest{
		Run:        r.supervisor.runView(record.State),
		RoleConfig: cloneRoleConfig(roleConfig),
	})
	finishedAt := r.now().UTC()

	if ctxErr := ctx.Err(); ctxErr != nil {
		roleState := record.State.RoleStates[role]
		roleState.Status = RoleStatusCancelled
		roleState.LastError = boundedDiagnostic(ctxErr)
		roleState.UpdatedAt = finishedAt
		roleState.CompletedAt = timePointer(finishedAt)
		record.State.RoleStates[role] = roleState
		state, cancelErr := r.supervisor.cancelRun(record.State, ctxErr)
		record.State = state
		record.Phase = CheckpointTerminal
		record.Failure = persistedFailure("cancelled", cancelErr, r.now())
		checkpointContext := context.WithoutCancel(ctx)
		record, checkpointErr := r.checkpoint(checkpointContext, record)
		if checkpointErr != nil {
			return record, checkpointErr
		}
		return record, cancelErr
	}

	var approvalErr *ApprovalRequiredError
	if errors.As(runErr, &approvalErr) {
		return r.pauseForApproval(ctx, record, role, approvalErr)
	}
	if runErr != nil {
		return r.persistRoleFailure(
			ctx,
			record,
			role,
			&RoleExecutionError{Role: role, Cause: runErr},
		)
	}
	if err := validateRoleRunResult(r.supervisor.graph, result); err != nil {
		return r.persistRoleFailure(
			ctx,
			record,
			role,
			&RoleExecutionError{Role: role, Cause: err},
		)
	}
	transition, err := r.supervisor.graph.Resolve(role, result.Outcome)
	if err != nil {
		return r.persistRoleFailure(ctx, record, role, err)
	}

	r.supervisor.completeRole(&record.State, role, result, startedAt, finishedAt)
	roleState := record.State.RoleStates[role]
	roleState.LastExecutionKey = record.ActiveExecutionKey
	record.State.RoleStates[role] = roleState
	record.LastCompletedExecutionKey = record.ActiveExecutionKey

	if budgetErr := r.supervisor.checkAfterRole(record.State, role, roleConfig); budgetErr != nil {
		state, terminalErr := r.supervisor.exhaustRun(record.State, budgetErr)
		record.State = state
		record.Phase = CheckpointTerminal
		record.Failure = persistedFailure("budget_exhausted", terminalErr, r.now())
		record, checkpointErr := r.checkpoint(ctx, record)
		if checkpointErr != nil {
			return record, checkpointErr
		}
		return record, terminalErr
	}
	if budgetErr := r.supervisor.checkLoopBudget(record.State, transition); budgetErr != nil {
		state, terminalErr := r.supervisor.exhaustRun(record.State, budgetErr)
		record.State = state
		record.Phase = CheckpointTerminal
		record.Failure = persistedFailure("budget_exhausted", terminalErr, r.now())
		record, checkpointErr := r.checkpoint(ctx, record)
		if checkpointErr != nil {
			return record, checkpointErr
		}
		return record, terminalErr
	}
	// Purely additive: warn one traversal before this loop edge's budget
	// would be exhausted. This changes no pass/fail/routing decision — it
	// only adds an event on the existing checkLoopBudget code path, using
	// the same used/limit values checkLoopBudget itself just computed to
	// decide the transition was still within budget. loopBudgetSlug
	// (supervisor_types.go) keeps the exact
	// "tester_to_developer_loop"/"reviewer_to_developer_loop" payload
	// strings stable, since LoopKind's underlying value is no longer that
	// literal slug (it is now edgeID-shaped).
	if loop, ok := r.supervisor.graph.LoopFor(transition); ok {
		used := record.State.LoopTraversals.Get(loop.Kind) + 1
		limit := loop.MaxTraversals
		if limit != Unlimited && used == int(limit)-1 {
			r.supervisor.emitBudgetWarning(
				record.State,
				loopBudgetSlug(loop.Kind)+"_loop",
				transition.From,
				used,
				int(limit),
			)
		}
	}

	record.PendingTransition = &PendingTransition{
		ExecutionKey: record.ActiveExecutionKey,
		Resolved:     transition,
		Handoff:      cloneHandoffDraft(result.OutgoingHandoff),
	}
	record.Phase = CheckpointTransitionPending
	return r.checkpoint(ctx, record)
}

func (r *DurableRuntime) applyPendingTransition(
	ctx context.Context,
	record DurableRun,
	recovering bool,
) (DurableRun, error) {
	pending := record.PendingTransition
	if pending == nil {
		return record, &RecoveryFailedError{
			RunID: record.State.RunID,
			Cause: errors.New("transition checkpoint has no pending transition"),
		}
	}
	transition := pending.Resolved
	if transition.Terminal == "" {
		handoff, err := r.supervisor.createHandoff(
			record.State,
			transition,
			cloneHandoffDraft(pending.Handoff),
		)
		if err != nil {
			return r.persistRoleFailure(ctx, record, transition.From, err)
		}
		record.State.LatestHandoff = &handoff
		r.supervisor.emitHandoff(record.State, handoff)
	} else if pending.Handoff != nil {
		return r.persistRoleFailure(
			ctx,
			record,
			transition.From,
			errors.New("terminal transition must not contain a handoff"),
		)
	}

	record.State.TransitionCount++
	record.State.UpdatedAt = r.now().UTC()
	r.supervisor.emitTransition(record.State, transition)
	if loop, ok := r.supervisor.graph.LoopFor(transition); ok {
		record.State.LoopTraversals.increment(loop.Kind)
		r.supervisor.emitLoopTraversal(record.State, transition)
	}

	record.PendingTransition = nil
	record.ActiveExecutionKey = ""
	if transition.Terminal != "" {
		state, terminalErr := r.supervisor.finishTerminal(record.State, transition)
		record.State = state
		record.Phase = CheckpointTerminal
		if terminalErr != nil {
			record.Failure = persistedFailure("terminal", terminalErr, r.now())
		}
		if recovering {
			r.emitRecovery(
				event.EventMultiAgentRecoveryCompleted,
				record.State,
				"pending transition applied",
			)
		}
		record, checkpointErr := r.checkpoint(ctx, record)
		if checkpointErr != nil {
			return record, checkpointErr
		}
		return record, terminalErr
	}

	record.State.CurrentRole = transition.To
	record.State.UpdatedAt = r.now().UTC()
	record.Phase = CheckpointPendingRole
	return r.checkpoint(ctx, record)
}

func (r *DurableRuntime) pauseForApproval(
	ctx context.Context,
	record DurableRun,
	role Role,
	approvalErr *ApprovalRequiredError,
) (DurableRun, error) {
	now := r.now().UTC()
	roleState := record.State.RoleStates[role]
	roleState.Status = RoleStatusWaiting
	roleState.ApprovalStatus = string(approvalErr.Status)
	roleState.LastError = boundedDiagnostic(approvalErr)
	roleState.UpdatedAt = now
	record.State.RoleStates[role] = roleState
	record.State.Status = RunStatusPaused
	record.State.UpdatedAt = now
	record.Phase = CheckpointWaiting
	record.Waiting = &WaitingState{
		Kind:        "approval",
		Reason:      boundedDiagnostic(approvalErr),
		SafeToRetry: true,
		Since:       now,
	}
	r.supervisor.emitRun(event.EventMultiAgentRunPaused, record.State, record.Waiting.Reason)
	record, err := r.checkpoint(ctx, record)
	if err != nil {
		return record, err
	}
	return record, waitingError(record)
}

func (r *DurableRuntime) pauseUncertainExecution(
	ctx context.Context,
	record DurableRun,
) (DurableRun, error) {
	now := r.now().UTC()
	role := record.State.CurrentRole
	roleState := record.State.RoleStates[role]
	roleState.Status = RoleStatusWaiting
	roleState.LastError = "execution outcome is unknown after process interruption"
	roleState.UpdatedAt = now
	record.State.RoleStates[role] = roleState
	record.State.Status = RunStatusPaused
	record.State.UpdatedAt = now
	record.Phase = CheckpointWaiting
	record.Waiting = &WaitingState{
		Kind:        "execution_reconciliation",
		Reason:      roleState.LastError,
		SafeToRetry: false,
		Since:       now,
	}
	record.Failure = &PersistedFailure{
		Kind:    "uncertain_execution",
		Message: roleState.LastError,
		At:      now,
	}
	r.supervisor.emitRun(event.EventMultiAgentRunPaused, record.State, record.Waiting.Reason)
	r.emitRecovery(event.EventMultiAgentRecoveryFailed, record.State, record.Waiting.Reason)
	return r.checkpoint(ctx, record)
}

func (r *DurableRuntime) persistRoleFailure(
	ctx context.Context,
	record DurableRun,
	role Role,
	cause error,
) (DurableRun, error) {
	state, terminalErr := r.supervisor.failRole(record.State, role, cause)
	record.State = state
	record.Phase = CheckpointTerminal
	record.Failure = persistedFailure("role_execution", terminalErr, r.now())
	record.PendingTransition = nil
	record, checkpointErr := r.checkpoint(ctx, record)
	if checkpointErr != nil {
		return record, checkpointErr
	}
	return record, terminalErr
}

func (r *DurableRuntime) checkpoint(
	ctx context.Context,
	record DurableRun,
) (DurableRun, error) {
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record, err
	}
	saved, err := r.store.Checkpoint(
		ctx,
		record.Revision,
		record,
		r.buffer.snapshot(),
	)
	if err != nil {
		return record, err
	}
	r.buffer.reset()
	if err := r.dispatchOutbox(ctx, saved.State.RunID); err != nil {
		return saved, err
	}
	return saved, nil
}

func (r *DurableRuntime) dispatchOutbox(ctx context.Context, runID string) error {
	for {
		pending, err := r.store.PendingEvents(ctx, runID, 100)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		eventIDs := make([]string, 0, len(pending))
		for _, stored := range pending {
			if err := event.Validate(stored.Event); err != nil {
				return fmt.Errorf("multiagent: invalid outbox event %q: %w", stored.Event.ID, err)
			}
			if err := r.publisher.Store(ctx, stored.Event); err != nil {
				return fmt.Errorf("multiagent: publish outbox event %q: %w", stored.Event.ID, err)
			}
			eventIDs = append(eventIDs, stored.Event.ID)
		}
		if err := r.store.MarkEventsPublished(ctx, eventIDs); err != nil {
			return err
		}
	}
}

func (r *DurableRuntime) emitRecovery(
	eventType string,
	state RunState,
	reason string,
) {
	payload := map[string]any{
		"run_id":      state.RunID,
		"workflow_id": state.WorkflowID,
		"status":      string(state.Status),
	}
	if reason != "" {
		payload["reason"] = reason
	}
	if state.TerminalOutcome != nil {
		payload["terminal_condition"] = string(state.TerminalOutcome.Condition)
	}
	evt := event.NewEvent(eventType, durableEventSource, payload).
		WithCorrelationID(state.RunID).
		WithMetadata(event.EventMetadata{RunID: state.RunID})
	evt.Timestamp = r.now().UTC().Format(time.RFC3339Nano)
	r.buffer.Emit(evt)
}

func (r *DurableRuntime) publishRecoveryFailure(
	ctx context.Context,
	runID string,
	cause error,
) {
	state := RunState{
		RunID:      runID,
		WorkflowID: r.supervisor.graph.WorkflowID(),
		Status:     RunStatusFailed,
	}
	evt := r.recoveryEvent(
		event.EventMultiAgentRecoveryFailed,
		state,
		boundedDiagnostic(cause),
	)
	_ = r.publisher.Store(ctx, evt)
}

func (r *DurableRuntime) publishRecoveryCompleted(
	ctx context.Context,
	state RunState,
	reason string,
) {
	evt := r.recoveryEvent(event.EventMultiAgentRecoveryCompleted, state, reason)
	_ = r.publisher.Store(ctx, evt)
}

func (r *DurableRuntime) recoveryEvent(
	eventType string,
	state RunState,
	reason string,
) event.Event {
	payload := map[string]any{
		"run_id":      state.RunID,
		"workflow_id": state.WorkflowID,
		"status":      string(state.Status),
	}
	if reason != "" {
		payload["reason"] = reason
	}
	if state.TerminalOutcome != nil {
		payload["terminal_condition"] = string(state.TerminalOutcome.Condition)
	}
	evt := event.NewEvent(eventType, durableEventSource, payload).
		WithCorrelationID(state.RunID).
		WithMetadata(event.EventMetadata{RunID: state.RunID})
	digest := sha256.Sum256([]byte(
		eventType + "\x00" + state.RunID + "\x00" + reason))
	evt.ID = fmt.Sprintf("evt_recovery_%x", digest[:12])
	evt.Timestamp = r.now().UTC().Format(time.RFC3339Nano)
	return evt
}

func waitingError(record DurableRun) error {
	if record.Waiting == nil {
		return &RunWaitingError{
			RunID:  record.State.RunID,
			Kind:   "unknown",
			Reason: "waiting checkpoint has no reason",
		}
	}
	return &RunWaitingError{
		RunID:  record.State.RunID,
		Kind:   record.Waiting.Kind,
		Reason: record.Waiting.Reason,
	}
}

func terminalError(state RunState) error {
	if state.Status == RunStatusCompleted {
		return nil
	}
	if state.TerminalOutcome == nil {
		return fmt.Errorf("multiagent: run %q is terminal without outcome", state.RunID)
	}
	return errors.New(state.TerminalOutcome.Reason)
}

func persistedFailure(kind string, err error, at time.Time) *PersistedFailure {
	if err == nil {
		return nil
	}
	return &PersistedFailure{
		Kind:    kind,
		Message: boundedDiagnostic(err),
		At:      at.UTC(),
	}
}

func boundedDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	value := strings.Join(strings.Fields(err.Error()), " ")
	const maxDiagnosticBytes = 512
	if len(value) > maxDiagnosticBytes {
		value = value[:maxDiagnosticBytes]
	}
	return value
}

func cloneHandoffDraft(draft *HandoffDraft) *HandoffDraft {
	if draft == nil {
		return nil
	}
	cloned := *draft
	cloned.Artifacts = cloneArtifactRefs(draft.Artifacts)
	cloned.Evidence = cloneArtifactRefs(draft.Evidence)
	cloned.ValidationResults = append(
		[]validation.Result(nil),
		draft.ValidationResults...,
	)
	cloned.UnresolvedIssues = cloneIssues(draft.UnresolvedIssues)
	return &cloned
}

type durableEventBuffer struct {
	mu     sync.Mutex
	events []event.Event
}

func (b *durableEventBuffer) Emit(evt event.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, evt)
}

func (b *durableEventBuffer) snapshot() []event.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]event.Event(nil), b.events...)
}

func (b *durableEventBuffer) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = nil
}
