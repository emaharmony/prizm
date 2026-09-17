// Package tool provides git tools for Prizm agents.
//
// Git tools are split into read-only (always allowed) and mutation (requires approval).
//
// Read-only git tools: git_status, git_log, git_diff, git_branch_list
// Mutation git tools: git_commit (requires approval), git_push (requires approval)
package tool

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/emaharmony/prizm/internal/gitx"
)

// gitRefPattern matches safe git ref/remote names: letters, digits, and the
// limited punctuation git itself permits. It deliberately excludes anything
// that could be interpreted as a flag, URL, path, or shell metacharacter.
var gitRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// validateGitRef rejects empty-handling refs that look like flags or contain
// metacharacters. An empty ref is allowed (callers omit the arg). This blocks
// argument injection where a model-supplied branch/ref becomes a git option.
func validateGitRef(ref string) error {
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid ref %q: must not start with '-'", ref)
	}
	if !gitRefPattern.MatchString(ref) {
		return fmt.Errorf("invalid ref %q: only letters, digits, '.', '_', '/', '-' are allowed", ref)
	}
	return nil
}

// validateGitRemote validates a remote. A bare configured remote name is
// required; URLs and flag-like values are rejected to prevent push to an
// arbitrary destination or argument injection.
func validateGitRemote(remote string) error {
	if remote == "" {
		return nil
	}
	if strings.HasPrefix(remote, "-") {
		return fmt.Errorf("invalid remote %q: must not start with '-'", remote)
	}
	if strings.Contains(remote, "://") || strings.ContainsAny(remote, "@:") {
		return fmt.Errorf("invalid remote %q: must be a configured remote name, not a URL", remote)
	}
	if !gitRefPattern.MatchString(remote) {
		return fmt.Errorf("invalid remote %q: only letters, digits, '.', '_', '/', '-' are allowed", remote)
	}
	return nil
}

// ---------- Read-only Git Tools (always allowed) ----------

// GitStatusTool runs `git status` in a repository directory.
type GitStatusTool struct {
	ToolPaths ToolPaths
}

func (t *GitStatusTool) Name() string { return "git_status" }
func (t *GitStatusTool) Description() string {
	return "Shows the working tree status (git status). Lists modified, staged, and untracked files."
}
func (t *GitStatusTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"repo_path": {Type: "string", Description: "Absolute path to the git repository. Use absolute paths like /Users/ema/projects/repos/bassbook. If unsure, use project_overview first.", Required: false},
		},
		Output: ParamSpec{Type: "string", Description: "Git status output"},
	}
}
func (t *GitStatusTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	repoDir := t.ToolPaths.WorkspaceRoot
	if p, ok := input["repo_path"].(string); ok && p != "" {
		resolved, err := FuzzyResolvePath(t.ToolPaths, p)
		if err != nil {
			return ToolResult{Success: false, Error: fmt.Sprintf("repo_path resolution failed: %v", err)}, nil
		}
		repoDir = resolved
	}

	out, exitCode, err := runGitCommand(repoDir, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("git status failed: %v", err)}, nil
	}
	if exitCode != 0 {
		return ToolResult{Success: false, Error: fmt.Sprintf("git status exited with code %d: %s", exitCode, out)}, nil
	}
	return ToolResult{
		Success: true,
		Output:  map[string]any{"output": out},
	}, nil
}

// GitLogTool runs `git log` to show recent commits.
type GitLogTool struct {
	ToolPaths ToolPaths
}

