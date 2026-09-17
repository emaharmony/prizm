package subagent

import (
	"context"
	"path/filepath"

	"github.com/emaharmony/prizm/internal/gitx"
)

// worktree.go gives file-mutating sub-agents their own git worktree so parallel
// runs can't collide on the working tree. It reuses the V56 gitx primitives: a
// per-task worktree on a fresh subagent/<task> branch, removed on completion
// (the branch survives if the agent pushed it). Non-mutating agents skip
// isolation entirely.

// WorktreeProvider hands a task an isolated working directory. Acquire returns
// the dir plus a release func the caller must always invoke (cleanup).
type WorktreeProvider interface {
	Acquire(ctx context.Context, taskID string) (dir string, release func(), err error)
}

// NoopWorktreeProvider runs every task in a single shared root with no
// isolation. Used when isolation is disabled.
type NoopWorktreeProvider struct{ Root string }

func (n NoopWorktreeProvider) Acquire(_ context.Context, _ string) (string, func(), error) {
	return n.Root, func() {}, nil
}

// GitWorktreeProvider creates a per-task git worktree under
// <Root>/.prizm/worktrees/subagent-<task> on branch subagent/<task>.
type GitWorktreeProvider struct{ Root string }

func (g GitWorktreeProvider) Acquire(ctx context.Context, taskID string) (string, func(), error) {
	// Keep the worktree dir out of the parent repo's dirty status.
	_ = gitx.EnsureExcluded(ctx, g.Root, ".prizm/")
	id := gitx.SafeID(taskID, "task")
	path := filepath.Join(g.Root, ".prizm", "worktrees", "subagent-"+id)
	branch := "subagent/" + id
	if err := gitx.CreateBranchWorktree(ctx, g.Root, path, branch, ""); err != nil {
		return "", nil, err
	}
	release := func() { gitx.RemoveWorktree(context.Background(), g.Root, path) }
	return path, release, nil
}
