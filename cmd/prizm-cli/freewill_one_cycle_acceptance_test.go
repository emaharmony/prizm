package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/policy"
	"github.com/emaharmony/prizm/internal/subagent"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/validation"
	multiagent "github.com/emaharmony/prizm/internal/workflow/multiagent"
)

type staticOneCycleSelector struct{ selection freewill.Selection }

func (s staticOneCycleSelector) SelectFreewill(context.Context, multiagent.FreewillSelectionInput) (freewill.Selection, multiagent.FreewillSelectionEvidence, error) {
	return s.selection, multiagent.FreewillSelectionEvidence{Provider: "test-static", Model: "fixed-fixture"}, nil
}

type oneCyclePolicy struct{}

func (oneCyclePolicy) Evaluate(policy.PolicyRequest) policy.PolicyDecision {
	return policy.PolicyDecision{Decision: policy.DecisionRequiresApproval, RuleID: "synthetic-owner-gate"}
}

type oneCycleRoleRunner struct {
	workspace, runDir string
	approvalStore     *approval.Store
	session           *fakeSandboxReviewSession
	proposalCalls     int
}

func (r *oneCycleRoleRunner) RunRole(ctx context.Context, request multiagent.RoleRunRequest) (multiagent.RoleRunResult, error) {
	result := multiagent.RoleRunResult{LocalIterations: 1, TokenUsage: cost.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		Metadata: multiagent.ExecutionMetadata{AgentRef: request.RoleConfig.AgentRef, Attempt: 1}}
	switch request.Run.CurrentRole {
	case multiagent.RolePlanner:
		result.Outcome = multiagent.OutcomePlanReady
	case multiagent.RoleDeveloper:
		result.Outcome = multiagent.OutcomeImplementationReady
		result.OutgoingHandoff = &multiagent.HandoffDraft{Objective: "Apply the approved synthetic patch", Reason: "The bounded implementation is ready."}
		if r.proposalCalls != 0 {
			return result, nil
		}
		r.proposalCalls++
		runtime := subagent.AgentRuntime{AgentID: request.RoleConfig.AgentRef, RunID: request.Run.RunID,
			TaskID: request.Run.Task.ID, ExecutionKey: request.Run.ExecutionKey, WorkspaceID: request.Run.WorkspaceID,
			WorkDir: r.workspace, Capabilities: []string{"code"}}
		identity, err := newSandboxRuntimeIdentity(runtime)
		if err != nil {
			return multiagent.RoleRunResult{}, err
		}
		owner := &subAgentSandboxTool{sourceRoot: r.workspace, session: r.session, identity: &identity}
		registry := tool.NewRegistry()
		if err := registry.Register(&tool.ApplyPatchProposal{WorkspaceRoot: r.workspace, AllowedPaths: []string{r.workspace}}); err != nil {
			return multiagent.RoleRunResult{}, err
		}
		toolPolicy := tool.DefaultPolicyConfig()
		toolPolicy.WorkspaceRoot, toolPolicy.WriteRoots, toolPolicy.AllowedPaths = r.workspace, []string{r.workspace}, []string{r.workspace}
		toolPolicy.WriteAgents = map[string]bool{runtime.AgentID: true}
		executor := tool.NewExecutor(registry, &toolPolicy)
		executor.SetApprovalStore(r.approvalStore)
		proposal := &subAgentSandboxProposalTool{sandbox: owner, approvalExec: executor, runtime: runtime, project: "freewill-one-cycle"}
		created, err := proposal.Execute(ctx, map[string]any{})
		if err != nil || !created.Success || created.Output["status"] != "pending_approval" {
			return multiagent.RoleRunResult{}, errors.Join(errors.New("sandbox proposal was not pending"), err)
		}
		refs, err := (approvalProposalResolver{store: r.approvalStore, workspace: r.workspace}).ResolveProposals(ctx,
			multiagent.ProposalQuery{RunID: runtime.RunID, ExecutionKey: runtime.ExecutionKey, AgentID: runtime.AgentID})
		if err != nil || len(refs) != 1 {
			return multiagent.RoleRunResult{}, errors.Join(errors.New("sandbox proposal did not resolve to one durable reference"), err)
		}
		result.Proposals = refs
	case multiagent.RoleTester:
		result.Outcome = multiagent.OutcomeTestsPassed
		result.Metadata.ValidationStatus = "passed"
		result.OutgoingHandoff = &multiagent.HandoffDraft{Objective: "Synthetic validation passed", Reason: "The synthetic test stage completed.",
			ValidationResults: []validation.Result{{Profile: "synthetic_patch_check", Status: "passed"}}}
	case multiagent.RoleReviewer:
		result.Outcome = multiagent.OutcomeReviewApproved
	default:
		return multiagent.RoleRunResult{}, errors.New("unexpected reference role")
	}
	if result.Outcome != multiagent.OutcomeReviewApproved {
		if result.OutgoingHandoff == nil {
			result.OutgoingHandoff = &multiagent.HandoffDraft{Objective: "Continue bounded workflow", Reason: "Synthetic role completed."}
		}
	}
	return result, nil
}

