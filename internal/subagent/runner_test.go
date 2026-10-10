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
	turns      []Turn
	parse      Parser
	toolErr    error
	toolErrs   map[string]error
	toolResult string
	toolCalls  []string
	messages   [][]v2.Message
}

func (b *scriptBackend) Bind(_ AgentRuntime) (LLMFunc, Parser, ToolExec, error) {
	i := 0
	llm := func(_ context.Context, messages []v2.Message) (Turn, error) {
		b.messages = append(b.messages, append([]v2.Message(nil), messages...))
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
		if err := b.toolErrs[tool]; err != nil {
			return "", err
		}
		if b.toolResult != "" {
			return b.toolResult, nil
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

func TestLoopRunner_ProposalToolThenRoleFinal(t *testing.T) {
	backend := &scriptBackend{
		parse: func(text string) Action {
			if strings.Contains(text, `"type":"tool_request"`) {
				return Action{Tool: "write_file_proposal", Input: map[string]any{"path": "feature.txt", "content": "proposal"}}
			}
			return Action{Final: true, Content: text}
		},
		turns: []Turn{
			{Text: `{"type":"tool_request","tool":"write_file_proposal","input":{"path":"feature.txt","content":"proposal"}}`, PromptTokens: 10, CompletionTokens: 5},
			{Text: `{"schema_version":1,"summary":"proposed","changed_artifacts":[{"kind":"file","uri":"feature.txt"}],"handoff":{"objective":"test","reason":"proposal recorded"}}`},
		},
	}
	// The existing scripted backend is intentionally simple; this focused run
	// proves the loop accepts a proposal action then a strict role final within
	// the narrowed two-turn budget.
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 2, MaxTokens: 100})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-proposal"}, AgentRuntime{AgentID: "forge", MaxIterations: 2})
	if err != nil || !strings.Contains(res.Summary, `"schema_version":1`) || len(backend.toolCalls) != 1 {
		t.Fatalf("result=%+v toolCalls=%v err=%v", res, backend.toolCalls, err)
	}
	if len(backend.messages) < 2 || !strings.Contains(backend.messages[1][len(backend.messages[1])-1].Content, `Tool "write_file_proposal" result`) {
		t.Fatalf("proposal result was not returned before final: %#v", backend.messages)
	}
	if got := backend.messages[1][len(backend.messages[1])-1].Content; !strings.Contains(got, "1 model turns and 85 aggregate tokens remain") {
		t.Fatalf("proposal result omitted the effective budget notice: %q", got)
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

func TestClassifyAction(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		action Action
		want   string
	}{
		{name: "final", text: "done", action: Action{Final: true}, want: "final"},
		{name: "tool", text: "call", action: Action{Tool: "read_file"}, want: "tool"},
		{name: "empty", text: "  \n", want: "empty"},
		{name: "unrecognized", text: "plain text", want: "unrecognized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyAction(tt.text, tt.action); got != tt.want {
				t.Fatalf("classifyAction() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWithBudgetNotice(t *testing.T) {
	if got := withBudgetNotice("result", 3, 1200); got != "result\n3 model turns and 1200 aggregate tokens remain; finish as soon as you have enough evidence." {
		t.Fatalf("notice = %q", got)
	}
	if got := withBudgetNotice("result", 3, -1); got != "result\n3 model turns remain; finish as soon as you have enough evidence." {
		t.Fatalf("unlimited notice = %q", got)
	}
	if got := withBudgetNotice("result", 0, 1200); got != "result\nNo model turns remain after this response." {
		t.Fatalf("exhausted notice = %q", got)
	}
}

func TestLoopRunnerReservesFinalReplyAfterLateToolFeedback(t *testing.T) {
	const contract = `JSON schema: {"schema_version":1,"task_breakdown":["..."]}`
	backend := &scriptBackend{
		parse: lineParser,
		turns: []Turn{
			{Text: "TOOL read_file", PromptTokens: 400, CompletionTokens: 50},
			{Text: "FINAL: bounded result", PromptTokens: 20, CompletionTokens: 10},
		},
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 3, MaxTokens: 1600, FinalReplyReserveTokens: 100})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-reserve", FinalResponseContract: contract}, AgentRuntime{AgentID: "planner"})
	if err != nil || res.Summary != "bounded result" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if len(backend.toolCalls) != 1 || backend.toolCalls[0] != "read_file" {
		t.Fatalf("late tool result was not preserved before finalization: %v", backend.toolCalls)
	}
	if got := backend.messages[1][len(backend.messages[1])-1].Content; !strings.Contains(got, "tool phase is now closed") || !strings.Contains(got, contract) || !strings.HasSuffix(got, "Return exactly one JSON object. Do not wrap it in Markdown and do not add prose.") {
		t.Fatalf("finalization instruction missing: %q", got)
	}
}

func TestFinalizationInstructionPreservesGenericCompatibility(t *testing.T) {
	if got := finalizationInstruction(""); got != genericFinalizationInstruction {
		t.Fatalf("generic instruction changed: %q", got)
	}
}

func TestLoopRunnerFailsWhenToolRequestedDuringFinalization(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{
		{Text: "TOOL read_file", PromptTokens: 400, CompletionTokens: 50},
		{Text: "TOOL search_files", PromptTokens: 20, CompletionTokens: 10},
	}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 3, MaxTokens: 1600, FinalReplyReserveTokens: 100})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-ignore-reserve"}, AgentRuntime{AgentID: "planner"})
	if err == nil || !strings.Contains(err.Error(), "did not return a final answer during its reserved finalization turn") {
		t.Fatalf("err=%v", err)
	}
	if len(backend.toolCalls) != 1 || backend.toolCalls[0] != "read_file" {
		t.Fatalf("unexpected tool executions across finalization: %v", backend.toolCalls)
	}
}

func TestLoopRunnerFailsWhenFinalizationTurnIsEmpty(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{
		{Text: "TOOL read_file", PromptTokens: 400, CompletionTokens: 50},
		{Text: "", PromptTokens: 20, CompletionTokens: 10},
	}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 3, MaxTokens: 1600, FinalReplyReserveTokens: 100})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-empty-final"}, AgentRuntime{AgentID: "planner"})
	if err == nil || !strings.Contains(err.Error(), "did not return a final answer during its reserved finalization turn") {
		t.Fatalf("err=%v", err)
	}
	if len(backend.messages) != 2 {
		t.Fatalf("finalization made %d provider calls, want exactly 2 total", len(backend.messages))
	}
}

