package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/validation"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestApprovalProposalResolverUsesExecutionCorrelation(t *testing.T) {
	store := approval.NewStore(t.TempDir())
	workspace := t.TempDir()
	matching := approval.NewApproval("run-1", "execution-1", "developer", "prizm", approval.MutationWriteFile, "a.txt", "a", approval.PolicyDecision{Decision: approval.DecisionRequiresApproval})
	unrelated := approval.NewApproval("run-1", "other-execution", "developer", "prizm", approval.MutationWriteFile, "b.txt", "b", approval.PolicyDecision{Decision: approval.DecisionRequiresApproval})
	if err := store.Save(matching); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(unrelated); err != nil {
		t.Fatal(err)
	}
	refs, err := (approvalProposalResolver{store: store, workspace: workspace}).ResolveProposals(context.Background(), multiagent.ProposalQuery{RunID: "run-1", ExecutionKey: "execution-1", AgentID: "developer"})
	if err != nil || len(refs) != 1 || refs[0].ApprovalID != matching.ApprovalID || refs[0].ProposalID != matching.ProposalID || refs[0].ProposalID == refs[0].ApprovalID || len(refs[0].Artifacts) != 1 || refs[0].Artifacts[0].URI != "a.txt" {
		t.Fatalf("refs=%#v err=%v", refs, err)
	}
}

func TestApprovalProposalResolverCanonicalizesContainedAbsoluteTarget(t *testing.T) {
	store := approval.NewStore(t.TempDir())
	workspace := t.TempDir()
	target := filepath.Join(workspace, "a.txt")
	matching := approval.NewApproval("run-1", "execution-1", "developer", "prizm", approval.MutationWriteFile, target, "a", approval.PolicyDecision{Decision: approval.DecisionRequiresApproval})
	if err := store.Save(matching); err != nil {
		t.Fatal(err)
	}
	refs, err := (approvalProposalResolver{store: store, workspace: workspace}).ResolveProposals(context.Background(), multiagent.ProposalQuery{RunID: "run-1", ExecutionKey: "execution-1", AgentID: "developer"})
	if err != nil || len(refs) != 1 || len(refs[0].Artifacts) != 1 || refs[0].Artifacts[0].URI != "a.txt" {
		t.Fatalf("refs=%#v err=%v", refs, err)
	}
}

func TestApprovalProposalResolverRejectsTraversalTarget(t *testing.T) {
	store := approval.NewStore(t.TempDir())
	item := approval.NewApproval("run-1", "execution-1", "developer", "prizm", approval.MutationWriteFile, "../escape.txt", "x", approval.PolicyDecision{Decision: approval.DecisionRequiresApproval})
	if err := store.Save(item); err != nil {
		t.Fatal(err)
	}
	_, err := (approvalProposalResolver{store: store, workspace: t.TempDir()}).ResolveProposals(context.Background(), multiagent.ProposalQuery{RunID: "run-1", ExecutionKey: "execution-1", AgentID: "developer"})
	if err == nil {
		t.Fatal("traversal proposal target was accepted")
	}
}

