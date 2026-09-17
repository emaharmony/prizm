package openai_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/openai"
)

func TestProvider_Name(t *testing.T) {
	p := openai.New("test-key")
	if p.Name() != "openai" {
		t.Errorf("Name() = %q, want openai", p.Name())
	}
}

func TestProvider_Tier(t *testing.T) {
	p := openai.New("test-key")
	if p.Tier() != openai.TierPaid {
		t.Errorf("Tier() = %q, want paid", p.Tier())
	}
}

func TestProvider_Generate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"model": "gpt-4",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hello!"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`))
	}))
	defer server.Close()

	p := openai.NewWithBaseURL("test-key", server.URL)
	resp, err := p.Generate(context.Background(), provider.GenerateRequest{
		Prompt: "Say hello",
		Model:  "gpt-4",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.Text != "Hello!" {
		t.Errorf("Text = %q, want Hello!", resp.Text)
	}
	if resp.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", resp.Provider)
	}
	if resp.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", resp.PromptTokens)
	}
	if resp.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", resp.OutputTokens)
	}
}

func TestProvider_Generate_RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	p := openai.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !openai.IsRetryableError(err) {
		t.Error("429 error should be retryable")
	}
}

func TestProvider_Generate_ServiceUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	p := openai.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error for 503")
	}
	if !openai.IsRetryableError(err) {
		t.Error("503 error should be retryable")
	}
}

func TestProvider_Generate_BadGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	p := openai.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error for 502")
	}
	if !openai.IsRetryableError(err) {
		t.Error("502 error should be retryable")
	}
}

func TestProvider_Generate_ForbiddenError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error": {"message": "invalid api key"}}`))
	}))
	defer server.Close()

	p := openai.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if openai.IsRetryableError(err) {
		t.Error("403 error should NOT be retryable")
	}
}

func TestProvider_Generate_NoChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": "test", "object": "chat.completion", "model": "gpt-4", "choices": []}`))
	}))
	defer server.Close()

	p := openai.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestProvider_Generate_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := openai.New("test-key")
	_, err := p.Generate(ctx, provider.GenerateRequest{Prompt: "test", Model: "gpt-4"})
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

	p := openai.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestProvider_Generate_BadRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": {"message": "model not found"}}`))
	}))
	defer server.Close()

	p := openai.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error for 400")
	}
}

func TestProvider_Generate_EmptyResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := openai.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error for empty response body")
	}
}

func TestProvider_Generate_NonJSONError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`<html>Internal Server Error</html>`))
	}))
	defer server.Close()

	p := openai.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error for HTML error response")
	}
}

func TestProvider_DefaultBaseURL(t *testing.T) {
	p := openai.New("test-key")
	if p.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL = %q, want https://api.openai.com/v1", p.BaseURL)
	}
}

func TestProvider_CustomBaseURL(t *testing.T) {
	p := openai.NewWithBaseURL("test-key", "https://api.together.ai/v1")
	if p.BaseURL != "https://api.together.ai/v1" {
		t.Errorf("BaseURL = %q, want https://api.together.ai/v1", p.BaseURL)
	}
}

func TestTogetherProvider(t *testing.T) {
	p := openai.NewTogetherProvider("test-key")
	if p.BaseURL != "https://api.together.xyz/v1" {
		t.Errorf("Together BaseURL = %q, want https://api.together.xyz/v1", p.BaseURL)
	}
	if p.Tier() != openai.TierPaid {
		t.Errorf("Together Tier = %q, want paid", p.Tier())
	}
}

func TestGroqProvider(t *testing.T) {
	p := openai.NewGroqProvider("test-key")
	if p.BaseURL != "https://api.groq.com/openai/v1" {
		t.Errorf("Groq BaseURL = %q, want https://api.groq.com/openai/v1", p.BaseURL)
	}
}

func TestOllamaCompatProvider(t *testing.T) {
	p := openai.NewOllamaCompatProvider()
	if p.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("Ollama compat BaseURL = %q, want http://localhost:11434/v1", p.BaseURL)
	}
	if p.Tier() != provider.TierFree {
		t.Errorf("Ollama compat Tier = %q, want free", p.Tier())
	}
}
