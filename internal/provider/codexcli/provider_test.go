package codexcli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
)

type fakeRunner struct {
	result       RunResult
	err          error
	gotArgs      []string
	gotStdin     string
	gotCwd       string
	writeLastMsg string // if set, write this to the --output-last-message path
}

func (f *fakeRunner) Run(_ context.Context, _ string, args []string, stdin, cwd string) (RunResult, error) {
	f.gotArgs = args
	f.gotStdin = stdin
	f.gotCwd = cwd
	if f.writeLastMsg != "" {
		for i, a := range args {
			if a == "--output-last-message" && i+1 < len(args) {
				_ = os.WriteFile(args[i+1], []byte(f.writeLastMsg), 0644)
			}
		}
	}
	return f.result, f.err
}

func containsArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func argVal(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestGenerateBuildsExecArgs(t *testing.T) {
	fr := &fakeRunner{result: RunResult{Stdout: "fallback"}}
	p := NewWithRunner(Config{Workspace: "."}, fr)

	resp, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "research X"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(fr.gotArgs) == 0 || fr.gotArgs[0] != "exec" {
		t.Fatalf("first arg = %v, want exec", fr.gotArgs)
	}
	if argVal(fr.gotArgs, "--sandbox") != "read-only" {
		t.Errorf("sandbox = %q, want read-only", argVal(fr.gotArgs, "--sandbox"))
	}
	if !containsArg(fr.gotArgs, "--skip-git-repo-check") {
		t.Errorf("args missing --skip-git-repo-check: %v", fr.gotArgs)
	}
	if containsArg(fr.gotArgs, "--ask-for-approval") {
		t.Errorf("args should not contain --ask-for-approval (removed in codex exec): %v", fr.gotArgs)
	}
	if fr.gotArgs[len(fr.gotArgs)-1] != "-" {
		t.Errorf("last arg = %q, want -", fr.gotArgs[len(fr.gotArgs)-1])
	}
	if fr.gotStdin != "research X" {
		t.Errorf("stdin = %q", fr.gotStdin)
	}
	// No model configured → no --model flag.
	if argVal(fr.gotArgs, "--model") != "" {
		t.Errorf("unexpected --model %q", argVal(fr.gotArgs, "--model"))
	}
	// Empty last-message file → falls back to stdout.
	if resp.Text != "fallback" {
		t.Errorf("Text = %q, want fallback", resp.Text)
	}
	if resp.Provider != "codex" {
		t.Errorf("Provider = %q, want codex", resp.Provider)
	}
}

func TestGeneratePrefersLastMessageAndModel(t *testing.T) {
	fr := &fakeRunner{result: RunResult{Stdout: "streamed transcript"}, writeLastMsg: "final answer"}
	p := NewWithRunner(Config{Model: "gpt-5-codex", Workspace: "."}, fr)

	resp, err := p.Generate(context.Background(), provider.GenerateRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "final answer" {
		t.Errorf("Text = %q, want final answer (from --output-last-message)", resp.Text)
	}
	if argVal(fr.gotArgs, "--model") != "gpt-5-codex" {
		t.Errorf("model = %q, want gpt-5-codex", argVal(fr.gotArgs, "--model"))
	}
}

func TestNormalizeDefaults(t *testing.T) {
	c := Normalize(Config{})
	if c.Sandbox != "read-only" || c.ApprovalPolicy != "never" {
		t.Errorf("defaults sandbox=%q approval=%q", c.Sandbox, c.ApprovalPolicy)
	}
	if c.TimeoutMinutes != DefaultTimeoutMinutes {
		t.Errorf("timeout = %d", c.TimeoutMinutes)
	}
	if !strings.Contains(c.Executable, "codex") {
		t.Errorf("executable = %q", c.Executable)
	}
}
