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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"gopkg.in/yaml.v3"
)

// E63 verifies that standalone serve recovery captures bounded multidimensional appraisal scores and reasons
// taxonomy through the production task-appraisal sink. The terminal receipts
// are synthetic reconciliation fixtures; no child is executed.
func TestE63StandaloneServeCapturesMultidimensionalTaskAppraisalAcrossRestart(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("Synthetic E63 task-reason fixture."), 0600); err != nil {
		t.Fatal(err)
	}
	const ownerID, projectID, agentID = "e63-synthetic-owner", "e63-synthetic-project", "e63-synthetic-agent"
	const firstParent, firstChild = "run_e63_parent_first", "run_e63_child_first"
	const secondParent, secondChild = "run_e63_parent_second", "run_e63_child_second"
	dbPath := filepath.Join(dataDir, "freewill.db")
	seedE59TerminalReceipt(t, root, dbPath, runsDir, workspace, projectID, agentID, firstParent, firstChild, "e63-first")
	canonicalWorkspace, err := canonicalFreewillWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	taskToChild := map[string]string{
		freewillTaskScopeID(projectID, canonicalWorkspace, firstChild):  firstChild,
		freewillTaskScopeID(projectID, canonicalWorkspace, secondChild): secondChild,
	}

	var callsMu sync.Mutex
	providerCalls := map[string]int{}
	providerFailures := make(chan string, 4)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 || !strings.Contains(request.Messages[0].Content, "Write a bounded qualitative task self-report") {
			providerFailures <- "unexpected or malformed local fake-provider request"
			http.Error(w, "unexpected synthetic provider role", http.StatusBadRequest)
			return
		}
		var input struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
			providerFailures <- "local fake provider received invalid task-appraisal input"
			http.Error(w, "invalid synthetic task input", http.StatusBadRequest)
			return
		}
		childID := taskToChild[input.TaskID]
		if childID == "" {
			providerFailures <- "local fake provider received an unbound task scope"
			http.Error(w, "unbound synthetic task scope", http.StatusBadRequest)
			return
		}
		callsMu.Lock()
		providerCalls[childID]++
		callsMu.Unlock()
		payload, _ := json.Marshal(freewillTaskAppraisalResponse{
			AppraisalKind: "satisfaction", PerceivedOutcome: "unknown", Why: "Synthetic fixture only.",
			Context: "Local test context.", Narrative: "A tentative task self-report.",
			Uncertainty:  "One synthetic receipt is not stable evidence.",
			ReasonLabels: []string{"creation", "iterative_debugging"},
			Scores:       map[string]int{"fun": 3, "meaning": 4, "challenge": 2, "reward": 4, "satisfaction": 3, "curiosity": 5},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "e63-local-fake", "done": true,
			"message": map[string]any{"role": "assistant", "content": string(payload)}})
	}))
	defer providerServer.Close()
	natsURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("start isolated local NATS: %v", err)
	}
	defer stopBus()
	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.InstanceID = "e63-served-task-reason-taxonomy"
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir, cfg.Prizm.Workspace = dataDir, runsDir, workspace
	cfg.Prizm.ReadRoots = []string{workspace}
	cfg.Prizm.BindHost, cfg.Prizm.NATSURL, cfg.Prizm.OllamaURL = "127.0.0.1", natsURL, providerServer.URL
	cfg.Prizm.Scheduler = orchestrator.SchedulerConfig{Enabled: false}
	cfg.Prizm.ContextCompression = &agent.CompressionConfig{Enabled: false}
	cfg.API.AuthToken, cfg.API.AuthTokenEnv = "e63-synthetic-api-token", ""
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: agentID, ProjectID: projectID, Model: "e63-local-fake", PolicyDir: policyDir,
		TaskAppraisalEnabled: true, TaskAppraisalOwnerID: ownerID}
	cfg.Freewill.Scheduling.Enabled = false
	cfg.Agents = []orchestrator.AgentConfig{{ID: agentID, Role: "researcher", Provider: "ollama", Model: "e63-local-fake", Primary: true}}
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
	binaryBytes, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read standalone prizm CLI build for evidence: %v", err)
	}
	buildSHA := sha256.Sum256(binaryBytes)
	t.Logf("standalone prizm CLI build SHA-256: %x", buildSHA)
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
	callCount := func(childID string) int {
		callsMu.Lock()
		defer callsMu.Unlock()
		return providerCalls[childID]
	}
	waitAppraisalCall := func(childID string, p serveProcess) {
		t.Helper()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for callCount(childID) != 1 {
			select {
			case failure := <-providerFailures:
				t.Fatal(failure)
			case err := <-p.done:
				t.Fatalf("serve process exited before task appraisal for %s: %v", childID, err)
			case <-ctx.Done():
				t.Fatalf("timed out waiting for task appraisal for %s; calls=%d", childID, callCount(childID))
			case <-ticker.C:
			}
		}
	}
	first := startServe()
	waitAppraisalCall(firstChild, first)
	firstProof := waitForE63ReasonProof(t, ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, firstParent, firstChild)
	stopServe(first)
	if callCount(firstChild) != 1 {
		t.Fatalf("first served appraisal calls=%d want=1", callCount(firstChild))
	}

	// The second receipt proves process B completed startup recovery; the first
	// receipt's exact appraisal must remain idempotent and must not be re-run.
	seedE59TerminalReceipt(t, root, dbPath, runsDir, workspace, projectID, agentID, secondParent, secondChild, "e63-second")
	second := startServe()
	waitAppraisalCall(secondChild, second)
	_ = waitForE63ReasonProof(t, ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, secondParent, secondChild)
	stopServe(second)
	if callCount(firstChild) != 1 || callCount(secondChild) != 1 {
		t.Fatalf("served appraisal calls after restart first=%d second=%d, want 1 each", callCount(firstChild), callCount(secondChild))
	}
	select {
	case failure := <-providerFailures:
		t.Fatal(failure)
	default:
	}
	secondProof := readE63ReasonProof(t, ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, firstParent, firstChild)
	if firstProof.AppraisalContent != secondProof.AppraisalContent || firstProof.AppraisalID != secondProof.AppraisalID ||
		!reflect.DeepEqual(firstProof.Labels, secondProof.Labels) || firstProof.OutcomeEvent.ID != secondProof.OutcomeEvent.ID {
		t.Fatalf("first task reason appraisal changed across restart: before=%#v after=%#v", firstProof, secondProof)
	}
	assertE59NoApprovalOrEffects(t, ctx, dbPath)
	assertE63NoSelectorEvents(t, ctx, dbPath)
}

