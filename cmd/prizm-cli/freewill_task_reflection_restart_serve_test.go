package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/policy"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	"gopkg.in/yaml.v3"
)

// E59 proves that standalone serve startup recovery reuses the durable E32
// adaptive reflection reservation across OS processes. The terminal receipt
// is seeded by the host reconciliation API below; no child is executed.
func TestE59StandaloneServeRecoversAdaptiveTaskReflectionOnceAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dataDir, runsDir := filepath.Join(root, "data"), filepath.Join(root, "runs")
	workspace, policyDir := filepath.Join(root, "workspace"), filepath.Join(root, "policies")
	for _, dir := range []string{dataDir, runsDir, workspace, policyDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("Synthetic E59 task reflection fixture."), 0600); err != nil {
		t.Fatal(err)
	}
	const ownerID, projectID, agentID = "e59-synthetic-owner", "e59-synthetic-project", "e59-synthetic-agent"
	const firstParent, firstChild = "run_e59_parent_first", "run_e59_child_first"
	const secondParent, secondChild = "run_e59_parent_second", "run_e59_child_second"
	dbPath := filepath.Join(dataDir, "freewill.db")
	seedE59TerminalReceipt(t, root, dbPath, runsDir, workspace, projectID, agentID, firstParent, firstChild, "first")
	reportDigest := func(childID string) string {
		body := []byte(fmt.Sprintf(`{"kind":"e59_synthetic_terminal_report","child_run_id":%q}`, childID))
		sum := sha256.Sum256(body)
		return hex.EncodeToString(sum[:])
	}
	evidenceToChild := map[string]string{
		"sha256:" + reportDigest(firstChild):  firstChild,
		"sha256:" + reportDigest(secondChild): secondChild,
	}

	type callCounts struct{ appraisal, reflection, followup int }
	var callsMu sync.Mutex
	calls := map[string]callCounts{}
	providerFailures := make(chan string, 4)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 {
			providerFailures <- "invalid local fake provider request"
			http.Error(w, "invalid local fake provider request", http.StatusBadRequest)
			return
		}
		all := request.Messages[0].Content + "\n" + request.Messages[1].Content
		task := ""
		for _, childID := range []string{firstChild, secondChild} {
			if strings.Contains(all, childID) {
				task = childID
				break
			}
		}
		for evidenceID, childID := range evidenceToChild {
			if task == "" && strings.Contains(all, evidenceID) {
				task = childID
			}
		}
		if task == "" {
			providerFailures <- fmt.Sprintf("local fake provider received an unbound task request (known=%v): %s", evidenceToChild, all)
			http.Error(w, "unbound synthetic task", http.StatusBadRequest)
			return
		}
		var kind string
		var content string
		switch {
		case strings.Contains(request.Messages[0].Content, "Write a bounded qualitative task self-report"):
			kind = "appraisal"
			content = fmt.Sprintf(`{"appraisal_kind":"satisfaction","perceived_outcome":"completed","why":"The synthetic task felt complete.","context":"Local fixture only.","narrative":"A tentative report for %s.","uncertainty":"One synthetic task is not stable evidence.","reason_labels":["completion"],"scores":{"satisfaction":4,"meaning":3}}`, task)
		case strings.Contains(request.Messages[0].Content, "Perform one bounded reflection pass"):
			kind = "reflection"
			content = fmt.Sprintf(`{"alignment":"disagreement","hypothesis":"The synthetic self-report for %s differs from the host receipt.","uncertainty":"One synthetic task cannot establish a stable pattern."}`, task)
		case strings.Contains(request.Messages[0].Content, "Answer exactly one host-authored follow-up"):
			kind = "followup"
			content = fmt.Sprintf(`{"hypothesis":"The synthetic mismatch for %s remains task-specific.","uncertainty":"No durable preference follows from this fixture."}`, task)
		default:
			providerFailures <- "unexpected local fake provider role"
			http.Error(w, "unexpected synthetic provider role", http.StatusBadRequest)
			return
		}
		callsMu.Lock()
		count := calls[task]
		switch kind {
		case "appraisal":
			count.appraisal++
		case "reflection":
			count.reflection++
		case "followup":
			count.followup++
		}
		calls[task] = count
		callsMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "e59-local-fake", "done": true,
			"message": map[string]any{"role": "assistant", "content": content}})
	}))
	defer providerServer.Close()
	natsURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("start isolated local NATS: %v", err)
	}
	defer stopBus()
	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.InstanceID = "e59-standalone-reflection-recovery"
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir, cfg.Prizm.Workspace = dataDir, runsDir, workspace
	cfg.Prizm.ReadRoots = []string{workspace}
	cfg.Prizm.BindHost, cfg.Prizm.NATSURL, cfg.Prizm.OllamaURL = "127.0.0.1", natsURL, providerServer.URL
	cfg.Prizm.Scheduler = orchestrator.SchedulerConfig{Enabled: false}
	cfg.Prizm.ContextCompression = &agent.CompressionConfig{Enabled: false}
	cfg.API.AuthToken, cfg.API.AuthTokenEnv = "e59-synthetic-api-token", ""
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: agentID, ProjectID: projectID, Model: "e59-local-fake", PolicyDir: policyDir,
		TaskAppraisalEnabled: true, TaskAppraisalOwnerID: ownerID, TaskReflectionEnabled: true, TaskReflectionOwnerID: ownerID,
		MemoryContext: orchestrator.FreewillMemoryContextConfig{OwnerID: ownerID}}
	cfg.Agents = []orchestrator.AgentConfig{{ID: agentID, Role: "researcher", Provider: "ollama", Model: "e59-local-fake", Primary: true}}
	cfg.Projects = []orchestrator.ProjectConfig{{ID: projectID, RepoPath: workspace, FreewillTerminalFactsProjectID: projectID, FreewillTerminalFactsRepoPath: workspace}}
	cfg.Channels, cfg.Actions, cfg.MCPServers = []orchestrator.ChannelConfig{}, []orchestrator.ActionConfig{}, []orchestrator.MCPServerConfig{}
	cfg.Remembrance.Enabled, cfg.FactoryMonitor.Enabled, cfg.Bridge.Enabled, cfg.Codex.Enabled, cfg.ClaudeCode.Enabled, cfg.Autopatch.Enabled = false, false, false, false, false, false
	configBytes, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "prizm.yaml")
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(root, "prizm.exe")
	build := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "./cmd/prizm-cli")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build standalone prizm CLI: %v: %s", err, strings.TrimSpace(string(output)))
	}
	port, err := e27ReservePort()
	if err != nil {
		t.Fatal(err)
	}
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/api/v1/status", port+1)
	type serveProcess struct {
		cmd    *exec.Cmd
		done   chan error
		output *bytes.Buffer
	}
	var processes []serveProcess
	startServe := func() serveProcess {
		output := &bytes.Buffer{}
		cmd := exec.Command(binaryPath, "serve", "--config", configPath, "--port", fmt.Sprint(port))
		cmd.Dir, cmd.Stdout, cmd.Stderr = repo, output, output
		cmd.Env = append(os.Environ(), "TEMP="+root, "TMP="+root, "TMPDIR="+root)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start isolated prizm serve: %v", err)
		}
		p := serveProcess{cmd: cmd, done: make(chan error, 1), output: output}
		processes = append(processes, p)
		go func() { p.done <- cmd.Wait() }()
		if !e27WaitHealth(ctx, strings.TrimSuffix(healthURL, "/api/v1/status"), p.done) {
			t.Fatalf("standalone prizm serve pid %d did not become healthy: %s", cmd.Process.Pid, output.String())
		}
		return p
	}
	stopServe := func(p serveProcess) {
		if p.cmd.ProcessState != nil {
			return
		}
		if err := p.cmd.Process.Kill(); err != nil {
			t.Fatalf("stop isolated prizm serve pid %d: %v", p.cmd.Process.Pid, err)
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("standalone prizm serve pid %d did not stop", p.cmd.Process.Pid)
		}
	}
	t.Cleanup(func() {
		for _, p := range processes {
			if p.cmd.ProcessState == nil {
				_ = p.cmd.Process.Kill()
				select {
				case <-p.done:
				case <-time.After(10 * time.Second):
					t.Errorf("standalone prizm serve pid %d did not stop", p.cmd.Process.Pid)
				}
			}
		}
	})
	countFor := func(task string) callCounts {
		callsMu.Lock()
		defer callsMu.Unlock()
		return calls[task]
	}
	waitFollowUp := func(task string, p serveProcess) {
		t.Helper()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for countFor(task).followup != 1 {
			select {
			case failure := <-providerFailures:
				t.Fatal(failure)
			case err := <-p.done:
				t.Fatalf("serve process exited before adaptive follow-up: %v", err)
			case <-ctx.Done():
				stopServe(p)
				t.Fatalf("timed out waiting for adaptive follow-up for %s: %+v; serve output:\n%s", task, countFor(task), p.output.String())
			case <-ticker.C:
			}
		}
	}
	first := startServe()
	waitFollowUp(firstChild, first)
	firstProof := waitForE59PersistedReflectionProof(t, ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, firstParent, firstChild, reportDigest(firstChild))
	stopServe(first)
	if got := countFor(firstChild); got != (callCounts{appraisal: 1, reflection: 1, followup: 1}) {
		t.Fatalf("first process provider calls=%+v, want one appraisal/reflection/follow-up", got)
	}

	// A second terminal record acts as a recovery-completion sentinel for process B.
	seedE59TerminalReceipt(t, root, dbPath, runsDir, workspace, projectID, agentID, secondParent, secondChild, "second")
	second := startServe()
	waitFollowUp(secondChild, second)
	_ = waitForE59PersistedReflectionProof(t, ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, secondParent, secondChild, reportDigest(secondChild))
	stopServe(second)
	if got := countFor(firstChild); got != (callCounts{appraisal: 1, reflection: 1, followup: 1}) {
		t.Fatalf("restart replay repeated first task provider calls: %+v", got)
	}
	if got := countFor(secondChild); got != (callCounts{appraisal: 1, reflection: 1, followup: 1}) {
		t.Fatalf("second-process recovery provider calls=%+v, want one per bounded stage", got)
	}
	secondProof := readE59PersistedReflectionProof(t, ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, firstParent, firstChild, reportDigest(firstChild))
	if firstProof.AdaptiveContent != secondProof.AdaptiveContent || firstProof.AdaptiveContentSHA256 != secondProof.AdaptiveContentSHA256 ||
		firstProof.OutcomeContent != secondProof.OutcomeContent || firstProof.OutcomeEvent.ID != secondProof.OutcomeEvent.ID ||
		firstProof.OutcomeEvent.Payload["report_sha256"] != secondProof.OutcomeEvent.Payload["report_sha256"] {
		t.Fatalf("persisted first-task adaptive/receipt content changed across restart: before=%#v after=%#v", firstProof, secondProof)
	}
	assertE59NoApprovalOrEffects(t, ctx, dbPath)
}

