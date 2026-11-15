package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/memory"
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
		t.Fatal("malformed terminal report allowed parent reconciliation")
	}
	if scheduledTicks != 0 {
		t.Fatalf("scheduled tick admitted new work while terminal evidence was invalid: ticks=%d", scheduledTicks)
	}
	quarantined, err := store.Load(ctx, parent.State.RunID)
	if err != nil || quarantined.Freewill.ExecutionDispatch.Status != "recovery_uncertain" ||
		quarantined.Freewill.ExecutionDispatch.RecoveryReason != "terminal_report_invalid" ||
		quarantined.Freewill.ExecutionDispatch.ChildRunID != childRunID {
		t.Fatalf("invalid report was not durably quarantined: dispatch=%#v err=%v", quarantined.Freewill.ExecutionDispatch, err)
	}
	if err := os.Remove(reportPath); err != nil {
		t.Fatal(err)
	}
	if err := runFreewillScheduledTick(ctx, runtime.recoverPending, handleTick); err != nil {
		t.Fatalf("host report rebuild did not reconcile evidence quarantine: %v", err)
	}
	if scheduledTicks != 1 {
		t.Fatalf("scheduled tick was not admitted after host evidence repair: ticks=%d", scheduledTicks)
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
	unchanged, err := store.Load(ctx, parent.State.RunID)
	if err != nil || unchanged.Revision != revision || unchanged.Freewill.ExecutionDispatch.ChildRunID != childRunID || scheduledTicks != 2 {
		t.Fatalf("terminal recovery replay changed identity/state or blocked tick: revision=%d want=%d child=%q ticks=%d err=%v",
			unchanged.Revision, revision, unchanged.Freewill.ExecutionDispatch.ChildRunID, scheduledTicks, err)
	}
}

