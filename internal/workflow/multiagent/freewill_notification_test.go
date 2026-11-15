package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

type recordingSink struct {
	mu      sync.Mutex
	got     []FreewillNotification
	failFor map[string]int // eventID -> remaining failures
}

func (r *recordingSink) NotifyFreewill(ctx context.Context, n FreewillNotification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failFor[n.EventID] > 0 {
		r.failFor[n.EventID]--
		return errors.New("sink injected failure")
	}
	r.got = append(r.got, n)
	return nil
}

func newTestService(t *testing.T, sink FreewillNotificationSink) (*FreewillNotificationService, string) {
	t.Helper()
	dir := t.TempDir()
	events, err := event.NewSQLiteEventStore(dir + "/freewill.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { events.Close() })
	svc, err := NewFreewillNotificationService(dir+"/freewill.db", events, sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)
	t.Cleanup(func() { cancel(); svc.Close() })
	return svc, dir
}

func TestFreewillNotificationEnqueueDelivers(t *testing.T) {
	sink := &recordingSink{}
	svc, _ := newTestService(t, sink)

	evt := event.NewEvent(event.EventFreewillApprovalProposed, "test", map[string]any{"run_id": "freewill_x", "selected_title": "T"})
	evt.CorrelationID = event.NewCorrelationID()
	if err := svc.Enqueue(context.Background(), evt); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.got)
		sink.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("notification not delivered in time")
		}
		time.Sleep(200 * time.Millisecond)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.got[0].Payload["selected_title"] != "T" {
		t.Fatalf("payload not passed through: %+v", sink.got[0].Payload)
	}
}

func TestFreewillNotificationEnqueueDedupesByEventID(t *testing.T) {
	sink := &recordingSink{}
	svc, _ := newTestService(t, sink)

	evt := event.NewEvent(event.EventFreewillPolicyDenied, "test", map[string]any{"run_id": "freewill_x"})
	evt.CorrelationID = event.NewCorrelationID()
	_ = svc.Enqueue(context.Background(), evt)
	_ = svc.Enqueue(context.Background(), evt) // same event id — must not double-send
	deadline := time.Now().Add(15 * time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.got)
		sink.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			sink.mu.Lock()
			defer sink.mu.Unlock()
			t.Fatalf("expected exactly 1 delivery, got %d", len(sink.got))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestFreewillNotificationReconcilerScansCanonicalEvents(t *testing.T) {
	sink := &recordingSink{}
	svc, _ := newTestService(t, sink)

	ctx := context.Background()
	// Seed canonical events: one worthy, one chatter, one worthy after.
	e1 := event.NewEvent(event.EventFreewillApprovalDecided, "test", map[string]any{"run_id": "freewill_a"})
	e1.CorrelationID = event.NewCorrelationID()
	e2 := event.NewEvent(event.EventFreewillWakeClaimed, "test", map[string]any{"run_id": "freewill_a"})
	e2.CorrelationID = event.NewCorrelationID()
	e3 := event.NewEvent(event.EventFreewillSelectionHalted, "test", map[string]any{"run_id": "freewill_a"})
	e3.CorrelationID = event.NewCorrelationID()
	if err := svc.events.StoreBatch(ctx, []event.Event{e1, e2, e3}); err != nil {
		t.Fatal(err)
	}

	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(ctx); err != nil { // second scan must not duplicate
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.got)
		sink.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			sink.mu.Lock()
			defer sink.mu.Unlock()
			t.Fatalf("expected 2 worthy notifications (chatter excluded), got %d", len(sink.got))
		}
		time.Sleep(200 * time.Millisecond)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	types := map[string]bool{}
	for _, n := range sink.got {
		types[n.EventType] = true
	}
	if types[event.EventFreewillWakeClaimed] {
		t.Fatal("chatter event must not be enqueued")
	}
	if svc.scanEvery != 15*time.Second {
		t.Fatal("scan interval default changed unexpectedly")
	}
}

func TestFreewillNotificationWorthyIncludesTerminalExecutionOnly(t *testing.T) {
	for _, eventType := range []string{event.EventFreewillExecutionCompleted, event.EventFreewillExecutionFailed, event.EventFreewillExecutionCancelled} {
		if !freewillNotifyWorthy(eventType) {
			t.Errorf("terminal execution event %q is not notification-worthy", eventType)
		}
	}
	for _, eventType := range []string{event.EventFreewillExecutionDispatching, event.EventFreewillExecutionDispatched} {
		if freewillNotifyWorthy(eventType) {
			t.Errorf("nonterminal execution event %q is notification-worthy", eventType)
		}
	}
}

