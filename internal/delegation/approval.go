package delegation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/emaharmony/prizm/internal/task"
)

// ApprovalType defines the kind of approval being requested.
type ApprovalType string

const (
	// ApprovalPush requests approval to push code.
	ApprovalPush ApprovalType = "push"

	// ApprovalDeploy requests approval to deploy.
	ApprovalDeploy ApprovalType = "deploy"

	// ApprovalDelete requests approval to delete resources.
	ApprovalDelete ApprovalType = "delete"

	// ApprovalMerge requests approval to merge a PR.
	ApprovalMerge ApprovalType = "merge"

	// ApprovalGeneric requests approval for a generic action.
	ApprovalGeneric ApprovalType = "generic"
)

// ApprovalRequest represents a request for user approval.
type ApprovalRequest struct {
	// ID is the unique approval identifier.
	ID string `json:"id"`

	// TaskID references the task that triggered this approval.
	TaskID string `json:"task_id"`

	// AgentID is the agent requesting approval.
	AgentID string `json:"agent_id"`

	// Type is the kind of approval being requested.
	Type ApprovalType `json:"type"`

	// Description is a human-readable description of what needs approval.
	Description string `json:"description"`

	// Target is the target of the action (e.g., branch name, environment).
	Target string `json:"target,omitempty"`

	// CreatedAt is when the request was created.
	CreatedAt time.Time `json:"created_at"`

	// Status is the current approval status: pending, granted, or denied.
	Status ApprovalStatus `json:"status"`

	// ResolvedAt is when the approval was resolved (nil if pending).
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`

	// ResolvedBy is the user ID who resolved the approval.
	ResolvedBy string `json:"resolved_by,omitempty"`
}

// ApprovalStatus represents the state of an approval request.
type ApprovalStatus string

const (
	ApprovalPending ApprovalStatus = "pending"
	ApprovalGranted ApprovalStatus = "granted"
	ApprovalDenied  ApprovalStatus = "denied"
)

// ApprovalManager manages approval requests and their resolution.
type ApprovalManager struct {
	store  *task.Store
	engine *Engine
}

// NewApprovalManager creates a new approval manager.
func NewApprovalManager(store *task.Store, engine *Engine) *ApprovalManager {
	return &ApprovalManager{
		store:  store,
		engine: engine,
	}
}

// RequestApproval creates an approval request and publishes the event.
// The request is stored in the task store as a special task with type "approval".
func (am *ApprovalManager) RequestApproval(ctx context.Context, agentID string, approvalType ApprovalType, description string, target string) (*ApprovalRequest, error) {
	now := time.Now()
	req := &ApprovalRequest{
		ID:          fmt.Sprintf("approval-%s", generateApprovalID()),
		AgentID:     agentID,
		Type:        approvalType,
		Description: description,
		Target:      target,
		CreatedAt:   now,
		Status:      ApprovalPending,
	}

	// Store as a task for tracking
	contextData := map[string]any{
		"approval_id":   req.ID,
		"approval_type": string(approvalType),
		"target":        target,
	}

	delegateTask, err := am.engine.Delegate(ctx, agentID, agentID, string(approvalType), description, contextData)
	if err != nil {
		return nil, fmt.Errorf("approval: create task: %w", err)
	}

	req.TaskID = delegateTask.ID

	// Publish approval.requested event
	event := map[string]any{
		"v":             1,
		"approval_id":   req.ID,
		"task_id":       req.TaskID,
		"agent_id":      agentID,
		"approval_type": string(approvalType),
		"description":   description,
		"target":        target,
		"status":        string(ApprovalPending),
		"created_at":    now.Format(time.RFC3339),
	}

	if err := am.engine.publishEvent(agentID+".approval.requested", event); err != nil {
		log.Printf("[APPROVAL] event publish failed for %s: %v", req.ID, err)
	}

	log.Printf("[APPROVAL] %s requested %s approval from %s (task: %s)", agentID, approvalType, agentID, req.TaskID)
	return req, nil
}

// GrantApproval marks an approval as granted and publishes the event.
func (am *ApprovalManager) GrantApproval(ctx context.Context, taskID string, resolvedBy string) error {
	task, err := am.store.Get(taskID)
	if err != nil {
		return fmt.Errorf("approval: get task %s: %w", taskID, err)
	}

	result := map[string]any{
		"approval_status": string(ApprovalGranted),
		"resolved_by":     resolvedBy,
	}

	if err := am.engine.Complete(ctx, taskID, result); err != nil {
		return fmt.Errorf("approval: grant: %w", err)
	}

	// Publish approval.granted event
	event := map[string]any{
		"v":           1,
		"task_id":     taskID,
		"approved_by": resolvedBy,
		"approved_at": time.Now().Format(time.RFC3339),
	}

	if err := am.engine.publishEvent(task.DelegatedTo+".approval.granted", event); err != nil {
		log.Printf("[APPROVAL] grant event publish failed for %s: %v", taskID, err)
	}

	log.Printf("[APPROVAL] task %s granted by %s", taskID, resolvedBy)
	return nil
}

// DenyApproval marks an approval as denied and publishes the event.
func (am *ApprovalManager) DenyApproval(ctx context.Context, taskID string, resolvedBy string, reason string) error {
	task, err := am.store.Get(taskID)
	if err != nil {
		return fmt.Errorf("approval: get task %s: %w", taskID, err)
	}

	if err := am.engine.Fail(ctx, taskID, reason); err != nil {
		return fmt.Errorf("approval: deny: %w", err)
	}

	// Publish approval.denied event
	event := map[string]any{
		"v":         1,
		"task_id":   taskID,
		"denied_by": resolvedBy,
		"reason":    reason,
		"denied_at": time.Now().Format(time.RFC3339),
	}

	if err := am.engine.publishEvent(task.DelegatedTo+".approval.denied", event); err != nil {
		log.Printf("[APPROVAL] deny event publish failed for %s: %v", taskID, err)
	}

	log.Printf("[APPROVAL] task %s denied by %s: %s", taskID, resolvedBy, reason)
	return nil
}

// GetApproval retrieves an approval request by task ID.
func (am *ApprovalManager) GetApproval(taskID string) (*ApprovalRequest, error) {
	tsk, err := am.store.Get(taskID)
	if err != nil {
		return nil, fmt.Errorf("approval: get task %s: %w", taskID, err)
	}

	status := ApprovalPending
	if tsk.Status == task.StatusCompleted {
		status = ApprovalGranted
	} else if tsk.Status == task.StatusFailed || tsk.Status == task.StatusCancelled {
		status = ApprovalDenied
	}

	approvalType := ApprovalType(tsk.Type)
	if approvalType != ApprovalPush && approvalType != ApprovalDeploy &&
		approvalType != ApprovalDelete && approvalType != ApprovalMerge &&
		approvalType != ApprovalGeneric {
		return nil, fmt.Errorf("approval: task %s is not an approval task (type: %s)", taskID, tsk.Type)
	}

	req := &ApprovalRequest{
		TaskID:      tsk.ID,
		AgentID:     tsk.DelegatedBy,
		Type:        approvalType,
		Description: tsk.Description,
		Status:      status,
		CreatedAt:   tsk.CreatedAt,
	}

	// Extract approval-specific context
	if tsk.Context != nil {
		if id, ok := tsk.Context["approval_id"].(string); ok {
			req.ID = id
		}
		if target, ok := tsk.Context["target"].(string); ok {
			req.Target = target
		}
	}

	if tsk.CompletedAt != nil {
		req.ResolvedAt = tsk.CompletedAt
	}

	if tsk.Result != nil {
		if resolvedBy, ok := tsk.Result["resolved_by"].(string); ok {
			req.ResolvedBy = resolvedBy
		}
	}

	return req, nil
}

// ListPendingApprovals returns all approval requests in pending status.
// Approvals may be in "assigned" or "in_progress" status.
func (am *ApprovalManager) ListPendingApprovals() ([]*ApprovalRequest, error) {
	var pending []*ApprovalRequest

	approvalTypes := map[string]bool{
		string(ApprovalPush):    true,
		string(ApprovalDeploy):  true,
		string(ApprovalDelete):  true,
		string(ApprovalMerge):   true,
		string(ApprovalGeneric): true,
	}

	// Check both assigned and in_progress statuses
	for _, status := range []task.Status{task.StatusAssigned, task.StatusInProgress} {
		tasks, err := am.store.ListByStatus(status)
		if err != nil {
			return nil, fmt.Errorf("approval: list %s: %w", status, err)
		}

		for _, tsk := range tasks {
			if !approvalTypes[tsk.Type] {
				continue
			}
			req := &ApprovalRequest{
				TaskID:      tsk.ID,
				AgentID:     tsk.DelegatedBy,
				Type:        ApprovalType(tsk.Type),
				Description: tsk.Description,
				Status:      ApprovalPending,
				CreatedAt:   tsk.CreatedAt,
			}
			pending = append(pending, req)
		}
	}

	return pending, nil
}

// generateApprovalID creates a unique approval identifier.
func generateApprovalID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
