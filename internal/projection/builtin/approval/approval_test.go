package approval

import (
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

func TestApprovalStateProjection_Name(t *testing.T) {
	p := New()
	if p.Name() != "approval_state" {
		t.Errorf("Name() = %q, want %q", p.Name(), "approval_state")
	}
}

func TestApprovalStateProjection_Subscribe(t *testing.T) {
	p := New()
	subs := p.Subscribe()
	if len(subs) != 4 {
		t.Fatalf("Subscribe() length = %d, want 4", len(subs))
	}
}

func TestApprovalStateProjection_FullLifecycle(t *testing.T) {
	p := New()

	// Approval requested
	p.Apply(event.NewEvent("prizm.approval.requested", "test", map[string]any{
		"approval_id":     "appr_001",
		"mutation_type":   "write_file",
		"target_path":     "/src/main.go",
		"requested_by":    "lumi",
		"policy_decision": "requires_approval",
	}))
	snap := p.Snapshot()
	pendingCount, _ := snap["pending_count"].(int)
	totalCount, _ := snap["total_count"].(int)
	if pendingCount != 1 {
		t.Errorf("after requested: pending_count = %d, want 1", pendingCount)
	}
	if totalCount != 1 {
		t.Errorf("after requested: total_count = %d, want 1", totalCount)
	}

	// Approval granted
	p.Apply(event.NewEvent("prizm.approval.granted", "test", map[string]any{
		"approval_id": "appr_001",
	}))
	snap = p.Snapshot()
	pendingCount, _ = snap["pending_count"].(int)
	if pendingCount != 0 {
		t.Errorf("after granted: pending_count = %d, want 0", pendingCount)
	}

	// Check the approval entry status
	approvals := snap["approvals"].(map[string]any)
	entry := approvals["appr_001"].(map[string]any)
	if entry["status"] != "approved" {
		t.Errorf("approval status = %v, want approved", entry["status"])
	}
}

func TestApprovalStateProjection_Denied(t *testing.T) {
	p := New()

	p.Apply(event.NewEvent("prizm.approval.requested", "test", map[string]any{
		"approval_id": "appr_002",
	}))
	p.Apply(event.NewEvent("prizm.approval.denied", "test", map[string]any{
		"approval_id": "appr_002",
	}))

	snap := p.Snapshot()
	approvals := snap["approvals"].(map[string]any)
	entry := approvals["appr_002"].(map[string]any)
	if entry["status"] != "denied" {
		t.Errorf("approval status = %v, want denied", entry["status"])
	}
	pendingCount, _ := snap["pending_count"].(int)
	if pendingCount != 0 {
		t.Errorf("pending_count = %d, want 0", pendingCount)
	}
}

func TestApprovalStateProjection_Expired(t *testing.T) {
	p := New()

	p.Apply(event.NewEvent("prizm.approval.requested", "test", map[string]any{
		"approval_id": "appr_003",
	}))
	p.Apply(event.NewEvent("prizm.approval.expired", "test", map[string]any{
		"approval_id": "appr_003",
	}))

	snap := p.Snapshot()
	approvals := snap["approvals"].(map[string]any)
	entry := approvals["appr_003"].(map[string]any)
	if entry["status"] != "expired" {
		t.Errorf("approval status = %v, want expired", entry["status"])
	}
}

func TestApprovalStateProjection_MultipleApprovals(t *testing.T) {
	p := New()

	p.Apply(event.NewEvent("prizm.approval.requested", "test", map[string]any{
		"approval_id": "appr_010",
	}))
	p.Apply(event.NewEvent("prizm.approval.requested", "test", map[string]any{
		"approval_id": "appr_011",
	}))
	p.Apply(event.NewEvent("prizm.approval.requested", "test", map[string]any{
		"approval_id": "appr_012",
	}))
	p.Apply(event.NewEvent("prizm.approval.granted", "test", map[string]any{
		"approval_id": "appr_010",
	}))

	snap := p.Snapshot()
	pendingCount, _ := snap["pending_count"].(int)
	totalCount, _ := snap["total_count"].(int)
	if pendingCount != 2 {
		t.Errorf("pending_count = %d, want 2", pendingCount)
	}
	if totalCount != 3 {
		t.Errorf("total_count = %d, want 3", totalCount)
	}
}

func TestApprovalStateProjection_IgnoresUnrelatedEvents(t *testing.T) {
	p := New()
	p.Apply(event.NewEvent("prizm.task.created", "test", nil))

	snap := p.Snapshot()
	totalCount, _ := snap["total_count"].(int)
	if totalCount != 0 {
		t.Errorf("total_count = %d, want 0", totalCount)
	}
}
