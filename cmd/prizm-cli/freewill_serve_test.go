package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/ollama"
	multiagent "github.com/emaharmony/prizm/internal/workflow/multiagent"
	"github.com/nats-io/nats.go"
)

type directFreewillChatStub struct{ chat provider.ChatProvider }

func (*directFreewillChatStub) Generate(context.Context, provider.GenerateRequest) (provider.GenerateResponse, error) {
	return provider.GenerateResponse{}, nil
}

func (s *directFreewillChatStub) ChatGenerate(ctx context.Context, req provider.ChatGenerateRequest) (provider.ChatGenerateResponse, error) {
	if s.chat != nil {
		return s.chat.ChatGenerate(ctx, req)
	}
	return provider.ChatGenerateResponse{}, nil
}

func TestResolveFreewillSelectorProviderRejectsFallbackAndUnsafeCodexConfig(t *testing.T) {
	base := &orchestrator.Config{}
	base.Codex.Executable = os.Args[0]
	base.Codex.Model = "gpt-5.6-luna"
	registry := provider.NewProviderRegistry()

	agent := orchestrator.AgentConfig{Provider: "codex", Model: "gpt-5.6-luna",
		Fallbacks: []orchestrator.ModelFallback{{Provider: "ollama", Model: "other"}}}
	if _, err := resolveFreewillSelectorProvider(base, registry, agent, agent.Model); err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Fatalf("fallback config was not rejected: %v", err)
	}
	agent.Fallbacks = nil
	base.Codex.Profile = "unsafe-profile"
	if _, err := resolveFreewillSelectorProvider(base, registry, agent, agent.Model); err == nil || !strings.Contains(err.Error(), "profiles") {
		t.Fatalf("profile config was not rejected: %v", err)
	}
	base.Codex.Profile = ""
	base.Codex.ExtraArgs = []string{"--sandbox", "workspace-write"}
	if _, err := resolveFreewillSelectorProvider(base, registry, agent, agent.Model); err == nil || !strings.Contains(err.Error(), "extra arguments") {
		t.Fatalf("extra args were not rejected: %v", err)
	}
	base.Codex.ExtraArgs = nil
	base.Codex.Model = "other"
	if _, err := resolveFreewillSelectorProvider(base, registry, agent, agent.Model); err == nil || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("model mismatch was not rejected: %v", err)
	}
}

func TestResolveFreewillSelectorProviderUsesDedicatedCodexWithoutRegistryFallback(t *testing.T) {
	cfg := &orchestrator.Config{}
	cfg.Codex.Executable = os.Args[0]
	cfg.Codex.Model = "gpt-5.6-luna"
	got, err := resolveFreewillSelectorProvider(cfg, provider.NewProviderRegistry(),
		orchestrator.AgentConfig{Provider: "codex", Model: "gpt-5.6-luna"}, "gpt-5.6-luna")
	if err != nil {
		t.Fatalf("resolve dedicated Codex selector: %v", err)
	}
	if got == nil {
		t.Fatal("dedicated Codex selector was nil")
	}
}

func TestResolveFreewillSelectorProviderUsesExactDirectOllamaModel(t *testing.T) {
	const model = "glm-5.3-flash:cloud"
	registry := provider.NewProviderRegistry()
	direct := &directFreewillChatStub{}
	registry.Register(model, direct, provider.ModelInfo{ProviderName: "ollama"})
	got, err := resolveFreewillSelectorProvider(&orchestrator.Config{}, registry,
		orchestrator.AgentConfig{Provider: "ollama", Model: model}, model)
	if err != nil {
		t.Fatalf("resolve direct Ollama selector: %v", err)
	}
	if got != direct {
		t.Fatalf("selector resolved wrapper/fallback instead of exact provider: %T", got)
	}

	mismatch := provider.NewProviderRegistry()
	mismatch.Register(model, direct, provider.ModelInfo{ProviderName: "openai"})
	if _, err := resolveFreewillSelectorProvider(&orchestrator.Config{}, mismatch,
		orchestrator.AgentConfig{Provider: "ollama", Model: model}, model); err == nil || !strings.Contains(err.Error(), "does not match Ollama") {
		t.Fatalf("mismatched registry binding was not rejected: %v", err)
	}
}

