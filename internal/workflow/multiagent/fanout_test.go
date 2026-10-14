package multiagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/subagent"
)

type failOnceFanoutDispatcher struct {
	store     DurableRunStore
	failed    bool
	cmds      []event.Command
	attempted []event.Command
}

type interleavedCheckpointStore struct {
	DurableRunStore
	entered chan struct{}
	release chan struct{}
	armed   atomic.Bool
}

func (s *interleavedCheckpointStore) Checkpoint(ctx context.Context, revision int64, record DurableRun, events []event.Event) (DurableRun, error) {
	if s.armed.CompareAndSwap(true, false) {
		close(s.entered)
		<-s.release
	}
	return s.DurableRunStore.Checkpoint(ctx, revision, record, events)
}

func (d *failOnceFanoutDispatcher) Dispatch(ctx context.Context, _ string, cmd event.Command) error {
	d.attempted = append(d.attempted, cmd)
	record, err := d.store.Load(ctx, cmd.RunID)
	if err != nil {
		return err
	}
	if record.Waiting == nil || (record.Waiting.Kind != "delegation_outcome" && record.Waiting.Kind != "delegation_join") {
		return errors.New("dispatch before wait checkpoint")
	}
	if record.Waiting.Kind == "delegation_join" && cmd.TaskID == record.DelegationJoin.Children[1].ChildID && !d.failed {
		d.failed = true
		return errors.New("injected lane publish failure")
	}
	d.cmds = append(d.cmds, cmd)
	return nil
}

func validFanOutPlan() *FanOutPlan {
	return &FanOutPlan{Tasks: []FanOutTask{
		{Lane: FanOutResearch, Role: RolePlanner, Description: "research the change"},
		{Lane: FanOutImplementation, Role: RoleDeveloper, Description: "implement the change"},
		{Lane: FanOutReview, Role: RoleReviewer, Description: "review the change"},
	}}
}

func fanOutDefinitionForRole(role Role) WorkflowDefinition {
	planner := roleNode("strategy", string(OutcomePlanReady), "ok")
	planner.Role = string(role)
	return WorkflowDefinition{APIVersion: SchemaAPIVersion, Kind: SchemaKind,
		Metadata: WorkflowMetadata{Name: "fanout-planner", ID: "fanout-planner", Version: "1.0.0"},
		Spec: WorkflowSpec{EntryNode: "strategy", Nodes: []SchemaNode{planner, terminalNode("end", "completed")},
			Edges: []SchemaEdge{edge("planner-end", "strategy", "end", string(OutcomePlanReady)), edge("planner-end-ok", "strategy", "end", "ok")}}}
}

func newPlannerDelegationRuntime(t *testing.T, env durableTestEnvironment, dispatch DelegationDispatcher, source DelegationOutcomeSource, clock func() time.Time) *DurableRuntime {
	return newDelegationRuntimeForRole(t, env, dispatch, source, clock, RolePlanner)
}

func newDelegationRuntimeForRole(t *testing.T, env durableTestEnvironment, dispatch DelegationDispatcher, source DelegationOutcomeSource, clock func() time.Time, role Role) *DurableRuntime {
	t.Helper()
	graph, diagnostics, err := Compile(fanOutDefinitionForRole(role), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("compile planner workflow: %v (%s)", err, diagnostics.Error())
	}
	runtime, err := NewDurableRuntime(graph, &countingRoleRunner{}, env.store, env.claimer, env.events, DurableRuntimeOptions{
		Clock: clock,
		Delegation: &DurableDelegationOptions{Subject: "graph.roles", Dispatcher: dispatch,
			Outcomes: source, Deadline: time.Minute},
	})
	if err != nil {
		t.Fatalf("new planner durable runtime: %v", err)
	}
	return runtime
}

func newWaitingPlannerDelegationRuntime(t *testing.T, runID string) (*DurableRuntime, durableTestEnvironment, *recordingDelegationDispatcher, *recordingOutcomeSource) {
	t.Helper()
	env := newDurableTestEnvironment(t)
	dispatch := &recordingDelegationDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store, runID: runID}
	runtime := newPlannerDelegationRuntime(t, env, dispatch, source, func() time.Time {
		return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	})
	_, err := runtime.Run(t.Context(), RunRequest{RunID: runID, Task: TaskReference{ID: "parent-task", Description: "delegate"}})
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) {
		t.Fatalf("Run() err=%v", err)
	}
	return runtime, env, dispatch, source
}

func TestFanOutPlanValidateStrictLanes(t *testing.T) {
	if err := validFanOutPlan().Validate(); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*FanOutPlan)
	}{
		{"missing lane", func(p *FanOutPlan) { p.Tasks = p.Tasks[:2] }},
		{"duplicate lane", func(p *FanOutPlan) { p.Tasks[1].Lane = FanOutResearch }},
		{"wrong role", func(p *FanOutPlan) { p.Tasks[2].Role = RoleDeveloper }},
		{"empty description", func(p *FanOutPlan) { p.Tasks[0].Description = " " }},
		{"unknown lane", func(p *FanOutPlan) { p.Tasks[0].Lane = FanOutLane("other") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validFanOutPlan()
			test.mutate(plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
}

func TestPlannerFanOutDecodesAndRejectsInvalidPlan(t *testing.T) {
	raw := `{
  "schema_version": 1,
  "understanding": "bounded parallel work",
  "implementation_plan": ["research", "implement", "review"],
  "task_breakdown": ["three lanes"],
  "acceptance_criteria": ["all lanes complete"],
  "risks": [], "assumptions": [],
  "fan_out": {"tasks": [
    {"lane":"research","role":"planner","description":"research"},
    {"lane":"implementation","role":"developer","description":"implement"},
    {"lane":"review","role":"reviewer","description":"review"}
  ]},
  "handoff": {"objective":"execute","reason":"plan ready"}
}`
	decoded, err := decodeRoleOutput(RolePlanner, raw)
	if err != nil || decoded.FanOut == nil {
		t.Fatalf("fan-out planner output failed: result=%#v err=%v", decoded, err)
	}
	bad := `{"schema_version":1,"understanding":"x","implementation_plan":["x"],"task_breakdown":["x"],"acceptance_criteria":["x"],"risks":[],"assumptions":[],"fan_out":{"tasks":[{"lane":"other","role":"planner","description":"x"},{"lane":"implementation","role":"developer","description":"x"},{"lane":"review","role":"reviewer","description":"x"}]}}`
	if _, err := decodeRoleOutput(RolePlanner, bad); err == nil {
		t.Fatal("invalid fan-out plan accepted")
	}
}

func TestDurablePlannerCapabilityCarriesDecodedFanOutToCorrelatedDispatch(t *testing.T) {
	env := newDurableTestEnvironment(t)
	dispatch := &recordingDelegationDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store, runID: "run-capability-fanout"}
	runtime := newPlannerDelegationRuntime(t, env, dispatch, source, func() time.Time {
		return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	})
	plannerOutput := `{"schema_version":1,"understanding":"bounded parallel work","implementation_plan":["research","implement","review"],"task_breakdown":["three lanes"],"acceptance_criteria":["all lanes complete"],"risks":[],"assumptions":[],"fan_out":{"tasks":[{"lane":"research","role":"planner","description":"research"},{"lane":"implementation","role":"developer","description":"implement"},{"lane":"review","role":"reviewer","description":"review"}]},"handoff":{"objective":"execute","reason":"plan ready"}}`
	// The adapter is invoked with the exact request decoded from the persisted
	// durable command, so the capability is proven across the real transport.
	adapter := newAdapterForTest(t, agentExecutorFunc(func(_ context.Context, request AgentExecutionRequest) (AgentExecutionResult, error) {
		return AgentExecutionResult{Output: plannerOutput, LocalIterations: 1}, nil
	}), nil, nil)
	_, err := runtime.Run(t.Context(), RunRequest{RunID: "run-capability-fanout", Task: TaskReference{ID: "parent-task", Description: "delegate"}})
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) || len(dispatch.commands) != 1 {
		t.Fatalf("Run err=%v dispatched=%d", err, len(dispatch.commands))
	}
	var root GraphRoleCommand
	if err := json.Unmarshal(dispatch.command.Payload, &root); err != nil {
		t.Fatal(err)
	}
	if root.Role != RolePlanner || !root.Request.FanOutAvailable {
		t.Fatalf("root request role=%s fanout_available=%v", root.Role, root.Request.FanOutAvailable)
	}
	parent, err := adapter.RunRole(t.Context(), root.Request)
	if err != nil || parent.FanOut == nil {
		t.Fatalf("decoded planner result=%#v err=%v", parent, err)
	}
	payload, err := json.Marshal(GraphRoleOutcome{Result: parent})
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = append(source.outcomes, matchingOutcome(dispatch.command, "planner-fanout", event.OutcomeSucceeded, 1, payload))
	_, err = runtime.Resume(t.Context(), "run-capability-fanout")
	if !errors.As(err, &waitingErr) {
		t.Fatalf("Resume err=%v, want join waiting", err)
	}
	if len(dispatch.commands) != 4 {
		t.Fatalf("dispatched %d commands, want parent plus three lanes", len(dispatch.commands))
	}
	seen := map[FanOutLane]bool{}
	for _, command := range dispatch.commands[1:] {
		var child GraphRoleCommand
		if err := json.Unmarshal(command.Payload, &child); err != nil {
			t.Fatal(err)
		}
		if child.Request.FanOutAvailable {
			t.Errorf("child lane %s inherited planner fan-out capability", child.Lane)
		}
		if child.JoinID == "" || child.CorrelationID == "" || child.Lane == "" {
			t.Errorf("child correlation incomplete: %#v", child)
		}
		seen[child.Lane] = true
	}
	if len(seen) != 3 || !seen[FanOutResearch] || !seen[FanOutImplementation] || !seen[FanOutReview] {
		t.Fatalf("dispatched lanes=%v", seen)
	}
}

type fanOutOnlyRunner struct{}

func (fanOutOnlyRunner) RunRole(context.Context, RoleRunRequest) (RoleRunResult, error) {
	return RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}, nil
}

