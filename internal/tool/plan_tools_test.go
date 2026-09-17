package tool

import (
	"context"
	"testing"

	"github.com/emaharmony/prizm/internal/plan"
)

func TestPlanCreateWithSteps(t *testing.T) {
	mgr := plan.NewManager(t.TempDir())
	tool := &PlanCreateTool{Mgr: &planManagerAdapter{Mgr: mgr}}

	result, err := tool.Execute(context.Background(), map[string]any{
		"title":       "Test plan with steps",
		"description": "Testing step support",
		"steps":       []any{"Research the problem", "Implement the fix", "Write tests", "Deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Error)
	}
	if result.Output["steps"] != 4 {
		t.Errorf("expected 4 steps, got %v", result.Output["steps"])
	}
	if result.Output["steps_completed"] != 0 {
		t.Errorf("expected 0 completed, got %v", result.Output["steps_completed"])
	}
}

func TestPlanUpdateStepStatus(t *testing.T) {
	mgr := plan.NewManager(t.TempDir())
	adapter := &planManagerAdapter{Mgr: mgr}
	createTool := &PlanCreateTool{Mgr: adapter}
	updateTool := &PlanUpdateTool{Mgr: adapter}

	// Create a plan with steps
	result, _ := createTool.Execute(context.Background(), map[string]any{
		"title": "Step update test",
		"steps": []any{"Step one", "Step two"},
	})
	planID := result.Output["plan_id"].(string)

	// Mark S1 as completed
	result, err := updateTool.Execute(context.Background(), map[string]any{
		"id":          planID,
		"step_id":     "S1",
		"step_status": "completed",
		"step_notes":  "done with testing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Error)
	}

	// Verify the step is marked completed
	plans, _ := mgr.LoadPlans()
	if plans[0].Steps[0].Status != plan.StepCompleted {
		t.Errorf("S1 status = %s, want completed", plans[0].Steps[0].Status)
	}
}

func TestPlanReopen(t *testing.T) {
	mgr := plan.NewManager(t.TempDir())
	adapter := &planManagerAdapter{Mgr: mgr}
	createTool := &PlanCreateTool{Mgr: adapter}
	completeTool := &PlanCompleteTool{Mgr: adapter}
	reopenTool := &PlanReopenTool{Mgr: adapter}

	result, _ := createTool.Execute(context.Background(), map[string]any{"title": "Reopen test"})
	planID := result.Output["plan_id"].(string)

	completeTool.Execute(context.Background(), map[string]any{"id": planID})

	plans, _ := mgr.LoadPlans()
	if plans[0].Status != plan.StatusCompleted {
		t.Fatalf("want completed, got %s", plans[0].Status)
	}

	reopenResult, err := reopenTool.Execute(context.Background(), map[string]any{"id": planID})
	if err != nil {
		t.Fatal(err)
	}
	if !reopenResult.Success {
		t.Fatalf("expected success, got: %s", reopenResult.Error)
	}

	plans, _ = mgr.LoadPlans()
	if plans[0].Status != plan.StatusAutoProceed {
		t.Errorf("want auto_proceed after reopen, got %s", plans[0].Status)
	}
}

func TestPlanAddStep(t *testing.T) {
	mgr := plan.NewManager(t.TempDir())
	adapter := &planManagerAdapter{Mgr: mgr}
	createTool := &PlanCreateTool{Mgr: adapter}
	updateTool := &PlanUpdateTool{Mgr: adapter}

	result, _ := createTool.Execute(context.Background(), map[string]any{"title": "Add step test"})
	planID := result.Output["plan_id"].(string)

	addResult, err := updateTool.Execute(context.Background(), map[string]any{
		"id":       planID,
		"add_step": "New step added later",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !addResult.Success {
		t.Fatalf("expected success, got: %s", addResult.Error)
	}

	plans, _ := mgr.LoadPlans()
	if len(plans[0].Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plans[0].Steps))
	}
	if plans[0].Steps[0].Title != "New step added later" {
		t.Errorf("step title = %s, want 'New step added later'", plans[0].Steps[0].Title)
	}
}