package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

const freewillStrategyExperimentSource = "prizm:freewill-strategy-experiment"

var errFreewillStrategyComparisonMissing = errors.New("freewill: strategy comparison is not available")

type freewillStrategyDurableRunReader interface {
	Load(context.Context, string) (multiagent.DurableRun, error)
}

type freewillStrategyApprovalReader interface {
	List(string) ([]*approval.Approval, error)
}

type freewillStrategyExperimentSink struct {
	appraisals  *freewillTaskAppraisalSink
	runs        freewillStrategyDurableRunReader
	approvals   freewillStrategyApprovalReader
	ownerID     string
	workspace   string
	agentID     string
	strategyIDs [2]string
	versions    [2]string
	patchSHA256 [2]string
}

func newFreewillStrategyExperimentSink(appraisals *freewillTaskAppraisalSink, runs freewillStrategyDurableRunReader, approvals freewillStrategyApprovalReader, ownerID, workspace, agentID string, strategyIDs, versions, patchSHA256s []string) (*freewillStrategyExperimentSink, error) {
	if appraisals == nil || appraisals.facts == nil || appraisals.facts.store == nil || runs == nil || approvals == nil || ownerID == "" || ownerID != appraisals.ownerID || ownerID != strings.TrimSpace(ownerID) ||
		strings.TrimSpace(workspace) == "" || strings.TrimSpace(agentID) == "" || len(strategyIDs) != 2 || len(versions) != 2 || len(patchSHA256s) != 2 || strategyIDs[0] == "" || strategyIDs[1] == "" || strategyIDs[0] == strategyIDs[1] || patchSHA256s[0] == patchSHA256s[1] ||
		!validFreewillFactSHA256(patchSHA256s[0]) || !validFreewillFactSHA256(patchSHA256s[1]) {
		return nil, errors.New("freewill: strategy comparison requires local receipts, durable runs, approvals, owner, and two fixed variants")
	}
	canonical, err := canonicalFreewillWorkspace(workspace)
	if err != nil || canonical != appraisals.facts.workspace {
		return nil, errors.New("freewill: strategy experiment must use the exact configured workspace")
	}
	for i := 0; i < 2; i++ {
		if strings.TrimSpace(strategyIDs[i]) != strategyIDs[i] || strings.TrimSpace(versions[i]) == "" || strings.TrimSpace(versions[i]) != versions[i] {
			return nil, errors.New("freewill: fixed strategy IDs and versions must be exact")
		}
	}
	return &freewillStrategyExperimentSink{appraisals: appraisals, runs: runs, approvals: approvals, ownerID: ownerID, workspace: canonical, agentID: agentID,
		strategyIDs: [2]string{strategyIDs[0], strategyIDs[1]}, versions: [2]string{versions[0], versions[1]}, patchSHA256: [2]string{strings.ToLower(patchSHA256s[0]), strings.ToLower(patchSHA256s[1])}}, nil
}

func freewillStrategyExperimentID(ownerID, projectID, workspaceID, agentID string, taskIDs []string) string {
	pair := append([]string(nil), taskIDs...)
	sort.Strings(pair)
	return "fwexp-" + freewillBindingDigest("freewill-strategy-experiment/v1", ownerID, projectID, workspaceID, agentID, pair[0], pair[1])
}

func freewillStrategyExperimentScope(ownerID, projectID, agentID, experimentID string) memory.Scope {
	return memory.Scope{UserID: ownerID, IncludeUserScope: true, ProjectID: projectID, TaskID: experimentID, AgentID: agentID, CorrelationID: experimentID}
}