func TestLoopRunnerFailsBeforeFinalCallWhenReserveCannotFit(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{{Text: "TOOL read_file", PromptTokens: 600, CompletionTokens: 100}}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 3, MaxTokens: 1000, FinalReplyReserveTokens: 400})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-no-room"}, AgentRuntime{AgentID: "planner"})
	if err == nil || !strings.Contains(err.Error(), "lacks budget for its reserved final response") {
		t.Fatalf("err=%v", err)
	}
	if len(backend.messages) != 1 {
		t.Fatalf("unexpected extra provider call: %d", len(backend.messages))
	}
}

func TestLoopRunnerDisablesImpossibleReserveForSmallerTaskBudget(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{
		{Text: "TOOL read_file", PromptTokens: 1000, CompletionTokens: 100},
		{Text: "FINAL: default workflow remains usable", PromptTokens: 1200, CompletionTokens: 100},
	}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 3, MaxTokens: 100_000, FinalReplyReserveTokens: 8192})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-small-budget", MaxTokens: 8000}, AgentRuntime{AgentID: "planner"})
	if err != nil || res.Summary != "default workflow remains usable" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if len(backend.messages) != 2 {
		t.Fatalf("provider calls=%d, want 2", len(backend.messages))
	}
	for _, message := range backend.messages[1] {
		if strings.Contains(message.Content, "tool phase is now closed") {
			t.Fatalf("impossible finalization phase was armed: %q", message.Content)
		}
	}
}

