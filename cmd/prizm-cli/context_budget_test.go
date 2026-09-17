package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
)

func TestResolveContextMode(t *testing.T) {
	tests := []struct {
		name       string
		globalMode string
		agentMode  string
		want       string
	}{
		{"default both empty", "", "", "full"},
		{"global open_book", "open_book", "", "open_book"},
		{"global full", "full", "", "full"},
		{"agent override takes priority", "full", "open_book", "open_book"},
		{"agent override to full", "open_book", "full", "full"},
		{"agent override when global empty", "", "open_book", "open_book"},
		{"agent override when global empty full", "", "full", "full"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &orchestrator.Config{
				Prizm: orchestrator.PrizmConfig{
					ContextMode: tt.globalMode,
				},
			}
			agentCfg := &orchestrator.AgentConfig{
				ContextMode: tt.agentMode,
			}
			got := resolveContextMode(cfg, agentCfg)
			if got != tt.want {
				t.Errorf("resolveContextMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveAgentLoop(t *testing.T) {
	tests := []struct {
		name       string
		globalLoop string
		agentLoop  string
		want       string
	}{
		{"default both empty", "", "", "classic"},
		{"global agentic", "agentic", "", "agentic"},
		{"global classic", "classic", "", "classic"},
		{"agent override takes priority", "classic", "agentic", "agentic"},
		{"agent override to classic", "agentic", "classic", "classic"},
		{"agent override when global empty", "", "agentic", "agentic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &orchestrator.Config{
				Prizm: orchestrator.PrizmConfig{
					AgentLoop: tt.globalLoop,
				},
			}
			agentCfg := &orchestrator.AgentConfig{
				AgentLoop: tt.agentLoop,
			}
			got := resolveAgentLoop(cfg, agentCfg)
			if got != tt.want {
				t.Errorf("resolveAgentLoop() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetModelContextTokens(t *testing.T) {
	tests := []struct {
		model string
		want  int
		found bool
	}{
		{"glm-5.1:cloud", 202752, true},
		{"deepseek-v4-pro:cloud", 131072, true},
		{"unknown-model", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, ok := getModelContextTokens(tt.model)
			if ok != tt.found {
				t.Errorf("getModelContextTokens(%q) found = %v, want %v", tt.model, ok, tt.found)
			}
			if ok && got != tt.want {
				t.Errorf("getModelContextTokens(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

func TestEstimateTokenCount(t *testing.T) {
	msg := []provider.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello!"},
		{Role: "assistant", Content: "Hi there!"},
	}
	count := estimateTokenCount(msg)
	if count <= 0 {
		t.Errorf("estimateTokenCount() = %d, want > 0", count)
	}
	// Rough check: ~3.2 chars/token, total ~50 chars => ~16 tokens
	if count > 150 {
		t.Errorf("estimateTokenCount() = %d, seems too high for ~50 chars", count)
	}
}

func TestCompressToolResultsDigest(t *testing.T) {
	longResult := strings.Repeat("x", 2000)
	shortResult := "OK"

	messages := []provider.ChatMessage{
		{Role: "user", Content: "Hello"},
		{Role: "tool", Content: longResult, ToolID: "1"},  // index 1, old
		{Role: "tool", Content: longResult, ToolID: "2"},  // index 2, old
		{Role: "tool", Content: shortResult, ToolID: "3"},  // index 3, recent
		{Role: "tool", Content: "plan data", ToolID: "4"},   // index 4, most recent
	}

	compressed := compressToolResults(messages, 2)
	if compressed != 2 {
		t.Errorf("compressToolResults() compressed %d messages, want 2", compressed)
	}
	// Compressed messages should have [SUMMARY] prefix
	if !strings.HasPrefix(messages[1].Content, "[SUMMARY]") {
		t.Errorf("first tool result not compressed to digest: %s", messages[1].Content[:50])
	}
	if !strings.HasPrefix(messages[2].Content, "[SUMMARY]") {
		t.Errorf("second tool result not compressed to digest: %s", messages[2].Content[:50])
	}
	// Recent results should be unchanged
	if messages[3].Content != shortResult {
		t.Errorf("recent tool result was modified: %s", messages[3].Content)
	}
}

func TestCompressToolDigest(t *testing.T) {
	// Short result should be kept as-is
	short := "OK"
	result := compressToolDigest(short, "1")
	if result != short {
		t.Errorf("short result should be kept, got: %s", result)
	}

	// Long error result should get ✗ marker
	longErr := strings.Repeat("error detail ", 30) + `{"error": "file not found", "success": false}`
	result = compressToolDigest(longErr, "2")
	if !strings.Contains(result, "✗") {
		t.Errorf("long error result should have ✗ marker, got: %s", result)
	}

	// Long multi-line result should be compressed to digest
	longResult := "line1\nline2\nline3\nline4\nline5\n"
	result = compressToolDigest(longResult, "3")
	if !strings.HasPrefix(result, "[SUMMARY]") {
		t.Errorf("long result should be compressed to digest, got: %s", result[:50])
	}
}