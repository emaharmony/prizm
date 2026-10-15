package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeTreeReportsBoundedGitDirectoryFailureStage(t *testing.T) {
	root := t.TempDir()
	initRepo := exec.Command("git", "init", "--quiet", root)
	if output, err := initRepo.CombinedOutput(); err != nil {
		t.Fatalf("initialize empty repository: %v (%d output bytes)", err, len(output))
	}
	_, err := WorktreeTree(context.Background(), root)
	var inspection *WorktreeInspectionError
	if !errors.As(err, &inspection) {
		t.Fatalf("error type=%T, want WorktreeInspectionError", err)
	}
	if inspection.Stage != InspectReadTree || inspection.Category != InspectCommandFailed || inspection.ExitCode == 0 {
		t.Fatalf("inspection=%#v", inspection)
	}
	if strings.Contains(err.Error(), root) {
		t.Fatal("public error leaked the inspected path")
	}
}

func TestWorktreeTreeReportsMissingGitWithoutEchoingExecutablePath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := WorktreeTree(context.Background(), t.TempDir())
	var inspection *WorktreeInspectionError
	if !errors.As(err, &inspection) || inspection.Stage != InspectResolveGitDir || inspection.Category != InspectUnavailable {
		t.Fatalf("inspection=%#v error=%v", inspection, err)
	}
	if strings.Contains(err.Error(), "git.exe") || strings.Contains(err.Error(), "PATH") {
		t.Fatalf("missing executable diagnostic leaked host details: %v", err)
	}
}

func TestPatchSourceRangesTreatsRemovedHeaderLikeLineAsHunkContent(t *testing.T) {
	patch := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n" +
		"@@ -1 +1 @@\n--- a/covered.txt\n+replacement\n" +
		"@@ -10 +10 @@\n-old\n+new\n"

	ranges, err := PatchSourceRanges(patch)
	if err != nil {
		t.Fatal(err)
	}
	want := []PatchSourceRange{
		{Path: "README.md", StartLine: 1, LineCount: 1},
		{Path: "README.md", StartLine: 10, LineCount: 1},
	}
	if len(ranges) != len(want) {
		t.Fatalf("ranges=%+v, want %+v", ranges, want)
	}
	for i := range want {
		if ranges[i] != want[i] {
			t.Fatalf("ranges=%+v, want %+v", ranges, want)
		}
	}
}

func TestPatchSourceRangesHandlesAddedAndDeletedFiles(t *testing.T) {
	patch := "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n" +
		"@@ -0,0 +1 @@\n+new\n" +
		"diff --git a/deleted.txt b/deleted.txt\ndeleted file mode 100644\n--- a/deleted.txt\n+++ /dev/null\n" +
		"@@ -1 +0,0 @@\n-old\n"

	ranges, err := PatchSourceRanges(patch)
	if err != nil {
		t.Fatal(err)
	}
	want := []PatchSourceRange{{Path: "deleted.txt", StartLine: 1, LineCount: 1}}
	if len(ranges) != len(want) || ranges[0] != want[0] {
		t.Fatalf("ranges=%+v, want %+v", ranges, want)
	}
}

func TestPlanAndApplyPatchAtomicallyChangesMultipleFiles(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "doomed.txt"), []byte("doomed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCommand(ctx, root, "", "git", "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCommand(ctx, root, "", "git", "commit", "-m", "fixture"); err != nil {
		t.Fatal(err)
	}
	base, _ := CurrentSHA(ctx, root)
	patch := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-hello\n+changed\n" +
		"diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n" +
		"diff --git a/doomed.txt b/doomed.txt\ndeleted file mode 100644\n--- a/doomed.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-doomed\n"
	plan, err := PlanPatch(ctx, root, patch, base)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.Paths, ","); got != "README.md,doomed.txt,new.txt" {
		t.Fatalf("paths=%q", got)
	}
	if err := EnsureClean(ctx, root); err != nil {
		t.Fatalf("proposal mutated repository: %v", err)
	}
	if err := ApplyPlannedPatch(ctx, root, patch, plan); err != nil {
		t.Fatal(err)
	}
	tree, err := WorktreeTree(ctx, root)
	if err != nil || tree != plan.ExpectedTree {
		t.Fatalf("tree=%q expected=%q err=%v", tree, plan.ExpectedTree, err)
	}
	if _, err := os.Stat(filepath.Join(root, "doomed.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file still present: %v", err)
	}
}

