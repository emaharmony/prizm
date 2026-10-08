package memory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

type idempotentPrimary struct {
	mu          sync.Mutex
	err         error
	deliveries  map[string]string
	remoteCalls int
}

// barrierFailingPrimary holds concurrent deliveries until both workers have
// observed the same unresolved generation. It models the race that retry
// event IDs must collapse without serializing independent workers.
type barrierFailingPrimary struct {
	mu         sync.Mutex
	entered    int
	release    chan struct{}
	deliveries map[string]string
}

func (p *barrierFailingPrimary) Capture(context.Context, Memory) (string, error) {
	return "", errors.New("offline")
}

func (p *barrierFailingPrimary) CaptureIdempotent(context.Context, Memory, string) (string, error) {
	p.mu.Lock()
	p.entered++
	if p.entered == 2 {
		close(p.release)
	}
	p.mu.Unlock()
	<-p.release
	return "", errors.New("offline")
}

func (p *barrierFailingPrimary) Search(context.Context, SearchRequest) ([]Memory, error) {
	return nil, nil
}

func (p *idempotentPrimary) Capture(_ context.Context, mem Memory) (string, error) {
	return p.CaptureIdempotent(context.Background(), mem, mem.ID)
}

func (p *idempotentPrimary) CaptureIdempotent(_ context.Context, mem Memory, key string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return "", p.err
	}
	if id, ok := p.deliveries[key]; ok {
		return id, nil
	}
	p.remoteCalls++
	id := "recall-" + mem.ID
	p.deliveries[key] = id
	return id, nil
}

func (p *idempotentPrimary) Search(context.Context, SearchRequest) ([]Memory, error) { return nil, nil }

func newReconcileStore(t *testing.T) event.EventStore {
	t.Helper()
	store, err := event.NewSQLiteEventStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createPendingCapture(t *testing.T, local MemoryStore, events event.EventStore, primary PrimaryBackend, scope Scope) Memory {
	t.Helper()
	f := &Facade{Local: local, Events: events, Primary: primary, Source: "test"}
	mem, fallback, err := f.Capture(context.Background(), CaptureRequest{Scope: scope, CaptureKey: "pending", Content: "recover after outage"})
	if err != nil || !fallback {
		t.Fatalf("pending capture = %#v fallback=%v err=%v", mem, fallback, err)
	}
	return mem
}

func syncEvents(t *testing.T, store event.EventStore, typ string) []event.Event {
	t.Helper()
	all, err := store.Query(context.Background(), event.EventFilter{Type: typ, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func TestReconcilerRestartsPendingCaptureAndWritesOneTerminalSync(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	primary := &idempotentPrimary{err: errors.New("offline"), deliveries: map[string]string{}}
	mem := createPendingCapture(t, local, events, primary, scopedTestScope())
	primary.err = nil
	// A new consumer simulates a process restart discovering the old fact.
	NewReconciler(local, primary, events, "test").RunOnce(context.Background())
	synced := syncEvents(t, events, EventScopedCaptureSynced)
	if len(synced) != 1 || synced[0].Payload["memory_id"] != mem.ID || primary.remoteCalls != 1 {
		t.Fatalf("synced=%#v remoteCalls=%d", synced, primary.remoteCalls)
	}
}

func TestReconcilerConcurrentWorkersNeedOneRemoteDeliveryAndTerminalEvent(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	primary := &idempotentPrimary{err: errors.New("offline"), deliveries: map[string]string{}}
	createPendingCapture(t, local, events, primary, scopedTestScope())
	primary.err = nil
	left := NewReconciler(local, primary, events, "left")
	right := NewReconciler(local, primary, events, "right")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); left.RunOnce(context.Background()) }()
	go func() { defer wg.Done(); right.RunOnce(context.Background()) }()
	wg.Wait()
	if got := len(syncEvents(t, events, EventScopedCaptureSynced)); got != 1 {
		t.Fatalf("terminal synced events = %d", got)
	}
	if primary.remoteCalls != 1 {
		t.Fatalf("remote delivery count = %d", primary.remoteCalls)
	}
}

func TestReconcilerRetriesRemoteSuccessAfterLocalSyncWriteFailure(t *testing.T) {
	local := tempStore(t)
	base := newReconcileStore(t)
	primary := &idempotentPrimary{err: errors.New("offline"), deliveries: map[string]string{}}
	mem := createPendingCapture(t, local, base, primary, scopedTestScope())
	primary.err = nil
	flaky := &syncWriteFailureStore{EventStore: base, failSync: true}
	NewReconciler(local, primary, flaky, "test").RunOnce(context.Background())
	if got := len(syncEvents(t, base, EventScopedCaptureSynced)); got != 0 || primary.remoteCalls != 1 {
		t.Fatalf("first pass synced=%d remoteCalls=%d", got, primary.remoteCalls)
	}
	NewReconciler(local, primary, base, "test").RunOnce(context.Background())
	synced := syncEvents(t, base, EventScopedCaptureSynced)
	if len(synced) != 1 || synced[0].Payload["memory_id"] != mem.ID || primary.remoteCalls != 1 {
		t.Fatalf("recovery synced=%#v remoteCalls=%d", synced, primary.remoteCalls)
	}
}

func TestCaptureReplaysRemoteSuccessAfterSyncedEventWriteFailure(t *testing.T) {
	local := tempStore(t)
	base := newReconcileStore(t)
	primary := &idempotentPrimary{deliveries: map[string]string{}}
	flaky := &syncWriteFailureStore{EventStore: base, failSync: true}
	f := &Facade{Local: local, Primary: primary, Events: flaky, Source: "test"}
	_, _, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), CaptureKey: "initial-sync-crash", Content: "replay exact key"})
	if err == nil {
		t.Fatal("expected local synced event write failure")
	}
	if primary.remoteCalls != 1 {
		t.Fatalf("initial remote deliveries = %d", primary.remoteCalls)
	}
	NewReconciler(local, primary, base, "test").RunOnce(context.Background())
	if got := primary.remoteCalls; got != 1 {
		t.Fatalf("replay duplicated remote delivery: %d", got)
	}
	if got := len(syncEvents(t, base, EventScopedCaptureSynced)); got != 1 {
		t.Fatalf("synced events = %d", got)
	}
}

