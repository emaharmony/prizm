package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/gitx"
)

// initGitRepo creates a git repo with one commit on "main" in a temp dir and
// returns its path. Mirrors internal/gitx/gitx_test.go's initRepo helper.
func initGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustRun := func(args ...string) {
		t.Helper()
		if out, exitCode, err := runGitCommand(root, args...); err != nil || exitCode != 0 {
			t.Fatalf("git %v failed (exit %d): %v\n%s", args, exitCode, err, out)
		}
	}
	mustRun("init", "-b", "main")
	mustRun("config", "user.email", "test@prizm.local")
	mustRun("config", "user.name", "Prizm Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun("add", ".")
	mustRun("commit", "-m", "initial")
	return root
}

// writeAndStage creates a file in root and stages it with git add.
func writeAndStage(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if out, exitCode, err := runGitCommand(root, "add", name); err != nil || exitCode != 0 {
		t.Fatalf("git add failed (exit %d): %v\n%s", exitCode, err, out)
	}
}

func currentSHA(t *testing.T, root string) string {
	t.Helper()
	sha, err := gitx.CurrentSHA(context.Background(), root)
	if err != nil {
		t.Fatalf("gitx.CurrentSHA: %v", err)
	}
	return sha
}

func TestGitCommitTool_BlockedOnProtectedBranch(t *testing.T) {
	root := initGitRepo(t)
	writeAndStage(t, root, "file.txt", "content\n")
	before := currentSHA(t, root)

	tool := &GitCommitTool{
		ToolPaths:       ToolPaths{WorkspaceRoot: root},
		ProtectedBranch: "main",
	}
	result, err := tool.Execute(context.Background(), map[string]any{"message": "should be blocked"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected commit to be blocked on protected branch")
	}
	if reason, _ := result.Output["blocked_reason"].(string); reason != "protected_branch" {
		t.Errorf("expected blocked_reason=protected_branch, got %v", result.Output["blocked_reason"])
	}

	after := currentSHA(t, root)
	if before != after {
		t.Errorf("expected no new commit to be created; HEAD moved from %s to %s", before, after)
	}
}

func TestGitPushTool_BlockedOnProtectedBranch_CurrentBranch(t *testing.T) {
	root := initGitRepo(t)

	tool := &GitPushTool{
		ToolPaths:       ToolPaths{WorkspaceRoot: root},
		ProtectedBranch: "main",
	}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected push to be blocked on protected branch")
	}
	blocked, _ := result.Output["blocked"].(bool)
	if !blocked {
		t.Errorf("expected blocked=true (proving the block fired before the real push, which has no remote configured), got output: %v", result.Output)
	}
}

func TestGitPushTool_BlockedOnProtectedBranch_ExplicitBranchParam(t *testing.T) {
	root := initGitRepo(t)
	if out, exitCode, err := runGitCommand(root, "checkout", "-b", "feature/x"); err != nil || exitCode != 0 {
		t.Fatalf("git checkout -b feature/x failed (exit %d): %v\n%s", exitCode, err, out)
	}

	tool := &GitPushTool{
		ToolPaths:       ToolPaths{WorkspaceRoot: root},
		ProtectedBranch: "main",
	}
	result, err := tool.Execute(context.Background(), map[string]any{"branch": "main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected push to be blocked when explicitly targeting the protected branch")
	}
	blocked, _ := result.Output["blocked"].(bool)
	if !blocked {
		t.Errorf("expected blocked=true even though current branch is feature/x, got output: %v", result.Output)
	}
}

func TestGitCommitTool_AllowedOnFeatureBranch(t *testing.T) {
	root := initGitRepo(t)
	if out, exitCode, err := runGitCommand(root, "checkout", "-b", "feature/x"); err != nil || exitCode != 0 {
		t.Fatalf("git checkout -b feature/x failed (exit %d): %v\n%s", exitCode, err, out)
	}
	writeAndStage(t, root, "file.txt", "content\n")
	before := currentSHA(t, root)

	tool := &GitCommitTool{
		ToolPaths:       ToolPaths{WorkspaceRoot: root},
		ProtectedBranch: "main",
	}
	result, err := tool.Execute(context.Background(), map[string]any{"message": "feature work"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected commit to succeed on feature branch, got error: %s", result.Error)
	}

	after := currentSHA(t, root)
	if before == after {
		t.Error("expected a new commit to be created on the feature branch")
	}
}

func TestGitCommitTool_AllowedWhenProtectedBranchEmpty(t *testing.T) {
	root := initGitRepo(t)
	writeAndStage(t, root, "file.txt", "content\n")
	before := currentSHA(t, root)

	tool := &GitCommitTool{
		ToolPaths:       ToolPaths{WorkspaceRoot: root},
		ProtectedBranch: "",
	}
	result, err := tool.Execute(context.Background(), map[string]any{"message": "opt-out of protection"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected commit to succeed when ProtectedBranch is empty, got error: %s", result.Error)
	}

	after := currentSHA(t, root)
	if before == after {
		t.Error("expected a new commit to be created")
	}
}

func TestGitPushTool_AllowedOnFeatureBranch_NoExplicitBranch(t *testing.T) {
	root := initGitRepo(t)
	if out, exitCode, err := runGitCommand(root, "checkout", "-b", "feature/x"); err != nil || exitCode != 0 {
		t.Fatalf("git checkout -b feature/x failed (exit %d): %v\n%s", exitCode, err, out)
	}

	tool := &GitPushTool{
		ToolPaths:       ToolPaths{WorkspaceRoot: root},
		ProtectedBranch: "main",
	}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No remote is configured in this repo, so the real `git push` will fail
	// on its own — but it must NOT fail via the protected-branch block.
	if result.Success {
		t.Fatal("expected push to fail (no remote configured), but not via protected-branch block")
	}
	if blocked, _ := result.Output["blocked"].(bool); blocked {
		t.Errorf("expected the failure to come from the real git push (no remote), not the protected-branch check; got output: %v", result.Output)
	}
	if !strings.Contains(result.Error, "git push") {
		t.Errorf("expected a git-push-related error, got: %s", result.Error)
	}
}