func (s *freewillStrategyExperimentSink) Finalize(ctx context.Context, ownerID, projectID, workspace, agentID string, taskIDs []string) (api.FreewillStrategyExperimentRecord, error) {
	if err := s.validateScope(ownerID, projectID, workspace, agentID, taskIDs); err != nil {
		return api.FreewillStrategyExperimentRecord{}, err
	}
	experimentID := freewillStrategyExperimentID(ownerID, projectID, s.appraisals.facts.workspaceID, agentID, taskIDs)
	scope := freewillStrategyExperimentScope(ownerID, projectID, agentID, experimentID)
	if existing, err := s.loadRecord(ctx, scope, experimentID); err == nil {
		return s.revalidate(ctx, existing, ownerID, projectID, agentID, taskIDs)
	} else if !errors.Is(err, errFreewillStrategyComparisonMissing) {
		return api.FreewillStrategyExperimentRecord{}, err
	}
	record, err := s.buildRecord(ctx, ownerID, projectID, agentID, taskIDs, experimentID)
	if err != nil {
		return api.FreewillStrategyExperimentRecord{}, err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return api.FreewillStrategyExperimentRecord{}, err
	}
	_, _, err = s.appraisals.facts.store.Capture(ctx, memory.CaptureRequest{Scope: scope, Content: string(encoded), Summary: "Bounded Freewill strategy comparison", Category: "strategy_experiment", Tier: "persist", Source: freewillStrategyExperimentSource, CaptureKey: "freewill-strategy-experiment/v1:" + experimentID})
	if err != nil {
		return api.FreewillStrategyExperimentRecord{}, err
	}
	return record, nil
}

func (s *freewillStrategyExperimentSink) ReadStrategyExperiment(ctx context.Context, ownerID, projectID, workspace, agentID string, taskIDs []string) (api.FreewillStrategyExperimentRecord, error) {
	if err := s.validateScope(ownerID, projectID, workspace, agentID, taskIDs); err != nil {
		return api.FreewillStrategyExperimentRecord{}, err
	}
	experimentID := freewillStrategyExperimentID(ownerID, projectID, s.appraisals.facts.workspaceID, agentID, taskIDs)
	record, err := s.loadRecord(ctx, freewillStrategyExperimentScope(ownerID, projectID, agentID, experimentID), experimentID)
	if err != nil {
		return api.FreewillStrategyExperimentRecord{}, err
	}
	return s.revalidate(ctx, record, ownerID, projectID, agentID, taskIDs)
}

func (s *freewillStrategyExperimentSink) validateScope(ownerID, projectID, workspace, agentID string, taskIDs []string) error {
	if s == nil || s.appraisals == nil || s.appraisals.facts == nil || ownerID != s.ownerID || projectID != s.appraisals.facts.projectID || agentID != s.agentID || len(taskIDs) != 2 || taskIDs[0] == "" || taskIDs[1] == "" || taskIDs[0] != strings.TrimSpace(taskIDs[0]) || taskIDs[1] != strings.TrimSpace(taskIDs[1]) || taskIDs[0] == taskIDs[1] {
		return errors.New("freewill: strategy experiment scope or task pair is unavailable")
	}
	canonical, err := canonicalFreewillWorkspace(workspace)
	if err != nil || canonical != s.workspace {
		return errors.New("freewill: strategy experiment workspace is outside the configured binding")
	}
	return nil
}

func (s *freewillStrategyExperimentSink) loadRecord(ctx context.Context, scope memory.Scope, experimentID string) (api.FreewillStrategyExperimentRecord, error) {
	entries, err := s.appraisals.facts.store.Local.ListRecent(ctx, 0)
	if err != nil {
		return api.FreewillStrategyExperimentRecord{}, err
	}
	var found *api.FreewillStrategyExperimentRecord
	for _, entry := range entries {
		if entry.UserID != scope.UserID || entry.ProjectID != scope.ProjectID || entry.AgentID != scope.AgentID || entry.Source != freewillStrategyExperimentSource || entry.Category != "strategy_experiment" {
			continue
		}
		var record api.FreewillStrategyExperimentRecord
		decodeErr := json.Unmarshal([]byte(entry.Content), &record)
		if decodeErr != nil || record.Version != "freewill-strategy-experiment/v1" || record.ExperimentID != experimentID {
			return api.FreewillStrategyExperimentRecord{}, fmt.Errorf("freewill: stored strategy comparison is invalid decode=%v bytes=%d version=%q id=%q expected=%q", decodeErr, len(entry.Content), record.Version, record.ExperimentID, experimentID)
		}
		if found != nil {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: duplicate strategy comparisons are ambiguous")
		}
		copy := record
		found = &copy
	}
	if found == nil {
		return api.FreewillStrategyExperimentRecord{}, errFreewillStrategyComparisonMissing
	}
	return *found, nil
}

