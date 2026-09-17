package stage

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

// mockStage is a simple stage for testing the pipeline.
type mockStage struct {
	name        string
	validateErr error
	result      *StageResult
	execErr     error
	rollback    error
	executed    bool
	rolledBack  bool
}

func (m *mockStage) Name() string { return m.name }

func (m *mockStage) Validate(rc *RunContext) error {
	return m.validateErr
}

func (m *mockStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	m.executed = true
	return rc, m.result, m.execErr
}

func (m *mockStage) Rollback(ctx context.Context, rc *RunContext) error {
	m.rolledBack = true
	return m.rollback
}

func TestPipeline_Success(t *testing.T) {
	pipeline := NewPipeline(
		&mockStage{name: "stage1", result: &StageResult{StageName: "stage1", Success: true}},
		&mockStage{name: "stage2", result: &StageResult{StageName: "stage2", Success: true}},
		&mockStage{name: "stage3", result: &StageResult{StageName: "stage3", Success: true}},
	)

	rc := &RunContext{RunID: "test-run", Task: "hello"}
	final, err := pipeline.Run(context.Background(), rc)
	if err != nil {
		t.Fatalf("pipeline.Run() error = %v", err)
	}
	if len(final.Results) != 3 {
		t.Errorf("expected 3 results, got %d", len(final.Results))
	}
}

func TestPipeline_StageFailure(t *testing.T) {
	pipeline := NewPipeline(
		&mockStage{name: "stage1", result: &StageResult{StageName: "stage1", Success: true}},
		&mockStage{name: "stage2", result: &StageResult{StageName: "stage2", Success: false, Error: "something went wrong"}},
		&mockStage{name: "stage3", result: &StageResult{StageName: "stage3", Success: true}},
	)

	rc := &RunContext{RunID: "test-run", Task: "hello"}
	final, err := pipeline.Run(context.Background(), rc)
	if err != nil {
		t.Fatalf("pipeline.Run() error = %v, want nil (stage failure is not an infrastructure error)", err)
	}
	if final == nil {
		t.Fatal("final RunContext is nil")
	}
	// Stage 2 should have failed
	if final.Results["stage2"].Success {
		t.Error("stage2 should have failed")
	}
	// Stage 3 should NOT have executed (pipeline stops on stage failure)
	s3 := pipeline.Stages[2].(*mockStage)
	if s3.executed {
		t.Error("stage3 should not have been executed after stage2 failure")
	}
}

func TestPipeline_ValidationError(t *testing.T) {
	pipeline := NewPipeline(
		&mockStage{name: "stage1", result: &StageResult{StageName: "stage1", Success: true}},
		&mockStage{name: "stage2", validateErr: fmt.Errorf("missing provider"), result: &StageResult{StageName: "stage2", Success: true}},
	)

	rc := &RunContext{RunID: "test-run"}
	_, err := pipeline.Run(context.Background(), rc)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("error should mention validation, got: %v", err)
	}
}

func TestPipeline_InfrastructureError(t *testing.T) {
	pipeline := NewPipeline(
		&mockStage{name: "stage1", result: &StageResult{StageName: "stage1", Success: true}},
		&mockStage{name: "stage2", result: &StageResult{StageName: "stage2", Success: true}, execErr: fmt.Errorf("NATS connection lost")},
	)

	rc := &RunContext{RunID: "test-run"}
	_, err := pipeline.Run(context.Background(), rc)
	if err == nil {
		t.Fatal("expected infrastructure error, got nil")
	}
	if !strings.Contains(err.Error(), "infrastructure error") {
		t.Errorf("error should mention infrastructure, got: %v", err)
	}
}

func TestRunContext_WithEvent(t *testing.T) {
	rc := &RunContext{
		RunID:  "test-run",
		Events: []event.Event{},
	}

	evt := event.NewEvent("test.stage.started", "pipeline", map[string]any{"key": "value"})
	newRc := rc.WithEvent(evt)

	// Original context should be unchanged
	if len(rc.Events) != 0 {
		t.Error("original RunContext was mutated")
	}

	// New context should have the event
	if len(newRc.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(newRc.Events))
	}

	// New context should preserve other fields
	if newRc.RunID != "test-run" {
		t.Errorf("RunID = %q, want test-run", newRc.RunID)
	}
}

