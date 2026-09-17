package delegation

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/task"
)

// Tracker monitors task lifecycle and provides end-to-end tracking.
// It ensures no tasks are dropped by:
// 1. Watching for tasks stuck in "assigned" or "in_progress" for too long
// 2. Providing status queries for active tasks
// 3. Logging task state transitions for audit
type Tracker struct {
	store    *task.Store
	engine   *Engine
	timeout  time.Duration // Max time a task can be in_progress before alerting
	interval time.Duration // How often to check for stuck tasks
	stopCh   chan struct{}
	stopOnce sync.Once // Guard against double-close panic
}

// TrackerConfig holds configuration for the task tracker.
type TrackerConfig struct {
	// TaskTimeout is the maximum time a task can remain in_progress
	// before being flagged as stuck. Default: 10 minutes.
	TaskTimeout time.Duration

	// CheckInterval is how often the tracker checks for stuck tasks.
	// Default: 1 minute.
	CheckInterval time.Duration
}

// NewTracker creates a new task tracker with the given store and engine.
func NewTracker(store *task.Store, engine *Engine, cfg TrackerConfig) *Tracker {
	timeout := cfg.TaskTimeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	interval := cfg.CheckInterval
	if interval == 0 {
		interval = 1 * time.Minute
	}

	return &Tracker{
		store:    store,
		engine:   engine,
		timeout:  timeout,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the stuck-task monitor loop.
// It runs until Stop() is called or the context is cancelled.
func (t *Tracker) Start(ctx context.Context) {
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	log.Printf("[TRACKER] started (timeout=%s, interval=%s)", t.timeout, t.interval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[TRACKER] stopped by context")
			return
		case <-t.stopCh:
			log.Printf("[TRACKER] stopped")
			return
		case <-ticker.C:
			t.checkStuckTasks(ctx)
		}
	}
}

// Stop stops the stuck-task monitor. Safe to call multiple times.
func (t *Tracker) Stop() {
	t.stopOnce.Do(func() {
		close(t.stopCh)
	})
}

// checkStuckTasks finds tasks that have been in_progress for too long
// and flags them.
func (t *Tracker) checkStuckTasks(ctx context.Context) {
	// Find tasks stuck in "in_progress"
	inProgress, err := t.store.ListByStatus(task.StatusInProgress)
	if err != nil {
		log.Printf("[TRACKER] error listing in_progress tasks: %v", err)
		return
	}

	now := time.Now()
	for _, tsk := range inProgress {
		elapsed := now.Sub(tsk.UpdatedAt)
		if elapsed > t.timeout {
			log.Printf("[TRACKER] ⚠️ task %s stuck in_progress for %s (agent: %s, type: %s)",
				tsk.ID, elapsed.Round(time.Second), tsk.DelegatedTo, tsk.Type)

			// Optionally: fail the stuck task
			if err := t.engine.Fail(ctx, tsk.ID, fmt.Sprintf("task stuck in_progress for %s", elapsed.Round(time.Second))); err != nil {
				log.Printf("[TRACKER] failed to fail stuck task %s: %v", tsk.ID, err)
			}
		}
	}

	// Also check for tasks stuck in "assigned" (never picked up)
	assigned, err := t.store.ListByStatus(task.StatusAssigned)
	if err != nil {
		log.Printf("[TRACKER] error listing assigned tasks: %v", err)
		return
	}

	for _, tsk := range assigned {
		elapsed := now.Sub(tsk.UpdatedAt)
		if elapsed > t.timeout {
			log.Printf("[TRACKER] ⚠️ task %s stuck in assigned for %s (agent: %s, type: %s) — never picked up",
				tsk.ID, elapsed.Round(time.Second), tsk.DelegatedTo, tsk.Type)

			// Cancel tasks that were never picked up
			if err := t.engine.Cancel(ctx, tsk.ID); err != nil {
				log.Printf("[TRACKER] failed to cancel unassigned task %s: %v", tsk.ID, err)
			}
		}
	}
}

// TaskStatus returns a summary of all tasks grouped by status.
func (t *Tracker) TaskStatus() (*TaskStatusSummary, error) {
	summary := &TaskStatusSummary{
		ByStatus: make(map[string]int),
	}

	for _, status := range []task.Status{
		task.StatusCreated,
		task.StatusAssigned,
		task.StatusInProgress,
		task.StatusCompleted,
		task.StatusFailed,
		task.StatusCancelled,
	} {
		tasks, err := t.store.ListByStatus(status)
		if err != nil {
			return nil, fmt.Errorf("tracker: list by status %s: %w", status, err)
		}
		summary.ByStatus[string(status)] = len(tasks)
		summary.Total += len(tasks)
	}

	return summary, nil
}

// ActiveTasks returns all tasks that are not in a terminal state.
func (t *Tracker) ActiveTasks() ([]*task.Task, error) {
	var active []*task.Task

	for _, status := range []task.Status{
		task.StatusCreated,
		task.StatusAssigned,
		task.StatusInProgress,
	} {
		tasks, err := t.store.ListByStatus(status)
		if err != nil {
			return nil, fmt.Errorf("tracker: list by status %s: %w", status, err)
		}
		active = append(active, tasks...)
	}

	return active, nil
}

// StuckTasks returns tasks that have been in_progress or assigned longer than the timeout.
func (t *Tracker) StuckTasks() ([]*task.Task, error) {
	var stuck []*task.Task

	for _, status := range []task.Status{task.StatusInProgress, task.StatusAssigned} {
		tasks, err := t.store.ListByStatus(status)
		if err != nil {
			return nil, fmt.Errorf("tracker: list %s: %w", status, err)
		}

		now := time.Now()
		for _, tsk := range tasks {
			if now.Sub(tsk.UpdatedAt) > t.timeout {
				stuck = append(stuck, tsk)
			}
		}
	}

	return stuck, nil
}

// TaskStatusSummary provides a count of tasks by status.
type TaskStatusSummary struct {
	Total    int
	ByStatus map[string]int
}
