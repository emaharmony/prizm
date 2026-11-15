package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"gopkg.in/yaml.v3"
)

const e27SyntheticToken = "e27-local-synthetic-bearer"

func TestE35StandaloneServedPromisePrincipalBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	dataDir, runsDir, workspace := filepath.Join(work, "data"), filepath.Join(work, "runs"), filepath.Join(work, "workspace")
	for _, p := range []string{dataDir, runsDir, workspace} {
		if err = os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	natsURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("embedded local NATS: %v", err)
	}
	defer stopBus()
	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.InstanceID = "e27-promise-fixture"
	cfg.Prizm.DataDir = dataDir
	cfg.Prizm.RunsDir = runsDir
	cfg.Prizm.Workspace = workspace
	cfg.Prizm.BindHost = "127.0.0.1"
	cfg.Prizm.NATSURL = natsURL
	cfg.Prizm.OllamaURL = "http://127.0.0.1:1"
	cfg.Prizm.Scheduler.Enabled = false
	cfg.Prizm.ContextCompression = &agent.CompressionConfig{Enabled: false}
	cfg.API.AuthToken = e27SyntheticToken
	cfg.API.AuthTokenEnv = ""
	ownerToken, recipientToken := "e35-synthetic-owner-token", "e35-synthetic-recipient-token"
	cfg.API.LocalDiaryPrincipalAuth = orchestrator.LocalDiaryPrincipalAuthConfig{Enabled: true, OwnerPrincipalID: "synthetic-owner", Credentials: []orchestrator.LocalDiaryPrincipalCredentialConfig{
		{PrincipalID: "synthetic-owner", TokenSHA256: diaryCredentialDigest(ownerToken)},
		{PrincipalID: "synthetic-recipient", TokenSHA256: diaryCredentialDigest(recipientToken)},
	}}
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: "e27-selector", ProjectID: "e27-project", Model: "unused-local-fake", PolicyDir: filepath.Join(root, "policies"), PromiseConfirmation: orchestrator.FreewillPromiseConfirmationConfig{Enabled: true, OwnerID: "synthetic-owner", PromiseID: "promise-e27", Scope: "review the specified local fixture", Deadline: "2026-11-12T17:00:00Z"}}
	cfg.Freewill.DiaryEnabled = true
	cfg.Freewill.DiaryOwnerID = "synthetic-owner"
	cfg.Freewill.DiaryAnnotationsEnabled = true
	cfg.Freewill.DiaryAnnotationsOwnerID = "synthetic-owner"
	cfg.Agents = []orchestrator.AgentConfig{{ID: "e27-selector", Role: "researcher", Provider: "ollama", Model: "unused-local-fake", Primary: true}}
	cfg.Projects = []orchestrator.ProjectConfig{{ID: "e27-project", RepoPath: workspace, FreewillTerminalFactsProjectID: "e27-project", FreewillTerminalFactsRepoPath: workspace}}
	cfg.Channels = []orchestrator.ChannelConfig{}
	cfg.Actions = []orchestrator.ActionConfig{}
	cfg.MCPServers = []orchestrator.MCPServerConfig{}
	cfg.Remembrance.Enabled = false
	cfg.FactoryMonitor.Enabled = false
	cfg.Bridge.Enabled = false
	cfg.Codex.Enabled = false
	cfg.ClaudeCode.Enabled = false
	cfg.Autopatch.Enabled = false
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
	build.Dir = root
	build.Env = append(os.Environ(), "GOPROXY=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual serve binary: %v: %s", err, strings.TrimSpace(string(out)))
	}
	binaryBytes, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	port, err := e27ReservePort()
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port+1)
	type process struct {
		cmd  *exec.Cmd
		done chan error
	}
	var processes []process
	start := func() process {
		cmd := exec.Command(binaryPath, "serve", "--config", configPath, "--port", strconv.Itoa(port))
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "TEMP="+work, "TMP="+work, "TMPDIR="+work)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start standalone prizm serve: %v", err)
		}
		p := process{cmd: cmd, done: make(chan error, 1)}
		processes = append(processes, p)
		go func() { p.done <- cmd.Wait() }()
		if !e27WaitHealth(ctx, base, p.done) {
			t.Fatalf("serve did not become healthy; process pid %d", cmd.Process.Pid)
		}
		return p
	}
	stop := func(p process) {
		if p.cmd.ProcessState != nil {
			return
		}
		_ = p.cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("serve process %d did not exit", p.cmd.Process.Pid)
		}
	}
	t.Cleanup(func() {
		for _, p := range processes {
			if p.cmd.ProcessState == nil {
				_ = p.cmd.Process.Kill()
				select {
				case <-p.done:
				case <-time.After(10 * time.Second):
					t.Errorf("cleanup failed waiting for serve")
				}
			}
		}
	})
	queryValues := url.Values{"owner_id": {"synthetic-owner"}, "project_id": {"e27-project"}, "workspace": {workspace}, "agent_id": {"e27-selector"}}
	query := queryValues.Encode()
	get := func() []byte {
		return e27APIRequestWithBearer(t, http.MethodGet, base+"/api/v1/freewill/promises/promise-e27?"+query, "", ownerToken)
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
	if json.Unmarshal(initial, &initialView) != nil || initialView.Current.Revision != 1 {
		t.Fatalf("initial seed not served: %s", initial)
	}
	proposalBodyBytes, err := json.Marshal(map[string]any{"owner_id": "synthetic-owner", "project_id": "e27-project", "workspace": workspace, "agent_id": "e27-selector", "request_id": "e27-fake-request-1", "expected_revision": 1, "scope": "review the fixture and summarize the requested files", "deadline": "2026-11-13T17:00:00Z", "impact_hypothesis": "The proposed scope and deadline may change what the user receives."})
	if err != nil {
		t.Fatal(err)
	}
	proposalBody := string(proposalBodyBytes)
	pending := post("/api/v1/freewill/promises/promise-e27/proposals", proposalBody)
	var pendingView struct {
		Current struct {
			Revision int `json:"revision"`
		} `json:"current"`
		Proposals []struct {
			ProposalID string `json:"proposal_id"`
			Proposer   string `json:"proposer"`
			Status     string `json:"status"`
		} `json:"proposals"`
	}
	if json.Unmarshal(pending, &pendingView) != nil || pendingView.Current.Revision != 1 || len(pendingView.Proposals) != 1 || pendingView.Proposals[0].Status != "pending" || pendingView.Proposals[0].Proposer != "local_synthetic_fake" {
		t.Fatalf("pending proposal changed effective promise: %s", pending)
	}
	stop(serveA)
	serveB := start()
	afterRestart := get()
	if string(afterRestart) != string(pending) {
		t.Fatalf("pending history changed across restart: %s != %s", afterRestart, pending)
	}
	decisionBodyBytes, err := json.Marshal(map[string]string{"owner_id": "synthetic-owner", "project_id": "e27-project", "workspace": workspace, "agent_id": "e27-selector", "proposal_id": pendingView.Proposals[0].ProposalID, "decision": "accept"})
	if err != nil {
		t.Fatal(err)
	}
	decisionBody := string(decisionBodyBytes)
	accepted := post("/api/v1/freewill/promises/promise-e27/decisions/"+pendingView.Proposals[0].ProposalID, decisionBody)
	var acceptedView struct {
		Current struct {
			Revision int    `json:"revision"`
			Scope    string `json:"scope"`
		} `json:"current"`
		History []json.RawMessage `json:"history"`
	}
	if json.Unmarshal(accepted, &acceptedView) != nil || acceptedView.Current.Revision != 2 || acceptedView.Current.Scope != "review the fixture and summarize the requested files" || len(acceptedView.History) != 2 {
		t.Fatalf("owner acceptance did not append current revision: %s", accepted)
	}
	stop(serveB)
	serveC := start()
	final := get()
	if string(final) != string(accepted) {
		t.Fatalf("accepted promise history changed across restart: %s != %s", final, accepted)
	}
	// Missing and invalid bearer reads are rejected before the promise reader.
	for _, token := range []string{"", "Bearer wrong"} {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/freewill/promises/promise-e27?"+query, nil)
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bad bearer read status=%d", res.StatusCode)
		}
	}
	for _, test := range []struct {
		name, token, ownerID string
		want                 int
	}{
		{name: "service-wide bearer", token: e27SyntheticToken, ownerID: "synthetic-owner", want: http.StatusUnauthorized},
		{name: "forged owner", token: ownerToken, ownerID: "synthetic-recipient", want: http.StatusForbidden},
		{name: "recipient principal", token: recipientToken, ownerID: "synthetic-owner", want: http.StatusForbidden},
	} {
		values := url.Values{"owner_id": {test.ownerID}, "project_id": {"e27-project"}, "workspace": {workspace}, "agent_id": {"e27-selector"}}
		req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/freewill/promises/promise-e27?"+values.Encode(), nil)
		req.Header.Set("Authorization", "Bearer "+test.token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != test.want {
			t.Fatalf("%s status=%d want=%d", test.name, res.StatusCode, test.want)
		}
	}
	stop(serveC)
	head, tree := e27Git(ctx, root, "rev-parse", "HEAD"), e27Git(ctx, root, "rev-parse", "HEAD^{tree}")
	meta := map[string]any{"schema_version": 1, "acceptance_kind": "local synthetic principal-bound served promise confirmation proof", "source_branch": "codex/e35-principal-owner-routes-20261111", "source_head": head, "source_tree": tree, "binary_sha256": e27Hash(binaryBytes), "config_sha256": e27Hash(cfgBytes), "binary_go_version": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "serve_processes": len(processes), "all_processes_waited": true, "owner_transitions": []string{"promise revision 1 read by principal owner", "proposal appended and persisted pending", "pending proposal reopened after restart", "owner accepted proposal", "accepted revision reopened after restart"}, "service_bearer_status": http.StatusUnauthorized, "forged_owner_status": http.StatusForbidden, "recipient_owner_read_status": http.StatusForbidden, "missing_or_invalid_bearer_status": http.StatusUnauthorized, "proposal_and_decision_text_included": false, "provider_calls": 0, "docker_used": false, "cloud_or_discord_used": false, "real_person_identity_verified": false}
	evidence := filepath.Join(work, "e35-principal-owner-routes-local-20261111.json")
	if err = os.WriteFile(evidence, appendJSON(meta), 0600); err != nil {
		t.Fatalf("write metadata-only evidence: %v", err)
	}
}

func e27APIRequestWithBearer(t *testing.T, method, url, body, token string) []byte {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		t.Fatalf("served %s %s status=%d body=%s", method, url, res.StatusCode, data)
	}
	return data
}
func e27WaitHealth(ctx context.Context, base string, done <-chan error) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-done:
			return false
		case <-ticker.C:
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/status", nil)
			res, err := client.Do(req)
			if err == nil {
				res.Body.Close()
				if res.StatusCode == 200 {
					return true
				}
			}
		}
	}
}
func e27ReservePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	l2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port+1))
	if err != nil {
		return 0, err
	}
	_ = l2.Close()
	return port, nil
}
func e27Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func e27Git(ctx context.Context, root string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	b, err := cmd.Output()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(b))
}
func appendJSON(v any) []byte { b, _ := json.MarshalIndent(v, "", "  "); return append(b, '\n') }
