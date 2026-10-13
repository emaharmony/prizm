package api

import (
	"context"
	"errors"
)

var (
	ErrProposalDecisionConflict = errors.New("proposal decision conflicts with durable run state")
	ErrProposalNotWaiting       = errors.New("run is not waiting for this proposal")
)

// MultiAgentProposal is the bounded inspection view needed to review the
// exact proposed mutation before a dashboard decision.
type MultiAgentProposal struct {
	RunID        string   `json:"run_id"`
	ProposalID   string   `json:"proposal_id"`
	ApprovalID   string   `json:"approval_id"`
	Status       string   `json:"status"`
	MutationType string   `json:"mutation_type"`
	TargetPath   string   `json:"target_path,omitempty"`
	BaseSHA      string   `json:"base_sha,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	Patch        string   `json:"patch,omitempty"`
}

// MultiAgentController is the narrow, transport-facing seam the mutating
// multi-agent run routes (POST .../resume, .../cancel, .../pause) call
// through. It intentionally knows nothing about agents, providers, tools, or
// how a live runtime is assembled — that composition lives in cmd/prism-cli,
// which implements this interface by reusing its existing,
// already-tested run-opening helpers (openLiveReferenceRuntime for Resume,
// RunLocator.OpenInspection for Cancel/Pause). Keeping the interface this
// narrow lets the API package depend only on the shape of the operation, not
// on how it is wired.
type MultiAgentController interface {
	// Proposal reads the exact pending proposal from durable run state and its
	// existing approval artifact. It must not advance or execute the run.
	Proposal(ctx context.Context, runID, proposalID string) (MultiAgentProposal, error)
	// DecideProposal persists a decision for an exact proposal attached to the
	// run's current durable approval wait. The implementation must validate all
	// run/proposal/approval identities before changing the approval store.
	DecideProposal(ctx context.Context, runID, proposalID, approvalID, decision, reason string) (resume bool, err error)
	// Resume re-invokes the role runner for a paused/waiting run. Because a
	// resume can run for a long time (it may execute one or more further
	// roles), implementations are expected to return once the resume has
	// been durably kicked off rather than blocking for the full run — the
	// caller observes progress and completion via the existing SSE stream.
	Resume(ctx context.Context, runID string) error
	// Cancel persists an operator cancellation request. An active owner
	// observes it at the next role boundary; an idle run becomes terminal
	// immediately.
	Cancel(ctx context.Context, runID, reason string) error
	// Pause persists an operator pause request. Unlike Cancel, a paused run
	// never becomes terminal and remains resumable.
	Pause(ctx context.Context, runID, reason string) error
}