func TestReconcilerFailsClosedForScopeMismatchAndNonIdempotentPrimary(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	scope := scopedTestScope()
	mem := Memory{ID: "wrong-scope", Content: "private", ProjectID: "other", TaskID: scope.TaskID, UserID: scope.UserID}
	if _, err := local.Store(context.Background(), mem); err != nil {
		t.Fatal(err)
	}
	pending := event.NewEvent(EventScopedCaptureSyncPending, "test", map[string]any{"memory_id": mem.ID, "sync_key": mem.ID, "project_id": scope.ProjectID, "task_id": scope.TaskID, "user_id": scope.UserID, "reason": "offline"}).WithCorrelationID(scope.CorrelationID)
	if err := events.Store(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	NewReconciler(local, &scopedPrimary{}, events, "test").RunOnce(context.Background())
	failed := syncEvents(t, events, EventScopedCaptureSyncFailed)
	if len(failed) != 1 || failed[0].Payload["reason"] != "scope_mismatch" {
		t.Fatalf("failed=%#v", failed)
	}
}

func TestReconcilerKeepsPendingWhenPrimaryCannotProveIdempotency(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	primary := &scopedPrimary{err: errors.New("offline")}
	createPendingCapture(t, local, events, primary, scopedTestScope())
	primary.err = nil
	r := NewReconciler(local, primary, events, "test")
	r.RunOnce(context.Background())
	if failed := syncEvents(t, events, EventScopedCaptureSyncFailed); len(failed) != 0 {
		t.Fatalf("pending delivery was dead-lettered: %#v", failed)
	}
	if status := r.Status(); status.Pending != 1 || status.LastErr == "" {
		t.Fatalf("status = %#v, want one pending capture and availability error", status)
	}
}

func TestReconcilerKeepsPendingWhenPrimaryIsUnconfiguredAtStartup(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	f := &Facade{Local: local, Events: events, Source: "serve"}
	if _, fallback, err := f.Capture(context.Background(), CaptureRequest{Scope: scopedTestScope(), CaptureKey: "serve-startup", Content: "wait for configured primary"}); err != nil || fallback {
		t.Fatalf("local-only capture = fallback=%v err=%v", fallback, err)
	}
	r := NewReconciler(local, nil, events, "serve")
	r.RunOnce(context.Background())
	if failed := syncEvents(t, events, EventScopedCaptureSyncFailed); len(failed) != 0 {
		t.Fatalf("unconfigured startup dead-lettered valid memory: %#v", failed)
	}
	if status := r.Status(); status.Pending != 1 || status.LastErr == "" {
		t.Fatalf("status = %#v, want one retained pending capture", status)
	}
}

func TestReconcilerStatusCountsOnlyUnresolvedPendingFacts(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	scope := scopedTestScope()
	mem := Memory{ID: "already-synced", Content: "durable", ProjectID: scope.ProjectID, TaskID: scope.TaskID, UserID: scope.UserID}
	if _, err := local.Store(context.Background(), mem); err != nil {
		t.Fatal(err)
	}
	pending := event.NewEvent(EventScopedCaptureSyncPending, "test", map[string]any{"memory_id": mem.ID, "sync_key": mem.ID, "project_id": scope.ProjectID, "task_id": scope.TaskID, "user_id": scope.UserID}).WithCorrelationID(scope.CorrelationID)
	synced := event.NewEvent(EventScopedCaptureSynced, "test", map[string]any{"memory_id": mem.ID, "sync_key": mem.ID, "project_id": scope.ProjectID, "task_id": scope.TaskID, "user_id": scope.UserID}).WithCorrelationID(scope.CorrelationID)
	if err := events.Store(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if err := events.Store(context.Background(), synced); err != nil {
		t.Fatal(err)
	}
	r := NewReconciler(local, nil, events, "test")
	r.RunOnce(context.Background())
	if status := r.Status(); status.Pending != 0 {
		t.Fatalf("status pending = %d, want 0 after terminal sync", status.Pending)
	}
}

func TestReconcilerKeepsTwoProjectDeliveriesIsolated(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	primary := &idempotentPrimary{err: errors.New("offline"), deliveries: map[string]string{}}
	first := createPendingCapture(t, local, events, primary, scopedTestScope())
	other := scopedTestScope()
	other.ProjectID = "project-b"
	other.CorrelationID = "corr-b"
	second := createPendingCapture(t, local, events, primary, other)
	primary.err = nil
	NewReconciler(local, primary, events, "test").RunOnce(context.Background())
	synced := syncEvents(t, events, EventScopedCaptureSynced)
	if len(synced) != 2 || primary.remoteCalls != 2 {
		t.Fatalf("synced=%#v remoteCalls=%d", synced, primary.remoteCalls)
	}
	seen := map[string]string{}
	for _, e := range synced {
		seen[e.Payload["memory_id"].(string)] = e.Payload["project_id"].(string)
	}
	if seen[first.ID] != "project-a" || seen[second.ID] != "project-b" {
		t.Fatalf("scope leakage in synced events: %#v", seen)
	}
}

func TestReconcilerRecordsRetryThenTerminalFailure(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	primary := &idempotentPrimary{err: errors.New("offline"), deliveries: map[string]string{}}
	createPendingCapture(t, local, events, primary, scopedTestScope())
	r := NewReconciler(local, primary, events, "test")
	r.MaxAttempts = 2
	r.RetryBase = time.Nanosecond
	r.RunOnce(context.Background())
	time.Sleep(time.Millisecond)
	r.RunOnce(context.Background())
	if got := len(syncEvents(t, events, EventScopedCaptureSyncRetry)); got != 1 {
		t.Fatalf("retry events = %d", got)
	}
	failed := syncEvents(t, events, EventScopedCaptureSyncFailed)
	if len(failed) != 1 || failed[0].Payload["reason"] != "retry_exhausted" {
		t.Fatalf("terminal failure=%#v", failed)
	}
}

func TestReconcilerConcurrentFailuresWriteOneRetryPerAttempt(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	setupPrimary := &idempotentPrimary{err: errors.New("offline"), deliveries: map[string]string{}}
	createPendingCapture(t, local, events, setupPrimary, scopedTestScope())
	barrier := &barrierFailingPrimary{release: make(chan struct{}), deliveries: map[string]string{}}

	runConcurrent := func(primary PrimaryBackend) {
		left := NewReconciler(local, primary, events, "left")
		right := NewReconciler(local, primary, events, "right")
		left.MaxAttempts, right.MaxAttempts = 3, 3
		left.RetryBase, right.RetryBase = time.Nanosecond, time.Nanosecond
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); left.RunOnce(context.Background()) }()
		go func() { defer wg.Done(); right.RunOnce(context.Background()) }()
		wg.Wait()
	}

	runConcurrent(barrier)
	if retries := syncEvents(t, events, EventScopedCaptureSyncRetry); len(retries) != 1 || retries[0].Payload["attempt"] != float64(1) {
		t.Fatalf("first concurrent failure retries=%#v, want one attempt 1", retries)
	}
	if failed := syncEvents(t, events, EventScopedCaptureSyncFailed); len(failed) != 0 {
		t.Fatalf("first concurrent failure prematurely exhausted retries: %#v", failed)
	}

	primary := &idempotentPrimary{err: errors.New("offline"), deliveries: map[string]string{}}
	time.Sleep(time.Millisecond)
	next := NewReconciler(local, primary, events, "next")
	next.MaxAttempts = 3
	next.RetryBase = time.Nanosecond
	next.RunOnce(context.Background())
	if retries := syncEvents(t, events, EventScopedCaptureSyncRetry); len(retries) != 2 {
		t.Fatalf("second concurrent failure retries=%#v, want one fact per distinct attempt", retries)
	}
	if failed := syncEvents(t, events, EventScopedCaptureSyncFailed); len(failed) != 0 {
		t.Fatalf("second delivery prematurely exhausted retries: %#v", failed)
	}

	time.Sleep(time.Millisecond)
	terminal := NewReconciler(local, primary, events, "terminal")
	terminal.MaxAttempts = 3
	terminal.RetryBase = time.Nanosecond
	terminal.RunOnce(context.Background())
	if failed := syncEvents(t, events, EventScopedCaptureSyncFailed); len(failed) != 1 || failed[0].Payload["reason"] != "retry_exhausted" {
		t.Fatalf("terminal failure=%#v, want one exhausted terminal fact", failed)
	}
}