func TestLoopRunnerCompletesRequiredToolBeforeFinalization(t *testing.T) {
	const contract = `JSON schema: {"schema_version":1,"summary":"..."}`
	backend := &scriptBackend{parse: lineParser, turns: []Turn{
		{Text: "TOOL read_file", PromptTokens: 4000, CompletionTokens: 100},
		{Text: "TOOL write_file_proposal", PromptTokens: 8500, CompletionTokens: 100},
		{Text: "FINAL: proposal persisted", PromptTokens: 17_000, CompletionTokens: 200},
	}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 4, MaxTokens: 45_000, FinalReplyReserveTokens: 8192})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-prerequisite", FinalizationPrerequisiteTool: "write_file_proposal", FinalResponseContract: contract}, AgentRuntime{AgentID: "developer"})
	if err != nil || res.Summary != "proposal persisted" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if got := strings.Join(backend.toolCalls, ","); got != "read_file,write_file_proposal" {
		t.Fatalf("tool calls=%q", got)
	}
	if transcript := backend.messages[1]; !strings.Contains(transcript[len(transcript)-1].Content, "required governance action") {
		t.Fatalf("prerequisite instruction missing: %#v", transcript)
	}
	if transcript := backend.messages[2]; !strings.Contains(transcript[len(transcript)-1].Content, "tool phase is now closed") || !strings.Contains(transcript[len(transcript)-1].Content, contract) {
		t.Fatalf("finalization instruction missing: %#v", transcript)
	}
}

func TestLoopRunnerRejectsImmediateFinalBeforeRequiredTool(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{{Text: "FINAL: skipped proposal", PromptTokens: 1000, CompletionTokens: 100}}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 4, MaxTokens: 48_000, FinalReplyReserveTokens: 8192})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-immediate-final", FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
	if err == nil || !strings.Contains(err.Error(), "before required tool") {
		t.Fatalf("err=%v", err)
	}
	if len(backend.toolCalls) != 0 || len(backend.messages) != 1 {
		t.Fatalf("calls=%d tools=%v", len(backend.messages), backend.toolCalls)
	}
}

func TestLoopRunnerDisablesTwoReplyReserveBelowExactThreshold(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{
		{Text: "TOOL write_file_proposal", PromptTokens: 1000, CompletionTokens: 100},
		{Text: "FINAL: proposal persisted", PromptTokens: 2200, CompletionTokens: 100},
	}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 3, MaxTokens: 100_000, FinalReplyReserveTokens: 8192})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-small-governed-budget", MaxTokens: 15_000, FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
	if err != nil || res.Summary != "proposal persisted" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if got := strings.Join(backend.toolCalls, ","); got != "write_file_proposal" {
		t.Fatalf("tool calls=%q", got)
	}
	for _, message := range backend.messages[1] {
		if strings.Contains(message.Content, "tool phase is now closed") || strings.Contains(message.Content, "remaining task budget is reserved") {
			t.Fatalf("two-reply reserve armed below threshold: %q", message.Content)
		}
	}
}

func TestLoopRunnerKeepsTwoReplyReserveAtExactThreshold(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{{Text: "TOOL read_file", PromptTokens: 1000, CompletionTokens: 100}}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 4, MaxTokens: 100_000, FinalReplyReserveTokens: 8192})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-exact-governed-threshold", MaxTokens: 16_384, FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
	if err == nil || !strings.Contains(err.Error(), "lacks budget for required tool") {
		t.Fatalf("err=%v", err)
	}
	if len(backend.messages) != 1 {
		t.Fatalf("provider calls=%d, want 1", len(backend.messages))
	}
}

