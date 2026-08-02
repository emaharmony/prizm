package multiagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prism/internal/event"
)

func TestDurableRuntimeNormalRoundTripAndTerminalResume(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := happyDurableRunner()
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-durable-happy"

	state, err := runtime.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if state.Status != RunStatusCompleted ||
		state.TransitionCount != 4 ||
		state.LatestCompletedRole != RoleReviewer {
		t.Fatalf("terminal state = %#v", state)
	}
	if state.BudgetUsage.Tokens.TotalTokens != 40 {
		t.Fatalf("tokens = %d, want 40", state.BudgetUsage.Tokens.TotalTokens)
	}
	if state.WorkspaceID != "workspace-durable" {
		t.Fatalf("workspace id = %q, want workspace-durable", state.WorkspaceID)
	}

	stored, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stored.Phase != CheckpointTerminal ||
		stored.Revision != 13 ||
		stored.LastCompletedExecutionKey != "run-durable-happy:reviewer:1" ||
		stored.State.WorkspaceID != "workspace-durable" {
		t.Fatalf("stored envelope = %#v", stored)
	}
	for _, role := range Phase1Roles() {
		roleState := stored.State.RoleStates[role]
		if roleState.Visits != 1 || roleState.LastExecutionKey == "" {
			t.Errorf("%s state = %#v", role, roleState)
		}
	}
	pending, err := env.store.PendingEvents(
		context.Background(), request.RunID, 100)
	if err != nil {
		t.Fatalf("pending events: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("outbox still has %d events", len(pending))
	}

	callsBefore := runnerCalls(runner)
	for i := 0; i < 2; i++ {
		resumed, resumeErr := runtime.Resume(context.Background(), request.RunID)
		if resumeErr != nil {
			t.Fatalf("terminal resume %d: %v", i+1, resumeErr)
		}
		if resumed.Status != RunStatusCompleted {
			t.Fatalf("terminal resume status = %q", resumed.Status)
		}
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, callsBefore) {
		t.Fatalf("terminal resume executed roles: before %v after %v", callsBefore, got)
	}
	recoveryEvents := queryEventsForTest(
		t, env.events, request.RunID, event.EventMultiAgentRecoveryCompleted)
	if len(recoveryEvents) != 1 {
		t.Fatalf("idempotent terminal recovery events = %d, want 1", len(recoveryEvents))
	}
}

func TestDurableRuntimeRecoversCompletedRoleWithoutReexecution(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := happyDurableRunner()
	failing := &checkpointFailureStore{
		DurableRunStore: env.store,
		failCheckpoint:  3,
	}
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), runner, failing, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-partial-transition"

	_, err := runtime.Run(context.Background(), request)
	if !errors.Is(err, errInjectedCheckpoint) {
		t.Fatalf("expected injected checkpoint failure, got %v", err)
	}
	stored, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("load interrupted run: %v", err)
	}
	if stored.Phase != CheckpointTransitionPending ||
		stored.LastCompletedExecutionKey != "run-partial-transition:planner:1" {
		t.Fatalf("interrupted checkpoint = %#v", stored)
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, []Role{RolePlanner}) {
		t.Fatalf("calls before resume = %v", got)
	}

	resumer := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events)
	state, err := resumer.Resume(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if state.Status != RunStatusCompleted || state.TransitionCount != 4 {
		t.Fatalf("resumed state = %#v", state)
	}
	wantCalls := []Role{RolePlanner, RoleDeveloper, RoleTester, RoleReviewer}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("role was re-executed: got %v want %v", got, wantCalls)
	}
	if state.BudgetUsage.Tokens.TotalTokens != 40 {
		t.Fatalf("usage double-counted: %d", state.BudgetUsage.Tokens.TotalTokens)
	}
}

