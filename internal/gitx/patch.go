package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// WorktreeInspectionStage and Category are fixed diagnostics for a failed
// read-only tree inspection. They intentionally omit paths and command output.
type WorktreeInspectionStage string
type WorktreeInspectionCategory string

const (
	InspectResolveGitDir WorktreeInspectionStage = "gitdir_discovery"
	InspectCreateIndex   WorktreeInspectionStage = "temp_index_create"
	InspectCloseIndex    WorktreeInspectionStage = "temp_index_close"
	InspectRemoveIndex   WorktreeInspectionStage = "temp_index_remove"
	InspectReadTree      WorktreeInspectionStage = "read_tree"
	InspectAddWorktree   WorktreeInspectionStage = "stage_worktree"
	InspectWriteTree     WorktreeInspectionStage = "write_tree"

	InspectCancelled     WorktreeInspectionCategory = "cancelled"
	InspectPermission    WorktreeInspectionCategory = "permission"
	InspectUnavailable   WorktreeInspectionCategory = "unavailable"
	InspectCommandFailed WorktreeInspectionCategory = "command_failed"
	InspectIOFailed      WorktreeInspectionCategory = "io_failed"
)

type WorktreeInspectionError struct {
	Stage    WorktreeInspectionStage
	Category WorktreeInspectionCategory
	ExitCode int
	cause    error
}

func (e *WorktreeInspectionError) Error() string { return "git worktree inspection failed" }
func (e *WorktreeInspectionError) Unwrap() error { return e.cause }

func worktreeInspectionError(ctx context.Context, stage WorktreeInspectionStage, err error) error {
	if err == nil {
		return nil
	}
	category := InspectIOFailed
	exitCode := 0
	switch {
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(ctx.Err(), context.DeadlineExceeded):
		category = InspectCancelled
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		category = InspectPermission
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		category = InspectUnavailable
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			category = InspectCommandFailed
			exitCode = exitErr.ExitCode()
		} else {
			var pathErr *os.PathError
			if errors.As(err, &pathErr) && errors.Is(pathErr.Err, fs.ErrNotExist) {
				category = InspectUnavailable
			}
		}
	}
	return &WorktreeInspectionError{Stage: stage, Category: category, ExitCode: exitCode, cause: err}
}

// PatchPlan is the immutable result of validating a unified diff against an
// exact repository state. ExpectedTree identifies the complete tree produced
// by applying Patch at BaseSHA, without mutating the real index or worktree.
type PatchPlan struct {
	BaseSHA      string            `json:"base_sha"`
	BaseTree     string            `json:"base_tree"`
	PlanningTree string            `json:"planning_tree"`
	ExpectedTree string            `json:"expected_tree"`
	PatchSHA256  string            `json:"patch_sha256"`
	Paths        []string          `json:"paths"`
	Basis        *TrustedTreeBasis `json:"basis,omitempty"`
}

// TrustedTreeBasis identifies a prior, host-verified applied proposal in the
// same run. It is supplied by workflow state, never by patch input.
type TrustedTreeBasis struct {
	RunID        string   `json:"run_id"`
	WorkspaceID  string   `json:"workspace_id"`
	ProposalID   string   `json:"proposal_id"`
	ApprovalID   string   `json:"approval_id"`
	ApplyKey     string   `json:"apply_key"`
	ExecutionKey string   `json:"execution_key"`
	Role         string   `json:"role"`
	BaseSHA      string   `json:"base_sha"`
	BaseTree     string   `json:"base_tree"`
	ExpectedTree string   `json:"expected_tree"`
	PatchSHA256  string   `json:"patch_sha256"`
	Paths        []string `json:"paths"`
}

// PatchSourceRange is an old-side source range a unified-diff hunk relies on.
// New files have no old-side ranges and therefore produce no entries.
type PatchSourceRange struct {
	Path      string
	StartLine int
	LineCount int
}

var unifiedHunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+[0-9]+(?:,[0-9]+)? @@`)

// PatchSourceRanges parses the old-side line ranges that must be grounded
// before a governed patch proposal may be persisted. It intentionally accepts
// only ordinary git unified-diff file headers; unsupported encodings fail
// closed and remain subject to PlanPatch's stronger Git validation.
func PatchSourceRanges(patch string) ([]PatchSourceRange, error) {
	var current string
	var ranges []PatchSourceRange
	var inHunk bool
	var hunkStart, hunkOldLines int
	lines := strings.Split(patch, "\n")
	flushHunk := func() {
		if inHunk && current != "" && current != "/dev/null" && hunkOldLines > 0 {
			ranges = append(ranges, PatchSourceRange{Path: current, StartLine: hunkStart, LineCount: hunkOldLines})
		}
		inHunk = false
		hunkOldLines = 0
	}
	for index, line := range lines {
		if inHunk {
			if line == "" && index == len(lines)-1 {
				flushHunk()
				break
			}
			// Git file markers and the next hunk header delimit a hunk. In
			// particular, a removed source line such as "-- README.md" is
			// represented by "--- README.md" and must remain hunk content.
			if strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "@@ ") {
				flushHunk()
			} else if line == "\\ No newline at end of file" {
				continue
			} else if len(line) > 0 && (line[0] == ' ' || line[0] == '+' || line[0] == '-') {
				if line[0] == ' ' || line[0] == '-' {
					hunkOldLines++
				}
				continue
			} else {
				return nil, fmt.Errorf("patch contains malformed hunk body")
			}
		}
		switch {
		case strings.HasPrefix(line, "--- "):
			path, err := patchHeaderPath(strings.TrimPrefix(line, "--- "))
			if err != nil {
				return nil, err
			}
			current = path
		case strings.HasPrefix(line, "@@ "):
			match := unifiedHunkHeader.FindStringSubmatch(line)
			if match == nil {
				return nil, fmt.Errorf("patch contains malformed hunk header")
			}
			start, _ := strconv.Atoi(match[1])
			inHunk = true
			hunkStart = start
			hunkOldLines = 0
		}
	}
	flushHunk()
	return ranges, nil
}

func patchHeaderPath(value string) (string, error) {
	value = strings.TrimSpace(strings.SplitN(value, "\t", 2)[0])
	if value == "/dev/null" {
		return value, nil
	}
	if strings.HasPrefix(value, `"`) || value == "" {
		return "", fmt.Errorf("patch contains an unsupported source path encoding")
	}
	value = strings.TrimPrefix(value, "a/")
	path := filepath.Clean(filepath.FromSlash(value))
	first := strings.Split(path, string(filepath.Separator))[0]
	if filepath.IsAbs(path) || path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) || strings.EqualFold(first, ".git") {
		return "", fmt.Errorf("patch source path %q is outside the workspace", value)
	}
	return filepath.ToSlash(path), nil
}

const (
	MaxPatchBytes = 1024 * 1024
	MaxPatchPaths = 256
)

// PlanPatch validates a unified diff without changing the repository.
func PlanPatch(ctx context.Context, root, patch, expectedBase string) (PatchPlan, error) {
	return planPatch(ctx, root, patch, expectedBase, nil)
}

// PlanPatchOnTrustedTree validates a correction against a host-verified dirty
// worktree produced by an earlier approved patch. Legacy callers retain the
// strict clean-worktree behavior of PlanPatch.
func PlanPatchOnTrustedTree(ctx context.Context, root, patch string, basis TrustedTreeBasis) (PatchPlan, error) {
	return planPatch(ctx, root, patch, basis.BaseSHA, &basis)
}

type trustedTreeBasisContextKey struct{}

// ContextWithTrustedTreeBasis carries host-derived patch provenance to the
// apply-patch proposal tool. The basis is not a tool argument or model claim.
func ContextWithTrustedTreeBasis(ctx context.Context, basis TrustedTreeBasis) context.Context {
	return context.WithValue(ctx, trustedTreeBasisContextKey{}, cloneTrustedTreeBasis(&basis))
}

// TrustedTreeBasisFromContext returns a defensive copy of host-injected patch
// provenance, if the current role visit has an approved applied predecessor.
func TrustedTreeBasisFromContext(ctx context.Context) (TrustedTreeBasis, bool) {
	basis, ok := ctx.Value(trustedTreeBasisContextKey{}).(*TrustedTreeBasis)
	if !ok || basis == nil {
		return TrustedTreeBasis{}, false
	}
	return *cloneTrustedTreeBasis(basis), true
}

func validTrustedTreeBasis(basis TrustedTreeBasis) bool {
	if strings.TrimSpace(basis.RunID) == "" || strings.TrimSpace(basis.WorkspaceID) == "" ||
		strings.TrimSpace(basis.ProposalID) == "" || strings.TrimSpace(basis.ApprovalID) == "" ||
		strings.TrimSpace(basis.ApplyKey) == "" || strings.TrimSpace(basis.ExecutionKey) == "" ||
		strings.TrimSpace(basis.Role) == "" || basis.BaseSHA == "" || basis.BaseTree == "" ||
		basis.ExpectedTree == "" || len(basis.PatchSHA256) != 64 || len(basis.Paths) == 0 {
		return false
	}
	for i, path := range basis.Paths {
		if strings.TrimSpace(path) == "" || filepath.IsAbs(filepath.FromSlash(path)) ||
			filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) != path ||
			(i > 0 && basis.Paths[i-1] >= path) {
			return false
		}
	}
	return true
}

