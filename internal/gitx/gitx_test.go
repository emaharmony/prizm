package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a git repo with one commit in a temp dir and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	mustRun := func(args ...string) {
		t.Helper()
		if out, err := RunCommand(ctx, root, "", "git", args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
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

func TestCreateBranchWorktree(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	wt := filepath.Join(root, ".prizm", "worktrees", "gl-1")

	// Repo-local exclude keeps the parent repo clean even though the
	// worktree lives under <repo>/.prizm/.
	if err := EnsureExcluded(ctx, root, ".prizm/"); err != nil {
		t.Fatalf("EnsureExcluded: %v", err)
	}
	if err := CreateBranchWorktree(ctx, root, wt, "prizm/gl-1", ""); err != nil {
		t.Fatalf("CreateBranchWorktree: %v", err)
	}
	// The worktree exists and is on the new branch.
	branch, err := CurrentBranch(ctx, wt)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "prizm/gl-1" {
		t.Fatalf("worktree branch = %q, want prizm/gl-1", branch)
	}
	// The main worktree is untouched.
	mainBranch, _ := CurrentBranch(ctx, root)
	if mainBranch != "main" {
		t.Fatalf("main worktree branch = %q, want main", mainBranch)
	}

	// Work in the worktree does not dirty the main worktree.
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureClean(ctx, root); err != nil {
		t.Fatalf("main worktree should stay clean while worktree is dirty: %v", err)
	}

	RemoveWorktree(ctx, root, wt)
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree dir should be removed, stat err = %v", err)
	}
	// The branch survives worktree removal (it may have been pushed).
	out, err := RunCommand(ctx, root, "", "git", "branch", "--list", "prizm/gl-1")
	if err != nil || !strings.Contains(out, "prizm/gl-1") {
		t.Fatalf("branch should survive worktree removal: out=%q err=%v", out, err)
	}
}

func TestCreateDetachedWorktree(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	wt := filepath.Join(root, ".prizm", "worktrees", "ap-1")

	if err := CreateDetachedWorktree(ctx, root, wt); err != nil {
		t.Fatalf("CreateDetachedWorktree: %v", err)
	}
	branch, err := CurrentBranch(ctx, wt)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "" {
		t.Fatalf("detached worktree should have no branch, got %q", branch)
	}
	RemoveWorktree(ctx, root, wt)
}

func TestEnsureClean(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()

	if err := EnsureClean(ctx, root); err != nil {
		t.Fatalf("fresh repo should be clean: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := EnsureClean(ctx, root)
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("expected ErrDirtyWorktree, got %v", err)
	}
}

func TestResetHardAndCurrentSHA(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()

	startSHA, err := CurrentSHA(ctx, root)
	if err != nil {
		t.Fatalf("CurrentSHA: %v", err)
	}
	// Make a second commit.
	if err := os.WriteFile(filepath.Join(root, "second.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCommand(ctx, root, "", "git", "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCommand(ctx, root, "", "git", "commit", "-m", "second"); err != nil {
		t.Fatal(err)
	}
	// Roll back.
	if err := ResetHard(ctx, root, startSHA); err != nil {
		t.Fatalf("ResetHard: %v", err)
	}
	sha, _ := CurrentSHA(ctx, root)
	if sha != startSHA {
		t.Fatalf("HEAD = %s, want %s after rollback", sha, startSHA)
	}
	if _, err := os.Stat(filepath.Join(root, "second.txt")); !os.IsNotExist(err) {
		t.Fatal("second.txt should be gone after reset --hard")
	}
}

func TestDeleteBranch(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	if _, err := RunCommand(ctx, root, "", "git", "branch", "doomed"); err != nil {
		t.Fatal(err)
	}
	DeleteBranch(ctx, root, "doomed")
	out, _ := RunCommand(ctx, root, "", "git", "branch", "--list", "doomed")
	if strings.Contains(out, "doomed") {
		t.Fatal("branch should be deleted")
	}
}

func TestSafeID(t *testing.T) {
	cases := map[string]string{
		"gl-1700000000":    "gl-1700000000",
		"weird/id with sp": "weird-id-with-sp",
		"---":              "fallback",
		"":                 "fallback",
	}
	for in, want := range cases {
		if got := SafeID(in, "fallback"); got != want {
			t.Errorf("SafeID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveUnder(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "x")
	if got := ResolveUnder("/root", abs); got != abs {
		t.Errorf("absolute path should pass through, got %q", got)
	}
	if got := ResolveUnder("root", "rel"); got != filepath.Join("root", "rel") {
		t.Errorf("relative path should join root, got %q", got)
	}
}