func TestDurableRuntimeRecoversBetweenRolesDuringCorrectionLoop(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner: {{result: scriptedResult(OutcomePlanReady)}},
		RoleDeveloper: {
			{result: scriptedResult(OutcomeImplementationReady)},
			{result: scriptedResult(OutcomeImplementationReady)},
		},
		RoleTester: {
			{result: scriptedResult(OutcomeTestsFailed)},
			{result: scriptedResult(OutcomeTestsPassed)},
		},
		RoleReviewer: {{result: scriptedResult(OutcomeReviewApproved)}},
	}}
	failing := &checkpointFailureStore{
		DurableRunStore: env.store,
		failCheckpoint:  10,
	}
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), runner, failing, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-loop-restart"

	_, err := runtime.Run(context.Background(), request)
	if !errors.Is(err, errInjectedCheckpoint) {
		t.Fatalf("expected injected checkpoint failure, got %v", err)
	}
	stored, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stored.Phase != CheckpointPendingRole ||
		stored.State.CurrentRole != RoleDeveloper ||
		stored.State.LoopTraversals.Get(LoopTesterToDeveloper) != 1 {
		t.Fatalf("loop checkpoint = %#v", stored)
	}

	resumer := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events)
	state, err := resumer.Resume(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("resume loop: %v", err)
	}
	if state.RoleStates[RoleDeveloper].Visits != 2 ||
		state.RoleStates[RoleTester].Visits != 2 ||
		state.LoopTraversals.Get(LoopTesterToDeveloper) != 1 ||
		state.TransitionCount != 6 {
		t.Fatalf("resumed loop counters = %#v", state)
	}
}

func TestDurableRuntimeBudgetExhaustionSurvivesRestart(t *testing.T) {
	env := newDurableTestEnvironment(t)
	definition := validDefinition()
	for i := range definition.Roles {
		if definition.Roles[i].Role == RolePlanner {
			definition.Roles[i].MaxLocalIterations = 1
		}
	}
	result := scriptedResult(OutcomePlanReady)
	result.LocalIterations = 2
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner: {{result: result}},
	}}
	runtime := newDurableRuntimeForTest(
		t, definition, runner, env.store, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-budget-restart"

	state, err := runtime.Run(context.Background(), request)
	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected budget exhaustion, got %v", err)
	}
	if state.Status != RunStatusBudgetExhausted ||
		state.BudgetUsage.TotalLocalIterations != 2 {
		t.Fatalf("budget state = %#v", state)
	}
	callsBefore := runnerCalls(runner)
	resumed, resumeErr := runtime.Resume(context.Background(), request.RunID)
	if resumeErr != nil {
		t.Fatalf("budget-exhausted resume: %v", resumeErr)
	}
	if resumed.Status != RunStatusBudgetExhausted ||
		resumed.BudgetUsage.TotalLocalIterations != 2 {
		t.Fatalf("resumed budget state = %#v", resumed)
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, callsBefore) {
		t.Fatalf("budget restart executed a role: %v", got)
	}
}

func TestDurableRuntimeApprovalWaitingResumesSameVisit(t *testing.T) {
	env := newDurableTestEnvironment(t)
	approvalErr := &ApprovalRequiredError{
		Check: ApprovalCheck{
			RunID: "run-approval-wait",
			Role:  RolePlanner,
			Visit: 1,
		},
		Status: ApprovalPending,
	}
	runner := happyDurableRunner()
	runner.scripts[RolePlanner] = []scriptedStep{
		{err: approvalErr},
		{result: scriptedResult(OutcomePlanReady)},
	}
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-approval-wait"

	state, err := runtime.Run(context.Background(), request)
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) || waitingErr.Kind != "approval" {
		t.Fatalf("expected approval waiting, got %v", err)
	}
	if state.Status != RunStatusPaused ||
		state.RoleStates[RolePlanner].Status != RoleStatusWaiting ||
		state.RoleStates[RolePlanner].ApprovalStatus != string(ApprovalPending) {
		t.Fatalf("waiting state = %#v", state)
	}

	state, err = runtime.Resume(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("resume after approval: %v", err)
	}
	if state.Status != RunStatusCompleted ||
		state.RoleStates[RolePlanner].Visits != 1 {
		t.Fatalf("approval resume state = %#v", state)
	}
	wantCalls := []Role{
		RolePlanner,
		RolePlanner,
		RoleDeveloper,
		RoleTester,
		RoleReviewer,
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("approval resume calls = %v, want %v", got, wantCalls)
	}
}

