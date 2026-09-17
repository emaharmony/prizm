package multiagent

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestSQLiteDurableRunStoreCancellationRequest(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	if err := store.RequestCancellation(ctx, "missing", "stop"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("missing cancellation request error = %v", err)
	}
	record := durableRecordForTest(t, "run-cancel-request")
	if _, err := store.Create(ctx, record, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.RequestCancellation(ctx, record.State.RunID, "first reason"); err != nil {
		t.Fatalf("request cancellation: %v", err)
	}
	reason, requested, err := store.CancellationRequest(ctx, record.State.RunID)
	if err != nil || !requested || reason != "first reason" {
		t.Fatalf("request = %q/%t, error %v", reason, requested, err)
	}
	if err := store.RequestCancellation(ctx, record.State.RunID, "operator override"); err != nil {
		t.Fatalf("update cancellation: %v", err)
	}
	reason, requested, err = store.CancellationRequest(ctx, record.State.RunID)
	if err != nil || !requested || reason != "operator override" {
		t.Fatalf("updated request = %q/%t, error %v", reason, requested, err)
	}
}

func TestDurableRuntimeActiveCancellationStopsAtRoleBoundary(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := &blockingPlannerRunner{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	runtime := newDurableRuntimeForTest(
		t, validDefinition(), runner, env.store, env.claimer, env.events,
	)
	request := testRunRequest()
	request.RunID = "run-active-cancel"

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

	state, err := runtime.Cancel(context.Background(), request.RunID, "operator stop")
	if err != nil {
		t.Fatalf("request active cancellation: %v", err)
	}
	if state.Status != RunStatusRunning {
		t.Fatalf("active cancellation returned status %q", state.Status)
	}
	close(runner.release)
	completed := <-result
	if completed.state.Status != RunStatusCancelled || completed.state.CancellationReason != "operator stop" {
		t.Fatalf("cancelled state = %#v", completed.state)
	}
	if completed.err == nil || completed.err.Error() != "operator stop" {
		t.Fatalf("run cancellation error = %v", completed.err)
	}
	if calls := runner.callCount(); calls != 1 {
		t.Fatalf("role calls = %d, want planner only", calls)
	}
	record, err := runtime.Inspect(context.Background(), request.RunID)
	if err != nil || record.Phase != CheckpointTerminal || record.State.Status != RunStatusCancelled {
		t.Fatalf("persisted cancellation = %#v, error %v", record, err)
	}
}

type blockingPlannerRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

func (r *blockingPlannerRunner) RunRole(context.Context, RoleRunRequest) (RoleRunResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	r.once.Do(func() { close(r.started) })
	<-r.release
	result := scriptedResult(OutcomePlanReady)
	result.Metadata.WorkspaceID = "workspace-durable"
	return result, nil
}

func (r *blockingPlannerRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}
