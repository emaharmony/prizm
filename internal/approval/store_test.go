package approval

import (
	"os"
	"testing"
)

func TestApprovalStoreFileExistence(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	policy := PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "test.txt", "hello", policy)

	if err := store.Save(a); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	// Verify the file exists on disk
	path := store.approvalPath("run_01KM", a.ApprovalID)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("approval file does not exist: %s", path)
	}
}

func TestApprovalStoreSaveIdempotentOnSameRun(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	policy := PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a1 := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "file1.txt", "content1", policy)
	a2 := NewApproval("run_01KM", "corr_01KM", "test-cli", "prizm", "write_file", "file2.txt", "content2", policy)

	store.Save(a1)
	store.Save(a2)

	for _, a := range []*Approval{a1, a2} {
		path := store.approvalPath("run_01KM", a.ApprovalID)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("approval file does not exist: %s", path)
		}
	}
}

func TestNewApprovalDifferentRunIDs(t *testing.T) {
	policy := PolicyDecision{Decision: "requires_approval", Reason: "test"}
	a1 := NewApproval("run_A", "corr_A", "test", "prizm", "write_file", "test.txt", "x", policy)
	a2 := NewApproval("run_B", "corr_B", "test", "prizm", "write_file", "test.txt", "x", policy)

	if a1.ApprovalID == a2.ApprovalID {
		t.Error("different runs should produce different approval IDs")
	}
	if a1.RunID != "run_A" || a2.RunID != "run_B" {
		t.Error("run IDs not preserved")
	}
}
