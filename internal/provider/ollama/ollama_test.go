package ollama_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/ollama"
)

func TestOllamaProviderImplementsInterface(t *testing.T) {
	var _ provider.Provider = ollama.New("")
	var _ provider.Provider = ollama.New("http://custom:1234")
}

func TestOllamaProviderConnectionRefused(t *testing.T) {
	p := ollama.New("http://127.0.0.1:1")
	req := provider.GenerateRequest{
		RunID:   "run_conn",
		Agent:   "lumi",
		Project: "prizm",
		Task:    "should fail",
		Prompt:  "test",
		Model:   "test-model",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := p.Generate(ctx, req)
	if err == nil {
		t.Fatal("expected error for connection refused, got nil")
	}
}

func TestOllamaProviderSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("expected path /api/generate, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		resp := map[string]any{
			"model":             "test-model",
			"response":          "Hello from Ollama!",
			"prompt_eval_count": 10,
			"eval_count":        5,
			"total_duration":    500000000,
			"done":              true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := ollama.New(server.URL)
	req := provider.GenerateRequest{
		RunID:   "run_test",
		Agent:   "lumi",
		Project: "prizm",
		Task:    "Say hello",
		Prompt:  "You are a helpful assistant.",
		Model:   "test-model",
	}

	resp, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp.Text != "Hello from Ollama!" {
		t.Errorf("expected text 'Hello from Ollama!', got '%s'", resp.Text)
	}
	if resp.Provider != ollama.Name {
		t.Errorf("expected provider '%s', got '%s'", ollama.Name, resp.Provider)
	}
	if resp.Model != "test-model" {
		t.Errorf("expected model 'test-model', got '%s'", resp.Model)
	}
	if resp.PromptTokens != 10 {
		t.Errorf("expected 10 prompt tokens, got %d", resp.PromptTokens)
	}
	if resp.OutputTokens != 5 {
		t.Errorf("expected 5 output tokens, got %d", resp.OutputTokens)
	}
}

func TestOllamaProviderModelError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"error": "model 'nonexistent' not found",
			"done":  true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := ollama.New(server.URL)
	req := provider.GenerateRequest{
		Task:   "test",
		Prompt: "test",
		Model:  "nonexistent",
	}

	_, err := p.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for model not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestOllamaProviderHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	p := ollama.New(server.URL)
	req := provider.GenerateRequest{
		Task:   "test",
		Prompt: "test",
		Model:  "test",
	}

	_, err := p.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestOllamaProviderContextCancellation(t *testing.T) {
	p := ollama.New("http://127.0.0.1:1")
	req := provider.GenerateRequest{
		Task:   "cancel test",
		Prompt: "test",
		Model:  "test",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	_, err := p.Generate(ctx, req)
	if err == nil {
		t.Error("expected error for context cancellation")
	}
}

func TestOllamaProviderDefaultURL(t *testing.T) {
	p := ollama.New("")
	if p.BaseURL != ollama.DefaultBaseURL {
		t.Errorf("expected default URL '%s', got '%s'", ollama.DefaultBaseURL, p.BaseURL)
	}
}

func TestOllamaProviderCustomURL(t *testing.T) {
	p := ollama.New("http://custom:1234")
	if p.BaseURL != "http://custom:1234" {
		t.Errorf("expected custom URL, got '%s'", p.BaseURL)
	}
}

func TestOllamaProviderTrailingSlash(t *testing.T) {
	p := ollama.New("http://custom:1234/")
	if p.BaseURL != "http://custom:1234" {
		t.Errorf("expected trailing slash stripped, got '%s'", p.BaseURL)
	}
}

func TestOllamaProviderTokenFallback(t *testing.T) {
	// When Ollama returns 0 for token counts, we fall back to rough estimates
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"model":             "test-model",
			"response":          "Hello!",
			"prompt_eval_count": 0,
			"eval_count":        0,
			"done":              true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := ollama.New(server.URL)
	req := provider.GenerateRequest{
		Task:   "test",
		Prompt: "a short prompt",
		Model:  "test-model",
	}

	resp, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Fallback: len(prompt)/4 and len(response)/4
	if resp.PromptTokens <= 0 {
		t.Error("expected positive prompt tokens from fallback")
	}
	if resp.OutputTokens <= 0 {
		t.Error("expected positive output tokens from fallback")
	}
}

func TestOllamaProviderNameMethod(t *testing.T) {
	p := ollama.New("http://localhost:11434")
	if p.Name() != ollama.Name {
		t.Errorf("expected Name() = %q, got %q", ollama.Name, p.Name())
	}
}

func TestOllamaProviderTierMethod(t *testing.T) {
	p := ollama.New("http://localhost:11434")
	if p.Tier() != provider.TierFree {
		t.Errorf("expected Tier() = %q, got %q", provider.TierFree, p.Tier())
	}
}
