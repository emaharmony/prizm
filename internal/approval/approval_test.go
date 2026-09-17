package approval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewApproval(t *testing.T) {
	policy := PolicyDecision{Decision: "requires_approval", Reason: "file writes require approval"}
	a := NewApproval(
		"run_01KM", "corr_01KM", "test-cli",
		"prizm", "write_file", "test.txt",
		"hello world", policy,
	)

	if a.Status != StatusPending {
		t.Errorf("expected status pending, got %q", a.Status)
	}
	if a.MutationType != "write_file" {
		t.Errorf("expected mutation_type write_file, got %q", a.MutationType)
	}
	if a.TargetPath != "test.txt" {
		t.Errorf("expected target_path test.txt, got %q", a.TargetPath)
	}
	if a.Content != "hello world" {
		t.Errorf("expected content 'hello world', got %q", a.Content)
	}
	if a.Preview != "hello world" {
		t.Errorf("expected preview 'hello world', got %q", a.Preview)
	}
	if a.RequestedBy != "test-cli" {
		t.Errorf("expected requested_by 'test-cli', got %q", a.RequestedBy)
	}
	if a.Policy.Decision != "requires_approval" {
		t.Errorf("expected policy decision 'requires_approval', got %q", a.Policy.Decision)
	}
}

func TestNewApprovalLongContentPreview(t *testing.T) {
	policy := PolicyDecision{Decision: "requires_approval", Reason: "test"}
	longContent := ""
	for i := 0; i < 600; i++ {
		longContent += "x"
	}
	a := NewApproval("run_01KM", "corr_01KM", "test", "prizm", "write_file", "test.txt", longContent, policy)

	if len(a.Preview) != 503 { // 500 chars + "..."
		t.Errorf("expected preview length 503, got %d", len(a.Preview))
	}
	if a.Preview[500:] != "..." {
		t.Errorf("expected preview to end with '...', got %q", a.Preview[500:])
	}
}

func TestNewApprovalID(t *testing.T) {
	id := NewApprovalID()
	if len(id) < len("appr_01KM") {
		t.Errorf("approval ID too short: %q", id)
	}
	if id[:5] != "appr_" {
		t.Errorf("expected 'appr_' prefix, got %q", id[:5])
	}
}

func TestApprovalApprove(t *testing.T) {
	a := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "content", PolicyDecision{Decision: "requires_approval", Reason: "test"})

	err := a.Approve("ema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Status != StatusApproved {
		t.Errorf("expected status approved, got %q", a.Status)
	}
	if a.ApprovedBy != "ema" {
		t.Errorf("expected approved_by 'ema', got %q", a.ApprovedBy)
	}
	if a.ApprovedAt == nil {
		t.Error("expected approved_at to be set")
	}
}

func TestApprovalApproveAlreadyDenied(t *testing.T) {
	a := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "content", PolicyDecision{Decision: "requires_approval", Reason: "test"})
	a.Deny("ema", "not needed")

	err := a.Approve("ema")
	if err == nil {
		t.Fatal("expected error when approving denied approval")
	}
}

func TestApprovalApproveAlreadyApproved(t *testing.T) {
	a := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "content", PolicyDecision{Decision: "requires_approval", Reason: "test"})
	a.Approve("ema")

	err := a.Approve("ema")
	if err == nil {
		t.Fatal("expected error when approving already-approved approval")
	}
}

func TestApprovalDeny(t *testing.T) {
	a := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "content", PolicyDecision{Decision: "requires_approval", Reason: "test"})

	err := a.Deny("ema", "not needed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Status != StatusDenied {
		t.Errorf("expected status denied, got %q", a.Status)
	}
	if a.DeniedBy != "ema" {
		t.Errorf("expected denied_by 'ema', got %q", a.DeniedBy)
	}
	if a.DenialReason != "not needed" {
		t.Errorf("expected denial_reason 'not needed', got %q", a.DenialReason)
	}
	if a.DeniedAt == nil {
		t.Error("expected denied_at to be set")
	}
}

func TestApprovalDenyAlreadyApproved(t *testing.T) {
	a := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "content", PolicyDecision{Decision: "requires_approval", Reason: "test"})
	a.Approve("ema")

	err := a.Deny("ema", "changed mind")
	if err == nil {
		t.Fatal("expected error when denying already-approved approval")
	}
}

