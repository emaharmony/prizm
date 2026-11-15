package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

type strategyExperimentRunReader map[string]multiagent.DurableRun

func (r strategyExperimentRunReader) Load(_ context.Context, id string) (multiagent.DurableRun, error) {
	value, ok := r[id]
	if !ok {
		return multiagent.DurableRun{}, errFreewillStrategyComparisonMissing
	}
	return value, nil
}

type strategyExperimentApprovalReader map[string][]*approval.Approval

func (r strategyExperimentApprovalReader) List(id string) ([]*approval.Approval, error) {
	return r[id], nil
}

func TestFreewillStrategyExperimentFinalizesFixedPairAndReopensWithReceipts(t *testing.T) {
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	if _, err := gitx.RunCommand(ctx, workspace, "", "git", "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"user.email", "synthetic@prizm.local"}, {"user.name", "Prizm synthetic"}} {
		if _, err := gitx.RunCommand(ctx, workspace, "", "git", "config", pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("synthetic source snapshot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "synthetic source"}} {
		if _, err := gitx.RunCommand(ctx, workspace, "", "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	baseSHA, err := gitx.RunCommand(ctx, workspace, "", "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	baseTree, err := gitx.RunCommand(ctx, workspace, "", "git", "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	baseSHA, baseTree = strings.TrimSpace(baseSHA), strings.TrimSpace(baseTree)
	facts, err := newFreewillTerminalFactMemorySink(dataDir, "project-a", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	const goal = "Create one synthetic artifact that includes the approved marker and passes host validation."
	persistedWorkspace := filepath.FromSlash(facts.workspace)
	taskIDs := []string{"", ""}
	runs := strategyExperimentRunReader{}
	patches := strategyExperimentApprovalReader{}
	for i, strategy := range []string{"baseline", "alternative"} {
		parentID, childID := "parent-"+string(rune('a'+i)), "child-"+string(rune('a'+i))
		taskID := freewillTaskScopeID("project-a", facts.workspace, childID)
		taskIDs[i] = taskID
		outcome := freewillTaskOutcome{Version: "freewill-task-outcome/v1", Kind: "host_task_outcome", ProjectID: "project-a", WorkspaceID: facts.workspaceID, Workspace: facts.workspace, AgentID: "selector-a", TaskID: taskID,
			ParentRunID: parentID, ChildRunID: childID, Status: "completed", Validation: "passed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat(string(rune('a'+i)), 64), Confidence: "host_validated_terminal_receipt"}
		encoded, _ := json.Marshal(outcome)
		if _, err := facts.store.Local.Store(ctx, memory.Memory{Content: string(encoded), Summary: "Freewill task outcome", Category: "fact", Tier: "persist", Source: freewillTaskOutcomeSource, UserID: "owner-a", ProjectID: "project-a", TaskID: taskID, AgentID: "selector-a", Metadata: map[string]string{"correlation_id": parentID}}); err != nil {
			t.Fatal(err)
		}
		candidate := freewill.CandidateIntention{CandidateID: "candidate-" + strategy, Request: freewill.IntentionRequest{RequestID: "request-" + strategy, AgentID: "selector-a", Scope: freewill.Scope{ProjectID: "project-a", Workspace: persistedWorkspace}, Title: "Create the synthetic artifact", Goal: goal}}
		parent := multiagent.DurableRun{Kind: multiagent.DurableRunKindFreewill, State: multiagent.RunState{RunID: parentID, RequestIdentity: &multiagent.RequestIdentity{RequesterID: "selector-a"}}, Freewill: &multiagent.FreewillRunState{
			Scope: freewill.Scope{ProjectID: "project-a", Workspace: persistedWorkspace}, Candidates: []freewill.CandidateIntention{candidate}, ChoiceID: candidate.CandidateID,
			ApprovalReceipt:   &multiagent.FreewillApprovalReceipt{ApprovalID: "intention-" + strategy, Decision: multiagent.FreewillApprovalGranted},
			ExecutionDispatch: &multiagent.FreewillExecutionDispatch{Status: "completed", ChildRunID: childID, ReportRef: "multiagent_report.json"},
			StrategyID:        strategy, StrategyVersion: "v1", ExpectedPatchSHA256: strings.Repeat(string(rune('c'+i)), 64),
		}}
		runs[parentID] = parent
		patches[childID] = []*approval.Approval{{RunID: childID, ApprovalID: "patch-" + strategy, MutationType: approval.MutationApplyPatch, Status: approval.StatusApproved,
			PatchPlan: &approval.PatchPlan{BaseSHA: baseSHA, BaseTree: baseTree, PatchSHA256: strings.Repeat(string(rune('c'+i)), 64)}}}
	}
	patchDigests := []string{strings.Repeat("c", 64), strings.Repeat("d", 64)}
	sink, err := newFreewillStrategyExperimentSink(appraisals, runs, patches, "owner-a", workspace, "selector-a", []string{"baseline", "alternative"}, []string{"v1", "v1"}, patchDigests)
	if err != nil {
		t.Fatal(err)
	}
	record, err := sink.Finalize(ctx, "owner-a", "project-a", workspace, "selector-a", taskIDs)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Runs) != 2 || record.Runs[0].Role != "baseline" || record.Runs[1].Role != "alternative" || record.SourceSnapshotTree != baseTree || record.Runs[0].ApprovedPatchSHA256 == record.Runs[1].ApprovedPatchSHA256 || record.TitleGoalBindingSHA256 == "" || !strings.Contains(record.Uncertainty, "one run") {
		t.Fatalf("unexpected comparison: %#v", record)
	}
	reopened, err := sink.ReadStrategyExperiment(ctx, "owner-a", "project-a", workspace, "selector-a", taskIDs)
	if err != nil || reopened.ExperimentID != record.ExperimentID || len(reopened.Runs) != 2 {
		t.Fatalf("reopen record=%#v err=%v", reopened, err)
	}
	parent := runs["parent-a"]
	parent.Freewill.Candidates[0].Request.Title = "Different approved task title"
	runs["parent-a"] = parent
	if _, err := sink.ReadStrategyExperiment(ctx, "owner-a", "project-a", workspace, "selector-a", taskIDs); err == nil {
		t.Fatal("comparison accepted different complete task objectives")
	}
	parent.Freewill.Candidates[0].Request.Title = "Create the synthetic artifact"
	runs["parent-a"] = parent
	approved := *patches["child-a"][0]
	plan := *approved.PatchPlan
	plan.PatchSHA256 = strings.Repeat("e", 64)
	approved.PatchPlan = &plan
	patches["child-a"] = []*approval.Approval{&approved}
	if _, err := sink.ReadStrategyExperiment(ctx, "owner-a", "project-a", workspace, "selector-a", taskIDs); err == nil {
		t.Fatal("comparison accepted an approved patch outside the fixed strategy fingerprint")
	}
	if _, err := sink.ReadStrategyExperiment(ctx, "other-owner", "project-a", workspace, "selector-a", taskIDs); err == nil {
		t.Fatal("wrong owner read accepted")
	}
	if _, err := sink.Finalize(ctx, "owner-a", "project-a", workspace, "other-agent", taskIDs); err == nil {
		t.Fatal("wrong agent finalize accepted")
	}
}