func (s *freewillStrategyExperimentSink) revalidate(ctx context.Context, stored api.FreewillStrategyExperimentRecord, ownerID, projectID, agentID string, taskIDs []string) (api.FreewillStrategyExperimentRecord, error) {
	workspaceID := s.appraisals.facts.workspaceID
	if stored.OwnerID != ownerID || stored.ProjectID != projectID || stored.WorkspaceID != workspaceID || stored.AgentID != agentID || stored.ExperimentID != freewillStrategyExperimentID(ownerID, projectID, workspaceID, agentID, taskIDs) {
		return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: stored strategy comparison is outside requested scope")
	}
	current, err := s.buildRecord(ctx, ownerID, projectID, agentID, taskIDs, stored.ExperimentID)
	if err != nil {
		return api.FreewillStrategyExperimentRecord{}, err
	}
	if len(stored.Runs) != 2 || stored.SourceSnapshotTree != current.SourceSnapshotTree || stored.TitleGoalBindingSHA256 != current.TitleGoalBindingSHA256 || stored.Comparison != current.Comparison || stored.Uncertainty != current.Uncertainty {
		return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: stored strategy comparison binding is invalid")
	}
	for i := range current.Runs {
		if stored.Runs[i] != current.Runs[i] {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: strategy comparison receipt citation no longer matches host evidence")
		}
	}
	return stored, nil
}

type freewillStrategySourceRun struct {
	receipt memory.Memory
	outcome freewillTaskOutcome
	parent  multiagent.DurableRun
	title   string
	goal    string
	patch   *approval.Approval
}