func TestDurableRuntimePersistsFailureForUnsupportedLocalFanOut(t *testing.T) {
	env := newDurableTestEnvironment(t)
	planner := roleNode("strategy", string(OutcomePlanReady))
	planner.Role = string(RolePlanner)
	planner.AllowedOutcomes = append(planner.AllowedOutcomes, "ok")
	definition := WorkflowDefinition{APIVersion: SchemaAPIVersion, Kind: SchemaKind,
		Metadata: WorkflowMetadata{Name: "fanout-local", ID: "fanout-local", Version: "1.0.0"},
		Spec: WorkflowSpec{EntryNode: "strategy", Nodes: []SchemaNode{planner, terminalNode("end", "completed")},
			Edges: []SchemaEdge{edge("planner-end", "strategy", "end", string(OutcomePlanReady)), edge("planner-end-ok", "strategy", "end", "ok")}}}
	graph, diagnostics, err := Compile(definition, nil, CompileOptions{})
	if err != nil {
		t.Fatalf("compile workflow: %v (%s)", err, diagnostics.Error())
	}
	runtime, err := NewDurableRuntime(graph, fanOutOnlyRunner{}, env.store, env.claimer, env.events, DurableRuntimeOptions{})
	if err != nil {
		t.Fatalf("new durable runtime: %v", err)
	}
	_, err = runtime.Run(t.Context(), RunRequest{RunID: "run-local-fanout", Task: TaskReference{ID: "task-local", Description: "local"}})
	if err == nil || err.Error() != "unsupported_fan_out" {
		t.Fatalf("Run err=%v, want visible unsupported-fanout failure", err)
	}
	record, loadErr := env.store.Load(t.Context(), "run-local-fanout")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if record.Phase != CheckpointTerminal || record.Failure == nil || record.Failure.Kind != "role_execution" || record.Failure.Message != "unsupported_fan_out" {
		t.Fatalf("unsupported local fan-out was not persisted as failure: phase=%s failure=%#v", record.Phase, record.Failure)
	}
}

func TestDurableRuntimePersistsFailureForNonPlannerDelegatedFanOut(t *testing.T) {
	const runID = "run-nonplanner-fanout"
	env := newDurableTestEnvironment(t)
	dispatch := &recordingDelegationDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store, runID: runID}
	clock := func() time.Time { return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC) }
	runtime := newDelegationRuntimeForRole(t, env, dispatch, source, clock, RoleDeveloper)
	_, err := runtime.Run(t.Context(), RunRequest{RunID: runID, Task: TaskReference{ID: "task", Description: "implement"}})
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || len(dispatch.commands) != 1 {
		t.Fatalf("initial dispatch err=%v count=%d", err, len(dispatch.commands))
	}
	var child GraphRoleCommand
	if err := json.Unmarshal(dispatch.command.Payload, &child); err != nil {
		t.Fatal(err)
	}
	if child.Request.FanOutAvailable {
		t.Fatal("non-planner request received fan-out capability")
	}
	payload, err := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1, FanOut: validFanOutPlan()}})
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = []event.Outcome{matchingOutcome(dispatch.command, "forged-nonplanner-fanout", event.OutcomeSucceeded, 1, payload)}
	if _, err := runtime.Resume(t.Context(), runID); err == nil {
		t.Fatal("non-planner fan-out outcome was accepted")
	}
	record, err := env.store.Load(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Phase != CheckpointTerminal || record.Failure == nil || record.Failure.Kind != "fanout_unsupported" || len(dispatch.commands) != 1 {
		t.Fatalf("non-planner fan-out was not durably rejected: phase=%s failure=%#v dispatches=%d", record.Phase, record.Failure, len(dispatch.commands))
	}
}

func TestFanOutJoinRejectsNestedChildResult(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-nested-fanout")
	parentPayload, err := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = []event.Outcome{matchingOutcome(dispatch.command, "parent-nested", event.OutcomeSucceeded, 1, parentPayload)}
	if _, err := runtime.Resume(t.Context(), "run-nested-fanout"); err == nil {
		t.Fatal("parent fan-out did not wait for children")
	}
	record, err := env.store.Load(t.Context(), "run-nested-fanout")
	if err != nil {
		t.Fatal(err)
	}
	child := record.DelegationJoin.Children[0]
	childCommand := event.Command{EventID: child.CommandEventID, RunID: record.State.RunID, TaskID: child.ChildID,
		DelegationID: child.DelegationID, JoinID: child.JoinID, Lane: string(child.Lane),
		CorrelationID: child.CorrelationID, IdempotencyKey: child.DeliveryKey}
	nestedPayload, err := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = []event.Outcome{matchingOutcome(childCommand, "nested-child", event.OutcomeSucceeded, 1, nestedPayload)}
	if _, err := runtime.Resume(t.Context(), "run-nested-fanout"); err == nil || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("nested child result err=%v", err)
	}
	if len(dispatch.commands) != 4 {
		t.Fatalf("nested result triggered further dispatch: count=%d", len(dispatch.commands))
	}
}

func TestLegacyScalarWaitingRemainsValidWithJoinAdditions(t *testing.T) {
	runtime, env, _, _ := newWaitingDelegationRuntime(t, "run-graph-delegation")
	record, err := env.store.Load(t.Context(), "run-graph-delegation")
	if err != nil {
		t.Fatal(err)
	}
	if record.DelegationJoin != nil || record.Waiting == nil || record.Waiting.Kind != "delegation_outcome" {
		t.Fatalf("legacy scalar checkpoint changed: waiting=%#v join=%#v", record.Waiting, record.DelegationJoin)
	}
	if err := record.Validate(runtime.supervisor.graph); err != nil {
		t.Fatalf("legacy scalar checkpoint no longer validates: %v", err)
	}
}

