package provider

import "context"

// ChatProvider handles LLM generation with native chat messages and tool calling.
// Providers implementing ChatProvider support structured messages (system/user/assistant/tool)
// and native tool_calls in responses, eliminating the need for text-based tool parsing.
//
// Use Go interface assertions to check if a Provider also implements ChatProvider:
//
//	if chatProv, ok := prov.(ChatProvider); ok {
//	    // use native tool calling
//	} else {
//	    // fall back to text-based ParseAgentOutput
//	}
type ChatProvider interface {
	// ChatGenerate sends a chat completion request with structured messages and optional tools.
	// Returns a ChatGenerateResponse which may contain Content (final text) and/or ToolCalls.
	ChatGenerate(ctx context.Context, req ChatGenerateRequest) (ChatGenerateResponse, error)
}

// ChatGenerateRequest is the input for a chat-based generation call.
// Unlike GenerateRequest which takes a flat Prompt string, this uses structured messages.
type ChatGenerateRequest struct {
	RunID         string
	CorrelationID string
	Agent         string
	Model         string
	Messages      []ChatMessage
	Tools         []ChatTool
	Temperature   float64
	MaxTokens     int
	// ReasoningEffort selects a provider-supported named thinking level. Empty
	// leaves the provider's model-specific default in effect.
	ReasoningEffort string
}

// ChatMessage represents a single message in a conversation.
type ChatMessage struct {
	Role      string     `json:"role"`                   // "system", "user", "assistant", "tool"
	Content   string     `json:"content"`                // text content (may be empty for tool_calls-only messages)
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`   // present when role="assistant" and model requests tools
	ToolID    string     `json:"tool_call_id,omitempty"` // present when role="tool" to link result to the call
}

// ToolCall represents a single tool call in an assistant message.
type ToolCall struct {
	ID       string       `json:"id"`
	Function FunctionCall `json:"function"`
}

// FunctionCall is the function details of a tool call.
type FunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ChatGenerateResponse is the result from a ChatProvider.
// Content is the text response (may be empty if only tool_calls are returned).
// ToolCalls is the list of tool calls the model wants to execute (may be empty for final responses).
// At least one of Content or ToolCalls will be non-empty.
type ChatGenerateResponse struct {
	Content      string
	ToolCalls    []ToolCall
	Model        string
	Provider     string
	LatencyMS    int64
	PromptTokens int
	OutputTokens int
	Raw          map[string]any
}

// HasToolCalls returns true if the response contains at least one tool call.
func (r ChatGenerateResponse) HasToolCalls() bool {
	return len(r.ToolCalls) > 0
}

// IsFinal returns true if the response is a final text response (no tool calls).
func (r ChatGenerateResponse) IsFinal() bool {
	return len(r.ToolCalls) == 0 && r.Content != ""
}

// ChatTool describes a tool that can be called by the model.
// This is converted from the tool registry's ToolInfo into Ollama's function schema format.
type ChatTool struct {
	Type     string      `json:"type"` // always "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef describes a function that the model can call.
type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema object
}
