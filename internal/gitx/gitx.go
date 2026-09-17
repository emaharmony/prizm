// Package gitx provides shared git helpers for run isolation: worktree
// create/remove, clean-tree checks, and rollback primitives.
//
// Lifted from the autopatch pipeline (V50/V51) so the gated loop can reuse the
// same battle-tested worktree mechanics for per-run isolation (V56) and
// automated rollback (V57). Before this package existed the repo had four
// separate git invocation paths; new git plumbing belongs here.
package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrDirtyWorktree is returned by EnsureClean when the repository has
// uncommitted changes.
var ErrDirtyWorktree = errors.New("operation requires a clean git worktree")

// CreateDetachedWorktree adds a worktree at path, detached at HEAD. This is
// the autopatch mode: no branch is created, and the worktree is discarded
// wholesale after the patch is captured.
func CreateDetachedWorktree(ctx context.Context, root, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	_, err := RunCommand(ctx, root, "", "git", "worktree", "add", "--detach", path, "HEAD")
	if err != nil {
		return fmt.Errorf("create worktree: %w", err)
	}
	return nil
}

// CreateBranchWorktree adds a worktree at path on a NEW branch created from
// base (empty base = HEAD). This is the gated-loop mode (V56): the run works
// on its own branch inside its own directory, so parallel runs in one repo
// cannot collide, and the branch survives worktree cleanup once pushed.
func CreateBranchWorktree(ctx context.Context, root, path, branch, base string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if base == "" {
		base = "HEAD"
	}
	_, err := RunCommand(ctx, root, "", "git", "worktree", "add", "-b", branch, path, base)
	if err != nil {
		return fmt.Errorf("create branch worktree: %w", err)
	}
	return nil
}

// RemoveWorktree force-removes a worktree and its directory. Best-effort:
// cleanup failures must never mask the run's real outcome.
func RemoveWorktree(ctx context.Context, root, path string) {
	_, _ = RunCommand(ctx, root, "", "git", "worktree", "remove", "--force", path)
	_ = os.RemoveAll(path)
}

// EnsureClean returns an ErrDirtyWorktree-wrapped error when the repository
// has uncommitted changes.
func EnsureClean(ctx context.Context, root string) error {
	out, err := RunCommand(ctx, root, "", "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("%w: %s", ErrDirtyWorktree, strings.TrimSpace(out))
	}
	return nil
}

// CurrentSHA returns the commit hash of HEAD.
func CurrentSHA(ctx context.Context, root string) (string, error) {
	out, err := RunCommand(ctx, root, "", "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CurrentBranch returns the checked-out branch name (empty on detached HEAD).
func CurrentBranch(ctx context.Context, root string) (string, error) {
	out, err := RunCommand(ctx, root, "", "git", "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ResetHard discards all changes and moves the current branch back to sha.
// V57 rollback primitive — callers gate this behind explicit config.
func ResetHard(ctx context.Context, root, sha string) error {
	_, err := RunCommand(ctx, root, "", "git", "reset", "--hard", sha)
	if err != nil {
		return fmt.Errorf("reset --hard %s: %w", sha, err)
	}
	return nil
}

// DeleteBranch force-deletes a local branch. Best-effort (V57 rollback of a
// discarded run branch).
func DeleteBranch(ctx context.Context, root, branch string) {
	_, _ = RunCommand(ctx, root, "", "git", "branch", "-D", branch)
}

// EnsureExcluded appends pattern to the repo's .git/info/exclude (repo-local
// ignore, never committed) unless already present. Used so run worktrees under
// .prizm/ don't make the parent repo read as dirty in target repos that don't
// gitignore .prizm themselves.
func EnsureExcluded(ctx context.Context, root, pattern string) error {
	gitDir, err := RunCommand(ctx, root, "", "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	excludePath := filepath.Join(ResolveUnder(root, strings.TrimSpace(gitDir)), "info", "exclude")
	if data, err := os.ReadFile(excludePath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == pattern {
				return nil
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(pattern + "\n")
	return err
}

// RunCommand executes name with args in dir, feeding stdin when non-empty,
// and returns the combined output. Errors include the trimmed output.
func RunCommand(ctx context.Context, dir, stdin, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ResolveUnder joins path to root unless path is already absolute.
func ResolveUnder(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

// SafeID sanitizes value for use as a filesystem path segment, keeping
// [a-zA-Z0-9._-] and capping length. fallback is used when nothing survives.
func SafeID(value, fallback string) string {
	value = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, value)
	value = strings.Trim(value, ".-_")
	if value == "" {
		return fallback
	}
	if len(value) > 120 {
		return value[:120]
	}
	return value
}
