package multiagent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/event"
)

type scriptedStep struct {
	result RoleRunResult
	err    error
}

type scriptedRunner struct {
	mu      sync.Mutex
	scripts map[Role][]scriptedStep
	calls   []Role
}

func (r *scriptedRunner) RunRole(_ context.Context, request RoleRunRequest) (RoleRunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	role := request.Run.CurrentRole
	r.calls = append(r.calls, role)
	steps := r.scripts[role]
	if len(steps) == 0 {
		return RoleRunResult{}, errors.New("unexpected role execution")
	}
	step := steps[0]
	r.scripts[role] = steps[1:]
	return step.result, step.err
}

type captureEventSink struct {
	mu     sync.Mutex
	events []event.Event
	onEmit func(event.Event)
}

func (s *captureEventSink) Emit(evt event.Event) {
	s.mu.Lock()
	s.events = append(s.events, evt)
	onEmit := s.onEmit
	s.mu.Unlock()
	if onEmit != nil {
		onEmit(evt)
	}
}

func (s *captureEventSink) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	types := make([]string, len(s.events))
	for i, evt := range s.events {
		types[i] = evt.Type
	}
	return types
}

func (s *captureEventSink) snapshot() []event.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event.Event(nil), s.events...)
}

func TestSupervisorReferenceFlows(t *testing.T) {
	tests := []struct {
		name          string
		outcomes      map[Role][]TransitionOutcome
		wantCalls     []Role
		wantVisits    map[Role]int
		wantTester    int
		wantReviewer  int
		wantTransfers int
	}{
		{
			name: "happy path",
			outcomes: map[Role][]TransitionOutcome{
				RolePlanner:   {OutcomePlanReady},
				RoleDeveloper: {OutcomeImplementationReady},
				RoleTester:    {OutcomeTestsPassed},
				RoleReviewer:  {OutcomeReviewApproved},
			},
			wantCalls:     []Role{RolePlanner, RoleDeveloper, RoleTester, RoleReviewer},
			wantVisits:    map[Role]int{RolePlanner: 1, RoleDeveloper: 1, RoleTester: 1, RoleReviewer: 1},
			wantTransfers: 4,
		},
		{
			name: "test failure loop",
			outcomes: map[Role][]TransitionOutcome{
				RolePlanner:   {OutcomePlanReady},
				RoleDeveloper: {OutcomeImplementationReady, OutcomeImplementationReady},
				RoleTester:    {OutcomeTestsFailed, OutcomeTestsPassed},
				RoleReviewer:  {OutcomeReviewApproved},
			},
			wantCalls: []Role{
				RolePlanner,
				RoleDeveloper,
				RoleTester,
				RoleDeveloper,
				RoleTester,
				RoleReviewer,
			},
			wantVisits:    map[Role]int{RolePlanner: 1, RoleDeveloper: 2, RoleTester: 2, RoleReviewer: 1},
			wantTester:    1,
			wantTransfers: 6,
		},
		{
			name: "review change loop",
			outcomes: map[Role][]TransitionOutcome{
				RolePlanner:   {OutcomePlanReady},
				RoleDeveloper: {OutcomeImplementationReady, OutcomeImplementationReady},
				RoleTester:    {OutcomeTestsPassed, OutcomeTestsPassed},
				RoleReviewer:  {OutcomeChangesRequested, OutcomeReviewApproved},
			},
			wantCalls: []Role{
				RolePlanner,
				RoleDeveloper,
				RoleTester,
				RoleReviewer,
				RoleDeveloper,
				RoleTester,
				RoleReviewer,
			},
			wantVisits:    map[Role]int{RolePlanner: 1, RoleDeveloper: 2, RoleTester: 2, RoleReviewer: 2},
			wantReviewer:  1,
			wantTransfers: 7,
		},
		{
			name: "combined loops",
			outcomes: map[Role][]TransitionOutcome{
				RolePlanner: {
					OutcomePlanReady,
				},
				RoleDeveloper: {
					OutcomeImplementationReady,
					OutcomeImplementationReady,
					OutcomeImplementationReady,
				},
				RoleTester: {
					OutcomeTestsFailed,
					OutcomeTestsPassed,
					OutcomeTestsPassed,
				},
				RoleReviewer: {
					OutcomeChangesRequested,
					OutcomeReviewApproved,
				},
			},
			wantCalls: []Role{
				RolePlanner,
				RoleDeveloper,
				RoleTester,
				RoleDeveloper,
				RoleTester,
				RoleReviewer,
				RoleDeveloper,
				RoleTester,
				RoleReviewer,
			},
			wantVisits:    map[Role]int{RolePlanner: 1, RoleDeveloper: 3, RoleTester: 3, RoleReviewer: 2},
			wantTester:    1,
			wantReviewer:  1,
			wantTransfers: 9,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := newScriptedRunner(test.outcomes)
			sink := &captureEventSink{}
			supervisor := newTestSupervisor(t, validDefinition(), runner, sink)

			state, err := supervisor.Run(context.Background(), testRunRequest())
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if state.Status != RunStatusCompleted {
				t.Fatalf("status = %q, want %q", state.Status, RunStatusCompleted)
			}
			if state.TransitionCount != test.wantTransfers {
				t.Errorf("transition count = %d, want %d", state.TransitionCount, test.wantTransfers)
			}
			if state.LoopTraversals.TesterToDeveloper != test.wantTester {
				t.Errorf(
					"tester loop count = %d, want %d",
					state.LoopTraversals.TesterToDeveloper,
					test.wantTester,
				)
			}
			if state.LoopTraversals.ReviewerToDeveloper != test.wantReviewer {
				t.Errorf(
					"reviewer loop count = %d, want %d",
					state.LoopTraversals.ReviewerToDeveloper,
					test.wantReviewer,
				)
			}
			for role, wantVisits := range test.wantVisits {
				if got := state.RoleStates[role].Visits; got != wantVisits {
					t.Errorf("%s visits = %d, want %d", role, got, wantVisits)
				}
			}
			if !reflect.DeepEqual(runner.calls, test.wantCalls) {
				t.Errorf("calls = %v, want %v", runner.calls, test.wantCalls)
			}
			if state.LatestHandoff == nil {
				t.Fatal("latest handoff is nil")
			}
			if state.LatestHandoff.SourceRole != RoleTester ||
				state.LatestHandoff.DestinationRole != RoleReviewer {
				t.Errorf("latest handoff route = %s -> %s", state.LatestHandoff.SourceRole, state.LatestHandoff.DestinationRole)
			}
			if err := state.Validate(validDefinition()); err != nil {
				t.Fatalf("final state invalid: %v", err)
			}
			for _, evt := range sink.snapshot() {
				if err := event.Validate(evt); err != nil {
					t.Fatalf("invalid emitted event %q: %v", evt.Type, err)
				}
			}
		})
	}
}

