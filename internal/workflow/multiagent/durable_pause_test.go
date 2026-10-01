package multiagent

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// --- SQLiteDurableRunStore pause request persistence ---

func TestSQLiteDurableRunStorePauseRequest(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	if err := store.RequestPause(ctx, "missing", "stop"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("missing pause request error = %v", err)
	}
	record := durableRecordForTest(t, "run-pause-request")
	if _, err := store.Create(ctx, record, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.RequestPause(ctx, record.State.RunID, "first reason"); err != nil {
		t.Fatalf("request pause: %v", err)
	}
	reason, requested, err := store.PauseRequest(ctx, record.State.RunID)
	if err != nil || !requested || reason != "first reason" {
		t.Fatalf("request = %q/%t, error %v", reason, requested, err)
	}
	if err := store.RequestPause(ctx, record.State.RunID, "operator override"); err != nil {
		t.Fatalf("update pause: %v", err)
	}
	reason, requested, err = store.PauseRequest(ctx, record.State.RunID)
	if err != nil || !requested || reason != "operator override" {
		t.Fatalf("updated request = %q/%t, error %v", reason, requested, err)
	}
	if err := store.ClearPauseRequest(ctx, record.State.RunID); err != nil {
		t.Fatalf("clear pause: %v", err)
	}
	_, requested, err = store.PauseRequest(ctx, record.State.RunID)
	if err != nil || requested {
		t.Fatalf("expected cleared pause request, got requested=%t err=%v", requested, err)
	}
	// Clearing an absent request must be a no-op, not an error.
	if err := store.ClearPauseRequest(ctx, "never-existed"); err != nil {
		t.Fatalf("clear absent pause: %v", err)
	}
}

// --- DurableRuntime.Pause / operator_pause wait lifecycle ---

// TestDurableRuntimeIdlePauseObservedAtNextRoleBoundary covers an idle run
// (nobody currently driving it): Pause persists the request without itself
// transitioning any state, and the request is only observed the next time
// something calls Resume, at the very first role boundary.
func TestDurableRuntimeIdlePauseObservedAtNextRoleBoundary(t *testing.T) {
	env := newDurableTestEnvironment(t)
	record := durableRecordForTest(t, "run-idle-pause")
	if _, err := env.store.Create(context.Background(), record, nil); err != nil {
		t.Fatalf("seed idle run: %v", err)
	}

	pauseRuntime := newDurableRuntimeForTest(
		t, validDefinition(), &countingRoleRunner{}, env.store, env.claimer, env.events)
	state, err := pauseRuntime.Pause(context.Background(), record.State.RunID, "operator break")
	if err != nil {
		t.Fatalf("pause idle run: %v", err)
	}
	if state.Status != RunStatusRunning {
		t.Fatalf("Pause must not itself transition state, got status %q", state.Status)
	}

	runner := &countingRoleRunner{}
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events)
	resumed, err := runtime.Resume(context.Background(), record.State.RunID)
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) || waitingErr.Kind != "operator_pause" {
		t.Fatalf("expected operator_pause waiting, got %v", err)
	}
	if resumed.Status != RunStatusPaused {
		t.Fatalf("resumed status = %q, want %q", resumed.Status, RunStatusPaused)
	}
	if runner.calls != 0 {
		t.Fatalf("role was executed while paused: calls=%d", runner.calls)
	}

	stored, err := env.store.Load(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("load paused run: %v", err)
	}
	if stored.Phase != CheckpointWaiting ||
		stored.Waiting == nil ||
		stored.Waiting.Kind != "operator_pause" ||
		stored.Waiting.Reason != "operator break" ||
		!stored.Waiting.SafeToRetry ||
		stored.State.Status != RunStatusPaused {
		t.Fatalf("persisted pause = %#v", stored)
	}
}