func TestFreewillNotificationReconcilerAdvancesCursorAcrossScans(t *testing.T) {
	sink := &recordingSink{}
	svc, _ := newTestService(t, sink)
	ctx := context.Background()

	first := event.NewEvent(event.EventFreewillApprovalDecided, "test", map[string]any{"run_id": "freewill_a"})
	if err := svc.events.StoreBatch(ctx, []event.Event{first}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	firstCursor, _, err := svc.loadCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}

	second := event.NewEvent(event.EventFreewillPolicyDenied, "test", map[string]any{"run_id": "freewill_a"})
	if err := svc.events.StoreBatch(ctx, []event.Event{second}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile after cursor advance: %v", err)
	}
	secondCursor, lastEventID, err := svc.loadCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if secondCursor <= firstCursor || lastEventID != second.ID {
		t.Fatalf("cursor did not advance: first=%d second=%d last_event_id=%q want=%q", firstCursor, secondCursor, lastEventID, second.ID)
	}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("repeated reconcile at current cursor: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.got)
		sink.mu.Unlock()
		if n == 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected exactly 2 reconciled notifications, got %d", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFreewillNotificationReconcilerMigratesLegacyCursor(t *testing.T) {
	sink := &recordingSink{}
	svc, _ := newTestService(t, sink)
	ctx := context.Background()

	previous := event.NewEvent(event.EventFreewillApprovalDecided, "test", map[string]any{"run_id": "freewill_a"})
	if err := svc.events.StoreBatch(ctx, []event.Event{previous}); err != nil {
		t.Fatal(err)
	}
	_, legacyRowID, err := svc.events.QueryInInsertionOrderPage(ctx, event.EventFilter{Type: "prism.freewill.", Limit: 10}, 0)
	if err != nil {
		t.Fatal(err)
	}
	legacyPayload, err := json.Marshal(map[string]any{"row_id": legacyRowID, "last_event_id": previous.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.outbox.Accept(ctx, "freewill.notification.cursor", event.Command{
		SchemaVersion:  event.CommandSchemaVersion,
		EventID:        "legacy-freewill-notify-cursor",
		Type:           "freewill.notify.cursor",
		RunID:          "freewill-notification-cursor",
		CorrelationID:  "freewill-notify-cursor",
		IdempotencyKey: freewillNotifyCursorKey,
		Payload:        legacyPayload,
	})
	if err != nil {
		t.Fatal(err)
	}

	current, _, err := svc.loadCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current != legacyRowID {
		t.Fatalf("legacy cursor was not migrated: got %d want %d", current, legacyRowID)
	}
	next := event.NewEvent(event.EventFreewillPolicyDenied, "test", map[string]any{"run_id": "freewill_a"})
	if err := svc.events.StoreBatch(ctx, []event.Event{next}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile after legacy cursor migration: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		sink.mu.Lock()
		got := append([]FreewillNotification(nil), sink.got...)
		sink.mu.Unlock()
		if len(got) == 1 {
			if got[0].EventID != next.ID {
				t.Fatalf("legacy cursor replayed a prior notification: got event %q want %q", got[0].EventID, next.ID)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected only the post-cursor notification, got %d", len(got))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFreewillNotificationRetryOnSinkFailure(t *testing.T) {
	sink := &recordingSink{failFor: map[string]int{"evt-1": 1}}
	svc, _ := newTestService(t, sink)

	evt := event.NewEvent(event.EventFreewillApprovalProposed, "test", map[string]any{"run_id": "freewill_r"})
	evt.ID = "evt-1"
	evt.CorrelationID = event.NewCorrelationID()
	if err := svc.Enqueue(context.Background(), evt); err != nil {
		t.Fatal(err)
	}
	// First dispatch fails (injected), retry after RetryAfter (10s in prod;
	// deliver loop ticks every 2s). Wait long enough for one retry.
	deadline := time.Now().Add(20 * time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.got)
		sink.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			sink.mu.Lock()
			defer sink.mu.Unlock()
			t.Fatalf("failed sink call not retried, deliveries: %d", len(sink.got))
		}
		time.Sleep(500 * time.Millisecond)
	}
}