type e63ServedReasonProof struct {
	OutcomeContent   string
	AppraisalContent string
	AppraisalID      string
	Labels           []freewillTaskReasonLabel
	Scores           map[string]int
	Overall          *freewillTaskOverallAssessment
	OutcomeEvent     event.Event
}

func waitForE63ReasonProof(t *testing.T, ctx context.Context, dataDir, dbPath, ownerID, projectID, workspace, agentID, parentRunID, childRunID string) e63ServedReasonProof {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	var lastErr error
	for {
		proof, err := tryReadE63ReasonProof(ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, parentRunID, childRunID)
		if err == nil && len(proof.Labels) == 2 {
			return proof
		}
		lastErr = err
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for served reason capture for %s: %v", childRunID, ctx.Err())
		case <-deadline.C:
			t.Fatalf("task-appraisal provider returned but exact reason records remained unavailable for %s: %v", childRunID, lastErr)
		case <-ticker.C:
		}
	}
}

func readE63ReasonProof(t *testing.T, ctx context.Context, dataDir, dbPath, ownerID, projectID, workspace, agentID, parentRunID, childRunID string) e63ServedReasonProof {
	t.Helper()
	proof, err := tryReadE63ReasonProof(ctx, dataDir, dbPath, ownerID, projectID, workspace, agentID, parentRunID, childRunID)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func tryReadE63ReasonProof(ctx context.Context, dataDir, dbPath, ownerID, projectID, workspace, agentID, parentRunID, childRunID string) (e63ServedReasonProof, error) {
	proof := e63ServedReasonProof{}
	canonicalWorkspace, err := canonicalFreewillWorkspace(workspace)
	if err != nil {
		return proof, err
	}
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		return proof, err
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, projectID, canonicalWorkspace, events, ownerID)
	if err != nil {
		return proof, err
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, ownerID)
	if err != nil {
		return proof, err
	}
	taskID := freewillTaskScopeID(projectID, canonicalWorkspace, childRunID)
	rows, err := appraisals.ReadOwnerTask(ctx, ownerID, projectID, canonicalWorkspace, taskID, agentID, parentRunID, childRunID)
	if err != nil {
		return proof, err
	}
	var outcome freewillTaskOutcome
	var appraisal freewillTaskAppraisal
	var outcomeID string
	for i := range rows {
		entry := &rows[i]
		switch entry.Source {
		case freewillTaskOutcomeSource:
			if json.Unmarshal([]byte(entry.Content), &outcome) == nil && outcome.Kind == "host_task_outcome" {
				proof.OutcomeContent, outcomeID = entry.Content, entry.ID
			}
		case freewillTaskAppraisalSource:
			if json.Unmarshal([]byte(entry.Content), &appraisal) == nil && appraisal.Kind == "model_task_self_report" {
				proof.AppraisalContent, proof.AppraisalID = entry.Content, entry.ID
				proof.Scores, proof.Overall = appraisal.Scores, appraisal.Overall
			}
		}
	}
	if proof.OutcomeContent == "" || proof.AppraisalContent == "" {
		return proof, fmt.Errorf("exact outcome or appraisal not persisted for %s", childRunID)
	}
	proof.Labels, err = appraisals.ReadOwnerTaskReasonLabels(ctx, ownerID, projectID, canonicalWorkspace, taskID, agentID, parentRunID, childRunID)
	if err != nil {
		return proof, err
	}
	if outcome.ProjectID != projectID || outcome.Workspace != canonicalWorkspace || outcome.TaskID != taskID || outcome.AgentID != agentID ||
		outcome.ParentRunID != parentRunID || outcome.ChildRunID != childRunID || outcome.Status != "execution_failed" ||
		outcome.Validation != "failed" || outcome.ReportRef != "multiagent_report.json" || outcome.ReportSHA256 != e63SyntheticReportSHA256(childRunID) ||
		outcome.Confidence != "host_validated_terminal_receipt" || appraisal.OutcomeID != outcomeID || appraisal.ParentRunID != parentRunID ||
		appraisal.ChildRunID != childRunID || appraisal.Confidence != "model_self_report" || len(outcome.ParentEventRefs) != 1 {
		return proof, fmt.Errorf("persisted host outcome/appraisal scope or provenance mismatch for %s", childRunID)
	}
	wantScores := map[string]int{"fun": 3, "meaning": 4, "challenge": 2, "reward": 4, "satisfaction": 3, "curiosity": 5}
	if !reflect.DeepEqual(proof.Scores, wantScores) || proof.Overall == nil || proof.Overall.Score != 3.5 || proof.Overall.DimensionCount != 6 ||
		proof.Overall.Scale != "1_to_5" || proof.Overall.Method != "mean_of_reported_dimensions_rounded_to_1_decimal" || proof.Overall.Confidence != "derived_from_unverified_model_self_report" {
		return proof, fmt.Errorf("persisted multidimensional appraisal or host-derived overall mismatch for %s: scores=%#v overall=%#v", childRunID, proof.Scores, proof.Overall)
	}
	if len(proof.Labels) != 2 || !reflect.DeepEqual(appraisal.ReasonLabels, proof.Labels) {
		return proof, fmt.Errorf("reason label readback mismatch for %s: appraisal=%#v labels=%#v", childRunID, appraisal.ReasonLabels, proof.Labels)
	}
	eventRows, err := events.Query(ctx, event.EventFilter{RunID: parentRunID, Type: event.EventFreewillExecutionFailed, Limit: 10})
	if err != nil {
		return proof, err
	}
	if len(eventRows) != 1 {
		return proof, fmt.Errorf("terminal event count for %s=%d want=1", childRunID, len(eventRows))
	}
	proof.OutcomeEvent = eventRows[0]
	if proof.OutcomeEvent.ID != outcome.ParentEventRefs[0] || proof.OutcomeEvent.Metadata.RunID != parentRunID ||
		proof.OutcomeEvent.Payload["child_run_id"] != childRunID || proof.OutcomeEvent.Payload["report_ref"] != outcome.ReportRef ||
		proof.OutcomeEvent.Payload["report_sha256"] != outcome.ReportSHA256 || proof.OutcomeEvent.Payload["validation_summary"] != outcome.Validation {
		return proof, fmt.Errorf("terminal event does not match persisted outcome for %s", childRunID)
	}
	wantRefs := append([]string{outcomeID}, outcome.ParentEventRefs...)
	seen := map[string]freewillTaskReasonLabel{}
	for _, label := range proof.Labels {
		if !reflect.DeepEqual(label.SourceRefs, wantRefs) || label.Confidence != freewillTaskReasonModelHypothesis {
			return proof, fmt.Errorf("reason label sources/confidence do not match host receipt: %#v", label)
		}
		seen[label.Label] = label
	}
	standard, hasStandard := seen["creation"]
	proposed, hasProposed := seen["iterative_debugging"]
	if !hasStandard || standard.Class != freewillTaskReasonStandard || standard.Status != "catalogued" ||
		!hasProposed || proposed.Class != freewillTaskReasonProposed || proposed.Status != freewillTaskReasonProvisional {
		return proof, fmt.Errorf("standard/provisional reason taxonomy mismatch: %#v", proof.Labels)
	}
	return proof, nil
}

func assertE63NoSelectorEvents(t *testing.T, ctx context.Context, dbPath string) {
	t.Helper()
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	rows, err := events.Query(ctx, event.EventFilter{Type: event.EventFreewillSelectionRetryScheduled, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("unexpected selector retry events: %d", len(rows))
	}
}

func e63SyntheticReportSHA256(childRunID string) string {
	body := []byte(fmt.Sprintf(`{"kind":"e59_synthetic_terminal_report","child_run_id":%q}`, childRunID))
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
