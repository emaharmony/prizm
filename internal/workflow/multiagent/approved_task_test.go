package multiagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/mutation"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/validation"
)

type actualPatchLifecycle struct {
	store           *approval.Store
	executor        *mutation.Executor
	workspace       string
	sourceWorkspace string
	onApply         func()
}

func (l actualPatchLifecycle) Decision(_ context.Context, op ProposalOperation) (ProposalDecision, string, error) {
	a, err := l.store.Load(op.RunID, op.ApprovalID)
	if err != nil {
		return "", "", err
	}
	switch a.Status {
	case approval.StatusPending:
		return ProposalPending, "pending", nil
	case approval.StatusApproved:
		return ProposalGranted, "granted", nil
	case approval.StatusDenied:
		return ProposalDenied, a.DenialReason, nil
	default:
		return ProposalExpired, a.Status, nil
	}
}
func (l actualPatchLifecycle) Apply(ctx context.Context, op ProposalOperation) (ProposalApplyResult, error) {
	a, err := l.store.Load(op.RunID, op.ApprovalID)
	if err != nil {
		return ProposalApplyResult{}, err
	}
	if a.PatchPlan != nil && l.sourceWorkspace != "" {
		ctx, err = gitx.WithVerifiedRunWorktree(ctx, l.workspace, l.sourceWorkspace, op.RunID, a.PatchPlan.BaseSHA)
		if err != nil {
			return ProposalApplyResult{}, err
		}
	}
	r, err := l.executor.ApplyWithRun(ctx, op.RunID, op.ApprovalID, a.ApprovedBy)
	if err != nil {
		return ProposalApplyResult{}, err
	}
	if r.Success && l.onApply != nil {
		l.onApply()
	}
	applyResult := ProposalApplyResult{Success: r.Success, TargetPath: a.TargetPath, Message: r.Message}
	if r.Success && a.PatchPlan != nil {
		status, receipt, reconcileErr := l.Reconcile(ctx, op)
		if reconcileErr != nil {
			return ProposalApplyResult{}, reconcileErr
		}
		if status != ProposalApplied || !receipt.Success || receipt.ReconcileCode != ProposalReconcileExpectedTree {
			return receipt, errors.New("applied patch did not reconcile to its exact expected tree")
		}
		return receipt, nil
	}
	return applyResult, nil
}
func (l actualPatchLifecycle) Reconcile(ctx context.Context, op ProposalOperation) (ProposalReconciliation, ProposalApplyResult, error) {
	a, err := l.store.Load(op.RunID, op.ApprovalID)
	if err != nil {
		return "", ProposalApplyResult{}, err
	}
	if a.PatchPlan == nil {
		return ProposalAmbiguous, ProposalApplyResult{}, nil
	}
	if l.sourceWorkspace != "" {
		ctx, err = gitx.WithVerifiedRunWorktree(ctx, l.workspace, l.sourceWorkspace, op.RunID, a.PatchPlan.BaseSHA)
		if err != nil {
			return ProposalAmbiguous, ProposalApplyResult{}, nil
		}
	}
	tree, err := gitx.WorktreeTree(ctx, l.workspace)
	if err != nil {
		return ProposalAmbiguous, ProposalApplyResult{}, nil
	}
	if tree == a.PatchPlan.ExpectedTree {
		return ProposalApplied, ProposalApplyResult{Success: true, TargetPath: a.TargetPath,
			ReconcileCode: ProposalReconcileExpectedTree, ObservedTree: tree, PatchPlan: gitxPatchPlanForTest(a.PatchPlan)}, nil
	}
	planningTree := a.PatchPlan.PlanningTree
	if planningTree == "" {
		planningTree = a.PatchPlan.BaseTree
	}
	if tree == planningTree {
		return ProposalNotApplied, ProposalApplyResult{TargetPath: a.TargetPath}, nil
	}
	return ProposalAmbiguous, ProposalApplyResult{TargetPath: a.TargetPath}, nil
}

