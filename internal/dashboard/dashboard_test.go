package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/safety"
)

func TestNewServer(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(":0", dir, "policies")
	if s == nil {
		t.Fatal("NewServer() returned nil")
	}
	if s.runDir != dir {
		t.Errorf("runDir = %q, want %q", s.runDir, dir)
	}
}

func TestHandleRuns(t *testing.T) {
	// Create a run directory with a summary
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run_test123")
	os.MkdirAll(runDir, 0755)

	summary := map[string]any{
		"run_id": "run_test123",
		"status": "completed",
		"task":   "hello world",
		"agent":  "lumi",
	}
	data, _ := json.Marshal(summary)
	os.WriteFile(filepath.Join(runDir, "summary.json"), data, 0644)

	s := NewServer(":0", dir, "policies")
	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var runs []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs count = %d, want 1", len(runs))
	}
	if runs[0]["run_id"] != "run_test123" {
		t.Errorf("run_id = %v, want run_test123", runs[0]["run_id"])
	}
}

func TestHandleRunDetail(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run_test456")
	os.MkdirAll(runDir, 0755)

	summary := map[string]any{"run_id": "run_test456", "status": "completed"}
	data, _ := json.Marshal(summary)
	os.WriteFile(filepath.Join(runDir, "summary.json"), data, 0644)

	s := NewServer(":0", dir, "policies")
	req := httptest.NewRequest(http.MethodGet, "/api/runs/run_test456", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var detail map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if detail["run_id"] != "run_test456" {
		t.Errorf("run_id = %v, want run_test456", detail["run_id"])
	}
}

func TestHandleRunDetail_NotFound(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(":0", dir, "policies")
	req := httptest.NewRequest(http.MethodGet, "/api/runs/nonexistent", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandleIndexHTML(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(":0", dir, "policies")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if body == "" {
		t.Error("index.html response is empty")
	}
	// Check that it contains "Prizm overview"
	if !contains(body, "Prizm overview") {
		t.Error("index.html does not contain 'Prizm Dashboard'")
	}
}

func TestFreewillDashboardExposesSeparateBoundChildPatchReview(t *testing.T) {
	page, err := staticFS.ReadFile("static/freewill.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(page)
	for _, contract := range []string{
		"Intention approval", "Child patch approval", "execution_status === 'accepted'",
		"child_status === 'paused'", "/children/${encodeURIComponent(parent.child_run_id)}/approvals",
		"sandbox_binding", "artifact_digest", "patch_sha256", "Exact proposed diff",
		"data-child-decision=\"grant\"", "data-child-decision=\"deny\"",
		"reason: reason.trim()", "ui.escape(approval.patch || '')",
	} {
		if !strings.Contains(body, contract) {
			t.Errorf("Freewill dashboard is missing child patch review contract %q", contract)
		}
	}
}

func TestFreewillDashboardExposesTerminalExecutionOutcomes(t *testing.T) {
	page, err := staticFS.ReadFile("static/freewill.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(page)
	for _, contract := range []string{
		"Execution outcomes", "execution_status", "child_status", "validation_summary",
		"execution_failure_code", "execution_report_sha256", "selection_failure_code", "selection_next_at",
		"approval_status === 'denied'", "Load child report",
		"/multiagent/runs/${encodeURIComponent(childRunID)}/report",
		"report.artifacts", "Changed files:", "Validation checks:", "Full terminal report",
		"ui.escape(JSON.stringify(report, null, 2))",
	} {
		if !strings.Contains(body, contract) {
			t.Errorf("Freewill dashboard is missing terminal outcome contract %q", contract)
		}
	}
}

func TestOverviewUsesLiveAPIAndSharedDashboardShell(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(":0", dir, "policies")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	body := w.Body.String()
	for _, want := range []string{"Prizm overview", "/app.js", "/app.css", "Operational summary", "Immediate attention"} {
		if !contains(body, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}

func TestV2DashboardLinksBackToOriginalDashboard(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(":0", dir, "policies")
	req := httptest.NewRequest(http.MethodGet, "/v2.html", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if !contains(w.Body.String(), `href="/index.html">← Original dashboard`) {
		t.Error("v2.html is missing the original-dashboard return link")
	}
}

func TestSanitizePath(t *testing.T) {
	// Normal path
	tmpDir := t.TempDir()
	path, err := safety.ResolveAndContain(tmpDir, "run_123")
	if err != nil {
		t.Errorf("normal path error: %v", err)
	}
	if path == "" {
		t.Error("normal path returned empty")
	}

	// Path traversal
	_, err = safety.ResolveAndContain(tmpDir, "../../etc/passwd")
	if err == nil {
		t.Error("path traversal should return error")
	}
}

func TestRunIDFromPath(t *testing.T) {
	tests := []struct {
		path   string
		prefix string
		want   string
	}{
		{"/api/runs/run_123", "/api/runs/", "run_123"},
		{"/api/runs/run_123/", "/api/runs/", "run_123"},
		{"/api/events/run_456", "/api/events/", "run_456"},
	}

	for _, tt := range tests {
		got := runIDFromPath(tt.path, tt.prefix)
		if got != tt.want {
			t.Errorf("runIDFromPath(%q, %q) = %q, want %q", tt.path, tt.prefix, got, tt.want)
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
