package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

type recordingDelegationDispatcher struct {
	store   DurableRunStore
	command event.Command
	called  bool
	err     error
}

func (d *recordingDelegationDispatcher) Dispatch(ctx context.Context, _ string, cmd event.Command) error {
	record, err := d.store.Load(ctx, cmd.RunID)
	if err != nil {
		return err
	}
	if record.Phase != CheckpointWaiting || record.Waiting == nil ||
		record.Waiting.CommandEventID != cmd.EventID || record.Waiting.DeliveryKey != cmd.IdempotencyKey {
		return errors.New("delegation dispatched before exact identity checkpoint")
	}
	d.called = true
	d.command = cmd
	return d.err
}

type recordingOutcomeSource struct {
	store    DurableRunStore
	outcomes []event.Outcome
	acked    []string
}

type outboxDelegationDispatcher struct {
	outbox  *event.SQLiteOutbox
	command event.Command
}

func (d *outboxDelegationDispatcher) Dispatch(ctx context.Context, subject string, cmd event.Command) error {
	if _, err := d.outbox.Accept(ctx, subject, cmd); err != nil {
		return err
	}
	d.command = cmd
	return nil
}

func (s *recordingOutcomeSource) PendingOutcomes(context.Context, string) ([]event.Outcome, error) {
	return append([]event.Outcome(nil), s.outcomes...), nil
}

func (s *recordingOutcomeSource) MarkOutcomeConsumed(ctx context.Context, eventID string) error {
	record, err := s.store.Load(ctx, "run-graph-delegation")
	if err != nil {
		return err
	}
	if record.Phase == CheckpointWaiting && record.Waiting.LastOutcomeSequence == 0 {
		return errors.New("outcome acknowledged before graph checkpoint")
	}
	s.acked = append(s.acked, eventID)
	return nil
}

func TestDurableGraphDelegationCheckpointsBeforeDispatchAndRoutesOutcome(t *testing.T) {
	env := newDurableTestEnvironment(t)
	dispatch := &recordingDelegationDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store}
	runtime := newGraphDelegationRuntime(t, env, dispatch, source)

	request := RunRequest{RunID: "run-graph-delegation", Task: TaskReference{ID: "parent-task", Description: "delegate the graph role"}}
	state, err := runtime.Run(t.Context(), request)
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) || state.Status != RunStatusPaused || !dispatch.called {
		t.Fatalf("Run() state=%#v err=%v dispatched=%v", state, err, dispatch.called)
	}
	stored, err := env.store.Load(t.Context(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	waiting := stored.Waiting
	if waiting.ChildID == "" || waiting.DelegationID == "" || waiting.DeliveryKey == "" ||
		waiting.CommandEventID != dispatch.command.EventID || waiting.CorrelationID != dispatch.command.CorrelationID {
		t.Fatalf("checkpoint identity=%#v command=%#v", waiting, dispatch.command)
	}

	accepted := matchingOutcome(dispatch.command, "accepted", event.OutcomeAccepted, 1, nil)
	resultPayload, _ := json.Marshal(delegatedRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1}})
	terminal := matchingOutcome(dispatch.command, "terminal", event.OutcomeSucceeded, 2, resultPayload)
	source.outcomes = []event.Outcome{accepted, terminal}

	state, err = runtime.Resume(t.Context(), request.RunID)
	if err != nil || state.Status != RunStatusCompleted {
		t.Fatalf("Resume() state=%#v err=%v", state, err)
	}
	if len(source.acked) != 2 || source.acked[0] != "accepted" || source.acked[1] != "terminal" {
		t.Fatalf("acknowledged=%v", source.acked)
	}
	stored, err = env.store.Load(t.Context(), request.RunID)
	if err != nil || stored.Phase != CheckpointTerminal || stored.State.LatestCompletedRole != Role("start") {
		t.Fatalf("terminal checkpoint=%#v err=%v", stored, err)
	}
}

func TestDurableGraphDelegationRejectsMismatchedOutcomeWithoutAck(t *testing.T) {
	runtime, env, dispatch, source := newWaitingDelegationRuntime(t, "run-graph-delegation")
	outcome := matchingOutcome(dispatch.command, "forged", event.OutcomeAccepted, 1, nil)
	outcome.DelegationID = "forged"
	source.outcomes = []event.Outcome{outcome}

	_, err := runtime.Resume(t.Context(), "run-graph-delegation")
	var recoveryErr *RecoveryFailedError
	if !errors.As(err, &recoveryErr) || len(source.acked) != 0 {
		t.Fatalf("Resume() err=%v acknowledged=%v", err, source.acked)
	}
	stored, loadErr := env.store.Load(t.Context(), "run-graph-delegation")
	if loadErr != nil || stored.Phase != CheckpointWaiting || stored.Waiting.LastOutcomeSequence != 0 {
		t.Fatalf("checkpoint=%#v loadErr=%v", stored, loadErr)
	}
}