func TestDurableRuntimeInterruptedExecutionRequiresReconciliation(t *testing.T) {
	env := newDurableTestEnvironment(t)
	panicRunner := &panickingRoleRunner{}
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), panicRunner, env.store, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-uncertain-execution"

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("expected simulated process crash")
			}
		}()
		_, _ = runtime.Run(context.Background(), request)
	}()
	stored, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("load crashed run: %v", err)
	}
	if stored.Phase != CheckpointRoleRunning {
		t.Fatalf("crash checkpoint = %q", stored.Phase)
	}

	safeRunner := &countingRoleRunner{}
	resumer := newDurableRuntimeForTest(
		t, validDefinition(), safeRunner, env.store, env.claimer, env.events)
	state, err := resumer.Resume(context.Background(), request.RunID)
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) ||
		waitingErr.Kind != "execution_reconciliation" {
		t.Fatalf("expected reconciliation wait, got %v", err)
	}
	if state.Status != RunStatusPaused ||
		state.RoleStates[RolePlanner].Status != RoleStatusWaiting ||
		safeRunner.calls != 0 {
		t.Fatalf("unsafe recovery state=%#v calls=%d", state, safeRunner.calls)
	}
	record, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("load waiting record: %v", err)
	}
	if record.Waiting == nil || record.Waiting.SafeToRetry {
		t.Fatalf("uncertain execution was marked retryable: %#v", record.Waiting)
	}
}

func TestDurableRuntimeDuplicateResumeCannotAcquireClaim(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := happyDurableRunner()
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-duplicate-resume"
	if _, err := runtime.Run(context.Background(), request); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	claim, err := env.claimer.Acquire(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("hold claim: %v", err)
	}
	defer claim.Release()
	_, err = runtime.Resume(context.Background(), request.RunID)
	if !errors.Is(err, ErrRunClaimed) {
		t.Fatalf("expected duplicate claim rejection, got %v", err)
	}
}

func TestDurableRuntimeReemitsOutboxIdempotently(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := happyDurableRunner()
	publisher := &publishThenFailOnce{publisher: env.events}
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, publisher)
	request := testRunRequest()
	request.RunID = "run-outbox-reemit"

	_, err := runtime.Run(context.Background(), request)
	if !errors.Is(err, errInjectedPublish) {
		t.Fatalf("expected publisher interruption, got %v", err)
	}
	pending, err := env.store.PendingEvents(
		context.Background(), request.RunID, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending outbox after interruption = %#v err=%v", pending, err)
	}

	resumer := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events)
	state, err := resumer.Resume(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("resume after publisher interruption: %v", err)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("resumed state = %q", state.Status)
	}
	started := queryEventsForTest(
		t, env.events, request.RunID, event.EventMultiAgentRunStarted)
	if len(started) != 1 {
		t.Fatalf("run.started was duplicated: %d", len(started))
	}
}

