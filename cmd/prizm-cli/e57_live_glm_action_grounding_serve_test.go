package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
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

const e57LiveCloudOptIn = "PRIZM_E57_LIVE_CLOUD"

// TestE57StandaloneLiveGLMSyntheticActionGrounding is a one-shot served probe.
// It passes only synthetic community-event project context to exact glm-5.3-flash:cloud.
// At most two identical event wakes are published; one ProcessWake has at most
// an ideas and a choice call, so the entire test is capped at four Cloud calls.
func TestE57StandaloneLiveGLMSyntheticActionGrounding(t *testing.T) {
	if os.Getenv(e57LiveCloudOptIn) != "1" {
		t.Skip("set PRIZM_E57_LIVE_CLOUD=1 to run the bounded synthetic Cloud probe")
	}
	const model = "glm-5.3-flash:cloud"
	const cloudOptIn = "PRIZM_FREEWILL_OLLAMA_CLOUD_DIRECT"
	if os.Getenv(cloudOptIn) != "1" {
		t.Fatal("direct Ollama Cloud opt-in is required")
	}
	apiKey := os.Getenv("OLLAMA_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		t.Fatal("process-scoped Ollama API key is unavailable")
	}
	// Keep the credential only in this test's local variable until it is passed
	// to the explicitly allowlisted `prizm serve` child. The build child gets a
	// separate minimal environment without provider credentials or opt-in flags.
	for _, name := range []string{"OLLAMA_API_KEY", cloudOptIn, e57LiveCloudOptIn} {
		if err := os.Unsetenv(name); err != nil {
			t.Fatal("clear ambient Cloud credentials before build")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	repo, buildMeta := e57RepositoryBuildRoot(t)
	work := t.TempDir()
	dataDir, runsDir := filepath.Join(work, "data"), filepath.Join(work, "runs")
	workspace, policyDir := filepath.Join(work, "workspace"), filepath.Join(work, "policies")
	for _, dir := range []string{dataDir, runsDir, workspace, policyDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal("create synthetic acceptance directory")
		}
	}
	const projectID, agentID = "e57-synthetic-event-service", "e57-synthetic-agent"
	const eventKind = "prizm.e57.grounding_probe"
	const actionID = "project:roadmap:M2"
	const actionText = "Improve event scheduling and public listings"
	readme := "# Synthetic Community Event Service\n\n" +
		"A fictional service that helps local organizers publish events and coordinate venue schedules.\n\n" +
		"## Milestone Roadmap\n\n| Milestone | Scope | Status |\n| --- | --- | --- |\n| M2 | " + actionText + " | In progress |\n"
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(readme), 0600); err != nil {
		t.Fatal("write synthetic README")
	}
	profileURL, profileCommit := "https://github.com/example/e57-synthetic-event-service", strings.Repeat("7", 40)
	profileFacts := freewill.ProjectContextProfile{
		Identity:     freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: projectID, SourceIDs: []string{"github:profile"}},
		Purpose:      freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Help local organizers publish events and coordinate venues.", SourceIDs: []string{"github:profile"}},
		Domain:       freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Community event scheduling and public listings.", SourceIDs: []string{"github:profile"}},
		Users:        freewill.ProjectContextFact{Status: freewill.ProjectContextFactInferred, Value: "Local organizers and venue managers.", SourceIDs: []string{"github:profile"}},
		Architecture: freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown},
		Capabilities: freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Create event listings and coordinate venue schedules.", SourceIDs: []string{"github:profile"}},
		Goals:        freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Make event schedules easier to publish and review.", SourceIDs: []string{"github:profile"}},
		Exclusions:   freewill.ProjectContextFact{Status: freewill.ProjectContextFactInferred, Value: "Household appliance repair is outside scope.", SourceIDs: []string{"github:profile"}},
		History:      freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown},
		Claims: []freewill.ProjectContextClaim{
			{ID: "synthetic-event-listing-capability", Status: freewill.ProjectContextFactObserved, Value: "The fictional service supports public event listings.", SourceIDs: []string{"github:profile"}},
			{ID: "synthetic-schedule-capability", Status: freewill.ProjectContextFactObserved, Value: "The fictional service supports venue schedules.", SourceIDs: []string{"github:profile"}},
		},
	}
	profileBody, err := json.Marshal(struct {
		ProjectID     string                         `json:"project_id"`
		RepositoryURL string                         `json:"repository_url"`
		CommitSHA     string                         `json:"commit_sha"`
		Facts         freewill.ProjectContextProfile `json:"facts"`
	}{projectID, profileURL, profileCommit, profileFacts})
	if err != nil {
		t.Fatal("encode synthetic profile")
	}
	profileDigest := e57SHA256Hex(profileBody)
	profile := freewill.ImportedProjectProfile{Version: 1, ProjectID: projectID, RepositoryURL: profileURL, CommitSHA: profileCommit,
		ProfileDigest: profileDigest, Facts: profileFacts}
	profilePath := filepath.Join(workspace, ".codex", "private-context", projectID+".json")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0700); err != nil {
		t.Fatal("create synthetic profile directory")
	}
	profileBytes, err := json.Marshal(profile)
	if err != nil {
		t.Fatal("encode synthetic imported profile")
	}
	if err := os.WriteFile(profilePath, profileBytes, 0600); err != nil {
		t.Fatal("write synthetic imported profile")
	}
	policy := "policies:\n  - id: e57-owner-review-required\n    description: Synthetic proposal-only policy.\n    match:\n      action: intention.propose\n      resource.type: intention\n      resource.name: freewill_candidate\n    decision: requires_approval\n    reason: Owner review is required for this synthetic proposal.\n"
	if err := os.WriteFile(filepath.Join(policyDir, "e57.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal("write synthetic proposal policy")
	}

	natsURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal("start isolated embedded NATS")
	}
	defer stopBus()
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal("connect to isolated embedded NATS")
	}
	defer nc.Close()

	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.InstanceID = "e57-live-synthetic-grounding"
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir, cfg.Prizm.Workspace = dataDir, runsDir, workspace
	cfg.Prizm.ReadRoots = []string{workspace}
	cfg.Prizm.BindHost, cfg.Prizm.NATSURL = "127.0.0.1", natsURL
	cfg.Prizm.ContextCompression = &agent.CompressionConfig{Enabled: false}
	cfg.API.AuthToken, cfg.API.AuthTokenEnv = "e57-synthetic-api-token", ""
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: agentID, ProjectID: projectID, Model: model, PolicyDir: policyDir,
		ProjectProfileRepositoryURL: profileURL, ProjectProfileCommitSHA: profileCommit,
		Objective:  "Review the synthetic project's roadmap action for improving event schedules and public listings.",
		Scheduling: orchestrator.FreewillSchedulingConfig{EventKinds: []string{eventKind}}}
	cfg.Agents = []orchestrator.AgentConfig{{ID: agentID, Role: "researcher", Provider: "ollama", Model: model, Primary: true}}
	cfg.Projects = []orchestrator.ProjectConfig{{ID: projectID, RepoPath: workspace,
		FreewillRemoteContextProjectID: projectID, FreewillRemoteContextRepoPath: workspace}}
	cfg.Channels, cfg.Actions, cfg.MCPServers = []orchestrator.ChannelConfig{}, []orchestrator.ActionConfig{}, []orchestrator.MCPServerConfig{}
	cfg.Remembrance.Enabled, cfg.FactoryMonitor.Enabled, cfg.Bridge.Enabled, cfg.Codex.Enabled, cfg.ClaudeCode.Enabled, cfg.Autopatch.Enabled = false, false, false, false, false, false
	configBytes, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal("encode standalone serve config")
	}
	configPath := filepath.Join(work, "prizm.yaml")
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal("write standalone serve config")
	}
	binaryPath := filepath.Join(work, "prizm.exe")
	build := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "./cmd/prizm-cli")
	build.Dir = repo
	build.Env = e57MinimalProcessEnvironment(work)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build standalone CLI failed (output bytes=%d): %v", len(output), err)
	}
	childBinarySHA, err := e57FileSHA256(binaryPath)
	if err != nil {
		t.Fatal("hash standalone CLI binary")
	}
	t.Logf("E57_BUILD_META root_id=%s common_dir_id=%s head=%s test_source_sha256=%s child_binary_sha256=%s", buildMeta.rootID, buildMeta.commonDirID, buildMeta.head, buildMeta.testSourceSHA256, childBinarySHA)
	port, err := e27ReservePort()
	if err != nil {
		t.Fatal("reserve isolated serve port")
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port+1)
	cmd := exec.Command(binaryPath, "serve", "--config", configPath, "--port", strconv.Itoa(port))
	cmd.Dir = repo
	cmd.Env = e57MinimalServeEnvironment(work, apiKey)
	var serveOutput e57LockedBuffer
	cmd.Stdout, cmd.Stderr = &serveOutput, &serveOutput
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal("start isolated `prizm serve`")
	}
	go func() { done <- cmd.Wait() }()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Errorf("standalone serve child did not stop")
			}
		}
		logs := serveOutput.String()
		subscriptionHash := e57SHA256Hex([]byte(eventKind))
		mapperMetadata := strings.Join(e57SafeMapperDiagnosticSummary(logs), ";")
		if mapperMetadata == "" {
			mapperMetadata = "none"
		}
		t.Logf("E57_STARTUP_META pid=%d executable=prizm.exe config_sha256=%s nats_url_matches=%t subscription_config_matches=%t subscription_ready=%t freewill_enabled=true selector_route_requested=direct_cloud startup_runtime_line=%t nats_connected_line=%t freewill_start_error=%t callback_entered=%t callback_skipped=%t callback_rejected=%t callback_deferred=%t callback_returned=%t event_wake_ignored=%t event_wake_deferred=%t recovery_scan_failed=%t mapper_diagnostics=%s", cmd.Process.Pid, e57SHA256Hex(configBytes), strings.Contains(logs, "NATS: external at "+natsURL), strings.Contains(logs, fmt.Sprintf("[FREEWILL-DIAG] event_subscription_config wake_sources=true scheduling=false count=1 subjects_sha256=%s", subscriptionHash)), strings.Contains(logs, fmt.Sprintf("[FREEWILL-DIAG] event_subscriptions_ready count=1 subjects_sha256=%s", subscriptionHash)), strings.Contains(logs, "decision-only runtime enabled"), strings.Contains(logs, "NATS: connected"), strings.Contains(logs, "Error starting Freewill"), strings.Contains(logs, "[FREEWILL-DIAG] event_callback_entered"), strings.Contains(logs, "[FREEWILL-DIAG] event_callback_skipped"), strings.Contains(logs, "[FREEWILL-DIAG] event_callback_rejected"), strings.Contains(logs, "[FREEWILL-DIAG] event_callback_deferred"), strings.Contains(logs, "[FREEWILL-DIAG] event_callback_returned"), strings.Contains(logs, "event wake ignored"), strings.Contains(logs, "event-triggered wake deferred"), strings.Contains(logs, "recovery scan failed"), mapperMetadata)
	}()
	healthCtx, healthCancel := context.WithTimeout(ctx, 30*time.Second)
	defer healthCancel()
	if !e27WaitHealth(healthCtx, baseURL, done) {
		t.Fatal("standalone serve health endpoint did not become ready")
	}
	if !e57VerifyServeAgent(ctx, baseURL, "e57-synthetic-api-token", agentID) {
		t.Fatal("health endpoint did not identify the isolated E57 serve child")
	}
	t.Logf("E57_API_META health_ready=true unique_agent_identity_matched=true api_port=%d", port+1)

	echo := e57SubscribeNATSEcho(t, nc, eventKind)
	probeEvent := event.NewEvent(eventKind, "e57-synthetic-test", map[string]any{"purpose": "synthetic source-action grounding acceptance"})
	probeBytes, err := json.Marshal(probeEvent)
	if err != nil {
		t.Fatal("encode synthetic NATS event")
	}
	dbPath := filepath.Join(dataDir, "freewill.db")
	publish := func() {
		t.Helper()
		time.Sleep(250 * time.Millisecond)
		if err := nc.Publish(eventKind, probeBytes); err != nil {
			t.Fatal("publish synthetic Freewill event")
		}
		if err := nc.FlushTimeout(2 * time.Second); err != nil {
			t.Fatal("flush synthetic Freewill event")
		}
		t.Logf("E57_NATS_META subject_matches_config=true publish_succeeded=true flush_succeeded=true")
		if !e57WaitForNATSEcho(ctx, echo) {
			t.Fatal("publisher did not observe the event on the embedded NATS broker")
		}
	}
	publish()
	if _, err := e57WaitForAdmission(ctx, dbPath, projectID, eventKind, 30*time.Second); err != nil {
		t.Fatalf("served event was not durably admitted: %v", err)
	}
	observedRun := e57WaitForRunResult(t, ctx, dbPath, projectID, eventKind, 1, 240*time.Second)
	run := observedRun.Run
	feedbackObserved := observedRun.RecoveryFeedbackObserved
	wakes := 1
	if run.Freewill != nil && run.Freewill.Lifecycle == freewill.StateSelecting && run.Freewill.SelectionNextAt.After(time.Now().UTC()) {
		// A schema/basis/action correction is a separate recovery attempt. Wait
		// across a UTC-minute boundary, then replay the same host-valid event ID;
		// RecoverPending runs before HandleEvent deduplicates that event ID.
		nextMinute := time.Now().UTC().Truncate(time.Minute).Add(time.Minute).Add(100 * time.Millisecond)
		for time.Now().UTC().Before(nextMinute) {
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for next recovery window")
			case <-time.After(100 * time.Millisecond):
			}
		}
		publish()
		wakes++
		if _, err := e57WaitForAdmission(ctx, dbPath, projectID, eventKind, 30*time.Second); err != nil {
			t.Fatalf("replayed event was not durably admitted: %v", err)
		}
		observedRun = e57WaitForRunResult(t, ctx, dbPath, projectID, eventKind, 2, 240*time.Second)
		run = observedRun.Run
		feedbackObserved = feedbackObserved || observedRun.RecoveryFeedbackObserved
	}
	if wakes > 2 {
		t.Fatal("synthetic wake budget exceeded")
	}
	if cmd.ProcessState == nil {
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal("stop standalone serve after bounded probe")
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("standalone serve did not stop after bounded probe")
		}
	}

	if run.Freewill == nil {
		t.Fatal("served run omitted Freewill state")
	}
	status := "unassessed"
	selectedActionBound := false
	choiceMade := run.Freewill.ChoiceID != ""
	basisBound, uncertaintyPresent, sourceManifestValid := false, false, false
	if run.Freewill.Lifecycle == freewill.StateApprovalPending || run.Freewill.Lifecycle == freewill.StateProposed {
		if len(run.Freewill.Candidates) == 3 && choiceMade {
			for _, candidate := range run.Freewill.Candidates {
				if candidate.CandidateID != run.Freewill.ChoiceID {
					continue
				}
				basisBound = len(candidate.BasisRefs) == 1 && candidate.BasisRefs[0] == "project:readme"
				uncertaintyPresent = strings.TrimSpace(candidate.Uncertainty) != ""
				selectedActionBound = candidate.ProjectGrounding == "roadmap_action" && candidate.ProjectActionID == actionID &&
					candidate.Request.Title == actionText && candidate.Request.Goal == actionText
			}
		}
		manifest := run.Freewill.ProjectContextManifest
		sourceManifestValid = manifest != nil && manifest.Validate() == nil
		if selectedActionBound && basisBound && uncertaintyPresent && sourceManifestValid {
			status = "host_action_bound_pending_approval"
		} else {
			status = "proposal_pending_but_host_binding_incomplete"
		}
	} else if run.Freewill.Lifecycle == freewill.StateSleeping {
		status = "safe_defer_or_exploration_sleep"
	} else if (run.Freewill.Lifecycle == freewill.StateSelecting || run.Freewill.Lifecycle == freewill.StateHalted) && run.Freewill.SelectionFailureCode != "" {
		status = "selection_failed_after_bounded_attempts"
	}
	assertE51NoApprovalOrEffects(t, ctx, dbPath, run.Freewill.Lifecycle == freewill.StateApprovalPending || run.Freewill.Lifecycle == freewill.StateProposed)
	selectionRetries := int(run.Freewill.SelectionRecoveryNumber)
	if wakes > 2 || selectionRetries > 2 {
		t.Fatal("bounded Cloud attempt limit exceeded")
	}
	retryMetadata := e57SelectionRetryMetadata(ctx, dbPath, run.State.RunID)
	mapperMetadata := strings.Join(e57SafeMapperDiagnosticSummary(serveOutput.String()), ";")
	if mapperMetadata == "" {
		mapperMetadata = "none"
	}
	finalClass, finalCode := e57SafeMetadataCode(run.Freewill.SelectionFailureClass), e57SafeMetadataCode(run.Freewill.SelectionFailureCode)
	t.Logf("E57_META model=%s status=%s wakes=%d recovery_attempts=%d failure_class=%s failure_code=%s recovery_feedback_observed=%t retry_metadata=%s mapper_diagnostics=%s candidates=%d choice=%t host_action_bound=%t basis_bound=%t uncertainty_present=%t source_manifest_valid=%t effects=zero cloud_call_upper_bound=%d raw_output_recorded=false",
		model, status, wakes, selectionRetries, finalClass, finalCode, feedbackObserved, retryMetadata, mapperMetadata, len(run.Freewill.Candidates), choiceMade, selectedActionBound, basisBound, uncertaintyPresent, sourceManifestValid, wakes*2)
}

