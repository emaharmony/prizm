package main

import (
	"bytes"
	"context"
	"errors"
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
		artifacts, artifactErr := canonicalProposalArtifacts(item, r.workspace)
		if artifactErr != nil {
			return nil, artifactErr
		}
		refs = append(refs, multiagent.ProposalReference{ProposalID: proposalID, ApprovalID: item.ApprovalID, Artifacts: artifacts, PatchPlan: gitxPatchPlan(item.PatchPlan)})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ProposalID < refs[j].ProposalID })
	return refs, nil
}

func (r approvalProposalResolver) ValidateTrustedTreeBasis(ctx context.Context, basis gitx.TrustedTreeBasis, workspace multiagent.Workspace) error {
	if r.store == nil || basis.RunID == "" || basis.ApprovalID == "" || basis.ProposalID == "" || basis.ApplyKey == "" || basis.WorkspaceID != workspace.ID ||
		filepath.Clean(r.workspace) != filepath.Clean(workspace.Path) || basis.ExecutionKey == "" || basis.Role != string(multiagent.RoleDeveloper) {
		return fmt.Errorf("trusted patch basis scope is invalid")
	}
	item, err := r.store.Load(basis.RunID, basis.ApprovalID)
	if err != nil {
		return err
	}
	proposalID := item.ProposalID
	if proposalID == "" {
		proposalID = item.ApprovalID
	}
	if item.RunID != basis.RunID || item.ApprovalID != basis.ApprovalID || proposalID != basis.ProposalID ||
		item.CorrelationID != basis.ExecutionKey || item.Status != approval.StatusApproved ||
		item.MutationType != approval.MutationApplyPatch || item.PatchPlan == nil {
		return fmt.Errorf("trusted patch basis does not match an approved patch receipt")
	}
	plan := item.PatchPlan
	if plan.BaseSHA != basis.BaseSHA || plan.BaseTree != basis.BaseTree || plan.ExpectedTree != basis.ExpectedTree ||
		plan.PatchSHA256 != basis.PatchSHA256 || !sameStringPaths(plan.Paths, basis.Paths) {
		return fmt.Errorf("trusted patch basis does not match its stored patch plan")
	}
	head, err := gitx.CurrentSHA(ctx, workspace.Path)
	if err != nil || head != basis.BaseSHA {
		return fmt.Errorf("trusted patch base commit changed")
	}
	tree, err := gitx.WorktreeTree(ctx, workspace.Path)
	if err != nil || tree != basis.ExpectedTree {
		return fmt.Errorf("trusted patch worktree changed")
	}
	return nil
}

