// Package tool provides plan management tools for the V32 Plan-First Pipeline.
package tool

import (
	"context"
	"fmt"

	"github.com/emaharmony/prizm/internal/plan"
)

// PlanManager interface for tool access to plan state.
// The Executor provides the real implementation; tests can mock it.
type PlanManager interface {
	CreatePlan(p plan.Plan) error
	LoadPlans() ([]plan.Plan, error)
	ApprovePlan(id, approvedBy string) error
	CompletePlan(id string) error
	AbandonPlan(id string) error
	UpdatePlan(id string, updates map[string]any) error
	UpdateStepStatus(planID, stepID string, status plan.StepStatus, notes string) error
	AddStep(planID, title string) error
	ReopenPlan(id string) error
}

// --- PlanCreate ---

type PlanCreateTool struct {
	Mgr PlanManager
}

func (t *PlanCreateTool) Name() string { return "plan_create" }
func (t *PlanCreateTool) Description() string {
	return "Create a new task plan. Every code change should have a plan first. The approval level is determined automatically based on the scope — bugs and improvements auto-proceed, architecture changes require approval."
}
func (t *PlanCreateTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"title":        {Type: "string", Description: "Short description of what you're doing", Required: true},
			"description":  {Type: "string", Description: "Full description of the task"},
			"reasoning":    {Type: "string", Description: "Why you're doing this"},
			"scope":        {Type: "string", Description: "What is explicitly OUT of scope"},
			"steps":        {Type: "array", Description: "Ordered checklist of steps. Each step is a string describing what to do."},
			"deliverables": {Type: "array", Description: "Expected outputs"},
		},
		Output: ParamSpec{Type: "string", Description: "Confirmation with plan ID, steps, and approval level"},
	}
}
func (t *PlanCreateTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	title, _ := getString(input, "title")
	if title == "" {
		return ToolResult{Success: false, Error: "title is required"}, nil
	}

	description, _ := getString(input, "description")
	reasoning, _ := getString(input, "reasoning")
	scope, _ := getString(input, "scope")

	var deliverables []string
	if d, ok := input["deliverables"].([]any); ok {
		for _, v := range d {
			if s, ok := v.(string); ok {
				deliverables = append(deliverables, s)
			}
		}
	}

	var steps []plan.Step
	if s, ok := input["steps"].([]any); ok {
		for i, v := range s {
			if title, ok := v.(string); ok {
				steps = append(steps, plan.Step{
					ID:     fmt.Sprintf("S%d", i+1),
					Title:  title,
					Status: plan.StepPending,
				})
			}
		}
	}

	// Determine approval level
	approvalLevel := plan.NeedsApproval(title, scope)

	p := plan.Plan{
		Title:         title,
		Description:   description,
		Reasoning:     reasoning,
		Scope:         scope,
		Steps:         steps,
		Deliverables:  deliverables,
		ApprovalLevel: approvalLevel,
	}

	if err := t.Mgr.CreatePlan(p); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to create plan: %v", err)}, nil
	}

	// Reload to get auto-generated ID
	plans, err := t.Mgr.LoadPlans()
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to reload plans: %v", err)}, nil
	}
	created := plans[len(plans)-1]
	p.ID = created.ID
	p.Status = created.Status
	p.ApprovalLevel = created.ApprovalLevel

	status := "auto_proceed"
	if approvalLevel == plan.ApprovalRequired || approvalLevel == plan.ApprovalCritical {
		status = "pending_approval"
	}

	completed, total := plan.StepProgress(&p)
	msg := fmt.Sprintf("Plan created: %s — %s (approval: %s, status: %s)", p.ID, p.Title, approvalLevel, status)
	if status == "auto_proceed" {
		msg += " — PROCEED WITH EXECUTION NOW. Do not ask for confirmation."
	} else {
		msg += fmt.Sprintf(" — WAITING FOR APPROVAL. Inform the user: 'Plan %s needs approval. Reply approve %s to proceed.'", p.ID, p.ID)
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"plan_id":        p.ID,
			"title":          p.Title,
			"status":         status,
			"approval_level": string(approvalLevel),
			"steps":           total,
			"steps_completed":  completed,
			"message":        msg,
		},
	}, nil
}