func TestDelegationJoinCheckpointsAllChildrenBeforeFirstDispatch(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-graph-delegation")
	payload, err := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{
		Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = append(source.outcomes, matchingOutcome(dispatch.command, "parent-fanout", event.OutcomeSucceeded, 1, payload))
	_, err = runtime.Resume(t.Context(), "run-graph-delegation")
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) {
		t.Fatalf("Resume() err=%v, want waiting", err)
	}
	record, err := env.store.Load(t.Context(), "run-graph-delegation")
	if err != nil {
		t.Fatal(err)
	}
	if record.Waiting == nil || record.Waiting.Kind != "delegation_join" || record.DelegationJoin == nil {
		t.Fatalf("join checkpoint missing: waiting=%#v join=%#v", record.Waiting, record.DelegationJoin)
	}
	if len(record.DelegationJoin.Children) != 3 || len(dispatch.commands) != 4 {
		t.Fatalf("children=%d dispatched=%d", len(record.DelegationJoin.Children), len(dispatch.commands))
	}
	for _, child := range record.DelegationJoin.Children {
		if child.DispatchPending {
			t.Errorf("child %s remained pending after successful dispatch", child.Lane)
		}
	}
}

func TestDelegationJoinConcurrentChildCheckpointsReloadAndMergeSQLite(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-child-cas")
	payload, err := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{
		Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = append(source.outcomes, matchingOutcome(dispatch.command, "parent-fanout", event.OutcomeSucceeded, 1, payload))
	if _, err := runtime.Resume(t.Context(), "run-child-cas"); err == nil {
		t.Fatal("Resume() unexpectedly completed")
	}
	record, err := env.store.Load(t.Context(), "run-child-cas")
	if err != nil || record.DelegationJoin == nil {
		t.Fatalf("load active join: join=%#v err=%v", record.DelegationJoin, err)
	}
	joinID := record.DelegationJoin.JoinID
	var ready sync.WaitGroup
	ready.Add(len(record.DelegationJoin.Children))
	start := make(chan struct{})
	var calls atomic.Int32
	var workers sync.WaitGroup
	for _, child := range record.DelegationJoin.Children {
		child := child
		workers.Add(1)
		go func() {
			defer workers.Done()
			var first sync.Once
			_, updateErr := updateDelegationJoinChild(
				t.Context(), env.store, "run-child-cas", joinID, child.ChildID, child.DeliveryAttemptID, child.Retry.AttemptID,
				func(current *DelegationJoinChild) error {
					calls.Add(1)
					first.Do(func() { ready.Done(); <-start })
					current.Error = "safe child checkpoint marker"
					return nil
				}, nil,
			)
			if updateErr != nil {
				t.Errorf("update child %s: %v", child.Lane, updateErr)
			}
		}()
	}
	ready.Wait()
	close(start)
	workers.Wait()
	if got := calls.Load(); got <= int32(len(record.DelegationJoin.Children)) {
		t.Fatalf("updates=%d, want a revision-conflict reload and reapply", got)
	}
	record, err = env.store.Load(t.Context(), "run-child-cas")
	if err != nil {
		t.Fatal(err)
	}
	if record.DelegationJoin == nil || record.Revision <= 1 {
		t.Fatalf("latest join/revision missing: revision=%d join=%#v", record.Revision, record.DelegationJoin)
	}
	for _, child := range record.DelegationJoin.Children {
		if child.Error != "safe child checkpoint marker" {
			t.Errorf("child %s update was lost: error=%q", child.Lane, child.Error)
		}
	}
}

func TestResumeReloadsAfterConcurrentChildCheckpointConflict(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-resume-child-conflict")
	payload, err := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{
		Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = append(source.outcomes, matchingOutcome(dispatch.command, "parent-fanout", event.OutcomeSucceeded, 1, payload))
	if _, err := runtime.Resume(t.Context(), "run-resume-child-conflict"); err == nil {
		t.Fatal("Resume() unexpectedly completed")
	}
	record, err := env.store.Load(t.Context(), "run-resume-child-conflict")
	if err != nil || record.DelegationJoin == nil {
		t.Fatalf("load active join: join=%#v err=%v", record.DelegationJoin, err)
	}
	join := record.DelegationJoin
	previousRetry := record.Retry
	previousRole := record.State.CurrentRole
	implementation := join.Children[1]
	research := join.Children[0]
	result, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1}})
	cmd := dispatch.commands[implementationIndex(dispatch.commands, implementation.ChildID)]
	source.outcomes = []event.Outcome{matchingOutcome(cmd, "child-implementation-complete", event.OutcomeSucceeded, 1, result)}
	source.runID = "run-resume-child-conflict"
	interleaved := &interleavedCheckpointStore{DurableRunStore: env.store, entered: make(chan struct{}), release: make(chan struct{})}
	runtime.store = interleaved
	interleaved.armed.Store(true)
	resumeDone := make(chan error, 1)
	go func() {
		_, resumeErr := runtime.Resume(t.Context(), "run-resume-child-conflict")
		resumeDone <- resumeErr
	}()
	<-interleaved.entered
	if _, err := updateDelegationJoinChild(t.Context(), env.store, "run-resume-child-conflict", join.JoinID, research.ChildID, research.DeliveryAttemptID, research.Retry.AttemptID, func(child *DelegationJoinChild) error {
		child.Error = "concurrent safe checkpoint"
		return nil
	}, nil); err != nil {
		close(interleaved.release)
		t.Fatalf("concurrent child checkpoint: %v", err)
	}
	close(interleaved.release)
	if err := <-resumeDone; err != nil {
		var waitingErr *RunWaitingError
		if !errors.As(err, &waitingErr) {
			t.Fatalf("Resume() did not reload after conflict: %v", err)
		}
	}
	record, err = env.store.Load(t.Context(), "run-resume-child-conflict")
	if err != nil || record.DelegationJoin == nil {
		t.Fatalf("load recovered join: join=%#v err=%v", record.DelegationJoin, err)
	}
	if got := record.DelegationJoin.Children[0].Error; got != "concurrent safe checkpoint" {
		t.Fatalf("child update lost after parent Resume conflict: error=%q", got)
	}
	if record.DelegationJoin.Children[1].Result == nil || record.State.CurrentRole != previousRole ||
		record.ApprovedTask != nil || record.PendingFanOutAdoption != nil || !reflect.DeepEqual(record.Retry, previousRetry) {
		t.Fatalf("parent checkpoint lost or changed unrelated state: result=%t role=%s approved=%t adoption=%t retry_preserved=%t", record.DelegationJoin.Children[1].Result != nil, record.State.CurrentRole, record.ApprovedTask != nil, record.PendingFanOutAdoption != nil, reflect.DeepEqual(record.Retry, previousRetry))
	}
	if countString(source.acked, "child-implementation-complete") != 1 {
		t.Fatalf("outcome acknowledgements=%v", source.acked)
	}
	completed := queryEventsForTest(t, env.events, "run-resume-child-conflict", event.EventMultiAgentFanoutChildCompleted)
	if len(completed) != 1 {
		t.Fatalf("fan-out completion events=%d, want one after CAS replay", len(completed))
	}
}