func (t *GitLogTool) Name() string { return "git_log" }
func (t *GitLogTool) Description() string {
	return "Shows recent git commit history. Use this to understand what changes have been made recently."
}
func (t *GitLogTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"count":     {Type: "integer", Description: "Number of commits to show (default: 10, max: 50)", Required: false},
			"branch":    {Type: "string", Description: "Branch name to show log for (default: current branch)", Required: false},
			"repo_path": {Type: "string", Description: "Absolute path to the git repository. Use absolute paths like /Users/ema/projects/repos/bassbook.", Required: false},
		},
		Output: ParamSpec{Type: "string", Description: "Git log output with commit hashes, authors, dates, and messages"},
	}
}
func (t *GitLogTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	repoDir := t.ToolPaths.WorkspaceRoot
	if p, ok := input["repo_path"].(string); ok && p != "" {
		resolved, err := FuzzyResolvePath(t.ToolPaths, p)
		if err != nil {
			return ToolResult{Success: false, Error: fmt.Sprintf("repo_path resolution failed: %v", err)}, nil
		}
		repoDir = resolved
	}

	count := 10
	if c, ok := input["count"].(float64); ok && c > 0 {
		count = int(c)
		if count > 50 {
			count = 50
		}
	}

	args := []string{"log", "--oneline", "--decorate", "-n", strconv.Itoa(count)}

	if branch, ok := input["branch"].(string); ok && branch != "" {
		if err := validateGitRef(branch); err != nil {
			return ToolResult{Success: false, Error: err.Error()}, nil
		}
		args = append(args, branch)
	}

	out, exitCode, err := runGitCommand(repoDir, args...)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("git log failed: %v", err)}, nil
	}
	if exitCode != 0 {
		return ToolResult{Success: false, Error: fmt.Sprintf("git log exited with code %d: %s", exitCode, out)}, nil
	}
	return ToolResult{
		Success: true,
		Output:  map[string]any{"output": out, "count": count},
	}, nil
}

// GitDiffTool shows git diffs (unstaged, staged, or against a branch).
type GitDiffTool struct {
	ToolPaths ToolPaths
}

func (t *GitDiffTool) Name() string { return "git_diff" }
func (t *GitDiffTool) Description() string {
	return "Shows git diffs — changes between working tree, staging area, and commits. Use this to review what code changes look like."
}
func (t *GitDiffTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"staged":    {Type: "boolean", Description: "Show staged changes instead of unstaged (default: false)", Required: false},
			"branch":    {Type: "string", Description: "Compare against a branch (e.g., 'main') instead of working tree", Required: false},
			"path":      {Type: "string", Description: "Specific file or directory to diff", Required: false},
			"repo_path": {Type: "string", Description: "Absolute path to the git repository. Use absolute paths like /Users/ema/projects/repos/bassbook.", Required: false},
		},
		Output: ParamSpec{Type: "string", Description: "Git diff output"},
	}
}
func (t *GitDiffTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	repoDir := t.ToolPaths.WorkspaceRoot
	if p, ok := input["repo_path"].(string); ok && p != "" {
		resolved, err := FuzzyResolvePath(t.ToolPaths, p)
		if err != nil {
			return ToolResult{Success: false, Error: fmt.Sprintf("repo_path resolution failed: %v", err)}, nil
		}
		repoDir = resolved
	}

	args := []string{"diff"}

	if staged, ok := input["staged"].(bool); ok && staged {
		args = append(args, "--staged")
	}

	if branch, ok := input["branch"].(string); ok && branch != "" {
		if err := validateGitRef(branch); err != nil {
			return ToolResult{Success: false, Error: err.Error()}, nil
		}
		args = append(args, branch)
	}

	// Append -- before path to disambiguate from branches
	if diffPath, ok := input["path"].(string); ok && diffPath != "" {
		args = append(args, "--", diffPath)
	}

	out, exitCode, err := runGitCommand(repoDir, args...)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("git diff failed: %v", err)}, nil
	}
	if exitCode != 0 {
		return ToolResult{Success: false, Error: fmt.Sprintf("git diff exited with code %d: %s", exitCode, out)}, nil
	}

	// Truncate very large diffs
	output := out
	if len(output) > 50000 {
		output = output[:50000] + "\n... (truncated, diff too large)"
	}

	return ToolResult{
		Success: true,
		Output:  map[string]any{"output": output, "length": len(out)},
	}, nil
}

// GitBranchListTool lists local branches.
type GitBranchListTool struct {
	ToolPaths ToolPaths
}