func TestReconcilerPaginatesCausalFactsPastOnePage(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	scope := scopedTestScope()
	mem := Memory{ID: "paged-memory", Content: "already delivered", ProjectID: scope.ProjectID, TaskID: scope.TaskID, UserID: scope.UserID}
	if _, err := local.Store(context.Background(), mem); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"memory_id": mem.ID, "sync_key": mem.ID, "project_id": scope.ProjectID, "task_id": scope.TaskID, "user_id": scope.UserID}
	pending := event.NewEvent(EventScopedCaptureSyncPending, "test", payload).WithCorrelationID(scope.CorrelationID)
	if err := events.Store(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	noise := make([]event.Event, reconciliationPageSize)
	for i := range noise {
		noise[i] = event.NewEvent("prizm.memory.capture.noise", "test", map[string]any{"memory_id": fmt.Sprintf("noise-%d", i), "sync_key": fmt.Sprintf("noise-%d", i)})
	}
	if err := events.StoreBatch(context.Background(), noise); err != nil {
		t.Fatal(err)
	}
	synced := event.NewEvent(EventScopedCaptureSynced, "test", payload).WithCorrelationID(scope.CorrelationID)
	synced.ID = stableSyncEventID(EventScopedCaptureSynced, mem.ID, pending.ID)
	if err := events.Store(context.Background(), synced); err != nil {
		t.Fatal(err)
	}
	primary := &idempotentPrimary{deliveries: map[string]string{}}
	r := NewReconciler(local, primary, events, "test")
	r.RunOnce(context.Background())
	if primary.remoteCalls != 0 {
		t.Fatalf("remote calls = %d, terminal fact on later causal page was ignored", primary.remoteCalls)
	}
	if status := r.Status(); status.Pending != 0 {
		t.Fatalf("status=%#v, want terminal delivery to leave no pending work", status)
	}
}

