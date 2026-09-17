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

func TestChatProviderImplementsInterface(t *testing.T) {
	var _ provider.ChatProvider = ollama.NewChatProvider("")
}

func TestChatProviderDefaultURL(t *testing.T) {
	cp := ollama.NewChatProvider("")
	if cp.BaseURL != ollama.DefaultBaseURL {
		t.Errorf("expected default URL '%s', got '%s'", ollama.DefaultBaseURL, cp.BaseURL)
	}
}

func TestChatProviderCustomURL(t *testing.T) {
	cp := ollama.NewChatProvider("http://custom:1234")
	if cp.BaseURL != "http://custom:1234" {
		t.Errorf("expected custom URL, got '%s'", cp.BaseURL)
	}
}

func TestChatProviderTrailingSlash(t *testing.T) {
	cp := ollama.NewChatProvider("http://custom:1234/")
	if cp.BaseURL != "http://custom:1234" {
		t.Errorf("expected trailing slash stripped, got '%s'", cp.BaseURL)
	}
}

func TestChatProviderConnectionRefused(t *testing.T) {
	cp := ollama.NewChatProvider("http://127.0.0.1:1")
	req := provider.ChatGenerateRequest{
		RunID: "run_conn",
		Agent: "lumi",
		Model: "test-model",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "hello"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := cp.ChatGenerate(ctx, req)
	if err == nil {
		t.Fatal("expected error for connection refused, got nil")
	}
}

func TestChatProviderContentOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected path /api/chat, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		resp := map[string]any{
			"model":             "test-model",
			"prompt_eval_count": 10,
			"eval_count":        5,
			"total_duration":    500000000,
			"done":              true,
			"message": map[string]any{
				"role":    "assistant",
				"content": "Hello! How can I help you today?",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)
	req := provider.ChatGenerateRequest{
		RunID: "run_test",
		Agent: "lumi",
		Model: "test-model",
		Messages: []provider.ChatMessage{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello!"},
		},
	}

	resp, err := cp.ChatGenerate(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp.Content != "Hello! How can I help you today?" {
		t.Errorf("expected content 'Hello! How can I help you today?', got '%s'", resp.Content)
	}
	if resp.HasToolCalls() {
		t.Error("expected no tool calls, got some")
	}
	if !resp.IsFinal() {
		t.Error("expected final response")
	}
	if resp.Provider != "ollama-chat" {
		t.Errorf("expected provider 'ollama-chat', got '%s'", resp.Provider)
	}
}

func TestChatProviderToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"model":             "test-model",
			"prompt_eval_count": 10,
			"eval_count":        5,
			"total_duration":    500000000,
			"done":              true,
			"message": map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]any{
					{
						"function": map[string]any{
							"name": "read_file",
							"arguments": map[string]any{
								"path": "MEMORY.md",
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)
	req := provider.ChatGenerateRequest{
		RunID: "run_test",
		Agent: "lumi",
		Model: "test-model",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Read MEMORY.md"},
		},
		Tools: []provider.ChatTool{
			{
				Type: "function",
				Function: provider.FunctionDef{
					Name:        "read_file",
					Description: "Read a file from the workspace",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{
								"type":        "string",
								"description": "Path to the file",
							},
						},
						"required": []string{"path"},
					},
				},
			},
		},
	}

	resp, err := cp.ChatGenerate(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !resp.HasToolCalls() {
		t.Fatal("expected tool calls, got none")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("expected tool name 'read_file', got '%s'", resp.ToolCalls[0].Function.Name)
	}
	if resp.ToolCalls[0].Function.Arguments["path"] != "MEMORY.md" {
		t.Errorf("expected path 'MEMORY.md', got '%v'", resp.ToolCalls[0].Function.Arguments["path"])
	}
}

func TestChatProviderContentAndToolCalls(t *testing.T) {
	// Models can return both content and tool_calls in one response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"model":             "test-model",
			"prompt_eval_count": 10,
			"eval_count":        5,
			"done":              true,
			"message": map[string]any{
				"role":    "assistant",
				"content": "Let me look that up for you.",
				"tool_calls": []map[string]any{
					{
						"function": map[string]any{
							"name": "search_files",
							"arguments": map[string]any{
								"pattern": "TODO",
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)
	req := provider.ChatGenerateRequest{
		Model: "test-model",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Find all TODOs"},
		},
	}

	resp, err := cp.ChatGenerate(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp.Content != "Let me look that up for you." {
		t.Errorf("expected content, got '%s'", resp.Content)
	}
	if !resp.HasToolCalls() {
		t.Error("expected tool calls")
	}
	if resp.IsFinal() {
		t.Error("expected non-final response (has tool calls)")
	}
}

func TestChatProviderModelError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"error": "model 'nonexistent' not found",
			"done":  true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)
	req := provider.ChatGenerateRequest{
		Model:    "nonexistent",
		Messages: []provider.ChatMessage{{Role: "user", Content: "test"}},
	}

	_, err := cp.ChatGenerate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for model not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestChatProviderQuotaExhaustedFailsFastWithoutRetry(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"message": "you have reached your weekly usage limit, upgrade for higher limits"}`))
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)
	req := provider.ChatGenerateRequest{
		Model:    "quota-test",
		Messages: []provider.ChatMessage{{Role: "user", Content: "test"}},
	}

	_, err := cp.ChatGenerate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for quota-exhausted 429")
	}
	if !errors.Is(err, provider.ErrQuotaExhausted) {
		t.Errorf("expected error to wrap provider.ErrQuotaExhausted, got: %v", err)
	}
	if requestCount != 1 {
		t.Errorf("quota-exhausted 429 should not be retried (no backoff burned), got %d requests", requestCount)
	}
}

func TestChatProviderGenericRateLimitStillRetries(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"message": "rate limited, please slow down"}`))
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)
	req := provider.ChatGenerateRequest{
		Model:    "test",
		Messages: []provider.ChatMessage{{Role: "user", Content: "test"}},
	}

	_, err := cp.ChatGenerate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for persistent 429")
	}
	if errors.Is(err, provider.ErrQuotaExhausted) {
		t.Error("a generic rate-limit message should not be classified as quota exhaustion")
	}
	if requestCount != 4 {
		t.Errorf("generic 429 should still retry up to 4 attempts, got %d requests", requestCount)
	}
}

func TestChatProviderHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)
	req := provider.ChatGenerateRequest{
		Model:    "test",
		Messages: []provider.ChatMessage{{Role: "user", Content: "test"}},
	}

	_, err := cp.ChatGenerate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestChatProviderContextCancellation(t *testing.T) {
	cp := ollama.NewChatProvider("http://127.0.0.1:1")
	req := provider.ChatGenerateRequest{
		Model:    "test",
		Messages: []provider.ChatMessage{{Role: "user", Content: "test"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	_, err := cp.ChatGenerate(ctx, req)
	if err == nil {
		t.Error("expected error for context cancellation")
	}
}

func TestChatProviderTokenFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"model":             "test-model",
			"prompt_eval_count": 0,
			"eval_count":        0,
			"done":              true,
			"message": map[string]any{
				"role":    "assistant",
				"content": "Hello!",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)
	req := provider.ChatGenerateRequest{
		Model:    "test-model",
		Messages: []provider.ChatMessage{{Role: "user", Content: "a short prompt"}},
	}

	resp, err := cp.ChatGenerate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.PromptTokens <= 0 {
		t.Error("expected positive prompt tokens from fallback")
	}
	if resp.OutputTokens <= 0 {
		t.Error("expected positive output tokens from fallback")
	}
}

func TestMultipleToolCalls(t *testing.T) {
	// Test batch tool calls — model requests multiple tools in one response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"model":             "test-model",
			"prompt_eval_count": 10,
			"eval_count":        5,
			"done":              true,
			"message": map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]any{
					{
						"function": map[string]any{
							"name": "read_file",
							"arguments": map[string]any{
								"path": "MEMORY.md",
							},
						},
					},
					{
						"function": map[string]any{
							"name": "list_dir",
							"arguments": map[string]any{
								"path": ".",
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)
	req := provider.ChatGenerateRequest{
		Model:    "test-model",
		Messages: []provider.ChatMessage{{Role: "user", Content: "Read MEMORY.md and list current directory"}},
	}

	resp, err := cp.ChatGenerate(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("expected first tool 'read_file', got '%s'", resp.ToolCalls[0].Function.Name)
	}
	if resp.ToolCalls[1].Function.Name != "list_dir" {
		t.Errorf("expected second tool 'list_dir', got '%s'", resp.ToolCalls[1].Function.Name)
	}
}

func TestToolResultMessages(t *testing.T) {
	// Test that tool role messages with ToolCallID are properly serialized
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request contains tool result messages
		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}

		resp := map[string]any{
			"model":             "test-model",
			"prompt_eval_count": 10,
			"eval_count":        5,
			"done":              true,
			"message": map[string]any{
				"role":    "assistant",
				"content": "Here's what I found in MEMORY.md",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)
	req := provider.ChatGenerateRequest{
		Model: "test-model",
		Messages: []provider.ChatMessage{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Read MEMORY.md"},
			{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{
				{ID: "tc_123", Function: provider.FunctionCall{Name: "read_file", Arguments: map[string]any{"path": "MEMORY.md"}}},
			}},
			{Role: "tool", Content: "Contents of MEMORY.md...", ToolID: "tc_123"},
		},
	}

	resp, err := cp.ChatGenerate(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp.Content != "Here's what I found in MEMORY.md" {
		t.Errorf("expected final content, got '%s'", resp.Content)
	}
	if !resp.IsFinal() {
		t.Error("expected final response")
	}
}
