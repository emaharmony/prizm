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
	report, err := box.Report(t.Context(), cmd.RunID)
	if err != nil || len(report) != 1 || len(report[0].Outcomes) != 1 || report[0].Outcomes[0].Status != OutcomeTimedOut {
		t.Fatalf("report=%+v err=%v", report, err)
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

func TestMarkOutcomeConsumedIsIdempotentButRejectsUnknownOutcome(t *testing.T) {
	box, err := NewSQLiteOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	cmd := testCommand("ack-idempotent")
	if _, err := box.Accept(t.Context(), "s", cmd); err != nil {
		t.Fatal(err)
	}
	outcome := Outcome{EventID: "ack-idempotent-outcome", CommandEventID: cmd.EventID, RunID: cmd.RunID,
		TaskID: cmd.TaskID, DelegationID: cmd.DelegationID, CorrelationID: cmd.CorrelationID,
		CausationID: cmd.EventID, DeliveryKey: cmd.IdempotencyKey, Status: OutcomeAccepted,
		Sequence: 1, OccurredAt: time.Now().UTC()}
	if inserted, err := box.RecordOutcome(t.Context(), outcome); err != nil || !inserted {
		t.Fatalf("record outcome inserted=%v err=%v", inserted, err)
	}
	if err := box.MarkOutcomeConsumed(t.Context(), outcome.EventID); err != nil {
		t.Fatalf("first acknowledgement: %v", err)
	}
	if err := box.MarkOutcomeConsumed(t.Context(), outcome.EventID); err != nil {
		t.Fatalf("duplicate acknowledgement: %v", err)
	}
	if err := box.MarkOutcomeConsumed(t.Context(), "unknown-outcome"); err == nil {
		t.Fatal("unknown outcome acknowledgement succeeded")
	}
}

func TestRecordOutcomeRejectsMismatchedIdentityAndProgressBeforeAccepted(t *testing.T) {
	box, _ := NewSQLiteOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	defer box.Close()
	cmd := testCommand("bound")
	box.Accept(t.Context(), "s", cmd)
	base := Outcome{EventID: "out", CommandEventID: cmd.EventID, RunID: cmd.RunID, TaskID: cmd.TaskID, DelegationID: cmd.DelegationID, CorrelationID: cmd.CorrelationID, CausationID: cmd.EventID, DeliveryKey: cmd.IdempotencyKey, Status: OutcomeProgress, Sequence: 2, OccurredAt: time.Now().UTC()}
	if _, err := box.RecordOutcome(t.Context(), base); err == nil {
		t.Fatal("progress before accepted must fail")
	}
	for name, mutate := range map[string]func(*Outcome){"run": func(o *Outcome) { o.RunID = "forged" }, "task": func(o *Outcome) { o.TaskID = "forged" }, "command": func(o *Outcome) { o.CommandEventID = "forged" }, "correlation": func(o *Outcome) { o.CorrelationID = "forged" }} {
		t.Run(name, func(t *testing.T) {
			out := base
			out.EventID = "out-" + name
			out.Status = OutcomeAccepted
			out.Sequence = 1
			mutate(&out)
			if _, err := box.RecordOutcome(t.Context(), out); err == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
}

func TestRecordOutcomeDuplicateEventIDMustMatchCanonicalContent(t *testing.T) {
	box, _ := NewSQLiteOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	defer box.Close()
	cmd := testCommand("dup-outcome")
	box.Accept(t.Context(), "s", cmd)
	out := Outcome{EventID: "same", CommandEventID: cmd.EventID, RunID: cmd.RunID, TaskID: cmd.TaskID, DelegationID: cmd.DelegationID, CorrelationID: cmd.CorrelationID, CausationID: cmd.EventID, DeliveryKey: cmd.IdempotencyKey, Status: OutcomeAccepted, Sequence: 1, OccurredAt: time.Now().UTC()}
	if ok, err := box.RecordOutcome(t.Context(), out); err != nil || !ok {
		t.Fatalf("first=%v %v", ok, err)
	}
	if ok, err := box.RecordOutcome(t.Context(), out); err != nil || ok {
		t.Fatalf("same duplicate=%v %v", ok, err)
	}
	changed := out
	changed.Status = OutcomeProgress
	changed.Sequence = 2
	if _, err := box.RecordOutcome(t.Context(), changed); err == nil {
		t.Fatal("changed duplicate accepted")
	}
}

func TestTerminalFailureUsesExistingWorkerTerminalOutcome(t *testing.T) {
	box, err := NewSQLiteOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	cmd := testCommand("terminal-worker-won")
	if _, err := box.Accept(t.Context(), "s", cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := box.Claim(t.Context(), time.Minute); err != nil {
		t.Fatal(err)
	}
	workerTerminal := Outcome{EventID: outcomeEventID(cmd.IdempotencyKey, OutcomeFailed), CommandEventID: cmd.EventID, RunID: cmd.RunID, TaskID: cmd.TaskID, DelegationID: cmd.DelegationID, CorrelationID: cmd.CorrelationID, CausationID: cmd.EventID, DeliveryKey: cmd.IdempotencyKey, Status: OutcomeFailed, Sequence: 2, OccurredAt: time.Now().UTC(), Payload: json.RawMessage(`{"message":"worker failed"}`)}
	if inserted, err := box.RecordOutcome(t.Context(), workerTerminal); err != nil || !inserted {
		t.Fatalf("worker terminal inserted=%v err=%v", inserted, err)
	}
	if err := box.terminalFailure(t.Context(), cmd.IdempotencyKey, "publisher flush failed", OutcomeFailed); err != nil {
		t.Fatalf("terminal failure must preserve existing worker fact: %v", err)
	}
	trace, err := box.Report(t.Context(), cmd.RunID)
	if err != nil || len(trace) != 1 || len(trace[0].Outcomes) != 1 || trace[0].Outcomes[0].EventID != workerTerminal.EventID {
		t.Fatalf("trace=%+v err=%v", trace, err)
	}
	if trace[0].State != DeliveryDelivered {
		t.Fatalf("delivery state=%s want %s", trace[0].State, DeliveryDelivered)
	}
}

func TestTerminalFailureKeepsWorkerAcceptanceRecoverable(t *testing.T) {
	box, err := NewSQLiteOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	cmd := testCommand("accepted-worker-won")
	if _, err := box.Accept(t.Context(), "s", cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := box.Claim(t.Context(), time.Minute); err != nil {
		t.Fatal(err)
	}
	accepted := Outcome{EventID: outcomeEventID(cmd.IdempotencyKey, OutcomeAccepted), CommandEventID: cmd.EventID, RunID: cmd.RunID, TaskID: cmd.TaskID, DelegationID: cmd.DelegationID, CorrelationID: cmd.CorrelationID, CausationID: cmd.EventID, DeliveryKey: cmd.IdempotencyKey, Status: OutcomeAccepted, Sequence: 1, OccurredAt: time.Now().UTC()}
	if inserted, err := box.RecordOutcome(t.Context(), accepted); err != nil || !inserted {
		t.Fatalf("worker acceptance inserted=%v err=%v", inserted, err)
	}
	if err := box.terminalFailure(t.Context(), cmd.IdempotencyKey, "publisher flush failed", OutcomeFailed); err != nil {
		t.Fatalf("publisher failure must preserve worker recovery path: %v", err)
	}
	trace, err := box.Report(t.Context(), cmd.RunID)
	if err != nil || len(trace) != 1 || len(trace[0].Outcomes) != 1 || trace[0].Outcomes[0].Status != OutcomeAccepted {
		t.Fatalf("trace=%+v err=%v", trace, err)
	}
	if trace[0].State != DeliveryPending {
		t.Fatalf("delivery state=%s want %s", trace[0].State, DeliveryPending)
	}
	if retry, err := box.Claim(t.Context(), time.Minute); err != nil || retry == nil || retry.Command.IdempotencyKey != cmd.IdempotencyKey {
		t.Fatalf("accepted command must be redeliverable after publisher failure: retry=%+v err=%v", retry, err)
	}
}