func TestDelegationChildRecoveryIsSharedScopedAndRescheduledAfterTerminalFailure(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-child-recovery")
	now := time.Now().UTC()
	runtime.now = func() time.Time { return now }
	parentPayload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
	source.outcomes = []event.Outcome{matchingOutcome(dispatch.command, "parent-fanout", event.OutcomeSucceeded, 1, parentPayload)}
	if _, err := runtime.Resume(t.Context(), "run-child-recovery"); err == nil {
		t.Fatal("expected fan-out to remain waiting")
	}
	source.outcomes = nil
	record, err := env.store.Load(t.Context(), "run-child-recovery")
	if err != nil || record.DelegationJoin == nil {
		t.Fatalf("load join: join=%#v err=%v", record.DelegationJoin, err)
	}
	child := record.DelegationJoin.Children[0]
	oldDelivery, oldAttempt := child.DeliveryAttemptID, child.Retry.AttemptID
	handle, _, err := NewDelegationChildRetryHandle(t.Context(), env.store, "run-child-recovery", child.JoinID, child.ChildID, oldDelivery, oldAttempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ConsumeRecovery(oldAttempt, "invalid_schema"); err != nil {
		t.Fatal(err)
	}
	current := handle.CurrentAttempt()
	if current.AttemptID == oldAttempt || current.RecoveryNumber != 1 || current.Remaining != maxRoleRecoveryRetries-1 {
		t.Fatalf("shared retry state did not advance once: %#v", current)
	}
	if err := handle.RecordProgress(oldAttempt, "evidence", "tool:read_file", "hash"); !errors.Is(err, ErrStaleRetryAttempt) {
		t.Fatalf("old attempt remained writable: %v", err)
	}
	record, _ = env.store.Load(t.Context(), "run-child-recovery")
	child = record.DelegationJoin.Children[0]
	if child.DeliveryAttemptID != oldDelivery || child.DispatchPending || child.Retry.NextAt.IsZero() {
		t.Fatalf("internal correction incorrectly created parallel delivery: %#v", child)
	}

	// A terminal worker failure is the only point that schedules a replacement
	// delivery. The replacement uses the same logical child and current shared
	// allowance, with a new delivery identity.
	typedSchemaFailure := structuredError(child.Role, &json.UnmarshalTypeError{
		Value: "object", Type: reflect.TypeOf([]string{}), Field: "task_breakdown",
	})
	failure := ClassifyRoleExecutionFailure(typedSchemaFailure)
	failure.DeliveryAttemptID, failure.AttemptID = child.DeliveryAttemptID, child.Retry.AttemptID
	failure.Usage = cost.TokenUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}
	if failure.CorrectionField != "task_breakdown" || failure.ExpectedType != "a JSON array of strings" {
		t.Fatalf("worker failure omitted safe schema detail: %#v", failure)
	}
	payload, _ := json.Marshal(GraphRoleOutcome{Failure: &failure})
	failedCommand := event.Command{EventID: child.CommandEventID, RunID: "run-child-recovery", TaskID: child.ChildID,
		DelegationID: child.DelegationID, JoinID: child.JoinID, Lane: string(child.Lane),
		CorrelationID: child.CorrelationID, IdempotencyKey: child.DeliveryKey}
	source.outcomes = []event.Outcome{matchingOutcome(failedCommand, "child-failed", event.OutcomeFailed, 1, payload)}
	_, resumeErr := runtime.Resume(t.Context(), "run-child-recovery")
	if resumeErr == nil {
		t.Fatal("expected join to remain waiting")
	}
	record, err = env.store.Load(t.Context(), "run-child-recovery")
	if err != nil || record.DelegationJoin == nil {
		t.Fatalf("load rescheduled join: join=%#v err=%v", record.DelegationJoin, err)
	}
	child = record.DelegationJoin.Children[0]
	if child.Status.Terminal() || !child.DispatchPending || child.DeliveryNumber != 1 || child.DeliveryAttemptID == oldDelivery ||
		child.Retry.RecoveryNumber != 2 || child.Retry.Number != 3 || child.Retry.AccumulatedUsage.TotalTokens != 10 {
		t.Fatalf("terminal failure did not schedule the next shared attempt: err=%v status=%s delivery=%d recoveries=%d pending=%t", resumeErr, child.Status, child.DeliveryNumber, child.Retry.RecoveryNumber, child.DispatchPending)
	}
	if !strings.Contains(child.Retry.RecoveryFeedback, "task_breakdown") || !strings.Contains(child.Retry.RecoveryFeedback, "a JSON array of strings") {
		t.Fatalf("persisted retry feedback omitted safe field/type: %q", child.Retry.RecoveryFeedback)
	}
	staleFailure := RoleExecutionFailure{DeliveryAttemptID: oldDelivery, AttemptID: oldAttempt, Class: "schema", Code: "invalid_schema", Retryable: true}
	stalePayload, _ := json.Marshal(GraphRoleOutcome{Failure: &staleFailure})
	oldCommand := event.Command{EventID: event.CommandEventID(child.DelegationID + ":0"), RunID: "run-child-recovery",
		TaskID: child.ChildID, DelegationID: child.DelegationID, JoinID: child.JoinID, Lane: string(child.Lane),
		CorrelationID: "run-child-recovery:" + child.ExecutionKey + ":delivery:0", IdempotencyKey: child.DelegationID + ":0"}
	source.outcomes = []event.Outcome{matchingOutcome(oldCommand, "obsolete-child-failure", event.OutcomeFailed, 1, stalePayload)}
	if _, err := runtime.Resume(t.Context(), "run-child-recovery"); err == nil {
		t.Fatal("expected stale outcome to leave join waiting")
	}
	record, _ = env.store.Load(t.Context(), "run-child-recovery")
	if record.DelegationJoin.Children[0].Retry.RecoveryNumber != 2 || record.DelegationJoin.Children[0].DeliveryNumber != 1 || len(dispatch.commands) != 4 {
		t.Fatalf("obsolete child failure changed active attempt or redispatched: retries=%d delivery=%d commands=%d", record.DelegationJoin.Children[0].Retry.RecoveryNumber, record.DelegationJoin.Children[0].DeliveryNumber, len(dispatch.commands))
	}
	now = child.Retry.NextAt.Add(time.Second)
	if candidate := describeRecoveryCandidate(record, now); !candidate.Ready || candidate.WaitingKind != "delegation_join" {
		t.Fatalf("due child was not discoverable after restart: %#v", candidate)
	}
	source.outcomes = nil
	_, secondResumeErr := runtime.Resume(t.Context(), "run-child-recovery")
	if secondResumeErr == nil {
		t.Fatal("expected resumed join to remain waiting")
	}
	if len(dispatch.commands) != 5 {
		reloaded, _ := env.store.Load(t.Context(), "run-child-recovery")
		t.Fatalf("replacement delivery was not dispatched exactly once: commands=%d err=%v candidate=%#v", len(dispatch.commands), secondResumeErr, describeRecoveryCandidate(reloaded, now))
	}
	record, _ = env.store.Load(t.Context(), "run-child-recovery")
	child = record.DelegationJoin.Children[0]
	if child.DispatchPending || !child.Retry.NextAt.IsZero() {
		t.Fatalf("due replacement was not durably dispatched: %#v", child)
	}
	var retryCommand GraphRoleCommand
	if err := json.Unmarshal(dispatch.commands[4].Payload, &retryCommand); err != nil {
		t.Fatalf("decode replacement worker command: %v", err)
	}
	if !strings.Contains(retryCommand.RecoveryFeedback, "task_breakdown") ||
		!strings.Contains(retryCommand.RecoveryFeedback, "a JSON array of strings") {
		t.Fatalf("replacement worker command omitted specific safe correction: %q", retryCommand.RecoveryFeedback)
	}
}

