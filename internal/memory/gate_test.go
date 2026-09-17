package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewGateExtractor(t *testing.T) {
	g := NewGateExtractor([]string{"nemotron-3-nano:4b", "qwen3.5:4b"}, "http://localhost:11434", "glm-5.1:cloud")
	if len(g.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(g.Models))
	}
	if g.FallbackModel != "glm-5.1:cloud" {
		t.Errorf("FallbackModel = %q, want glm-5.1:cloud", g.FallbackModel)
	}
	if g.OllamaURL != "http://localhost:11434" {
		t.Errorf("OllamaURL = %q", g.OllamaURL)
	}
}

func TestGateExtractor_Gate_YesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaGenerateRequest
		json.NewDecoder(r.Body).Decode(&req)
		resp := ollamaGenerateResponse{
			Response: "YES\nThis contains an important decision about architecture.",
			Model:    req.Model,
			Done:     true,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	g := NewGateExtractor([]string{"test-model"}, server.URL, "")
	result, err := g.Gate(context.Background(), "We decided to use SQLite for local storage.")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if !result.ShouldRemember {
		t.Error("expected ShouldRemember = true")
	}
	if result.Reasoning == "" {
		t.Error("expected non-empty reasoning")
	}
}

func TestGateExtractor_Gate_NoResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ollamaGenerateResponse{
			Response: "NO\nThis is just casual small talk with no lasting information.",
			Model:    "test-model",
			Done:     true,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	g := NewGateExtractor([]string{"test-model"}, server.URL, "")
	result, err := g.Gate(context.Background(), "Hey how's the weather?")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if result.ShouldRemember {
		t.Error("expected ShouldRemember = false")
	}
}

func TestGateExtractor_Extract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ollamaGenerateResponse{
			Response: "CATEGORY: decision\nTIER: active\nSUMMARY: Use local models for memory extraction\nTOPICS: memory, local-models, architecture\nCONTENT: Decided to use nemotron-3-nano as the primary memory gate model.",
			Model:    "test-model",
			Done:     true,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	g := NewGateExtractor([]string{"test-model"}, server.URL, "")
	result, err := g.Extract(context.Background(), "We decided to use local models for memory extraction.")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Category != "project" {
		t.Errorf("Category = %q, want project (was decision, now mapped)", result.Category)
	}
	if result.Tier != "active" {
		t.Errorf("Tier = %q, want active", result.Tier)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(result.KeyTopics) != 3 {
		t.Errorf("KeyTopics = %v, want 3 topics", result.KeyTopics)
	}
}

func TestGateExtractor_FallbackChain(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaGenerateRequest
		json.NewDecoder(r.Body).Decode(&req)
		callCount++
		if req.Model == "fail-model" {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("model not found"))
			return
		}
		resp := ollamaGenerateResponse{
			Response: "YES\nImportant decision.",
			Model:    req.Model,
			Done:     true,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	g := NewGateExtractor([]string{"fail-model", "good-model"}, server.URL, "")
	result, err := g.Gate(context.Background(), "Important decision made.")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if !result.ShouldRemember {
		t.Error("expected ShouldRemember = true after fallback")
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls (fail + succeed), got %d", callCount)
	}
}

func TestGateExtractor_AllModelsFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("all models down"))
	}))
	defer server.Close()

	g := NewGateExtractor([]string{"model1", "model2"}, server.URL, "")
	_, err := g.Gate(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error when all models fail")
	}
	if !errors.Is(err, ErrNoModelAvailable) {
		t.Errorf("expected ErrNoModelAvailable, got %v", err)
	}
}

func TestParseExtractResponse(t *testing.T) {
	raw := "CATEGORY: preference\nTIER: persist\nSUMMARY: Prefers dark mode\nTOPICS: ui, theme\nCONTENT: User stated they prefer dark mode for all interfaces."
	result := parseExtractResponse(raw)

	if result.Category != "user" {
		t.Errorf("Category = %q, want user (was preference, now mapped)", result.Category)
	}
	if result.Tier != "persist" {
		t.Errorf("Tier = %q, want persist", result.Tier)
	}
	if result.Summary != "Prefers dark mode" {
		t.Errorf("Summary = %q", result.Summary)
	}
	if len(result.KeyTopics) != 2 {
		t.Errorf("KeyTopics = %v", result.KeyTopics)
	}
}