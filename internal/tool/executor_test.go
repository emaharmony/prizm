package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/approval"
)

func TestExecutorEchoApproved(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg, ".", 1024*1024)
	cfg := DefaultPolicyConfig()
	exec := NewExecutor(reg, &cfg)

	var events []string
	exec.SetEmitter(func(eventType, source string, payload map[string]any) {
		events = append(events, eventType)
	})

	result, err := exec.ExecuteWithPolicy(context.Background(), "echo", "lumi", "prizm", "corr_test", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("echo should succeed, got error: %s", result.Error)
	}
	if result.Output["text"] != "hello" {
		t.Errorf("expected output text 'hello', got %v", result.Output["text"])
	}

	// Should have: requested, approved, started, completed
	expected := []string{"prizm.tool.requested", "prizm.tool.approved", "prizm.tool.started", "prizm.tool.completed"}
	if len(events) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(events), events)
	}
	for i, exp := range expected {
		if events[i] != exp {
			t.Errorf("event %d: expected %s, got %s", i, exp, events[i])
		}
	}
}

func TestExecutorDeniedTool(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg, ".", 1024*1024)
	cfg := DefaultPolicyConfig()
	exec := NewExecutor(reg, &cfg)

	var events []string
	exec.SetEmitter(func(eventType, source string, payload map[string]any) {
		events = append(events, eventType)
	})

	// V60: Shell tool now requires approval in gated mode, not denied.
	// Use a truly unknown tool name to test the denied path.
	result, err := exec.ExecuteWithPolicy(context.Background(), "nonexistent_tool", "lumi", "prizm", "corr_test", map[string]any{})
	if err != nil {
		t.Fatalf("denied tool should not return an error, just a denial result")
	}
	if result.Success {
		t.Error("denied tool should not succeed")
	}
	if result.Error == "" {
		t.Error("denied tool result should have an error message")
	}

	// Should have: requested, denied
	expected := []string{"prizm.tool.requested", "prizm.tool.denied"}
	if len(events) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(events), events)
	}
	for i, exp := range expected {
		if events[i] != exp {
			t.Errorf("event %d: expected %s, got %s", i, exp, events[i])
		}
	}
}

func TestExecutorPathTraversalDenied(t *testing.T) {
	tmpDir := t.TempDir()
	reg := NewRegistry()
	RegisterBuiltins(reg, tmpDir, 1024*1024)
	cfg := PolicyConfig{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	exec := NewExecutor(reg, &cfg)

	var events []string
	exec.SetEmitter(func(eventType, source string, payload map[string]any) {
		events = append(events, eventType)
	})

	result, err := exec.ExecuteWithPolicy(context.Background(), "read_file", "lumi", "prizm", "corr_test", map[string]any{"path": "../../../etc/passwd"})
	if err != nil {
		t.Fatalf("denied tool should not return an error")
	}
	if result.Success {
		t.Error("path traversal should be denied")
	}

	// Should have: requested, denied
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %v", len(events), events)
	}
	if events[0] != "prizm.tool.requested" {
		t.Errorf("first event should be requested, got %s", events[0])
	}
	if events[1] != "prizm.tool.denied" {
		t.Errorf("second event should be denied, got %s", events[1])
	}
}

func TestExecutorListDirWithinRoot(t *testing.T) {
	tmpDir := t.TempDir()
	// Create some test files
	os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("hello"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)

	reg := NewRegistry()
	RegisterBuiltins(reg, tmpDir, 1024*1024)
	cfg := PolicyConfig{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	exec := NewExecutor(reg, &cfg)

	var events []string
	exec.SetEmitter(func(eventType, source string, payload map[string]any) {
		events = append(events, eventType)
	})

	result, err := exec.ExecuteWithPolicy(context.Background(), "list_dir", "lumi", "prizm", "corr_test", map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("list_dir should succeed, got error: %s", result.Error)
	}

	// Should have: requested, approved, started, completed
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %v", len(events), events)
	}
	if events[0] != "prizm.tool.requested" {
		t.Errorf("first event should be requested, got %s", events[0])
	}
	if events[1] != "prizm.tool.approved" {
		t.Errorf("second event should be approved, got %s", events[1])
	}
}