func TestLegacyDelegationJoinChildFailureDoesNotReplay(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-legacy-child-no-replay")
	parentPayload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
	source.outcomes = []event.Outcome{matchingOutcome(dispatch.command, "legacy-parent-fanout", event.OutcomeSucceeded, 1, parentPayload)}
	if _, err := runtime.Resume(t.Context(), "run-legacy-child-no-replay"); err == nil {
		t.Fatal("expected child join to remain waiting")
	}
	record, err := env.store.Load(t.Context(), "run-legacy-child-no-replay")
	if err != nil || record.DelegationJoin == nil {
		t.Fatalf("load join: join=%#v err=%v", record.DelegationJoin, err)
	}
	child := &record.DelegationJoin.Children[0]
	child.Retry = nil // legacy durable records have no retry ledger and remain once-only.
	if _, err := env.store.Checkpoint(t.Context(), record.Revision, record, nil); err != nil {
		t.Fatal(err)
	}
	var childCommand event.Command
	for _, command := range dispatch.commands {
		if command.TaskID == child.ChildID {
			childCommand = command
			break
		}
	}
	failure := RoleExecutionFailure{DeliveryAttemptID: child.DeliveryAttemptID, AttemptID: "legacy-unbound",
		Class: "schema", Code: "invalid_schema", Retryable: true}
	payload, _ := json.Marshal(GraphRoleOutcome{Failure: &failure})
	source.outcomes = []event.Outcome{matchingOutcome(childCommand, "legacy-child-failed", event.OutcomeFailed, 1, payload)}
	if _, err := runtime.Resume(t.Context(), "run-legacy-child-no-replay"); err == nil {
		t.Fatal("expected the remaining child lanes to stay pending")
	}
	record, err = env.store.Load(t.Context(), "run-legacy-child-no-replay")
	if err != nil || record.DelegationJoin == nil {
		t.Fatalf("load legacy outcome: join=%#v err=%v", record.DelegationJoin, err)
	}
	child = &record.DelegationJoin.Children[0]
	if child.Status != event.OutcomeFailed || child.Retry != nil || child.DeliveryNumber != 0 || child.DispatchPending || len(dispatch.commands) != 4 {
		t.Fatalf("legacy child was replayed or lost its terminal result: status=%s retry=%#v delivery=%d pending=%t commands=%d", child.Status, child.Retry, child.DeliveryNumber, child.DispatchPending, len(dispatch.commands))
	}
}

func TestLegacySuccessfulFanOutJoinWithoutRetryLedgersGetsNoAdoption(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-legacy-success-no-adoption")
	parentPayload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
	source.outcomes = []event.Outcome{matchingOutcome(dispatch.command, "legacy-success-parent", event.OutcomeSucceeded, 1, parentPayload)}
	if _, err := runtime.Resume(t.Context(), "run-legacy-success-no-adoption"); err == nil {
		t.Fatal("expected child join to remain waiting")
	}
	record, err := env.store.Load(t.Context(), "run-legacy-success-no-adoption")
	if err != nil || record.DelegationJoin == nil {
		t.Fatalf("load join: join=%#v err=%v", record.DelegationJoin, err)
	}
	for i := range record.DelegationJoin.Children {
		record.DelegationJoin.Children[i].Retry = nil // preserve legacy once-only child schema.
	}
	if _, err := env.store.Checkpoint(t.Context(), record.Revision, record, nil); err != nil {
		t.Fatal(err)
	}
	var outcomes []event.Outcome
	for _, command := range dispatch.commands {
		if command.JoinID == "" {
			continue
		}
		var child GraphRoleCommand
		if err := json.Unmarshal(command.Payload, &child); err != nil {
			t.Fatal(err)
		}
		result := RoleRunResult{LocalIterations: 1}
		switch child.Lane {
		case FanOutResearch:
			result.Outcome = OutcomePlanReady
		case FanOutImplementation:
			result.Outcome = OutcomeImplementationReady
		case FanOutReview:
			result.Outcome = OutcomeReviewApproved
		default:
			t.Fatalf("unexpected lane %q", child.Lane)
		}
		payload, _ := json.Marshal(GraphRoleOutcome{Result: result})
		outcomes = append(outcomes, matchingOutcome(command, "legacy-success-"+string(child.Lane), event.OutcomeSucceeded, 1, payload))
	}
	source.outcomes = outcomes
	record, _ = env.store.Load(t.Context(), "run-legacy-success-no-adoption")
	joined, err := runtime.resumeDelegationJoin(t.Context(), record)
	if err != nil && joined.Phase == CheckpointTerminal {
		t.Fatalf("legacy successful join failed: status=%s failure=%#v err=%v", joined.State.Status, joined.Failure, err)
	}
	if joined.PendingFanOutAdoption != nil || joined.ApprovedTask != nil {
		t.Fatalf("legacy child success invented adoption or approval authority: adoption=%#v approved=%#v", joined.PendingFanOutAdoption, joined.ApprovedTask)
	}
}

