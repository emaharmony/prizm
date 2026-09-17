package delegation

import (
	"context"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/task"
)

func TestApprovalManager_RequestApproval(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)
	am := NewApprovalManager(store, engine)

	ctx := context.Background()
	req, err := am.RequestApproval(ctx, "lumi", ApprovalPush, "Push to feature branch?", "origin/v22-multi-agent")
	if err != nil {
		t.Fatalf("failed to request approval: %v", err)
	}

	if req.ID == "" {
		t.Error("expected approval ID to be set")
	}
	if req.TaskID == "" {
		t.Error("expected task ID to be set")
	}
	if req.Type != ApprovalPush {
		t.Errorf("expected approval type push, got %s", req.Type)
	}
	if req.Status != ApprovalPending {
		t.Errorf("expected pending status, got %s", req.Status)
	}
	if req.AgentID != "lumi" {
		t.Errorf("expected agent lumi, got %s", req.AgentID)
	}
}

func TestApprovalManager_GrantApproval(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)
	am := NewApprovalManager(store, engine)

	ctx := context.Background()
	req, err := am.RequestApproval(ctx, "lumi", ApprovalMerge, "Merge PR #34?", "origin/main")
	if err != nil {
		t.Fatalf("failed to request approval: %v", err)
	}

	// Grant the approval
	err = am.GrantApproval(ctx, req.TaskID, "user-123456")
	if err != nil {
		t.Fatalf("failed to grant approval: %v", err)
	}

	// Verify the task is completed
	tsk, err := store.Get(req.TaskID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Errorf("expected task status completed, got %s", tsk.Status)
	}
}

func TestApprovalManager_DenyApproval(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)
	am := NewApprovalManager(store, engine)

	ctx := context.Background()
	req, err := am.RequestApproval(ctx, "lumi", ApprovalDeploy, "Deploy to production?", "production")
	if err != nil {
		t.Fatalf("failed to request approval: %v", err)
	}

	// Deny the approval
	err = am.DenyApproval(ctx, req.TaskID, "user-123456", "Not ready yet")
	if err != nil {
		t.Fatalf("failed to deny approval: %v", err)
	}

	// Verify the task is failed
	tsk, err := store.Get(req.TaskID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if tsk.Status != task.StatusFailed {
		t.Errorf("expected task status failed, got %s", tsk.Status)
	}
}

func TestApprovalManager_GetApproval(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)
	am := NewApprovalManager(store, engine)

	ctx := context.Background()
	req, err := am.RequestApproval(ctx, "lumi", ApprovalPush, "Push to branch?", "origin/feature")
	if err != nil {
		t.Fatalf("failed to request approval: %v", err)
	}

	// Retrieve the approval
	got, err := am.GetApproval(req.TaskID)
	if err != nil {
		t.Fatalf("failed to get approval: %v", err)
	}

	if got.TaskID != req.TaskID {
		t.Errorf("expected task ID %s, got %s", req.TaskID, got.TaskID)
	}
	if got.Type != ApprovalPush {
		t.Errorf("expected approval type push, got %s", got.Type)
	}
	if got.Status != ApprovalPending {
		t.Errorf("expected pending status, got %s", got.Status)
	}
	if got.AgentID != "lumi" {
		t.Errorf("expected agent lumi, got %s", got.AgentID)
	}
}

func TestApprovalManager_GetApproval_Granted(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)
	am := NewApprovalManager(store, engine)

	ctx := context.Background()
	req, _ := am.RequestApproval(ctx, "lumi", ApprovalPush, "Push?", "origin/main")
	am.GrantApproval(ctx, req.TaskID, "user-999")

	got, err := am.GetApproval(req.TaskID)
	if err != nil {
		t.Fatalf("failed to get approval: %v", err)
	}
	if got.Status != ApprovalGranted {
		t.Errorf("expected granted status, got %s", got.Status)
	}
	if got.ResolvedBy != "user-999" {
		t.Errorf("expected resolved_by user-999, got %s", got.ResolvedBy)
	}
}

