package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestGraphRoleWorkerEmbeddedNATSExecutesThreeFanoutLanesConcurrently(t *testing.T) {
	runDir := t.TempDir()
	base := graphWorkerTestCommand("run-fanout-nats", time.Now().Add(time.Minute))
	commands := []multiagent.GraphRoleCommand{
		fanoutWorkerCommand(base, multiagent.FanOutResearch, multiagent.RolePlanner),
		fanoutWorkerCommand(base, multiagent.FanOutImplementation, multiagent.RoleDeveloper),
		fanoutWorkerCommand(base, multiagent.FanOutReview, multiagent.RoleReviewer),
	}
	for _, command := range commands {
		prepareGraphWorkerRun(t, runDir, command)
	}
	url, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	nc, err := bus.ConnectToBus(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var active, maxActive atomic.Int32
	runner := func(ctx context.Context, command multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error) {
		current := active.Add(1)
		for {
			old := maxActive.Load()
			if current <= old || maxActive.CompareAndSwap(old, current) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return multiagent.RoleRunResult{}, ctx.Err()
		}
		active.Add(-1)
		return multiagent.RoleRunResult{Outcome: multiagent.OutcomeImplementationReady, LocalIterations: 1}, nil
	}
	worker, err := startGraphRoleWorkerWithRunner(nc, runDir, "", nil, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	for _, command := range commands {
		payload, _ := json.Marshal(command)
		if err := nc.Publish(graphRoleDelegationSubject, payload); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("fanout lane did not start")
		}
	}
	if maxActive.Load() < 2 {
		t.Fatalf("fanout lanes did not overlap; max active=%d", maxActive.Load())
	}
	close(release)
	for _, command := range commands {
		outcome := waitForGraphLedgerTerminal(t, runDir, command)
		if outcome.JoinID != string(command.JoinID) || outcome.Lane != string(command.Lane) {
			t.Fatalf("trusted outcome identity mismatch: %#v", outcome)
		}
	}
}

func TestGraphRoleWorkerFanoutExpiryClosesAcceptedChildrenWithoutDuplicates(t *testing.T) {
	runDir := t.TempDir()
	base := graphWorkerTestCommand("run-fanout-expiry", time.Now().Add(-time.Second))
	commands := []multiagent.GraphRoleCommand{
		fanoutWorkerCommand(base, multiagent.FanOutResearch, multiagent.RolePlanner),
		fanoutWorkerCommand(base, multiagent.FanOutImplementation, multiagent.RoleDeveloper),
		fanoutWorkerCommand(base, multiagent.FanOutReview, multiagent.RoleReviewer),
	}
	worker := &graphRoleWorker{runDir: runDir, workerID: "crashed-worker", publish: func(context.Context, string, []byte) error { return nil }}
	for _, command := range commands {
		prepareGraphWorkerRun(t, runDir, command)
		path := filepath.Join(runDir, command.RunID, "multiagent.db")
		if action, _, err := worker.claimExecution(t.Context(), path, command); err != nil || action != "execute" {
			t.Fatalf("claim %s action=%q err=%v", command.DeliveryKey, action, err)
		}
		if err := worker.persistAcceptedAndPublish(t.Context(), path, command); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate one child having reached a terminal fact before the worker crash.
	path := filepath.Join(runDir, base.RunID, "multiagent.db")
	if err := worker.persistAndPublishTerminal(t.Context(), path, commands[0], event.OutcomeSucceeded, multiagent.GraphRoleOutcome{Result: multiagent.RoleRunResult{Outcome: multiagent.OutcomePlanReady, LocalIterations: 1}}); err != nil {
		t.Fatal(err)
	}
	for _, command := range commands {
		if err := worker.recoverExpiredOutcome(t.Context(), command.RunID, command.DeliveryKey); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range commands {
		trace := waitForGraphTerminal(t, runDir, command.RunID, command.DeliveryKey)
		if got := trace.Outcomes[len(trace.Outcomes)-1].Status; !got.Terminal() {
			t.Fatalf("child %s terminal=%s", command.DeliveryKey, got)
		}
		if err := worker.recoverExpiredOutcome(t.Context(), command.RunID, command.DeliveryKey); err != nil {
			t.Fatal(err)
		}
		trace = waitForGraphTerminal(t, runDir, command.RunID, command.DeliveryKey)
		if len(trace.Outcomes) != 2 {
			t.Fatalf("child %s outcomes=%d want accepted plus one terminal", command.DeliveryKey, len(trace.Outcomes))
		}
	}
}

func TestGraphRoleWorkerEmbeddedNATSRecoversPersistedFanoutJoin(t *testing.T) {
	runDir := t.TempDir()
	runID := "run-integrated-fanout-expiry"
	workspaceID := "workspace-integrated-fanout"
	dir := filepath.Join(runDir, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeReferenceManifest(runDir, referenceWorkflowManifest{
		SchemaVersion:    referenceManifestSchemaVersion,
		RunID:            runID,
		WorkflowID:       "test",
		WorkflowVersion:  1,
		WorkspaceID:      workspaceID,
		WorkspaceCleaned: true,
	}); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "multiagent.db")
	store, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	box, err := event.NewSQLiteOutbox(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	url, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	nc, err := bus.ConnectToBus(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC()
	var clock atomic.Value
	clock.Store(start)
	dispatcher := graphDelegationOutbox{SQLiteOutbox: box, publisher: graphWorkerPublisherFunc(func(_ context.Context, subject string, payload []byte) error {
		if err := nc.Publish(subject, payload); err != nil {
			return err
		}
		return nc.Flush()
	})}
	runtime, err := multiagent.NewDurableRuntime(graph, graphWorkerRoleRunner(func(context.Context, multiagent.RoleRunRequest) (multiagent.RoleRunResult, error) {
		return multiagent.RoleRunResult{}, errors.New("local role runner must not execute delegated roles")
	}), store, multiagent.FileRunClaimer{Root: runDir}, events, multiagent.DurableRuntimeOptions{
		Clock: func() time.Time { return clock.Load().(time.Time) },
		Delegation: &multiagent.DurableDelegationOptions{
			Subject: graphRoleDelegationSubject, Dispatcher: dispatcher, Outcomes: box,
			Deadline: 10 * time.Second, RequireWorkspaceID: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	acceptedChildren := make(chan multiagent.FanOutLane, 2)
	release := make(chan struct{})
	var childExecutions atomic.Int32
	worker, err := startGraphRoleWorkerWithRunner(nc, runDir, "", func(ctx context.Context, gotRunID string) error {
		_, resumeErr := runtime.Resume(ctx, gotRunID)
		return resumeErr
	}, func(ctx context.Context, command multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error) {
		if command.Lane == "" {
			return multiagent.RoleRunResult{Outcome: multiagent.OutcomePlanReady, LocalIterations: 1, FanOut: &multiagent.FanOutPlan{Tasks: []multiagent.FanOutTask{
				{Lane: multiagent.FanOutResearch, Role: multiagent.RolePlanner, Description: "research recovery"},
				{Lane: multiagent.FanOutImplementation, Role: multiagent.RoleDeveloper, Description: "implement recovery"},
				{Lane: multiagent.FanOutReview, Role: multiagent.RoleReviewer, Description: "review recovery"},
			}}}, nil
		}
		if childExecutions.Add(1) == 1 {
			return multiagent.RoleRunResult{Outcome: multiagent.OutcomePlanReady, LocalIterations: 1}, nil
		}
		acceptedChildren <- command.Lane
		<-release // Simulate a worker crash after its durable accepted fact.
		return multiagent.RoleRunResult{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	defer close(release)

	state, runErr := runtime.Run(t.Context(), multiagent.RunRequest{
		RunID: runID, Task: multiagent.TaskReference{ID: "parent-task", Description: "recover three delegated children"}, WorkspaceID: workspaceID,
	})
	var waitingErr *multiagent.RunWaitingError
	if !errors.As(runErr, &waitingErr) || state.Status != multiagent.RunStatusPaused {
		t.Fatalf("Run() state=%#v err=%v", state, runErr)
	}
	initial, err := box.Report(t.Context(), runID)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial command report=%+v err=%v", initial, err)
	}
	waitForGraphTerminal(t, runDir, runID, initial[0].Command.IdempotencyKey)
	state, runErr = runtime.Resume(t.Context(), runID)
	if !errors.As(runErr, &waitingErr) || state.Status != multiagent.RunStatusPaused {
		t.Fatalf("parent Resume() state=%#v err=%v", state, runErr)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-acceptedChildren:
		case <-time.After(3 * time.Second):
			record, _ := store.Load(t.Context(), runID)
			traces, _ := box.Report(t.Context(), runID)
			t.Fatalf("fan-out child did not reach accepted-only execution; waiting=%#v join=%#v traces=%+v", record.Waiting, record.DelegationJoin, traces)
		}
	}

	var waiting multiagent.DurableRun
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		waiting, err = store.Load(t.Context(), runID)
		if err == nil && waiting.Waiting != nil && waiting.Waiting.Kind == "delegation_join" && waiting.DelegationJoin != nil && len(waiting.DelegationJoin.Children) == 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if waiting.DelegationJoin == nil || len(waiting.DelegationJoin.Children) != 3 {
		t.Fatalf("persisted delegation join=%#v err=%v", waiting.DelegationJoin, err)
	}

	clock.Store(start.Add(11 * time.Second))
	worker.wakeExpired(t.Context(), start.Add(11*time.Second))
	deadline = time.Now().Add(3 * time.Second)
	var terminal multiagent.DurableRun
	for time.Now().Before(deadline) {
		terminal, err = store.Load(t.Context(), runID)
		if err == nil && terminal.State.Status == multiagent.RunStatusFailed && terminal.Failure != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || terminal.State.Status != multiagent.RunStatusFailed || terminal.Failure == nil {
		t.Fatalf("terminal run=%#v err=%v", terminal, err)
	}

	traces, err := box.Report(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	children := make(map[string]event.CommandTrace)
	for _, trace := range traces {
		if trace.Command.JoinID != "" {
			children[trace.Command.Lane] = trace
		}
	}
	if len(children) != 3 {
		t.Fatalf("child traces=%d want 3; report=%+v", len(children), traces)
	}
	stable := make(map[string][]string, 3)
	for lane, trace := range children {
		if len(trace.Outcomes) != 2 || trace.Outcomes[0].Status != event.OutcomeAccepted || !trace.Outcomes[1].Status.Terminal() {
			t.Fatalf("lane %s outcomes=%+v", lane, trace.Outcomes)
		}
		if trace.Outcomes[1].DeliveryKey != trace.Command.IdempotencyKey || trace.Outcomes[1].JoinID != trace.Command.JoinID || trace.Outcomes[1].Lane != trace.Command.Lane {
			t.Fatalf("lane %s terminal identity=%+v command=%+v", lane, trace.Outcomes[1], trace.Command)
		}
		stable[lane] = []string{trace.Outcomes[0].EventID, trace.Outcomes[1].EventID}
	}
	succeeded, timedOut := 0, 0
	for _, trace := range children {
		switch trace.Outcomes[1].Status {
		case event.OutcomeSucceeded:
			succeeded++
		case event.OutcomeTimedOut:
			timedOut++
		}
	}
	if succeeded != 1 || timedOut != 2 {
		t.Fatalf("mixed child terminals=%+v", children)
	}

	allEvents, err := events.Query(t.Context(), event.EventFilter{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	report := multiagent.BuildReferenceRunReport(multiagent.ReferenceWorkflowInput{Objective: "recover three delegated children"}, terminal.State, allEvents)
	if len(report.FanOut) != 3 {
		t.Fatalf("terminal fan-out report=%+v", report.FanOut)
	}

	worker.wakeExpired(t.Context(), start.Add(12*time.Second))
	replayed, err := box.Report(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, trace := range replayed {
		if trace.Command.JoinID == "" {
			continue
		}
		ids := stable[trace.Command.Lane]
		if len(trace.Outcomes) != 2 || len(ids) != 2 || trace.Outcomes[0].EventID != ids[0] || trace.Outcomes[1].EventID != ids[1] {
			t.Fatalf("lane %s replay changed trusted facts: %+v", trace.Command.Lane, trace.Outcomes)
		}
	}
}

func TestGraphRoleWorkerExpiryClosesUnclaimedCommand(t *testing.T) {
	runDir := t.TempDir()
	command := graphWorkerTestCommand("run-unclaimed-expiry", time.Now().Add(-time.Second))
	prepareGraphWorkerRun(t, runDir, command)
	worker := &graphRoleWorker{runDir: runDir, workerID: "deadline-scanner"}
	if err := worker.recoverExpiredOutcome(t.Context(), command.RunID, command.DeliveryKey); err != nil {
		t.Fatal(err)
	}
	trace := waitForGraphTerminal(t, runDir, command.RunID, command.DeliveryKey)
	if len(trace.Outcomes) != 1 || trace.Outcomes[0].Status != event.OutcomeTimedOut || trace.Outcomes[0].Sequence != 1 {
		t.Fatalf("unclaimed expiry trace=%+v", trace)
	}
}

func waitForGraphLedgerTerminal(t *testing.T, runDir string, command multiagent.GraphRoleCommand) event.Outcome {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		db, err := openGraphRoleLedger(t.Context(), filepath.Join(runDir, command.RunID, "multiagent.db"))
		if err == nil {
			var state string
			var encoded []byte
			err = db.QueryRowContext(t.Context(), `SELECT state,outcome_json FROM graph_role_executions WHERE delivery_key=?`, command.DeliveryKey).Scan(&state, &encoded)
			_ = db.Close()
			if err == nil && state == "terminal" {
				var outcome event.Outcome
				if json.Unmarshal(encoded, &outcome) == nil && outcome.Status.Terminal() {
					return outcome
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for graph ledger terminal outcome")
	return event.Outcome{}
}

func TestGraphRoleWorkerEmbeddedNATSDeduplicatesAndReplaysAfterRestart(t *testing.T) {
	runDir := t.TempDir()
	runID := "run-graph-worker"
	command := graphWorkerTestCommand(runID, time.Now().Add(time.Minute))
	prepareGraphWorkerRun(t, runDir, command)

	url, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	nc, err := bus.ConnectToBus(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	var executions atomic.Int32
	runner := func(context.Context, multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error) {
		executions.Add(1)
		return multiagent.RoleRunResult{Outcome: multiagent.OutcomePlanReady, LocalIterations: 1}, nil
	}
	worker, err := startGraphRoleWorkerWithRunner(nc, runDir, "", nil, runner)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(command)
	if err := nc.Publish(graphRoleDelegationSubject, payload); err != nil {
		t.Fatal(err)
	}
	if err := nc.Publish(graphRoleDelegationSubject, payload); err != nil {
		t.Fatal(err)
	}
	waitForGraphTerminal(t, runDir, runID, command.DeliveryKey)
	if got := executions.Load(); got != 1 {
		t.Fatalf("executions=%d want 1", got)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := startGraphRoleWorkerWithRunner(nc, runDir, "", nil, func(context.Context, multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error) {
		executions.Add(1)
		return multiagent.RoleRunResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := nc.Publish(graphRoleDelegationSubject, payload); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := executions.Load(); got != 1 {
		t.Fatalf("restart replay executed role again: %d", got)
	}
}

func TestGraphRoleWorkerEmbeddedNATSPreservesWorkerTerminalDuringPublisherFailure(t *testing.T) {
	runDir := t.TempDir()
	command := graphWorkerTestCommand("run-terminal-before-publisher-failure", time.Now().Add(time.Minute))
	prepareGraphWorkerRun(t, runDir, command)
	url, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	nc, err := bus.ConnectToBus(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	worker, err := startGraphRoleWorkerWithRunner(nc, runDir, "", nil, func(context.Context, multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error) {
		return multiagent.RoleRunResult{}, errors.New("worker terminal evidence")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	box, err := event.NewSQLiteOutbox(filepath.Join(runDir, command.RunID, "multiagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	publisher := graphWorkerPublisherFunc(func(_ context.Context, subject string, payload []byte) error {
		if err := nc.Publish(subject, payload); err != nil {
			return err
		}
		if err := nc.Flush(); err != nil {
			return err
		}
		trace := waitForGraphTerminal(t, runDir, command.RunID, command.DeliveryKey)
		if got := trace.Outcomes[len(trace.Outcomes)-1].Status; got != event.OutcomeFailed {
			return fmt.Errorf("worker terminal status=%s want failed", got)
		}
		time.Sleep(25 * time.Millisecond)
		return errors.New("publisher flush failed after worker terminal")
	})
	dispatcher := event.Dispatcher{Outbox: box, Publisher: publisher, Lease: time.Minute, MaxAttempts: 1}
	if processed, err := dispatcher.DispatchOne(t.Context()); !processed || err != nil {
		t.Fatalf("dispatch processed=%v err=%v", processed, err)
	}
	traces, err := box.Report(t.Context(), command.RunID)
	if err != nil || len(traces) != 1 || len(traces[0].Outcomes) != 2 || traces[0].Outcomes[1].Status != event.OutcomeFailed {
		t.Fatalf("trace=%+v err=%v", traces, err)
	}
	if traces[0].State != event.DeliveryDelivered {
		t.Fatalf("delivery state=%s want %s", traces[0].State, event.DeliveryDelivered)
	}
}

func TestGraphRoleWorkerRestartDuringExecutionFailsClosed(t *testing.T) {
	runDir := t.TempDir()
	command := graphWorkerTestCommand("run-ambiguous", time.Now().Add(time.Minute))
	prepareGraphWorkerRun(t, runDir, command)
	url, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	nc, err := bus.ConnectToBus(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	first := &graphRoleWorker{workerID: "lost-worker"}
	path := filepath.Join(runDir, command.RunID, "multiagent.db")
	claim, _, err := first.claimExecution(t.Context(), path, command)
	if err != nil || claim != "execute" {
		t.Fatalf("claim=%q err=%v", claim, err)
	}
	worker, err := startGraphRoleWorkerWithRunner(nc, runDir, "", nil, func(context.Context, multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error) {
		t.Fatal("ambiguous execution must not be repeated")
		return multiagent.RoleRunResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	payload, _ := json.Marshal(command)
	if err := nc.Publish(graphRoleDelegationSubject, payload); err != nil {
		t.Fatal(err)
	}
	trace := waitForGraphTerminal(t, runDir, command.RunID, command.DeliveryKey)
	if trace.Outcomes[len(trace.Outcomes)-1].Status != event.OutcomeFailed {
		t.Fatalf("terminal=%s want failed", trace.Outcomes[len(trace.Outcomes)-1].Status)
	}
}

func TestGraphRoleWorkerRejectsForgedOutcomeBeforeTrustedLedgerCommit(t *testing.T) {
	runDir := t.TempDir()
	command := graphWorkerTestCommand("run-forged-outcome", time.Now().Add(time.Minute))
	prepareGraphWorkerRun(t, runDir, command)
	url, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	nc, err := bus.ConnectToBus(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	release := make(chan struct{})
	worker, err := startGraphRoleWorkerWithRunner(nc, runDir, "", nil, func(context.Context, multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error) {
		<-release
		return multiagent.RoleRunResult{Outcome: multiagent.OutcomePlanReady, LocalIterations: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	payload, _ := json.Marshal(command)
	if err := nc.Publish(graphRoleDelegationSubject, payload); err != nil {
		t.Fatal(err)
	}
	waitForGraphStatus(t, runDir, command.RunID, event.OutcomeAccepted)
	forged := event.Outcome{EventID: event.CommandEventID(command.DeliveryKey + ":forged"), CommandEventID: command.CommandEventID,
		RunID: command.RunID, TaskID: command.ChildID, DelegationID: command.DelegationID, CorrelationID: command.CorrelationID,
		CausationID: command.CommandEventID, DeliveryKey: command.DeliveryKey, Status: event.OutcomeSucceeded,
		Sequence: 2, OccurredAt: time.Now().UTC(), Payload: json.RawMessage(`{"result":{"Outcome":"plan_ready","LocalIterations":1}}`)}
	forgedBytes, _ := json.Marshal(forged)
	if err := nc.Publish(graphRoleOutcomeSubject, forgedBytes); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	box, _ := event.NewSQLiteOutbox(filepath.Join(runDir, command.RunID, "multiagent.db"))
	traces, _ := box.Report(t.Context(), command.RunID)
	box.Close()
	if len(traces) != 1 || len(traces[0].Outcomes) != 1 || traces[0].Outcomes[0].Status != event.OutcomeAccepted {
		t.Fatalf("forged outcome entered durable report: %#v", traces)
	}
	close(release)
	waitForGraphTerminal(t, runDir, command.RunID, command.DeliveryKey)
}

func TestGraphRoleWorkerDurablyRejectsMalformedCanonicalCommand(t *testing.T) {
	runDir := t.TempDir()
	command := graphWorkerTestCommand("run-malformed-command", time.Now().Add(time.Minute))
	command.Request.Run.WorkspaceID = "forged-workspace"
	prepareGraphWorkerRun(t, runDir, command)
	url, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	nc, err := bus.ConnectToBus(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	worker, err := startGraphRoleWorkerWithRunner(nc, runDir, "", nil, func(context.Context, multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error) {
		t.Fatal("malformed command executed")
		return multiagent.RoleRunResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	payload, _ := json.Marshal(command)
	if err := nc.Publish(graphRoleDelegationSubject, payload); err != nil {
		t.Fatal(err)
	}
	trace := waitForGraphTerminal(t, runDir, command.RunID, command.DeliveryKey)
	if got := trace.Outcomes[len(trace.Outcomes)-1].Status; got != event.OutcomeRejected {
		t.Fatalf("terminal=%s want rejected", got)
	}
}

func TestGraphRoleWorkerAutonomouslyWakesExpiredDelegation(t *testing.T) {
	runDir := t.TempDir()
	runID := "run-deadline-wake"
	dir := filepath.Join(runDir, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "multiagent.db")
	store, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	box, err := event.NewSQLiteOutbox(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := multiagent.NewDurableRuntime(graph, graphWorkerRoleRunner(func(context.Context, multiagent.RoleRunRequest) (multiagent.RoleRunResult, error) {
		return multiagent.RoleRunResult{}, nil
	}), store, multiagent.FileRunClaimer{Root: runDir}, events, multiagent.DurableRuntimeOptions{Delegation: &multiagent.DurableDelegationOptions{
		Subject: graphRoleDelegationSubject, Dispatcher: graphWorkerOutboxDispatcher{box}, Outcomes: box, Deadline: 20 * time.Millisecond,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(t.Context(), multiagent.RunRequest{RunID: runID, Task: multiagent.TaskReference{ID: "task", Description: "wake"}}); err == nil {
		t.Fatal("run should pause for delegated outcome")
	}
	time.Sleep(30 * time.Millisecond)
	woke := make(chan string, 1)
	worker := &graphRoleWorker{runDir: runDir, resume: func(_ context.Context, got string) error { woke <- got; return nil }}
	worker.wakeExpired(t.Context(), time.Now().UTC())
	select {
	case got := <-woke:
		if got != runID {
			t.Fatalf("woke run %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expired delegation was not woken")
	}
}

func TestGraphRoleWorkerExpiryTerminatesAcceptedWorkerCrash(t *testing.T) {
	runDir := t.TempDir()
	runID := "run-accepted-worker-crash"
	dir := filepath.Join(runDir, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "multiagent.db")
	store, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	box, err := event.NewSQLiteOutbox(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := multiagent.NewDurableRuntime(graph, graphWorkerRoleRunner(func(context.Context, multiagent.RoleRunRequest) (multiagent.RoleRunResult, error) {
		return multiagent.RoleRunResult{}, nil
	}), store, multiagent.FileRunClaimer{Root: runDir}, events, multiagent.DurableRuntimeOptions{Delegation: &multiagent.DurableDelegationOptions{
		Subject: graphRoleDelegationSubject, Dispatcher: graphWorkerOutboxDispatcher{box}, Outcomes: box, Deadline: 20 * time.Millisecond,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(t.Context(), multiagent.RunRequest{RunID: runID, Task: multiagent.TaskReference{ID: "task", Description: "wake"}}); err == nil {
		t.Fatal("run should pause for delegated outcome")
	}
	traces, err := box.Report(t.Context(), runID)
	if err != nil || len(traces) != 1 {
		t.Fatalf("report=%+v err=%v", traces, err)
	}
	var command multiagent.GraphRoleCommand
	if err := json.Unmarshal(traces[0].Command.Payload, &command); err != nil {
		t.Fatal(err)
	}
	woke := make(chan string, 1)
	worker := &graphRoleWorker{runDir: runDir, workerID: "worker-crashed", publish: func(context.Context, string, []byte) error { return nil }, resume: func(_ context.Context, got string) error {
		woke <- got
		return nil
	}}
	if action, _, err := worker.claimExecution(t.Context(), dbPath, command); err != nil || action != "execute" {
		t.Fatalf("claim action=%q err=%v", action, err)
	}
	if err := worker.persistAcceptedAndPublish(t.Context(), dbPath, command); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	worker.wakeExpired(t.Context(), time.Now().UTC())
	select {
	case got := <-woke:
		if got != runID {
			t.Fatalf("woke run %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expired accepted worker was not woken")
	}
	trace := waitForGraphTerminal(t, runDir, runID, command.DeliveryKey)
	if got := trace.Outcomes[len(trace.Outcomes)-1].Status; got != event.OutcomeTimedOut {
		t.Fatalf("terminal status=%s want timed_out", got)
	}
}

func TestGraphRoleWorkerPublishExhaustionStillClosesAndReplays(t *testing.T) {
	runDir := t.TempDir()
	command := graphWorkerTestCommand("run-publish-exhaustion", time.Now().Add(time.Minute))
	prepareGraphWorkerRun(t, runDir, command)
	var executions atomic.Int32
	var attempts atomic.Int32
	var published []event.Outcome
	worker := &graphRoleWorker{runDir: runDir, workerID: "worker-1", executeRole: func(context.Context, multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error) {
		executions.Add(1)
		return multiagent.RoleRunResult{Outcome: multiagent.OutcomePlanReady, LocalIterations: 1}, nil
	}, publish: func(_ context.Context, _ string, payload []byte) error {
		if attempts.Add(1) <= 4 {
			return errors.New("publish unavailable")
		}
		var outcome event.Outcome
		if err := json.Unmarshal(payload, &outcome); err != nil {
			return err
		}
		published = append(published, outcome)
		return nil
	}}
	worker.execute(command)
	worker.execute(command)
	if executions.Load() != 1 {
		t.Fatalf("executions=%d want 1", executions.Load())
	}
	if attempts.Load() != 5 || len(published) != 1 || !published[0].Status.Terminal() {
		t.Fatalf("publish attempts=%d published=%#v", attempts.Load(), published)
	}
	state, outcome := graphWorkerLedgerOutcome(t, runDir, command)
	if state != "terminal" || outcome.Status != event.OutcomeSucceeded {
		t.Fatalf("ledger state=%q outcome=%#v", state, outcome)
	}
	box, err := event.NewSQLiteOutbox(filepath.Join(runDir, command.RunID, "multiagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	trace, err := box.Report(t.Context(), command.RunID)
	if err != nil || len(trace) != 1 || len(trace[0].Outcomes) != 2 || !trace[0].Outcomes[1].Status.Terminal() {
		t.Fatalf("outcomes must persist before notification, trace=%+v err=%v", trace, err)
	}
}

func TestGraphRoleWorkerAcceptanceReplayUsesPersistedBytes(t *testing.T) {
	runDir := t.TempDir()
	command := graphWorkerTestCommand("run-acceptance-replay", time.Now().Add(time.Minute))
	prepareGraphWorkerRun(t, runDir, command)
	var published [][]byte
	worker := &graphRoleWorker{runDir: runDir, workerID: "worker-acceptance", publish: func(_ context.Context, _ string, payload []byte) error {
		published = append(published, append([]byte(nil), payload...))
		return nil
	}}
	ledgerPath := filepath.Join(runDir, command.RunID, "multiagent.db")
	claim, _, err := worker.claimExecution(t.Context(), ledgerPath, command)
	if err != nil || claim != "execute" {
		t.Fatalf("claim=%q err=%v", claim, err)
	}
	if err := worker.persistAcceptedAndPublish(t.Context(), ledgerPath, command); err != nil {
		t.Fatal(err)
	}
	if err := worker.persistAcceptedAndPublish(t.Context(), ledgerPath, command); err != nil {
		t.Fatal(err)
	}
	if len(published) != 2 || !bytes.Equal(published[0], published[1]) {
		t.Fatalf("acceptance replay must use immutable ledger bytes: %q / %q", published[0], published[1])
	}
}

func TestGraphRoleWorkerRetriesResumeAfterDurableClaimRelease(t *testing.T) {
	var calls atomic.Int32
	resumed := make(chan struct{}, 1)
	worker := &graphRoleWorker{running: make(map[string]struct{}), resume: func(context.Context, string) error {
		if calls.Add(1) == 1 {
			return fmt.Errorf("%w: run", multiagent.ErrRunClaimed)
		}
		resumed <- struct{}{}
		return nil
	}}
	worker.resumeRun("run-resume-retry", "outcome resume")
	select {
	case <-resumed:
	case <-time.After(time.Second):
		t.Fatal("resume was not retried after the durable claim released")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("resume calls=%d want 2", got)
	}
}

func TestGraphRoleWorkerExpiredDeliveryPersistsTerminalBeforePublish(t *testing.T) {
	runDir := t.TempDir()
	command := graphWorkerTestCommand("run-expired-delivery", time.Now().Add(-time.Second))
	prepareGraphWorkerRun(t, runDir, command)
	var published []event.Outcome
	worker := &graphRoleWorker{runDir: runDir, workerID: "worker-expired", publish: func(_ context.Context, _ string, payload []byte) error {
		var outcome event.Outcome
		if err := json.Unmarshal(payload, &outcome); err != nil {
			return err
		}
		published = append(published, outcome)
		return nil
	}}
	worker.execute(command)
	state, outcome := graphWorkerLedgerOutcome(t, runDir, command)
	if state != "terminal" || outcome.Status != event.OutcomeTimedOut {
		t.Fatalf("ledger state=%q outcome=%#v", state, outcome)
	}
	if len(published) != 2 || published[1].Status != event.OutcomeTimedOut {
		t.Fatalf("published=%#v", published)
	}
}

func TestGraphRoleWorkerReplaysTerminalAfterDeadline(t *testing.T) {
	runDir := t.TempDir()
	command := graphWorkerTestCommand("run-replay-after-deadline", time.Now().Add(500*time.Millisecond))
	prepareGraphWorkerRun(t, runDir, command)
	var first []event.Outcome
	firstWorker := &graphRoleWorker{runDir: runDir, workerID: "worker-first", executeRole: func(context.Context, multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error) {
		return multiagent.RoleRunResult{Outcome: multiagent.OutcomePlanReady, LocalIterations: 1}, nil
	}, publish: func(_ context.Context, _ string, payload []byte) error {
		var outcome event.Outcome
		if err := json.Unmarshal(payload, &outcome); err != nil {
			return err
		}
		first = append(first, outcome)
		return nil
	}}
	firstWorker.execute(command)
	time.Sleep(600 * time.Millisecond)
	var replay []event.Outcome
	secondWorker := &graphRoleWorker{runDir: runDir, workerID: "worker-second", publish: func(_ context.Context, _ string, payload []byte) error {
		var outcome event.Outcome
		if err := json.Unmarshal(payload, &outcome); err != nil {
			return err
		}
		replay = append(replay, outcome)
		return nil
	}}
	secondWorker.execute(command)
	if len(first) != 2 || first[1].Status != event.OutcomeSucceeded || len(replay) != 1 || replay[0].Status != event.OutcomeSucceeded {
		t.Fatalf("first=%#v replay=%#v", first, replay)
	}
}

func TestGraphRoleWorkerLateSuccessIsTimedOut(t *testing.T) {
	runDir := t.TempDir()
	command := graphWorkerTestCommand("run-late-success", time.Now().Add(20*time.Millisecond))
	prepareGraphWorkerRun(t, runDir, command)
	var published []event.Outcome
	worker := &graphRoleWorker{runDir: runDir, workerID: "worker-late", executeRole: func(context.Context, multiagent.GraphRoleCommand) (multiagent.RoleRunResult, error) {
		time.Sleep(60 * time.Millisecond)
		return multiagent.RoleRunResult{Outcome: multiagent.OutcomePlanReady, LocalIterations: 1}, nil
	}, publish: func(_ context.Context, _ string, payload []byte) error {
		var outcome event.Outcome
		if err := json.Unmarshal(payload, &outcome); err != nil {
			return err
		}
		published = append(published, outcome)
		return nil
	}}
	worker.execute(command)
	state, outcome := graphWorkerLedgerOutcome(t, runDir, command)
	if state != "terminal" || outcome.Status != event.OutcomeTimedOut || len(published) != 2 || published[1].Status != event.OutcomeTimedOut {
		t.Fatalf("state=%q ledger=%#v published=%#v", state, outcome, published)
	}
}

func TestGraphDelegationPublisherRetriesBeforeReturning(t *testing.T) {
	t.Setenv(graphRoleDelegationRequestedEnv, "1")
	var attempts atomic.Int32
	restore := configureGraphRolePublisher(graphWorkerPublisherFunc(func(context.Context, string, []byte) error {
		if attempts.Add(1) < 3 {
			return context.DeadlineExceeded
		}
		return nil
	}))
	defer restore()
	outbox, options, err := newGraphDelegationOutbox(filepath.Join(t.TempDir(), "multiagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	command := event.Command{EventID: event.CommandEventID("retry:0"), Type: multiagent.GraphRoleDelegationCommandType,
		RunID: "run", TaskID: "task", DelegationID: "retry", CorrelationID: "corr", IdempotencyKey: "retry:0",
		Deadline: time.Now().Add(time.Minute), SchemaVersion: event.CommandSchemaVersion, Payload: json.RawMessage(`{}`)}
	if options == nil {
		t.Fatal("production delegation was not composed")
	}
	if err := options.Dispatcher.Dispatch(t.Context(), options.Subject, command); err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("publish attempts=%d want 3", got)
	}
}

type graphWorkerRoleRunner func(context.Context, multiagent.RoleRunRequest) (multiagent.RoleRunResult, error)

func (f graphWorkerRoleRunner) RunRole(ctx context.Context, request multiagent.RoleRunRequest) (multiagent.RoleRunResult, error) {
	return f(ctx, request)
}

type graphWorkerOutboxDispatcher struct{ box *event.SQLiteOutbox }

func (d graphWorkerOutboxDispatcher) Dispatch(ctx context.Context, subject string, command event.Command) error {
	_, err := d.box.Accept(ctx, subject, command)
	return err
}

type graphWorkerPublisherFunc func(context.Context, string, []byte) error

func (f graphWorkerPublisherFunc) Publish(ctx context.Context, subject string, payload []byte) error {
	return f(ctx, subject, payload)
}

func graphWorkerTestCommand(runID string, deadline time.Time) multiagent.GraphRoleCommand {
	key := "graph:exec:0"
	request := multiagent.RoleRunRequest{Run: multiagent.RunView{RunID: runID, Task: multiagent.TaskReference{ID: "task", Description: "plan"}, WorkspaceID: "workspace", CurrentRole: multiagent.RolePlanner, ExecutionKey: "exec", Visit: 1}}
	return multiagent.GraphRoleCommand{ChildID: "task:planner:1", Role: multiagent.RolePlanner, Task: request.Run.Task,
		ExecutionKey: "exec", RunID: runID, WorkspaceID: "workspace", DelegationID: "graph:exec",
		DeliveryKey: key, CorrelationID: runID + ":exec", CommandEventID: event.CommandEventID(key), Deadline: deadline, Request: request}
}

func fanoutWorkerCommand(base multiagent.GraphRoleCommand, lane multiagent.FanOutLane, role multiagent.Role) multiagent.GraphRoleCommand {
	command := base
	command.Lane, command.JoinID, command.Role = lane, "join:"+base.RunID, role
	command.ChildID = "parent-task:" + string(lane)
	command.Task = multiagent.TaskReference{ID: command.ChildID, Description: string(lane)}
	command.ExecutionKey = base.ExecutionKey + ":fanout:" + string(lane)
	command.DelegationID = "graph:join:" + base.RunID + ":" + string(lane)
	command.DeliveryKey = command.DelegationID + ":0"
	command.CorrelationID = base.RunID + ":" + command.ExecutionKey
	command.CommandEventID = event.CommandEventID(command.DeliveryKey)
	command.Request.Run.CurrentRole = role
	command.Request.Run.Task = command.Task
	command.Request.Run.ExecutionKey = command.ExecutionKey
	return command
}

func prepareGraphWorkerRun(t *testing.T, runDir string, command multiagent.GraphRoleCommand) {
	t.Helper()
	manifest := referenceWorkflowManifest{SchemaVersion: referenceManifestSchemaVersion, RunID: command.RunID,
		WorkflowID: "test", WorkspaceID: command.WorkspaceID, WorkspaceCleaned: true, WorkflowVersion: 1}
	if err := writeReferenceManifest(runDir, manifest); err != nil {
		t.Fatal(err)
	}
	box, err := event.NewSQLiteOutbox(filepath.Join(runDir, command.RunID, "multiagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(command)
	cmd := event.Command{EventID: command.CommandEventID, Type: multiagent.GraphRoleDelegationCommandType,
		RunID: command.RunID, TaskID: command.ChildID, DelegationID: command.DelegationID, JoinID: command.JoinID, Lane: string(command.Lane),
		CorrelationID: command.CorrelationID, IdempotencyKey: command.DeliveryKey,
		Deadline: command.Deadline, SchemaVersion: event.CommandSchemaVersion, Payload: payload}
	if _, err := box.Accept(t.Context(), graphRoleDelegationSubject, cmd); err != nil {
		t.Fatal(err)
	}
	box.Close()
}

func graphWorkerLedgerOutcome(t *testing.T, runDir string, command multiagent.GraphRoleCommand) (string, event.Outcome) {
	t.Helper()
	db, err := openGraphRoleLedger(t.Context(), filepath.Join(runDir, command.RunID, "multiagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	var encoded []byte
	if err := db.QueryRowContext(t.Context(), `SELECT state,outcome_json FROM graph_role_executions WHERE delivery_key=?`, command.DeliveryKey).Scan(&state, &encoded); err != nil {
		t.Fatal(err)
	}
	var outcome event.Outcome
	if err := json.Unmarshal(encoded, &outcome); err != nil {
		t.Fatal(err)
	}
	return state, outcome
}

func waitForGraphTerminal(t *testing.T, runDir, runID, key string) event.CommandTrace {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		box, err := event.NewSQLiteOutbox(filepath.Join(runDir, runID, "multiagent.db"))
		if err == nil {
			traces, reportErr := box.Report(t.Context(), runID)
			box.Close()
			if reportErr == nil {
				for _, trace := range traces {
					if trace.Command.IdempotencyKey == key && len(trace.Outcomes) > 0 && trace.Outcomes[len(trace.Outcomes)-1].Status.Terminal() {
						return trace
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for graph terminal outcome")
	return event.CommandTrace{}
}

func waitForGraphStatus(t *testing.T, runDir, runID string, status event.OutcomeStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		box, err := event.NewSQLiteOutbox(filepath.Join(runDir, runID, "multiagent.db"))
		if err == nil {
			traces, reportErr := box.Report(t.Context(), runID)
			box.Close()
			if reportErr == nil {
				for _, trace := range traces {
					for _, outcome := range trace.Outcomes {
						if outcome.Status == status {
							return
						}
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for graph status %s", status)
}
