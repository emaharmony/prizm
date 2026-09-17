// Package tool provides Prizm's state management tools — the actions an AI agent
// can use to manage its working state (active task, decisions, blocked items,
// and working context). These tools let the agent persist what it's doing
// across sessions, so it "wakes up knowing what it was doing."
//
// V32: Lumi Operating Environment — Phase 1
package tool

import (
	"context"
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/state"
)

// StateManager interface for tool access to working state.
// The Executor provides the real implementation; tests can mock it.
type StateManager interface {
	SaveActiveTask(task *state.ActiveTask) error
	LoadActiveTask() (*state.ActiveTask, error)
	ClearActiveTask() error
	RecordDecision(d state.Decision) error
	AddBlocked(item state.BlockedItem) error
	Unblock(id string) error
	SaveContext(ctx *state.WorkingContext) error
	LoadContext() (*state.WorkingContext, error)
}

// --- SetActiveTask ---

type SetActiveTaskTool struct {
	Mgr StateManager
}

func (t *SetActiveTaskTool) Name() string { return "set_active_task" }
func (t *SetActiveTaskTool) Description() string {
	return "Set the current active task. Use when starting new work or updating task status. This replaces any previous active task."
}
func (t *SetActiveTaskTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"task":   {Type: "string", Description: "What you're working on", Required: true},
			"plan":   {Type: "string", Description: "The plan — what you'll do, why, what you won't do"},
			"scope":  {Type: "string", Description: "Scope boundary — what's explicitly out of scope"},
			"branch": {Type: "string", Description: "Git branch name if applicable"},
			"pr":     {Type: "string", Description: "PR number if applicable"},
			"status": {Type: "string", Description: "Task status: planning, executing, reviewing, done, blocked"},
		},
		Output: ParamSpec{Type: "string", Description: "Confirmation message"},
	}
}
func (t *SetActiveTaskTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	task, _ := getString(input, "task")
	if task == "" {
		return ToolResult{Success: false, Error: "task is required"}, nil
	}

	status, _ := getString(input, "status")
	if status == "" {
		status = "executing"
	}

	// Preserve started_at if updating existing task
	existing, _ := t.Mgr.LoadActiveTask()
	startedAt := time.Now()
	if existing != nil && !existing.StartedAt.IsZero() {
		startedAt = existing.StartedAt
	}

	tk := &state.ActiveTask{
		Task:      task,
		Plan:      mustGetString(input, "plan"),
		Scope:     mustGetString(input, "scope"),
		Branch:    mustGetString(input, "branch"),
		PR:        mustGetString(input, "pr"),
		Status:    status,
		StartedAt: startedAt,
	}

	if err := t.Mgr.SaveActiveTask(tk); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("save active task: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output:  map[string]any{"message": fmt.Sprintf("Active task set: %q (status: %s)", task, status)},
	}, nil
}

// --- ClearActiveTask ---

type ClearActiveTaskTool struct {
	Mgr StateManager
}

func (t *ClearActiveTaskTool) Name() string { return "clear_active_task" }
func (t *ClearActiveTaskTool) Description() string {
	return "Clear the current active task. Use when work is complete or abandoned."
}
func (t *ClearActiveTaskTool) Schema() ToolSchema {
	return ToolSchema{
		Input:  map[string]ParamSpec{},
		Output: ParamSpec{Type: "string", Description: "Confirmation message"},
	}
}
func (t *ClearActiveTaskTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	if err := t.Mgr.ClearActiveTask(); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("clear active task: %v", err)}, nil
	}
	return ToolResult{Success: true, Output: map[string]any{"message": "Active task cleared."}}, nil
}

// --- RecordDecision ---

type RecordDecisionTool struct {
	Mgr StateManager
}

func (t *RecordDecisionTool) Name() string { return "record_decision" }
func (t *RecordDecisionTool) Description() string {
	return "Record a decision that was made, with reasoning. Use when a meaningful choice affects the project."
}
func (t *RecordDecisionTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"decision":     {Type: "string", Description: "What was decided", Required: true},
			"reasoning":    {Type: "string", Description: "Why this decision was made"},
			"alternatives": {Type: "string", Description: "What was considered and rejected"},
			"author":       {Type: "string", Description: "Who made the decision (lumi, ema, guard)"},
		},
		Output: ParamSpec{Type: "string", Description: "Confirmation with decision ID"},
	}
}
func (t *RecordDecisionTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	decision, _ := getString(input, "decision")
	if decision == "" {
		return ToolResult{Success: false, Error: "decision is required"}, nil
	}

	author, _ := getString(input, "author")
	if author == "" {
		author = "lumi"
	}

	d := state.Decision{
		Decision:     decision,
		Reasoning:    mustGetString(input, "reasoning"),
		Alternatives: mustGetString(input, "alternatives"),
		Author:       author,
	}

	if err := t.Mgr.RecordDecision(d); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("record decision: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output:  map[string]any{"message": fmt.Sprintf("Decision recorded: %q (by %s)", decision, author)},
	}, nil
}

