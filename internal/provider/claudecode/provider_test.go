package claudecode

import (
	"context"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
)

type fakeRunner struct {
	result   RunResult
	err      error
	gotArgs  []string
	gotStdin string
}

func (f *fakeRunner) Run(_ context.Context, _ string, args []string, stdin, _ string) (RunResult, error) {
	f.gotArgs = args
	f.gotStdin = stdin
	return f.result, f.err
}

func TestGenerateParsesEnvelope(t *testing.T) {
	out := `{"type":"result","subtype":"success","is_error":false,"result":"{\"type\":\"final\",\"content\":\"hi\"}","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}`
	fr := &fakeRunner{result: RunResult{Stdout: out}}
	p := NewWithRunner(Config{Model: "claude-opus-4-8"}, fr)

	resp, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "do it"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != `{"type":"final","content":"hi"}` {
		t.Errorf("Text = %q", resp.Text)
	}
	if resp.PromptTokens != 130 { // 10 + 100 + 20
		t.Errorf("PromptTokens = %d, want 130", resp.PromptTokens)
	}
	if resp.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", resp.OutputTokens)
	}
	if resp.Provider != "claude_code" {
		t.Errorf("Provider = %q", resp.Provider)
	}
	if fr.gotStdin != "do it" {
		t.Errorf("stdin = %q, want prompt", fr.gotStdin)
	}
}

func TestGenerateDisablesToolsAndSetsModel(t *testing.T) {
	fr := &fakeRunner{result: RunResult{Stdout: `{"is_error":false,"result":"ok","usage":{}}`}}
	p := NewWithRunner(Config{}, fr)

	if _, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "x", Model: "claude-sonnet-4-6"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	joined := strings.Join(fr.gotArgs, " ")
	if !strings.Contains(joined, "-p") || !strings.Contains(joined, "--output-format json") {
		t.Errorf("args missing print/json: %v", fr.gotArgs)
	}
	// Tools must be disabled: an --allowedTools flag followed by an empty value.
	foundEmptyAllow := false
	for i, a := range fr.gotArgs {
		if a == "--allowedTools" && i+1 < len(fr.gotArgs) && fr.gotArgs[i+1] == "" {
			foundEmptyAllow = true
		}
	}
	if !foundEmptyAllow {
		t.Errorf("expected --allowedTools \"\" to disable tools, got %v", fr.gotArgs)
	}
	// Request model overrides config model.
	if !strings.Contains(joined, "--model claude-sonnet-4-6") {
		t.Errorf("expected request model in args, got %v", fr.gotArgs)
	}
}

func TestGenerateErrorEnvelope(t *testing.T) {
	fr := &fakeRunner{result: RunResult{Stdout: `{"is_error":true,"subtype":"error_max_turns","result":"boom"}`}}
	p := NewWithRunner(Config{}, fr)

	if _, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "x"}); err == nil {
		t.Fatal("expected error for is_error envelope")
	}
}

func TestNormalizeDefaults(t *testing.T) {
	cfg := Normalize(Config{})
	if cfg.Executable != "claude" {
		t.Errorf("Executable = %q, want claude", cfg.Executable)
	}
	if cfg.TimeoutMinutes != DefaultTimeoutMinutes {
		t.Errorf("TimeoutMinutes = %d, want %d", cfg.TimeoutMinutes, DefaultTimeoutMinutes)
	}
}
