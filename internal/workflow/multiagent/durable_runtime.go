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

	prizmAdapter "github.com/emaharmony/prizm/internal/adapter"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/validation"
)

const durableEventSource = "prism-multi-agent-durable-runtime"

// DurableRuntimeOptions contains deterministic supervisor seams.
type DurableRuntimeOptions struct {
	Clock        func() time.Time
	NewHandoffID func() string
	Logger       *slog.Logger
	// Interaction optionally runs the adapter cascade for graph nodes that
	// advertise execution metadata. Its audit events are connected to the
	// same durable outbox as supervisor events.
	Interaction *InteractionScheduler
	Reflection  ReflectionRunner
	Memory      ReflectionMemorySink
	Proposals   ProposalLifecycle
	Delegation  *DurableDelegationOptions
}

// DurableRuntime advances a run only through atomically persisted checkpoints.
type DurableRuntime struct {
	supervisor  *Supervisor
	buffer      *durableEventBuffer
	store       DurableRunStore
	claimer     RunClaimer
	publisher   EventPublisher
	now         func() time.Time
	interaction *InteractionScheduler
	reflection  ReflectionRunner
	memory      ReflectionMemorySink
	proposals   ProposalLifecycle
	delegation  *DurableDelegationOptions
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
	if options.Delegation != nil && (strings.TrimSpace(options.Delegation.Subject) == "" ||
		options.Delegation.Dispatcher == nil || options.Delegation.Outcomes == nil || options.Delegation.Deadline <= 0) {
		return nil, errors.New("multiagent: delegated role execution requires subject, dispatcher, outcome source, and positive deadline")
	}
	buffer := &durableEventBuffer{}
	supervisor, err := NewSupervisor(
		graph,
		runner,
		buffer,
		SupervisorOptions{
			Clock:        options.Clock,
			NewHandoffID: options.NewHandoffID,
			Logger:       options.Logger,
		},
	)
	if err != nil {
		return nil, err
	}
	if options.Interaction != nil {
		options.Interaction.SetEventSink(buffer)
	}
	if setter, ok := runner.(interface{ SetEventSink(EventSink) }); ok {
		setter.SetEventSink(buffer)
	}
	return &DurableRuntime{
		supervisor:  supervisor,
		buffer:      buffer,
		store:       store,
		claimer:     claimer,
		publisher:   publisher,
		now:         supervisor.now,
		interaction: options.Interaction,
		reflection:  options.Reflection,
		memory:      options.Memory,
		proposals:   options.Proposals,
		delegation:  options.Delegation,
	}, nil
}

// Run creates and durably advances a new run.
func (r *DurableRuntime) Run(
	ctx context.Context,
	request RunRequest,
) (RunState, error) {
	if _, err := r.Accept(ctx, request); err != nil {
		return RunState{}, err
	}
	return r.RunAccepted(ctx, request.RunID)
}

// Accept atomically persists trusted request origin and run.created before
// any provider or tool execution begins. Idempotent repeats return the first
// accepted state without scheduling a second execution.
func (r *DurableRuntime) Accept(
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
	state.Status = RunStatusCreated
	if state.RequestIdentity != nil {
		state.RequestIdentity.AcceptedAt = r.now().UTC()
	}
	r.supervisor.emitRun(event.EventMultiAgentRunCreated, state, "")
	record := DurableRun{
		SchemaVersion: DurableRunSchemaVersion,
		State:         state,
		Phase:         CheckpointPendingRole,
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return RunState{}, err
	}
	events := r.buffer.snapshot()
	created := true
	if acceptor, ok := r.store.(IdempotentRunAcceptor); ok {
		record, created, err = acceptor.AcceptRun(ctx, record, events)
	} else {
		record, err = r.store.Create(ctx, record, events)
	}
	if err != nil {
		r.buffer.reset()
		if errors.Is(err, ErrRunExists) && request.Identity != nil && request.Identity.IdempotencyKey != "" {
			existing, loadErr := r.store.Load(ctx, request.RunID)
			if loadErr == nil {
				if acceptedRequestMatches(existing, state) {
					return existing.State, nil
				}
				return existing.State, ErrRequestIdentityConflict
			}
		}
		return RunState{}, err
	}
	r.buffer.reset()
	if !created {
		if !acceptedRequestMatches(record, state) {
			return record.State, ErrRequestIdentityConflict
		}
		return record.State, nil
	}
	if err := r.dispatchOutbox(ctx, request.RunID); err != nil {
		return record.State, err
	}
	return record.State, nil
}

// RunAccepted starts only the revision-one pending checkpoint created by
// Accept. Duplicate API deliveries cannot accidentally resume or replay work.
func (r *DurableRuntime) RunAccepted(ctx context.Context, runID string) (RunState, error) {
	record, err := r.store.Load(ctx, runID)
	if err != nil {
		return RunState{}, err
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record.State, err
	}
	if record.Revision != 1 || record.Phase != CheckpointPendingRole || record.State.Status != RunStatusCreated {
		return record.State, nil
	}
	return r.Resume(ctx, runID)
}

func acceptedRequestMatches(existing DurableRun, requested RunState) bool {
	return existing.State.RunID == requested.RunID &&
		existing.State.WorkflowID == requested.WorkflowID &&
		existing.State.WorkflowVersion == requested.WorkflowVersion &&
		existing.State.WorkflowFingerprint == requested.WorkflowFingerprint &&
		existing.State.CurrentTask == requested.CurrentTask &&
		existing.State.WorkspaceID == requested.WorkspaceID &&
		equalRequestIdentity(existing.State.RequestIdentity, requested.RequestIdentity)
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
	for attempt := 0; attempt < 8; attempt++ {
		state, resumeErr := r.resumeClaimed(ctx, runID)
		if !errors.Is(resumeErr, ErrRevisionConflict) {
			return state, resumeErr
		}
		latest, loadErr := r.store.Load(ctx, runID)
		if loadErr != nil || !safeDelegationResumeCheckpoint(latest) {
			return state, resumeErr
		}
		// Failed CAS events were never committed. Rebuild them from the latest
		// durable state rather than appending a duplicate on the next pass.
		r.buffer.reset()
	}
	return RunState{}, fmt.Errorf("multiagent: resume exceeded revision conflict retries for run %s", runID)
}

func safeDelegationResumeCheckpoint(record DurableRun) bool {
	if record.Phase != CheckpointWaiting || record.Waiting == nil {
		return false
	}
	switch record.Waiting.Kind {
	case "delegation_join", "delegation_outcome":
		return true
	default:
		return false
	}
}

