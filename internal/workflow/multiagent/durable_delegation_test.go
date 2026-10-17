package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/cost"
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
	runID    string
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
	runID := s.runID
	if runID == "" {
		runID = "run-graph-delegation"
	}
	record, err := s.store.Load(ctx, runID)
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

func TestDelegatedParentRetryHookIsAttemptScopedAndSuccessfulDeliveryCompletes(t *testing.T) {
	runtime, env, dispatch, source := newWaitingDelegationRuntime(t, "run-parent-retry-hook")
	source.runID = "run-parent-retry-hook"
	record, err := env.store.Load(t.Context(), "run-parent-retry-hook")
	if err != nil || record.Waiting == nil || record.Waiting.AttemptID == "" || record.Waiting.DeliveryAttemptID == "" {
		t.Fatalf("outer delivery lacks trusted retry identity: waiting=%#v err=%v", record.Waiting, err)
	}
	initial := record.Waiting.AttemptID
	handle, toolSink, err := NewDelegatedRoleRetryHandle(t.Context(), env.store, "run-parent-retry-hook", record.Waiting.DeliveryAttemptID, initial)
	if err != nil || toolSink == nil {
		t.Fatalf("bind outer role retry hook: handle=%T sink=%v err=%v", handle, toolSink != nil, err)
	}
	if _, err := handle.ConsumeRecovery(initial, "invalid_schema"); err != nil {
		t.Fatal(err)
	}
	updated, err := env.store.Load(t.Context(), "run-parent-retry-hook")
	if err != nil || updated.Retry.RecoveryNumber != 1 || updated.Waiting.AttemptID != handle.CurrentAttempt().AttemptID || updated.Waiting.DeliveryAttemptID != record.Waiting.DeliveryAttemptID {
		t.Fatalf("retry hook lost scope or allowance: retry=%#v waiting=%#v err=%v", updated.Retry, updated.Waiting, err)
	}
	if err := handle.RecordProgress(initial, "evidence", "tool:read_file", "old"); !errors.Is(err, ErrStaleRetryAttempt) {
		t.Fatalf("stale attempt was allowed to write: %v", err)
	}
	activeAttempt := handle.CurrentAttempt().AttemptID
	payload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1}})
	cmd := dispatch.command
	source.outcomes = []event.Outcome{matchingOutcome(cmd, "parent-retry-success", event.OutcomeSucceeded, 1, payload)}
	state, err := runtime.Resume(t.Context(), "run-parent-retry-hook")
	if err != nil || state.Status != RunStatusCompleted || activeAttempt == initial {
		t.Fatalf("successful outer delivery after recovery: status=%s activeChanged=%t err=%v", state.Status, activeAttempt != initial, err)
	}
}

