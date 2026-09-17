package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

// writeRun creates synthetic run artifacts in baseDir.
func writeRun(t *testing.T, baseDir, runID, status string, withReport bool) {
	t.Helper()
	state := `{"run_id":"` + runID + `","status":"` + status + `","started_at":"2026-06-29T10:00:00Z"}`
	if err := os.WriteFile(filepath.Join(baseDir, "workflow-"+runID+".json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	if withReport {
		dir := filepath.Join(baseDir, runID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "REPORT.md"), []byte("# Report for "+runID+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListRunsFromDir(t *testing.T) {
	base := t.TempDir()
	writeRun(t, base, "gl-1", "completed", true)
	writeRun(t, base, "gl-2", "blocked", false)

	entries, err := listRunsFromDir(base)
	if err != nil {
		t.Fatalf("listRunsFromDir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(entries))
	}
	byID := map[string]runEntry{}
	for _, e := range entries {
		byID[e.RunID] = e
	}
	if byID["gl-1"].Status != "completed" || !byID["gl-1"].HasReport {
		t.Fatalf("gl-1 wrong: %+v", byID["gl-1"])
	}
	if byID["gl-2"].Status != "blocked" || byID["gl-2"].HasReport {
		t.Fatalf("gl-2 wrong: %+v", byID["gl-2"])
	}
}

func TestListRunsReportOnlyRun(t *testing.T) {
	base := t.TempDir()
	// A run with only a REPORT.md (no state file) still appears.
	dir := filepath.Join(base, "gl-orphan")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "REPORT.md"), []byte("x"), 0o644)

	entries, err := listRunsFromDir(base)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 1 || entries[0].RunID != "gl-orphan" || !entries[0].HasReport {
		t.Fatalf("report-only run not listed: %+v", entries)
	}
}

func TestRenderRunsList(t *testing.T) {
	out := renderRunsList([]runEntry{
		{RunID: "gl-1", Status: "completed", StartedAt: "2026-06-29T10:00:00Z", HasReport: true},
		{RunID: "gl-2", Status: "blocked"},
	})
	for _, want := range []string{"gated-loop runs", "gl-1", "completed", "📄", "gl-2", "blocked", "2 run(s)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(renderRunsList(nil), "no runs found") {
		t.Fatalf("empty list should say no runs found")
	}
}

func TestReadRunReport(t *testing.T) {
	base := t.TempDir()
	writeRun(t, base, "gl-9", "completed", true)
	content, err := readRunReport(base, "gl-9")
	if err != nil {
		t.Fatalf("readRunReport: %v", err)
	}
	if !strings.Contains(content, "Report for gl-9") {
		t.Fatalf("unexpected report content: %q", content)
	}
	if _, err := readRunReport(base, "gl-missing"); err == nil {
		t.Fatal("expected error for missing report")
	}
}

func TestRunsToJSON(t *testing.T) {
	data, err := runsToJSON([]runEntry{
		{RunID: "gl-1", Status: "completed", StartedAt: "2026-06-29T10:00:00Z", HasReport: true},
	})
	if err != nil {
		t.Fatalf("runsToJSON: %v", err)
	}
	var got []map[string]any
	if err := jsonUnmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got) != 1 || got[0]["run_id"] != "gl-1" || got[0]["status"] != "completed" || got[0]["has_report"] != true {
		t.Fatalf("unexpected JSON: %s", data)
	}
}

func TestFormatRunStateSummary(t *testing.T) {
	st := v2.NewWorkflowState(v2.DefaultConfig())
	st.RunID = "gl-7"
	st.Status = v2.StatusInProgress
	st.AddTokens(1200, 800)
	st.Verification = &v2.VerificationRecord{Profile: "go_test_all", Passed: false, ExitCode: 1, Attempts: 2}
	st.SetPhaseStatus("EXECUTION", v2.PhaseStatusInProgress)
	st.Delegations = []v2.DelegationState{{TaskID: "T2", Agent: "coder", Status: "sent"}}

	out := formatRunStateSummary(st)
	for _, want := range []string{"run gl-7", "in_progress", "2.0k", "go_test_all FAILED", "EXECUTION", "Delegations", "T2", "coder"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestShowRunDetailFallsBackToState(t *testing.T) {
	base := t.TempDir()
	// Run with state file but NO report → summary fallback.
	st := v2.NewWorkflowState(v2.DefaultConfig())
	st.RunID = "gl-8"
	st.Status = v2.StatusBlocked
	if err := v2.SaveWorkflowState(st, base); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := showRunDetail(base, "gl-8")
	if err != nil {
		t.Fatalf("showRunDetail: %v", err)
	}
	if !strings.Contains(out, "run gl-8") || !strings.Contains(out, "blocked") {
		t.Fatalf("expected state summary, got:\n%s", out)
	}
	// Neither report nor state → error.
	if _, err := showRunDetail(base, "gl-missing"); err == nil {
		t.Fatal("expected error when nothing exists")
	}
}

func TestShowRunDetailPrefersReport(t *testing.T) {
	base := t.TempDir()
	writeRun(t, base, "gl-9", "completed", true)
	out, err := showRunDetail(base, "gl-9")
	if err != nil {
		t.Fatalf("showRunDetail: %v", err)
	}
	if !strings.Contains(out, "Report for gl-9") {
		t.Fatalf("expected REPORT.md to win, got:\n%s", out)
	}
}

func TestRunStateToJSON(t *testing.T) {
	st := v2.NewWorkflowState(v2.DefaultConfig())
	st.RunID = "gl-11"
	st.Status = v2.StatusCompleted
	st.AddTokens(500, 300)
	st.Verification = &v2.VerificationRecord{Profile: "go_test_all", Passed: true, ExitCode: 0, Attempts: 1}
	st.SetPhaseStatus("EXECUTION", v2.PhaseStatusCompleted)
	st.SetPhaseGateResult("EXECUTION", v2.GateResult{Passed: true, Score: 0.9})
	st.Delegations = []v2.DelegationState{{TaskID: "T1", Agent: "coder", Status: "completed"}}

	data, err := runStateToJSON(st)
	if err != nil {
		t.Fatalf("runStateToJSON: %v", err)
	}
	var got map[string]any
	if err := jsonUnmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["run_id"] != "gl-11" || got["status"] != "completed" {
		t.Fatalf("header wrong: %v", got)
	}
	if got["prompt_tokens"].(float64) != 500 || got["completion_tokens"].(float64) != 300 {
		t.Fatalf("tokens wrong: %v", got)
	}
	v, _ := got["verification"].(map[string]any)
	if v == nil || v["profile"] != "go_test_all" || v["passed"] != true {
		t.Fatalf("verification wrong: %v", got["verification"])
	}
	phases, _ := got["phases"].([]any)
	if len(phases) == 0 {
		t.Fatalf("expected phases in JSON")
	}
	dels, _ := got["delegations"].([]any)
	if len(dels) != 1 {
		t.Fatalf("expected one delegation, got %v", got["delegations"])
	}
}

func TestLatestRunID(t *testing.T) {
	base := t.TempDir()
	// Two runs; bump the second's report mtime so it is newest.
	writeRun(t, base, "gl-old", "completed", true)
	writeRun(t, base, "gl-new", "completed", true)
	newReport := filepath.Join(base, "gl-new", "REPORT.md")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(newReport, future, future); err != nil {
		t.Fatal(err)
	}
	id, err := latestRunID(base)
	if err != nil {
		t.Fatalf("latestRunID: %v", err)
	}
	if id != "gl-new" {
		t.Fatalf("expected newest run gl-new, got %q", id)
	}
	// Empty dir → error.
	if _, err := latestRunID(t.TempDir()); err == nil {
		t.Fatal("expected error for empty dir")
	}
}
