package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/subagent"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

type fakeSandboxReviewSession struct {
	patch, baseSHA, baseTree, digest string
	artifact                         string
	commands                         int
}

func (s *fakeSandboxReviewSession) Execute(context.Context, string) (string, int, error) {
	s.commands++
	return "ran", 0, nil
}
func (s *fakeSandboxReviewSession) ExportArtifact() (string, error) { return s.artifact, nil }
func (s *fakeSandboxReviewSession) ReviewPatch(context.Context) (string, string, string, string, error) {
	return s.patch, s.baseSHA, s.baseTree, s.digest, nil
}
func (*fakeSandboxReviewSession) Close() error { return nil }

func TestSandboxProposalCreatesOnePendingApprovalAndReusesDecision(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	cleanApprovalTestGitObjects(t, source)
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@prizm.local"}, {"config", "user.name", "Prizm Test"}} {
		if _, err := gitx.RunCommand(ctx, source, "", "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "base.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, source, "", "git", "add", "--all"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, source, "", "git", "commit", "-m", "base"); err != nil {
		t.Fatal(err)
	}
	runID := "run-sandbox-approval"
	workspace, _, sourceWorkspace, err := isolatedReferenceWorkspace(ctx, source, runID)
	if err != nil {
		t.Fatal(err)
	}
	baseSHA, err := gitx.CurrentSHA(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	baseTree, err := gitx.RunCommand(ctx, workspace, "", "git", "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	patch := "diff --git a/base.txt b/base.txt\n--- a/base.txt\n+++ b/base.txt\n@@ -1 +1 @@\n-base\n+changed\n"
	digestBytes := sha256.Sum256([]byte("canonical-sandbox-change-set"))
	digest := hex.EncodeToString(digestBytes[:])
	artifact := filepath.Join(t.TempDir(), "changes.zip")
	if err := os.WriteFile(artifact, []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	session := &fakeSandboxReviewSession{patch: patch, baseSHA: baseSHA, baseTree: strings.TrimSpace(baseTree), digest: digest, artifact: artifact}
	runtime := subagent.AgentRuntime{AgentID: "developer", RunID: runID, TaskID: "task-1", ExecutionKey: "exec-1", WorkspaceID: "workspace-1", WorkDir: workspace, Capabilities: []string{"code"}}
	identity, err := newSandboxRuntimeIdentity(runtime)
	if err != nil {
		t.Fatal(err)
	}
	owner := &subAgentSandboxTool{sourceRoot: workspace, session: session, identity: &identity}
	store := approval.NewStore(t.TempDir())
	registry := tool.NewRegistry()
	if err := registry.Register(&tool.ApplyPatchProposal{WorkspaceRoot: workspace, AllowedPaths: []string{workspace}}); err != nil {
		t.Fatal(err)
	}
	policy := tool.DefaultPolicyConfig()
	policy.WorkspaceRoot, policy.WriteRoots, policy.AllowedPaths = workspace, []string{workspace}, []string{workspace}
	policy.WriteAgents = map[string]bool{"developer": true}
	approvalExec := tool.NewExecutor(registry, &policy)
	approvalExec.SetApprovalStore(store)
	proposal := &subAgentSandboxProposalTool{sandbox: owner, approvalExec: approvalExec, runtime: runtime, project: "multiagent"}
	first, err := proposal.Execute(ctx, map[string]any{})
	if err != nil || !first.Success || first.Output["status"] != "pending_approval" {
		t.Fatalf("first proposal=%#v err=%v", first, err)
	}
	approvalID, _ := first.Output["approval_id"].(string)
	items, err := store.List(runID)
	if err != nil || len(items) != 1 {
		t.Fatalf("pending approvals=%d err=%v", len(items), err)
	}
	item := items[0]
	if item.ApprovalID != approvalID || item.SandboxBinding == nil || item.SandboxBinding.ArtifactDigest != digest || item.PatchPlan == nil {
		t.Fatalf("host binding missing from approval: %#v", item)
	}
	if err := gitx.EnsureClean(ctx, workspace); err != nil {
		t.Fatalf("pending proposal wrote to host workspace: %v", err)
	}

	second, err := proposal.Execute(ctx, map[string]any{})
	if err != nil || !second.Success || second.Output["approval_id"] != approvalID || second.Output["status"] != "pending_approval" {
		t.Fatalf("duplicate proposal=%#v err=%v", second, err)
	}
	if lenMustList(t, store, runID) != 1 {
		t.Fatal("duplicate proposal created another approval")
	}

	item, err = store.Load(runID, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := item.Approve("reviewer"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(item); err != nil {
		t.Fatal(err)
	}
	third, err := proposal.Execute(ctx, map[string]any{})
	if err != nil || !third.Success || third.Output["approval_id"] != approvalID || third.Output["status"] != approval.StatusApproved {
		t.Fatalf("decided proposal replay=%#v err=%v", third, err)
	}
	if lenMustList(t, store, runID) != 1 {
		t.Fatal("decided replay created another approval")
	}
	// The existing approval lifecycle is the only path that applies the patch.
	refs, err := (approvalProposalResolver{store: store, workspace: workspace}).ResolveProposals(ctx, multiagent.ProposalQuery{RunID: runID, ExecutionKey: runtime.ExecutionKey, AgentID: runtime.AgentID})
	if err != nil || len(refs) != 1 {
		t.Fatalf("resolved proposals=%#v err=%v", refs, err)
	}
	op := multiagent.ProposalOperation{RunID: runID, ProposalID: refs[0].ProposalID, ApprovalID: refs[0].ApprovalID, ExecutionKey: runtime.ExecutionKey, ApplyKey: "apply-sandbox-1", PatchPlan: refs[0].PatchPlan}
	lifecycle := newApprovalProposalLifecycle(store, workspace, t.TempDir(), sourceWorkspace)
	decision, _, err := lifecycle.Decision(ctx, op)
	if err != nil || decision != multiagent.ProposalGranted {
		t.Fatalf("decision=%q err=%v", decision, err)
	}
	result, err := lifecycle.Apply(ctx, op)
	if err != nil || !result.Success {
		t.Fatalf("approved apply=%#v err=%v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "base.txt"))
	if err != nil || strings.ReplaceAll(string(data), "\r\n", "\n") != "changed\n" {
		t.Fatalf("applied content=%q err=%v", data, err)
	}
	status, reconciled, err := lifecycle.Reconcile(ctx, op)
	if err != nil || status != multiagent.ProposalApplied || !reconciled.Success {
		t.Fatalf("post-apply reconciliation=%q %#v err=%v", status, reconciled, err)
	}
	blocked, err := owner.Execute(ctx, map[string]any{"command": "echo forbidden"})
	if err != nil || blocked.Success {
		t.Fatal("sandbox command remained enabled after proposal freeze")
	}
	if session.commands != 0 {
		t.Fatalf("frozen sandbox command executed %d times", session.commands)
	}
	if lenMustList(t, store, runID) != 1 {
		t.Fatal("approved apply created or reset an approval")
	}
	session.digest = strings.Repeat("d", 64)
	changedArtifact, err := proposal.Execute(ctx, map[string]any{})
	if err != nil || changedArtifact.Success {
		t.Fatalf("session accepted a changed artifact after proposal: %#v err=%v", changedArtifact, err)
	}

	// A separate task verifies denial replay leaves the workspace untouched.
	deniedRun := "run-sandbox-denied"
	deniedWorkspace, _, _, err := isolatedReferenceWorkspace(ctx, source, deniedRun)
	if err != nil {
		t.Fatal(err)
	}
	deniedBase, err := gitx.CurrentSHA(ctx, deniedWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	deniedTree, err := gitx.RunCommand(ctx, deniedWorkspace, "", "git", "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	deniedSession := &fakeSandboxReviewSession{patch: patch, baseSHA: deniedBase, baseTree: strings.TrimSpace(deniedTree), digest: digest, artifact: artifact}
	deniedRuntime := subagent.AgentRuntime{AgentID: "developer", RunID: deniedRun, TaskID: "task-2", ExecutionKey: "exec-2", WorkspaceID: "workspace-2", WorkDir: deniedWorkspace, Capabilities: []string{"code"}}
	deniedIdentity, err := newSandboxRuntimeIdentity(deniedRuntime)
	if err != nil {
		t.Fatal(err)
	}
	deniedOwner := &subAgentSandboxTool{sourceRoot: deniedWorkspace, session: deniedSession, identity: &deniedIdentity}
	deniedRegistry := tool.NewRegistry()
	if err := deniedRegistry.Register(&tool.ApplyPatchProposal{WorkspaceRoot: deniedWorkspace, AllowedPaths: []string{deniedWorkspace}}); err != nil {
		t.Fatal(err)
	}
	deniedPolicy := tool.DefaultPolicyConfig()
	deniedPolicy.WorkspaceRoot, deniedPolicy.WriteRoots, deniedPolicy.AllowedPaths = deniedWorkspace, []string{deniedWorkspace}, []string{deniedWorkspace}
	deniedPolicy.WriteAgents = map[string]bool{"developer": true}
	deniedExec := tool.NewExecutor(deniedRegistry, &deniedPolicy)
	deniedExec.SetApprovalStore(store)
	deniedProposal := &subAgentSandboxProposalTool{sandbox: deniedOwner, approvalExec: deniedExec, runtime: deniedRuntime, project: "multiagent"}
	deniedFirst, err := deniedProposal.Execute(ctx, map[string]any{})
	if err != nil || !deniedFirst.Success {
		t.Fatalf("denied pending=%#v err=%v", deniedFirst, err)
	}
	deniedID := deniedFirst.Output["approval_id"].(string)
	deniedRecord, err := store.Load(deniedRun, deniedID)
	if err != nil {
		t.Fatal(err)
	}
	if err := deniedRecord.Deny("reviewer", "declined"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(deniedRecord); err != nil {
		t.Fatal(err)
	}
	deniedReplay, err := deniedProposal.Execute(ctx, map[string]any{})
	if err != nil || !deniedReplay.Success || deniedReplay.Output["approval_id"] != deniedID || deniedReplay.Output["status"] != approval.StatusDenied {
		t.Fatalf("denied replay=%#v err=%v", deniedReplay, err)
	}
	if err := gitx.EnsureClean(ctx, deniedWorkspace); err != nil {
		t.Fatalf("denied proposal wrote to host workspace: %v", err)
	}
	if lenMustList(t, store, deniedRun) != 1 {
		t.Fatal("denied replay created another approval")
	}
}

func TestSandboxProposalRejectsUntrustedInputsAndBindsEachIdentityField(t *testing.T) {
	if _, err := sandboxProposalIdempotencyKey("run", "task", "exec", "agent", "workspace"); err != nil {
		t.Fatal(err)
	}
	base := []string{"run", "task", "exec", "agent", "workspace"}
	for i := range base {
		changed := append([]string(nil), base...)
		changed[i] += "-other"
		want, _ := sandboxProposalIdempotencyKey(base[0], base[1], base[2], base[3], base[4])
		got, _ := sandboxProposalIdempotencyKey(changed[0], changed[1], changed[2], changed[3], changed[4])
		if got == want {
			t.Fatalf("identity field %d did not affect proposal idempotency", i)
		}
	}
	if _, err := sandboxProposalIdempotencyKey("run", "", "exec", "agent", "workspace"); err == nil {
		t.Fatal("incomplete trusted identity accepted")
	}

	root := t.TempDir()
	baseRuntime := subagent.AgentRuntime{AgentID: "agent", RunID: "run", TaskID: "task", ExecutionKey: "exec", WorkspaceID: "workspace", WorkDir: root}
	identity, err := newSandboxRuntimeIdentity(baseRuntime)
	if err != nil {
		t.Fatal(err)
	}
	owner := &subAgentSandboxTool{sourceRoot: root, identity: &identity}
	proposalTool := &subAgentSandboxProposalTool{sandbox: owner, runtime: baseRuntime}
	result, err := proposalTool.Execute(context.Background(), map[string]any{"path": "C:\\host\\file"})
	if err != nil || result.Success {
		t.Fatalf("model supplied proposal input/path: result=%#v err=%v", result, err)
	}
	for name, mutate := range map[string]func(*subagent.AgentRuntime){
		"run":            func(rt *subagent.AgentRuntime) { rt.RunID += "-foreign" },
		"task":           func(rt *subagent.AgentRuntime) { rt.TaskID += "-foreign" },
		"execution":      func(rt *subagent.AgentRuntime) { rt.ExecutionKey += "-foreign" },
		"agent":          func(rt *subagent.AgentRuntime) { rt.AgentID += "-foreign" },
		"workspace_id":   func(rt *subagent.AgentRuntime) { rt.WorkspaceID += "-foreign" },
		"workspace_path": func(rt *subagent.AgentRuntime) { rt.WorkDir = filepath.Join(root, "foreign") },
	} {
		runtime := baseRuntime
		mutate(&runtime)
		foreignTool := &subAgentSandboxProposalTool{sandbox: owner, runtime: runtime}
		result, err := foreignTool.Execute(context.Background(), map[string]any{})
		if err != nil || result.Success {
			t.Errorf("foreign %s runtime received session: result=%#v err=%v", name, result, err)
		}
	}
}

func lenMustList(t *testing.T, store *approval.Store, runID string) int {
	t.Helper()
	items, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func cleanApprovalTestGitObjects(t *testing.T, repo string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	objects := filepath.Join(repo, ".git", "objects")
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