func TestDurableApprovedTaskRealAtomicPatchRestartThroughValidation(t *testing.T) {
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
	patch := "diff --git a/base.txt b/base.txt\n--- a/base.txt\n+++ b/base.txt\n@@ -1 +1 @@\n-base\n+changed\n" + "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n"
	runID := "run-real-atomic-patch"
	approvalStore := approval.NewStore(t.TempDir())
	registry := tool.NewRegistry()
	tool.RegisterBuiltinsV4(registry, workspace, 1024*1024, "", workspace)
	cfg := tool.PolicyConfig{WorkspaceRoot: workspace, AllowedPaths: []string{workspace}, MaxFileSize: 1024 * 1024}
	toolExecutor := tool.NewExecutor(registry, &cfg)
	toolExecutor.SetApprovalStore(approvalStore)
	proposal, err := toolExecutor.ExecuteWithPolicy(ctx, "apply_patch_proposal", "developer", "prizm", "execution-real-patch", map[string]any{"_run_id": runID, "patch": patch, "base_sha": base})
	if err != nil || !proposal.Success {
		t.Fatalf("proposal=%#v err=%v", proposal, err)
	}
	approvalID, _ := proposal.Output["approval_id"].(string)
	proposalID, _ := proposal.Output["proposal_id"].(string)
	baseRunner := approvedTaskRunner()
	developer := baseRunner.scripts[RoleDeveloper][0].result
	developer.Proposals = []ProposalReference{{ProposalID: proposalID, ApprovalID: approvalID, Artifacts: []ArtifactRef{{Kind: ArtifactFile, URI: "base.txt"}, {Kind: ArtifactFile, URI: "new.txt"}}}}
	baseRunner.scripts[RoleDeveloper][0].result = developer
	runner := &postApplyValidationRunner{scriptedRunner: baseRunner}
	lifecycle := actualPatchLifecycle{store: approvalStore, executor: mutation.NewExecutor(workspace, approvalStore, workspace), workspace: workspace, onApply: func() { runner.workspaceChanged = true }}
	env := newDurableTestEnvironment(t)
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events, DurableRuntimeOptions{Proposals: lifecycle})
	request := testRunRequest()
	request.RunID = runID
	state, err := runtime.Run(ctx, request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || state.Status != RunStatusPaused {
		t.Fatalf("state=%q err=%v", state.Status, err)
	}
	a, err := approvalStore.Load(runID, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Approve("operator"); err != nil {
		t.Fatal(err)
	}
	if err := approvalStore.Save(a); err != nil {
		t.Fatal(err)
	}
	restarted := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events, DurableRuntimeOptions{Proposals: lifecycle})
	state, err = restarted.Resume(ctx, runID)
	if err != nil || state.Status != RunStatusCompleted || runner.validationCalls != 1 {
		t.Fatalf("state=%q validation=%d err=%v", state.Status, runner.validationCalls, err)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "new.txt")); err != nil || strings.TrimSpace(string(data)) != "new" {
		t.Fatalf("new file=%q err=%v", data, err)
	}
}