// --- AddBlocked ---

type AddBlockedTool struct {
	Mgr StateManager
}

func (t *AddBlockedTool) Name() string { return "add_blocked" }
func (t *AddBlockedTool) Description() string {
	return "Add a blocked item — something waiting on external input. Use when you can't proceed without outside help."
}
func (t *AddBlockedTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"item":       {Type: "string", Description: "What's blocked", Required: true},
			"waiting_on": {Type: "string", Description: "What it's waiting for (ema approval, mango review, etc.)", Required: true},
			"task_ref":   {Type: "string", Description: "Reference to the active task or ticket"},
		},
		Output: ParamSpec{Type: "string", Description: "Confirmation with blocked item ID"},
	}
}
func (t *AddBlockedTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	item, _ := getString(input, "item")
	if item == "" {
		return ToolResult{Success: false, Error: "item is required"}, nil
	}
	waitingOn, _ := getString(input, "waiting_on")
	if waitingOn == "" {
		return ToolResult{Success: false, Error: "waiting_on is required"}, nil
	}

	b := state.BlockedItem{
		Item:      item,
		WaitingOn: waitingOn,
		TaskRef:   mustGetString(input, "task_ref"),
	}

	if err := t.Mgr.AddBlocked(b); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("add blocked: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output:  map[string]any{"message": fmt.Sprintf("Blocked item added: %q (waiting on: %s)", item, waitingOn)},
	}, nil
}

// --- Unblock ---

type UnblockTool struct {
	Mgr StateManager
}

func (t *UnblockTool) Name() string { return "unblock" }
func (t *UnblockTool) Description() string {
	return "Remove a blocked item by ID. Use when the blocker has been resolved."
}
func (t *UnblockTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"id": {Type: "string", Description: "The blocked item ID (e.g., B-001)", Required: true},
		},
		Output: ParamSpec{Type: "string", Description: "Confirmation message"},
	}
}
func (t *UnblockTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	id, _ := getString(input, "id")
	if id == "" {
		return ToolResult{Success: false, Error: "id is required"}, nil
	}

	if err := t.Mgr.Unblock(id); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("unblock: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output:  map[string]any{"message": fmt.Sprintf("Blocked item %s unblocked.", id)},
	}, nil
}

// --- UpdateContext ---

type UpdateContextTool struct {
	Mgr StateManager
}

func (t *UpdateContextTool) Name() string { return "update_context" }
func (t *UpdateContextTool) Description() string {
	return "Update the current working context. Use when branch, open files, PR, or notes change."
}
func (t *UpdateContextTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"branch":      {Type: "string", Description: "Current git branch"},
			"last_action": {Type: "string", Description: "What was just done"},
			"pr":          {Type: "string", Description: "Current PR number"},
			"notes":       {Type: "string", Description: "Freeform context notes"},
		},
		Output: ParamSpec{Type: "string", Description: "Confirmation message"},
	}
}
func (t *UpdateContextTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	// Load existing context to merge
	existing, _ := t.Mgr.LoadContext()
	if existing == nil {
		existing = &state.WorkingContext{}
	}

	// Only update non-empty fields
	if branch, ok := getString(input, "branch"); ok && branch != "" {
		existing.Branch = branch
	}
	if lastAction, ok := getString(input, "last_action"); ok && lastAction != "" {
		existing.LastAction = lastAction
		existing.LastActionAt = time.Now()
	}
	if pr, ok := getString(input, "pr"); ok && pr != "" {
		existing.PR = pr
	}
	if notes, ok := getString(input, "notes"); ok && notes != "" {
		existing.Notes = notes
	}

	if err := t.Mgr.SaveContext(existing); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("save context: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output:  map[string]any{"message": fmt.Sprintf("Working context updated (branch: %s, last action: %s)", existing.Branch, existing.LastAction)},
	}, nil
}

// --- RegisterStateTools adds V32 state management tools to the registry. ---
func RegisterStateTools(registry *Registry, mgr StateManager) {
	registry.Register(&SetActiveTaskTool{Mgr: mgr})
	registry.Register(&ClearActiveTaskTool{Mgr: mgr})
	registry.Register(&RecordDecisionTool{Mgr: mgr})
	registry.Register(&AddBlockedTool{Mgr: mgr})
	registry.Register(&UnblockTool{Mgr: mgr})
	registry.Register(&UpdateContextTool{Mgr: mgr})
}

// --- Helpers ---

func getString(input map[string]any, key string) (string, bool) {
	val, ok := input[key]
	if !ok || val == nil {
		return "", false
	}
	s, ok := val.(string)
	return s, ok
}

func mustGetString(input map[string]any, key string) string {
	s, _ := getString(input, key)
	return s
}
