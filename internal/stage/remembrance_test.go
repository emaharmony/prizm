package stage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── RemembranceStage Tests ───────────────────────────────────────

func TestRemembranceStage_Disabled(t *testing.T) {
	s := NewRemembranceStage(false, false, "", WithCapture(true), WithContext(true))

	rc := &RunContext{Task: "test task", LLMResponse: "test output"}
	newRC, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success when disabled, got failure: %s", result.Error)
	}
	if result.Data["source"] != "disabled" {
		t.Errorf("expected source=disabled, got %v", result.Data["source"])
	}
	if newRC.Task != "test task" {
		t.Error("RunContext should be unchanged")
	}
}

func TestRemembranceStage_ValidateRequiresURL(t *testing.T) {
	s := NewRemembranceStage(true, false, "")
	rc := &RunContext{Task: "test"}
	if err := s.Validate(rc); err == nil {
		t.Error("expected validation error for missing URL")
	}
}

func TestRemembranceStage_UnavailableGraceful(t *testing.T) {
	// Point at a port that's definitely not running
	s := NewRemembranceStage(true, false, "http://localhost:59999", WithCapture(true), WithContext(true))

	rc := &RunContext{Task: "test task", LLMResponse: "test output"}
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected graceful degradation, got failure: %s", result.Error)
	}
	if result.Data["source"] != "unavailable" {
		t.Errorf("expected source=unavailable, got %v", result.Data["source"])
	}
}

func TestRemembranceStage_UnavailableRequired(t *testing.T) {
	s := NewRemembranceStage(true, true, "http://localhost:59999", WithCapture(true), WithContext(true))

	rc := &RunContext{Task: "test task", LLMResponse: "test output"}
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when RequireMemory=true and service unavailable")
	}
}

func TestRemembranceStage_ContextBuild(t *testing.T) {
	// Mock Remembrance server
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
				"task": "who is ema",
				"selected_memories": ["m1"],
				"context_markdown": "# Retrieved Remembrance Context\n\n## Task\nwho is ema\n\n## Relevant Context\n- **Ema is the founder** \u2014 Ema founded the project\n",
				"context_json": {
					"project_id": "prizm",
					"agent_id": "lumi",
					"task": "who is ema",
					"selected_memories": [
						{"memory_id": "m1", "title": "Ema is the founder", "summary": "Ema founded the project", "score": 0.9, "reason": "highly relevant"}
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

	s := NewRemembranceStage(true, false, server.URL, WithCapture(false), WithContext(true))

	rc := &RunContext{Task: "who is ema", Project: "prizm", Agent: "lumi"}
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Error)
	}
	if result.Data["source"] != "injected" {
		t.Errorf("expected source=injected, got %v", result.Data["source"])
	}
	ctxStr, ok := result.Data["context"].(string)
	if !ok || ctxStr == "" {
		t.Error("expected non-empty context string")
	}
}

func TestRemembranceStage_Capture(t *testing.T) {
	captureCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		case "/v1/memory/ingest":
			captureCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"memory_id": "mem_123",
				"status": "ingested",
				"confidence": 0.92,
				"backend": "dilbert",
				"category": "project",
				"tier": "persist",
				"summary": "Agent output captured",
				"topics": ["prizm", "remembrance"],
				"entities": ["ema"],
				"new_entities": [],
				"edges_created": 1
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	s := NewRemembranceStage(true, false, server.URL, WithCapture(true), WithContext(false))

	rc := &RunContext{Task: "test task", Agent: "lumi", Project: "prizm", LLMResponse: "I've completed the task."}
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Error)
	}
	if !captureCalled {
		t.Error("expected capture to be called")
	}
}

