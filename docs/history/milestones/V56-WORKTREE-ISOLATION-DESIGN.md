# V56 — Gated-Loop Worktree Isolation

Status: source-current
Date: 2026-07-04

## Goal

Run each gated loop in its own git worktree so parallel runs on the same repo
cannot collide, and the main worktree is never touched. Before V56 only the
autopatch pipeline was worktree-isolated; the gated loop shared the main
worktree, isolated by feature branch alone — two concurrent runs would step on
each other's uncommitted files.

This closes the "Worktrees" gap from the loop-engineering readiness assessment
(see `docs/LOOP-READINESS-STATE.md`).

## Design

### New shared package `internal/gitx`

The repo had four separate git invocation paths (autopatch `runCommand`, tool
`runGitCommand`, wake-handler `exec_command`, raw `exec.Command`). V56 lifts
the autopatch worktree helpers into `internal/gitx`, the first shared git
package:

- `CreateDetachedWorktree(ctx, root, path)` — autopatch mode (`--detach`)
- `CreateBranchWorktree(ctx, root, path, branch, base)` — gated-loop mode
  (`git worktree add -b <branch> <path> <base>`); the branch survives worktree
  cleanup once pushed
- `RemoveWorktree` (best-effort force removal), `EnsureClean` (+ exported
  `ErrDirtyWorktree`)
- `EnsureExcluded(ctx, root, pattern)` — appends to `.git/info/exclude`
  (repo-local, never committed) so worktrees under `<repo>/.prizm/` don't make
  the parent repo read as dirty in target repos that don't gitignore `.prizm`
- Rollback primitives for V57: `CurrentSHA`, `CurrentBranch`, `ResetHard`,
  `DeleteBranch`
- `RunCommand`, `ResolveUnder`, `SafeID` helpers

`internal/autopatch` now delegates to gitx through thin wrappers (call sites
unchanged, behavior preserved; `autopatch.ErrDirtyWorktree` aliases
`gitx.ErrDirtyWorktree` so `errors.Is` works across both packages).

### Config

`projects[].worktree_isolation: bool` (default false) on
`orchestrator.ProjectConfig`. Surfaced by `prizm config` as
`projects: N (M worktree-isolated)` / `isolated_projects` in JSON.

### RunGatedLoop wiring (`cmd/prizm-cli/wake_handler.go`)

When enabled, after `runID` is minted:

1. `gitx.EnsureExcluded(repo, ".prizm/")`, then
   `gitx.CreateBranchWorktree(repo, <repo>/.prizm/worktrees/<runID>, prizm/<runID>, HEAD)`
2. `workRoot` = the worktree path; **fail closed** — if the worktree cannot be
   created the run aborts rather than running un-isolated
3. Everything that touches the tree now uses `workRoot` instead of `repoPath`:
   the injected `repo_path` default for git tools, the validation executor
   root, `getBranch`, the system prompt + tool prompt suffix,
   `DriveOptions.RepoPath` (reviewers must diff the worktree), and
   `state.RepoPath`
4. The system prompt tells the model the run branch already exists — it must
   NOT `git_checkout create=true`
5. `defer gitx.RemoveWorktree(...)` — cleanup on every exit path; the branch
   (and its pushed commits) survive
6. `stateDir` (`runs/gated-loop`) is process-relative, outside the worktree —
   run state and REPORT.md persist after cleanup
7. PROJECT_STATE.md writes land inside the worktree → on the run branch →
   travel via push, exactly like code changes

The v2 loop is `repo_path`-argument driven rather than cwd-driven, so no tool
changes were needed — only the injected default.

## Known limits

- `runID` is `gl-<unix-seconds>`; two runs started within the same second on
  the same repo would collide on worktree path and branch name. Acceptable for
  now (wake events are not that fast); fix by adding an xid suffix if it bites.
- Worktree isolation requires git ≥ 2.5 (worktree support).

## Tests

`internal/gitx/gitx_test.go` — real `git init` repos in `t.TempDir()`:
branch-worktree create/remove + branch survival, detached mode, clean/dirty
detection, `ResetHard` rollback to a captured SHA, `DeleteBranch`, `SafeID`,
`ResolveUnder`, and the `.git/info/exclude` behavior keeping the parent clean.
Autopatch's existing suite covers the delegation wrappers.