func TestReconcileFactsTreatsLaterPendingAsNewUnresolvedDelivery(t *testing.T) {
	scope := scopedTestScope()
	pending := event.NewEvent(EventScopedCaptureSyncPending, "test", map[string]any{"memory_id": "mem-1", "sync_key": "key-1", "project_id": scope.ProjectID, "task_id": scope.TaskID, "user_id": scope.UserID}).WithCorrelationID(scope.CorrelationID)
	synced := event.NewEvent(EventScopedCaptureSynced, "test", map[string]any{"memory_id": "mem-1", "sync_key": "key-1", "project_id": scope.ProjectID, "task_id": scope.TaskID, "user_id": scope.UserID}).WithCorrelationID(scope.CorrelationID)
	repending := event.NewEvent(EventScopedCaptureSyncPending, "test", map[string]any{"memory_id": "mem-1", "sync_key": "key-1", "project_id": scope.ProjectID, "task_id": scope.TaskID, "user_id": scope.UserID}).WithCorrelationID(scope.CorrelationID)
	unresolved, terminal, _ := reconcileFacts([]event.Event{pending, synced, repending})
	if len(unresolved) != 1 || terminal["key-1"] {
		t.Fatalf("unresolved=%#v terminal=%#v", unresolved, terminal)
	}
}

