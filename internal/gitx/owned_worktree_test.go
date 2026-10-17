package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedRunWorktreeTrustIsScopedAndLeavesGlobalConfigUntouched(t *testing.T) {
	source, workspace, runID, base := makeLinkedRunWorktree(t)
	global := filepath.Join(t.TempDir(), "global.gitconfig")
	wantConfig := []byte("[safe]\n\tdirectory = /existing/trusted/path\n")
	if err := os.WriteFile(global, wantConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	ctx, err := WithVerifiedRunWorktree(context.Background(), workspace, source, runID, base)
	if err != nil {
		t.Fatalf("verify trusted worktree: %v", err)
	}
	if _, err := WorktreeTree(ctx, workspace); err != nil {
		t.Fatalf("inspect trusted worktree: %v", err)
	}
	if _, err := RunCommand(ctx, workspace, "", "git", "--git-dir", filepath.Join(source, ".git"), "rev-parse", "HEAD"); err == nil {
		t.Fatal("repository override was accepted")
	}
	if _, err := RunCommand(ctx, source, "", "git", "rev-parse", "HEAD"); err == nil {
		t.Fatal("verified context trusted a different command directory")
	}
	gotConfig, err := os.ReadFile(global)
	if err != nil || string(gotConfig) != string(wantConfig) {
		t.Fatalf("global Git config changed: err=%v", err)
	}
}

func TestVerifiedRunWorktreeRejectsUntrustedPathAndBase(t *testing.T) {
	source, workspace, runID, base := makeLinkedRunWorktree(t)
	if _, err := WithVerifiedRunWorktree(context.Background(), t.TempDir(), source, runID, base); err == nil {
		t.Fatal("workspace outside the saved run path was accepted")
	}
	wrongBase := strings.Repeat("0", len(base))
	if wrongBase == base {
		wrongBase = strings.Repeat("1", len(base))
	}
	if _, err := WithVerifiedRunWorktree(context.Background(), workspace, source, runID, wrongBase); err == nil {
		t.Fatal("worktree with a base mismatch was trusted")
	}
	if _, err := WithVerifiedRunWorktree(context.Background(), workspace, source, "different-run", base); err == nil {
		t.Fatal("workspace belonging to another run ID was trusted")
	}
}

func TestVerifiedRunWorktreeRejectsDifferentRepositoryLink(t *testing.T) {
	source, _, runID, base := makeLinkedRunWorktree(t)
	workspace := filepath.Join(source, ".prizm", "worktrees", SafeID(runID, "run"))
	if err := os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, workspace, "init", "-b", "main")
	gitTestCommand(t, workspace, "config", "user.email", "test@prizm.local")
	gitTestCommand(t, workspace, "config", "user.name", "Prizm Test")
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("substitute\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, workspace, "add", ".")
	gitTestCommand(t, workspace, "commit", "-m", "substitute")
	if _, err := WithVerifiedRunWorktree(context.Background(), workspace, source, runID, base); err == nil {
		t.Fatal("different repository under the approved run path was trusted")
	}
}

func TestVerifiedRunWorktreeAllowsSourceToAdvanceAfterApproval(t *testing.T) {
	source, workspace, runID, base := makeLinkedRunWorktree(t)
	if err := os.WriteFile(filepath.Join(source, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, source, "add", "later.txt")
	gitTestCommand(t, source, "commit", "-m", "advance source after approval")
	advanced := strings.TrimSpace(gitTestCommand(t, source, "rev-parse", "HEAD"))
	if advanced == base {
		t.Fatal("test source did not advance")
	}
	if got := strings.TrimSpace(gitTestCommand(t, workspace, "rev-parse", "HEAD")); got != base {
		t.Fatalf("isolated worktree moved from approved base: got %s", got)
	}
	if _, err := WithVerifiedRunWorktree(context.Background(), workspace, source, runID, base); err != nil {
		t.Fatalf("source advancement invalidated approved worktree: %v", err)
	}
}

func TestVerifiedRunWorktreeRejectsLinkedWorkspaceAlias(t *testing.T) {
	source, workspace, runID, base := makeLinkedRunWorktree(t)
	alias := filepath.Join(filepath.Dir(workspace), "alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	if _, err := WithVerifiedRunWorktree(context.Background(), alias, source, runID, base); err == nil {
		t.Fatal("symlink alias for the run worktree was trusted")
	}
}

func makeLinkedRunWorktree(t *testing.T) (source, workspace, runID, base string) {
	t.Helper()
	runID = "run-owned"
	source = filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, source, "init", "-b", "main")
	gitTestCommand(t, source, "config", "user.email", "test@prizm.local")
	gitTestCommand(t, source, "config", "user.name", "Prizm Test")
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, source, "add", ".")
	gitTestCommand(t, source, "commit", "-m", "base")
	base = strings.TrimSpace(gitTestCommand(t, source, "rev-parse", "HEAD"))
	workspace = filepath.Join(source, ".prizm", "worktrees", SafeID(runID, "run"))
	if err := os.MkdirAll(filepath.Dir(workspace), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, source, "worktree", "add", "--detach", workspace, base)
	return source, workspace, runID, base
}

func gitTestCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed (output bytes=%d): %v", strings.Join(args, " "), len(out), err)
	}
	return string(out)
}