func TestApprovalManager_GetApproval_Denied(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)
	am := NewApprovalManager(store, engine)

	ctx := context.Background()
	req, _ := am.RequestApproval(ctx, "lumi", ApprovalDelete, "Delete stale branch?", "origin/old-branch")
	am.DenyApproval(ctx, req.TaskID, "user-999", "Still in use")

	got, err := am.GetApproval(req.TaskID)
	if err != nil {
		t.Fatalf("failed to get approval: %v", err)
	}
	if got.Status != ApprovalDenied {
		t.Errorf("expected denied status, got %s", got.Status)
	}
}

func TestApprovalManager_ApprovalTypes(t *testing.T) {
	types := []ApprovalType{
		ApprovalPush,
		ApprovalDeploy,
		ApprovalDelete,
		ApprovalMerge,
		ApprovalGeneric,
	}

	for _, at := range types {
		store := newTestStore(t)
		engine := NewEngine(store, nil)
		am := NewApprovalManager(store, engine)

		ctx := context.Background()
		req, err := am.RequestApproval(ctx, "lumi", at, "Test "+string(at), "target")
		if err != nil {
			t.Errorf("failed to request %s approval: %v", at, err)
			continue
		}
		if req.Type != at {
			t.Errorf("expected approval type %s, got %s", at, req.Type)
		}
	}
}

func TestApprovalManager_NonExistentTask(t *testing.T) {
	store := newTestStore(t)
	engine := NewEngine(store, nil)
	am := NewApprovalManager(store, engine)

	ctx := context.Background()

	// Grant on non-existent task should fail
	err := am.GrantApproval(ctx, "nonexistent", "user-1")
	if err == nil {
		t.Error("expected error for non-existent task")
	}

	// Deny on non-existent task should fail
	err = am.DenyApproval(ctx, "nonexistent", "user-1", "reason")
	if err == nil {
		t.Error("expected error for non-existent task")
	}

	// Get on non-existent task should fail
	_, err = am.GetApproval("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent task")
	}
}

func TestApprovalManager_NonApprovalTask(t *testing.T) {
	// GetApproval on a regular (non-approval) task should return error
	store := newTestStore(t)
	engine := NewEngine(store, nil)
	am := NewApprovalManager(store, engine)

	ctx := context.Background()

	// Create a regular delegation task (not an approval)
	regularTask, err := engine.Delegate(ctx, "lumi", "mango", "code", "Regular coding task", nil)
	if err != nil {
		t.Fatalf("failed to delegate: %v", err)
	}

	// GetApproval should reject it
	_, err = am.GetApproval(regularTask.ID)
	if err == nil {
		t.Error("expected error for non-approval task")
	}
}

func TestApprovalManager_TrackerIntegration(t *testing.T) {
	// Test that approvals are tracked by the task tracker
	store := newTestStore(t)
	engine := NewEngine(store, nil)
	am := NewApprovalManager(store, engine)

	ctx := context.Background()
	req, err := am.RequestApproval(ctx, "lumi", ApprovalPush, "Push to branch?", "origin/feature")
	if err != nil {
		t.Fatalf("failed to request approval: %v", err)
	}

	// The approval should appear as an active task
	tracker := NewTracker(store, engine, TrackerConfig{
		TaskTimeout:   10 * time.Minute,
		CheckInterval: 1 * time.Minute,
	})

	active, err := tracker.ActiveTasks()
	if err != nil {
		t.Fatalf("failed to get active tasks: %v", err)
	}

	found := false
	for _, tsk := range active {
		if tsk.ID == req.TaskID {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected approval task to appear in active tasks")
	}

	// Grant the approval
	am.GrantApproval(ctx, req.TaskID, "user-1")

	// Now the task should not be in active tasks
	active, err = tracker.ActiveTasks()
	if err != nil {
		t.Fatalf("failed to get active tasks: %v", err)
	}

	for _, tsk := range active {
		if tsk.ID == req.TaskID {
			t.Error("expected approval task to be removed from active after granting")
		}
	}
}