func TestDelegationChildRetryRefusesUnresolvedMutation(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-child-unsafe-retry")
	parentPayload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
	source.outcomes = []event.Outcome{matchingOutcome(dispatch.command, "unsafe-parent-fanout", event.OutcomeSucceeded, 1, parentPayload)}
	if _, err := runtime.Resume(t.Context(), "run-child-unsafe-retry"); err == nil {
		t.Fatal("expected child join to remain waiting")
	}
	record, err := env.store.Load(t.Context(), "run-child-unsafe-retry")
	if err != nil || record.DelegationJoin == nil {
		t.Fatalf("load join: join=%#v err=%v", record.DelegationJoin, err)
	}
	child := record.DelegationJoin.Children[0]
	runID := record.State.RunID
	handle, sink, err := NewDelegationChildRetryHandle(t.Context(), env.store, runID, child.JoinID, child.ChildID, child.DeliveryAttemptID, child.Retry.AttemptID)
	if err != nil || sink == nil {
		t.Fatalf("bind child retry hook: sink=%t err=%v", sink != nil, err)
	}
	if err := sink(ToolDiagnostic{RunID: runID, TaskID: child.ChildID, Role: child.Role,
		ExecutionKey: child.ExecutionKey, AttemptID: child.Retry.AttemptID, MutationID: strings.Repeat("a", 64),
		Tool: "apply_patch_proposal", Status: "started", MutationPossible: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ConsumeRecovery(child.Retry.AttemptID, "invalid_schema"); err == nil {
		t.Fatal("retry consumed despite unresolved mutation")
	}
	record, err = env.store.Load(t.Context(), "run-child-unsafe-retry")
	if err != nil {
		t.Fatal(err)
	}
	stored := findDelegationChild(record, child.JoinID, child.ChildID)
	if stored == nil || stored.Retry.RecoveryNumber != 0 || !stored.Retry.MutationPossible || len(stored.Retry.UnsafeMutationIDs) != 1 {
		t.Fatalf("unsafe child retry state changed: %#v", stored)
	}
}

func countString(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func implementationIndex(commands []event.Command, childID string) int {
	for i, command := range commands {
		if command.TaskID == childID {
			return i
		}
	}
	return -1
}

func TestDelegationJoinRecoversPendingDispatchAfterRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "delegation-restart.db")
	openEnv := func() durableTestEnvironment {
		events, err := event.NewSQLiteEventStore(dbPath)
		if err != nil {
			t.Fatalf("open event store: %v", err)
		}
		store, err := NewSQLiteDurableRunStore(dbPath)
		if err != nil {
			_ = events.Close()
			t.Fatalf("open durable store: %v", err)
		}
		return durableTestEnvironment{store: store, events: events, claimer: newMemoryRunClaimer()}
	}
	env := openEnv()
	defer func() {
		if env.store != nil {
			if err := env.store.Close(); err != nil {
				t.Errorf("close durable store: %v", err)
			}
		}
		if env.events != nil {
			if err := env.events.Close(); err != nil {
				t.Errorf("close event store: %v", err)
			}
		}
	}()
	dispatch := &failOnceFanoutDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store}
	const parentCanary = "PARENT_REQUIREMENT_EVENT_PRIVACY_CANARY"
	parentTask := TaskReference{ID: "parent-task", Description: "Implement the complete regression. Preserve the same branch, worktree, and baseline across retries. Assert every configured command gate passes on both attempts. " + parentCanary}
	runtime := newPlannerDelegationRuntime(t, env, dispatch, source, func() time.Time {
		return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	})
	_, err := runtime.Run(t.Context(), RunRequest{RunID: "run-graph-delegation", Task: parentTask})
	if err == nil {
		t.Fatal("Run unexpectedly completed")
	}
	payload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1, FanOut: validFanOutPlan()}})
	parent := dispatch.cmds[0]
	source.outcomes = append(source.outcomes, matchingOutcome(parent, "parent-fanout", event.OutcomeSucceeded, 1, payload))
	if _, err = runtime.Resume(t.Context(), "run-graph-delegation"); err == nil {
		t.Fatal("expected injected dispatch failure")
	}
	record, err := env.store.Load(t.Context(), "run-graph-delegation")
	if err != nil {
		t.Fatal(err)
	}
	if record.DelegationJoin == nil || !record.DelegationJoin.Children[1].DispatchPending {
		t.Fatalf("failed lane was not recoverably pending: %#v", record.DelegationJoin)
	}
	if record.DelegationJoin.ParentTask == nil || *record.DelegationJoin.ParentTask != parentTask {
		t.Fatalf("durable join parent task=%#v, want original task", record.DelegationJoin.ParentTask)
	}
	corrupt := record
	corruptJoin := *record.DelegationJoin
	corruptParent := parentTask
	corruptParent.Description += " forged"
	corruptJoin.ParentTask = &corruptParent
	corrupt.DelegationJoin = &corruptJoin
	if err := corrupt.Validate(runtime.supervisor.graph); err == nil {
		t.Fatal("durable run validation accepted a join parent task that differs from the original run task")
	}
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := env.events.Close(); err != nil {
		t.Fatal(err)
	}
	env = openEnv()
	dispatch.store = env.store
	source.store = env.store
	source.outcomes = nil
	dispatch.failed = true
	runtime = newPlannerDelegationRuntime(t, env, dispatch, source, func() time.Time {
		return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	})
	_, err = runtime.Resume(t.Context(), "run-graph-delegation")
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) {
		t.Fatalf("retry Resume() err=%v, want waiting", err)
	}
	record, err = env.store.Load(t.Context(), "run-graph-delegation")
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range record.DelegationJoin.Children {
		if child.DispatchPending {
			t.Errorf("child %s still pending", child.Lane)
		}
	}
	firstPayloadByKey := make(map[string][]byte)
	seenLanes := make(map[FanOutLane]bool)
	for _, command := range dispatch.attempted {
		if command.JoinID == "" {
			continue
		}
		var child GraphRoleCommand
		if err := json.Unmarshal(command.Payload, &child); err != nil {
			t.Fatal(err)
		}
		if child.Request.ParentTask == nil || *child.Request.ParentTask != parentTask {
			t.Fatalf("child lane %s lost the original requirements after dispatch/restart", command.Lane)
		}
		prompt, err := BuildRolePrompt(child.Request)
		if err != nil || !strings.Contains(prompt, parentTask.Description) || !strings.Contains(prompt, "LANE TASK: "+child.Task.Description) {
			t.Fatalf("child lane %s prompt omitted parent or lane requirements: err=%v", command.Lane, err)
		}
		if err := ValidateDelegatedRoleCommand(t.Context(), env.store, child); err != nil {
			t.Fatalf("validate child lane %s command: %v", command.Lane, err)
		}
		forged := child
		forgedParent := *child.Request.ParentTask
		forgedParent.Description += " forged"
		forged.Request.ParentTask = &forgedParent
		if err := ValidateDelegatedRoleCommand(t.Context(), env.store, forged); err == nil {
			t.Fatalf("child lane %s accepted a forged parent task", command.Lane)
		}
		if prior, ok := firstPayloadByKey[command.IdempotencyKey]; ok {
			if !bytes.Equal(prior, command.Payload) {
				t.Fatalf("replayed lane %s changed its durable command payload", command.Lane)
			}
		} else {
			firstPayloadByKey[command.IdempotencyKey] = append([]byte(nil), command.Payload...)
		}
		seenLanes[child.Lane] = true
	}
	if len(seenLanes) != 3 || !seenLanes[FanOutResearch] || !seenLanes[FanOutImplementation] || !seenLanes[FanOutReview] {
		t.Fatalf("recovered fan-out lanes=%v", seenLanes)
	}
	events, err := env.events.Query(t.Context(), event.EventFilter{RunID: "run-graph-delegation"})
	if err != nil {
		t.Fatal(err)
	}
	eventData, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(eventData, []byte(parentCanary)) {
		t.Fatal("original task requirements leaked into the event projection")
	}
}

func TestDelegationJoinDeadlineEmitsTimedOutChildrenAndReport(t *testing.T) {
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	clock := now
	env := newDurableTestEnvironment(t)
	dispatch := &recordingDelegationDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store}
	runtime := newPlannerDelegationRuntime(t, env, dispatch, source, func() time.Time { return clock })
	_, err := runtime.Run(t.Context(), RunRequest{RunID: "run-graph-delegation", Task: TaskReference{ID: "parent-task", Description: "delegate"}})
	if err == nil {
		t.Fatal("Run unexpectedly completed")
	}
	payload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
	source.outcomes = append(source.outcomes, matchingOutcome(dispatch.command, "parent-fanout", event.OutcomeSucceeded, 1, payload))
	if _, err = runtime.Resume(t.Context(), "run-graph-delegation"); err == nil {
		t.Fatal("expected join wait")
	}
	source.outcomes = nil
	clock = clock.Add(2 * time.Minute)
	if _, err = runtime.Resume(t.Context(), "run-graph-delegation"); err == nil {
		t.Fatal("expected timeout terminal error")
	}
	events, err := env.events.Query(t.Context(), event.EventFilter{RunID: "run-graph-delegation", Type: event.EventMultiAgentFanoutChildCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("timed-out child events=%d want 3", len(events))
	}
	for _, evt := range events {
		if payloadString(evt.Payload, "status") != string(event.OutcomeTimedOut) {
			t.Fatalf("child status=%v", evt.Payload)
		}
	}
	joined, err := env.events.Query(t.Context(), event.EventFilter{RunID: "run-graph-delegation", Type: event.EventMultiAgentFanoutJoined})
	if err != nil || len(joined) != 1 || payloadString(joined[0].Payload, "status") != string(event.OutcomeTimedOut) {
		t.Fatalf("joined=%#v err=%v", joined, err)
	}
	report := BuildReferenceRunReport(ReferenceWorkflowInput{Objective: "delegate"}, RunState{RunID: "run-graph-delegation", Status: RunStatusFailed}, events)
	if len(report.FanOut) != 3 {
		t.Fatalf("report fanout=%#v", report.FanOut)
	}
}

