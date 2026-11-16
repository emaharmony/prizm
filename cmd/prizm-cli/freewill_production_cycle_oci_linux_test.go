//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	"gopkg.in/yaml.v3"
)

// TestFreewillProductionOneCycleOCI exercises the real Freewill dispatcher,
// reference workflow, OCI proposal approval, accepted-child recovery/resume,
// and parent terminal report projection with a deterministic local
// Ollama-compatible API.
func TestFreewillProductionOneCycleOCI(t *testing.T) {
	if os.Getenv("PRIZM_SANDBOX_OCI_CONFORMANCE") != "1" {
		t.Skip("set PRIZM_SANDBOX_OCI_CONFORMANCE=1 to require a Linux OCI engine")
	}
	runFreewillProductionCycleOCI(t, false)
}

func TestFreewillProductionOperatorHaltDuringOCI(t *testing.T) {
	if os.Getenv("PRIZM_SANDBOX_OCI_CONFORMANCE") != "1" || strings.TrimSpace(os.Getenv("PRIZM_SANDBOX_OCI_IMAGE")) == "" {
		t.Skip("set PRIZM_SANDBOX_OCI_CONFORMANCE=1 and PRIZM_SANDBOX_OCI_IMAGE to a locally cached digest-pinned image")
	}
	runFreewillProductionCycleOCI(t, true)
}

