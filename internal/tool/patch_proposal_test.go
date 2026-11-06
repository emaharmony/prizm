package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/gitx"
)

func patchProposalRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cleanPatchTestGitObjects(t, root)
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

func cleanPatchTestGitObjects(t *testing.T, root string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	objects := filepath.Join(root, ".git", "objects")
	t.Cleanup(func() {
		_ = filepath.WalkDir(objects, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return walkErr
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				return err
			}
			return os.Chmod(path, 0600)
		})
	})
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

func TestApplyPatchProposalHostIdempotencyPreservesDecisionAndRejectsChangedArtifact(t *testing.T) {
	root := patchProposalRepo(t)
	runs := t.TempDir()
	ctx := context.Background()
	base, err := gitx.CurrentSHA(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	patch := "diff --git a/base.txt b/base.txt\n--- a/base.txt\n+++ b/base.txt\n@@ -1 +1 @@\n-base\n+changed\n"
	plan, err := gitx.PlanPatch(ctx, root, patch, base)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.Register(&ApplyPatchProposal{WorkspaceRoot: root, AllowedPaths: []string{root}}); err != nil {
		t.Fatal(err)
	}
	cfg := PolicyConfig{WorkspaceRoot: root, AllowedPaths: []string{root}, WriteRoots: []string{root}, WriteAgents: map[string]bool{"coder": true}, MaxFileSize: 1024 * 1024}
	store := approval.NewStore(runs)
	executor := NewExecutor(reg, &cfg)
	executor.SetApprovalStore(store)
	key := strings.Repeat("a", 64)
	binding := approval.SandboxProposalBinding{TaskID: "task-1", ExecutionKey: "exec-1", AgentID: "coder", WorkspaceID: "workspace-1", WorkspacePath: root, BaseSHA: plan.BaseSHA, BaseTree: plan.BaseTree, ArtifactDigest: strings.Repeat("b", 64)}
	requestCtx := WithApprovalSandboxBinding(WithApprovalIdempotencyKey(ctx, key), binding)
	input := map[string]any{"_run_id": "run-1", "patch": patch, "base_sha": base}
	first, err := executor.ExecuteWithPolicy(requestCtx, "apply_patch_proposal", "coder", "multiagent", "exec-1", input)
	if err != nil || !first.Success || first.Output["status"] != "pending_approval" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	approvalID := first.Output["approval_id"].(string)
	item, err := store.Load("run-1", approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if item.IdempotencyKey != key || item.SandboxBinding == nil || item.SandboxBinding.ArtifactDigest != binding.ArtifactDigest {
		t.Fatalf("host binding=%#v", item.SandboxBinding)
	}
	if err := item.Approve("reviewer"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(item); err != nil {
		t.Fatal(err)
	}
	replay, err := executor.ExecuteWithPolicy(requestCtx, "apply_patch_proposal", "coder", "multiagent", "exec-1", input)
	if err != nil || !replay.Success || replay.Output["approval_id"] != approvalID || replay.Output["status"] != approval.StatusApproved {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	if items, err := store.List("run-1"); err != nil || len(items) != 1 {
		t.Fatalf("approvals=%d err=%v", len(items), err)
	}

	changedPatch := strings.Replace(patch, "+changed", "+different", 1)
	changedBinding := binding
	changedBinding.ArtifactDigest = strings.Repeat("c", 64)
	changedCtx := WithApprovalSandboxBinding(WithApprovalIdempotencyKey(ctx, key), changedBinding)
	changed, err := executor.ExecuteWithPolicy(changedCtx, "apply_patch_proposal", "coder", "multiagent", "exec-1", map[string]any{"_run_id": "run-1", "patch": changedPatch, "base_sha": base})
	if err != nil || changed.Success {
		t.Fatalf("changed artifact reused approval: result=%#v err=%v", changed, err)
	}
	after, err := store.Load("run-1", approvalID)
	if err != nil || after.Status != approval.StatusApproved {
		t.Fatalf("changed replay altered decision: %#v err=%v", after, err)
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
	if err != nil || result.Success || result.FailureEffect != FailureEffectNoEffects {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	items, err := store.List("run-1")
	if err != nil || len(items) != 0 || requested {
		t.Fatalf("approvals=%d requested=%v err=%v", len(items), requested, err)
	}
}

func TestApplyPatchProposalRejectsIncorrectHunkCountsBeforeApproval(t *testing.T) {
	root := patchProposalRepo(t)
	runs := t.TempDir()
	ctx := context.Background()
	base, err := gitx.CurrentSHA(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
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
	patch := "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n" +
		"@@ -0,0 +1,19 @@\n" + strings.Repeat("+added\n", 21)
	result, err := exec.ExecuteWithPolicy(ctx, "apply_patch_proposal", "astraea", "prizm", "exec-bad-hunk-count", map[string]any{
		"_run_id": "run-bad-hunk-count", "patch": patch, "base_sha": base,
	})
	if err != nil || result.Success || result.FailureEffect != FailureEffectNoEffects {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	items, err := store.List("run-bad-hunk-count")
	if err != nil || len(items) != 0 || requested {
		t.Fatalf("approvals=%d requested=%v err=%v", len(items), requested, err)
	}
	if err := gitx.EnsureClean(ctx, root); err != nil {
		t.Fatalf("rejected proposal mutated repo: %v", err)
	}
}

type failingPatchApprovalStore struct{}

func (failingPatchApprovalStore) Save(*approval.Approval) error {
	return errors.New("approval persistence failed")
}

func TestApplyPatchProposalApprovalPersistenceFailureRemainsAmbiguous(t *testing.T) {
	root := patchProposalRepo(t)
	base, err := gitx.CurrentSHA(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	patch := "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n"
	reg := NewRegistry()
	RegisterBuiltinsV4(reg, root, 1024*1024, "", root)
	cfg := PolicyConfig{WorkspaceRoot: root, AllowedPaths: []string{root}, MaxFileSize: 1024 * 1024}
	exec := NewExecutor(reg, &cfg)
	exec.SetApprovalStore(failingPatchApprovalStore{})
	result, err := exec.ExecuteWithPolicy(context.Background(), "apply_patch_proposal", "developer", "prizm", "exec-fail-save", map[string]any{"_run_id": "run-fail-save", "patch": patch, "base_sha": base})
	if err != nil || result.Success || result.FailureEffect != FailureEffectUnknown {
		t.Fatalf("persist failure certainty must fail closed: result=%#v err=%v", result, err)
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