func TestLoopRunnerPrerequisiteRequiresTwoRemainingIterations(t *testing.T) {
	t.Run("exactly two succeeds", func(t *testing.T) {
		backend := &scriptBackend{parse: lineParser, turns: []Turn{
			{Text: "TOOL write_file_proposal", PromptTokens: 1000, CompletionTokens: 100},
			{Text: "FINAL: done", PromptTokens: 2200, CompletionTokens: 100},
		}}
		r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 2, MaxTokens: 48_000, FinalReplyReserveTokens: 8192})
		res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-two-iterations", FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
		if err != nil || res.Summary != "done" {
			t.Fatalf("result=%+v err=%v", res, err)
		}
		if transcript := backend.messages[0]; !strings.Contains(transcript[len(transcript)-1].Content, "required governance action") {
			t.Fatalf("prerequisite was not armed before the first of two calls: %#v", transcript)
		}
	})

	t.Run("one fails before provider call", func(t *testing.T) {
		backend := &scriptBackend{parse: lineParser, turns: []Turn{{Text: "TOOL write_file_proposal", PromptTokens: 1000, CompletionTokens: 100}}}
		r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 1, MaxTokens: 48_000, FinalReplyReserveTokens: 8192})
		_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-one-iteration", FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
		if err == nil || !strings.Contains(err.Error(), "lacks two iterations") {
			t.Fatalf("err=%v", err)
		}
		if len(backend.messages) != 0 {
			t.Fatalf("provider calls=%d, want 0", len(backend.messages))
		}
	})
}

func TestLoopRunnerRejectsInvalidPrerequisiteTurnWithoutExecutingTool(t *testing.T) {
	tests := []struct {
		name string
		turn Turn
	}{
		{name: "wrong tool", turn: Turn{Text: "TOOL search_files", PromptTokens: 4200, CompletionTokens: 100}},
		{name: "final", turn: Turn{Text: "FINAL: skipped proposal", PromptTokens: 4200, CompletionTokens: 100}},
		{name: "empty", turn: Turn{Text: "", PromptTokens: 4200, CompletionTokens: 100}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &scriptBackend{parse: lineParser, turns: []Turn{
				{Text: "TOOL read_file", PromptTokens: 4000, CompletionTokens: 100}, tt.turn,
			}}
			r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 4, MaxTokens: 45_000, FinalReplyReserveTokens: 8192})
			_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-bad-prerequisite", FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
			if err == nil {
				t.Fatal("expected prerequisite error")
			}
			if got := strings.Join(backend.toolCalls, ","); got != "read_file" {
				t.Fatalf("unexpected tool execution: %q", got)
			}
		})
	}
}

func TestLoopRunnerRequiredToolFailureDoesNotSatisfyPrerequisite(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, toolErrs: map[string]error{"write_file_proposal": errors.New("proposal rejected")}, turns: []Turn{
		{Text: "TOOL read_file", PromptTokens: 4000, CompletionTokens: 100},
		{Text: "TOOL write_file_proposal", PromptTokens: 4200, CompletionTokens: 100},
	}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 4, MaxTokens: 45_000, FinalReplyReserveTokens: 8192})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-failed-prerequisite", FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
	if err == nil || !strings.Contains(err.Error(), "required tool") {
		t.Fatalf("err=%v", err)
	}
	if len(backend.messages) != 2 {
		t.Fatalf("provider calls=%d, want 2", len(backend.messages))
	}
}

func TestLoopRunnerDeniedRequiredToolDoesNotSatisfyPrerequisite(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{
		{Text: "TOOL read_file", PromptTokens: 4000, CompletionTokens: 100},
		{Text: "TOOL write_file_proposal", PromptTokens: 4200, CompletionTokens: 100},
	}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, Scope: DefaultToolScope(), MaxIterations: 4, MaxTokens: 45_000, FinalReplyReserveTokens: 8192})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-denied-prerequisite", FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err=%v", err)
	}
	if got := strings.Join(backend.toolCalls, ","); got != "read_file" {
		t.Fatalf("denied prerequisite executed: %q", got)
	}
}

func TestLoopRunnerFailsBeforeUnaffordablePrerequisiteCall(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{
		{Text: "TOOL read_file", PromptTokens: 7000, CompletionTokens: 100},
		{Text: "TOOL write_file_proposal", PromptTokens: 15_000, CompletionTokens: 8192},
	}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 4, MaxTokens: 45_000, FinalReplyReserveTokens: 8192})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-no-prerequisite-room", FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
	if err == nil || !strings.Contains(err.Error(), "required tool") {
		t.Fatalf("err=%v", err)
	}
	if len(backend.messages) != 1 || len(backend.toolCalls) != 1 {
		t.Fatalf("calls=%d tools=%v", len(backend.messages), backend.toolCalls)
	}
}