func (r *DurableRuntime) resumeClaimed(
	ctx context.Context,
	runID string,
) (RunState, error) {
	record, err := r.store.Load(ctx, runID)
	if err != nil {
		r.publishRecoveryFailure(ctx, runID, err)
		return RunState{}, &RecoveryFailedError{RunID: runID, Cause: err}
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		r.publishRecoveryFailure(ctx, runID, err)
		return record.State, &RecoveryFailedError{RunID: runID, Cause: err}
	}
	// A scheduled retry is a safe role boundary. Honor a persisted operator
	// pause before consuming that retry, including when the service recovery
	// scanner calls Resume rather than an operator.
	if record.Phase == CheckpointWaiting && record.Waiting != nil && record.Waiting.Kind == "role_retry" {
		reason, requested, pauseErr := r.store.PauseRequest(ctx, runID)
		if pauseErr != nil {
			return record.State, pauseErr
		}
		if requested {
			now := r.now().UTC()
			record.State.Status = RunStatusPaused
			record.State.UpdatedAt = now
			record.Waiting = &WaitingState{Kind: "operator_pause", Reason: reason, SafeToRetry: true, Since: now}
			r.supervisor.emitRun(event.EventMultiAgentRunPaused, record.State, reason)
			record, err = r.checkpoint(ctx, record)
			if err != nil {
				return record.State, err
			}
			return record.State, waitingError(record)
		}
	}
	if record.Revision == 1 && record.Phase == CheckpointPendingRole && record.State.Status == RunStatusCreated {
		now := r.now().UTC()
		record.State.Status = RunStatusRunning
		record.State.UpdatedAt = now
		r.supervisor.emitRun(event.EventMultiAgentRunStarted, record.State, "")
		record, err = r.checkpoint(ctx, record)
		if err != nil {
			return record.State, err
		}
		return r.drive(ctx, record, false)
	}
	if record.PendingDelegationOutcomeAck != "" {
		record, err = r.ackDelegationOutcome(ctx, record, record.PendingDelegationOutcomeAck)
		if err != nil {
			return record.State, err
		}
	}
	// A terminal fact can be durable even when the dispatch checkpoint was
	// interrupted. Consume facts before interpreting DispatchPending/deadline
	// state so recovery never discards a completed worker result.
	if record.Phase == CheckpointWaiting && record.Waiting != nil && record.Waiting.Kind == "delegation_outcome" && r.delegation != nil && r.delegation.Outcomes != nil {
		outcomes, outcomeErr := r.delegation.Outcomes.PendingOutcomes(ctx, runID)
		if outcomeErr != nil {
			return record.State, outcomeErr
		}
		if len(outcomes) > 0 {
			record, err = r.resumeDelegation(ctx, record)
			if err != nil {
				return record.State, err
			}
			if record.Phase == CheckpointWaiting && !hasDuePendingDelegatedRole(record, r.now().UTC()) {
				return record.State, waitingError(record)
			}
		}
	}
	// Fan-out checkpoints have the same ordering requirement as a single
	// delegation: durable child facts must be consumed before an expired
	// dispatch checkpoint can synthesize a join timeout.
	if record.Phase == CheckpointWaiting && record.Waiting != nil && record.Waiting.Kind == "delegation_join" && r.delegation != nil && r.delegation.Outcomes != nil {
		outcomes, outcomeErr := r.delegation.Outcomes.PendingOutcomes(ctx, runID)
		if outcomeErr != nil {
			return record.State, outcomeErr
		}
		if len(outcomes) > 0 {
			record, err = r.resumeDelegationJoin(ctx, record)
			if err != nil {
				return record.State, err
			}
			if record.Phase == CheckpointWaiting && !hasDuePendingDelegationChild(record, r.now().UTC()) {
				return record.State, waitingError(record)
			}
		}
	}
	if record.Phase == CheckpointWaiting && record.Waiting != nil &&
		record.Waiting.Kind == "delegation_outcome" && record.Waiting.DispatchPending {
		if !r.now().UTC().Before(record.Waiting.Deadline) {
			record, err = r.timeoutDelegation(ctx, record, "delegated role deadline exceeded before dispatch completed")
			return record.State, err
		}
		record, err = r.dispatchPendingDelegation(ctx, record)
		if err != nil {
			return record.State, err
		}
	}
	if record.Phase == CheckpointWaiting && record.Waiting != nil &&
		record.Waiting.Kind == "delegation_join" && record.DelegationJoin != nil {
		pending := false
		for _, child := range record.DelegationJoin.Children {
			pending = pending || child.DispatchPending
		}
		if pending {
			if !r.now().UTC().Before(record.Waiting.Deadline) {
				record, err = r.expireDelegationJoin(ctx, record)
				return record.State, err
			}
			record, err = r.dispatchPendingDelegationJoin(ctx, record)
			if err != nil {
				return record.State, err
			}
		}
	}
	if err := r.dispatchOutbox(ctx, runID); err != nil {
		return record.State, err
	}
	if record.Phase == CheckpointTerminal {
		r.publishRecoveryCompleted(ctx, record.State, "terminal run unchanged")
		return record.State, nil
	}
	if record.Phase == CheckpointWaiting && record.Waiting != nil && record.Waiting.Kind == "delegation_outcome" {
		record, err = r.resumeDelegation(ctx, record)
		if err != nil {
			return record.State, err
		}
		if record.Phase == CheckpointWaiting {
			return record.State, waitingError(record)
		}
	}
	if record.Phase == CheckpointWaiting && record.Waiting != nil && record.Waiting.Kind == "delegation_join" {
		record, err = r.resumeDelegationJoin(ctx, record)
		if err != nil {
			return record.State, err
		}
		if record.Phase == CheckpointWaiting {
			return record.State, waitingError(record)
		}
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
		record.State.UpdatedAt = now
		if record.Retry != nil && !record.Retry.NextAt.IsZero() && record.Retry.Role == record.State.CurrentRole {
			record.State.Status = RunStatusPaused
			record.Waiting = &WaitingState{Kind: "role_retry", Reason: retryWaitReason(record.Retry), SafeToRetry: true, Since: now}
			record.Phase = CheckpointWaiting
		} else {
			record.State.Status = RunStatusRunning
			record.Waiting = nil
			record.Phase = CheckpointPendingRole
		}
		r.supervisor.emitRun(event.EventMultiAgentRunResumed, record.State, "operator pause cleared")
	}
	if record.Phase == CheckpointRoleRunning && record.Retry != nil && record.Retry.ReplaySafe && !record.Retry.MutationPossible {
		if record.Retry.RecoveryNumber >= record.Retry.MaxRecoveries {
			returnState, terminalErr := r.persistRoleFailure(ctx, record, record.State.CurrentRole, ErrRetryBudgetExhausted)
			return returnState.State, terminalErr
		}
		now := r.now().UTC()
		roleState := record.State.RoleStates[record.State.CurrentRole]
		roleConfig, _ := r.supervisor.graph.RoleConfig(record.State.CurrentRole)
		if roleState.EnteredAt != nil && roleConfig.TimeBudget > 0 && roleConfig.TimeBudget != UnlimitedDuration && !now.Before(roleState.EnteredAt.Add(roleConfig.TimeBudget)) {
			returnState, terminalErr := r.persistRoleFailure(ctx, record, record.State.CurrentRole, RetryDeadlineError{})
			return returnState.State, terminalErr
		}
		record.Retry.RecoveryNumber++
		record.Retry.Number++
		record.Retry.AttemptID = retryAttemptID(record.Retry.VisitID, record.Retry.Number)
		record.Retry.NextAt = now
		record.Retry.FailureClass = "interrupted_safe_attempt"
		record.Retry.FailureCode = "safe_attempt_interrupted"
		record.Retry.ReplaySafe = true
		roleState.Status = RoleStatusWaiting
		roleState.LastError = record.Retry.FailureCode
		roleState.UpdatedAt = now
		record.State.RoleStates[record.State.CurrentRole] = roleState
		record.State.Status = RunStatusPaused
		record.State.UpdatedAt = now
		record.Phase = CheckpointWaiting
		record.Waiting = &WaitingState{Kind: "role_retry", Reason: retryWaitReason(record.Retry), SafeToRetry: true, Since: now}
		r.emitRetry(event.EventMultiAgentRetryScheduled, record, *record.Retry)
		record, err = r.checkpoint(ctx, record)
		if err != nil {
			return record.State, err
		}
	}

	r.emitRecovery(event.EventMultiAgentRecoveryStarted, record.State, "")
	record, err = r.checkpoint(ctx, record)
	if err != nil {
		return record.State, err
	}
	return r.drive(ctx, record, true)
}