func TestRemembranceStage_CaptureAndContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		case "/v1/memory/ingest":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"memory_id": "mem_123",
				"status": "ingested",
				"confidence": 0.92,
				"backend": "dilbert",
				"category": "project",
				"tier": "persist",
				"summary": "Output captured",
				"topics": [],
				"entities": [],
				"new_entities": [],
				"edges_created": 0
			}`))
		case "/v1/context/build":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"project_id": "prizm",
				"agent_id": "lumi",
				"task": "test",
				"selected_memories": [],
				"context_markdown": "",
				"token_count": 0
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	s := NewRemembranceStage(true, false, server.URL, WithCapture(true), WithContext(true))

	rc := &RunContext{Task: "test task", Agent: "lumi", Project: "prizm", LLMResponse: "Done."}
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Error)
	}
	// Both capture and context should be present
	if _, ok := result.Data["capture"]; !ok {
		t.Error("expected capture result")
	}
	if _, ok := result.Data["context"]; !ok {
		t.Error("expected context result")
	}
}

func TestRemembranceStage_EmptyOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture should NOT be called for empty output
		if r.URL.Path == "/v1/memory/ingest" {
			t.Error("capture should not be called for empty output")
		}
		if r.URL.Path == "/v1/health" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		}
		if r.URL.Path == "/v1/context/build" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"project_id":"prizm","task":"test","selected_memories":[],"context_markdown":"","token_count":0}`))
		}
	}))
	defer server.Close()

	s := NewRemembranceStage(true, false, server.URL, WithCapture(true), WithContext(true))

	rc := &RunContext{Task: "test", LLMResponse: ""}
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Error)
	}
}

func TestRemembranceStage_ContextBuildFails_Required(t *testing.T) {
	// Server returns 500 on /context/build — memory is required
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		case "/v1/context/build":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "internal server error"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	s := NewRemembranceStage(true, true, server.URL, WithCapture(false), WithContext(true))

	rc := &RunContext{Task: "test task", Agent: "lumi", Project: "prizm"}
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when RequireMemory=true and context build fails")
	}
	if result.Error == "" {
		t.Error("expected error message in result")
	}
}

func TestRemembranceStage_ContextBuildFails_Graceful(t *testing.T) {
	// Server returns 500 on /context/build — memory is NOT required (graceful)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		case "/v1/context/build":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "internal server error"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	s := NewRemembranceStage(true, false, server.URL, WithCapture(false), WithContext(true))

	rc := &RunContext{Task: "test task", Agent: "lumi", Project: "prizm"}
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected graceful degradation, got failure: %s", result.Error)
	}
	if result.Data["source"] != "failed" {
		t.Errorf("expected source=failed, got %v", result.Data["source"])
	}
}

func TestRemembranceStage_HealthCheckCache(t *testing.T) {
	healthChecks := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			healthChecks++
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		case "/v1/context/build":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"project_id":"prizm","task":"test","selected_memories":[],"context_markdown":"","token_count":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	s := NewRemembranceStage(true, false, server.URL, WithCapture(false), WithContext(true))

	rc := &RunContext{Task: "test", Agent: "lumi"}

	// First call — health check should fire
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success: %s", result.Error)
	}
	firstChecks := healthChecks

	// Second call within 30s — health check should be cached
	_, result, err = s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success: %s", result.Error)
	}

	// Health check should NOT have been called again (cached)
	if healthChecks > firstChecks {
		t.Errorf("expected cached health check, got %d total checks (was %d)", healthChecks, firstChecks)
	}
}

func TestRemembranceStage_Rollback(t *testing.T) {
	s := NewRemembranceStage(true, false, "http://localhost:8788")
	err := s.Rollback(context.Background(), &RunContext{})
	if err != nil {
		t.Errorf("rollback should be no-op, got: %v", err)
	}
}

func TestRemembranceStage_ClientInitializedEagerly(t *testing.T) {
	// Verify that the client is created in the constructor, not lazily
	s := NewRemembranceStage(true, false, "http://localhost:8788")
	if s.client == nil {
		t.Error("expected client to be initialized in constructor, got nil")
	}
}

func TestRemembranceStage_ContextBuildNoTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /context/build should NOT be called when task is empty
		if r.URL.Path == "/v1/context/build" {
			t.Error("context/build should not be called for empty task")
		}
		if r.URL.Path == "/v1/health" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer server.Close()

	s := NewRemembranceStage(true, false, server.URL, WithCapture(false), WithContext(true))
	rc := &RunContext{Task: ""}
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success: %s", result.Error)
	}
	if result.Data["source"] != "no_task" {
		t.Errorf("expected source=no_task, got %v", result.Data["source"])
	}
}
