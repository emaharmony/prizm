package tool

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestShellTool_Success(t *testing.T) {
	policy := ShellPolicy{
		Tier:      "tier_3",
		Allowlist: []string{"*"},
		Blocklist: []string{},
	}

	shell := &ShellTool{
		Policy:         policy,
		DefaultTimeout: 10,
		MaxOutputBytes: 10240,
		MaxStderrBytes: 5120,
	}

	result, err := shell.Execute(context.Background(), map[string]any{
		"command": "echo hello world",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	stdout, ok := result.Output["stdout"].(string)
	if !ok {
		t.Fatal("expected stdout in output")
	}
	if stdout != "hello world\n" {
		t.Errorf("expected 'hello world\\n', got %q", stdout)
	}

	exitCode, ok := result.Output["exit_code"].(int)
	if !ok {
		t.Fatal("expected exit_code in output")
	}
	if exitCode != 0 {
		t.Errorf("expected exit_code 0, got %d", exitCode)
	}

	duration, ok := result.Output["duration_ms"].(int64)
	if !ok {
		t.Fatal("expected duration_ms in output")
	}
	if duration < 0 {
		t.Errorf("expected non-negative duration, got %d", duration)
	}
}

func TestShellTool_Failure(t *testing.T) {
	policy := ShellPolicy{
		Tier:      "tier_3",
		Allowlist: []string{"*"},
		Blocklist: []string{},
	}

	shell := &ShellTool{
		Policy:         policy,
		DefaultTimeout: 10,
		MaxOutputBytes: 10240,
		MaxStderrBytes: 5120,
	}

	result, err := shell.Execute(context.Background(), map[string]any{
		"command": "exit 42",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for exit 42")
	}

	exitCode, ok := result.Output["exit_code"].(int)
	if !ok {
		t.Fatal("expected exit_code in output")
	}
	if exitCode != 42 {
		t.Errorf("expected exit_code 42, got %d", exitCode)
	}
}

func TestShellTool_Timeout(t *testing.T) {
	policy := ShellPolicy{
		Tier:      "tier_3",
		Allowlist: []string{"*"},
		Blocklist: []string{},
	}

	shell := &ShellTool{
		Policy:         policy,
		DefaultTimeout: 1, // 1 second timeout
		MaxOutputBytes: 10240,
		MaxStderrBytes: 5120,
	}

	// sleep 10 spawns a real child process of sh; on Windows, killing the
	// timed-out sh.exe handle doesn't kill that orphaned grandchild, so the
	// timeout never resolves quickly. A shell-builtin busy-loop never execs
	// a subprocess, so killing sh.exe terminates it immediately on both
	// platforms and still exercises the real timeout path.
	command := "sleep 10"
	if runtime.GOOS == "windows" {
		command = "while true; do :; done"
	}

	start := time.Now()
	result, err := shell.Execute(context.Background(), map[string]any{
		"command": command,
		"timeout": float64(1),
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for timed-out command")
	}

	// Should have timed out in roughly 1 second
	if elapsed > 15*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}

	exitCode, ok := result.Output["exit_code"].(int)
	if !ok {
		t.Fatal("expected exit_code in output")
	}
	if exitCode != -1 {
		t.Errorf("expected exit_code -1 for timeout, got %d", exitCode)
	}
}

func TestShellTool_BlockedByPolicy(t *testing.T) {
	policy := ShellPolicy{
		Tier:      "tier_1",
		Allowlist: []string{"go build*", "go test*"},
		Blocklist: []string{},
	}

	shell := &ShellTool{
		Policy:         policy,
		DefaultTimeout: 10,
		MaxOutputBytes: 10240,
		MaxStderrBytes: 5120,
	}

	result, err := shell.Execute(context.Background(), map[string]any{
		"command": "rm -rf /tmp/test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected blocked command to fail")
	}

	blocked, ok := result.Output["blocked"].(bool)
	if !ok || !blocked {
		t.Errorf("expected blocked=true in output, got %v", result.Output)
	}
}

func TestShellTool_BlockedByHardBlocklist(t *testing.T) {
	policy := ShellPolicy{
		Tier:      "tier_3", // full access
		Allowlist: []string{"*"},
		Blocklist: []string{},
	}

	shell := &ShellTool{
		Policy:         policy,
		DefaultTimeout: 10,
		MaxOutputBytes: 10240,
		MaxStderrBytes: 5120,
	}

	// Even with tier_3, hard blocklist should block this
	result, err := shell.Execute(context.Background(), map[string]any{
		"command": "rm -rf /",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("hard blocklist should block rm -rf / even in tier_3")
	}

	blocked, ok := result.Output["blocked"].(bool)
	if !ok || !blocked {
		t.Errorf("expected blocked=true in output, got %v", result.Output)
	}
}

func TestShellTool_Stderr(t *testing.T) {
	policy := ShellPolicy{
		Tier:      "tier_3",
		Allowlist: []string{"*"},
		Blocklist: []string{},
	}

	shell := &ShellTool{
		Policy:         policy,
		DefaultTimeout: 10,
		MaxOutputBytes: 10240,
		MaxStderrBytes: 5120,
	}

	result, err := shell.Execute(context.Background(), map[string]any{
		"command": "echo stderr test >&2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	stderr, ok := result.Output["stderr"].(string)
	if !ok {
		t.Fatal("expected stderr in output")
	}
	if stderr != "stderr test\n" {
		t.Errorf("expected 'stderr test\\n', got %q", stderr)
	}
}

func TestShellTool_Cwd(t *testing.T) {
	policy := ShellPolicy{
		Tier:      "tier_3",
		Allowlist: []string{"*"},
		Blocklist: []string{},
	}

	shell := &ShellTool{
		Policy:         policy,
		DefaultTimeout: 10,
		MaxOutputBytes: 10240,
		MaxStderrBytes: 5120,
	}

	// Use a real, portable directory instead of the POSIX-only "/tmp" (which
	// doesn't exist on Windows and previously caused this test to fail with
	// a misreported "timed out" error instead of a clear cwd failure — see
	// the shell.go exit-code fix). Prove cwd was honored by planting a
	// marker file and listing the directory, rather than comparing pwd's
	// output string: Git Bash's sh on Windows maps a Windows temp path to a
	// POSIX-style path (e.g. "/c/Users/..."), so a literal string comparison
	// against the Windows-style dir would be fragile even when correct.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cwd-marker.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("failed to write marker file: %v", err)
	}

	result, err := shell.Execute(context.Background(), map[string]any{
		"command": "ls",
		"cwd":     dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	stdout, ok := result.Output["stdout"].(string)
	if !ok {
		t.Fatal("expected stdout in output")
	}
	if !strings.Contains(stdout, "cwd-marker.txt") {
		t.Errorf("expected ls output to contain cwd-marker.txt (proving cwd was honored), got %q", stdout)
	}
}

func TestShellTool_MissingCommand(t *testing.T) {
	policy := ShellPolicy{
		Tier:      "tier_3",
		Allowlist: []string{"*"},
		Blocklist: []string{},
	}

	shell := &ShellTool{
		Policy:         policy,
		DefaultTimeout: 10,
		MaxOutputBytes: 10240,
		MaxStderrBytes: 5120,
	}

	result, err := shell.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for missing command")
	}
}

func TestShellTool_OutputTruncation(t *testing.T) {
	policy := ShellPolicy{
		Tier:      "tier_3",
		Allowlist: []string{"*"},
		Blocklist: []string{},
	}

	shell := &ShellTool{
		Policy:         policy,
		DefaultTimeout: 10,
		MaxOutputBytes: 50, // very small limit
		MaxStderrBytes: 30,
	}

	// Generate a long output
	result, err := shell.Execute(context.Background(), map[string]any{
		"command": "python3 -c \"print('x' * 200)\" 2>/dev/null || printf 'x%.0s' {1..200}",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stdout, ok := result.Output["stdout"].(string)
	if !ok {
		t.Fatal("expected stdout in output")
	}
	if len(stdout) > 50+100 { // allow some extra for truncation message
		t.Errorf("stdout should be truncated to ~50 bytes, got %d bytes: %q", len(stdout), stdout)
	}
}