func TestDurableRuntimeRejectsCorruptAndFutureState(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, *SQLiteDurableRunStore, DurableRun)
	}{
		{
			name: "future envelope version",
			corrupt: func(t *testing.T, store *SQLiteDurableRunStore, record DurableRun) {
				t.Helper()
				record.SchemaVersion = DurableRunSchemaVersion + 1
				record.State.UpdatedAt = record.State.UpdatedAt.Add(time.Second)
				if _, err := store.Checkpoint(
					context.Background(), record.Revision, record, nil); err != nil {
					t.Fatalf("persist future state: %v", err)
				}
			},
		},
		{
			name: "corrupt json",
			corrupt: func(t *testing.T, store *SQLiteDurableRunStore, record DurableRun) {
				t.Helper()
				if _, err := store.db.Exec(
					`UPDATE multiagent_runs SET record_json = '{' WHERE run_id = ?`,
					record.State.RunID,
				); err != nil {
					t.Fatalf("corrupt state: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newDurableTestEnvironment(t)
			record := durableRecordForTest(t, "run-corrupt-"+test.name)
			created, err := env.store.Create(context.Background(), record, nil)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			test.corrupt(t, env.store, created)
			runner := &countingRoleRunner{}
			runtime := newDurableRuntimeForTest(
				t, validDefinition(), runner, env.store, env.claimer, env.events)

			_, err = runtime.Resume(context.Background(), record.State.RunID)
			var recoveryErr *RecoveryFailedError
			if !errors.As(err, &recoveryErr) {
				t.Fatalf("expected RecoveryFailedError, got %v", err)
			}
			if runner.calls != 0 {
				t.Fatalf("corrupt state executed %d roles", runner.calls)
			}
			failures := queryEventsForTest(
				t,
				env.events,
				record.State.RunID,
				event.EventMultiAgentRecoveryFailed,
			)
			if len(failures) != 1 {
				t.Fatalf("recovery failure events = %d, want 1", len(failures))
			}
		})
	}
}

func TestDurableRuntimeTerminalFailuresNeverExecuteAgain(t *testing.T) {
	tests := []struct {
		name       string
		outcome    TransitionOutcome
		wantStatus RunStatus
	}{
		{
			name:       "failed",
			outcome:    OutcomeTerminalFailure,
			wantStatus: RunStatusFailed,
		},
		{
			name:       "cancelled",
			outcome:    OutcomeCancelled,
			wantStatus: RunStatusCancelled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newDurableTestEnvironment(t)
			runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
				RolePlanner: {{result: scriptedResult(test.outcome)}},
			}}
			runtime := newDurableRuntimeForTest(
				t, validDefinition(), runner, env.store, env.claimer, env.events)
			request := testRunRequest()
			request.RunID = "run-terminal-" + test.name

			state, err := runtime.Run(context.Background(), request)
			if err == nil || state.Status != test.wantStatus {
				t.Fatalf("terminal run state=%q err=%v", state.Status, err)
			}
			callsBefore := runnerCalls(runner)
			resumed, resumeErr := runtime.Resume(context.Background(), request.RunID)
			if resumeErr != nil || resumed.Status != test.wantStatus {
				t.Fatalf("terminal resume state=%q err=%v", resumed.Status, resumeErr)
			}
			if got := runnerCalls(runner); !reflect.DeepEqual(got, callsBefore) {
				t.Fatalf("terminal resume executed work: %v", got)
			}
		})
	}
}

type durableTestEnvironment struct {
	store   *SQLiteDurableRunStore
	events  *event.SQLiteEventStore
	claimer *memoryRunClaimer
}

func newDurableTestEnvironment(t *testing.T) durableTestEnvironment {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "prism-runtime.db")
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	store, err := NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		_ = events.Close()
		t.Fatalf("new durable store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close durable store: %v", err)
		}
		if err := events.Close(); err != nil {
			t.Errorf("close event store: %v", err)
		}
	})
	return durableTestEnvironment{
		store:   store,
		events:  events,
		claimer: newMemoryRunClaimer(),
	}
}

