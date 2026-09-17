package plan

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNeedsApproval(t *testing.T) {
	tests := []struct {
		title    string
		scope    string
		expected ApprovalLevel
	}{
		{"Fix map iteration bug", "P-007 only", ApprovalAuto},
		{"Add state tools", "State files only", ApprovalAuto},
		{"Update README", "Docs only", ApprovalAuto},
		{"Refactor tool loop", "Core loop rewrite", ApprovalRequired},
		{"Migrate to new event bus", "Replace NATS with Redis", ApprovalRequired},
		{"Remove deprecated API", "Only removal", ApprovalRequired},
		{"Database schema change", "Add foreign keys", ApprovalCritical},
		{"Security fix for auth", "Fix auth bypass", ApprovalCritical},
		{"Deploy to production", "Release v1.0", ApprovalCritical},
	}

	for _, tt := range tests {
		result := NeedsApproval(tt.title, tt.scope)
		if result != tt.expected {
			t.Errorf("NeedsApproval(%q, %q) = %q, want %q", tt.title, tt.scope, result, tt.expected)
		}
	}
}

func TestCanProceed(t *testing.T) {
	tests := []struct {
		status   PlanStatus
		expected bool
	}{
		{StatusDraft, false},
		{StatusPendingApproval, false},
		{StatusApproved, true},
		{StatusAutoProceed, true},
		{StatusRejected, false},
		{StatusCompleted, false},
		{StatusAbandoned, false},
	}

	for _, tt := range tests {
		plan := &Plan{Status: tt.status}
		result := CanProceed(plan)
		if result != tt.expected {
			t.Errorf("CanProceed(%q) = %v, want %v", tt.status, result, tt.expected)
		}
	}

	// Nil plan cannot proceed
	if CanProceed(nil) {
		t.Error("CanProceed(nil) should be false")
	}
}

func TestHasPlan(t *testing.T) {
	plans := []Plan{
		{ID: "P-001", Title: "Completed", Status: StatusCompleted},
		{ID: "P-002", Title: "Abandoned", Status: StatusAbandoned},
	}
	if HasPlan(plans) {
		t.Error("HasPlan should be false when all plans are completed/abandoned")
	}

	plans = append(plans, Plan{ID: "P-003", Title: "Active", Status: StatusAutoProceed})
	if !HasPlan(plans) {
		t.Error("HasPlan should be true when an active plan exists")
	}
}

func TestActivePlan(t *testing.T) {
	plans := []Plan{
		{ID: "P-001", Title: "Completed", Status: StatusCompleted},
		{ID: "P-002", Title: "Active", Status: StatusAutoProceed},
		{ID: "P-003", Title: "Draft", Status: StatusDraft},
	}

	active := ActivePlan(plans)
	if active == nil {
		t.Fatal("ActivePlan should return non-nil")
	}
	if active.ID != "P-002" {
		t.Errorf("ActivePlan.ID = %q, want P-002", active.ID)
	}

	// No active plans
	plans = []Plan{
		{ID: "P-001", Title: "Completed", Status: StatusCompleted},
	}
	active = ActivePlan(plans)
	if active != nil {
		t.Error("ActivePlan should return nil when no active plans")
	}
}

