package agent

import (
	"testing"

	"github.com/emaharmony/prizm/internal/tool"
)

func TestParseFinalResponse(t *testing.T) {
	input := `{"type": "final", "content": "The project uses event-driven architecture."}`
	resp := ParseAgentOutput(input)

	if resp.Type != ResponseFinal {
		t.Errorf("expected type 'final', got %s", resp.Type)
	}
	if resp.Content != "The project uses event-driven architecture." {
		t.Errorf("unexpected content: %s", resp.Content)
	}
}

func TestParseToolRequest(t *testing.T) {
	input := `{"type": "tool_request", "tool": "read_file", "input": {"path": "README.md"}}`
	resp := ParseAgentOutput(input)

	if resp.Type != ResponseToolRequest {
		t.Errorf("expected type 'tool_request', got %s", resp.Type)
	}
	if resp.ToolName != "read_file" {
		t.Errorf("expected tool 'read_file', got %s", resp.ToolName)
	}
	if resp.ToolInput["path"] != "README.md" {
		t.Errorf("expected input path 'README.md', got %v", resp.ToolInput["path"])
	}
}

func TestParseInvalidJSON(t *testing.T) {
	input := "This is just plain text, not JSON."
	resp := ParseAgentOutput(input)

	if resp.Type != ResponseFinal {
		t.Errorf("invalid JSON should fall back to 'final', got %s", resp.Type)
	}
	if resp.Content != input {
		t.Errorf("fallback content should be the raw input, got %s", resp.Content)
	}
}

func TestParseUnknownType(t *testing.T) {
	input := `{"type": "unknown_type", "data": "something"}`
	resp := ParseAgentOutput(input)

	if resp.Type != ResponseFinal {
		t.Errorf("unknown type should fall back to 'final', got %s", resp.Type)
	}
}

func TestParseToolRequestMissingToolName(t *testing.T) {
	input := `{"type": "tool_request", "input": {"path": "README.md"}}`
	resp := ParseAgentOutput(input)

	// Missing tool name should fall back to final response
	if resp.Type != ResponseFinal {
		t.Errorf("tool_request without tool name should fall back to 'final', got %s", resp.Type)
	}
}

func TestParseToolRequestWithEchoTool(t *testing.T) {
	input := `{"type": "tool_request", "tool": "echo", "input": {"text": "hello"}}`
	resp := ParseAgentOutput(input)

	if resp.Type != ResponseToolRequest {
		t.Errorf("expected type 'tool_request', got %s", resp.Type)
	}
	if resp.ToolName != "echo" {
		t.Errorf("expected tool 'echo', got %s", resp.ToolName)
	}
	if resp.ToolInput["text"] != "hello" {
		t.Errorf("expected input text 'hello', got %v", resp.ToolInput["text"])
	}
}

func TestParseJSONWithMarkdownFence(t *testing.T) {
	input := "```json\n{\"type\": \"final\", \"content\": \"Hello world\"}\n```"
	resp := ParseAgentOutput(input)

	if resp.Type != ResponseFinal {
		t.Errorf("expected type 'final', got %s", resp.Type)
	}
	if resp.Content != "Hello world" {
		t.Errorf("expected content 'Hello world', got %s", resp.Content)
	}
}

func TestParseToolRequestWithMarkdownFence(t *testing.T) {
	input := "```json\n{\"type\": \"tool_request\", \"tool\": \"list_dir\", \"input\": {\"path\": \".\"}}\n```"
	resp := ParseAgentOutput(input)

	if resp.Type != ResponseToolRequest {
		t.Errorf("expected type 'tool_request', got %s", resp.Type)
	}
	if resp.ToolName != "list_dir" {
		t.Errorf("expected tool 'list_dir', got %s", resp.ToolName)
	}
}

func TestBuildToolPromptSuffix(t *testing.T) {
	toolInfos := []tool.ToolInfo{
		{Name: "echo", Description: "Returns the input text unchanged.", Schema: ToolSchemaForTest("echo")},
		{Name: "read_file", Description: "Reads a text file.", Schema: ToolSchemaForTest("read_file")},
	}
	suffix := BuildToolPromptSuffix(toolInfos, "/test/workspace", "/other/projects")

	if suffix == "" {
		t.Error("tool prompt suffix should not be empty")
	}
	if !containsStr(suffix, "echo") {
		t.Error("tool prompt suffix should list available tools including echo")
	}
	if !containsStr(suffix, "read_file") {
		t.Error("tool prompt suffix should list available tools including read_file")
	}
	if !containsStr(suffix, "tool_request") {
		t.Error("tool prompt suffix should explain tool_request format")
	}
	if !containsStr(suffix, "final") {
		t.Error("tool prompt suffix should explain final format")
	}
}

func ToolSchemaForTest(name string) tool.ToolSchema {
	switch name {
	case "echo":
		return tool.ToolSchema{
			Input: map[string]tool.ParamSpec{"text": {Type: "string", Description: "The text to echo", Required: true}},
		}
	case "read_file":
		return tool.ToolSchema{
			Input: map[string]tool.ParamSpec{"path": {Type: "string", Description: "File path", Required: true}},
		}
	default:
		return tool.ToolSchema{}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
func TestParseAgentOutputWithFallbackEmbeddedToolRequest(t *testing.T) {
	// This is the exact pattern Prizm produces: natural language + embedded JSON
	input := `Let me start by staging the files. I'll also add the backward-compatible aliases. {"type":"tool_request","tool":"git_add","input":{"path":"/Users/ema/projects/repos/BassBook/apps/web/src/app/globals.css"}}`
	resp := ParseAgentOutputWithFallback(input)

	if resp.Type != ResponseToolRequest {
		t.Errorf("expected type 'tool_request', got %s (content: %s)", resp.Type, resp.Content)
	}
	if resp.ToolName != "git_add" {
		t.Errorf("expected tool 'git_add', got %s", resp.ToolName)
	}
}

func TestParseAgentOutputWithFallbackPureToolRequest(t *testing.T) {
	input := `{"type":"tool_request","tool":"read_file","input":{"path":"README.md"}}`
	resp := ParseAgentOutputWithFallback(input)

	if resp.Type != ResponseToolRequest {
		t.Errorf("expected type 'tool_request', got %s", resp.Type)
	}
	if resp.ToolName != "read_file" {
		t.Errorf("expected tool 'read_file', got %s", resp.ToolName)
	}
}

func TestParseAgentOutputWithFallbackFinalResponse(t *testing.T) {
	input := `{"type":"final","content":"Done working on BassBook."}`
	resp := ParseAgentOutputWithFallback(input)

	if resp.Type != ResponseFinal {
		t.Errorf("expected type 'final', got %s", resp.Type)
	}
	if resp.Content != "Done working on BassBook." {
		t.Errorf("unexpected content: %s", resp.Content)
	}
}

func TestParseAgentOutputWithFallbackPlainText(t *testing.T) {
	input := `I'll read the file now. Let me check the current state.`
	resp := ParseAgentOutputWithFallback(input)

	if resp.Type != ResponseFinal {
		t.Errorf("expected type 'final' for plain text, got %s", resp.Type)
	}
}
