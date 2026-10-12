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
	store     *approval.Store
	executor  *mutation.Executor
	workspace string
	onApply   func()
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
	r, err := l.executor.ApplyWithRun(ctx, op.RunID, op.ApprovalID, a.ApprovedBy)
	if err != nil {
		return ProposalApplyResult{}, err
	}
	if r.Success && l.onApply != nil {
		l.onApply()
	}
	return ProposalApplyResult{Success: r.Success, TargetPath: a.TargetPath, Message: r.Message}, nil
}
func (l actualPatchLifecycle) Reconcile(ctx context.Context, op ProposalOperation) (ProposalReconciliation, ProposalApplyResult, error) {
	a, err := l.store.Load(op.RunID, op.ApprovalID)
	if err != nil {
		return "", ProposalApplyResult{}, err
	}
	if a.PatchPlan == nil {
		return ProposalAmbiguous, ProposalApplyResult{}, nil
	}
	tree, err := gitx.WorktreeTree(ctx, l.workspace)
	if err != nil {
		return ProposalAmbiguous, ProposalApplyResult{}, nil
	}
	if tree == a.PatchPlan.ExpectedTree {
		return ProposalApplied, ProposalApplyResult{Success: true, TargetPath: a.TargetPath}, nil
	}
	if tree == a.PatchPlan.BaseTree {
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

type fakeProposalLifecycle struct {
	mu         sync.Mutex
	decisions  map[string]ProposalDecision
	reasons    map[string]string
	applyCalls []ProposalOperation
	reconciled ProposalReconciliation
	applyFails int
	onApply    func()
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
	return f.reconciled, ProposalApplyResult{Success: f.reconciled == ProposalApplied, TargetPath: "feature.txt"}, nil
}

func approvedTaskRunner() *scriptedRunner {
	runner := happyDurableRunner()
	result := runner.scripts[RoleDeveloper][0].result
	result.Proposals = []ProposalReference{{ProposalID: "proposal-1", ApprovalID: "approval-1"}}
	runner.scripts[RoleDeveloper][0].result = result
	return runner
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
