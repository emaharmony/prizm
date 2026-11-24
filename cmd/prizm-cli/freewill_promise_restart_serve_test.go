package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	"gopkg.in/yaml.v3"
)

func TestE79ServedPendingPromiseRevisionPreservesApprovedChildAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	dataDir := filepath.Join(work, "data")
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	workspace := newFreewillDispatchTestRepo(t)
	if err = os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = gitx.RunCommand(ctx, workspace, "", "git", "add", "base.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err = gitx.RunCommand(ctx, workspace, "", "git", "commit", "-m", "add synthetic target"); err != nil {
		t.Fatal(err)
	}
	parentRoot := filepath.Join(dataDir, "fixture")
	if err = os.MkdirAll(parentRoot, 0700); err != nil {
		t.Fatal(err)
	}
	parent, childApprovals, dispatcher, original := seedE79ApprovedParent(t, ctx, parentRoot, workspace)
	defer func() {
		if dispatcher.runtime != nil {
			_ = dispatcher.runtime.close()
		}
	}()
	if err = parent.events.Close(); err != nil {
		t.Fatal(err)
	}
	if err = parent.store.Close(); err != nil {
		t.Fatal(err)
	}
	parentDB, err := os.ReadFile(filepath.Join(parentRoot, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dataDir, "freewill.db"), parentDB, 0600); err != nil {
		t.Fatal(err)
	}

	natsURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("embedded local NATS: %v", err)
	}
	defer stopBus()
	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.InstanceID = "e79-synthetic"
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir = dataDir, filepath.Join(parentRoot, "child-runs")
	cfg.Prizm.Workspace, cfg.Prizm.BindHost, cfg.Prizm.NATSURL = workspace, "127.0.0.1", natsURL
	cfg.Prizm.OllamaURL = "http://127.0.0.1:1"
	cfg.Prizm.Scheduler.Enabled = false
	cfg.Prizm.ContextCompression = &agent.CompressionConfig{Enabled: false}
	cfg.API.AuthToken, cfg.API.AuthTokenEnv = e27SyntheticToken, ""
	ownerToken, recipientToken := "e79-synthetic-owner-token", "e79-synthetic-recipient-token"
	cfg.API.LocalDiaryPrincipalAuth = orchestrator.LocalDiaryPrincipalAuthConfig{Enabled: true, OwnerPrincipalID: "synthetic-owner", Credentials: []orchestrator.LocalDiaryPrincipalCredentialConfig{
		{PrincipalID: "synthetic-owner", TokenSHA256: diaryCredentialDigest(ownerToken)},
		{PrincipalID: "synthetic-recipient", TokenSHA256: diaryCredentialDigest(recipientToken)},
	}}
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: "e28-agent", ProjectID: "e28-project", Model: "unused-local-fake", PolicyDir: filepath.Join(repo, "policies"), PromiseConfirmation: orchestrator.FreewillPromiseConfirmationConfig{Enabled: true, OwnerID: "synthetic-owner", PromiseID: "promise-e79", Scope: "review the specified local fixture", Deadline: "2026-11-12T17:00:00Z"}}
	cfg.Freewill.DiaryEnabled = true
	cfg.Freewill.DiaryOwnerID = "synthetic-owner"
	cfg.Freewill.DiaryAnnotationsEnabled = true
	cfg.Freewill.DiaryAnnotationsOwnerID = "synthetic-owner"
	cfg.Agents = []orchestrator.AgentConfig{{ID: "e28-agent", Role: "researcher", Provider: "ollama", Model: "unused-local-fake", Primary: true}}
	cfg.Projects = []orchestrator.ProjectConfig{{ID: "e28-project", RepoPath: workspace, FreewillTerminalFactsProjectID: "e28-project", FreewillTerminalFactsRepoPath: workspace}}
	cfg.Channels, cfg.Actions, cfg.MCPServers = []orchestrator.ChannelConfig{}, []orchestrator.ActionConfig{}, []orchestrator.MCPServerConfig{}
	cfg.Remembrance.Enabled, cfg.FactoryMonitor.Enabled, cfg.Bridge.Enabled = false, false, false
	cfg.Codex.Enabled, cfg.ClaudeCode.Enabled, cfg.Autopatch.Enabled = false, false, false
	cfgBytes, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "prizm.yaml")
	if err = os.WriteFile(configPath, cfgBytes, 0600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(work, "prizm.exe")
	build := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "./cmd/prizm-cli")
	build.Dir = repo
	build.Env = append(os.Environ(), "GOPROXY=off")
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build actual serve binary: %v: %s", buildErr, strings.TrimSpace(string(output)))
	}
	port, err := e27ReservePort()
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port+1)
	var processes []e27ServeProcess
	t.Cleanup(func() { e27CleanupServeProcesses(t, processes) })
	start := func() e27ServeProcess {
		return e27StartServe(t, ctx, binaryPath, configPath, repo, work, port, base, &processes)
	}
	stop := func(p e27ServeProcess) { e27StopServe(t, p) }
	query := url.Values{"owner_id": {"synthetic-owner"}, "project_id": {"e28-project"}, "workspace": {workspace}, "agent_id": {"e28-agent"}}.Encode()
	get := func() []byte {
		return e27APIRequestWithBearer(t, http.MethodGet, base+"/api/v1/freewill/promises/promise-e79?"+query, "", ownerToken)
	}
	post := func(path, body string) []byte {
		return e27APIRequestWithBearer(t, http.MethodPost, base+path, body, ownerToken)
	}

	serveA := start()
	initial := get()
	var initialView struct {
		Current struct {
			Revision int `json:"revision"`
		} `json:"current"`
	}
	if err = json.Unmarshal(initial, &initialView); err != nil || initialView.Current.Revision != 1 {
		t.Fatalf("initial promise not served: %s err=%v", initial, err)
	}
	proposalBody, err := json.Marshal(map[string]any{"owner_id": "synthetic-owner", "project_id": "e28-project", "workspace": workspace, "agent_id": "e28-agent", "request_id": "e79-fake-request-1", "expected_revision": 1, "scope": "review the fixture and provide a concise handoff", "deadline": "2026-11-13T17:00:00Z", "impact_hypothesis": "A changed scope or deadline may alter the user-facing promise."})
	if err != nil {
		t.Fatal(err)
	}
	pending := post("/api/v1/freewill/promises/promise-e79/proposals", string(proposalBody))
	var pendingView struct {
		Current struct {
			Revision int `json:"revision"`
		} `json:"current"`
		Proposals []struct {
			ProposalID string `json:"proposal_id"`
			Status     string `json:"status"`
			Proposer   string `json:"proposer"`
		} `json:"proposals"`
	}
	if err = json.Unmarshal(pending, &pendingView); err != nil || pendingView.Current.Revision != 1 || len(pendingView.Proposals) != 1 || pendingView.Proposals[0].Status != "pending" || pendingView.Proposals[0].Proposer != "local_synthetic_fake" {
		t.Fatalf("proposal changed effective promise or did not remain pending: %s err=%v", pending, err)
	}
	assertE79StateUnchanged(t, ctx, dataDir, original, childApprovals, dispatcher, workspace)
	stop(serveA)
	serveB := start()
	afterRestart := get()
	if !reflect.DeepEqual(afterRestart, pending) {
		t.Fatalf("pending proposal changed across actual serve restart: %s != %s", afterRestart, pending)
	}
	assertE79StateUnchanged(t, ctx, dataDir, original, childApprovals, dispatcher, workspace)
	stop(serveB)
}