func TestPlanPatchRejectsSecondPatchAfterFirstAppliedInSameWorktree(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	base, err := CurrentSHA(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	first := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-hello\n+approved change\n"
	firstPlan, err := PlanPatch(ctx, root, first, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyPlannedPatch(ctx, root, first, firstPlan); err != nil {
		t.Fatal(err)
	}
	second := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-approved change\n+approved correction\n"
	if _, err := PlanPatch(ctx, root, second, base); !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("second plan error=%v, want existing clean-worktree rejection", err)
	}
}

func TestPlanAndApplyPatchRecountsIncorrectHunkCounts(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	base, _ := CurrentSHA(ctx, root)
	patch := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1,99 +1,99 @@\n-hello\n+changed\n" +
		"diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1,99 @@\n+new\n"
	plan, err := PlanPatch(ctx, root, patch, base)
	if err != nil {
		t.Fatalf("plan recounted patch: %v", err)
	}
	sum := sha256.Sum256([]byte(patch))
	if plan.PatchSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("patch hash=%q, want exact input hash", plan.PatchSHA256)
	}
	if got := strings.Join(plan.Paths, ","); got != "README.md,new.txt" {
		t.Fatalf("paths=%q", got)
	}
	if !CheckPatchDirection(ctx, root, patch, false) {
		t.Fatal("forward direction was not detected before apply")
	}
	if err := ApplyPlannedPatch(ctx, root, patch, plan); err != nil {
		t.Fatalf("apply recounted patch: %v", err)
	}
	actualTree, err := WorktreeTree(ctx, root)
	if err != nil || actualTree != plan.ExpectedTree {
		t.Fatalf("tree=%q want=%q err=%v", actualTree, plan.ExpectedTree, err)
	}
	if !CheckPatchDirection(ctx, root, patch, true) {
		t.Fatal("reverse direction was not detected after apply")
	}
}

