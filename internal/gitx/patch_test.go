package gitx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestPlanPatchRejectsUnsafeOrStaleInput(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	base, _ := CurrentSHA(ctx, root)
	tests := []struct{ name, patch, base string }{
		{"malformed", "not a patch", base},
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