// --- PlanList ---

type PlanListTool struct {
	Mgr PlanManager
}

func (t *PlanListTool) Name() string { return "plan_list" }
func (t *PlanListTool) Description() string {
	return "List all task plans. Shows ID, title, status, and approval level."
}
func (t *PlanListTool) Schema() ToolSchema {
	return ToolSchema{
		Input:  map[string]ParamSpec{},
		Output: ParamSpec{Type: "string", Description: "List of all plans"},
	}
}
func (t *PlanListTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	plans, err := t.Mgr.LoadPlans()
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to load plans: %v", err)}, nil
	}

	if len(plans) == 0 {
		return ToolResult{
			Success: true,
			Output: map[string]any{
				"message": "No plans found. Create one with plan_create.",
				"count":   0,
			},
		}, nil
	}

	var result string
	for _, p := range plans {
		completed, total := plan.StepProgress(&p)
		result += fmt.Sprintf("- %s: %s [%s] (approval: %s, progress: %d/%d)\n", p.ID, p.Title, p.Status, p.ApprovalLevel, completed, total)
		for _, s := range p.Steps {
			check := "[ ]"
			if s.Status == plan.StepCompleted {
				check = "[x]"
			} else if s.Status == plan.StepInProgress {
				check = "[~]"
			} else if s.Status == plan.StepBlocked {
				check = "[!]"
			} else if s.Status == plan.StepSkipped {
				check = "[-]"
			}
			result += fmt.Sprintf("  %s %s: %s\n", check, s.ID, s.Title)
		}
	}
	return ToolResult{
		Success: true,
		Output: map[string]any{
			"message": result,
			"count":   len(plans),
		},
	}, nil
}

// --- PlanApprove ---

type PlanApproveTool struct {
	Mgr PlanManager
}

func (t *PlanApproveTool) Name() string { return "plan_approve" }
func (t *PlanApproveTool) Description() string {
	return "Approve a plan so code execution can proceed. Only use for plans that require approval (architecture, direction changes)."
}
func (t *PlanApproveTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"id":          {Type: "string", Description: "Plan ID (e.g., P-001)", Required: true},
			"approved_by": {Type: "string", Description: "Who approved this plan"},
		},
		Output: ParamSpec{Type: "string", Description: "Confirmation message"},
	}
}
func (t *PlanApproveTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	id, _ := getString(input, "id")
	if id == "" {
		return ToolResult{Success: false, Error: "id is required"}, nil
	}

	approvedBy, _ := getString(input, "approved_by")
	if approvedBy == "" {
		approvedBy = "agent"
	}

	if err := t.Mgr.ApprovePlan(id, approvedBy); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to approve plan: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"plan_id": id,
			"message": fmt.Sprintf("Plan %s approved by %s", id, approvedBy),
		},
	}, nil
}

// --- PlanComplete ---

type PlanCompleteTool struct {
	Mgr PlanManager
}

func (t *PlanCompleteTool) Name() string { return "plan_complete" }
func (t *PlanCompleteTool) Description() string {
	return "Mark a plan as completed. Use when all deliverables are done."
}
func (t *PlanCompleteTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"id": {Type: "string", Description: "Plan ID (e.g., P-001)", Required: true},
		},
		Output: ParamSpec{Type: "string", Description: "Confirmation message"},
	}
}
func (t *PlanCompleteTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	id, _ := getString(input, "id")
	if id == "" {
		return ToolResult{Success: false, Error: "id is required"}, nil
	}

	if err := t.Mgr.CompletePlan(id); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to complete plan: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"plan_id": id,
			"message": fmt.Sprintf("Plan %s completed", id),
		},
	}, nil
}