func TestApprovalDenyAlreadyDenied(t *testing.T) {
	a := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "content", PolicyDecision{Decision: "requires_approval", Reason: "test"})
	a.Deny("ema", "not needed")

	err := a.Deny("ema", "still not needed")
	if err == nil {
		t.Fatal("expected error when denying already-denied approval")
	}
}

func TestApprovalSerialize(t *testing.T) {
	policy := PolicyDecision{Decision: "requires_approval", Reason: "file writes require approval"}
	a := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "hello", policy)

	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded Approval
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.ApprovalID != a.ApprovalID {
		t.Errorf("approval_id mismatch: %q vs %q", decoded.ApprovalID, a.ApprovalID)
	}
	if decoded.Status != StatusPending {
		t.Errorf("status mismatch: %q", decoded.Status)
	}
	if decoded.Content != "hello" {
		t.Errorf("content mismatch: %q", decoded.Content)
	}
	if decoded.Policy.Decision != "requires_approval" {
		t.Errorf("policy decision mismatch: %q", decoded.Policy.Decision)
	}
}

func TestApprovalStoreSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	policy := PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "hello world", policy)

	if err := store.Save(a); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	loaded, err := store.Load("run_01KM", a.ApprovalID)
	if err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	if loaded.ApprovalID != a.ApprovalID {
		t.Errorf("loaded approval_id mismatch: %q vs %q", loaded.ApprovalID, a.ApprovalID)
	}
	if loaded.Status != StatusPending {
		t.Errorf("loaded status mismatch: %q", loaded.Status)
	}
	if loaded.Content != "hello world" {
		t.Errorf("loaded content mismatch: %q", loaded.Content)
	}
}

func TestApprovalStoreLoadNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	_, err := store.Load("run_01KM", "appr_nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent approval")
	}
}

func TestApprovalStoreList(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	policy := PolicyDecision{Decision: "requires_approval", Reason: "test"}

	a1 := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test1.txt", "content1", policy)
	a2 := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test2.txt", "content2", policy)

	store.Save(a1)
	store.Save(a2)

	approvals, err := store.List("run_01KM")
	if err != nil {
		t.Fatalf("failed to list: %v", err)
	}

	if len(approvals) != 2 {
		t.Errorf("expected 2 approvals, got %d", len(approvals))
	}

	ids := make(map[string]bool)
	for _, a := range approvals {
		ids[a.ApprovalID] = true
	}
	if !ids[a1.ApprovalID] || !ids[a2.ApprovalID] {
		t.Errorf("not all approvals found in list")
	}
}

func TestApprovalStoreListEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// No approvals directory created
	approvals, err := store.List("run_nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(approvals) != 0 {
		t.Errorf("expected 0 approvals for nonexistent run, got %d", len(approvals))
	}
}

func TestApprovalStoreSaveUpdatesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	policy := PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "hello", policy)

	store.Save(a)

	// Approve and re-save
	a.Approve("ema")
	store.Save(a)

	loaded, err := store.Load("run_01KM", a.ApprovalID)
	if err != nil {
		t.Fatalf("failed to reload: %v", err)
	}

	if loaded.Status != StatusApproved {
		t.Errorf("expected status approved after update, got %q", loaded.Status)
	}
	if loaded.ApprovedBy != "ema" {
		t.Errorf("expected approved_by 'ema', got %q", loaded.ApprovedBy)
	}
}

func TestApprovalTimestamps(t *testing.T) {
	before := time.Now().UTC()
	a := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "content", PolicyDecision{Decision: "requires_approval", Reason: "test"})
	after := time.Now().UTC()

	if a.CreatedAt.Before(before) || a.CreatedAt.After(after) {
		t.Errorf("created_at out of range: %v (expected between %v and %v)", a.CreatedAt, before, after)
	}

	time.Sleep(time.Millisecond * 10)
	a.Approve("ema")

	if a.ApprovedAt == nil || !a.ApprovedAt.After(before) {
		t.Error("approved_at should be after created_at")
	}

	if filepath.Base(os.Args[0]) == "" {
		// just prevent unused import issues
	}
}
