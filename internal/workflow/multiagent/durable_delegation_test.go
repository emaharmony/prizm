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
	store    DurableRunStore
	command  event.Command
	commands []event.Command
	called   bool
	err      error
}

func (d *recordingDelegationDispatcher) Dispatch(ctx context.Context, _ string, cmd event.Command) error {
	record, err := d.store.Load(ctx, cmd.RunID)
	if err != nil {
		return err
	}
	if record.Phase != CheckpointWaiting || record.Waiting == nil {
		return errors.New("delegation dispatched before exact identity checkpoint")
	}
	if record.Waiting.Kind == "delegation_outcome" &&
		(record.Waiting.CommandEventID != cmd.EventID || record.Waiting.DeliveryKey != cmd.IdempotencyKey) {
		return errors.New("delegation dispatched before exact identity checkpoint")
	}
	if record.Waiting.Kind == "delegation_join" && record.DelegationJoin == nil {
		return errors.New("delegation join dispatched before join checkpoint")
	}
	d.called = true
	d.command = cmd
	d.commands = append(d.commands, cmd)
	return d.err
}

type recordingOutcomeSource struct {
	store    DurableRunStore
	outcomes []event.Outcome
	acked    []string
}

type acknowledgementCheckpointFailureStore struct {
	DurableRunStore
	armed  bool
	failed bool
}

func (s *acknowledgementCheckpointFailureStore) Checkpoint(
	ctx context.Context,
	expectedRevision int64,
	record DurableRun,
	events []event.Event,
) (DurableRun, error) {
	if s.armed && !s.failed && record.PendingDelegationOutcomeAck == "" {
		s.failed = true
		return DurableRun{}, errInjectedCheckpoint
	}
	return s.DurableRunStore.Checkpoint(ctx, expectedRevision, record, events)
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
	if record.PendingDelegationOutcomeAck != eventID {
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

func TestDurableGraphDelegationRecoversAfterOutcomeAcknowledgedBeforeCheckpoint(t *testing.T) {
	env := newDurableTestEnvironment(t)
	store := &acknowledgementCheckpointFailureStore{DurableRunStore: env.store}
	box, err := event.NewSQLiteOutbox(filepath.Join(t.TempDir(), "delegation-outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = box.Close() })
	dispatch := &outboxDelegationDispatcher{outbox: box}
	runtime := newGraphDelegationRuntimeWithStore(t, env, store, dispatch, box, func() time.Time {
		return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	})
	request := RunRequest{RunID: "run-graph-delegation", Task: TaskReference{ID: "parent-task", Description: "ack recovery"}}
	if _, err := runtime.Run(t.Context(), request); err == nil {
		t.Fatal("Run() should wait for delegated outcome")
	}
	payload, _ := json.Marshal(delegatedRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1}})
	terminal := matchingOutcome(dispatch.command, "sqlite-terminal-after-ack", event.OutcomeSucceeded, 1, payload)
	if inserted, err := box.RecordOutcome(t.Context(), terminal); err != nil || !inserted {
		t.Fatalf("record terminal inserted=%v err=%v", inserted, err)
	}
	store.armed = true
	if _, err := runtime.Resume(t.Context(), request.RunID); !errors.Is(err, errInjectedCheckpoint) {
		t.Fatalf("Resume() before acknowledgement checkpoint err=%v", err)
	}
	stored, err := env.store.Load(t.Context(), request.RunID)
	if err != nil || stored.PendingDelegationOutcomeAck != terminal.EventID {
		t.Fatalf("stored acknowledgement=%q err=%v", stored.PendingDelegationOutcomeAck, err)
	}
	pending, err := box.PendingOutcomes(t.Context(), request.RunID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("consumed outcome pending=%v err=%v", pending, err)
	}
	state, err := runtime.Resume(t.Context(), request.RunID)
	if err != nil || state.Status != RunStatusCompleted {
		t.Fatalf("Resume() recovery state=%#v err=%v", state, err)
	}
	stored, err = env.store.Load(t.Context(), request.RunID)
	if err != nil || stored.PendingDelegationOutcomeAck != "" {
		t.Fatalf("cleared acknowledgement=%q err=%v", stored.PendingDelegationOutcomeAck, err)
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

func TestDurableGraphDelegationRetriesPendingDispatchWithSameCommand(t *testing.T) {
	env := newDurableTestEnvironment(t)
	dispatch := &recordingDelegationDispatcher{store: env.store, err: errors.New("publish interrupted")}
	source := &recordingOutcomeSource{store: env.store}
	runtime := newGraphDelegationRuntime(t, env, dispatch, source)
	request := RunRequest{RunID: "run-graph-delegation", Task: TaskReference{ID: "parent-task", Description: "retry dispatch"}}
	if _, err := runtime.Run(t.Context(), request); err == nil {
		t.Fatal("Run() should report interrupted dispatch")
	}
	stored, err := env.store.Load(t.Context(), request.RunID)
	if err != nil || stored.Waiting == nil || !stored.Waiting.DispatchPending {
		t.Fatalf("pending dispatch checkpoint=%#v err=%v", stored, err)
	}
	first := dispatch.command
	dispatch.err = nil
	_, err = runtime.Resume(t.Context(), request.RunID)
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) || len(dispatch.commands) != 2 ||
		dispatch.commands[1].EventID != first.EventID || dispatch.commands[1].IdempotencyKey != first.IdempotencyKey ||
		string(dispatch.commands[1].Payload) != string(first.Payload) || !dispatch.commands[1].Deadline.Equal(first.Deadline) {
		t.Fatalf("Resume() err=%v commands=%#v", err, dispatch.commands)
	}
	stored, err = env.store.Load(t.Context(), request.RunID)
	if err != nil || stored.Waiting.DispatchPending {
		t.Fatalf("completed dispatch checkpoint=%#v err=%v", stored, err)
	}
}

func TestDurableGraphDelegationRoutesChildProposalsToParentApprovalLifecycle(t *testing.T) {
	runtime, env, dispatch, source := newWaitingDelegationRuntime(t, "run-graph-delegation")
	payload, _ := json.Marshal(delegatedRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1,
		Proposals: []ProposalReference{{ProposalID: "proposal-child", ApprovalID: "approval-child"}}}})
	source.outcomes = []event.Outcome{matchingOutcome(dispatch.command, "terminal-proposal", event.OutcomeSucceeded, 1, payload)}

	state, err := runtime.Resume(t.Context(), "run-graph-delegation")
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) || state.Status != RunStatusPaused || len(source.acked) != 1 {
		t.Fatalf("Resume() state=%#v err=%v acknowledged=%v", state, err, source.acked)
	}
	stored, loadErr := env.store.Load(t.Context(), "run-graph-delegation")
	if loadErr != nil || stored.Phase != CheckpointWaiting || stored.Waiting == nil || stored.Waiting.Kind != "proposal_approval" ||
		stored.ApprovedTask == nil || len(stored.ApprovedTask.Proposals) != 1 ||
		stored.ApprovedTask.Proposals[0].ProposalID != "proposal-child" || stored.ApprovedTask.Proposals[0].ApprovalID != "approval-child" {
		t.Fatalf("parent approval checkpoint=%#v loadErr=%v", stored, loadErr)
	}
}