func runFreewillProductionCycleOCI(t *testing.T, haltDuringOCI bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	dockerPath := strings.TrimSpace(os.Getenv("PRIZM_SUBAGENT_SANDBOX_DOCKER"))
	if dockerPath == "" {
		dockerPath = "docker"
	}
	image := ""
	if haltDuringOCI {
		image = requireCachedProductionOCIImage(t, ctx, dockerPath, os.Getenv("PRIZM_SANDBOX_OCI_IMAGE"))
	} else {
		image = requireFreewillProductionOCIImage(t, ctx, dockerPath)
	}

	root := t.TempDir()
	runsDir := filepath.Join(root, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := newFreewillDispatchTestRepo(t)
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.invalid/freewillcycle\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "approved_test.go"), []byte("package freewillcycle\n\nimport (\"os\"; \"testing\")\n\nfunc TestApprovedArtifact(t *testing.T) { b, err := os.ReadFile(\"approved.txt\"); if err != nil { t.Fatal(err) }; if string(b) != \"approved-change\\n\" { t.Fatalf(\"artifact = %q\", b) } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "go.mod", "approved_test.go"}, {"commit", "-m", "add approved artifact validation"}} {
		if _, err := gitx.RunCommand(ctx, source, "", "git", args...); err != nil {
			t.Fatalf("prepare validation fixture: %v", err)
		}
	}
	if err := gitx.EnsureClean(ctx, source); err != nil {
		t.Fatalf("source fixture is not clean: %v", err)
	}

	ociStartMarker := "e45-" + filepath.Base(root)
	sandboxExecReply := productionCycleSandboxExecRequest
	if haltDuringOCI {
		sandboxExecReply = fmt.Sprintf(`{"type":"tool_request","tool":"sandbox_exec","input":{"command":"printf 'running\\n' > %s; while :; do sleep 1; done; printf 'approved-change\\n' > approved.txt"}}`, ociStartMarker)
	}
	providerScript := newProductionCycleModelScript([]string{
		productionCyclePlannerReply,
		sandboxExecReply,
		productionCycleSandboxProposalRequest,
		productionCycleDeveloperReply,
		productionCycleTesterReply,
		productionCycleReviewerReply,
	})
	providerServer := httptest.NewServer(providerScript)
	defer providerServer.Close()
	t.Setenv("OLLAMA_BASE_URL", providerServer.URL)
	t.Setenv(multiAgentSandboxApprovalEnv, "oci-approval")
	t.Setenv("PRIZM_SUBAGENT_SANDBOX_DOCKER", dockerPath)
	t.Setenv("PRIZM_SUBAGENT_SANDBOX_IMAGE", image)
	t.Setenv("PRIZM_SUBAGENT_SANDBOX_WORK_ROOT", filepath.Join(root, "sandbox-work"))
	t.Setenv("PRIZM_SUBAGENT_SANDBOX_ARTIFACT_ROOT", filepath.Join(root, "sandbox-artifacts"))

	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.OllamaURL = providerServer.URL
	cfg.Agents = []orchestrator.AgentConfig{
		{ID: "cycle-planner", Role: "planner", Provider: "ollama", Model: "fw-cycle-script", Capabilities: []string{"plan"}},
		{ID: "cycle-developer", Role: "developer", Provider: "ollama", Model: "fw-cycle-script", Capabilities: []string{"code"}},
		{ID: "cycle-tester", Role: "tester", Provider: "ollama", Model: "fw-cycle-script", Capabilities: []string{"test"}},
		{ID: "cycle-reviewer", Role: "reviewer", Provider: "ollama", Model: "fw-cycle-script", Capabilities: []string{"review"}},
	}
	configBytes, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "prizm.yaml")
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
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
	notificationSink := &productionCycleNotificationSink{}
	notifications, err := multiagent.NewFreewillNotificationService(dbPath, events, notificationSink)
	if err != nil {
		t.Fatal(err)
	}
	notificationCtx, stopNotifications := context.WithCancel(ctx)
	defer stopNotifications()
	notifications.Start(notificationCtx)
	defer notifications.Close()
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
	scope := freewill.Scope{ProjectID: "production-cycle-fixture", Workspace: source}
	selection := freewill.Selection{ChoiceID: "candidate-1", ReasonSummary: "One isolated artifact has a bounded, directly verifiable change.", Candidates: []freewill.CandidateIntention{
		{CandidateID: "candidate-1", Score: 90, ScoreFactors: []string{"scope_fit"}, ScoreRationale: "The requested artifact is isolated and testable.", Request: freewill.IntentionRequest{RequestID: "cycle-request-1", AgentID: "synthetic-agent", Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: "Create one approved artifact", Goal: "Create approved.txt containing approved-change", ReasonSummary: "A single file is validated by the existing reference workflow.", RequestedCapabilities: []string{"code"}, Scope: scope, IdempotencyKey: "cycle-intent-1", CorrelationID: "cycle-correlation-1"}},
		{CandidateID: "candidate-2", Score: 70, ScoreFactors: []string{"feasibility"}, ScoreRationale: "A second bounded candidate.", Request: freewill.IntentionRequest{RequestID: "cycle-request-2", AgentID: "synthetic-agent", Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: "Inspect fixture", Goal: "Inspect the fixture", ReasonSummary: "A bounded read-only alternative.", RequestedCapabilities: []string{"code"}, Scope: scope, IdempotencyKey: "cycle-intent-2", CorrelationID: "cycle-correlation-2"}},
		{CandidateID: "candidate-3", Score: 60, ScoreFactors: []string{"impact"}, ScoreRationale: "A third bounded candidate.", Request: freewill.IntentionRequest{RequestID: "cycle-request-3", AgentID: "synthetic-agent", Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: "Review fixture", Goal: "Review the fixture", ReasonSummary: "A bounded review alternative.", RequestedCapabilities: []string{"code"}, Scope: scope, IdempotencyKey: "cycle-intent-3", CorrelationID: "cycle-correlation-3"}},
	}}
	wake, err := multiagent.NewFreewillWakeService(parent, store, staticOneCycleSelector{selection: selection}, multiagent.FreewillWakeServiceConfig{
		Enabled: true, SchedulingEnabled: true, ExecutionEnabled: true, AgentID: "synthetic-agent", Scope: scope,
		Objective: "Select one bounded synthetic coding task", Owner: "test-scheduler", LeaseFor: time.Minute, OperatorControl: control}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerApprovals := approval.NewStore(runsDir)
	service, err := multiagent.NewFreewillApprovalService(parent, store, ownerApprovals, oneCyclePolicy{}, "freewill-production-cycle")
	if err != nil {
		t.Fatal(err)
	}
	sandboxConfig, err := freewillSandboxConfigFromEnvironment(source)
	if err != nil {
		t.Fatal(err)
	}
	childApprovals := approval.NewStore(runsDir)
	serve := &freewillServeRuntime{approvals: service, store: store, control: control, childApprovals: childApprovals,
		sandboxConfig: sandboxConfig, runDir: runsDir, configPath: configPath,
		executionEnabled: true, resumingChildren: make(map[string]bool)}
	dispatcher := freewillReferenceExecutionDispatcher{runDir: runsDir, configPath: configPath, approvals: service, control: control, sandboxConfig: sandboxConfig}
	service.SetExecutionDispatcher(dispatcher)
	apiHandler := api.NewServer(api.Config{Addr: ":0", AuthToken: "synthetic-token", FreewillApprovals: service, FreewillChildApprovals: serve,
		FreewillControl: control}).Handler()
	apiServer := httptest.NewServer(apiHandler)
	defer apiServer.Close()
	woken, err := wake.HandleScheduledTick(ctx, time.Date(2031, 2, 3, 4, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("static Freewill wake: %v", err)
	}
	pending, err := service.RequestApproval(ctx, woken.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "approved.txt")); !os.IsNotExist(err) {
		t.Fatalf("unapproved intention mutated source: stat err=%v", err)
	}
	if pending.Freewill.ExecutionDispatch != nil {
		t.Fatalf("unapproved intention dispatched: %#v", pending.Freewill.ExecutionDispatch)
	}
	parentPath := fmt.Sprintf("/api/v1/freewill/runs/%s/approvals/%s/decision", woken.State.RunID, pending.Freewill.ApprovalRef)
	parentApproval := postFreewillProductionCycleDecision(t, apiServer.URL+parentPath, "synthetic-token", `{"decision":"grant"}`)
	var granted multiagent.FreewillApprovalStatus
	if err := json.Unmarshal(parentApproval, &granted); err != nil {
		t.Fatalf("decode served parent approval: %v", err)
	}
	if granted.RunID != woken.State.RunID || granted.ApprovalStatus != "approved" {
		t.Fatalf("served parent approval response=%#v", granted)
	}
	childID := granted.ChildRunID
	if childID == "" || granted.ExecutionStatus != "accepted" {
		t.Fatalf("production dispatcher did not durably admit child: %#v", granted)
	}
	if haltDuringOCI {
		containerID := waitForProductionCycleOCIContainer(t, ctx, dockerPath, image, ociStartMarker)
		controlBody, err := json.Marshal(map[string]any{"agent_id": "synthetic-agent", "project_id": scope.ProjectID,
			"workspace": scope.Workspace, "halted": true})
		if err != nil {
			t.Fatal(err)
		}
		haltResponse := postFreewillProductionCycleDecision(t, apiServer.URL+"/api/v1/freewill/control", "synthetic-token", string(controlBody))
		var haltState multiagent.FreewillOperatorControlState
		if err := json.Unmarshal(haltResponse, &haltState); err != nil || !haltState.Halted {
			t.Fatalf("authenticated same-scope halt response=%s err=%v", haltResponse, err)
		}
		status, err := waitForProductionCycleStatus(t, ctx, service, woken.State.RunID, "execution_cancelled")
		if err != nil {
			t.Fatalf("wait for halted parent cancellation: %v", err)
		}
		waitForNoProductionCycleOCIContainer(t, ctx, dockerPath, containerID)
		manifest, err := loadReferenceManifest(runsDir, childID)
		if err != nil {
			t.Fatal(err)
		}
		inspection, err := openInspectionReferenceRuntime(runsDir, manifest)
		if err != nil {
			t.Fatal(err)
		}
		child, inspectErr := inspection.runtime.Inspect(ctx, childID)
		inspection.close()
		if inspectErr != nil || child.State.Status != multiagent.RunStatusCancelled {
			t.Fatalf("halted OCI child state=%#v err=%v, want canceled", child.State, inspectErr)
		}
		if status.ChildStatus != string(child.State.Status) {
			t.Fatalf("parent child outcome=%q differs from durable child status=%q", status.ChildStatus, child.State.Status)
		}
		if status.ExecutionReportRef != "multiagent_report.json" || status.ValidationSummary != "not_run" {
			t.Fatalf("canceled child report/validation summary=%#v, want cancellation receipt without validation", status)
		}
		reportData, err := os.ReadFile(filepath.Join(runsDir, childID, "multiagent_report.json"))
		if err != nil {
			t.Fatalf("read durable cancellation report: %v", err)
		}
		var report multiagent.ReferenceRunReport
		if err := json.Unmarshal(reportData, &report); err != nil || report.FinalStatus != multiagent.RunStatusCancelled || len(report.ValidationResults) != 0 || len(report.TestsExecuted) != 0 {
			t.Fatalf("canceled child report=%#v err=%v, want canceled with no validation", report, err)
		}
		if calls := providerScript.callCount(); calls != 2 {
			t.Fatalf("halted child continued provider/role work: calls=%d, want planner plus active developer request", calls)
		}
		if _, err := os.Stat(filepath.Join(source, "approved.txt")); !os.IsNotExist(err) {
			t.Fatalf("canceled OCI artifact reached source workspace: %v", err)
		}
		if _, err := os.Stat(filepath.Join(runsDir, childID, "approved-task")); !os.IsNotExist(err) {
			t.Fatalf("canceled OCI artifact was promoted into approved-task: %v", err)
		}
		before, err := store.Load(ctx, woken.State.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := serve.recoverPending(ctx); err != nil {
			t.Fatalf("replay halted child recovery: %v", err)
		}
		controlBody, err = json.Marshal(map[string]any{"agent_id": "synthetic-agent", "project_id": scope.ProjectID,
			"workspace": scope.Workspace, "halted": false})
		if err != nil {
			t.Fatal(err)
		}
		resumeResponse := postFreewillProductionCycleDecision(t, apiServer.URL+"/api/v1/freewill/control", "synthetic-token", string(controlBody))
		if err := json.Unmarshal(resumeResponse, &haltState); err != nil || haltState.Halted {
			t.Fatalf("authenticated control resume response=%s err=%v", resumeResponse, err)
		}
		if err := serve.recoverPending(ctx); err != nil {
			t.Fatalf("replay after unhalt: %v", err)
		}
		after, err := store.Load(ctx, woken.State.RunID)
		if err != nil || after.Revision != before.Revision {
			t.Fatalf("halted parent was resurrected by replay: before revision=%d after=%d err=%v", before.Revision, after.Revision, err)
		}
		reopenedStore, err := multiagent.NewSQLiteDurableRunStore(dbPath)
		if err != nil {
			t.Fatalf("reopen durable run store after halt replay: %v", err)
		}
		reopenedParent, loadErr := reopenedStore.Load(ctx, woken.State.RunID)
		closeErr := reopenedStore.Close()
		if loadErr != nil || closeErr != nil || reopenedParent.Revision != before.Revision || reopenedParent.Freewill == nil ||
			reopenedParent.Freewill.ExecutionDispatch == nil || reopenedParent.Freewill.ExecutionDispatch.Status != "execution_cancelled" {
			t.Fatalf("reopened parent state changed after halt replay: parent=%#v load=%v close=%v", reopenedParent, loadErr, closeErr)
		}
		freshStore, err := multiagent.NewSQLiteDurableRunStore(dbPath)
		if err != nil {
			t.Fatalf("open fresh runtime store after halt: %v", err)
		}
		freshEvents, err := event.NewSQLiteEventStore(dbPath)
		if err != nil {
			_ = freshStore.Close()
			t.Fatalf("open fresh runtime event store after halt: %v", err)
		}
		freshParent, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, freshStore,
			multiagent.FileRunClaimer{Root: runsDir}, freshEvents, multiagent.DurableRuntimeOptions{})
		if err != nil {
			_ = freshEvents.Close()
			_ = freshStore.Close()
			t.Fatalf("construct fresh durable runtime after halt: %v", err)
		}
		freshControl, err := multiagent.NewFreewillOperatorControl(freshStore, nil)
		if err != nil {
			_ = freshEvents.Close()
			_ = freshStore.Close()
			t.Fatalf("construct fresh operator control after halt: %v", err)
		}
		freshService, err := multiagent.NewFreewillApprovalService(freshParent, freshStore, approval.NewStore(runsDir), oneCyclePolicy{}, "freewill-production-cycle-restart")
		if err != nil {
			_ = freshEvents.Close()
			_ = freshStore.Close()
			t.Fatalf("construct fresh approval service after halt: %v", err)
		}
		freshService.SetExecutionDispatcher(freewillReferenceExecutionDispatcher{runDir: runsDir, configPath: configPath,
			approvals: freshService, control: freshControl, sandboxConfig: sandboxConfig})
		freshServe := &freewillServeRuntime{approvals: freshService, store: freshStore, control: freshControl,
			childApprovals: approval.NewStore(runsDir), sandboxConfig: sandboxConfig, runDir: runsDir,
			configPath: configPath, executionEnabled: true, resumingChildren: make(map[string]bool)}
		if err := freshServe.recoverPending(ctx); err != nil {
			_ = freshEvents.Close()
			_ = freshStore.Close()
			t.Fatalf("fresh runtime recovery after halt: %v", err)
		}
		freshAfter, err := freshStore.Load(ctx, woken.State.RunID)
		if err != nil || freshAfter.Revision != before.Revision || freshAfter.Freewill == nil ||
			freshAfter.Freewill.ExecutionDispatch == nil || freshAfter.Freewill.ExecutionDispatch.Status != "execution_cancelled" {
			_ = freshEvents.Close()
			_ = freshStore.Close()
			t.Fatalf("fresh runtime recovery changed halted parent: parent=%#v err=%v", freshAfter, err)
		}
		if err := freshEvents.Close(); err != nil {
			_ = freshStore.Close()
			t.Fatalf("close fresh runtime event store: %v", err)
		}
		if err := freshStore.Close(); err != nil {
			t.Fatalf("close fresh runtime run store: %v", err)
		}
		parentCorrelationID := reopenedParent.Freewill.CorrelationID
		if parentCorrelationID == "" || parentCorrelationID != reopenedParent.State.RunID || reopenedParent.State.RequestIdentity == nil ||
			reopenedParent.State.RequestIdentity.SessionID != parentCorrelationID {
			t.Fatalf("reopened parent lost the accepted intention correlation: parent=%#v", reopenedParent)
		}
		intentionCorrelationFound := false
		for _, candidate := range reopenedParent.Freewill.Candidates {
			if candidate.CandidateID == reopenedParent.Freewill.ChoiceID && candidate.Request.CorrelationID == "cycle-correlation-1" {
				intentionCorrelationFound = true
				break
			}
		}
		if !intentionCorrelationFound {
			t.Fatalf("reopened parent lost the selected intention correlation: candidates=%#v choice=%q", reopenedParent.Freewill.Candidates, reopenedParent.Freewill.ChoiceID)
		}
		reopenedInspection, err := openInspectionReferenceRuntime(runsDir, manifest)
		if err != nil {
			t.Fatalf("reopen canceled child after halt replay: %v", err)
		}
		reopenedChild, inspectErr := reopenedInspection.runtime.Inspect(ctx, childID)
		reopenedInspection.close()
		if inspectErr != nil || reopenedChild.State.Status != multiagent.RunStatusCancelled {
			t.Fatalf("child state changed after halt replay: child=%#v err=%v", reopenedChild.State, inspectErr)
		}
		if reopenedChild.State.RequestIdentity == nil || reopenedChild.State.RequestIdentity.SessionID != parentCorrelationID {
			t.Fatalf("reopened canceled child lost the parent wake correlation: child=%#v correlation=%q", reopenedChild.State, parentCorrelationID)
		}
		if calls := providerScript.callCount(); calls != 2 {
			t.Fatalf("same-runtime or fresh-runtime halt recovery restarted provider work: calls=%d, want planner plus active developer request", calls)
		}
		return
	}
	patchApproval := waitForProductionCycleApproval(t, ctx, childApprovals, childID)
	manifest, err := loadReferenceManifest(runsDir, childID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.WorkspacePath == source || manifest.SourceWorkspacePath != source {
		t.Fatalf("child workspace is not isolated: workspace=%q source=%q want=%q", manifest.WorkspacePath, manifest.SourceWorkspacePath, source)
	}
	for _, workspace := range []string{source, manifest.WorkspacePath} {
		if _, err := os.Stat(filepath.Join(workspace, "approved.txt")); !os.IsNotExist(err) {
			t.Fatalf("workspace mutated before patch approval: workspace=%s stat err=%v", workspace, err)
		}
	}
	if patchApproval.Status != approval.StatusPending {
		t.Fatalf("child proposal status=%s, want pending", patchApproval.Status)
	}
	childPath := fmt.Sprintf("/api/v1/freewill/runs/%s/children/%s/approvals", woken.State.RunID, childID)
	listedPatch := getFreewillProductionCycleApprovals(t, apiServer.URL+childPath, "synthetic-token")
	if len(listedPatch) != 1 || listedPatch[0].ApprovalID != patchApproval.ApprovalID || listedPatch[0].Patch == "" || listedPatch[0].TargetPath != "approved.txt" {
		t.Fatalf("served pending patch projection=%#v", listedPatch)
	}
	decisionPath := fmt.Sprintf("%s/%s/decision", childPath, patchApproval.ApprovalID)
	postFreewillProductionCycleDecision(t, apiServer.URL+decisionPath, "synthetic-token", `{"decision":"grant"}`)
	patchApprovalAfter, err := childApprovals.Load(childID, patchApproval.ApprovalID)
	if err != nil || patchApprovalAfter.Status != approval.StatusApproved {
		t.Fatalf("served patch approval status=%v err=%v", patchApprovalAfter, err)
	}
	if err := serve.recoverPending(ctx); err != nil {
		t.Fatalf("production accepted-child recovery: %v", err)
	}
	status, err := waitForProductionCycleStatus(t, ctx, service, woken.State.RunID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	result, err := freewillTerminalResultFromReferenceRun(ctx, runsDir, childID)
	if err != nil {
		t.Fatalf("verify production child report: %v", err)
	}
	if status.ExecutionReportSHA256 != result.ReportSHA256 || status.ExecutionReportRef != "multiagent_report.json" || status.ValidationSummary != "passed" {
		t.Fatalf("parent status omitted verified report/validation: status=%#v result=%#v", status, result)
	}
	if err := notifications.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile durable Freewill notifications: %v", err)
	}
	noticeCtx, cancelNotice := context.WithTimeout(ctx, 5*time.Second)
	defer cancelNotice()
	got := waitForProductionCycleNotification(t, noticeCtx, notificationSink, woken.State.RunID, event.EventFreewillExecutionCompleted)
	if got == nil || got.Payload["report_sha256"] != status.ExecutionReportSHA256 || got.Payload["validation_summary"] != "passed" {
		t.Fatalf("terminal Freewill notification=%#v, want report SHA and passed validation for parent %s", got, woken.State.RunID)
	}
	terminalManifest, err := loadReferenceManifest(runsDir, childID)
	if err != nil {
		t.Fatal(err)
	}
	if !terminalManifest.WorkspaceCleaned {
		t.Fatalf("terminal child worktree was not cleaned after report finalization: %#v", terminalManifest)
	}
	if _, err := os.Stat(terminalManifest.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("terminal child worktree cleanup did not complete: path=%s err=%v", terminalManifest.WorkspacePath, err)
	}
	if _, err := os.Stat(filepath.Join(source, "approved.txt")); !os.IsNotExist(err) {
		t.Fatalf("approved child mutation escaped into source: stat err=%v", err)
	}
	reportBytes, err := os.ReadFile(filepath.Join(runsDir, childID, "multiagent_report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report multiagent.ReferenceRunReport
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		t.Fatalf("decode durable reference report: %v", err)
	}
	if len(report.ValidationResults) != 1 || report.ValidationResults[0].Profile != "go_test_all" ||
		report.ValidationResults[0].Status != "passed" || report.ValidationResults[0].ExitCode != 0 ||
		!strings.Contains(strings.Join(report.TestsExecuted, "\n"), "go test ./...") {
		t.Fatalf("durable report does not prove the approved artifact passed go_test_all: %#v", report.ValidationResults)
	}
	patchDir := filepath.Join(runsDir, childID, "approved-task")
	patchFiles, err := os.ReadDir(patchDir)
	if err != nil || len(patchFiles) != 1 || patchFiles[0].IsDir() {
		t.Fatalf("durable approval evidence files=%#v err=%v, want one patch", patchFiles, err)
	}
	patch, err := os.ReadFile(filepath.Join(patchDir, patchFiles[0].Name()))
	if err != nil || strings.Count(string(patch), "diff --git ") != 1 || !strings.Contains(string(patch), "diff --git a/approved.txt b/approved.txt") {
		t.Fatalf("durable approved patch does not contain exactly one approved.txt mutation: %q err=%v", patch, err)
	}
	patchStat, err := gitx.RunCommand(ctx, source, string(patch), "git", "apply", "--stat", "-")
	if err != nil || !strings.Contains(string(patchStat), "approved.txt") || !strings.Contains(string(patchStat), "1 file changed") {
		t.Fatalf("durable approved patch stat=%q err=%v, want one changed file", patchStat, err)
	}
	if err := gitx.EnsureClean(ctx, source); err != nil {
		t.Fatalf("source repository changed: %v", err)
	}
	callsAtTerminal := providerScript.callCount()
	if callsAtTerminal != 6 {
		t.Fatalf("provider calls=%d, want planner/developer/tool/developer/tester/reviewer sequence", callsAtTerminal)
	}

	before, err := store.Load(ctx, woken.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DispatchApproved(ctx, woken.State.RunID); err != nil {
		t.Fatalf("replay production dispatch: %v", err)
	}
	if err := serve.recoverPending(ctx); err != nil {
		t.Fatalf("replay production recovery: %v", err)
	}
	after, err := store.Load(ctx, woken.State.RunID)
	if err != nil || after.Revision != before.Revision {
		t.Fatalf("terminal replay changed parent revision: before=%d after=%d err=%v", before.Revision, after.Revision, err)
	}
	statusAfter, err := service.Status(ctx, woken.State.RunID)
	if err != nil || statusAfter.ExecutionReportSHA256 != status.ExecutionReportSHA256 || statusAfter.ExecutionStatus != "completed" {
		t.Fatalf("terminal replay changed status: before=%#v after=%#v err=%v", status, statusAfter, err)
	}
	if callsAfterReplay := providerScript.callCount(); callsAfterReplay != callsAtTerminal {
		t.Fatalf("terminal recovery replay invoked provider again: before=%d after=%d", callsAtTerminal, callsAfterReplay)
	}
}

const (
	productionCyclePlannerReply           = `{"schema_version":1,"understanding":"Create one approved artifact","implementation_plan":["Write approved.txt in the isolated workspace"],"task_breakdown":["developer creates the artifact"],"acceptance_criteria":["approved.txt contains approved-change"],"risks":[],"assumptions":[],"handoff":{"objective":"Create the fixture artifact","reason":"bounded owner-approved change","evidence":[],"unresolved_issues":[],"notes":""}}`
	productionCycleSandboxExecRequest     = `{"type":"tool_request","tool":"sandbox_exec","input":{"command":"printf 'approved-change\\n' > approved.txt"}}`
	productionCycleSandboxProposalRequest = `{"type":"tool_request","tool":"sandbox_propose_artifact","input":{}}`
	productionCycleDeveloperReply         = `{"schema_version":1,"summary":"Created the requested fixture file","changed_artifacts":[],"commands_executed":["sandbox_exec"],"known_limitations":[],"handoff":{"objective":"Implement the fixture","reason":"approved artifact proposed","evidence":[],"unresolved_issues":[],"notes":""}}`
	productionCycleTesterReply            = `{"schema_version":1,"result":"passed","tests_executed":[{"name":"go_test_all","status":"passed","evidence":[]}],"failure_evidence":[],"reproduction":[],"handoff":{"objective":"Validate the artifact","reason":"the host-registered go_test_all profile checks the approved file","evidence":[],"unresolved_issues":[],"notes":""}}`
	productionCycleReviewerReply          = `{"schema_version":1,"decision":"approved","findings":[],"required_corrections":[],"evidence":[]}`
)

type productionCycleModelScript struct {
	mu        sync.Mutex
	responses []string
	calls     int
	bad       string
}

func newProductionCycleModelScript(responses []string) *productionCycleModelScript {
	return &productionCycleModelScript{responses: append([]string(nil), responses...)}
}

func (s *productionCycleModelScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
		http.Error(w, "unexpected scripted provider route", http.StatusNotFound)
		return
	}
	var request struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil || request.Model != "fw-cycle-script" {
		s.mu.Lock()
		s.bad = "unexpected scripted provider request"
		s.mu.Unlock()
		http.Error(w, "unexpected scripted provider request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if s.calls >= len(s.responses) {
		s.bad = "scripted provider response budget exhausted"
		s.mu.Unlock()
		http.Error(w, "scripted provider response budget exhausted", http.StatusInternalServerError)
		return
	}
	content := s.responses[s.calls]
	s.calls++
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"model": request.Model, "message": map[string]string{"role": "assistant", "content": content}, "done": true, "prompt_eval_count": 1, "eval_count": 1})
}

func (s *productionCycleModelScript) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func waitForProductionCycleApproval(t *testing.T, ctx context.Context, store *approval.Store, runID string) *approval.Approval {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		items, err := store.List(runID)
		if err == nil && len(items) == 1 && items[0].Status == approval.StatusPending {
			return items[0]
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for child OCI proposal approval: items=%#v err=%v: %v", items, err, ctx.Err())
		case <-ticker.C:
		}
	}
}