// --- PlanAbandon ---

type PlanAbandonTool struct {
	Mgr PlanManager
}

func (t *PlanAbandonTool) Name() string { return "plan_abandon" }
func (t *PlanAbandonTool) Description() string {
	return "Mark a plan as abandoned (no longer pursuing). Use when scope or direction changes."
}
func (t *PlanAbandonTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"id": {Type: "string", Description: "Plan ID (e.g., P-001)", Required: true},
		},
		Output: ParamSpec{Type: "string", Description: "Confirmation message"},
	}
}
func (t *PlanAbandonTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	id, _ := getString(input, "id")
	if id == "" {
		return ToolResult{Success: false, Error: "id is required"}, nil
	}

	if err := t.Mgr.AbandonPlan(id); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to abandon plan: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"plan_id": id,
			"message": fmt.Sprintf("Plan %s abandoned", id),
		},
	}, nil
}

// --- PlanUpdate ---

type PlanUpdateTool struct {
	Mgr PlanManager
}

func (t *PlanUpdateTool) Name() string { return "plan_update" }
func (t *PlanUpdateTool) Description() string {
	return "Update a plan's metadata (title, description, branch, PR) or step statuses. Use plan_update with step_id and step_status to mark steps complete, in-progress, blocked, or skipped."
}
func (t *PlanUpdateTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"id":           {Type: "string", Description: "Plan ID (e.g., P-001)", Required: true},
			"title":        {Type: "string", Description: "Updated title"},
			"description":  {Type: "string", Description: "Updated description"},
			"reasoning":    {Type: "string", Description: "Updated reasoning"},
			"scope":        {Type: "string", Description: "Updated scope"},
			"branch":       {Type: "string", Description: "Git branch name"},
			"pr":           {Type: "string", Description: "PR number"},
			"step_id":      {Type: "string", Description: "Step ID to update (e.g., S1)"},
			"step_status":  {Type: "string", Description: "New status for the step: pending, in_progress, completed, blocked, skipped"},
			"step_notes":   {Type: "string", Description: "Notes for the step"},
			"add_step":     {Type: "string", Description: "Title of a new step to add"},
		},
		Output: ParamSpec{Type: "string", Description: "Updated plan summary"},
	}
}
func (t *PlanUpdateTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	id, _ := getString(input, "id")
	if id == "" {
		return ToolResult{Success: false, Error: "id is required"}, nil
	}

	// Step status update
	if stepID, _ := getString(input, "step_id"); stepID != "" {
		stepStatus, _ := getString(input, "step_status")
		stepNotes, _ := getString(input, "step_notes")
		if stepStatus == "" {
			stepStatus = string(plan.StepCompleted) // default to completed
		}
		if err := t.Mgr.UpdateStepStatus(id, stepID, plan.StepStatus(stepStatus), stepNotes); err != nil {
			return ToolResult{Success: false, Error: fmt.Sprintf("failed to update step: %v", err)}, nil
		}
		return ToolResult{
			Success: true,
			Output: map[string]any{
				"plan_id":  id,
				"step_id":  stepID,
				"status":   stepStatus,
				"message":  fmt.Sprintf("Step %s of plan %s updated to %s", stepID, id, stepStatus),
			},
		}, nil
	}

	// Add step
	if addStep, _ := getString(input, "add_step"); addStep != "" {
		if err := t.Mgr.AddStep(id, addStep); err != nil {
			return ToolResult{Success: false, Error: fmt.Sprintf("failed to add step: %v", err)}, nil
		}
		return ToolResult{
			Success: true,
			Output: map[string]any{
				"plan_id": id,
				"message":  fmt.Sprintf("Added step to plan %s: %s", id, addStep),
			},
		}, nil
	}

	// Plan metadata update
	updates := map[string]any{}
	if title, _ := getString(input, "title"); title != "" {
		updates["title"] = title
	}
	if desc, _ := getString(input, "description"); desc != "" {
		updates["description"] = desc
	}
	if reasoning, _ := getString(input, "reasoning"); reasoning != "" {
		updates["reasoning"] = reasoning
	}
	if scope, _ := getString(input, "scope"); scope != "" {
		updates["scope"] = scope
	}
	if branch, _ := getString(input, "branch"); branch != "" {
		updates["branch"] = branch
	}
	if pr, _ := getString(input, "pr"); pr != "" {
		updates["pr"] = pr
	}

	if len(updates) == 0 {
		return ToolResult{Success: false, Error: "no fields to update — provide at least one of: title, description, reasoning, scope, branch, pr, step_id, or add_step"}, nil
	}

	if err := t.Mgr.UpdatePlan(id, updates); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to update plan: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"plan_id": id,
			"updated":  updates,
			"message":  fmt.Sprintf("Plan %s updated: %v", id, updates),
		},
	}, nil
}

