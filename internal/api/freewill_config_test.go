package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// freewillConfigTestServer builds a server around a minimal prizm.yaml with a
// disabled freewill block and one agent/project pair matching Ema's live shape.
func freewillConfigTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	yaml := `prizm:
  data_dir: .prizm/data
  runs_dir: runs
  scheduler:
    enabled: false
projects:
- id: bassbook
  repo_path: ` + filepath.Join(dir, "repo") + `
agents:
- id: lumi
  role: lead
  provider: ollama
  model: glm-5.3-flash:cloud
freewill:
  enabled: false
  agent_id: lumi
  project_id: bassbook
  model: glm-5.3-flash:cloud
  objective: "Original objective"
  policy_dir: policies
  scheduling:
    enabled: false
    schedule: '*/10 * * * *'
`
	path := filepath.Join(dir, "prizm.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	// repo path must exist for host binding validation.
	if err := os.MkdirAll(filepath.Join(dir, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	return NewServer(Config{Addr: ":0", ConfigPath: path})
}

func TestFreewillConfigDraftDoesNotWrite(t *testing.T) {
	s := freewillConfigTestServer(t)
	rec := httptest.NewRecorder()
	body := `{"enabled":true,"objective":"Draft objective","scheduling":{"enabled":true,"schedule":"*/5 * * * *"}}`
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/config/freewill", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("draft status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Status  string                    `json:"status"`
		DraftID string                    `json:"draft_id"`
		Summary map[string]map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Status != "draft" || payload.DraftID == "" {
		t.Fatalf("payload=%+v", payload)
	}
	if _, ok := payload.Summary["objective"]; !ok {
		t.Fatalf("summary missing objective change: %+v", payload.Summary)
	}
	// The on-disk config must be untouched.
	disk, err := os.ReadFile(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), "Draft objective") {
		t.Fatal("draft mode wrote to disk")
	}
}

func TestFreewillConfigConfirmWritesAndRequiresDraft(t *testing.T) {
	s := freewillConfigTestServer(t)
	// Confirm with no draft id first → 400.
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/config/freewill",
		strings.NewReader(`{"confirm":true,"actor":"rhem"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bare confirm status=%d", rec.Code)
	}

	// Draft, then confirm.
	draft := httptest.NewRecorder()
	s.mux.ServeHTTP(draft, httptest.NewRequest(http.MethodPut, "/api/v1/config/freewill",
		strings.NewReader(`{"enabled":true}`)))
	if draft.Code != http.StatusOK {
		t.Fatalf("draft status=%d body=%s", draft.Code, draft.Body.String())
	}
	var dr struct {
		DraftID string `json:"draft_id"`
	}
	_ = json.Unmarshal(draft.Body.Bytes(), &dr)
	if dr.DraftID == "" {
		t.Fatal("no draft id")
	}

	confirm := httptest.NewRecorder()
	s.mux.ServeHTTP(confirm, httptest.NewRequest(http.MethodPut, "/api/v1/config/freewill",
		strings.NewReader(`{"confirm":true,"draft_id":"`+dr.DraftID+`","actor":"rhem"}`)))
	if confirm.Code != http.StatusOK || !strings.Contains(confirm.Body.String(), `"confirmed_by":"rhem"`) {
		t.Fatalf("confirm status=%d body=%s", confirm.Code, confirm.Body.String())
	}
	disk, err := os.ReadFile(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(disk), "enabled: true") || !strings.Contains(string(disk), "objective:") {
		t.Fatalf("disk after confirm:\n%s", disk)
	}

	// Draft is consumed: replay → 409.
	replay := httptest.NewRecorder()
	s.mux.ServeHTTP(replay, httptest.NewRequest(http.MethodPut, "/api/v1/config/freewill",
		strings.NewReader(`{"confirm":true,"draft_id":"`+dr.DraftID+`","actor":"rhem"}`)))
	if replay.Code != http.StatusConflict {
		t.Fatalf("replay status=%d", replay.Code)
	}
}

func TestFreewillConfigUnknownDraftConflicts(t *testing.T) {
	s := freewillConfigTestServer(t)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/config/freewill",
		strings.NewReader(`{"confirm":true,"draft_id":"fwcfg-doesnotexist","actor":"rhem"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("unknown draft status=%d", rec.Code)
	}
}

func TestFreewillConfigDraftRejectsInvalidBinding(t *testing.T) {
	s := freewillConfigTestServer(t)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/config/freewill",
		strings.NewReader(`{"enabled":true,"agent_id":"nonexistent","project_id":"nonexistent"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid binding draft status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "freewill binding invalid") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestFreewillConfigNoEffectiveChangeRejected(t *testing.T) {
	s := freewillConfigTestServer(t)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/config/freewill",
		strings.NewReader(`{"objective":"Original objective"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no-op status=%d body=%s", rec.Code, rec.Body.String())
	}
}
