package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type ownedWorktreeContextKey struct{}

type ownedWorktreeScope struct {
	workspace string
	source    string
	runID     string
	baseSHA   string
}

// WithVerifiedRunWorktree permits Git to trust one manifest-owned run
// worktree, but only while it remains linked to the saved source repository
// and at the approved base commit. It never changes Git configuration.
func WithVerifiedRunWorktree(ctx context.Context, workspace, sourceWorkspace, runID, expectedBase string) (context.Context, error) {
	scope, err := verifyRunWorktree(ctx, workspace, sourceWorkspace, runID, expectedBase)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, ownedWorktreeContextKey{}, scope), nil
}

func verifyRunWorktree(ctx context.Context, workspace, sourceWorkspace, runID, expectedBase string) (*ownedWorktreeScope, error) {
	workspace, err := canonicalExistingDirectory(workspace)
	if err != nil {
		return nil, fmt.Errorf("run workspace is not a canonical directory")
	}
	sourceWorkspace, err = canonicalExistingDirectory(sourceWorkspace)
	if err != nil {
		return nil, fmt.Errorf("source workspace is not a canonical directory")
	}
	if !validCommitID(expectedBase) {
		return nil, fmt.Errorf("approved base identity is invalid")
	}
	worktreeParent := filepath.Join(sourceWorkspace, ".prizm", "worktrees")
	wantWorkspace := filepath.Join(worktreeParent, SafeID(runID, "run"))
	if strings.TrimSpace(runID) == "" || !withinCanonicalRoot(worktreeParent, workspace) || !samePath(workspace, wantWorkspace) {
		return nil, fmt.Errorf("run workspace does not match its saved run identity")
	}
	workTop, workCommon, workHead, err := inspectRepository(ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("run workspace repository identity is unavailable")
	}
	sourceTop, sourceCommon, _, err := inspectRepository(ctx, sourceWorkspace)
	if err != nil {
		return nil, fmt.Errorf("source repository identity is unavailable")
	}
	if !samePath(workTop, workspace) || !samePath(sourceTop, sourceWorkspace) ||
		!samePath(workCommon, sourceCommon) || !strings.EqualFold(workHead, expectedBase) {
		return nil, fmt.Errorf("run worktree does not match its saved repository and base")
	}
	if err := verifyCommitExists(ctx, sourceWorkspace, expectedBase); err != nil {
		return nil, fmt.Errorf("approved base is unavailable in the saved source repository")
	}
	return &ownedWorktreeScope{workspace: workspace, source: sourceWorkspace, runID: runID, baseSHA: expectedBase}, nil
}

func verifyCommitExists(ctx context.Context, root, commit string) error {
	cmd := exec.CommandContext(ctx, "git", "-c", "safe.directory="+root, "-c", "core.excludesFile=", "cat-file", "-e", commit+"^{commit}")
	cmd.Dir = root
	cmd.Env = filteredGitEnvironment(os.Environ())
	return cmd.Run()
}

func canonicalExistingDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("not a directory")
	}
	if !samePath(filepath.Clean(abs), filepath.Clean(resolved)) {
		return "", errors.New("path traverses a link")
	}
	return filepath.Clean(resolved), nil
}

func withinCanonicalRoot(root, path string) bool {
	root, err := canonicalExistingDirectory(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func samePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func validCommitID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func inspectRepository(ctx context.Context, root string) (top, common, head string, err error) {
	read := func(args ...string) (string, error) {
		commandArgs := []string{"-c", "safe.directory=" + root, "-c", "core.excludesFile="}
		commandArgs = append(commandArgs, args...)
		cmd := exec.CommandContext(ctx, "git", commandArgs...)
		cmd.Dir = root
		cmd.Env = filteredGitEnvironment(os.Environ())
		out, runErr := cmd.Output()
		if runErr != nil {
			return "", runErr
		}
		return strings.TrimSpace(string(out)), nil
	}
	top, err = read("rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", "", err
	}
	common, err = read("rev-parse", "--git-common-dir")
	if err != nil {
		return "", "", "", err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return "", "", "", err
	}
	head, err = read("rev-parse", "HEAD")
	return filepath.Clean(top), filepath.Clean(common), strings.TrimSpace(head), err
}

func ownedScopeForCommand(ctx context.Context, dir string) (*ownedWorktreeScope, error) {
	scope, _ := ctx.Value(ownedWorktreeContextKey{}).(*ownedWorktreeScope)
	if scope == nil {
		return nil, nil
	}
	canonical, err := canonicalExistingDirectory(dir)
	if err != nil || !samePath(canonical, scope.workspace) {
		return nil, fmt.Errorf("verified run workspace context used with a different directory")
	}
	verified, err := verifyRunWorktree(ctx, scope.workspace, scope.source, scope.runID, scope.baseSHA)
	if err != nil {
		return nil, err
	}
	return verified, nil
}

func hasRepositoryOverride(args []string) bool {
	for _, arg := range args {
		lower := strings.ToLower(arg)
		if lower == "-c" || lower == "-C" || lower == "--git-dir" || lower == "--work-tree" ||
			lower == "--config-env" || lower == "--super-prefix" ||
			strings.HasPrefix(lower, "--git-dir=") || strings.HasPrefix(lower, "--work-tree=") ||
			strings.HasPrefix(lower, "--config-env=") || strings.HasPrefix(lower, "--super-prefix=") {
			return true
		}
	}
	return false
}

func filteredGitEnvironment(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if upper == "GIT_DIR" || upper == "GIT_WORK_TREE" || upper == "GIT_COMMON_DIR" ||
			upper == "GIT_INDEX_FILE" || upper == "GIT_OBJECT_DIRECTORY" || upper == "GIT_ALTERNATE_OBJECT_DIRECTORIES" ||
			upper == "GIT_NAMESPACE" || upper == "GIT_PREFIX" || upper == "GIT_CONFIG_PARAMETERS" ||
			upper == "GIT_CONFIG_COUNT" || strings.HasPrefix(upper, "GIT_CONFIG_KEY_") || strings.HasPrefix(upper, "GIT_CONFIG_VALUE_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
