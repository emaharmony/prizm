package gemini_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/gemini"
)

// ---------- Streaming tests ----------

func TestProvider_GenerateStream_Success(t *testing.T) {
	sseInput := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}]}`,
		"",
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify alt=sse in URL
		if r.URL.Query().Get("alt") != "sse" {
			t.Errorf("alt = %q, want sse", r.URL.Query().Get("alt"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseInput))
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	ch, err := p.GenerateStream(context.Background(), provider.GenerateRequest{
		Prompt: "Say hello",
		Model:  "gemini-2.0-flash",
	})
	if err != nil {
		t.Fatalf("GenerateStream() error = %v", err)
	}

	var tokens []string
	var finished bool
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("Stream error: %v", chunk.Error)
		}
		tokens = append(tokens, chunk.Tokens...)
		if chunk.Finished {
			finished = true
		}
	}

	if !finished {
		t.Error("Stream never finished")
	}
	fullText := strings.Join(tokens, "")
	if fullText != "Hello" {
		t.Errorf("Text = %q, want Hello", fullText)
	}
}

func TestProvider_GenerateStream_SafetyFinish(t *testing.T) {
	sseInput := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"SAFETY"}]}`,
		"",
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseInput))
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	ch, err := p.GenerateStream(context.Background(), provider.GenerateRequest{
		Prompt: "test", Model: "gemini-2.0-flash",
	})
	if err != nil {
		t.Fatalf("GenerateStream() error = %v", err)
	}

	var finished bool
	var finishReason string
	for chunk := range ch {
		if chunk.Finished {
			finished = true
			if chunk.Raw != nil {
				if fr, ok := chunk.Raw["finish_reason"]; ok {
					finishReason = fr.(string)
				}
			}
		}
	}

	if !finished {
		t.Error("Stream never finished")
	}
	if finishReason != "SAFETY" {
		t.Errorf("finish_reason = %q, want SAFETY", finishReason)
	}
}

func TestProvider_GenerateStream_RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.GenerateStream(context.Background(), provider.GenerateRequest{
		Prompt: "test", Model: "gemini-2.0-flash",
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !gemini.IsRetryableError(err) {
		t.Error("429 should be retryable")
	}
}

func TestProvider_GenerateStream_BadGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.GenerateStream(context.Background(), provider.GenerateRequest{
		Prompt: "test", Model: "gemini-2.0-flash",
	})
	if err == nil {
		t.Fatal("expected error for 502")
	}
	if !gemini.IsRetryableError(err) {
		t.Error("502 should be retryable")
	}
}

func TestProvider_GenerateStream_ServiceUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.GenerateStream(context.Background(), provider.GenerateRequest{
		Prompt: "test", Model: "gemini-2.0-flash",
	})
	if err == nil {
		t.Fatal("expected error for 503")
	}
	if !gemini.IsRetryableError(err) {
		t.Error("503 should be retryable")
	}
}

// ---------- Missing sync edge cases from Mango review ----------

func TestProvider_Generate_ForbiddenError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":403,"message":"API key not valid","status":"PERMISSION_DENIED"}}`))
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if gemini.IsRetryableError(err) {
		t.Error("403 should NOT be retryable")
	}
}

func TestProvider_Generate_UnauthorizedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"code":401,"message":"API key not provided","status":"UNAUTHENTICATED"}}`))
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if gemini.IsRetryableError(err) {
		t.Error("401 should NOT be retryable")
	}
}

func TestProvider_Generate_BadGatewayRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	p := gemini.NewWithBaseURL("test-key", server.URL)
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "test", Model: "gemini-2.0-flash"})
	if err == nil {
		t.Fatal("expected error for 502")
	}
	if !gemini.IsRetryableError(err) {
		t.Error("502 should be retryable")
	}
}
