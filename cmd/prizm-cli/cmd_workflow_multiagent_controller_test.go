package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

type proposalControllerFixture struct {
	runDir    string
	runID     string
	proposal  string
	approval  string
	workspace string
	runtime   *referenceRuntime
	progress  multiagent.ProposalProgress
}

func newProposalControllerFixture(t *testing.T, runID string) proposalControllerFixture {
	t.Helper()
	ctx := context.Background()
	runDir := t.TempDir()
	workspace := t.TempDir()
	definition := multiagent.DefaultReferenceDefinition()
	manifest := referenceWorkflowManifest{
		SchemaVersion: referenceManifestSchemaVersion, RunID: runID,
		WorkflowID: definition.ID, Definition: definition, WorkspaceID: "workspace-test",
		WorkspacePath:    workspace,
		Input:            multiagent.ReferenceWorkflowInput{Objective: "proposal controller regression", Workspace: workspace},
		WorkspaceCleaned: true,
	}
	if err := writeReferenceManifest(runDir, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	rt, err := openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := rt.close(); err != nil {
			t.Errorf("close fixture runtime: %v", err)
		}
	})
	_, err = rt.runtime.Accept(ctx, multiagent.RunRequest{
		RunID: runID, WorkspaceID: manifest.WorkspaceID,
		Task: multiagent.TaskReference{ID: "task-" + runID, Description: "proposal decision controller test"},
	})
	if err != nil {
		t.Fatalf("accept fixture run: %v", err)
	}
	item := approval.NewApproval(runID, "execution-1", "developer", "prizm", approval.MutationApplyPatch,
		"internal/retry/retry_test.go", "diff --git a/x b/x", approval.PolicyDecision{Decision: approval.DecisionRequiresApproval})
	item.PatchPlan = &approval.PatchPlan{BaseSHA: "base-sha", PatchSHA256: "patch-hash", Paths: []string{"internal/retry/retry_test.go"}}
	if err := approval.NewStore(runDir).Save(item); err != nil {
		t.Fatalf("save approval: %v", err)
	}
	sum := sha256.Sum256([]byte(runID + "\x00" + item.ProposalID))
	progress := multiagent.ProposalProgress{
		ProposalOperation: multiagent.ProposalOperation{
			RunID: runID, ProposalID: item.ProposalID, ApprovalID: item.ApprovalID,
			ApplyKey: fmt.Sprintf("apply_%x", sum[:16]), WorkspaceID: manifest.WorkspaceID, ExecutionKey: "execution-1",
		},
		Phase: "waiting",
	}
	record, err := rt.store.Load(ctx, runID)
	if err != nil {
		t.Fatalf("load fixture run: %v", err)
	}
	now := time.Now().UTC()
	record.State.Status = multiagent.RunStatusPaused
	record.State.UpdatedAt = now
	record.Phase = multiagent.CheckpointWaiting
	record.Waiting = &multiagent.WaitingState{Kind: "proposal_approval", Reason: "waiting for approval", SafeToRetry: true, Since: now, ProposalID: item.ProposalID, ApprovalID: item.ApprovalID}
	record.ApprovedTask = &multiagent.ApprovedTaskState{Role: record.State.CurrentRole, StartedAt: now, FinishedAt: now, Proposals: []multiagent.ProposalProgress{progress}}
	if _, err := rt.store.Checkpoint(ctx, record.Revision, record, nil); err != nil {
		t.Fatalf("checkpoint proposal wait: %v", err)
	}
	return proposalControllerFixture{runDir: runDir, runID: runID, proposal: item.ProposalID, approval: item.ApprovalID, workspace: workspace, runtime: rt, progress: progress}
}