// TestDurableRuntimeActivePauseStopsAtRoleBoundary covers an actively
// executing run: Pause takes effect only once the in-flight role finishes and
// the run reaches its next role boundary, never interrupting the role
// currently running. Mirrors
// TestDurableRuntimeActiveCancellationStopsAtRoleBoundary's structure.
func TestDurableRuntimeActivePauseStopsAtRoleBoundary(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := &blockingPlannerRunner{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events,
	)
	request := testRunRequest()
	request.RunID = "run-active-pause"

	type runResult struct {
		state RunState
		err   error
	}
	result := make(chan runResult, 1)
	go func() {
		state, err := runtime.Run(context.Background(), request)
		result <- runResult{state: state, err: err}
	}()
	<-runner.started

	state, err := runtime.Pause(context.Background(), request.RunID, "operator break")
	if err != nil {
		t.Fatalf("request active pause: %v", err)
	}
	if state.Status != RunStatusRunning {
		t.Fatalf("active pause returned status %q, want running (mid-role unaffected)", state.Status)
	}
	close(runner.release)
	completed := <-result
	var waitingErr *RunWaitingError
	if !errors.As(completed.err, &waitingErr) || waitingErr.Kind != "operator_pause" {
		t.Fatalf("expected operator_pause waiting at next boundary, got %v", completed.err)
	}
	if completed.state.Status != RunStatusPaused {
		t.Fatalf("paused state = %#v", completed.state)
	}
	if calls := runner.callCount(); calls != 1 {
		t.Fatalf("role calls = %d, want planner only (developer must not start)", calls)
	}
	record, err := runtime.Inspect(context.Background(), request.RunID)
	if err != nil || record.Phase != CheckpointWaiting ||
		record.Waiting == nil || record.Waiting.Kind != "operator_pause" ||
		record.State.Status != RunStatusPaused {
		t.Fatalf("persisted pause = %#v, error %v", record, err)
	}
}

// TestDurableRuntimeTerminalRunCannotBePaused covers pausing a finished run:
// it must fail cleanly with ErrRunAlreadyTerminal and must not persist a
// pause request that could confuse a later, unrelated run reusing the id.
func TestDurableRuntimeTerminalRunCannotBePaused(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := happyDurableRunner()
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-terminal-pause"

	state, err := runtime.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("seed terminal run: %v", err)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("seed run status = %q, want completed", state.Status)
	}

	_, err = runtime.Pause(context.Background(), request.RunID, "too late")
	if !errors.Is(err, ErrRunAlreadyTerminal) {
		t.Fatalf("expected ErrRunAlreadyTerminal, got %v", err)
	}

	_, requested, err := env.store.PauseRequest(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("pause request lookup: %v", err)
	}
	if requested {
		t.Fatal("terminal pause must not persist a request")
	}
}

// TestDurableRuntimeResumeClearsPauseAndDoesNotImmediatelyRepause covers the
// full clear-on-resume contract: after Resume clears an operator_pause wait,
// the run must run to completion without re-observing the (now-cleared) flag
// at any later role boundary.
func TestDurableRuntimeResumeClearsPauseAndDoesNotImmediatelyRepause(t *testing.T) {
	env := newDurableTestEnvironment(t)
	record := durableRecordForTest(t, "run-pause-clear")
	if _, err := env.store.Create(context.Background(), record, nil); err != nil {
		t.Fatalf("seed idle run: %v", err)
	}

	pauseRuntime := newDurableRuntimeForTest(
		t, validDefinition(), &countingRoleRunner{}, env.store, env.claimer, env.events)
	if _, err := pauseRuntime.Pause(context.Background(), record.State.RunID, "before start"); err != nil {
		t.Fatalf("pause idle run: %v", err)
	}

	waitRunner := &countingRoleRunner{}
	waitRuntime := newDurableRuntimeForTest(
		t, validDefinition(), waitRunner, env.store, env.claimer, env.events)
	_, err := waitRuntime.Resume(context.Background(), record.State.RunID)
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) || waitingErr.Kind != "operator_pause" {
		t.Fatalf("expected operator_pause waiting, got %v", err)
	}
	if waitRunner.calls != 0 {
		t.Fatalf("role executed while paused: calls=%d", waitRunner.calls)
	}

	runner := happyDurableRunner()
	resumer := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events)
	state, err := resumer.Resume(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("resume after pause clear: %v", err)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("resumed state = %#v", state)
	}
	wantCalls := []Role{RolePlanner, RoleDeveloper, RoleTester, RoleReviewer}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("post-pause-clear calls = %v, want %v (run must not re-pause)", got, wantCalls)
	}

	_, requested, err := env.store.PauseRequest(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("pause request lookup: %v", err)
	}
	if requested {
		t.Fatal("pause request was not cleared on resume")
	}
}

