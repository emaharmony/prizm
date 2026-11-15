package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/policy"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

type e28Policy struct{}

func (e28Policy) Evaluate(policy.PolicyRequest) policy.PolicyDecision {
	return policy.PolicyDecision{Decision: policy.DecisionRequiresApproval, RuleID: "e28-synthetic-review"}
}

type e28IntegratedState struct {
	store   *multiagent.SQLiteDurableRunStore
	events  *event.SQLiteEventStore
	runtime *multiagent.DurableRuntime
	service *multiagent.FreewillApprovalService
}

func openE28IntegratedState(t *testing.T, root string) e28IntegratedState {
	t.Helper()
	store, err := multiagent.NewSQLiteDurableRunStore(filepath.Join(root, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := event.NewSQLiteEventStore(filepath.Join(root, "events.db"))
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	runtime, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, store,
		multiagent.FileRunClaimer{Root: filepath.Join(root, "claims")}, events, multiagent.DurableRuntimeOptions{})
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	service, err := multiagent.NewFreewillApprovalService(runtime, store,
		approval.NewStore(filepath.Join(root, "approval-runs")), e28Policy{}, "e28-policy")
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	return e28IntegratedState{store: store, events: events, runtime: runtime, service: service}
}

func TestE28PendingPromiseRevisionPreservesAuthorizedGovernedWork(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspace := newFreewillDispatchTestRepo(t)
	if err := os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, workspace, "", "git", "add", "base.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, workspace, "", "git", "commit", "-m", "add synthetic target"); err != nil {
		t.Fatal(err)
	}
	runsDir := filepath.Join(root, "child-runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	childApprovals := approval.NewStore(filepath.Join(root, "child-approvals"))
	dispatcher := &oneCycleChildDispatcher{runDir: runsDir, source: workspace, approvalStore: childApprovals}
	defer func() {
		if dispatcher.runtime != nil {
			_ = dispatcher.runtime.close()
		}
	}()
	const project, agent, owner, promise, runID = "e28-project", "e28-agent", "synthetic-owner", "promise-e28", "run-e28-authorized"
	if err := filepath.IsAbs(workspace); !err {
		t.Fatal("workspace must be absolute")
	}
	cfg := orchestrator.FreewillPromiseConfirmationConfig{Enabled: true, OwnerID: owner, PromiseID: promise,
		Scope: "review the specified local fixture", Deadline: "2026-11-12T17:00:00Z"}
	open := openE28IntegratedState(t, root)
	defer func() { _ = open.events.Close(); _ = open.store.Close() }()
	open.service.SetExecutionDispatcher(dispatcher)

	request := freewill.IntentionRequest{RequestID: "e28-request-1", AgentID: agent, Origin: freewill.OriginAgent,
		CandidateSource: freewill.SourceNew, Title: "Change one fixture", Goal: "Change base.txt to changed",
		ReasonSummary: "The request is bounded to one patch target and a separate patch grant.", RequestedCapabilities: []string{"code"},
		Scope: freewill.Scope{ProjectID: project, Workspace: workspace}, IdempotencyKey: "e28-idem-1", CorrelationID: "e28-corr-1"}
	now := time.Now().UTC()
	run := multiagent.DurableRun{SchemaVersion: multiagent.DurableRunSchemaVersion, Kind: multiagent.DurableRunKindFreewill,
		Phase: multiagent.CheckpointFreewillWake,
		State: multiagent.RunState{RunID: runID, WorkflowID: multiagent.FreewillWorkflowID, WorkflowVersion: 1,
			RequestIdentity: &multiagent.RequestIdentity{RequesterID: agent, IdempotencyKey: request.IdempotencyKey}, CreatedAt: now, UpdatedAt: now},
		Freewill: &multiagent.FreewillRunState{Lifecycle: freewill.StateProposed, Scope: request.Scope, WakeID: "e28-wake-1",
			Trigger: "scheduled", DedupeKey: request.IdempotencyKey, CorrelationID: request.CorrelationID,
			Candidates: []freewill.CandidateIntention{{CandidateID: "e28-candidate-1", Request: request}},
			ChoiceID:   "e28-candidate-1", ReasonSummary: "Selected the bounded review.", ExecutionDisabled: true}}
	if _, err := open.store.Create(ctx, run, nil); err != nil {
		t.Fatalf("create authorized work proposal: %v", err)
	}
	pendingApproval, err := open.service.RequestApproval(ctx, runID)
	if err != nil {
		t.Fatalf("request governed approval: %v", err)
	}
	approved, err := open.service.Decide(ctx, runID, pendingApproval.Freewill.ApprovalRef, "grant", "synthetic-owner", "")
	if err != nil {
		t.Fatalf("grant governed work: %v", err)
	}
	if approved.Freewill.ApprovalReceipt == nil || approved.Freewill.ApprovalReceipt.Decision != multiagent.FreewillApprovalGranted ||
		approved.Freewill.ExecutionDispatch == nil || approved.Freewill.ExecutionDispatch.Status != "accepted" || dispatcher.starts != 1 || dispatcher.runtime == nil {
		t.Fatalf("work was not governed and accepted: %#v", approved.Freewill)
	}
	childID := approved.Freewill.ExecutionDispatch.ChildRunID
	childPending, err := childApprovals.List(childID)
	if err != nil || len(childPending) != 1 || childPending[0].Status != approval.StatusPending {
		t.Fatalf("governed child did not pause for separate patch approval: %#v err=%v", childPending, err)
	}
	original := approved.Freewill.Candidates[0].Request
	originalReceipt := *approved.Freewill.ApprovalReceipt
	originalFingerprint := approved.Freewill.ApprovalFingerprint
	originalApprovalID := approved.Freewill.ApprovalRef
	originalChild := approved.Freewill.ExecutionDispatch.ChildRunID
	if dispatcher.manifest.SourceWorkspacePath != original.Scope.Workspace || dispatcher.manifest.Input.Workspace != dispatcher.workspace ||
		dispatcher.manifest.Input.Objective != "Freewill approved intention: "+original.Title+"\n\nGoal: "+original.Goal {
		t.Fatal("governed child manifest does not preserve the originally approved intention")
	}

	facts, err := newFreewillTerminalFactMemorySink(filepath.Join(root, "data"), project, workspace, open.events)
	if err != nil {
		t.Fatal(err)
	}
	promises, err := newFreewillPromiseConfirmationSink(facts, project, workspace, agent, cfg)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := promises.ProposePromiseRevision(ctx, owner, project, workspace, agent, promise, "e28-request-1", 1,
		"review the fixture and provide a concise handoff", "2026-11-13T17:00:00Z", "A changed scope or deadline may alter the user-facing promise.")
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Current.Revision != 1 || proposal.Current.Scope != cfg.Scope || proposal.Proposals[0].Status != "pending" {
		t.Fatalf("pending revision replaced the effective promise: %#v", proposal)
	}
	unauthorized := run
	unauthorized.State.RunID = "run-e28-without-approval"
	unauthorized.State.RequestIdentity = &multiagent.RequestIdentity{RequesterID: agent, IdempotencyKey: "e28-idem-unapproved"}
	unauthorized.Freewill = &multiagent.FreewillRunState{Lifecycle: freewill.StateProposed, Scope: request.Scope, WakeID: "e28-wake-unapproved",
		Trigger: "scheduled", DedupeKey: "e28-idem-unapproved", CorrelationID: "e28-corr-unapproved",
		Candidates: []freewill.CandidateIntention{{CandidateID: "candidate-unapproved", Request: request}}, ChoiceID: "candidate-unapproved"}
	if _, err = open.store.Create(ctx, unauthorized, nil); err != nil {
		t.Fatalf("create unapproved work: %v", err)
	}
	if _, err = open.service.DispatchApproved(ctx, unauthorized.State.RunID); err == nil {
		t.Fatal("pending promise revision granted a missing intention approval")
	}
	assertE28AuthorizedWorkUnchanged(t, ctx, open, runID, original, originalReceipt, originalFingerprint, originalApprovalID, originalChild)

	// Reopen parent and child at the exact durable patch-approval pause.
	if err = dispatcher.runtime.close(); err != nil {
		t.Fatal(err)
	}
	dispatcher.runtime = nil
	if err = open.events.Close(); err != nil {
		t.Fatal(err)
	}
	if err = open.store.Close(); err != nil {
		t.Fatal(err)
	}
	open = openE28IntegratedState(t, root)
	open.service.SetExecutionDispatcher(dispatcher)
	lifecycle := newApprovalProposalLifecycle(childApprovals, dispatcher.workspace, filepath.Join(runsDir, childID), workspace)
	dispatcher.runtime, err = openReferenceRuntimeWithInteraction(runsDir, dispatcher.manifest, dispatcher.runner, nil, lifecycle)
	if err != nil {
		t.Fatalf("reopen authorized child at patch approval: %v", err)
	}
	reopenedFacts, err := newFreewillTerminalFactMemorySink(filepath.Join(root, "data"), project, workspace, open.events)
	if err != nil {
		t.Fatal(err)
	}
	promises, err = newFreewillPromiseConfirmationSink(reopenedFacts, project, workspace, agent, cfg)
	if err != nil {
		t.Fatal(err)
	}
	reopenedPending, err := promises.ReadPromise(ctx, owner, project, workspace, agent, promise)
	if err != nil || reopenedPending.Current.Revision != 1 || len(reopenedPending.Proposals) != 1 || reopenedPending.Proposals[0].Status != "pending" {
		t.Fatalf("pending promise after restart=%#v err=%v", reopenedPending, err)
	}
	assertE28AuthorizedWorkUnchanged(t, ctx, open, runID, original, originalReceipt, originalFingerprint, originalApprovalID, originalChild)
	if err = open.service.ValidateAcceptedExecutionResume(ctx, runID); err != nil {
		t.Fatalf("existing governed child lost its original resume authorization while proposal was pending: %v", err)
	}
	childPaused, err := dispatcher.runtime.runtime.Inspect(ctx, childID)
	if err != nil || childPaused.State.Status != multiagent.RunStatusPaused {
		t.Fatalf("child did not survive restart at the same approval pause: status=%s err=%v", childPaused.State.Status, err)
	}
	patchApproval, err := childApprovals.Load(childID, childPending[0].ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if err = patchApproval.ApproveWithReason("patch-owner", "reviewed original one-file scope"); err != nil {
		t.Fatal(err)
	}
	if err = childApprovals.Save(patchApproval); err != nil {
		t.Fatal(err)
	}
	terminalState, err := dispatcher.runtime.runtime.Resume(ctx, childID)
	if err != nil || terminalState.Status != multiagent.RunStatusCompleted {
		t.Fatalf("original authorized child did not continue while promise revision was pending: status=%s err=%v", terminalState.Status, err)
	}
	childAfter, err := os.ReadFile(filepath.Join(dispatcher.workspace, "base.txt"))
	if err != nil || strings.ReplaceAll(string(childAfter), "\r\n", "\n") != "changed\n" {
		t.Fatalf("original approved patch result=%q err=%v", childAfter, err)
	}
	if sourceAfter, err := os.ReadFile(filepath.Join(workspace, "base.txt")); err != nil || strings.ReplaceAll(string(sourceAfter), "\r\n", "\n") != "base\n" {
		t.Fatalf("authorized child escaped isolated workspace: %q err=%v", sourceAfter, err)
	}
	if dispatcher.runner.session.commands != 0 {
		t.Fatalf("offline fake sandbox executed %d commands", dispatcher.runner.session.commands)
	}
	if err = finalizeTerminalReferenceRun(ctx, runsDir, &dispatcher.manifest, dispatcher.runtime, terminalState); err != nil {
		t.Fatalf("finalize continued synthetic child: %v", err)
	}
	result, err := freewillTerminalResultFromReferenceRun(ctx, runsDir, childID)
	if err != nil {
		t.Fatalf("read continued child terminal report: %v", err)
	}
	terminal, err := open.service.ReconcileExecutionTerminal(ctx, runID, result)
	if err != nil || terminal.Freewill == nil || terminal.Freewill.ExecutionDispatch == nil || terminal.Freewill.ExecutionDispatch.Status != "completed" {
		t.Fatalf("reconcile continued original child: %#v err=%v", terminal.Freewill, err)
	}
	stillPending, err := promises.ReadPromise(ctx, owner, project, workspace, agent, promise)
	if err != nil || stillPending.Current.Revision != 1 || stillPending.Proposals[0].Status != "pending" {
		t.Fatalf("work continuation silently accepted the pending revision: %#v err=%v", stillPending, err)
	}
	assertE28AuthorizedWorkUnchanged(t, ctx, open, runID, original, originalReceipt, originalFingerprint, originalApprovalID, originalChild)

	rejected, err := promises.DecidePromiseRevision(ctx, owner, project, workspace, agent, promise, reopenedPending.Proposals[0].ProposalID, "reject")
	if err != nil || rejected.Current.Revision != 1 || rejected.Proposals[0].Status != "reject" {
		t.Fatalf("rejection changed the effective promise: %#v err=%v", rejected, err)
	}
	assertE28AuthorizedWorkUnchanged(t, ctx, open, runID, original, originalReceipt, originalFingerprint, originalApprovalID, originalChild)

	second, err := promises.ProposePromiseRevision(ctx, owner, project, workspace, agent, promise, "e28-request-2", 1,
		"review the fixture and provide a concise handoff", "2026-11-14T17:00:00Z", "A later deadline hypothesis could affect the same user promise.")
	if err != nil {
		t.Fatal(err)
	}
	if second.Proposals[1].Status != "pending" || second.Current.Revision != 1 {
		t.Fatalf("second proposal not pending against revision 1: %#v", second)
	}
	accepted, err := promises.DecidePromiseRevision(ctx, owner, project, workspace, agent, promise, second.Proposals[1].ProposalID, "accept")
	if err != nil || accepted.Current.Revision != 2 || accepted.Current.Scope != "review the fixture and provide a concise handoff" {
		t.Fatalf("owner acceptance did not advance effective promise: %#v err=%v", accepted, err)
	}
	assertE28AuthorizedWorkUnchanged(t, ctx, open, runID, original, originalReceipt, originalFingerprint, originalApprovalID, originalChild)

	if err = dispatcher.runtime.close(); err != nil {
		t.Fatal(err)
	}
	dispatcher.runtime = nil
	if err = open.events.Close(); err != nil {
		t.Fatal(err)
	}
	if err = open.store.Close(); err != nil {
		t.Fatal(err)
	}
	open = openE28IntegratedState(t, root)
	open.service.SetExecutionDispatcher(dispatcher)
	finalFacts, err := newFreewillTerminalFactMemorySink(filepath.Join(root, "data"), project, workspace, open.events)
	if err != nil {
		t.Fatal(err)
	}
	promises, err = newFreewillPromiseConfirmationSink(finalFacts, project, workspace, agent, cfg)
	if err != nil {
		t.Fatal(err)
	}
	finalPromise, err := promises.ReadPromise(ctx, owner, project, workspace, agent, promise)
	if err != nil || finalPromise.Current.Revision != 2 || len(finalPromise.History) != 2 || finalPromise.Proposals[0].Status != "reject" {
		t.Fatalf("promise history after acceptance restart=%#v err=%v", finalPromise, err)
	}
	finalRun, err := open.store.Load(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if finalRun.Freewill.ApprovalReceipt == nil || *finalRun.Freewill.ApprovalReceipt != originalReceipt ||
		finalRun.Freewill.ApprovalFingerprint != originalFingerprint || finalRun.Freewill.ApprovalRef != originalApprovalID ||
		finalRun.Freewill.ExecutionDispatch == nil || finalRun.Freewill.ExecutionDispatch.ChildRunID != originalChild ||
		!reflect.DeepEqual(finalRun.Freewill.Candidates[0].Request, original) {
		t.Fatalf("accepted promise revision rewrote already authorized work: %#v", finalRun.Freewill)
	}
	if finalRun.Freewill.ExecutionDispatch.Status != "completed" {
		t.Fatalf("promise acceptance rewrote completed work status: %#v", finalRun.Freewill.ExecutionDispatch)
	}
	// A pending/accepted promise revision does not stand in for a work approval.
	if _, err = open.service.DispatchApproved(ctx, unauthorized.State.RunID); err == nil {
		t.Fatal("accepted promise revision granted a missing intention approval")
	}
}

func assertE28AuthorizedWorkUnchanged(t *testing.T, ctx context.Context, state e28IntegratedState, runID string,
	request freewill.IntentionRequest, receipt multiagent.FreewillApprovalReceipt, fingerprint, approvalID, childID string) {
	t.Helper()
	run, err := state.store.Load(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Freewill == nil || run.Freewill.ApprovalReceipt == nil || *run.Freewill.ApprovalReceipt != receipt ||
		run.Freewill.ApprovalFingerprint != fingerprint || run.Freewill.ApprovalRef != approvalID ||
		run.Freewill.ExecutionDispatch == nil || run.Freewill.ExecutionDispatch.ChildRunID != childID ||
		!reflect.DeepEqual(run.Freewill.Candidates[0].Request, request) {
		t.Fatalf("promise revision rewrote governed work authorization or scope: %#v", run.Freewill)
	}
}
