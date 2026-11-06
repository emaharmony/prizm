package approval

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestSaveIdempotentConcurrentCreateAndDecisionReplay(t *testing.T) {
	runsDir := t.TempDir()
	store := NewStore(runsDir)
	newRequest := func() *Approval {
		a := NewApproval("run-keyed", "execution-keyed", "agent-keyed", "prizm", MutationApplyPatch, "1 files", "patch", PolicyDecision{Decision: DecisionRequiresApproval, Reason: "test"})
		a.ApprovalID = "appr_sb_fixed"
		a.ProposalID = "prop_sb_fixed"
		a.IdempotencyKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		a.PatchPlan = &PatchPlan{BaseSHA: "base-sha", BaseTree: "base-tree", ExpectedTree: "expected-tree", PatchSHA256: "patch-hash", Paths: []string{"file.txt"}}
		a.SandboxBinding = &SandboxProposalBinding{TaskID: "task", ExecutionKey: "execution-keyed", AgentID: "agent-keyed", WorkspaceID: "workspace", WorkspacePath: "/workspace", BaseSHA: "base-sha", BaseTree: "base-tree", ArtifactDigest: strings.Repeat("a", 64)}
		return a
	}
	const callers = 12
	var wg sync.WaitGroup
	results := make(chan *Approval, callers)
	errors := make(chan error, callers)
	created := make(chan bool, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			saved, wasCreated, err := NewStore(runsDir).SaveIdempotent(newRequest())
			if err != nil {
				errors <- err
				return
			}
			results <- saved
			created <- wasCreated
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	close(created)
	for err := range errors {
		t.Fatal(err)
	}
	createCount := 0
	for wasCreated := range created {
		if wasCreated {
			createCount++
		}
	}
	if createCount != 1 {
		t.Fatalf("expected exactly one atomic creator, got %d", createCount)
	}
	for saved := range results {
		if saved.ApprovalID != "appr_sb_fixed" || saved.Status != StatusPending {
			t.Fatalf("unexpected replay record: %#v", saved)
		}
	}

	decided, err := store.Load("run-keyed", "appr_sb_fixed")
	if err != nil {
		t.Fatal(err)
	}
	decided.Status = StatusDenied
	decided.DeniedBy = "reviewer"
	if err := store.Save(decided); err != nil {
		t.Fatal(err)
	}
	saved, wasCreated, err := store.SaveIdempotent(newRequest())
	if err != nil {
		t.Fatal(err)
	}
	if wasCreated || saved.Status != StatusDenied || saved.DeniedBy != "reviewer" {
		t.Fatalf("replay reset a decided approval: %#v", saved)
	}
}

func TestSaveIdempotentRejectsKeyPayloadConflict(t *testing.T) {
	store := NewStore(t.TempDir())
	first := NewApproval("run-keyed", "execution-keyed", "agent-keyed", "prizm", MutationApplyPatch, "1 files", "patch-one", PolicyDecision{Decision: DecisionRequiresApproval, Reason: "test"})
	first.ApprovalID, first.ProposalID, first.IdempotencyKey = "appr_sb_fixed", "prop_sb_fixed", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	first.PatchPlan = &PatchPlan{BaseSHA: "base-sha", BaseTree: "base-tree", ExpectedTree: "expected-tree", PatchSHA256: "patch-hash", Paths: []string{"one.txt"}}
	first.SandboxBinding = &SandboxProposalBinding{TaskID: "task", ExecutionKey: "execution-keyed", AgentID: "agent-keyed", WorkspaceID: "workspace", WorkspacePath: "/workspace", BaseSHA: "base-sha", BaseTree: "base-tree", ArtifactDigest: strings.Repeat("a", 64)}
	if _, _, err := store.SaveIdempotent(first); err != nil {
		t.Fatal(err)
	}
	conflict := NewApproval("run-keyed", "execution-keyed", "agent-keyed", "prizm", MutationApplyPatch, "1 files", "patch-two", PolicyDecision{Decision: DecisionRequiresApproval, Reason: "test"})
	conflict.ApprovalID, conflict.ProposalID, conflict.IdempotencyKey = first.ApprovalID, first.ProposalID, first.IdempotencyKey
	conflict.PatchPlan = &PatchPlan{BaseSHA: "base-sha", BaseTree: "base-tree", ExpectedTree: "expected-tree", PatchSHA256: "different", Paths: []string{"two.txt"}}
	conflict.SandboxBinding = &SandboxProposalBinding{TaskID: "task", ExecutionKey: "execution-keyed", AgentID: "agent-keyed", WorkspaceID: "workspace", WorkspacePath: "/workspace", BaseSHA: "base-sha", BaseTree: "base-tree", ArtifactDigest: strings.Repeat("b", 64)}
	if _, _, err := store.SaveIdempotent(conflict); err == nil {
		t.Fatal("expected conflicting payload for an existing key to fail")
	}
}

func TestSaveIdempotentRecoversStalePartialStagingFile(t *testing.T) {
	store := NewStore(t.TempDir())
	a := NewApproval("run-keyed", "execution-keyed", "agent-keyed", "prizm", MutationApplyPatch, "1 files", "patch", PolicyDecision{Decision: DecisionRequiresApproval, Reason: "test"})
	a.ApprovalID, a.ProposalID, a.IdempotencyKey = "appr_sb_fixed", "prop_sb_fixed", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	a.PatchPlan = &PatchPlan{BaseSHA: "base-sha", BaseTree: "base-tree", ExpectedTree: "expected-tree", PatchSHA256: "patch-hash", Paths: []string{"file.txt"}}
	a.SandboxBinding = &SandboxProposalBinding{TaskID: "task", ExecutionKey: "execution-keyed", AgentID: "agent-keyed", WorkspaceID: "workspace", WorkspacePath: "/workspace", BaseSHA: "base-sha", BaseTree: "base-tree", ArtifactDigest: strings.Repeat("a", 64)}
	dir := store.approvalDir(a.RunID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(dir, ".approval-idempotent-crashed.tmp")
	if err := os.WriteFile(partial, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(partial, old, old); err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.SaveIdempotent(a); err != nil || !created {
		t.Fatalf("retry after stale staging file: created=%v err=%v", created, err)
	}
	if _, err := os.Lstat(partial); !os.IsNotExist(err) {
		t.Fatalf("stale partial staging file was not removed: %v", err)
	}
}