func TestExecutorWriteFileDryRunNoDiskWrite(t *testing.T) {
	tmpDir := t.TempDir()
	reg := NewRegistry()
	RegisterBuiltins(reg, tmpDir, 1024*1024)
	cfg := PolicyConfig{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	exec := NewExecutor(reg, &cfg)

	result, err := exec.ExecuteWithPolicy(context.Background(), "write_file_dry_run", "lumi", "prizm", "corr_test", map[string]any{
		"path":    "should_not_exist.txt",
		"content": "This content should never be written",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("write_file_dry_run should succeed, got error: %s", result.Error)
	}

	// Verify the file was NOT written to disk
	if _, err := os.Stat(filepath.Join(tmpDir, "should_not_exist.txt")); !os.IsNotExist(err) {
		t.Error("write_file_dry_run should NOT create files on disk")
	}

	// Verify the result indicates no write
	if result.Output["written"] != false {
		t.Error("write_file_dry_run result should have written=false")
	}
}

func TestExecutorWriteFileProposalPersistsEmptyContentApproval(t *testing.T) {
	workspace := t.TempDir()
	allowedRoot := t.TempDir()
	runsDir := t.TempDir()
	targetPath := filepath.Join(allowedRoot, "clear-me.txt")

	reg := NewRegistry()
	RegisterBuiltinsV4(reg, workspace, 1024*1024, "", allowedRoot)
	cfg := PolicyConfig{
		WorkspaceRoot: workspace,
		AllowedPaths:  []string{allowedRoot},
		MaxFileSize:   1024 * 1024,
	}
	store := approval.NewStore(runsDir)
	exec := NewExecutor(reg, &cfg)
	exec.SetApprovalStore(store)

	result, err := exec.ExecuteWithPolicy(context.Background(), "write_file_proposal", "astraea", "prizm", "corr_test", map[string]any{
		"_run_id": "run_test",
		"path":    targetPath,
		"content": "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("write_file_proposal should succeed, got error: %s", result.Error)
	}
	approvalID, _ := result.Output["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("expected approval_id in tool result")
	}

	a, err := store.Load("run_test", approvalID)
	if err != nil {
		t.Fatalf("expected persisted approval: %v", err)
	}
	if a.Content != "" {
		t.Fatalf("expected empty proposed content, got %q", a.Content)
	}
	if a.TargetPath != targetPath {
		t.Fatalf("target path = %q, want %q", a.TargetPath, targetPath)
	}
	if a.Status != approval.StatusPending {
		t.Fatalf("status = %q, want pending", a.Status)
	}
}

func TestExecutorCreateDirectoryProposalPersistsApproval(t *testing.T) {
	workspace := t.TempDir()
	allowedRoot := t.TempDir()
	runsDir := t.TempDir()
	targetPath := filepath.Join(allowedRoot, "new-dir")

	reg := NewRegistry()
	RegisterBuiltinsV4(reg, workspace, 1024*1024, "", allowedRoot)
	cfg := PolicyConfig{
		WorkspaceRoot: workspace,
		AllowedPaths:  []string{allowedRoot},
		MaxFileSize:   1024 * 1024,
	}
	store := approval.NewStore(runsDir)
	exec := NewExecutor(reg, &cfg)
	exec.SetApprovalStore(store)

	result, err := exec.ExecuteWithPolicy(context.Background(), "create_directory_proposal", "astraea", "prizm", "corr_test", map[string]any{
		"_run_id": "run_test",
		"path":    targetPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("create_directory_proposal should succeed, got error: %s", result.Error)
	}
	approvalID, _ := result.Output["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("expected approval_id in tool result")
	}

	a, err := store.Load("run_test", approvalID)
	if err != nil {
		t.Fatalf("expected persisted approval: %v", err)
	}
	if a.MutationType != approval.MutationCreateDirectory {
		t.Fatalf("mutation type = %q, want %q", a.MutationType, approval.MutationCreateDirectory)
	}
	if a.TargetPath != targetPath {
		t.Fatalf("target path = %q, want %q", a.TargetPath, targetPath)
	}
	if a.Preview == "" {
		t.Fatalf("expected directory proposal preview")
	}
}

func TestExecutorShellRequiresApprovalDoesNotExecute(t *testing.T) {
	runsDir := t.TempDir()
	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "marker.txt")

	reg := NewRegistry()
	if err := reg.Register(&ShellTool{
		Policy:         ShellPolicy{Tier: "tier_3", Allowlist: []string{"*"}, Blocklist: []string{}},
		DefaultTimeout: 10,
		MaxOutputBytes: 10240,
		MaxStderrBytes: 5120,
	}); err != nil {
		t.Fatalf("register shell tool: %v", err)
	}

	// Gated mode: AutoApproveMutations is false, so the shell tool requires
	// approval. This reproduces two historical bugs in one test: (1)
	// persistApproval used to error out on tools with no "path" input, and
	// (2) the shell command used to actually run immediately, before any
	// human approval — asserted here by checking the marker file was NOT
	// created by this call.
	cfg := DefaultPolicyConfig()
	store := approval.NewStore(runsDir)
	exec := NewExecutor(reg, &cfg)
	exec.SetApprovalStore(store)

	command := "touch " + marker
	result, err := exec.ExecuteWithPolicy(context.Background(), "shell", "lumi", "prizm", "corr_test", map[string]any{
		"_run_id": "run_test",
		"command": command,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("shell call should succeed, got error: %s", result.Error)
	}

	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("shell command must not execute before approval — marker file exists")
	}

	approvalID, _ := result.Output["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("expected approval_id in tool result")
	}

	a, err := store.Load("run_test", approvalID)
	if err != nil {
		t.Fatalf("expected persisted approval: %v", err)
	}
	if a.MutationType != approval.MutationToolCall {
		t.Fatalf("mutation type = %q, want %q", a.MutationType, approval.MutationToolCall)
	}
	if a.TargetPath != command {
		t.Fatalf("target path = %q, want %q", a.TargetPath, command)
	}
	if a.ToolName != "shell" {
		t.Fatalf("tool name = %q, want %q", a.ToolName, "shell")
	}
	if a.Input["command"] != command {
		t.Fatalf("input[command] = %v, want %q", a.Input["command"], command)
	}
}

func TestExecutorGitCommitRequiresApprovalDoesNotExecute(t *testing.T) {
	root := initGitRepo(t)
	writeAndStage(t, root, "file.txt", "content\n")
	before := currentSHA(t, root)

	runsDir := t.TempDir()
	reg := NewRegistry()
	if err := reg.Register(&GitCommitTool{ToolPaths: ToolPaths{WorkspaceRoot: root}}); err != nil {
		t.Fatalf("register git_commit tool: %v", err)
	}

	// Gated mode (AutoApproveMutations=false): historically git_commit's
	// Execute() ran the real commit immediately here, before any human
	// approval — asserted by checking HEAD hasn't moved.
	cfg := DefaultPolicyConfig()
	store := approval.NewStore(runsDir)
	exec := NewExecutor(reg, &cfg)
	exec.SetApprovalStore(store)

	result, err := exec.ExecuteWithPolicy(context.Background(), "git_commit", "lumi", "prizm", "corr_test", map[string]any{
		"_run_id": "run_test",
		"message": "should not commit yet",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("git_commit call should succeed, got error: %s", result.Error)
	}

	after := currentSHA(t, root)
	if before != after {
		t.Fatalf("git_commit must not execute before approval — HEAD moved from %s to %s", before, after)
	}

	approvalID, _ := result.Output["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("expected approval_id in tool result — this used to fail with 'target_path is required'")
	}

	a, err := store.Load("run_test", approvalID)
	if err != nil {
		t.Fatalf("expected persisted approval: %v", err)
	}
	if a.MutationType != approval.MutationToolCall {
		t.Fatalf("mutation type = %q, want %q", a.MutationType, approval.MutationToolCall)
	}
	if a.ToolName != "git_commit" {
		t.Fatalf("tool name = %q, want %q", a.ToolName, "git_commit")
	}
	if a.Input["message"] != "should not commit yet" {
		t.Fatalf("input[message] = %v, want %q", a.Input["message"], "should not commit yet")
	}
}

func TestExecutorToolFailedEvent(t *testing.T) {
	reg := NewRegistry()
	// Don't register builtins — resolve will fail for unknown tools
	cfg := DefaultPolicyConfig()
	exec := NewExecutor(reg, &cfg)

	var events []string
	exec.SetEmitter(func(eventType, source string, payload map[string]any) {
		events = append(events, eventType)
	})

	// Try to call "echo" which isn't registered
	result, err := exec.ExecuteWithPolicy(context.Background(), "echo", "lumi", "prizm", "corr_test", map[string]any{"text": "hello"})
	// The policy check for echo will pass, but the registry won't find the tool
	// This should result in a tool.failed event
	_ = result
	_ = err

	// Should have: requested, approved, started, failed
	found := false
	for _, e := range events {
		if e == "prizm.tool.failed" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected tool.failed event when tool not found in registry, events: %v", events)
	}
}
