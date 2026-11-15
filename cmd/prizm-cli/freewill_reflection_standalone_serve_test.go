package main

import (
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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	"gopkg.in/yaml.v3"
)

func TestE54StandaloneServeReloadsPinnedProjectProfileAndReflection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	dataDir, runsDir := filepath.Join(work, "data"), filepath.Join(work, "runs")
	workspace, policyDir := filepath.Join(work, "workspace"), filepath.Join(work, "policies")
	for _, dir := range []string{dataDir, runsDir, workspace, policyDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("Synthetic E51 decision-only reflection fixture."), 0600); err != nil {
		t.Fatal(err)
	}
	profileURL, profileCommit := "https://github.com/example/synthetic-project", strings.Repeat("c", 40)
	profileFacts := freewill.ProjectContextProfile{
		Identity: freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "e51-synthetic-project", SourceIDs: []string{"github:profile"}},
		Purpose:  freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown}, Domain: freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown},
		Users: freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown}, Architecture: freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown},
		Capabilities: freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown}, Goals: freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown},
		Exclusions: freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown}, History: freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown},
		Claims:    []freewill.ProjectContextClaim{{ID: "synthetic-docs-stack", Status: freewill.ProjectContextFactObserved, Value: "Synthetic README stack claim.", SourceIDs: []string{"github:profile"}}, {ID: "synthetic-code-stack", Status: freewill.ProjectContextFactObserved, Value: "Synthetic code stack claim.", SourceIDs: []string{"github:profile"}}},
		Conflicts: []freewill.ProjectContextConflict{{ID: "synthetic-stack-conflict", ClaimIDs: []string{"synthetic-docs-stack", "synthetic-code-stack"}}},
	}
	profileDigestBody, err := json.Marshal(struct {
		ProjectID     string                         `json:"project_id"`
		RepositoryURL string                         `json:"repository_url"`
		CommitSHA     string                         `json:"commit_sha"`
		Facts         freewill.ProjectContextProfile `json:"facts"`
	}{"e51-synthetic-project", profileURL, profileCommit, profileFacts})
	if err != nil {
		t.Fatal(err)
	}
	profileDigest := sha256.Sum256(profileDigestBody)
	privateProfile := freewill.ImportedProjectProfile{Version: 1, ProjectID: "e51-synthetic-project", RepositoryURL: profileURL, CommitSHA: profileCommit, ProfileDigest: hex.EncodeToString(profileDigest[:]), Facts: profileFacts}
	privateProfilePath := filepath.Join(workspace, ".codex", "private-context", "e51-synthetic-project.json")
	if err := os.MkdirAll(filepath.Dir(privateProfilePath), 0700); err != nil {
		t.Fatal(err)
	}
	privateProfileBytes, err := json.Marshal(privateProfile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateProfilePath, privateProfileBytes, 0600); err != nil {
		t.Fatal(err)
	}
	policy := "policies:\n  - id: e51-requires-owner-review\n    description: E51 synthetic proposal-only gate.\n    match:\n      action: intention.propose\n      resource.type: intention\n      resource.name: freewill_candidate\n    decision: requires_approval\n    reason: Owner review is required for every proposal.\n"
	if err := os.WriteFile(filepath.Join(policyDir, "e51.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}

	const ownerID, projectID, agentID = "e51-synthetic-owner", "e51-synthetic-project", "e51-synthetic-agent"
	dbPath := filepath.Join(dataDir, "freewill.db")
	priorEvents, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	fact := multiagent.FreewillTerminalFact{ProjectID: projectID, Workspace: workspace, AgentID: agentID, IntentionID: "e51-prior-intention", ParentRunID: "e51-prior-parent", ChildRunID: "e51-prior-child", Status: "execution_failed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64), ValidationSummary: "failed", FailureCode: "synthetic_failure", RecordedAt: time.Now().UTC().Add(-2 * time.Hour)}
	receipt := event.NewEvent(event.EventFreewillExecutionFailed, "e51-synthetic-host", map[string]any{"child_run_id": fact.ChildRunID, "report_ref": fact.ReportRef, "report_sha256": fact.ReportSHA256, "validation_summary": fact.ValidationSummary})
	receipt.Metadata.RunID = fact.ParentRunID
	if err := priorEvents.Store(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	facts, err := newFreewillTerminalFactMemorySink(dataDir, projectID, workspace, priorEvents, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := facts.CaptureFreewillTerminalFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	appraisalResponse, _ := json.Marshal(freewillTaskAppraisalResponse{AppraisalKind: "satisfaction", PerceivedOutcome: "completed", Why: "Synthetic self-report differs from the host receipt.", Context: "Local fixture only.", Narrative: "Tentative source for a bounded reflection.", Uncertainty: "One synthetic task is not stable evidence.", Scores: freewillSyntheticTaskScores()})
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{narrative: string(appraisalResponse), provider: "local-fake", model: "e51-appraiser", version: "fixture-v1"}, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := appraisals.Capture(ctx, fact); err != nil {
		t.Fatal(err)
	}
	reflection, err := newFreewillTaskReflectionSink(appraisals, &freewillAdaptiveTaskReflectionFake{}, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := reflection.ReflectTask(ctx, fact); err != nil {
		t.Fatal(err)
	}
	prior, err := reflection.ReadLatestOwnerProjectReflection(ctx, ownerID, projectID, workspace, agentID)
	if err != nil || prior == nil || prior.Uncertainty == "" || prior.Binding.ID == "" || prior.Binding.Source != freewillTaskReflectionSource {
		t.Fatalf("seeded host-linked uncertain reflection unavailable: %#v err=%v", prior, err)
	}
	if err := priorEvents.Close(); err != nil {
		t.Fatal(err)
	}

	// A fabricated ID is rejected by the same exact-binding validation used by
	// the served selection. The real served request below supplies only the
	// host-issued reflection ID.
	wrongBinding := freewill.ReflectionBinding{ID: "e51-fabricated-reflection", Revision: prior.Binding.Revision, Source: prior.Binding.Source}
	wrongCandidates := make([]freewill.CandidateIntention, 0, 3)
	for i := 0; i < 3; i++ {
		source := freewill.SourceNew
		reflectionID, revision, reflectionSource := "", "", ""
		if i == 0 {
			source, reflectionID, revision, reflectionSource = freewill.SourceReflection, wrongBinding.ID, wrongBinding.Revision, wrongBinding.Source
		}
		requestID := fmt.Sprintf("e51-request-%d", i)
		request := freewill.IntentionRequest{RequestID: requestID, AgentID: agentID, Origin: freewill.OriginAgent, CandidateSource: source, ReflectionID: reflectionID, ReflectionRevision: revision, ReflectionSource: reflectionSource, Title: "Review prior reflection", Goal: "Perform a bounded read-only evidence review.", ReasonSummary: "The reflection is uncertain.", RequestedCapabilities: []string{"decision_only"}, Scope: freewill.Scope{ProjectID: projectID, Workspace: workspace}, IdempotencyKey: "e51-idempotency-" + requestID, CorrelationID: "e51-correlation-" + requestID}
		wrongCandidates = append(wrongCandidates, freewill.CandidateIntention{CandidateID: fmt.Sprintf("e51-candidate-%d", i), Request: request, Score: 70 - i, ScoreFactors: []string{"feasibility"}, ScoreRationale: "Bounded evidence review.", Uncertainty: "Tentative hypothesis."})
	}
	wrong := freewill.Selection{Candidates: wrongCandidates, ChoiceID: wrongCandidates[0].CandidateID, ReasonSummary: "A reflection may be reviewed without granting authority."}
	if err := wrong.Validate(agentID, freewill.Scope{ProjectID: projectID, Workspace: workspace}, nil, map[string]freewill.ReflectionBinding{prior.Binding.ID: prior.Binding}); err == nil || !strings.Contains(err.Error(), "not bound to a host-verified reflection") {
		t.Fatal("fabricated reflection ID passed host-issued citation validation")
	}

	var providerCalls atomic.Int32
	providerFailure := make(chan string, 8)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 {
			providerFailure <- "invalid local Ollama request"
			http.Error(w, "invalid local fixture request", http.StatusBadRequest)
			return
		}
		var input struct {
			PriorReflection *multiagent.FreewillReflectionEvidence `json:"prior_reflection"`
			ProjectContext  freewill.ProjectContextSnapshot        `json:"project_context"`
		}
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil || input.PriorReflection == nil || input.PriorReflection.Binding.ID != prior.Binding.ID || input.PriorReflection.Binding.Revision != prior.Binding.Revision || input.PriorReflection.Binding.Source != prior.Binding.Source || input.PriorReflection.OwnerID != ownerID || input.PriorReflection.ProjectID != projectID || input.PriorReflection.Workspace != workspace || input.PriorReflection.AgentID != agentID || input.PriorReflection.Alignment != "disagreement" || input.PriorReflection.Hypothesis != prior.Hypothesis || input.PriorReflection.Uncertainty == "" || input.ProjectContext.ProjectID != projectID || len(input.ProjectContext.Sources) < 2 || input.ProjectContext.Sources[len(input.ProjectContext.Sources)-1].ID != "github:profile" || input.ProjectContext.Sources[len(input.ProjectContext.Sources)-1].SourceURL != profileURL || input.ProjectContext.Sources[len(input.ProjectContext.Sources)-1].CommitSHA != profileCommit || input.ProjectContext.Sources[len(input.ProjectContext.Sources)-1].Confidence != "host_reviewed_uncertain_import" || len(input.ProjectContext.Profile.Conflicts) != 1 {
			providerFailure <- "standalone selector did not receive exact persisted reflection and pinned synthetic project profile"
			http.Error(w, "context binding mismatch", http.StatusBadRequest)
			return
		}
		call := providerCalls.Add(1)
		reflectionID := prior.Binding.ID
		if call == 1 {
			reflectionID = "e51-fabricated-reflection"
		}
		payload := e51ReflectionSelectionJSON(reflectionID)
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "e51-local-fake", "done": true, "message": map[string]any{"role": "assistant", "content": payload}})
	}))
	defer providerServer.Close()

	natsURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("start isolated local NATS: %v", err)
	}
	defer stopBus()
	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.InstanceID = "e51-standalone-reflection-fixture"
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir, cfg.Prizm.Workspace = dataDir, runsDir, workspace
	cfg.Prizm.ReadRoots = []string{workspace}
	cfg.Prizm.BindHost, cfg.Prizm.NATSURL, cfg.Prizm.OllamaURL = "127.0.0.1", natsURL, providerServer.URL
	cfg.Prizm.Scheduler = orchestrator.SchedulerConfig{Enabled: true, Jobs: []orchestrator.SchedulerJobConfig{{Name: "e51-synthetic-freewill-tick", Schedule: "* * * * *", Event: "prizm.freewill.scheduled", Payload: map[string]any{"source": "e51-synthetic-scheduler"}, Enabled: true}}}
	cfg.Prizm.ContextCompression = &agent.CompressionConfig{Enabled: false}
	cfg.API.AuthToken, cfg.API.AuthTokenEnv = "e51-synthetic-api-token", ""
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: agentID, ProjectID: projectID, Model: "e51-local-fake", PolicyDir: policyDir,
		ProjectProfileRepositoryURL: profileURL, ProjectProfileCommitSHA: profileCommit,
		TaskAppraisalEnabled: true, TaskAppraisalOwnerID: ownerID, TaskReflectionEnabled: true, TaskReflectionOwnerID: ownerID,
		Scheduling:    orchestrator.FreewillSchedulingConfig{Enabled: true, Schedule: "* * * * *"},
		MemoryContext: orchestrator.FreewillMemoryContextConfig{OwnerID: ownerID}}
	cfg.Agents = []orchestrator.AgentConfig{{ID: agentID, Role: "researcher", Provider: "ollama", Model: "e51-local-fake", Primary: true}}
	cfg.Projects = []orchestrator.ProjectConfig{{ID: projectID, RepoPath: workspace, FreewillTerminalFactsProjectID: projectID, FreewillTerminalFactsRepoPath: workspace}}
	cfg.Channels, cfg.Actions, cfg.MCPServers = []orchestrator.ChannelConfig{}, []orchestrator.ActionConfig{}, []orchestrator.MCPServerConfig{}
	cfg.Remembrance.Enabled, cfg.FactoryMonitor.Enabled, cfg.Bridge.Enabled, cfg.Codex.Enabled, cfg.ClaudeCode.Enabled, cfg.Autopatch.Enabled = false, false, false, false, false, false
	configBytes, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "prizm.yaml")
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(work, "prizm.exe")
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
		cmd  *exec.Cmd
		done chan error
	}
	var processes []serveProcess
	startServe := func() serveProcess {
		cmd := exec.Command(binaryPath, "serve", "--config", configPath, "--port", strconv.Itoa(port))
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "TEMP="+work, "TMP="+work, "TMPDIR="+work)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start isolated prizm serve: %v", err)
		}
		p := serveProcess{cmd: cmd, done: make(chan error, 1)}
		processes = append(processes, p)
		go func() { p.done <- cmd.Wait() }()
		if !e27WaitHealth(ctx, strings.TrimSuffix(healthURL, "/api/v1/status"), p.done) {
			t.Fatalf("standalone prizm serve pid %d did not become healthy", cmd.Process.Pid)
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
	waitProviderCall := func(want int32) {
		t.Helper()
		for providerCalls.Load() < want {
			select {
			case failure := <-providerFailure:
				t.Fatal(failure)
			case <-ctx.Done():
				t.Fatalf("real scheduled wake did not reach local selector call %d: %v", want, ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
		select {
		case failure := <-providerFailure:
			t.Fatal(failure)
		default:
		}
	}

	// The first real cron-fired wake receives a fabricated citation and must
	// fail host validation without producing a proposal. Restarting the actual
	// service then exercises the same persisted reflection with a valid ID.
	first := startServe()
	waitProviderCall(1)
	waitE51Event(t, ctx, dbPath, event.EventFreewillSelectionRetryScheduled)
	stopServe(first)
	failedStore, err := multiagent.OpenExistingSQLiteDurableRunStoreReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	failedRuns, err := failedStore.ListFreewillRuns(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	_ = failedStore.Close()
	var failedCitation bool
	for _, run := range failedRuns {
		if run.Freewill != nil && run.Freewill.SelectionFailureCode != "" && run.Freewill.Lifecycle != freewill.StateApprovalPending {
			failedCitation = true
		}
	}
	if !failedCitation {
		t.Fatal("fabricated selector reflection ID did not persist as a rejected selection")
	}
	assertE51NoApprovalOrEffects(t, ctx, dbPath, false)

	second := startServe()
	waitProviderCall(2)
	waitE51Event(t, ctx, dbPath, event.EventFreewillApprovalProposed)
	stopServe(second)
	store, err := multiagent.OpenExistingSQLiteDurableRunStoreReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := store.ListFreewillRuns(ctx, "", 30)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	var proposed *multiagent.DurableRun
	for i := range runs {
		if runs[i].Freewill != nil && runs[i].Freewill.Lifecycle == freewill.StateApprovalPending {
			copy := runs[i]
			proposed = &copy
		}
	}
	if proposed == nil {
		t.Fatal("valid scheduled wake did not persist an approval-pending proposal after service restart")
	}
	manifest := proposed.Freewill.ProjectContextManifest
	if manifest == nil || manifest.Version != freewill.ProjectContextSourceManifestVersion || manifest.ProjectID != projectID || manifest.Validate() != nil {
		t.Fatalf("restart proposal lacks valid v2 project context manifest: %#v", manifest)
	}
	var profileSource *freewill.ProjectContextSourceEntry
	for i := range manifest.Sources {
		if manifest.Sources[i].ID == "github:profile" {
			profileSource = &manifest.Sources[i]
			break
		}
	}
	if profileSource == nil || profileSource.SourceURL != profileURL || profileSource.CommitSHA != profileCommit || profileSource.Confidence != "host_reviewed_uncertain_import" || profileSource.Digest == "" {
		t.Fatalf("persisted restart manifest lacks pinned private-profile provenance: %#v", profileSource)
	}
	if proposed.Freewill.ChoiceID == "" || proposed.Freewill.ApprovalRef == "" || proposed.Freewill.ApprovalReceipt == nil || proposed.Freewill.ApprovalReceipt.Decision != multiagent.FreewillApprovalPending || proposed.Freewill.ExecutionDispatch != nil || !proposed.Freewill.ExecutionDisabled {
		t.Fatalf("proposal crossed the decision-only boundary: %#v", proposed.Freewill)
	}
	if len(proposed.Freewill.Candidates) != 3 {
		t.Fatalf("fresh candidate inventory was not preserved: got %d want 3", len(proposed.Freewill.Candidates))
	}
	var reflectionCandidates int
	for _, candidate := range proposed.Freewill.Candidates {
		if candidate.Request.CandidateSource == freewill.SourceReflection {
			reflectionCandidates++
			if candidate.Request.ReflectionID != prior.Binding.ID || candidate.Request.ReflectionRevision != prior.Binding.Revision || candidate.Request.ReflectionSource != prior.Binding.Source || candidate.Uncertainty == "" {
				t.Fatalf("persisted reflection candidate lacks exact uncertain host binding: %#v", candidate)
			}
			if candidate.CandidateID != proposed.Freewill.ChoiceID {
				t.Fatalf("proposal did not choose the reflection-linked follow-up candidate: choice=%s candidate=%s", proposed.Freewill.ChoiceID, candidate.CandidateID)
			}
		}
	}
	if reflectionCandidates != 1 {
		t.Fatalf("reflection candidate count=%d want=1", reflectionCandidates)
	}
	assertE51NoApprovalOrEffects(t, ctx, dbPath, true)
	if got := providerCalls.Load(); got != 2 {
		t.Fatalf("local fake selector calls=%d want=2 (one rejected citation, one accepted after restart)", got)
	}
}

func e51ReflectionSelectionJSON(reflectionID string) string {
	return `{"candidates":[` +
		`{"title":"Review the prior hypothesis","goal":"Compare the uncertain reflection hypothesis with a bounded read-only evidence review.","reason_summary":"One synthetic task is not enough to infer a stable interest.","requested_capabilities":["decision_only"],"budget":null,"score":74,"score_factors":["feasibility"],"score_rationale":"A narrow evidence review is feasible.","basis_refs":["project:readme"],"uncertainty":"The reflection is a tentative hypothesis.","reflection_id":"` + reflectionID + `"},` +
		`{"title":"Review local test coverage","goal":"Summarize the local test coverage at a high level.","reason_summary":"This is bounded and read-only.","requested_capabilities":["decision_only"],"budget":null,"score":63,"score_factors":["scope_fit"],"score_rationale":"A small read-only summary is feasible.","basis_refs":["project:readme"],"uncertainty":"The README may be incomplete."},` +
		`{"title":"Map project modules","goal":"List top-level modules for later review.","reason_summary":"A bounded inventory may clarify project structure.","requested_capabilities":["decision_only"],"budget":null,"score":54,"score_factors":["readiness"],"score_rationale":"A module inventory is a small proposal.","basis_refs":["project:readme"],"uncertainty":"The README may be stale."}` +
		`],"resume_candidate_id":"","reason_summary":"The cited hypothesis is advisory and requires normal owner review.","sleep_reason":""}`
}

func waitE51Event(t *testing.T, ctx context.Context, dbPath, eventType string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := event.NewSQLiteEventStore(dbPath)
		if err == nil {
			rows, queryErr := events.Query(ctx, event.EventFilter{Type: eventType, Limit: 100})
			_ = events.Close()
			if queryErr == nil && len(rows) > 0 {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for persisted event %s: %v", eventType, ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertE51NoApprovalOrEffects(t *testing.T, ctx context.Context, dbPath string, expectProposal bool) {
	t.Helper()
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	for _, eventType := range []string{event.EventFreewillApprovalProposed, event.EventFreewillApprovalDecided, event.EventFreewillExecutionDispatched, event.EventMultiAgentToolDiagnostic, event.EventInteractionActionExecuted} {
		rows, err := events.Query(ctx, event.EventFilter{Type: eventType, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if eventType == event.EventFreewillApprovalProposed && expectProposal {
			if len(rows) != 1 {
				t.Fatalf("pending proposals=%d want=1", len(rows))
			}
			continue
		}
		if len(rows) != 0 {
			t.Fatalf("unexpected approval/effect event %s count=%d", eventType, len(rows))
		}
	}
}
