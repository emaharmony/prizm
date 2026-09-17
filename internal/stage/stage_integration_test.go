package stage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
	mockpkg "github.com/emaharmony/prizm/internal/provider/mock"
	"github.com/emaharmony/prizm/internal/tool"
)

func TestConnectionStage_Validate(t *testing.T) {
	stage := &ConnectionStage{RunDir: t.TempDir()}
	tests := []struct {
		name    string
		rc      *RunContext
		wantErr bool
	}{
		{
			name:    "valid context",
			rc:      &RunContext{RunID: "run_123", Task: "hello"},
			wantErr: false,
		},
		{
			name:    "missing run ID",
			rc:      &RunContext{Task: "hello"},
			wantErr: true,
		},
		{
			name:    "missing task",
			rc:      &RunContext{RunID: "run_123"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := stage.Validate(tt.rc)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConnectionStage_Execute(t *testing.T) {
	tmpDir := t.TempDir()
	stage := &ConnectionStage{RunDir: tmpDir}
	rc := &RunContext{RunID: "run_test", Task: "hello", Project: "prizm", Agent: "lumi"}

	newRC, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false, want true")
	}
	if newRC.RunDir == "" {
		t.Error("Execute() did not set RunDir")
	}
	// Check that run directory was created
	if _, statErr := os.Stat(newRC.RunDir); os.IsNotExist(statErr) {
		t.Errorf("run directory not created: %s", newRC.RunDir)
	}
	// Check that events were emitted
	if len(newRC.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(newRC.Events))
	}
}

func TestConnectionStage_Rollback(t *testing.T) {
	tmpDir := t.TempDir()
	stage := &ConnectionStage{RunDir: tmpDir}
	rc := &RunContext{RunID: "run_rollback", Task: "hello"}

	// Execute first to create the directory
	newRC, _, _ := stage.Execute(context.Background(), rc)
	if _, err := os.Stat(newRC.RunDir); os.IsNotExist(err) {
		t.Fatalf("run directory not created before rollback")
	}

	// Rollback should remove the directory
	err := stage.Rollback(context.Background(), newRC)
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if _, err := os.Stat(newRC.RunDir); !os.IsNotExist(err) {
		t.Error("run directory still exists after rollback")
	}
}

// RemembranceStage tests moved to remembrance_test.go

func TestLLMStage_Validate(t *testing.T) {
	stage := &LLMStage{}
	tests := []struct {
		name    string
		rc      *RunContext
		wantErr bool
	}{
		{
			name:    "valid context",
			rc:      &RunContext{Provider: mockpkg.New(), Model: "mock-model"},
			wantErr: false,
		},
		{
			name:    "missing provider",
			rc:      &RunContext{Model: "mock-model"},
			wantErr: true,
		},
		{
			name:    "missing model",
			rc:      &RunContext{Provider: mockpkg.New()},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := stage.Validate(tt.rc)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLLMStage_Sync(t *testing.T) {
	stage := &LLMStage{DryRun: false}
	rc := &RunContext{
		RunID:        "run_llm_test",
		Task:         "say hello",
		Project:      "prizm",
		Agent:        "lumi",
		Provider:     mockpkg.New(),
		ProviderName: "mock",
		Model:        "mock-model",
		Temperature:  0.2,
		MaxTokens:    1024,
		Events:       []event.Event{},
	}

	newRC, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false, want true; error: %s", result.Error)
	}
	if newRC.LLMResponse == "" {
		t.Error("Execute() did not set LLMResponse")
	}
	// Should emit started + completed events
	if len(newRC.Events) < 2 {
		t.Errorf("expected at least 2 events, got %d", len(newRC.Events))
	}
}

func TestLLMStage_DryRun(t *testing.T) {
	stage := &LLMStage{DryRun: true}
	rc := &RunContext{
		RunID:        "run_dry_test",
		Task:         "say hello",
		Provider:     mockpkg.New(),
		ProviderName: "mock",
		Model:        "mock-model",
		Events:       []event.Event{},
	}

	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false, want true")
	}
	if result.Data["dry_run"] != true {
		t.Error("dry run result should have dry_run=true")
	}
}

func TestLLMStage_Streaming(t *testing.T) {
	stage := &LLMStage{DryRun: false}
	rc := &RunContext{
		RunID:        "run_stream_test",
		Task:         "stream hello",
		Project:      "prizm",
		Agent:        "lumi",
		Provider:     mockpkg.New(),
		ProviderName: "mock",
		Model:        "mock-model",
		Temperature:  0.2,
		MaxTokens:    1024,
		Events:       []event.Event{},
	}

	newRC, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false, want true; error: %s", result.Error)
	}
	if newRC.LLMResponse == "" {
		t.Error("Execute() did not set LLMResponse in streaming mode")
	}
	// Streaming result should have streamed=true
	if result.Data["streamed"] != true {
		t.Error("streaming result should have streamed=true")
	}
}

func TestToolStage_Execute_NoResponse(t *testing.T) {
	stage := &ToolStage{WorkspaceRoot: "."}
	rc := &RunContext{RunID: "run_tool_test", Events: []event.Event{}}

	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false, want true")
	}
	if result.Data["tool_called"] != false {
		t.Error("tool_called should be false when no LLM response")
	}
}

func TestApprovalStage_Execute(t *testing.T) {
	stage := &ApprovalStage{WorkspaceRoot: "."}
	rc := &RunContext{RunID: "run_approval_test", Events: []event.Event{}}

	newRC, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false, want true")
	}
	// Should emit an event
	if len(newRC.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(newRC.Events))
	}
}

