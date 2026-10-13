package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

// referenceMultiAgentController implements api.MultiAgentController by
// reusing this package's existing, already-tested run-opening helpers: it
// closes over the same cfg/run-dir/config-path values `prism serve` already
// builds. It knows nothing about the transport layer (internal/api never
// imports cmd/prism-cli); wiring happens the other direction, in
// cmd_serve.go, which passes a *referenceMultiAgentController satisfying
// api.MultiAgentController into api.Config.
type referenceMultiAgentController struct {
	runDir     string
	configPath string
	decisionMu sync.Mutex
}

// newReferenceMultiAgentController builds the controller `prism serve` wires
// into api.Config.MultiAgentController.
func newReferenceMultiAgentController(runDir, configPath string) *referenceMultiAgentController {
	return &referenceMultiAgentController{runDir: runDir, configPath: configPath}
}

// Cancel and Pause do not need live agent wiring: both only persist a flag
// checked at the next role boundary, so they reuse RunLocator's read-only-
// safe inspection path (the same one the dashboard's GET endpoints already
// use), never constructing a live agent/provider/tool registry.
func (c *referenceMultiAgentController) Cancel(ctx context.Context, runID, reason string) error {
	locator := multiagent.RunLocator{Root: c.runDir}
	runtime, store, eventStore, err := locator.OpenInspection(ctx, runID)
	if err != nil {
		return err
	}
	defer store.Close()
	defer eventStore.Close()
	_, err = runtime.Cancel(ctx, runID, reason)
	return err
}

// Pause reuses the same read-only-safe inspection path as Cancel: it never
// forces a state transition itself, so it needs no live role runner either.
func (c *referenceMultiAgentController) Pause(ctx context.Context, runID, reason string) error {
	locator := multiagent.RunLocator{Root: c.runDir}
	runtime, store, eventStore, err := locator.OpenInspection(ctx, runID)
	if err != nil {
		return err
	}
	defer store.Close()
	defer eventStore.Close()
	_, err = runtime.Pause(ctx, runID, reason)
	return err
}

// Proposal returns only the proposal tied to this run's current persisted wait.
// It opens the inspection runtime only and never constructs a provider or
// executes a role.
func (c *referenceMultiAgentController) Proposal(ctx context.Context, runID, proposalID string) (api.MultiAgentProposal, error) {
	_, item, err := c.loadWaitingProposal(ctx, runID, proposalID, "")
	if err != nil {
		return api.MultiAgentProposal{}, err
	}
	view := api.MultiAgentProposal{
		RunID: runID, ProposalID: proposalID, ApprovalID: item.ApprovalID,
		Status: item.Status, MutationType: item.MutationType, TargetPath: item.TargetPath,
	}
	if item.PatchPlan != nil {
		view.BaseSHA = item.PatchPlan.BaseSHA
		view.Paths = append([]string(nil), item.PatchPlan.Paths...)
	}
	if item.MutationType == approval.MutationApplyPatch {
		view.Patch = item.Content
	}
	return view, nil
}

// DecideProposal records a decision only for the exact proposal and approval
// in the run's current durable proposal wait. The lock serializes decisions
// within the serving process; Resume's normal durable claim serializes any
// subsequent execution across API, scheduler, and restarted processes.
func (c *referenceMultiAgentController) DecideProposal(ctx context.Context, runID, proposalID, approvalID, decision, reason string) (bool, error) {
	c.decisionMu.Lock()
	defer c.decisionMu.Unlock()

	record, item, err := c.loadProposalRecord(ctx, runID, proposalID, approvalID)
	if err != nil {
		return false, err
	}
	activeWait := record.State.Status == multiagent.RunStatusPaused && record.Phase == multiagent.CheckpointWaiting &&
		record.Waiting != nil && record.Waiting.Kind == "proposal_approval" &&
		record.Waiting.ProposalID == proposalID && record.Waiting.ApprovalID == approvalID && record.ApprovedTask != nil
	requestedStatus := approval.StatusApproved
	if decision == "deny" {
		requestedStatus = approval.StatusDenied
	} else if decision != "grant" {
		return false, api.ErrProposalDecisionConflict
	}
	if item.Status == requestedStatus && !activeWait {
		// The exact decision is already durable and this run has moved beyond
		// its approval wait. Never resume a completed or independently paused
		// run just because a caller repeated its old decision.
		return false, nil
	}
	if !activeWait {
		return false, api.ErrProposalNotWaiting
	}
	switch decision {
	case "grant":
		if item.Status == approval.StatusApproved {
			return true, nil // same decision at the wait boundary resumes safely
		}
		if item.Status != approval.StatusPending {
			return false, api.ErrProposalDecisionConflict
		}
		if err := item.Approve("dashboard-operator"); err != nil {
			return false, api.ErrProposalDecisionConflict
		}
	case "deny":
		if item.Status == approval.StatusDenied {
			return true, nil // same decision at the wait boundary resumes safely
		}
		if item.Status != approval.StatusPending {
			return false, api.ErrProposalDecisionConflict
		}
		denial := strings.TrimSpace(reason)
		if denial == "" {
			denial = "denied by dashboard operator"
		}
		if len(denial) > 512 {
			return false, api.ErrProposalDecisionConflict
		}
		if err := item.Deny("dashboard-operator", denial); err != nil {
			return false, api.ErrProposalDecisionConflict
		}
	default:
		return false, api.ErrProposalDecisionConflict
	}
	if err := approval.NewStore(c.runDir).Save(item); err != nil {
		return false, err
	}
	return true, nil
}

