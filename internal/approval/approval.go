// Package approval implements Prizm V4's approval-gated mutation model.
// Approvals are the gate between an agent proposing a file change and Prizm
// actually performing it. Every approval is persisted as a JSON artifact and
// every state transition is emitted as an event.
package approval

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/gitx"
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

const (
	SubjectMutation  = "mutation"
	SubjectIntention = "intention"
)

// PolicyDecision constants.
const (
	DecisionRequiresApproval = "requires_approval"
	DecisionApproved         = "approved"
	DecisionDenied           = "denied"
)

// Approval represents a pending, approved, or denied file mutation.
type Approval struct {
	// SubjectType separates ordinary mutation authorization from proposal-only
	// Freewill intention authorization. Empty is accepted only for legacy
	// mutation records written before this discriminator existed.
	SubjectType   string                `json:"subject_type,omitempty"`
	Intention     *IntentionApprovalRef `json:"intention,omitempty"`
	ProposalID    string                `json:"proposal_id,omitempty"`
	ApprovalID    string                `json:"approval_id"`
	RunID         string                `json:"run_id"`
	CorrelationID string                `json:"correlation_id"`
	Status        string                `json:"status"` // pending, approved, denied, expired
	RequestedBy   string                `json:"requested_by"`
	Project       string                `json:"project"`
	MutationType  string                `json:"mutation_type"` // write_file, create_directory, apply_patch, tool_call
	TargetPath    string                `json:"target_path"`
	Content       string                `json:"content,omitempty"`
	Preview       string                `json:"preview,omitempty"`
	// ToolName and Input preserve the original tool call for MutationToolCall
	// approvals, so mutation.Executor can re-invoke the exact same tool with
	// the exact same input after a human approves it.
	ToolName     string         `json:"tool_name,omitempty"`
	Input        map[string]any `json:"input,omitempty"`
	PatchPlan    *PatchPlan     `json:"patch_plan,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	ExpiresAt    *time.Time     `json:"expires_at,omitempty"`
	ApprovedBy   string         `json:"approved_by,omitempty"`
	ApprovedAt   *time.Time     `json:"approved_at,omitempty"`
	DeniedBy     string         `json:"denied_by,omitempty"`
	DeniedAt     *time.Time     `json:"denied_at,omitempty"`
	DenialReason string         `json:"denial_reason,omitempty"`
	Policy       PolicyDecision `json:"policy"`
}

// IntentionApprovalRef binds an approval to one canonical, workflow-owned
// intention request. It deliberately contains no executable task/tool data.
type IntentionApprovalRef struct {
	RequestID          string   `json:"request_id"`
	RequestFingerprint string   `json:"request_fingerprint"`
	ControlGeneration  uint64   `json:"control_generation,omitempty"`
	ScopeSummary       string   `json:"scope_summary"`
	Capabilities       []string `json:"capabilities,omitempty"`
}

func NewIntentionApproval(runID, correlationID, requestedBy, project string, ref IntentionApprovalRef, policy PolicyDecision) (*Approval, error) {
	if decoded, err := hex.DecodeString(ref.RequestFingerprint); err != nil || len(decoded) != 32 {
		return nil, fmt.Errorf("intention approval fingerprint is invalid")
	}
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(ref.RequestID) == "" ||
		strings.TrimSpace(ref.ScopeSummary) == "" ||
		len(ref.ScopeSummary) > 512 || len(ref.Capabilities) > 16 {
		return nil, fmt.Errorf("intention approval identity is invalid")
	}
	for _, capability := range ref.Capabilities {
		if strings.TrimSpace(capability) == "" || len(capability) > 64 || strings.ContainsAny(capability, "\r\n\x00") {
			return nil, fmt.Errorf("intention approval capability is invalid")
		}
	}
	a := NewApproval(runID, correlationID, requestedBy, project, "", "", "", policy)
	a.SubjectType = SubjectIntention
	a.Intention = &ref
	return a, nil
}

// ValidateSubject rejects mixed authority: intention records cannot carry
// mutation payloads, and unknown subject kinds fail closed.
func (a *Approval) ValidateSubject() error {
	if a == nil {
		return fmt.Errorf("approval is required")
	}
	switch a.SubjectType {
	case "", SubjectMutation:
		if a.Intention != nil {
			return fmt.Errorf("mutation approval cannot contain an intention subject")
		}
	case SubjectIntention:
		if a.Intention == nil || a.MutationType != "" || a.TargetPath != "" || a.Content != "" || a.ToolName != "" || len(a.Input) != 0 || a.PatchPlan != nil {
			return fmt.Errorf("intention approval has mixed subject data")
		}
		if _, err := NewIntentionApproval(a.RunID, a.CorrelationID, a.RequestedBy, a.Project, *a.Intention, a.Policy); err != nil {
			return err
		}
	default:
		return fmt.Errorf("approval subject type is unsupported")
	}
	return nil
}

// PatchPlan is the immutable identity of one validated multi-file patch.
// It is persisted with the approval so application and restart recovery can
// prove the exact pre- and post-mutation repository trees.
type PatchPlan struct {
	BaseSHA      string                 `json:"base_sha"`
	BaseTree     string                 `json:"base_tree"`
	PlanningTree string                 `json:"planning_tree,omitempty"`
	ExpectedTree string                 `json:"expected_tree"`
	PatchSHA256  string                 `json:"patch_sha256"`
	Paths        []string               `json:"paths"`
	Basis        *gitx.TrustedTreeBasis `json:"basis,omitempty"`
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

// NewProposalID generates the identity of the proposed mutation. It remains
// distinct from the authorization decision that may later approve it.
func NewProposalID() string {
	id := ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader)
	return fmt.Sprintf("prop_%s", id.String())
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
		ProposalID:    NewProposalID(),
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
