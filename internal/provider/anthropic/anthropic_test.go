package anthropic_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/anthropic"
)

func TestProvider_Name(t *testing.T) {
	p := anthropic.New("test-key")
	if p.Name() != "anthropic" {
		t.Errorf("Name() = %q, want anthropic", p.Name())
	}
}

func TestProvider_Tier(t *testing.T) {
	p := anthropic.New("test-key")
	if p.Tier() != provider.TierPaid {
		t.Errorf("Tier() = %q, want paid", p.Tier())
	}
}

func TestProvider_DefaultBaseURL(t *testing.T) {
	p := anthropic.New("test-key")
	if p.BaseURL != "https://api.anthropic.com" {
		t.Errorf("BaseURL = %q, want https://api.anthropic.com", p.BaseURL)
	}
}

func TestProvider_Generate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %q, want test-key", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want 2023-06-01", r.Header.Get("anthropic-version"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "msg_test",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Hello from Claude!"}],
			"model": "claude-sonnet-4-20250514",
			"usage": {"input_tokens": 15, "output_tokens": 8},
			"stop_reason": "end_turn"
		}`))
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	resp, err := p.Generate(context.Background(), provider.GenerateRequest{
		Prompt: "Say hello",
		Model:  "claude-sonnet-4-20250514",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.Text != "Hello from Claude!" {
		t.Errorf("Text = %q, want Hello from Claude!", resp.Text)
	}
	if resp.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", resp.Provider)
	}
	if resp.PromptTokens != 15 {
		t.Errorf("PromptTokens = %d, want 15", resp.PromptTokens)
	}
	if resp.OutputTokens != 8 {
		t.Errorf("OutputTokens = %d, want 8", resp.OutputTokens)
	}
}

func TestProvider_Generate_RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "claude-sonnet-4-20250514"})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !anthropic.IsRetryableError(err) {
		t.Error("429 error should be retryable")
	}
}

func TestProvider_Generate_ServiceUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "claude-sonnet-4-20250514"})
	if err == nil {
		t.Fatal("expected error for 503")
	}
	if !anthropic.IsRetryableError(err) {
		t.Error("503 error should be retryable")
	}
}

func TestProvider_Generate_BadGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "claude-sonnet-4-20250514"})
	if err == nil {
		t.Fatal("expected error for 502")
	}
	if !anthropic.IsRetryableError(err) {
		t.Error("502 error should be retryable")
	}
}

func TestProvider_Generate_ForbiddenError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error": {"type": "authentication_error", "message": "invalid api key"}}`))
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "claude-sonnet-4-20250514"})
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if anthropic.IsRetryableError(err) {
		t.Error("403 error should NOT be retryable")
	}
}

func TestProvider_Generate_NoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": "msg_test", "type": "message", "role": "assistant", "content": [], "model": "claude-sonnet-4-20250514", "usage": {"input_tokens": 10, "output_tokens": 0}}`))
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "claude-sonnet-4-20250514"})
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestProvider_Generate_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := anthropic.New("test-key")
	_, err := p.Generate(ctx, provider.GenerateRequest{Prompt: "test", Model: "claude-sonnet-4-20250514"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestProvider_Generate_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{invalid json`))
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "claude-sonnet-4-20250514"})
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestProvider_Generate_MultipleContentBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "msg_test",
			"type": "message",
			"role": "assistant",
			"content": [
				{"type": "text", "text": "Hello "},
				{"type": "text", "text": "from "},
				{"type": "text", "text": "Claude!"}
			],
			"model": "claude-sonnet-4-20250514",
			"usage": {"input_tokens": 15, "output_tokens": 10},
			"stop_reason": "end_turn"
		}`))
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	resp, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "claude-sonnet-4-20250514"})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.Text != "Hello from Claude!" {
		t.Errorf("Text = %q, want Hello from Claude!", resp.Text)
	}
}

func TestProvider_Generate_InternalServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": {"type": "api_error", "message": "overloaded"}}`))
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "claude-sonnet-4-20250514"})
	if err == nil {
		t.Fatal("expected error for 500")
	}
}
