package main

import (
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
)

func TestFormatPRList_Empty(t *testing.T) {
	wh := &WakeHandler{}
	got := wh.formatPRList("[]")
	// An empty PR list returns "" so handleDirectAction skips sending a message.
	if got != "" {
		t.Errorf("expected empty string for no open PRs, got %q", got)
	}
}

func TestFormatPRList_SinglePR(t *testing.T) {
	wh := &WakeHandler{}
	input := `[{
		"number": 42,
		"title": "Add caching layer",
		"author": {"login": "sarah-chen"},
		"updatedAt": "2026-06-07T14:23:00Z",
		"reviewDecision": "APPROVED",
		"statusCheckRollup": [
			{"state": "success", "name": "ci/lint"},
			{"state": "success", "name": "ci/test"}
		],
		"labels": [{"name": "enhancement"}]
	}]`

	got := wh.formatPRList(input)
	if got == "" {
		t.Fatal("expected non-empty output")
	}
	// Should contain PR number, title, author, review status
	checks := []string{"#42", "Add caching layer", "sarah-chen", "Approved"}
	for _, check := range checks {
		if !contains(got, check) {
			t.Errorf("expected output to contain %q, got: %s", check, got)
		}
	}
}

func TestFormatPRList_MultiplePRs(t *testing.T) {
	wh := &WakeHandler{}
	input := `[
		{
			"number": 42,
			"title": "Add caching",
			"author": {"login": "alice"},
			"updatedAt": "2026-06-07T14:23:00Z",
			"reviewDecision": "APPROVED",
			"statusCheckRollup": [{"state": "success", "name": "ci/test"}],
			"labels": [{"name": "enhancement"}]
		},
		{
			"number": 39,
			"title": "Fix timezone bug",
			"author": {"login": "bob"},
			"updatedAt": "2026-05-28T09:15:00Z",
			"reviewDecision": "CHANGES_REQUESTED",
			"statusCheckRollup": [{"state": "failure", "name": "ci/test"}],
			"labels": [{"name": "bug"}]
		}
	]`

	got := wh.formatPRList(input)
	if got == "" {
		t.Fatal("expected non-empty output")
	}
	if !contains(got, "2 open PRs") {
		t.Errorf("expected '2 open PRs' in output, got: %s", got)
	}
	if !contains(got, "Changes Requested") {
		t.Errorf("expected 'Changes Requested' in output, got: %s", got)
	}
}

func TestFormatPRList_FailingCI(t *testing.T) {
	wh := &WakeHandler{}
	input := `[{
		"number": 39,
		"title": "Fix bug",
		"author": {"login": "dev"},
		"updatedAt": "2026-05-28T09:15:00Z",
		"reviewDecision": "CHANGES_REQUESTED",
		"statusCheckRollup": [
			{"state": "failure", "name": "ci/test"},
			{"state": "success", "name": "ci/lint"}
		],
		"labels": [{"name": "bug"}, {"name": "priority-high"}]
	}]`

	got := wh.formatPRList(input)
	if !contains(got, "Failing") {
		t.Errorf("expected 'Failing' CI status, got: %s", got)
	}
	if !contains(got, "bug") || !contains(got, "priority-high") {
		t.Errorf("expected labels in output, got: %s", got)
	}
}

func TestFormatPRList_InvalidJSON(t *testing.T) {
	wh := &WakeHandler{}
	got := wh.formatPRList("not json at all")
	if !contains(got, "PR Status:") {
		t.Errorf("expected fallback output for invalid JSON, got: %s", got)
	}
}

func TestFormatPRList_ReviewRequired(t *testing.T) {
	wh := &WakeHandler{}
	input := `[{
		"number": 37,
		"title": "Update docs",
		"author": {"login": "alex-w"},
		"updatedAt": "2026-05-15T11:42:00Z",
		"reviewDecision": "REVIEW_REQUIRED",
		"statusCheckRollup": [{"state": "success", "name": "ci/lint"}],
		"labels": []
	}]`

	got := wh.formatPRList(input)
	if !contains(got, "Review Required") {
		t.Errorf("expected 'Review Required' in output, got: %s", got)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestLoadWorkflowConfigAppliesProjectTokenBudget(t *testing.T) {
	wh := &WakeHandler{cfg: orchestrator.DefaultConfig()}
	cfg := wh.loadWorkflowConfig(&orchestrator.ProjectConfig{ID: "example", TokenBudget: 12345})
	if cfg.Global.MaxTotalTokens != 12345 {
		t.Fatalf("MaxTotalTokens = %d, want 12345", cfg.Global.MaxTotalTokens)
	}
}

func TestLoadWorkflowConfigAppliesUnlimitedProjectTokenBudget(t *testing.T) {
	wh := &WakeHandler{cfg: orchestrator.DefaultConfig()}
	cfg := wh.loadWorkflowConfig(&orchestrator.ProjectConfig{ID: "example", TokenBudget: -1})
	if cfg.Global.MaxTotalTokens != -1 {
		t.Fatalf("MaxTotalTokens = %d, want -1", cfg.Global.MaxTotalTokens)
	}
}

func TestWakeTokenBudgetExceededRespectsUnlimited(t *testing.T) {
	if wakeTokenBudgetExceeded(10_000_000, -1) {
		t.Fatal("unlimited token ceiling should not trip")
	}
	if !wakeTokenBudgetExceeded(100, 100) {
		t.Fatal("positive token ceiling should trip at the cap")
	}
}
