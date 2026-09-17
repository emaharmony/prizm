package delegation

import (
	"context"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/task"
)

// TestTracker_StuckTasks finds tasks stuck in_progress.
func TestTracker_StuckTasks(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)

	tracker := NewTracker(store, engine, TrackerConfig{
		TaskTimeout:   100 * time.Millisecond,
		CheckInterval: 50 * time.Millisecond,
	})

	// Create a task and set it to in_progress
	ctx := context.Background()
	delegateTask, err := engine.Delegate(ctx, "lumi", "mango", "code", "Stuck task", nil)
	if err != nil {
		t.Fatalf("failed to delegate: %v", err)
	}

	// Manually set to in_progress
	if err := store.UpdateStatus(delegateTask.ID, task.StatusInProgress, nil); err != nil {
		t.Fatalf("failed to set in_progress: %v", err)
	}

	// Wait for timeout
	time.Sleep(150 * time.Millisecond)

	// Check for stuck tasks
	stuck, err := tracker.StuckTasks()
	if err != nil {
		t.Fatalf("failed to get stuck tasks: %v", err)
	}

	if len(stuck) != 1 {
		t.Errorf("expected 1 stuck task, got %d", len(stuck))
	}

	if len(stuck) > 0 && stuck[0].ID != delegateTask.ID {
		t.Errorf("expected stuck task ID %s, got %s", delegateTask.ID, stuck[0].ID)
	}
}

// TestTracker_TaskStatus returns counts by status.
func TestTracker_TaskStatus(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)

	tracker := NewTracker(store, engine, TrackerConfig{
		TaskTimeout:   10 * time.Minute,
		CheckInterval: 1 * time.Minute,
	})

	ctx := context.Background()
	// Create several tasks
	engine.Delegate(ctx, "lumi", "mango", "code", "Task 1", nil)
	engine.Delegate(ctx, "lumi", "mango", "code", "Task 2", nil)

	// Complete one task
	tasks, _ := store.ListByStatus(task.StatusAssigned)
	if len(tasks) > 0 {
		engine.Complete(ctx, tasks[0].ID, map[string]any{"status": "done"})
	}

	summary, err := tracker.TaskStatus()
	if err != nil {
		t.Fatalf("failed to get task status: %v", err)
	}

	if summary.Total < 2 {
		t.Errorf("expected at least 2 total tasks, got %d", summary.Total)
	}

	if summary.ByStatus[string(task.StatusCompleted)] < 1 {
		t.Error("expected at least 1 completed task")
	}
}

// TestTracker_ActiveTasks returns non-terminal tasks.
func TestTracker_ActiveTasks(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)

	tracker := NewTracker(store, engine, TrackerConfig{
		TaskTimeout:   10 * time.Minute,
		CheckInterval: 1 * time.Minute,
	})

	ctx := context.Background()
	engine.Delegate(ctx, "lumi", "mango", "code", "Active task", nil)
	engine.Delegate(ctx, "lumi", "mango", "code", "Another active", nil)

	active, err := tracker.ActiveTasks()
	if err != nil {
		t.Fatalf("failed to get active tasks: %v", err)
	}

	if len(active) < 2 {
		t.Errorf("expected at least 2 active tasks, got %d", len(active))
	}
}

// TestTracker_CheckStuckTasks fails stuck tasks.
func TestTracker_CheckStuckTasks(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)

	tracker := NewTracker(store, engine, TrackerConfig{
		TaskTimeout:   100 * time.Millisecond,
		CheckInterval: 50 * time.Millisecond,
	})

	ctx := context.Background()
	delegateTask, err := engine.Delegate(ctx, "lumi", "mango", "code", "Will be stuck", nil)
	if err != nil {
		t.Fatalf("failed to delegate: %v", err)
	}

	// Set to in_progress
	store.UpdateStatus(delegateTask.ID, task.StatusInProgress, nil)

	// Wait for timeout + check interval
	time.Sleep(200 * time.Millisecond)

	// Run check manually
	tracker.checkStuckTasks(ctx)

	// Task should be failed
	got, err := store.Get(delegateTask.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if got.Status != task.StatusFailed {
		t.Errorf("expected stuck task to be failed, got %s", got.Status)
	}
}

// TestTracker_CancelUnassignedTasks cancels tasks never picked up.
func TestTracker_CancelUnassignedTasks(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)

	tracker := NewTracker(store, engine, TrackerConfig{
		TaskTimeout:   100 * time.Millisecond,
		CheckInterval: 50 * time.Millisecond,
	})

	ctx := context.Background()
	delegateTask, err := engine.Delegate(ctx, "lumi", "mango", "code", "Never picked up", nil)
	if err != nil {
		t.Fatalf("failed to delegate: %v", err)
	}

	// Task is in "assigned" status but never transitions to in_progress
	// Wait for timeout
	time.Sleep(200 * time.Millisecond)

	// Run check manually
	tracker.checkStuckTasks(ctx)

	// Task should be cancelled
	got, err := store.Get(delegateTask.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if got.Status != task.StatusCancelled {
		t.Errorf("expected unassigned task to be cancelled, got %s", got.Status)
	}
}

func TestTracker_Stop_DoubleClose(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)

	tracker := NewTracker(store, engine, TrackerConfig{
		TaskTimeout:   10 * time.Minute,
		CheckInterval: 1 * time.Minute,
	})

	// Stop should be safe to call multiple times
	tracker.Stop()
	tracker.Stop() // should not panic
	tracker.Stop() // really should not panic
}

func TestTracker_StuckTasks_Assigned(t *testing.T) {
	// StuckTasks should include both stuck in_progress AND stuck assigned tasks
	store := newTestStore(t)
	engine := NewEngine(store, nil)

	tracker := NewTracker(store, engine, TrackerConfig{
		TaskTimeout:   100 * time.Millisecond,
		CheckInterval: 50 * time.Millisecond,
	})

	ctx := context.Background()
	// Create a task that stays in "assigned"
	assigned, err := engine.Delegate(ctx, "lumi", "mango", "code", "Stuck assigned", nil)
	if err != nil {
		t.Fatalf("failed to delegate: %v", err)
	}

	// Create a task in "in_progress"
	inProgress, err := engine.Delegate(ctx, "lumi", "mango", "code", "Stuck in progress", nil)
	if err != nil {
		t.Fatalf("failed to delegate: %v", err)
	}
	store.UpdateStatus(inProgress.ID, task.StatusInProgress, nil)

	// Wait for timeout
	time.Sleep(150 * time.Millisecond)

	stuck, err := tracker.StuckTasks()
	if err != nil {
		t.Fatalf("failed to get stuck tasks: %v", err)
	}

	// Should find both tasks
	if len(stuck) != 2 {
		t.Errorf("expected 2 stuck tasks (assigned + in_progress), got %d", len(stuck))
	}

	ids := map[string]bool{}
	for _, tsk := range stuck {
		ids[tsk.ID] = true
	}
	if !ids[assigned.ID] {
		t.Error("expected stuck assigned task to be included")
	}
	if !ids[inProgress.ID] {
		t.Error("expected stuck in_progress task to be included")
	}
}
