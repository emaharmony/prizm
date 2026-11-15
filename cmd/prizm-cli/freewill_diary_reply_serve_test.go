package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func TestE36StandaloneDiaryShareReplyPrincipalBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
	seed := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE34SeedStandaloneDiaryFixture$")
	seed.Dir = root
	seed.Env = append(os.Environ(), "E34_STANDALONE_SEED_DIR="+dataDir)
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed existing synthetic diary fixture: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var fixture struct {
		EntryID     string `json:"entry_id"`
		WorkspaceID string `json:"workspace_id"`
		ProjectID   string `json:"project_id"`
		Workspace   string `json:"workspace"`
		AgentID     string `json:"agent_id"`
		IntentionID string `json:"intention_id"`
		ParentRunID string `json:"parent_run_id"`
	}
	fixtureBytes, err := os.ReadFile(filepath.Join(dataDir, "e34-fixture-metadata.json"))
	if err != nil || json.Unmarshal(fixtureBytes, &fixture) != nil || fixture.EntryID == "" {
		t.Fatalf("read seeded fixture metadata: err=%v data=%s", err, fixtureBytes)
	}
	natsURL, stopBus, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("embedded local NATS: %v", err)
	}
	defer stopBus()
	cfg := orchestrator.DefaultConfig()
	cfg.Prizm.InstanceID = "e36-diary-reply-fixture"
	cfg.Prizm.DataDir, cfg.Prizm.RunsDir, cfg.Prizm.Workspace = dataDir, runsDir, workspace
	cfg.Prizm.BindHost, cfg.Prizm.NATSURL, cfg.Prizm.OllamaURL = "127.0.0.1", natsURL, "http://127.0.0.1:1"
	cfg.Prizm.Scheduler.Enabled = false
	cfg.Prizm.ContextCompression = &agent.CompressionConfig{Enabled: false}
	cfg.API.AuthToken, cfg.API.AuthTokenEnv = e27SyntheticToken, ""
	ownerToken, recipientToken := "e36-synthetic-owner-token", "e36-synthetic-recipient-token"
	const ownerID, recipientID = "synthetic-owner-a", "synthetic-recipient-b"
	cfg.API.LocalDiaryPrincipalAuth = orchestrator.LocalDiaryPrincipalAuthConfig{Enabled: true, OwnerPrincipalID: ownerID, Credentials: []orchestrator.LocalDiaryPrincipalCredentialConfig{
		{PrincipalID: ownerID, TokenSHA256: diaryCredentialDigest(ownerToken)},
		{PrincipalID: recipientID, TokenSHA256: diaryCredentialDigest(recipientToken)},
	}}
	cfg.Freewill = orchestrator.FreewillConfig{Enabled: true, AgentID: fixture.AgentID, ProjectID: fixture.ProjectID, Model: "unused-local-fake", PolicyDir: filepath.Join(root, "policies")}
	cfg.Freewill.DiaryEnabled, cfg.Freewill.DiaryOwnerID = true, ownerID
	cfg.Freewill.DiaryAnnotationsEnabled, cfg.Freewill.DiaryAnnotationsOwnerID = true, ownerID
	cfg.Agents = []orchestrator.AgentConfig{{ID: fixture.AgentID, Role: "researcher", Provider: "ollama", Model: "unused-local-fake", Primary: true}}
	cfg.Projects = []orchestrator.ProjectConfig{{ID: fixture.ProjectID, RepoPath: fixture.Workspace, FreewillTerminalFactsProjectID: fixture.ProjectID, FreewillTerminalFactsRepoPath: fixture.Workspace}}
	cfg.Channels, cfg.Actions, cfg.MCPServers = []orchestrator.ChannelConfig{}, []orchestrator.ActionConfig{}, []orchestrator.MCPServerConfig{}
	cfg.Remembrance.Enabled, cfg.FactoryMonitor.Enabled, cfg.Bridge.Enabled, cfg.Codex.Enabled, cfg.ClaudeCode.Enabled, cfg.Autopatch.Enabled = false, false, false, false, false, false
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
	build.Dir, build.Env = root, append(os.Environ(), "GOPROXY=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build prizm serve binary: %v: %s", err, strings.TrimSpace(string(out)))
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
		cmd.Dir, cmd.Env = root, append(os.Environ(), "TEMP="+work, "TMP="+work, "TMPDIR="+work)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start serve: %v", err)
		}
		p := process{cmd: cmd, done: make(chan error, 1)}
		processes = append(processes, p)
		go func() { p.done <- cmd.Wait() }()
		if !e27WaitHealth(ctx, base, p.done) {
			t.Fatalf("serve did not become healthy (pid %d)", cmd.Process.Pid)
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
					t.Errorf("serve process did not exit")
				}
			}
		}
	})
	request := func(method, path string, body any, token string) (int, []byte) {
		var reader io.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = strings.NewReader(string(encoded))
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, data
	}
	sharePath := "/api/v1/freewill/diary/shares"
	grantBody := map[string]any{"recipient_principal_id": recipientID, "project_id": fixture.ProjectID, "workspace": fixture.Workspace,
		"workspace_id": fixture.WorkspaceID, "agent_id": fixture.AgentID, "intention_id": fixture.IntentionID, "parent_run_id": fixture.ParentRunID,
		"entry_id": fixture.EntryID, "expected_revision": 1, "expires_at": time.Now().Add(time.Hour).UTC()}
	serveA := start()
	status, grantBytes := request(http.MethodPost, sharePath, grantBody, ownerToken)
	if status != http.StatusOK {
		t.Fatalf("create exact frozen grant status=%d body=%s", status, grantBytes)
	}
	var grantResponse struct {
		Grant struct {
			GrantID     string `json:"grant_id"`
			EntryID     string `json:"entry_id"`
			Revision    int    `json:"revision"`
			EntrySHA256 string `json:"entry_sha256"`
			ProjectID   string `json:"project_id"`
			WorkspaceID string `json:"workspace_id"`
			AgentID     string `json:"agent_id"`
			IntentionID string `json:"intention_id"`
			ParentRunID string `json:"parent_run_id"`
		} `json:"grant"`
	}
	if err := json.Unmarshal(grantBytes, &grantResponse); err != nil || grantResponse.Grant.GrantID == "" {
		t.Fatalf("invalid grant response %s err=%v", grantBytes, err)
	}
	grant := grantResponse.Grant
	repliesPath := sharePath + "/" + grant.GrantID + "/replies"
	reply := map[string]any{"request_id": "e36-reply-1", "grant_id": grant.GrantID, "entry_id": grant.EntryID, "revision": grant.Revision,
		"entry_sha256": grant.EntrySHA256, "project_id": grant.ProjectID, "workspace_id": grant.WorkspaceID, "agent_id": grant.AgentID,
		"intention_id": grant.IntentionID, "parent_run_id": grant.ParentRunID, "text": "Synthetic recipient comment; unverified and attributed only."}
	status, replyBytes := request(http.MethodPost, repliesPath, reply, recipientToken)
	if status != http.StatusOK || !strings.Contains(string(replyBytes), "Synthetic recipient comment") || !strings.Contains(string(replyBytes), grant.EntrySHA256) {
		t.Fatalf("append exact grant reply status=%d body=%s", status, replyBytes)
	}
	for _, attempt := range []struct {
		name, token string
		body        any
		want        int
	}{{"replay", recipientToken, reply, http.StatusConflict}, {"owner masquerades as recipient", ownerToken, map[string]any{
		"request_id": "e36-owner-attempt", "grant_id": grant.GrantID, "entry_id": grant.EntryID, "revision": grant.Revision, "entry_sha256": grant.EntrySHA256,
		"project_id": grant.ProjectID, "workspace_id": grant.WorkspaceID, "agent_id": grant.AgentID, "intention_id": grant.IntentionID, "parent_run_id": grant.ParentRunID, "text": "forged owner reply"}, http.StatusConflict},
		{"service bearer", e27SyntheticToken, reply, http.StatusUnauthorized}} {
		got, body := request(http.MethodPost, repliesPath, attempt.body, attempt.token)
		if got != attempt.want {
			t.Fatalf("%s reply status=%d want=%d body=%s", attempt.name, got, attempt.want, body)
		}
	}
	staleReply := map[string]any{}
	for key, value := range reply {
		staleReply[key] = value
	}
	staleReply["request_id"], staleReply["revision"] = "e36-stale-revision", grant.Revision+1
	if got, body := request(http.MethodPost, repliesPath, staleReply, recipientToken); got != http.StatusConflict {
		t.Fatalf("stale revision status=%d body=%s", got, body)
	}
	roleSpoof := map[string]any{}
	for key, value := range reply {
		roleSpoof[key] = value
	}
	roleSpoof["principal_id"], roleSpoof["role"] = ownerID, "owner"
	if got, body := request(http.MethodPost, repliesPath, roleSpoof, recipientToken); got != http.StatusBadRequest {
		t.Fatalf("client role/principal spoof status=%d body=%s", got, body)
	}
	if got, body := request(http.MethodGet, repliesPath, nil, recipientToken); got != http.StatusNotFound {
		t.Fatalf("recipient history read status=%d body=%s", got, body)
	}
	if got, _ := request(http.MethodGet, repliesPath+"?token="+e27SyntheticToken, nil, ownerToken); got != http.StatusUnauthorized {
		t.Fatalf("query token bypass status=%d", got)
	}
	if got, body := request(http.MethodGet, repliesPath, nil, ownerToken); got != http.StatusOK || !strings.Contains(string(body), "Synthetic recipient comment") {
		t.Fatalf("owner history read status=%d body=%s", got, body)
	}
	stop(serveA)
	serveB := start()
	if got, body := request(http.MethodGet, repliesPath, nil, ownerToken); got != http.StatusOK || !strings.Contains(string(body), "Synthetic recipient comment") {
		t.Fatalf("owner reply history after restart status=%d body=%s", got, body)
	}
	if got, body := request(http.MethodDelete, sharePath+"/"+grant.GrantID, nil, ownerToken); got != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", got, body)
	}
	stop(serveB)
	serveC := start()
	if got, body := request(http.MethodGet, sharePath+"/"+grant.GrantID, nil, recipientToken); got != http.StatusNotFound {
		t.Fatalf("recipient read after revoke status=%d body=%s", got, body)
	}
	reply["request_id"] = "e36-reply-after-revoke"
	if got, body := request(http.MethodPost, repliesPath, reply, recipientToken); got != http.StatusConflict {
		t.Fatalf("recipient reply after revoke status=%d body=%s", got, body)
	}
	if got, body := request(http.MethodGet, repliesPath, nil, ownerToken); got != http.StatusOK || !strings.Contains(string(body), "Synthetic recipient comment") {
		t.Fatalf("owner retained history after revoke status=%d body=%s", got, body)
	}
	stop(serveC)
	meta := map[string]any{"schema_version": 1, "acceptance_kind": "E36 local synthetic principal-bound frozen diary reply served proof", "source_branch": "codex/e36-dia07-diary-reply",
		"source_head": e27Git(ctx, root, "rev-parse", "HEAD"), "source_tree": e27Git(ctx, root, "rev-parse", "HEAD^{tree}"), "binary_sha256": e27Hash(mustReadFile(t, binaryPath)),
		"config_sha256": e27Hash(cfgBytes), "binary_go_version": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "serve_processes": len(processes), "all_processes_waited": true,
		"reply_bound_to_exact_grant_revision_digest_scope": true, "recipient_history_read_status": http.StatusNotFound, "owner_history_read_status": http.StatusOK,
		"replay_status": http.StatusConflict, "owner_masquerade_status": http.StatusConflict, "service_bearer_status": http.StatusUnauthorized,
		"recipient_read_after_revoke_status": http.StatusNotFound, "recipient_reply_after_revoke_status": http.StatusConflict, "owner_history_after_revoke_status": http.StatusOK,
		"audit_contains_reply_text": false, "provider_calls": 0, "cloud_or_discord_used": false, "real_person_identity_verified": false, "retention_or_deletion_policy_implemented": false}
	evidence := filepath.Join(t.TempDir(), "e36-diary-share-reply-local-20261111.json")
	if err := os.WriteFile(evidence, appendJSON(meta), 0600); err != nil {
		t.Fatalf("write metadata-only evidence: %v", err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