func (s *freewillStrategyExperimentSink) buildRecord(ctx context.Context, ownerID, projectID, agentID string, taskIDs []string, experimentID string) (api.FreewillStrategyExperimentRecord, error) {
	all, err := s.appraisals.facts.store.Local.ListRecent(ctx, 0)
	if err != nil {
		return api.FreewillStrategyExperimentRecord{}, err
	}
	byTask := make(map[string]freewillStrategySourceRun, 2)
	for _, entry := range all {
		if entry.UserID != ownerID || entry.ProjectID != projectID || entry.AgentID != agentID || entry.Source != freewillTaskOutcomeSource || entry.Category != "fact" {
			continue
		}
		var outcome freewillTaskOutcome
		if json.Unmarshal([]byte(entry.Content), &outcome) != nil || outcome.Kind != "host_task_outcome" || outcome.Confidence != "host_validated_terminal_receipt" || outcome.WorkspaceID != s.appraisals.facts.workspaceID {
			continue
		}
		if outcome.TaskID != taskIDs[0] && outcome.TaskID != taskIDs[1] {
			continue
		}
		if _, exists := byTask[outcome.TaskID]; exists {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: configured task has duplicate host terminal receipts")
		}
		if outcome.TaskID != freewillTaskScopeID(projectID, s.workspace, outcome.ChildRunID) || outcome.ParentRunID == "" || outcome.ChildRunID == "" || !validFreewillFactStatus(outcome.Status) || !validFreewillFactValidation(outcome.Validation) || !validFreewillFactSHA256(outcome.ReportSHA256) || outcome.ReportRef != "multiagent_report.json" {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: host terminal receipt is incomplete or has invalid task identity")
		}
		parent, err := s.runs.Load(ctx, outcome.ParentRunID)
		if err != nil {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: host terminal receipt parent run is unavailable")
		}
		if parent.Kind != multiagent.DurableRunKindFreewill || parent.Freewill == nil {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: host terminal receipt parent is not a Freewill run")
		}
		if parent.Freewill.Scope.ProjectID != projectID || !freewillStrategyWorkspaceMatches(parent.Freewill.Scope.Workspace, s.workspace) {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: host terminal receipt parent scope differs from configured project/workspace")
		}
		if parent.State.RequestIdentity == nil || parent.State.RequestIdentity.RequesterID != agentID {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: host terminal receipt parent requester differs from configured agent")
		}
		if parent.Freewill.ExecutionDispatch == nil || parent.Freewill.ExecutionDispatch.ChildRunID != outcome.ChildRunID {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: host terminal receipt is not linked to the exact child dispatch")
		}
		if parent.Freewill.ApprovalReceipt == nil || parent.Freewill.ApprovalReceipt.Decision != multiagent.FreewillApprovalGranted {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: host terminal receipt is not linked to an approved durable intention")
		}
		var chosen *freewill.CandidateIntention
		for i := range parent.Freewill.Candidates {
			candidate := parent.Freewill.Candidates[i]
			if candidate.CandidateID == parent.Freewill.ChoiceID {
				copy := candidate
				chosen = &copy
				break
			}
		}
		if chosen == nil || chosen.Request.Scope.ProjectID != projectID || !freewillStrategyWorkspaceMatches(chosen.Request.Scope.Workspace, s.workspace) || chosen.Request.AgentID != agentID || strings.TrimSpace(chosen.Request.Title) == "" || strings.TrimSpace(chosen.Request.Goal) == "" || parent.Freewill.StrategyID == "" || parent.Freewill.StrategyVersion == "" {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: approved intention has no exact host-configured strategy binding")
		}
		variantIndex := -1
		for i := 0; i < 2; i++ {
			if parent.Freewill.StrategyID == s.strategyIDs[i] && parent.Freewill.StrategyVersion == s.versions[i] {
				variantIndex = i
			}
		}
		if variantIndex < 0 {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: run strategy differs from the configured fixed variant set")
		}
		if parent.Freewill.ExpectedPatchSHA256 != s.patchSHA256[variantIndex] {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: durable wake patch fingerprint differs from the host-predeclared variant")
		}
		patches, err := s.approvals.List(outcome.ChildRunID)
		if err != nil {
			return api.FreewillStrategyExperimentRecord{}, err
		}
		var patch *approval.Approval
		for _, candidate := range patches {
			if candidate != nil && candidate.MutationType == approval.MutationApplyPatch && candidate.Status == approval.StatusApproved && candidate.PatchPlan != nil {
				if patch != nil {
					return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: strategy run has more than one approved patch")
				}
				patch = candidate
			}
		}
		if patch == nil || patch.PatchPlan.PatchSHA256 == "" || patch.PatchPlan.BaseSHA == "" || patch.PatchPlan.BaseTree == "" || strings.ToLower(patch.PatchPlan.PatchSHA256) != s.patchSHA256[variantIndex] {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: strategy run lacks one exact owner-approved patch plan")
		}
		baseTree, err := gitx.RunCommand(ctx, s.workspace, "", "git", "rev-parse", patch.PatchPlan.BaseSHA+"^{tree}")
		if err != nil || strings.TrimSpace(baseTree) != patch.PatchPlan.BaseTree {
			return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: approved patch base tree cannot be verified from the configured repository")
		}
		byTask[outcome.TaskID] = freewillStrategySourceRun{receipt: entry, outcome: outcome, parent: parent, title: chosen.Request.Title, goal: chosen.Request.Goal, patch: patch}
	}
	if len(byTask) != 2 {
		return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: both exact host terminal receipts are required")
	}
	one, okOne := byTask[taskIDs[0]]
	two, okTwo := byTask[taskIDs[1]]
	sameParents, sameChildren, sameTitle, sameGoal, sameBaseTree, distinctPatchHashes := false, false, false, false, false, false
	if okOne && okTwo {
		sameParents = one.outcome.ParentRunID == two.outcome.ParentRunID
		sameChildren = one.outcome.ChildRunID == two.outcome.ChildRunID
		sameTitle = one.title == two.title
		sameGoal = one.goal == two.goal
		sameBaseTree = one.patch.PatchPlan.BaseTree == two.patch.PatchPlan.BaseTree
		distinctPatchHashes = one.patch.PatchPlan.PatchSHA256 != two.patch.PatchPlan.PatchSHA256
	}
	if !okOne || !okTwo || sameParents || sameChildren || !sameTitle || !sameGoal || !sameBaseTree || !distinctPatchHashes {
		return api.FreewillStrategyExperimentRecord{}, fmt.Errorf("freewill: run pair binding mismatch (one=%t two=%t different_parents=%t different_children=%t same_title=%t same_goal=%t same_base_tree=%t distinct_patch_hashes=%t)", okOne, okTwo, !sameParents, !sameChildren, sameTitle, sameGoal, sameBaseTree, distinctPatchHashes)
	}
	if one.parent.Freewill.StrategyID == two.parent.Freewill.StrategyID {
		return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: runs must carry both predeclared strategy variants")
	}
	objectiveBytes, err := json.Marshal(struct {
		Title string `json:"title"`
		Goal  string `json:"goal"`
	}{Title: one.title, Goal: one.goal})
	if err != nil {
		return api.FreewillStrategyExperimentRecord{}, err
	}
	goalSum := sha256.Sum256(objectiveBytes)
	goalHash := hex.EncodeToString(goalSum[:])
	makeRun := func(source freewillStrategySourceRun) api.FreewillStrategyExperimentRun {
		role := "baseline"
		if source.parent.Freewill.StrategyID == s.strategyIDs[1] {
			role = "alternative"
		}
		return api.FreewillStrategyExperimentRun{TaskID: source.outcome.TaskID, ParentRunID: source.outcome.ParentRunID, ChildRunID: source.outcome.ChildRunID,
			StrategyID: source.parent.Freewill.StrategyID, StrategyVersion: source.parent.Freewill.StrategyVersion, Role: role,
			ReceiptID:    source.receipt.ID,
			ReportSHA256: strings.ToLower(source.outcome.ReportSHA256), ApprovedPatchSHA256: strings.ToLower(source.patch.PatchPlan.PatchSHA256),
			StatusClassification: source.outcome.Status, ValidationClassification: source.outcome.Validation}
	}
	runs := []api.FreewillStrategyExperimentRun{makeRun(one), makeRun(two)}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Role == "baseline" && runs[j].Role != "baseline" })
	comparison := "Host receipt classifications differed; one synthetic run per fixed strategy cannot establish a preference."
	if runs[0].StatusClassification == runs[1].StatusClassification && runs[0].ValidationClassification == runs[1].ValidationClassification {
		comparison = "Host receipt classifications matched; one synthetic run per fixed strategy cannot establish a preference."
	}
	return api.FreewillStrategyExperimentRecord{Version: "freewill-strategy-experiment/v1", ExperimentID: experimentID, OwnerID: ownerID, ProjectID: projectID,
		WorkspaceID: s.appraisals.facts.workspaceID, AgentID: agentID, SourceSnapshotTree: byTask[taskIDs[0]].patch.PatchPlan.BaseTree, TitleGoalBindingSHA256: goalHash, Runs: runs,
		Comparison: comparison, Uncertainty: "one run per fixed variant; outcomes do not establish a repeatable advantage or justify adaptation", RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

func freewillStrategyWorkspaceMatches(value, canonicalWorkspace string) bool {
	canonical, err := canonicalFreewillWorkspace(value)
	return err == nil && canonical == canonicalWorkspace
}

func (r *freewillServeRuntime) Finalize(ctx context.Context, ownerID, projectID, workspace, agentID string, taskIDs []string) (api.FreewillStrategyExperimentRecord, error) {
	if r == nil || r.strategyExperiments == nil {
		return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: strategy experiment finalizer is not configured")
	}
	return r.strategyExperiments.Finalize(ctx, ownerID, projectID, workspace, agentID, taskIDs)
}

func (r *freewillServeRuntime) ReadStrategyExperiment(ctx context.Context, ownerID, projectID, workspace, agentID string, taskIDs []string) (api.FreewillStrategyExperimentRecord, error) {
	if r == nil || r.strategyExperiments == nil {
		return api.FreewillStrategyExperimentRecord{}, errors.New("freewill: strategy experiment reader is not configured")
	}
	return r.strategyExperiments.ReadStrategyExperiment(ctx, ownerID, projectID, workspace, agentID, taskIDs)
}

var _ api.FreewillStrategyExperimentController = (*freewillServeRuntime)(nil)