func TestValidateDelegatedRoleCommandBindsOuterAndChildScope(t *testing.T) {
	outerRuntime, outerEnv, outerDispatch, _ := newWaitingDelegationRuntime(t, "run-command-scope-outer")
	_ = outerRuntime
	var outer GraphRoleCommand
	if err := json.Unmarshal(outerDispatch.command.Payload, &outer); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDelegatedRoleCommand(t.Context(), outerEnv.store, outer); err != nil {
		t.Fatalf("valid outer command rejected: %v", err)
	}
	for name, mutate := range map[string]func(*GraphRoleCommand){
		"role":              func(c *GraphRoleCommand) { c.Role = RoleReviewer },
		"task":              func(c *GraphRoleCommand) { c.Task.ID = "other-task" },
		"workspace":         func(c *GraphRoleCommand) { c.WorkspaceID = "other-workspace" },
		"delivery":          func(c *GraphRoleCommand) { c.DeliveryAttemptID += ":stale" },
		"request-role":      func(c *GraphRoleCommand) { c.Request.Run.CurrentRole = RoleReviewer },
		"request-task":      func(c *GraphRoleCommand) { c.Request.Run.Task.ID = "other-task" },
		"request-workspace": func(c *GraphRoleCommand) { c.Request.Run.WorkspaceID = "other-workspace" },
		"request-execution": func(c *GraphRoleCommand) { c.Request.Run.ExecutionKey += ":other" },
	} {
		t.Run("outer/"+name, func(t *testing.T) {
			candidate := outer
			mutate(&candidate)
			if err := ValidateDelegatedRoleCommand(t.Context(), outerEnv.store, candidate); err == nil {
				t.Fatalf("scope mismatch accepted: %v", err)
			}
		})
	}

	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-command-scope-child")
	parentPayload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
	source.outcomes = []event.Outcome{matchingOutcome(dispatch.command, "parent-fanout", event.OutcomeSucceeded, 1, parentPayload)}
	if _, err := runtime.Resume(t.Context(), "run-command-scope-child"); err == nil {
		t.Fatal("expected child join to remain waiting")
	}
	var child GraphRoleCommand
	for _, command := range dispatch.commands {
		if command.JoinID != "" && command.Lane == string(FanOutImplementation) {
			if err := json.Unmarshal(command.Payload, &child); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if child.JoinID == "" {
		t.Fatal("implementation child command not dispatched")
	}
	if err := ValidateDelegatedRoleCommand(t.Context(), env.store, child); err != nil {
		t.Fatalf("valid child command rejected: %v", err)
	}
	for name, mutate := range map[string]func(*GraphRoleCommand){
		"role":              func(c *GraphRoleCommand) { c.Role = RoleReviewer },
		"task":              func(c *GraphRoleCommand) { c.Task.ID = "other-task" },
		"workspace":         func(c *GraphRoleCommand) { c.WorkspaceID = "other-workspace" },
		"delivery":          func(c *GraphRoleCommand) { c.DeliveryAttemptID += ":stale" },
		"request-role":      func(c *GraphRoleCommand) { c.Request.Run.CurrentRole = RoleReviewer },
		"request-task":      func(c *GraphRoleCommand) { c.Request.Run.Task.ID = "other-task" },
		"request-workspace": func(c *GraphRoleCommand) { c.Request.Run.WorkspaceID = "other-workspace" },
		"request-execution": func(c *GraphRoleCommand) { c.Request.Run.ExecutionKey += ":other" },
	} {
		t.Run("child/"+name, func(t *testing.T) {
			candidate := child
			mutate(&candidate)
			if err := ValidateDelegatedRoleCommand(t.Context(), env.store, candidate); err == nil {
				t.Fatalf("scope mismatch accepted: %v", err)
			}
		})
	}
}

func TestDelegatedParentTypedFailureSchedulesDurableRetryThenSuccess(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-parent-retry-routing")
	source.runID = "run-parent-retry-routing"
	now := time.Now().UTC()
	runtime.now = func() time.Time { return now }
	record, err := env.store.Load(t.Context(), "run-parent-retry-routing")
	if err != nil {
		t.Fatal(err)
	}
	record.Waiting.Deadline = now.Add(10 * time.Minute)
	if _, err := env.store.Checkpoint(t.Context(), record.Revision, record, nil); err != nil {
		t.Fatal(err)
	}
	record, _ = env.store.Load(t.Context(), "run-parent-retry-routing")
	handle, _, err := NewDelegatedRoleRetryHandle(t.Context(), env.store, "run-parent-retry-routing", record.Waiting.DeliveryAttemptID, record.Waiting.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ConsumeRecovery(record.Waiting.AttemptID, "invalid_schema"); err != nil {
		t.Fatal(err)
	}
	currentAttempt := handle.CurrentAttempt().AttemptID
	typedSchemaFailure := structuredError(record.State.CurrentRole, &json.UnmarshalTypeError{
		Value: "object", Type: reflect.TypeOf([]string{}), Field: "task_breakdown",
	})
	failure := ClassifyRoleExecutionFailure(typedSchemaFailure)
	failure.DeliveryAttemptID, failure.AttemptID = record.Waiting.DeliveryAttemptID, currentAttempt
	failure.Usage = cost.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	failurePayload, _ := json.Marshal(GraphRoleOutcome{Failure: &failure})
	failed := matchingOutcome(dispatch.command, "outer-schema-failure", event.OutcomeFailed, 1, failurePayload)
	failed.OccurredAt = now
	source.outcomes = []event.Outcome{failed}
	if _, err := runtime.Resume(t.Context(), "run-parent-retry-routing"); err == nil {
		t.Fatal("expected scheduled retry wait")
	}
	record, err = env.store.Load(t.Context(), "run-parent-retry-routing")
	if err != nil || record.Waiting == nil || !record.Waiting.DispatchPending || record.Retry.RecoveryNumber != 2 || record.Waiting.DeliveryNumber != 1 || record.PendingDelegationOutcomeAck != "" {
		t.Fatalf("typed failure was not durably scheduled and acknowledged: retry=%#v waiting=%#v ack=%s err=%v", record.Retry, record.Waiting, record.PendingDelegationOutcomeAck, err)
	}
	if !strings.Contains(record.Retry.RecoveryFeedback, "task_breakdown") || !strings.Contains(record.Retry.RecoveryFeedback, "a JSON array of strings") {
		t.Fatalf("outer retry feedback omitted safe field/type: %q", record.Retry.RecoveryFeedback)
	}
	if got := describeRecoveryCandidate(record, record.Retry.NextAt.Add(time.Second)); !got.Ready || got.WaitingKind != "delegation_outcome" {
		t.Fatalf("retry missing from recovery scanner: %#v", got)
	}
	now = record.Retry.NextAt.Add(time.Second)
	source.outcomes = nil
	if _, err := runtime.Resume(t.Context(), "run-parent-retry-routing"); err == nil {
		t.Fatal("expected second delivery to wait")
	}
	if len(dispatch.commands) != 2 {
		t.Fatalf("parent recovery delivery count=%d", len(dispatch.commands))
	}
	record, err = env.store.Load(t.Context(), "run-parent-retry-routing")
	if err != nil || record.Waiting.DispatchPending || record.Waiting.DeliveryNumber != 1 || !record.Retry.NextAt.IsZero() {
		t.Fatalf("new attempt was not dispatched with shared retry state: waiting=%#v retry=%#v err=%v", record.Waiting, record.Retry, err)
	}
	var retryCommand GraphRoleCommand
	if err := json.Unmarshal(dispatch.commands[1].Payload, &retryCommand); err != nil {
		t.Fatalf("decode replacement parent command: %v", err)
	}
	if !strings.Contains(retryCommand.RecoveryFeedback, "task_breakdown") ||
		!strings.Contains(retryCommand.RecoveryFeedback, "a JSON array of strings") {
		t.Fatalf("replacement parent worker command omitted specific safe correction: %q", retryCommand.RecoveryFeedback)
	}
	newHandle, _, err := NewDelegatedRoleRetryHandle(t.Context(), env.store, "run-parent-retry-routing", record.Waiting.DeliveryAttemptID, record.Waiting.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	successPayload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1}})
	success := matchingOutcome(dispatch.commands[1], "outer-retry-success", event.OutcomeSucceeded, 1, successPayload)
	success.OccurredAt = now
	source.outcomes = []event.Outcome{success}
	state, err := runtime.Resume(t.Context(), "run-parent-retry-routing")
	if err != nil || state.Status != RunStatusCompleted || newHandle.CurrentAttempt().AttemptID == "" {
		t.Fatalf("outer recovery did not complete: status=%s err=%v", state.Status, err)
	}
	if state.BudgetUsage.Tokens.TotalTokens != 15 {
		t.Fatalf("failed provider usage was not retained exactly once: usage=%#v", state.BudgetUsage.Tokens)
	}
}

func TestDelegatedParentCapabilityFailureIsTerminalAndPubliclyClassified(t *testing.T) {
	const runID = "run-parent-capability-terminal"
	const privateReason = "PRIVATE_CAPABILITY_REASON_CANARY"
	runtime, env, dispatch, source := newWaitingDelegationRuntime(t, runID)
	source.runID = runID
	record, err := env.store.Load(t.Context(), runID)
	if err != nil || record.Waiting == nil || record.Retry == nil {
		t.Fatalf("load waiting parent delegation: waiting=%#v retry=%#v err=%v", record.Waiting, record.Retry, err)
	}
	failure := ClassifyRoleExecutionFailure(&GovernanceError{Kind: "capability", Reason: privateReason})
	failure.DeliveryAttemptID, failure.AttemptID = record.Waiting.DeliveryAttemptID, record.Waiting.AttemptID
	payload, err := json.Marshal(GraphRoleOutcome{Failure: &failure})
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = []event.Outcome{matchingOutcome(dispatch.command, "parent-capability-failure", event.OutcomeFailed, 1, payload)}
	state, resumeErr := runtime.Resume(t.Context(), runID)
	if resumeErr == nil || state.Status != RunStatusFailed {
		t.Fatalf("capability failure status=%s err=%v", state.Status, resumeErr)
	}
	terminal, err := env.store.Load(t.Context(), runID)
	if err != nil || terminal.Phase != CheckpointTerminal || terminal.Waiting != nil || terminal.Failure == nil {
		t.Fatalf("terminal record=%#v err=%v", terminal, err)
	}
	if record.Retry.RecoveryNumber != 0 || (terminal.Retry != nil && (terminal.Retry.RecoveryNumber != 0 || !terminal.Retry.NextAt.IsZero())) || len(dispatch.commands) != 1 ||
		!strings.Contains(terminal.Failure.Message, "governance/capability") {
		t.Fatalf("failure was not classified terminally: retry=%#v commands=%d failure=%#v", terminal.Retry, len(dispatch.commands), terminal.Failure)
	}
	retries, err := env.events.Query(t.Context(), event.EventFilter{RunID: runID, Type: event.EventMultiAgentRetryScheduled})
	if err != nil || len(retries) != 0 {
		t.Fatalf("unexpected retry events=%d err=%v", len(retries), err)
	}
	serialized, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), privateReason) {
		t.Fatal("private governance reason leaked into parent terminal report")
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

func TestDurableGraphDelegationConsumesTerminalBeforeExpiredPendingDispatch(t *testing.T) {
	env := newDurableTestEnvironment(t)
	dispatch := &recordingDelegationDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store}
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	runtime := newGraphDelegationRuntimeWithClock(t, env, dispatch, source, func() time.Time { return now })
	request := RunRequest{RunID: "run-graph-delegation", Task: TaskReference{ID: "parent-task", Description: "terminal first"}}
	if _, err := runtime.Run(t.Context(), request); err == nil {
		t.Fatal("Run() should wait for delegated outcome")
	}
	payload, err := json.Marshal(delegatedRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1}})
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = []event.Outcome{
		matchingOutcome(dispatch.command, "accepted-before-pending-dispatch", event.OutcomeAccepted, 1, nil),
		matchingOutcome(dispatch.command, "terminal-before-pending-dispatch", event.OutcomeSucceeded, 2, payload),
	}
	stored, err := env.store.Load(t.Context(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Waiting.DispatchPending = true // model a crash before the dispatch-complete checkpoint.
	if _, err := env.store.Checkpoint(t.Context(), stored.Revision, stored, nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	state, err := runtime.Resume(t.Context(), request.RunID)
	if err != nil || state.Status != RunStatusCompleted {
		t.Fatalf("Resume() state=%#v err=%v", state, err)
	}
	if len(dispatch.commands) != 1 {
		t.Fatalf("terminal outcome must prevent redispatch, commands=%d", len(dispatch.commands))
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
		DelegationID: cmd.DelegationID, JoinID: cmd.JoinID, Lane: cmd.Lane, CorrelationID: cmd.CorrelationID, CausationID: cmd.EventID,
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
