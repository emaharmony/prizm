package multiagent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prism/internal/event"
)

func TestSQLiteDurableRunStoreRoundTripAndCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	record := durableRecordForTest(t, "run-store-roundtrip")
	started := durableEventForTest(record.State, event.EventMultiAgentRunStarted)

	created, err := store.Create(ctx, record, []event.Event{started})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Revision != 1 {
		t.Fatalf("created revision = %d, want 1", created.Revision)
	}

	loaded, err := store.Load(ctx, record.State.RunID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Revision != 1 || loaded.State.CurrentTask.ID != record.State.CurrentTask.ID {
		t.Fatalf("loaded record = %#v", loaded)
	}

	loaded.State.TransitionCount = 1
	loaded.State.UpdatedAt = loaded.State.UpdatedAt.Add(time.Second)
	checkpointEvent := durableEventForTest(
		loaded.State,
		event.EventMultiAgentTransitionSelected,
	)
	checkpointEvent.Payload["source_role"] = string(RolePlanner)
	checkpointEvent.Payload["outcome"] = string(OutcomePlanReady)
	checkpointEvent.Payload["transition_count"] = 1
	saved, err := store.Checkpoint(
		ctx,
		loaded.Revision,
		loaded,
		[]event.Event{checkpointEvent},
	)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if saved.Revision != 2 || saved.State.TransitionCount != 1 {
		t.Fatalf("saved record = %#v", saved)
	}

	_, err = store.Checkpoint(ctx, 1, loaded, nil)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}

	pending, err := store.PendingEvents(ctx, record.State.RunID, 10)
	if err != nil {
		t.Fatalf("pending events: %v", err)
	}
	if len(pending) != 2 ||
		pending[0].Revision != 1 ||
		pending[1].Revision != 2 {
		t.Fatalf("pending events = %#v", pending)
	}
	if err := store.MarkEventsPublished(
		ctx,
		[]string{pending[0].Event.ID, pending[1].Event.ID},
	); err != nil {
		t.Fatalf("mark published: %v", err)
	}
	pending, err = store.PendingEvents(ctx, record.State.RunID, 10)
	if err != nil {
		t.Fatalf("pending events after ack: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending events after ack = %d", len(pending))
	}
}

func TestSQLiteDurableRunStoreCheckpointAndOutboxAreAtomic(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	record := durableRecordForTest(t, "run-store-atomic")
	badEvent := durableEventForTest(record.State, event.EventMultiAgentRunStarted)
	badEvent.Payload["unserializable"] = make(chan int)

	_, err := store.Create(ctx, record, []event.Event{badEvent})
	if err == nil {
		t.Fatal("expected outbox serialization failure")
	}
	_, loadErr := store.Load(ctx, record.State.RunID)
	if !errors.Is(loadErr, ErrRunNotFound) {
		t.Fatalf("run insert was not rolled back: %v", loadErr)
	}

	created, err := store.Create(ctx, record, nil)
	if err != nil {
		t.Fatalf("create after rollback: %v", err)
	}
	created.State.TransitionCount = 1
	created.State.UpdatedAt = created.State.UpdatedAt.Add(time.Second)
	_, err = store.Checkpoint(
		ctx,
		created.Revision,
		created,
		[]event.Event{badEvent},
	)
	if err == nil {
		t.Fatal("expected checkpoint outbox serialization failure")
	}
	loaded, err := store.Load(ctx, record.State.RunID)
	if err != nil {
		t.Fatalf("load after rollback: %v", err)
	}
	if loaded.Revision != 1 || loaded.State.TransitionCount != 0 {
		t.Fatalf("checkpoint state was not rolled back: %#v", loaded)
	}
}

func TestSQLiteDurableRunStoreRejectsDuplicateRun(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	record := durableRecordForTest(t, "run-store-duplicate")
	if _, err := store.Create(ctx, record, nil); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := store.Create(ctx, record, nil); !errors.Is(err, ErrRunExists) {
		t.Fatalf("expected ErrRunExists, got %v", err)
	}
}

func newSQLiteDurableStoreForTest(t *testing.T) *SQLiteDurableRunStore {
	t.Helper()
	store, err := NewSQLiteDurableRunStore(
		filepath.Join(t.TempDir(), "prism-test.db"),
	)
	if err != nil {
		t.Fatalf("new sqlite durable store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close durable store: %v", err)
		}
	})
	return store
}

func durableRecordForTest(t *testing.T, runID string) DurableRun {
	t.Helper()
	definition := validDefinition()
	supervisor := newTestSupervisor(
		t,
		definition,
		&scriptedRunner{scripts: map[Role][]scriptedStep{}},
		&captureEventSink{},
	)
	request := testRunRequest()
	request.RunID = runID
	return DurableRun{
		SchemaVersion: DurableRunSchemaVersion,
		State:         supervisor.newRunState(request),
		Phase:         CheckpointPendingRole,
	}
}

func durableEventForTest(state RunState, eventType string) event.Event {
	evt := event.NewEvent(eventType, durableEventSource, map[string]any{
		"run_id":      state.RunID,
		"workflow_id": state.WorkflowID,
		"status":      string(state.Status),
	}).WithCorrelationID(state.RunID).
		WithMetadata(event.EventMetadata{RunID: state.RunID})
	evt.Timestamp = state.UpdatedAt.UTC().Format(time.RFC3339Nano)
	return evt
}
