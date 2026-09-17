package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/tool"
)

// ── Tool Loop Tests ──────────────────────────────────────────────

func TestFormatToolResult(t *testing.T) {
	tests := []struct {
		name   string
		result tool.ToolResult
		want   string
	}{
		{
			name:   "success with output",
			result: tool.ToolResult{Output: map[string]any{"output": "file contents here"}, Success: true},
			want:   "file contents here",
		},
		{
			name:   "error result",
			result: tool.ToolResult{Success: false, Error: "file not found"},
			want:   "Error: file not found",
		},
		{
			name:   "empty output",
			result: tool.ToolResult{Success: true},
			want:   "(tool returned no output)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolResult(tt.result)
			if got != tt.want {
				t.Errorf("formatToolResult() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatToolResultTruncation(t *testing.T) {
	result := tool.ToolResult{Output: map[string]any{"output": strings.Repeat("x", 10000)}, Success: true}
	got := formatToolResult(result)
	if len(got) > 8100 {
		t.Errorf("formatToolResult() too long: %d chars", len(got))
	}
	if !strings.HasSuffix(got, "... (truncated)") {
		t.Errorf("formatToolResult() should end with '... (truncated)', got suffix: %q", got[len(got)-20:])
	}
}

func TestFormatToolResultPrompt(t *testing.T) {
	result := formatToolResultPrompt("read_file", "file contents here")
	if len(result) == 0 {
		t.Error("formatToolResultPrompt should not be empty")
	}
	if result[:5] != "Tool " {
		t.Errorf("expected prompt to start with 'Tool ', got %q", result[:5])
	}
}

func TestFormatToolErrorPrompt(t *testing.T) {
	err := formatToolErrorPrompt("read_file", fmt.Errorf("file not found"))
	if len(err) == 0 {
		t.Error("formatToolErrorPrompt should not be empty")
	}
	if err[:5] != "Tool " {
		t.Errorf("expected prompt to start with 'Tool ', got %q", err[:5])
	}
}

func TestFormatApprovalMessage(t *testing.T) {
	parsed := agent.AgentResponse{
		Type:      agent.ResponseToolRequest,
		ToolName:  "write_file_proposal",
		ToolInput: map[string]any{"path": "test.go"},
	}
	msg := formatApprovalMessage(parsed, tool.ToolResult{Success: true, Output: map[string]any{
		"approval_id": "appr_test",
		"run_id":      "run_test",
	}})
	if msg == "" {
		t.Error("formatApprovalMessage should not be empty")
	}
	if len(msg) < 10 {
		t.Errorf("approval message too short: %q", msg)
	}
}

func TestFormatToolAction(t *testing.T) {
	tests := []struct {
		name   string
		parsed agent.AgentResponse
		want   string
	}{
		{
			name: "read_file with path",
			parsed: agent.AgentResponse{
				Type:      agent.ResponseToolRequest,
				ToolName:  "read_file",
				ToolInput: map[string]any{"path": "main.go"},
			},
			want: "read file main.go",
		},
		{
			name: "list_dir with path",
			parsed: agent.AgentResponse{
				Type:      agent.ResponseToolRequest,
				ToolName:  "list_dir",
				ToolInput: map[string]any{"path": "/tmp"},
			},
			want: "list directory /tmp",
		},
		{
			name: "write_file_proposal with path",
			parsed: agent.AgentResponse{
				Type:      agent.ResponseToolRequest,
				ToolName:  "write_file_proposal",
				ToolInput: map[string]any{"path": "output.txt"},
			},
			want: "write to file output.txt",
		},
		{
			name: "unknown tool",
			parsed: agent.AgentResponse{
				Type:     agent.ResponseToolRequest,
				ToolName: "custom_tool",
			},
			want: "use tool custom_tool",
		},
		{
			name: "read_file without path",
			parsed: agent.AgentResponse{
				Type:     agent.ResponseToolRequest,
				ToolName: "read_file",
			},
			want: "read a file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolAction(tt.parsed)
			if got != tt.want {
				t.Errorf("formatToolAction() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateStr(t *testing.T) {
	if got := truncateStr("hello", 10); got != "hello" {
		t.Errorf("truncateStr(hello, 10) = %q, want %q", got, "hello")
	}
	if got := truncateStr("hello world", 5); got != "hello..." {
		t.Errorf("truncateStr(hello world, 5) = %q, want %q", got, "hello...")
	}
}

func TestMaxToolIterations(t *testing.T) {
	if maxToolIterations != 10 {
		t.Errorf("maxToolIterations = %d, want 10", maxToolIterations)
	}
}

func TestToolLoopTimeout(t *testing.T) {
	if toolLoopTimeout != 2*time.Minute {
		t.Errorf("toolLoopTimeout = %v, want 2m", toolLoopTimeout)
	}
}
