package anthropic_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/anthropic"
)

// ---------- Streaming tests ----------

func TestProvider_GenerateStream_Success(t *testing.T) {
	sseInput := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_test"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" from"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseInput))
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	ch, err := p.GenerateStream(context.Background(), provider.GenerateRequest{
		Prompt: "Say hello",
		Model:  "claude-sonnet-4-20250514",
	})
	if err != nil {
		t.Fatalf("GenerateStream() error = %v", err)
	}

	var tokens []string
	var finished bool
	var stopReason string
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("Stream error: %v", chunk.Error)
		}
		tokens = append(tokens, chunk.Tokens...)
		if chunk.Finished {
			finished = true
			if chunk.Raw != nil {
				if sr, ok := chunk.Raw["stop_reason"]; ok {
					stopReason = sr.(string)
				}
			}
		}
	}

	if !finished {
		t.Error("Stream never finished")
	}
	fullText := strings.Join(tokens, "")
	if fullText != "Hello from" {
		t.Errorf("Text = %q, want Hello from", fullText)
	}
	if stopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", stopReason)
	}
}

func TestProvider_GenerateStream_ErrorEvent(t *testing.T) {
	sseInput := strings.Join([]string{
		"event: error",
		`data: {"type":"error","error":{"type":"overloaded","message":"Server is overloaded"}}`,
		"",
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseInput))
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	ch, err := p.GenerateStream(context.Background(), provider.GenerateRequest{
		Prompt: "test", Model: "claude-sonnet-4-20250514",
	})
	if err != nil {
		t.Fatalf("GenerateStream() error = %v", err)
	}

	for chunk := range ch {
		if chunk.Error != nil {
			if !strings.Contains(chunk.Error.Error(), "stream error") {
				t.Errorf("Error = %q, want stream error", chunk.Error.Error())
			}
			if !chunk.Finished {
				t.Error("Error chunk should have Finished=true")
			}
			return
		}
	}
	t.Error("Expected error from stream")
}

func TestProvider_GenerateStream_RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	_, err := p.GenerateStream(context.Background(), provider.GenerateRequest{
		Prompt: "test", Model: "claude-sonnet-4-20250514",
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !anthropic.IsRetryableError(err) {
		t.Error("429 should be retryable")
	}
}

func TestProvider_GenerateStream_BadGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	_, err := p.GenerateStream(context.Background(), provider.GenerateRequest{
		Prompt: "test", Model: "claude-sonnet-4-20250514",
	})
	if err == nil {
		t.Fatal("expected error for 502")
	}
	if !anthropic.IsRetryableError(err) {
		t.Error("502 should be retryable")
	}
}

func TestProvider_GenerateStream_ServiceUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	_, err := p.GenerateStream(context.Background(), provider.GenerateRequest{
		Prompt: "test", Model: "claude-sonnet-4-20250514",
	})
	if err == nil {
		t.Fatal("expected error for 503")
	}
	if !anthropic.IsRetryableError(err) {
		t.Error("503 should be retryable")
	}
}

func TestProvider_GenerateStream_AuthHeaders(t *testing.T) {
	var gotAPIKey, gotVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: message_stop\ndata: {}\n\n"))
	}))
	defer server.Close()

	p := anthropic.NewWithBaseURL("test-key", server.URL)
	ch, _ := p.GenerateStream(context.Background(), provider.GenerateRequest{
		Prompt: "test", Model: "claude-sonnet-4-20250514",
	})
	for range ch {
	}

	if gotAPIKey != "test-key" {
		t.Errorf("x-api-key = %q, want test-key", gotAPIKey)
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", gotVersion)
	}
}
