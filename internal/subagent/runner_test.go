package subagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

// scriptBackend is a mock Backend that replays a scripted sequence of model
// turns and records tool executions.
type scriptBackend struct {
	turns     []Turn
	parse     Parser
	toolErr   error
	toolCalls []string
}

func (b *scriptBackend) Bind(_ AgentRuntime) (LLMFunc, Parser, ToolExec, error) {
	i := 0
	llm := func(_ context.Context, _ []v2.Message) (Turn, error) {
		if i >= len(b.turns) {
			return Turn{Text: "FINAL: done (ran out of script)"}, nil
		}
		t := b.turns[i]
		i++
		return t, nil
	}
	exec := func(_ context.Context, tool string, _ map[string]any) (string, error) {
		b.toolCalls = append(b.toolCalls, tool)
		if b.toolErr != nil {
			return "", b.toolErr
		}
		return "ok", nil
	}
	return llm, b.parse, exec, nil
}

// simple parser: "TOOL <name>" → tool call; "FINAL: x" → final; else neither.
func lineParser(text string) Action {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "FINAL:") {
		return Action{Final: true, Content: strings.TrimSpace(strings.TrimPrefix(text, "FINAL:"))}
	}
	if strings.HasPrefix(text, "TOOL ") {
		return Action{Tool: strings.TrimSpace(strings.TrimPrefix(text, "TOOL "))}
	}
	return Action{}
}

func TestLoopRunner_ToolThenFinal(t *testing.T) {
	backend := &scriptBackend{
		parse: func(text string) Action {
			// fetch_image tool call carries a path so we can assert artifact capture.
			if strings.HasPrefix(text, "TOOL") {
				return Action{Tool: "fetch_image", Input: map[string]any{"path": "references/goblin.png"}}
			}
			return lineParser(text)
		},
		turns: []Turn{
			{Text: "TOOL fetch_image", PromptTokens: 10, CompletionTokens: 5},
			{Text: "FINAL: saved one reference image", PromptTokens: 8, CompletionTokens: 4},
		},
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend})

	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T1", Description: "find a goblin ref"}, AgentRuntime{AgentID: "scout"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Summary != "saved one reference image" {
		t.Errorf("summary = %q", res.Summary)
	}
	if len(res.Artifacts.FilePaths) != 1 || res.Artifacts.FilePaths[0] != "references/goblin.png" {
		t.Errorf("artifacts not captured: %+v", res.Artifacts)
	}
	if len(backend.toolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(backend.toolCalls))
	}
}

func TestLoopRunner_ImmediateFinal(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{{Text: "FINAL: nothing to do"}}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T2"}, AgentRuntime{AgentID: "muse"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Summary != "nothing to do" {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestLoopRunner_ToolFailureIsFedBackNotFatal(t *testing.T) {
	backend := &scriptBackend{
		parse: func(text string) Action {
			if strings.HasPrefix(text, "TOOL") {
				return Action{Tool: "mcp_blender_export", Input: map[string]any{}}
			}
			return lineParser(text)
		},
		toolErr: errors.New("blender not running"),
		turns: []Turn{
			{Text: "TOOL mcp_blender_export"},
			{Text: "FINAL: could not export, blender offline"},
		},
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T3"}, AgentRuntime{AgentID: "chisel"})
	if err != nil {
		t.Fatalf("tool failure should not be fatal to the run: %v", err)
	}
	if !strings.Contains(res.Summary, "could not export") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestLoopRunner_IterationBudgetExceeded(t *testing.T) {
	// Always asks for a tool, never finalizes → must hit the iteration cap.
	backend := &scriptBackend{parse: func(string) Action { return Action{Tool: "read_file", Input: map[string]any{}} }}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 3})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T4"}, AgentRuntime{AgentID: "scout"})
	if err == nil || !strings.Contains(err.Error(), "did not complete within 3 iterations") {
		t.Fatalf("expected iteration-budget error, got %v", err)
	}
	if len(backend.toolCalls) != 3 {
		t.Errorf("expected 3 tool calls before budget, got %d", len(backend.toolCalls))
	}
}

func TestLoopRunner_RuntimeIterationBudgetNarrowsRunnerLimit(t *testing.T) {
	backend := &scriptBackend{
		parse: func(string) Action {
			return Action{Tool: "read_file", Input: map[string]any{}}
		},
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 10})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-role-limit"},
		AgentRuntime{AgentID: "scout", MaxIterations: 2})
	if err == nil || !strings.Contains(err.Error(), "did not complete within 2 iterations") {
		t.Fatalf("expected role iteration-budget error, got %v", err)
	}
	if len(backend.toolCalls) != 2 {
		t.Errorf("expected 2 tool calls before role budget, got %d", len(backend.toolCalls))
	}
}

