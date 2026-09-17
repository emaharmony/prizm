package remembrance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/adapter"
	remcli "github.com/emaharmony/prizm/internal/remembrance"
)

func TestAdapterName(t *testing.T) {
	a := NewAdapter(DefaultConfig())
	if a.Name() != "remembrance" {
		t.Errorf("expected name 'remembrance', got %q", a.Name())
	}
	if a.Version() != "2.0.0" {
		t.Errorf("expected version '2.0.0', got %q", a.Version())
	}
}

func TestDefaultConfigTimeout(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Timeout != remcli.DefaultTimeout {
		t.Errorf("DefaultConfig timeout = %v, want %v", cfg.Timeout, remcli.DefaultTimeout)
	}
}

func TestAdapterCapabilities(t *testing.T) {
	a := NewAdapter(DefaultConfig())
	caps := a.Capabilities()
	if len(caps) != 6 {
		t.Errorf("expected 6 capabilities, got %d", len(caps))
	}

	expected := []string{"capture", "search", "context_build", "entity_get", "graph_query", "dream"}
	for i, cap := range caps {
		if cap.Action != expected[i] {
			t.Errorf("expected capability %q, got %q", expected[i], cap.Action)
		}
	}
}

func TestAdapterHealth(t *testing.T) {
	// Test with mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}
	}))
	defer server.Close()

	cfg := DefaultConfig()
	cfg.BaseURL = server.URL
	a := NewAdapter(cfg)

	result, err := a.Health(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Ready {
		t.Errorf("expected ready=true, got false")
	}
}

func TestAdapterHealthUnavailable(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseURL = "http://localhost:59999" // unreachable
	cfg.Timeout = 1 * time.Second
	a := NewAdapter(cfg)

	result, err := a.Health(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Ready {
		t.Errorf("expected ready=false for unreachable service")
	}
}

func TestAdapterExecuteCapture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/memory/ingest" {
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"id":            "mem_123",
				"decision":      "PERSIST",
				"confidence":    0.92,
				"category":      "project",
				"tier":          "persist",
				"summary":       "Ema decided Prizm stays domain-agnostic",
				"entities":      []string{"ema", "prizm"},
				"new_entities":  []string{"prizm"},
				"edges_created": 2,
			})
		}
	}))
	defer server.Close()

	cfg := DefaultConfig()
	cfg.BaseURL = server.URL
	a := NewAdapter(cfg)

	result, err := a.Execute(t.Context(), "capture", map[string]any{
		"text":   "Ema decided Prizm stays domain-agnostic",
		"source": "test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success=true, got false: %s", result.Error)
	}
}

func TestAdapterExecuteSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/search" {
			json.NewEncoder(w).Encode(map[string]any{
				"results": []any{
					map[string]any{"id": "mem_1", "content": "test", "score": 0.9},
				},
				"count": 1,
			})
		}
	}))
	defer server.Close()

	cfg := DefaultConfig()
	cfg.BaseURL = server.URL
	a := NewAdapter(cfg)

	result, err := a.Execute(t.Context(), "search", map[string]any{
		"query": "Prizm",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success=true, got false: %s", result.Error)
	}
}

func TestAdapterExecuteUnknownAction(t *testing.T) {
	a := NewAdapter(DefaultConfig())
	_, err := a.Execute(t.Context(), "unknown", map[string]any{})
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestAdapterExecuteCaptureMissingText(t *testing.T) {
	a := NewAdapter(DefaultConfig())
	result, err := a.Execute(t.Context(), "capture", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected success=false for missing text")
	}
}

func TestClientIsAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := remcli.NewClient(server.URL)
	if !client.IsAvailable() {
		t.Error("expected client to be available")
	}
}

func TestClientIsAvailableUnreachable(t *testing.T) {
	client := remcli.NewClientWithTimeout("http://localhost:59999", 1*time.Second)
	if client.IsAvailable() {
		t.Error("expected client to be unavailable")
	}
}

func TestValidateAdapterName(t *testing.T) {
	if err := adapter.ValidateAdapterName("remembrance"); err != nil {
		t.Errorf("expected valid name 'remembrance', got error: %v", err)
	}
	if err := adapter.ValidateAdapterName("my-adapter"); err != nil {
		t.Errorf("expected valid name 'my-adapter', got error: %v", err)
	}
	if err := adapter.ValidateAdapterName("my.adapter"); err == nil {
		t.Error("expected error for name with dots")
	}
}