func TestReconcilerUsesSQLiteInsertionOrderForRePendingGeneration(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	scope := scopedTestScope()
	bootstrap := newReconcileStore(t)
	primary := &idempotentPrimary{err: errors.New("offline"), deliveries: map[string]string{}}
	mem := createPendingCapture(t, local, bootstrap, primary, scope)
	primary.err = nil
	payload := map[string]any{"memory_id": mem.ID, "sync_key": "causal-key", "project_id": scope.ProjectID, "task_id": scope.TaskID, "user_id": scope.UserID}
	oldPending := event.NewEvent(EventScopedCaptureSyncPending, "test", payload).WithCorrelationID(scope.CorrelationID)
	oldPending.ID = "z-old-pending"
	oldSynced := event.NewEvent(EventScopedCaptureSynced, "test", payload).WithCorrelationID(scope.CorrelationID)
	oldSynced.ID = stableSyncEventID(EventScopedCaptureSynced, "causal-key", oldPending.ID)
	newPending := event.NewEvent(EventScopedCaptureSyncPending, "test", payload).WithCorrelationID(scope.CorrelationID)
	newPending.ID = "a-new-pending"
	for _, fact := range []event.Event{oldPending, oldSynced, newPending} {
		if err := events.Store(context.Background(), fact); err != nil {
			t.Fatal(err)
		}
	}
	r := NewReconciler(local, primary, events, "test")
	r.RunOnce(context.Background())
	if primary.remoteCalls != 1 {
		t.Fatalf("remote calls = %d, want replay of the later pending generation", primary.remoteCalls)
	}
	if status := r.Status(); status.Pending != 0 {
		t.Fatalf("status = %#v, want re-pending generation to receive terminal evidence", status)
	}
	if got := len(syncEvents(t, events, EventScopedCaptureSynced)); got != 2 {
		t.Fatalf("synced events = %d, want one terminal fact per pending generation", got)
	}
}

