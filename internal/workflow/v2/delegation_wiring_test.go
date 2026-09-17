package v2

import (
	"context"
	"strings"
	"testing"
	"time"
)

func delegConfig() *WorkflowConfig {
	return &WorkflowConfig{
		Name: "deleg", Version: 1,
		Global: GlobalConfig{MaxTotalIterations: 20, MaxRepeatedToolCalls: 6},
		Phases: []PhaseConfig{{
			Name: "EXECUTION", Type: "execution", MaxIterations: 8,
			AllowedTools: []string{"delegate", "write_file"},
			Gate:         GateConfig{Type: "task_completion", Mode: "all_tasks"},
		}},
	}
}

func TestHandleDelegateHappyPath(t *testing.T) {
	dm := NewDelegationManager("prizm.agent.x", "prizm.agent.x.complete")
	engine := NewEngine(delegConfig(), nil, dm)
	engine.GetState().Plan = &PlanGraph{Tasks: []PlanTask{{ID: "T2", Description: "build the thing", Agent: "mango", Status: "pending"}}}

	var published *TaskPacket
	engine.SetTaskPublisher(func(p TaskPacket) error { published = &p; return nil })

	msg := engine.handleDelegate(context.Background(), &ToolRequest{Tool: "delegate", Input: map[string]any{"task_id": "T2"}})
	if !strings.Contains(msg, "dispatched") {
		t.Fatalf("expected dispatched ack, got %q", msg)
	}
	if published == nil || published.TaskID != "T2" || published.TargetAgent != "mango" {
		t.Fatalf("publisher did not receive the right packet: %+v", published)
	}
	st := engine.GetState()
	if len(st.Delegations) != 1 || st.Delegations[0].TaskID != "T2" {
		t.Fatalf("delegation not recorded: %+v", st.Delegations)
	}
	if st.Plan.Tasks[0].Status != "in_progress" {
		t.Fatalf("task should be in_progress after delegation, got %q", st.Plan.Tasks[0].Status)
	}
}

func TestHandleDelegateGuards(t *testing.T) {
	// No manager.
	e0 := NewEngine(delegConfig(), nil, nil)
	if msg := e0.handleDelegate(context.Background(), &ToolRequest{Tool: "delegate"}); !strings.Contains(msg, "not available") {
		t.Fatalf("expected not-available, got %q", msg)
	}

	dm := NewDelegationManager("s", "s.complete")
	e := NewEngine(delegConfig(), nil, dm)
	if msg := e.handleDelegate(context.Background(), &ToolRequest{Tool: "delegate"}); !strings.Contains(msg, "no plan") {
		t.Fatalf("expected no-plan, got %q", msg)
	}
	e.GetState().Plan = &PlanGraph{Tasks: []PlanTask{{ID: "T1", Agent: "mango"}, {ID: "T2"}}}
	if msg := e.handleDelegate(context.Background(), &ToolRequest{Tool: "delegate"}); !strings.Contains(msg, "task_id") {
		t.Fatalf("expected task_id required, got %q", msg)
	}
	if msg := e.handleDelegate(context.Background(), &ToolRequest{Tool: "delegate", Input: map[string]any{"task_id": "ZZ"}}); !strings.Contains(msg, "no task") {
		t.Fatalf("expected unknown-task, got %q", msg)
	}
	if msg := e.handleDelegate(context.Background(), &ToolRequest{Tool: "delegate", Input: map[string]any{"task_id": "T2"}}); !strings.Contains(msg, "no agent") {
		t.Fatalf("expected no-agent, got %q", msg)
	}
}

// End-to-end: the model delegates a task, its completion arrives as an external
// event, and the task_completion gate then passes.
func TestDriveDelegatesAndCompletes(t *testing.T) {
	dm := NewDelegationManager("prizm.agent.x", "prizm.agent.x.complete")
	engine := NewEngine(delegConfig(), nil, dm)
	engine.GetState().Plan = &PlanGraph{Tasks: []PlanTask{{ID: "T2", Description: "x", Agent: "mango", Status: "pending"}}}
	ch := engine.GetExternalEventChannel()

	step := 0
	llm := func(_ context.Context, _ []Message) (string, int, int, error) {
		step++
		if step == 1 {
			// Queue the completion; it is drained at the top of the next iteration,
			// after this delegate call is processed.
			ch <- ExternalEvent{Type: "task_complete", Source: "test", Data: map[string]any{"task_id": "T2", "status": "completed"}}
			return `{"type":"tool_request","tool":"delegate","input":{"task_id":"T2"}}`, 1, 1, nil
		}
		return `{"type":"final","content":"done"}`, 1, 1, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := engine.Drive(ctx, llm, noopTool, DriveOptions{SystemPrompt: "s", UserPrompt: "u", SkipPushRequirement: true})
	if err != nil {
		t.Fatalf("Drive error: %v", err)
	}
	if state.Plan.Tasks[0].Status != "completed" {
		t.Fatalf("delegated task should be completed, got %q", state.Plan.Tasks[0].Status)
	}
	if len(state.Delegations) != 1 || state.Delegations[0].Status != "completed" {
		t.Fatalf("delegation should be closed out: %+v", state.Delegations)
	}
	if ps := state.PhaseStates["EXECUTION"]; ps == nil || ps.Status != PhaseStatusCompleted {
		t.Fatalf("EXECUTION should complete after delegated task done: %+v", ps)
	}
}

func TestFailDelegatedTask(t *testing.T) {
	st := NewWorkflowState(&WorkflowConfig{Name: "x"})
	st.Plan = &PlanGraph{Tasks: []PlanTask{{ID: "T2", Agent: "mango", Status: "in_progress"}}}
	st.Delegations = []DelegationState{{DelegationID: "D1", TaskID: "T2", Agent: "mango", Status: "sent", SentAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}}
	st.FailDelegatedTask("T2")
	if st.Delegations[0].Status != "timed_out" {
		t.Fatalf("delegation should be timed_out, got %q", st.Delegations[0].Status)
	}
	if st.Plan.Tasks[0].Status != "failed" {
		t.Fatalf("task should be failed, got %q", st.Plan.Tasks[0].Status)
	}
}