func cloneTrustedTreeBasis(basis *TrustedTreeBasis) *TrustedTreeBasis {
	if basis == nil {
		return nil
	}
	cloned := *basis
	cloned.Paths = append([]string(nil), basis.Paths...)
	return &cloned
}

func equalTrustedTreeBasis(left, right *TrustedTreeBasis) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.RunID == right.RunID && left.WorkspaceID == right.WorkspaceID &&
		left.ProposalID == right.ProposalID && left.ApprovalID == right.ApprovalID &&
		left.ApplyKey == right.ApplyKey && left.ExecutionKey == right.ExecutionKey && left.Role == right.Role &&
		left.BaseSHA == right.BaseSHA && left.BaseTree == right.BaseTree && left.ExpectedTree == right.ExpectedTree &&
		left.PatchSHA256 == right.PatchSHA256 && equalStrings(left.Paths, right.Paths)
}

// EqualTrustedTreeBasis compares the complete receipt bound into a patch plan.
func EqualTrustedTreeBasis(left, right *TrustedTreeBasis) bool {
	return equalTrustedTreeBasis(left, right)
}

// ClonePatchPlan returns a defensive copy suitable for persistence or request
// transport.
func ClonePatchPlan(plan PatchPlan) PatchPlan {
	plan.Paths = append([]string(nil), plan.Paths...)
	plan.Basis = cloneTrustedTreeBasis(plan.Basis)
	return plan
}

// EqualPatchPlan compares every approval-bound field in two patch plans.
func EqualPatchPlan(left, right PatchPlan) bool {
	return left.BaseSHA == right.BaseSHA && left.BaseTree == right.BaseTree &&
		left.PlanningTree == right.PlanningTree && left.ExpectedTree == right.ExpectedTree &&
		left.PatchSHA256 == right.PatchSHA256 && equalStrings(left.Paths, right.Paths) &&
		equalTrustedTreeBasis(left.Basis, right.Basis)
}