func (r *oneCycleRoleRunner) ValidateApprovedRole(_ context.Context, _ multiagent.RoleRunRequest, result multiagent.RoleRunResult) (multiagent.RoleRunResult, error) {
	data, err := os.ReadFile(filepath.Join(r.workspace, "base.txt"))
	if err != nil || strings.ReplaceAll(string(data), "\r\n", "\n") != "changed\n" {
		return result, errors.Join(errors.New("approved patch was not present at validation boundary"), err)
	}
	result.Metadata.ValidationStatus = "passed"
	if result.OutgoingHandoff == nil {
		result.OutgoingHandoff = &multiagent.HandoffDraft{Objective: "Synthetic validation passed", Reason: "The patch was validated."}
	}
	result.OutgoingHandoff.ValidationResults = []validation.Result{{Profile: "synthetic_patch_check", Status: "passed"}}
	return result, nil
}

type oneCycleChildDispatcher struct {
	runDir, source string
	approvalStore  *approval.Store
	runtime        *referenceRuntime
	manifest       referenceWorkflowManifest
	workspace      string
	runner         *oneCycleRoleRunner
	starts         int
}

func (d *oneCycleChildDispatcher) Start(ctx context.Context, request multiagent.FreewillExecutionRequest) (multiagent.FreewillExecutionResult, error) {
	d.starts++
	if d.runtime != nil {
		return multiagent.FreewillExecutionResult{RunID: request.RunID, ReportRef: "multiagent_report.json"}, nil
	}
	if request.Identity == nil || stableRequestRunID(request.Identity) != request.RunID {
		return multiagent.FreewillExecutionResult{}, errors.New("child identity was not stable")
	}
	workspace, workspaceID, sourceWorkspace, _, err := recoverOrCreateFreewillReferenceWorkspace(ctx, d.source, request.RunID)
	if err != nil {
		return multiagent.FreewillExecutionResult{}, err
	}
	input, err := freewillReferenceInput(request.Candidate.Request)
	if err != nil {
		return multiagent.FreewillExecutionResult{}, err
	}
	if input.Budgets.MaxLocalIterations == nil || *input.Budgets.MaxLocalIterations != 8 {
		return multiagent.FreewillExecutionResult{}, errors.New("default Freewill allowance does not match the bounded reference workflow")
	}
	input.Workspace = workspace
	input.ValidationProfiles = map[multiagent.Role][]string{multiagent.RoleTester: {}}
	definition, err := multiagent.ApplyReferenceOverrides(multiagent.DefaultReferenceDefinition(), input)
	if err != nil {
		return multiagent.FreewillExecutionResult{}, err
	}
	manifest := referenceWorkflowManifest{SchemaVersion: referenceManifestSchemaVersion, RunID: request.RunID,
		WorkflowID: multiagent.ReferenceWorkflowID, Input: input, Definition: definition, WorkspaceID: workspaceID,
		WorkspacePath: workspace, SourceWorkspacePath: sourceWorkspace, SandboxApprovalMode: true}
	if err := writeReferenceManifest(d.runDir, manifest); err != nil {
		return multiagent.FreewillExecutionResult{}, err
	}
	baseSHA, err := gitx.CurrentSHA(ctx, workspace)
	if err != nil {
		return multiagent.FreewillExecutionResult{}, err
	}
	baseTree, err := gitx.RunCommand(ctx, workspace, "", "git", "rev-parse", "HEAD^{tree}")
	if err != nil {
		return multiagent.FreewillExecutionResult{}, err
	}
	patch := "diff --git a/base.txt b/base.txt\n--- a/base.txt\n+++ b/base.txt\n@@ -1 +1 @@\n-base\n+changed\n"
	digest := sha256.Sum256([]byte("one-cycle-synthetic-artifact"))
	artifact := filepath.Join(d.runDir, request.RunID, "synthetic.zip")
	if err := os.WriteFile(artifact, []byte("synthetic artifact"), 0o600); err != nil {
		return multiagent.FreewillExecutionResult{}, err
	}
	runner := &oneCycleRoleRunner{workspace: workspace, runDir: d.runDir, approvalStore: d.approvalStore,
		session: &fakeSandboxReviewSession{patch: patch, baseSHA: baseSHA, baseTree: strings.TrimSpace(baseTree), digest: hex.EncodeToString(digest[:]), artifact: artifact}}
	lifecycle := newApprovalProposalLifecycle(d.approvalStore, workspace, filepath.Join(d.runDir, request.RunID), sourceWorkspace)
	child, err := openReferenceRuntimeWithInteraction(d.runDir, manifest, runner, nil, lifecycle)
	if err != nil {
		return multiagent.FreewillExecutionResult{}, err
	}
	_, err = child.runtime.Accept(ctx, multiagent.RunRequest{RunID: request.RunID,
		Task:        multiagent.TaskReference{ID: "task_" + request.RunID, Description: multiagent.ReferenceTaskDescription(input)},
		WorkspaceID: workspaceID, Identity: request.Identity})
	if err != nil {
		_ = child.close()
		return multiagent.FreewillExecutionResult{}, err
	}
	state, runErr := child.runtime.RunAccepted(ctx, request.RunID)
	var waiting *multiagent.RunWaitingError
	if !errors.As(runErr, &waiting) || state.Status != multiagent.RunStatusPaused {
		_ = child.close()
		return multiagent.FreewillExecutionResult{}, errors.Join(errors.New("child did not pause for separate patch approval"), runErr)
	}
	d.runtime, d.manifest, d.workspace, d.runner = child, manifest, workspace, runner
	return multiagent.FreewillExecutionResult{RunID: request.RunID, ReportRef: "multiagent_report.json"}, nil
}