func seedE59TerminalReceipt(t *testing.T, root, dbPath, runsDir, workspace, projectID, agentID, parentRunID, childRunID, suffix string) {
	t.Helper()
	ctx := context.Background()
	store, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	defer func() { _ = store.Close(); _ = events.Close() }()
	correlationID := "e59-correlation-" + suffix
	request := freewill.IntentionRequest{RequestID: "e59-request-" + suffix, AgentID: agentID, Origin: freewill.OriginAgent,
		CandidateSource: freewill.SourceNew, Title: "Synthetic terminal receipt", Goal: "Exercise served reflection recovery",
		ReasonSummary: "Synthetic local fixture.", RequestedCapabilities: []string{"code"},
		Scope: freewill.Scope{ProjectID: projectID, Workspace: workspace}, IdempotencyKey: "e59-idem-" + suffix, CorrelationID: correlationID}
	fingerprintBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	fingerprintSum := sha256.Sum256(fingerprintBytes)
	fingerprint := hex.EncodeToString(fingerprintSum[:])
	now := time.Now().UTC()
	approvalStore := approval.NewStore(runsDir)
	approvalRecord, err := approval.NewIntentionApproval(parentRunID, correlationID, agentID, projectID,
		approval.IntentionApprovalRef{RequestID: request.RequestID, RequestFingerprint: fingerprint,
			ScopeSummary: "synthetic project workspace", Capabilities: request.RequestedCapabilities},
		approval.PolicyDecision{Decision: approval.DecisionRequiresApproval, Reason: "synthetic fixture state"})
	if err != nil {
		t.Fatal(err)
	}
	approvalRecord.Status, approvalRecord.ApprovedBy, approvalRecord.ApprovedAt = approval.StatusApproved, "synthetic-fixture", &now
	if err := approvalStore.Save(approvalRecord); err != nil {
		t.Fatal(err)
	}
	parent := multiagent.DurableRun{SchemaVersion: multiagent.DurableRunSchemaVersion, Kind: multiagent.DurableRunKindFreewill,
		Phase: multiagent.CheckpointFreewillWake,
		State: multiagent.RunState{SchemaVersion: multiagent.RunStateSchemaVersion, RunID: parentRunID,
			WorkflowID: multiagent.FreewillWorkflowID, WorkflowVersion: 1, CurrentTask: multiagent.TaskReference{ID: "freewill-wake"},
			Status: multiagent.RunStatusCreated, RequestIdentity: &multiagent.RequestIdentity{RequesterID: agentID,
				SessionID: correlationID, Source: "freewill_scheduler", IdempotencyKey: request.IdempotencyKey}, CreatedAt: now, UpdatedAt: now},
		Freewill: &multiagent.FreewillRunState{Lifecycle: freewill.StateRunning, Scope: request.Scope, WakeID: "e59-wake-" + suffix,
			Trigger: "scheduled", DedupeKey: request.IdempotencyKey, CorrelationID: correlationID,
			Candidates: []freewill.CandidateIntention{{CandidateID: "e59-candidate-" + suffix, Request: request}},
			ChoiceID:   "e59-candidate-" + suffix, ExecutionDisabled: true, ApprovalRef: approvalRecord.ApprovalID,
			ApprovalFingerprint: fingerprint, ApprovalReceipt: &multiagent.FreewillApprovalReceipt{ApprovalID: approvalRecord.ApprovalID,
				RequestFingerprint: fingerprint, Decision: multiagent.FreewillApprovalGranted},
			ExecutionDispatch: &multiagent.FreewillExecutionDispatch{Status: "accepted", ChildRunID: childRunID,
				ReportRef: "multiagent_report.json", UpdatedAt: now}}}
	if _, err := store.Create(ctx, parent, nil); err != nil {
		t.Fatalf("create synthetic accepted parent: %v", err)
	}
	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, store,
		multiagent.FileRunClaimer{Root: filepath.Join(root, "approval-claims")}, events, multiagent.DurableRuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := multiagent.NewFreewillApprovalService(runtime, store, approvalStore, policy.NewEvaluator(policy.NewRegistry()), "freewill-policy")
	if err != nil {
		t.Fatal(err)
	}
	approvals.SetTerminalFactSink(&failOnceRecoveryFactSink{})
	reportBody := []byte(fmt.Sprintf(`{"kind":"e59_synthetic_terminal_report","child_run_id":%q}`, childRunID))
	reportDigest := sha256.Sum256(reportBody)
	result := multiagent.FreewillExecutionTerminalResult{ChildRunID: childRunID, ChildStatus: multiagent.RunStatusFailed,
		ReportRef: "multiagent_report.json", ReportSHA256: hex.EncodeToString(reportDigest[:]),
		ValidationSummary: "failed", FailureCode: "validation_failed"}
	terminal, err := approvals.ReconcileExecutionTerminal(ctx, parentRunID, result)
	if err == nil || terminal.Freewill.ExecutionDispatch.Status != "execution_failed" || terminal.Freewill.ExecutionDispatch.ReportSHA256 != result.ReportSHA256 {
		t.Fatalf("synthetic host reconciliation did not persist terminal receipt before capture failure: run=%#v err=%v", terminal.Freewill, err)
	}
}