func hasDuePendingDelegatedRole(record DurableRun, now time.Time) bool {
	return record.Waiting != nil && record.Waiting.Kind == "delegation_outcome" && record.Waiting.DispatchPending &&
		record.Retry != nil && (record.Retry.NextAt.IsZero() || !record.Retry.NextAt.After(now))
}

func hasDuePendingDelegationChild(record DurableRun, now time.Time) bool {
	if record.Waiting == nil || record.Waiting.Kind != "delegation_join" || record.DelegationJoin == nil {
		return false
	}
	for _, child := range record.DelegationJoin.Children {
		if child.DispatchPending && (child.Retry == nil || child.Retry.NextAt.IsZero() || !child.Retry.NextAt.After(now)) {
			return true
		}
	}
	return false
}

// FailAcceptedRun records a deterministic, safe configuration failure for an
// accepted run before execution is resumed. Repeated calls are idempotent.
func (r *DurableRuntime) FailAcceptedRun(ctx context.Context, runID, code string) (RunState, error) {
	claim, err := r.claimer.Acquire(ctx, runID)
	if err != nil {
		return RunState{}, err
	}
	defer claim.Release()
	record, err := r.store.Load(ctx, runID)
	if err != nil {
		return RunState{}, err
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return record.State, err
	}
	if record.Phase == CheckpointTerminal {
		return record.State, nil
	}
	if code != "validation_profile_configuration_changed" {
		return record.State, errors.New("unsupported accepted-run failure code")
	}
	role := record.State.CurrentRole
	if role == "" {
		return record.State, errors.New("accepted run has no current role")
	}
	saved, failureErr := r.persistRoleFailure(ctx, record, role, &GovernanceError{Kind: "validation_profile_configuration", Reason: code})
	return saved.State, failureErr
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
			if record.Waiting != nil && record.Waiting.Kind == "role_retry" {
				if record.Retry == nil || !record.Retry.ReplaySafe || record.Retry.MutationPossible {
					return record.State, waitingError(record)
				}
				if r.now().UTC().Before(record.Retry.NextAt) {
					return record.State, waitingError(record)
				}
				record.State.Status = RunStatusRunning
				record.State.UpdatedAt = r.now().UTC()
				record.Retry.NextAt = time.Time{}
				roleState := record.State.RoleStates[record.State.CurrentRole]
				roleState.Status, roleState.LastError, roleState.UpdatedAt = RoleStatusRunning, "", record.State.UpdatedAt
				record.State.RoleStates[record.State.CurrentRole] = roleState
				record.Waiting = nil
				record.Phase = CheckpointRoleRunning
				r.emitRetry(event.EventMultiAgentRetryAttemptStarted, record, *record.Retry)
				var err error
				record, err = r.checkpoint(ctx, record)
				if err != nil {
					return record.State, err
				}
				canExecutePrepared = true
				continue
			}
			if record.Waiting != nil && record.Waiting.Kind == "proposal_approval" {
				var err error
				record, err = r.advanceApprovedTask(ctx, record)
				if err != nil {
					return record.State, err
				}
				if record.Phase == CheckpointWaiting || record.Phase == CheckpointTerminal {
					return record.State, waitingError(record)
				}
				continue
			}
			if record.Waiting == nil || !record.Waiting.SafeToRetry {
				return record.State, waitingError(record)
			}
			if r.reflection != nil {
				trigger := ReflectionRecovery
				if record.Waiting.Kind == "approval" || record.Waiting.Kind == "interaction_approval" {
					trigger = ReflectionApprovalResume
				}
				input := ReflectionInput{Trigger: trigger, SourceRole: record.State.CurrentRole, Error: record.Waiting.Reason, Goal: record.State.CurrentTask.Description}
				if r.runReflection(ctx, &record, trigger, input) {
					var checkpointErr error
					record, checkpointErr = r.checkpoint(ctx, record)
					if checkpointErr != nil {
						return record.State, checkpointErr
					}
					continue
				}
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
	node, _ := r.supervisor.graph.Node(nodeID(role))
	visitID := roleVisitID(record.State.RunID, role, roleState.Visits)
	record.Retry = &RetryState{
		VisitID: visitID, ExecutionKey: executionKey, Role: role,
		AttemptID: retryAttemptID(visitID, 1), Number: 1,
		MaxRecoveries: roleRetryLimit(node), ReplaySafe: true, MutationLedgerReady: true,
	}
	record.PendingTransition = nil
	record.Waiting = nil
	record.Phase = CheckpointRoleRunning
	return r.checkpoint(ctx, record)
}

func (r *DurableRuntime) executePreparedRole(
	ctx context.Context,
	record DurableRun,
) (DurableRun, error) {
	if record.PendingFanOutAdoption != nil {
		return r.executeFanOutAdoption(ctx, record)
	}
	if r.delegation != nil {
		return r.dispatchDelegatedRole(ctx, record)
	}
	role := record.State.CurrentRole
	roleConfig, _ := r.supervisor.graph.RoleConfig(role)
	startedAt := r.now().UTC()
	if r.interaction != nil {
		if node, ok := r.supervisor.graph.Node(nodeID(role)); ok && node.Execution != nil {
			roleState := record.State.RoleStates[role]
			interactionID := fmt.Sprintf("%s-%s-%d", record.State.RunID, role, roleState.Visits)
			interactionRecord, interactionErr := r.interaction.Run(ctx, InteractionRunRequest{
				RunID:       interactionID,
				EventRunID:  record.State.RunID,
				Goal:        record.State.CurrentTask.Description,
				Observation: prizmAdapter.ObservationRequest{},
				Plan:        interactionPlanForNode(node),
			})
			if interactionErr != nil {
				if interactionRecord.Status == InteractionPaused {
					return r.pauseForInteractionApproval(ctx, record, interactionRecord)
				}
				if r.runReflection(ctx, &record, ReflectionFailure, reflectionFailureInput(role, "", interactionErr, record.State)) {
					return r.checkpoint(ctx, record)
				}
				return r.persistRoleFailure(ctx, record, role, &RoleExecutionError{Role: role, Cause: interactionErr})
			}
			if interactionRecord.Status == InteractionPaused {
				return r.pauseForInteractionApproval(ctx, record, interactionRecord)
			}
			if interactionRecord.Status == InteractionFailed {
				return r.persistRoleFailure(ctx, record, role, &RoleExecutionError{Role: role, Cause: errors.New(interactionRecord.LastError)})
			}
			if interactionRecord.Interrupted {
				input := ReflectionInput{Trigger: ReflectionInterruption, SourceRole: role, Error: interactionRecord.InterruptReason, Goal: record.State.CurrentTask.Description, Observation: interactionRecord.LastObservation, Action: interactionRecord.LastAction, ActionResult: interactionRecord.LastResult}
				if r.runReflection(ctx, &record, ReflectionInterruption, input) {
					return r.checkpoint(ctx, record)
				}
			}
		}
	}
	retryHandle := newDurableRoleRetryHandle(ctx, r, record)
	result, runErr := r.supervisor.runner.RunRole(ctx, RoleRunRequest{
		Run:              r.supervisor.runView(record.State),
		RoleConfig:       cloneRoleConfig(roleConfig),
		TrustedTreeBasis: trustedTreeBasisForRole(record, role),
		RecoveryFeedback: func() string {
			if record.Retry != nil {
				return record.Retry.RecoveryFeedback
			}
			return ""
		}(),
		RetryHandle: retryHandle,
		ToolEventSink: func(diagnostic ToolDiagnostic) error {
			if diagnostic.RunID == "" {
				diagnostic.RunID = record.State.RunID
			}
			if diagnostic.TaskID == "" {
				diagnostic.TaskID = record.State.CurrentTask.ID
			}
			if diagnostic.Role == "" {
				diagnostic.Role = role
			}
			if diagnostic.ExecutionKey == "" {
				diagnostic.ExecutionKey = record.ActiveExecutionKey
			}
			if diagnostic.AttemptID == "" {
				diagnostic.AttemptID = retryHandle.CurrentAttempt().AttemptID
			}
			return retryHandle.RecordToolDiagnostic(diagnostic)
		},
	})
	record = retryHandle.close()
	finishedAt := r.now().UTC()
	var retryDeadline time.Time
	if entered := record.State.RoleStates[role].EnteredAt; entered != nil && roleConfig.TimeBudget > 0 && roleConfig.TimeBudget != UnlimitedDuration {
		retryDeadline = entered.Add(roleConfig.TimeBudget)
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		record = accumulateFailedAttempt(record, result)
		if record.Retry != nil {
			retryState := *record.Retry
			retryState.FailureClass = "cancellation"
			retryState.FailureCode = "cancelled"
			roleState := record.State.RoleStates[role]
			roleState.LocalIterations += retryState.AccumulatedIterations
			roleState.Retries += retryState.RecoveryNumber
			roleState.TokenUsage = addTokenUsage(roleState.TokenUsage, retryState.AccumulatedUsage)
			record.State.RoleStates[role] = roleState
			record.State.BudgetUsage.TotalLocalIterations += retryState.AccumulatedIterations
			record.State.BudgetUsage.Retries += retryState.RecoveryNumber
			record.State.BudgetUsage.Tokens = addTokenUsage(record.State.BudgetUsage.Tokens, retryState.AccumulatedUsage)
			appendRetryHistory(&record, &retryState)
			record.Retry = nil
		}
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
		if record.Retry != nil && record.Retry.MutationPossible {
			record = accumulateFailedAttempt(record, result)
			return r.pauseUncertainExecution(ctx, record)
		}
		if r.runReflection(ctx, &record, ReflectionFailure, reflectionFailureInput(role, "", runErr, record.State)) {
			record = archiveRetryForReflection(record, result, role, roleFailureCode(runErr), r.now().UTC())
			return r.checkpoint(ctx, record)
		}
		if class, code, retryAfter, retryable := classifyRetryableRoleFailure(runErr); retryable {
			return r.scheduleRoleRetry(ctx, record, role, result, runErr, class, code, retryAfter, retryDeadline)
		}
		record = accumulateFailedAttempt(record, result)
		return r.persistRoleFailure(
			ctx,
			record,
			role,
			&RoleExecutionError{Role: role, Cause: runErr},
		)
	}
	if record.Retry != nil && record.Retry.UnresolvedMutationFailure {
		record = accumulateFailedAttempt(record, result)
		return r.pauseUncertainExecution(ctx, record)
	}
	if err := validateRoleRunResult(r.supervisor.graph, result); err != nil {
		if record.Retry != nil && record.Retry.MutationPossible {
			record = accumulateFailedAttempt(record, result)
			return r.pauseUncertainExecution(ctx, record)
		}
		if r.runReflection(ctx, &record, ReflectionFailure, reflectionFailureInput(role, result.Outcome, err, record.State)) {
			record = archiveRetryForReflection(record, result, role, roleFailureCode(err), r.now().UTC())
			return r.checkpoint(ctx, record)
		}
		if class, code, retryAfter, retryable := classifyRetryableRoleFailure(err); retryable {
			return r.scheduleRoleRetry(ctx, record, role, result, err, class, code, retryAfter, retryDeadline)
		}
		record = accumulateFailedAttempt(record, result)
		return r.persistRoleFailure(
			ctx,
			record,
			role,
			&RoleExecutionError{Role: role, Cause: err},
		)
	}
	if result.FanOut != nil {
		record = accumulateFailedAttempt(record, result)
		return r.persistRoleFailure(ctx, record, role, &GovernanceError{Kind: "fan_out", Reason: "result cannot be executed without durable planner delegation"})
	}
	if record.Retry != nil {
		result.Retries = record.Retry.RecoveryNumber
		result.LocalIterations += record.Retry.AccumulatedIterations
		result.TokenUsage = addTokenUsage(result.TokenUsage, record.Retry.AccumulatedUsage)
	}
	if len(result.Proposals) > 0 {
		appendRetryHistory(&record, record.Retry)
		record.Retry = nil
		return r.pauseForProposals(ctx, record, role, result, startedAt, finishedAt, nil)
	}
	return r.finishPreparedRole(ctx, record, role, roleConfig, result, startedAt, finishedAt)
}

func (r *DurableRuntime) verifyFanOutAdoptionApproval(ctx context.Context, record DurableRun, task *ApprovedTaskState) (*FanOutAdoptionState, error) {
	candidate := task.FanOutAdoption
	expectedNodeID, hasDeveloperNode := r.supervisor.graph.NodeIDForRole(RoleDeveloper)
	if candidate == nil || !validFanOutAdoptionSource(*candidate, record.State, r.supervisor.graph) || task.Role != candidate.ParentRole || candidate.RunID != record.State.RunID ||
		candidate.Task != record.State.CurrentTask || candidate.WorkflowID != record.State.WorkflowID ||
		candidate.WorkflowFingerprint != record.State.WorkflowFingerprint || candidate.WorkspaceID != record.State.WorkspaceID ||
		candidate.ParentExecutionKey != record.ActiveExecutionKey || candidate.ParentVisit != record.State.RoleStates[candidate.ParentRole].Visits ||
		candidate.ExpectedRole != RoleDeveloper || !hasDeveloperNode || candidate.ExpectedNodeID != expectedNodeID ||
		candidate.ExpectedVisit != record.State.RoleStates[RoleDeveloper].Visits+1 ||
		!sameProposalSet(candidate.Proposals, task.Result.Proposals) || !sameProposalSet(candidate.Proposals, proposalProgressRefs(task.Proposals)) {
		return nil, errors.New("fan-out adoption source scope does not match the approved task")
	}
	transition, err := r.supervisor.graph.Resolve(task.Role, task.Result.Outcome)
	if err != nil || transition.Terminal != "" || transition.To != candidate.ExpectedRole {
		return nil, errors.New("fan-out adoption no longer targets the immediate Developer successor")
	}
	if record.State.WorkspaceID == "" || candidate.ChildID == "" || candidate.ChildExecutionKey == "" || candidate.JoinID == "" ||
		candidate.DeliveryKey == "" || candidate.CommandEventID == "" || candidate.CorrelationID == "" ||
		candidate.Result.Metadata.WorkspaceID != candidate.WorkspaceID || candidate.Result.Metadata.StartedAt.IsZero() || candidate.Result.Metadata.FinishedAt.IsZero() {
		return nil, errors.New("fan-out adoption source execution evidence is incomplete")
	}
	for _, progress := range task.Proposals {
		if progress.Phase != "applied" || progress.Result == nil || !progress.Result.Success {
			return nil, errors.New("fan-out adoption proposal was not durably applied")
		}
		decision, _, decisionErr := r.proposals.Decision(ctx, progress.ProposalOperation)
		if decisionErr != nil || decision != ProposalGranted {
			return nil, errors.New("fan-out adoption proposal is not currently granted")
		}
		status, result, reconcileErr := r.proposals.Reconcile(ctx, progress.ProposalOperation)
		if reconcileErr != nil || status != ProposalApplied || !result.Success {
			return nil, errors.New("fan-out adoption worktree does not match the approved proposal tree")
		}
	}
	return cloneFanOutAdoption(candidate), nil
}

func (r *DurableRuntime) executeFanOutAdoption(ctx context.Context, record DurableRun) (DurableRun, error) {
	adoption := record.PendingFanOutAdoption
	role := record.State.CurrentRole
	roleState := record.State.RoleStates[role]
	currentNodeID, hasCurrentNode := r.supervisor.graph.NodeIDForRole(role)
	failClosed := func(reason string) (DurableRun, error) {
		record.PendingFanOutAdoption = nil
		return r.persistRoleFailure(ctx, record, role, &GovernanceError{Kind: "fan_out_adoption", Reason: reason})
	}
	if adoption == nil || role != RoleDeveloper || !hasCurrentNode || adoption.ExpectedRole != role || adoption.ExpectedNodeID != currentNodeID ||
		adoption.RunID != record.State.RunID || adoption.Task != record.State.CurrentTask ||
		adoption.WorkflowID != record.State.WorkflowID || adoption.WorkflowFingerprint != record.State.WorkflowFingerprint ||
		adoption.WorkspaceID == "" || adoption.WorkspaceID != record.State.WorkspaceID ||
		adoption.ParentRole != RolePlanner || adoption.ParentExecutionKey == "" ||
		record.LastCompletedExecutionKey != adoption.ParentExecutionKey ||
		adoption.ParentVisit != record.State.RoleStates[adoption.ParentRole].Visits || adoption.ExpectedVisit != roleState.Visits ||
		adoption.ChildID == "" || adoption.ChildExecutionKey == "" || adoption.JoinID == "" || adoption.DeliveryKey == "" ||
		adoption.DeliveryAttemptID != fmt.Sprintf("%s:delivery:%d", adoption.ChildExecutionKey, adoption.DeliveryNumber) ||
		adoption.AttemptID != retryAttemptID(adoption.ChildExecutionKey, adoption.AttemptNumber) || adoption.AttemptNumber <= 0 ||
		adoption.CommandEventID == "" || adoption.CorrelationID == "" || adoption.Result.Metadata.WorkspaceID != adoption.WorkspaceID ||
		!sameProposalSet(adoption.Proposals, adoption.Result.Proposals) {
		return failClosed("approved implementation result is outside its bound successor scope")
	}
	transition, err := r.supervisor.graph.Resolve(role, adoption.Result.Outcome)
	if err != nil || transition.Terminal != "" || transition.To != RoleTester {
		return failClosed("approved implementation result does not route to the configured Tester")
	}
	if r.proposals == nil {
		return failClosed("proposal lifecycle is unavailable for adoption reconciliation")
	}
	for _, proposal := range adoption.Proposals {
		op := ProposalOperation{RunID: adoption.RunID, ProposalID: proposal.ProposalID, ApprovalID: proposal.ApprovalID,
			ApplyKey: proposalApplyKey(adoption.RunID, proposal.ProposalID), WorkspaceID: adoption.WorkspaceID,
			ExecutionKey: adoption.ParentExecutionKey}
		decision, _, decisionErr := r.proposals.Decision(ctx, op)
		if decisionErr != nil || decision != ProposalGranted {
			return failClosed("approved implementation proposal is no longer granted")
		}
		status, result, reconcileErr := r.proposals.Reconcile(ctx, op)
		if reconcileErr != nil || status != ProposalApplied || !result.Success {
			return failClosed("approved implementation tree changed before adoption")
		}
	}
	result := cloneRoleRunResult(adoption.Result)
	result.Proposals = nil
	result.FanOut = nil
	result.Metadata.ExecutionMode = "approved_fanout_adoption"
	result.Metadata.SourceJoinID = adoption.JoinID
	result.Metadata.SourceChildID = adoption.ChildID
	result.Metadata.SourceDeliveryKey = adoption.DeliveryKey
	result.Metadata.SourceProposalIDs = make([]string, 0, len(adoption.Proposals))
	for _, proposal := range adoption.Proposals {
		result.Metadata.SourceProposalIDs = append(result.Metadata.SourceProposalIDs, proposal.ProposalID)
	}
	startedAt, finishedAt := result.Metadata.StartedAt, result.Metadata.FinishedAt
	record.PendingFanOutAdoption = nil
	roleConfig, _ := r.supervisor.graph.RoleConfig(role)
	return r.finishPreparedRole(ctx, record, role, roleConfig, result, startedAt, finishedAt)
}

func proposalProgressRefs(progress []ProposalProgress) []ProposalReference {
	refs := make([]ProposalReference, 0, len(progress))
	for _, item := range progress {
		refs = append(refs, ProposalReference{ProposalID: item.ProposalID, ApprovalID: item.ApprovalID, PatchPlan: cloneGitPatchPlan(item.PatchPlan)})
	}
	return refs
}

func cloneProposalReferences(refs []ProposalReference) []ProposalReference {
	cloned := make([]ProposalReference, len(refs))
	for i, ref := range refs {
		cloned[i] = ref
		cloned[i].Artifacts = cloneArtifactRefs(ref.Artifacts)
		cloned[i].PatchPlan = cloneGitPatchPlan(ref.PatchPlan)
	}
	return cloned
}

func cloneGitPatchPlan(plan *gitx.PatchPlan) *gitx.PatchPlan {
	if plan == nil {
		return nil
	}
	cloned := gitx.ClonePatchPlan(*plan)
	return &cloned
}

func cloneTrustedTreeBasis(basis *gitx.TrustedTreeBasis) *gitx.TrustedTreeBasis {
	if basis == nil {
		return nil
	}
	cloned := *basis
	cloned.Paths = append([]string(nil), basis.Paths...)
	return &cloned
}

func cloneFanOutAdoption(adoption *FanOutAdoptionState) *FanOutAdoptionState {
	if adoption == nil {
		return nil
	}
	cloned := *adoption
	cloned.Proposals = cloneProposalReferences(adoption.Proposals)
	cloned.Result = cloneRoleRunResult(adoption.Result)
	return &cloned
}

func (r *DurableRuntime) finishPreparedRole(
	ctx context.Context,
	record DurableRun,
	role Role,
	roleConfig RoleConfig,
	result RoleRunResult,
	startedAt time.Time,
	finishedAt time.Time,
) (DurableRun, error) {
	transition, err := r.supervisor.graph.Resolve(role, result.Outcome)
	if err != nil {
		if record.Retry != nil && record.Retry.MutationPossible {
			record = accumulateFailedAttempt(record, result)
			return r.pauseUncertainExecution(ctx, record)
		}
		if r.runReflection(ctx, &record, ReflectionFailure, reflectionFailureInput(role, result.Outcome, err, record.State)) {
			record = archiveRetryForReflection(record, result, role, roleFailureCode(err), r.now().UTC())
			return r.checkpoint(ctx, record)
		}
		return r.persistRoleFailure(ctx, record, role, err)
	}

	r.supervisor.completeRole(&record.State, role, result, startedAt, finishedAt)
	appendRetryHistory(&record, record.Retry)
	record.Retry = nil
	roleState := record.State.RoleStates[role]
	roleState.LastExecutionKey = record.ActiveExecutionKey
	record.State.RoleStates[role] = roleState
	record.LastCompletedExecutionKey = record.ActiveExecutionKey
	if transition.Terminal != "" {
		input := ReflectionInput{Trigger: ReflectionTerminal, SourceRole: role, Outcome: result.Outcome, Goal: record.State.CurrentTask.Description}
		if result.OutgoingHandoff != nil {
			input.Evidence = append(input.Evidence, result.OutgoingHandoff.Evidence...)
			input.Evidence = append(input.Evidence, result.OutgoingHandoff.Artifacts...)
		}
		if r.runReflection(ctx, &record, ReflectionTerminal, input) {
			return r.checkpoint(ctx, record)
		}
	}

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

func (r *DurableRuntime) pauseForProposals(
	ctx context.Context,
	record DurableRun,
	role Role,
	result RoleRunResult,
	startedAt time.Time,
	finishedAt time.Time,
	fanOutAdoption *FanOutAdoptionState,
) (DurableRun, error) {
	now := r.now().UTC()
	seen := make(map[string]struct{}, len(result.Proposals))
	progress := make([]ProposalProgress, 0, len(result.Proposals))
	for _, ref := range result.Proposals {
		proposalID := strings.TrimSpace(ref.ProposalID)
		approvalID := strings.TrimSpace(ref.ApprovalID)
		if proposalID == "" || approvalID == "" {
			return r.persistRoleFailure(ctx, record, role, errors.New("proposal and approval ids are required"))
		}
		key := proposalID + "\x00" + approvalID
		if _, exists := seen[key]; exists {
			return r.persistRoleFailure(ctx, record, role, fmt.Errorf("duplicate proposal %q", proposalID))
		}
		seen[key] = struct{}{}
		operation := ProposalOperation{
			RunID: record.State.RunID, ProposalID: proposalID, ApprovalID: approvalID,
			ApplyKey:    proposalApplyKey(record.State.RunID, proposalID),
			WorkspaceID: record.State.WorkspaceID, ExecutionKey: record.ActiveExecutionKey, Role: role,
			PatchPlan: cloneGitPatchPlan(ref.PatchPlan),
		}
		progress = append(progress, ProposalProgress{ProposalOperation: operation, Phase: "waiting"})
		r.emitProposal(event.EventProposalRecorded, operation, "")
	}
	record.ApprovedTask = &ApprovedTaskState{
		Role: role, Result: result, StartedAt: startedAt, FinishedAt: finishedAt, Proposals: progress,
		FanOutAdoption: cloneFanOutAdoption(fanOutAdoption),
	}
	record.State.Status = RunStatusPaused
	roleState := record.State.RoleStates[role]
	roleState.Status = RoleStatusWaiting
	roleState.ApprovalStatus = string(ProposalPending)
	roleState.UpdatedAt = now
	record.State.RoleStates[role] = roleState
	record.State.UpdatedAt = now
	record.Phase = CheckpointWaiting
	record.Waiting = proposalWaitingState(progress[0], "waiting for exact proposal approval", r.proposals != nil, now)
	r.emitProposal(event.EventProposalApprovalPending, progress[0].ProposalOperation, record.Waiting.Reason)
	r.supervisor.emitRun(event.EventMultiAgentRunPaused, record.State, record.Waiting.Reason)
	record, err := r.checkpoint(ctx, record)
	if err != nil {
		return record, err
	}
	return record, waitingError(record)
}

func trustedTreeBasisForRole(record DurableRun, role Role) *gitx.TrustedTreeBasis {
	if role != RoleDeveloper {
		return nil
	}
	if record.ApprovedTask != nil && record.ApprovedTask.Role == role {
		for index := len(record.ApprovedTask.Proposals) - 1; index >= 0; index-- {
			if basis := trustedTreeBasisFromProposal(record, role, record.ApprovedTask.Proposals[index]); basis != nil {
				return basis
			}
		}
	}
	for index := len(record.ProposalResults) - 1; index >= 0; index-- {
		if basis := trustedTreeBasisFromProposal(record, role, record.ProposalResults[index]); basis != nil {
			return basis
		}
	}
	return nil
}

func trustedTreeBasisFromProposal(record DurableRun, role Role, progress ProposalProgress) *gitx.TrustedTreeBasis {
	op := progress.ProposalOperation
	plan := progress.ResultPlan()
	if plan == nil || progress.Phase != "applied" || op.PatchPlan == nil ||
		op.RunID != record.State.RunID || op.WorkspaceID != record.State.WorkspaceID ||
		op.ProposalID == "" || op.ApprovalID == "" || op.ApplyKey != proposalApplyKey(op.RunID, op.ProposalID) ||
		op.ExecutionKey == "" || op.Role != role || progress.Result == nil || !progress.Result.Success ||
		progress.Result.ReconcileCode != ProposalReconcileExpectedTree || progress.Result.ObservedTree != plan.ExpectedTree ||
		!validTrustedPatchPlan(*plan) {
		return nil
	}
	if op.PatchPlan != nil && !gitx.EqualPatchPlan(*op.PatchPlan, *plan) {
		return nil
	}
	return &gitx.TrustedTreeBasis{
		RunID: op.RunID, WorkspaceID: op.WorkspaceID, ProposalID: op.ProposalID, ApprovalID: op.ApprovalID,
		ApplyKey: op.ApplyKey, ExecutionKey: op.ExecutionKey, Role: string(role),
		BaseSHA: plan.BaseSHA, BaseTree: plan.BaseTree, ExpectedTree: plan.ExpectedTree,
		PatchSHA256: plan.PatchSHA256, Paths: append([]string(nil), plan.Paths...),
	}
}

func (progress ProposalProgress) ResultPlan() *gitx.PatchPlan {
	if progress.Result == nil || progress.Result.PatchPlan == nil {
		return nil
	}
	plan := gitx.ClonePatchPlan(*progress.Result.PatchPlan)
	return &plan
}

func validTrustedPatchPlan(plan gitx.PatchPlan) bool {
	if plan.BaseSHA == "" || plan.BaseTree == "" || plan.ExpectedTree == "" || plan.PatchSHA256 == "" || len(plan.Paths) == 0 {
		return false
	}
	planningTree := plan.PlanningTree
	if planningTree == "" {
		planningTree = plan.BaseTree
	}
	return plan.ExpectedTree != planningTree
}

func (r *DurableRuntime) advanceApprovedTask(ctx context.Context, record DurableRun) (DurableRun, error) {
	if record.ApprovedTask == nil {
		return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: errors.New("proposal wait has no approved task state")}
	}
	if r.proposals == nil {
		record.Waiting.SafeToRetry = false
		record.Waiting.Reason = "proposal lifecycle is unavailable"
		record.Failure = persistedFailure("proposal_lifecycle_unavailable", errors.New(record.Waiting.Reason), r.now())
		return r.checkpoint(ctx, record)
	}
	for index := range record.ApprovedTask.Proposals {
		progress := &record.ApprovedTask.Proposals[index]
		if progress.Phase == "applied" {
			continue
		}
		decision, reason, err := r.proposals.Decision(ctx, progress.ProposalOperation)
		if err != nil {
			return record, fmt.Errorf("check proposal approval %q: %w", progress.ApprovalID, err)
		}
		switch decision {
		case ProposalPending:
			record.Waiting = proposalWaitingState(*progress, firstNonEmpty(reason, "waiting for exact proposal approval"), true, record.Waiting.Since)
			r.emitProposal(event.EventProposalApprovalPending, progress.ProposalOperation, record.Waiting.Reason)
			record, err = r.checkpoint(ctx, record)
			return record, err
		case ProposalDenied, ProposalExpired:
			r.emitProposal(event.EventProposalApprovalDenied, progress.ProposalOperation, reason)
			return r.persistRoleFailure(ctx, record, record.ApprovedTask.Role,
				fmt.Errorf("proposal %s: %s", decision, firstNonEmpty(reason, "authorization refused")))
		case ProposalGranted:
			// Continue below.
		default:
			return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: fmt.Errorf("unknown proposal decision %q", decision)}
		}

		// Reconcile before every application. This handles both a process crash
		// after the side effect and today's CLI path, where granting an approval
		// may have already applied it.
		if progress.Phase == "applying" || decision == ProposalGranted {
			status, result, reconcileErr := r.proposals.Reconcile(ctx, progress.ProposalOperation)
			if reconcileErr != nil {
				return record, fmt.Errorf("reconcile proposal %q: %w", progress.ProposalID, reconcileErr)
			}
			switch status {
			case ProposalApplied:
				progress.Phase, progress.Result = "applied", &result
				r.emitProposalResult(event.EventProposalReconciled, progress.ProposalOperation, result)
				var checkpointErr error
				record, checkpointErr = r.checkpoint(ctx, record)
				if checkpointErr != nil {
					return record, checkpointErr
				}
				continue
			case ProposalAmbiguous:
				result = safeProposalReconcileResult(result)
				progress.Result = &result
				record.Waiting = proposalWaitingState(*progress, "proposal application outcome is ambiguous", false, r.now().UTC())
				record.Failure = persistedFailure("uncertain_proposal_application", errors.New(record.Waiting.Reason), r.now())
				r.emitProposalReconcileFailure(progress.ProposalOperation, record.Waiting.Reason, result)
				return r.checkpoint(ctx, record)
			case ProposalNotApplied:
				// Safe to retry the stable apply key.
			default:
				return record, &RecoveryFailedError{RunID: record.State.RunID, Cause: fmt.Errorf("unknown proposal reconciliation %q", status)}
			}
		}

		progress.Phase = "applying"
		progress.Attempts++
		r.emitProposal(event.EventProposalApprovalGranted, progress.ProposalOperation, reason)
		r.emitProposal(event.EventProposalApplyStarted, progress.ProposalOperation, "")
		var checkpointErr error
		record, checkpointErr = r.checkpoint(ctx, record)
		if checkpointErr != nil {
			return record, checkpointErr
		}
		progress = &record.ApprovedTask.Proposals[index]
		result, applyErr := r.proposals.Apply(ctx, progress.ProposalOperation)
		if applyErr != nil || !result.Success {
			message := result.Message
			if applyErr != nil {
				message = applyErr.Error()
			}
			r.emitProposal(event.EventProposalApplyFailed, progress.ProposalOperation, message)
			if progress.Attempts < 2 {
				progress.Phase = "waiting"
				record.Waiting = proposalWaitingState(*progress, "proposal application failed; bounded retry available: "+boundedDiagnostic(errors.New(message)), true, r.now().UTC())
				return r.checkpoint(ctx, record)
			}
			return r.persistRoleFailure(ctx, record, record.ApprovedTask.Role, fmt.Errorf("apply proposal %q: %s", progress.ProposalID, message))
		}
		progress.Phase, progress.Result = "applied", &result
		r.emitProposalResult(event.EventProposalApplied, progress.ProposalOperation, result)
		record, checkpointErr = r.checkpoint(ctx, record)
		if checkpointErr != nil {
			return record, checkpointErr
		}
	}

	task := record.ApprovedTask
	var adoption *FanOutAdoptionState
	if task.FanOutAdoption != nil {
		var adoptionErr error
		adoption, adoptionErr = r.verifyFanOutAdoptionApproval(ctx, record, task)
		if adoptionErr != nil {
			return r.persistRoleFailure(ctx, record, task.Role, &GovernanceError{Kind: "fan_out_adoption", Reason: adoptionErr.Error()})
		}
	}
	roleConfig, _ := r.supervisor.graph.RoleConfig(task.Role)
	validator, ok := r.supervisor.runner.(ApprovedRoleValidator)
	if !ok {
		return r.persistRoleFailure(ctx, record, task.Role, &GovernanceError{
			Kind:   "validation",
			Reason: "mutation-bearing proposal requires post-apply validation support",
		})
	}
	{
		validated, validationErr := validator.ValidateApprovedRole(ctx, RoleRunRequest{
			Run:        r.supervisor.runView(record.State),
			RoleConfig: cloneRoleConfig(roleConfig),
		}, task.Result)
		// Retain post-apply validation evidence even when it fails, so recovery
		// and the terminal report explain why continuation was stopped.
		record.ApprovedTask.Result = validated
		var checkpointErr error
		record, checkpointErr = r.checkpoint(ctx, record)
		if checkpointErr != nil {
			return record, checkpointErr
		}
		if validationErr != nil {
			if r.runReflection(ctx, &record, ReflectionFailure, reflectionFailureInput(task.Role, validated.Outcome, validationErr, record.State)) {
				return r.checkpoint(ctx, record)
			}
			return r.persistRoleFailure(ctx, record, task.Role, &RoleExecutionError{Role: task.Role, Cause: validationErr})
		}
		task = record.ApprovedTask
	}
	// The developer's handoff was produced before the operator decided the
	// proposal. Remove only a now-stale, identity-bearing pending-approval
	// issue and add the durable fact that supersedes it. Later roles must not
	// treat a resolved human gate as an outstanding blocker after recovery.
	reconcileApprovedTaskHandoff(&task.Result, task.Proposals)
	record.ApprovedTask.Result = task.Result
	var checkpointErr error
	record, checkpointErr = r.checkpoint(ctx, record)
	if checkpointErr != nil {
		return record, checkpointErr
	}
	record.ProposalResults = append(record.ProposalResults, task.Proposals...)
	record.ApprovedTask = nil
	record.PendingFanOutAdoption = adoption
	record.Waiting = nil
	record.Failure = nil
	record.State.Status = RunStatusRunning
	roleState := record.State.RoleStates[task.Role]
	roleState.Status = RoleStatusRunning
	roleState.ApprovalStatus = string(ProposalGranted)
	roleState.LastError = ""
	roleState.UpdatedAt = r.now().UTC()
	record.State.RoleStates[task.Role] = roleState
	record.State.UpdatedAt = roleState.UpdatedAt
	record.Phase = CheckpointRoleRunning
	r.supervisor.emitRun(event.EventMultiAgentRunResumed, record.State, "approved proposal applied")
	var err error
	record, err = r.checkpoint(ctx, record)
	if err != nil {
		return record, err
	}
	return r.finishPreparedRole(ctx, record, task.Role, roleConfig, task.Result, task.StartedAt, task.FinishedAt)
}

