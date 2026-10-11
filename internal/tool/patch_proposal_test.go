package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/gitx"
)

func patchProposalRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@prizm.local"}, {"config", "user.name", "Prizm Test"}} {
		if _, err := gitx.RunCommand(ctx, root, "", "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, root, "", "git", "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, root, "", "git", "commit", "-m", "base"); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestApplyPatchProposalPersistsOneExactApprovalWithoutMutation(t *testing.T) {
	root := patchProposalRepo(t)
	runs := t.TempDir()
	ctx := context.Background()
	base, _ := gitx.CurrentSHA(ctx, root)
	patch := "diff --git a/a.txt b/a.txt\nnew file mode 100644\n--- /dev/null\n+++ b/a.txt\n@@ -0,0 +1 @@\n+a\n" +
		"diff --git a/b.txt b/b.txt\nnew file mode 100644\n--- /dev/null\n+++ b/b.txt\n@@ -0,0 +1 @@\n+b\n"
	reg := NewRegistry()
	RegisterBuiltinsV4(reg, root, 1024*1024, "", root)
	cfg := PolicyConfig{WorkspaceRoot: root, AllowedPaths: []string{root}, MaxFileSize: 1024 * 1024}
	store := approval.NewStore(runs)
	exec := NewExecutor(reg, &cfg)
	exec.SetApprovalStore(store)
	result, err := exec.ExecuteWithPolicy(ctx, "apply_patch_proposal", "astraea", "prizm", "exec-1", map[string]any{"_run_id": "run-1", "patch": patch, "base_sha": base})
	if err != nil || !result.Success {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	items, err := store.List("run-1")
	if err != nil || len(items) != 1 {
		t.Fatalf("approvals=%d err=%v", len(items), err)
	}
	item := items[0]
	if item.MutationType != approval.MutationApplyPatch || item.Content != patch || item.PatchPlan == nil || len(item.PatchPlan.Paths) != 2 {
		t.Fatalf("approval=%#v", item)
	}
	if !strings.HasSuffix(item.Content, "\n") {
		t.Fatal("approval lost the patch's trailing newline")
	}
	sum := sha256.Sum256([]byte(patch))
	if item.PatchPlan.PatchSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("patch hash=%q, want exact input hash", item.PatchPlan.PatchSHA256)
	}
	if err := gitx.EnsureClean(ctx, root); err != nil {
		t.Fatalf("proposal mutated repo: %v", err)
	}
}

func TestInvalidApplyPatchProposalPersistsNoApproval(t *testing.T) {
	root := patchProposalRepo(t)
	runs := t.TempDir()
	ctx := context.Background()
	base, _ := gitx.CurrentSHA(ctx, root)
	reg := NewRegistry()
	RegisterBuiltinsV4(reg, root, 1024*1024, "", root)
	cfg := PolicyConfig{WorkspaceRoot: root, AllowedPaths: []string{root}, MaxFileSize: 1024 * 1024}
	store := approval.NewStore(runs)
	exec := NewExecutor(reg, &cfg)
	exec.SetApprovalStore(store)
	var requested bool
	exec.SetEmitter(func(eventType, _ string, _ map[string]any) {
		if eventType == "prizm.approval.file_requested" {
			requested = true
		}
	})
	result, err := exec.ExecuteWithPolicy(ctx, "apply_patch_proposal", "astraea", "prizm", "exec-1", map[string]any{"_run_id": "run-1", "patch": "malformed", "base_sha": base})
	if err != nil || result.Success {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	items, err := store.List("run-1")
	if err != nil || len(items) != 0 || requested {
		t.Fatalf("approvals=%d requested=%v err=%v", len(items), requested, err)
	}
}

func TestApplyPatchProposalEnforcesNarrowWriteRoots(t *testing.T) {
	root := patchProposalRepo(t)
	allowed := filepath.Join(root, "allowed")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allowed, ".keep"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(context.Background(), root, "", "git", "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(context.Background(), root, "", "git", "commit", "-m", "allowed root"); err != nil {
		t.Fatal(err)
	}
	base, _ := gitx.CurrentSHA(context.Background(), root)
	outside := "diff --git a/outside.txt b/outside.txt\nnew file mode 100644\n--- /dev/null\n+++ b/outside.txt\n@@ -0,0 +1 @@\n+x\n"
	inside := "diff --git a/allowed/inside.txt b/allowed/inside.txt\nnew file mode 100644\n--- /dev/null\n+++ b/allowed/inside.txt\n@@ -0,0 +1 @@\n+x\n"
	reg := NewRegistry()
	_ = reg.Register(&ApplyPatchProposal{WorkspaceRoot: root, AllowedPaths: []string{allowed}})
	cfg := PolicyConfig{WorkspaceRoot: root, WriteRoots: []string{allowed}, MaxFileSize: 1024 * 1024}
	store := approval.NewStore(t.TempDir())
	exec := NewExecutor(reg, &cfg)
	exec.SetApprovalStore(store)
	for _, tc := range []struct {
		name, patch string
		success     bool
	}{{"outside", outside, false}, {"inside", inside, true}} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := exec.ExecuteWithPolicy(context.Background(), "apply_patch_proposal", "developer", "prizm", "exec-"+tc.name, map[string]any{"_run_id": "run-" + tc.name, "patch": tc.patch, "base_sha": base})
			if err != nil || result.Success != tc.success {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			items, err := store.List("run-" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.success {
				want = 1
			}
			if len(items) != want {
				t.Fatalf("approvals=%d want=%d", len(items), want)
			}
		})
	}
	if err := gitx.EnsureClean(context.Background(), root); err != nil {
		t.Fatalf("proposal mutated repo: %v", err)
	}
}
