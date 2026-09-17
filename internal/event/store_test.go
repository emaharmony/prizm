package event

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteEventStore_StoreAndQuery(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}
	defer store.Close()

	event := Event{
		ID:            "evt_001",
		Type:          "prizm.task.created",
		Timestamp:     "2026-05-18T12:00:00Z",
		CorrelationID: "corr_001",
		ParentID:      "",
		Payload:       map[string]any{"task": "hello world"},
		Metadata:      EventMetadata{RunID: "run_test123"},
	}

	ctx := context.Background()
	err = store.Store(ctx, event)
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	events, err := store.Query(ctx, EventFilter{RunID: "run_test123"})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ID != "evt_001" {
		t.Errorf("event ID = %q, want evt_001", events[0].ID)
	}
	if events[0].Type != "prizm.task.created" {
		t.Errorf("event Type = %q, want prizm.task.created", events[0].Type)
	}
}

func TestSQLiteEventStore_StoreBatch(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}
	defer store.Close()

	events := []Event{
		{ID: "evt_001", Type: "prizm.task.created", Timestamp: "2026-05-18T12:00:00Z", Payload: map[string]any{}, Metadata: EventMetadata{RunID: "run_batch"}},
		{ID: "evt_002", Type: "prizm.task.started", Timestamp: "2026-05-18T12:00:01Z", Payload: map[string]any{}, Metadata: EventMetadata{RunID: "run_batch"}},
		{ID: "evt_003", Type: "prizm.task.completed", Timestamp: "2026-05-18T12:00:02Z", Payload: map[string]any{}, Metadata: EventMetadata{RunID: "run_batch"}},
	}

	ctx := context.Background()
	err = store.StoreBatch(ctx, events)
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}

	results, err := store.Query(ctx, EventFilter{RunID: "run_batch"})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 events, got %d", len(results))
	}
}

func TestSQLiteEventStore_QueryByType(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	events := []Event{
		{ID: "evt_001", Type: "prizm.task.created", Timestamp: "2026-05-18T12:00:00Z", Payload: map[string]any{}, Metadata: EventMetadata{RunID: "run_1"}},
		{ID: "evt_002", Type: "prizm.agent.started", Timestamp: "2026-05-18T12:00:01Z", Payload: map[string]any{}, Metadata: EventMetadata{RunID: "run_1"}},
		{ID: "evt_003", Type: "prizm.task.completed", Timestamp: "2026-05-18T12:00:02Z", Payload: map[string]any{}, Metadata: EventMetadata{RunID: "run_1"}},
	}
	store.StoreBatch(ctx, events)

	results, err := store.Query(ctx, EventFilter{Type: "prizm.task"})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 task events, got %d", len(results))
	}
}

func TestSQLiteEventStore_QueryWithLimit(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	events := make([]Event, 10)
	for i := range events {
		events[i] = Event{
			ID:        fmt.Sprintf("evt_%03d", i),
			Type:      "prizm.task.created",
			Timestamp: "2026-05-18T12:00:00Z",
			Payload:   map[string]any{},
			Metadata:  EventMetadata{RunID: "run_limit"},
		}
	}
	store.StoreBatch(ctx, events)

	results, err := store.Query(ctx, EventFilter{RunID: "run_limit", Limit: 5})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(results) != 5 {
		t.Errorf("expected 5 events (limit), got %d", len(results))
	}
}

func TestSQLiteEventStore_QueryEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	results, err := store.Query(ctx, EventFilter{RunID: "nonexistent"})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 events for nonexistent run, got %d", len(results))
	}
}

func TestSQLiteEventStore_DefaultLimit(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	events := make([]Event, 150)
	for i := range events {
		events[i] = Event{
			ID:        fmt.Sprintf("evt_%03d", i),
			Type:      "prizm.task.created",
			Timestamp: "2026-05-18T12:00:00Z",
			Payload:   map[string]any{},
			Metadata:  EventMetadata{RunID: "run_default_limit"},
		}
	}
	store.StoreBatch(ctx, events)

	results, err := store.Query(ctx, EventFilter{RunID: "run_default_limit"})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(results) != 100 {
		t.Errorf("expected 100 events (default limit), got %d", len(results))
	}
}

func TestSQLiteEventStore_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "nested", "dir", "test.db")

	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}
	defer store.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file not created")
	}
}

func TestSQLiteEventStore_WALMode(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}
	defer store.Close()

	var mode string
	err = store.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if err != nil {
		t.Fatalf("PRAGMA journal_mode error = %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestSQLiteEventStore_QueryOrdersByIDNotTimestamp(t *testing.T) {
	store, err := NewSQLiteEventStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	// Both events share one colliding timestamp. Insert the lexically
	// larger ID first so an insertion-order or timestamp-order query would
	// return it first; ORDER BY id ASC must still return evt_a before
	// evt_b.
	const collidingTimestamp = "2026-07-30T12:00:00Z"
	later := Event{
		ID:        "evt_b_second",
		Type:      "prizm.task.created",
		Timestamp: collidingTimestamp,
		Payload:   map[string]any{},
		Metadata:  EventMetadata{RunID: "run_id_order"},
	}
	earlier := Event{
		ID:        "evt_a_first",
		Type:      "prizm.task.created",
		Timestamp: collidingTimestamp,
		Payload:   map[string]any{},
		Metadata:  EventMetadata{RunID: "run_id_order"},
	}
	if err := store.Store(ctx, later); err != nil {
		t.Fatalf("store later: %v", err)
	}
	if err := store.Store(ctx, earlier); err != nil {
		t.Fatalf("store earlier: %v", err)
	}

	results, err := store.Query(ctx, EventFilter{RunID: "run_id_order"})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 events, got %d", len(results))
	}
	if results[0].ID != "evt_a_first" || results[1].ID != "evt_b_second" {
		t.Fatalf("events not ordered by id: got [%s, %s], want [evt_a_first, evt_b_second]",
			results[0].ID, results[1].ID)
	}
}

func TestSQLiteEventStore_DuplicateEventIDIsIdempotent(t *testing.T) {
	store, err := NewSQLiteEventStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	evt := Event{
		ID:        "evt_idempotent_reemit",
		Type:      "prizm.workflow.multi_agent.recovery.completed",
		Timestamp: "2026-07-23T12:00:00Z",
		Payload: map[string]any{
			"run_id":      "run_idempotent",
			"workflow_id": "phase1-reference",
			"status":      "completed",
			"reason":      "terminal run unchanged",
		},
		Metadata: EventMetadata{RunID: "run_idempotent"},
	}
	if err := store.Store(ctx, evt); err != nil {
		t.Fatalf("first store: %v", err)
	}
	if err := store.Store(ctx, evt); err != nil {
		t.Fatalf("duplicate store: %v", err)
	}
	if err := store.StoreBatch(ctx, []Event{evt, evt}); err != nil {
		t.Fatalf("duplicate batch: %v", err)
	}

	results, err := store.Query(ctx, EventFilter{
		RunID: "run_idempotent",
		Type:  evt.Type,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("stored duplicates = %d, want 1", len(results))
	}
}