func TestPatchPlanningAndWorktreeTreeUseLinkedGitMetadataWhenOSTempIsUnavailable(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	linked := filepath.Join(t.TempDir(), "linked-worktree")
	if _, err := RunCommand(ctx, root, "", "git", "worktree", "add", "--detach", linked, "HEAD"); err != nil {
		t.Fatalf("create linked worktree: %v", err)
	}
	base, err := CurrentSHA(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := RunCommand(ctx, linked, "", "git", "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(strings.TrimSpace(gitDir), "index")
	indexBefore, err := fileSHA256(indexPath)
	if err != nil {
		t.Fatalf("hash linked worktree index: %v", err)
	}
	blockedTemp := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedTemp, []byte("block temp directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, blockedTemp)
	}

	patch := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-hello\n+changed\n"
	plan, err := PlanPatch(ctx, linked, patch, base)
	if err != nil {
		t.Fatalf("plan patch with unavailable OS temp: %v", err)
	}
	tree, err := WorktreeTree(ctx, linked)
	if err != nil {
		t.Fatalf("read linked worktree tree with unavailable OS temp: %v", err)
	}
	if tree != plan.BaseTree {
		t.Fatalf("planning changed worktree tree: got %s want base %s", tree, plan.BaseTree)
	}
	if err := EnsureClean(ctx, linked); err != nil {
		t.Fatalf("planning changed linked worktree status: %v", err)
	}
	indexAfter, err := fileSHA256(indexPath)
	if err != nil {
		t.Fatalf("hash linked worktree index after planning: %v", err)
	}
	if indexAfter != indexBefore {
		t.Fatalf("planning modified linked worktree's real index: before=%s after=%s", indexBefore, indexAfter)
	}
	leftovers, err := filepath.Glob(filepath.Join(strings.TrimSpace(gitDir), "prizm-patch-index-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary metadata indexes were not cleaned up: %v", leftovers)
	}
}

func TestTrustedTreeBasisAllowsApprovedSerialPatchAndRejectsForeignMutation(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	base, err := CurrentSHA(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	patchA := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-hello\n+changed\n"
	planA, err := PlanPatch(ctx, root, patchA, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyPlannedPatch(ctx, root, patchA, planA); err != nil {
		t.Fatalf("apply approved patch A: %v", err)
	}
	basis := TrustedTreeBasis{
		RunID: "run", WorkspaceID: "workspace", ProposalID: "proposal-a", ApprovalID: "approval-a",
		ApplyKey: "apply-a", ExecutionKey: "execution-a", Role: "developer", BaseSHA: planA.BaseSHA,
		BaseTree: planA.BaseTree, ExpectedTree: planA.ExpectedTree, PatchSHA256: planA.PatchSHA256, Paths: append([]string(nil), planA.Paths...),
	}
	patchB := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-changed\n+corrected\n"
	if _, err := PlanPatch(ctx, root, patchB, base); err == nil {
		t.Fatal("legacy clean-HEAD planner accepted a dirty worktree")
	}
	planB, err := PlanPatchOnTrustedTree(ctx, root, patchB, basis)
	if err != nil {
		t.Fatalf("plan approved correction B from A receipt: %v", err)
	}
	if planB.BaseTree != planA.BaseTree || planB.PlanningTree != planA.ExpectedTree || planB.Basis == nil || !EqualTrustedTreeBasis(planB.Basis, &basis) {
		t.Fatalf("correction plan did not preserve both source and planning trees: %#v", planB)
	}
	if err := ApplyPlannedPatch(ctx, root, patchB, planB); err != nil {
		t.Fatalf("apply approved correction B: %v", err)
	}
	if tree, err := WorktreeTree(ctx, root); err != nil || tree != planB.ExpectedTree {
		t.Fatalf("correction tree=%q want=%q err=%v", tree, planB.ExpectedTree, err)
	}

	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("foreign edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patchC := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-foreign edit\n+third\n"
	if _, err := PlanPatchOnTrustedTree(ctx, root, patchC, basis); err == nil {
		t.Fatal("trusted basis accepted an out-of-band worktree mutation")
	}
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func TestPlanPatchRejectsUnsafeOrStaleInput(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	base, _ := CurrentSHA(ctx, root)
	tests := []struct{ name, patch, base string }{
		{"malformed", "not a patch", base},
		{"invalid hunk body", "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n?invalid\n", base},
		{"dotgit", "diff --git a/.git/config b/.git/config\nnew file mode 100644\n--- /dev/null\n+++ b/.git/config\n@@ -0,0 +1 @@\n+x\n", base},
		{"mixed case dotgit", "diff --git a/.GIT/config b/.GIT/config\nnew file mode 100644\n--- /dev/null\n+++ b/.GIT/config\n@@ -0,0 +1 @@\n+x\n", base},
		{"symlink", "diff --git a/link b/link\nnew file mode 120000\n--- /dev/null\n+++ b/link\n@@ -0,0 +1 @@\n+outside\n", base},
		{"base drift", "diff --git a/x b/x\nnew file mode 100644\n--- /dev/null\n+++ b/x\n@@ -0,0 +1 @@\n+x\n", strings.Repeat("0", 40)},
		{"oversize", strings.Repeat("x", MaxPatchBytes+1), base},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PlanPatch(ctx, root, tc.patch, tc.base); err == nil {
				t.Fatal("unsafe patch accepted")
			}
			if err := EnsureClean(ctx, root); err != nil {
				t.Fatalf("rejection mutated repo: %v", err)
			}
		})
	}
}

func TestApplyPlannedPatchFailureLeavesBaseline(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	base, _ := CurrentSHA(ctx, root)
	patch := "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n"
	plan, err := PlanPatch(ctx, root, patch, base)
	if err != nil {
		t.Fatal(err)
	}
	plan.PatchSHA256 = strings.Repeat("0", 64)
	if err := ApplyPlannedPatch(ctx, root, patch, plan); err == nil {
		t.Fatal("tampered plan applied")
	}
	if err := EnsureClean(ctx, root); err != nil {
		t.Fatalf("failed apply changed baseline: %v", err)
	}
}
