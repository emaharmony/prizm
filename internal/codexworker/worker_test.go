package codexworker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/crossprizm"
)

type fakeRunner struct {
	calls []fakeCall
	run   func(ctx context.Context, executable string, args []string, stdin, cwd string) (RunResult, error)
}

type fakeCall struct {
	executable string
	args       []string
	stdin      string
	cwd        string
}

func (f *fakeRunner) Run(ctx context.Context, executable string, args []string, stdin, cwd string) (RunResult, error) {
	f.calls = append(f.calls, fakeCall{executable: executable, args: append([]string(nil), args...), stdin: stdin, cwd: cwd})
	if f.run != nil {
		return f.run(ctx, executable, args, stdin, cwd)
	}
	return RunResult{}, nil
}

func TestNormalizeConfigDefaults(t *testing.T) {
	cfg := NormalizeConfig(Config{Workspace: "."})
	if cfg.Sandbox != DefaultSandbox {
		t.Fatalf("sandbox = %q", cfg.Sandbox)
	}
	if cfg.ApprovalPolicy != DefaultApprovalPolicy {
		t.Fatalf("approval = %q", cfg.ApprovalPolicy)
	}
	if cfg.TimeoutMinutes != DefaultTimeoutMinutes {
		t.Fatalf("timeout = %d", cfg.TimeoutMinutes)
	}
	if cfg.MaxConcurrency != DefaultMaxConcurrency {
		t.Fatalf("concurrency = %d", cfg.MaxConcurrency)
	}
	if cfg.Executable == "" {
		t.Fatal("expected executable default")
	}
}

func TestValidateConfigRejectsBadSandbox(t *testing.T) {
	err := ValidateConfig(Config{
		Workspace:      ".",
		Sandbox:        "bad",
		ApprovalPolicy: DefaultApprovalPolicy,
		TimeoutMinutes: 1,
		MaxConcurrency: 1,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRunTaskRequiresLogin(t *testing.T) {
	workspace := t.TempDir()
	fake := &fakeRunner{
		run: func(ctx context.Context, executable string, args []string, stdin, cwd string) (RunResult, error) {
			return RunResult{ExitCode: 1, Stderr: "Not logged in"}, os.ErrPermission
		},
	}
	worker, err := NewWithRunner(Config{
		Enabled:        true,
		Executable:     "codex",
		Workspace:      workspace,
		DataDir:        t.TempDir(),
		Sandbox:        DefaultSandbox,
		ApprovalPolicy: DefaultApprovalPolicy,
		TimeoutMinutes: 1,
		MaxConcurrency: 1,
	}, fake)
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	_, err = worker.RunTask(context.Background(), "task-1", "do work", nil)
	if err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("expected login error, got %v", err)
	}
}

func TestRunTaskBuildsExecCommandAndCapturesLastMessage(t *testing.T) {
	workspace := t.TempDir()
	dataDir := t.TempDir()
	fake := &fakeRunner{
		run: func(ctx context.Context, executable string, args []string, stdin, cwd string) (RunResult, error) {
			if len(args) >= 2 && args[0] == "login" && args[1] == "status" {
				return RunResult{ExitCode: 0, Stdout: "Logged in"}, nil
			}
			lastPath := argAfter(args, "--output-last-message")
			if lastPath == "" {
				t.Fatal("missing --output-last-message")
			}
			if err := os.WriteFile(lastPath, []byte("done\n\nChanged files: none"), 0644); err != nil {
				t.Fatalf("write last message: %v", err)
			}
			return RunResult{ExitCode: 0, Stdout: "stdout"}, nil
		},
	}
	worker, err := NewWithRunner(Config{
		Enabled:        true,
		Executable:     "codex",
		Model:          "gpt-5.4",
		Workspace:      workspace,
		DataDir:        dataDir,
		Sandbox:        "workspace-write",
		ApprovalPolicy: "on-request",
		TimeoutMinutes: 1,
		MaxConcurrency: 1,
		CaptureDiff:    false,
	}, fake)
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	result, err := worker.RunTask(context.Background(), "task-1", "do work", map[string]any{"source": "test"})
	if err != nil {
		t.Fatalf("run task: %v", err)
	}
	if result["status"] != "completed" {
		t.Fatalf("status = %v", result["status"])
	}
	if result["final_message"] != "done\n\nChanged files: none" {
		t.Fatalf("final message = %q", result["final_message"])
	}
	if len(fake.calls) != 2 {
		t.Fatalf("calls = %d", len(fake.calls))
	}
	execCall := fake.calls[1]
	if execCall.args[0] != "exec" {
		t.Fatalf("expected exec call, got %v", execCall.args)
	}
	if !containsArgPair(execCall.args, "--model", "gpt-5.4") {
		t.Fatalf("missing model args: %v", execCall.args)
	}
	if !strings.Contains(execCall.stdin, "do work") {
		t.Fatalf("stdin missing task: %q", execCall.stdin)
	}
	if !containsArgPair(execCall.args, "--sandbox", "read-only") || containsArgPair(execCall.args, "--sandbox", "workspace-write") {
		t.Fatalf("delegated worker was not forced read-only: %v", execCall.args)
	}
	if containsArgPair(execCall.args, "--ask-for-approval", "on-request") {
		t.Fatalf("delegated worker passed native approval flags: %v", execCall.args)
	}
	if !strings.Contains(execCall.stdin, "read-only delegated task") {
		t.Fatalf("prompt lacks no-mutation instruction: %q", execCall.stdin)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "task-1", "prompt.md")); err != nil {
		t.Fatalf("prompt artifact missing: %v", err)
	}
}

func TestHandleCrossPrizmTaskForcesReadOnlyExecution(t *testing.T) {
	workspace := t.TempDir()
	fake := &fakeRunner{run: func(_ context.Context, _ string, args []string, _ string, _ string) (RunResult, error) {
		if len(args) >= 2 && args[0] == "login" && args[1] == "status" {
			return RunResult{ExitCode: 0}, nil
		}
		return RunResult{ExitCode: 0, Stdout: "research report"}, nil
	}}
	worker, err := NewWithRunner(Config{Enabled: true, Executable: "codex", Workspace: workspace, DataDir: t.TempDir(), Sandbox: "danger-full-access", ApprovalPolicy: "on-request", TimeoutMinutes: 1, MaxConcurrency: 1}, fake)
	if err != nil {
		t.Fatal(err)
	}
	response, err := worker.HandleCrossPrizmTask(context.Background(), crossprizm.Message{MessageType: crossprizm.TypeTaskRequest, CorrelationID: "cross-test", Request: map[string]any{"text": "research only"}})
	if err != nil || response == nil || response.MessageType != crossprizm.TypeTaskResult {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	execCall := fake.calls[len(fake.calls)-1]
	if !containsArgPair(execCall.args, "--sandbox", "read-only") || !strings.Contains(execCall.stdin, "read-only delegated task") {
		t.Fatalf("cross-prizm execution was not read-only: args=%v prompt=%q", execCall.args, execCall.stdin)
	}
}

func argAfter(args []string, key string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