func TestDurableGraphDelegationReplaysAndAcknowledgesSQLiteOutboxOutcome(t *testing.T) {
	env := newDurableTestEnvironment(t)
	box, err := event.NewSQLiteOutbox(filepath.Join(t.TempDir(), "delegation-outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = box.Close() })
	dispatch := &outboxDelegationDispatcher{outbox: box}
	runtime := newGraphDelegationRuntime(t, env, dispatch, box)
	request := RunRequest{RunID: "run-graph-delegation", Task: TaskReference{ID: "parent-task", Description: "durable outcome"}}
	if _, err := runtime.Run(t.Context(), request); err == nil {
		t.Fatal("Run() should wait for delegated outcome")
	}
	accepted := matchingOutcome(dispatch.command, "sqlite-accepted", event.OutcomeAccepted, 1, nil)
	resultPayload, _ := json.Marshal(delegatedRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1}})
	terminal := matchingOutcome(dispatch.command, "sqlite-terminal", event.OutcomeSucceeded, 2, resultPayload)
	if inserted, err := box.RecordOutcome(t.Context(), accepted); err != nil || !inserted {
		t.Fatalf("record accepted inserted=%v err=%v", inserted, err)
	}
	if inserted, err := box.RecordOutcome(t.Context(), terminal); err != nil || !inserted {
		t.Fatalf("record terminal inserted=%v err=%v", inserted, err)
	}

	state, err := runtime.Resume(t.Context(), request.RunID)
	if err != nil || state.Status != RunStatusCompleted {
		t.Fatalf("Resume() state=%#v err=%v", state, err)
	}
	pending, err := box.PendingOutcomes(t.Context(), request.RunID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending outcomes=%v err=%v", pending, err)
	}
}

func TestDurableGraphDelegationRejectsDuplicateSequenceAfterPersistingFirst(t *testing.T) {
	runtime, env, dispatch, source := newWaitingDelegationRuntime(t, "run-graph-delegation")
	first := matchingOutcome(dispatch.command, "accepted-1", event.OutcomeAccepted, 1, nil)
	duplicate := matchingOutcome(dispatch.command, "accepted-duplicate", event.OutcomeProgress, 1, nil)
	source.outcomes = []event.Outcome{first, duplicate}

	_, err := runtime.Resume(t.Context(), "run-graph-delegation")
	var recoveryErr *RecoveryFailedError
	if !errors.As(err, &recoveryErr) || len(source.acked) != 1 || source.acked[0] != first.EventID {
		t.Fatalf("Resume() err=%v acknowledged=%v", err, source.acked)
	}
	stored, loadErr := env.store.Load(t.Context(), "run-graph-delegation")
	if loadErr != nil || stored.Waiting.LastOutcomeSequence != 1 {
		t.Fatalf("checkpoint=%#v loadErr=%v", stored, loadErr)
	}
}

func newWaitingDelegationRuntime(t *testing.T, runID string) (*DurableRuntime, durableTestEnvironment, *recordingDelegationDispatcher, *recordingOutcomeSource) {
	t.Helper()
	env := newDurableTestEnvironment(t)
	dispatch := &recordingDelegationDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store}
	runtime := newGraphDelegationRuntime(t, env, dispatch, source)
	_, err := runtime.Run(t.Context(), RunRequest{RunID: runID, Task: TaskReference{ID: "parent-task", Description: "delegate"}})
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) {
		t.Fatalf("Run() err=%v", err)
	}
	return runtime, env, dispatch, source
}

func matchingOutcome(cmd event.Command, id string, status event.OutcomeStatus, sequence int64, payload []byte) event.Outcome {
	return event.Outcome{EventID: id, CommandEventID: cmd.EventID, RunID: cmd.RunID, TaskID: cmd.TaskID,
		DelegationID: cmd.DelegationID, CorrelationID: cmd.CorrelationID, CausationID: cmd.EventID,
		DeliveryKey: cmd.IdempotencyKey, Status: status, Sequence: sequence,
		OccurredAt: time.Date(2026, time.July, 23, 12, 1, 0, 0, time.UTC), Payload: payload}
}

func newGraphDelegationRuntime(t *testing.T, env durableTestEnvironment, dispatch DelegationDispatcher, source DelegationOutcomeSource) *DurableRuntime {
	t.Helper()
	graph, diagnostics, err := Compile(baseDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("compile graph: %v (%s)", err, diagnostics.Error())
	}
	runtime, err := NewDurableRuntime(graph, &countingRoleRunner{}, env.store, env.claimer, env.events, DurableRuntimeOptions{
		Clock:      func() time.Time { return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC) },
		Delegation: &DurableDelegationOptions{Subject: "graph.roles", Dispatcher: dispatch, Outcomes: source},
	})
	if err != nil {
		t.Fatalf("new durable runtime: %v", err)
	}
	return runtime
}
