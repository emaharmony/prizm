package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

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
		RunID: command.RunID, TaskID: command.ChildID, DelegationID: command.DelegationID,
		CorrelationID: command.CorrelationID, IdempotencyKey: command.DeliveryKey,
		Deadline: command.Deadline, SchemaVersion: event.CommandSchemaVersion, Payload: payload}
	if _, err := box.Accept(t.Context(), graphRoleDelegationSubject, cmd); err != nil {
		t.Fatal(err)
	}
	box.Close()
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
