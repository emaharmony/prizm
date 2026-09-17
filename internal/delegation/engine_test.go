package delegation

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/task"
	"github.com/nats-io/nats.go"
)

// TestEngine_Delegate creates a task and verifies it's persisted.
func TestEngine_Delegate(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	// Use nil NATS connection — events are logged but not published
	engine := NewEngine(store, nil)

	ctx := context.Background()
	delegateTask, err := engine.Delegate(ctx, "lumi", "mango", "code_implementation", "Implement the X feature", map[string]any{"file": "test.go"})
	if err != nil {
		t.Fatalf("failed to delegate: %v", err)
	}

	if delegateTask.ID == "" {
		t.Error("expected task ID to be set")
	}
	if delegateTask.DelegatedBy != "lumi" {
		t.Errorf("expected delegated_by lumi, got %s", delegateTask.DelegatedBy)
	}
	if delegateTask.DelegatedTo != "mango" {
		t.Errorf("expected delegated_to mango, got %s", delegateTask.DelegatedTo)
	}
	if delegateTask.Status != task.StatusAssigned {
		t.Errorf("expected status assigned, got %s", delegateTask.Status)
	}

	// Verify it's persisted
	got, err := store.Get(delegateTask.ID)
	if err != nil {
		t.Fatalf("failed to get task from store: %v", err)
	}
	if got.Description != "Implement the X feature" {
		t.Errorf("expected description, got %s", got.Description)
	}
}

