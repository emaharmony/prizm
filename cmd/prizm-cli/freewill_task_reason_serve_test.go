package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	"gopkg.in/yaml.v3"
)

func TestE48StandaloneServeRestartsPreserveTaskReasonTaxonomy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	dataDir, runsDir, workspace := filepath.Join(work, "data"), filepath.Join(work, "runs"), filepath.Join(work, "workspace")
	for _, path := range []string{dataDir, runsDir, workspace} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	const ownerID, projectID, agentID = "e48-synthetic-owner", "e48-synthetic-project", "e48-synthetic-agent"
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	facts, err := newFreewillTerminalFactMemorySink(dataDir, projectID, workspace, events, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(freewillTaskAppraisalResponse{AppraisalKind: "unknown", PerceivedOutcome: "unknown", Why: "The outcome suggests an unresolved pattern.", Context: "Synthetic governed task receipt.", Narrative: "Synthetic model self-report only.", Uncertainty: "Two synthetic receipts do not establish a verified reason.", ReasonLabels: []string{"emergent_pattern"}, Scores: freewillSyntheticTaskScores()})
	provider := &freewillRetrospectiveFake{narrative: string(response), provider: "local-fake", model: "e48-synthetic-appraiser", version: "fixture-v1"}
	appraisals, err := newFreewillTaskAppraisalSink(facts, provider, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	tasks := []multiagent.FreewillTerminalFact{
		{ProjectID: projectID, Workspace: workspace, AgentID: agentID, IntentionID: "e48-intention-a", ParentRunID: "e48-parent-a", ChildRunID: "e48-child-a", Status: "completed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64), ValidationSummary: "passed", RecordedAt: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)},
		{ProjectID: projectID, Workspace: workspace, AgentID: agentID, IntentionID: "e48-intention-b", ParentRunID: "e48-parent-b", ChildRunID: "e48-child-b", Status: "completed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("b", 64), ValidationSummary: "passed", RecordedAt: time.Date(2026, 11, 12, 12, 1, 0, 0, time.UTC)},
	}
	for _, fact := range tasks {
		receipt := event.NewEvent(event.EventFreewillExecutionCompleted, "synthetic-host", map[string]any{"child_run_id": fact.ChildRunID, "report_ref": fact.ReportRef, "report_sha256": fact.ReportSHA256, "validation_summary": fact.ValidationSummary})
		receipt.Metadata.RunID = fact.ParentRunID
		if err := events.Store(ctx, receipt); err != nil {
			t.Fatal(err)
		}
		if err := facts.CaptureFreewillTerminalFact(ctx, fact); err != nil {
			t.Fatal(err)
		}
		if err := appraisals.Capture(ctx, fact); err != nil {
			t.Fatal(err)
		}
		if err := appraisals.Capture(ctx, fact); err != nil {
			t.Fatalf("same task receipt replay failed: %v", err)
		}
	}
	if provider.calls != len(tasks) {
		t.Fatalf("same-receipt replay repeated model call: got=%d want=%d", provider.calls, len(tasks))
	}
	if err := events.Close(); err != nil {
		t.Fatal(err)
	}

	natsURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer stopBus()
	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.InstanceID = "e48-task-reason-taxonomy"
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir, cfg.Prizm.Workspace = dataDir, runsDir, workspace
	cfg.Prizm.BindHost, cfg.Prizm.NATSURL, cfg.Prizm.OllamaURL = "127.0.0.1", natsURL, "http://127.0.0.1:1"
	cfg.Prizm.Scheduler.Enabled = false
	cfg.Prizm.ContextCompression = &agent.CompressionConfig{Enabled: false}
	cfg.API.AuthToken, cfg.API.AuthTokenEnv = "e48-synthetic-token", ""
	cfg.Freewill.Enabled, cfg.Freewill.AgentID, cfg.Freewill.ProjectID = true, agentID, projectID
	cfg.Freewill.Model, cfg.Freewill.PolicyDir = "unused-local-fake", filepath.Join(root, "policies")
	cfg.Freewill.TaskAppraisalEnabled, cfg.Freewill.TaskAppraisalOwnerID = true, ownerID
	cfg.Agents = []orchestrator.AgentConfig{{ID: agentID, Role: "researcher", Provider: "ollama", Model: "unused-local-fake", Primary: true}}
	cfg.Projects = []orchestrator.ProjectConfig{{ID: projectID, RepoPath: workspace, FreewillTerminalFactsProjectID: projectID, FreewillTerminalFactsRepoPath: workspace}}
	cfg.Channels, cfg.Actions, cfg.MCPServers = []orchestrator.ChannelConfig{}, []orchestrator.ActionConfig{}, []orchestrator.MCPServerConfig{}
	cfg.Remembrance.Enabled, cfg.FactoryMonitor.Enabled, cfg.Bridge.Enabled, cfg.Codex.Enabled, cfg.ClaudeCode.Enabled, cfg.Autopatch.Enabled = false, false, false, false, false, false
	cfgBytes, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "prizm.yaml")
	if err := os.WriteFile(configPath, cfgBytes, 0600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(work, "prizm.exe")
	build := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "./cmd/prizm-cli")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build prizm: %v: %s", err, strings.TrimSpace(string(output)))
	}
	port, err := e27ReservePort()
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port+1)
	type running struct {
		cmd  *exec.Cmd
		done chan error
	}
	var processes []running
	start := func() running {
		cmd := exec.Command(binaryPath, "serve", "--config", configPath, "--port", strconv.Itoa(port))
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "TEMP="+work, "TMP="+work, "TMPDIR="+work)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		process := running{cmd: cmd, done: make(chan error, 1)}
		processes = append(processes, process)
		go func() { process.done <- cmd.Wait() }()
		if !e27WaitHealth(ctx, base, process.done) {
			t.Fatalf("prizm serve did not start pid=%d", cmd.Process.Pid)
		}
		return process
	}
	stop := func(process running) {
		if process.cmd.ProcessState != nil {
			return
		}
		if err := process.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-process.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("serve process %d did not exit", process.cmd.Process.Pid)
		}
	}
	t.Cleanup(func() {
		for _, process := range processes {
			if process.cmd.ProcessState == nil {
				_ = process.cmd.Process.Kill()
				select {
				case <-process.done:
				case <-time.After(10 * time.Second):
					t.Errorf("serve process did not exit")
				}
			}
		}
	})

	readPersisted := func(stage string) {
		events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer events.Close()
		facts, err := newFreewillTerminalFactMemorySink(dataDir, projectID, workspace, events, ownerID)
		if err != nil {
			t.Fatal(err)
		}
		sink, err := newFreewillTaskAppraisalSink(facts, provider, ownerID)
		if err != nil {
			t.Fatal(err)
		}
		for _, fact := range tasks {
			taskID := freewillTaskScopeID(projectID, facts.workspace, fact.ChildRunID)
			rows, err := sink.ReadOwnerTask(ctx, ownerID, projectID, workspace, taskID, agentID, fact.ParentRunID, fact.ChildRunID)
			if err != nil || len(rows) != 2 {
				t.Fatalf("%s exact owner read rows=%d err=%v", stage, len(rows), err)
			}
			var outcome freewillTaskOutcome
			var appraisal freewillTaskAppraisal
			for _, row := range rows {
				switch row.Source {
				case freewillTaskOutcomeSource:
					_ = json.Unmarshal([]byte(row.Content), &outcome)
				case freewillTaskAppraisalSource:
					_ = json.Unmarshal([]byte(row.Content), &appraisal)
				}
			}
			if outcome.Confidence != "host_validated_terminal_receipt" || len(appraisal.ReasonLabels) != 1 || appraisal.ReasonLabels[0].Label != "emergent_pattern" || appraisal.ReasonLabels[0].Class != freewillTaskReasonProposed || appraisal.ReasonLabels[0].Status != freewillTaskReasonProvisional || appraisal.ReasonLabels[0].Confidence != freewillTaskReasonModelHypothesis {
				t.Fatalf("%s reason taxonomy lost provisional status after independent receipt/restart: %#v", stage, appraisal.ReasonLabels)
			}
			if err := validateFreewillTaskReasonLabels(appraisal, outcome, appraisal.OutcomeID); err != nil {
				t.Fatalf("%s reason source refs failed revalidation: %v", stage, err)
			}
			labels, err := sink.ReadOwnerTaskReasonLabels(ctx, ownerID, projectID, workspace, taskID, agentID, fact.ParentRunID, fact.ChildRunID)
			if err != nil || len(labels) != 1 || labels[0].Status != freewillTaskReasonProvisional {
				t.Fatalf("%s validated reason read labels=%#v err=%v", stage, labels, err)
			}
		}
		if _, err := sink.ReadOwnerTask(ctx, "wrong-owner", projectID, workspace, freewillTaskScopeID(projectID, facts.workspace, tasks[0].ChildRunID), agentID, tasks[0].ParentRunID, tasks[0].ChildRunID); err == nil {
			t.Fatal("wrong owner was accepted")
		}
	}

	first := start()
	stop(first)
	readPersisted("after first serve process")
	second := start()
	stop(second)
	readPersisted("after second serve process")
	if provider.calls != len(tasks) {
		t.Fatalf("read/restart path unexpectedly called provider: %d", provider.calls)
	}
	events, err = event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	for _, kind := range []string{event.EventFreewillSelectionStarted, event.EventFreewillSelectionCommitted, event.EventFreewillApprovalProposed, event.EventFreewillApprovalDecided, event.EventFreewillExecutionDispatched, event.EventMultiAgentToolDiagnostic, event.EventInteractionActionExecuted} {
		observed, err := events.Query(ctx, event.EventFilter{Type: kind, Limit: 100})
		if err != nil || len(observed) != 0 {
			t.Fatalf("taxonomy persistence path changed %s events: count=%d err=%v", kind, len(observed), err)
		}
	}
}
