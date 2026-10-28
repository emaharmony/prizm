package codexcli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
)

type constrainedSelectorRunner struct {
	args             []string
	stdin            string
	cwd              string
	schemaPath       string
	lastPath         string
	schema           []byte
	workspaceEntries int
	writeLast        string
	result           RunResult
	err              error
	calls            int
}

func (r *constrainedSelectorRunner) Run(_ context.Context, _ string, args []string, stdin, cwd string) (RunResult, error) {
	r.calls++
	r.args = append([]string(nil), args...)
	r.stdin = stdin
	r.cwd = cwd
	entries, err := os.ReadDir(cwd)
	if err != nil {
		return RunResult{}, err
	}
	r.workspaceEntries = len(entries)
	r.schemaPath = argVal(args, "--output-schema")
	r.lastPath = argVal(args, "--output-last-message")
	r.schema, err = os.ReadFile(r.schemaPath)
	if err != nil {
		return RunResult{}, err
	}
	if r.writeLast != "" {
		if err := os.WriteFile(r.lastPath, []byte(r.writeLast), 0o600); err != nil {
			return RunResult{}, err
		}
	}
	return r.result, r.err
}

func TestConstrainedSelectorEnforcesFixedSafeInvocation(t *testing.T) {
	schema := []byte(`{"type":"object","additionalProperties":false,"required":["value"],"properties":{"value":{"type":"string"}}}`)
	runner := &constrainedSelectorRunner{writeLast: `{"value":"safe"}`}
	selector, err := NewConstrainedSelectorWithRunner(ConstrainedSelectorConfig{
		Executable: "codex-test", Model: "gpt-5.6-luna", TimeoutMinutes: 2, OutputSchema: schema,
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	response, err := selector.ChatGenerate(context.Background(), provider.ChatGenerateRequest{
		Model:    "gpt-5.6-luna",
		Messages: []provider.ChatMessage{{Role: "system", Content: "strict"}, {Role: "user", Content: "choose"}},
	})
	if err != nil {
		t.Fatalf("ChatGenerate: %v", err)
	}
	if response.Content != `{"value":"safe"}` || response.Model != "gpt-5.6-luna" || response.Provider != "codex" {
		t.Fatalf("response = %#v", response)
	}
	for _, flag := range []string{"--ignore-user-config", "--ephemeral", "--skip-git-repo-check"} {
		if !containsArg(runner.args, flag) {
			t.Errorf("missing %s in %v", flag, runner.args)
		}
	}
	if argVal(runner.args, "--sandbox") != "read-only" || argVal(runner.args, "--model") != "gpt-5.6-luna" {
		t.Fatalf("unsafe sandbox/model args: %v", runner.args)
	}
	for _, feature := range constrainedSelectorDisabledFeatures {
		if !containsArgPair(runner.args, "--disable", feature) {
			t.Errorf("feature %q was not disabled: %v", feature, runner.args)
		}
	}
	if containsArg(runner.args, "--profile") || strings.Contains(strings.Join(runner.args, " "), "workspace-write") {
		t.Fatalf("selector inherited configurable authority: %v", runner.args)
	}
	if runner.workspaceEntries != 0 || argVal(runner.args, "--cd") != runner.cwd {
		t.Fatalf("selector cwd was not fresh and empty: cwd=%q entries=%d args=%v", runner.cwd, runner.workspaceEntries, runner.args)
	}
	if string(runner.schema) != string(schema) {
		t.Fatalf("schema file changed: %s", runner.schema)
	}
	if !strings.Contains(runner.stdin, "[system]") || !strings.Contains(runner.stdin, "[user]") {
		t.Fatalf("prompt roles missing: %q", runner.stdin)
	}
	for _, path := range []string{runner.cwd, runner.schemaPath, runner.lastPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("temporary path was not removed: %s err=%v", path, err)
		}
	}
}

func TestConstrainedSelectorRejectsOverridesBeforeExecution(t *testing.T) {
	schema := []byte(`{"type":"object"}`)
	runner := &constrainedSelectorRunner{}
	selector, err := NewConstrainedSelectorWithRunner(ConstrainedSelectorConfig{Model: "gpt-5.6-luna", OutputSchema: schema}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selector.ChatGenerate(context.Background(), provider.ChatGenerateRequest{
		Model: "other", Messages: []provider.ChatMessage{{Role: "user", Content: "x"}},
	}); err == nil {
		t.Fatal("model override was accepted")
	}
	if _, err := selector.ChatGenerate(context.Background(), provider.ChatGenerateRequest{
		Model: "gpt-5.6-luna", Tools: []provider.ChatTool{{}},
	}); err == nil {
		t.Fatal("tool-bearing request was accepted")
	}
	if runner.calls != 0 {
		t.Fatalf("runner called %d times for rejected configuration", runner.calls)
	}
}

func TestConstrainedSelectorRejectsPartialOutputOnFailure(t *testing.T) {
	runner := &constrainedSelectorRunner{
		writeLast: `{"partial":true}`,
		result:    RunResult{ExitCode: 2, Stderr: "unsupported feature"},
		err:       errors.New("exit status 2"),
	}
	selector, err := NewConstrainedSelectorWithRunner(ConstrainedSelectorConfig{
		Model: "gpt-5.6-luna", OutputSchema: []byte(`{"type":"object"}`),
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := selector.ChatGenerate(context.Background(), provider.ChatGenerateRequest{Model: "gpt-5.6-luna"}); err == nil || response.Content != "" {
		t.Fatalf("partial failed output accepted: response=%#v err=%v", response, err)
	}
}

func TestConstrainedSelectorRequiresModelAndSchema(t *testing.T) {
	if _, err := NewConstrainedSelectorWithRunner(ConstrainedSelectorConfig{OutputSchema: []byte(`{"type":"object"}`)}, &constrainedSelectorRunner{}); err == nil {
		t.Fatal("missing model accepted")
	}
	if _, err := NewConstrainedSelectorWithRunner(ConstrainedSelectorConfig{Model: "gpt-5.6-luna", OutputSchema: []byte(`[]`)}, &constrainedSelectorRunner{}); err == nil {
		t.Fatal("non-object schema accepted")
	}
}

func containsArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