// TestEngine_Complete marks a task as completed.
func TestEngine_Complete(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	engine := NewEngine(store, nil)

	ctx := context.Background()
	delegateTask, err := engine.Delegate(ctx, "lumi", "mango", "code_implementation", "Implement Y", nil)
	if err != nil {
		t.Fatalf("failed to delegate: %v", err)
	}

	// Mark as in progress first
	if err := store.UpdateStatus(delegateTask.ID, task.StatusInProgress, nil); err != nil {
		t.Fatalf("failed to mark in_progress: %v", err)
	}

	// Complete the task
	result := map[string]any{"lines_changed": 42}
	if err := engine.Complete(ctx, delegateTask.ID, result); err != nil {
		t.Fatalf("failed to complete: %v", err)
	}

	got, err := store.Get(delegateTask.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if got.Status != task.StatusCompleted {
		t.Errorf("expected status completed, got %s", got.Status)
	}
	if got.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

// TestEngine_Fail marks a task as failed.
func TestEngine_Fail(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	engine := NewEngine(store, nil)

	ctx := context.Background()
	delegateTask, err := engine.Delegate(ctx, "lumi", "mango", "code_implementation", "Implement Z", nil)
	if err != nil {
		t.Fatalf("failed to delegate: %v", err)
	}

	if err := engine.Fail(ctx, delegateTask.ID, "something went wrong"); err != nil {
		t.Fatalf("failed to fail task: %v", err)
	}

	got, err := store.Get(delegateTask.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if got.Status != task.StatusFailed {
		t.Errorf("expected status failed, got %s", got.Status)
	}
	if got.Result["error"] != "something went wrong" {
		t.Errorf("expected error message in result, got %v", got.Result)
	}
}

// TestEngine_Cancel cancels a task.
func TestEngine_Cancel(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	engine := NewEngine(store, nil)

	ctx := context.Background()
	delegateTask, err := engine.Delegate(ctx, "lumi", "mango", "review", "Review the PR", nil)
	if err != nil {
		t.Fatalf("failed to delegate: %v", err)
	}

	if err := engine.Cancel(ctx, delegateTask.ID); err != nil {
		t.Fatalf("failed to cancel: %v", err)
	}

	got, err := store.Get(delegateTask.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if got.Status != task.StatusCancelled {
		t.Errorf("expected status cancelled, got %s", got.Status)
	}
}

// TestEngine_GetTask retrieves a task.
func TestEngine_GetTask(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	engine := NewEngine(store, nil)

	ctx := context.Background()
	delegateTask, err := engine.Delegate(ctx, "lumi", "mango", "code", "Task", nil)
	if err != nil {
		t.Fatalf("failed to delegate: %v", err)
	}

	got, err := engine.GetTask(delegateTask.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if got.ID != delegateTask.ID {
		t.Errorf("expected ID %s, got %s", delegateTask.ID, got.ID)
	}
}

// TestEngine_ListTasksByAgent lists tasks for an agent.
func TestEngine_ListTasksByAgent(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	engine := NewEngine(store, nil)

	ctx := context.Background()
	engine.Delegate(ctx, "lumi", "mango", "code", "Task 1", nil)
	engine.Delegate(ctx, "lumi", "mango", "review", "Task 2", nil)
	engine.Delegate(ctx, "lumi", "junie", "research", "Task 3", nil)

	tasks, err := engine.ListTasksByAgent("mango")
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks for mango, got %d", len(tasks))
	}

	tasks, err = engine.ListTasksByAgent("junie")
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task for junie, got %d", len(tasks))
	}
}

// TestEngine_HandleTaskCreated processes an incoming NATS message.
func TestEngine_HandleTaskCreated(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	engine := NewEngine(store, nil)

	// First create a task directly in the store
	now := time.Now()
	delegateTask := &task.Task{
		ID:          "task-handle-test",
		Type:        "code",
		Status:      task.StatusAssigned,
		DelegatedBy: "lumi",
		DelegatedTo: "mango",
		Description: "Handle this task",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.Create(delegateTask); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Register a handler
	var handlerCalled bool
	var receivedTask *task.Task
	handler := func(ctx context.Context, t *task.Task) error {
		handlerCalled = true
		receivedTask = t
		return nil
	}

	engine.handlers["mango"] = handler

	// Simulate an incoming NATS message
	eventData, _ := json.Marshal(map[string]any{
		"v":            1,
		"task_id":      "task-handle-test",
		"delegated_by": "lumi",
		"delegated_to": "mango",
		"task_type":    "code",
		"description":  "Handle this task",
	})

	msg := &nats.Msg{
		Subject: "mango.task.created",
		Data:    eventData,
	}

	engine.handleTaskCreated(msg, "mango")

	if !handlerCalled {
		t.Error("expected handler to be called")
	}
	if receivedTask == nil || receivedTask.ID != "task-handle-test" {
		t.Error("expected handler to receive the correct task")
	}

	// Verify task was transitioned to in_progress
	got, err := store.Get("task-handle-test")
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if got.Status != task.StatusInProgress {
		t.Errorf("expected status in_progress, got %s", got.Status)
	}
}

// TestEngine_HandleTaskCreated_HandlerFailure marks task as failed when handler errors.
func TestEngine_HandleTaskCreated_HandlerFailure(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	engine := NewEngine(store, nil)

	now := time.Now()
	delegateTask := &task.Task{
		ID:          "task-fail-handler",
		Type:        "code",
		Status:      task.StatusAssigned,
		DelegatedBy: "lumi",
		DelegatedTo: "mango",
		Description: "This will fail",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.Create(delegateTask); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Register a failing handler
	engine.handlers["mango"] = func(ctx context.Context, t *task.Task) error {
		return fmt.Errorf("handler failed")
	}

	eventData, _ := json.Marshal(map[string]any{
		"v":       1,
		"task_id": "task-fail-handler",
	})

	msg := &nats.Msg{
		Subject: "mango.task.created",
		Data:    eventData,
	}

	engine.handleTaskCreated(msg, "mango")

	got, err := store.Get("task-fail-handler")
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if got.Status != task.StatusFailed {
		t.Errorf("expected status failed, got %s", got.Status)
	}
}

// TestEngine_PublishEvent_NilNATS does not fail when NATS is nil.
func TestEngine_PublishEvent_NilNATS(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	engine := NewEngine(store, nil)

	// Delegate with nil NATS — should not fail
	_, err := engine.Delegate(context.Background(), "lumi", "mango", "code", "Task", nil)
	if err != nil {
		t.Fatalf("delegate should succeed with nil NATS: %v", err)
	}
}

// newTestStore creates a temporary task store for testing.
func newTestStore(t *testing.T) *task.Store {
	t.Helper()
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
