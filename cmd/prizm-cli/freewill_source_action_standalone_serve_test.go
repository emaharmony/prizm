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
	"github.com/nats-io/nats.go"
	"gopkg.in/yaml.v3"
)

func TestE56StandaloneServeBindsSourceBackedActionAcrossRestart(t *testing.T) {
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
	const projectID, agentID = "e56-synthetic-project", "e56-synthetic-agent"
	const model = "glm-5.3-flash"
	const actionID = "project:roadmap:M2"
	const actionText = "Improve artist listings and venue booking discovery"
	readme := "# Synthetic Music Platform\n\n## Milestone Roadmap\n\n| Milestone | Scope | Status |\n| --- | --- | --- |\n| M2 | " + actionText + " | In progress |\n"
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(readme), 0600); err != nil {
		t.Fatal(err)
	}
	profileURL, profileCommit := "https://github.com/example/e56-synthetic", strings.Repeat("e", 40)
	profileFacts := freewill.ProjectContextProfile{
		Identity:     freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: projectID, SourceIDs: []string{"github:profile"}},
		Purpose:      freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Synthetic talent-first music discovery and booking.", SourceIDs: []string{"github:profile"}},
		Domain:       freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Music listings, artist discovery, and venue booking.", SourceIDs: []string{"github:profile"}},
		Users:        freewill.ProjectContextFact{Status: freewill.ProjectContextFactInferred, Value: "Artists and venues are the primary synthetic users.", SourceIDs: []string{"github:profile"}},
		Architecture: freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown},
		Capabilities: freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Artist listings and venue booking discovery.", SourceIDs: []string{"github:profile"}},
		Goals:        freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Make listing and booking workflows clearer.", SourceIDs: []string{"github:profile"}},
		Exclusions:   freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Instrument repair and string recommendations are out of scope.", SourceIDs: []string{"github:profile"}},
		History:      freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown},
		Claims: []freewill.ProjectContextClaim{
			{ID: "synthetic-booking-capability", Status: freewill.ProjectContextFactObserved, Value: "The fictional platform supports venue booking.", SourceIDs: []string{"github:profile"}},
			{ID: "synthetic-instrument-exclusion", Status: freewill.ProjectContextFactInferred, Value: "Instrument maintenance is outside the platform scope.", SourceIDs: []string{"github:profile"}},
		},
		Conflicts: []freewill.ProjectContextConflict{{ID: "synthetic-scope-disagreement", ClaimIDs: []string{"synthetic-booking-capability", "synthetic-instrument-exclusion"}}},
	}
	profileBody, err := json.Marshal(struct {
		ProjectID     string                         `json:"project_id"`
		RepositoryURL string                         `json:"repository_url"`
		CommitSHA     string                         `json:"commit_sha"`
		Facts         freewill.ProjectContextProfile `json:"facts"`
	}{projectID, profileURL, profileCommit, profileFacts})
	if err != nil {
		t.Fatal(err)
	}
	profileDigest := sha256.Sum256(profileBody)
	profile := freewill.ImportedProjectProfile{Version: 1, ProjectID: projectID, RepositoryURL: profileURL, CommitSHA: profileCommit,
		ProfileDigest: hex.EncodeToString(profileDigest[:]), Facts: profileFacts}
	profilePath := filepath.Join(workspace, ".codex", "private-context", projectID+".json")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0700); err != nil {
		t.Fatal(err)
	}
	profileBytes, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, profileBytes, 0600); err != nil {
		t.Fatal(err)
	}
	policy := "policies:\n  - id: e56-owner-review-required\n    description: Synthetic proposal-only policy.\n    match:\n      action: intention.propose\n      resource.type: intention\n      resource.name: freewill_candidate\n    decision: requires_approval\n    reason: Owner review is required for this synthetic proposal.\n"
	if err := os.WriteFile(filepath.Join(policyDir, "e56.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}

	var providerCalls atomic.Int32
	var ideaCalls atomic.Int32
	var choiceCalls atomic.Int32
	providerFailure := make(chan string, 8)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			providerFailure <- "unexpected fake Ollama endpoint"
			http.Error(w, "invalid synthetic request", http.StatusBadRequest)
			return
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != model || len(request.Messages) != 2 || len(request.Tools) != 0 {
			providerFailure <- "invalid local selector provider contract"
			http.Error(w, "invalid synthetic request", http.StatusBadRequest)
			return
		}
		call := providerCalls.Add(1)
		if call > 4 {
			providerFailure <- "E56 fake provider call bound exceeded"
			http.Error(w, "E56 call bound exceeded", http.StatusTooManyRequests)
			return
		}
		var content string
		if strings.Contains(request.Messages[0].Content, "exactly three safe, decision-only blocks") {
			if call != 1 && call != 3 {
				providerFailure <- "E56 unexpected idea/choice phase order"
				http.Error(w, "unexpected phase", http.StatusBadRequest)
				return
			}
			ideaCalls.Add(1)
			var input struct {
				ProjectContext freewill.ProjectContextSnapshot `json:"project_context"`
			}
			if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil || input.ProjectContext.ProjectID != projectID ||
				len(input.ProjectContext.RoadmapActions) != 1 || input.ProjectContext.RoadmapActions[0].ID != actionID ||
				input.ProjectContext.RoadmapActions[0].Text != actionText || input.ProjectContext.RoadmapActions[0].SourceID != "project:readme" ||
				input.ProjectContext.Profile.Domain.Value == "" || len(input.ProjectContext.Profile.Claims) != 2 || len(input.ProjectContext.Profile.Conflicts) != 1 {
				providerFailure <- "E56 selector did not receive the synthetic profile and exact host action"
				http.Error(w, "synthetic context mismatch", http.StatusBadRequest)
				return
			}
			secondWake := call == 3
			content = e56IdeaResponse(secondWake, actionID)
		} else {
			if call != 2 && call != 4 {
				providerFailure <- "E56 unexpected idea/choice phase order"
				http.Error(w, "unexpected phase", http.StatusBadRequest)
				return
			}
			choiceCalls.Add(1)
			var input struct {
				Candidates []struct {
					ID    string `json:"id"`
					Title string `json:"title"`
					Goal  string `json:"goal"`
				} `json:"candidates"`
			}
			if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil || len(input.Candidates) != 3 {
				providerFailure <- "E56 choice request omitted the three candidate allowlist"
				http.Error(w, "invalid choice context", http.StatusBadRequest)
				return
			}
			selectedID := input.Candidates[0].ID
			if call == 4 {
				selectedID = ""
				for _, candidate := range input.Candidates {
					if candidate.Title == actionText && candidate.Goal == actionText {
						if selectedID != "" {
							providerFailure <- "E56 host action was duplicated in choice context"
							http.Error(w, "duplicate bound action", http.StatusBadRequest)
							return
						}
						selectedID = candidate.ID
					}
				}
				if selectedID == "" {
					providerFailure <- "E56 host action text was not bound before phase-two choice"
					http.Error(w, "host action not bound", http.StatusBadRequest)
					return
				}
			}
			content = "CHOICE: " + selectedID + "\nREASON: The host-observed project action best fits this bounded review."
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "done": true,
			"message": map[string]any{"role": "assistant", "content": content}})
	}))
	defer providerServer.Close()

	natsURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("start isolated local NATS: %v", err)
	}
	defer stopBus()
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("connect isolated local NATS: %v", err)
	}
	defer nc.Close()

	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.InstanceID = "e56-source-action-fixture"
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir, cfg.Prizm.Workspace = dataDir, runsDir, workspace
	cfg.Prizm.ReadRoots = []string{workspace}
	cfg.Prizm.BindHost, cfg.Prizm.NATSURL, cfg.Prizm.OllamaURL = "127.0.0.1", natsURL, providerServer.URL
	cfg.Prizm.ContextCompression = &agent.CompressionConfig{Enabled: false}
	cfg.API.AuthToken, cfg.API.AuthTokenEnv = "e56-synthetic-api-token", ""
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: agentID, ProjectID: projectID, Model: model, PolicyDir: policyDir,
		ProjectProfileRepositoryURL: profileURL, ProjectProfileCommitSHA: profileCommit,
		Scheduling: orchestrator.FreewillSchedulingConfig{Enabled: true, Schedule: "* * * * *"}}
	cfg.Agents = []orchestrator.AgentConfig{{ID: agentID, Role: "researcher", Provider: "ollama", Model: model, Primary: true}}
	cfg.Projects = []orchestrator.ProjectConfig{{ID: projectID, RepoPath: workspace}}
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
		t.Fatalf("build standalone Prizm CLI: %v: %s", err, strings.TrimSpace(string(output)))
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
		childEnv := make([]string, 0, len(os.Environ())+3)
		for _, entry := range os.Environ() {
			upper := strings.ToUpper(entry)
			if strings.HasPrefix(upper, "OLLAMA_API_KEY=") || strings.HasPrefix(upper, "PRIZM_FREEWILL_OLLAMA_CLOUD_DIRECT=") {
				continue
			}
			childEnv = append(childEnv, entry)
		}
		cmd.Env = append(childEnv, "TEMP="+work, "TMP="+work, "TMPDIR="+work, "PRIZM_FREEWILL_OLLAMA_CLOUD_DIRECT=0")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start isolated Prizm serve: %v", err)
		}
		p := serveProcess{cmd: cmd, done: make(chan error, 1)}
		processes = append(processes, p)
		go func() { p.done <- cmd.Wait() }()
		if !e27WaitHealth(ctx, strings.TrimSuffix(healthURL, "/api/v1/status"), p.done) {
			t.Fatalf("standalone Prizm serve pid %d did not become healthy", cmd.Process.Pid)
		}
		return p
	}
	stopServe := func(p serveProcess) {
		if p.cmd.ProcessState != nil {
			return
		}
		if err := p.cmd.Process.Kill(); err != nil {
			t.Fatalf("stop isolated Prizm serve pid %d: %v", p.cmd.Process.Pid, err)
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("standalone Prizm serve pid %d did not stop", p.cmd.Process.Pid)
		}
	}
	t.Cleanup(func() {
		for _, p := range processes {
			if p.cmd.ProcessState == nil {
				_ = p.cmd.Process.Kill()
				select {
				case <-p.done:
				case <-time.After(10 * time.Second):
					t.Errorf("standalone Prizm serve pid %d did not stop", p.cmd.Process.Pid)
				}
			}
		}
	})
	publishWake := func() {
		t.Helper()
		// Health becomes available during server startup; let the Freewill
		// runtime finish installing and flushing its NATS subscription first.
		time.Sleep(250 * time.Millisecond)
		if err := nc.Publish(freewillScheduleSubject, []byte(`{"fixture":"e56-synthetic"}`)); err != nil {
			t.Fatalf("publish one synthetic wake: %v", err)
		}
		if err := nc.FlushTimeout(2 * time.Second); err != nil {
			t.Fatalf("flush one synthetic wake: %v", err)
		}
	}
	waitProviderCalls := func(want int32) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for providerCalls.Load() < want {
			if time.Now().After(deadline) {
				t.Fatalf("served synthetic selector calls=%d want at least %d", providerCalls.Load(), want)
			}
			select {
			case failure := <-providerFailure:
				t.Fatal(failure)
			case <-ctx.Done():
				t.Fatalf("served synthetic selector did not reach call %d: %v", want, ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
		select {
		case failure := <-providerFailure:
			t.Fatal(failure)
		default:
		}
	}
	waitEventCount := func(eventType string, count int) {
		t.Helper()
		for {
			store, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "freewill.db"))
			if err == nil {
				rows, queryErr := store.Query(ctx, event.EventFilter{Type: eventType, Limit: 100})
				_ = store.Close()
				if queryErr == nil && len(rows) >= count {
					return
				}
			}
			select {
			case failure := <-providerFailure:
				t.Fatal(failure)
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %d persisted %s events: %v", count, eventType, ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	listRuns := func() []multiagent.DurableRun {
		t.Helper()
		store, err := multiagent.OpenExistingSQLiteDurableRunStoreReadOnly(filepath.Join(dataDir, "freewill.db"))
		if err != nil {
			t.Fatal(err)
		}
		runs, err := store.ListFreewillRuns(ctx, projectID, 20)
		_ = store.Close()
		if err != nil {
			t.Fatal(err)
		}
		return runs
	}

	first := startServe()
	publishWake()
	waitProviderCalls(2)
	waitEventCount(event.EventFreewillSelectionCommitted, 1)
	stopServe(first)
	var sleeping *multiagent.DurableRun
	for _, run := range listRuns() {
		if run.Freewill != nil && run.Freewill.Lifecycle == freewill.StateSleeping {
			copy := run
			sleeping = &copy
			break
		}
	}
	if sleeping == nil || len(sleeping.Freewill.Candidates) != 3 || sleeping.Freewill.ChoiceID != "" || strings.TrimSpace(sleeping.Freewill.SleepReason) == "" || sleeping.Freewill.ApprovalRef != "" {
		t.Fatalf("citation-only exploration was not safely persisted as sleep: %#v", sleeping)
	}
	for _, candidate := range sleeping.Freewill.Candidates {
		if candidate.ProjectGrounding != "exploration" || candidate.ProjectActionID != "" || len(candidate.BasisRefs) != 1 || candidate.BasisRefs[0] != "project:readme" {
			t.Fatalf("source citation alone became a roadmap action: %#v", candidate)
		}
	}
	assertE51NoApprovalOrEffects(t, ctx, filepath.Join(dataDir, "freewill.db"), false)

	// Scheduled wake IDs are deduplicated by UTC minute. The restarted service
	// must exercise a new minute so this second wake is independently admitted.
	nextTick := time.Now().UTC().Truncate(time.Minute).Add(time.Minute).Add(100 * time.Millisecond)
	for time.Now().UTC().Before(nextTick) {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for a fresh scheduled-tick identity: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	second := startServe()
	publishWake()
	waitProviderCalls(4)
	waitEventCount(event.EventFreewillApprovalProposed, 1)
	waitEventCount(event.EventFreewillSelectionCommitted, 2)
	stopServe(second)
	var proposed *multiagent.DurableRun
	for _, run := range listRuns() {
		if run.Freewill != nil && run.Freewill.Lifecycle == freewill.StateApprovalPending {
			copy := run
			proposed = &copy
			break
		}
	}
	if proposed == nil || len(proposed.Freewill.Candidates) != 3 || proposed.Freewill.ChoiceID == "" || proposed.Freewill.ApprovalRef == "" {
		t.Fatalf("valid host action did not persist as a reviewable proposal: %#v", proposed)
	}
	var selected *freewill.CandidateIntention
	for i := range proposed.Freewill.Candidates {
		candidate := &proposed.Freewill.Candidates[i]
		if candidate.CandidateID == proposed.Freewill.ChoiceID {
			selected = candidate
		}
		if candidate.Request.CandidateSource != freewill.SourceNew || len(candidate.Request.RequestedCapabilities) != 1 || candidate.Request.RequestedCapabilities[0] != "decision_only" {
			t.Fatalf("selector altered host-owned candidate source/capabilities: %#v", candidate.Request)
		}
	}
	if selected == nil || selected.ProjectGrounding != "roadmap_action" || selected.ProjectActionID != actionID ||
		selected.Request.Title != actionText || selected.Request.Goal != actionText || strings.Contains(strings.ToLower(selected.Request.Title), "guitar") ||
		len(selected.BasisRefs) != 1 || selected.BasisRefs[0] != "project:readme" || strings.TrimSpace(selected.Uncertainty) == "" {
		t.Fatalf("selected candidate was not rebound to exact source-backed action: %#v", selected)
	}
	manifest := proposed.Freewill.ProjectContextManifest
	if manifest == nil || manifest.Version != freewill.ProjectContextSourceManifestVersion || manifest.ProjectID != projectID || manifest.Validate() != nil {
		t.Fatalf("proposal did not persist valid v2 source manifest: %#v", manifest)
	}
	var profileSource *freewill.ProjectContextSourceEntry
	var roadmapSource *freewill.ProjectContextSourceEntry
	for i := range manifest.Sources {
		switch manifest.Sources[i].ID {
		case "github:profile":
			profileSource = &manifest.Sources[i]
		case "project:readme":
			roadmapSource = &manifest.Sources[i]
		}
	}
	if profileSource == nil || profileSource.SourceURL != profileURL || profileSource.CommitSHA != profileCommit || profileSource.Confidence != "host_reviewed_uncertain_import" || profileSource.Digest == "" ||
		len(manifest.Profile.Claims) != 2 || len(manifest.Profile.Conflicts) != 1 {
		t.Fatalf("persisted manifest omitted the synthetic reviewed profile provenance: %#v", profileSource)
	}
	if roadmapSource == nil || roadmapSource.Path != "README.md" || roadmapSource.SourceType != "local_file" || roadmapSource.Digest == "" || roadmapSource.Revision == "" {
		t.Fatalf("persisted manifest omitted exact synthetic README provenance: %#v", roadmapSource)
	}
	assertE51NoApprovalOrEffects(t, ctx, filepath.Join(dataDir, "freewill.db"), true)
	if providerCalls.Load() != 4 || ideaCalls.Load() != 2 || choiceCalls.Load() != 2 {
		t.Fatalf("bounded local fake calls mismatch: total=%d idea=%d choice=%d; want 4/2/2", providerCalls.Load(), ideaCalls.Load(), choiceCalls.Load())
	}
}

func e56IdeaResponse(secondWake bool, actionID string) string {
	first := "IDEA: Summarize synthetic platform capabilities\nSCORE: 90\nFACTORS: scope_fit\nRATIONALE: This bounded review follows from the synthetic project context and avoids changing repository state.\nBASIS_REFS: project:readme\nUNCERTAINTY: The synthetic README may omit current capabilities.\n"
	second := "IDEA: Explore candidate matching\nSCORE: 70\nFACTORS: feasibility\nRATIONALE: This bounded review follows from the synthetic project context and avoids changing repository state.\nBASIS_REFS: project:readme\nUNCERTAINTY: The synthetic README may omit current capabilities.\n" +
		"IDEA: Compare guitar string gauges\nSCORE: 99\nFACTORS: impact\nRATIONALE: This adversarial freeform suggestion tests host rebinding and does not authorize any repository change.\nACTION_ID: " + actionID + "\nBASIS_REFS: project:readme\nUNCERTAINTY: The synthetic README may omit current capabilities.\n" +
		"IDEA: Review general software architecture\nSCORE: 60\nFACTORS: readiness\nRATIONALE: This bounded review follows from the synthetic project context and avoids changing repository state.\nBASIS_REFS: project:readme\nUNCERTAINTY: The synthetic README may omit current capabilities.\n"
	if secondWake {
		return second
	}
	return first + strings.Replace(first, "Summarize synthetic platform capabilities", "Map synthetic project boundaries", 1) +
		strings.Replace(first, "Summarize synthetic platform capabilities", "Review synthetic venue workflows", 1)
}
