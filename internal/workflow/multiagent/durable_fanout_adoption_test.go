package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/event"
)

type adoptionProposalLifecycle struct {
	mu         sync.Mutex
	decision   ProposalDecision
	reconciled ProposalReconciliation
	applyCalls int
}

func (l *adoptionProposalLifecycle) Decision(context.Context, ProposalOperation) (ProposalDecision, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.decision, "test decision", nil
}

func (l *adoptionProposalLifecycle) Apply(context.Context, ProposalOperation) (ProposalApplyResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applyCalls++
	l.reconciled = ProposalApplied
	return ProposalApplyResult{Success: true, TargetPath: "tests/test_production_e2e_fake.py"}, nil
}

func (l *adoptionProposalLifecycle) Reconcile(context.Context, ProposalOperation) (ProposalReconciliation, ProposalApplyResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reconciled, ProposalApplyResult{Success: l.reconciled == ProposalApplied}, nil
}

type persistThenInterruptAdoptionCheckpoint struct {
	DurableRunStore
	interrupted bool
}

func (s *persistThenInterruptAdoptionCheckpoint) Checkpoint(ctx context.Context, revision int64, record DurableRun, events []event.Event) (DurableRun, error) {
	saved, err := s.DurableRunStore.Checkpoint(ctx, revision, record, events)
	if err == nil && !s.interrupted && record.PendingFanOutAdoption != nil && record.PendingTransition != nil {
		s.interrupted = true
		return saved, errInjectedCheckpoint
	}
	return saved, err
}

func fanOutAdoptionGraph(t *testing.T) *CompiledGraph {
	t.Helper()
	planner := roleNode("strategy-source-id", string(OutcomePlanReady), string(OutcomeCancelled))
	planner.Role = string(RolePlanner)
	developer := roleNode("implementation-source-id", string(OutcomeImplementationReady), string(OutcomeTerminalFailure))
	developer.Role = string(RoleDeveloper)
	tester := roleNode("verification-source-id", string(OutcomeTestsPassed))
	tester.Role = string(RoleTester)
	reviewer := roleNode("release-review-source-id", string(OutcomeReviewApproved))
	reviewer.Role = string(RoleReviewer)
	definition := WorkflowDefinition{APIVersion: SchemaAPIVersion, Kind: SchemaKind,
		Metadata: WorkflowMetadata{Name: "fanout-adoption", ID: "fanout-adoption", Version: "1.0.0"},
		Spec: WorkflowSpec{EntryNode: planner.ID, Nodes: []SchemaNode{planner, developer, tester, reviewer, terminalNode("done-source-id", "completed")},
			Edges: []SchemaEdge{
				edge("plan-to-implementation", planner.ID, developer.ID, string(OutcomePlanReady)),
				edge("implementation-to-test", developer.ID, tester.ID, string(OutcomeImplementationReady)),
				edge("test-to-review", tester.ID, reviewer.ID, string(OutcomeTestsPassed)),
				edge("review-to-done", reviewer.ID, "done-source-id", string(OutcomeReviewApproved)),
				edge("cancel-run", planner.ID, "done-source-id", string(OutcomeCancelled)),
				edge("developer-failed", developer.ID, "done-source-id", string(OutcomeTerminalFailure)),
			}}}
	graph, diagnostics, err := Compile(definition, nil, CompileOptions{})
	if err != nil {
		t.Fatalf("compile custom-source-ID workflow: %v (%s)", err, diagnostics.Error())
	}
	for role := range map[Role]bool{RolePlanner: true, RoleDeveloper: true, RoleTester: true, RoleReviewer: true} {
		got, ok := graph.NodeIDForRole(role)
		if !ok {
			t.Fatalf("compiled graph has no semantic node for role %s", role)
		}
		if got != nodeID(role) {
			t.Fatalf("compiled graph node for semantic role %s = %q, want normalized role ID %q", role, got, nodeID(role))
		}
	}
	return graph
}