func (t *GitBranchListTool) Name() string { return "git_branch_list" }
func (t *GitBranchListTool) Description() string {
	return "Lists local git branches with the current branch marked. Use this to see what branches exist."
}
func (t *GitBranchListTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"repo_path": {Type: "string", Description: "Absolute path to the git repository. Use absolute paths like /Users/ema/projects/repos/bassbook.", Required: false},
		},
		Output: ParamSpec{Type: "array", Description: "List of branch names with current branch indicator"},
	}
}
func (t *GitBranchListTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	repoDir := t.ToolPaths.WorkspaceRoot
	if p, ok := input["repo_path"].(string); ok && p != "" {
		resolved, err := FuzzyResolvePath(t.ToolPaths, p)
		if err != nil {
			return ToolResult{Success: false, Error: fmt.Sprintf("repo_path resolution failed: %v", err)}, nil
		}
		repoDir = resolved
	}

	out, exitCode, err := runGitCommand(repoDir, "branch", "--list")
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("git branch failed: %v", err)}, nil
	}
	if exitCode != 0 {
		return ToolResult{Success: false, Error: fmt.Sprintf("git branch exited with code %d: %s", exitCode, out)}, nil
	}

	// Parse branch list
	var branches []map[string]any
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		isCurrent := strings.HasPrefix(line, "*")
		name := strings.TrimPrefix(line, "* ")
		name = strings.TrimSpace(name)
		branches = append(branches, map[string]any{
			"name":    name,
			"current": isCurrent,
		})
	}

	return ToolResult{
		Success: true,
		Output:  map[string]any{"branches": branches, "count": len(branches)},
	}, nil
}

// GitCheckoutTool creates and/or switches git branches. This is a mutation.
type GitCheckoutTool struct {
	ToolPaths ToolPaths
}

func (t *GitCheckoutTool) Name() string { return "git_checkout" }
func (t *GitCheckoutTool) Description() string {
	return "Creates a new git branch and switches to it, or switches to an existing branch. Use this to create feature branches before writing code. Example: create branch 'feature/bb-auth-fix' from main."
}
func (t *GitCheckoutTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"branch":    {Type: "string", Description: "Branch name to create or switch to (e.g. feature/bb-auth-fix)", Required: true},
			"create":    {Type: "boolean", Description: "If true, create the branch (-b flag). If false, just switch to existing branch.", Required: false},
			"repo_path": {Type: "string", Description: "Absolute path to the git repository", Required: false},
		},
		Output: ParamSpec{Type: "string", Description: "Checkout result"},
	}
}
func (t *GitCheckoutTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	repoDir := t.ToolPaths.WorkspaceRoot
	if p, ok := input["repo_path"].(string); ok && p != "" {
		resolved, err := FuzzyResolvePath(t.ToolPaths, p)
		if err != nil {
			return ToolResult{Success: false, Error: fmt.Sprintf("repo_path resolution failed: %v", err)}, nil
		}
		repoDir = resolved
	}

	branchVal, ok := input["branch"].(string)
	if !ok || branchVal == "" {
		return ToolResult{Success: false, Error: "required parameter 'branch' must be a non-empty string"}, nil
	}

	// Block dangerous branch names
	if strings.Contains(branchVal, "..") || strings.Contains(branchVal, ";") || strings.Contains(branchVal, "&") || strings.Contains(branchVal, "|") {
		return ToolResult{Success: false, Error: "invalid branch name"}, nil
	}

	create, _ := input["create"].(bool)

	var args []string
	if create {
		args = []string{"checkout", "-b", branchVal}
	} else {
		args = []string{"checkout", branchVal}
	}

	out, exitCode, err := runGitCommand(repoDir, args...)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("git checkout failed: %v", err)}, nil
	}
	if exitCode != 0 {
		return ToolResult{Success: false, Error: fmt.Sprintf("git checkout exited with code %d: %s", exitCode, out)}, nil
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"branch":  branchVal,
			"created": create,
			"message": fmt.Sprintf("Switched to branch '%s'", branchVal),
		},
	}, nil
}

// GitAddTool stages files for commit. This is a mutation — it requires approval.
type GitAddTool struct {
	ToolPaths ToolPaths
}

