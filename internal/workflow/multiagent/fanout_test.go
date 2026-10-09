package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

type failOnceFanoutDispatcher struct {
	store  DurableRunStore
	failed bool
	cmds   []event.Command
}

func (d *failOnceFanoutDispatcher) Dispatch(ctx context.Context, _ string, cmd event.Command) error {
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
	runtime, env, dispatch, source := newWaitingDelegationRuntime(t, "run-graph-delegation")
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

func TestDelegationJoinRecoversPendingDispatchAfterRestart(t *testing.T) {
	env := newDurableTestEnvironment(t)
	dispatch := &failOnceFanoutDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store}
	runtime := newGraphDelegationRuntime(t, env, dispatch, source)
	_, err := runtime.Run(t.Context(), RunRequest{RunID: "run-graph-delegation", Task: TaskReference{ID: "parent-task", Description: "delegate"}})
	if err == nil {
		t.Fatal("Run unexpectedly completed")
	}
	payload, _ := json.Marshal(GraphRoleOutcome{Result: RoleRunResult{Outcome: OutcomePlanReady, LocalIterations: 1, FanOut: validFanOutPlan()}})
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
	source.outcomes = nil
	dispatch.failed = true
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
}

func TestDelegationJoinDeadlineEmitsTimedOutChildrenAndReport(t *testing.T) {
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	clock := now
	env := newDurableTestEnvironment(t)
	dispatch := &recordingDelegationDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store}
	runtime := newGraphDelegationRuntimeWithClock(t, env, dispatch, source, func() time.Time { return clock })
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