// reconcileApprovedTaskHandoff preserves developer-raised issues except for a
// pending-approval claim that identifies a proposal or approval this runtime
// has durably reconciled as applied. The durable lifecycle, not a pre-approval
// model response, is authoritative for that fact.
func reconcileApprovedTaskHandoff(result *RoleRunResult, proposals []ProposalProgress) {
	if result == nil || result.OutgoingHandoff == nil || len(proposals) == 0 {
		return
	}
	identities := make([]string, 0, len(proposals)*2)
	for _, proposal := range proposals {
		identities = append(identities, strings.ToLower(proposal.ProposalID), strings.ToLower(proposal.ApprovalID))
	}
	issues := result.OutgoingHandoff.UnresolvedIssues[:0]
	for _, issue := range result.OutgoingHandoff.UnresolvedIssues {
		summary := strings.ToLower(issue.Summary)
		isPending := strings.Contains(summary, "pending") || strings.Contains(summary, "outstanding")
		identifiesAppliedProposal := false
		for _, identity := range identities {
			if identity != "" && strings.Contains(summary, identity) {
				identifiesAppliedProposal = true
				break
			}
		}
		if isPending && identifiesAppliedProposal {
			continue
		}
		issues = append(issues, issue)
	}
	result.OutgoingHandoff.UnresolvedIssues = issues
	const settled = "Authoritative runtime state: the exact approved proposal was applied and post-apply validation completed; earlier pending-approval statements for that proposal are superseded."
	if !strings.Contains(result.OutgoingHandoff.Notes, settled) {
		result.OutgoingHandoff.Notes = strings.TrimSpace(strings.Join([]string{result.OutgoingHandoff.Notes, settled}, "\n"))
	}
}

