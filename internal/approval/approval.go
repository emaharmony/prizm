// Package approval implements Prizm V4's approval-gated mutation model.
// Approvals are the gate between an agent proposing a file change and Prizm
// actually performing it. Every approval is persisted as a JSON artifact and
// every state transition is emitted as an event.
package approval

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Status constants for the approval state machine.
// The state machine is intentionally simple: pending → approved OR denied.
// There is no "un-approve" or "un-deny" — once a decision is made, it's final.
// This prevents race conditions where a mutation could be approved, applied,
// then "un-approved" after the file is already on disk.
//
// State transitions:
//
//	pending → approved (human says yes, mutation is applied)
//	pending → denied  (human says no, mutation is NOT applied)
//	pending → expired (optional: approval request timed out, mutation is NOT applied)
//	approved → ✗ (locked — cannot be reversed)
//	denied → ✗ (locked — cannot be reversed)
const (
	StatusPending  = "pending"  // Awaiting human decision
	StatusApproved = "approved" // Human approved — mutation can be applied
	StatusDenied   = "denied"   // Human denied — mutation is discarded
	StatusExpired  = "expired"  // Approval request timed out — mutation is discarded
)

// MutationType constants.
const (
	MutationWriteFile       = "write_file"
	MutationCreateDirectory = "create_directory"
	MutationApplyPatch      = "apply_patch"
	// MutationToolCall represents "re-invoke this exact tool with this exact
	// input, for real, now that a human approved it" — used for any
	// approval-gated tool whose Execute() performs a real, non-file-write
	// side effect (shell, git_checkout, git_add, git_commit, git_push,
	// create_pr, mcp_* tools). See Approval.ToolName/Input.
	MutationToolCall = "tool_call"
)

// PolicyDecision constants.
const (
	DecisionRequiresApproval = "requires_approval"
	DecisionApproved         = "approved"
	DecisionDenied           = "denied"
)

// Approval represents a pending, approved, or denied file mutation.
type Approval struct {
	ApprovalID    string         `json:"approval_id"`
	RunID         string         `json:"run_id"`
	CorrelationID string         `json:"correlation_id"`
	Status        string         `json:"status"` // pending, approved, denied, expired
	RequestedBy   string         `json:"requested_by"`
	Project       string         `json:"project"`
	MutationType  string         `json:"mutation_type"` // write_file, create_directory, apply_patch, tool_call
	TargetPath    string         `json:"target_path"`
	Content       string         `json:"content,omitempty"`
	Preview       string         `json:"preview,omitempty"`
	// ToolName and Input preserve the original tool call for MutationToolCall
	// approvals, so mutation.Executor can re-invoke the exact same tool with
	// the exact same input after a human approves it.
	ToolName string         `json:"tool_name,omitempty"`
	Input    map[string]any `json:"input,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	ExpiresAt     *time.Time     `json:"expires_at,omitempty"`
	ApprovedBy    string         `json:"approved_by,omitempty"`
	ApprovedAt    *time.Time     `json:"approved_at,omitempty"`
	DeniedBy      string         `json:"denied_by,omitempty"`
	DeniedAt      *time.Time     `json:"denied_at,omitempty"`
	DenialReason  string         `json:"denial_reason,omitempty"`
	Policy        PolicyDecision `json:"policy"`
}

// PolicyDecision captures the policy outcome that led to this approval request.
type PolicyDecision struct {
	Decision string `json:"decision"` // requires_approval, approved, denied
	Reason   string `json:"reason"`
}

// NewApprovalID generates a unique approval ID.
// Format: appr_<ulid>
func NewApprovalID() string {
	id := ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader)
	return fmt.Sprintf("appr_%s", id.String())
}

// NewApproval creates a new Approval in pending status.
func NewApproval(runID, correlationID, requestedBy, project, mutationType, targetPath, content string, policy PolicyDecision) *Approval {
	now := time.Now().UTC()
	var preview string
	if content != "" {
		// Show first 500 chars of content as preview
		previewLen := len(content)
		if previewLen > 500 {
			previewLen = 500
		}
		preview = content[:previewLen]
		if len(content) > 500 {
			preview += "..."
		}
	}

	return &Approval{
		ApprovalID:    NewApprovalID(),
		RunID:         runID,
		CorrelationID: correlationID,
		Status:        StatusPending,
		RequestedBy:   requestedBy,
		Project:       project,
		MutationType:  mutationType,
		TargetPath:    targetPath,
		Content:       content,
		Preview:       preview,
		CreatedAt:     now,
		Policy:        policy,
	}
}

// Approve transitions the approval from pending to approved.
// Returns an error if the status is not pending or is already denied.
func (a *Approval) Approve(approvedBy string) error {
	if strings.TrimSpace(approvedBy) == "" {
		return fmt.Errorf("approved_by is required")
	}
	if a.Status == StatusDenied {
		return fmt.Errorf("cannot approve approval %s: already denied", a.ApprovalID)
	}
	if a.Status == StatusApproved {
		return fmt.Errorf("cannot approve approval %s: already approved", a.ApprovalID)
	}
	if a.Status == StatusExpired {
		return fmt.Errorf("cannot approve approval %s: already expired", a.ApprovalID)
	}
	if a.Status != StatusPending {
		return fmt.Errorf("cannot approve approval %s: status is %q", a.ApprovalID, a.Status)
	}

	now := time.Now().UTC()
	a.Status = StatusApproved
	a.ApprovedBy = approvedBy
	a.ApprovedAt = &now
	return nil
}

// Deny transitions the approval from pending to denied.
// Returns an error if the status is not pending or is already approved.
func (a *Approval) Deny(deniedBy, reason string) error {
	if strings.TrimSpace(deniedBy) == "" {
		return fmt.Errorf("denied_by is required")
	}
	if a.Status == StatusApproved {
		return fmt.Errorf("cannot deny approval %s: already approved", a.ApprovalID)
	}
	if a.Status == StatusDenied {
		return fmt.Errorf("cannot deny approval %s: already denied", a.ApprovalID)
	}
	if a.Status == StatusExpired {
		return fmt.Errorf("cannot deny approval %s: already expired", a.ApprovalID)
	}
	if a.Status != StatusPending {
		return fmt.Errorf("cannot deny approval %s: status is %q", a.ApprovalID, a.Status)
	}

	now := time.Now().UTC()
	a.Status = StatusDenied
	a.DeniedBy = deniedBy
	a.DeniedAt = &now
	a.DenialReason = reason
	return nil
}
