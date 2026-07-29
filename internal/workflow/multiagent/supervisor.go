package multiagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/emaharmony/prism/internal/cost"
	"github.com/emaharmony/prism/internal/event"
	"github.com/emaharmony/prism/internal/validation"
)

const supervisorEventSource = "prism-multi-agent-supervisor"

// SupervisorOptions contains deterministic seams for tests and embedding.
type SupervisorOptions struct {
	Clock        func() time.Time
	NewHandoffID func() string
	Logger       *slog.Logger
}

// Supervisor is the single logical writer of one in-memory multi-agent run.
type Supervisor struct {
	definition   Definition
	runner       RoleRunner
	events       EventSink
	resolver     *TransitionResolver
	now          func() time.Time
	newHandoffID func() string
	logger       *slog.Logger
}

// NewSupervisor constructs an in-memory supervisor from a validated definition.
func NewSupervisor(
	definition Definition,
	runner RoleRunner,
	events EventSink,
	options SupervisorOptions,
) (*Supervisor, error) {
	if runner == nil {
		return nil, errors.New("multiagent: role runner is required")
	}
	if events == nil {
		return nil, errors.New("multiagent: event sink is required")
	}

	definition = cloneDefinition(definition)
	resolver, err := NewTransitionResolver(definition)
	if err != nil {
		return nil, err
	}

	now := options.Clock
	if now == nil {
		now = time.Now
	}
	newHandoffID := options.NewHandoffID
	if newHandoffID == nil {
		newHandoffID = func() string {
			return "handoff_" + strings.TrimPrefix(event.NewID(), "evt_")
		}
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Supervisor{
		definition:   definition,
		runner:       runner,
		events:       events,
		resolver:     resolver,
		now:          now,
		newHandoffID: newHandoffID,
		logger:       logger,
	}, nil
}

// Run executes the bounded Phase 1 flow entirely in memory.
func (s *Supervisor) Run(ctx context.Context, request RunRequest) (RunState, error) {
	if strings.TrimSpace(request.RunID) == "" {
		return RunState{}, errors.New("multiagent: run id is required")
	}
	if strings.TrimSpace(request.Task.ID) == "" {
		return RunState{}, errors.New("multiagent: task id is required")
	}

	state := s.newRunState(request)
	s.emitRun(event.EventMultiAgentRunStarted, state, "")
	s.log(state, "run started", slog.String("role", string(state.CurrentRole)))

	for {
		if err := ctx.Err(); err != nil {
			return s.cancelRun(state, err)
		}
		if budgetErr := s.checkBeforeRole(state); budgetErr != nil {
			return s.exhaustRun(state, budgetErr)
		}

		role := state.CurrentRole
		roleConfig, _ := s.definition.RoleConfig(role)
		s.enterRole(&state, role)

		startedAt := s.now().UTC()
		result, runErr := s.runner.RunRole(ctx, RoleRunRequest{
			Run:        s.runView(state),
			RoleConfig: cloneRoleConfig(roleConfig),
		})
		finishedAt := s.now().UTC()

		if ctxErr := ctx.Err(); ctxErr != nil {
			roleState := state.RoleStates[role]
			roleState.Status = RoleStatusCancelled
			roleState.LastError = ctxErr.Error()
			roleState.UpdatedAt = finishedAt
			roleState.CompletedAt = timePointer(finishedAt)
			state.RoleStates[role] = roleState
			return s.cancelRun(state, ctxErr)
		}
		if runErr != nil {
			return s.failRole(state, role, &RoleExecutionError{Role: role, Cause: runErr})
		}
		if err := validateRoleRunResult(result); err != nil {
			return s.failRole(state, role, &RoleExecutionError{Role: role, Cause: err})
		}

		transition, err := s.resolver.Resolve(role, result.Outcome)
		if err != nil {
			return s.failRole(state, role, err)
		}

		s.completeRole(&state, role, result, startedAt, finishedAt)
		if budgetErr := s.checkAfterRole(state, role, roleConfig); budgetErr != nil {
			return s.exhaustRun(state, budgetErr)
		}
		if budgetErr := s.checkLoopBudget(state, transition); budgetErr != nil {
			return s.exhaustRun(state, budgetErr)
		}

		if transition.Terminal == "" {
			handoff, err := s.createHandoff(state, transition, result.OutgoingHandoff)
			if err != nil {
				return s.failRole(state, role, &RoleExecutionError{Role: role, Cause: err})
			}
			state.LatestHandoff = &handoff
			s.emitHandoff(state, handoff)
		} else if result.OutgoingHandoff != nil {
			return s.failRole(
				state,
				role,
				&RoleExecutionError{
					Role:  role,
					Cause: errors.New("terminal role result must not include an outgoing handoff"),
				},
			)
		}

		state.TransitionCount++
		state.UpdatedAt = s.now().UTC()
		s.emitTransition(state, transition)
		s.log(
			state,
			"transition selected",
			slog.String("role", string(role)),
			slog.String("outcome", string(result.Outcome)),
			slog.String("transition", transitionLabel(transition)),
			slog.Int("iteration", state.TransitionCount),
		)

		if loopKind, ok := correctionLoop(transition); ok {
			state.LoopTraversals.increment(loopKind)
			s.emitLoopTraversal(state, transition)
		}

		if transition.Terminal != "" {
			return s.finishTerminal(state, transition)
		}

		state.CurrentRole = transition.To
		state.UpdatedAt = s.now().UTC()
	}
}

func (s *Supervisor) newRunState(request RunRequest) RunState {
	now := s.now().UTC()
	roleStates := make(map[Role]RoleState, len(s.definition.Roles))
	for _, cfg := range s.definition.Roles {
		roleStates[cfg.Role] = RoleState{
			Role:      cfg.Role,
			Status:    RoleStatusPending,
			UpdatedAt: now,
		}
	}
	return RunState{
		SchemaVersion:   RunStateSchemaVersion,
		RunID:           request.RunID,
		WorkflowID:      s.definition.ID,
		WorkflowVersion: s.definition.Version,
		CurrentRole:     RolePlanner,
		CurrentTask:     request.Task,
		Status:          RunStatusRunning,
		RoleStates:      roleStates,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func (s *Supervisor) enterRole(state *RunState, role Role) {
	now := s.now().UTC()
	roleState := state.RoleStates[role]
	roleState.Status = RoleStatusRunning
	roleState.Visits++
	roleState.EnteredAt = timePointer(now)
	roleState.CompletedAt = nil
	roleState.LastError = ""
	roleState.UpdatedAt = now
	state.RoleStates[role] = roleState
	state.UpdatedAt = now

	s.emitRole(event.EventMultiAgentRoleEntered, *state, roleState, "", nil)
	s.log(
		*state,
		"role entered",
		slog.String("role", string(role)),
		slog.Int("iteration", roleState.Visits),
	)
}

func (s *Supervisor) completeRole(
	state *RunState,
	role Role,
	result RoleRunResult,
	startedAt time.Time,
	finishedAt time.Time,
) {
	roleState := state.RoleStates[role]
	switch result.Outcome {
	case OutcomeCancelled:
		roleState.Status = RoleStatusCancelled
	case OutcomeTerminalFailure:
		roleState.Status = RoleStatusFailed
	default:
		roleState.Status = RoleStatusCompleted
	}
	roleState.LocalIterations += result.LocalIterations
	roleState.Retries += result.Retries
	roleState.TokenUsage = addTokenUsage(roleState.TokenUsage, result.TokenUsage)
	if elapsed := finishedAt.Sub(startedAt); elapsed > 0 {
		roleState.Elapsed += elapsed
	}
	roleState.LastOutcome = result.Outcome
	roleState.WorkspaceID = result.Metadata.WorkspaceID
	if workspaceID := strings.TrimSpace(result.Metadata.WorkspaceID); workspaceID != "" && state.WorkspaceID == "" {
		state.WorkspaceID = workspaceID
	}
	roleState.ValidationStatus = result.Metadata.ValidationStatus
	roleState.ApprovalStatus = result.Metadata.ApprovalStatus
	if result.OutgoingHandoff != nil {
		roleState.Artifacts = append(
			cloneArtifactRefs(result.OutgoingHandoff.Artifacts),
			cloneArtifactRefs(result.OutgoingHandoff.Evidence)...,
		)
	}
	roleState.UpdatedAt = finishedAt
	roleState.CompletedAt = timePointer(finishedAt)
	state.RoleStates[role] = roleState

	state.BudgetUsage.TotalLocalIterations += result.LocalIterations
	state.BudgetUsage.Retries += result.Retries
	state.BudgetUsage.Tokens = addTokenUsage(state.BudgetUsage.Tokens, result.TokenUsage)
	if result.Outcome == OutcomeTestsFailed || result.Outcome == OutcomeChangesRequested {
		state.BudgetUsage.RepeatedFailures++
	}
	if elapsed := finishedAt.Sub(state.CreatedAt); elapsed > 0 {
		state.BudgetUsage.Elapsed = elapsed
	}
	state.UpdatedAt = finishedAt
	state.LatestCompletedRole = role

	s.emitRole(event.EventMultiAgentRoleCompleted, *state, roleState, result.Outcome, &result)
}

func (s *Supervisor) checkBeforeRole(state RunState) *BudgetExceededError {
	if exceeded(state.TransitionCount+1, s.definition.Budgets.MaxTransitions) {
		return newBudgetError("max_transitions", "", state.TransitionCount+1, s.definition.Budgets.MaxTransitions)
	}
	roleState := state.RoleStates[state.CurrentRole]
	visitLimit := s.definition.Budgets.MaxVisitsPerRole[state.CurrentRole]
	if exceeded(roleState.Visits+1, visitLimit) {
		return newBudgetError("max_visits_per_role", state.CurrentRole, roleState.Visits+1, visitLimit)
	}
	if s.definition.Budgets.MaxDuration != UnlimitedDuration &&
		s.now().UTC().Sub(state.CreatedAt) >= s.definition.Budgets.MaxDuration {
		return &BudgetExceededError{
			Budget: "max_duration",
			Used:   durationBudgetValue(s.now().UTC().Sub(state.CreatedAt)),
			Limit:  durationBudgetValue(s.definition.Budgets.MaxDuration),
		}
	}
	return nil
}

func (s *Supervisor) checkAfterRole(
	state RunState,
	role Role,
	config RoleConfig,
) *BudgetExceededError {
	roleState := state.RoleStates[role]
	checks := []struct {
		name  string
		role  Role
		used  int
		limit Limit
	}{
		{"max_local_iterations", "", state.BudgetUsage.TotalLocalIterations, s.definition.Budgets.MaxLocalIterations},
		{"role_max_local_iterations", role, roleState.LocalIterations, config.MaxLocalIterations},
		{"max_retries", "", state.BudgetUsage.Retries, s.definition.Budgets.MaxRetries},
		{"max_tokens", "", state.BudgetUsage.Tokens.TotalTokens, s.definition.Budgets.MaxTokens},
		{"role_token_budget", role, roleState.TokenUsage.TotalTokens, config.TokenBudget},
		{
			"max_repeated_failures",
			"",
			state.BudgetUsage.RepeatedFailures,
			s.definition.Budgets.MaxRepeatedFailure,
		},
	}
	for _, check := range checks {
		if exceeded(check.used, check.limit) {
			return newBudgetError(check.name, check.role, check.used, check.limit)
		}
	}
	if config.TimeBudget != UnlimitedDuration && roleState.Elapsed > config.TimeBudget {
		return &BudgetExceededError{
			Budget: "role_time_budget",
			Role:   role,
			Used:   durationBudgetValue(roleState.Elapsed),
			Limit:  durationBudgetValue(config.TimeBudget),
		}
	}
	if s.definition.Budgets.MaxDuration != UnlimitedDuration &&
		state.BudgetUsage.Elapsed > s.definition.Budgets.MaxDuration {
		return &BudgetExceededError{
			Budget: "max_duration",
			Used:   durationBudgetValue(state.BudgetUsage.Elapsed),
			Limit:  durationBudgetValue(s.definition.Budgets.MaxDuration),
		}
	}
	return nil
}

func (s *Supervisor) checkLoopBudget(
	state RunState,
	transition ResolvedTransition,
) *BudgetExceededError {
	kind, ok := correctionLoop(transition)
	if !ok {
		return nil
	}
	used := state.LoopTraversals.Get(kind) + 1
	var limit Limit
	switch kind {
	case LoopTesterToDeveloper:
		limit = s.definition.Budgets.MaxTesterToDeveloperLoops
	case LoopReviewerToDeveloper:
		limit = s.definition.Budgets.MaxReviewerToDeveloperLoops
	}
	if exceeded(used, limit) {
		return newBudgetError("max_"+string(kind)+"_loops", transition.From, used, limit)
	}
	return nil
}

func (s *Supervisor) createHandoff(
	state RunState,
	transition ResolvedTransition,
	draft *HandoffDraft,
) (Handoff, error) {
	if draft == nil {
		return Handoff{}, errors.New("non-terminal role result requires an outgoing handoff")
	}
	id := strings.TrimSpace(draft.ID)
	if id == "" {
		id = s.newHandoffID()
	}
	taskRef := strings.TrimSpace(draft.TaskRef)
	if taskRef == "" {
		taskRef = state.CurrentTask.ID
	}
	handoff := Handoff{
		ID:                id,
		RunID:             state.RunID,
		SourceRole:        transition.From,
		DestinationRole:   transition.To,
		TaskRef:           taskRef,
		Objective:         draft.Objective,
		Artifacts:         cloneArtifactRefs(draft.Artifacts),
		Evidence:          cloneArtifactRefs(draft.Evidence),
		Outcome:           transition.Outcome,
		Reason:            draft.Reason,
		ValidationResults: append([]validation.Result(nil), draft.ValidationResults...),
		UnresolvedIssues:  cloneIssues(draft.UnresolvedIssues),
		Notes:             draft.Notes,
		CreatedAt:         s.now().UTC(),
	}
	if err := handoff.Validate(s.definition); err != nil {
		return Handoff{}, err
	}
	return handoff, nil
}

func (s *Supervisor) finishTerminal(
	state RunState,
	transition ResolvedTransition,
) (RunState, error) {
	switch transition.Terminal {
	case TerminalConditionCompleted:
		return s.completeRun(state), nil
	case TerminalConditionCancelled:
		return s.cancelRun(state, context.Canceled)
	case TerminalConditionFailed:
		err := &RoleExecutionError{
			Role:  transition.From,
			Cause: errors.New("runner returned terminal failure"),
		}
		return s.failRun(state, err), err
	default:
		err := &InvalidTransitionError{Role: transition.From, Outcome: transition.Outcome}
		return s.failRun(state, err), err
	}
}

func (s *Supervisor) completeRun(state RunState) RunState {
	now := s.now().UTC()
	state.Status = RunStatusCompleted
	state.TerminalOutcome = &TerminalOutcome{
		Condition: TerminalConditionCompleted,
		Reason:    "review approved",
		At:        now,
	}
	state.CompletedAt = timePointer(now)
	state.UpdatedAt = now
	s.emitRun(event.EventMultiAgentRunCompleted, state, state.TerminalOutcome.Reason)
	s.log(state, "run completed")
	return state
}

func (s *Supervisor) failRole(state RunState, role Role, err error) (RunState, error) {
	now := s.now().UTC()
	roleState := state.RoleStates[role]
	roleState.Status = RoleStatusFailed
	roleState.LastError = err.Error()
	roleState.UpdatedAt = now
	roleState.CompletedAt = timePointer(now)
	state.RoleStates[role] = roleState
	return s.failRun(state, err), err
}

func (s *Supervisor) failRun(state RunState, err error) RunState {
	now := s.now().UTC()
	state.Status = RunStatusFailed
	state.TerminalOutcome = &TerminalOutcome{
		Condition: TerminalConditionFailed,
		Reason:    err.Error(),
		At:        now,
	}
	state.CompletedAt = timePointer(now)
	state.UpdatedAt = now
	s.emitRun(event.EventMultiAgentRunFailed, state, err.Error())
	s.log(state, "run failed", slog.String("outcome", "failed"), slog.String("error", err.Error()))
	return state
}

func (s *Supervisor) cancelRun(state RunState, err error) (RunState, error) {
	if err == nil {
		err = context.Canceled
	}
	now := s.now().UTC()
	state.Status = RunStatusCancelled
	state.CancellationReason = err.Error()
	state.TerminalOutcome = &TerminalOutcome{
		Condition: TerminalConditionCancelled,
		Reason:    err.Error(),
		At:        now,
	}
	state.CompletedAt = timePointer(now)
	state.UpdatedAt = now
	s.emitRun(event.EventMultiAgentRunCancelled, state, err.Error())
	s.log(state, "run cancelled", slog.String("outcome", "cancelled"))
	return state, err
}

func (s *Supervisor) exhaustRun(
	state RunState,
	err *BudgetExceededError,
) (RunState, error) {
	now := s.now().UTC()
	state.Status = RunStatusBudgetExhausted
	state.TerminalOutcome = &TerminalOutcome{
		Condition: TerminalConditionBudgetExhausted,
		Reason:    err.Error(),
		At:        now,
	}
	state.CompletedAt = timePointer(now)
	state.UpdatedAt = now
	s.emitBudgetExhausted(state, err)
	s.log(
		state,
		"budget exhausted",
		slog.String("outcome", "budget_exhausted"),
		slog.String("role", string(err.Role)),
		slog.String("budget", err.Budget),
	)
	return state, err
}

func (s *Supervisor) runView(state RunState) RunView {
	return RunView{
		RunID:           state.RunID,
		WorkflowID:      state.WorkflowID,
		Task:            state.CurrentTask,
		WorkspaceID:     state.WorkspaceID,
		CurrentRole:     state.CurrentRole,
		ExecutionKey:    state.RoleStates[state.CurrentRole].LastExecutionKey,
		Visit:           state.RoleStates[state.CurrentRole].Visits,
		TransitionCount: state.TransitionCount,
		LoopTraversals:  state.LoopTraversals,
		BudgetUsage:     state.BudgetUsage,
		IncomingHandoff: cloneHandoff(state.LatestHandoff),
	}
}

func (s *Supervisor) emitRun(eventType string, state RunState, reason string) {
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
	s.emit(eventType, state, payload)
}

func (s *Supervisor) emitRole(
	eventType string,
	state RunState,
	roleState RoleState,
	outcome TransitionOutcome,
	result *RoleRunResult,
) {
	payload := map[string]any{
		"run_id":      state.RunID,
		"workflow_id": state.WorkflowID,
		"role":        string(roleState.Role),
		"status":      string(roleState.Status),
		"visit":       roleState.Visits,
	}
	if outcome != "" {
		payload["outcome"] = string(outcome)
	}
	if result != nil {
		payload["token_usage"] = event.TokenUsage{
			PromptTokens:     result.TokenUsage.PromptTokens,
			CompletionTokens: result.TokenUsage.CompletionTokens,
			TotalTokens:      result.TokenUsage.TotalTokens,
			EstimatedCostUsd: result.TokenUsage.EstimatedCostUsd,
		}
		if result.Metadata.AgentRef != "" {
			payload["agent_ref"] = result.Metadata.AgentRef
		}
		if result.Metadata.Provider != "" {
			payload["provider"] = result.Metadata.Provider
		}
		if result.Metadata.Model != "" {
			payload["model"] = result.Metadata.Model
		}
		if !result.Metadata.StartedAt.IsZero() &&
			!result.Metadata.FinishedAt.IsZero() {
			payload["duration_ms"] = result.Metadata.FinishedAt.
				Sub(result.Metadata.StartedAt).Milliseconds()
		}
		if result.Metadata.ToolCalls > 0 {
			payload["tool_calls"] = result.Metadata.ToolCalls
		}
		if result.Metadata.DeniedToolCalls > 0 {
			payload["denied_tool_calls"] = result.Metadata.DeniedToolCalls
		}
		if result.Metadata.ValidationStatus != "" {
			payload["validation_status"] = result.Metadata.ValidationStatus
		}
		if result.Metadata.ApprovalStatus != "" {
			payload["approval_status"] = result.Metadata.ApprovalStatus
		}
		if result.OutgoingHandoff != nil {
			payload["artifact_uris"] = artifactURIs(result.OutgoingHandoff.Artifacts)
		}
	}
	s.emit(eventType, state, payload)
}

func (s *Supervisor) emitHandoff(state RunState, handoff Handoff) {
	s.emit(event.EventMultiAgentHandoffCreated, state, map[string]any{
		"run_id":           state.RunID,
		"workflow_id":      state.WorkflowID,
		"handoff_id":       handoff.ID,
		"source_role":      string(handoff.SourceRole),
		"destination_role": string(handoff.DestinationRole),
		"task_ref":         handoff.TaskRef,
		"outcome":          string(handoff.Outcome),
	})
}

func (s *Supervisor) emitTransition(state RunState, transition ResolvedTransition) {
	payload := transitionPayload(state, transition)
	s.emit(event.EventMultiAgentTransitionSelected, state, payload)
}

func (s *Supervisor) emitLoopTraversal(state RunState, transition ResolvedTransition) {
	payload := transitionPayload(state, transition)
	s.emit(event.EventMultiAgentLoopTraversal, state, payload)
}

func (s *Supervisor) emitBudgetExhausted(state RunState, budgetErr *BudgetExceededError) {
	payload := map[string]any{
		"run_id":      state.RunID,
		"workflow_id": state.WorkflowID,
		"budget":      budgetErr.Budget,
		"used":        budgetErr.Used,
		"limit":       budgetErr.Limit,
		"reason":      budgetErr.Error(),
	}
	if budgetErr.Role != "" {
		payload["role"] = string(budgetErr.Role)
	}
	s.emit(event.EventMultiAgentBudgetExhausted, state, payload)
}

func (s *Supervisor) emit(eventType string, state RunState, payload map[string]any) {
	now := s.now().UTC()
	evt := event.NewEvent(eventType, supervisorEventSource, payload).
		WithCorrelationID(state.RunID).
		WithMetadata(event.EventMetadata{RunID: state.RunID})
	evt.Timestamp = now.Format(time.RFC3339Nano)
	s.events.Emit(evt)
}

func (s *Supervisor) log(state RunState, message string, attrs ...any) {
	base := []any{
		slog.String("run_id", state.RunID),
		slog.String("workflow_id", state.WorkflowID),
	}
	s.logger.Info(message, append(base, attrs...)...)
}

func transitionPayload(state RunState, transition ResolvedTransition) map[string]any {
	payload := map[string]any{
		"run_id":           state.RunID,
		"workflow_id":      state.WorkflowID,
		"source_role":      string(transition.From),
		"outcome":          string(transition.Outcome),
		"transition_count": state.TransitionCount,
	}
	if transition.To != "" {
		payload["destination_role"] = string(transition.To)
	}
	if transition.Terminal != "" {
		payload["terminal_condition"] = string(transition.Terminal)
	}
	return payload
}

func validateRoleRunResult(result RoleRunResult) error {
	if !result.Outcome.Valid() {
		return fmt.Errorf("runner returned unsupported outcome %q", result.Outcome)
	}
	if result.LocalIterations <= 0 {
		return errors.New("runner local iterations must be positive")
	}
	if result.Retries < 0 {
		return errors.New("runner retries must be non-negative")
	}
	if result.TokenUsage.PromptTokens < 0 ||
		result.TokenUsage.CompletionTokens < 0 ||
		result.TokenUsage.TotalTokens < 0 ||
		result.TokenUsage.EstimatedCostUsd < 0 {
		return errors.New("runner token usage must be non-negative")
	}
	return nil
}

func correctionLoop(transition ResolvedTransition) (LoopKind, bool) {
	switch {
	case transition.From == RoleTester &&
		transition.Outcome == OutcomeTestsFailed &&
		transition.To == RoleDeveloper:
		return LoopTesterToDeveloper, true
	case transition.From == RoleReviewer &&
		transition.Outcome == OutcomeChangesRequested &&
		transition.To == RoleDeveloper:
		return LoopReviewerToDeveloper, true
	default:
		return "", false
	}
}

func exceeded(used int, limit Limit) bool {
	return limit != Unlimited && used > int(limit)
}

func newBudgetError(name string, role Role, used int, limit Limit) *BudgetExceededError {
	return &BudgetExceededError{
		Budget: name,
		Role:   role,
		Used:   used,
		Limit:  int(limit),
	}
}

func durationBudgetValue(duration time.Duration) int {
	return int(duration.Milliseconds())
}

func addTokenUsage(a cost.TokenUsage, b cost.TokenUsage) cost.TokenUsage {
	return cost.TokenUsage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
		EstimatedCostUsd: a.EstimatedCostUsd + b.EstimatedCostUsd,
	}
}

func cloneDefinition(definition Definition) Definition {
	cloned := definition
	cloned.Roles = make([]RoleConfig, len(definition.Roles))
	for i, config := range definition.Roles {
		cloned.Roles[i] = cloneRoleConfig(config)
	}
	cloned.Transitions = append([]TransitionRule(nil), definition.Transitions...)
	cloned.Budgets.MaxVisitsPerRole = make(map[Role]Limit, len(definition.Budgets.MaxVisitsPerRole))
	for role, limit := range definition.Budgets.MaxVisitsPerRole {
		cloned.Budgets.MaxVisitsPerRole[role] = limit
	}
	return cloned
}

func cloneRoleConfig(config RoleConfig) RoleConfig {
	config.AllowedTools = append([]string(nil), config.AllowedTools...)
	config.Capabilities = append([]string(nil), config.Capabilities...)
	config.Approval.Approvers = append([]Role(nil), config.Approval.Approvers...)
	config.ValidationProfiles = append([]string(nil), config.ValidationProfiles...)
	return config
}

func cloneHandoff(handoff *Handoff) *Handoff {
	if handoff == nil {
		return nil
	}
	cloned := *handoff
	cloned.Artifacts = cloneArtifactRefs(handoff.Artifacts)
	cloned.Evidence = cloneArtifactRefs(handoff.Evidence)
	cloned.ValidationResults = append(cloned.ValidationResults[:0:0], handoff.ValidationResults...)
	cloned.UnresolvedIssues = cloneIssues(handoff.UnresolvedIssues)
	return &cloned
}

func cloneArtifactRefs(refs []ArtifactRef) []ArtifactRef {
	return append([]ArtifactRef(nil), refs...)
}

func cloneIssues(issues []Issue) []Issue {
	return append([]Issue(nil), issues...)
}

func artifactURIs(refs []ArtifactRef) []string {
	uris := make([]string, 0, len(refs))
	for _, ref := range refs {
		uris = append(uris, ref.URI)
	}
	return uris
}

func transitionLabel(transition ResolvedTransition) string {
	if transition.To != "" {
		return string(transition.From) + "->" + string(transition.To)
	}
	return string(transition.From) + "->" + string(transition.Terminal)
}

func timePointer(value time.Time) *time.Time {
	return &value
}
