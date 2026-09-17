package mutation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/tool"
)

func TestExecutorApplyApprovedWrites(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "output.txt", "hello world", policy)
	store.Save(a)

	executor := NewExecutor(tmpDir, store)

	var events []string
	executor.SetEmitter(func(eventType, source string, payload map[string]any) {
		events = append(events, eventType)
	})

	result, err := executor.ApplyWithRun(context.Background(), "run_01KM", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Message)
	}

	// Verify file was written
	absPath := filepath.Join(tmpDir, "output.txt")
	data, readErr := os.ReadFile(absPath)
	if readErr != nil {
		t.Fatalf("expected file to exist: %v", readErr)
	}
	if string(data) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(data))
	}

	// Verify events emitted
	hasGranted := false
	hasValidated := false
	hasApplied := false
	for _, evt := range events {
		switch evt {
		case "prizm.approval.granted":
			hasGranted = true
		case "prizm.mutation.validated":
			hasValidated = true
		case "prizm.mutation.applied":
			hasApplied = true
		}
	}
	if !hasGranted {
		t.Error("expected prizm.approval.granted event")
	}
	if !hasValidated {
		t.Error("expected prizm.mutation.validated event")
	}
	if !hasApplied {
		t.Error("expected prizm.mutation.applied event")
	}
}

func TestExecutorApplyToolCallExecutesShellCommand(t *testing.T) {
	runsDir := t.TempDir()
	store := approval.NewStore(runsDir)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "shell tool requires approval in gated mode"}
	a := approval.NewApproval("run_shell", "corr_shell", "lumi", "prizm", approval.MutationToolCall, "echo hello", "", policy)
	a.ToolName = "shell"
	a.Input = map[string]any{"command": "echo hello"}
	store.Save(a)

	executor := NewExecutor(".", store)
	executor.SetShellTool(&tool.ShellTool{
		Policy:         tool.ShellPolicy{Tier: "tier_3"},
		DefaultTimeout: 10,
		MaxOutputBytes: 10240,
		MaxStderrBytes: 5120,
	})

	result, err := executor.ApplyWithRun(context.Background(), "run_shell", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}
	if !strings.Contains(result.Message, "hello") {
		t.Errorf("expected stdout to contain 'hello', got %q", result.Message)
	}
}

func TestExecutorApplyToolCallWithoutShellToolFailsClearly(t *testing.T) {
	runsDir := t.TempDir()
	store := approval.NewStore(runsDir)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_shell", "corr_shell", "lumi", "prizm", approval.MutationToolCall, "echo hello", "", policy)
	a.ToolName = "shell"
	a.Input = map[string]any{"command": "echo hello"}
	store.Save(a)

	executor := NewExecutor(".", store) // no SetShellTool call

	result, err := executor.ApplyWithRun(context.Background(), "run_shell", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure without a configured shell tool")
	}
	if !strings.Contains(result.Message, "not configured") {
		t.Errorf("expected a clear 'not configured' message, got %q", result.Message)
	}
}

func TestExecutorApplyToolCallHardBlocklistSurvivesApproval(t *testing.T) {
	runsDir := t.TempDir()
	store := approval.NewStore(runsDir)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	// Simulates a stale/tampered approval record targeting a hard-blocklisted
	// command — validateSafety must refuse this even with a tier_3 shell tool
	// configured, since the hard blocklist is always enforced first.
	a := approval.NewApproval("run_shell", "corr_shell", "lumi", "prizm", approval.MutationToolCall, "rm -rf /*", "", policy)
	a.ToolName = "shell"
	a.Input = map[string]any{"command": "rm -rf /*"}
	store.Save(a)

	executor := NewExecutor(".", store)
	executor.SetShellTool(&tool.ShellTool{Policy: tool.ShellPolicy{Tier: "tier_3"}})

	result, err := executor.ApplyWithRun(context.Background(), "run_shell", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected hard-blocklisted command to fail even when approved")
	}
}

