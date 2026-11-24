package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/ollama"
	multiagent "github.com/emaharmony/prizm/internal/workflow/multiagent"
	"github.com/nats-io/nats.go"
)

func TestFreewillEventWakeNATSServeAllowlistAndDedupeAfterRuntimeReopen(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, "project")
	policyDir := filepath.Join(root, "policies")
	dataDir, runsDir := filepath.Join(root, "data"), filepath.Join(root, "runs")
	for _, dir := range []string{projectPath, policyDir, runsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(projectPath, "README.md"), []byte("Synthetic local event wake fixture."), 0o600); err != nil {
		t.Fatal(err)
	}
	selection := `{"candidates":[` +
		`{"title":"A","goal":"Bounded read-only follow-up A","reason_summary":"Small and safe.","requested_capabilities":["decision_only"],"budget":null,"score":71,"score_factors":["scope_fit"],"score_rationale":"Fits the synthetic project.","basis_refs":["project:readme"],"uncertainty":"The README may be stale."},` +
		`{"title":"B","goal":"Bounded read-only follow-up B","reason_summary":"Useful alternative.","requested_capabilities":["decision_only"],"budget":null,"score":61,"score_factors":["feasibility"],"score_rationale":"Fits the synthetic project.","basis_refs":["project:readme"],"uncertainty":"The README may be stale."},` +
		`{"title":"C","goal":"Bounded read-only follow-up C","reason_summary":"Another alternative.","requested_capabilities":["decision_only"],"budget":null,"score":51,"score_factors":["readiness"],"score_rationale":"Fits the synthetic project.","basis_refs":["project:readme"],"uncertainty":"The README may be stale."}` +
		`],"resume_candidate_id":"","reason_summary":"","sleep_reason":"No bounded follow-up is needed for this synthetic event."}`
	var providerCalls atomic.Int32
	var providerFailure atomic.Value
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			providerFailure.Store("unexpected fake provider endpoint")
			http.Error(w, "unexpected endpoint", http.StatusBadRequest)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			providerFailure.Store("invalid request")
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var wantSchema any
		if err := json.Unmarshal(multiagent.FreewillSelectionOutputSchema(), &wantSchema); err != nil {
			providerFailure.Store("invalid expected schema")
		}
		if request["model"] != "synthetic-model" || request["stream"] != false {
			providerFailure.Store("model or stream mismatch")
		}
		if _, present := request["tools"]; present {
			providerFailure.Store("tools field was present")
		}
		if format, ok := request["format"]; !ok || !reflect.DeepEqual(format, wantSchema) {
			providerFailure.Store("native selector schema mismatch")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "synthetic-model", "done": true, "message": map[string]any{"role": "assistant", "content": selection}})
	}))
	defer providerServer.Close()

	cfg := &orchestrator.Config{}
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir, cfg.Prizm.Workspace = dataDir, runsDir, projectPath
	cfg.Prizm.ReadRoots = []string{projectPath}
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: "synthetic-agent", ProjectID: "synthetic-project", Model: "synthetic-model", PolicyDir: policyDir,
		Scheduling: orchestrator.FreewillSchedulingConfig{Enabled: true, Schedule: "* * * * *", EventKinds: []string{"prizm.task.completed"}}}
	cfg.Agents = []orchestrator.AgentConfig{{ID: "synthetic-agent", Provider: "ollama", Model: "synthetic-model"}}
	cfg.Projects = []orchestrator.ProjectConfig{{ID: "synthetic-project", RepoPath: projectPath}}
	providers := provider.NewProviderRegistry()
	providers.Register("synthetic-model", &directFreewillChatStub{chat: ollama.NewChatProvider(providerServer.URL)}, provider.ModelInfo{ProviderName: "ollama"})

	busURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer stopBus()
	nc, err := nats.Connect(busURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	firstRuntime, err := openFreewillServeRuntime(cfg, providers, "", filepath.Join(root, "prizm.yaml"), "")
	if err != nil {
		t.Fatalf("compose first Freewill runtime: %v", err)
	}
	t.Cleanup(firstRuntime.Close)
	if err := firstRuntime.Start(ctx, nc, true); err != nil {
		firstRuntime.Close()
		t.Fatalf("start first event subscription: %v", err)
	}
	allowed := event.NewEvent("prizm.task.completed", "synthetic-test", map[string]any{"task": "safe synthetic context"})
	if err := nc.Publish("prizm.agent.completed", mustMarshalEventForTest(t, event.NewEvent("prizm.agent.completed", "synthetic-test", nil))); err != nil {
		t.Fatal(err)
	}
	mismatched := event.NewEvent("prizm.agent.completed", "synthetic-test", nil)
	if err := nc.Publish("prizm.task.completed", mustMarshalEventForTest(t, mismatched)); err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if providerCalls.Load() != 0 {
		t.Fatalf("unlisted or mismatched event woke selector: calls=%d", providerCalls.Load())
	}
	if err := nc.Publish("prizm.task.completed", mustMarshalEventForTest(t, allowed)); err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	first := waitForSingleFreewillRun(t, ctx, firstRuntime)
	if first.Freewill == nil || first.Freewill.Lifecycle != freewill.StateSleeping || first.Freewill.ChoiceID != "" || first.Freewill.ApprovalRef != "" || !first.Freewill.ExecutionDisabled {
		t.Fatalf("allowed event did not produce a decision-only sleep: %#v", first.Freewill)
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("allowed event selector calls=%d, want one", providerCalls.Load())
	}
	if failure, _ := providerFailure.Load().(string); failure != "" {
		t.Fatalf("fake provider rejected request: %s", failure)
	}
	assertFreewillRunHasNoAuthorityEvents(t, ctx, firstRuntime, first.State.RunID)

	// The same serve runtime must accept both configured wake sources. Process
	// the timer tick only after the event wake reaches its terminal sleep state:
	// active actor admissions are fenced, and concurrent wake events are not
	// buffered by this POC.
	if err := nc.Publish(freewillScheduleSubject, []byte(`{"fixture":"synthetic-timer"}`)); err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	runsBeforeRestart := waitForFreewillRunCount(t, ctx, firstRuntime, 2)
	if providerCalls.Load() != 2 {
		t.Fatalf("event and timer wakes invoked selector %d times, want once each", providerCalls.Load())
	}
	var timerRun *multiagent.DurableRun
	for i := range runsBeforeRestart {
		run := &runsBeforeRestart[i]
		if run.Freewill == nil || run.Freewill.Lifecycle != freewill.StateSleeping || run.Freewill.ChoiceID != "" ||
			run.Freewill.ApprovalRef != "" || !run.Freewill.ExecutionDisabled {
			t.Fatalf("event/timer wake did not remain decision-only: %#v", run.Freewill)
		}
		if run.Freewill.Trigger == "scheduled_tick" {
			timerRun = run
		}
		assertFreewillRunHasNoAuthorityEvents(t, ctx, firstRuntime, run.State.RunID)
	}
	if timerRun == nil || timerRun.State.RunID == first.State.RunID || first.Freewill.Trigger != "event:prizm.task.completed" {
		t.Fatalf("configured timer and event sources did not produce distinct persisted wakes: event=%#v timer=%#v", first.Freewill, timerRun)
	}
	firstRuntime.Close()

	secondRuntime, err := openFreewillServeRuntime(cfg, providers, "", filepath.Join(root, "prizm.yaml"), "")
	if err != nil {
		t.Fatalf("reopen Freewill runtime: %v", err)
	}
	closeDone := make(chan struct{})
	var closeStarted atomic.Bool
	closeRuntime := func() {
		if closeStarted.CompareAndSwap(false, true) {
			go func() {
				secondRuntime.Close()
				close(closeDone)
			}()
		}
	}
	var releaseHandler func()
	t.Cleanup(func() {
		if releaseHandler != nil {
			releaseHandler()
		}
		closeRuntime()
		select {
		case <-closeDone:
		case <-time.After(5 * time.Second):
			t.Error("runtime cleanup did not finish")
		}
	})
	if err := secondRuntime.Start(ctx, nc, true); err != nil {
		t.Fatalf("start reopened event subscription: %v", err)
	}
	if err := nc.Publish("prizm.task.completed", mustMarshalEventForTest(t, allowed)); err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	runs, err := secondRuntime.store.ListFreewillRuns(ctx, "", 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("duplicate event after runtime/store reopen changed durable run set: runs=%d err=%v", len(runs), err)
	}
	if providerCalls.Load() != 2 {
		t.Fatalf("duplicate event after reopen invoked selector again: calls=%d", providerCalls.Load())
	}

	handlerEntered := make(chan struct{}, 1)
	releaseHandlerCh := make(chan struct{})
	releasedHandler := false
	releaseHandler = func() {
		if !releasedHandler {
			close(releaseHandlerCh)
			releasedHandler = true
		}
	}
	secondRuntime.beforeWakeHandler = func() {
		handlerEntered <- struct{}{}
		<-releaseHandlerCh
	}
	secondEvent := event.NewEvent("prizm.task.completed", "synthetic-test", map[string]any{"task": "close-wait fixture"})
	if err := nc.Publish("prizm.task.completed", mustMarshalEventForTest(t, secondEvent)); err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handlerEntered:
	case <-ctx.Done():
		t.Fatal("second NATS callback did not reach the blocking handler barrier")
	}
	closeRuntime()
	select {
	case <-closeDone:
		t.Fatal("runtime Close returned while an event wake handler was still in flight")
	case <-time.After(100 * time.Millisecond):
	}
	releaseHandler()
	select {
	case <-closeDone:
	case <-ctx.Done():
		t.Fatal("runtime Close did not finish after the in-flight event handler exited")
	}
}