func TestTerminalFanOutFailuresPersistSafeClassificationThroughReport(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-terminal-fanout-diagnostics")
	parentPayload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
	source.outcomes = []event.Outcome{matchingOutcome(dispatch.command, "parent-diagnostics", event.OutcomeSucceeded, 1, parentPayload)}
	if _, err := runtime.Resume(t.Context(), "run-terminal-fanout-diagnostics"); err == nil {
		t.Fatal("expected child join to remain waiting")
	}
	record, err := env.store.Load(t.Context(), "run-terminal-fanout-diagnostics")
	if err != nil || record.DelegationJoin == nil {
		t.Fatalf("load join: %#v err=%v", record.DelegationJoin, err)
	}
	for i := range record.DelegationJoin.Children {
		child := &record.DelegationJoin.Children[i]
		if i < 2 {
			child.Retry.MaxRecoveries = 0 // exercise terminal reporting at the configured recovery limit.
		}
	}
	if _, err := env.store.Checkpoint(t.Context(), record.Revision, record, nil); err != nil {
		t.Fatal(err)
	}
	var outcomes []event.Outcome
	for i, child := range record.DelegationJoin.Children {
		command := event.Command{EventID: child.CommandEventID, RunID: record.State.RunID, TaskID: child.ChildID,
			DelegationID: child.DelegationID, JoinID: child.JoinID, Lane: string(child.Lane),
			CorrelationID: child.CorrelationID, IdempotencyKey: child.DeliveryKey}
		var payload []byte
		if i < 2 {
			field, expected := "task_breakdown", "a JSON array of strings"
			if i == 1 {
				failure := ClassifyRoleExecutionFailure(&subagent.SafeFailureError{Code: "invalid_input"})
				failure.DeliveryAttemptID, failure.AttemptID = child.DeliveryAttemptID, child.Retry.AttemptID
				failure.CorrectionField, failure.ExpectedType = "SECRET_CANARY_APIKEY", "TOKEN_SENTINEL"
				failure.AllowedValues = []string{"VALUE_SENTINEL"}
				payload, _ = json.Marshal(GraphRoleOutcome{Failure: &failure})
				outcomes = append(outcomes, matchingOutcome(command, "child-diagnostic-"+string(child.Lane), event.OutcomeFailed, 1, payload))
				continue
			}
			failure := RoleExecutionFailure{DeliveryAttemptID: child.DeliveryAttemptID, AttemptID: child.Retry.AttemptID,
				Class: "schema_correction", Code: "invalid_field_type", CorrectionField: field,
				ExpectedType: expected, AllowedValues: []string{"VALUE_SENTINEL"}, Retryable: true}
			payload, _ = json.Marshal(GraphRoleOutcome{Failure: &failure})
		} else {
			payload, _ = json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1}})
		}
		outcomes = append(outcomes, matchingOutcome(command, "child-diagnostic-"+string(child.Lane), func() event.OutcomeStatus {
			if i < 2 {
				return event.OutcomeFailed
			}
			return event.OutcomeSucceeded
		}(), 1, payload))
	}
	for i, outcome := range outcomes {
		source.outcomes = []event.Outcome{outcome}
		_, resumeErr := runtime.Resume(t.Context(), "run-terminal-fanout-diagnostics")
		if resumeErr == nil {
			t.Fatalf("child %d unexpectedly completed the run", i)
		}
	}
	record, err = env.store.Load(t.Context(), "run-terminal-fanout-diagnostics")
	if err != nil || record.Failure == nil || record.DelegationJoin != nil || len(record.Failure.FanOutFailures) != 2 {
		t.Fatalf("terminal diagnostics were not retained: failure=%#v join=%#v err=%v", record.Failure, record.DelegationJoin, err)
	}
	if got := record.Failure.FanOutFailures[0].Failure; got.Class != "schema_correction" || got.Code != "invalid_field_type" || got.AttemptID == "" ||
		got.CorrectionField != "task_breakdown" || got.ExpectedType != "a JSON array of strings" || got.RecoveryNumber != 0 || got.MaxRecoveries != 0 || !got.Retryable || got.Reason != "recovery_limit_reached" {
		t.Fatalf("safe field/type or retry facts missing: %#v", got)
	}
	if got := record.Failure.FanOutFailures[1].Failure; got.Class != "tool" || got.Code != "invalid_input" || got.Retryable || got.Reason != "not_retryable" ||
		got.CorrectionField != "" || got.ExpectedType != "" || len(got.AllowedValues) != 0 {
		t.Fatalf("untrusted schema identifiers were retained: %#v", got)
	}
	childEvents, err := env.events.Query(t.Context(), event.EventFilter{RunID: record.State.RunID, Type: event.EventMultiAgentFanoutChildCompleted})
	if err != nil || len(childEvents) != 3 {
		t.Fatalf("child events=%d err=%v", len(childEvents), err)
	}
	// The public report is assembled from the same canonical event projection
	// used by the run-report endpoint.
	report := BuildReferenceRunReport(ReferenceWorkflowInput{Objective: "diagnostic test"}, record.State, childEvents)
	if len(report.FanOut) != 3 {
		t.Fatalf("report fanout=%#v", report.FanOut)
	}
	foundSpecific, foundUnknown := false, false
	for _, lane := range report.FanOut {
		if lane.CorrectionField == "task_breakdown" && lane.ExpectedType == "a JSON array of strings" {
			foundSpecific = lane.FailureClass == "schema_correction" && lane.FailureCode == "invalid_field_type" &&
				lane.AttemptID != "" && lane.Retryable && lane.FailureReason == "recovery_limit_reached" && lane.RecoveryNumber == 0
		}
		if lane.CorrectionField == "" && lane.ExpectedType == "" && lane.Status == string(event.OutcomeFailed) {
			foundUnknown = true
		}
	}
	if !foundSpecific || !foundUnknown {
		t.Fatalf("report omitted safe diagnostics: %#v", report.FanOut)
	}
	serialized, err := json.Marshal(struct {
		Failure *PersistedFailure
		Events  []event.Event
		Report  ReferenceRunReport
	}{record.Failure, childEvents, report})
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{"SECRET_CANARY_APIKEY", "TOKEN_SENTINEL", "VALUE_SENTINEL"} {
		if strings.Contains(string(serialized), sentinel) {
			t.Fatalf("unsafe value leaked into durable diagnostics: %s", sentinel)
		}
	}
	if !strings.Contains(string(serialized), `"retryable":false`) ||
		!strings.Contains(string(serialized), `"recovery_number":0`) ||
		!strings.Contains(string(serialized), `"max_recoveries":0`) {
		t.Fatalf("terminal report omitted explicit zero/false recovery facts: %s", serialized)
	}
}

func TestCapabilityFailureIsTerminalWithoutRetryOrReasonLeak(t *testing.T) {
	const runID = "run-capability-terminal"
	const privateReason = "PRIVATE_CAPABILITY_REASON_CANARY"
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, runID)
	parentPayload, err := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = []event.Outcome{matchingOutcome(dispatch.command, "capability-parent", event.OutcomeSucceeded, 1, parentPayload)}
	if _, err := runtime.Resume(t.Context(), runID); err == nil {
		t.Fatal("expected fan-out join to remain waiting")
	}
	source.outcomes = nil
	dispatchCount := len(dispatch.commands)
	record, err := env.store.Load(t.Context(), runID)
	if err != nil || record.DelegationJoin == nil || len(record.DelegationJoin.Children) != 3 {
		t.Fatalf("load fan-out join: join=%#v err=%v", record.DelegationJoin, err)
	}
	child := record.DelegationJoin.Children[0]
	command := event.Command{EventID: child.CommandEventID, RunID: record.State.RunID, TaskID: child.ChildID,
		DelegationID: child.DelegationID, JoinID: child.JoinID, Lane: string(child.Lane),
		CorrelationID: child.CorrelationID, IdempotencyKey: child.DeliveryKey}
	failure := ClassifyRoleExecutionFailure(&GovernanceError{Kind: "capability", Reason: privateReason})
	failure.DeliveryAttemptID, failure.AttemptID = child.DeliveryAttemptID, child.Retry.AttemptID
	payload, err := json.Marshal(GraphRoleOutcome{Failure: &failure})
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = []event.Outcome{matchingOutcome(command, "capability-terminal", event.OutcomeFailed, 1, payload)}
	if _, err := runtime.Resume(t.Context(), runID); err == nil {
		t.Fatal("expected join to remain waiting for the other lanes")
	}
	updated, err := env.store.Load(t.Context(), runID)
	if err != nil || updated.DelegationJoin == nil {
		t.Fatalf("load updated join: %#v err=%v", updated.DelegationJoin, err)
	}
	failed := updated.DelegationJoin.Children[0]
	if failed.Status != event.OutcomeFailed || failed.DispatchPending || failed.Retry.RecoveryNumber != 0 ||
		!failed.Retry.NextAt.IsZero() || failed.Retry.FailureClass != "governance" || failed.Retry.FailureCode != "capability" {
		t.Fatalf("capability failure was not terminal/non-retryable: %#v", failed)
	}
	if len(dispatch.commands) != dispatchCount {
		t.Fatalf("terminal failure caused a replacement dispatch: %d commands", len(dispatch.commands))
	}
	retries, err := env.events.Query(t.Context(), event.EventFilter{RunID: runID, Type: event.EventMultiAgentRetryScheduled})
	if err != nil || len(retries) != 0 {
		t.Fatalf("unexpected retry events=%d err=%v", len(retries), err)
	}
	serialized, err := json.Marshal(updated)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), privateReason) {
		t.Fatal("private governance reason leaked into durable parent state")
	}
}

