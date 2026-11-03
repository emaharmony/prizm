package multiagent

import (
	"context"
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