func TestReferenceControllerProposalDecisionIsRunScopedAndRestartIdempotent(t *testing.T) {
	fixture := newProposalControllerFixture(t, "run-proposal-grant")
	controller := newReferenceMultiAgentController(fixture.runDir, "")
	view, err := controller.Proposal(context.Background(), fixture.runID, fixture.proposal)
	if err != nil || view.Patch == "" || view.BaseSHA != "base-sha" || view.ApprovalID != fixture.approval {
		t.Fatalf("proposal view=%+v err=%v", view, err)
	}
	if _, err := controller.DecideProposal(context.Background(), fixture.runID, fixture.proposal, "appr_wrong", "grant", ""); err == nil {
		t.Fatal("mismatched approval ID was accepted")
	}
	resume, err := controller.DecideProposal(context.Background(), fixture.runID, fixture.proposal, fixture.approval, "grant", "")
	if err != nil || !resume {
		t.Fatalf("grant resume=%t err=%v", resume, err)
	}
	// A new controller models a serving-process restart. The same recorded grant
	// remains idempotent while the durable run is still at its approval wait.
	restarted := newReferenceMultiAgentController(fixture.runDir, "")
	resume, err = restarted.DecideProposal(context.Background(), fixture.runID, fixture.proposal, fixture.approval, "grant", "")
	if err != nil || !resume {
		t.Fatalf("restart duplicate grant resume=%t err=%v", resume, err)
	}
	if _, err := restarted.DecideProposal(context.Background(), fixture.runID, fixture.proposal, fixture.approval, "deny", "conflict"); err == nil {
		t.Fatal("deny after grant was accepted")
	}
	item, err := approval.NewStore(fixture.runDir).Load(fixture.runID, fixture.approval)
	if err != nil || item.Status != approval.StatusApproved {
		t.Fatalf("persisted status=%v err=%v", item, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.workspace, "internal", "retry", "retry_test.go")); !os.IsNotExist(err) {
		t.Fatalf("grant unexpectedly applied a patch before Resume: %v", err)
	}
	if _, err := restarted.DecideProposal(context.Background(), fixture.runID, "prop_unknown", fixture.approval, "grant", ""); err == nil {
		t.Fatal("mismatched proposal ID was accepted")
	}

	// Simulate successful advancement and then a terminal completion while the
	// proposal tuple is retained in ProposalResults. Replaying the same grant
	// must report the recorded decision without requesting another Resume.
	record, err := fixture.runtime.store.Load(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	record.ApprovedTask = nil
	record.Waiting = nil
	record.Phase = multiagent.CheckpointPendingRole
	record.State.Status = multiagent.RunStatusRunning
	record.State.UpdatedAt = time.Now().UTC()
	completedProgress := fixture.progress
	completedProgress.Phase = "applied"
	record.ProposalResults = append(record.ProposalResults, completedProgress)
	record, err = fixture.runtime.store.Checkpoint(context.Background(), record.Revision, record, nil)
	if err != nil {
		t.Fatalf("checkpoint advanced run: %v", err)
	}
	now := time.Now().UTC()
	record.Phase = multiagent.CheckpointTerminal
	record.State.Status = multiagent.RunStatusCompleted
	record.State.CompletedAt = &now
	record.State.TerminalOutcome = &multiagent.TerminalOutcome{Condition: multiagent.TerminalConditionCompleted, Reason: "test completed", At: now}
	record.State.UpdatedAt = now
	if _, err := fixture.runtime.store.Checkpoint(context.Background(), record.Revision, record, nil); err != nil {
		t.Fatalf("checkpoint completed run: %v", err)
	}
	resume, err = restarted.DecideProposal(context.Background(), fixture.runID, fixture.proposal, fixture.approval, "grant", "")
	if err != nil || resume {
		t.Fatalf("completed duplicate grant resume=%t err=%v, want no resume", resume, err)
	}
}

func TestReferenceControllerProposalDenialIsIdempotentAndDoesNotApply(t *testing.T) {
	fixture := newProposalControllerFixture(t, "run-proposal-deny")
	controller := newReferenceMultiAgentController(fixture.runDir, "")
	resume, err := controller.DecideProposal(context.Background(), fixture.runID, fixture.proposal, fixture.approval, "deny", "out of scope")
	if err != nil || !resume {
		t.Fatalf("deny resume=%t err=%v", resume, err)
	}
	item, err := approval.NewStore(fixture.runDir).Load(fixture.runID, fixture.approval)
	if err != nil || item.Status != approval.StatusDenied || item.DenialReason != "out of scope" {
		t.Fatalf("persisted denial=%+v err=%v", item, err)
	}
	resume, err = newReferenceMultiAgentController(fixture.runDir, "").DecideProposal(context.Background(), fixture.runID, fixture.proposal, fixture.approval, "deny", "out of scope")
	if err != nil || !resume {
		t.Fatalf("duplicate deny resume=%t err=%v", resume, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.workspace, "internal", "retry", "retry_test.go")); !os.IsNotExist(err) {
		t.Fatalf("denial unexpectedly applied a patch: %v", err)
	}
	record, err := fixture.runtime.store.Load(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record.Phase = multiagent.CheckpointTerminal
	record.Waiting = nil
	record.State.Status = multiagent.RunStatusFailed
	record.State.CompletedAt = &now
	record.State.TerminalOutcome = &multiagent.TerminalOutcome{Condition: multiagent.TerminalConditionFailed, Reason: "proposal denied", At: now}
	record.State.UpdatedAt = now
	if _, err := fixture.runtime.store.Checkpoint(context.Background(), record.Revision, record, nil); err != nil {
		t.Fatalf("checkpoint denied terminal run: %v", err)
	}
	resume, err = newReferenceMultiAgentController(fixture.runDir, "").DecideProposal(context.Background(), fixture.runID, fixture.proposal, fixture.approval, "deny", "out of scope")
	if err != nil || resume {
		t.Fatalf("terminal duplicate denial resume=%t err=%v, want no resume", resume, err)
	}
}
