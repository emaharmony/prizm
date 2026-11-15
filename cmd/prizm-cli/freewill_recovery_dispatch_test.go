package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/policy"
	multiagent "github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestFreewillRecoverPendingResumesAcceptedTerminalChildAndRebuildsReport(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runDir := filepath.Join(root, "runs")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	const childRunID = "run_aaaaaaaaaaaaaaaaaaaa"
	childInput := multiagent.ReferenceWorkflowInput{Objective: "rebuild terminal report", Workspace: workspace}
	childDefinition, err := multiagent.ApplyReferenceOverrides(multiagent.DefaultReferenceDefinition(), childInput)
	if err != nil {
		t.Fatal(err)
	}
	canonicalWorkspace, workspaceID, err := referenceWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	manifest := referenceWorkflowManifest{SchemaVersion: referenceManifestSchemaVersion, RunID: childRunID,
		WorkflowID: multiagent.ReferenceWorkflowID, Input: childInput, Definition: childDefinition,
		WorkspaceID: workspaceID, WorkspacePath: canonicalWorkspace, SandboxApprovalMode: true}
	if err := writeReferenceManifest(runDir, manifest); err != nil {
		t.Fatal(err)
	}
	childRuntime, err := openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	childIdentity := &multiagent.RequestIdentity{RequesterID: "agent-recovery", SessionID: "correlation-recovery", Source: "freewill", IdempotencyKey: "child-idempotency"}
	if _, err := childRuntime.runtime.Accept(ctx, multiagent.RunRequest{RunID: childRunID,
		Task: multiagent.TaskReference{ID: "task_" + childRunID, Description: "recovery fixture"}, WorkspaceID: workspaceID, Identity: childIdentity}); err != nil {
		_ = childRuntime.close()
		t.Fatal(err)
	}
	terminal, err := childRuntime.runtime.FailAcceptedRun(ctx, childRunID, "validation_profile_configuration_changed")
	closeErr := childRuntime.close()
	if err == nil || err.Error() != "validation_profile_configuration_changed" || closeErr != nil {
		t.Fatalf("make child terminal err=%v close=%v", err, closeErr)
	}
	if !terminal.Status.Terminal() {
		t.Fatalf("fixture child is not terminal: %s", terminal.Status)
	}

	store, err := multiagent.NewSQLiteDurableRunStore(filepath.Join(root, "freewill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := freewill.IntentionRequest{RequestID: "request-recovery", AgentID: "agent-recovery", Origin: freewill.OriginAgent,
		CandidateSource: freewill.SourceNew, Title: "Recover child report", Goal: "Resume one already accepted run",
		ReasonSummary: "This deterministic fixture tests startup recovery.", RequestedCapabilities: []string{"code"},
		Scope: freewill.Scope{ProjectID: "project-recovery", Workspace: workspace}, IdempotencyKey: "idem-recovery", CorrelationID: "corr-recovery"}
	fingerprintBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	fingerprintSum := sha256.Sum256(fingerprintBytes)
	fingerprint := hex.EncodeToString(fingerprintSum[:])
	now := time.Now().UTC()
	approvalStore := approval.NewStore(runDir)
	approvalRecord, err := approval.NewIntentionApproval("run_freewill_recovery_parent", request.CorrelationID, request.AgentID, request.Scope.ProjectID,
		approval.IntentionApprovalRef{RequestID: request.RequestID, RequestFingerprint: fingerprint, ScopeSummary: "project-recovery workspace",
			Capabilities: request.RequestedCapabilities}, approval.PolicyDecision{Decision: approval.DecisionRequiresApproval, Reason: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	approvalID := approvalRecord.ApprovalID
	approvalRecord.Status, approvalRecord.ApprovedBy, approvalRecord.ApprovedAt = approval.StatusApproved, "owner", &now
	if err := approvalStore.Save(approvalRecord); err != nil {
		t.Fatal(err)
	}
	parent := multiagent.DurableRun{SchemaVersion: multiagent.DurableRunSchemaVersion, Kind: multiagent.DurableRunKindFreewill,
		Phase: multiagent.CheckpointFreewillWake,
		State: multiagent.RunState{SchemaVersion: multiagent.RunStateSchemaVersion, RunID: "run_freewill_recovery_parent",
			WorkflowID: multiagent.FreewillWorkflowID, WorkflowVersion: 1, CurrentTask: multiagent.TaskReference{ID: "freewill-wake"},
			Status: multiagent.RunStatusCreated, RequestIdentity: &multiagent.RequestIdentity{RequesterID: request.AgentID,
				SessionID: request.CorrelationID, Source: "freewill_scheduler", IdempotencyKey: request.IdempotencyKey},
			CreatedAt: now, UpdatedAt: now},
		Freewill: &multiagent.FreewillRunState{Lifecycle: freewill.StateRunning, Scope: request.Scope, WakeID: "wake-recovery",
			Trigger: "scheduled", DedupeKey: request.IdempotencyKey, CorrelationID: request.CorrelationID,
			Candidates: []freewill.CandidateIntention{{CandidateID: "candidate-recovery", Request: request}}, ChoiceID: "candidate-recovery",
			ExecutionDisabled: true, ApprovalRef: approvalID, ApprovalFingerprint: fingerprint,
			ApprovalReceipt: &multiagent.FreewillApprovalReceipt{ApprovalID: approvalID, RequestFingerprint: fingerprint,
				Decision: multiagent.FreewillApprovalGranted},
			ExecutionDispatch: &multiagent.FreewillExecutionDispatch{Status: "accepted", ChildRunID: childRunID,
				ReportRef: "multiagent_report.json", UpdatedAt: now}}}
	if _, err := store.Create(ctx, parent, nil); err != nil {
		t.Fatalf("persist approved parent fixture: %v", err)
	}
	parentEvents, err := event.NewSQLiteEventStore(filepath.Join(root, "freewill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer parentEvents.Close()
	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		t.Fatal(err)
	}
	parentRuntime, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, store,
		multiagent.FileRunClaimer{Root: filepath.Join(root, "claims")}, parentEvents, multiagent.DurableRuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := multiagent.NewFreewillApprovalService(parentRuntime, store, approvalStore,
		policy.NewEvaluator(policy.NewRegistry()), "freewill-policy")
	if err != nil {
		t.Fatal(err)
	}
	controls, err := multiagent.NewFreewillOperatorControl(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controls.SetHalted(ctx, request.AgentID, request.Scope, true, "owner"); err != nil {
		t.Fatal(err)
	}
	runtime := &freewillServeRuntime{store: store, executionEnabled: true, runDir: runDir,
		approvals: approvals, configPath: filepath.Join(root, "prizm.yaml"), resumingChildren: make(map[string]bool)}
	scheduledTicks := 0
	handleTick := func(context.Context) error {
		scheduledTicks++
		return nil
	}
	reportPath := filepath.Join(runDir, childRunID, "multiagent_report.json")
	if err := os.WriteFile(reportPath, []byte("{malformed report"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runFreewillScheduledTick(ctx, runtime.recoverPending, handleTick); err == nil {
		t.Fatal("invalid report allowed parent terminal projection")
	}
	if scheduledTicks != 0 {
		t.Fatalf("scheduled tick admitted new work after accepted-child recovery failed: ticks=%d", scheduledTicks)
	}
	unchanged, err := store.Load(ctx, parent.State.RunID)
	if err != nil || unchanged.Freewill.ExecutionDispatch.Status != "accepted" || unchanged.Freewill.ExecutionDispatch.ChildRunID != childRunID {
		t.Fatalf("invalid report changed accepted parent: dispatch=%#v err=%v", unchanged.Freewill.ExecutionDispatch, err)
	}
	if err := os.Remove(reportPath); err != nil {
		t.Fatal(err)
	}
	if err := runFreewillScheduledTick(ctx, runtime.recoverPending, handleTick); err != nil {
		t.Fatalf("scheduled recovery retry: %v", err)
	}
	if scheduledTicks != 1 {
		t.Fatalf("later tick was not admitted after accepted-child recovery: ticks=%d", scheduledTicks)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("terminal child report was not regenerated: %v", err)
	}
	var report multiagent.ReferenceRunReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode regenerated report: %v", err)
	}
	if report.RunID != childRunID || report.FinalStatus != terminal.Status {
		t.Fatalf("regenerated report mismatch: run=%q status=%q want run=%q status=%q", report.RunID, report.FinalStatus, childRunID, terminal.Status)
	}
	projected, err := store.Load(ctx, parent.State.RunID)
	reportSHA := sha256.Sum256(data)
	if err != nil || projected.Freewill.ExecutionDispatch.Status != "execution_failed" ||
		projected.Freewill.ExecutionDispatch.ChildRunID != childRunID ||
		projected.Freewill.ExecutionDispatch.ChildStatus != string(terminal.Status) ||
		projected.Freewill.ExecutionDispatch.ReportSHA256 != hex.EncodeToString(reportSHA[:]) {
		t.Fatalf("terminal child did not project to parent: dispatch=%#v err=%v", projected.Freewill.ExecutionDispatch, err)
	}
	revision := projected.Revision
	if err := runFreewillScheduledTick(ctx, runtime.recoverPending, handleTick); err != nil {
		t.Fatalf("repeat recovery: %v", err)
	}
	unchanged, err = store.Load(ctx, parent.State.RunID)
	if err != nil || unchanged.Revision != revision || unchanged.Freewill.ExecutionDispatch.ChildRunID != childRunID || scheduledTicks != 2 {
		t.Fatalf("terminal recovery replay changed identity/state or blocked tick: revision=%d want=%d child=%q ticks=%d err=%v",
			unchanged.Revision, revision, unchanged.Freewill.ExecutionDispatch.ChildRunID, scheduledTicks, err)
	}
}

func TestFreewillStartupHaltCancelsAcceptedChildWithoutResurrection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runDir := filepath.Join(root, "runs")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	const parentRunID = "run_freewill_halted_recovery_parent"
	const childRunID = "run_bbbbbbbbbbbbbbbbbbbb"
	childInput := multiagent.ReferenceWorkflowInput{Objective: "startup halted child recovery", Workspace: workspace}
	definition, err := multiagent.ApplyReferenceOverrides(multiagent.DefaultReferenceDefinition(), childInput)
	if err != nil {
		t.Fatal(err)
	}
	canonicalWorkspace, workspaceID, err := referenceWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	manifest := referenceWorkflowManifest{SchemaVersion: referenceManifestSchemaVersion, RunID: childRunID,
		WorkflowID: multiagent.ReferenceWorkflowID, Input: childInput, Definition: definition,
		WorkspaceID: workspaceID, WorkspacePath: canonicalWorkspace, SandboxApprovalMode: true}
	if err := writeReferenceManifest(runDir, manifest); err != nil {
		t.Fatal(err)
	}
	child, err := openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	childIdentity := &multiagent.RequestIdentity{RequesterID: "agent-halt-recovery", SessionID: "corr-halt-recovery", Source: "freewill", IdempotencyKey: "child-halt-recovery"}
	if _, err := child.runtime.Accept(ctx, multiagent.RunRequest{RunID: childRunID,
		Task:        multiagent.TaskReference{ID: "task_" + childRunID, Description: multiagent.ReferenceTaskDescription(childInput)},
		WorkspaceID: workspaceID, Identity: childIdentity}); err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	if err := child.close(); err != nil {
		t.Fatal(err)
	}

	store, err := multiagent.NewSQLiteDurableRunStore(filepath.Join(root, "freewill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := freewill.IntentionRequest{RequestID: "request-halt-recovery", AgentID: "agent-halt-recovery", Origin: freewill.OriginAgent,
		CandidateSource: freewill.SourceNew, Title: "Cancel after startup halt", Goal: "Do not resume the approved child after emergency halt",
		ReasonSummary: "Synthetic startup cancellation fixture.", RequestedCapabilities: []string{"code"},
		Scope: freewill.Scope{ProjectID: "project-halt-recovery", Workspace: workspace}, IdempotencyKey: "idem-halt-recovery", CorrelationID: "corr-halt-recovery"}
	fingerprintBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	fingerprintSum := sha256.Sum256(fingerprintBytes)
	fingerprint := hex.EncodeToString(fingerprintSum[:])
	now := time.Now().UTC()
	approvalStore := approval.NewStore(runDir)
	approvalRecord, err := approval.NewIntentionApproval(parentRunID, request.CorrelationID, request.AgentID, request.Scope.ProjectID,
		approval.IntentionApprovalRef{RequestID: request.RequestID, RequestFingerprint: fingerprint, ScopeSummary: "project-halt-recovery workspace",
			Capabilities: request.RequestedCapabilities}, approval.PolicyDecision{Decision: approval.DecisionRequiresApproval, Reason: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	approvalRecord.Status, approvalRecord.ApprovedBy, approvalRecord.ApprovedAt = approval.StatusApproved, "owner", &now
	if err := approvalStore.Save(approvalRecord); err != nil {
		t.Fatal(err)
	}
	parent := multiagent.DurableRun{SchemaVersion: multiagent.DurableRunSchemaVersion, Kind: multiagent.DurableRunKindFreewill,
		Phase: multiagent.CheckpointFreewillWake,
		State: multiagent.RunState{SchemaVersion: multiagent.RunStateSchemaVersion, RunID: parentRunID,
			WorkflowID: multiagent.FreewillWorkflowID, WorkflowVersion: 1, CurrentTask: multiagent.TaskReference{ID: "freewill-wake"},
			Status: multiagent.RunStatusCreated, RequestIdentity: &multiagent.RequestIdentity{RequesterID: request.AgentID,
				SessionID: request.CorrelationID, Source: "freewill_scheduler", IdempotencyKey: request.IdempotencyKey},
			CreatedAt: now, UpdatedAt: now},
		Freewill: &multiagent.FreewillRunState{Lifecycle: freewill.StateRunning, Scope: request.Scope, WakeID: "wake-halt-recovery",
			Trigger: "scheduled", DedupeKey: request.IdempotencyKey, CorrelationID: request.CorrelationID,
			Candidates: []freewill.CandidateIntention{{CandidateID: "candidate-halt-recovery", Request: request}},
			ChoiceID:   "candidate-halt-recovery", ExecutionDisabled: true, ApprovalRef: approvalRecord.ApprovalID,
			ApprovalFingerprint: fingerprint, ApprovalReceipt: &multiagent.FreewillApprovalReceipt{ApprovalID: approvalRecord.ApprovalID,
				RequestFingerprint: fingerprint, Decision: multiagent.FreewillApprovalGranted},
			ExecutionDispatch: &multiagent.FreewillExecutionDispatch{Status: "accepted", ChildRunID: childRunID,
				ReportRef: "multiagent_report.json", UpdatedAt: now}}}
	if _, err := store.Create(ctx, parent, nil); err != nil {
		t.Fatalf("persist accepted parent fixture: %v", err)
	}
	parentEvents, err := event.NewSQLiteEventStore(filepath.Join(root, "freewill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer parentEvents.Close()
	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		t.Fatal(err)
	}
	parentRuntime, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, store,
		multiagent.FileRunClaimer{Root: filepath.Join(root, "claims")}, parentEvents, multiagent.DurableRuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := multiagent.NewFreewillApprovalService(parentRuntime, store, approvalStore,
		policy.NewEvaluator(policy.NewRegistry()), "freewill-policy")
	if err != nil {
		t.Fatal(err)
	}
	control, err := multiagent.NewFreewillOperatorControl(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.SetHalted(ctx, request.AgentID, request.Scope, true, "owner"); err != nil {
		t.Fatal(err)
	}
	serve := &freewillServeRuntime{store: store, approvals: approvals, control: control, executionEnabled: true,
		runDir: runDir, configPath: filepath.Join(root, "prizm.yaml")}
	if err := serve.recoverPending(ctx); err != nil {
		t.Fatalf("recover halted accepted child: %v", err)
	}
	projected, err := store.Load(ctx, parentRunID)
	if err != nil || projected.Freewill.ExecutionDispatch.Status != "execution_cancelled" ||
		projected.Freewill.ExecutionDispatch.ChildStatus != string(multiagent.RunStatusCancelled) {
		t.Fatalf("halted child did not become durably canceled: dispatch=%#v err=%v", projected.Freewill.ExecutionDispatch, err)
	}
	terminal, err := freewillReferenceChildIsTerminal(ctx, runDir, childRunID)
	if err != nil || !terminal {
		t.Fatalf("child cancellation was not terminal: terminal=%v err=%v", terminal, err)
	}
	if _, err := control.SetHalted(ctx, request.AgentID, request.Scope, false, "owner"); err != nil {
		t.Fatal(err)
	}
	revision := projected.Revision
	if err := serve.recoverPending(ctx); err != nil {
		t.Fatalf("recover after unhalt: %v", err)
	}
	unchanged, err := store.Load(ctx, parentRunID)
	if err != nil || unchanged.Revision != revision || unchanged.Freewill.ExecutionDispatch.Status != "execution_cancelled" {
		t.Fatalf("unhalt resurrected canceled child: dispatch=%#v revision=%d want=%d err=%v", unchanged.Freewill.ExecutionDispatch, unchanged.Revision, revision, err)
	}
}

func TestFreewillTerminalManifestDispatchDefersParentReconciliation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runDir := filepath.Join(root, "runs")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	intention := freewill.IntentionRequest{RequestID: "request-terminal-retry", AgentID: "agent-terminal-retry", Origin: freewill.OriginAgent,
		CandidateSource: freewill.SourceNew, Title: "Retry terminal child", Goal: "Reconcile after parent claim release",
		ReasonSummary: "Exercise manifest retry while dispatch owns its parent claim.", RequestedCapabilities: []string{"code"},
		Scope: freewill.Scope{ProjectID: "project-terminal-retry", Workspace: workspace}, IdempotencyKey: "idem-terminal-retry", CorrelationID: "corr-terminal-retry"}
	input, err := freewillReferenceInput(intention)
	if err != nil {
		t.Fatal(err)
	}
	identity := &multiagent.RequestIdentity{RequesterID: intention.AgentID, SessionID: intention.CorrelationID, Source: "freewill", IdempotencyKey: intention.IdempotencyKey}
	childRunID := stableRequestRunID(identity)
	definition, err := multiagent.ApplyReferenceOverrides(multiagent.DefaultReferenceDefinition(), input)
	if err != nil {
		t.Fatal(err)
	}
	canonicalWorkspace, workspaceID, err := referenceWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	manifest := referenceWorkflowManifest{SchemaVersion: referenceManifestSchemaVersion, RunID: childRunID,
		WorkflowID: multiagent.ReferenceWorkflowID, Input: input, Definition: definition,
		WorkspaceID: workspaceID, WorkspacePath: canonicalWorkspace, SandboxApprovalMode: true}
	if err := writeReferenceManifest(runDir, manifest); err != nil {
		t.Fatal(err)
	}
	childRuntime, err := openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := childRuntime.runtime.Accept(ctx, multiagent.RunRequest{RunID: childRunID,
		Task: multiagent.TaskReference{ID: "task_" + childRunID, Description: multiagent.ReferenceTaskDescription(input)}, WorkspaceID: workspaceID, Identity: identity}); err != nil {
		_ = childRuntime.close()
		t.Fatal(err)
	}
	_, runErr := childRuntime.runtime.FailAcceptedRun(ctx, childRunID, "fixture_terminal_failure")
	closeErr := childRuntime.close()
	if runErr == nil || closeErr != nil {
		t.Fatalf("make child terminal: run=%v close=%v", runErr, closeErr)
	}

	claimHeld := make(chan struct{})
	releaseClaim := make(chan struct{})
	reconciled := make(chan string, 1)
	reconcile := func(_ context.Context, runID string) error {
		close(claimHeld)
		<-releaseClaim
		reconciled <- runID
		return nil
	}
	request := multiagent.FreewillExecutionRequest{ParentRunID: "parent-terminal-retry", ApprovalID: "approval-terminal-retry",
		RequestFingerprint: "fingerprint-terminal-retry", RunID: childRunID, Identity: identity,
		Candidate: freewill.CandidateIntention{CandidateID: "candidate-terminal-retry", Request: intention}}
	started := make(chan error, 1)
	go func() {
		_, err := startFreewillReferenceRun(ctx, runDir, filepath.Join(root, "prizm.yaml"), request, reconcile)
		started <- err
	}()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("retry existing terminal child: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(releaseClaim)
		t.Fatal("dispatch waited for parent reconciliation while holding its claim")
	}
	select {
	case <-claimHeld:
	case <-time.After(2 * time.Second):
		close(releaseClaim)
		t.Fatal("terminal child reconciliation was not scheduled")
	}
	close(releaseClaim)
	select {
	case got := <-reconciled:
		if got != childRunID {
			t.Fatalf("reconciled child %q, want %q", got, childRunID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal child reconciliation did not finish after parent claim release")
	}
}
