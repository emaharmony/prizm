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
	graph        *CompiledGraph
	entryRole    Role
	runner       RoleRunner
	events       EventSink
	now          func() time.Time
	newHandoffID func() string
	logger       *slog.Logger
}

// NewSupervisor constructs an in-memory supervisor from an already-compiled
// graph. Before Phase 3 this took a legacy Definition and built its own
// *TransitionResolver, validating the definition itself
// (definition.Validate()) as part of construction; CompiledGraph absorbed
// TransitionResolver entirely, and validation/compilation now happens before
// NewSupervisor is ever called (ValidateDefinition+Compile for an authored
// graph, Definition.Validate for a CompatAdaptDefinition-adapted one) — so
// construction here is mostly a nil check, not a validate-and-build step.
// CompiledGraph is already immutable (every accessor returns an independent
// copy), so there is no cloneDefinition-equivalent defensive copy to make.
//
// The one exception is the entry role: NewSupervisor resolves and caches
// graph.EntryNodeID()'s role here, once, so Run() (and newRunState, which it
// calls on every run) never needs to re-derive it and can never silently
// fall back to a hardcoded role. This should be unreachable in practice —
// PR2's ValidateDefinition (structural.entry-node-missing and friends)
// already guarantees a compiled graph's entry node exists and resolves to a
// role/terminal node — but catching it here, at construction time, means a
// caller gets a clear error immediately instead of Run() failing
// confusingly (or worse, silently defaulting to the wrong role) on every
// invocation.
func NewSupervisor(
	graph *CompiledGraph,
	runner RoleRunner,
	events EventSink,
	options SupervisorOptions,
) (*Supervisor, error) {
	if graph == nil {
		return nil, errors.New("multiagent: compiled graph is required")
	}
	if runner == nil {
		return nil, errors.New("multiagent: role runner is required")
	}
	if events == nil {
		return nil, errors.New("multiagent: event sink is required")
	}

	entryNode, ok := graph.Node(graph.EntryNodeID())
	if !ok || entryNode.Kind != "role" {
		return nil, fmt.Errorf(
			"multiagent: compiled graph entry node %q is not a valid role node (kind=%q, found=%t)",
			graph.EntryNodeID(), entryNode.Kind, ok,
		)
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
		graph:        graph,
		entryRole:    entryNode.Role,
		runner:       runner,
		events:       events,
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
		roleConfig, _ := s.graph.RoleConfig(role)
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
		if err := validateRoleRunResult(s.graph, result); err != nil {
			return s.failRole(state, role, &RoleExecutionError{Role: role, Cause: err})
		}

		transition, err := s.graph.Resolve(role, result.Outcome)
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

		if loop, ok := s.graph.LoopFor(transition); ok {
			state.LoopTraversals.increment(loop.Kind)
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
	nodes := s.graph.Nodes()
	roleStates := make(map[Role]RoleState, len(nodes))
	for _, node := range nodes {
		if node.Kind != "role" {
			continue
		}
		roleStates[node.Role] = RoleState{
			Role:      node.Role,
			Status:    RoleStatusPending,
			UpdatedAt: now,
		}
	}
	return RunState{
		SchemaVersion: RunStateSchemaVersion,
		RunID:         request.RunID,
		WorkflowID:    s.graph.WorkflowID(),
		// WorkflowVersion is the registry-assigned internal version
		// (CompiledGraph.RegistryVersion) rather than the old user-facing
		// int; WorkflowUserVersion/WorkflowFingerprint are the new additive
		// fields carrying what used to be implied by the int alone. See
		// state.go's RunState field docs.
		WorkflowVersion:     s.graph.RegistryVersion(),
		WorkflowUserVersion: s.graph.WorkflowVersion(),
		WorkflowFingerprint: s.graph.Fingerprint(),
		// CurrentRole is derived from the compiled graph's own entry node
		// (s.entryRole, resolved once in NewSupervisor from
		// graph.EntryNodeID()), not hardcoded to RolePlanner. A
		// CompatAdaptDefinition-derived graph's entry node always IS the
		// RolePlanner node (Definition.Validate requires it), so the Phase 1
		// compat path resolves to the exact same role as before; an
		// authored graph with an arbitrary entry role (e.g. "implementer"
		// for a Security Review template) now starts there instead of
		// failing at the very first RoleConfig lookup.
		CurrentRole: s.entryRole,
		CurrentTask: request.Task,
		Status:      RunStatusRunning,
		RoleStates:  roleStates,
		CreatedAt:   now,
		UpdatedAt:   now,
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
	budgets := s.graph.Budgets()
	if exceeded(state.TransitionCount+1, budgets.MaxTransitions) {
		return newBudgetError("max_transitions", "", state.TransitionCount+1, budgets.MaxTransitions)
	}
	roleState := state.RoleStates[state.CurrentRole]
	visitLimit := budgets.MaxVisitsPerRole[state.CurrentRole]
	if exceeded(roleState.Visits+1, visitLimit) {
		return newBudgetError("max_visits_per_role", state.CurrentRole, roleState.Visits+1, visitLimit)
	}
	if budgets.MaxDuration != UnlimitedDuration &&
		s.now().UTC().Sub(state.CreatedAt) >= budgets.MaxDuration {
		return &BudgetExceededError{
			Budget: "max_duration",
			Used:   durationBudgetValue(s.now().UTC().Sub(state.CreatedAt)),
			Limit:  durationBudgetValue(budgets.MaxDuration),
		}
	}
	return nil
}

func (s *Supervisor) checkAfterRole(
	state RunState,
	role Role,
	config RoleConfig,
) *BudgetExceededError {
	budgets := s.graph.Budgets()
	roleState := state.RoleStates[role]
	checks := []struct {
		name  string
		role  Role
		used  int
		limit Limit
	}{
		{"max_local_iterations", "", state.BudgetUsage.TotalLocalIterations, budgets.MaxLocalIterations},
		{"role_max_local_iterations", role, roleState.LocalIterations, config.MaxLocalIterations},
		{"max_retries", "", state.BudgetUsage.Retries, budgets.MaxRetries},
		{"max_tokens", "", state.BudgetUsage.Tokens.TotalTokens, budgets.MaxTokens},
		{"role_token_budget", role, roleState.TokenUsage.TotalTokens, config.TokenBudget},
		{
			"max_repeated_failures",
			"",
			state.BudgetUsage.RepeatedFailures,
			budgets.MaxRepeatedFailure,
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
	if budgets.MaxDuration != UnlimitedDuration &&
		state.BudgetUsage.Elapsed > budgets.MaxDuration {
		return &BudgetExceededError{
			Budget: "max_duration",
			Used:   durationBudgetValue(state.BudgetUsage.Elapsed),
			Limit:  durationBudgetValue(budgets.MaxDuration),
		}
	}
	return nil
}

// checkLoopBudget replaces the old hardcoded switch over
// LoopTesterToDeveloper/LoopReviewerToDeveloper with a lookup through the
// compiled graph's own precomputed loop metadata (CompiledGraph.LoopFor) —
// generic to any number of developer-defined loops, not just the two Phase 1
// built-ins. loopBudgetName (supervisor_types.go) keeps the exact
// "max_tester_to_developer_loops"/"max_reviewer_to_developer_loops" budget
// names stable for those two, since LoopKind's underlying value is no longer
// that literal slug (it is now edgeID-shaped).
func (s *Supervisor) checkLoopBudget(
	state RunState,
	transition ResolvedTransition,
) *BudgetExceededError {
	loop, ok := s.graph.LoopFor(transition)
	if !ok {
		return nil
	}
	used := state.LoopTraversals.Get(loop.Kind) + 1
	if exceeded(used, loop.MaxTraversals) {
		return newBudgetError(loopBudgetName(loop.Kind), transition.From, used, loop.MaxTraversals)
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
	if err := handoff.Validate(s.graph); err != nil {
		return Handoff{}, err
	}
	return handoff, nil
}

// finishTerminal resolves a transition whose destination is a terminal node.
// Phase 1 only ever declared three terminal conditions
// (completed/cancelled/failed — TerminalConditionBudgetExhausted is reached
// through a different path, exhaustRun, not through a role's own declared
// transition), so those three keep their exact, pre-Phase-3 behavior here
// unchanged (completeRun/cancelRun/failRun, byte-identical to before this
// PR — e2e_dashboard_scenario_test.go asserts completeRun's exact
// Reason string literally, so that call is untouched).
//
// A Phase 3 authored workflow, by contrast, may declare ANY terminal
// condition string (SchemaNode.TerminalCondition has no enum — the
// validator and compiler both already treat it as an open, developer-chosen
// value; see schema_types.go and compiler.go). Before this fix, ANY
// terminal condition outside the three legacy values fell through to the
// `default` case below and was rejected as an *InvalidTransitionError* —
// meaning every authored workflow that used its own terminal condition name
// (e.g. "published", "needs_revision") could never actually finish a run
// through that edge, even though `prism graph validate`/`compile` accepted
// the definition with zero errors. This is a real bug found while building
// this PR's shipped templates (docs/workflows/security.md and this PR's
// final report document it in full), not a hypothetical: templates/
// documentation-change.yaml's very first scenario tripped it.
//
// The fix: any terminal condition that is not one of the three legacy
// values now completes the run successfully (RunStatusCompleted), carrying
// the AUTHOR'S ACTUAL declared condition string (not a hardcoded
// "completed") and a generic, condition-derived reason — reaching a
// terminal node via a real, validated, routed edge is by definition a
// successful, designed run outcome; nothing in the schema distinguishes a
// "success" terminal from a "failure" terminal by name (there is no
// separate boolean), so treating "the graph said to stop here" as success
// is the least surprising, most conservative available default. A workflow
// author who wants failure semantics for a given terminal already has the
// tool for it: name that terminal's condition "failed" (or route to it via
// TerminalConditionFailed) to get the existing, unchanged failure path.
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
		return s.completeRunWithCondition(state, transition.Terminal), nil
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

// completeRunWithCondition is completeRun's generalization for an authored
// workflow's own (non-legacy) terminal condition — see finishTerminal's doc
// comment above for why this exists as a separate function rather than a
// change to completeRun itself (completeRun's exact output is pinned by an
// existing Phase 1 test and must not change).
func (s *Supervisor) completeRunWithCondition(state RunState, condition TerminalCondition) RunState {
	now := s.now().UTC()
	state.Status = RunStatusCompleted
	state.TerminalOutcome = &TerminalOutcome{
		Condition: condition,
		Reason:    fmt.Sprintf("reached terminal condition %q", condition),
		At:        now,
	}
	state.CompletedAt = timePointer(now)
	state.UpdatedAt = now
	s.emitRun(event.EventMultiAgentRunCompleted, state, state.TerminalOutcome.Reason)
	s.log(state, "run completed", slog.String("terminal_condition", string(condition)))
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

// emitBudgetWarning emits EventMultiAgentBudgetWarning: a non-terminal,
// purely informational signal that a correction loop is one traversal away
// from exhausting its budget. It never affects pass/fail/routing decisions —
// callers decide when to fire it (see DurableRuntime.executePreparedRole).
func (s *Supervisor) emitBudgetWarning(
	state RunState,
	budget string,
	role Role,
	used int,
	limit int,
) {
	payload := map[string]any{
		"run_id":      state.RunID,
		"workflow_id": state.WorkflowID,
		"budget":      budget,
		"used":        used,
		"limit":       limit,
	}
	if role != "" {
		payload["role"] = string(role)
	}
	s.emit(event.EventMultiAgentBudgetWarning, state, payload)
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

// validateRoleRunResult checks a runner's result against graph's own outcome
// vocabulary. Before Phase 3 this called result.Outcome.Valid(), the
// Phase-1-hardcoded switch over exactly 8 outcomes — correct only for a
// Phase 1 (or CompatAdaptDefinition-derived) graph, and wrongly rejective for
// any other authored graph's own outcome vocabulary. graph.HasOutcome
// preserves the same looseness the old check had (accepts any outcome
// declared *anywhere* in the graph, not specifically for the current role;
// a role/outcome combination that is syntactically a real outcome but not
// valid for this particular role is still caught next, by graph.Resolve
// returning InvalidTransitionError).
func validateRoleRunResult(graph *CompiledGraph, result RoleRunResult) error {
	if !graph.HasOutcome(result.Outcome) {
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

// correctionLoop is a hardcoded pattern-match over the two Phase 1
// correction-loop edges. Supervisor/DurableRuntime no longer call it as of
// PR4 — they use CompiledGraph.LoopFor(transition) instead, a lookup
// precomputed at compile/adapt time rather than pattern-matched at runtime.
// This function is KEPT, unmodified, because graph_projection.go's
// BuildRunGraph (Phase 2's dashboard read model, which operates on the
// legacy Definition/RunState shape directly and is explicitly out of scope
// for Phase 3 changes — see the plan's "Phase 2's dashboard read model...
// zero changes" requirement) still calls it directly.
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