func TestExecutorApplyToolCallExecutesGitAddViaRegistry(t *testing.T) {
	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@prizm.local")
	runGit("config", "user.name", "Prizm Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	runsDir := t.TempDir()
	store := approval.NewStore(runsDir)
	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_git", "corr_git", "lumi", "prizm", approval.MutationToolCall, "file.txt", "", policy)
	a.ToolName = "git_add"
	a.Input = map[string]any{"path": "file.txt", "repo_path": root}
	store.Save(a)

	registry := tool.NewRegistry()
	if err := registry.Register(&tool.GitAddTool{ToolPaths: tool.ToolPaths{WorkspaceRoot: root}}); err != nil {
		t.Fatalf("register git_add tool: %v", err)
	}
	executor := NewExecutor(root, store)
	executor.SetRegistry(registry)

	result, err := executor.ApplyWithRun(context.Background(), "run_git", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	// Confirm the file was actually staged, exactly once — this is the real
	// side effect of the approval, not just an after-the-fact record.
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = root
	out, err := statusCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "A  file.txt") {
		t.Fatalf("expected file.txt to be staged after approval, got status:\n%s", out)
	}
}

func TestExecutorApplyApprovedAbsolutePathWithinAllowedRoot(t *testing.T) {
	workspace := t.TempDir()
	allowedRoot := t.TempDir()
	runsDir := t.TempDir()
	store := approval.NewStore(runsDir)

	targetPath := filepath.Join(allowedRoot, "clear-me.txt")
	if err := os.WriteFile(targetPath, []byte("delete me"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_clear", "corr_clear", "test-cli", "prizm", "write_file", targetPath, "", policy)
	if err := store.Save(a); err != nil {
		t.Fatalf("save approval: %v", err)
	}

	executor := NewExecutor(workspace, store, allowedRoot)
	result, err := executor.ApplyWithRun(context.Background(), "run_clear", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	data, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("expected file to exist: %v", readErr)
	}
	if string(data) != "" {
		t.Fatalf("expected file to be cleared, got %q", string(data))
	}
}

func TestExecutorDeniedApprovalDoesNotWrite(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "output.txt", "hello world", policy)
	a.Deny("ema", "not needed")
	store.Save(a)

	executor := NewExecutor(tmpDir, store)

	result, err := executor.ApplyWithRun(context.Background(), "run_01KM", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for denied approval")
	}

	// Verify file was NOT written
	absPath := filepath.Join(tmpDir, "output.txt")
	if _, readErr := os.Stat(absPath); !os.IsNotExist(readErr) {
		t.Error("expected file to NOT exist")
	}
}

func TestExecutorUnsafePathDoesNotWrite(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}

	// Path traversal
	a := approval.NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "../outside.txt", "malicious", policy)
	store.Save(a)

	executor := NewExecutor(tmpDir, store)

	var events []string
	executor.SetEmitter(func(eventType, source string, payload map[string]any) {
		events = append(events, eventType)
	})

	result, err := executor.ApplyWithRun(context.Background(), "run_01KM", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for path traversal")
	}

	hasFailed := false
	for _, evt := range events {
		if evt == "prizm.mutation.failed" {
			hasFailed = true
		}
	}
	if !hasFailed {
		t.Error("expected prizm.mutation.failed event")
	}
}

func TestExecutorAbsolutePathDoesNotWrite(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "/etc/passwd", "malicious", policy)
	store.Save(a)

	executor := NewExecutor(tmpDir, store)

	result, err := executor.ApplyWithRun(context.Background(), "run_01KM", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for absolute path")
	}
}

func TestExecutorFailedMutationEmitsFailed(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	// Trying to write to a directory that doesn't exist, but path goes outside
	a := approval.NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "", "", policy)
	store.Save(a)

	executor := NewExecutor(tmpDir, store)

	var events []string
	executor.SetEmitter(func(eventType, source string, payload map[string]any) {
		events = append(events, eventType)
	})

	result, err := executor.ApplyWithRun(context.Background(), "run_01KM", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for empty path")
	}

	hasFailed := false
	for _, evt := range events {
		if evt == "prizm.mutation.failed" {
			hasFailed = true
		}
	}
	if !hasFailed {
		t.Error("expected prizm.mutation.failed event for empty path")
	}
}

