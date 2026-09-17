// Package delegation provides agent delegation through the NATS event bus.
//
// Agents delegate tasks to each other by publishing events:
//
//	Lumi publishes mango.task.created → Mango subscribes → Mango processes
//	Mango publishes mango.task.completed → Lumi receives result
//
// The engine manages task lifecycle tracking via the task.Store and
// publishes/subscribes to NATS subjects following the per-agent namespace
// convention established in V21.
package delegation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/task"
	"github.com/nats-io/nats.go"
)

// Engine manages agent delegation through NATS events.
type Engine struct {
	store    *task.Store
	conn     *nats.Conn
	mu       sync.Mutex
	subs     map[string][]*nats.Subscription
	handlers map[string]Handler
}

// Handler is called when a task event is received for a subscribed agent.
type Handler func(ctx context.Context, t *task.Task) error

// NewEngine creates a new delegation engine with the given task store and
// NATS connection.
func NewEngine(store *task.Store, conn *nats.Conn) *Engine {
	return &Engine{
		store:    store,
		conn:     conn,
		subs:     make(map[string][]*nats.Subscription),
		handlers: make(map[string]Handler),
	}
}

// Delegate creates a new task and publishes a <agent>.task.created event.
// The task is tracked in the store and the event is published to NATS.
func (e *Engine) Delegate(ctx context.Context, delegatedBy, delegatedTo string, taskType, description string, contextData map[string]any) (*task.Task, error) {
	now := time.Now()
	t := &task.Task{
		ID:          generateTaskID(),
		Type:        taskType,
		Status:      task.StatusCreated,
		DelegatedBy: delegatedBy,
		DelegatedTo: delegatedTo,
		Description: description,
		Context:     contextData,
		Priority:    "normal",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Persist the task
	if err := e.store.Create(t); err != nil {
		return nil, fmt.Errorf("delegation: create task: %w", err)
	}

	// Transition to assigned
	if err := e.store.UpdateStatus(t.ID, task.StatusAssigned, nil); err != nil {
		return nil, fmt.Errorf("delegation: assign task: %w", err)
	}

	// Update the returned task status
	t.Status = task.StatusAssigned
	t.UpdatedAt = time.Now()

	// Publish the task.created event
	event := map[string]any{
		"v":            1,
		"task_id":      t.ID,
		"delegated_by": delegatedBy,
		"delegated_to": delegatedTo,
		"task_type":    taskType,
		"description":  description,
		"priority":     t.Priority,
		"created_at":   now.Format(time.RFC3339),
	}

	if contextData != nil {
		event["context"] = contextData
	}

	if err := e.publishEvent(delegatedTo+".task.created", event); err != nil {
		// Task is persisted but event publish failed — log but don't fail
		log.Printf("[DELEGATION] task %s created but event publish failed: %v", t.ID, err)
	}

	log.Printf("[DELEGATION] %s delegated task %s to %s (type: %s)", delegatedBy, t.ID, delegatedTo, taskType)
	return t, nil
}

// Subscribe registers a handler for an agent's task events.
// The handler is called when a <agent>.task.created event is received.
func (e *Engine) Subscribe(agentID string, handler Handler) error {
	subject := agentID + ".task.created"

	e.mu.Lock()
	e.handlers[agentID] = handler
	e.mu.Unlock()

	if e.conn == nil || !e.conn.IsConnected() {
		log.Printf("[DELEGATION] NATS not connected, subscription for %s will be deferred", agentID)
		return nil
	}

	sub, err := e.conn.Subscribe(subject, func(msg *nats.Msg) {
		e.handleTaskCreated(msg, agentID)
	})
	if err != nil {
		return fmt.Errorf("delegation: subscribe to %s: %w", subject, err)
	}

	e.mu.Lock()
	e.subs[agentID] = append(e.subs[agentID], sub)
	e.mu.Unlock()

	log.Printf("[DELEGATION] subscribed to %s", subject)
	return nil
}

// Complete marks a task as completed and publishes a <agent>.task.completed event.
func (e *Engine) Complete(ctx context.Context, taskID string, result map[string]any) error {
	t, err := e.store.Get(taskID)
	if err != nil {
		return fmt.Errorf("delegation: get task %s: %w", taskID, err)
	}

	if err := e.store.UpdateStatus(taskID, task.StatusCompleted, result); err != nil {
		return fmt.Errorf("delegation: complete task %s: %w", taskID, err)
	}

	// Publish task.completed event with context for downstream consumers
	event := map[string]any{
		"v":            1,
		"task_id":      taskID,
		"completed_by": t.DelegatedTo,
		"status":       string(task.StatusCompleted),
		"context_data": t.Context,
	}

	if result != nil {
		event["result"] = result
	}

	if err := e.publishEvent(t.DelegatedTo+".task.completed", event); err != nil {
		log.Printf("[DELEGATION] task %s completed but event publish failed: %v", taskID, err)
	}

	log.Printf("[DELEGATION] task %s completed by %s", taskID, t.DelegatedTo)
	return nil
}

// Fail marks a task as failed and publishes a <agent>.task.failed event.
func (e *Engine) Fail(ctx context.Context, taskID string, errMsg string) error {
	t, err := e.store.Get(taskID)
	if err != nil {
		return fmt.Errorf("delegation: get task %s: %w", taskID, err)
	}

	result := map[string]any{"error": errMsg}
	if err := e.store.UpdateStatus(taskID, task.StatusFailed, result); err != nil {
		return fmt.Errorf("delegation: fail task %s: %w", taskID, err)
	}

	// Publish task.failed event
	event := map[string]any{
		"v":         1,
		"task_id":   taskID,
		"failed_by": t.DelegatedTo,
		"status":    string(task.StatusFailed),
		"error":     errMsg,
	}

	if err := e.publishEvent(t.DelegatedTo+".task.failed", event); err != nil {
		log.Printf("[DELEGATION] task %s failed but event publish failed: %v", taskID, err)
	}

	log.Printf("[DELEGATION] task %s failed by %s: %s", taskID, t.DelegatedTo, errMsg)
	return nil
}

// Cancel marks a task as cancelled.
func (e *Engine) Cancel(ctx context.Context, taskID string) error {
	if err := e.store.UpdateStatus(taskID, task.StatusCancelled, nil); err != nil {
		return fmt.Errorf("delegation: cancel task %s: %w", taskID, err)
	}

	log.Printf("[DELEGATION] task %s cancelled", taskID)
	return nil
}

// GetTask retrieves a task by ID from the store.
func (e *Engine) GetTask(taskID string) (*task.Task, error) {
	return e.store.Get(taskID)
}

// ListTasksByAgent returns all tasks delegated to a given agent.
func (e *Engine) ListTasksByAgent(agentID string) ([]*task.Task, error) {
	return e.store.ListByAgent(agentID)
}

// handleTaskCreated processes an incoming task.created event.
func (e *Engine) handleTaskCreated(msg *nats.Msg, agentID string) {
	var event map[string]any
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		log.Printf("[DELEGATION] invalid task.created event: %v", err)
		return
	}

	taskID, _ := event["task_id"].(string)
	if taskID == "" {
		log.Printf("[DELEGATION] task.created event missing task_id")
		return
	}

	t, err := e.store.Get(taskID)
	if err != nil {
		log.Printf("[DELEGATION] task %s not found in store: %v", taskID, err)
		return
	}

	// Idempotency guard: skip if task is already in a terminal or active state
	if t.Status == task.StatusInProgress || t.Status == task.StatusCompleted ||
		t.Status == task.StatusFailed || t.Status == task.StatusCancelled {
		log.Printf("[DELEGATION] task %s already in status %s, skipping", taskID, t.Status)
		return
	}

	// Transition to in_progress
	if err := e.store.UpdateStatus(taskID, task.StatusInProgress, nil); err != nil {
		log.Printf("[DELEGATION] failed to mark task %s in_progress: %v", taskID, err)
		return
	}

	// Call the registered handler
	e.mu.Lock()
	handler, ok := e.handlers[agentID]
	e.mu.Unlock()

	if !ok {
		log.Printf("[DELEGATION] no handler registered for agent %s", agentID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := handler(ctx, t); err != nil {
		log.Printf("[DELEGATION] handler for task %s failed: %v", taskID, err)
		// Use background context for Fail() to avoid cancelled context propagation
		_ = e.Fail(context.Background(), taskID, err.Error())
		return
	}

	log.Printf("[DELEGATION] task %s processed successfully by %s", taskID, agentID)
}

// publishEvent publishes an event to NATS.
func (e *Engine) publishEvent(subject string, payload map[string]any) error {
	if e.conn == nil || !e.conn.IsConnected() {
		log.Printf("[DELEGATION] NATS not connected, skipping event %s", subject)
		return nil
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	if err := e.conn.Publish(subject, data); err != nil {
		return fmt.Errorf("publish event: %w", err)
	}

	return nil
}

// generateTaskID creates a unique task identifier using crypto/rand.
func generateTaskID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "task-" + hex.EncodeToString(b)
}