// --- PlanReopen ---

type PlanReopenTool struct {
	Mgr PlanManager
}

func (t *PlanReopenTool) Name() string { return "plan_reopen" }
func (t *PlanReopenTool) Description() string {
	return "Reopen a completed or abandoned plan so work can resume. Sets status to auto_proceed."
}
func (t *PlanReopenTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"id": {Type: "string", Description: "Plan ID (e.g., P-001)", Required: true},
		},
		Output: ParamSpec{Type: "string", Description: "Confirmation message"},
	}
}
func (t *PlanReopenTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	id, _ := getString(input, "id")
	if id == "" {
		return ToolResult{Success: false, Error: "id is required"}, nil
	}

	if err := t.Mgr.ReopenPlan(id); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to reopen plan: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"plan_id": id,
			"message":  fmt.Sprintf("Plan %s reopened — work can resume", id),
		},
	}, nil
}

// RegisterPlanTools registers all plan tools with the tool registry.
func RegisterPlanTools(registry *Registry, planMgr *plan.Manager) {
	mgr := &planManagerAdapter{Mgr: planMgr}
	registry.Register(&PlanCreateTool{Mgr: mgr})
	registry.Register(&PlanListTool{Mgr: mgr})
	registry.Register(&PlanApproveTool{Mgr: mgr})
	registry.Register(&PlanCompleteTool{Mgr: mgr})
	registry.Register(&PlanAbandonTool{Mgr: mgr})
	registry.Register(&PlanUpdateTool{Mgr: mgr})
	registry.Register(&PlanReopenTool{Mgr: mgr})
}

// planManagerAdapter wraps plan.Manager to satisfy the PlanManager interface.
type planManagerAdapter struct {
	Mgr *plan.Manager
}

func (a *planManagerAdapter) CreatePlan(p plan.Plan) error                { return a.Mgr.CreatePlan(p) }
func (a *planManagerAdapter) LoadPlans() ([]plan.Plan, error)             { return a.Mgr.LoadPlans() }
func (a *planManagerAdapter) ApprovePlan(id, by string) error              { return a.Mgr.ApprovePlan(id, by) }
func (a *planManagerAdapter) CompletePlan(id string) error                { return a.Mgr.CompletePlan(id) }
func (a *planManagerAdapter) AbandonPlan(id string) error                 { return a.Mgr.AbandonPlan(id) }
func (a *planManagerAdapter) UpdatePlan(id string, updates map[string]any) error { return a.Mgr.UpdatePlan(id, updates) }
func (a *planManagerAdapter) UpdateStepStatus(planID, stepID string, status plan.StepStatus, notes string) error { return a.Mgr.UpdateStepStatus(planID, stepID, status, notes) }
func (a *planManagerAdapter) AddStep(planID, title string) error         { return a.Mgr.AddStep(planID, title) }
func (a *planManagerAdapter) ReopenPlan(id string) error                  { return a.Mgr.ReopenPlan(id) }