func proposalWaitingState(progress ProposalProgress, reason string, safe bool, since time.Time) *WaitingState {
	return &WaitingState{Kind: "proposal_approval", Reason: reason, SafeToRetry: safe, Since: since,
		ProposalID: progress.ProposalID, ApprovalID: progress.ApprovalID}
}

func proposalApplyKey(runID, proposalID string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + proposalID))
	return fmt.Sprintf("apply_%x", sum[:16])
}

func (r *DurableRuntime) emitProposal(eventType string, operation ProposalOperation, reason string) {
	payload := map[string]any{"run_id": operation.RunID, "proposal_id": operation.ProposalID,
		"approval_id": operation.ApprovalID, "apply_key": operation.ApplyKey,
		"workspace_id": operation.WorkspaceID, "execution_key": operation.ExecutionKey}
	if reason != "" {
		payload["reason"] = reason
	}
	r.buffer.Emit(event.NewEvent(eventType, durableEventSource, payload).
		WithCorrelationID(operation.ApplyKey).WithMetadata(event.EventMetadata{RunID: operation.RunID}))
}

func (r *DurableRuntime) emitProposalReconcileFailure(operation ProposalOperation, reason string, result ProposalApplyResult) {
	payload := map[string]any{"run_id": operation.RunID, "proposal_id": operation.ProposalID,
		"approval_id": operation.ApprovalID, "apply_key": operation.ApplyKey,
		"workspace_id": operation.WorkspaceID, "execution_key": operation.ExecutionKey, "reason": reason}
	if validProposalReconcileCode(result.ReconcileCode) {
		payload["reconcile_code"] = string(result.ReconcileCode)
	}
	if validGitTreeID(result.ObservedTree) {
		payload["observed_tree"] = result.ObservedTree
	}
	if validInspectionStage(result.InspectionStage) && validInspectionCategory(result.InspectionCategory) {
		payload["inspection_stage"] = result.InspectionStage
		payload["inspection_category"] = result.InspectionCategory
		if result.InspectionCategory == "command_failed" && result.InspectionExitCode >= 0 && result.InspectionExitCode <= 255 {
			payload["inspection_exit_code"] = result.InspectionExitCode
		}
	}
	r.buffer.Emit(event.NewEvent(event.EventProposalApplyFailed, durableEventSource, payload).
		WithCorrelationID(operation.ApplyKey).WithMetadata(event.EventMetadata{RunID: operation.RunID}))
}