func prepareFanOutAdoptionApproval(t *testing.T, env durableTestEnvironment, runID string, store DurableRunStore) (*DurableRuntime, *CompiledGraph, *recordingDelegationDispatcher, *recordingOutcomeSource, *scriptedRunner, *adoptionProposalLifecycle) {
	t.Helper()
	if store == nil {
		store = env.store
	}
	dispatch := &recordingDelegationDispatcher{store: env.store}
	source := &recordingOutcomeSource{store: env.store, runID: runID}
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RoleTester:   {{result: scriptedResult(OutcomeTestsPassed)}},
		RoleReviewer: {{result: scriptedResult(OutcomeReviewApproved)}},
	}}
	lifecycle := &adoptionProposalLifecycle{decision: ProposalGranted, reconciled: ProposalNotApplied}
	now := time.Date(2026, 10, 12, 16, 24, 0, 0, time.UTC)
	options := DurableRuntimeOptions{Delegation: &DurableDelegationOptions{Subject: "graph.roles", Dispatcher: dispatch, Outcomes: source, Deadline: time.Minute}, Proposals: lifecycle}
	options.Clock = func() time.Time { return now }
	graph := fanOutAdoptionGraph(t)
	runtime, err := NewDurableRuntime(graph, runner, store, env.claimer, env.events, options)
	if err != nil {
		t.Fatalf("new durable adoption runtime: %v", err)
	}
	state, err := runtime.Run(t.Context(), RunRequest{RunID: runID, Task: TaskReference{ID: "factory-task", Description: "resume production verification after simulated Studio blocking"}, WorkspaceID: "workspace-durable"})
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || state.Status != RunStatusPaused || len(dispatch.commands) != 1 {
		t.Fatalf("initial state=%s commands=%d err=%v", state.Status, len(dispatch.commands), err)
	}
	parent := scriptedResult(OutcomePlanReady)
	parent.FanOut = validFanOutPlan()
	parent.Metadata.WorkspaceID = "workspace-durable"
	parent.Metadata.StartedAt, parent.Metadata.FinishedAt = now, now.Add(time.Second)
	parentPayload, _ := json.Marshal(GraphRoleOutcome{Result: parent})
	parentOutcome := matchingOutcome(dispatch.commands[0], "parent-fanout", event.OutcomeSucceeded, 1, parentPayload)
	parentOutcome.OccurredAt = now
	source.outcomes = []event.Outcome{parentOutcome}
	if _, err = runtime.Resume(t.Context(), runID); !errors.As(err, &waiting) || len(dispatch.commands) != 4 {
		t.Fatalf("fan-out state wait=%v commands=%d err=%v", waiting != nil, len(dispatch.commands), err)
	}
	record, loadErr := env.store.Load(t.Context(), runID)
	if loadErr != nil || record.DelegationJoin == nil {
		t.Fatalf("join checkpoint=%#v err=%v", record.DelegationJoin, loadErr)
	}
	source.outcomes = nil
	for i, child := range record.DelegationJoin.Children {
		outcome := OutcomePlanReady
		result := scriptedResult(outcome)
		switch child.Lane {
		case FanOutImplementation:
			result = scriptedResult(OutcomeImplementationReady)
			result.Proposals = []ProposalReference{{ProposalID: "factory-test-proposal", ApprovalID: "factory-test-approval"}}
		case FanOutReview:
			result = scriptedResult(OutcomeReviewApproved)
		}
		result.Metadata.WorkspaceID = "workspace-durable"
		result.Metadata.StartedAt, result.Metadata.FinishedAt = now, now.Add(time.Duration(i+1)*time.Second)
		result.Metadata.Provider, result.Metadata.Model = "ollama", "glm-5.3:cloud"
		result.TokenUsage = cost.TokenUsage{PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10, EstimatedCostUsd: 0.01}
		childPayload, _ := json.Marshal(GraphRoleOutcome{Result: result})
		command := event.Command{EventID: child.CommandEventID, RunID: runID, TaskID: child.ChildID, DelegationID: child.DelegationID,
			JoinID: child.JoinID, Lane: string(child.Lane), CorrelationID: child.CorrelationID, IdempotencyKey: child.DeliveryKey}
		childOutcome := matchingOutcome(command, "child-"+string(rune('a'+i)), event.OutcomeSucceeded, 1, childPayload)
		childOutcome.OccurredAt = now
		source.outcomes = append(source.outcomes, childOutcome)
	}
	if _, err = runtime.Resume(t.Context(), runID); !errors.As(err, &waiting) {
		t.Fatalf("proposal checkpoint err=%v", err)
	}
	joined := queryEventsForTest(t, env.events, runID, event.EventMultiAgentFanoutJoined)
	if len(joined) != 1 {
		t.Fatalf("fan-out joined audit events = %d, want 1", len(joined))
	}
	rawSummaries, err := json.Marshal(joined[0].Payload["child_summaries"])
	if err != nil {
		t.Fatalf("marshal child summaries: %v", err)
	}
	var summaries []event.MultiAgentFanoutChildSummary
	if err := json.Unmarshal(rawSummaries, &summaries); err != nil || len(summaries) != 3 {
		t.Fatalf("joined child summaries = %s, err=%v; want three bounded execution records", rawSummaries, err)
	}
	for _, summary := range summaries {
		if summary.ChildID == "" || summary.ExecutionKey == "" || summary.DeliveryKey == "" || summary.Status != string(event.OutcomeSucceeded) ||
			summary.Provider != "ollama" || summary.Model != "glm-5.3:cloud" || summary.TokenUsage.TotalTokens == 0 ||
			summary.StartedAt == "" || summary.FinishedAt == "" || summary.DurationMs < 1000 {
			t.Fatalf("joined physical execution summary lost safe identity/usage/timing: %#v", summary)
		}
	}
	record, loadErr = env.store.Load(t.Context(), runID)
	if loadErr != nil || record.ApprovedTask == nil || record.ApprovedTask.FanOutAdoption == nil || len(record.ApprovedTask.Proposals) != 1 {
		t.Fatalf("approval checkpoint candidate=%#v err=%v", record.ApprovedTask, loadErr)
	}
	return runtime, graph, dispatch, source, runner, lifecycle
}