func TestDelegationJoinConsumesTerminalBeforeExpiry(t *testing.T) {
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	clock := now
	env := newDurableTestEnvironment(t)
	dispatch := &recordingDelegationDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store}
	runtime := newPlannerDelegationRuntime(t, env, dispatch, source, func() time.Time { return clock })
	if _, err := runtime.Run(t.Context(), RunRequest{RunID: "run-graph-delegation", Task: TaskReference{ID: "parent-task", Description: "delegate"}}); err == nil {
		t.Fatal("Run unexpectedly completed")
	}
	payload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
	parentOutcome := matchingOutcome(dispatch.command, "parent-fanout", event.OutcomeSucceeded, 1, payload)
	parentOutcome.OccurredAt = now.Add(30 * time.Second)
	source.outcomes = []event.Outcome{parentOutcome}
	if _, err := runtime.Resume(t.Context(), "run-graph-delegation"); err == nil {
		t.Fatal("expected join wait")
	}
	record, err := env.store.Load(t.Context(), "run-graph-delegation")
	if err != nil {
		t.Fatal(err)
	}
	child := record.DelegationJoin.Children[0]
	childPayload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1}})
	childOutcome := matchingOutcome(event.Command{EventID: child.CommandEventID, RunID: record.State.RunID, TaskID: child.ChildID, DelegationID: child.DelegationID, JoinID: child.JoinID, Lane: string(child.Lane), CorrelationID: child.CorrelationID, IdempotencyKey: child.DeliveryKey}, "child-terminal", event.OutcomeSucceeded, 1, childPayload)
	childOutcome.OccurredAt = now.Add(30 * time.Second)
	source.outcomes = []event.Outcome{childOutcome}
	clock = clock.Add(2 * time.Minute)
	if _, err := runtime.Resume(t.Context(), "run-graph-delegation"); err == nil {
		t.Fatal("missing children should finalize the expired join")
	}
	record, err = env.store.Load(t.Context(), "run-graph-delegation")
	if err != nil || record.Failure == nil || record.Failure.Kind != "delegation_timeout" {
		t.Fatalf("join did not finalize at expiry: %#v err=%v", record, err)
	}
}

func TestDelegationJoinConsumesAllChildrenAndConvergesParent(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-graph-delegation")
	payload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1, FanOut: validFanOutPlan()}})
	source.outcomes = append(source.outcomes, matchingOutcome(dispatch.command, "parent-fanout", event.OutcomeSucceeded, 1, payload))
	if _, err := runtime.Resume(t.Context(), "run-graph-delegation"); err == nil {
		t.Fatal("expected join wait after parent result")
	}
	record, err := env.store.Load(t.Context(), "run-graph-delegation")
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = nil
	for i, child := range record.DelegationJoin.Children {
		childPayload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1}})
		source.outcomes = append(source.outcomes, matchingOutcome(event.Command{EventID: child.CommandEventID, RunID: record.State.RunID, TaskID: child.ChildID, DelegationID: child.DelegationID, JoinID: child.JoinID, Lane: string(child.Lane), CorrelationID: child.CorrelationID, IdempotencyKey: child.DeliveryKey}, "child-"+string(rune('a'+i)), event.OutcomeSucceeded, 1, childPayload))
	}
	state, err := runtime.Resume(t.Context(), "run-graph-delegation")
	if err != nil || state.Status == RunStatusPaused {
		t.Fatalf("join convergence state=%#v err=%v", state, err)
	}
	record, err = env.store.Load(t.Context(), "run-graph-delegation")
	if err != nil {
		t.Fatal(err)
	}
	if record.DelegationJoin != nil || record.Waiting != nil {
		t.Fatalf("join remained after convergence: %#v", record)
	}
}

func TestDelegationJoinValidateRejectsDuplicateLaneAndMismatchedJoin(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-graph-delegation")
	payload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1, FanOut: validFanOutPlan()}})
	source.outcomes = append(source.outcomes, matchingOutcome(dispatch.command, "parent-fanout", event.OutcomeSucceeded, 1, payload))
	if _, err := runtime.Resume(t.Context(), "run-graph-delegation"); err == nil {
		t.Fatal("expected join wait")
	}
	record, err := env.store.Load(t.Context(), "run-graph-delegation")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := record
	duplicate.DelegationJoin = cloneDelegationJoin(record.DelegationJoin)
	duplicate.DelegationJoin.Children[1].Lane = duplicate.DelegationJoin.Children[0].Lane
	if err := duplicate.Validate(runtime.supervisor.graph); err == nil {
		t.Fatal("duplicate lane accepted")
	}
	mismatch := record
	mismatch.DelegationJoin = cloneDelegationJoin(record.DelegationJoin)
	mismatch.DelegationJoin.Children[0].JoinID = "join:forged"
	if err := mismatch.Validate(runtime.supervisor.graph); err == nil {
		t.Fatal("mismatched child join accepted")
	}
}

func TestDelegationJoinRoutesChildProposalToParentApproval(t *testing.T) {
	runtime, env, dispatch, source := newWaitingPlannerDelegationRuntime(t, "run-graph-delegation")
	parentPayload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1, FanOut: validFanOutPlan()}})
	source.outcomes = append(source.outcomes, matchingOutcome(dispatch.command, "parent-fanout", event.OutcomeSucceeded, 1, parentPayload))
	if _, err := runtime.Resume(t.Context(), "run-graph-delegation"); err == nil {
		t.Fatal("expected join wait")
	}
	record, err := env.store.Load(t.Context(), "run-graph-delegation")
	if err != nil {
		t.Fatal(err)
	}
	source.outcomes = nil
	for i, child := range record.DelegationJoin.Children {
		result := RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1}
		if child.Lane == FanOutImplementation {
			result.Proposals = []ProposalReference{{ProposalID: "child-proposal", ApprovalID: "child-approval"}}
		}
		childPayload, _ := json.Marshal(GraphRoleOutcome{Result: result})
		cmd := event.Command{EventID: child.CommandEventID, RunID: record.State.RunID, TaskID: child.ChildID, DelegationID: child.DelegationID, JoinID: child.JoinID, Lane: string(child.Lane), CorrelationID: child.CorrelationID, IdempotencyKey: child.DeliveryKey}
		source.outcomes = append(source.outcomes, matchingOutcome(cmd, "child-proposal-"+string(rune('a'+i)), event.OutcomeSucceeded, 1, childPayload))
	}
	state, err := runtime.Resume(t.Context(), "run-graph-delegation")
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) || state.Status != RunStatusPaused {
		t.Fatalf("proposal state=%#v err=%v", state, err)
	}
	record, err = env.store.Load(t.Context(), "run-graph-delegation")
	if err != nil || record.Waiting == nil || record.Waiting.Kind != "proposal_approval" || record.ApprovedTask == nil || len(record.ApprovedTask.Proposals) != 1 {
		t.Fatalf("parent proposal checkpoint=%#v err=%v", record, err)
	}
}

func cloneDelegationJoin(join *DelegationJoinState) *DelegationJoinState {
	if join == nil {
		return nil
	}
	cloned := *join
	cloned.Children = append([]DelegationJoinChild(nil), join.Children...)
	return &cloned
}