func seedE79ApprovedParent(t *testing.T, ctx context.Context, root, workspace string) (e28IntegratedState, *approval.Store, *oneCycleChildDispatcher, multiagent.DurableRun) {
	t.Helper()
	runsDir := filepath.Join(root, "child-runs")
	if err := os.MkdirAll(runsDir, 0700); err != nil {
		t.Fatal(err)
	}
	childApprovals := approval.NewStore(filepath.Join(root, "child-approvals"))
	dispatcher := &oneCycleChildDispatcher{runDir: runsDir, source: workspace, approvalStore: childApprovals}
	open := openE28IntegratedState(t, root)
	open.service.SetExecutionDispatcher(dispatcher)
	const project, agent, owner, runID = "e28-project", "e28-agent", "synthetic-owner", "run-e79-authorized"
	request := freewill.IntentionRequest{RequestID: "e79-request-1", AgentID: agent, Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: "Change one fixture", Goal: "Change base.txt to changed", ReasonSummary: "The request is bounded to one patch target and a separate patch grant.", RequestedCapabilities: []string{"code"}, Scope: freewill.Scope{ProjectID: project, Workspace: workspace}, IdempotencyKey: "e79-idem-1", CorrelationID: "e79-corr-1"}
	now := time.Now().UTC()
	run := multiagent.DurableRun{SchemaVersion: multiagent.DurableRunSchemaVersion, Kind: multiagent.DurableRunKindFreewill, Phase: multiagent.CheckpointFreewillWake,
		State:    multiagent.RunState{RunID: runID, WorkflowID: multiagent.FreewillWorkflowID, WorkflowVersion: 1, RequestIdentity: &multiagent.RequestIdentity{RequesterID: agent, IdempotencyKey: request.IdempotencyKey}, CreatedAt: now, UpdatedAt: now},
		Freewill: &multiagent.FreewillRunState{Lifecycle: freewill.StateProposed, Scope: request.Scope, WakeID: "e79-wake-1", Trigger: "scheduled", DedupeKey: request.IdempotencyKey, CorrelationID: request.CorrelationID, Candidates: []freewill.CandidateIntention{{CandidateID: "e79-candidate-1", Request: request}}, ChoiceID: "e79-candidate-1", ReasonSummary: "Selected the bounded review.", ExecutionDisabled: true}}
	if _, err := open.store.Create(ctx, run, nil); err != nil {
		t.Fatal(err)
	}
	pending, err := open.service.RequestApproval(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := open.service.Decide(ctx, runID, pending.Freewill.ApprovalRef, "grant", owner, "")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Freewill.ApprovalReceipt == nil || approved.Freewill.ApprovalReceipt.Decision != multiagent.FreewillApprovalGranted || approved.Freewill.ExecutionDispatch == nil || approved.Freewill.ExecutionDispatch.Status != "accepted" {
		t.Fatalf("synthetic prior approval was not validly accepted: %#v", approved.Freewill)
	}
	childID := approved.Freewill.ExecutionDispatch.ChildRunID
	pendingChild, err := childApprovals.List(childID)
	if err != nil || len(pendingChild) != 1 || pendingChild[0].Status != approval.StatusPending {
		t.Fatalf("synthetic child did not pause for its separate patch approval: %#v err=%v", pendingChild, err)
	}
	if dispatcher.runtime == nil {
		t.Fatal("synthetic child runtime was not started")
	}
	child, err := dispatcher.runtime.runtime.Inspect(ctx, childID)
	if err != nil || child.State.Status != multiagent.RunStatusPaused {
		t.Fatalf("synthetic child did not pause: status=%s err=%v", child.State.Status, err)
	}
	return open, childApprovals, dispatcher, approved
}

func assertE79StateUnchanged(t *testing.T, ctx context.Context, dataDir string, original multiagent.DurableRun, childApprovals *approval.Store, dispatcher *oneCycleChildDispatcher, workspace string) {
	t.Helper()
	store, err := multiagent.OpenExistingSQLiteDurableRunStoreReadOnly(filepath.Join(dataDir, "freewill.db"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(ctx, original.State.RunID)
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got.Freewill == nil || original.Freewill == nil || got.Freewill.ApprovalReceipt == nil ||
		*got.Freewill.ApprovalReceipt != *original.Freewill.ApprovalReceipt || got.Freewill.ApprovalRef != original.Freewill.ApprovalRef ||
		got.Freewill.ApprovalFingerprint != original.Freewill.ApprovalFingerprint ||
		!reflect.DeepEqual(got.Freewill.ExecutionDispatch, original.Freewill.ExecutionDispatch) ||
		!reflect.DeepEqual(got.Freewill.Candidates, original.Freewill.Candidates) || got.Freewill.Lifecycle != original.Freewill.Lifecycle {
		t.Fatalf("served promise proposal/restart changed prior parent authority or child binding: got=%#v want=%#v", got.Freewill, original.Freewill)
	}
	childID := original.Freewill.ExecutionDispatch.ChildRunID
	pending, err := childApprovals.List(childID)
	if err != nil || len(pending) != 1 || pending[0].Status != approval.StatusPending {
		t.Fatalf("existing separate child patch approval changed: %#v err=%v", pending, err)
	}
	child, err := dispatcher.runtime.runtime.Inspect(ctx, childID)
	if err != nil || child.State.Status != multiagent.RunStatusPaused || dispatcher.starts != 1 {
		t.Fatalf("child resumed or was re-dispatched: status=%s starts=%d err=%v", child.State.Status, dispatcher.starts, err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "base.txt"))
	if err != nil || string(content) != "base\n" {
		t.Fatalf("source fixture changed: content=%q err=%v", content, err)
	}
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "freewill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	for _, eventType := range []string{event.EventFreewillApprovalDecided, event.EventFreewillExecutionDispatching, event.EventFreewillExecutionDispatched, event.EventMultiAgentToolDiagnostic, event.EventInteractionActionExecuted} {
		rows, queryErr := events.Query(ctx, event.EventFilter{Type: eventType, Limit: 100})
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		if len(rows) != 0 {
			t.Fatalf("served promise proposal/restart emitted authority/effect event %s: %d", eventType, len(rows))
		}
	}
}