func (c *referenceMultiAgentController) loadWaitingProposal(ctx context.Context, runID, proposalID, approvalID string) (multiagent.DurableRun, *approval.Approval, error) {
	record, item, err := c.loadProposalRecord(ctx, runID, proposalID, approvalID)
	if err != nil {
		return record, nil, err
	}
	if record.State.Status != multiagent.RunStatusPaused || record.Phase != multiagent.CheckpointWaiting ||
		record.Waiting == nil || record.Waiting.Kind != "proposal_approval" || record.ApprovedTask == nil ||
		record.Waiting.ProposalID != proposalID || record.Waiting.ApprovalID != item.ApprovalID {
		return record, nil, api.ErrProposalNotWaiting
	}
	return record, item, nil
}

func (c *referenceMultiAgentController) loadProposalRecord(ctx context.Context, runID, proposalID, approvalID string) (multiagent.DurableRun, *approval.Approval, error) {
	locator := multiagent.RunLocator{Root: c.runDir}
	runtime, runStore, eventStore, err := locator.OpenInspection(ctx, runID)
	if err != nil {
		return multiagent.DurableRun{}, nil, err
	}
	defer runStore.Close()
	defer eventStore.Close()
	record, err := runtime.Inspect(ctx, runID)
	if err != nil {
		return record, nil, err
	}
	matched := false
	var storedApprovalID string
	if record.ApprovedTask != nil {
		for _, progress := range record.ApprovedTask.Proposals {
			if progress.RunID == runID && progress.ProposalID == proposalID && (approvalID == "" || progress.ApprovalID == approvalID) {
				matched, storedApprovalID = true, progress.ApprovalID
				break
			}
		}
	}
	if !matched {
		for _, progress := range record.ProposalResults {
			if progress.RunID == runID && progress.ProposalID == proposalID && (approvalID == "" || progress.ApprovalID == approvalID) {
				matched, storedApprovalID = true, progress.ApprovalID
				break
			}
		}
	}
	if !matched {
		return record, nil, api.ErrProposalDecisionConflict
	}
	item, err := approval.NewStore(c.runDir).Load(runID, storedApprovalID)
	if err != nil || item.RunID != runID || item.ProposalID != proposalID || item.ApprovalID != storedApprovalID || (approvalID != "" && item.ApprovalID != approvalID) {
		return record, nil, api.ErrProposalDecisionConflict
	}
	return record, item, nil
}

// Resume needs live agent wiring, since DurableRuntime.Resume actually
// re-invokes the role runner. It loads the run's manifest (the same way
// executeReferenceWorkflowResume does), builds a fully-wired live runtime via
// the existing openLiveReferenceRuntime, and then drives Resume in a
// detached goroutine so the HTTP handler that called this does not block for
// a potentially multi-minute run: the goroutine intentionally uses
// context.Background(), not ctx, because ctx belongs to the HTTP request and
// would be cancelled the moment that request/response cycle completes, which
// happens (by design) well before Resume finishes.
//
// Resume validates synchronously — by opening the manifest and the live
// runtime before returning — so a caller still gets an immediate, accurate
// error for an unknown run, a claim conflict, or a broken config/agent
// wiring; only the actual Resume() execution is detached.
func (c *referenceMultiAgentController) Resume(ctx context.Context, runID string) error {
	manifest, err := loadReferenceManifest(c.runDir, runID)
	if err != nil {
		return err
	}
	rt, err := openLiveReferenceRuntime(c.runDir, c.configPath, manifest)
	if err != nil {
		return fmt.Errorf("multiagent: open live runtime for run %q: %w", runID, err)
	}
	go func() {
		defer rt.close()
		// context.Background(): this resume must outlive the HTTP
		// request/response cycle that triggered it.
		if _, resumeErr := rt.runtime.Resume(context.Background(), runID); resumeErr != nil {
			// Not returned to the caller: the HTTP handler has already
			// responded by the time this completes. Progress/failure is
			// observable via the run's own SSE event stream; this log line
			// is only a local operator breadcrumb, matching how prism serve
			// logs other detached background failures (e.g. the API server
			// goroutine in cmd_serve.go).
			log.Printf("[WARN] multiagent resume for run %s ended: %v", runID, resumeErr)
		}
	}()
	return nil
}

// resumeSync is used by the serve-owned graph worker. Keeping the runtime open
// until Resume returns lets deadline and outcome supervisors serialize a run
// locally while the durable run claimer arbitrates across processes.
func (c *referenceMultiAgentController) resumeSync(ctx context.Context, runID string) error {
	manifest, err := loadReferenceManifest(c.runDir, runID)
	if err != nil {
		return err
	}
	rt, err := openLiveReferenceRuntime(c.runDir, c.configPath, manifest)
	if err != nil {
		return fmt.Errorf("multiagent: open live runtime for run %q: %w", runID, err)
	}
	defer rt.close()
	_, err = rt.runtime.Resume(ctx, runID)
	return err
}