func TestPatchProposalResolvesManyArtifactsAndReconcilesTrees(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@prizm.local"}, {"config", "user.name", "Prizm Test"}} {
		if _, err := gitx.RunCommand(ctx, workspace, "", "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, workspace, "", "git", "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, workspace, "", "git", "commit", "-m", "base"); err != nil {
		t.Fatal(err)
	}
	base, _ := gitx.CurrentSHA(ctx, workspace)
	patch := "diff --git a/base.txt b/base.txt\n--- a/base.txt\n+++ b/base.txt\n@@ -1 +1 @@\n-base\n+changed\n" +
		"diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n"
	plan, err := gitx.PlanPatch(ctx, workspace, patch, base)
	if err != nil {
		t.Fatal(err)
	}
	store := approval.NewStore(t.TempDir())
	item := approval.NewApproval("run-patch", "execution-patch", "developer", "prizm", approval.MutationApplyPatch, "2 files", patch, approval.PolicyDecision{Decision: approval.DecisionRequiresApproval})
	item.PatchPlan = &approval.PatchPlan{BaseSHA: plan.BaseSHA, BaseTree: plan.BaseTree, ExpectedTree: plan.ExpectedTree, PatchSHA256: plan.PatchSHA256, Paths: plan.Paths}
	if err := store.Save(item); err != nil {
		t.Fatal(err)
	}
	refs, err := (approvalProposalResolver{store: store, workspace: workspace}).ResolveProposals(ctx, multiagent.ProposalQuery{RunID: item.RunID, ExecutionKey: item.CorrelationID, AgentID: item.RequestedBy})
	if err != nil || len(refs) != 1 || len(refs[0].Artifacts) != 2 {
		t.Fatalf("refs=%#v err=%v", refs, err)
	}
	lifecycle := newApprovalProposalLifecycle(store, workspace, t.TempDir())
	op := multiagent.ProposalOperation{RunID: item.RunID, ProposalID: item.ProposalID, ApprovalID: item.ApprovalID, ApplyKey: "apply-patch", ExecutionKey: item.CorrelationID}
	status, _, err := lifecycle.Reconcile(ctx, op)
	if err != nil || status != multiagent.ProposalNotApplied {
		t.Fatalf("base status=%q err=%v", status, err)
	}
	if err := item.Approve("operator"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(item); err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.Apply(ctx, op)
	if err != nil || !result.Success {
		t.Fatalf("apply=%#v err=%v", result, err)
	}
	data, err := os.ReadFile(result.DiffPath)
	if err != nil || string(data) != patch {
		t.Fatalf("evidence is not exact approved patch: err=%v", err)
	}
	status, _, err = lifecycle.Reconcile(ctx, op)
	if err != nil || status != multiagent.ProposalApplied {
		t.Fatalf("applied status=%q err=%v", status, err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "unrelated.txt"), []byte("drift"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, _, err = lifecycle.Reconcile(ctx, op)
	if err != nil || status != multiagent.ProposalAmbiguous {
		t.Fatalf("drift status=%q err=%v", status, err)
	}
}

func TestIsolatedReferenceWorkspaceCreatesRunWorktree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}}
	for _, args := range commands {
		if _, err := gitx.RunCommand(context.Background(), root, "", "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(context.Background(), root, "", "git", "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(context.Background(), root, "", "git", "commit", "-m", "base"); err != nil {
		t.Fatal(err)
	}
	workspace, workspaceID, source, err := isolatedReferenceWorkspace(context.Background(), root, "run-isolated")
	if err != nil {
		t.Fatal(err)
	}
	if workspace == source || workspaceID == "" {
		t.Fatalf("workspace=%q source=%q id=%q", workspace, source, workspaceID)
	}
	if err := os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("isolated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("source workspace was mutated: %v", err)
	}
	runDir := t.TempDir()
	manifest := referenceWorkflowManifest{SchemaVersion: referenceManifestSchemaVersion, RunID: "run-isolated", WorkspacePath: workspace, SourceWorkspacePath: source}
	if err := cleanupTerminalReferenceWorkspace(context.Background(), runDir, &manifest); err != nil {
		t.Fatal(err)
	}
	if !manifest.WorkspaceCleaned {
		t.Fatal("terminal worktree was not marked cleaned")
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("terminal worktree still exists: %v", err)
	}
}

func TestApprovalProposalLifecycleAppliesAndReconcilesExactContent(t *testing.T) {
	workspace := t.TempDir()
	store := approval.NewStore(t.TempDir())
	item := approval.NewApproval("run-1", "execution-1", "developer", "prizm", approval.MutationWriteFile, "feature.txt", "approved content", approval.PolicyDecision{Decision: approval.DecisionRequiresApproval})
	if err := store.Save(item); err != nil {
		t.Fatal(err)
	}
	lifecycle := newApprovalProposalLifecycle(store, workspace, t.TempDir())
	op := multiagent.ProposalOperation{RunID: "run-1", ProposalID: item.ProposalID, ApprovalID: item.ApprovalID, ApplyKey: "apply-1", WorkspaceID: "workspace-1", ExecutionKey: "execution-1"}
	decision, _, err := lifecycle.Decision(context.Background(), op)
	if err != nil || decision != multiagent.ProposalPending {
		t.Fatalf("decision=%q err=%v", decision, err)
	}
	if err := item.Approve("operator"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(item); err != nil {
		t.Fatal(err)
	}
	status, _, err := lifecycle.Reconcile(context.Background(), op)
	if err != nil || status != multiagent.ProposalNotApplied {
		t.Fatalf("before apply status=%q err=%v", status, err)
	}
	result, err := lifecycle.Apply(context.Background(), op)
	if err != nil || !result.Success {
		t.Fatalf("apply=%#v err=%v", result, err)
	}
	status, result, err = lifecycle.Reconcile(context.Background(), op)
	if err != nil || status != multiagent.ProposalApplied || !result.Success {
		t.Fatalf("after apply status=%q result=%#v err=%v", status, result, err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "feature.txt"))
	if err != nil || string(content) != "approved content" {
		t.Fatalf("content=%q err=%v", content, err)
	}
}

func TestWorkspaceValidationRunnerUsesResolvedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	registry := validation.NewEmptyRegistry()
	if err := registry.Register(validation.Profile{Name: "go_version", Command: "go", Args: []string{"version"}, TimeoutSeconds: 10, AllowedExitCodes: []int{0}}); err != nil {
		t.Fatal(err)
	}
	runner := workspaceValidationRunner{registry: registry, fallbackRoot: t.TempDir(), artifactRoot: t.TempDir()}
	result, err := runner.RunValidationInWorkspace(context.Background(), "go_version", "run-1", multiagent.Workspace{ID: "workspace-1", Path: workspace})
	if err != nil || result.Status != "passed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
