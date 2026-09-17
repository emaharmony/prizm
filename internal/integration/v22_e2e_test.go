package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/delegation"
	"github.com/emaharmony/prizm/internal/stage"
	"github.com/emaharmony/prizm/internal/task"
)

// TestE2E_DelegationFlow tests the full delegation flow:
// DelegationStage detects marker → creates task → NATS delivers to Mango →
// Mango completes → task store updated.
func TestE2E_DelegationFlow(t *testing.T) {
	// 1. Set up task store
	taskStore, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create task store: %v", err)
	}
	defer taskStore.Close()

	// 2. Set up embedded NATS
	natsURL, natsShutdown, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("failed to start NATS server: %v", err)
	}
	defer natsShutdown()

	conn, err := bus.ConnectToBus(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to NATS: %v", err)
	}
	defer conn.Close()

	// Wait for connection
	time.Sleep(100 * time.Millisecond)

	// 3. Set up delegation engine
	engine := delegation.NewEngine(taskStore, conn)

	// 4. Register Mango as a subscriber
	receivedTasks := make(chan task.Task, 1)
	mangoHandler := func(ctx context.Context, t *task.Task) error {
		receivedTask := *t
		// Mango "completes" the task
		result := map[string]any{
			"status":        "done",
			"lines_changed": 42,
		}
		if err := engine.Complete(ctx, t.ID, result); err != nil {
			return err
		}
		receivedTasks <- receivedTask
		return nil
	}

	if err := engine.Subscribe("mango", mangoHandler); err != nil {
		t.Fatalf("failed to subscribe mango: %v", err)
	}

	// 5. Run DelegationStage directly with a marker in the LLM response
	delegStage := &stage.DelegationStage{Engine: engine, StripMarkers: true}

	rc := &stage.RunContext{
		RunID:       "e2e-delegation-run",
		Task:        "delegate to mango",
		Agent:       "lumi",
		LLMResponse: "I'll delegate this task. [DELEGATE: mango | code_implementation] Implement the X feature with tests",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	finalRC, result, err := delegStage.Execute(ctx, rc)
	if err != nil {
		t.Fatalf("delegation stage failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("delegation stage failed: %s", result.Error)
	}
	if result.Data["delegations"] != 1 {
		t.Fatalf("expected 1 delegation, got %v", result.Data["delegations"])
	}

	// Verify marker was stripped
	if contains(finalRC.LLMResponse, "[DELEGATE:") {
		t.Error("expected delegation marker to be stripped from response")
	}

	// 6. Wait for Mango to process the task via NATS.
	var receivedTask task.Task
	select {
	case receivedTask = <-receivedTasks:
	case <-ctx.Done():
		t.Fatal("timed out waiting for Mango to receive the delegated task")
	}

	// 7. Verify Mango received the task.
	if receivedTask.DelegatedBy != "lumi" {
		t.Errorf("expected delegated_by lumi, got %s", receivedTask.DelegatedBy)
	}
	if receivedTask.DelegatedTo != "mango" {
		t.Errorf("expected delegated_to mango, got %s", receivedTask.DelegatedTo)
	}
	if receivedTask.Type != "code_implementation" {
		t.Errorf("expected type code_implementation, got %s", receivedTask.Type)
	}

	// 8. Verify task is completed in the store
	completedTask, err := taskStore.Get(receivedTask.ID)
	if err != nil {
		t.Fatalf("failed to get completed task: %v", err)
	}
	if completedTask.Status != task.StatusCompleted {
		t.Errorf("expected status completed, got %s", completedTask.Status)
	}
	if completedTask.Result["lines_changed"] != float64(42) {
		t.Errorf("expected result lines_changed=42, got %v", completedTask.Result["lines_changed"])
	}
}

// TestE2E_DelegationParallelTasks tests that multiple agents can be
// delegated tasks in parallel and all complete successfully.
func TestE2E_DelegationParallelTasks(t *testing.T) {
	taskStore, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create task store: %v", err)
	}
	defer taskStore.Close()

	natsURL, natsShutdown, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("failed to start NATS server: %v", err)
	}
	defer natsShutdown()

	conn, err := bus.ConnectToBus(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to NATS: %v", err)
	}
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	engine := delegation.NewEngine(taskStore, conn)

	// Register Mango with completion tracking
	mangoCompleted := make(chan string, 2)
	if err := engine.Subscribe("mango", func(ctx context.Context, t *task.Task) error {
		result := map[string]any{"status": "done"}
		err := engine.Complete(ctx, t.ID, result)
		if err == nil {
			mangoCompleted <- t.ID
		}
		return err
	}); err != nil {
		t.Fatalf("failed to subscribe mango: %v", err)
	}

	// Delegate two tasks to Mango
	ctx := context.Background()
	task1, err := engine.Delegate(ctx, "lumi", "mango", "code", "Implement feature A", nil)
	if err != nil {
		t.Fatalf("failed to delegate task1: %v", err)
	}
	task2, err := engine.Delegate(ctx, "lumi", "mango", "review", "Review feature B", nil)
	if err != nil {
		t.Fatalf("failed to delegate task2: %v", err)
	}

	// Wait for both tasks to complete via channel
	completed := map[string]bool{}
	timeout := time.After(5 * time.Second)
	for len(completed) < 2 {
		select {
		case taskID := <-mangoCompleted:
			completed[taskID] = true
		case <-timeout:
			t.Fatalf("timed out waiting for tasks to complete, got %d/2", len(completed))
		}
	}

	// Verify both tasks are completed in the store
	got1, err := taskStore.Get(task1.ID)
	if err != nil {
		t.Fatalf("failed to get task1: %v", err)
	}
	got2, err := taskStore.Get(task2.ID)
	if err != nil {
		t.Fatalf("failed to get task2: %v", err)
	}

	// Both should be completed
	if got1.Status != task.StatusCompleted {
		t.Errorf("task1 expected completed, got %s", got1.Status)
	}
	if got2.Status != task.StatusCompleted {
		t.Errorf("task2 expected completed, got %s", got2.Status)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
