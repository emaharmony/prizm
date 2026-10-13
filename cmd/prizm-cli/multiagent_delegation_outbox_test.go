package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

type graphDelegationTestRunner struct{}

func (graphDelegationTestRunner) RunRole(context.Context, multiagent.RoleRunRequest) (multiagent.RoleRunResult, error) {
	return multiagent.RoleRunResult{}, errors.New("role must be delegated through the production composition")
}

type graphInlineTestRunner struct{}

func (graphInlineTestRunner) RunRole(context.Context, multiagent.RoleRunRequest) (multiagent.RoleRunResult, error) {
	return multiagent.RoleRunResult{Outcome: multiagent.TransitionOutcome("done"), LocalIterations: 1}, nil
}

func TestGraphDelegationOutboxBoundsBackgroundPublishByCommandDeadline(t *testing.T) {
	t.Setenv(graphRoleDelegationRequestedEnv, "1")
	seen := make(chan time.Time, 1)
	restore := configureGraphRolePublisher(graphWorkerPublisherFunc(func(ctx context.Context, _ string, _ []byte) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("background dispatch did not receive a command deadline")
		}
		seen <- deadline
		return nil
	}))
	defer restore()
	outbox, _, err := newGraphDelegationOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	deadline := time.Now().Add(time.Minute).UTC()
	command := event.Command{EventID: "evt-background", Type: multiagent.GraphRoleDelegationCommandType, RunID: "run-background", TaskID: "task", DelegationID: "delegation", CorrelationID: "corr", IdempotencyKey: "delivery-background", Deadline: deadline, SchemaVersion: event.CommandSchemaVersion, Payload: json.RawMessage(`{}`)}
	if err := outbox.Dispatch(context.Background(), graphRoleDelegationSubject, command); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; !got.Equal(deadline) {
		t.Fatalf("publish deadline=%v want %v", got, deadline)
	}
}

