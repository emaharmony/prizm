package guard

import (
	"testing"

	"github.com/emaharmony/prizm/internal/plan"
)

func TestCheckToolExecution_ReadTools(t *testing.T) {
	tmpDir := t.TempDir()
	pm := plan.NewManager(tmpDir)
	pm.EnsureDir()
	g := NewGuard(pm, nil)

	readTools := []string{"read_project", "search_files", "project_overview", "git_status", "git_log", "git_diff", "git_branch_list"}
	for _, tool := range readTools {
		result := g.CheckToolExecution(tool, nil)
		if result.Decision != Proceed {
			t.Errorf("CheckToolExecution(%q) = %q, want Proceed", tool, result.Decision)
		}
	}
}

func TestCheckToolExecution_MutationBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	pm := plan.NewManager(tmpDir)
	pm.EnsureDir()
	g := NewGuard(pm, nil)

	// No plan exists — mutation should be blocked
	mutationTools := []string{"write_file", "write_file_proposal", "create_directory", "create_directory_proposal", "edit_file", "git_add", "git_commit", "git_push", "shell_exec"}
	for _, tool := range mutationTools {
		result := g.CheckToolExecution(tool, nil)
		if result.Decision != Block {
			t.Errorf("CheckToolExecution(%q) without plan = %q, want Block", tool, result.Decision)
		}
	}
}

func TestCheckToolExecution_MutationWithPlan(t *testing.T) {
	tmpDir := t.TempDir()
	pm := plan.NewManager(tmpDir)
	pm.EnsureDir()
	g := NewGuard(pm, nil)

	// Create an auto-proceed plan
	pm.CreatePlan(plan.Plan{
		Title: "Fix bug",
		Scope: "Small fix only",
	})

	// Mutation tools should now proceed
	result := g.CheckToolExecution("write_file", nil)
	if result.Decision != Proceed {
		t.Errorf("CheckToolExecution(write_file) with auto-proceed plan = %q, want Proceed", result.Decision)
	}
	if result.PlanID != "P-001" {
		t.Errorf("PlanID = %q, want P-001", result.PlanID)
	}
}

func TestCheckToolExecution_MutationWithPendingPlan(t *testing.T) {
	tmpDir := t.TempDir()
	pm := plan.NewManager(tmpDir)
	pm.EnsureDir()
	g := NewGuard(pm, nil)

	// Create a plan that requires approval
	pm.CreatePlan(plan.Plan{
		Title:         "Refactor architecture",
		Scope:         "Major changes",
		ApprovalLevel: plan.ApprovalRequired,
		Status:        plan.StatusDraft, // Not yet approved
	})

	// Mutation tools should be blocked
	result := g.CheckToolExecution("write_file", nil)
	if result.Decision != Block {
		t.Errorf("CheckToolExecution(write_file) with pending plan = %q, want Block", result.Decision)
	}
}

func TestCheckToolExecution_MutationWithApprovedPlan(t *testing.T) {
	tmpDir := t.TempDir()
	pm := plan.NewManager(tmpDir)
	pm.EnsureDir()
	g := NewGuard(pm, nil)

	// Create and approve a plan
	pm.CreatePlan(plan.Plan{
		Title:         "Feature work",
		Scope:         "New feature",
		ApprovalLevel: plan.ApprovalRequired,
		Status:        plan.StatusPendingApproval,
	})
	pm.ApprovePlan("P-001", "ema")

	// Mutation tools should proceed
	result := g.CheckToolExecution("write_file", nil)
	if result.Decision != Proceed {
		t.Errorf("CheckToolExecution(write_file) with approved plan = %q, want Proceed", result.Decision)
	}
}