func TestReconcilerResetsRetryBackoffForNewPendingGeneration(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	scope := scopedTestScope()
	bootstrap := newReconcileStore(t)
	primary := &idempotentPrimary{err: errors.New("offline"), deliveries: map[string]string{}}
	mem := createPendingCapture(t, local, bootstrap, primary, scope)
	payload := map[string]any{"memory_id": mem.ID, "sync_key": "retry-generation-key", "project_id": scope.ProjectID, "task_id": scope.TaskID, "user_id": scope.UserID}
	oldPending := event.NewEvent(EventScopedCaptureSyncPending, "test", payload).WithCorrelationID(scope.CorrelationID)
	oldPending.ID = "old-pending"
	oldRetryPayload := map[string]any{"memory_id": mem.ID, "sync_key": "retry-generation-key", "project_id": scope.ProjectID, "task_id": scope.TaskID, "user_id": scope.UserID, "attempt": 1, "reason": "primary_unavailable"}
	oldRetry := event.NewEvent(EventScopedCaptureSyncRetry, "test", oldRetryPayload).WithCorrelationID(scope.CorrelationID)
	oldFailed := event.NewEvent(EventScopedCaptureSyncFailed, "test", payload).WithCorrelationID(scope.CorrelationID)
	oldFailed.ID = stableSyncEventID(EventScopedCaptureSyncFailed, "retry-generation-key", oldPending.ID)
	newPending := event.NewEvent(EventScopedCaptureSyncPending, "test", payload).WithCorrelationID(scope.CorrelationID)
	newPending.ID = "new-pending"
	for _, fact := range []event.Event{oldPending, oldRetry, oldFailed, newPending} {
		if err := events.Store(context.Background(), fact); err != nil {
			t.Fatal(err)
		}
	}
	r := NewReconciler(local, primary, events, "test")
	r.MaxAttempts = 2
	r.RetryBase = time.Hour
	r.RunOnce(context.Background())
	if primary.remoteCalls != 0 {
		t.Fatalf("successful remote calls = %d, want failed attempted delivery", primary.remoteCalls)
	}
	if got := len(syncEvents(t, events, EventScopedCaptureSyncRetry)); got != 2 {
		t.Fatalf("retry events = %d, want old retry plus immediate new-generation retry", got)
	}
}

func TestReconcilerRetainsTerminalEventStoreFailureInStatus(t *testing.T) {
	local := tempStore(t)
	base := newReconcileStore(t)
	primary := &idempotentPrimary{err: errors.New("offline"), deliveries: map[string]string{}}
	createPendingCapture(t, local, base, primary, scopedTestScope())
	primary.err = nil
	failing := &terminalWriteFailureStore{EventStore: base, failType: EventScopedCaptureSynced}
	r := NewReconciler(local, primary, failing, "test")
	r.RunOnce(context.Background())
	if status := r.Status(); status.LastErr == "" {
		t.Fatalf("status = %#v, want terminal event persistence error", status)
	}
	if got := len(syncEvents(t, base, EventScopedCaptureSynced)); got != 0 {
		t.Fatalf("synced events = %d, want failed terminal write to remain visible", got)
	}
}

func TestReconcilerFailsClosedWithoutCausalEventOrder(t *testing.T) {
	local := tempStore(t)
	base := newReconcileStore(t)
	r := NewReconciler(local, nil, &nonCausalEventStore{EventStore: base}, "test")
	r.RunOnce(context.Background())
	if status := r.Status(); status.LastErr == "" {
		t.Fatalf("status = %#v, want causal-order capability error", status)
	}
}

type syncWriteFailureStore struct {
	event.EventStore
	failSync bool
}

type terminalWriteFailureStore struct {
	event.EventStore
	failType string
}

type nonCausalEventStore struct {
	event.EventStore
}

func (s *terminalWriteFailureStore) Store(ctx context.Context, e event.Event) error {
	if e.Type == s.failType {
		return errors.New("terminal event store unavailable")
	}
	return s.EventStore.Store(ctx, e)
}

func (s *terminalWriteFailureStore) QueryInInsertionOrderPage(ctx context.Context, filter event.EventFilter, afterRowID int64) ([]event.Event, int64, error) {
	return s.EventStore.(*event.SQLiteEventStore).QueryInInsertionOrderPage(ctx, filter, afterRowID)
}

func (s *syncWriteFailureStore) Store(ctx context.Context, e event.Event) error {
	if s.failSync && e.Type == EventScopedCaptureSynced {
		s.failSync = false
		return errors.New("crash before local synced")
	}
	return s.EventStore.Store(ctx, e)
}

func (s *syncWriteFailureStore) QueryInInsertionOrderPage(ctx context.Context, filter event.EventFilter, afterRowID int64) ([]event.Event, int64, error) {
	return s.EventStore.(*event.SQLiteEventStore).QueryInInsertionOrderPage(ctx, filter, afterRowID)
}