func TestRunContext_WithEvent_ImmutableChain(t *testing.T) {
	rc := &RunContext{
		RunID:  "test-run",
		Events: []event.Event{},
	}

	evt1 := event.NewEvent("test.one", "pipeline", nil)
	evt2 := event.NewEvent("test.two", "pipeline", nil)

	rc2 := rc.WithEvent(evt1)
	rc3 := rc2.WithEvent(evt2)

	// Original should have 0 events
	if len(rc.Events) != 0 {
		t.Errorf("original has %d events, want 0", len(rc.Events))
	}

	// Second should have 1 event
	if len(rc2.Events) != 1 {
		t.Errorf("rc2 has %d events, want 1", len(rc2.Events))
	}

	// Third should have 2 events
	if len(rc3.Events) != 2 {
		t.Errorf("rc3 has %d events, want 2", len(rc3.Events))
	}
}

func TestRunContext_WithResults(t *testing.T) {
	rc := &RunContext{RunID: "test-run"}
	results := map[string]*StageResult{
		"stage1": {StageName: "stage1", Success: true},
	}

	newRc := rc.WithResults(results)
	if newRc.Results["stage1"] == nil {
		t.Error("WithResults did not copy results")
	}

	// Original should not have results
	if rc.Results != nil {
		t.Error("original RunContext was mutated")
	}
}

func TestRunContext_WithEvent_PreservesAllFields(t *testing.T) {
	rc := &RunContext{
		RunID:         "test-run",
		CorrelationID: "corr-123",
		Task:          "implement feature X",
		Project:       "prizm",
		Agent:         "lumi",
		ProviderName:  "ollama",
		Model:         "qwen3",
		Events:        []event.Event{},
	}

	evt := event.NewEvent("test.event", "pipeline", nil)
	newRc := rc.WithEvent(evt)

	if newRc.CorrelationID != "corr-123" {
		t.Errorf("CorrelationID = %q, want corr-123", newRc.CorrelationID)
	}
	if newRc.Task != "implement feature X" {
		t.Errorf("Task = %q, want 'implement feature X'", newRc.Task)
	}
	if newRc.Project != "prizm" {
		t.Errorf("Project = %q, want prizm", newRc.Project)
	}
	if newRc.Agent != "lumi" {
		t.Errorf("Agent = %q, want lumi", newRc.Agent)
	}
}

func TestPipeline_Empty(t *testing.T) {
	pipeline := NewPipeline()
	rc := &RunContext{RunID: "test-run"}
	final, err := pipeline.Run(context.Background(), rc)
	if err != nil {
		t.Fatalf("empty pipeline should not error, got: %v", err)
	}
	if final.RunID != "test-run" {
		t.Errorf("RunID = %q, want test-run", final.RunID)
	}
}

func TestRunContext_WithEvent_ResultsImmutability(t *testing.T) {
	// Verify that WithEvent deep-copies Results, preventing shared mutation
	result1 := &StageResult{StageName: "stage1", Success: true}
	rc := &RunContext{
		RunID:   "test-run",
		Events:  []event.Event{},
		Results: map[string]*StageResult{"stage1": result1},
	}

	evt := event.NewEvent("test.event", "pipeline", nil)
	rc2 := rc.WithEvent(evt)

	// Mutating rc2's Results should NOT affect rc's Results
	rc2.Results["stage2"] = &StageResult{StageName: "stage2", Success: true}
	if len(rc.Results) != 1 {
		t.Errorf("original Results was mutated: got %d entries, want 1", len(rc.Results))
	}
	if len(rc2.Results) != 2 {
		t.Errorf("new Results has wrong count: got %d, want 2", len(rc2.Results))
	}
}

func TestPipeline_SingleStage(t *testing.T) {
	pipeline := NewPipeline(
		&mockStage{name: "only", result: &StageResult{StageName: "only", Success: true}},
	)

	rc := &RunContext{RunID: "test-run"}
	final, err := pipeline.Run(context.Background(), rc)
	if err != nil {
		t.Fatalf("single-stage pipeline error = %v", err)
	}
	if len(final.Results) != 1 {
		t.Errorf("expected 1 result, got %d", len(final.Results))
	}
	if final.Results["only"] == nil || !final.Results["only"].Success {
		t.Error("single stage result not recorded correctly")
	}
}