func TestPersistenceStage_Execute(t *testing.T) {
	tmpDir := t.TempDir()
	stage := &PersistenceStage{}
	rc := &RunContext{
		RunID:        "run_persist_test",
		Task:         "test task",
		Project:      "prizm",
		Agent:        "lumi",
		ProviderName: "mock",
		Model:        "mock-model",
		LLMResponse:  "test output",
		RunDir:       filepath.Join(tmpDir, "run_persist_test"),
		Events:       []event.Event{},
	}

	// Create the run directory first
	os.MkdirAll(rc.RunDir, 0755)

	newRC, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false, want true; error: %s", result.Error)
	}

	// Check that files were created
	eventsPath := filepath.Join(rc.RunDir, "events.jsonl")
	if _, statErr := os.Stat(eventsPath); os.IsNotExist(statErr) {
		t.Error("events.jsonl not created")
	}
	summaryPath := filepath.Join(rc.RunDir, "summary.json")
	if _, statErr := os.Stat(summaryPath); os.IsNotExist(statErr) {
		t.Error("summary.json not created")
	}
	outputPath := filepath.Join(rc.RunDir, "output.md")
	if _, statErr := os.Stat(outputPath); os.IsNotExist(statErr) {
		t.Error("output.md not created")
	}
	_ = newRC // used for events check
}

func TestPersistenceStage_Rollback(t *testing.T) {
	tmpDir := t.TempDir()
	stage := &PersistenceStage{}
	rc := &RunContext{
		RunID:        "run_rollback_test",
		Task:         "test task",
		Project:      "prizm",
		Agent:        "lumi",
		ProviderName: "mock",
		Model:        "mock-model",
		LLMResponse:  "test output",
		RunDir:       filepath.Join(tmpDir, "run_rollback_test"),
		Events:       []event.Event{},
	}

	// Execute first
	os.MkdirAll(rc.RunDir, 0755)
	_, result, _ := stage.Execute(context.Background(), rc)
	if !result.Success {
		t.Fatalf("Execute() failed: %s", result.Error)
	}

	// Verify files exist
	if _, err := os.Stat(filepath.Join(rc.RunDir, "events.jsonl")); os.IsNotExist(err) {
		t.Fatal("events.jsonl not created")
	}

	// Rollback
	err := stage.Rollback(context.Background(), rc)
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	// Verify files are removed
	if _, err := os.Stat(filepath.Join(rc.RunDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Error("events.jsonl still exists after rollback")
	}
}

func TestFullPipeline_Integration(t *testing.T) {
	// Integration test: run a full pipeline with all 6 stages
	tmpDir := t.TempDir()

	toolReg := tool.NewRegistry()
	tool.RegisterBuiltinsV4(toolReg, ".", 1024*1024, "")

	pipeline := NewPipeline(
		&ConnectionStage{RunDir: tmpDir},
		&RemembranceStage{MemoryEnabled: false},
		&LLMStage{DryRun: true},
		&ToolStage{ToolRegistry: toolReg, PolicyConfig: tool.PolicyConfig{WorkspaceRoot: ".", MaxFileSize: 1024 * 1024}, WorkspaceRoot: "."},
		&ApprovalStage{WorkspaceRoot: "."},
		&PersistenceStage{},
	)

	rc := &RunContext{
		RunID:        "run_integration_test",
		Task:         "integration test task",
		Project:      "prizm",
		Agent:        "lumi",
		Provider:     mockpkg.New(),
		ProviderName: "mock",
		Model:        "mock-model",
		Temperature:  0.2,
		MaxTokens:    1024,
	}

	final, err := pipeline.Run(context.Background(), rc)
	if err != nil {
		t.Fatalf("pipeline.Run() error = %v", err)
	}

	// All 6 stages should have results
	if len(final.Results) != 6 {
		t.Errorf("expected 6 results, got %d", len(final.Results))
	}

	// All stages should have succeeded
	for name, result := range final.Results {
		if !result.Success {
			t.Errorf("stage %s failed: %s", name, result.Error)
		}
	}

	// Events should have been emitted
	if len(final.Events) == 0 {
		t.Error("no events emitted during pipeline")
	}

	// Run directory should exist
	if _, statErr := os.Stat(final.RunDir); os.IsNotExist(statErr) {
		t.Error("run directory not created")
	}

	// Summary should have been written
	summaryPath := filepath.Join(final.RunDir, "summary.json")
	if _, statErr := os.Stat(summaryPath); os.IsNotExist(statErr) {
		t.Error("summary.json not created")
	}
}