// TestDurableRuntimeRepeatedPauseResumeCycles drives two independent
// pause/resume cycles across a single run's lifecycle: one at an idle role
// boundary before the run has done any work, and a second layered on top of
// an unrelated approval wait, to confirm operator_pause composes correctly
// with a different wait kind rather than clobbering or being clobbered by it.
func TestDurableRuntimeRepeatedPauseResumeCycles(t *testing.T) {
	env := newDurableTestEnvironment(t)
	approvalErr := &ApprovalRequiredError{
		Check: ApprovalCheck{
			RunID: "run-repeated-pause",
			Role:  RoleDeveloper,
			Visit: 1,
		},
		Status: ApprovalPending,
	}
	runner := happyDurableRunner()
	runner.scripts[RoleDeveloper] = []scriptedStep{
		{err: approvalErr},
		{result: scriptedResult(OutcomeImplementationReady)},
	}
	runner.scripts[RoleDeveloper][1].result.Metadata.WorkspaceID = "workspace-durable"

	record := durableRecordForTest(t, "run-repeated-pause")
	if _, err := env.store.Create(context.Background(), record, nil); err != nil {
		t.Fatalf("seed idle run: %v", err)
	}

	// Cycle 1: pause before the run has done anything.
	pauser := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events)
	if _, err := pauser.Pause(context.Background(), record.State.RunID, "cycle one"); err != nil {
		t.Fatalf("cycle 1 pause: %v", err)
	}
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events)
	_, err := runtime.Resume(context.Background(), record.State.RunID)
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) || waitingErr.Kind != "operator_pause" {
		t.Fatalf("cycle 1: expected operator_pause waiting, got %v", err)
	}
	if got := runnerCalls(runner); len(got) != 0 {
		t.Fatalf("cycle 1: role executed before pause cleared: %v", got)
	}

	// Resume clears cycle 1: planner runs, developer runs into its scripted
	// approval wait (unrelated to pause).
	state, err := runtime.Resume(context.Background(), record.State.RunID)
	if !errors.As(err, &waitingErr) || waitingErr.Kind != "approval" {
		t.Fatalf("expected approval waiting after cycle 1 clear, got %v (state=%#v)", err, state)
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, []Role{RolePlanner, RoleDeveloper}) {
		t.Fatalf("calls after cycle 1 clear = %v, want [planner developer]", got)
	}

	// Cycle 2: pause while the run sits on the (unrelated) approval wait.
	if _, err := runtime.Pause(context.Background(), record.State.RunID, "cycle two"); err != nil {
		t.Fatalf("cycle 2 pause: %v", err)
	}
	// Resuming retries the approval-waited role (developer succeeds this
	// time) and only then observes cycle 2's pause at the next boundary
	// (tester), never re-triggering the resolved approval wait.
	_, err = runtime.Resume(context.Background(), record.State.RunID)
	if !errors.As(err, &waitingErr) || waitingErr.Kind != "operator_pause" {
		t.Fatalf("cycle 2: expected operator_pause waiting, got %v", err)
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, []Role{RolePlanner, RoleDeveloper, RoleDeveloper}) {
		t.Fatalf("calls after cycle 2 trigger = %v, want [planner developer developer]", got)
	}

	// Resume clears cycle 2: tester and reviewer run to completion.
	final, err := runtime.Resume(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("resume after cycle 2 clear: %v", err)
	}
	if final.Status != RunStatusCompleted {
		t.Fatalf("final state = %#v, want completed", final)
	}
	wantCalls := []Role{RolePlanner, RoleDeveloper, RoleDeveloper, RoleTester, RoleReviewer}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("final calls = %v, want %v", got, wantCalls)
	}

	_, requested, err := env.store.PauseRequest(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("pause request lookup: %v", err)
	}
	if requested {
		t.Fatal("pause request was not cleared after cycle 2")
	}
}
