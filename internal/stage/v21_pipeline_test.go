package stage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/provider/mock"
)

// mockStreamCallback captures callback invocations for testing.
type mockStreamCallback struct {
	tokens    []string
	indices   []int
	finished  []bool
	err       error
	callCount int
}

func (m *mockStreamCallback) call(token string, index int, finished bool) error {
	m.tokens = append(m.tokens, token)
	m.indices = append(m.indices, index)
	m.finished = append(m.finished, finished)
	m.callCount++
	if m.err != nil {
		return m.err
	}
	return nil
}

// --- LLMStage StreamCallback Tests ---

func TestLLMStage_StreamingWithCallback(t *testing.T) {
	prov := mock.New()
	cb := &mockStreamCallback{}
	stage := &LLMStage{}

	rc := &RunContext{
		RunID:          "test-run",
		Task:           "hello",
		Agent:          "lumi",
		Provider:       prov,
		ProviderName:   "mock",
		Model:          "mock-model",
		StreamCallback: cb.call,
	}

	finalRC, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if finalRC.LLMResponse == "" {
		t.Error("expected non-empty LLM response")
	}
	if cb.callCount == 0 {
		t.Error("expected callback to be called at least once")
	}
	// Last callback should have finished=true
	if len(cb.finished) > 0 && !cb.finished[len(cb.finished)-1] {
		t.Error("last callback should have finished=true")
	}
}

func TestLLMStage_StreamingCallbackError(t *testing.T) {
	prov := mock.New()
	cb := &mockStreamCallback{err: fmt.Errorf("delivery failed")}
	stage := &LLMStage{}

	rc := &RunContext{
		RunID:          "test-run",
		Task:           "hello",
		Agent:          "lumi",
		Provider:       prov,
		ProviderName:   "mock",
		Model:          "mock-model",
		StreamCallback: cb.call,
	}

	finalRC, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected infra error: %v", err)
	}
	if !result.Success {
		// Callback error doesn't fail the stage — it just stops streaming
		t.Errorf("expected success despite callback error, got: %s", result.Error)
	}
	// Should still have a response (partial)
	if finalRC.LLMResponse == "" {
		t.Error("expected partial response even with callback error")
	}
	// callback_error should be in result data
	if result.Data["callback_error"] == nil {
		t.Error("expected callback_error in result data")
	}
}

func TestLLMStage_StreamingNoCallback(t *testing.T) {
	prov := mock.New()
	stage := &LLMStage{}

	rc := &RunContext{
		RunID:        "test-run",
		Task:         "hello",
		Agent:        "lumi",
		Provider:     prov,
		ProviderName: "mock",
		Model:        "mock-model",
		// No StreamCallback
	}

	finalRC, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if finalRC.LLMResponse == "" {
		t.Error("expected non-empty LLM response")
	}
}

// --- PersistenceStage Empty RunDir Tests ---

func TestPersistenceStage_EmptyRunDir_SkipsGracefully(t *testing.T) {
	stage := &PersistenceStage{BusURL: ""}

	rc := &RunContext{
		RunID:  "test-run",
		Task:   "hello",
		RunDir: "", // Empty — conversation mode
		Events: []event.Event{},
	}

	// Validate should pass even with empty RunDir
	err := stage.Validate(rc)
	if err != nil {
		t.Fatalf("Validate should not fail with empty RunDir: %v", err)
	}

	// Execute should skip gracefully
	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success with skipped=true, got error: %s", result.Error)
	}
	if result.Data["skipped"] != true {
		t.Error("expected skipped=true for empty RunDir")
	}
}

func TestPersistenceStage_NonEmptyRunDir_WritesFiles(t *testing.T) {
	tmpDir := t.TempDir()
	stage := &PersistenceStage{BusURL: ""}

	rc := &RunContext{
		RunID:        "test-run",
		Task:         "hello",
		Agent:        "lumi",
		Model:        "mock",
		ProviderName: "mock",
		RunDir:       tmpDir,
		Events: []event.Event{
			{ID: "evt-1", Type: "prizm.run.started", Source: "prizm"},
		},
		Results: map[string]*StageResult{
			"llm": {StageName: "llm", Success: true},
		},
		LLMResponse: "Hello from the LLM!",
	}

	err := stage.Validate(rc)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	// Verify files were created
	if _, err := os.Stat(filepath.Join(tmpDir, "events.jsonl")); err != nil {
		t.Errorf("events.jsonl not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "summary.json")); err != nil {
		t.Errorf("summary.json not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "output.md")); err != nil {
		t.Errorf("output.md not created: %v", err)
	}
}

// --- 3-Stage Discord Pipeline Integration Test ---

func TestDiscordPipeline_Integration(t *testing.T) {
	prov := mock.New()
	tmpDir := t.TempDir()

	pipeline := NewPipeline(
		&LLMStage{},
		&PersistenceStage{BusURL: ""},
		&EventPublishStage{Publisher: nil, BusURL: ""}, // No NATS
	)

	rc := &RunContext{
		RunID:        "test-run",
		Task:         "You are lumi, a lead assistant.\n\nUser: hello\nlumi:",
		Agent:        "lumi",
		Provider:     prov,
		ProviderName: "mock",
		Model:        "mock-model",
		RunDir:       tmpDir,
	}

	finalRC, err := pipeline.Run(context.Background(), rc)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}

	// All stages should succeed
	for name, result := range finalRC.Results {
		if result != nil && !result.Success {
			t.Errorf("stage %s failed: %s", name, result.Error)
		}
	}

	// LLM response should be present
	if finalRC.LLMResponse == "" {
		t.Error("expected non-empty LLM response")
	}

	// Events should have been accumulated
	if len(finalRC.Events) == 0 {
		t.Error("expected events to be accumulated")
	}
}