func mustMarshalEventForTest(t *testing.T, evt event.Event) []byte {
	t.Helper()
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func waitForSingleFreewillRun(t *testing.T, ctx context.Context, runtime *freewillServeRuntime) multiagent.DurableRun {
	t.Helper()
	for {
		runs, err := runtime.store.ListFreewillRuns(ctx, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 1 && runs[0].Freewill != nil && runs[0].Freewill.Lifecycle == freewill.StateSleeping {
			return runs[0]
		}
		select {
		case <-ctx.Done():
			t.Fatalf("event wake did not finish as a sleeping run: runs=%d err=%v", len(runs), ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitForFreewillRunCount(t *testing.T, ctx context.Context, runtime *freewillServeRuntime, want int) []multiagent.DurableRun {
	t.Helper()
	for {
		runs, err := runtime.store.ListFreewillRuns(ctx, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == want {
			allSleeping := true
			for _, run := range runs {
				if run.Freewill == nil || run.Freewill.Lifecycle != freewill.StateSleeping {
					allSleeping = false
					break
				}
			}
			if allSleeping {
				return runs
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Freewill runs did not reach sleeping state: want=%d got=%d err=%v", want, len(runs), ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func assertFreewillRunHasNoAuthorityEvents(t *testing.T, ctx context.Context, runtime *freewillServeRuntime, runID string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		stored, err := runtime.events.Query(ctx, event.EventFilter{RunID: runID, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		wakeClaims, selectionStarts, selectionCommits := 0, 0, 0
		for _, evt := range stored {
			lower := strings.ToLower(evt.Type)
			if strings.Contains(lower, "approval") || strings.Contains(lower, "dispatch") || strings.Contains(lower, "tool.") ||
				strings.Contains(lower, "execution") || strings.Contains(lower, "action") || strings.Contains(lower, "apply") {
				t.Fatalf("decision-only wake emitted forbidden authority/tool event %q", evt.Type)
			}
			switch evt.Type {
			case event.EventFreewillWakeClaimed:
				wakeClaims++
			case event.EventFreewillSelectionStarted:
				selectionStarts++
			case event.EventFreewillSelectionCommitted:
				selectionCommits++
			}
		}
		if wakeClaims > 1 || selectionStarts > 1 || selectionCommits > 1 {
			t.Fatalf("decision-only wake has duplicate persisted trace: wake_claimed=%d selection_started=%d selection_committed=%d", wakeClaims, selectionStarts, selectionCommits)
		}
		if selectionCommits == 1 {
			if wakeClaims != 1 || selectionStarts != 1 {
				t.Fatalf("decision-only wake has incomplete persisted trace: wake_claimed=%d selection_started=%d selection_committed=%d", wakeClaims, selectionStarts, selectionCommits)
			}
			pending, err := runtime.store.PendingEvents(ctx, runID, 100)
			if err != nil {
				t.Fatalf("query durable wake outbox: %v", err)
			}
			if len(pending) == 0 {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("decision-only wake trace/outbox wait canceled before settling: %v", ctx.Err())
		case <-deadline.C:
			t.Fatalf("decision-only wake trace/outbox did not settle within 5s: wake_claimed=%d selection_started=%d selection_committed=%d", wakeClaims, selectionStarts, selectionCommits)
		case <-ticker.C:
		}
	}
}
