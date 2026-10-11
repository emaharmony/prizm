package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// PatchPlan is the immutable result of validating a unified diff against an
// exact repository state. ExpectedTree identifies the complete tree produced
// by applying Patch at BaseSHA, without mutating the real index or worktree.
type PatchPlan struct {
	BaseSHA      string
	BaseTree     string
	ExpectedTree string
	PatchSHA256  string
	Paths        []string
}

const (
	MaxPatchBytes = 1024 * 1024
	MaxPatchPaths = 256
)

// PlanPatch validates a unified diff without changing the repository.
func PlanPatch(ctx context.Context, root, patch, expectedBase string) (PatchPlan, error) {
	if strings.TrimSpace(patch) == "" {
		return PatchPlan{}, fmt.Errorf("patch is empty")
	}
	if len(patch) > MaxPatchBytes {
		return PatchPlan{}, fmt.Errorf("patch size %d exceeds maximum %d bytes", len(patch), MaxPatchBytes)
	}
	base, err := CurrentSHA(ctx, root)
	if err != nil {
		return PatchPlan{}, fmt.Errorf("resolve patch base: %w", err)
	}
	if expectedBase = strings.TrimSpace(expectedBase); expectedBase == "" || base != expectedBase {
		return PatchPlan{}, fmt.Errorf("patch base mismatch: workspace is %s, proposal requires %s", base, expectedBase)
	}
	if err := EnsureClean(ctx, root); err != nil {
		return PatchPlan{}, fmt.Errorf("validate patch workspace: %w", err)
	}
	paths, err := patchPaths(ctx, root, patch)
	if err != nil {
		return PatchPlan{}, err
	}
	if len(paths) == 0 {
		return PatchPlan{}, fmt.Errorf("patch has no changed paths")
	}
	if len(paths) > MaxPatchPaths {
		return PatchPlan{}, fmt.Errorf("patch changes %d paths; maximum is %d", len(paths), MaxPatchPaths)
	}
	for _, path := range paths {
		if err := validatePatchPathParents(root, path); err != nil {
			return PatchPlan{}, err
		}
	}
	if _, err := RunCommand(ctx, root, patch, "git", "apply", "--check", "--index", "--binary", "--recount", "--whitespace=nowarn", "-"); err != nil {
		return PatchPlan{}, fmt.Errorf("patch precheck failed: %w", err)
	}
	expectedTree, err := treeWithPatch(ctx, root, base, patch)
	if err != nil {
		return PatchPlan{}, fmt.Errorf("compute expected patch tree: %w", err)
	}
	baseTree, err := revisionTree(ctx, root, base)
	if err != nil {
		return PatchPlan{}, err
	}
	if expectedTree == baseTree {
		return PatchPlan{}, fmt.Errorf("patch produces no tree change")
	}
	for _, path := range paths {
		for _, tree := range []string{baseTree, expectedTree} {
			mode, modeErr := treePathMode(ctx, root, tree, path)
			if modeErr != nil {
				return PatchPlan{}, modeErr
			}
			if mode != "" && mode != "100644" && mode != "100755" {
				return PatchPlan{}, fmt.Errorf("patch path %q has unsupported git mode %s", path, mode)
			}
		}
	}
	sum := sha256.Sum256([]byte(patch))
	return PatchPlan{BaseSHA: base, BaseTree: baseTree, ExpectedTree: expectedTree, PatchSHA256: hex.EncodeToString(sum[:]), Paths: paths}, nil
}

// ApplyPlannedPatch revalidates and atomically applies a previously planned
// patch. git apply is invoked without --reject so a failing hunk cannot leave a
// partial result. The resulting full tree must match the persisted plan.
func ApplyPlannedPatch(ctx context.Context, root, patch string, plan PatchPlan) error {
	replanned, err := PlanPatch(ctx, root, patch, plan.BaseSHA)
	if err != nil {
		return err
	}
	if replanned.BaseTree != plan.BaseTree || replanned.ExpectedTree != plan.ExpectedTree || replanned.PatchSHA256 != plan.PatchSHA256 || !equalStrings(replanned.Paths, plan.Paths) {
		return fmt.Errorf("patch plan does not match persisted proposal")
	}
	if _, err := RunCommand(ctx, root, patch, "git", "apply", "--binary", "--recount", "--whitespace=nowarn", "-"); err != nil {
		return fmt.Errorf("apply patch: %w", err)
	}
	actual, treeErr := WorktreeTree(ctx, root)
	if treeErr == nil && actual == plan.ExpectedTree {
		return nil
	}
	// A post-apply mismatch is fail-closed. Reverse the exact patch and verify
	// the baseline so the caller never observes a partially accepted mutation.
	_, reverseErr := RunCommand(ctx, root, patch, "git", "apply", "--reverse", "--binary", "--recount", "--whitespace=nowarn", "-")
	rolledBack, rollbackTreeErr := WorktreeTree(ctx, root)
	baseTree, baseTreeErr := revisionTree(ctx, root, plan.BaseSHA)
	if reverseErr != nil || rollbackTreeErr != nil || baseTreeErr != nil || rolledBack != baseTree {
		return fmt.Errorf("applied patch tree mismatch and rollback could not be verified")
	}
	if treeErr != nil {
		return fmt.Errorf("verify applied patch tree: %w", treeErr)
	}
	return fmt.Errorf("applied patch tree mismatch")
}