func TestExecutorDenyApproval(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "content", policy)
	store.Save(a)

	executor := NewExecutor(tmpDir, store)

	var events []string
	executor.SetEmitter(func(eventType, source string, payload map[string]any) {
		events = append(events, eventType)
	})

	err := executor.DenyApproval("run_01KM", a.ApprovalID, "ema", "not needed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify approval was saved with denied status
	loaded, err := store.Load("run_01KM", a.ApprovalID)
	if err != nil {
		t.Fatalf("failed to reload: %v", err)
	}
	if loaded.Status != approval.StatusDenied {
		t.Errorf("expected status denied, got %q", loaded.Status)
	}

	// Verify event emitted
	hasDenied := false
	for _, evt := range events {
		if evt == "prizm.approval.denied" {
			hasDenied = true
		}
	}
	if !hasDenied {
		t.Error("expected prizm.approval.denied event")
	}
}

func TestExecutorContentSizeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	// Create content larger than 1MB
	largeContent := make([]byte, MaxContentSize+1)
	for i := range largeContent {
		largeContent[i] = 'x'
	}

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "big.txt", string(largeContent), policy)
	store.Save(a)

	executor := NewExecutor(tmpDir, store)

	result, err := executor.ApplyWithRun(context.Background(), "run_01KM", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for oversized content")
	}
}

func TestExecutorApprovalNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	executor := NewExecutor(tmpDir, store)

	var events []string
	executor.SetEmitter(func(eventType, source string, payload map[string]any) {
		events = append(events, eventType)
	})

	result, err := executor.ApplyWithRun(context.Background(), "run_01KM", "appr_nonexistent", "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for nonexistent approval")
	}

	hasFailed := false
	for _, evt := range events {
		if evt == "prizm.mutation.failed" {
			hasFailed = true
		}
	}
	if !hasFailed {
		t.Error("expected prizm.mutation.failed event")
	}
}

func TestExecutorWriteToSubdirectory(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "subdir/output.txt", "hello world", policy)
	store.Save(a)

	executor := NewExecutor(tmpDir, store)

	result, err := executor.ApplyWithRun(context.Background(), "run_01KM", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Message)
	}

	absPath := filepath.Join(tmpDir, "subdir", "output.txt")
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(data))
	}
}

func TestExecutorCreateDirectoryApprovedCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_mkdir", "corr_mkdir", "test-cli", "prizm", approval.MutationCreateDirectory, "empty/child", "", policy)
	if err := store.Save(a); err != nil {
		t.Fatalf("save approval: %v", err)
	}

	executor := NewExecutor(tmpDir, store)
	result, err := executor.ApplyWithRun(context.Background(), "run_mkdir", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	absPath := filepath.Join(tmpDir, "empty", "child")
	info, statErr := os.Stat(absPath)
	if statErr != nil {
		t.Fatalf("expected directory to exist: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("target should be a directory")
	}
}

func TestExecutorCreateDirectoryApprovedAbsolutePathWithinAllowedRoot(t *testing.T) {
	workspace := t.TempDir()
	allowedRoot := t.TempDir()
	runsDir := t.TempDir()
	store := approval.NewStore(runsDir)

	targetPath := filepath.Join(allowedRoot, "empty")
	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_mkdir_abs", "corr_mkdir_abs", "test-cli", "prizm", approval.MutationCreateDirectory, targetPath, "", policy)
	if err := store.Save(a); err != nil {
		t.Fatalf("save approval: %v", err)
	}

	executor := NewExecutor(workspace, store, allowedRoot)
	result, err := executor.ApplyWithRun(context.Background(), "run_mkdir_abs", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}
	info, statErr := os.Stat(targetPath)
	if statErr != nil {
		t.Fatalf("expected directory to exist: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("target should be a directory")
	}
}

func TestExecutorCreateDirectoryRejectsExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_mkdir_file", "corr_mkdir_file", "test-cli", "prizm", approval.MutationCreateDirectory, "file.txt", "", policy)
	if err := store.Save(a); err != nil {
		t.Fatalf("save approval: %v", err)
	}

	executor := NewExecutor(tmpDir, store)
	result, err := executor.ApplyWithRun(context.Background(), "run_mkdir_file", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure when target is an existing file")
	}
}

func TestExecutorCannotOverwriteDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	// Create a directory
	dirPath := filepath.Join(tmpDir, "mydir")
	os.MkdirAll(dirPath, 0755)

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "mydir", "content", policy)
	store.Save(a)

	executor := NewExecutor(tmpDir, store)

	result, err := executor.ApplyWithRun(context.Background(), "run_01KM", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when overwriting directory")
	}
}