func TestGraphDelegationOutboxCancelsBlockedBackgroundPublishAtCommandDeadline(t *testing.T) {
	t.Setenv(graphRoleDelegationRequestedEnv, "1")
	restore := configureGraphRolePublisher(graphWorkerPublisherFunc(func(ctx context.Context, _ string, _ []byte) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	defer restore()
	outbox, _, err := newGraphDelegationOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	deadline := time.Now().Add(40 * time.Millisecond).UTC()
	command := event.Command{EventID: "evt-blocked", Type: multiagent.GraphRoleDelegationCommandType, RunID: "run-blocked", TaskID: "task", DelegationID: "delegation", CorrelationID: "corr", IdempotencyKey: "delivery-blocked", Deadline: deadline, SchemaVersion: event.CommandSchemaVersion, Payload: json.RawMessage(`{}`)}
	if err := outbox.Dispatch(context.Background(), graphRoleDelegationSubject, command); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked publish err=%v", err)
	}
}

func TestGraphDelegationOutboxPreservesEarlierCallerDeadline(t *testing.T) {
	t.Setenv(graphRoleDelegationRequestedEnv, "1")
	seen := make(chan time.Time, 1)
	restore := configureGraphRolePublisher(graphWorkerPublisherFunc(func(ctx context.Context, _ string, _ []byte) error { d, _ := ctx.Deadline(); seen <- d; return nil }))
	defer restore()
	outbox, _, err := newGraphDelegationOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	callerDeadline := time.Now().Add(time.Minute).UTC()
	ctx, cancel := context.WithDeadline(context.Background(), callerDeadline)
	defer cancel()
	command := event.Command{EventID: "evt-caller", Type: multiagent.GraphRoleDelegationCommandType, RunID: "run-caller", TaskID: "task", DelegationID: "delegation", CorrelationID: "corr", IdempotencyKey: "delivery-caller", Deadline: time.Now().Add(2 * time.Minute).UTC(), SchemaVersion: event.CommandSchemaVersion, Payload: json.RawMessage(`{}`)}
	if err := outbox.Dispatch(ctx, graphRoleDelegationSubject, command); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; !got.Equal(callerDeadline) {
		t.Fatalf("deadline=%v want caller %v", got, callerDeadline)
	}
}

func TestCLIGraphRunUsesInlineRoleRunnerWhileGraphDelegationIsDisabled(t *testing.T) {
	t.Setenv(graphRoleDelegationRequestedEnv, "")
	ctx := t.Context()
	root := t.TempDir()
	runDir := filepath.Join(root, "runs")
	definitionDB := filepath.Join(root, "definitions.db")
	definition := multiagent.WorkflowDefinition{
		APIVersion: multiagent.SchemaAPIVersion,
		Kind:       multiagent.SchemaKind,
		Metadata:   multiagent.WorkflowMetadata{Name: "cli-inline", ID: "cli-inline", Version: "1.0.0"},
		Spec: multiagent.WorkflowSpec{
			EntryNode: "worker",
			Nodes: []multiagent.SchemaNode{
				{ID: "worker", Type: "role", Role: "worker", AgentProfile: "worker-agent", AllowedOutcomes: []string{"done"}},
				{ID: "complete", Type: "terminal", TerminalCondition: string(multiagent.TerminalConditionCompleted)},
			},
			Edges: []multiagent.SchemaEdge{{ID: "worker-done", From: "worker", To: "complete", When: multiagent.SchemaCondition{Outcome: "done"}}},
		},
	}
	graph, diagnostics, err := multiagent.Compile(definition, nil, multiagent.CompileOptions{})
	if err != nil {
		t.Fatalf("compile graph: %v (%s)", err, diagnostics.Error())
	}
	definitionStore, err := multiagent.NewDefinitionStore(definitionDB)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := definitionStore.Register(ctx, definition, graph, "test:cli-composition")
	if closeErr := definitionStore.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	manifest := referenceWorkflowManifest{SchemaVersion: referenceManifestSchemaVersion, RunID: "run-cli-inline", WorkflowID: registered.WorkflowID, WorkflowVersion: registered.Version, DefinitionDBPath: definitionDB}
	runtime, err := openReferenceRuntimeWithInteraction(runDir, manifest, graphInlineTestRunner{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.close()
	state, err := runtime.runtime.Run(ctx, multiagent.RunRequest{RunID: manifest.RunID, Task: multiagent.TaskReference{ID: "parent", Description: "complete inline"}})
	if err != nil || state.Status != multiagent.RunStatusCompleted {
		t.Fatalf("graph delegation disabled must complete inline, state=%s err=%v", state.Status, err)
	}
	trace, err := runtime.delegation.Report(ctx, manifest.RunID)
	if err != nil || len(trace) != 0 {
		t.Fatalf("disabled graph delegation outbox trace=%+v err=%v", trace, err)
	}
}

func TestNewGraphDelegationOutboxRejectsProductionEnablementWithoutWorker(t *testing.T) {
	t.Setenv(graphRoleDelegationRequestedEnv, "1")
	_, _, err := newGraphDelegationOutbox(filepath.Join(t.TempDir(), "multiagent.db"))
	if err == nil || err.Error() != "graph role delegation is unavailable: no durable graph-role worker is composed" {
		t.Fatalf("production graph delegation enablement error=%v", err)
	}
}

func TestNewGraphDelegationOutboxRequiresWorkspaceForProductionDispatch(t *testing.T) {
	t.Setenv(graphRoleDelegationRequestedEnv, "1")
	restore := configureGraphRolePublisher(graphWorkerPublisherFunc(func(context.Context, string, []byte) error { return nil }))
	defer restore()
	outbox, options, err := newGraphDelegationOutbox(filepath.Join(t.TempDir(), "multiagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	if options == nil || !options.RequireWorkspaceID {
		t.Fatalf("production durable delegation must require a workspace identity: %+v", options)
	}
}

func TestCLIGraphRunDelegatesAndResumesThroughDurableOutboxAfterRestart(t *testing.T) {
	t.Setenv(graphRoleDelegationRequestedEnv, "")
	ctx := t.Context()
	root := t.TempDir()
	runDir := filepath.Join(root, "runs")
	definitionDB := filepath.Join(root, "definitions.db")
	definition := multiagent.WorkflowDefinition{
		APIVersion: multiagent.SchemaAPIVersion,
		Kind:       multiagent.SchemaKind,
		Metadata: multiagent.WorkflowMetadata{
			Name: "cli-outbox-restart", ID: "cli-outbox-restart", Version: "1.0.0",
		},
		Spec: multiagent.WorkflowSpec{
			EntryNode: "worker",
			Nodes: []multiagent.SchemaNode{
				{ID: "worker", Type: "role", Role: "worker", AgentProfile: "worker-agent", AllowedOutcomes: []string{"done"}},
				{ID: "complete", Type: "terminal", TerminalCondition: string(multiagent.TerminalConditionCompleted)},
			},
			Edges: []multiagent.SchemaEdge{{ID: "worker-done", From: "worker", To: "complete", When: multiagent.SchemaCondition{Outcome: "done"}}},
		},
	}
	graph, diagnostics, err := multiagent.Compile(definition, nil, multiagent.CompileOptions{})
	if err != nil {
		t.Fatalf("compile graph: %v (%s)", err, diagnostics.Error())
	}
	definitionStore, err := multiagent.NewDefinitionStore(definitionDB)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := definitionStore.Register(ctx, definition, graph, "test:cli-composition")
	if closeErr := definitionStore.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	const runID = "run-cli-outbox-restart"
	manifest := referenceWorkflowManifest{
		SchemaVersion: referenceManifestSchemaVersion,
		RunID:         runID, WorkflowID: registered.WorkflowID, WorkflowVersion: registered.Version,
		DefinitionDBPath: definitionDB, WorkspaceID: "workspace-bound-before-dispatch",
	}
	first, err := openReferenceRuntimeWithInteractionForDelegationTest(runDir, manifest, graphDelegationTestRunner{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, runErr := first.runtime.Run(ctx, multiagent.RunRequest{RunID: runID, Task: multiagent.TaskReference{ID: "parent", Description: "delegate once"}, WorkspaceID: first.workspaceID})
	var waiting *multiagent.RunWaitingError
	if !errors.As(runErr, &waiting) || state.Status != multiagent.RunStatusPaused {
		t.Fatalf("run state=%s err=%v", state.Status, runErr)
	}
	trace, err := first.delegation.Report(ctx, runID)
	if err != nil || len(trace) != 1 {
		t.Fatalf("outbox trace=%+v err=%v", trace, err)
	}
	if trace[0].Subject != graphRoleDelegationSubject || trace[0].Command.Deadline.IsZero() {
		t.Fatalf("delegation command=%+v subject=%q", trace[0].Command, trace[0].Subject)
	}
	command := trace[0].Command
	var dispatched multiagent.GraphRoleCommand
	if err := json.Unmarshal(command.Payload, &dispatched); err != nil {
		t.Fatal(err)
	}
	if dispatched.WorkspaceID != first.workspaceID || dispatched.Request.Run.WorkspaceID != first.workspaceID {
		t.Fatalf("delegated command workspace=%q run workspace=%q want %q", dispatched.WorkspaceID, dispatched.Request.Run.WorkspaceID, first.workspaceID)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := openReferenceRuntimeWithInteractionForDelegationTest(runDir, manifest, graphDelegationTestRunner{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.close()
	accepted := event.Outcome{
		EventID: "accepted-after-restart", CommandEventID: command.EventID,
		RunID: runID, TaskID: command.TaskID, DelegationID: command.DelegationID,
		CorrelationID: command.CorrelationID, CausationID: command.EventID, DeliveryKey: command.IdempotencyKey,
		Status: event.OutcomeAccepted, Sequence: 1, OccurredAt: time.Now().UTC(),
	}
	resultPayload, err := json.Marshal(struct {
		Result multiagent.RoleRunResult `json:"result"`
	}{Result: multiagent.RoleRunResult{Outcome: multiagent.TransitionOutcome("done"), LocalIterations: 1}})
	if err != nil {
		t.Fatal(err)
	}
	terminal := accepted
	terminal.EventID = "completed-after-restart"
	terminal.Status = event.OutcomeSucceeded
	terminal.Sequence = 2
	terminal.Payload = resultPayload
	if inserted, err := restarted.delegation.RecordOutcome(ctx, accepted); err != nil || !inserted {
		t.Fatalf("record accepted inserted=%v err=%v", inserted, err)
	}
	if inserted, err := restarted.delegation.RecordOutcome(ctx, terminal); err != nil || !inserted {
		t.Fatalf("record terminal inserted=%v err=%v", inserted, err)
	}

	state, err = restarted.runtime.Resume(ctx, runID)
	if err != nil || state.Status != multiagent.RunStatusCompleted {
		t.Fatalf("resume state=%s err=%v", state.Status, err)
	}
	pending, err := restarted.delegation.PendingOutcomes(ctx, runID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending outcomes=%+v err=%v", pending, err)
	}
}
