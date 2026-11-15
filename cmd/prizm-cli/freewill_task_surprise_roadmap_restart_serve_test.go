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
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/freewill/telemetry"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/policy"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	"gopkg.in/yaml.v3"
)

// E61 proves that the opt-in roadmap-action surprise pass runs after durable
// terminal/appraisal capture in standalone serve, then appends one later audit
// across a process restart. Parent approvals and terminal results are synthetic
// host fixtures; no OCI child, selector, or external provider is run.
func TestE61StandaloneServeCapturesRoadmapTaskSurpriseAcrossRestart(t *testing.T) {
	runE61StandaloneServeScenario(t, false)
}

func TestE65StandaloneServeCapturesContradictionAuditAcrossRestart(t *testing.T) {
	runE61StandaloneServeScenario(t, true)
}

func runE61StandaloneServeScenario(t *testing.T, contradictionAudit bool) {
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
	readme := "# Milestone Roadmap\n\n| Milestone | Scope | Status |\n|---|---|---|\n| M2 | Add synthetic audit trail | Planned |\n"
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(readme), 0600); err != nil {
		t.Fatal(err)
	}
	const ownerID, projectID, agentID = "e61-synthetic-owner", "e61-synthetic-project", "e61-synthetic-agent"
	const actionID, sourceID = "project:roadmap:M2", "project:readme"
	const firstParent, firstChild = "run_e61_parent_a", "run_e61_child_a"
	const secondParent, secondChild = "run_e61_parent_b", "run_e61_child_b"
	snapshot, err := tool.BuildFreewillProjectContext(ctx, projectID, "synthetic roadmap task", workspace, "")
	if err != nil {
		t.Fatalf("build synthetic project context: %v", err)
	}
	var action *freewill.ProjectRoadmapAction
	for i := range snapshot.RoadmapActions {
		if snapshot.RoadmapActions[i].ID == actionID {
			action = &snapshot.RoadmapActions[i]
			break
		}
	}
	if action == nil || action.SourceID != sourceID {
		t.Fatalf("synthetic README did not produce expected host action: %#v", snapshot.RoadmapActions)
	}
	var sourceRevision string
	for _, source := range snapshot.Sources {
		if source.ID == sourceID {
			sourceRevision = source.Revision
		}
	}
	if sourceRevision == "" {
		t.Fatal("synthetic README source revision is missing")
	}
	dbPath := filepath.Join(dataDir, "freewill.db")
	if seedE61ApprovedTerminalParent(t, root, dbPath, runsDir, workspace, projectID, agentID, ownerID, snapshot.Manifest(), *action, firstParent, firstChild, "a", multiagent.RunStatusCompleted) == "" {
		t.Fatal("synthetic first host report digest is missing")
	}

	var providerCalls atomic.Int32
	providerFailures := make(chan string, 8)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 ||
			!strings.Contains(request.Messages[0].Content, "Write a bounded qualitative task self-report") {
			providerFailures <- "unexpected local fake provider request"
			http.Error(w, "unexpected synthetic provider request", http.StatusBadRequest)
			return
		}
		var input struct {
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(request.Messages[1].Content), &input) != nil || (input.Status != "completed" && input.Status != "execution_failed") {
			providerFailures <- "unbound local fake task appraisal input"
			http.Error(w, "unbound synthetic task", http.StatusBadRequest)
			return
		}
		perceived := "completed"
		if input.Status == "execution_failed" {
			perceived = "not_completed"
		}
		providerCalls.Add(1)
		if contradictionAudit && input.Status == "execution_failed" {
			http.Error(w, "synthetic self-report unavailable", http.StatusServiceUnavailable)
			return
		}
		content := fmt.Sprintf(`{"appraisal_kind":"satisfaction","perceived_outcome":%q,"why":"Synthetic report only.","context":"Local fixture only.","narrative":"A tentative report.","uncertainty":"One synthetic task is not stable evidence.","reason_labels":["completion"],"scores":{"satisfaction":4,"curiosity":3}}`, perceived)
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "e61-local-fake", "done": true,
			"message": map[string]any{"role": "assistant", "content": content}})
	}))
	defer providerServer.Close()
	natsURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("start isolated local NATS: %v", err)
	}
	defer stopBus()
	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.InstanceID = "e61-standalone-surprise-recovery"
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir, cfg.Prizm.Workspace = dataDir, runsDir, workspace
	cfg.Prizm.ReadRoots = []string{workspace}
	cfg.Prizm.BindHost, cfg.Prizm.NATSURL, cfg.Prizm.OllamaURL = "127.0.0.1", natsURL, providerServer.URL
	cfg.Prizm.Scheduler = orchestrator.SchedulerConfig{Enabled: false}
	cfg.Prizm.ContextCompression = &agent.CompressionConfig{Enabled: false}
	cfg.API.AuthToken, cfg.API.AuthTokenEnv = "e61-synthetic-api-token", ""
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: agentID, ProjectID: projectID, Model: "e61-local-fake", PolicyDir: policyDir,
		TaskAppraisalEnabled: true, TaskAppraisalOwnerID: ownerID,
		TaskSurprise: orchestrator.FreewillTaskSurpriseConfig{Enabled: true, ProjectActionID: actionID, SourceID: sourceID,
			SourceRevisionSHA256: sourceRevision, ExpectedOutcome: orchestrator.FreewillTaskSurpriseCompleted, ContradictionAuditEnabled: contradictionAudit},
		MemoryContext: orchestrator.FreewillMemoryContextConfig{OwnerID: ownerID}}
	cfg.Agents = []orchestrator.AgentConfig{{ID: agentID, Role: "researcher", Provider: "ollama", Model: "e61-local-fake", Primary: true}}
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
			t.Fatalf("stop standalone prizm serve pid %d: %v", p.cmd.Process.Pid, err)
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("standalone prizm serve pid %d did not stop", p.cmd.Process.Pid)
		}
	}
	defer func() {
		for _, p := range processes {
			if p.cmd.ProcessState == nil {
				_ = p.cmd.Process.Kill()
			}
		}
	}()
	firstProcess := startServe()
	firstProof := waitForE61SurpriseProof(t, ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, actionID, sourceID, sourceRevision, *action, 0, firstProcess.output)
	var firstContradictionProof e65ContradictionProof
	if contradictionAudit {
		firstContradictionProof = waitForE65ContradictionProof(t, ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, actionID, sourceID, sourceRevision, action.Text, 0, firstProcess.output)
		if firstContradictionProof.Forecast.ExpectedOutcome != "completed" || firstContradictionProof.Forecast.Source.Claim != "completed" ||
			len(firstContradictionProof.Audits) != 0 {
			t.Fatalf("process A did not retain only the tentative host-configured forecast and first host receipt: %#v", firstContradictionProof)
		}
	}
	if firstProof.Prediction.SourceReceipt.ParentRunID != firstParent || firstProof.Prediction.SourceReceipt.ChildRunID != firstChild ||
		firstProof.Prediction.SourceReceipt.Digest == "" || firstProof.Prediction.SourceSelfReport.ParentRunID != firstParent ||
		firstProof.Prediction.SourceSelfReport.ChildRunID != firstChild || firstProof.Prediction.ExpectedOutcome != telemetry.OutcomeCompleted {
		t.Fatalf("process A prediction has incorrect source lineage: %#v", firstProof.Prediction)
	}
	stopServe(firstProcess)
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("process A provider calls=%d, want exactly one task appraisal", got)
	}
	secondReportDigest := seedE61ApprovedTerminalParent(t, root, dbPath, runsDir, workspace, projectID, agentID, ownerID, snapshot.Manifest(), *action, secondParent, secondChild, "b", multiagent.RunStatusFailed)
	secondProcess := startServe()
	var secondProof e61SurpriseProof
	var secondContradictionProof e65ContradictionProof
	if contradictionAudit {
		secondContradictionProof = waitForE65ContradictionProof(t, ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, actionID, sourceID, sourceRevision, action.Text, 1, secondProcess.output)
	} else {
		secondProof = waitForE61SurpriseProof(t, ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, actionID, sourceID, sourceRevision, *action, 1, secondProcess.output)
	}
	if got := providerCalls.Load(); got != 2 {
		t.Fatalf("both processes provider calls=%d, want one task appraisal per independent source", got)
	}
	if !contradictionAudit && len(secondProof.Revisions) != 1 {
		t.Fatalf("served surprise revision count=%d, want exactly one: %#v", len(secondProof.Revisions), secondProof.Revisions)
	}
	if contradictionAudit {
		if len(secondContradictionProof.Audits) != 1 || secondContradictionProof.Audits[0].Result != "contradiction" ||
			secondContradictionProof.Audits[0].Attribution != "host_decided_from_typed_receipts_only" ||
			secondContradictionProof.Audits[0].NewSource.Claim != "not_completed" ||
			secondContradictionProof.Audits[0].NewSource.ParentRunID != secondParent || secondContradictionProof.Audits[0].NewSource.ChildRunID != secondChild ||
			secondContradictionProof.Audits[0].NewSource.Digest == "" ||
			secondContradictionProof.AuditSelfReportPresent ||
			secondContradictionProof.ForecastRow.ID != firstContradictionProof.ForecastRow.ID ||
			secondContradictionProof.ForecastRow.Content != firstContradictionProof.ForecastRow.Content ||
			secondContradictionProof.Forecast.ForecastID != firstContradictionProof.Forecast.ForecastID {
			t.Fatalf("process B did not append one source-bound contradiction while preserving the original forecast: before=%#v after=%#v", firstContradictionProof, secondContradictionProof)
		}
		if secondContradictionProof.Audits[0].OriginalForecastDigest != freewillBindingDigest(secondContradictionProof.ForecastRow.ID,
			secondContradictionProof.ForecastRow.Content, secondContradictionProof.ForecastRow.Source) ||
			secondContradictionProof.Audits[0].OriginalForecastID != secondContradictionProof.Forecast.ForecastID {
			t.Fatal("process B audit does not retain the exact original forecast digest and ID")
		}
	}
	stopServe(secondProcess)
	if contradictionAudit {
		if !strings.Contains(secondProcess.output.String(), "[FREEWILL] recovery scan failed: provider: HTTP request failed") {
			t.Fatal("optional E49 audit swallowed the original terminal-capture appraisal error")
		}
		assertE59NoApprovalOrEffects(t, ctx, dbPath)
		assertE63NoSelectorEvents(t, ctx, dbPath)
		return
	}
	revision := secondProof.Revisions[0]
	if revision.Relation != telemetry.RelationOpposite || revision.ExpectedOutcome != telemetry.OutcomeCompleted || revision.HostOutcome != telemetry.OutcomeNotCompleted ||
		revision.ReportedOutcome != telemetry.OutcomeNotCompleted || !revision.FurtherEvidenceRequired || revision.DurableInterest ||
		revision.SourceReceipt.ParentRunID != secondParent || revision.SourceReceipt.ChildRunID != secondChild || revision.SourceReceipt.Digest == "" ||
		revision.SourceSelfReport.ParentRunID != secondParent || revision.SourceSelfReport.ChildRunID != secondChild || revision.SourceSelfReport.Digest == "" {
		t.Fatalf("served surprise revision does not preserve bounded opposite evidence: %#v", revision)
	}
	if secondProof.PredictionContent != firstProof.PredictionContent || secondProof.PredictionHash != firstProof.PredictionHash ||
		secondProof.Prediction.SourceReceipt.ID != firstProof.Prediction.SourceReceipt.ID || secondProof.Prediction.SourceReceipt.Digest != firstProof.Prediction.SourceReceipt.Digest ||
		secondProof.Prediction.SourceSelfReport.ID != firstProof.Prediction.SourceSelfReport.ID || secondProof.Prediction.SourceSelfReport.Digest != firstProof.Prediction.SourceSelfReport.Digest {
		t.Fatal("restart changed the original prediction bytes or source lineage")
	}
	if secondReportDigest == "" {
		t.Fatal("synthetic second report digest is missing")
	}
	assertE59NoApprovalOrEffects(t, ctx, dbPath)
	assertE63NoSelectorEvents(t, ctx, dbPath)
}