func sameStringPaths(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func canonicalProposalArtifacts(item *approval.Approval, workspace string) ([]multiagent.ArtifactRef, error) {
	if item == nil {
		return nil, fmt.Errorf("proposal does not identify canonical file artifacts")
	}
	if workspace == "" {
		return nil, fmt.Errorf("proposal workspace is required")
	}
	paths := []string{item.TargetPath}
	if item.MutationType == approval.MutationApplyPatch {
		if item.PatchPlan == nil || len(item.PatchPlan.Paths) == 0 {
			return nil, fmt.Errorf("patch proposal does not identify canonical file artifacts")
		}
		paths = item.PatchPlan.Paths
	} else if item.MutationType != approval.MutationWriteFile {
		return nil, fmt.Errorf("proposal does not identify canonical file artifacts")
	}
	artifacts := make([]multiagent.ArtifactRef, 0, len(paths))
	// Proposal tools accept either a workspace-relative path or an absolute
	// path already contained by the run workspace. Preserve that tool contract
	// when deriving the canonical, workspace-relative artifact reference.
	for _, target := range paths {
		absolute, err := safety.ResolveAndContainMulti([]string{workspace}, target)
		if err != nil {
			return nil, fmt.Errorf("resolve proposal target: %w", err)
		}
		relative, err := filepath.Rel(workspace, absolute)
		if err != nil || relative == "." || filepath.IsAbs(relative) {
			return nil, fmt.Errorf("canonicalize proposal target %q", target)
		}
		artifacts = append(artifacts, multiagent.ArtifactRef{Kind: multiagent.ArtifactFile, URI: filepath.ToSlash(relative)})
	}
	return artifacts, nil
}

// approvalProposalLifecycle composes the existing approval store and mutation
// executor behind the workflow-owned durable sequencing contract.
type approvalProposalLifecycle struct {
	store           *approval.Store
	executor        *mutation.Executor
	workspace       string
	sourceWorkspace string
	artifactRoot    string
}

func newApprovalProposalLifecycle(store *approval.Store, workspace, artifactRoot string, sourceWorkspace ...string) approvalProposalLifecycle {
	source := ""
	if len(sourceWorkspace) == 1 {
		source = sourceWorkspace[0]
	}
	return approvalProposalLifecycle{store: store, executor: mutation.NewExecutor(workspace, store, workspace), workspace: workspace, sourceWorkspace: source, artifactRoot: artifactRoot}
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
	if op.PatchPlan != nil && (item.PatchPlan == nil || !gitx.EqualPatchPlan(*op.PatchPlan, *gitxPatchPlan(item.PatchPlan))) {
		return nil, fmt.Errorf("approval patch plan does not match proposal checkpoint")
	}
	return item, nil
}

func gitxPatchPlan(plan *approval.PatchPlan) *gitx.PatchPlan {
	if plan == nil {
		return nil
	}
	return &gitx.PatchPlan{BaseSHA: plan.BaseSHA, BaseTree: plan.BaseTree, PlanningTree: plan.PlanningTree,
		ExpectedTree: plan.ExpectedTree, PatchSHA256: plan.PatchSHA256, Paths: append([]string(nil), plan.Paths...),
		Basis: func() *gitx.TrustedTreeBasis {
			if plan.Basis == nil {
				return nil
			}
			basis := *plan.Basis
			basis.Paths = append([]string(nil), plan.Basis.Paths...)
			return &basis
		}(),
	}
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
	if item.MutationType == approval.MutationApplyPatch {
		if item.PatchPlan == nil || l.sourceWorkspace == "" {
			return multiagent.ProposalApplyResult{}, fmt.Errorf("approved patch lacks a trusted run workspace binding")
		}
		ctx, err = gitx.WithVerifiedRunWorktree(ctx, l.workspace, l.sourceWorkspace, op.RunID, item.PatchPlan.BaseSHA)
		if err != nil {
			return multiagent.ProposalApplyResult{}, err
		}
	}
	result, err := l.executor.ApplyWithRun(ctx, op.RunID, op.ApprovalID, item.ApprovedBy)
	if err != nil {
		return multiagent.ProposalApplyResult{}, err
	}
	applyResult := multiagent.ProposalApplyResult{Success: result.Success, TargetPath: result.TargetPath, Message: result.Message}
	if item.MutationType == approval.MutationApplyPatch {
		if !result.Success {
			return l.patchReceipt(item, applyResult), nil
		}
		status, verified, verifyErr := l.Reconcile(ctx, op)
		if verifyErr != nil {
			return multiagent.ProposalApplyResult{}, verifyErr
		}
		if status != multiagent.ProposalApplied || !verified.Success || verified.ReconcileCode != multiagent.ProposalReconcileExpectedTree {
			return verified, fmt.Errorf("approved patch result did not reconcile to its exact expected tree")
		}
		return l.withDiffEvidence(ctx, op, verified), nil
	}
	return l.withDiffEvidence(ctx, op, applyResult), nil
}

func (l approvalProposalLifecycle) Reconcile(ctx context.Context, op multiagent.ProposalOperation) (multiagent.ProposalReconciliation, multiagent.ProposalApplyResult, error) {
	item, err := l.load(op)
	if err != nil {
		return "", multiagent.ProposalApplyResult{}, err
	}
	result := l.patchReceipt(item, multiagent.ProposalApplyResult{TargetPath: item.TargetPath})
	switch item.MutationType {
	case approval.MutationApplyPatch:
		if item.PatchPlan == nil {
			result.ReconcileCode = multiagent.ProposalReconcilePlanMissing
			return multiagent.ProposalAmbiguous, result, nil
		}
		if l.sourceWorkspace == "" {
			result.ReconcileCode = multiagent.ProposalReconcileInspectFailed
			return multiagent.ProposalAmbiguous, result, nil
		}
		trustedCtx, trustErr := gitx.WithVerifiedRunWorktree(ctx, l.workspace, l.sourceWorkspace, op.RunID, item.PatchPlan.BaseSHA)
		if trustErr != nil {
			result.ReconcileCode = multiagent.ProposalReconcileInspectFailed
			return multiagent.ProposalAmbiguous, result, nil
		}
		tree, treeErr := gitx.WorktreeTree(trustedCtx, l.workspace)
		if treeErr != nil {
			if ctx.Err() != nil {
				result.ReconcileCode = multiagent.ProposalReconcileContextCancelled
			} else {
				result.ReconcileCode = multiagent.ProposalReconcileInspectFailed
			}
			var inspectErr *gitx.WorktreeInspectionError
			if errors.As(treeErr, &inspectErr) {
				result.InspectionStage = string(inspectErr.Stage)
				result.InspectionCategory = string(inspectErr.Category)
				if inspectErr.Category == gitx.InspectCommandFailed {
					result.InspectionExitCode = inspectErr.ExitCode
				}
			}
			return multiagent.ProposalAmbiguous, result, nil
		}
		planningTree := item.PatchPlan.PlanningTree
		if planningTree == "" { // schema-v1 clean-base approval compatibility
			planningTree = item.PatchPlan.BaseTree
		}
		switch tree {
		case item.PatchPlan.ExpectedTree:
			result.Success, result.Message = true, "approved patch tree present"
			result.ReconcileCode = multiagent.ProposalReconcileExpectedTree
			result.ObservedTree = tree
			return multiagent.ProposalApplied, l.withDiffEvidence(trustedCtx, op, result), nil
		case planningTree:
			result.ReconcileCode = multiagent.ProposalReconcileBaseTree
			result.ObservedTree = tree
			return multiagent.ProposalNotApplied, result, nil
		default:
			result.ReconcileCode = multiagent.ProposalReconcileTreeMismatch
			result.ObservedTree = tree
			return multiagent.ProposalAmbiguous, result, nil
		}
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
			return multiagent.ProposalApplied, l.withDiffEvidence(ctx, op, result), nil
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
		return multiagent.ProposalApplied, l.withDiffEvidence(ctx, op, result), nil
	default:
		// External tool effects cannot be inferred from approval status alone.
		return multiagent.ProposalAmbiguous, result, nil
	}
}

func (l approvalProposalLifecycle) patchReceipt(item *approval.Approval, result multiagent.ProposalApplyResult) multiagent.ProposalApplyResult {
	if item != nil && item.MutationType == approval.MutationApplyPatch && item.PatchPlan != nil {
		result.PatchPlan = gitxPatchPlan(item.PatchPlan)
	}
	return result
}

func (l approvalProposalLifecycle) withDiffEvidence(ctx context.Context, op multiagent.ProposalOperation, result multiagent.ProposalApplyResult) multiagent.ProposalApplyResult {
	diff, diffErr := gitx.RunCommand(ctx, l.workspace, "", "git", "diff", "--binary")
	stat, statErr := gitx.RunCommand(ctx, l.workspace, "", "git", "diff", "--stat")
	if item, err := l.store.Load(op.RunID, op.ApprovalID); err == nil && item.MutationType == approval.MutationApplyPatch && item.PatchPlan != nil {
		diff = item.Content
		diffErr = nil
		stat, statErr = gitx.RunCommand(context.Background(), l.workspace, item.Content, "git", "apply", "--stat", "-")
	}
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
