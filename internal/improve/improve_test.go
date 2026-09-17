package improve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/plan"
)

func TestRecordError_ThreeOccurrences(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	// First two occurrences — no improvement yet
	imp := m.RecordError("map_iteration", "random map key ordering", "tool_loop")
	if imp != nil {
		t.Error("First occurrence should not create improvement")
	}
	imp = m.RecordError("map_iteration", "random map key ordering", "tool_loop")
	if imp != nil {
		t.Error("Second occurrence should not create improvement")
	}

	// Third occurrence — creates improvement
	imp = m.RecordError("map_iteration", "random map key ordering", "tool_loop")
	if imp == nil {
		t.Fatal("Third occurrence should create improvement")
	}
	if imp.Category != CategoryErrorPattern {
		t.Errorf("Category = %q, want error_pattern", imp.Category)
	}
	if imp.Priority != 2 {
		t.Errorf("Priority = %d, want 2", imp.Priority)
	}
	if imp.Status != StatusProposed {
		t.Errorf("Status = %q, want proposed", imp.Status)
	}
	if imp.ID != "IMP-001" {
		t.Errorf("ID = %q, want IMP-001", imp.ID)
	}
}

func TestRecordError_DifferentErrorTypes(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	// Two different errors, each occurring 3 times
	for i := 0; i < 3; i++ {
		m.RecordError("error_a", "message a", "source_a")
		m.RecordError("error_b", "message b", "source_b")
	}

	improvements := m.GetImprovements()
	if len(improvements) != 2 {
		t.Fatalf("expected 2 improvements, got %d", len(improvements))
	}
}

func TestRecordError_UpdatesMessage(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	m.RecordError("test_error", "first message", "source")
	m.RecordError("test_error", "second message", "source")
	m.RecordError("test_error", "third message", "source")

	patterns := m.GetErrorPatterns()
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}
	if patterns[0].Message != "third message" {
		t.Errorf("Message = %q, want 'third message'", patterns[0].Message)
	}
	if patterns[0].Count != 3 {
		t.Errorf("Count = %d, want 3", patterns[0].Count)
	}
}

func TestRecordViolation(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	m.RecordViolation("no_plan_before_code", "Code executed without active plan", "high")
	m.RecordViolation("scope_drift", "Task expanded beyond original scope", "medium")

	violations := m.GetViolations()
	if len(violations) != 2 {
		t.Fatalf("expected 2 violations, got %d", len(violations))
	}
	if violations[0].Rule != "no_plan_before_code" {
		t.Errorf("Rule = %q, want no_plan_before_code", violations[0].Rule)
	}
	if violations[0].Severity != "high" {
		t.Errorf("Severity = %q, want high", violations[0].Severity)
	}
}

func TestGetActiveImprovements(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	// Create improvement via error pattern
	m.RecordError("test_error", "msg", "source")
	m.RecordError("test_error", "msg", "source")
	m.RecordError("test_error", "msg", "source")

	active := m.GetActiveImprovements()
	if len(active) != 1 {
		t.Fatalf("expected 1 active improvement, got %d", len(active))
	}

	// Dismiss it
	improvements := m.GetImprovements()
	m.DismissImprovement(improvements[0].ID)

	active = m.GetActiveImprovements()
	if len(active) != 0 {
		t.Errorf("expected 0 active improvements after dismissal, got %d", len(active))
	}
}

func TestDismissNonexistentImprovement(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	err := m.DismissImprovement("IMP-999")
	if err == nil {
		t.Error("DismissImprovement should return error for nonexistent ID")
	}
}

func TestShouldAutoPR(t *testing.T) {
	tests := []struct {
		category ImprovementCategory
		priority int
		expected bool
	}{
		{CategoryBugFix, 1, true},
		{CategoryErrorPattern, 2, true},
		{CategoryTestCoverage, 3, true},
		{CategoryDocUpdate, 4, true},
		{CategoryRefactor, 3, true},     // Low priority refactor = auto
		{CategoryRefactor, 2, false},    // High priority refactor = needs approval
		{CategoryProcessFix, 1, false},  // Process fixes always need approval
		{CategoryPerformance, 3, true},  // Low priority perf = auto
		{CategoryPerformance, 1, false}, // Critical perf = needs approval
	}

	for _, tt := range tests {
		imp := Improvement{Category: tt.category, Priority: tt.priority}
		result := ShouldAutoPR(imp)
		if result != tt.expected {
			t.Errorf("ShouldAutoPR(%q, priority=%d) = %v, want %v", tt.category, tt.priority, result, tt.expected)
		}
	}
}

