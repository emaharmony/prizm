package multiagent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

type fakeProposalLifecycle struct {
	mu         sync.Mutex
	decisions  map[string]ProposalDecision
	reasons    map[string]string
	applyCalls []ProposalOperation
	reconciled ProposalReconciliation
	applyFails int
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
	return ProposalApplyResult{Success: true, TargetPath: "feature.txt", Message: "applied"}, nil
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
