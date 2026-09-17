package stage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/delegation"
	"github.com/emaharmony/prizm/internal/task"
)

// TestDelegationStage_NoEngine verifies that a nil engine is a no-op.
func TestDelegationStage_NoEngine(t *testing.T) {
	stage := &DelegationStage{Engine: nil, StripMarkers: true}

	rc := &RunContext{
		RunID:       "test-run",
		Task:        "hello",
		Agent:       "lumi",
		LLMResponse: "I will handle this myself.",
	}

	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["skipped"] != true {
		t.Error("expected skipped=true for nil engine")
	}
	if rc.LLMResponse != "I will handle this myself." {
		t.Error("response should not be modified when engine is nil")
	}
}

// TestDelegationStage_NoMarker verifies that no delegation marker is a no-op.
func TestDelegationStage_NoMarker(t *testing.T) {
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	engine := delegation.NewEngine(store, nil)
	stage := &DelegationStage{Engine: engine, StripMarkers: true}

	rc := &RunContext{
		RunID:       "test-run",
		Task:        "hello",
		Agent:       "lumi",
		LLMResponse: "Here is the answer to your question.",
	}

	finalRC, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["delegations"] != 0 {
		t.Errorf("expected 0 delegations, got %v", result.Data["delegations"])
	}
	_ = finalRC
}

// TestDelegationStage_WithMarker verifies that a delegation marker creates a task.
func TestDelegationStage_WithMarker(t *testing.T) {
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	engine := delegation.NewEngine(store, nil)
	stage := &DelegationStage{Engine: engine, StripMarkers: true}

	rc := &RunContext{
		RunID:       "test-run",
		Task:        "implement the feature",
		Agent:       "lumi",
		LLMResponse: "I'll delegate this to Mango. [DELEGATE: mango | code_implementation] Implement the X feature",
	}

	finalRC, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["delegations"] != 1 {
		t.Errorf("expected 1 delegation, got %v", result.Data["delegations"])
	}

	// Verify the marker was stripped from the response
	if finalRC.LLMResponse != "I'll delegate this to Mango.  Implement the X feature" {
		t.Errorf("expected marker stripped, got: %s", finalRC.LLMResponse)
	}

	// Verify the task was created in the store
	taskIDs := result.Data["delegated_task_ids"].([]string)
	if len(taskIDs) != 1 {
		t.Fatalf("expected 1 task ID, got %d", len(taskIDs))
	}

	delegateTask, err := store.Get(taskIDs[0])
	if err != nil {
		t.Fatalf("failed to get delegated task: %v", err)
	}
	if delegateTask.DelegatedBy != "lumi" {
		t.Errorf("expected delegated_by lumi, got %s", delegateTask.DelegatedBy)
	}
	if delegateTask.DelegatedTo != "mango" {
		t.Errorf("expected delegated_to mango, got %s", delegateTask.DelegatedTo)
	}
	if delegateTask.Type != "code_implementation" {
		t.Errorf("expected type code_implementation, got %s", delegateTask.Type)
	}
}

// TestDelegationStage_MultipleMarkers verifies multiple delegations.
func TestDelegationStage_MultipleMarkers(t *testing.T) {
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	engine := delegation.NewEngine(store, nil)
	stage := &DelegationStage{Engine: engine, StripMarkers: true}

	rc := &RunContext{
		RunID:       "test-run",
		Task:        "multi-delegate",
		Agent:       "lumi",
		LLMResponse: "Let me split this up. [DELEGATE: mango | code] Write the code. [DELEGATE: researcher | research] Find the docs.",
	}

	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["delegations"] != 2 {
		t.Errorf("expected 2 delegations, got %v", result.Data["delegations"])
	}

	// Verify both tasks exist
	taskIDs := result.Data["delegated_task_ids"].([]string)
	if len(taskIDs) != 2 {
		t.Fatalf("expected 2 task IDs, got %d", len(taskIDs))
	}
}

// TestDelegationStage_NoStripMarkers verifies that markers are kept when StripMarkers is false.
func TestDelegationStage_NoStripMarkers(t *testing.T) {
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	engine := delegation.NewEngine(store, nil)
	stage := &DelegationStage{Engine: engine, StripMarkers: false}

	rc := &RunContext{
		RunID:       "test-run",
		Task:        "hello",
		Agent:       "lumi",
		LLMResponse: "Delegating [DELEGATE: mango] Implement feature",
	}

	finalRC, _, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Marker should still be present
	if finalRC.LLMResponse != "Delegating [DELEGATE: mango] Implement feature" {
		t.Errorf("expected marker preserved, got: %s", finalRC.LLMResponse)
	}
}

// TestDelegationStage_EmptyResponse verifies empty response is a no-op.
func TestDelegationStage_EmptyResponse(t *testing.T) {
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	engine := delegation.NewEngine(store, nil)
	stage := &DelegationStage{Engine: engine, StripMarkers: true}

	rc := &RunContext{
		RunID:       "test-run",
		Task:        "hello",
		Agent:       "lumi",
		LLMResponse: "",
	}

	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["skipped"] != true {
		t.Error("expected skipped=true for empty response")
	}
}

// TestDelegationStage_EventsNoLongerInRunContext verifies that delegation
// events are NOT added to RunContext.Events (the engine publishes to NATS directly).
func TestDelegationStage_EventsNoLongerInRunContext(t *testing.T) {
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	engine := delegation.NewEngine(store, nil)
	stage := &DelegationStage{Engine: engine, StripMarkers: true}

	rc := &RunContext{
		RunID:       "test-run",
		Task:        "hello",
		Agent:       "lumi",
		LLMResponse: "Let me delegate. [DELEGATE: mango | code] Do the thing",
		Events:      nil,
	}

	finalRC, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	// Verify that events were NOT added to RunContext
	// (the engine publishes to NATS directly, not through RunContext.Events)
	if len(finalRC.Events) != 0 {
		t.Errorf("expected 0 events in RunContext (engine publishes directly), got %d", len(finalRC.Events))
	}

	// Verify that the delegation was tracked in the result
	if result.Data["delegations"] != 1 {
		t.Errorf("expected 1 delegation in result, got %v", result.Data["delegations"])
	}
}