func TestLoopRunnerPrerequisiteAffordabilityRetainsPendingFeedbackGrowth(t *testing.T) {
	backend := &scriptBackend{
		parse:      lineParser,
		toolResult: strings.Repeat("x", 3000),
		turns: []Turn{
			{Text: "TOOL read_file", PromptTokens: 1000, CompletionTokens: 100},
			{Text: "TOOL write_file_proposal", PromptTokens: 5000, CompletionTokens: 100},
		},
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 4, MaxTokens: 34_000, FinalReplyReserveTokens: 8192})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-prerequisite-feedback-growth", FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
	if err == nil || !strings.Contains(err.Error(), "lacks budget for required tool") {
		t.Fatalf("err=%v", err)
	}
	if len(backend.messages) != 1 || strings.Join(backend.toolCalls, ",") != "read_file" {
		t.Fatalf("pending feedback allowed unsafe prerequisite call: calls=%d tools=%v", len(backend.messages), backend.toolCalls)
	}
}

func TestLoopRunnerPrerequisiteAffordabilityIncludesLongFinalContract(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{
		{Text: "TOOL read_file", PromptTokens: 1000, CompletionTokens: 100},
		{Text: "TOOL write_file_proposal", PromptTokens: 3000, CompletionTokens: 100},
	}}
	contract := "JSON schema: " + strings.Repeat("x", 20_000)
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 4, MaxTokens: 48_000, FinalReplyReserveTokens: 8192})
	_, err := r.Run(context.Background(), v2.TaskPacket{
		TaskID:                       "T-long-final-contract",
		FinalizationPrerequisiteTool: "write_file_proposal",
		FinalResponseContract:        contract,
	}, AgentRuntime{AgentID: "developer"})
	if err == nil || !strings.Contains(err.Error(), "lacks budget for required tool") {
		t.Fatalf("err=%v", err)
	}
	if len(backend.messages) != 1 || strings.Join(backend.toolCalls, ",") != "read_file" {
		t.Fatalf("long contract allowed unsafe prerequisite call: calls=%d tools=%v", len(backend.messages), backend.toolCalls)
	}
}

func TestLoopRunnerArmsPrerequisiteBeforeUnaffordableOrdinaryTurn(t *testing.T) {
	backend := &scriptBackend{
		parse:      lineParser,
		toolResult: strings.Repeat("x", 3000),
		turns: []Turn{
			{Text: "TOOL read_file", PromptTokens: 2313, CompletionTokens: 69},
			{Text: "TOOL write_file_proposal", PromptTokens: 6000, CompletionTokens: 100},
			{Text: "FINAL: proposal persisted", PromptTokens: 12_000, CompletionTokens: 200},
		},
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 5, MaxTokens: 48_000, FinalReplyReserveTokens: 8192})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-forecast-full-branch", FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
	if err != nil || res.Summary != "proposal persisted" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if got := strings.Join(backend.toolCalls, ","); got != "read_file,write_file_proposal" {
		t.Fatalf("unreserved ordinary turn executed: %q", got)
	}
	transcript := backend.messages[1]
	want := prerequisiteInstruction("write_file_proposal")
	if transcript[len(transcript)-1].Content != want {
		t.Fatalf("call 2 instruction=%q, want %q", transcript[len(transcript)-1].Content, want)
	}
}

func TestShouldEnterPrerequisiteFullBranchBoundary(t *testing.T) {
	const (
		prompt     = 2313
		completion = 69
		reserve    = 8192
		growth     = 3274
	)
	futureGrowth := futureToolTurnGrowthReserve(finalizationInstruction(""), prerequisiteInstruction("write_file_proposal"))
	threeCall := ordinaryPrerequisiteAndFinalEstimate(prompt, completion, reserve, growth, futureGrowth)
	if !shouldEnterPrerequisite(threeCall, 0, prompt, completion, reserve, growth, futureGrowth) {
		t.Fatal("exact full-branch boundary did not arm prerequisite")
	}
	if shouldEnterPrerequisite(threeCall+1, 0, prompt, completion, reserve, growth, futureGrowth) {
		t.Fatal("one token above full-branch boundary armed prerequisite")
	}
}