func seedE61ApprovedTerminalParent(t *testing.T, root, dbPath, runsDir, workspace, projectID, agentID, ownerID string,
	projectManifest freewill.ProjectContextManifest, action freewill.ProjectRoadmapAction, parentRunID, childRunID, hashByte string,
	childStatus multiagent.RunStatus) string {
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
	suffix := strings.TrimPrefix(parentRunID, "run_e61_parent_")
	correlationID := "e61-correlation-" + suffix
	request := freewill.IntentionRequest{RequestID: "e61-request-" + suffix, AgentID: agentID, Origin: freewill.OriginAgent,
		CandidateSource: freewill.SourceNew, Title: action.Text, Goal: action.Text, ReasonSummary: "Host-bound synthetic roadmap action.",
		RequestedCapabilities: []string{"code"}, Scope: freewill.Scope{ProjectID: projectID, Workspace: workspace},
		IdempotencyKey: "e61-idem-" + suffix, CorrelationID: correlationID}
	fingerprint := e61RequestFingerprint(request)
	now := time.Now().UTC()
	approvalStore := approval.NewStore(runsDir)
	approvalRecord, err := approval.NewIntentionApproval(parentRunID, correlationID, agentID, projectID,
		approval.IntentionApprovalRef{RequestID: request.RequestID, RequestFingerprint: fingerprint,
			ScopeSummary: "synthetic project roadmap action", Capabilities: request.RequestedCapabilities},
		approval.PolicyDecision{Decision: approval.DecisionRequiresApproval, Reason: "synthetic fixture state"})
	if err != nil {
		t.Fatal(err)
	}
	approvalRecord.Status, approvalRecord.ApprovedBy, approvalRecord.ApprovedAt = approval.StatusApproved, "synthetic-fixture", &now
	if err := approvalStore.Save(approvalRecord); err != nil {
		t.Fatal(err)
	}
	candidateID := "e61-candidate-" + suffix
	candidate := freewill.CandidateIntention{CandidateID: candidateID, Request: request, BasisRefs: []string{action.SourceID},
		Uncertainty: "Synthetic README evidence only.", ProjectActionID: action.ID, ProjectGrounding: "roadmap_action"}
	parent := multiagent.DurableRun{SchemaVersion: multiagent.DurableRunSchemaVersion, Kind: multiagent.DurableRunKindFreewill, Phase: multiagent.CheckpointFreewillWake,
		State: multiagent.RunState{SchemaVersion: multiagent.RunStateSchemaVersion, RunID: parentRunID, WorkflowID: multiagent.FreewillWorkflowID,
			WorkflowVersion: 1, CurrentTask: multiagent.TaskReference{ID: "freewill-wake"}, Status: multiagent.RunStatusCreated,
			RequestIdentity: &multiagent.RequestIdentity{RequesterID: agentID, SessionID: correlationID, Source: "freewill_scheduler", IdempotencyKey: request.IdempotencyKey},
			CreatedAt:       now, UpdatedAt: now},
		Freewill: &multiagent.FreewillRunState{Lifecycle: freewill.StateRunning, Scope: request.Scope, WakeID: "e61-wake-" + suffix,
			Trigger: "scheduled", DedupeKey: request.IdempotencyKey, CorrelationID: correlationID,
			Candidates: []freewill.CandidateIntention{candidate}, ChoiceID: candidateID, ExecutionDisabled: true,
			ApprovalRef: approvalRecord.ApprovalID, ApprovalFingerprint: fingerprint,
			ApprovalReceipt:        &multiagent.FreewillApprovalReceipt{ApprovalID: approvalRecord.ApprovalID, RequestFingerprint: fingerprint, Decision: multiagent.FreewillApprovalGranted},
			ExecutionDispatch:      &multiagent.FreewillExecutionDispatch{Status: "accepted", ChildRunID: childRunID, ReportRef: "multiagent_report.json", UpdatedAt: now},
			ProjectContextManifest: &projectManifest}}
	if _, err := store.Create(ctx, parent, nil); err != nil {
		t.Fatalf("create synthetic approved parent: %v", err)
	}
	input, err := freewillReferenceInput(request)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := multiagent.ApplyReferenceOverrides(multiagent.DefaultReferenceDefinition(), input)
	if err != nil {
		t.Fatal(err)
	}
	childManifest := referenceWorkflowManifest{SchemaVersion: 1, RunID: childRunID, WorkflowID: multiagent.ReferenceWorkflowID,
		Input: input, Definition: definition, WorkspaceID: freewillBindingDigest("e61-workspace", childRunID), WorkspacePath: workspace,
		SourceWorkspacePath: workspace, WorkspaceCleaned: true}
	manifestBytes, err := json.Marshal(childManifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := referenceManifestPath(runsDir, childRunID)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestBytes, 0600); err != nil {
		t.Fatal(err)
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
	reportBody := []byte(fmt.Sprintf(`{"kind":"e61_synthetic_terminal_report","child_run_id":%q}`, childRunID))
	reportSum := sha256.Sum256(reportBody)
	reportDigest := hex.EncodeToString(reportSum[:])
	validation, failureCode := "passed", ""
	if childStatus == multiagent.RunStatusFailed {
		validation, failureCode = "failed", "validation_failed"
	}
	result := multiagent.FreewillExecutionTerminalResult{ChildRunID: childRunID, ChildStatus: childStatus,
		ReportRef: "multiagent_report.json", ReportSHA256: reportDigest, ValidationSummary: validation, FailureCode: failureCode}
	terminal, err := approvals.ReconcileExecutionTerminal(ctx, parentRunID, result)
	if err == nil || terminal.Freewill.ExecutionDispatch.ReportSHA256 != reportDigest {
		t.Fatalf("synthetic host terminal reconciliation did not persist before the seeded sink failure: status=%#v err=%v", terminal.Freewill.ExecutionDispatch, err)
	}
	return reportDigest
}

func e61RequestFingerprint(request freewill.IntentionRequest) string {
	request.RequestedCapabilities = append([]string(nil), request.RequestedCapabilities...)
	sort.Strings(request.RequestedCapabilities)
	encoded, _ := json.Marshal(request)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

type e61UnusedAppraisalProvider struct{}

func (e61UnusedAppraisalProvider) Retrospect(context.Context, memory.Memory) (string, string, string, string, error) {
	return "", "", "", "", fmt.Errorf("unused reader provider")
}

type e61SurpriseProof struct {
	PredictionContent string
	PredictionHash    string
	Prediction        telemetry.Prediction
	Revisions         []telemetry.SurpriseRevision
}

type e65ContradictionProof struct {
	ForecastRow            memory.Memory
	Forecast               freewillTaskContradictionForecast
	AuditRows              []memory.Memory
	Audits                 []freewillTaskContradictionAudit
	AuditSelfReportPresent bool
}

func waitForE65ContradictionProof(t *testing.T, ctx context.Context, dataDir, dbPath, ownerID, projectID, workspace, agentID,
	actionID, sourceID, sourceRevision, actionText string, wantAudits int, serveLogs *bytes.Buffer) e65ContradictionProof {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	var latest e65ContradictionProof
	var lastErr error
	for {
		proof, err := readE65ContradictionProof(ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, actionID, sourceID, sourceRevision, actionText)
		if err == nil {
			latest = proof
			if proof.ForecastRow.ID != "" && len(proof.Audits) >= wantAudits {
				return proof
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for E65 contradiction records: %v", ctx.Err())
		case <-deadline.C:
			logs := ""
			if serveLogs != nil {
				logs = serveLogs.String()
			}
			t.Fatalf("timed out waiting for E65 contradiction records (wanted %d audits; proof=%#v; read error=%v; serve logs: %s)", wantAudits, latest, lastErr, logs)
		case <-ticker.C:
		}
	}
}

func readE65ContradictionProof(ctx context.Context, dataDir, dbPath, ownerID, projectID, workspace, agentID,
	actionID, sourceID, sourceRevision, actionText string) (e65ContradictionProof, error) {
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		return e65ContradictionProof{}, err
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, projectID, workspace, events, ownerID)
	if err != nil {
		return e65ContradictionProof{}, err
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, e61UnusedAppraisalProvider{}, ownerID)
	if err != nil {
		return e65ContradictionProof{}, err
	}
	binding := freewillTaskAuditBinding{OwnerID: ownerID, ProjectID: projectID, Workspace: workspace, AgentID: agentID,
		TaskIdentity: actionID, ManifestID: freewillBindingDigest("freewill-roadmap-source/v1", sourceID, sourceRevision),
		ContextDigest: freewillBindingDigest("freewill-roadmap-action/v1", actionID, sourceID, sourceRevision, freewillBindingDigest(actionText))}
	sink, err := newFreewillTaskContradictionAuditSink(appraisals, binding)
	if err != nil {
		return e65ContradictionProof{}, err
	}
	rows, err := sink.Read(ctx)
	if err != nil {
		return e65ContradictionProof{}, err
	}
	var proof e65ContradictionProof
	for _, row := range rows {
		switch row.Category {
		case "hypothesis":
			if proof.ForecastRow.ID != "" || json.Unmarshal([]byte(row.Content), &proof.Forecast) != nil {
				return e65ContradictionProof{}, fmt.Errorf("E65 forecast is malformed or ambiguous")
			}
			proof.ForecastRow = row
		case "audit":
			var audit freewillTaskContradictionAudit
			if json.Unmarshal([]byte(row.Content), &audit) != nil {
				return e65ContradictionProof{}, fmt.Errorf("E65 audit is malformed")
			}
			proof.AuditRows = append(proof.AuditRows, row)
			proof.Audits = append(proof.Audits, audit)
		}
	}
	for _, audit := range proof.Audits {
		canonicalWorkspace, err := canonicalFreewillWorkspace(workspace)
		if err != nil {
			return e65ContradictionProof{}, err
		}
		taskID := freewillTaskScopeID(projectID, canonicalWorkspace, audit.NewSource.ChildRunID)
		taskRows, err := appraisals.ReadOwnerTask(ctx, ownerID, projectID, canonicalWorkspace, taskID, agentID, audit.NewSource.ParentRunID, audit.NewSource.ChildRunID)
		if err != nil {
			return e65ContradictionProof{}, err
		}
		for _, row := range taskRows {
			if row.Source == freewillTaskAppraisalSource && row.Category == "appraisal" {
				proof.AuditSelfReportPresent = true
			}
		}
	}
	return proof, nil
}

func waitForE61SurpriseProof(t *testing.T, ctx context.Context, dataDir, dbPath, ownerID, projectID, workspace, agentID,
	actionID, sourceID, sourceRevision string, action freewill.ProjectRoadmapAction, wantRevisions int, serveLogs *bytes.Buffer) e61SurpriseProof {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		proof, err := readE61SurpriseProof(ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, actionID, sourceID, sourceRevision, action)
		if err == nil && proof.PredictionContent != "" && len(proof.Revisions) >= wantRevisions {
			return proof
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for E61 durable surprise evidence: %v", ctx.Err())
		case <-deadline.C:
			logs := ""
			if serveLogs != nil {
				logs = serveLogs.String()
			}
			t.Fatalf("timed out waiting for E61 durable surprise evidence (wanted %d revisions; last read: %v; serve logs: %s)", wantRevisions, err, logs)
		case <-ticker.C:
		}
	}
}

func readE61SurpriseProof(ctx context.Context, dataDir, dbPath, ownerID, projectID, workspace, agentID,
	actionID, sourceID, sourceRevision string, action freewill.ProjectRoadmapAction) (e61SurpriseProof, error) {
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		return e61SurpriseProof{}, err
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, projectID, workspace, events, ownerID)
	if err != nil {
		return e61SurpriseProof{}, err
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, e61UnusedAppraisalProvider{}, ownerID)
	if err != nil {
		return e61SurpriseProof{}, err
	}
	binding := freewillTaskAuditBinding{OwnerID: ownerID, ProjectID: projectID, Workspace: workspace, AgentID: agentID,
		TaskIdentity: actionID, ManifestID: freewillBindingDigest("freewill-roadmap-source/v1", sourceID, sourceRevision),
		ContextDigest: freewillBindingDigest("freewill-roadmap-action/v1", actionID, sourceID, sourceRevision, freewillBindingDigest(action.Text))}
	surprise, err := newFreewillTaskSurpriseSink(appraisals, binding)
	if err != nil {
		return e61SurpriseProof{}, err
	}
	rows, err := surprise.Read(ctx)
	if err != nil {
		return e61SurpriseProof{}, err
	}
	var proof e61SurpriseProof
	for _, row := range rows {
		switch row.Category {
		case "hypothesis":
			var prediction telemetry.Prediction
			if json.Unmarshal([]byte(row.Content), &prediction) != nil || prediction.Kind != "tentative_task_appraisal_prediction" || proof.PredictionContent != "" {
				return e61SurpriseProof{}, fmt.Errorf("invalid or ambiguous E61 prediction")
			}
			proof.PredictionContent = row.Content
			proof.PredictionHash = freewillBindingDigest(row.ID, row.Content, row.Source)
			proof.Prediction = prediction
		case "audit":
			var revision telemetry.SurpriseRevision
			if json.Unmarshal([]byte(row.Content), &revision) != nil || revision.Kind != "preference_surprise_revision" {
				return e61SurpriseProof{}, fmt.Errorf("invalid E61 revision")
			}
			proof.Revisions = append(proof.Revisions, revision)
		}
	}
	_ = sourceID
	_ = sourceRevision
	return proof, nil
}