func TestSupervisorBudgetExhaustionStopsBeforeAnotherLoopTraversal(t *testing.T) {
	definition := validDefinition()
	definition.Budgets.MaxTesterToDeveloperLoops = 1
	runner := newScriptedRunner(map[Role][]TransitionOutcome{
		RolePlanner:   {OutcomePlanReady},
		RoleDeveloper: {OutcomeImplementationReady, OutcomeImplementationReady},
		RoleTester:    {OutcomeTestsFailed, OutcomeTestsFailed},
	})
	sink := &captureEventSink{}
	supervisor := newTestSupervisor(t, definition, runner, sink)

	state, err := supervisor.Run(context.Background(), testRunRequest())
	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected BudgetExceededError, got %T: %v", err, err)
	}
	if budgetErr.Budget != "max_tester_to_developer_loops" {
		t.Errorf("budget = %q", budgetErr.Budget)
	}
	if state.Status != RunStatusBudgetExhausted {
		t.Errorf("status = %q", state.Status)
	}
	if state.LoopTraversals.TesterToDeveloper != 1 {
		t.Errorf("recorded loop traversals = %d, want 1", state.LoopTraversals.TesterToDeveloper)
	}
	if state.RoleStates[RoleDeveloper].Visits != 2 {
		t.Errorf("developer visits = %d, want 2", state.RoleStates[RoleDeveloper].Visits)
	}
	if got := sink.types()[len(sink.types())-1]; got != event.EventMultiAgentBudgetExhausted {
		t.Errorf("last event = %q, want %q", got, event.EventMultiAgentBudgetExhausted)
	}
	if err := state.Validate(definition); err != nil {
		t.Fatalf("budget state invalid: %v", err)
	}
}