type e59PersistedReflectionProof struct {
	AdaptiveContent       string
	AdaptiveContentSHA256 string
	OutcomeContent        string
	OutcomeEntryID        string
	SelfReportEntryID     string
	BaseReflectionID      string
	BaseReflectionContent string
	Outcome               freewillTaskOutcome
	OutcomeEvent          event.Event
}

func waitForE59PersistedReflectionProof(t *testing.T, ctx context.Context, dataDir, dbPath, ownerID, projectID, workspace, agentID, parentRunID, childRunID, expectedReportSHA256 string) e59PersistedReflectionProof {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		proof := readE59PersistedReflectionProof(t, ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, parentRunID, childRunID, expectedReportSHA256)
		if proof.AdaptiveContent != "" {
			return proof
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for persisted adaptive record for %s: %v", childRunID, ctx.Err())
		case <-deadline.C:
			t.Fatalf("adaptive provider call completed but its source-validated record was not persisted for %s", childRunID)
		case <-ticker.C:
		}
	}
}

func readE59PersistedReflectionProof(t *testing.T, ctx context.Context, dataDir, dbPath, ownerID, projectID, workspace, agentID, parentRunID, childRunID, expectedReportSHA256 string) e59PersistedReflectionProof {
	t.Helper()
	canonicalWorkspace, err := canonicalFreewillWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, projectID, workspace, events, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	reflection, err := newFreewillTaskReflectionSink(appraisals, &freewillAdaptiveTaskReflectionFake{}, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	taskID := freewillTaskScopeID(projectID, canonicalWorkspace, childRunID)
	entries, err := appraisals.ReadOwnerTask(ctx, ownerID, projectID, canonicalWorkspace, taskID, agentID, parentRunID, childRunID)
	if err != nil {
		t.Fatal(err)
	}
	proof := e59PersistedReflectionProof{}
	for _, entry := range entries {
		if entry.Source == freewillTaskOutcomeSource && entry.Category == "fact" {
			if err := json.Unmarshal([]byte(entry.Content), &proof.Outcome); err == nil && proof.Outcome.Kind == "host_task_outcome" {
				proof.OutcomeContent = entry.Content
				proof.OutcomeEntryID = entry.ID
			}
		}
		if entry.Source == freewillTaskAppraisalSource && entry.Category == "appraisal" {
			var report freewillTaskAppraisal
			if json.Unmarshal([]byte(entry.Content), &report) == nil && report.Kind == "model_task_self_report" && report.ParentRunID == parentRunID && report.ChildRunID == childRunID {
				proof.SelfReportEntryID = entry.ID
			}
		}
	}
	reflectionEntries, err := reflection.ReadOwnerReflection(ctx, ownerID, projectID, canonicalWorkspace, taskID, agentID, parentRunID, childRunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range reflectionEntries {
		var adaptive freewillAdaptiveTaskReflection
		if entry.Source != freewillTaskReflectionSource {
			continue
		}
		if json.Unmarshal([]byte(entry.Content), &adaptive) != nil {
			continue
		}
		if adaptive.Kind == "task_reflection_hypothesis" {
			proof.BaseReflectionID = entry.ID
			proof.BaseReflectionContent = entry.Content
		}
		if adaptive.Kind == "task_reflection_adaptive_follow_up" {
			baseDigest := sha256.Sum256([]byte(proof.BaseReflectionContent))
			if entry.UserID != ownerID || entry.ProjectID != projectID || entry.AgentID != agentID || adaptive.FollowUpCalls != 1 || adaptive.MaxFollowUpCalls != 1 || adaptive.PriorReflectionID != proof.BaseReflectionID || adaptive.PriorReflectionRevision != hex.EncodeToString(baseDigest[:]) || adaptive.ParentRunID != parentRunID || adaptive.ChildRunID != childRunID || adaptive.OutcomeID != proof.OutcomeEntryID || adaptive.SelfReportID != proof.SelfReportEntryID || len(adaptive.SourceRefs) != 3 || adaptive.SourceRefs[0] != proof.OutcomeEntryID || adaptive.SourceRefs[1] != proof.SelfReportEntryID || adaptive.SourceRefs[2] != proof.BaseReflectionID || adaptive.HostOutcome != "not_completed" || adaptive.SelfReportOutcome != "completed" {
				t.Fatalf("adaptive reflection record lost bounds or scope: entry=%#v record=%#v", entry, adaptive)
			}
			proof.AdaptiveContent = entry.Content
			proof.AdaptiveContentSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(entry.Content)))
		}
	}
	if proof.OutcomeContent == "" {
		t.Fatalf("persisted host outcome missing: %#v", proof)
	}
	if proof.Outcome.ProjectID != projectID || proof.Outcome.Workspace != canonicalWorkspace || proof.Outcome.TaskID != taskID ||
		proof.Outcome.AgentID != agentID || proof.Outcome.ParentRunID != parentRunID || proof.Outcome.ChildRunID != childRunID ||
		proof.Outcome.Status != "execution_failed" || proof.Outcome.Validation != "failed" ||
		proof.Outcome.ReportRef != "multiagent_report.json" || proof.Outcome.ReportSHA256 != expectedReportSHA256 ||
		proof.Outcome.Confidence != "host_validated_terminal_receipt" || len(proof.Outcome.ParentEventRefs) != 1 {
		t.Fatalf("persisted host outcome differs from synthetic seed: outcome=%#v expected_report_sha256=%s", proof.Outcome, expectedReportSHA256)
	}
	rows, err := events.Query(ctx, event.EventFilter{RunID: parentRunID, Type: event.EventFreewillExecutionFailed, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted terminal event count=%d, want one for synthetic failed receipt", len(rows))
	}
	proof.OutcomeEvent = rows[0]
	if proof.OutcomeEvent.ID != proof.Outcome.ParentEventRefs[0] || proof.OutcomeEvent.Metadata.RunID != parentRunID ||
		proof.OutcomeEvent.Payload["child_run_id"] != childRunID || proof.OutcomeEvent.Payload["report_ref"] != proof.Outcome.ReportRef ||
		proof.OutcomeEvent.Payload["report_sha256"] != expectedReportSHA256 || proof.OutcomeEvent.Payload["validation_summary"] != "failed" {
		t.Fatalf("terminal event does not match seeded receipt and persisted source refs: outcome=%#v event=%#v", proof.Outcome, proof.OutcomeEvent)
	}
	return proof
}

func assertE59NoApprovalOrEffects(t *testing.T, ctx context.Context, dbPath string) {
	t.Helper()
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	for _, eventType := range []string{event.EventFreewillApprovalProposed, event.EventFreewillApprovalDecided,
		event.EventFreewillExecutionDispatched, event.EventMultiAgentToolDiagnostic, event.EventInteractionActionExecuted} {
		rows, err := events.Query(ctx, event.EventFilter{Type: eventType, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("unexpected approval or effect event %s count=%d", eventType, len(rows))
		}
	}
}