func TestDurableFanOutAdoptionRunsCanonicalSuccessorGraphOnceAfterRestart(t *testing.T) {
	env := newDurableTestEnvironment(t)
	store := &persistThenInterruptAdoptionCheckpoint{DurableRunStore: env.store}
	runtime, graph, dispatch, source, runner, lifecycle := prepareFanOutAdoptionApproval(t, env, "run-adopt-fanout", store)
	if _, err := runtime.Resume(t.Context(), "run-adopt-fanout"); !errors.Is(err, errInjectedCheckpoint) {
		t.Fatalf("expected restart boundary after durable adoption receipt, err=%v", err)
	}
	checkpoint, err := env.store.Load(t.Context(), "run-adopt-fanout")
	if err != nil || checkpoint.Phase != CheckpointTransitionPending || checkpoint.PendingFanOutAdoption == nil || checkpoint.PendingTransition == nil {
		t.Fatalf("adoption restart checkpoint phase=%s adoption=%#v transition=%#v err=%v", checkpoint.Phase, checkpoint.PendingFanOutAdoption, checkpoint.PendingTransition, err)
	}
	restartOptions := DurableRuntimeOptions{Delegation: runtime.delegation, Proposals: lifecycle}
	restartOptions.Clock = func() time.Time { return time.Date(2026, 10, 12, 16, 24, 0, 0, time.UTC) }
	restarted, err := NewDurableRuntime(graph, runner, store, env.claimer, env.events, restartOptions)
	if err != nil {
		t.Fatalf("restart adoption runtime: %v", err)
	}
	state, err := restarted.Resume(t.Context(), "run-adopt-fanout")
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) {
		t.Fatalf("resumed state=%s err=%v", state.Status, err)
	}
	if len(runnerCalls(runner)) != 0 {
		t.Fatalf("local runner unexpectedly executed during delegated graph: %v", runnerCalls(runner))
	}
	commands := append([]event.Command(nil), dispatch.commands...)
	if len(commands) != 5 {
		t.Fatalf("dispatch count after adoption=%d, want parent + three fan-out lanes + Tester", len(commands))
	}
	var testerCommand GraphRoleCommand
	if err := json.Unmarshal(commands[4].Payload, &testerCommand); err != nil || testerCommand.Role != RoleTester {
		t.Fatalf("next dispatch is not the configured Tester: role=%s err=%v", testerCommand.Role, err)
	}
	testerResult := scriptedResult(OutcomeTestsPassed)
	testerResult.Metadata.WorkspaceID = "workspace-durable"
	testerPayload, _ := json.Marshal(GraphRoleOutcome{Result: testerResult})
	testerOutcome := matchingOutcome(commands[4], "adopt-test-passed", event.OutcomeSucceeded, 1, testerPayload)
	testerOutcome.OccurredAt = time.Date(2026, 10, 12, 16, 24, 0, 0, time.UTC)
	source.outcomes = []event.Outcome{testerOutcome}
	if _, err = restarted.Resume(t.Context(), "run-adopt-fanout"); !errors.As(err, &waiting) {
		t.Fatalf("Tester outcome did not advance to fresh Reviewer: err=%v", err)
	}
	commands = append([]event.Command(nil), dispatch.commands...)
	if len(commands) != 6 {
		t.Fatalf("dispatch count after Tester=%d", len(commands))
	}
	var reviewerCommand GraphRoleCommand
	if err := json.Unmarshal(commands[5].Payload, &reviewerCommand); err != nil || reviewerCommand.Role != RoleReviewer {
		t.Fatalf("final dispatch is not a fresh Reviewer: role=%s err=%v", reviewerCommand.Role, err)
	}
	reviewerResult := scriptedResult(OutcomeReviewApproved)
	reviewerResult.Metadata.WorkspaceID = "workspace-durable"
	reviewerPayload, _ := json.Marshal(GraphRoleOutcome{Result: reviewerResult})
	reviewerOutcome := matchingOutcome(commands[5], "final-review-passed", event.OutcomeSucceeded, 1, reviewerPayload)
	reviewerOutcome.OccurredAt = time.Date(2026, 10, 12, 16, 24, 0, 0, time.UTC)
	source.outcomes = []event.Outcome{reviewerOutcome}
	state, err = restarted.Resume(t.Context(), "run-adopt-fanout")
	if err != nil || state.Status != RunStatusCompleted {
		t.Fatalf("final state=%s err=%v", state.Status, err)
	}
	if got := runnerCalls(runner); len(got) != 0 {
		t.Fatalf("delegated graph used local runner unexpectedly: %v", got)
	}
	if lifecycle.applyCalls != 1 {
		t.Fatalf("approved proposal applied %d times, want once", lifecycle.applyCalls)
	}
	developer := state.RoleStates[RoleDeveloper]
	if developer.Visits != 1 || developer.TokenUsage.TotalTokens != 10 || state.BudgetUsage.Tokens.TotalTokens != 40 {
		t.Fatalf("adopted developer accounting=%#v total usage=%#v", developer, state.BudgetUsage.Tokens)
	}
	if checkpoint.PendingFanOutAdoption == nil || checkpoint.PendingFanOutAdoption.Result.TokenUsage.TotalTokens != 10 {
		t.Fatal("restart checkpoint did not retain the source execution metrics")
	}
	completed := queryEventsForTest(t, env.events, "run-adopt-fanout", event.EventMultiAgentRoleCompleted)
	var adopted bool
	for _, evt := range completed {
		if evt.Payload["role"] == string(RoleDeveloper) {
			adopted = evt.Payload["execution_mode"] == "approved_fanout_adoption" &&
				evt.Payload["source_join_id"] == checkpoint.PendingFanOutAdoption.JoinID &&
				evt.Payload["source_child_id"] == checkpoint.PendingFanOutAdoption.ChildID
		}
	}
	if !adopted {
		t.Fatalf("developer completion did not expose its adopted source identity: events=%#v", completed)
	}
}

