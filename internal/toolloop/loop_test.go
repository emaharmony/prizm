package toolloop

import (
	"context"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
)

// mockChatProvider returns a response with no tool calls (final answer).
type mockChatProvider struct {
	response provider.ChatGenerateResponse
}

func (m *mockChatProvider) ChatGenerate(ctx context.Context, req provider.ChatGenerateRequest) (provider.ChatGenerateResponse, error) {
	return m.response, nil
}

// mockSink records calls for assertions.
type mockSink struct {
	toolCalls   []string
	toolResults []string
	completions []string
	errors      []error
}

func (m *mockSink) OnToolCall(name string, args map[string]any) {
	m.toolCalls = append(m.toolCalls, name)
}

func (m *mockSink) OnToolResult(name string, result string, summary CallSummary) {
	m.toolResults = append(m.toolResults, name)
}

func (m *mockSink) OnProgress(content string) {}

func (m *mockSink) OnComplete(content string, modelInfo ModelInfo) {
	m.completions = append(m.completions, content)
}

func (m *mockSink) OnError(err error) {
	m.errors = append(m.errors, err)
}

func TestRunChatLoop_FinalResponse(t *testing.T) {
	chatProv := &mockChatProvider{
		response: provider.ChatGenerateResponse{
			Content: "Hello! How can I help?",
			Model:   "test-model",
		},
	}
	sink := &mockSink{}
	cfg := DefaultConfig()
	agentCfg := &orchestrator.AgentConfig{ID: "test", Model: "test-model"}

	result, err := RunChatLoop(
		context.Background(),
		[]provider.ChatMessage{{Role: "user", Content: "hi"}},
		nil, // no tools
		chatProv,
		agentCfg,
		nil, // no tool executor needed
		sink,
		cfg,
		nil, // no hooks
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Hello! How can I help?" {
		t.Errorf("expected 'Hello! How can I help?', got %q", result.Content)
	}
	if len(sink.completions) != 1 {
		t.Errorf("expected 1 completion, got %d", len(sink.completions))
	}
	if len(sink.toolCalls) != 0 {
		t.Errorf("expected 0 tool calls, got %d", len(sink.toolCalls))
	}
}

func TestCLIConfig(t *testing.T) {
	cfg := CLIConfig()
	if cfg.MaxIterations <= 0 {
		t.Error("CLIConfig should have positive MaxIterations")
	}
	if cfg.NudgeAfter <= 0 {
		t.Error("CLIConfig should have positive NudgeAfter")
	}
	if cfg.ForceFinalAfter <= 0 {
		t.Error("CLIConfig should have positive ForceFinalAfter")
	}
	if cfg.MaxIterations >= DefaultConfig().MaxIterations {
		t.Error("CLIConfig should have fewer iterations than DefaultConfig")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxIterations != 100 {
		t.Errorf("expected MaxIterations=100, got %d", cfg.MaxIterations)
	}
	if cfg.NudgeAfter != 50 {
		t.Errorf("expected NudgeAfter=50, got %d", cfg.NudgeAfter)
	}
	if cfg.ForceFinalAfter != 80 {
		t.Errorf("expected ForceFinalAfter=80, got %d", cfg.ForceFinalAfter)
	}
}

func TestFormatToolOutput(t *testing.T) {
	tests := []struct {
		name   string
		output map[string]any
		want   string
	}{
		{"content key", map[string]any{"content": "hello"}, "hello"},
		{"output key", map[string]any{"output": "world"}, "world"},
		{"text key", map[string]any{"text": "docs"}, "docs"},
		{"fallback to json", map[string]any{"foo": "bar"}, "{\"foo\":\"bar\"}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolOutput(tt.output)
			if got != tt.want {
				t.Errorf("formatToolOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateStr(t *testing.T) {
	if got := truncateStr("hello", 10); got != "hello" {
		t.Errorf("truncateStr short = %q, want %q", got, "hello")
	}
	if got := truncateStr("hello world this is long", 10); got != "hello worl..." {
		t.Errorf("truncateStr long = %q, want %q", got, "hello worl...")
	}
}