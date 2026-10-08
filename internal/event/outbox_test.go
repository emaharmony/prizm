package event

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testCommand(key string) Command {
	return Command{EventID: "evt-" + key, Type: "prizm.command.delegate", RunID: "run-1", TaskID: "task-1", DelegationID: "del-1", CorrelationID: "corr-1", IdempotencyKey: key, SchemaVersion: CommandSchemaVersion, Payload: json.RawMessage(`{"work":"test"}`)}
}

func TestOutboxAcceptOnceAndRejectKeyCollision(t *testing.T) {
	box, err := NewSQLiteOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	ctx := context.Background()
	cmd := testCommand("delivery-1")
	if inserted, err := box.Accept(ctx, "prizm.command", cmd); err != nil || !inserted {
		t.Fatalf("first accept = %v, %v", inserted, err)
	}
	if inserted, err := box.Accept(ctx, "prizm.command", cmd); err != nil || inserted {
		t.Fatalf("duplicate accept = %v, %v", inserted, err)
	}
	changed := cmd
	changed.Type = "different"
	if _, err := box.Accept(ctx, "prizm.command", changed); err == nil {
		t.Fatal("expected idempotency collision")
	}
}

type recordingPublisher struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (p *recordingPublisher) Publish(_ context.Context, _ string, _ []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.fail {
		return errors.New("offline")
	}
	return nil
}

func TestOutboxPublishFailureRestartReplayOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	box, _ := NewSQLiteOutbox(path)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	box.now = func() time.Time { return now }
	cmd := testCommand("delivery-2")
	box.Accept(ctx, "prizm.command", cmd)
	bad := &recordingPublisher{fail: true}
	d := Dispatcher{Outbox: box, Publisher: bad, Lease: time.Second, MaxAttempts: 3, RetryAfter: time.Second}
	if ok, err := d.DispatchOne(ctx); !ok || err == nil {
		t.Fatalf("failed dispatch = %v, %v", ok, err)
	}
	item, _ := box.Get(ctx, cmd.IdempotencyKey)
	if item.State != DeliveryPending {
		t.Fatalf("state=%s", item.State)
	}
	box.Close()
	box, _ = NewSQLiteOutbox(path)
	defer box.Close()
	now = now.Add(2 * time.Second)
	box.now = func() time.Time { return now }
	good := &recordingPublisher{}
	d = Dispatcher{Outbox: box, Publisher: good, Lease: time.Second, MaxAttempts: 3, RetryAfter: time.Second}
	if ok, err := d.DispatchOne(ctx); !ok || err != nil {
		t.Fatalf("replay = %v, %v", ok, err)
	}
	if ok, err := d.DispatchOne(ctx); ok || err != nil {
		t.Fatalf("second replay = %v, %v", ok, err)
	}
	if good.calls != 1 {
		t.Fatalf("calls=%d", good.calls)
	}
}

func TestOutboxExpiredClaimAndBoundedFailure(t *testing.T) {
	box, _ := NewSQLiteOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	defer box.Close()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	box.now = func() time.Time { return now }
	cmd := testCommand("delivery-3")
	box.Accept(ctx, "s", cmd)
	if d, _ := box.Claim(ctx, time.Second); d == nil {
		t.Fatal("expected claim")
	}
	now = now.Add(2 * time.Second)
	if d, _ := box.Claim(ctx, time.Second); d == nil || d.Attempts != 2 {
		t.Fatalf("reclaim=%+v", d)
	}
	state, err := box.Fail(ctx, cmd.IdempotencyKey, errors.New("still offline"), 2, 0)
	if err != nil || state != DeliveryTerminalFailed {
		t.Fatalf("fail=%s %v", state, err)
	}
}

func TestDispatcherDeadlineFailsWithoutPublishing(t *testing.T) {
	box, _ := NewSQLiteOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	defer box.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	box.now = func() time.Time { return now }
	cmd := testCommand("deadline")
	cmd.Deadline = now.Add(-time.Second)
	box.Accept(t.Context(), "s", cmd)
	pub := &recordingPublisher{}
	d := Dispatcher{Outbox: box, Publisher: pub, Lease: time.Second, MaxAttempts: 3, RetryAfter: time.Second}
	handled, err := d.DispatchOne(t.Context())
	if !handled || err != nil {
		t.Fatalf("dispatch=%v %v", handled, err)
	}
	item, _ := box.Get(t.Context(), cmd.IdempotencyKey)
	if item.State != DeliveryTerminalFailed || pub.calls != 0 {
		t.Fatalf("item=%+v calls=%d", item, pub.calls)
	}
}

func TestOutboxOutcomeTraceRejectsDuplicateTerminalAndStaleRetry(t *testing.T) {
	box, _ := NewSQLiteOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	defer box.Close()
	ctx := context.Background()
	cmd := testCommand("del-1:0")
	box.Accept(ctx, "s", cmd)
	now := time.Now().UTC()
	accepted := Outcome{EventID: "out-1", CommandEventID: cmd.EventID, RunID: cmd.RunID, TaskID: cmd.TaskID, DelegationID: cmd.DelegationID, CorrelationID: cmd.CorrelationID, CausationID: cmd.EventID, DeliveryKey: cmd.IdempotencyKey, Status: OutcomeAccepted, Sequence: 1, OccurredAt: now}
	if inserted, err := box.RecordOutcome(ctx, accepted); err != nil || !inserted {
		t.Fatalf("accepted=%v %v", inserted, err)
	}
	done := accepted
	done.EventID = "out-2"
	done.Status = OutcomeSucceeded
	done.Sequence = 2
	if inserted, err := box.RecordOutcome(ctx, done); err != nil || !inserted {
		t.Fatalf("done=%v %v", inserted, err)
	}
	duplicate := done
	duplicate.EventID = "out-3"
	duplicate.Status = OutcomeFailed
	duplicate.Sequence = 3
	if _, err := box.RecordOutcome(ctx, duplicate); err == nil {
		t.Fatal("expected one-terminal constraint")
	}
	report, err := box.Report(ctx, cmd.RunID)
	if err != nil || len(report) != 1 || len(report[0].Outcomes) != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	retry := testCommand("del-1:1")
	retry.EventID = "evt-retry"
	box.Accept(ctx, "s", retry)
	late := done
	late.EventID = "out-late"
	late.Sequence = 4
	if _, err := box.RecordOutcome(ctx, late); err == nil {
		t.Fatal("expected stale retry rejection")
	}
}