func TestApprovalLevelForImprovement(t *testing.T) {
	tests := []struct {
		category ImprovementCategory
		priority int
		expected plan.ApprovalLevel
	}{
		{CategoryBugFix, 1, plan.ApprovalAuto},
		{CategoryErrorPattern, 2, plan.ApprovalAuto},
		{CategoryTestCoverage, 3, plan.ApprovalAuto},
		{CategoryDocUpdate, 4, plan.ApprovalAuto},
		{CategoryRefactor, 2, plan.ApprovalRequired}, // High priority refactor
		{CategoryRefactor, 3, plan.ApprovalAuto},     // Low priority refactor
		{CategoryProcessFix, 1, plan.ApprovalRequired},
		{CategoryPerformance, 1, plan.ApprovalRequired},
		{CategoryPerformance, 3, plan.ApprovalAuto},
	}

	for _, tt := range tests {
		imp := Improvement{Category: tt.category, Priority: tt.priority}
		result := ApprovalLevelForImprovement(imp)
		if result != tt.expected {
			t.Errorf("ApprovalLevelForImprovement(%q, priority=%d) = %q, want %q", tt.category, tt.priority, result, tt.expected)
		}
	}
}

func TestFormatImprovementsForPrompt(t *testing.T) {
	// Empty
	result := FormatImprovementsForPrompt(nil)
	if result != "No active improvement proposals." {
		t.Errorf("Empty improvements = %q", result)
	}

	// With improvements
	improvements := []Improvement{
		{ID: "IMP-001", Title: "Fix bug", Category: CategoryBugFix, Priority: 2},
	}
	result = FormatImprovementsForPrompt(improvements)
	if len(result) == 0 {
		t.Error("FormatImprovementsForPrompt should not be empty")
	}
}

func TestFormatErrorPatternsForPrompt(t *testing.T) {
	// Empty
	result := FormatErrorPatternsForPrompt(nil)
	if result != "No recurring error patterns detected." {
		t.Errorf("Empty patterns = %q", result)
	}
}

func TestPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	// Create improvement via error pattern
	m.RecordError("persist_test", "message", "source")
	m.RecordError("persist_test", "message", "source")
	m.RecordError("persist_test", "message", "source")

	// Create violation
	m.RecordViolation("test_rule", "test violation", "medium")

	// Verify files exist
	if _, err := os.Stat(filepath.Join(tmpDir, "state", "improvements.json")); os.IsNotExist(err) {
		t.Error("improvements.json should exist after recording")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "state", "violations.json")); os.IsNotExist(err) {
		t.Error("violations.json should exist after recording")
	}

	// Create new manager and load
	m2 := NewManager(tmpDir)
	m2.EnsureDir()
	m2.LoadState()

	improvements := m2.GetImprovements()
	if len(improvements) != 1 {
		t.Fatalf("expected 1 improvement after reload, got %d", len(improvements))
	}
	if improvements[0].ID != "IMP-001" {
		t.Errorf("ID = %q, want IMP-001", improvements[0].ID)
	}

	violations := m2.GetViolations()
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation after reload, got %d", len(violations))
	}
}

func TestLinkPlanToImprovement(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	// Create improvement
	m.RecordError("link_test", "msg", "src")
	m.RecordError("link_test", "msg", "src")
	m.RecordError("link_test", "msg", "src")

	improvements := m.GetImprovements()
	if len(improvements) == 0 {
		t.Fatal("expected at least one improvement")
	}

	// Link plan
	err := m.LinkPlanToImprovement(improvements[0].ID, "P-042")
	if err != nil {
		t.Fatalf("LinkPlanToImprovement failed: %v", err)
	}

	improvements = m.GetImprovements()
	if improvements[0].PlanID != "P-042" {
		t.Errorf("PlanID = %q, want P-042", improvements[0].PlanID)
	}
}

func TestLinkNonexistentImprovement(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	err := m.LinkPlanToImprovement("IMP-999", "P-001")
	if err == nil {
		t.Error("LinkPlanToImprovement should return error for nonexistent ID")
	}
}
