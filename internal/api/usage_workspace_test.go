package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/usage"
)

// sampleConfigYAML is a valid prizm.yaml with comments and two agents, used to
// verify surgical per-agent edits preserve comments and untouched agents.
const sampleConfigYAML = `# Prizm config — top comment must survive edits.
prizm:
  instance_id: test
  workspace: ./ws
agents:
  # First agent — keep this comment.
  - id: astraea
    role: orchestrator
    provider: ollama
    model: glm-5.1:cloud
    conversation_postfix: "Old postfix."
    context: [soul, identity]
  - id: eddie
    role: coder
    provider: ollama
    model: deepseek-v4:cloud
`

func newEndpointTestServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	dir := t.TempDir()

	// Usage store with a couple of recorded events.
	us, err := usage.NewStore(filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatalf("usage store: %v", err)
	}
	t.Cleanup(func() { us.Close() })
	_ = us.Record(usage.Event{Agent: "astraea", Provider: "anthropic", Model: "claude-sonnet-4-20250514", Source: "invoke", PromptTokens: 100, CompletionTokens: 50, CostUSD: 0.05})
	_ = us.Record(usage.Event{Agent: "eddie", Provider: "openai", Model: "gpt-4o", Source: "chat", PromptTokens: 20, CompletionTokens: 10, CostUSD: 0.01})

	// Workspace dir.
	ws := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("# Roster\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	// Config file.
	cfgPath := filepath.Join(dir, "prizm.yaml")
	if err := os.WriteFile(cfgPath, []byte(sampleConfigYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	s := NewServer(Config{
		Addr:       ":0",
		Usage:      us,
		Workspace:  ws,
		ConfigPath: cfgPath,
	})
	return s, ws, cfgPath
}

func TestHandleUsage(t *testing.T) {
	s, _, _ := newEndpointTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage?range=lifetime", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Range  string `json:"range"`
		Totals struct {
			Total int `json:"total"`
			Count int `json:"count"`
		} `json:"totals"`
		ByAgent []struct {
			Key   string `json:"key"`
			Total int    `json:"total"`
		} `json:"by_agent"`
		Series []map[string]any `json:"series"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Totals.Total != 180 { // 150 + 30
		t.Errorf("total = %d, want 180", resp.Totals.Total)
	}
	if resp.Totals.Count != 2 {
		t.Errorf("count = %d, want 2", resp.Totals.Count)
	}
	if len(resp.ByAgent) != 2 {
		t.Errorf("by_agent len = %d, want 2", len(resp.ByAgent))
	}
	if len(resp.Series) == 0 {
		t.Error("expected non-empty series")
	}
}

func TestWorkspaceFileReadWrite(t *testing.T) {
	s, ws, _ := newEndpointTestServer(t)

	// List
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/files", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "AGENTS.md") {
		t.Fatalf("list files failed: %d %s", w.Code, w.Body.String())
	}

	// Write
	body := strings.NewReader(`{"content":"# Roster\n\nNew rules.\n"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/workspace/files/AGENTS.md", body)
	w = httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("write failed: %d %s", w.Code, w.Body.String())
	}
	got, _ := os.ReadFile(filepath.Join(ws, "AGENTS.md"))
	if !strings.Contains(string(got), "New rules.") {
		t.Errorf("file not written: %q", got)
	}

	// Read back
	req = httptest.NewRequest(http.MethodGet, "/api/v1/workspace/files/AGENTS.md", nil)
	w = httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "New rules.") {
		t.Errorf("read back missing content: %s", w.Body.String())
	}
}

func TestWorkspaceFileTraversalRejected(t *testing.T) {
	s, _, _ := newEndpointTestServer(t)

	// A .md name that escapes the workspace root must be rejected.
	req := httptest.NewRequest(http.MethodPut, "/api/v1/workspace/files/..%2f..%2fevil.md",
		strings.NewReader(`{"content":"x"}`))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("traversal: status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}

	// A non-.md file must be rejected too.
	req = httptest.NewRequest(http.MethodPut, "/api/v1/workspace/files/secrets.env",
		strings.NewReader(`{"content":"x"}`))
	w = httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("non-md: status = %d, want 400", w.Code)
	}
}

func TestConfigAgentSurgicalEdit(t *testing.T) {
	s, _, cfgPath := newEndpointTestServer(t)

	// List agents.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/agents", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "astraea") {
		t.Fatalf("list agents failed: %d %s", w.Code, w.Body.String())
	}

	// Edit astraea's postfix + context + a state action.
	patch := `{"conversation_postfix":"New behavior.","context":["soul","agents","user"],"state_actions":{"manager-room":"Be terse."}}`
	req = httptest.NewRequest(http.MethodPut, "/api/v1/config/agents/astraea", strings.NewReader(patch))
	w = httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("edit agent failed: %d %s", w.Code, w.Body.String())
	}

	raw, _ := os.ReadFile(cfgPath)
	text := string(raw)
	// Comments and the untouched agent must survive.
	if !strings.Contains(text, "top comment must survive") {
		t.Error("top comment lost")
	}
	if !strings.Contains(text, "keep this comment") {
		t.Error("agent comment lost")
	}
	if !strings.Contains(text, "eddie") || !strings.Contains(text, "deepseek-v4:cloud") {
		t.Error("untouched agent eddie was damaged")
	}
	if !strings.Contains(text, "New behavior.") {
		t.Error("postfix not written")
	}

	// The edited config must still load, with the new values.
	cfg, err := orchestrator.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("reload edited config: %v", err)
	}
	var astraea *orchestrator.AgentConfig
	for i := range cfg.Agents {
		if cfg.Agents[i].ID == "astraea" {
			astraea = &cfg.Agents[i]
		}
	}
	if astraea == nil {
		t.Fatal("astraea missing after edit")
	}
	if astraea.ConversationPostfix != "New behavior." {
		t.Errorf("postfix = %q", astraea.ConversationPostfix)
	}
	if len(astraea.Context) != 3 || astraea.Context[1] != "agents" {
		t.Errorf("context = %v, want [soul agents user]", astraea.Context)
	}
	if sa, ok := astraea.StateActions["manager-room"]; !ok || sa.Inject != "Be terse." {
		t.Errorf("state_actions = %+v", astraea.StateActions)
	}
}