func TestFreewillRecoverPendingQuarantinesAcceptedChildWithPossibleMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runDir := filepath.Join(root, "runs")
	workspace := filepath.Join(root, "workspace")
	for _, dir := range []string{runDir, workspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const parentRunID = "run_freewill_mutation_uncertain_parent"
	const childRunID = "run_aaaaaaaaaaaaaaaaaaaa"
	request := freewill.IntentionRequest{RequestID: "request-mutation-uncertain", AgentID: "agent-mutation-uncertain", Origin: freewill.OriginAgent,
		CandidateSource: freewill.SourceNew, Title: "Recover uncertain child", Goal: "Do not replay after possible external mutation",
		ReasonSummary: "Local fixture for ambiguous child mutation recovery.", RequestedCapabilities: []string{"code"},
		Scope: freewill.Scope{ProjectID: "project-mutation-uncertain", Workspace: workspace}, IdempotencyKey: "idem-mutation-uncertain", CorrelationID: "corr-mutation-uncertain"}
	input, err := freewillReferenceInput(request)
	if err != nil {
		t.Fatal(err)
	}
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
	child, err := openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	childIdentity := &multiagent.RequestIdentity{RequesterID: request.AgentID, SessionID: request.CorrelationID,
		Source: "freewill", IdempotencyKey: request.IdempotencyKey}
	if _, err := child.runtime.Accept(ctx, multiagent.RunRequest{RunID: childRunID,
		Task:        multiagent.TaskReference{ID: "task_" + childRunID, Description: "mutation uncertainty fixture"},
		WorkspaceID: workspaceID, Identity: childIdentity}); err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	childRecord, err := child.store.Load(ctx, childRunID)
	if err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	role := childRecord.State.CurrentRole
	roleState := childRecord.State.RoleStates[role]
	roleState.Visits = 1
	roleState.Role = role
	roleState.Status = multiagent.RoleStatusRunning
	childRecord.State.RoleStates[role] = roleState
	childRecord.Phase = multiagent.CheckpointRoleRunning
	childRecord.State.Status = multiagent.RunStatusRunning
	childRecord.ActiveExecutionKey = "execution-mutation-fixture"
	visitID := fmt.Sprintf("%s:%s:1", childRunID, role)
	childRecord.Retry = &multiagent.RetryState{VisitID: visitID, ExecutionKey: childRecord.ActiveExecutionKey,
		Role: role, AttemptID: "attempt-mutation-fixture", Number: 1, MaxRecoveries: 3, MutationPossible: true}
	if _, err := child.store.Checkpoint(ctx, childRecord.Revision, childRecord, nil); err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	if uncertain, err := freewillReferenceChildHasUnresolvedMutation(ctx, runDir, childRunID); err != nil || !uncertain {
		_ = child.close()
		t.Fatalf("fixture mutation ledger was not visible to recovery inspection: uncertain=%v err=%v", uncertain, err)
	}
	if err := child.close(); err != nil {
		t.Fatal(err)
	}

	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "freewill.db")
	store, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	fingerprintBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	fingerprintSum := sha256.Sum256(fingerprintBytes)
	fingerprint := hex.EncodeToString(fingerprintSum[:])
	now := time.Now().UTC()
	approvalStore := approval.NewStore(runDir)
	approvalRecord, err := approval.NewIntentionApproval(parentRunID, request.CorrelationID, request.AgentID, request.Scope.ProjectID,
		approval.IntentionApprovalRef{RequestID: request.RequestID, RequestFingerprint: fingerprint,
			ScopeSummary: "project workspace", Capabilities: request.RequestedCapabilities},
		approval.PolicyDecision{Decision: approval.DecisionRequiresApproval, Reason: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	approvalRecord.Status, approvalRecord.ApprovedBy, approvalRecord.ApprovedAt = approval.StatusApproved, "synthetic-owner", &now
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
		Freewill: &multiagent.FreewillRunState{Lifecycle: freewill.StateRunning, Scope: request.Scope, WakeID: "wake-mutation-uncertain",
			Trigger: "scheduled", DedupeKey: request.IdempotencyKey, CorrelationID: request.CorrelationID,
			Candidates: []freewill.CandidateIntention{{CandidateID: "candidate-mutation-uncertain", Request: request}},
			ChoiceID:   "candidate-mutation-uncertain", ExecutionDisabled: true, ApprovalRef: approvalRecord.ApprovalID,
			ApprovalFingerprint: fingerprint, ApprovalReceipt: &multiagent.FreewillApprovalReceipt{ApprovalID: approvalRecord.ApprovalID,
				RequestFingerprint: fingerprint, Decision: multiagent.FreewillApprovalGranted},
			ExecutionDispatch: &multiagent.FreewillExecutionDispatch{Status: "accepted", ChildRunID: childRunID,
				ReportRef: "multiagent_report.json", UpdatedAt: now}}}
	if _, err := store.Create(ctx, parent, nil); err != nil {
		t.Fatalf("create accepted parent: %v", err)
	}
	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, store,
		multiagent.FileRunClaimer{Root: filepath.Join(root, "claims")}, events, multiagent.DurableRuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := multiagent.NewFreewillApprovalService(runtime, store, approvalStore,
		policy.NewEvaluator(policy.NewRegistry()), "freewill-policy")
	if err != nil {
		t.Fatal(err)
	}
	serve := &freewillServeRuntime{store: store, approvals: approvals, executionEnabled: true, runDir: runDir,
		configPath: filepath.Join(root, "prizm.yaml")}
	if err := serve.recoverPending(ctx); err == nil {
		t.Fatal("possible mutation silently resumed or admitted work")
	}
	quarantined, err := store.Load(ctx, parentRunID)
	if err != nil || quarantined.Freewill.ExecutionDispatch.Status != "recovery_uncertain" ||
		quarantined.Freewill.ExecutionDispatch.RecoveryReason != "child_mutation_possible" ||
		quarantined.Freewill.ExecutionDispatch.ChildRunID != childRunID {
		t.Fatalf("possible mutation was not durably quarantined: dispatch=%#v err=%v", quarantined.Freewill.ExecutionDispatch, err)
	}
	if terminal, err := freewillReferenceChildIsTerminal(ctx, runDir, childRunID); err != nil || terminal {
		t.Fatalf("quarantine unexpectedly resumed/terminated child: terminal=%v err=%v", terminal, err)
	}
	revision := quarantined.Revision
	if err := serve.recoverPending(ctx); err == nil {
		t.Fatal("persisted uncertainty did not continue blocking scheduled recovery")
	}
	unchanged, err := store.Load(ctx, parentRunID)
	if err != nil || unchanged.Revision != revision || unchanged.Freewill.ExecutionDispatch.Status != "recovery_uncertain" {
		t.Fatalf("quarantine retry changed parent state: dispatch=%#v revision=%d want=%d err=%v",
			unchanged.Freewill.ExecutionDispatch, unchanged.Revision, revision, err)
	}
	audit, err := events.Query(ctx, event.EventFilter{RunID: parentRunID, Type: event.EventFreewillExecutionRecoveryUncertain, Limit: 10})
	if err != nil || len(audit) != 1 {
		t.Fatalf("recovery uncertainty event count=%d err=%v want=1", len(audit), err)
	}

	// A partial proof must remain quarantined. Then reopen the child's SQLite
	// state and use a second parent runtime to prove the complete exact set is
	// durably adjudicated without resuming or synthesizing a child result.
	mutationOneSum := sha256.Sum256([]byte("mutation-one"))
	mutationTwoSum := sha256.Sum256([]byte("mutation-two"))
	mutationOne, mutationTwo := hex.EncodeToString(mutationOneSum[:]), hex.EncodeToString(mutationTwoSum[:])
	child, err = openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	childRecord, err = child.store.Load(ctx, childRunID)
	if err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	visitID = fmt.Sprintf("%s:%s:1", childRunID, childRecord.State.CurrentRole)
	attemptSum := sha256.Sum256([]byte(fmt.Sprintf("%s:attempt:1", visitID)))
	childRecord.Retry = &multiagent.RetryState{VisitID: visitID, ExecutionKey: childRecord.ActiveExecutionKey,
		Role: childRecord.State.CurrentRole, AttemptID: "ra_" + hex.EncodeToString(attemptSum[:12]), Number: 1, MaxRecoveries: 3,
		MutationPossible: true, UnresolvedMutationFailure: true, MutationLedgerReady: true,
		UnsafeMutationIDs: []string{mutationOne, mutationTwo}, ProvenNoEffectIDs: []string{mutationOne, mutationTwo}}
	if _, err := child.store.Checkpoint(ctx, childRecord.Revision, childRecord, nil); err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	if err := child.close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	secondEvents, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondEvents.Close()
	secondRuntime, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, secondStore,
		multiagent.FileRunClaimer{Root: filepath.Join(root, "claims")}, secondEvents, multiagent.DurableRuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secondApprovals, err := multiagent.NewFreewillApprovalService(secondRuntime, secondStore, approvalStore,
		policy.NewEvaluator(policy.NewRegistry()), "freewill-policy")
	if err != nil {
		t.Fatal(err)
	}
	secondServe := &freewillServeRuntime{store: secondStore, approvals: secondApprovals, executionEnabled: true, runDir: runDir,
		configPath: filepath.Join(root, "prizm.yaml")}
	if err := secondServe.recoverPending(ctx); err == nil {
		t.Fatal("unresolved mutation failure cleared quarantine despite complete no-effects IDs")
	}
	partial, err := secondStore.Load(ctx, parentRunID)
	if err != nil || partial.Freewill.ExecutionDispatch.Status != "recovery_uncertain" || partial.Revision != revision {
		t.Fatalf("partial proof changed quarantined parent: dispatch=%#v revision=%d want=%d err=%v",
			partial.Freewill.ExecutionDispatch, partial.Revision, revision, err)
	}
	child, err = openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	childRecord, err = child.store.Load(ctx, childRunID)
	if err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	childRecord.Retry.UnresolvedMutationFailure = false
	childRecord.Retry.ProvenNoEffectIDs = []string{mutationOne}
	if _, err := child.store.Checkpoint(ctx, childRecord.Revision, childRecord, nil); err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	if err := child.close(); err != nil {
		t.Fatal(err)
	}
	if err := secondServe.recoverPending(ctx); err == nil {
		t.Fatal("partial no-effects proof cleared mutation quarantine")
	}
	partial, err = secondStore.Load(ctx, parentRunID)
	if err != nil || partial.Freewill.ExecutionDispatch.Status != "recovery_uncertain" || partial.Revision != revision {
		t.Fatalf("partial proof changed quarantined parent: dispatch=%#v revision=%d want=%d err=%v",
			partial.Freewill.ExecutionDispatch, partial.Revision, revision, err)
	}
	child, err = openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	childRecord, err = child.store.Load(ctx, childRunID)
	if err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	childRecord.Retry.ProvenNoEffectIDs = []string{mutationOne, mutationTwo}
	if _, err := child.store.Checkpoint(ctx, childRecord.Revision, childRecord, nil); err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	mutationThreeSum := sha256.Sum256([]byte("mutation-three-after-first-snapshot"))
	mutationThree := hex.EncodeToString(mutationThreeSum[:])
	childClaim, err := (multiagent.FileRunClaimer{Root: runDir}).Acquire(ctx, childRunID)
	if err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	childRecord, err = child.store.Load(ctx, childRunID)
	if err != nil {
		_ = childClaim.Release()
		_ = child.close()
		t.Fatal(err)
	}
	childRecord.Retry.UnsafeMutationIDs = append(childRecord.Retry.UnsafeMutationIDs, mutationThree)
	staleMutationRecord := childRecord
	if _, err := child.store.Checkpoint(ctx, childRecord.Revision, childRecord, nil); err != nil {
		_ = childClaim.Release()
		_ = child.close()
		t.Fatal(err)
	}
	if busyErr := secondServe.recoverPending(ctx); !errors.Is(busyErr, multiagent.ErrRunClaimed) {
		_ = childClaim.Release()
		_ = child.close()
		t.Fatalf("reconciliation did not respect the active child claim: %v", busyErr)
	}
	if err := childClaim.Release(); err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	if err := secondServe.recoverPending(ctx); err == nil {
		_ = child.close()
		t.Fatal("later attributed mutation without proof cleared quarantine")
	}
	partial, err = secondStore.Load(ctx, parentRunID)
	if err != nil || partial.Freewill.ExecutionDispatch.Status != "recovery_uncertain" || partial.Revision != revision {
		_ = child.close()
		t.Fatalf("later mutation changed quarantined parent: dispatch=%#v revision=%d want=%d err=%v",
			partial.Freewill.ExecutionDispatch, partial.Revision, revision, err)
	}
	childRecord, err = child.store.Load(ctx, childRunID)
	if err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	childRecord.Retry.ProvenNoEffectIDs = append(childRecord.Retry.ProvenNoEffectIDs, mutationThree)
	if _, err := child.store.Checkpoint(ctx, childRecord.Revision, childRecord, nil); err != nil {
		_ = child.close()
		t.Fatal(err)
	}
	if err := secondServe.recoverPending(ctx); err != nil {
		_ = child.close()
		t.Fatalf("host no-effect reconciliation failed: %v", err)
	}
	if _, err := child.store.Checkpoint(ctx, staleMutationRecord.Revision, staleMutationRecord, nil); !errors.Is(err, multiagent.ErrRevisionConflict) {
		_ = child.close()
		t.Fatalf("stale child mutation checkpoint was not fenced by terminal CAS: %v", err)
	}
	if err := child.close(); err != nil {
		t.Fatal(err)
	}
	reconciled, err := secondStore.Load(ctx, parentRunID)
	if err != nil || reconciled.Freewill.ExecutionDispatch.Status != "mutation_no_effect_reconciled" ||
		reconciled.Freewill.Lifecycle != freewill.StatePaused || reconciled.Freewill.PauseReason != "child_mutation_no_effect_proven" ||
		reconciled.Freewill.ExecutionDispatch.ChildStatus != string(multiagent.RunStatusFailed) ||
		reconciled.Freewill.ExecutionDispatch.MutationAttemptID != "ra_"+hex.EncodeToString(attemptSum[:12]) ||
		reconciled.Freewill.ExecutionDispatch.ReportSHA256 != "" || reconciled.Freewill.ExecutionDispatch.ValidationSummary != "" ||
		reconciled.Freewill.ExecutionDispatch.FailureCode != "child_mutation_no_effect_proven" ||
		reconciled.Freewill.ApprovalRef != approvalRecord.ApprovalID || reconciled.State.RequestIdentity.RequesterID != request.AgentID {
		t.Fatalf("no-effect reconciliation did not preserve exact failed/approved scope: record=%#v err=%v", reconciled.Freewill.ExecutionDispatch, err)
	}
	expectedMutationIDs := []string{mutationOne, mutationTwo, mutationThree}
	sort.Strings(expectedMutationIDs)
	if strings.Join(reconciled.Freewill.ExecutionDispatch.NoEffectMutationIDs, ",") != strings.Join(expectedMutationIDs, ",") {
		t.Fatalf("no-effect receipt mutation ids=%v want=%v", reconciled.Freewill.ExecutionDispatch.NoEffectMutationIDs, expectedMutationIDs)
	}
	if terminal, err := freewillReferenceChildIsTerminal(ctx, runDir, childRunID); err != nil || !terminal {
		t.Fatalf("no-effect reconciliation did not fence child as failed: terminal=%v err=%v", terminal, err)
	}
	child, err = openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	finalChild, inspectErr := child.runtime.Inspect(ctx, childRunID)
	closeErr := child.close()
	if inspectErr != nil || closeErr != nil || finalChild.State.Status != multiagent.RunStatusFailed || finalChild.Failure == nil ||
		finalChild.Failure.Kind != "mutation_no_effect_reconciled" || finalChild.Failure.Message != "child_mutation_no_effect_proven" || finalChild.Retry != nil {
		t.Fatalf("child terminal fence is not an explicit failed no-effect outcome: state=%#v failure=%#v inspect=%v close=%v",
			finalChild.State.Status, finalChild.Failure, inspectErr, closeErr)
	}
	noEffectAudit, err := secondEvents.Query(ctx, event.EventFilter{RunID: parentRunID, Type: event.EventFreewillExecutionMutationNoEffectReconciled, Limit: 10})
	if err != nil || len(noEffectAudit) != 1 {
		t.Fatalf("no-effect audit count=%d err=%v want=1", len(noEffectAudit), err)
	}
	revision = reconciled.Revision
	if err := secondEvents.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secondStore.Close(); err != nil {
		t.Fatal(err)
	}
	thirdStore, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer thirdStore.Close()
	thirdEvents, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer thirdEvents.Close()
	thirdRuntime, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, thirdStore,
		multiagent.FileRunClaimer{Root: filepath.Join(root, "claims")}, thirdEvents, multiagent.DurableRuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	thirdApprovals, err := multiagent.NewFreewillApprovalService(thirdRuntime, thirdStore, approvalStore,
		policy.NewEvaluator(policy.NewRegistry()), "freewill-policy")
	if err != nil {
		t.Fatal(err)
	}
	thirdServe := &freewillServeRuntime{store: thirdStore, approvals: thirdApprovals, executionEnabled: true, runDir: runDir,
		configPath: filepath.Join(root, "prizm.yaml")}
	if err := thirdServe.recoverPending(ctx); err != nil {
		t.Fatalf("reopened no-effect reconciliation: %v", err)
	}
	unchanged, err = thirdStore.Load(ctx, parentRunID)
	if err != nil || unchanged.Revision != revision {
		t.Fatalf("no-effect reconciliation was not idempotent: revision=%d want=%d err=%v", unchanged.Revision, revision, err)
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
	configPath := filepath.Join(root, "prizm.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write default host config for terminal-child retry: %v", err)
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
		_, err := startFreewillReferenceRun(ctx, runDir, configPath, request, reconcile)
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

type failOnceRecoveryFactSink struct {
	calls int
}

func (s *failOnceRecoveryFactSink) CaptureFreewillTerminalFact(context.Context, multiagent.FreewillTerminalFact) error {
	s.calls++
	if s.calls == 1 {
		return errors.New("simulated local fact capture failure")
	}
	return nil
}

type recoveryDispatchSpy struct {
	starts int
}

func (s *recoveryDispatchSpy) Start(_ context.Context, request multiagent.FreewillExecutionRequest) (multiagent.FreewillExecutionResult, error) {
	s.starts++
	return multiagent.FreewillExecutionResult{RunID: request.RunID, ReportRef: "multiagent_report.json"}, nil
}

func TestFreewillRecoverPendingRetriesTerminalFactAfterSQLiteRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	runDir := filepath.Join(root, "runs")
	workspace := filepath.Join(root, "workspace")
	for _, dir := range []string{dataDir, runDir, workspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const parentRunID = "run_freewill_terminal_fact_recovery"
	const childRunID = "run_aaaaaaaaaaaaaaaaaaaa"
	const projectID = "project-terminal-fact-recovery"
	const agentID = "agent-terminal-fact-recovery"
	const correlationID = "corr-terminal-fact-recovery"
	dbPath := filepath.Join(dataDir, "freewill.db")

	initialStore, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	initialEvents, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		_ = initialStore.Close()
		t.Fatal(err)
	}
	initialOpen := true
	t.Cleanup(func() {
		if initialOpen {
			_ = initialStore.Close()
			_ = initialEvents.Close()
		}
	})
	request := freewill.IntentionRequest{RequestID: "request-terminal-fact-recovery", AgentID: agentID,
		Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: "Recover terminal fact",
		Goal: "Persist one host-validated terminal receipt after restart", ReasonSummary: "Synthetic recovery fixture.",
		RequestedCapabilities: []string{"code"}, Scope: freewill.Scope{ProjectID: projectID, Workspace: workspace},
		IdempotencyKey: "idem-terminal-fact-recovery", CorrelationID: correlationID}
	fingerprintBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	fingerprintSum := sha256.Sum256(fingerprintBytes)
	fingerprint := hex.EncodeToString(fingerprintSum[:])
	now := time.Now().UTC()
	approvalStore := approval.NewStore(runDir)
	approvalRecord, err := approval.NewIntentionApproval(parentRunID, correlationID, agentID, projectID,
		approval.IntentionApprovalRef{RequestID: request.RequestID, RequestFingerprint: fingerprint,
			ScopeSummary: "project workspace", Capabilities: request.RequestedCapabilities},
		approval.PolicyDecision{Decision: approval.DecisionRequiresApproval, Reason: "fixture"})
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
			Status: multiagent.RunStatusCreated, RequestIdentity: &multiagent.RequestIdentity{RequesterID: agentID,
				SessionID: correlationID, Source: "freewill_scheduler", IdempotencyKey: request.IdempotencyKey},
			CreatedAt: now, UpdatedAt: now},
		Freewill: &multiagent.FreewillRunState{Lifecycle: freewill.StateRunning, Scope: request.Scope, WakeID: "wake-terminal-fact-recovery",
			Trigger: "scheduled", DedupeKey: request.IdempotencyKey, CorrelationID: correlationID,
			Candidates: []freewill.CandidateIntention{{CandidateID: "candidate-terminal-fact-recovery", Request: request}},
			ChoiceID:   "candidate-terminal-fact-recovery", ExecutionDisabled: true,
			ApprovalRef: approvalRecord.ApprovalID, ApprovalFingerprint: fingerprint,
			ApprovalReceipt: &multiagent.FreewillApprovalReceipt{ApprovalID: approvalRecord.ApprovalID,
				RequestFingerprint: fingerprint, Decision: multiagent.FreewillApprovalGranted},
			ExecutionDispatch: &multiagent.FreewillExecutionDispatch{Status: "accepted", ChildRunID: childRunID,
				ReportRef: "multiagent_report.json", UpdatedAt: now}}}
	if _, err := initialStore.Create(ctx, parent, nil); err != nil {
		t.Fatalf("create accepted parent fixture: %v", err)
	}
	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		t.Fatal(err)
	}
	initialRuntime, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, initialStore,
		multiagent.FileRunClaimer{Root: filepath.Join(root, "approval-claims")}, initialEvents, multiagent.DurableRuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	initialApprovals, err := multiagent.NewFreewillApprovalService(initialRuntime, initialStore, approvalStore,
		policy.NewEvaluator(policy.NewRegistry()), "freewill-policy")
	if err != nil {
		t.Fatal(err)
	}
	failingSink := &failOnceRecoveryFactSink{}
	initialApprovals.SetTerminalFactSink(failingSink)
	result := multiagent.FreewillExecutionTerminalResult{ChildRunID: childRunID, ChildStatus: multiagent.RunStatusFailed,
		ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("d", 64),
		ValidationSummary: "failed", FailureCode: "validation_failed"}
	terminal, err := initialApprovals.ReconcileExecutionTerminal(ctx, parentRunID, result)
	if err == nil || failingSink.calls != 1 || terminal.Freewill.ExecutionDispatch.Status != "execution_failed" ||
		terminal.Freewill.ExecutionDispatch.ReportSHA256 != result.ReportSHA256 {
		t.Fatalf("simulated capture failure did not leave durable terminal receipt: dispatch=%#v calls=%d err=%v",
			terminal.Freewill.ExecutionDispatch, failingSink.calls, err)
	}
	terminalRevision := terminal.Revision
	if err := initialStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := initialEvents.Close(); err != nil {
		t.Fatal(err)
	}
	initialOpen = false

	reopenedStore, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	reopenedEvents, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		_ = reopenedStore.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reopenedStore.Close()
		_ = reopenedEvents.Close()
	})
	reopenedRuntime, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, reopenedStore,
		multiagent.FileRunClaimer{Root: filepath.Join(root, "approval-claims")}, reopenedEvents, multiagent.DurableRuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reopenedApprovals, err := multiagent.NewFreewillApprovalService(reopenedRuntime, reopenedStore,
		approval.NewStore(runDir), policy.NewEvaluator(policy.NewRegistry()), "freewill-policy")
	if err != nil {
		t.Fatal(err)
	}
	if !freewillTerminalFactsBindingMatches(projectID, workspace, projectID, workspace) {
		t.Fatal("exact project/path opt-in did not match recovery fixture")
	}
	factSink, err := newFreewillTerminalFactMemorySink(dataDir, projectID, workspace, reopenedEvents)
	if err != nil {
		t.Fatal(err)
	}
	retrospectiveProvider := &freewillRetrospectiveFake{
		narrative: "I remember this as a successful outcome, despite the host record showing execution failure and failed validation.",
		provider:  "local-fake", model: "synthetic-retrospective", version: "fixture-v1",
	}
	diarySink, err := newFreewillTerminalDiarySink(factSink, retrospectiveProvider, "synthetic-owner")
	if err != nil {
		t.Fatal(err)
	}
	reopenedApprovals.SetTerminalFactSink(factSink)
	reopenedApprovals.SetTerminalDiarySink(diarySink)
	dispatchSpy := &recoveryDispatchSpy{}
	reopenedApprovals.SetExecutionDispatcher(dispatchSpy)
	serve := &freewillServeRuntime{store: reopenedStore, approvals: reopenedApprovals, executionEnabled: true,
		runDir: runDir, configPath: filepath.Join(root, "prizm.yaml")}
	if err := serve.recoverPending(ctx); err != nil {
		t.Fatalf("recover terminal fact after SQLite reopen: %v", err)
	}
	if err := serve.recoverPending(ctx); err != nil {
		t.Fatalf("repeat terminal fact recovery: %v", err)
	}
	projected, err := reopenedStore.Load(ctx, parentRunID)
	if err != nil || projected.Revision != terminalRevision || projected.Freewill.ExecutionDispatch.Status != "execution_failed" || dispatchSpy.starts != 0 {
		t.Fatalf("recovery changed terminal state or redispatched child: revision=%d want=%d dispatch=%#v starts=%d err=%v",
			projected.Revision, terminalRevision, projected.Freewill.ExecutionDispatch, dispatchSpy.starts, err)
	}
	terminalEvents, err := reopenedEvents.Query(ctx, event.EventFilter{RunID: parentRunID, Type: event.EventFreewillExecutionFailed, Limit: 10})
	if err != nil || len(terminalEvents) != 1 {
		t.Fatalf("terminal event count=%d err=%v want=1", len(terminalEvents), err)
	}
	for _, eventType := range []string{memory.EventScopedCaptureStarted, memory.EventScopedCapturePersisted, memory.EventScopedCaptureSyncPending} {
		lifecycleEvents, queryErr := reopenedEvents.Query(ctx, event.EventFilter{Type: eventType, Limit: 10})
		if queryErr != nil || len(lifecycleEvents) != 2 {
			t.Fatalf("memory event %s count=%d err=%v want=2 (fact + diary)", eventType, len(lifecycleEvents), queryErr)
		}
	}
	scope, err := factSink.scopeFor(projectID, workspace, "candidate-terminal-fact-recovery", agentID, parentRunID)
	if err != nil {
		t.Fatal(err)
	}
	facts, _, err := factSink.store.Search(ctx, memory.SearchRequest{Scope: scope, Query: "host terminal outcome", Limit: 10})
	if err != nil {
		t.Fatalf("read exact-scope memory: %v", err)
	}
	factEntries := make([]memory.Memory, 0, 1)
	for _, entry := range facts {
		if entry.Source == "prizm:freewill-terminal" && entry.Category == "fact" {
			factEntries = append(factEntries, entry)
		}
	}
	facts = factEntries
	if len(facts) != 1 || !strings.Contains(facts[0].Content, result.ReportSHA256) {
		t.Fatalf("recovered exact-scope fact count=%d err=%v", len(facts), err)
	}
	if retrospectiveProvider.calls != 1 || !strings.Contains(retrospectiveProvider.fact.Content, result.ReportSHA256) {
		t.Fatalf("retrospective provider calls=%d fact=%q", retrospectiveProvider.calls, retrospectiveProvider.fact.Content)
	}
	// Reopen the Markdown-backed scoped store independently before owner reads.
	reopenedFacts, err := newFreewillTerminalFactMemorySink(dataDir, projectID, workspace, reopenedEvents)
	if err != nil {
		t.Fatal(err)
	}
	reopenedDiarySink, err := newFreewillTerminalDiarySink(reopenedFacts, retrospectiveProvider, "synthetic-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := reopenedDiarySink.CaptureFreewillTerminalRetrospective(ctx, multiagent.FreewillTerminalFact{
		ProjectID: projectID, Workspace: workspace, AgentID: agentID, IntentionID: "candidate-terminal-fact-recovery",
		ParentRunID: parentRunID, ChildRunID: result.ChildRunID, Status: "execution_failed",
		ReportRef: result.ReportRef, ReportSHA256: result.ReportSHA256, ValidationSummary: result.ValidationSummary,
		FailureCode: result.FailureCode, RecordedAt: projected.Freewill.ExecutionDispatch.UpdatedAt,
	}); err != nil {
		t.Fatalf("replay diary capture after reopen: %v", err)
	}
	if retrospectiveProvider.calls != 1 {
		t.Fatalf("reopen replay regenerated model retrospective: calls=%d", retrospectiveProvider.calls)
	}
	diary, err := reopenedDiarySink.ReadOwnerEntries(ctx, "synthetic-owner", projectID, workspace, "candidate-terminal-fact-recovery", agentID, parentRunID)
	if err != nil || len(diary) != 1 || diary[0].Category != "diary" || diary[0].Source != freewillDiarySource {
		t.Fatalf("owner diary records=%#v err=%v", diary, err)
	}
	var retrospective freewillDiaryRetrospective
	if err := json.Unmarshal([]byte(diary[0].Content), &retrospective); err != nil {
		t.Fatal(err)
	}
	if retrospective.Version != 1 || retrospective.Kind != "model_retrospective" || retrospective.EvidenceFactID != facts[0].ID ||
		retrospective.Provider != "local-fake" || retrospective.Model != "synthetic-retrospective" ||
		retrospective.ModelVersion != "fixture-v1" || retrospective.Confidence != "model_self_report" ||
		!strings.Contains(retrospective.Narrative, "successful outcome") || !strings.Contains(facts[0].Content, `"status":"execution_failed"`) {
		t.Fatalf("fact/diary relationship or disagreement was lost: fact=%s retrospective=%#v", facts[0].Content, retrospective)
	}
	if _, err := reopenedDiarySink.ReadOwnerEntries(ctx, "other-owner", projectID, workspace, "candidate-terminal-fact-recovery", agentID, parentRunID); err == nil {
		t.Fatal("diary read accepted a non-owner")
	}
	otherIntention := "another-intention"
	otherDiary, err := reopenedDiarySink.ReadOwnerEntries(ctx, "synthetic-owner", projectID, workspace, otherIntention, agentID, parentRunID)
	if err != nil || len(otherDiary) != 0 {
		t.Fatalf("out-of-scope diary read records=%#v err=%v", otherDiary, err)
	}
}

type freewillRetrospectiveFake struct {
	narrative string
	provider  string
	model     string
	version   string
	calls     int
	fact      memory.Memory
	started   chan struct{}
	release   <-chan struct{}
}

func (p *freewillRetrospectiveFake) Retrospect(_ context.Context, fact memory.Memory) (string, string, string, string, error) {
	p.calls++
	p.fact = fact
	if p.started != nil {
		close(p.started)
	}
	if p.release != nil {
		<-p.release
	}
	return p.narrative, p.provider, p.model, p.version, nil
}