func TestCheckToolExecution_UnknownTool(t *testing.T) {
	tmpDir := t.TempDir()
	pm := plan.NewManager(tmpDir)
	pm.EnsureDir()
	g := NewGuard(pm, nil)

	// Unknown tool — not classified as mutation — should proceed with warning
	result := g.CheckToolExecution("custom_tool", nil)
	if result.Decision != Proceed {
		t.Errorf("CheckToolExecution(custom_tool) = %q, want Proceed", result.Decision)
	}
}

func TestCheckMessageIntent(t *testing.T) {
	tests := []struct {
		message  string
		expected bool
	}{
		// Code intent
		{"Implement the new feature", true},
		{"Fix the bug in tool loop", true},
		{"Create a new file for this", true},
		{"Write a test for this", true},
		{"Add validation logic", true},
		{"Build the guard rail package", true},
		{"Deploy the new version", true},
		// Read intent
		{"What is the current status?", false},
		{"How does this work?", false},
		{"Show me the file", false},
		{"Explain the architecture", false},
		{"List the open PRs", false},
		{"Read the config file", false},
		// Mixed — code intent wins
		{"Check the status and fix the bug", true},
		{"Review the code and update the function", true},
		// Neither
		{"Hello, how are you?", false},
		{"Thanks for the help", false},
	}

	for _, tt := range tests {
		result := CheckMessageIntent(tt.message)
		if result != tt.expected {
			t.Errorf("CheckMessageIntent(%q) = %v, want %v", tt.message, result, tt.expected)
		}
	}
}

func TestFormatGuardResult(t *testing.T) {
	tests := []struct {
		result   CheckResult
		contains string
	}{
		{CheckResult{Decision: Proceed, Reason: "read-only"}, "✅"},
		{CheckResult{Decision: Proceed, PlanID: "P-001", ApprovalLevel: plan.ApprovalAuto}, "P-001"},
		{CheckResult{Decision: Block, Reason: "no active plan"}, "⛔"},
		{CheckResult{Decision: Block, Reason: "pending", PlanID: "P-002"}, "P-002"},
		{CheckResult{Decision: Warn, Warning: "plans unavailable"}, "⚠️"},
		{CheckResult{Decision: Defer}, "🔄"},
	}

	for _, tt := range tests {
		result := FormatGuardResult(tt.result)
		if !containsStr(result, tt.contains) {
			t.Errorf("FormatGuardResult(%+v) = %q, want to contain %q", tt.result, result, tt.contains)
		}
	}
}

func TestFormatPlansSummary(t *testing.T) {
	// No plans
	summary := FormatPlansSummary(nil)
	if !containsStr(summary, "No active plan") {
		t.Errorf("FormatPlansSummary(nil) = %q, want 'No active plan'", summary)
	}

	// With active plan
	plans := []plan.Plan{
		{ID: "P-001", Title: "Feature", Status: plan.StatusAutoProceed, ApprovalLevel: plan.ApprovalAuto, Reasoning: "Bug fix", Scope: "Small"},
	}
	summary = FormatPlansSummary(plans)
	if !containsStr(summary, "P-001") {
		t.Errorf("FormatPlansSummary should contain P-001, got %q", summary)
	}
	if !containsStr(summary, "auto_proceed") {
		t.Errorf("FormatPlansSummary should contain status, got %q", summary)
	}
}

func TestIsMutationTool(t *testing.T) {
	if !IsMutationTool("write_file") {
		t.Error("write_file should be a mutation tool")
	}
	if !IsMutationTool("create_directory") {
		t.Error("create_directory should be a mutation tool")
	}
	if !IsMutationTool("git_commit") {
		t.Error("git_commit should be a mutation tool")
	}
	if IsMutationTool("read_project") {
		t.Error("read_project should not be a mutation tool")
	}
}

func TestIsReadTool(t *testing.T) {
	if !IsReadTool("read_project") {
		t.Error("read_project should be a read tool")
	}
	if !IsReadTool("git_status") {
		t.Error("git_status should be a read tool")
	}
	if IsReadTool("write_file") {
		t.Error("write_file should not be a read tool")
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
