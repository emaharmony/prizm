package tool

import (
	"context"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/state"
)

// mockStateManager implements StateManager for testing.
type mockStateManager struct {
	activeTask *state.ActiveTask
	decisions  []state.Decision
	blocked    []state.BlockedItem
	ctx        *state.WorkingContext
}

func (m *mockStateManager) SaveActiveTask(task *state.ActiveTask) error {
	m.activeTask = task
	return nil
}
func (m *mockStateManager) LoadActiveTask() (*state.ActiveTask, error) {
	return m.activeTask, nil
}
func (m *mockStateManager) ClearActiveTask() error {
	m.activeTask = nil
	return nil
}
func (m *mockStateManager) RecordDecision(d state.Decision) error {
	if d.ID == "" {
		d.ID = "D-001"
	}
	d.Timestamp = time.Now()
	m.decisions = append(m.decisions, d)
	return nil
}
func (m *mockStateManager) AddBlocked(item state.BlockedItem) error {
	if item.ID == "" {
		item.ID = "B-001"
	}
	item.Since = time.Now()
	m.blocked = append(m.blocked, item)
	return nil
}
func (m *mockStateManager) Unblock(id string) error {
	filtered := make([]state.BlockedItem, 0)
	for _, item := range m.blocked {
		if item.ID != id {
			filtered = append(filtered, item)
		}
	}
	m.blocked = filtered
	return nil
}
func (m *mockStateManager) SaveContext(ctx *state.WorkingContext) error {
	m.ctx = ctx
	return nil
}
func (m *mockStateManager) LoadContext() (*state.WorkingContext, error) {
	return m.ctx, nil
}