func (t *GitAddTool) Name() string { return "git_add" }
func (t *GitAddTool) Description() string {
	return "Stages file changes for the next commit. Requires approval before executing. Use git_status to see what files have changed."
}
func (t *GitAddTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"path":      {Type: "string", Description: "File or directory to stage (use '.' for all changes)", Required: true},
			"repo_path": {Type: "string", Description: "Absolute path to the git repository. Use absolute paths like /Users/ema/projects/repos/bassbook.", Required: false},
		},
		Output: ParamSpec{Type: "string", Description: "Staging result"},
	}
}
func (t *GitAddTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	repoDir := t.ToolPaths.WorkspaceRoot
	if p, ok := input["repo_path"].(string); ok && p != "" {
		resolved, err := FuzzyResolvePath(t.ToolPaths, p)
		if err != nil {
			return ToolResult{Success: false, Error: fmt.Sprintf("repo_path resolution failed: %v", err)}, nil
		}
		repoDir = resolved
	}

	pathVal, ok := input["path"].(string)
	if !ok || pathVal == "" {
		return ToolResult{Success: false, Error: "required parameter 'path' must be a non-empty string"}, nil
	}

	// Block path traversal
	if strings.Contains(pathVal, "..") {
		return ToolResult{Success: false, Error: "path traversal blocked"}, nil
	}

	out, exitCode, err := runGitCommand(repoDir, "add", pathVal)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("git add failed: %v", err)}, nil
	}
	if exitCode != 0 {
		return ToolResult{Success: false, Error: fmt.Sprintf("git add exited with code %d: %s", exitCode, out)}, nil
	}

	return ToolResult{
		Success: true,
		Output:  map[string]any{"output": out, "path": pathVal},
	}, nil
}

// ---------- Mutation Git Tools (requires approval) ----------

// GitCommitTool creates a git commit. This is a mutation — it requires approval
// before executing. The agent must propose the commit, and a human must approve.
type GitCommitTool struct {
	ToolPaths ToolPaths

	// ProtectedBranch, if non-empty, is a branch name this tool refuses to
	// commit to directly, regardless of approval/auto-approve state. Empty
	// means no protection (the default for callers that don't opt in).
	ProtectedBranch string
}

func (t *GitCommitTool) Name() string { return "git_commit" }
func (t *GitCommitTool) Description() string {
	return "Creates a git commit with a message. Requires approval before executing. Stage files with git add first."
}
func (t *GitCommitTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"message":   {Type: "string", Description: "The commit message", Required: true},
			"repo_path": {Type: "string", Description: "Absolute path to the git repository. Use absolute paths like /Users/ema/projects/repos/bassbook.", Required: false},
		},
		Output: ParamSpec{Type: "string", Description: "Commit hash and summary"},
	}
}
func (t *GitCommitTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	repoDir := t.ToolPaths.WorkspaceRoot
	if p, ok := input["repo_path"].(string); ok && p != "" {
		resolved, err := FuzzyResolvePath(t.ToolPaths, p)
		if err != nil {
			return ToolResult{Success: false, Error: fmt.Sprintf("repo_path resolution failed: %v", err)}, nil
		}
		repoDir = resolved
	}

	if t.ProtectedBranch != "" {
		if branch, berr := gitx.CurrentBranch(ctx, repoDir); berr == nil && branch == t.ProtectedBranch {
			return ToolResult{
				Success: false,
				Output: map[string]any{
					"blocked":          true,
					"blocked_reason":   "protected_branch",
					"branch":           branch,
					"protected_branch": t.ProtectedBranch,
				},
				Error: fmt.Sprintf("git commit blocked: current branch %q is protected; create a feature branch with git_checkout first", branch),
			}, nil
		}
	}

	message, ok := input["message"].(string)
	if !ok || message == "" {
		return ToolResult{Success: false, Error: "required parameter 'message' must be a non-empty string"}, nil
	}

	// Security: sanitize commit message (no newlines to prevent message injection)
	message = strings.ReplaceAll(message, "\n", " ")
	message = strings.ReplaceAll(message, "\r", "")
	if len(message) > 500 {
		message = message[:500]
	}

	out, exitCode, err := runGitCommand(repoDir, "commit", "-m", message)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("git commit failed: %v", err)}, nil
	}
	if exitCode != 0 {
		return ToolResult{Success: false, Error: fmt.Sprintf("git commit exited with code %d: %s", exitCode, out)}, nil
	}

	return ToolResult{
		Success: true,
		Output:  map[string]any{"output": out, "message": message},
	}, nil
}

// GitPushTool pushes commits to a remote. This is a mutation — it requires
// approval before executing.
type GitPushTool struct {
	ToolPaths ToolPaths

	// ProtectedBranch, if non-empty, is a branch name this tool refuses to
	// push to directly, regardless of approval/auto-approve state. Blocks on
	// either the repo's current branch or an explicitly targeted branch
	// param matching this value. Empty means no protection.
	ProtectedBranch string
}

