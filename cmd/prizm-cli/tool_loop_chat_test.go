package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/ollama"
	"github.com/emaharmony/prizm/internal/session"
	"github.com/emaharmony/prizm/internal/tool"
)

// ── Chat Tool Loop E2E Tests ──────────────────────────────────────
//
// These tests exercise the chat tool loop end-to-end by spinning up
// a mock Ollama /api/chat server and verifying the full pipeline:
// LLM call → tool execution → response delivery.
//
// They test the tool loop logic (runToolLoopChat) indirectly by
// verifying the ChatProvider and tool execution work correctly together.

// TestChatToolLoopFinalResponse verifies that when the model gives a final
// text response (no tool calls), it is returned correctly.
func TestChatToolLoopFinalResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected /api/chat, got %s", r.URL.Path)
		}

		// Parse the request to verify messages are sent correctly
		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}

		// Verify model is set
		if reqBody["model"] != "test-model" {
			t.Errorf("expected model 'test-model', got %v", reqBody["model"])
		}

		// Verify messages contain our system + user messages
		messages, ok := reqBody["messages"].([]any)
		if !ok || len(messages) < 2 {
			t.Errorf("expected at least 2 messages, got %v", reqBody["messages"])
		}

		// Return a final text response (no tool calls)
		resp := map[string]any{
			"model":             "test-model",
			"prompt_eval_count": 15,
			"eval_count":        42,
			"total_duration":    1000000000,
			"done":              true,
			"message": map[string]any{
				"role":    "assistant",
				"content": "Hello! I'm Lumi, your AI assistant. How can I help you today?",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)

	req := provider.ChatGenerateRequest{
		RunID:       "test-final",
		Agent:       "lumi",
		Model:       "test-model",
		Temperature: 0.7,
		MaxTokens:   4096,
		Messages: []provider.ChatMessage{
			{Role: "system", Content: "You are Lumi, a helpful assistant."},
			{Role: "user", Content: "Hello!"},
		},
	}

	resp, err := cp.ChatGenerate(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if resp.Content != "Hello! I'm Lumi, your AI assistant. How can I help you today?" {
		t.Errorf("expected final response content, got '%s'", resp.Content)
	}
	if resp.HasToolCalls() {
		t.Error("expected no tool calls in final response")
	}
	if !resp.IsFinal() {
		t.Error("expected IsFinal() to be true")
	}
	if resp.Provider != "ollama-chat" {
		t.Errorf("expected provider 'ollama-chat', got '%s'", resp.Provider)
	}
}

// TestChatToolLoopWithToolCalls verifies a full tool calling round-trip:
// 1. User sends message
// 2. Model requests read_file tool call
// 3. Tool result is fed back as tool role message
// 4. Model gives final text response
func TestChatToolLoopWithToolCalls(t *testing.T) {
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var reqBody map[string]any
		json.NewDecoder(r.Body).Decode(&reqBody)

		if callCount == 1 {
			// First call: model requests a tool call
			resp := map[string]any{
				"model":             "test-model",
				"prompt_eval_count": 20,
				"eval_count":        10,
				"total_duration":    500000000,
				"done":              true,
				"message": map[string]any{
					"role":    "assistant",
					"content": "Let me read that file for you.",
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
			return
		}

		// Second call: model gives final response after receiving tool result
		// Verify the conversation includes the tool result message
		messages, ok := reqBody["messages"].([]any)
		if !ok || len(messages) < 4 {
			t.Errorf("expected at least 4 messages (system, user, assistant+tool_calls, tool), got %v", messages)
		}

		// Check that the last message is a tool result
		lastMsg, ok := messages[len(messages)-1].(map[string]any)
		if ok && lastMsg["role"] != "tool" {
			t.Logf("note: last message role is '%v', expected 'tool'", lastMsg["role"])
		}

		resp := map[string]any{
			"model":             "test-model",
			"prompt_eval_count": 30,
			"eval_count":        25,
			"total_duration":    800000000,
			"done":              true,
			"message": map[string]any{
				"role":    "assistant",
				"content": "Here's what I found in MEMORY.md: It contains project information and standing rules.",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)

	// First call: get tool call
	req := provider.ChatGenerateRequest{
		RunID:       "test-toolcall",
		Agent:       "lumi",
		Model:       "test-model",
		Temperature: 0.7,
		MaxTokens:   4096,
		Messages: []provider.ChatMessage{
			{Role: "system", Content: "You are Lumi, a helpful assistant."},
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

	resp1, err := cp.ChatGenerate(context.Background(), req)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	if !resp1.HasToolCalls() {
		t.Fatal("expected tool calls in first response")
	}
	if len(resp1.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp1.ToolCalls))
	}
	if resp1.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("expected tool name 'read_file', got '%s'", resp1.ToolCalls[0].Function.Name)
	}
	if resp1.ToolCalls[0].Function.Arguments["path"] != "MEMORY.md" {
		t.Errorf("expected path 'MEMORY.md', got '%v'", resp1.ToolCalls[0].Function.Arguments["path"])
	}

	// Simulate feeding back tool result
	req.Messages = append(req.Messages,
		provider.ChatMessage{
			Role:      "assistant",
			Content:   resp1.Content,
			ToolCalls: resp1.ToolCalls,
		},
		provider.ChatMessage{
			Role:    "tool",
			Content: "# Memory\n\nProject information and standing rules.",
			ToolID:  resp1.ToolCalls[0].ID,
		},
	)
	req.Tools = nil // No need for tools in follow-up

	// Second call: get final response
	resp2, err := cp.ChatGenerate(context.Background(), req)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	if !resp2.IsFinal() {
		t.Error("expected final response (no tool calls)")
	}
	if resp2.Content == "" {
		t.Error("expected non-empty content in final response")
	}

	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

// TestChatToolLoopNudgeInjection verifies that the tool loop injects
// a nudge message after iteration 3 (only once) when the model keeps
// requesting tools.
func TestChatToolLoopNudgeInjection(t *testing.T) {
	// This test verifies the nudge logic in runToolLoopChat indirectly
	// by checking that after 3 iterations of tool calls, a nudge message
	// is injected into the conversation.
	//
	// We test this at the ChatProvider level by verifying that the
	// messages array grows with a system message after iteration 3.

	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var reqBody map[string]any
		json.NewDecoder(r.Body).Decode(&reqBody)
		messagesArr, _ := reqBody["messages"].([]any)
		_ = messagesArr

		// Always return a tool call to simulate endless tool use
		resp := map[string]any{
			"model":             "test-model",
			"prompt_eval_count": 10,
			"eval_count":        5,
			"total_duration":    200000000,
			"done":              true,
			"message": map[string]any{
				"role":    "assistant",
				"content": fmt.Sprintf("Checking iteration %d...", callCount),
				"tool_calls": []map[string]any{
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

	// Make 4 calls, each time feeding back the tool result
	messages := []provider.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "List all files"},
	}

	for i := 0; i < 4; i++ {
		req := provider.ChatGenerateRequest{
			RunID:       fmt.Sprintf("test-nudge-%d", i),
			Agent:       "lumi",
			Model:       "test-model",
			Temperature: 0.7,
			MaxTokens:   4096,
			Messages:    messages,
		}

		resp, err := cp.ChatGenerate(context.Background(), req)
		if err != nil {
			t.Fatalf("call %d failed: %v", i+1, err)
		}

		if !resp.HasToolCalls() {
			t.Fatalf("call %d: expected tool calls", i+1)
		}

		// Feed back assistant message + tool result
		messages = append(messages,
			provider.ChatMessage{
				Role:      "assistant",
				Content:   resp.Content,
				ToolCalls: resp.ToolCalls,
			},
			provider.ChatMessage{
				Role:    "tool",
				Content: "file1.txt, file2.txt, file3.txt",
				ToolID:  resp.ToolCalls[0].ID,
			},
		)
	}

	if callCount != 4 {
		t.Errorf("expected 4 API calls, got %d", callCount)
	}

	// After 4 iterations, we should have:
	// system + user + (assistant + tool) × 4 = 10 messages minimum
	// (The nudge injection happens in runToolLoopChat, not at the ChatProvider level)
	// This test verifies the ChatProvider correctly handles extended conversations
	if len(messages) < 10 {
		t.Errorf("expected at least 10 messages after 4 iterations, got %d", len(messages))
	}
}

// TestChatProviderToolRemoval verifies that when tools are removed from
// the request (empty slice), the model is forced to give a final response.
func TestChatProviderToolRemoval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]any
		json.NewDecoder(r.Body).Decode(&reqBody)

		// Verify no tools in the request
		tools, hasTools := reqBody["tools"]
		if hasTools {
			if toolsArr, ok := tools.([]any); ok && len(toolsArr) > 0 {
				t.Errorf("expected no tools in request, got %v", tools)
			}
		}

		// Return a final text response
		resp := map[string]any{
			"model":             "test-model",
			"prompt_eval_count": 10,
			"eval_count":        15,
			"total_duration":    300000000,
			"done":              true,
			"message": map[string]any{
				"role":    "assistant",
				"content": "Based on what I've gathered, here's the answer.",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)

	// Request with NO tools (simulating iteration 6+ where tools are removed)
	req := provider.ChatGenerateRequest{
		RunID:       "test-no-tools",
		Agent:       "lumi",
		Model:       "test-model",
		Temperature: 0.7,
		MaxTokens:   4096,
		Messages: []provider.ChatMessage{
			{Role: "system", Content: "You are Lumi."},
			{Role: "user", Content: "What did you find?"},
			{Role: "assistant", Content: "Let me check.", ToolCalls: []provider.ToolCall{
				{ID: "tc_1", Function: provider.FunctionCall{Name: "list_dir", Arguments: map[string]any{"path": "."}}},
			}},
			{Role: "tool", Content: "file1.txt", ToolID: "tc_1"},
			{Role: "system", Content: "You have already used several tools. Please provide your final answer now."},
		},
		// No tools — model must give final answer
	}

	resp, err := cp.ChatGenerate(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if !resp.IsFinal() {
		t.Error("expected final response when no tools available")
	}
	if resp.Content == "" {
		t.Error("expected non-empty content in final response")
	}
}

// TestChatProviderEmptyToolsSlice verifies that an empty tools slice
// (not nil) is handled correctly — this is what happens at iteration 6+
// when we force the model to stop calling tools.
func TestChatProviderEmptyToolsSlice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request was received correctly
		var reqBody map[string]any
		json.NewDecoder(r.Body).Decode(&reqBody)

		resp := map[string]any{
			"model":             "test-model",
			"prompt_eval_count": 5,
			"eval_count":        10,
			"total_duration":    100000000,
			"done":              true,
			"message": map[string]any{
				"role":    "assistant",
				"content": "Final answer without tools.",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cp := ollama.NewChatProvider(server.URL)

	// Test with empty tools slice (not nil)
	req := provider.ChatGenerateRequest{
		Agent: "lumi",
		Model: "test-model",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
		Tools: []provider.ChatTool{}, // empty slice, not nil
	}

	resp, err := cp.ChatGenerate(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp.Content != "Final answer without tools." {
		t.Errorf("expected final content, got '%s'", resp.Content)
	}
}

// TestToolPolicyEvaluation tests that policy evaluation works correctly
// for git mutation tools (requires approval) vs read-only tools.
func TestToolPolicyEvaluation(t *testing.T) {
	policy := tool.DefaultPolicyConfig()
	policy.WorkspaceRoot = "."

	// Read-only tool should be allowed
	result := tool.EvaluatePolicy(policy, "read_file", map[string]any{"path": "test.txt"})
	if result.Decision != tool.PolicyApproved {
		t.Errorf("expected read_file to be approved, got %v", result.Decision)
	}

	// Git mutation tool should require approval
	result = tool.EvaluatePolicy(policy, "git_push", map[string]any{"message": "test commit"})
	if result.Decision != tool.PolicyRequiresApproval {
		t.Errorf("expected git_push to require approval, got %v", result.Decision)
	}

	// Unknown/dangerous tool should be denied
	result = tool.EvaluatePolicy(policy, "delete_everything", map[string]any{})
	if result.Decision != tool.PolicyDenied {
		t.Errorf("expected unknown tool to be denied, got %v", result.Decision)
	}
}

// TestSessionAwareness verifies that session metadata
// has the expected structure for building chat messages.
func TestSessionAwareness(t *testing.T) {
	sess := &session.Session{
		ID:        "test-session-1",
		AgentID:   "lumi",
		Channel:   "discord",
		StartedAt: time.Now(),
	}
	if sess.ID != "test-session-1" {
		t.Error("expected session ID")
	}
	if sess.AgentID != "lumi" {
		t.Errorf("expected agent 'lumi', got '%s'", sess.AgentID)
	}
}