// WorktreeTree returns the tree object for the current worktree, including
// tracked modifications and untracked files, without changing the real index.
func WorktreeTree(ctx context.Context, root string) (string, error) {
	index, cleanup, err := temporaryIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()
	if _, err := runGitWithIndex(ctx, root, index, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := runGitWithIndex(ctx, root, index, "add", "--all", "--", "."); err != nil {
		return "", err
	}
	out, err := runGitWithIndex(ctx, root, index, "write-tree")
	return strings.TrimSpace(out), err
}

// CheckPatchDirection checks whether the exact patch applies in the requested
// direction without mutating the worktree.
func CheckPatchDirection(ctx context.Context, root, patch string, reverse bool) bool {
	args := []string{"apply", "--check", "--binary", "--recount", "--whitespace=nowarn"}
	if reverse {
		args = append(args, "--reverse")
	}
	args = append(args, "-")
	_, err := RunCommand(ctx, root, patch, "git", args...)
	return err == nil
}

func treeWithPatch(ctx context.Context, root, base, patch string) (string, error) {
	index, cleanup, err := temporaryIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()
	if _, err := runGitWithIndex(ctx, root, index, "read-tree", base); err != nil {
		return "", err
	}
	if _, err := runGitWithIndexInput(ctx, root, index, patch, "apply", "--cached", "--binary", "--recount", "--whitespace=nowarn", "-"); err != nil {
		return "", err
	}
	out, err := runGitWithIndex(ctx, root, index, "write-tree")
	return strings.TrimSpace(out), err
}

func revisionTree(ctx context.Context, root, revision string) (string, error) {
	out, err := RunCommand(ctx, root, "", "git", "rev-parse", revision+"^{tree}")
	return strings.TrimSpace(out), err
}

func treePathMode(ctx context.Context, root, tree, path string) (string, error) {
	out, err := RunCommand(ctx, root, "", "git", "ls-tree", tree, "--", path)
	if err != nil {
		return "", fmt.Errorf("inspect patch path %q: %w", path, err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", nil
	}
	fields := strings.Fields(out)
	if len(fields) < 3 {
		return "", fmt.Errorf("inspect patch path %q: malformed tree entry", path)
	}
	return fields[0], nil
}

func patchPaths(ctx context.Context, root, patch string) ([]string, error) {
	out, err := RunCommand(ctx, root, patch, "git", "apply", "--numstat", "-z", "--binary", "--recount", "-")
	if err != nil {
		return nil, fmt.Errorf("inspect patch paths: %w", err)
	}
	parts := strings.Split(out, "\x00")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		fields := strings.Split(part, "\t")
		if len(fields) != 3 || strings.TrimSpace(fields[2]) == "" {
			return nil, fmt.Errorf("patch contains an unsupported rename or path encoding")
		}
		if strings.IndexFunc(fields[2], func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return nil, fmt.Errorf("patch path contains control characters")
		}
		path := filepath.Clean(filepath.FromSlash(fields[2]))
		first := strings.Split(path, string(filepath.Separator))[0]
		if filepath.IsAbs(path) || path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) || strings.EqualFold(first, ".git") {
			return nil, fmt.Errorf("patch path %q is outside the workspace", fields[2])
		}
		absolute := filepath.Join(root, path)
		relative, relErr := filepath.Rel(root, absolute)
		if relErr != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("patch path %q is outside the workspace", fields[2])
		}
		paths = append(paths, filepath.ToSlash(path))
	}
	sort.Strings(paths)
	paths = compactStrings(paths)
	return paths, nil
}

func validatePatchPathParents(root, path string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve patch root: %w", err)
	}
	absPath := filepath.Join(absRoot, filepath.FromSlash(path))
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("patch path %q is outside the workspace", path)
	}
	current := absRoot
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("inspect patch path %q: %w", path, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("patch path %q traverses a symlink", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("patch path %q has a non-directory parent", path)
		}
	}
	return nil
}

func temporaryIndex() (string, func(), error) {
	f, err := os.CreateTemp("", "prizm-patch-index-*")
	if err != nil {
		return "", nil, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return "", nil, err
	}
	if err := os.Remove(path); err != nil {
		return "", nil, err
	}
	return path, func() { _ = os.Remove(path) }, nil
}

func runGitWithIndex(ctx context.Context, root, index string, args ...string) (string, error) {
	return runGitWithIndexInput(ctx, root, index, "", args...)
}

func runGitWithIndexInput(ctx context.Context, root, index, stdin string, args ...string) (string, error) {
	args = append([]string{"-c", "core.excludesFile="}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}