func TestDurableGraphDelegationDeadlineFailsOnceWithoutRedispatch(t *testing.T) {
	env := newDurableTestEnvironment(t)
	dispatch := &recordingDelegationDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store}
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	runtime := newGraphDelegationRuntimeWithClock(t, env, dispatch, source, func() time.Time { return now })
	request := RunRequest{RunID: "run-graph-delegation", Task: TaskReference{ID: "parent-task", Description: "deadline"}}
	if _, err := runtime.Run(t.Context(), request); err == nil {
		t.Fatal("Run() should wait for delegated outcome")
	}
	if dispatch.command.Deadline.IsZero() || !dispatch.command.Deadline.Equal(now.Add(time.Minute)) {
		t.Fatalf("command deadline=%v", dispatch.command.Deadline)
	}
	now = now.Add(time.Minute)
	state, err := runtime.Resume(t.Context(), request.RunID)
	if err == nil || state.Status != RunStatusFailed {
		t.Fatalf("Resume() state=%#v err=%v", state, err)
	}
	stored, loadErr := env.store.Load(t.Context(), request.RunID)
	if loadErr != nil || stored.Failure == nil || stored.Failure.Kind != "delegation_timeout" || len(dispatch.commands) != 1 {
		t.Fatalf("timeout checkpoint=%#v dispatches=%d loadErr=%v", stored, len(dispatch.commands), loadErr)
	}
	state, err = runtime.Resume(t.Context(), request.RunID)
	if err != nil || state.Status != RunStatusFailed || len(dispatch.commands) != 1 {
		t.Fatalf("terminal Resume() state=%#v err=%v dispatches=%d", state, err, len(dispatch.commands))
	}
}

func TestNewDurableRuntimeRequiresPositiveDelegationDeadline(t *testing.T) {
	env := newDurableTestEnvironment(t)
	graph, diagnostics, err := Compile(baseDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("compile graph: %v (%s)", err, diagnostics.Error())
	}
	for _, deadline := range []time.Duration{0, -time.Second} {
		runtime, err := NewDurableRuntime(graph, &countingRoleRunner{}, env.store, env.claimer, env.events, DurableRuntimeOptions{
			Delegation: &DurableDelegationOptions{
				Subject: "graph.roles", Dispatcher: &recordingDelegationDispatcher{store: env.store},
				Outcomes: &recordingOutcomeSource{store: env.store}, Deadline: deadline,
			},
		})
		if err == nil || runtime != nil {
			t.Fatalf("NewDurableRuntime() deadline=%v runtime=%v err=%v", deadline, runtime, err)
		}
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
	return newGraphDelegationRuntimeWithClock(t, env, dispatch, source, func() time.Time {
		return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	})
}

func newGraphDelegationRuntimeWithClock(t *testing.T, env durableTestEnvironment, dispatch DelegationDispatcher, source DelegationOutcomeSource, clock func() time.Time) *DurableRuntime {
	return newGraphDelegationRuntimeWithStore(t, env, env.store, dispatch, source, clock)
}

func newGraphDelegationRuntimeWithStore(t *testing.T, env durableTestEnvironment, store DurableRunStore, dispatch DelegationDispatcher, source DelegationOutcomeSource, clock func() time.Time) *DurableRuntime {
	t.Helper()
	graph, diagnostics, err := Compile(baseDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("compile graph: %v (%s)", err, diagnostics.Error())
	}
	runtime, err := NewDurableRuntime(graph, &countingRoleRunner{}, store, env.claimer, env.events, DurableRuntimeOptions{
		Clock: clock,
		Delegation: &DurableDelegationOptions{Subject: "graph.roles", Dispatcher: dispatch, Outcomes: source,
			Deadline: time.Minute},
	})
	if err != nil {
		t.Fatalf("new durable runtime: %v", err)
	}
	return runtime
}
