package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestE31ReflectionInformsThreeCandidateWakesAcrossServeReopen(t *testing.T) {
	root := t.TempDir()
	projectPath, policyDir := filepath.Join(root, "project"), filepath.Join(root, "policies")
	dataDir, runsDir := filepath.Join(root, "data"), filepath.Join(root, "runs")
	for _, dir := range []string{projectPath, policyDir, runsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(projectPath, "README.md"), []byte("Synthetic local reflection wake fixture."), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := "policies:\n  - id: require_approval_for_freewill_candidate\n    description: E31 synthetic decision-only proposal gate.\n    match:\n      action: intention.propose\n      resource.type: intention\n      resource.name: freewill_candidate\n    decision: requires_approval\n    reason: Owner review is required for every proposal.\n"
	if err := os.WriteFile(filepath.Join(policyDir, "e31.yaml"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}

	// First synthetic task: persist the host outcome and linked self-report,
	// then produce the E17 hypothesis through the bounded local fake sink.
	priorEvents, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "freewill.db"))
	if err != nil {
		t.Fatal(err)
	}
	fact := multiagent.FreewillTerminalFact{ProjectID: "synthetic-project", Workspace: projectPath, AgentID: "synthetic-agent", IntentionID: "prior-intention", ParentRunID: "prior-parent", ChildRunID: "prior-child", Status: "execution_failed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64), ValidationSummary: "failed", FailureCode: "task_failed", RecordedAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	receipt := event.NewEvent(event.EventFreewillExecutionFailed, "synthetic-host", map[string]any{"child_run_id": fact.ChildRunID, "report_ref": fact.ReportRef, "report_sha256": fact.ReportSHA256, "validation_summary": fact.ValidationSummary})
	receipt.Metadata.RunID = fact.ParentRunID
	if err := priorEvents.Store(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	facts, err := newFreewillTerminalFactMemorySink(dataDir, fact.ProjectID, projectPath, priorEvents, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := facts.CaptureFreewillTerminalFact(context.Background(), fact); err != nil {
		t.Fatal(err)
	}
	appraisalJSON, _ := json.Marshal(freewillTaskAppraisalResponse{AppraisalKind: "satisfaction", PerceivedOutcome: "completed", Why: "A synthetic claim for the bounded local fixture.", Context: "The host receipt remains authoritative.", Narrative: "Synthetic task self-report only.", Uncertainty: "This claim is not independently verified.", Scores: freewillSyntheticTaskScores()})
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{narrative: string(appraisalJSON), provider: "local-fake", model: "synthetic-appraisal", version: "fixture-v1"}, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := appraisals.Capture(context.Background(), fact); err != nil {
		t.Fatal(err)
	}
	adaptiveReflectionFake := &freewillAdaptiveTaskReflectionFake{}
	reflection, err := newFreewillTaskReflectionSink(appraisals, adaptiveReflectionFake, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := reflection.ReflectTask(context.Background(), fact); err != nil {
		t.Fatal(err)
	}
	if adaptiveReflectionFake.base.calls != 1 || adaptiveReflectionFake.followUpCalls != 1 {
		t.Fatalf("host-verified contradiction did not consume exactly one bounded follow-up: base=%d follow-up=%d", adaptiveReflectionFake.base.calls, adaptiveReflectionFake.followUpCalls)
	}
	prior, err := reflection.ReadLatestOwnerProjectReflection(context.Background(), "owner-one", fact.ProjectID, projectPath, fact.AgentID)
	if err != nil || prior == nil {
		t.Fatalf("first task did not produce a readable reflection: %#v err=%v", prior, err)
	}
	if prior.Hypothesis != "The initial completion impression may have preceded the failed validation." || prior.Alignment != "disagreement" {
		t.Fatalf("latest exact-scope reflection did not return the validated follow-up revision: %#v", prior)
	}
	if err := priorEvents.Close(); err != nil {
		t.Fatal(err)
	}

	selection := func(reflectionID string) string {
		return `{"candidates":[` +
			`{"title":"Review the prior hypothesis","goal":"Compare the uncertain reflection hypothesis with a bounded read-only host evidence review.","reason_summary":"One task is not enough to infer a stable preference.","requested_capabilities":["decision_only"],"budget":null,"score":74,"score_factors":["feasibility"],"score_rationale":"A narrow review could examine the hypothesis without acting on it.","basis_refs":["project:readme"],"uncertainty":"The reflection is only a model hypothesis.","reflection_id":"` + reflectionID + `"},` +
			`{"title":"Review local test coverage","goal":"Summarize the local test coverage at a high level.","reason_summary":"This is bounded and read-only.","requested_capabilities":["decision_only"],"budget":null,"score":63,"score_factors":["scope_fit"],"score_rationale":"A small read-only summary is feasible.","basis_refs":["project:readme"],"uncertainty":"The README may be incomplete."},` +
			`{"title":"Map project modules","goal":"List the top-level project modules for later review.","reason_summary":"A bounded inventory may clarify project structure.","requested_capabilities":["decision_only"],"budget":null,"score":54,"score_factors":["readiness"],"score_rationale":"A module inventory is a small decision-only proposal.","basis_refs":["project:readme"],"uncertainty":"The README may be stale."}` +
			`],"resume_candidate_id":"","reason_summary":"The cited reflection hypothesis is advisory and this proposal requires normal review.","sleep_reason":""}`
	}
	var providerCalls atomic.Int32
	var providerFailure atomic.Value
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 {
			providerFailure.Store("invalid selector request")
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		var input struct {
			PriorReflection *multiagent.FreewillReflectionEvidence `json:"prior_reflection"`
		}
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil || input.PriorReflection == nil || input.PriorReflection.Binding.ID != prior.Binding.ID || input.PriorReflection.Binding.Revision != prior.Binding.Revision || input.PriorReflection.Binding.Source != prior.Binding.Source || input.PriorReflection.OwnerID != "owner-one" || input.PriorReflection.ProjectID != fact.ProjectID || input.PriorReflection.Workspace != projectPath || input.PriorReflection.AgentID != fact.AgentID || input.PriorReflection.TaskID != prior.TaskID || input.PriorReflection.Alignment != "disagreement" || input.PriorReflection.Hypothesis != prior.Hypothesis || input.PriorReflection.Uncertainty == "" {
			providerFailure.Store("persisted prior reflection was not supplied to later selector")
			http.Error(w, "missing reflection", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "synthetic-model", "done": true, "message": map[string]any{"role": "assistant", "content": selection(prior.Binding.ID)}})
	}))
	defer providerServer.Close()

	cfg := &orchestrator.Config{}
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir, cfg.Prizm.Workspace = dataDir, runsDir, projectPath
	cfg.Prizm.ReadRoots = []string{projectPath}
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: fact.AgentID, ProjectID: fact.ProjectID, Model: "synthetic-model", PolicyDir: policyDir,
		TaskAppraisalEnabled: true, TaskAppraisalOwnerID: "owner-one", TaskReflectionEnabled: true, TaskReflectionOwnerID: "owner-one",
		MemoryContext: orchestrator.FreewillMemoryContextConfig{OwnerID: "owner-one"},
		Scheduling:    orchestrator.FreewillSchedulingConfig{EventKinds: []string{"prizm.task.completed"}}}
	cfg.Agents = []orchestrator.AgentConfig{{ID: fact.AgentID, Provider: "ollama", Model: "synthetic-model"}}
	cfg.Projects = []orchestrator.ProjectConfig{{ID: fact.ProjectID, RepoPath: projectPath, FreewillTerminalFactsProjectID: fact.ProjectID, FreewillTerminalFactsRepoPath: projectPath}}
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
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	openAndStart := func() *freewillServeRuntime {
		runtime, err := openFreewillServeRuntime(cfg, providers, "", filepath.Join(root, "prizm.yaml"), "")
		if err != nil {
			t.Fatalf("compose production Freewill serve runtime: %v", err)
		}
		if err := runtime.Start(ctx, nc, true); err != nil {
			runtime.Close()
			t.Fatalf("start production event wake: %v", err)
		}
		if err := nc.FlushTimeout(2 * time.Second); err != nil {
			t.Fatalf("flush production wake subscription: %v", err)
		}
		return runtime
	}
	firstRuntime := openAndStart()
	t.Cleanup(func() { firstRuntime.Close() })
	firstEvent := event.NewEvent("prizm.task.completed", "synthetic-test", map[string]any{"ignored": "event data is not selector input"})
	if err := nc.Publish("prizm.task.completed", mustMarshalEventForTest(t, firstEvent)); err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	firstRuns := waitForProposedReflectionRun(t, ctx, firstRuntime, 1)
	first := firstRuns[0]
	if first.Freewill == nil || first.Freewill.Lifecycle != freewill.StateApprovalPending {
		failure, _ := providerFailure.Load().(string)
		t.Fatalf("first wake did not reach proposal: selector_calls=%d provider_failure=%q run=%#v", providerCalls.Load(), failure, first.Freewill)
	}
	assertE31ReflectionProposal(t, first, prior)
	assertE31NoUnapprovedEffects(t, ctx, firstRuntime, first)
	denied, err := firstRuntime.approvals.Decide(ctx, first.State.RunID, first.Freewill.ApprovalRef, "deny", "synthetic-owner", "Synthetic review declines further work on this hypothesis.")
	if err != nil || denied.Freewill == nil || denied.Freewill.Lifecycle != freewill.StateDenied || denied.Freewill.ApprovalReceipt == nil || denied.Freewill.ApprovalReceipt.Decision != multiagent.FreewillApprovalDenied {
		t.Fatalf("synthetic owner denial did not close the proposal cleanly: %#v err=%v", denied.Freewill, err)
	}
	firstRuntime.Close()

	secondRuntime := openAndStart()
	t.Cleanup(func() { secondRuntime.Close() })
	secondEvent := event.NewEvent("prizm.task.completed", "synthetic-test", map[string]any{"task": "distinct later wake"})
	if err := nc.Publish("prizm.task.completed", mustMarshalEventForTest(t, secondEvent)); err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	runs := waitForProposedReflectionRun(t, ctx, secondRuntime, 1)
	var later multiagent.DurableRun
	for _, run := range runs {
		if run.State.RunID != first.State.RunID {
			later = run
		}
	}
	assertE31ReflectionProposal(t, later, prior)
	assertE31NoUnapprovedEffects(t, ctx, secondRuntime, later)
	if calls := providerCalls.Load(); calls != 2 {
		t.Fatalf("distinct wakes selector calls=%d want=2", calls)
	}
	if failure, _ := providerFailure.Load().(string); failure != "" {
		t.Fatal(failure)
	}
}

func waitForProposedReflectionRun(t *testing.T, ctx context.Context, runtime *freewillServeRuntime, count int) []multiagent.DurableRun {
	t.Helper()
	for {
		runs, err := runtime.store.ListFreewillRuns(ctx, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		proposed := 0
		for _, run := range runs {
			if run.Freewill != nil && run.Freewill.Lifecycle == freewill.StateApprovalPending {
				proposed++
			}
		}
		if proposed >= count {
			return runs
		}
		select {
		case <-ctx.Done():
			for _, run := range runs {
				if run.Freewill != nil {
					t.Logf("wake state: lifecycle=%s revision=%d status=%s failure=%s/%s candidates=%d", run.Freewill.Lifecycle, run.Revision, run.State.Status, run.Freewill.SelectionFailureClass, run.Freewill.SelectionFailureCode, len(run.Freewill.Candidates))
				}
			}
			return runs
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func assertE31ReflectionProposal(t *testing.T, run multiagent.DurableRun, prior *multiagent.FreewillReflectionEvidence) {
	t.Helper()
	if run.Freewill == nil || run.Freewill.Lifecycle != freewill.StateApprovalPending || run.Freewill.ChoiceID == "" || run.Freewill.ApprovalRef == "" || run.Freewill.ApprovalReceipt == nil || run.Freewill.ApprovalReceipt.Decision != multiagent.FreewillApprovalPending || run.Freewill.ExecutionDispatch != nil || !run.Freewill.ExecutionDisabled {
		t.Fatalf("reflection wake crossed the proposal-only boundary: %#v", run.Freewill)
	}
	if len(run.Freewill.Candidates) != 3 {
		t.Fatalf("reflection changed the frozen three-candidate inventory: %d", len(run.Freewill.Candidates))
	}
	var cited int
	for _, candidate := range run.Freewill.Candidates {
		if candidate.Request.CandidateSource == freewill.SourceReflection {
			cited++
			if candidate.Request.ReflectionID != prior.Binding.ID || candidate.Request.ReflectionRevision != prior.Binding.Revision || candidate.Request.ReflectionSource != prior.Binding.Source {
				t.Fatalf("reflection candidate was not bound to exact host source: %#v", candidate.Request)
			}
		}
	}
	if cited != 1 {
		t.Fatalf("reflection candidate count=%d want=1", cited)
	}
}

func assertE31NoUnapprovedEffects(t *testing.T, ctx context.Context, runtime *freewillServeRuntime, run multiagent.DurableRun) {
	t.Helper()
	stored, err := runtime.events.Query(ctx, event.EventFilter{RunID: run.State.RunID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, evt := range stored {
		lower := strings.ToLower(evt.Type)
		if strings.Contains(lower, "dispatch") || strings.Contains(lower, "tool.") || strings.Contains(lower, "approved") || strings.Contains(lower, "decision") {
			t.Fatalf("pending reflection proposal produced an unapproved effect or approval decision event %q", evt.Type)
		}
	}
}