func validProposalReconcileCode(code ProposalReconcileCode) bool {
	switch code {
	case ProposalReconcilePlanMissing, ProposalReconcileInspectFailed, ProposalReconcileContextCancelled,
		ProposalReconcileExpectedTree, ProposalReconcileBaseTree, ProposalReconcileTreeMismatch:
		return true
	default:
		return false
	}
}

func safeProposalReconcileResult(result ProposalApplyResult) ProposalApplyResult {
	safe := ProposalApplyResult{}
	if validProposalReconcileCode(result.ReconcileCode) {
		safe.ReconcileCode = result.ReconcileCode
	}
	if validGitTreeID(result.ObservedTree) {
		safe.ObservedTree = result.ObservedTree
	}
	if validInspectionStage(result.InspectionStage) && validInspectionCategory(result.InspectionCategory) {
		safe.InspectionStage = result.InspectionStage
		safe.InspectionCategory = result.InspectionCategory
		if result.InspectionCategory == "command_failed" && result.InspectionExitCode >= 0 && result.InspectionExitCode <= 255 {
			safe.InspectionExitCode = result.InspectionExitCode
		}
	}
	return safe
}

func validGitTreeID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (r *DurableRuntime) emitProposalResult(eventType string, operation ProposalOperation, result ProposalApplyResult) {
	payload := map[string]any{"run_id": operation.RunID, "proposal_id": operation.ProposalID,
		"approval_id": operation.ApprovalID, "apply_key": operation.ApplyKey,
		"workspace_id": operation.WorkspaceID, "execution_key": operation.ExecutionKey,
		"success": result.Success, "target_path": result.TargetPath, "message": result.Message,
		"diff_path": result.DiffPath, "diff_stat": result.DiffStat}
	r.buffer.Emit(event.NewEvent(eventType, durableEventSource, payload).
		WithCorrelationID(operation.ApplyKey).WithMetadata(event.EventMetadata{RunID: operation.RunID}))
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

func (r *DurableRuntime) pauseForInteractionApproval(
	ctx context.Context,
	record DurableRun,
	interaction InteractionRunRecord,
) (DurableRun, error) {
	now := r.now().UTC()
	role := record.State.CurrentRole
	roleState := record.State.RoleStates[role]
	roleState.Status = RoleStatusWaiting
	roleState.ApprovalStatus = string(InteractionApprovalPending)
	roleState.LastError = boundedDiagnostic(errors.New(interaction.LastError))
	roleState.UpdatedAt = now
	record.State.RoleStates[role] = roleState
	record.State.Status = RunStatusPaused
	record.State.UpdatedAt = now
	record.Phase = CheckpointWaiting
	record.Waiting = &WaitingState{
		Kind:        "interaction_approval",
		Reason:      boundedDiagnostic(errors.New(interaction.LastError)),
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
	failureCode := roleFailureCode(cause)
	diagnostic := durableFailureDiagnostic(cause, record.Retry)
	if record.Retry != nil {
		roleState := record.State.RoleStates[role]
		roleState.LocalIterations += record.Retry.AccumulatedIterations
		roleState.Retries += record.Retry.RecoveryNumber
		roleState.TokenUsage = addTokenUsage(roleState.TokenUsage, record.Retry.AccumulatedUsage)
		roleState.UpdatedAt = r.now().UTC()
		roleState.CompletedAt = timePointer(roleState.UpdatedAt)
		record.State.RoleStates[role] = roleState
		record.State.BudgetUsage.TotalLocalIterations += record.Retry.AccumulatedIterations
		record.State.BudgetUsage.Retries += record.Retry.RecoveryNumber
		record.State.BudgetUsage.Tokens = addTokenUsage(record.State.BudgetUsage.Tokens, record.Retry.AccumulatedUsage)
		// Keep provider/tool bodies out of terminal run state and events.
		cause = errors.New(failureCode)
		appendRetryHistory(&record, record.Retry)
		record.Retry = nil
	}
	var governance *GovernanceError
	if errors.As(cause, &governance) && governance.Kind == "validation_profile_configuration" {
		cause = errors.New(failureCode)
	}
	state, terminalErr := r.supervisor.failRoleWithRetryDiagnostic(record.State, role, cause, diagnostic)
	record.State = state
	record.Phase = CheckpointTerminal
	record.Failure = diagnostic
	record.Failure.Kind = "role_execution"
	record.Failure.Message = failureCode
	record.Failure.At = r.now().UTC()
	record.PendingTransition = nil
	record.Waiting = nil
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
	if record.Phase == CheckpointTerminal {
		appendRetryHistory(&record, record.Retry)
		record.Retry = nil
	}
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