func TestSupervisorInvalidOutcomeFailsDeterministically(t *testing.T) {
	runner := newScriptedRunner(map[Role][]TransitionOutcome{
		RolePlanner: {OutcomeTestsPassed},
	})
	sink := &captureEventSink{}
	supervisor := newTestSupervisor(t, validDefinition(), runner, sink)

	state, err := supervisor.Run(context.Background(), testRunRequest())
	var transitionErr *InvalidTransitionError
	if !errors.As(err, &transitionErr) {
		t.Fatalf("expected InvalidTransitionError, got %T: %v", err, err)
	}
	if state.Status != RunStatusFailed {
		t.Errorf("status = %q, want %q", state.Status, RunStatusFailed)
	}
	if got := sink.types()[len(sink.types())-1]; got != event.EventMultiAgentRunFailed {
		t.Errorf("last event = %q, want %q", got, event.EventMultiAgentRunFailed)
	}
}

func TestSupervisorRunnerFailure(t *testing.T) {
	boom := errors.New("fake runner failed")
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner: {{err: boom}},
	}}
	sink := &captureEventSink{}
	supervisor := newTestSupervisor(t, validDefinition(), runner, sink)

	state, err := supervisor.Run(context.Background(), testRunRequest())
	var roleErr *RoleExecutionError
	if !errors.As(err, &roleErr) {
		t.Fatalf("expected RoleExecutionError, got %T: %v", err, err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error does not wrap runner failure: %v", err)
	}
	if state.Status != RunStatusFailed {
		t.Errorf("status = %q", state.Status)
	}
	if state.RoleStates[RolePlanner].Status != RoleStatusFailed {
		t.Errorf("planner status = %q", state.RoleStates[RolePlanner].Status)
	}
}

func TestSupervisorCancellationBetweenRoles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := newScriptedRunner(map[Role][]TransitionOutcome{
		RolePlanner: {OutcomePlanReady},
	})
	sink := &captureEventSink{}
	sink.onEmit = func(evt event.Event) {
		if evt.Type == event.EventMultiAgentTransitionSelected {
			cancel()
		}
	}
	supervisor := newTestSupervisor(t, validDefinition(), runner, sink)

	state, err := supervisor.Run(ctx, testRunRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if state.Status != RunStatusCancelled {
		t.Errorf("status = %q", state.Status)
	}
	if !reflect.DeepEqual(runner.calls, []Role{RolePlanner}) {
		t.Errorf("runner calls = %v, want planner only", runner.calls)
	}
	if got := sink.types()[len(sink.types())-1]; got != event.EventMultiAgentRunCancelled {
		t.Errorf("last event = %q, want %q", got, event.EventMultiAgentRunCancelled)
	}
}

func TestSupervisorHappyPathEventOrdering(t *testing.T) {
	runner := newScriptedRunner(map[Role][]TransitionOutcome{
		RolePlanner:   {OutcomePlanReady},
		RoleDeveloper: {OutcomeImplementationReady},
		RoleTester:    {OutcomeTestsPassed},
		RoleReviewer:  {OutcomeReviewApproved},
	})
	sink := &captureEventSink{}
	supervisor := newTestSupervisor(t, validDefinition(), runner, sink)

	if _, err := supervisor.Run(context.Background(), testRunRequest()); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := []string{
		event.EventMultiAgentRunStarted,
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentHandoffCreated,
		event.EventMultiAgentTransitionSelected,
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentHandoffCreated,
		event.EventMultiAgentTransitionSelected,
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentHandoffCreated,
		event.EventMultiAgentTransitionSelected,
		event.EventMultiAgentRoleEntered,
		event.EventMultiAgentRoleCompleted,
		event.EventMultiAgentTransitionSelected,
		event.EventMultiAgentRunCompleted,
	}
	if got := sink.types(); !reflect.DeepEqual(got, want) {
		t.Fatalf("event order:\n got: %v\nwant: %v", got, want)
	}
}