func planPatch(ctx context.Context, root, patch, expectedBase string, basis *TrustedTreeBasis) (PatchPlan, error) {
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
	baseTree, err := revisionTree(ctx, root, base)
	if err != nil {
		return PatchPlan{}, err
	}
	planningTree := baseTree
	if basis == nil {
		if err := EnsureClean(ctx, root); err != nil {
			return PatchPlan{}, fmt.Errorf("validate patch workspace: %w", err)
		}
	} else {
		if !validTrustedTreeBasis(*basis) || basis.BaseSHA != base || basis.BaseTree != baseTree {
			return PatchPlan{}, fmt.Errorf("trusted patch basis does not match the approved repository base")
		}
		observedTree, treeErr := WorktreeTree(ctx, root)
		if treeErr != nil {
			return PatchPlan{}, fmt.Errorf("inspect trusted patch basis: %w", treeErr)
		}
		if observedTree != basis.ExpectedTree {
			return PatchPlan{}, fmt.Errorf("worktree does not match trusted patch basis")
		}
		planningTree = basis.ExpectedTree
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
	checkArgs := []string{"apply", "--check", "--binary", "--whitespace=nowarn", "-"}
	if basis == nil {
		checkArgs = []string{"apply", "--check", "--index", "--binary", "--whitespace=nowarn", "-"}
	}
	if _, err := RunCommand(ctx, root, patch, "git", checkArgs...); err != nil {
		return PatchPlan{}, fmt.Errorf("patch precheck failed: %w", err)
	}
	expectedTree, err := treeWithPatch(ctx, root, planningTree, patch)
	if err != nil {
		return PatchPlan{}, fmt.Errorf("compute expected patch tree: %w", err)
	}
	if expectedTree == planningTree {
		return PatchPlan{}, fmt.Errorf("patch produces no tree change")
	}
	for _, path := range paths {
		for _, tree := range []string{planningTree, expectedTree} {
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
	return PatchPlan{BaseSHA: base, BaseTree: baseTree, PlanningTree: planningTree, ExpectedTree: expectedTree, PatchSHA256: hex.EncodeToString(sum[:]), Paths: paths, Basis: cloneTrustedTreeBasis(basis)}, nil
}

// ApplyPlannedPatch revalidates and atomically applies a previously planned
// patch. git apply is invoked without --reject so a failing hunk cannot leave a
// partial result. The resulting full tree must match the persisted plan.
func ApplyPlannedPatch(ctx context.Context, root, patch string, plan PatchPlan) error {
	var replanned PatchPlan
	var err error
	if plan.Basis == nil {
		replanned, err = PlanPatch(ctx, root, patch, plan.BaseSHA)
	} else {
		replanned, err = PlanPatchOnTrustedTree(ctx, root, patch, *plan.Basis)
	}
	if err != nil {
		return err
	}
	if replanned.BaseTree != plan.BaseTree || replanned.PlanningTree != plan.PlanningTree || replanned.ExpectedTree != plan.ExpectedTree || replanned.PatchSHA256 != plan.PatchSHA256 || !equalStrings(replanned.Paths, plan.Paths) || !equalTrustedTreeBasis(replanned.Basis, plan.Basis) {
		return fmt.Errorf("patch plan does not match persisted proposal")
	}
	if _, err := RunCommand(ctx, root, patch, "git", "apply", "--binary", "--whitespace=nowarn", "-"); err != nil {
		return fmt.Errorf("apply patch: %w", err)
	}
	actual, treeErr := WorktreeTree(ctx, root)
	if treeErr == nil && actual == plan.ExpectedTree {
		return nil
	}
	// A post-apply mismatch is fail-closed. Reverse the exact patch and verify
	// the baseline so the caller never observes a partially accepted mutation.
	_, reverseErr := RunCommand(ctx, root, patch, "git", "apply", "--reverse", "--binary", "--whitespace=nowarn", "-")
	rolledBack, rollbackTreeErr := WorktreeTree(ctx, root)
	if reverseErr != nil || rollbackTreeErr != nil || rolledBack != plan.PlanningTree {
		return fmt.Errorf("applied patch tree mismatch and rollback could not be verified")
	}
	_ = treeErr
	return fmt.Errorf("applied patch tree mismatch")
}

// WorktreeTree returns the tree object for the current worktree, including
// tracked modifications and untracked files, without changing the real index.
func WorktreeTree(ctx context.Context, root string) (string, error) {
	index, cleanup, err := temporaryIndex(ctx, root)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if _, err := runGitWithIndex(ctx, root, index, "read-tree", "HEAD"); err != nil {
		return "", worktreeInspectionError(ctx, InspectReadTree, err)
	}
	if _, err := runGitWithIndex(ctx, root, index, "add", "--all", "--", "."); err != nil {
		return "", worktreeInspectionError(ctx, InspectAddWorktree, err)
	}
	out, err := runGitWithIndex(ctx, root, index, "write-tree")
	if err != nil {
		return "", worktreeInspectionError(ctx, InspectWriteTree, err)
	}
	return strings.TrimSpace(out), nil
}

// CheckPatchDirection checks whether the exact patch applies in the requested
// direction without mutating the worktree.
func CheckPatchDirection(ctx context.Context, root, patch string, reverse bool) bool {
	args := []string{"apply", "--check", "--binary", "--whitespace=nowarn"}
	if reverse {
		args = append(args, "--reverse")
	}
	args = append(args, "-")
	_, err := RunCommand(ctx, root, patch, "git", args...)
	return err == nil
}

func treeWithPatch(ctx context.Context, root, base, patch string) (string, error) {
	index, cleanup, err := temporaryIndex(ctx, root)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if _, err := runGitWithIndex(ctx, root, index, "read-tree", base); err != nil {
		return "", err
	}
	if _, err := runGitWithIndexInput(ctx, root, index, patch, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
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
	out, err := RunCommand(ctx, root, patch, "git", "apply", "--numstat", "-z", "--binary", "-")
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

func temporaryIndex(ctx context.Context, root string) (string, func(), error) {
	gitDir, err := RunCommand(ctx, root, "", "git", "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", nil, worktreeInspectionError(ctx, InspectResolveGitDir, err)
	}
	gitDir = strings.TrimSpace(gitDir)
	if gitDir == "" {
		return "", nil, worktreeInspectionError(ctx, InspectResolveGitDir, errors.New("empty git directory"))
	}
	f, err := os.CreateTemp(gitDir, "prizm-patch-index-*")
	if err != nil {
		return "", nil, worktreeInspectionError(ctx, InspectCreateIndex, err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, worktreeInspectionError(ctx, InspectCloseIndex, err)
	}
	if err := os.Remove(path); err != nil {
		return "", nil, worktreeInspectionError(ctx, InspectRemoveIndex, err)
	}
	return path, func() { _ = os.Remove(path) }, nil
}

func runGitWithIndex(ctx context.Context, root, index string, args ...string) (string, error) {
	return runGitWithIndexInput(ctx, root, index, "", args...)
}

func runGitWithIndexInput(ctx context.Context, root, index, stdin string, args ...string) (string, error) {
	owned, err := ownedScopeForCommand(ctx, root)
	if err != nil {
		return "", err
	}
	if owned != nil && hasRepositoryOverride(args) {
		return "", fmt.Errorf("git repository override is not allowed for an owned worktree")
	}
	prefix := []string{"-c", "core.excludesFile="}
	if owned != nil {
		prefix = append([]string{"-c", "safe.directory=" + owned.workspace}, prefix...)
	}
	args = append(prefix, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	env := os.Environ()
	if owned != nil {
		env = filteredGitEnvironment(env)
	}
	cmd.Env = append(env, "GIT_INDEX_FILE="+index)
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