func newDurableRuntimeForTest(
	t *testing.T,
	definition Definition,
	runner RoleRunner,
	store DurableRunStore,
	claimer RunClaimer,
	publisher EventPublisher,
) *DurableRuntime {
	t.Helper()
	graph := mustAdaptGraph(t, definition)
	runtime, err := NewDurableRuntime(
		graph,
		runner,
		store,
		claimer,
		publisher,
		DurableRuntimeOptions{
			Clock: func() time.Time {
				return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
			},
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	if err != nil {
		t.Fatalf("new durable runtime: %v", err)
	}
	return runtime
}

func happyDurableRunner() *scriptedRunner {
	planner := scriptedResult(OutcomePlanReady)
	developer := scriptedResult(OutcomeImplementationReady)
	tester := scriptedResult(OutcomeTestsPassed)
	reviewer := scriptedResult(OutcomeReviewApproved)
	for _, result := range []*RoleRunResult{&planner, &developer, &tester, &reviewer} {
		result.Metadata.WorkspaceID = "workspace-durable"
	}
	return &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner:   {{result: planner}},
		RoleDeveloper: {{result: developer}},
		RoleTester:    {{result: tester}},
		RoleReviewer:  {{result: reviewer}},
	}}
}

func runnerCalls(runner *scriptedRunner) []Role {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]Role(nil), runner.calls...)
}

func queryEventsForTest(
	t *testing.T,
	store event.EventStore,
	runID string,
	eventType string,
) []event.Event {
	t.Helper()
	events, err := store.Query(context.Background(), event.EventFilter{
		RunID: runID,
		Type:  eventType,
		Limit: 500,
	})
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	return events
}

var errInjectedCheckpoint = errors.New("injected checkpoint interruption")

type checkpointFailureStore struct {
	DurableRunStore
	mu             sync.Mutex
	checkpoints    int
	failCheckpoint int
	failed         bool
}

func (s *checkpointFailureStore) Checkpoint(
	ctx context.Context,
	expectedRevision int64,
	record DurableRun,
	events []event.Event,
) (DurableRun, error) {
	s.mu.Lock()
	s.checkpoints++
	shouldFail := !s.failed && s.checkpoints == s.failCheckpoint
	if shouldFail {
		s.failed = true
	}
	s.mu.Unlock()
	if shouldFail {
		return DurableRun{}, errInjectedCheckpoint
	}
	return s.DurableRunStore.Checkpoint(ctx, expectedRevision, record, events)
}

type memoryRunClaimer struct {
	mu   sync.Mutex
	held map[string]bool
}

func newMemoryRunClaimer() *memoryRunClaimer {
	return &memoryRunClaimer{held: map[string]bool{}}
}

func (c *memoryRunClaimer) Acquire(
	_ context.Context,
	runID string,
) (ExecutionClaim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held[runID] {
		return nil, fmt.Errorf("%w: %s", ErrRunClaimed, runID)
	}
	c.held[runID] = true
	return &memoryExecutionClaim{claimer: c, runID: runID}, nil
}

type memoryExecutionClaim struct {
	claimer *memoryRunClaimer
	runID   string
	once    sync.Once
}

func (c *memoryExecutionClaim) Release() error {
	c.once.Do(func() {
		c.claimer.mu.Lock()
		delete(c.claimer.held, c.runID)
		c.claimer.mu.Unlock()
	})
	return nil
}

type panickingRoleRunner struct{}

func (*panickingRoleRunner) RunRole(
	context.Context,
	RoleRunRequest,
) (RoleRunResult, error) {
	panic("simulated process crash")
}

type countingRoleRunner struct {
	calls int
}

func (r *countingRoleRunner) RunRole(
	context.Context,
	RoleRunRequest,
) (RoleRunResult, error) {
	r.calls++
	return RoleRunResult{}, errors.New("unexpected execution")
}

var errInjectedPublish = errors.New("injected publisher interruption")

type publishThenFailOnce struct {
	publisher EventPublisher
	mu        sync.Mutex
	failed    bool
}

func (p *publishThenFailOnce) Store(
	ctx context.Context,
	evt event.Event,
) error {
	if err := p.publisher.Store(ctx, evt); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.failed {
		p.failed = true
		return errInjectedPublish
	}
	return nil
}
