package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/mutation"
	"github.com/emaharmony/prizm/internal/safety"
	"github.com/emaharmony/prizm/internal/validation"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

// approvalProposalResolver binds proposals created by one tool-loop execution
// to the exact durable role checkpoint using its correlation/execution key.
type approvalProposalResolver struct {
	store     *approval.Store
	workspace string
}

func (r approvalProposalResolver) ResolveProposals(_ context.Context, query multiagent.ProposalQuery) ([]multiagent.ProposalReference, error) {
	approvals, err := r.store.List(query.RunID)
	if err != nil {
		return nil, err
	}
	refs := make([]multiagent.ProposalReference, 0)
	for _, item := range approvals {
		if item.CorrelationID != query.ExecutionKey || item.RequestedBy != query.AgentID {
			continue
		}
		proposalID := item.ProposalID
		if proposalID == "" { // schema-v1 approval compatibility
			proposalID = item.ApprovalID
		}
		artifact, artifactErr := canonicalProposalArtifact(item, r.workspace)
		if artifactErr != nil {
			return nil, artifactErr
		}
		refs = append(refs, multiagent.ProposalReference{ProposalID: proposalID, ApprovalID: item.ApprovalID, Artifacts: []multiagent.ArtifactRef{artifact}})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ProposalID < refs[j].ProposalID })
	return refs, nil
}

func canonicalProposalArtifact(item *approval.Approval, workspace string) (multiagent.ArtifactRef, error) {
	if item == nil || item.MutationType != approval.MutationWriteFile {
		return multiagent.ArtifactRef{}, fmt.Errorf("proposal does not identify a canonical file artifact")
	}
	if workspace == "" {
		return multiagent.ArtifactRef{}, fmt.Errorf("proposal workspace is required")
	}
	absolute, err := safety.ResolveAndContain(workspace, item.TargetPath)
	if err != nil {
		return multiagent.ArtifactRef{}, fmt.Errorf("resolve proposal target: %w", err)
	}
	relative, err := filepath.Rel(workspace, absolute)
	if err != nil || relative == "." || filepath.IsAbs(relative) {
		return multiagent.ArtifactRef{}, fmt.Errorf("canonicalize proposal target %q", item.TargetPath)
	}
	return multiagent.ArtifactRef{Kind: multiagent.ArtifactFile, URI: filepath.ToSlash(relative)}, nil
}

// approvalProposalLifecycle composes the existing approval store and mutation
// executor behind the workflow-owned durable sequencing contract.
type approvalProposalLifecycle struct {
	store        *approval.Store
	executor     *mutation.Executor
	workspace    string
	artifactRoot string
}

func newApprovalProposalLifecycle(store *approval.Store, workspace, artifactRoot string) approvalProposalLifecycle {
	return approvalProposalLifecycle{store: store, executor: mutation.NewExecutor(workspace, store, workspace), workspace: workspace, artifactRoot: artifactRoot}
}

func (l approvalProposalLifecycle) load(op multiagent.ProposalOperation) (*approval.Approval, error) {
	item, err := l.store.Load(op.RunID, op.ApprovalID)
	if err != nil {
		return nil, err
	}
	proposalID := item.ProposalID
	if proposalID == "" { // schema-v1 approval compatibility
		proposalID = item.ApprovalID
	}
	if item.ApprovalID != op.ApprovalID || item.RunID != op.RunID || op.ProposalID != proposalID {
		return nil, fmt.Errorf("approval identity does not match proposal checkpoint")
	}
	return item, nil
}

func (l approvalProposalLifecycle) Decision(_ context.Context, op multiagent.ProposalOperation) (multiagent.ProposalDecision, string, error) {
	item, err := l.load(op)
	if err != nil {
		return "", "", err
	}
	switch item.Status {
	case approval.StatusPending:
		return multiagent.ProposalPending, "waiting for operator decision", nil
	case approval.StatusApproved:
		return multiagent.ProposalGranted, "approved by " + item.ApprovedBy, nil
	case approval.StatusDenied:
		return multiagent.ProposalDenied, item.DenialReason, nil
	case approval.StatusExpired:
		return multiagent.ProposalExpired, "approval expired", nil
	default:
		return "", "", fmt.Errorf("unknown approval status %q", item.Status)
	}
}

