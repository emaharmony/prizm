package ollama_test

import (
	"context"
	"encoding/json"
	"errors"
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

func TestOllamaProviderRemovesPrivateReasoningFromGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "test-model", "response": "<ThInK>PRIVATE_CANARY</tHiNk>Public answer.", "done": true,
		})
	}))
	defer server.Close()

	resp, err := ollama.New(server.URL).Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "test-model"})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if resp.Text != "Public answer." || strings.Contains(resp.Text, "PRIVATE_CANARY") {
		t.Fatalf("public text leaked private reasoning: %q", resp.Text)
	}
}

func TestOllamaProviderRejectsUnterminatedThinkAnywhere(t *testing.T) {
	for _, output := range []string{"<think>PRIVATE_CANARY", "Public. <think>PRIVATE_CANARY"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "test-model", "response": output, "done": true})
		}))
		_, err := ollama.New(server.URL).Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "test-model"})
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "no public answer") || strings.Contains(err.Error(), "PRIVATE_CANARY") {
			t.Fatalf("expected privacy-safe no-public-answer error for %q, got %v", output, err)
		}
	}
}

func TestOllamaProviderPreservesThinkTagsInCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "test-model", "response": "Use `<think>literal</think>` in this example.\n```html\n<think>literal</think>\n```", "done": true,
		})
	}))
	defer server.Close()

	resp, err := ollama.New(server.URL).Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "test-model"})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if strings.Count(resp.Text, "<think>literal</think>") != 2 {
		t.Fatalf("code example was rewritten: %q", resp.Text)
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
	if strings.Contains(err.Error(), "nonexistent") || strings.Contains(err.Error(), "PRIVATE_CANARY") {
		t.Errorf("provider error exposed response detail: %v", err)
	}
}

func TestOllamaProviderHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("<think>PRIVATE_CANARY</think>internal server error"))
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
	if strings.Contains(err.Error(), "PRIVATE_CANARY") {
		t.Fatalf("provider response body leaked through error: %v", err)
	}
	var failure *provider.HTTPFailure
	if !errors.As(err, &failure) || failure.HTTPStatusCode() != http.StatusInternalServerError {
		t.Fatalf("expected safe typed HTTP status, got %T: %v", err, err)
	}
}

func TestOllamaGenerateCarriesSafeRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("PRIVATE_PROVIDER_BODY"))
	}))
	defer server.Close()
	_, err := ollama.New(server.URL).Generate(context.Background(), provider.GenerateRequest{Model: "test", Prompt: "test"})
	var failure *provider.HTTPFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed provider failure, got %v", err)
	}
	if failure.HTTPStatusCode() != http.StatusTooManyRequests || failure.RetryAfter() != 90*time.Second {
		t.Fatalf("failure metadata status=%d retry-after=%s", failure.HTTPStatusCode(), failure.RetryAfter())
	}
	if strings.Contains(err.Error(), "PRIVATE_PROVIDER_BODY") {
		t.Fatalf("provider response body leaked through error: %v", err)
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
