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

const sampleConfig = `# top comment
prizm:
  instance_id: "astraea"   # inline
  port: 8321
  scheduler:
    enabled: false
    jobs: []

autopatch:
  enabled: false
  mode: "propose"

# remembrance section
remembrance:
  enabled: true
  url: "http://localhost:18790"
`

func newConfigTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "prizm.yaml")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewServer(Config{
		Addr:       ":0",
		ConfigPath: path,
		SchedulerActions: []SchedulerAction{
			{Key: "status_report", SkipLLM: true},
			{Key: "auto_patch", SkipLLM: false},
		},
	})
	return s, path
}

func TestConfig_Get(t *testing.T) {
	s, _ := newConfigTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body)
	}
	var resp configResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Settings.InstanceID != "astraea" {
		t.Errorf("instance_id = %q", resp.Settings.InstanceID)
	}
	if !resp.Settings.RemembranceEnabled {
		t.Errorf("remembrance should be enabled")
	}
}

func TestConfig_Scheduler_ValidWritePreservesComments(t *testing.T) {
	s, path := newConfigTestServer(t)
	body := `{"enabled":true,"jobs":[{"name":"status-report","schedule":"0 */2 * * *","action":"status_report","enabled":true}]}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config/scheduler", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body)
	}
	out, _ := os.ReadFile(path)
	so := string(out)
	for _, want := range []string{"# top comment", "# remembrance section", "status-report", "0 */2 * * *", "action: status_report"} {
		if !strings.Contains(so, want) {
			t.Errorf("written config missing %q:\n%s", want, so)
		}
	}
}

func TestConfig_Scheduler_BadCronRejectedNoWrite(t *testing.T) {
	s, path := newConfigTestServer(t)
	before, _ := os.ReadFile(path)
	body := `{"enabled":true,"jobs":[{"name":"x","schedule":"not a cron","action":"status_report","enabled":true}]}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config/scheduler", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("file was modified despite invalid cron")
	}
}

func TestConfig_Scheduler_DuplicateNameRejected(t *testing.T) {
	s, _ := newConfigTestServer(t)
	body := `{"enabled":true,"jobs":[{"name":"dup","schedule":"* * * * *","enabled":true},{"name":"dup","schedule":"* * * * *","enabled":true}]}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config/scheduler", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate name, got %d", w.Code)
	}
}

func TestConfig_Settings_SurgicalEdit(t *testing.T) {
	s, path := newConfigTestServer(t)
	body := `{"autopatch_enabled":true,"autopatch_mode":"pr"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config/settings", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body)
	}
	out := string(mustRead(t, path))
	if !strings.Contains(out, "mode: pr") && !strings.Contains(out, `mode: "pr"`) {
		t.Errorf("autopatch mode not updated:\n%s", out)
	}
	// Unrelated sections preserved.
	if !strings.Contains(out, "# top comment") || !strings.Contains(out, "http://localhost:18790") {
		t.Errorf("unrelated config lost:\n%s", out)
	}
}

func TestConfig_Settings_InvalidModeRejected(t *testing.T) {
	s, _ := newConfigTestServer(t)
	body := `{"autopatch_mode":"nonsense"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config/settings", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad mode, got %d", w.Code)
	}
}

func TestConfig_CronValidate(t *testing.T) {
	s, _ := newConfigTestServer(t)
	cases := map[string]bool{
		`{"schedule":"0 */2 * * *"}`: true,
		`{"schedule":"bogus"}`:       false,
	}
	for body, wantValid := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/cron/validate", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)
		var resp map[string]any
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp["valid"] != wantValid {
			t.Errorf("%s → valid=%v, want %v", body, resp["valid"], wantValid)
		}
	}
}

func TestConfig_Actions(t *testing.T) {
	s, _ := newConfigTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/actions", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Actions []SchedulerAction `json:"actions"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Actions) != 2 || resp.Actions[0].Key != "auto_patch" {
		t.Errorf("expected sorted actions, got %+v", resp.Actions)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