func TestManagerCRUD(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	// No plans initially
	plans, err := m.LoadPlans()
	if err != nil {
		t.Fatalf("LoadPlans failed: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("expected 0 plans, got %d", len(plans))
	}

	// Create a plan
	plan := Plan{
		Title:        "V32 Phase 2: Plan-First tracking",
		Description:  "Add plan tracking and guard checks before code execution",
		Reasoning:    "Ema called out that code gets written without plans",
		Scope:        "Plan package only — no guard rail yet",
		Deliverables: []string{"internal/plan package", "plan.json state file", "tests"},
	}
	if err := m.CreatePlan(plan); err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	// Load and verify
	plans, err = m.LoadPlans()
	if err != nil {
		t.Fatalf("LoadPlans failed: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	if plans[0].ID != "P-001" {
		t.Errorf("plan.ID = %q, want P-001", plans[0].ID)
	}
	if plans[0].Status != StatusAutoProceed {
		t.Errorf("plan.Status = %q, want auto_proceed (bug fix)", plans[0].Status)
	}
	if plans[0].ApprovalLevel != ApprovalAuto {
		t.Errorf("plan.ApprovalLevel = %q, want auto", plans[0].ApprovalLevel)
	}
	if plans[0].CreatedAt.IsZero() {
		t.Error("plan.CreatedAt should be auto-set")
	}

	// Approve it
	if err := m.ApprovePlan("P-001", "ema"); err != nil {
		t.Fatalf("ApprovePlan failed: %v", err)
	}

	plans, _ = m.LoadPlans()
	if plans[0].Status != StatusApproved {
		t.Errorf("plan.Status = %q, want approved", plans[0].Status)
	}
	if plans[0].ApprovedBy != "ema" {
		t.Errorf("plan.ApprovedBy = %q, want ema", plans[0].ApprovedBy)
	}
	if plans[0].ApprovedAt == nil {
		t.Error("plan.ApprovedAt should be set")
	}

	// Complete it
	if err := m.CompletePlan("P-001"); err != nil {
		t.Fatalf("CompletePlan failed: %v", err)
	}

	plans, _ = m.LoadPlans()
	if plans[0].Status != StatusCompleted {
		t.Errorf("plan.Status = %q, want completed", plans[0].Status)
	}
	if plans[0].CompletedAt == nil {
		t.Error("plan.CompletedAt should be set")
	}

	// CanProceed should be false for completed plan
	if CanProceed(ActivePlan(plans)) {
		t.Error("CanProceed should be false for completed plan")
	}
}

func TestManagerArchitecturePlan(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	// Architecture change should require approval
	plan := Plan{
		Title:       "Refactor pipeline architecture",
		Description: "Rewrite the entire pipeline to use a different pattern",
		Scope:       "Core pipeline — affects all stages",
	}
	if err := m.CreatePlan(plan); err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	plans, _ := m.LoadPlans()
	if plans[0].ApprovalLevel != ApprovalRequired {
		t.Errorf("architecture plan ApprovalLevel = %q, want required", plans[0].ApprovalLevel)
	}
	if plans[0].Status != StatusPendingApproval {
		t.Errorf("architecture plan Status = %q, want pending_approval", plans[0].Status)
	}
}

func TestManagerCriticalPlan(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	// Critical change should require approval
	plan := Plan{
		Title:       "Database schema change",
		Description: "Add foreign keys to all tables",
		Scope:       "Remembrance schema — breaking change",
	}
	if err := m.CreatePlan(plan); err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	plans, _ := m.LoadPlans()
	if plans[0].ApprovalLevel != ApprovalCritical {
		t.Errorf("critical plan ApprovalLevel = %q, want critical", plans[0].ApprovalLevel)
	}
	if plans[0].Status != StatusPendingApproval {
		t.Errorf("critical plan Status = %q, want pending_approval", plans[0].Status)
	}
}

func TestManagerAbandonPlan(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	plan := Plan{
		Title:       "Something we won't do",
		Description: "This plan will be abandoned",
		Scope:       "Out of scope",
	}
	m.CreatePlan(plan)

	if err := m.AbandonPlan("P-001"); err != nil {
		t.Fatalf("AbandonPlan failed: %v", err)
	}

	plans, _ := m.LoadPlans()
	if plans[0].Status != StatusAbandoned {
		t.Errorf("plan.Status = %q, want abandoned", plans[0].Status)
	}

	// CanProceed should be false for abandoned plan
	if CanProceed(ActivePlan(plans)) {
		t.Error("CanProceed should be false for abandoned plan")
	}
}

func TestManagerApproveNonexistent(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	err := m.ApprovePlan("P-999", "ema")
	if err == nil {
		t.Error("ApprovePlan should return error for nonexistent plan")
	}
}

func TestFormatPlanForPrompt(t *testing.T) {
	now := time.Now()
	plan := &Plan{
		ID:            "P-011",
		Title:         "V32 Phase 2: Plan-First Pipeline",
		Description:   "Add plan tracking and guard checks",
		Reasoning:     "No code without a plan",
		Scope:         "Plan package only",
		Deliverables:  []string{"internal/plan package", "tests", "integration"},
		ApprovalLevel: ApprovalAuto,
		Status:        StatusAutoProceed,
		Branch:        "feat/v32-plan-first",
		CreatedAt:     now,
	}
	result := FormatPlanForPrompt(plan)
	if result == "No active plan." {
		t.Error("FormatPlanForPrompt should not return placeholder for real plan")
	}
	if !contains(result, "V32 Phase 2") {
		t.Error("FormatPlanForPrompt should contain title")
	}
	if !contains(result, "auto_proceed") {
		t.Error("FormatPlanForPrompt should contain status")
	}
	if !contains(result, "Plan package") {
		t.Error("FormatPlanForPrompt should contain scope")
	}

	// Nil plan
	result = FormatPlanForPrompt(nil)
	if result != "No active plan." {
		t.Errorf("FormatPlanForPrompt(nil) = %q, want 'No active plan.'", result)
	}
}

func TestManagerExplicitApprovalLevel(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	// Explicitly set approval level — should not be overridden by NeedsApproval
	plan := Plan{
		Title:         "Small fix",
		Scope:         "Bug fix only",
		ApprovalLevel: ApprovalRequired, // Explicitly require approval even though it's a bug fix
		Status:        StatusDraft,      // Explicitly set draft
	}
	m.CreatePlan(plan)

	plans, _ := m.LoadPlans()
	if plans[0].ApprovalLevel != ApprovalRequired {
		t.Errorf("explicit ApprovalLevel should be preserved, got %q", plans[0].ApprovalLevel)
	}
	if plans[0].Status != StatusDraft {
		t.Errorf("explicit Status should be preserved, got %q", plans[0].Status)
	}
}

func TestManagerConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	m.EnsureDir()

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			plan := Plan{
				Title: fmt.Sprintf("Concurrent plan %d", i),
				Scope: "Bug fix",
			}
			m.CreatePlan(plan)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	plans, err := m.LoadPlans()
	if err != nil {
		t.Fatalf("LoadPlans failed: %v", err)
	}
	if len(plans) != 10 {
		t.Fatalf("expected 10 plans, got %d", len(plans))
	}
}

func TestStepProgress(t *testing.T) {
	p := &Plan{
		Steps: []Step{
			{ID: "S1", Title: "First", Status: StepCompleted},
			{ID: "S2", Title: "Second", Status: StepPending},
			{ID: "S3", Title: "Third", Status: StepCompleted},
			{ID: "S4", Title: "Fourth", Status: StepInProgress},
		},
	}
	completed, total := StepProgress(p)
	if completed != 2 || total != 4 {
		t.Errorf("StepProgress = %d/%d, want 2/4", completed, total)
	}
	if completed, total := StepProgress(nil); completed != 0 || total != 0 {
		t.Errorf("StepProgress(nil) = %d/%d, want 0/0", completed, total)
	}
}

func TestManagerUpdateStepStatus(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if err := m.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	p := Plan{Title: "Test", Steps: []Step{
		{ID: "S1", Title: "Step 1", Status: StepPending},
		{ID: "S2", Title: "Step 2", Status: StepPending},
	}}
	if err := m.CreatePlan(p); err != nil {
		t.Fatal(err)
	}
	plans, _ := m.LoadPlans()
	planID := plans[0].ID
	if err := m.UpdateStepStatus(planID, "S1", StepCompleted, "done"); err != nil {
		t.Fatalf("UpdateStepStatus: %v", err)
	}
	plans, _ = m.LoadPlans()
	if plans[0].Steps[0].Status != StepCompleted {
		t.Errorf("S1 status = %s, want completed", plans[0].Steps[0].Status)
	}
	if plans[0].Steps[0].Notes != "done" {
		t.Errorf("S1 notes = %s, want 'done'", plans[0].Steps[0].Notes)
	}
	if plans[0].Steps[0].CompletedAt == nil {
		t.Error("S1 CompletedAt should be set")
	}
}

func TestManagerAddStep(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if err := m.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	p := Plan{Title: "Test", Steps: []Step{
		{ID: "S1", Title: "Step 1", Status: StepPending},
	}}
	if err := m.CreatePlan(p); err != nil {
		t.Fatal(err)
	}
	plans, _ := m.LoadPlans()
	planID := plans[0].ID
	if err := m.AddStep(planID, "Step 2"); err != nil {
		t.Fatalf("AddStep: %v", err)
	}
	plans, _ = m.LoadPlans()
	if len(plans[0].Steps) != 2 {
		t.Errorf("Steps count = %d, want 2", len(plans[0].Steps))
	}
	if plans[0].Steps[1].ID != "S2" {
		t.Errorf("New step ID = %s, want S2", plans[0].Steps[1].ID)
	}
}

func TestManagerReopenPlan(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if err := m.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	p := Plan{Title: "Test"}
	if err := m.CreatePlan(p); err != nil {
		t.Fatal(err)
	}
	plans, _ := m.LoadPlans()
	planID := plans[0].ID
	if err := m.CompletePlan(planID); err != nil {
		t.Fatal(err)
	}
	plans, _ = m.LoadPlans()
	if plans[0].Status != StatusCompleted {
		t.Fatalf("want completed, got %s", plans[0].Status)
	}
	if err := m.ReopenPlan(planID); err != nil {
		t.Fatal(err)
	}
	plans, _ = m.LoadPlans()
	if plans[0].Status != StatusAutoProceed {
		t.Errorf("want auto_proceed, got %s", plans[0].Status)
	}
	if plans[0].CompletedAt != nil {
		t.Error("CompletedAt should be nil after reopen")
	}
}

func TestManagerUpdatePlan(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if err := m.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	p := Plan{Title: "Original"}
	if err := m.CreatePlan(p); err != nil {
		t.Fatal(err)
	}
	plans, _ := m.LoadPlans()
	planID := plans[0].ID
	if err := m.UpdatePlan(planID, map[string]any{"title": "Updated", "branch": "feat/test"}); err != nil {
		t.Fatal(err)
	}
	plans, _ = m.LoadPlans()
	if plans[0].Title != "Updated" {
		t.Errorf("Title = %s, want Updated", plans[0].Title)
	}
	if plans[0].Branch != "feat/test" {
		t.Errorf("Branch = %s, want feat/test", plans[0].Branch)
	}
}

func TestFormatPlanForPromptWithSteps(t *testing.T) {
	p := &Plan{
		Title:  "Test Plan",
		ID:     "P-001",
		Status: StatusAutoProceed,
		Steps: []Step{
			{ID: "S1", Title: "First step", Status: StepCompleted},
			{ID: "S2", Title: "Second step", Status: StepInProgress},
			{ID: "S3", Title: "Third step", Status: StepBlocked, Notes: "waiting on review"},
			{ID: "S4", Title: "Fourth step", Status: StepPending},
			{ID: "S5", Title: "Fifth step", Status: StepSkipped},
		},
	}
	result := FormatPlanForPrompt(p)
	if !strings.Contains(result, "[x] S1: First step") {
		t.Error("Missing completed step marker")
	}
	if !strings.Contains(result, "[~] S2: Second step") {
		t.Error("Missing in-progress step marker")
	}
	if !strings.Contains(result, "[!] S3: Third step (waiting on review)") {
		t.Error("Missing blocked step marker with notes")
	}
	if !strings.Contains(result, "[ ] S4: Fourth step") {
		t.Error("Missing pending step marker")
	}
	if !strings.Contains(result, "[-] S5: Fifth step") {
		t.Error("Missing skipped step marker")
	}
	if !strings.Contains(result, "**Progress:** 1/5 steps completed") {
		t.Error("Missing progress line")
	}
}