func postFreewillProductionCycleDecision(t *testing.T, endpoint, token, body string) []byte {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("served approval request: %v", err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("served approval status=%d body=%s", response.StatusCode, result)
	}
	return result
}

func getFreewillProductionCycleApprovals(t *testing.T, endpoint, token string) []api.FreewillChildApproval {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("served child approval list: %v", err)
	}
	defer response.Body.Close()
	var result struct {
		Approvals []api.FreewillChildApproval `json:"approvals"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("served child approval list status=%d approvals=%#v", response.StatusCode, result.Approvals)
	}
	return result.Approvals
}

type productionCycleNotificationSink struct {
	mu            sync.Mutex
	notifications []multiagent.FreewillNotification
}

func (s *productionCycleNotificationSink) NotifyFreewill(_ context.Context, notification multiagent.FreewillNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = append(s.notifications, notification)
	return nil
}

func (s *productionCycleNotificationSink) find(runID, eventType string) *multiagent.FreewillNotification {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, notification := range s.notifications {
		if notification.RunID == runID && notification.EventType == eventType {
			copy := notification
			return &copy
		}
	}
	return nil
}

func waitForProductionCycleNotification(t *testing.T, ctx context.Context, sink *productionCycleNotificationSink, runID, eventType string) *multiagent.FreewillNotification {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("notification %s for %s not delivered: %v", eventType, runID, ctx.Err())
			return nil
		case <-ticker.C:
			if notification := sink.find(runID, eventType); notification != nil {
				return notification
			}
		}
	}
}

func waitForProductionCycleStatus(t *testing.T, ctx context.Context, service *multiagent.FreewillApprovalService, runID, want string) (multiagent.FreewillApprovalStatus, error) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := service.Status(ctx, runID)
		if err == nil && status.ExecutionStatus == want {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, fmt.Errorf("waiting for parent execution status %q: last=%#v err=%v: %w", want, status, err, ctx.Err())
		case <-ticker.C:
		}
	}
}

func requireFreewillProductionOCIImage(t *testing.T, ctx context.Context, dockerPath string) string {
	t.Helper()
	if _, err := runFreewillProductionDocker(ctx, dockerPath, "info", "--format", "{{.ServerVersion}}"); err != nil {
		t.Fatalf("required Linux Docker engine unavailable: %v", err)
	}
	if _, err := runFreewillProductionDocker(ctx, dockerPath, "pull", "--quiet", "debian:bookworm-slim"); err != nil {
		t.Fatalf("pull Debian sandbox image: %v", err)
	}
	output, err := runFreewillProductionDocker(ctx, dockerPath, "image", "inspect", "--format", "{{index .RepoDigests 0}}", "debian:bookworm-slim")
	if err != nil {
		t.Fatalf("resolve immutable Debian image digest: %v", err)
	}
	image := strings.TrimSpace(string(output))
	if !strings.Contains(image, "@sha256:") {
		t.Fatalf("Docker returned no immutable Debian image digest: %q", image)
	}
	return image
}

func requireCachedProductionOCIImage(t *testing.T, ctx context.Context, dockerPath, image string) string {
	t.Helper()
	image = strings.TrimSpace(image)
	if !strings.Contains(image, "@sha256:") {
		t.Fatalf("offline OCI halt test requires an immutable image digest, got %q", image)
	}
	if _, err := runFreewillProductionDocker(ctx, dockerPath, "image", "inspect", image); err != nil {
		t.Fatalf("offline OCI halt test image is not already cached: %v", err)
	}
	return image
}

func waitForProductionCycleOCIContainer(t *testing.T, ctx context.Context, dockerPath, image, marker string) string {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		output, err := runFreewillProductionDocker(ctx, dockerPath, "ps", "--quiet", "--filter", "ancestor="+image)
		if err == nil {
			for _, id := range strings.Fields(string(output)) {
				command, inspectErr := runFreewillProductionDocker(ctx, dockerPath, "inspect", "--format", "{{json .Config.Cmd}}", id)
				if inspectErr != nil || !strings.Contains(string(command), marker) {
					continue
				}
				if _, execErr := runFreewillProductionDocker(ctx, dockerPath, "exec", id, "/bin/sh", "-c", "test -f /workspace/"+marker); execErr == nil {
					return id
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for this fixture's started OCI child (marker %q): err=%v context=%v", marker, err, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForNoProductionCycleOCIContainer(t *testing.T, ctx context.Context, dockerPath, containerID string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		output, err := runFreewillProductionDocker(ctx, dockerPath, "ps", "--all", "--quiet", "--filter", "id="+containerID)
		if err == nil && strings.TrimSpace(string(output)) == "" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for OCI child reap: output=%q err=%v context=%v", strings.TrimSpace(string(output)), err, ctx.Err())
		case <-deadline.C:
			t.Fatalf("fixture OCI child container was not reaped after halt: id=%s output=%q err=%v", containerID, strings.TrimSpace(string(output)), err)
		case <-ticker.C:
		}
	}
}

func runFreewillProductionDocker(ctx context.Context, dockerPath string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, dockerPath, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
