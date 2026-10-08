package memory

import (
	"context"
	"errors"
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

func TestReconcilerFailsClosedWhenPrimaryCannotProveIdempotency(t *testing.T) {
	local := tempStore(t)
	events := newReconcileStore(t)
	primary := &scopedPrimary{err: errors.New("offline")}
	createPendingCapture(t, local, events, primary, scopedTestScope())
	primary.err = nil
	NewReconciler(local, primary, events, "test").RunOnce(context.Background())
	failed := syncEvents(t, events, EventScopedCaptureSyncFailed)
	if len(failed) != 1 || failed[0].Payload["reason"] != "primary_not_idempotent" {
		t.Fatalf("failed=%#v", failed)
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

type syncWriteFailureStore struct {
	event.EventStore
	failSync bool
}

func (s *syncWriteFailureStore) Store(ctx context.Context, e event.Event) error {
	if s.failSync && e.Type == EventScopedCaptureSynced {
		s.failSync = false
		return errors.New("crash before local synced")
	}
	return s.EventStore.Store(ctx, e)
}