func e57MinimalProcessEnvironment(workDir string) []string {
	allowed := map[string]bool{}
	for _, key := range []string{"PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "TMPDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
		allowed[strings.ToUpper(key)] = true
	}
	env := make([]string, 0, len(allowed)+3)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[strings.ToUpper(key)] {
			env = append(env, entry)
		}
	}
	env = append(env, "TEMP="+workDir, "TMP="+workDir, "TMPDIR="+workDir)
	return env
}

func e57BuildProcessEnvironment(workDir string) ([]string, error) {
	env := e57MinimalProcessEnvironment(workDir)
	for _, key := range []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOROOT"} {
		value, err := exec.Command("go", "env", key).Output()
		if err != nil || strings.TrimSpace(string(value)) == "" {
			return nil, fmt.Errorf("resolve Go build environment variable %s", key)
		}
		env = append(env, key+"="+strings.TrimSpace(string(value)))
	}
	return env, nil
}
func e57MinimalServeEnvironment(workDir, apiKey string) []string {
	env := e57MinimalProcessEnvironment(workDir)
	return append(env, "OLLAMA_API_KEY="+apiKey, "PRIZM_FREEWILL_OLLAMA_CLOUD_DIRECT=1", "PRIZM_FREEWILL_EVENT_DIAGNOSTICS=1")
}

