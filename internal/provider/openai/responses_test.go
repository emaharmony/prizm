package openai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/openai"
	"github.com/emaharmony/prizm/internal/retry"
)

func TestResponsesProviderGenerateSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %s, want /responses", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing authorization header")
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["model"] != "gpt-5.1" {
			t.Fatalf("model = %v", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"resp_123",
			"model":"gpt-5.1",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from responses"}]}],
			"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}
		}`))
	}))
	defer server.Close()

	p := openai.NewResponsesWithBaseURL("test-key", server.URL)
	resp, err := p.Generate(context.Background(), provider.GenerateRequest{
		Model:  "gpt-5.1",
		Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if resp.Text != "hello from responses" {
		t.Fatalf("text = %q", resp.Text)
	}
	if resp.Provider != "openai_responses" {
		t.Fatalf("provider = %q", resp.Provider)
	}
	if resp.PromptTokens != 7 || resp.OutputTokens != 3 {
		t.Fatalf("usage = %d/%d", resp.PromptTokens, resp.OutputTokens)
	}
}

func TestResponsesProviderGenerateUsesOutputText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"resp_123","model":"gpt-5.1","output_text":"aggregated"}`))
	}))
	defer server.Close()

	p := openai.NewResponsesWithBaseURL("test-key", server.URL)
	resp, err := p.Generate(context.Background(), provider.GenerateRequest{Model: "gpt-5.1", Prompt: "hello"})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if resp.Text != "aggregated" {
		t.Fatalf("text = %q", resp.Text)
	}
}

func TestResponsesProviderRateLimitRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	p := openai.NewResponsesWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Model: "gpt-5.1", Prompt: "hello"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !retry.IsRetryable(err) {
		t.Fatalf("expected retryable error, got %v", err)
	}
}