func TestServedFreewillP1WakePersistsPolicyOnlyProposalAndHistory(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, "synthetic-project")
	policyDir := filepath.Join(root, "policies")
	dataDir := filepath.Join(root, "data")
	runsDir := filepath.Join(root, "runs")
	for _, dir := range []string{projectPath, policyDir, runsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	selection := `{"candidates":[` +
		`{"title":"A","goal":"Synthetic task A","reason_summary":"Small and safe.","requested_capabilities":["memory.read"],"budget":null,"score":71,"score_factors":["scope_fit"],"score_rationale":"Fits the empty synthetic project."},` +
		`{"title":"B","goal":"Synthetic task B","reason_summary":"Useful bounded review.","requested_capabilities":["memory.read"],"budget":null,"score":91,"score_factors":["impact"],"score_rationale":"Highest impact without mutation."},` +
		`{"title":"C","goal":"Synthetic task C","reason_summary":"Low-risk alternative.","requested_capabilities":["memory.read"],"budget":null,"score":61,"score_factors":["feasibility"],"score_rationale":"Feasible but less useful."}` +
		`],"resume_candidate_id":"","reason_summary":"B is the strongest read-only proposal.","sleep_reason":""}`
	var mu sync.Mutex
	requestCount := 0
	var requestFailure string
	fakeProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requestCount++
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			requestFailure = "unexpected fake provider endpoint"
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			requestFailure = "invalid provider request"
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var wantSchema any
		if err := json.Unmarshal(multiagent.FreewillSelectionOutputSchema(), &wantSchema); err != nil {
			requestFailure = "invalid expected schema"
		}
		options, _ := request["options"].(map[string]any)
		if request["model"] != "synthetic-model" || request["stream"] != false || !reflect.DeepEqual(request["format"], wantSchema) || options["num_predict"] != float64(4096) {
			requestFailure = "model, stream, or native schema mismatch"
		}
		if _, present := request["tools"]; present {
			requestFailure = "tools field was present"
		}
		if !json.Valid([]byte(selection)) {
			requestFailure = "invalid fixed selection fixture"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "synthetic-model", "done": true,
			"message": map[string]any{"role": "assistant", "content": selection}})
	}))
	defer fakeProvider.Close()

	cfg := &orchestrator.Config{}
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir = dataDir, runsDir
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: "synthetic-agent", ProjectID: "synthetic-project", Model: "synthetic-model",
		PolicyDir: policyDir, Scheduling: orchestrator.FreewillSchedulingConfig{Enabled: true, Schedule: "0 0 1 1 *"}}
	cfg.Agents = []orchestrator.AgentConfig{{ID: "synthetic-agent", Provider: "ollama", Model: "synthetic-model"}}
	cfg.Projects = []orchestrator.ProjectConfig{{ID: "synthetic-project", RepoPath: projectPath}}
	providers := provider.NewProviderRegistry()
	providers.Register("synthetic-model", &directFreewillChatStub{chat: ollama.NewChatProvider(fakeProvider.URL)}, provider.ModelInfo{ProviderName: "ollama"})
	runtime, err := openFreewillServeRuntime(cfg, providers, "", filepath.Join(root, "prizm.yaml"))
	if err != nil {
		t.Fatalf("compose Freewill runtime: %v", err)
	}
	runtimeClosed := false
	defer func() {
		if !runtimeClosed {
			runtime.Close()
		}
	}()

	busURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		runtime.Close()
		t.Fatalf("start loopback bus: %v", err)
	}
	defer stopBus()
	nc, err := nats.Connect(busURL)
	if err != nil {
		runtime.Close()
		t.Fatalf("connect loopback bus: %v", err)
	}
	defer nc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Start(ctx, nc, true); err != nil {
		runtime.Close()
		t.Fatalf("start Freewill wake service: %v", err)
	}
	if err := runtime.Start(ctx, nc, true); err != nil {
		runtime.Close()
		t.Fatalf("duplicate Freewill start should be idempotent: %v", err)
	}
	if err := nc.Publish(freewillScheduleSubject, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		t.Fatalf("flush single synthetic wake: %v", err)
	}

	var run multiagent.DurableRun
	observedRunCount := 0
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		runs, listErr := runtime.store.ListFreewillRuns(ctx, "", 10)
		if listErr != nil {
			t.Fatal(listErr)
		}
		observedRunCount = len(runs)
		if len(runs) > 0 && runs[0].Freewill != nil && runs[0].Freewill.Lifecycle == freewill.StateDenied {
			run = runs[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.Freewill == nil {
		t.Fatal("single scheduled wake did not reach policy evaluation")
	}
	if observedRunCount != 1 {
		t.Fatalf("one manual wake created %d durable Freewill runs", observedRunCount)
	}
	if len(run.Freewill.Candidates) != 3 || run.Freewill.ChoiceID == "" || run.Freewill.ReasonSummary == "" {
		t.Fatalf("selection was not durably reviewable: %#v", run.Freewill)
	}
	selected := -1
	for i, candidate := range run.Freewill.Candidates {
		if candidate.CandidateID == run.Freewill.ChoiceID {
			selected = i
		}
	}
	if selected != 1 || run.Freewill.Candidates[selected].Score != 91 {
		t.Fatalf("explainable ranking did not choose highest-scored synthetic candidate: choice=%q candidates=%#v", run.Freewill.ChoiceID, run.Freewill.Candidates)
	}
	if run.Freewill.PolicyDecision != "denied" || run.Freewill.PauseReason != "policy_denied" || !run.Freewill.ExecutionDisabled || run.Freewill.ApprovalRef != "" {
		t.Fatalf("default-deny proposal crossed policy-only boundary: %#v", run.Freewill)
	}
	mu.Lock()
	if requestCount != 1 || requestFailure != "" {
		t.Fatalf("fake model request count=%d failure=%q", requestCount, requestFailure)
	}
	mu.Unlock()

	storedEvents, err := runtime.events.Query(ctx, event.EventFilter{RunID: run.State.RunID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seenSelection, seenDenied := false, false
	for _, e := range storedEvents {
		if e.Type == event.EventFreewillSelectionCommitted {
			seenSelection = true
		}
		if e.Type == event.EventFreewillPolicyDenied {
			seenDenied = true
		}
		if e.Type == event.EventFreewillApprovalProposed || e.Type == event.EventFreewillApprovalDecided {
			t.Fatalf("default-deny proposal emitted approval event %q", e.Type)
		}
		if strings.HasPrefix(e.Type, "prism.workflow.multi_agent.role.") || strings.HasPrefix(e.Type, "prizm.workflow.proposal.apply_") ||
			e.Type == event.EventMultiAgentFanoutChildDispatched || e.Type == event.EventMultiAgentToolDiagnostic || strings.Contains(e.Type, ".tool.") {
			t.Fatalf("policy-only wake emitted execution/dispatch event %q", e.Type)
		}
	}
	if !seenSelection || !seenDenied {
		t.Fatalf("durable trace missing selection or policy denial: %#v", storedEvents)
	}
	if entries, err := os.ReadDir(projectPath); err != nil || len(entries) != 0 {
		t.Fatalf("synthetic project changed: entries=%v err=%v", entries, err)
	}

	activeAPI := api.NewServer(api.Config{AuthToken: "synthetic-token", FreewillApprovals: runtime.approvals})
	activeHTTP := httptest.NewServer(activeAPI.Handler())
	defer activeHTTP.Close()
	unauthorized, err := http.Get(activeHTTP.URL + "/api/v1/freewill/runs/" + run.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("served status did not enforce auth: %d", unauthorized.StatusCode)
	}
	getStatus := func(base string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/freewill/runs/"+run.State.RunID, nil)
		req.Header.Set("Authorization", "Bearer synthetic-token")
		resp, reqErr := http.DefaultClient.Do(req)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		return resp, string(body)
	}
	activeResponse, activeBody := getStatus(activeHTTP.URL)
	if activeResponse.StatusCode != http.StatusOK || !strings.Contains(activeBody, `"choice_id":"`+run.Freewill.ChoiceID+`"`) || !strings.Contains(activeBody, `"policy_decision":"denied"`) {
		t.Fatalf("authenticated served status missing persisted decision: status=%d body=%s", activeResponse.StatusCode, activeBody)
	}
	activeGrant, _ := http.NewRequest(http.MethodPost, activeHTTP.URL+"/api/v1/freewill/runs/"+run.State.RunID+"/approvals/forged/decision", strings.NewReader(`{"decision":"grant"}`))
	activeGrant.Header.Set("Authorization", "Bearer synthetic-token")
	activeGrantResponse, err := http.DefaultClient.Do(activeGrant)
	if err != nil {
		t.Fatal(err)
	}
	activeGrantResponse.Body.Close()
	if activeGrantResponse.StatusCode != http.StatusConflict {
		t.Fatalf("policy-denied choice accepted a grant: %d", activeGrantResponse.StatusCode)
	}
	activeAfterGrant, activeAfterBody := getStatus(activeHTTP.URL)
	if activeAfterGrant.StatusCode != http.StatusOK || !strings.Contains(activeAfterBody, `"policy_decision":"denied"`) || strings.Contains(activeAfterBody, `"approval_id":`) {
		t.Fatalf("forged grant changed denied choice: status=%d body=%s", activeAfterGrant.StatusCode, activeAfterBody)
	}
	activeHTTP.Close()

	runtime.Close()
	runtimeClosed = true
	if err := runtime.Start(ctx, nc, true); err == nil {
		t.Fatal("starting a closed Freewill runtime unexpectedly succeeded")
	}
	cfg.Freewill.Enabled = false
	statusRuntime, err := openFreewillStatusRuntime(cfg, filepath.Join(root, "prizm.yaml"))
	if err != nil || statusRuntime == nil || !statusRuntime.readOnly {
		t.Fatalf("open historical read-only status: runtime=%#v err=%v", statusRuntime, err)
	}
	defer statusRuntime.Close()
	historicalAPI := api.NewServer(api.Config{AuthToken: "synthetic-token", FreewillApprovals: statusRuntime.approvals})
	historicalHTTP := httptest.NewServer(historicalAPI.Handler())
	defer historicalHTTP.Close()
	historicalResponse, historicalBody := getStatus(historicalHTTP.URL)
	if historicalResponse.StatusCode != http.StatusOK || !strings.Contains(historicalBody, `"choice_id":"`+run.Freewill.ChoiceID+`"`) || !strings.Contains(historicalBody, `"execution_disabled":true`) {
		t.Fatalf("historical disabled status lost the decision trace: status=%d body=%s", historicalResponse.StatusCode, historicalBody)
	}
	post, _ := http.NewRequest(http.MethodPost, historicalHTTP.URL+"/api/v1/freewill/runs/"+run.State.RunID+"/approvals/forged/decision", strings.NewReader(`{"decision":"grant"}`))
	post.Header.Set("Authorization", "Bearer synthetic-token")
	postResponse, err := http.DefaultClient.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	postResponse.Body.Close()
	if postResponse.StatusCode != http.StatusConflict {
		t.Fatalf("read-only history accepted approval decision: %d", postResponse.StatusCode)
	}
	statusAfter, err := statusRuntime.store.Load(ctx, run.State.RunID)
	if err != nil || statusAfter.Freewill.PolicyDecision != "denied" || statusAfter.Freewill.ApprovalRef != "" {
		t.Fatalf("historical decision POST changed durable record: state=%#v err=%v", statusAfter.Freewill, err)
	}
	if entries, err := os.ReadDir(projectPath); err != nil || len(entries) != 0 {
		t.Fatalf("historical status changed project: entries=%v err=%v", entries, err)
	}
}

func TestDisabledFreewillStatusRuntimeDoesNotCreateOrWake(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	configPath := filepath.Join(root, "prizm.yaml")
	cfg := &orchestrator.Config{}
	cfg.Prizm.DataDir = dataDir
	cfg.Prizm.RunsDir = filepath.Join(root, "runs")

	runtime, err := openFreewillStatusRuntime(cfg, configPath)
	if err != nil || runtime != nil {
		t.Fatalf("missing history runtime=%#v err=%v", runtime, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "freewill.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled startup created database: %v", err)
	}
	readOnly := &freewillServeRuntime{readOnly: true}
	if err := readOnly.Start(context.Background(), nil, true); err != nil {
		t.Fatalf("read-only status runtime subscribed or started wake: %v", err)
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := multiagent.NewSQLiteDurableRunStore(filepath.Join(dataDir, "freewill.db"))
	if err != nil {
		t.Fatal(err)
	}
	history := multiagent.DurableRun{SchemaVersion: multiagent.DurableRunSchemaVersion, Kind: multiagent.DurableRunKindFreewill,
		Revision: 1, Phase: multiagent.CheckpointFreewillWake,
		State: multiagent.RunState{SchemaVersion: multiagent.RunStateSchemaVersion, RunID: "run-history", WorkflowID: multiagent.FreewillWorkflowID,
			WorkflowVersion: 1, CurrentTask: multiagent.TaskReference{ID: "freewill-wake"}, Status: multiagent.RunStatusCreated,
			RequestIdentity: &multiagent.RequestIdentity{RequesterID: "agent-history", SessionID: "corr-history", Source: "freewill_scheduler", IdempotencyKey: "history"}},
		Freewill: &multiagent.FreewillRunState{Lifecycle: freewill.StateSleeping, Scope: freewill.Scope{ProjectID: "project-history", Workspace: "workspace-history"}, WakeID: "run-history", Trigger: "scheduled_tick", DedupeKey: "history", CorrelationID: "corr-history"}}
	if _, created, err := store.AcceptRun(context.Background(), history, nil); err != nil || !created {
		t.Fatalf("seed history created=%t err=%v", created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Freewill.Scheduling.Enabled = true
	statusRuntime, err := openFreewillStatusRuntime(cfg, configPath)
	if err != nil || statusRuntime == nil || !statusRuntime.readOnly || statusRuntime.wake != nil || statusRuntime.events != nil {
		t.Fatalf("existing-history runtime=%#v err=%v", statusRuntime, err)
	}
	if err := statusRuntime.Start(context.Background(), nil, true); err != nil {
		t.Fatalf("disabled history runtime started wake: %v", err)
	}
	if _, _, err := statusRuntime.store.AcceptRun(context.Background(), multiagent.DurableRun{}, nil); err == nil {
		t.Fatal("disabled status store permits writes")
	}
	server := api.NewServer(api.Config{AuthToken: "history-token", FreewillApprovals: statusRuntime.approvals})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/freewill/runs/run-history", nil)
	request.Header.Set("Authorization", "Bearer history-token")
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"run_id":"run-history"`) {
		t.Fatalf("disabled-composed historical status=%d body=%s", response.Code, response.Body.String())
	}
	statusRuntime.Close()
}