func TestSetActiveTaskTool(t *testing.T) {
	mgr := &mockStateManager{}
	tool := &SetActiveTaskTool{Mgr: mgr}
	reg := NewRegistry()
	reg.Register(tool)

	// Test with all fields
	result, err := tool.Execute(context.Background(), map[string]any{
		"task":   "V32 Phase 1: Adaptive Context",
		"plan":   "Create state package and context injection",
		"scope":  "State files only — no guard rail",
		"branch": "feat/v32-adaptive-context",
		"status": "executing",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if mgr.activeTask == nil {
		t.Fatal("active task should be set")
	}
	if mgr.activeTask.Task != "V32 Phase 1: Adaptive Context" {
		t.Errorf("task = %q, want %q", mgr.activeTask.Task, "V32 Phase 1: Adaptive Context")
	}
	if mgr.activeTask.Status != "executing" {
		t.Errorf("status = %q, want %q", mgr.activeTask.Status, "executing")
	}
	if mgr.activeTask.Branch != "feat/v32-adaptive-context" {
		t.Errorf("branch = %q, want %q", mgr.activeTask.Branch, "feat/v32-adaptive-context")
	}

	// Test missing required field
	result, err = tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when task is missing")
	}

	// Test default status
	result, err = tool.Execute(context.Background(), map[string]any{
		"task": "Quick task",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if mgr.activeTask.Status != "executing" {
		t.Errorf("default status = %q, want %q", mgr.activeTask.Status, "executing")
	}
}

func TestClearActiveTaskTool(t *testing.T) {
	mgr := &mockStateManager{}
	mgr.activeTask = &state.ActiveTask{Task: "Something", Status: "executing"}

	tool := &ClearActiveTaskTool{Mgr: mgr}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if mgr.activeTask != nil {
		t.Error("active task should be nil after clear")
	}
}

func TestRecordDecisionTool(t *testing.T) {
	mgr := &mockStateManager{}
	tool := &RecordDecisionTool{Mgr: mgr}

	result, err := tool.Execute(context.Background(), map[string]any{
		"decision":     "Use local model for guard rail",
		"reasoning":    "Always faster than cloud",
		"alternatives": "Cloud model (higher capability but slower)",
		"author":       "ema",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if len(mgr.decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(mgr.decisions))
	}
	if mgr.decisions[0].Decision != "Use local model for guard rail" {
		t.Errorf("decision = %q, want %q", mgr.decisions[0].Decision, "Use local model for guard rail")
	}
	if mgr.decisions[0].Author != "ema" {
		t.Errorf("author = %q, want ema", mgr.decisions[0].Author)
	}

	// Test default author
	result, err = tool.Execute(context.Background(), map[string]any{
		"decision": "Another decision",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr.decisions[1].Author != "lumi" {
		t.Errorf("default author = %q, want lumi", mgr.decisions[1].Author)
	}

	// Test missing required field
	result, err = tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when decision is missing")
	}
}

func TestAddBlockedTool(t *testing.T) {
	mgr := &mockStateManager{}
	tool := &AddBlockedTool{Mgr: mgr}

	result, err := tool.Execute(context.Background(), map[string]any{
		"item":       "P-005: Add openclaw-lumi to Astraea",
		"waiting_on": "Windows access",
		"task_ref":   "P-005",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if len(mgr.blocked) != 1 {
		t.Fatalf("expected 1 blocked item, got %d", len(mgr.blocked))
	}
	if mgr.blocked[0].Item != "P-005: Add openclaw-lumi to Astraea" {
		t.Errorf("item = %q, want %q", mgr.blocked[0].Item, "P-005: Add openclaw-lumi to Astraea")
	}

	// Test missing required fields
	result, err = tool.Execute(context.Background(), map[string]any{
		"item": "Something blocked",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when waiting_on is missing")
	}

	result, err = tool.Execute(context.Background(), map[string]any{
		"waiting_on": "Something",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when item is missing")
	}
}

func TestUnblockTool(t *testing.T) {
	mgr := &mockStateManager{}
	mgr.blocked = []state.BlockedItem{
		{ID: "B-001", Item: "First", WaitingOn: "Ema"},
		{ID: "B-002", Item: "Second", WaitingOn: "Mango"},
	}

	tool := &UnblockTool{Mgr: mgr}
	result, err := tool.Execute(context.Background(), map[string]any{
		"id": "B-001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if len(mgr.blocked) != 1 {
		t.Fatalf("expected 1 blocked item after unblock, got %d", len(mgr.blocked))
	}
	if mgr.blocked[0].ID != "B-002" {
		t.Errorf("remaining item ID = %q, want B-002", mgr.blocked[0].ID)
	}

	// Test missing required field
	result, err = tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when id is missing")
	}
}

func TestUpdateContextTool(t *testing.T) {
	mgr := &mockStateManager{}
	tool := &UpdateContextTool{Mgr: mgr}

	// Test creating new context
	result, err := tool.Execute(context.Background(), map[string]any{
		"branch":      "feat/v32",
		"last_action": "Created state package",
		"pr":          "#43",
		"notes":       "Phase 1 state persistence",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if mgr.ctx == nil {
		t.Fatal("context should be set")
	}
	if mgr.ctx.Branch != "feat/v32" {
		t.Errorf("branch = %q, want %q", mgr.ctx.Branch, "feat/v32")
	}
	if mgr.ctx.PR != "#43" {
		t.Errorf("pr = %q, want %q", mgr.ctx.PR, "#43")
	}

	// Test merging into existing context
	result, err = tool.Execute(context.Background(), map[string]any{
		"last_action": "Added state tools",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	// Branch should be preserved
	if mgr.ctx.Branch != "feat/v32" {
		t.Errorf("branch should be preserved, got %q", mgr.ctx.Branch)
	}
	if mgr.ctx.LastAction != "Added state tools" {
		t.Errorf("last_action = %q, want %q", mgr.ctx.LastAction, "Added state tools")
	}
}

func TestRegisterStateTools(t *testing.T) {
	mgr := &mockStateManager{}
	reg := NewRegistry()
	RegisterStateTools(reg, mgr)

	// Check all 6 tools are registered
	expected := []string{
		"set_active_task", "clear_active_task", "record_decision",
		"add_blocked", "unblock", "update_context",
	}
	for _, name := range expected {
		if _, err := reg.Resolve(name); err != nil {
			t.Errorf("tool %q not registered: %v", name, err)
		}
	}

	// Verify total count
	tools := reg.List()
	if len(tools) < 6 {
		t.Errorf("expected at least 6 tools, got %d", len(tools))
	}
}

func TestGetStringHelper(t *testing.T) {
	// Test with string value
	val, ok := getString(map[string]any{"key": "hello"}, "key")
	if !ok || val != "hello" {
		t.Errorf("getString(string) = %q, %v; want 'hello', true", val, ok)
	}

	// Test with missing key
	val, ok = getString(map[string]any{}, "key")
	if ok || val != "" {
		t.Errorf("getString(missing) = %q, %v; want '', false", val, ok)
	}

	// Test with nil value
	val, ok = getString(map[string]any{"key": nil}, "key")
	if ok || val != "" {
		t.Errorf("getString(nil) = %q, %v; want '', false", val, ok)
	}

	// Test with non-string value
	val, ok = getString(map[string]any{"key": 42}, "key")
	if ok || val != "" {
		t.Errorf("getString(int) = %q, %v; want '', false", val, ok)
	}
}