func TestEmptyApply(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)
	executor := NewExecutor(tmpDir, store)

	// Apply without runID should fail
	_, err := executor.Apply(context.Background(), "appr_test", "ema")
	if err == nil {
		t.Error("expected error from Apply without runID")
	}
}

func TestExecutorSymlinkEscapeBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	// Create a directory outside the workspace
	outsideDir := t.TempDir()
	secretPath := filepath.Join(outsideDir, "secret.txt")
	os.WriteFile(secretPath, []byte("secret data"), 0644)

	// Create symlink inside workspace pointing outside
	linkPath := filepath.Join(tmpDir, "escape_link")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("symlink creation is unavailable on this host: %v", err)
	}

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_symlink", "corr_symlink", "test-cli", "prizm", "write_file", "escape_link/malicious.txt", "malicious content", policy)
	store.Save(a)

	executor := NewExecutor(tmpDir, store)

	result, err := executor.ApplyWithRun(context.Background(), "run_symlink", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("mutation through symlink escape should be blocked")
	}

	// Verify file was NOT written
	maliciousPath := filepath.Join(outsideDir, "malicious.txt")
	if _, statErr := os.Stat(maliciousPath); !os.IsNotExist(statErr) {
		t.Error("file should NOT have been written outside workspace")
	}
}

func TestExecutorDirectSymlinkBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)

	// Create a file outside the workspace
	outsideFile := filepath.Join(t.TempDir(), "outside_target.txt")
	os.WriteFile(outsideFile, []byte("original"), 0644)

	// Create symlink inside workspace pointing directly to outside file
	linkPath := filepath.Join(tmpDir, "target_link")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Skipf("symlink creation is unavailable on this host: %v", err)
	}

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_sym2", "corr_sym2", "test-cli", "prizm", "write_file", "target_link", "overwritten", policy)
	store.Save(a)

	executor := NewExecutor(tmpDir, store)

	result, err := executor.ApplyWithRun(context.Background(), "run_sym2", a.ApprovalID, "ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("mutation targeting symlink should be blocked")
	}
}

func TestExecutorAlreadyApprovedAppliesWithoutReapproval(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)
	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_approved", "corr", "test", "prizm", approval.MutationWriteFile, "approved.txt", "content", policy)
	if err := a.Approve("first-reviewer"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}

	result, err := NewExecutor(tmpDir, store).ApplyWithRun(context.Background(), "run_approved", a.ApprovalID, "second-reviewer")
	if err != nil || !result.Success {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	loaded, err := store.Load("run_approved", a.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ApprovedBy != "first-reviewer" {
		t.Fatalf("approved_by changed to %q", loaded.ApprovedBy)
	}
}

func TestExecutorDenyApprovalErrors(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)
	executor := NewExecutor(tmpDir, store)
	if err := executor.DenyApproval("missing-run", "missing", "reviewer", "reason"); err == nil {
		t.Fatal("missing approval was denied without error")
	}

	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_deny_error", "corr", "test", "prizm", approval.MutationWriteFile, "file.txt", "content", policy)
	if err := a.Approve("reviewer"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}
	if err := executor.DenyApproval("run_deny_error", a.ApprovalID, "other", "too late"); err == nil {
		t.Fatal("approved mutation was denied without error")
	}
}

func TestExecutorRejectsEmptyApprover(t *testing.T) {
	tmpDir := t.TempDir()
	store := approval.NewStore(tmpDir)
	policy := approval.PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := approval.NewApproval("run_empty_approver", "corr", "test", "prizm", approval.MutationWriteFile, "file.txt", "content", policy)
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}
	result, err := NewExecutor(tmpDir, store).ApplyWithRun(context.Background(), "run_empty_approver", a.ApprovalID, "")
	if err != nil || result.Success || result.Message != "failed to approve" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestValidateSafetyRejectsUnsupportedMutation(t *testing.T) {
	tmpDir := t.TempDir()
	executor := NewExecutor(tmpDir, approval.NewStore(tmpDir))
	a := &approval.Approval{MutationType: "unknown", TargetPath: "file.txt"}
	if err := executor.validateSafety(a); err == nil {
		t.Fatal("unsupported mutation passed safety validation")
	}
}