func TestLoopRunner_TokenBudgetExceeded(t *testing.T) {
	backend := &scriptBackend{
		parse: func(string) Action { return Action{Tool: "read_file", Input: map[string]any{}} },
		turns: []Turn{{Text: "TOOL read_file", PromptTokens: 100, CompletionTokens: 100}},
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 10, MaxTokens: 150})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T5"}, AgentRuntime{AgentID: "muse"})
	if err == nil || !strings.Contains(err.Error(), "token budget") {
		t.Fatalf("expected token-budget error, got %v", err)
	}
}
func TestLoopRunner_DefaultTokenBudget(t *testing.T) {
	r := NewLoopRunner(LoopRunnerConfig{Backend: &scriptBackend{}})
	if r.maxTokens != DefaultMaxTokens {
		t.Fatalf("default maxTokens = %d, want %d", r.maxTokens, DefaultMaxTokens)
	}

	r = NewLoopRunner(LoopRunnerConfig{Backend: &scriptBackend{}, MaxTokens: 123})
	if r.maxTokens != 123 {
		t.Fatalf("explicit maxTokens = %d, want 123", r.maxTokens)
	}
}

func TestLoopRunner_BindError(t *testing.T) {
	r := NewLoopRunner(LoopRunnerConfig{Backend: bindErrBackend{}})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T6"}, AgentRuntime{AgentID: "x"})
	if err == nil || !strings.Contains(err.Error(), "bind agent") {
		t.Fatalf("expected bind error, got %v", err)
	}
}

type bindErrBackend struct{}

func (bindErrBackend) Bind(AgentRuntime) (LLMFunc, Parser, ToolExec, error) {
	return nil, nil, nil, errors.New("no provider for model")
}

func TestLoopRunner_ContextCancel(t *testing.T) {
	backend := &scriptBackend{parse: func(string) Action { return Action{Tool: "read_file", Input: map[string]any{}} }}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 100})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := r.Run(ctx, v2.TaskPacket{TaskID: "T7"}, AgentRuntime{AgentID: "scout"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// End-to-end through the Worker: the LoopRunner feeding a real Worker.Handle.
func TestWorker_WithLoopRunner(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{{Text: "FINAL: complete"}}}
	runner := NewLoopRunner(LoopRunnerConfig{Backend: backend})
	res := mapResolver{"scout": {AgentID: "scout"}}
	w := NewWorker(res, runner, 0)

	c := w.Handle(context.Background(), v2.TaskPacket{TargetAgent: "scout", TaskID: "E2E"})
	if c.Status != "completed" || c.OutputSummary != "complete" {
		t.Fatalf("unexpected completion: %+v", c)
	}
}

func TestLoopRunner_TaskPacketTokenBudgetOverridesRunnerDefault(t *testing.T) {
	backend := &scriptBackend{
		parse: func(text string) Action { return Action{Tool: "read_file", Input: map[string]any{"path": "notes.md"}} },
		turns: []Turn{
			{Text: "TOOL read_file", PromptTokens: 100, CompletionTokens: 50},
			{Text: "TOOL read_file", PromptTokens: 100, CompletionTokens: 50},
		},
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 10})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-budget", MaxTokens: 250}, AgentRuntime{AgentID: "muse"})
	if err == nil || !strings.Contains(err.Error(), "token budget 250 exhausted") {
		t.Fatalf("expected packet token-budget error, got %v", err)
	}
	if res.PromptTokens+res.CompletionTokens != 300 {
		t.Fatalf("usage not returned on budget error: %+v", res)
	}
	if len(res.Artifacts.FilePaths) != 1 || res.Artifacts.FilePaths[0] != "notes.md" {
		t.Fatalf("partial artifacts not returned on budget error: %+v", res.Artifacts)
	}
	if len(backend.toolCalls) != 1 {
		t.Fatalf("exhausting turn should not execute another tool, got %d calls", len(backend.toolCalls))
	}
}

func TestLoopRunner_TaskPacketUnlimitedOverridesRunnerBudget(t *testing.T) {
	backend := &scriptBackend{
		parse: lineParser,
		turns: []Turn{
			{Text: "TOOL read_file", PromptTokens: 100, CompletionTokens: 100},
			{Text: "FINAL: complete", PromptTokens: 1, CompletionTokens: 1},
		},
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 10, MaxTokens: 10})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-unlimited", MaxTokens: v2.UnlimitedTokens}, AgentRuntime{AgentID: "muse"})
	if err != nil {
		t.Fatalf("unlimited packet budget should not trip runner budget: %v", err)
	}
	if res.Summary != "complete" {
		t.Fatalf("summary = %q", res.Summary)
	}
}

func TestLoopRunner_TokenBudgetStopsOverBudgetFinal(t *testing.T) {
	backend := &scriptBackend{
		parse: lineParser,
		turns: []Turn{{Text: "FINAL: complete", PromptTokens: 100, CompletionTokens: 100}},
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 10, MaxTokens: 150})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-final"}, AgentRuntime{AgentID: "muse"})
	if err == nil || !strings.Contains(err.Error(), "token budget 150 exhausted") {
		t.Fatalf("expected token-budget error, got %v", err)
	}
	if res.Summary == "complete" {
		t.Fatalf("over-budget final answer should not be reported as success: %+v", res)
	}
}
