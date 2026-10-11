package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

func TestDurableDelegationOutcomeRestartAndFullForward(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	box, _ := event.NewSQLiteOutbox(path)
	cmd := event.Command{EventID: event.CommandEventID("del:0"), Type: "prizm.command.delegation", RunID: "run", TaskID: "T1", DelegationID: "del", CorrelationID: "corr", IdempotencyKey: "del:0", SchemaVersion: event.CommandSchemaVersion, Payload: json.RawMessage(`{"task_id":"T1"}`)}
	box.Accept(t.Context(), "tasks", cmd)
	completion := v2.TaskCompletion{TaskID: "T1", DelegationID: "del", DeliveryKey: "del:0", Status: "completed", OutputSummary: "done", Artifacts: v2.CompletionArtifacts{FilePaths: []string{"a.go"}}}
	payload, _ := json.Marshal(completion)
	now := time.Now().UTC()
	accepted := event.Outcome{EventID: "accepted", CommandEventID: cmd.EventID, RunID: "run", TaskID: "T1", DelegationID: "del", CorrelationID: "corr", CausationID: cmd.EventID, DeliveryKey: "del:0", Status: event.OutcomeAccepted, Sequence: 1, OccurredAt: now}
	terminal := accepted
	terminal.EventID = "terminal"
	terminal.Status = event.OutcomeSucceeded
	terminal.Sequence = 2
	terminal.Payload = payload
	box.RecordOutcome(t.Context(), accepted)
	box.RecordOutcome(t.Context(), terminal)
	box.Close()
	box, _ = event.NewSQLiteOutbox(path)
	defer box.Close()
	ch := make(chan v2.ExternalEvent, 2)
	if err := forwardDurableDelegationOutcomes(t.Context(), box, "run", ch); err != nil {
		t.Fatal(err)
	}
	a := <-ch
	b := <-ch
	if a.Type != "task_accepted" || b.Type != "task_complete" {
		t.Fatalf("events=%s,%s", a.Type, b.Type)
	}
	got := b.Data["completion"].(v2.TaskCompletion)
	if got.DelegationID != "del" || got.DeliveryKey != "del:0" || len(got.Artifacts.FilePaths) != 1 {
		t.Fatalf("completion=%+v", got)
	}
	if err := a.Acknowledge(); err != nil {
		t.Fatal(err)
	}
	if err := b.Acknowledge(); err != nil {
		t.Fatal(err)
	}
	pending, _ := box.PendingOutcomes(t.Context(), "run")
	if len(pending) != 0 {
		t.Fatalf("pending=%d", len(pending))
	}
}

func TestDurableDelegationOutcomeBackpressureDoesNotDrop(t *testing.T) {
	box, _ := event.NewSQLiteOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	defer box.Close()
	cmd := event.Command{EventID: event.CommandEventID("del:0"), Type: "x", RunID: "run", TaskID: "T", DelegationID: "del", CorrelationID: "corr", IdempotencyKey: "del:0", SchemaVersion: event.CommandSchemaVersion, Payload: json.RawMessage(`{}`)}
	box.Accept(t.Context(), "s", cmd)
	out := event.Outcome{EventID: "accepted", CommandEventID: cmd.EventID, RunID: "run", TaskID: "T", DelegationID: "del", CorrelationID: "corr", CausationID: cmd.EventID, DeliveryKey: "del:0", Status: event.OutcomeAccepted, Sequence: 1, OccurredAt: time.Now().UTC()}
	box.RecordOutcome(t.Context(), out)
	ch := make(chan v2.ExternalEvent)
	done := make(chan error, 1)
	go func() { done <- forwardDurableDelegationOutcomes(context.Background(), box, "run", ch) }()
	time.Sleep(20 * time.Millisecond)
	pending, _ := box.PendingOutcomes(t.Context(), "run")
	if len(pending) != 1 {
		t.Fatalf("outcome dropped under backpressure")
	}
	evt := <-ch
	pending, _ = box.PendingOutcomes(t.Context(), "run")
	if len(pending) != 1 {
		t.Fatalf("outcome consumed before engine persistence")
	}
	if err := evt.Acknowledge(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	pending, _ = box.PendingOutcomes(t.Context(), "run")
	if len(pending) != 0 {
		t.Fatalf("outcome not consumed")
	}
}

func TestDurableDelegationOutcomeCrashAfterEnqueueReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	box, _ := event.NewSQLiteOutbox(path)
	cmd := event.Command{EventID: event.CommandEventID("d:0"), Type: "x", RunID: "run", TaskID: "T", DelegationID: "d", CorrelationID: "c", IdempotencyKey: "d:0", SchemaVersion: event.CommandSchemaVersion, Payload: json.RawMessage(`{}`)}
	box.Accept(t.Context(), "s", cmd)
	out := event.Outcome{EventID: "a", CommandEventID: cmd.EventID, RunID: "run", TaskID: "T", DelegationID: "d", CorrelationID: "c", CausationID: cmd.EventID, DeliveryKey: "d:0", Status: event.OutcomeAccepted, Sequence: 1, OccurredAt: time.Now().UTC()}
	box.RecordOutcome(t.Context(), out)
	ch := make(chan v2.ExternalEvent, 1)
	if err := forwardDurableDelegationOutcomes(t.Context(), box, "run", ch); err != nil {
		t.Fatal(err)
	}
	<-ch
	box.Close()
	box, _ = event.NewSQLiteOutbox(path)
	defer box.Close()
	ch = make(chan v2.ExternalEvent, 1)
	if err := forwardDurableDelegationOutcomes(t.Context(), box, "run", ch); err != nil {
		t.Fatal(err)
	}
	replayed := <-ch
	if replayed.Type != "task_accepted" {
		t.Fatalf("type=%s", replayed.Type)
	}
	if err := replayed.Acknowledge(); err != nil {
		t.Fatal(err)
	}
}