func TestDurableFailedValidationRetainsReceiptForSeparatelyApprovedCorrection(t *testing.T) {
	const runID = "run-validation-failure-correction"
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@prizm.local"}, {"config", "user.name", "Prizm Test"}} {
		if _, err := gitx.RunCommand(ctx, source, "", "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, source, "", "git", "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, source, "", "git", "commit", "-m", "base"); err != nil {
		t.Fatal(err)
	}
	base, err := gitx.CurrentSHA(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(source, ".prizm", "worktrees", gitx.SafeID(runID, "run"))
	if err := os.MkdirAll(filepath.Dir(workspace), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, source, "", "git", "worktree", "add", "--detach", workspace, base); err != nil {
		t.Fatalf("create owned run worktree: %v", err)
	}
	patchA := "diff --git a/base.txt b/base.txt\n--- a/base.txt\n+++ b/base.txt\n@@ -1 +1 @@\n-base\n+changed\n"
	approvalStore := approval.NewStore(t.TempDir())
	registry := tool.NewRegistry()
	tool.RegisterBuiltinsV4(registry, workspace, 1024*1024, "", workspace)
	cfg := tool.PolicyConfig{WorkspaceRoot: workspace, AllowedPaths: []string{workspace}, MaxFileSize: 1024 * 1024}
	toolExecutor := tool.NewExecutor(registry, &cfg)
	toolExecutor.SetApprovalStore(approvalStore)
	proposalA, err := toolExecutor.ExecuteWithPolicy(ctx, "apply_patch_proposal", "developer", "prizm", "execution-A", map[string]any{
		"_run_id": runID, "patch": patchA, "base_sha": base,
	})
	if err != nil || !proposalA.Success {
		t.Fatalf("proposal A=%#v err=%v", proposalA, err)
	}
	approvalIDA, _ := proposalA.Output["approval_id"].(string)
	proposalIDA, _ := proposalA.Output["proposal_id"].(string)
	storedA, err := approvalStore.Load(runID, approvalIDA)
	if err != nil || storedA.PatchPlan == nil {
		t.Fatalf("load patch A approval: plan=%#v err=%v", storedA, err)
	}
	planA := gitxPatchPlanForTest(storedA.PatchPlan)
	baseRunner := approvedTaskRunner()
	developer := baseRunner.scripts[RoleDeveloper][0].result
	developer.Proposals = []ProposalReference{{ProposalID: proposalIDA, ApprovalID: approvalIDA,
		PatchPlan: planA, Artifacts: []ArtifactRef{{Kind: ArtifactFile, URI: "base.txt"}}}}
	baseRunner.scripts[RoleDeveloper][0].result = developer
	runner := &postApplyValidationRunner{scriptedRunner: baseRunner, validationErr: errors.New("intentional validation rejection")}
	lifecycle := actualPatchLifecycle{store: approvalStore, executor: mutation.NewExecutor(workspace, approvalStore, workspace), workspace: workspace, sourceWorkspace: source,
		onApply: func() { runner.workspaceChanged = true }}
	env := newDurableTestEnvironment(t)
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events, DurableRuntimeOptions{Proposals: lifecycle})
	request := testRunRequest()
	request.RunID = runID
	state, err := runtime.Run(ctx, request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || state.Status != RunStatusPaused {
		t.Fatalf("initial state=%q err=%v", state.Status, err)
	}
	approvedA, err := approvalStore.Load(runID, approvalIDA)
	if err != nil || approvedA.PatchPlan == nil || approvedA.PatchPlan.ExpectedTree != planA.ExpectedTree {
		t.Fatalf("load A for grant: %#v err=%v", approvedA, err)
	}
	if err := approvedA.Approve("operator"); err != nil {
		t.Fatal(err)
	}
	if err := approvalStore.Save(approvedA); err != nil {
		t.Fatal(err)
	}
	state, err = runtime.Resume(ctx, runID)
	if err == nil || state.Status != RunStatusFailed || runner.validationCalls != 1 {
		t.Fatalf("post-apply validation state=%q calls=%d err=%v", state.Status, runner.validationCalls, err)
	}
	record, err := runtime.Inspect(ctx, runID)
	if err != nil || record.ApprovedTask == nil || len(record.ApprovedTask.Proposals) != 1 {
		t.Fatalf("failed validation lost approved task receipt: task=%#v err=%v", record.ApprovedTask, err)
	}
	basis := trustedTreeBasisForRole(record, RoleDeveloper)
	if basis == nil || basis.ApprovalID != approvalIDA || basis.ExpectedTree != planA.ExpectedTree {
		t.Fatalf("durable A receipt did not yield correction basis: basis=%#v progress=%#v", basis, record.ApprovedTask.Proposals[0])
	}
	if tree, err := gitx.WorktreeTree(ctx, workspace); err != nil || tree != basis.ExpectedTree {
		t.Fatalf("A applied tree=%q expected=%q err=%v", tree, basis.ExpectedTree, err)
	}

	patchB := "diff --git a/base.txt b/base.txt\n--- a/base.txt\n+++ b/base.txt\n@@ -1 +1 @@\n-changed\n+corrected\n"
	proposalB, err := toolExecutor.ExecuteWithPolicy(gitx.ContextWithTrustedTreeBasis(ctx, *basis), "apply_patch_proposal", "developer", "prizm", "execution-B", map[string]any{
		"_run_id": runID, "patch": patchB, "base_sha": base,
	})
	if err != nil || !proposalB.Success {
		t.Fatalf("proposal B from trusted receipt=%#v err=%v", proposalB, err)
	}
	approvalIDB, _ := proposalB.Output["approval_id"].(string)
	proposalIDB, _ := proposalB.Output["proposal_id"].(string)
	if approvalIDB == "" || proposalIDB == "" || approvalIDB == approvalIDA || proposalIDB == proposalIDA {
		t.Fatalf("correction reused source identity: proposal=%q approval=%q", proposalIDB, approvalIDB)
	}
	approvedB, err := approvalStore.Load(runID, approvalIDB)
	if err != nil || approvedB.PatchPlan == nil || !gitx.EqualTrustedTreeBasis(approvedB.PatchPlan.Basis, basis) {
		t.Fatalf("B approval did not bind exact A receipt: approval=%#v err=%v", approvedB, err)
	}
	if err := approvedB.Approve("operator"); err != nil {
		t.Fatal(err)
	}
	if err := approvalStore.Save(approvedB); err != nil {
		t.Fatal(err)
	}
	verifiedCtx, err := gitx.WithVerifiedRunWorktree(gitx.ContextWithTrustedTreeBasis(ctx, *basis), workspace, source, runID, base)
	if err != nil {
		t.Fatal(err)
	}
	applyB, err := lifecycle.executor.ApplyWithRun(verifiedCtx, runID, approvalIDB, "operator")
	if err != nil || !applyB.Success {
		t.Fatalf("apply separately approved B: result=%#v err=%v", applyB, err)
	}
	operationB := ProposalOperation{RunID: runID, ProposalID: proposalIDB, ApprovalID: approvalIDB,
		ApplyKey: proposalApplyKey(runID, proposalIDB), WorkspaceID: record.State.WorkspaceID, ExecutionKey: "execution-B",
		Role: RoleDeveloper, PatchPlan: gitxPatchPlanForTest(approvedB.PatchPlan)}
	reconciled, receiptB, err := lifecycle.Reconcile(ctx, operationB)
	if err != nil || reconciled != ProposalApplied || receiptB.Success != true {
		t.Fatalf("recover B application: status=%q result=%#v err=%v", reconciled, receiptB, err)
	}
}

type fakeProposalLifecycle struct {
	mu              sync.Mutex
	decisions       map[string]ProposalDecision
	reasons         map[string]string
	applyCalls      []ProposalOperation
	reconciled      ProposalReconciliation
	reconcileResult ProposalApplyResult
	applyFails      int
	onApply         func()
}

func (f *fakeProposalLifecycle) Decision(_ context.Context, op ProposalOperation) (ProposalDecision, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decisions[op.ApprovalID], f.reasons[op.ApprovalID], nil
}

func (f *fakeProposalLifecycle) Apply(_ context.Context, op ProposalOperation) (ProposalApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyCalls = append(f.applyCalls, op)
	if f.applyFails > 0 {
		f.applyFails--
		return ProposalApplyResult{}, errors.New("injected apply failure")
	}
	if f.onApply != nil {
		f.onApply()
	}
	return ProposalApplyResult{Success: true, TargetPath: "feature.txt", Message: "applied"}, nil
}

type postApplyValidationRunner struct {
	*scriptedRunner
	workspaceChanged bool
	validationCalls  int
	lastResult       RoleRunResult
	validationErr    error
}

type roleRunnerWithoutPostValidation struct{ inner *scriptedRunner }

func (r roleRunnerWithoutPostValidation) RunRole(ctx context.Context, request RoleRunRequest) (RoleRunResult, error) {
	return r.inner.RunRole(ctx, request)
}

func (r *postApplyValidationRunner) ValidateApprovedRole(_ context.Context, request RoleRunRequest, result RoleRunResult) (RoleRunResult, error) {
	r.validationCalls++
	if request.Run.CurrentRole != RoleDeveloper {
		return result, errors.New("post-apply validation received the wrong role")
	}
	if !r.workspaceChanged {
		return result, errors.New("pre-change validation would fail")
	}
	if result.OutgoingHandoff == nil {
		return result, errors.New("developer result has no handoff")
	}
	result.Metadata.ValidationStatus = "passed"
	if r.validationErr != nil {
		result.Metadata.ValidationStatus = "failed"
		result.OutgoingHandoff.ValidationResults = []validation.Result{{Profile: "post_apply", Status: "failed"}}
		r.lastResult = result
		return result, r.validationErr
	}
	result.OutgoingHandoff.ValidationResults = []validation.Result{{Profile: "post_apply", Status: "passed"}}
	r.lastResult = result
	return result, nil
}

func (f *fakeProposalLifecycle) Reconcile(_ context.Context, _ ProposalOperation) (ProposalReconciliation, ProposalApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reconciled == "" {
		return ProposalNotApplied, ProposalApplyResult{}, nil
	}
	result := f.reconcileResult
	result.Success = f.reconciled == ProposalApplied
	if result.TargetPath == "" {
		result.TargetPath = "feature.txt"
	}
	return f.reconciled, result, nil
}

func TestDurableApprovedTaskPersistsSafeReconcileFailure(t *testing.T) {
	env := newDurableTestEnvironment(t)
	lifecycle := &fakeProposalLifecycle{decisions: map[string]ProposalDecision{"approval-1": ProposalPending}, reconciled: ProposalNotApplied}
	runtime := newDurableRuntimeForTest(t, validDefinition(), approvedTaskRunner(), env.store, env.claimer, env.events,
		DurableRuntimeOptions{Proposals: lifecycle})
	request := testRunRequest()
	request.RunID = "run-proposal-safe-reconcile-failure"
	state, err := runtime.Run(context.Background(), request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || state.Status != RunStatusPaused {
		t.Fatalf("initial approval wait state=%q err=%v", state.Status, err)
	}
	lifecycle.decisions["approval-1"] = ProposalGranted
	lifecycle.reconciled = ProposalAmbiguous
	lifecycle.reconcileResult = ProposalApplyResult{ReconcileCode: ProposalReconcileTreeMismatch, ObservedTree: strings.Repeat("a", 40), Message: "PRIVATE_OUTPUT", TargetPath: "PRIVATE_PATH"}
	state, err = runtime.Resume(context.Background(), request.RunID)
	if !errors.As(err, &waiting) || state.Status != RunStatusPaused {
		t.Fatalf("ambiguous reconciliation state=%q err=%v", state.Status, err)
	}
	record, err := env.store.Load(context.Background(), request.RunID)
	if err != nil || record.ApprovedTask == nil || record.ApprovedTask.Proposals[0].Result == nil ||
		record.ApprovedTask.Proposals[0].Result.ReconcileCode != ProposalReconcileTreeMismatch ||
		record.ApprovedTask.Proposals[0].Result.ObservedTree != strings.Repeat("a", 40) ||
		record.ApprovedTask.Proposals[0].Result.Message != "" || record.ApprovedTask.Proposals[0].Result.TargetPath != "" ||
		record.Waiting == nil || record.Waiting.SafeToRetry {
		t.Fatalf("safe reconcile state=%#v err=%v", record, err)
	}
	events, err := env.events.Query(context.Background(), event.EventFilter{RunID: request.RunID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, evt := range events {
		if evt.Type != event.EventProposalApplyFailed {
			continue
		}
		found = true
		if err := event.Validate(evt); err != nil {
			t.Fatalf("reconcile event violates schema: %v", err)
		}
		if evt.Payload["reconcile_code"] != string(ProposalReconcileTreeMismatch) ||
			evt.Payload["observed_tree"] != strings.Repeat("a", 40) || evt.Payload["reason"] != "proposal application outcome is ambiguous" {
			t.Fatalf("unexpected safe reconcile event payload: %#v", evt.Payload)
		}
		for _, forbidden := range []string{"raw_error", "stdout", "stderr", "patch", "content", "PRIVATE_OUTPUT", "PRIVATE_PATH"} {
			if _, exists := evt.Payload[forbidden]; exists {
				t.Fatalf("reconcile event leaked %q", forbidden)
			}
		}
	}
	if !found || len(lifecycle.applyCalls) != 0 {
		t.Fatalf("ambiguous result emitted=%t apply_calls=%d", found, len(lifecycle.applyCalls))
	}
}

func TestValidProposalReconcileCodeAcceptsObservedTreesAndKnownFailures(t *testing.T) {
	for _, code := range []ProposalReconcileCode{ProposalReconcilePlanMissing, ProposalReconcileInspectFailed,
		ProposalReconcileContextCancelled, ProposalReconcileExpectedTree, ProposalReconcileBaseTree, ProposalReconcileTreeMismatch} {
		if !validProposalReconcileCode(code) {
			t.Errorf("known reconcile code %q rejected", code)
		}
	}
	if validProposalReconcileCode(ProposalReconcileCode("raw-error-text")) {
		t.Fatal("unrecognized reconcile code accepted")
	}
	unsafe := safeProposalReconcileResult(ProposalApplyResult{ReconcileCode: ProposalReconcileCode("private error text"),
		ObservedTree: "not-a-tree-id", Message: "private output", TargetPath: "private/path"})
	if unsafe != (ProposalApplyResult{}) {
		t.Fatalf("unsafe reconciliation fields survived projection: %#v", unsafe)
	}
}

func TestSafeProposalReconcileResultKeepsOnlyBoundedInspectionDiagnostic(t *testing.T) {
	result := safeProposalReconcileResult(ProposalApplyResult{
		ReconcileCode:   ProposalReconcileInspectFailed,
		InspectionStage: "stage_worktree", InspectionCategory: "command_failed", InspectionExitCode: 128,
		ObservedTree: strings.Repeat("a", 40), Message: "private command output", TargetPath: "private/path",
	})
	if result.InspectionStage != "stage_worktree" || result.InspectionCategory != "command_failed" || result.InspectionExitCode != 128 || result.Message != "" || result.TargetPath != "" {
		t.Fatalf("bounded diagnostic projection=%#v", result)
	}
	malicious := safeProposalReconcileResult(ProposalApplyResult{
		ReconcileCode: ProposalReconcileInspectFailed, InspectionStage: "C:/private/path",
		InspectionCategory: "permission denied: private file", InspectionExitCode: 999,
	})
	if malicious.InspectionStage != "" || malicious.InspectionCategory != "" || malicious.InspectionExitCode != 0 {
		t.Fatalf("unsafe diagnostic fields survived: %#v", malicious)
	}
	nonCommand := safeProposalReconcileResult(ProposalApplyResult{
		ReconcileCode: ProposalReconcileInspectFailed, InspectionStage: "read_tree",
		InspectionCategory: "permission", InspectionExitCode: 13,
	})
	if nonCommand.InspectionStage != "read_tree" || nonCommand.InspectionCategory != "permission" || nonCommand.InspectionExitCode != 0 {
		t.Fatalf("non-command failure retained exit status: %#v", nonCommand)
	}
}

func approvedTaskRunner() *scriptedRunner {
	runner := happyDurableRunner()
	result := runner.scripts[RoleDeveloper][0].result
	result.Proposals = []ProposalReference{{ProposalID: "proposal-1", ApprovalID: "approval-1"}}
	runner.scripts[RoleDeveloper][0].result = result
	return runner
}

func TestTrustedTreeBasisRequiresAppliedSameRunWorkspaceRoleReceipt(t *testing.T) {
	plan := &gitx.PatchPlan{BaseSHA: strings.Repeat("a", 40), BaseTree: strings.Repeat("b", 40),
		PlanningTree: strings.Repeat("b", 40), ExpectedTree: strings.Repeat("c", 40),
		PatchSHA256: strings.Repeat("d", 64), Paths: []string{"src/file.go"}}
	operationPlan := gitx.ClonePatchPlan(*plan)
	resultPlan := gitx.ClonePatchPlan(*plan)
	progress := ProposalProgress{ProposalOperation: ProposalOperation{RunID: "run-a", ProposalID: "proposal-a",
		ApprovalID: "approval-a", ApplyKey: proposalApplyKey("run-a", "proposal-a"), WorkspaceID: "workspace-a",
		ExecutionKey: "execution-a", Role: RoleDeveloper, PatchPlan: &operationPlan},
		Phase: "applied", Result: &ProposalApplyResult{Success: true, ReconcileCode: ProposalReconcileExpectedTree,
			ObservedTree: plan.ExpectedTree, PatchPlan: &resultPlan}}
	record := DurableRun{State: RunState{RunID: "run-a", WorkspaceID: "workspace-a"},
		ApprovedTask: &ApprovedTaskState{Role: RoleDeveloper, Proposals: []ProposalProgress{progress}}}
	if got := trustedTreeBasisForRole(record, RoleDeveloper); got == nil || got.ApprovalID != "approval-a" {
		t.Fatalf("canonical applied receipt did not produce basis: %#v", got)
	}
	if got := trustedTreeBasisForRole(record, RoleTester); got != nil {
		t.Fatalf("non-developer role received a patch basis: %#v", got)
	}

	tests := []struct {
		name   string
		mutate func(*DurableRun)
	}{
		{"wrong run", func(r *DurableRun) { r.ApprovedTask.Proposals[0].RunID = "run-b" }},
		{"wrong workspace", func(r *DurableRun) { r.ApprovedTask.Proposals[0].WorkspaceID = "workspace-b" }},
		{"wrong role", func(r *DurableRun) { r.ApprovedTask.Proposals[0].Role = RoleTester }},
		{"missing execution scope", func(r *DurableRun) { r.ApprovedTask.Proposals[0].ExecutionKey = "" }},
		{"wrong apply key", func(r *DurableRun) { r.ApprovedTask.Proposals[0].ApplyKey = "other" }},
		{"not durably applied", func(r *DurableRun) { r.ApprovedTask.Proposals[0].Phase = "waiting" }},
		{"observed tree mismatch", func(r *DurableRun) { r.ApprovedTask.Proposals[0].Result.ObservedTree = strings.Repeat("e", 40) }},
		{"operation plan tamper", func(r *DurableRun) { r.ApprovedTask.Proposals[0].PatchPlan.ExpectedTree = strings.Repeat("e", 40) }},
		{"result plan tamper", func(r *DurableRun) {
			r.ApprovedTask.Proposals[0].Result.PatchPlan.ExpectedTree = strings.Repeat("e", 40)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := record
			task := *record.ApprovedTask
			task.Proposals = append([]ProposalProgress(nil), record.ApprovedTask.Proposals...)
			candidate.ApprovedTask = &task
			operationPlan := gitx.ClonePatchPlan(*record.ApprovedTask.Proposals[0].PatchPlan)
			candidate.ApprovedTask.Proposals[0].ProposalOperation.PatchPlan = &operationPlan
			result := *record.ApprovedTask.Proposals[0].Result
			resultPlan := gitx.ClonePatchPlan(*record.ApprovedTask.Proposals[0].Result.PatchPlan)
			result.PatchPlan = &resultPlan
			candidate.ApprovedTask.Proposals[0].Result = &result
			tc.mutate(&candidate)
			if got := trustedTreeBasisForRole(candidate, RoleDeveloper); got != nil {
				t.Fatalf("invalid receipt produced basis: %#v", got)
			}
		})
	}
}

func gitxPatchPlanForTest(plan *approval.PatchPlan) *gitx.PatchPlan {
	if plan == nil {
		return nil
	}
	result := &gitx.PatchPlan{BaseSHA: plan.BaseSHA, BaseTree: plan.BaseTree, PlanningTree: plan.PlanningTree,
		ExpectedTree: plan.ExpectedTree, PatchSHA256: plan.PatchSHA256, Paths: append([]string(nil), plan.Paths...)}
	if plan.Basis != nil {
		basis := *plan.Basis
		basis.Paths = append([]string(nil), plan.Basis.Paths...)
		result.Basis = &basis
	}
	return result
}

func TestReconcileApprovedTaskHandoffSupersedesOnlySettledApprovalIssue(t *testing.T) {
	result := RoleRunResult{OutgoingHandoff: &HandoffDraft{
		Notes: "proposal created before human decision",
		UnresolvedIssues: []Issue{
			{ID: "approval-pending", Summary: "Approval approval-1 is pending", Blocking: true},
			{ID: "real-blocker", Summary: "Repository fixture still needs review", Blocking: true},
		},
	}}
	reconcileApprovedTaskHandoff(&result, []ProposalProgress{{
		ProposalOperation: ProposalOperation{ProposalID: "proposal-1", ApprovalID: "approval-1"},
	}})
	if len(result.OutgoingHandoff.UnresolvedIssues) != 1 || result.OutgoingHandoff.UnresolvedIssues[0].ID != "real-blocker" {
		t.Fatalf("unresolved issues = %#v", result.OutgoingHandoff.UnresolvedIssues)
	}
	if !strings.Contains(result.OutgoingHandoff.Notes, "Authoritative runtime state") {
		t.Fatalf("missing settled lifecycle fact: %q", result.OutgoingHandoff.Notes)
	}
}

func TestDurableApprovedTaskExactGrantApplyResumeAndDuplicate(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := approvedTaskRunner()
	lifecycle := &fakeProposalLifecycle{decisions: map[string]ProposalDecision{
		"approval-1": ProposalPending, "unrelated": ProposalGranted,
	}}
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events,
		DurableRuntimeOptions{Proposals: lifecycle})
	request := testRunRequest()
	request.RunID = "run-approved-task"

	state, err := runtime.Run(context.Background(), request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || waiting.Kind != "proposal_approval" || state.Status != RunStatusPaused {
		t.Fatalf("initial run state=%q err=%v", state.Status, err)
	}
	record, err := runtime.Inspect(context.Background(), request.RunID)
	if err != nil || record.Waiting.ApprovalID != "approval-1" || record.Waiting.ProposalID != "proposal-1" {
		t.Fatalf("exact waiting identity not persisted: %#v err=%v", record.Waiting, err)
	}
	applyKey := record.ApprovedTask.Proposals[0].ApplyKey
	if applyKey == "" || applyKey != proposalApplyKey(request.RunID, "proposal-1") {
		t.Fatalf("apply key = %q", applyKey)
	}

	// Granting an unrelated approval cannot advance this run.
	state, err = runtime.Resume(context.Background(), request.RunID)
	if !errors.As(err, &waiting) || state.Status != RunStatusPaused || len(lifecycle.applyCalls) != 0 {
		t.Fatalf("unrelated grant advanced run: state=%q calls=%d err=%v", state.Status, len(lifecycle.applyCalls), err)
	}
	lifecycle.decisions["approval-1"] = ProposalGranted
	state, err = runtime.Resume(context.Background(), request.RunID)
	if err != nil || state.Status != RunStatusCompleted {
		t.Fatalf("granted resume state=%q err=%v", state.Status, err)
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, []Role{RolePlanner, RoleDeveloper, RoleTester, RoleReviewer}) {
		t.Fatalf("role calls = %v; developer must not rerun", got)
	}
	if len(lifecycle.applyCalls) != 1 || lifecycle.applyCalls[0].ApplyKey != applyKey {
		t.Fatalf("apply calls = %#v", lifecycle.applyCalls)
	}
	completed, inspectErr := runtime.Inspect(context.Background(), request.RunID)
	if inspectErr != nil || len(completed.ProposalResults) != 1 || completed.ProposalResults[0].Result == nil || !completed.ProposalResults[0].Result.Success {
		t.Fatalf("persisted proposal results = %#v err=%v", completed.ProposalResults, inspectErr)
	}
	if _, err := runtime.Resume(context.Background(), request.RunID); err != nil || len(lifecycle.applyCalls) != 1 {
		t.Fatalf("terminal duplicate resume reapplied proposal: calls=%d err=%v", len(lifecycle.applyCalls), err)
	}

	events, err := env.events.Query(context.Background(), event.EventFilter{RunID: request.RunID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{event.EventProposalRecorded: false, event.EventProposalApprovalGranted: false,
		event.EventProposalApplyStarted: false, event.EventProposalApplied: false, event.EventMultiAgentRunResumed: false}
	for _, evt := range events {
		if _, ok := want[evt.Type]; ok {
			want[evt.Type] = true
		}
	}
	for typ, found := range want {
		if !found {
			t.Errorf("missing lifecycle event %s", typ)
		}
	}
}

func TestDurableApprovedTaskValidatesOnlyAfterApplyAndPersistsResults(t *testing.T) {
	env := newDurableTestEnvironment(t)
	base := approvedTaskRunner()
	runner := &postApplyValidationRunner{scriptedRunner: base}
	lifecycle := &fakeProposalLifecycle{decisions: map[string]ProposalDecision{"approval-1": ProposalPending}}
	lifecycle.onApply = func() { runner.workspaceChanged = true }
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events,
		DurableRuntimeOptions{Proposals: lifecycle})
	request := testRunRequest()
	request.RunID = "run-post-apply-validation"

	state, err := runtime.Run(context.Background(), request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || state.Status != RunStatusPaused || runner.validationCalls != 0 {
		t.Fatalf("pre-approval state=%q calls=%d err=%v", state.Status, runner.validationCalls, err)
	}
	lifecycle.decisions["approval-1"] = ProposalGranted
	state, err = runtime.Resume(context.Background(), request.RunID)
	if err != nil || state.Status != RunStatusCompleted || runner.validationCalls != 1 {
		t.Fatalf("post-apply state=%q calls=%d err=%v", state.Status, runner.validationCalls, err)
	}
	record, err := runtime.Inspect(context.Background(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	developer := record.State.RoleStates[RoleDeveloper]
	if developer.ValidationStatus != "passed" || len(runner.lastResult.OutgoingHandoff.ValidationResults) != 1 {
		t.Fatalf("developer validation state=%q result=%#v", developer.ValidationStatus, runner.lastResult.OutgoingHandoff)
	}
}

func TestDurableApprovedTaskRestartsAfterApplyBeforeValidation(t *testing.T) {
	env := newDurableTestEnvironment(t)
	base := approvedTaskRunner()
	runner := &postApplyValidationRunner{scriptedRunner: base, workspaceChanged: true}
	lifecycle := &fakeProposalLifecycle{decisions: map[string]ProposalDecision{"approval-1": ProposalGranted}}
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events,
		DurableRuntimeOptions{Proposals: lifecycle})
	request := testRunRequest()
	request.RunID = "run-post-apply-validation-restart"
	_, _ = runtime.Run(context.Background(), request)
	record, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after application was durably recorded and before
	// post-apply validation. Resume must validate before transition.
	record.ApprovedTask.Proposals[0].Phase = "applied"
	record.ApprovedTask.Proposals[0].Result = &ProposalApplyResult{Success: true, TargetPath: "feature.txt"}
	if _, err = env.store.Checkpoint(context.Background(), record.Revision, record, nil); err != nil {
		t.Fatal(err)
	}
	restarted := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events,
		DurableRuntimeOptions{Proposals: lifecycle})
	state, err := restarted.Resume(context.Background(), request.RunID)
	if err != nil || state.Status != RunStatusCompleted || runner.validationCalls != 1 {
		t.Fatalf("restart state=%q calls=%d err=%v", state.Status, runner.validationCalls, err)
	}
}

func TestDurableApprovedTaskPostApplyValidationFailureStopsContinuation(t *testing.T) {
	env := newDurableTestEnvironment(t)
	base := approvedTaskRunner()
	runner := &postApplyValidationRunner{scriptedRunner: base, validationErr: errors.New("post-change validation failed")}
	lifecycle := &fakeProposalLifecycle{decisions: map[string]ProposalDecision{"approval-1": ProposalPending}}
	lifecycle.onApply = func() { runner.workspaceChanged = true }
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events,
		DurableRuntimeOptions{Proposals: lifecycle})
	request := testRunRequest()
	request.RunID = "run-post-apply-validation-failure"
	_, _ = runtime.Run(context.Background(), request)
	lifecycle.decisions["approval-1"] = ProposalGranted
	state, err := runtime.Resume(context.Background(), request.RunID)
	if err == nil || state.Status != RunStatusFailed || runner.validationCalls != 1 {
		t.Fatalf("failure state=%q calls=%d err=%v", state.Status, runner.validationCalls, err)
	}
	if got := runnerCalls(base); !reflect.DeepEqual(got, []Role{RolePlanner, RoleDeveloper}) {
		t.Fatalf("post-apply validation continued roles: %v", got)
	}
}

func TestDurableApprovedTaskRejectsMissingPostApplyValidator(t *testing.T) {
	env := newDurableTestEnvironment(t)
	base := approvedTaskRunner()
	lifecycle := &fakeProposalLifecycle{decisions: map[string]ProposalDecision{"approval-1": ProposalPending}}
	runtime := newDurableRuntimeForTest(t, validDefinition(), roleRunnerWithoutPostValidation{inner: base}, env.store, env.claimer, env.events,
		DurableRuntimeOptions{Proposals: lifecycle})
	request := testRunRequest()
	request.RunID = "run-missing-post-apply-validator"
	_, _ = runtime.Run(context.Background(), request)
	lifecycle.decisions["approval-1"] = ProposalGranted
	state, err := runtime.Resume(context.Background(), request.RunID)
	if err == nil || state.Status != RunStatusFailed || len(lifecycle.applyCalls) != 1 {
		t.Fatalf("state=%q apply=%d err=%v", state.Status, len(lifecycle.applyCalls), err)
	}
	if got := runnerCalls(base); !reflect.DeepEqual(got, []Role{RolePlanner, RoleDeveloper}) {
		t.Fatalf("missing validator continued roles: %v", got)
	}
}

func TestDurableApprovedTaskDenialFailsClosed(t *testing.T) {
	env := newDurableTestEnvironment(t)
	lifecycle := &fakeProposalLifecycle{decisions: map[string]ProposalDecision{"approval-1": ProposalPending}}
	runtime := newDurableRuntimeForTest(t, validDefinition(), approvedTaskRunner(), env.store, env.claimer, env.events,
		DurableRuntimeOptions{Proposals: lifecycle})
	request := testRunRequest()
	request.RunID = "run-proposal-denied"
	_, _ = runtime.Run(context.Background(), request)
	lifecycle.decisions["approval-1"], lifecycle.reasons = ProposalDenied, map[string]string{"approval-1": "operator denied"}
	state, err := runtime.Resume(context.Background(), request.RunID)
	if err == nil || state.Status != RunStatusFailed || len(lifecycle.applyCalls) != 0 {
		t.Fatalf("denial state=%q calls=%d err=%v", state.Status, len(lifecycle.applyCalls), err)
	}
	stored, loadErr := env.store.Load(context.Background(), request.RunID)
	if loadErr != nil {
		t.Fatalf("load denied run: %v", loadErr)
	}
	if stored.Phase != CheckpointTerminal || stored.Waiting != nil {
		t.Fatalf("denied terminal checkpoint phase=%q waiting=%+v, want no waiting state", stored.Phase, stored.Waiting)
	}
}

func TestDurableApprovedTaskRestartReconcilesWithoutDuplicateApply(t *testing.T) {
	env := newDurableTestEnvironment(t)
	lifecycle := &fakeProposalLifecycle{decisions: map[string]ProposalDecision{"approval-1": ProposalGranted}, reconciled: ProposalApplied}
	runner := approvedTaskRunner()
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events,
		DurableRuntimeOptions{Proposals: lifecycle})
	request := testRunRequest()
	request.RunID = "run-proposal-reconcile"
	_, _ = runtime.Run(context.Background(), request)
	record, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	record.ApprovedTask.Proposals[0].Phase = "applying"
	if _, err = env.store.Checkpoint(context.Background(), record.Revision, record, nil); err != nil {
		t.Fatal(err)
	}

	restarted := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events,
		DurableRuntimeOptions{Proposals: lifecycle})
	state, err := restarted.Resume(context.Background(), request.RunID)
	if err != nil || state.Status != RunStatusCompleted || len(lifecycle.applyCalls) != 0 {
		t.Fatalf("reconcile state=%q apply calls=%d err=%v", state.Status, len(lifecycle.applyCalls), err)
	}
}

func TestDurableApprovedTaskBoundedApplyRetry(t *testing.T) {
	env := newDurableTestEnvironment(t)
	lifecycle := &fakeProposalLifecycle{decisions: map[string]ProposalDecision{"approval-1": ProposalGranted}, reconciled: ProposalNotApplied, applyFails: 1}
	runtime := newDurableRuntimeForTest(t, validDefinition(), approvedTaskRunner(), env.store, env.claimer, env.events,
		DurableRuntimeOptions{Proposals: lifecycle})
	request := testRunRequest()
	request.RunID = "run-proposal-retry"
	_, _ = runtime.Run(context.Background(), request)
	state, err := runtime.Resume(context.Background(), request.RunID)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || state.Status != RunStatusPaused || len(lifecycle.applyCalls) != 1 {
		t.Fatalf("first apply state=%q calls=%d err=%v", state.Status, len(lifecycle.applyCalls), err)
	}
	state, err = runtime.Resume(context.Background(), request.RunID)
	if err != nil || state.Status != RunStatusCompleted || len(lifecycle.applyCalls) != 2 {
		t.Fatalf("retry state=%q calls=%d err=%v", state.Status, len(lifecycle.applyCalls), err)
	}
}