type e57ObservedRun struct {
	Run                      multiagent.DurableRun
	RecoveryFeedbackObserved bool
}

func e57WaitForRunResult(t *testing.T, ctx context.Context, dbPath, projectID, eventKind string, minimumRecoveryCount int, timeout time.Duration) e57ObservedRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var observed string
	feedbackObserved := false
	for time.Now().Before(deadline) {
		store, err := multiagent.OpenExistingSQLiteDurableRunStoreReadOnly(dbPath)
		if err == nil {
			runs, listErr := store.ListFreewillRuns(ctx, "", 100)
			_ = store.Close()
			if listErr == nil {
				for _, run := range runs {
					if run.Freewill == nil || run.Freewill.Scope.ProjectID != projectID || run.Freewill.Trigger != "event:"+eventKind {
						continue
					}
					observed = fmt.Sprintf("run=%s state=%s attempt=%d recovery=%d failure_class=%s failure_code=%s", shortE57ID(run.State.RunID), run.Freewill.Lifecycle, run.Freewill.SelectionAttempt, run.Freewill.SelectionRecoveryNumber, run.Freewill.SelectionFailureClass, run.Freewill.SelectionFailureCode)
					if run.Freewill.SelectionRecoveryNumber > 0 && strings.TrimSpace(run.Freewill.SelectionFeedback) != "" {
						feedbackObserved = true
					}
					if run.Freewill != nil {
						finished := run.Freewill.Lifecycle == freewill.StateApprovalPending || run.Freewill.Lifecycle == freewill.StateProposed || run.Freewill.Lifecycle == freewill.StateSleeping || run.Freewill.Lifecycle == freewill.StateHalted
						retryStateReached := minimumRecoveryCount > 0 && run.Freewill.Lifecycle == freewill.StateSelecting && run.Freewill.SelectionRecoveryNumber >= minimumRecoveryCount
						if finished || retryStateReached {
							return e57ObservedRun{Run: run, RecoveryFeedbackObserved: feedbackObserved}
						}
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for served Freewill decision; %s", observed)
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for served Freewill decision; %s", observed)
	return e57ObservedRun{RecoveryFeedbackObserved: feedbackObserved}
}

func e57WaitForAdmission(ctx context.Context, dbPath, projectID, eventKind string, timeout time.Duration) (multiagent.DurableRun, error) {
	deadline := time.Now().Add(timeout)
	var observed []string
	for time.Now().Before(deadline) {
		store, err := multiagent.OpenExistingSQLiteDurableRunStoreReadOnly(dbPath)
		if err == nil {
			runs, listErr := store.ListFreewillRuns(ctx, "", 100)
			_ = store.Close()
			if listErr == nil {
				observed = observed[:0]
				for _, run := range runs {
					if run.Freewill == nil {
						observed = append(observed, "id="+shortE57ID(run.State.RunID)+" kind=non_freewill")
						continue
					}
					observed = append(observed, fmt.Sprintf("id=%s project=%s trigger=%s state=%s", shortE57ID(run.State.RunID), run.Freewill.Scope.ProjectID, run.Freewill.Trigger, run.Freewill.Lifecycle))
					if run.Freewill.Scope.ProjectID == projectID && run.Freewill.Trigger == "event:"+eventKind {
						return run, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return multiagent.DurableRun{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return multiagent.DurableRun{}, fmt.Errorf("admission deadline elapsed; durable_runs=%d metadata=%v", len(observed), observed)
}

// e57SelectionRetryMetadata reads only the allowlisted retry classifiers and
// counters from durable events. Event IDs, run IDs, timestamps, and payload
// text are deliberately omitted from its output.
func e57SelectionRetryMetadata(ctx context.Context, dbPath, runID string) string {
	store, err := event.OpenExistingSQLiteEventStoreReadOnly(dbPath)
	if err != nil {
		return "unavailable"
	}
	defer store.Close()
	events, err := store.Query(ctx, event.EventFilter{RunID: runID, Type: event.EventFreewillSelectionRetryScheduled, Limit: 16})
	if err != nil {
		return "unavailable"
	}
	parts := make([]string, 0, len(events))
	for _, evt := range events {
		p := evt.Payload
		attempt, attemptOK := e57SafeEventInt(p["attempt"])
		recovery, recoveryOK := e57SafeEventInt(p["recovery_count"])
		class, classOK := p["failure_class"].(string)
		code, codeOK := p["failure_code"].(string)
		if !attemptOK || !recoveryOK || !classOK || !codeOK {
			continue
		}
		parts = append(parts, fmt.Sprintf("attempt=%d,recovery=%d,class=%s,code=%s", attempt, recovery, e57SafeMetadataCode(class), e57SafeMetadataCode(code)))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ";")
}

func e57SafeEventInt(value any) (int64, bool) {
	switch n := value.(type) {
	case float64:
		if n >= 0 && n <= 1000 && n == float64(int64(n)) {
			return int64(n), true
		}
	case int:
		if n >= 0 && n <= 1000 {
			return int64(n), true
		}
	case int64:
		if n >= 0 && n <= 1000 {
			return n, true
		}
	case json.Number:
		parsed, err := n.Int64()
		if err == nil && parsed >= 0 && parsed <= 1000 {
			return parsed, true
		}
	}
	return 0, false
}

func e57SafeMetadataCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 96 {
		return "none"
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return "invalid"
		}
	}
	return value
}

// e57SafeMapperDiagnosticSummary parses only fixed numeric/boolean fields and
// safe parser enums from mapper diagnostics. It never returns raw log lines.
func e57SafeMapperDiagnosticSummary(logText string) []string {
	allow := map[string]bool{
		"stage": true, "parser_code": true, "content_len": true, "think_len": true,
		"tool_calls": true, "idea_markers": true, "parsed_blocks": true,
		"json_valid": true, "starts_object": true, "fenced": true,
		"candidates_key": true, "idea_key": true, "glm_plain": true,
		"json_shape": true,
	}
	var summaries []string
	for _, line := range strings.Split(logText, "\n") {
		const marker = "[FREEWILL] mapper diagnostic "
		at := strings.Index(line, marker)
		if at < 0 {
			continue
		}
		fields := make(map[string]string)
		for _, item := range strings.Fields(line[at+len(marker):]) {
			key, value, ok := strings.Cut(item, "=")
			if ok && allow[key] {
				fields[key] = value
			}
		}
		stage := fields["stage"]
		if stage != "idea" && stage != "choice" {
			continue
		}
		code := e57SafeMetadataCode(fields["parser_code"])
		parts := []string{"stage=" + stage, "parser_code=" + code}
		for _, key := range []string{"content_len", "think_len", "tool_calls", "idea_markers", "parsed_blocks"} {
			n, err := strconv.Atoi(fields[key])
			if err == nil && n >= 0 && n <= 10000000 {
				parts = append(parts, fmt.Sprintf("%s=%d", key, n))
			}
		}
		for _, key := range []string{"json_valid", "starts_object", "fenced", "candidates_key", "idea_key", "glm_plain"} {
			if fields[key] == "true" || fields[key] == "false" {
				parts = append(parts, key+"="+fields[key])
			}
		}
		if shape := e57SafeJSONShapeMetadata(fields["json_shape"]); shape != "" {
			parts = append(parts, "json_shape={"+shape+"}")
		}
		summaries = append(summaries, strings.Join(parts, ","))
	}
	return summaries
}

func e57SafeJSONShapeMetadata(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(strings.TrimSuffix(value, "}"), "{")
	if value == "" {
		return ""
	}
	validName := func(name string) bool {
		if name == "" || len(name) > 40 {
			return false
		}
		for i, r := range name {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9' || i == 0) && r != '_' {
				return false
			}
		}
		return true
	}
	var top, candidate, unknownTop, unknownCandidate string
	objects := -1
	authority := ""
	for _, field := range strings.Split(value, ";") {
		key, item, ok := strings.Cut(field, "=")
		if !ok {
			return ""
		}
		switch key {
		case "top_keys":
			top = item
		case "candidate_keys":
			candidate = item
		case "unknown_top_keys":
			unknownTop = item
		case "unknown_candidate_keys":
			unknownCandidate = item
		case "authority_key":
			if item != "true" && item != "false" {
				return ""
			}
			authority = item
		case "candidate_objects":
			parsed, err := strconv.Atoi(item)
			if err != nil || parsed < 0 || parsed > 3 {
				return ""
			}
			objects = parsed
		default:
			return ""
		}
	}
	if objects < 0 || authority == "" {
		return ""
	}
	for _, group := range []string{top, candidate, unknownTop, unknownCandidate} {
		for _, name := range strings.Split(group, "+") {
			if name == "" || strings.HasSuffix(name, "_more") {
				continue
			}
			if !validName(name) {
				return ""
			}
		}
	}
	return fmt.Sprintf("top_keys=%s;candidate_objects=%d;candidate_keys=%s;unknown_top_keys=%s;unknown_candidate_keys=%s;authority_key=%s", top, objects, candidate, unknownTop, unknownCandidate, authority)
}

func TestE57SafeFailureDiagnosticSummaries(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "events.db")
	store, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal("create synthetic event store")
	}
	retry := event.NewEvent(event.EventFreewillSelectionRetryScheduled, "synthetic-test", map[string]any{
		"run_id": "synthetic-run", "wake_id": "private-wake-id", "attempt": 2, "recovery_count": 1,
		"max_recoveries": 2, "failure_class": "schema", "failure_code": "invalid_project_basis",
		"next_at": "synthetic-time", "private_text": "DO_NOT_LEAK_THIS_TEXT",
	})
	retry.Metadata.RunID = "synthetic-run"
	if err := store.Store(ctx, retry); err != nil {
		t.Fatal("store synthetic retry event")
	}
	if err := store.Close(); err != nil {
		t.Fatal("close synthetic event store")
	}
	retrySummary := e57SelectionRetryMetadata(ctx, dbPath, "synthetic-run")
	if !strings.Contains(retrySummary, "attempt=2,recovery=1,class=schema,code=invalid_project_basis") ||
		strings.Contains(retrySummary, "private-wake-id") || strings.Contains(retrySummary, "DO_NOT_LEAK_THIS_TEXT") {
		t.Fatalf("durable retry summary did not preserve only safe fields: %s", retrySummary)
	}
	mapperSummary := strings.Join(e57SafeMapperDiagnosticSummary("2026 [FREEWILL] mapper diagnostic stage=idea model=private-model content_len=42 think_len=0 tool_calls=0 idea_markers=3 parsed_blocks=1 json_valid=false starts_object=false fenced=false candidates_key=false idea_key=false parser_code=invalid_project_basis glm_plain=true raw=DO_NOT_LEAK_THIS_TEXT"), "")
	if !strings.Contains(mapperSummary, "idea_markers=3") || !strings.Contains(mapperSummary, "parser_code=invalid_project_basis") ||
		strings.Contains(mapperSummary, "private-model") || strings.Contains(mapperSummary, "DO_NOT_LEAK_THIS_TEXT") {
		t.Fatalf("mapper summary did not preserve only safe fields: %s", mapperSummary)
	}
	shapeSummary := strings.Join(e57SafeMapperDiagnosticSummary("[FREEWILL] mapper diagnostic stage=idea content_len=42 parser_code=invalid_scored_ideas_json_unknown_field json_shape={top_keys=analysis+candidates+sleep;candidate_objects=1;candidate_keys=idea+score+title;unknown_top_keys=analysis;unknown_candidate_keys=title;authority_key=false}"), "")
	if !strings.Contains(shapeSummary, "unknown_top_keys=analysis") || !strings.Contains(shapeSummary, "unknown_candidate_keys=title") || !strings.Contains(shapeSummary, "authority_key=false") || strings.Contains(shapeSummary, "DO_NOT_LEAK") {
		t.Fatalf("mapper summary did not preserve only field-name shape metadata: %s", shapeSummary)
	}
}

func shortE57ID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func e57VerifyServeAgent(ctx context.Context, baseURL, authToken, expectedAgentID string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/agents", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var agents []struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(resp.Body).Decode(&agents) != nil {
		return false
	}
	for _, agent := range agents {
		if agent.ID == expectedAgentID {
			return true
		}
	}
	return false
}

func e57SubscribeNATSEcho(t *testing.T, nc *nats.Conn, subject string) <-chan struct{} {
	t.Helper()
	seen := make(chan struct{}, 1)
	sub, err := nc.Subscribe(subject, func(*nats.Msg) {
		select {
		case seen <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal("subscribe publisher-side NATS echo")
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		t.Fatal("flush publisher-side NATS echo subscription")
	}
	return seen
}

func e57WaitForNATSEcho(ctx context.Context, seen <-chan struct{}) bool {
	select {
	case <-seen:
		return true
	case <-ctx.Done():
		return false
	case <-time.After(2 * time.Second):
		return false
	}
}

type e57LockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *e57LockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *e57LockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func e57WaitForLogMarker(ctx context.Context, output *e57LockedBuffer, marker string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), marker) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
	return strings.Contains(output.String(), marker)
}

func TestE57SyntheticCloudContextRequiresExactHostBinding(t *testing.T) {
	const model, projectID = "glm-5.3-flash:cloud", "e57-synthetic-sample-project"
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	if !freewillSelectorIsRemote("ollama", model, "http://127.0.0.1:11434") {
		t.Fatal("exact Cloud model was not classified as remote")
	}
	cases := []struct {
		name, projectBinding, repoBinding string
		wantEgress                        bool
	}{
		{name: "unbound", wantEgress: false},
		{name: "wrong project", projectBinding: "other-project", repoBinding: workspace, wantEgress: false},
		{name: "wrong repository", projectBinding: projectID, repoBinding: otherWorkspace, wantEgress: false},
		{name: "exact host binding", projectBinding: projectID, repoBinding: workspace, wantEgress: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := freewillSelectorIsRemote("ollama", model, "http://127.0.0.1:11434") &&
				freewillRemoteContextBindingMatches(projectID, workspace, tc.projectBinding, tc.repoBinding)
			if got != tc.wantEgress {
				t.Fatalf("synthetic context egress=%t, want %t", got, tc.wantEgress)
			}
		})
	}
}

func TestE57StandaloneFakeProviderEventAdmission(t *testing.T) {
	e57RunStandaloneEventAdmission(t, false)
}

func TestE57StandaloneDirectRouteDeniedEgressEvent(t *testing.T) {
	e57RunStandaloneEventAdmission(t, true)
}

func e57RunStandaloneEventAdmission(t *testing.T, directCloud bool) {
	t.Helper()
	const model = "glm-5.3-flash:cloud"
	const actionID = "project:roadmap:M2"
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	repo, buildMeta := e57RepositoryBuildRoot(t)
	work := t.TempDir()
	dataDir, runsDir := filepath.Join(work, "data"), filepath.Join(work, "runs")
	workspace, policyDir := filepath.Join(work, "workspace"), filepath.Join(work, "policies")
	for _, dir := range []string{dataDir, runsDir, workspace, policyDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal("create fake acceptance directory")
		}
	}
	const actionText = "Improve event scheduling and public listings"
	readme := "# Synthetic Community Event Service\n\n" +
		"A fictional service that helps local organizers publish events and coordinate venue schedules.\n\n" +
		"## Milestone Roadmap\n\n| Milestone | Scope | Status |\n| --- | --- | --- |\n| M2 | " + actionText + " | In progress |\n"
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(readme), 0600); err != nil {
		t.Fatal("write synthetic README")
	}
	const projectID, agentID = "e57-synthetic-event-service", "e57-synthetic-agent"
	profileURL, profileCommit := "https://github.com/example/e57-synthetic-event-service", strings.Repeat("7", 40)
	profileFacts := freewill.ProjectContextProfile{
		Identity:     freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: projectID, SourceIDs: []string{"github:profile"}},
		Purpose:      freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Help local organizers publish events and coordinate venues.", SourceIDs: []string{"github:profile"}},
		Domain:       freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Community event scheduling and public listings.", SourceIDs: []string{"github:profile"}},
		Users:        freewill.ProjectContextFact{Status: freewill.ProjectContextFactInferred, Value: "Local organizers and venue managers.", SourceIDs: []string{"github:profile"}},
		Architecture: freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown},
		Capabilities: freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Create event listings and coordinate venue schedules.", SourceIDs: []string{"github:profile"}},
		Goals:        freewill.ProjectContextFact{Status: freewill.ProjectContextFactObserved, Value: "Make event schedules easier to publish and review.", SourceIDs: []string{"github:profile"}},
		Exclusions:   freewill.ProjectContextFact{Status: freewill.ProjectContextFactInferred, Value: "Household appliance repair is outside scope.", SourceIDs: []string{"github:profile"}},
		History:      freewill.ProjectContextFact{Status: freewill.ProjectContextFactUnknown},
		Claims: []freewill.ProjectContextClaim{
			{ID: "synthetic-event-listing-capability", Status: freewill.ProjectContextFactObserved, Value: "The fictional service supports public event listings.", SourceIDs: []string{"github:profile"}},
			{ID: "synthetic-schedule-capability", Status: freewill.ProjectContextFactObserved, Value: "The fictional service supports venue schedules.", SourceIDs: []string{"github:profile"}},
		},
	}
	profileBody, err := json.Marshal(struct {
		ProjectID     string                         `json:"project_id"`
		RepositoryURL string                         `json:"repository_url"`
		CommitSHA     string                         `json:"commit_sha"`
		Facts         freewill.ProjectContextProfile `json:"facts"`
	}{projectID, profileURL, profileCommit, profileFacts})
	if err != nil {
		t.Fatal("encode synthetic profile")
	}
	profile := freewill.ImportedProjectProfile{Version: 1, ProjectID: projectID, RepositoryURL: profileURL, CommitSHA: profileCommit, ProfileDigest: e57SHA256Hex(profileBody), Facts: profileFacts}
	profileBytes, err := json.Marshal(profile)
	if err != nil {
		t.Fatal("encode imported profile")
	}
	profilePath := filepath.Join(workspace, ".codex", "private-context", projectID+".json")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0700); err != nil {
		t.Fatal("create profile directory")
	}
	if err := os.WriteFile(profilePath, profileBytes, 0600); err != nil {
		t.Fatal("write imported profile")
	}
	policy := "policies:\n  - id: e57-owner-review-required\n    description: Synthetic proposal-only policy.\n    match:\n      action: intention.propose\n      resource.type: intention\n      resource.name: freewill_candidate\n    decision: requires_approval\n    reason: Owner review is required for this synthetic proposal.\n"
	if err := os.WriteFile(filepath.Join(policyDir, "e57.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal("write synthetic policy")
	}
	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			http.Error(w, "invalid fake provider route", http.StatusBadRequest)
			return
		}
		var request struct {
			Model    string          `json:"model"`
			Tools    json.RawMessage `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != model || len(request.Tools) != 0 || len(request.Messages) != 2 {
			http.Error(w, "invalid fake selector request", http.StatusBadRequest)
			return
		}
		content := e56IdeaResponse(false, actionID)
		if call == 2 {
			content = "choice: NONE\nreason: No candidate fits this synthetic objective."
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": request.Model, "done": true, "message": map[string]any{"role": "assistant", "content": content}})
	}))
	defer fake.Close()
	busURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal("start isolated NATS")
	}
	defer stopBus()
	nc, err := nats.Connect(busURL)
	if err != nil {
		t.Fatal("connect isolated NATS")
	}
	defer nc.Close()
	const eventKind = "prizm.e57.grounding_probe"
	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.InstanceID, cfg.Prizm.DataDir, cfg.Prizm.RunsDir = "e57-fake-serve", dataDir, runsDir
	cfg.Prizm.Workspace, cfg.Prizm.ReadRoots, cfg.Prizm.OllamaURL = workspace, []string{workspace}, fake.URL
	cfg.Prizm.BindHost, cfg.Prizm.NATSURL = "127.0.0.1", busURL
	cfg.Prizm.ContextCompression = &agent.CompressionConfig{Enabled: false}
	cfg.API.AuthToken, cfg.API.AuthTokenEnv = "e57-fake-token", ""
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: agentID, ProjectID: projectID, Model: model, PolicyDir: policyDir,
		ProjectProfileRepositoryURL: profileURL, ProjectProfileCommitSHA: profileCommit,
		Objective:  "Review the synthetic project's roadmap action for improving event schedules and public listings.",
		Scheduling: orchestrator.FreewillSchedulingConfig{EventKinds: []string{eventKind}}}
	cfg.Agents = []orchestrator.AgentConfig{{ID: agentID, Role: "researcher", Provider: "ollama", Model: model, Primary: true}}
	projectCfg := orchestrator.ProjectConfig{ID: projectID, RepoPath: workspace}
	if !directCloud {
		projectCfg.FreewillRemoteContextProjectID = projectID
		projectCfg.FreewillRemoteContextRepoPath = workspace
	}
	cfg.Projects = []orchestrator.ProjectConfig{projectCfg}
	cfg.Channels, cfg.Actions, cfg.MCPServers = []orchestrator.ChannelConfig{}, []orchestrator.ActionConfig{}, []orchestrator.MCPServerConfig{}
	cfg.Remembrance.Enabled, cfg.FactoryMonitor.Enabled, cfg.Bridge.Enabled, cfg.Codex.Enabled, cfg.ClaudeCode.Enabled, cfg.Autopatch.Enabled = false, false, false, false, false, false
	configBytes, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal("encode standalone fake config")
	}
	configPath := filepath.Join(work, "prizm.yaml")
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal("write standalone fake config")
	}
	binaryPath := filepath.Join(work, "prizm.exe")
	build := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "./cmd/prizm-cli")
	buildEnv, err := e57BuildProcessEnvironment(work)
	if err != nil {
		t.Fatal(err)
	}
	build.Dir, build.Env = repo, buildEnv
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build standalone CLI failed (output bytes=%d): %v", len(output), err)
	}
	childBinarySHA, err := e57FileSHA256(binaryPath)
	if err != nil {
		t.Fatal("hash standalone CLI binary")
	}
	t.Logf("E57_FAKE_BUILD_META root_id=%s common_dir_id=%s head=%s test_source_sha256=%s child_binary_sha256=%s", buildMeta.rootID, buildMeta.commonDirID, buildMeta.head, buildMeta.testSourceSHA256, childBinarySHA)
	port, err := e27ReservePort()
	if err != nil {
		t.Fatal("reserve fake serve port")
	}
	cmd := exec.Command(binaryPath, "serve", "--config", configPath, "--port", strconv.Itoa(port))
	cmd.Dir = repo
	if directCloud {
		cmd.Env = e57MinimalServeEnvironment(work, "e57-placeholder-no-network-key")
	} else {
		cmd.Env = append(e57MinimalProcessEnvironment(work), "PRIZM_FREEWILL_EVENT_DIAGNOSTICS=1")
	}
	var serveOutput e57LockedBuffer
	cmd.Stdout, cmd.Stderr = &serveOutput, &serveOutput
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal("start standalone fake serve")
	}
	go func() { done <- cmd.Wait() }()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("fake serve child did not stop")
			}
		}
		logs := serveOutput.String()
		subscriptionHash := e57SHA256Hex([]byte(eventKind))
		route := "local_fake"
		if directCloud {
			route = "direct_cloud_placeholder_no_binding"
		}
		t.Logf("E57_FAKE_STARTUP_META pid=%d executable=prizm.exe config_sha256=%s nats_url_matches=%t subscription_config_matches=%t subscription_ready=%t freewill_enabled=true selector_route=%s startup_runtime_line=%t nats_connected_line=%t freewill_start_error=%t callback_entered=%t callback_skipped=%t callback_rejected=%t callback_deferred=%t callback_returned=%t event_wake_ignored=%t event_wake_deferred=%t recovery_scan_failed=%t", cmd.Process.Pid, e57SHA256Hex(configBytes), strings.Contains(logs, "NATS: external at "+busURL), strings.Contains(logs, fmt.Sprintf("[FREEWILL-DIAG] event_subscription_config wake_sources=true scheduling=false count=1 subjects_sha256=%s", subscriptionHash)), strings.Contains(logs, fmt.Sprintf("[FREEWILL-DIAG] event_subscriptions_ready count=1 subjects_sha256=%s", subscriptionHash)), route, strings.Contains(logs, "decision-only runtime enabled"), strings.Contains(logs, "NATS: connected"), strings.Contains(logs, "Error starting Freewill"), strings.Contains(logs, "[FREEWILL-DIAG] event_callback_entered"), strings.Contains(logs, "[FREEWILL-DIAG] event_callback_skipped"), strings.Contains(logs, "[FREEWILL-DIAG] event_callback_rejected"), strings.Contains(logs, "[FREEWILL-DIAG] event_callback_deferred"), strings.Contains(logs, "[FREEWILL-DIAG] event_callback_returned"), strings.Contains(logs, "event wake ignored"), strings.Contains(logs, "event-triggered wake deferred"), strings.Contains(logs, "recovery scan failed"))
	}()
	healthCtx, healthCancel := context.WithTimeout(ctx, 30*time.Second)
	defer healthCancel()
	if !e27WaitHealth(healthCtx, fmt.Sprintf("http://127.0.0.1:%d", port+1), done) {
		t.Fatal("fake standalone serve health did not become ready")
	}
	if !e57VerifyServeAgent(ctx, fmt.Sprintf("http://127.0.0.1:%d", port+1), "e57-fake-token", agentID) {
		t.Fatal("health endpoint did not identify the isolated fake serve child")
	}
	t.Logf("E57_FAKE_API_META health_ready=true unique_agent_identity_matched=true api_port=%d", port+1)
	echo := e57SubscribeNATSEcho(t, nc, eventKind)
	evt := event.NewEvent(eventKind, "e57-fake-test", map[string]any{"purpose": "synthetic event admission"})
	if err := nc.Publish(eventKind, mustMarshalEventForTest(t, evt)); err != nil {
		t.Fatal("publish synthetic event")
	}
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		t.Fatal("flush synthetic event")
	}
	t.Logf("E57_FAKE_NATS_META subject_matches_config=true publish_succeeded=true flush_succeeded=true")
	if !e57WaitForNATSEcho(ctx, echo) {
		t.Fatal("publisher did not observe the event on the embedded NATS broker")
	}
	dbPath := filepath.Join(dataDir, "freewill.db")
	admitted, err := e57WaitForAdmission(ctx, dbPath, projectID, eventKind, 30*time.Second)
	if err != nil {
		t.Fatalf("fake event was not durably admitted: %v", err)
	}
	t.Logf("E57_FAKE_ADMISSION_META project=%s trigger=%s lifecycle=%s run=%s provider_calls=%d", admitted.Freewill.Scope.ProjectID, admitted.Freewill.Trigger, admitted.Freewill.Lifecycle, shortE57ID(admitted.State.RunID), calls.Load())
	callDeadline := time.Now().Add(10 * time.Second)
	for !directCloud && calls.Load() < 2 && time.Now().Before(callDeadline) {
		select {
		case <-ctx.Done():
			t.Fatal("fake provider call wait canceled")
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Logf("E57_FAKE_PROVIDER_META calls_after_admission=%d", calls.Load())
	observedRun := e57WaitForRunResult(t, ctx, dbPath, projectID, eventKind, 1, 30*time.Second)
	run := observedRun.Run
	wantCandidates, wantCalls := 3, int32(2)
	if directCloud {
		wantCandidates, wantCalls = 0, 0
		if run.Freewill == nil || run.Freewill.ProjectContextEgressAllowed {
			t.Fatal("unbound direct-cloud context was not denied by the host egress gate")
		}
	}
	candidateCount := -1
	if run.Freewill != nil {
		candidateCount = len(run.Freewill.Candidates)
	}
	if run.Freewill == nil || run.Freewill.Lifecycle != freewill.StateSleeping || candidateCount != wantCandidates || run.Freewill.ChoiceID != "" {
		t.Fatalf("fake served event did not safely sleep: candidates=%d wanted=%d", candidateCount, wantCandidates)
	}
	if calls.Load() != wantCalls {
		t.Fatalf("local fake provider call count=%d, want %d", calls.Load(), wantCalls)
	}
	if !strings.Contains(serveOutput.String(), fmt.Sprintf("[FREEWILL-DIAG] event_subscription_config wake_sources=true scheduling=false count=1 subjects_sha256=%s", e57SHA256Hex([]byte(eventKind)))) {
		t.Fatal("serve child did not load the expected one-subject event wake config")
	}
	assertE51NoApprovalOrEffects(t, ctx, dbPath, false)
	if !e57WaitForLogMarker(ctx, &serveOutput, "[FREEWILL-DIAG] event_callback_returned result=no_error", 2*time.Second) {
		t.Fatal("fake event callback did not return without error")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal("stop fake standalone serve")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fake standalone serve did not stop")
	}
	readyMarker := fmt.Sprintf("[FREEWILL-DIAG] event_subscriptions_ready count=1 subjects_sha256=%s", e57SHA256Hex([]byte(eventKind)))
	if !strings.Contains(serveOutput.String(), readyMarker) || !strings.Contains(serveOutput.String(), "[FREEWILL-DIAG] event_callback_entered") {
		t.Fatal("fake event did not traverse the instrumented callback path")
	}
	t.Logf("E57_FAKE_META direct_cloud=%t egress_allowed=%t admission=true lifecycle=%s candidates=%d local_fake_calls=%d effects=zero", directCloud, run.Freewill.ProjectContextEgressAllowed, run.Freewill.Lifecycle, len(run.Freewill.Candidates), calls.Load())
}

type e57BuildMetadata struct {
	rootID, commonDirID, head, testSourceSHA256 string
}

func e57RepositoryBuildRoot(t *testing.T) (string, e57BuildMetadata) {
	t.Helper()
	_, sourcePath, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root, err := e57SourceRootFromRuntimePath(sourcePath)
	if err != nil {
		t.Fatal("resolve validated source-derived build root")
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal("source-derived build root has no go.mod")
	}
	topCmd := exec.Command("git", "rev-parse", "--show-toplevel")
	topCmd.Dir = root
	topBytes, err := topCmd.Output()
	if err != nil || !strings.EqualFold(filepath.Clean(strings.TrimSpace(string(topBytes))), root) {
		t.Fatal("source-derived build root does not match Git worktree top level")
	}
	headCmd := exec.Command("git", "rev-parse", "HEAD")
	headCmd.Dir = root
	headBytes, err := headCmd.Output()
	if err != nil {
		t.Fatal("read source worktree HEAD")
	}
	commonCmd := exec.Command("git", "rev-parse", "--git-common-dir")
	commonCmd.Dir = root
	commonBytes, err := commonCmd.Output()
	if err != nil {
		t.Fatal("read source worktree common directory")
	}
	commonDir := strings.TrimSpace(string(commonBytes))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	commonDir, err = filepath.Abs(commonDir)
	if err != nil {
		t.Fatal("resolve source worktree common directory")
	}
	commonDirID := e57SHA256Hex([]byte(strings.ToLower(filepath.Clean(commonDir))))
	sourceSHA, err := e57FileSHA256(sourcePath)
	if err != nil {
		t.Fatal("hash E57 test source")
	}
	return root, e57BuildMetadata{
		rootID: e57SHA256Hex([]byte(strings.ToLower(root))), commonDirID: commonDirID,
		head: strings.TrimSpace(string(headBytes)), testSourceSHA256: sourceSHA,
	}
}

func e57SourceRootFromRuntimePath(sourcePath string) (string, error) {
	if !filepath.IsAbs(sourcePath) {
		return "", fmt.Errorf("runtime source path is not absolute")
	}
	absSourcePath, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve absolute runtime source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(absSourcePath), "..", "..")), nil
}

func TestE57BuildRootRejectsRelativeRuntimeSource(t *testing.T) {
	if _, err := e57SourceRootFromRuntimePath(filepath.Join("cmd", "prizm-cli", "e57_live_glm_action_grounding_serve_test.go")); err == nil {
		t.Fatal("relative runtime source path must fail closed")
	}
}

func e57FileSHA256(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return e57SHA256Hex(content), nil
}

func e57SHA256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return fmt.Sprintf("%x", sum[:])
}