func TestSupervisorStateAccounting(t *testing.T) {
	runner := newScriptedRunner(map[Role][]TransitionOutcome{
		RolePlanner:   {OutcomePlanReady},
		RoleDeveloper: {OutcomeImplementationReady, OutcomeImplementationReady},
		RoleTester:    {OutcomeTestsFailed, OutcomeTestsPassed},
		RoleReviewer:  {OutcomeReviewApproved},
	})
	sink := &captureEventSink{}
	supervisor := newTestSupervisor(t, validDefinition(), runner, sink)

	state, err := supervisor.Run(context.Background(), testRunRequest())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if state.BudgetUsage.TotalLocalIterations != 6 {
		t.Errorf("local iterations = %d, want 6", state.BudgetUsage.TotalLocalIterations)
	}
	if state.BudgetUsage.Tokens.TotalTokens != 60 {
		t.Errorf("tokens = %d, want 60", state.BudgetUsage.Tokens.TotalTokens)
	}
	if state.BudgetUsage.RepeatedFailures != 1 {
		t.Errorf("repeated failures = %d, want 1", state.BudgetUsage.RepeatedFailures)
	}
	if state.RoleStates[RoleDeveloper].TokenUsage.TotalTokens != 20 {
		t.Errorf("developer tokens = %d, want 20", state.RoleStates[RoleDeveloper].TokenUsage.TotalTokens)
	}
	if state.TerminalOutcome == nil ||
		state.TerminalOutcome.Condition != TerminalConditionCompleted {
		t.Fatalf("terminal outcome = %#v", state.TerminalOutcome)
	}
}

func newScriptedRunner(outcomes map[Role][]TransitionOutcome) *scriptedRunner {
	scripts := make(map[Role][]scriptedStep, len(outcomes))
	for role, roleOutcomes := range outcomes {
		for _, outcome := range roleOutcomes {
			scripts[role] = append(scripts[role], scriptedStep{result: scriptedResult(outcome)})
		}
	}
	return &scriptedRunner{scripts: scripts}
}

func scriptedResult(outcome TransitionOutcome) RoleRunResult {
	result := RoleRunResult{
		Outcome: outcome,
		TokenUsage: cost.TokenUsage{
			PromptTokens:     6,
			CompletionTokens: 4,
			TotalTokens:      10,
		},
		LocalIterations: 1,
		Metadata: ExecutionMetadata{
			AgentRef: "fake-agent",
			Attempt:  1,
		},
	}
	switch outcome {
	case OutcomeReviewApproved, OutcomeTerminalFailure, OutcomeCancelled:
	default:
		result.OutgoingHandoff = &HandoffDraft{
			Objective: "continue the reference flow",
			Reason:    "fake role completed",
			Artifacts: []ArtifactRef{{Kind: ArtifactReport, URI: "memory://fake-report"}},
		}
	}
	return result
}

func newTestSupervisor(
	t *testing.T,
	definition Definition,
	runner RoleRunner,
	sink EventSink,
) *Supervisor {
	t.Helper()
	var handoffSequence int
	supervisor, err := NewSupervisor(definition, runner, sink, SupervisorOptions{
		Clock: func() time.Time {
			return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
		},
		NewHandoffID: func() string {
			handoffSequence++
			return "handoff-test-" + string(rune('0'+handoffSequence))
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new supervisor: %v", err)
	}
	return supervisor
}

func testRunRequest() RunRequest {
	return RunRequest{
		RunID: "run-phase1-test",
		Task: TaskReference{
			ID:          "task-phase1-test",
			Description: "exercise the bounded reference flow",
		},
	}
}