func (t *GitPushTool) Name() string { return "git_push" }
func (t *GitPushTool) Description() string {
	return "Pushes commits to a remote repository. Requires approval before executing. Use git_log first to review what will be pushed."
}
func (t *GitPushTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"remote":    {Type: "string", Description: "Remote name (default: origin)", Required: false},
			"branch":    {Type: "string", Description: "Branch to push (default: current branch)", Required: false},
			"repo_path": {Type: "string", Description: "Absolute path to the git repository. Use absolute paths like /Users/ema/projects/repos/bassbook.", Required: false},
		},
		Output: ParamSpec{Type: "string", Description: "Push output or error"},
	}
}
func (t *GitPushTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	repoDir := t.ToolPaths.WorkspaceRoot
	if p, ok := input["repo_path"].(string); ok && p != "" {
		resolved, err := FuzzyResolvePath(t.ToolPaths, p)
		if err != nil {
			return ToolResult{Success: false, Error: fmt.Sprintf("repo_path resolution failed: %v", err)}, nil
		}
		repoDir = resolved
	}

	remote := "origin"
	if r, ok := input["remote"].(string); ok && r != "" {
		remote = r
	}
	if err := validateGitRemote(remote); err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}

	args := []string{"push", remote}

	if branch, ok := input["branch"].(string); ok && branch != "" {
		if err := validateGitRef(branch); err != nil {
			return ToolResult{Success: false, Error: err.Error()}, nil
		}
		args = append(args, branch)
	}

	if t.ProtectedBranch != "" {
		current, berr := gitx.CurrentBranch(ctx, repoDir)
		explicitBranch, _ := input["branch"].(string)
		blockedBranch := ""
		if berr == nil && current == t.ProtectedBranch {
			blockedBranch = current
		} else if explicitBranch != "" && explicitBranch == t.ProtectedBranch {
			blockedBranch = explicitBranch
		}
		if blockedBranch != "" {
			return ToolResult{
				Success: false,
				Output: map[string]any{
					"blocked":          true,
					"blocked_reason":   "protected_branch",
					"branch":           blockedBranch,
					"protected_branch": t.ProtectedBranch,
				},
				Error: fmt.Sprintf("git push blocked: %q is the protected branch; push a feature branch and open a PR instead", blockedBranch),
			}, nil
		}
	}

	out, exitCode, err := runGitCommand(repoDir, args...)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("git push failed: %v", err)}, nil
	}
	if exitCode != 0 {
		return ToolResult{Success: false, Error: fmt.Sprintf("git push exited with code %d: %s", exitCode, out)}, nil
	}

	return ToolResult{
		Success: true,
		Output:  map[string]any{"output": out, "remote": remote},
	}, nil
}

// GitCreatePRTool creates a pull request using the gh CLI.
type GitCreatePRTool struct{}

func (t *GitCreatePRTool) Name() string { return "create_pr" }
func (t *GitCreatePRTool) Description() string {
	return "Create a GitHub pull request using the gh CLI. Requires a title and body. The base branch defaults to main."
}
func (t *GitCreatePRTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"repo_path": {Type: "string", Required: true, Description: "Absolute path to the git repository"},
			"title":     {Type: "string", Required: true, Description: "PR title"},
			"body":      {Type: "string", Required: false, Description: "PR description (markdown)"},
			"base":      {Type: "string", Required: false, Description: "Base branch (default: main)"},
			"head":      {Type: "string", Required: false, Description: "Head branch (default: current branch)"},
		},
	}
}
func (t *GitCreatePRTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	repoDir, _ := input["repo_path"].(string)
	if repoDir == "" {
		return ToolResult{Success: false, Error: "repo_path is required"}, nil
	}

	title, _ := input["title"].(string)
	if title == "" {
		return ToolResult{Success: false, Error: "title is required"}, nil
	}

	body, _ := input["body"].(string)
	base, _ := input["base"].(string)
	if base == "" {
		base = "main"
	}
	head, _ := input["head"].(string)

	// Build gh pr create command
	args := []string{"pr", "create", "--title", title, "--body", body, "--base", base}
	if head != "" {
		args = append(args, "--head", head)
	}

	cmd := exec.Command("gh", args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("gh pr create failed: %v: %s", err, string(out))}, nil
	}

	return ToolResult{
		Success: true,
		Output:  map[string]any{"output": string(out), "url": strings.TrimSpace(string(out))},
	}, nil
}
