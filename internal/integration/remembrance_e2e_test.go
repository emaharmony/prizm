package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/remembrance"
	"github.com/emaharmony/prizm/internal/stage"
)

// ── Remembrance E2E Integration Tests ──────────────────────────

// TestE2E_RemembranceCaptureContext tests the full flow:
// capture agent output → build context for next turn
func TestE2E_RemembranceCaptureContext(t *testing.T) {
	var capturedText string
	var capturedSource string

	// Mock Remembrance server that records captures and returns context
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))

		case "/v1/memory/ingest":
			// Record what was captured
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			capturedText, _ = body["content"].(string)
			capturedSource, _ = body["source_type"].(string)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{
				"memory_id": "mem_e2e_001",
				"status": "ingested"
			}`))

		case "/v1/context/build":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"project_id": "prizm",
				"agent_id": "lumi",
				"task": "what is prizm",
				"selected_memories": ["mem_e2e_001"],
				"context_markdown": "# Retrieved Remembrance Context\n\n## Relevant Context\n- Prizm is an event-native agentic environment\n",
				"context_json": {
					"selected_memories": [
						{"memory_id": "mem_e2e_001", "title": "Prizm", "summary": "Prizm is an event-native agentic environment", "score": 0.9, "reason": "relevant"}
					]
				},
				"token_count": 100
			}`))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Create a RemembranceStage with both capture and context enabled
	s := stage.NewRemembranceStage(
		true,  // memoryEnabled
		false, // requireMemory
		server.URL,
		stage.WithCapture(true),
		stage.WithContext(true),
	)

	// --- Turn 1: Agent produces output → capture it ---
	rc1 := &stage.RunContext{
		RunID:       "run_e2e_001",
		Task:        "what is prizm",
		Agent:       "lumi",
		Project:     "prizm",
		LLMResponse: "Prizm is an event-native agentic environment that orchestrates AI agents through a NATS bus.",
	}

	newRC1, result1, err := s.Execute(context.Background(), rc1)
	if err != nil {
		t.Fatalf("Turn 1 execute error: %v", err)
	}
	if !result1.Success {
		t.Errorf("Turn 1 expected success, got failure: %s", result1.Error)
	}

	// Verify capture happened
	if capturedText == "" {
		t.Error("Expected capture to be called, but no text was captured")
	}
	if capturedSource != "agent" {
		t.Errorf("Expected source_type=agent, got %s", capturedSource)
	}

	// Verify context was built
	contextStr, _ := result1.Data["context"].(string)
	if contextStr == "" {
		t.Error("Expected non-empty context from BuildContext")
	}

	_ = newRC1 // RunContext is passed through unchanged

	// --- Turn 2: Next turn → context should be available ---
	rc2 := &stage.RunContext{
		RunID:       "run_e2e_002",
		Task:        "tell me more about prizm",
		Agent:       "lumi",
		Project:     "prizm",
		LLMResponse: "", // No new output yet, just context retrieval
	}

	_, result2, err := s.Execute(context.Background(), rc2)
	if err != nil {
		t.Fatalf("Turn 2 execute error: %v", err)
	}
	if !result2.Success {
		t.Errorf("Turn 2 expected success, got failure: %s", result2.Error)
	}

	// Turn 2 should have context but no capture (empty LLMResponse)
	contextStr2, _ := result2.Data["context"].(string)
	if contextStr2 == "" {
		t.Error("Expected context from Turn 2")
	}
}

// TestE2E_RemembranceGracefulDegradation tests that the pipeline
// continues when Remembrance is unavailable.
func TestE2E_RemembranceGracefulDegradation(t *testing.T) {
	// Point at a port that's not running
	s := stage.NewRemembranceStage(
		true,  // memoryEnabled
		false, // requireMemory (graceful)
		"http://localhost:59998",
		stage.WithCapture(true),
		stage.WithContext(true),
	)

	rc := &stage.RunContext{
		RunID:       "run_graceful_001",
		Task:        "test task",
		Agent:       "lumi",
		Project:     "prizm",
		LLMResponse: "This is a test response.",
	}

	newRC, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected graceful degradation (success), got failure: %s", result.Error)
	}
	// Context should be empty but stage should succeed
	if result.Data["source"] != "unavailable" {
		t.Errorf("Expected source=unavailable, got %v", result.Data["source"])
	}
	// RunContext should pass through unchanged
	if newRC.Task != "test task" {
		t.Error("RunContext should be unchanged")
	}
}

// TestE2E_RemembranceDisabled tests that the stage is a no-op when disabled.
func TestE2E_RemembranceDisabled(t *testing.T) {
	s := stage.NewRemembranceStage(
		false, // memoryEnabled
		false, // requireMemory
		"",    // no URL needed
	)

	rc := &stage.RunContext{
		RunID:       "run_disabled_001",
		Task:        "test task",
		Agent:       "lumi",
		Project:     "prizm",
		LLMResponse: "Response text.",
	}

	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success when disabled, got failure: %s", result.Error)
	}
	if result.Data["source"] != "disabled" {
		t.Errorf("Expected source=disabled, got %v", result.Data["source"])
	}
}

// TestE2E_RemembranceRequiredFailure tests that the stage fails when
// Remembrance is required but unavailable.
func TestE2E_RemembranceRequiredFailure(t *testing.T) {
	s := stage.NewRemembranceStage(
		true,                     // memoryEnabled
		true,                     // requireMemory
		"http://localhost:59998", // not running
		stage.WithCapture(true),
		stage.WithContext(true),
	)

	rc := &stage.RunContext{
		RunID:       "run_required_001",
		Task:        "test task",
		Agent:       "lumi",
		Project:     "prizm",
		LLMResponse: "Response text.",
	}

	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("Expected failure when RequireMemory=true and service unavailable")
	}
}

// TestE2E_RemembranceClientBuildContext tests the raw client BuildContext method.
func TestE2E_RemembranceClientBuildContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		case "/v1/context/build":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"project_id": "prizm",
				"agent_id": "lumi",
				"task": "prizm architecture",
				"selected_memories": ["m1"],
				"context_markdown": "# Retrieved Remembrance Context\n\n## Relevant Context\n- Prizm uses NATS JetStream\n",
				"context_json": {
					"selected_memories": [
						{"memory_id": "m1", "title": "NATS", "summary": "NATS bus", "score": 0.9, "reason": "relevant"}
					],
					"total_memories": 1
				},
				"token_count": 100
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := remembrance.NewClient(server.URL)

	// Test IsAvailable
	if !client.IsAvailable() {
		t.Error("Expected client to be available")
	}

	// Test BuildContext
	resp, err := client.BuildContext("prizm architecture", "prizm", "lumi", 5)
	if err != nil {
		t.Fatalf("BuildContext error: %v", err)
	}
	if len(resp.SelectedMemories) != 1 {
		t.Errorf("Expected 1 selected memory, got %d", len(resp.SelectedMemories))
	}
	if resp.ContextMarkdown == "" {
		t.Error("Expected non-empty context_markdown")
	}
	if resp.ContextJSON == nil || len(resp.ContextJSON.Memories) != 1 {
		t.Error("Expected 1 memory in context_json")
	}

	// Verify event types exist
	if event.V2EventTypes.ContextRequested == "" {
		t.Error("Expected ContextRequested event type to be defined")
	}
}