func TestFreewillOneCycleRequiresTwoApprovalsAndProjectsDurableReport(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runsDir := filepath.Join(root, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := newFreewillDispatchTestRepo(t)
	if err := os.WriteFile(filepath.Join(source, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, source, "", "git", "add", "base.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.RunCommand(ctx, source, "", "git", "commit", "-m", "add synthetic target"); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "freewill.db")
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
	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, store,
		multiagent.FileRunClaimer{Root: runsDir}, events, multiagent.DurableRuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	control, err := multiagent.NewFreewillOperatorControl(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := freewill.Scope{ProjectID: "synthetic-project", Workspace: source}
	selection := freewill.Selection{ChoiceID: "candidate-1", ReasonSummary: "A single bounded synthetic patch is the best scoped task.",
		Candidates: []freewill.CandidateIntention{
			{CandidateID: "candidate-1", Score: 90, ScoreFactors: []string{"scope_fit"}, ScoreRationale: "A tiny isolated file change is in scope.", Request: freewill.IntentionRequest{RequestID: "request-1", AgentID: "synthetic-agent", Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: "Change one fixture", Goal: "Change base.txt to changed", ReasonSummary: "The patch is one line and bounded.", RequestedCapabilities: []string{"code"}, Scope: scope, IdempotencyKey: "intent-1", CorrelationID: "intent-correlation-1"}},
			{CandidateID: "candidate-2", Score: 70, ScoreFactors: []string{"feasibility"}, ScoreRationale: "A secondary bounded synthetic task.", Request: freewill.IntentionRequest{RequestID: "request-2", AgentID: "synthetic-agent", Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: "Review fixture", Goal: "Review the fixture", ReasonSummary: "A secondary bounded review.", RequestedCapabilities: []string{"code"}, Scope: scope, IdempotencyKey: "intent-2", CorrelationID: "intent-correlation-2"}},
			{CandidateID: "candidate-3", Score: 60, ScoreFactors: []string{"impact"}, ScoreRationale: "A third safe choice for fixture selection.", Request: freewill.IntentionRequest{RequestID: "request-3", AgentID: "synthetic-agent", Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: "Inspect fixture", Goal: "Inspect the fixture", ReasonSummary: "A third bounded task.", RequestedCapabilities: []string{"code"}, Scope: scope, IdempotencyKey: "intent-3", CorrelationID: "intent-correlation-3"}},
		}}
	wake, err := multiagent.NewFreewillWakeService(parent, store, staticOneCycleSelector{selection: selection}, multiagent.FreewillWakeServiceConfig{
		Enabled: true, SchedulingEnabled: true, ExecutionEnabled: true, AgentID: "synthetic-agent", Scope: scope,
		Objective: "Select one synthetic coding task", Owner: "test-scheduler", LeaseFor: time.Minute, OperatorControl: control}, nil)
	if err != nil {
		t.Fatal(err)
	}
	parentApprovals := approval.NewStore(runsDir)
	service, err := multiagent.NewFreewillApprovalService(parent, store, parentApprovals, oneCyclePolicy{}, "freewill-policy")
	if err != nil {
		t.Fatal(err)
	}
	childApprovals := approval.NewStore(runsDir)
	dispatcher := &oneCycleChildDispatcher{runDir: runsDir, source: source, approvalStore: childApprovals}
	defer func() {
		if dispatcher.runtime != nil {
			_ = dispatcher.runtime.close()
		}
	}()
	service.SetExecutionDispatcher(dispatcher)
	woken, err := wake.HandleScheduledTick(ctx, time.Date(2031, 1, 2, 3, 4, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("static wake: %v", err)
	}
	beforeSource, err := os.ReadFile(filepath.Join(source, "base.txt"))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := service.RequestApproval(ctx, woken.State.RunID)
	if err != nil {
		t.Fatalf("request intention approval: %v", err)
	}
	if dispatcher.starts != 0 || string(beforeSource) != "base\n" {
		t.Fatal("unapproved intention dispatched or mutated source")
	}
	granted, err := service.Decide(ctx, woken.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", "")
	if err != nil {
		t.Fatalf("grant intention: %v", err)
	}
	childID := granted.Freewill.ExecutionDispatch.ChildRunID
	childPending, err := childApprovals.List(childID)
	if err != nil || len(childPending) != 1 || childPending[0].Status != approval.StatusPending {
		t.Fatalf("patch approval=%#v err=%v", childPending, err)
	}
	childBefore, err := os.ReadFile(filepath.Join(dispatcher.workspace, "base.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(childBefore), "\r\n", "\n") != "base\n" || dispatcher.runner.proposalCalls != 1 {
		t.Fatalf("child changed before patch grant: content=%q proposals=%d", childBefore, dispatcher.runner.proposalCalls)
	}
	if err := gitx.EnsureClean(ctx, source); err != nil {
		t.Fatalf("source changed before patch grant: %v", err)
	}
	patchApproval, err := childApprovals.Load(childID, childPending[0].ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := patchApproval.Approve("patch-owner"); err != nil {
		t.Fatal(err)
	}
	if err := childApprovals.Save(patchApproval); err != nil {
		t.Fatal(err)
	}
	terminalState, err := dispatcher.runtime.runtime.Resume(ctx, childID)
	if err != nil || terminalState.Status != multiagent.RunStatusCompleted {
		t.Fatalf("resume child after separate patch grant: status=%s err=%v", terminalState.Status, err)
	}
	childAfter, err := os.ReadFile(filepath.Join(dispatcher.workspace, "base.txt"))
	if err != nil || strings.ReplaceAll(string(childAfter), "\r\n", "\n") != "changed\n" {
		t.Fatalf("child patch result=%q err=%v", childAfter, err)
	}
	if sourceAfter, err := os.ReadFile(filepath.Join(source, "base.txt")); err != nil || strings.ReplaceAll(string(sourceAfter), "\r\n", "\n") != "base\n" {
		t.Fatalf("source was mutated: %q err=%v", sourceAfter, err)
	}
	if dispatcher.runner.session.commands != 0 {
		t.Fatalf("sandbox executed %d commands; expected no provider/OCI command", dispatcher.runner.session.commands)
	}
	if err := finalizeTerminalReferenceRun(ctx, runsDir, &dispatcher.manifest, dispatcher.runtime, terminalState); err != nil {
		t.Fatalf("write terminal report: %v", err)
	}
	result, err := freewillTerminalResultFromReferenceRun(ctx, runsDir, childID)
	if err != nil {
		t.Fatalf("verify terminal report: %v", err)
	}
	terminal, err := service.ReconcileExecutionTerminal(ctx, woken.State.RunID, result)
	if err != nil {
		t.Fatalf("project child result: %v", err)
	}
	status, err := service.Status(ctx, woken.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if status.ExecutionStatus != "completed" || status.ChildStatus != string(multiagent.RunStatusCompleted) || status.ExecutionReportSHA256 != result.ReportSHA256 || status.ValidationSummary != "passed" || status.ExecutionReportRef != "multiagent_report.json" {
		t.Fatalf("Freewill status omitted verified terminal report: %#v", status)
	}
	revision := terminal.Revision
	terminalChildRecord, err := dispatcher.runtime.runtime.Inspect(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DispatchApproved(ctx, woken.State.RunID); err != nil {
		t.Fatalf("replay accepted dispatch: %v", err)
	}
	replayed, err := service.ReconcileExecutionTerminal(ctx, woken.State.RunID, result)
	if err != nil || replayed.Revision != revision {
		t.Fatalf("terminal replay changed parent: revision=%d want=%d err=%v", replayed.Revision, revision, err)
	}
	if _, err := dispatcher.runtime.runtime.Resume(ctx, childID); err != nil {
		t.Fatalf("terminal child replay: %v", err)
	}
	childAfterReplay, err := dispatcher.runtime.runtime.Inspect(ctx, childID)
	if err != nil || childAfterReplay.Revision != terminalChildRecord.Revision || dispatcher.starts != 1 || dispatcher.runner.proposalCalls != 1 {
		t.Fatalf("child replay was not idempotent: revision=%d prior=%d starts=%d proposals=%d err=%v", childAfterReplay.Revision, terminalChildRecord.Revision, dispatcher.starts, dispatcher.runner.proposalCalls, err)
	}
}