func TestFanOutAdoptionFailsClosedForScopeDecisionAndTreeMismatch(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runtime, _, _, _, _, lifecycle := prepareFanOutAdoptionApproval(t, env, "run-adopt-guards", nil)
	record, err := env.store.Load(t.Context(), "run-adopt-guards")
	if err != nil {
		t.Fatal(err)
	}
	task := *record.ApprovedTask
	task.Proposals[0].Phase = "applied"
	task.Proposals[0].Result = &ProposalApplyResult{Success: true}
	tests := []struct {
		name   string
		mutate func(*ApprovedTaskState, *adoptionProposalLifecycle)
	}{
		{"different goal", func(task *ApprovedTaskState, _ *adoptionProposalLifecycle) {
			task.FanOutAdoption.Task.ID = "other-task"
		}},
		{"different workspace", func(task *ApprovedTaskState, _ *adoptionProposalLifecycle) {
			task.FanOutAdoption.WorkspaceID = "other-workspace"
		}},
		{"different workflow", func(task *ApprovedTaskState, _ *adoptionProposalLifecycle) {
			task.FanOutAdoption.WorkflowFingerprint = "other-workflow"
		}},
		{"different successor node", func(task *ApprovedTaskState, _ *adoptionProposalLifecycle) {
			task.FanOutAdoption.ExpectedNodeID = "other-developer-node"
		}},
		{"later developer visit", func(task *ApprovedTaskState, _ *adoptionProposalLifecycle) { task.FanOutAdoption.ExpectedVisit++ }},
		{"attempt exceeds persisted allowance", func(task *ApprovedTaskState, _ *adoptionProposalLifecycle) {
			a := task.FanOutAdoption
			a.RecoveryNumber = a.MaxRecoveries + 1
			a.AttemptNumber = a.RecoveryNumber + 1
			a.AttemptID = retryAttemptID(a.ChildExecutionKey, a.AttemptNumber)
		}},
		{"delivery exceeds recoveries", func(task *ApprovedTaskState, _ *adoptionProposalLifecycle) {
			a := task.FanOutAdoption
			a.DeliveryNumber = a.RecoveryNumber + 1
			a.DeliveryKey = fmt.Sprintf("%s:%d", a.DelegationID, a.DeliveryNumber)
			a.DeliveryAttemptID = fmt.Sprintf("%s:delivery:%d", a.ChildExecutionKey, a.DeliveryNumber)
			a.CommandEventID = event.CommandEventID(a.DeliveryKey)
			a.CorrelationID = fmt.Sprintf("%s:%s:delivery:%d", a.RunID, a.ChildExecutionKey, a.DeliveryNumber)
		}},
		{"different parent visit", func(task *ApprovedTaskState, _ *adoptionProposalLifecycle) {
			task.FanOutAdoption.ParentExecutionKey = "another-parent-execution"
		}},
		{"denied approval", func(_ *ApprovedTaskState, lifecycle *adoptionProposalLifecycle) { lifecycle.decision = ProposalDenied }},
		{"unapplied tree", func(_ *ApprovedTaskState, lifecycle *adoptionProposalLifecycle) {
			lifecycle.reconciled = ProposalNotApplied
		}},
		{"ambiguous tree", func(_ *ApprovedTaskState, lifecycle *adoptionProposalLifecycle) {
			lifecycle.reconciled = ProposalAmbiguous
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := task
			copy.Proposals = append([]ProposalProgress(nil), task.Proposals...)
			copy.FanOutAdoption = cloneFanOutAdoption(task.FanOutAdoption)
			lifecycle.decision, lifecycle.reconciled = ProposalGranted, ProposalApplied
			test.mutate(&copy, lifecycle)
			if _, err := runtime.verifyFanOutAdoptionApproval(t.Context(), record, &copy); err == nil {
				t.Fatal("unsafe adoption evidence accepted")
			}
		})
	}
}