func (l approvalProposalLifecycle) Apply(ctx context.Context, op multiagent.ProposalOperation) (multiagent.ProposalApplyResult, error) {
	item, err := l.load(op)
	if err != nil {
		return multiagent.ProposalApplyResult{}, err
	}
	if item.Status != approval.StatusApproved {
		return multiagent.ProposalApplyResult{}, fmt.Errorf("approval %s is not granted", op.ApprovalID)
	}
	result, err := l.executor.ApplyWithRun(ctx, op.RunID, op.ApprovalID, item.ApprovedBy)
	if err != nil {
		return multiagent.ProposalApplyResult{}, err
	}
	return l.withDiffEvidence(op, multiagent.ProposalApplyResult{Success: result.Success, TargetPath: result.TargetPath, Message: result.Message}), nil
}

func (l approvalProposalLifecycle) Reconcile(_ context.Context, op multiagent.ProposalOperation) (multiagent.ProposalReconciliation, multiagent.ProposalApplyResult, error) {
	item, err := l.load(op)
	if err != nil {
		return "", multiagent.ProposalApplyResult{}, err
	}
	result := multiagent.ProposalApplyResult{TargetPath: item.TargetPath}
	switch item.MutationType {
	case approval.MutationWriteFile:
		path, resolveErr := safety.ResolveAndContainMulti([]string{l.workspace}, item.TargetPath)
		if resolveErr != nil {
			return "", result, resolveErr
		}
		content, readErr := os.ReadFile(path)
		if os.IsNotExist(readErr) {
			return multiagent.ProposalNotApplied, result, nil
		}
		if readErr != nil {
			return multiagent.ProposalAmbiguous, result, nil
		}
		if bytes.Equal(content, []byte(item.Content)) {
			result.Success, result.Message = true, "approved content present"
			return multiagent.ProposalApplied, l.withDiffEvidence(op, result), nil
		}
		return multiagent.ProposalNotApplied, result, nil
	case approval.MutationCreateDirectory:
		path, resolveErr := safety.ResolveAndContainMulti([]string{l.workspace}, item.TargetPath)
		if resolveErr != nil {
			return "", result, resolveErr
		}
		info, statErr := os.Stat(path)
		if os.IsNotExist(statErr) {
			return multiagent.ProposalNotApplied, result, nil
		}
		if statErr != nil || !info.IsDir() {
			return multiagent.ProposalAmbiguous, result, nil
		}
		result.Success, result.Message = true, "approved directory present"
		return multiagent.ProposalApplied, l.withDiffEvidence(op, result), nil
	default:
		// External tool effects cannot be inferred from approval status alone.
		return multiagent.ProposalAmbiguous, result, nil
	}
}

func (l approvalProposalLifecycle) withDiffEvidence(op multiagent.ProposalOperation, result multiagent.ProposalApplyResult) multiagent.ProposalApplyResult {
	diff, diffErr := gitx.RunCommand(context.Background(), l.workspace, "", "git", "diff", "--binary")
	stat, statErr := gitx.RunCommand(context.Background(), l.workspace, "", "git", "diff", "--stat")
	if diffErr != nil || statErr != nil {
		return result
	}
	dir := filepath.Join(l.artifactRoot, op.RunID, "approved-task")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return result
	}
	path := filepath.Join(dir, op.ApplyKey+".patch")
	if err := os.WriteFile(path, []byte(diff), 0o644); err != nil {
		return result
	}
	result.DiffPath, result.DiffStat = path, stat
	return result
}

// workspaceValidationRunner creates an executor per call so the allowlisted
// profile is rooted in the run's persisted isolated workspace.
type workspaceValidationRunner struct {
	registry     *validation.Registry
	artifactRoot string
	fallbackRoot string
}

func (r workspaceValidationRunner) RunValidation(ctx context.Context, profile, correlationID string) (*validation.Result, error) {
	return validation.NewExecutor(r.registry, r.fallbackRoot, filepath.Join(r.artifactRoot, correlationID)).Run(ctx, profile, correlationID)
}

func (r workspaceValidationRunner) RunValidationInWorkspace(ctx context.Context, profile, correlationID string, workspace multiagent.Workspace) (*validation.Result, error) {
	return validation.NewExecutor(r.registry, workspace.Path, filepath.Join(r.artifactRoot, correlationID)).Run(ctx, profile, correlationID)
}