func TestLoopRunnerAlreadySatisfiedPrerequisiteUsesNormalFinalization(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{
		{Text: "TOOL write_file_proposal", PromptTokens: 4000, CompletionTokens: 100},
		{Text: "FINAL: done", PromptTokens: 4200, CompletionTokens: 100},
	}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 3, MaxTokens: 25_000, FinalReplyReserveTokens: 8192})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-satisfied", FinalizationPrerequisiteTool: "write_file_proposal"}, AgentRuntime{AgentID: "developer"})
	if err != nil || res.Summary != "done" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if transcript := backend.messages[1]; strings.Contains(transcript[len(transcript)-1].Content, "required governance action") {
		t.Fatalf("satisfied prerequisite re-armed prerequisite phase: %#v", transcript)
	}
}

func TestLoopRunnerFinalizationPreservesDenialAndToolErrorFeedback(t *testing.T) {
	tests := []struct {
		name         string
		backend      *scriptBackend
		scope        ToolScope
		wantFeedback string
	}{
		{
			name: "denial",
			backend: &scriptBackend{parse: lineParser, turns: []Turn{
				{Text: "TOOL write_file", PromptTokens: 400, CompletionTokens: 50},
				{Text: "FINAL: denied safely", PromptTokens: 20, CompletionTokens: 10},
			}},
			scope:        DefaultToolScope(),
			wantFeedback: "not permitted",
		},
		{
			name: "tool error",
			backend: &scriptBackend{parse: lineParser, toolErr: errors.New("offline"), turns: []Turn{
				{Text: "TOOL read_file", PromptTokens: 400, CompletionTokens: 50},
				{Text: "FINAL: error recorded", PromptTokens: 20, CompletionTokens: 10},
			}},
			wantFeedback: "failed: offline",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewLoopRunner(LoopRunnerConfig{Backend: tt.backend, Scope: tt.scope, MaxIterations: 3, MaxTokens: 1600, FinalReplyReserveTokens: 100})
			if _, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-feedback"}, AgentRuntime{AgentID: "planner"}); err != nil {
				t.Fatal(err)
			}
			transcript := tt.backend.messages[1]
			joined := ""
			for _, message := range transcript {
				joined += message.Content + "\n"
			}
			if !strings.Contains(joined, tt.wantFeedback) || !strings.Contains(joined, "tool phase is now closed") {
				t.Fatalf("finalization transcript omitted feedback: %q", joined)
			}
		})
	}
}

func TestLoopRunnerFinalizationFitsConfiguredReplyAllowance(t *testing.T) {
	backend := &scriptBackend{parse: lineParser, turns: []Turn{
		{Text: "TOOL read_file", PromptTokens: 4000, CompletionTokens: 1000},
		{Text: "FINAL: complete near allowance", PromptTokens: 4000, CompletionTokens: 8000},
	}}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 3, MaxTokens: 20000, FinalReplyReserveTokens: 8192})
	res, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-full-reply"}, AgentRuntime{AgentID: "planner"})
	if err != nil || res.Summary != "complete near allowance" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if res.PromptTokens+res.CompletionTokens != 17000 {
		t.Fatalf("usage=%+v", res)
	}
}

func TestLoopRunnerReserveIncludesPendingToolFeedback(t *testing.T) {
	backend := &scriptBackend{
		parse:      lineParser,
		toolResult: strings.Repeat("x", 3000),
		turns:      []Turn{{Text: "TOOL read_file", PromptTokens: 500, CompletionTokens: 100}},
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, MaxIterations: 3, MaxTokens: 10000, FinalReplyReserveTokens: 8192})
	_, err := r.Run(context.Background(), v2.TaskPacket{TaskID: "T-large-feedback"}, AgentRuntime{AgentID: "planner"})
	if err == nil || !strings.Contains(err.Error(), "lacks budget for its reserved final response") {
		t.Fatalf("err=%v", err)
	}
	if len(backend.messages) != 1 {
		t.Fatalf("large feedback allowed an unsafe final call: %d provider calls", len(backend.messages))
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