func TestPendingFanOutAdoptionRechecksTreeAfterRestart(t *testing.T) {
	env := newDurableTestEnvironment(t)
	store := &persistThenInterruptAdoptionCheckpoint{DurableRunStore: env.store}
	runtime, graph, dispatch, _, runner, lifecycle := prepareFanOutAdoptionApproval(t, env, "run-adopt-tree-changed", store)
	if _, err := runtime.Resume(t.Context(), "run-adopt-tree-changed"); !errors.Is(err, errInjectedCheckpoint) {
		t.Fatalf("expected durable adoption checkpoint, err=%v", err)
	}
	lifecycle.mu.Lock()
	lifecycle.reconciled = ProposalAmbiguous
	lifecycle.mu.Unlock()
	options := DurableRuntimeOptions{Delegation: runtime.delegation, Proposals: lifecycle}
	options.Clock = func() time.Time { return time.Date(2026, 10, 12, 16, 24, 0, 0, time.UTC) }
	restarted, err := NewDurableRuntime(graph, runner, store, env.claimer, env.events, options)
	if err != nil {
		t.Fatalf("restart adoption runtime: %v", err)
	}
	state, err := restarted.Resume(t.Context(), "run-adopt-tree-changed")
	if err == nil || state.Status != RunStatusFailed || state.RoleStates[RoleDeveloper].Status != RoleStatusFailed {
		t.Fatalf("changed tree did not fail closed: state=%s developer=%s err=%v", state.Status, state.RoleStates[RoleDeveloper].Status, err)
	}
	if lifecycle.applyCalls != 1 || len(dispatch.commands) != 4 || len(runnerCalls(runner)) != 0 {
		t.Fatalf("ambiguous post-restart receipt retried mutation or continued: apply=%d dispatch=%d local=%v", lifecycle.applyCalls, len(dispatch.commands), runnerCalls(runner))
	}
}
