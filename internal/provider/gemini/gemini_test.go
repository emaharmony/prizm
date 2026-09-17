package gemini_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/gemini"
)

func TestProvider_Name(t *testing.T) {
	p := gemini.New("test-key")
	if p.Name() != "gemini" {
		t.Errorf("Name() = %q, want gemini", p.Name())
	}
}

func TestProvider_Tier(t *testing.T) {
	p := gemini.New("test-key")
	if p.Tier() != provider.TierPaid {
		t.Errorf("Tier() = %q, want paid", p.Tier())
	}
}

func TestProvider_DefaultBaseURL(t *testing.T) {
	p := gemini.New("test-key")
	if p.BaseURL != "https://generativelanguage.googleapis.com" {
		t.Errorf("BaseURL = %q, want https://generativelanguage.googleapis.com", p.BaseURL)
	}
}

func TestProvider_Generate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify API key is in query parameter
		if r.URL.Query().Get("key") != "test-key" {
			t.Errorf("key param = %q, want test-key", r.URL.Query().Get("key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Hello from Gemini!"}]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 12,
				"candidatesTokenCount": 6,
				"totalTokenCount": 18
			}
		}`))
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	resp, err := p.Generate(context.Background(), provider.GenerateRequest{
		Prompt: "Say hello",
		Model:  "gemini-2.0-flash",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.Text != "Hello from Gemini!" {
		t.Errorf("Text = %q, want Hello from Gemini!", resp.Text)
	}
	if resp.Provider != "gemini" {
		t.Errorf("Provider = %q, want gemini", resp.Provider)
	}
	if resp.PromptTokens != 12 {
		t.Errorf("PromptTokens = %d, want 12", resp.PromptTokens)
	}
	if resp.OutputTokens != 6 {
		t.Errorf("OutputTokens = %d, want 6", resp.OutputTokens)
	}
}

func TestProvider_Generate_RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !gemini.IsRetryableError(err) {
		t.Error("429 error should be retryable")
	}
}

func TestProvider_Generate_ServiceUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
	if err == nil {
		t.Fatal("expected error for 503")
	}
	if !gemini.IsRetryableError(err) {
		t.Error("503 error should be retryable")
	}
}

func TestProvider_Generate_NoCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates": []}`))
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
	if err == nil {
		t.Fatal("expected error for empty candidates")
	}
}

func TestProvider_Generate_EmptyText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": []},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
	if err == nil {
		t.Fatal("expected error for empty text")
	}
}

func TestProvider_Generate_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := gemini.New("test-key")
	_, err := p.Generate(ctx, provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
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

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestProvider_Generate_InternalServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": {"code": 500, "message": "internal error", "status": "INTERNAL"}}`))
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestProvider_Generate_MultipleParts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [
						{"text": "Hello "},
						{"text": "from "},
						{"text": "Gemini!"}
					]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 12,
				"candidatesTokenCount": 8,
				"totalTokenCount": 20
			}
		}`))
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	resp, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.Text != "Hello from Gemini!" {
		t.Errorf("Text = %q, want Hello from Gemini!", resp.Text)
	}
}

func TestProvider_Generate_NoUsageMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "Hello!"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	resp, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.PromptTokens != 0 {
		t.Errorf("PromptTokens = %d, want 0 (no usage metadata)", resp.PromptTokens)
	}
	if resp.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0 (no usage metadata)", resp.OutputTokens)
	}
}
