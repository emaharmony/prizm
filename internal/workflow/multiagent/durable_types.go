package multiagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

// DurableRunSchemaVersion is the serialized recovery-envelope version.
const DurableRunSchemaVersion = 1

// CheckpointPhase identifies the only safe recovery points in Phase 1.
type CheckpointPhase string

const (
	CheckpointPendingRole       CheckpointPhase = "pending_role"
	CheckpointRoleRunning       CheckpointPhase = "role_running"
	CheckpointTransitionPending CheckpointPhase = "transition_pending"
	CheckpointWaiting           CheckpointPhase = "waiting"
	CheckpointTerminal          CheckpointPhase = "terminal"
)

// Valid reports whether the phase is recognized by this runtime.
func (p CheckpointPhase) Valid() bool {
	switch p {
	case CheckpointPendingRole,
		CheckpointRoleRunning,
		CheckpointTransitionPending,
		CheckpointWaiting,
		CheckpointTerminal:
		return true
	default:
		return false
	}
}

// PendingTransition is a completed, accounted role result whose deterministic
// transition has not yet been applied.
type PendingTransition struct {
	ExecutionKey string             `json:"execution_key"`
	Resolved     ResolvedTransition `json:"resolved"`
	Handoff      *HandoffDraft      `json:"handoff,omitempty"`
}

// WaitingState records an external condition that pauses safe advancement.
type WaitingState struct {
	Kind                string    `json:"kind"`
	Reason              string    `json:"reason"`
	SafeToRetry         bool      `json:"safe_to_retry"`
	Since               time.Time `json:"since"`
	ProposalID          string    `json:"proposal_id,omitempty"`
	ApprovalID          string    `json:"approval_id,omitempty"`
	ChildID             string    `json:"child_id,omitempty"`
	DelegationID        string    `json:"delegation_id,omitempty"`
	DeliveryKey         string    `json:"delivery_key,omitempty"`
	CommandEventID      string    `json:"command_event_id,omitempty"`
	CorrelationID       string    `json:"correlation_id,omitempty"`
	DeliveryAttemptID   string    `json:"delivery_attempt_id,omitempty"`
	DeliveryNumber      int       `json:"delivery_number,omitempty"`
	AttemptID           string    `json:"attempt_id,omitempty"`
	LastOutcomeSequence int64     `json:"last_outcome_sequence,omitempty"`
	StartedAt           time.Time `json:"started_at,omitempty"`
	Deadline            time.Time `json:"deadline,omitempty"`
	DispatchPending     bool      `json:"dispatch_pending,omitempty"`
	JoinID              string    `json:"join_id,omitempty"`
}

// ProposalDecision is the approval authority's decision for one exact
// proposal. Unknown values fail closed.
type ProposalDecision string

const (
	ProposalPending ProposalDecision = "pending"
	ProposalGranted ProposalDecision = "granted"
	ProposalDenied  ProposalDecision = "denied"
	ProposalExpired ProposalDecision = "expired"
)

// ProposalReconciliation describes whether a previously-started application
// can be proven complete after process interruption.
type ProposalReconciliation string

const (
	ProposalNotApplied ProposalReconciliation = "not_applied"
	ProposalApplied    ProposalReconciliation = "applied"
	ProposalAmbiguous  ProposalReconciliation = "ambiguous"
)

// ProposalOperation is the stable request passed to approval/application
// authorities. ApplyKey is deterministic for the run and proposal.
type ProposalOperation struct {
	RunID        string `json:"run_id"`
	ProposalID   string `json:"proposal_id"`
	ApprovalID   string `json:"approval_id"`
	ApplyKey     string `json:"apply_key"`
	WorkspaceID  string `json:"workspace_id"`
	ExecutionKey string `json:"execution_key"`
}

// ProposalApplyResult is bounded application evidence retained in the run.
type ProposalApplyResult struct {
	Success            bool                  `json:"success"`
	TargetPath         string                `json:"target_path,omitempty"`
	Message            string                `json:"message,omitempty"`
	DiffPath           string                `json:"diff_path,omitempty"`
	DiffStat           string                `json:"diff_stat,omitempty"`
	ReconcileCode      ProposalReconcileCode `json:"reconcile_code,omitempty"`
	ObservedTree       string                `json:"observed_tree,omitempty"`
	InspectionStage    string                `json:"inspection_stage,omitempty"`
	InspectionCategory string                `json:"inspection_category,omitempty"`
	InspectionExitCode int                   `json:"inspection_exit_code,omitempty"`
}

func validInspectionStage(stage string) bool {
	switch stage {
	case "gitdir_discovery", "temp_index_create", "temp_index_close", "temp_index_remove", "read_tree", "stage_worktree", "write_tree":
		return true
	default:
		return false
	}
}

func validInspectionCategory(category string) bool {
	switch category {
	case "cancelled", "permission", "unavailable", "command_failed", "io_failed":
		return true
	default:
		return false
	}
}

// ProposalReconcileCode is a bounded diagnostic from the trusted tree checker.
// It intentionally excludes raw Git errors, paths, and file contents.
type ProposalReconcileCode string

const (
	ProposalReconcilePlanMissing      ProposalReconcileCode = "patch_plan_missing"
	ProposalReconcileInspectFailed    ProposalReconcileCode = "worktree_inspection_failed"
	ProposalReconcileContextCancelled ProposalReconcileCode = "context_cancelled"
	ProposalReconcileExpectedTree     ProposalReconcileCode = "expected_tree_present"
	ProposalReconcileBaseTree         ProposalReconcileCode = "base_tree_present"
	ProposalReconcileTreeMismatch     ProposalReconcileCode = "tree_mismatch"
)

// ProposalLifecycle composes existing approval and mutation authorities. The
// workflow owns sequencing; the implementation owns approval and application
// semantics at their existing boundaries.
type ProposalLifecycle interface {
	Decision(context.Context, ProposalOperation) (ProposalDecision, string, error)
	Apply(context.Context, ProposalOperation) (ProposalApplyResult, error)
	Reconcile(context.Context, ProposalOperation) (ProposalReconciliation, ProposalApplyResult, error)
}

// ProposalProgress is the durable per-proposal apply checkpoint.
type ProposalProgress struct {
	ProposalOperation
	Phase    string               `json:"phase"`
	Attempts int                  `json:"attempts,omitempty"`
	Result   *ProposalApplyResult `json:"result,omitempty"`
}

// ApprovedTaskState retains a completed role result while its exact mutation
// proposals wait for approval and recoverable application.
type ApprovedTaskState struct {
	Role           Role                 `json:"role"`
	Result         RoleRunResult        `json:"result"`
	StartedAt      time.Time            `json:"started_at"`
	FinishedAt     time.Time            `json:"finished_at"`
	Proposals      []ProposalProgress   `json:"proposals"`
	FanOutAdoption *FanOutAdoptionState `json:"fan_out_adoption,omitempty"`
}

// FanOutAdoptionState is a trusted implementation-lane result retained across
// approval and application. Its source identity is rechecked before the
// result can satisfy the immediate Developer successor.
type FanOutAdoptionState struct {
	RunID               string              `json:"run_id"`
	Task                TaskReference       `json:"task"`
	WorkflowID          string              `json:"workflow_id"`
	WorkflowFingerprint string              `json:"workflow_fingerprint"`
	WorkspaceID         string              `json:"workspace_id"`
	ParentRole          Role                `json:"parent_role"`
	ParentExecutionKey  string              `json:"parent_execution_key"`
	ParentVisit         int                 `json:"parent_visit"`
	JoinID              string              `json:"join_id"`
	ChildID             string              `json:"child_id"`
	ChildExecutionKey   string              `json:"child_execution_key"`
	DelegationID        string              `json:"delegation_id"`
	DeliveryKey         string              `json:"delivery_key"`
	DeliveryAttemptID   string              `json:"delivery_attempt_id"`
	DeliveryNumber      int                 `json:"delivery_number"`
	AttemptID           string              `json:"attempt_id"`
	AttemptNumber       int                 `json:"attempt_number"`
	RecoveryNumber      int                 `json:"recovery_number"`
	MaxRecoveries       int                 `json:"max_recoveries"`
	CommandEventID      string              `json:"command_event_id"`
	CorrelationID       string              `json:"correlation_id"`
	ExpectedRole        Role                `json:"expected_role"`
	ExpectedNodeID      string              `json:"expected_node_id"`
	ExpectedVisit       int                 `json:"expected_visit"`
	Proposals           []ProposalReference `json:"proposals"`
	Result              RoleRunResult       `json:"result"`
}

// DelegationJoinChild is the durable identity and bounded result state for one
// parallel fan-out lane.
type DelegationJoinChild struct {
	ChildID           string               `json:"child_id"`
	Lane              FanOutLane           `json:"lane"`
	Role              Role                 `json:"role"`
	Task              TaskReference        `json:"task"`
	ExecutionKey      string               `json:"execution_key"`
	DelegationID      string               `json:"delegation_id"`
	JoinID            string               `json:"join_id"`
	DeliveryKey       string               `json:"delivery_key"`
	CommandEventID    string               `json:"command_event_id"`
	CorrelationID     string               `json:"correlation_id"`
	Deadline          time.Time            `json:"deadline"`
	DeliveryAttemptID string               `json:"delivery_attempt_id,omitempty"`
	DeliveryNumber    int                  `json:"delivery_number,omitempty"`
	Retry             *RetryState          `json:"retry,omitempty"`
	DispatchPending   bool                 `json:"dispatch_pending"`
	Status            event.OutcomeStatus  `json:"status"`
	LastSequence      int64                `json:"last_sequence,omitempty"`
	Result            *RoleRunResult       `json:"result,omitempty"`
	Error             string               `json:"error,omitempty"`
	TerminalFailure   *TerminalRoleFailure `json:"terminal_failure,omitempty"`
}

// TerminalRoleFailure is an allowlisted diagnosis for a completed delegated
// attempt. It contains schema-owned identifiers only, never provider output.
type TerminalRoleFailure struct {
	AttemptID       string   `json:"attempt_id,omitempty"`
	Class           string   `json:"class"`
	Code            string   `json:"code"`
	CorrectionField string   `json:"correction_field,omitempty"`
	ExpectedType    string   `json:"expected_type,omitempty"`
	AllowedValues   []string `json:"allowed_values,omitempty"`
	RecoveryNumber  int      `json:"recovery_number"`
	MaxRecoveries   int      `json:"max_recoveries"`
	Retryable       bool     `json:"retryable"`
	Reason          string   `json:"reason"`
}

// TerminalFanOutFailure preserves failed child identity after the active join
// is cleared from a terminal run.
type TerminalFanOutFailure struct {
	JoinID  string              `json:"join_id"`
	ChildID string              `json:"child_id"`
	Lane    FanOutLane          `json:"lane"`
	Role    Role                `json:"role"`
	Failure TerminalRoleFailure `json:"failure"`
}

// DelegationJoinState retains the parent result while three bounded child
// delegations execute in parallel and converge deterministically.
type DelegationJoinState struct {
	JoinID             string                `json:"join_id"`
	ParentExecutionKey string                `json:"parent_execution_key"`
	ParentRole         Role                  `json:"parent_role"`
	ParentResult       RoleRunResult         `json:"parent_result"`
	ParentStartedAt    time.Time             `json:"parent_started_at"`
	ParentFinishedAt   time.Time             `json:"parent_finished_at"`
	StartedAt          time.Time             `json:"started_at"`
	Deadline           time.Time             `json:"deadline"`
	FailurePolicy      string                `json:"failure_policy"`
	Children           []DelegationJoinChild `json:"children"`
}

// PersistedFailure is bounded diagnostic information. It never contains
// prompts, credentials, or provider output.
type PersistedFailure struct {
	Kind           string                  `json:"kind"`
	Message        string                  `json:"message"`
	At             time.Time               `json:"at"`
	AttemptID      string                  `json:"attempt_id,omitempty"`
	Attempt        int                     `json:"attempt,omitempty"`
	RecoveryNumber int                     `json:"recovery_number,omitempty"`
	MaxRecoveries  int                     `json:"max_recoveries,omitempty"`
	FailureClass   string                  `json:"failure_class,omitempty"`
	FailureCode    string                  `json:"failure_code,omitempty"`
	FailureReason  string                  `json:"failure_reason,omitempty"`
	Retryable      bool                    `json:"retryable,omitempty"`
	RetryExhausted bool                    `json:"retry_exhausted,omitempty"`
	FanOutFailures []TerminalFanOutFailure `json:"fanout_failures,omitempty"`
}

// DurableRun is the atomic persistence envelope around canonical RunState.
type DurableRun struct {
	SchemaVersion               int                  `json:"schema_version"`
	Revision                    int64                `json:"revision"`
	State                       RunState             `json:"state"`
	Phase                       CheckpointPhase      `json:"phase"`
	PendingTransition           *PendingTransition   `json:"pending_transition,omitempty"`
	ActiveExecutionKey          string               `json:"active_execution_key,omitempty"`
	LastCompletedExecutionKey   string               `json:"last_completed_execution_key,omitempty"`
	Waiting                     *WaitingState        `json:"waiting,omitempty"`
	Retry                       *RetryState          `json:"retry,omitempty"`
	RetryHistory                []RetryState         `json:"retry_history,omitempty"`
	DelegationJoin              *DelegationJoinState `json:"delegation_join,omitempty"`
	ApprovedTask                *ApprovedTaskState   `json:"approved_task,omitempty"`
	PendingFanOutAdoption       *FanOutAdoptionState `json:"pending_fan_out_adoption,omitempty"`
	ProposalResults             []ProposalProgress   `json:"proposal_results,omitempty"`
	Failure                     *PersistedFailure    `json:"failure,omitempty"`
	Reflections                 []ReflectionRecord   `json:"reflections,omitempty"`
	ReplanCount                 int                  `json:"replan_count,omitempty"`
	PendingDelegationOutcomeAck string               `json:"pending_delegation_outcome_ack,omitempty"`
}

// Validate rejects corrupt, internally contradictory, or future state.
func (r DurableRun) Validate(graph *CompiledGraph) error {
	var problems []string
	if r.SchemaVersion != DurableRunSchemaVersion {
		problems = append(problems, fmt.Sprintf(
			"durable schema_version must be %d", DurableRunSchemaVersion))
	}
	if r.Revision < 0 {
		problems = append(problems, "durable revision must be non-negative")
	}
	if !r.Phase.Valid() {
		problems = append(problems, fmt.Sprintf("unknown checkpoint phase %q", r.Phase))
	}
	if r.Failure != nil {
		seenFailureChildren := make(map[string]struct{}, len(r.Failure.FanOutFailures))
		laneRoles := map[FanOutLane]Role{FanOutResearch: RolePlanner, FanOutImplementation: RoleDeveloper, FanOutReview: RoleReviewer}
		for _, failure := range r.Failure.FanOutFailures {
			if failure.JoinID == "" || failure.ChildID == "" || laneRoles[failure.Lane] == "" || failure.Role != laneRoles[failure.Lane] ||
				!validTerminalRoleFailure(failure.Role, failure.Failure) {
				problems = append(problems, "persisted fan-out failure summary is invalid")
				break
			}
			if _, exists := seenFailureChildren[failure.ChildID]; exists {
				problems = append(problems, "persisted fan-out failure child is duplicated")
				break
			}
			seenFailureChildren[failure.ChildID] = struct{}{}
		}
	}
	if err := r.State.Validate(graph); err != nil {
		problems = append(problems, err.Error())
	}

	switch r.Phase {
	case CheckpointPendingRole:
		if r.State.Status != RunStatusRunning && r.State.Status != RunStatusCreated {
			problems = append(problems, "pending_role checkpoint requires running or accepted state")
		}
		if r.PendingTransition != nil || r.Waiting != nil || r.ApprovedTask != nil {
			problems = append(problems, "pending_role checkpoint cannot contain transition or waiting state")
		}
	case CheckpointRoleRunning:
		roleState := r.State.RoleStates[r.State.CurrentRole]
		if r.State.Status != RunStatusRunning || roleState.Status != RoleStatusRunning {
			problems = append(problems, "role_running checkpoint requires running run and role state")
		}
		if strings.TrimSpace(r.ActiveExecutionKey) == "" {
			problems = append(problems, "role_running checkpoint requires active_execution_key")
		}
	case CheckpointTransitionPending:
		if r.PendingTransition == nil {
			problems = append(problems, "transition_pending checkpoint requires pending_transition")
		} else if r.PendingTransition.ExecutionKey != r.LastCompletedExecutionKey {
			problems = append(problems, "pending transition execution key is not the last completed key")
		}
	case CheckpointWaiting:
		if r.State.Status != RunStatusPaused || r.Waiting == nil {
			problems = append(problems, "waiting checkpoint requires paused run and waiting state")
		}
		if r.Waiting != nil && r.Waiting.Kind == "proposal_approval" && r.ApprovedTask == nil {
			problems = append(problems, "proposal approval wait requires approved_task state")
		}
		if r.Waiting != nil && r.Waiting.Kind == "delegation_outcome" {
			if strings.TrimSpace(r.Waiting.ChildID) == "" || strings.TrimSpace(r.Waiting.DelegationID) == "" ||
				strings.TrimSpace(r.Waiting.DeliveryKey) == "" || strings.TrimSpace(r.Waiting.CommandEventID) == "" ||
				strings.TrimSpace(r.Waiting.CorrelationID) == "" || r.Waiting.StartedAt.IsZero() || r.Waiting.Deadline.IsZero() {
				problems = append(problems, "delegation outcome wait requires exact child, delegation, delivery, command, correlation, and start identity")
			}
			if (r.Waiting.DeliveryAttemptID == "") != (r.Waiting.AttemptID == "") {
				problems = append(problems, "delegation outcome retry identity must be complete")
			}
			if r.Waiting.AttemptID != "" && (r.Retry == nil || r.Retry.AttemptID != r.Waiting.AttemptID ||
				r.Waiting.DeliveryAttemptID != fmt.Sprintf("%s:delivery:%d", r.ActiveExecutionKey, r.Waiting.DeliveryNumber) ||
				r.Waiting.DeliveryKey != fmt.Sprintf("%s:%d", r.Waiting.DelegationID, r.Waiting.DeliveryNumber) ||
				r.Waiting.CommandEventID != event.CommandEventID(r.Waiting.DeliveryKey) ||
				r.Waiting.CorrelationID != fmt.Sprintf("%s:%s:delivery:%d", r.State.RunID, r.ActiveExecutionKey, r.Waiting.DeliveryNumber)) {
				problems = append(problems, "delegation outcome retry identity does not match active role visit")
			}
		}
		if r.Waiting != nil && r.Waiting.Kind == "delegation_join" {
			if r.DelegationJoin == nil {
				problems = append(problems, "delegation join wait requires delegation_join state")
			} else if err := r.DelegationJoin.Validate(r.State.RunID, r.State.CurrentRole); err != nil {
				problems = append(problems, err.Error())
			}
		}
		if r.ApprovedTask != nil {
			if r.ApprovedTask.Role != r.State.CurrentRole || len(r.ApprovedTask.Proposals) == 0 {
				problems = append(problems, "approved_task must belong to the current role and contain proposals")
			}
			if a := r.ApprovedTask.FanOutAdoption; a != nil && !validFanOutAdoptionSource(*a, r.State, graph) {
				problems = append(problems, "approved_task fan-out adoption source scope is invalid")
			}
			for _, proposal := range r.ApprovedTask.Proposals {
				if strings.TrimSpace(proposal.ProposalID) == "" || strings.TrimSpace(proposal.ApprovalID) == "" ||
					proposal.ApplyKey != proposalApplyKey(r.State.RunID, proposal.ProposalID) ||
					(proposal.Phase != "waiting" && proposal.Phase != "applying" && proposal.Phase != "applied") {
					problems = append(problems, "approved_task contains an invalid proposal checkpoint")
					break
				}
				if result := proposal.Result; result != nil && result.ReconcileCode != "" &&
					(!validProposalReconcileCode(result.ReconcileCode) || result.ObservedTree != "" && !validGitTreeID(result.ObservedTree)) {
					problems = append(problems, "approved_task contains an invalid reconciliation diagnostic")
					break
				}
			}
		}
	case CheckpointTerminal:
		if !r.State.Status.Terminal() {
			problems = append(problems, "terminal checkpoint requires terminal run state")
		}
	}
	if a := r.PendingFanOutAdoption; a != nil {
		if r.ApprovedTask != nil || !validFanOutAdoptionSource(*a, r.State, graph) ||
			(a.ExpectedRole != r.State.CurrentRole && r.State.CurrentRole != a.ParentRole) {
			problems = append(problems, "pending fan-out adoption does not match the active or immediate successor role")
		}
		switch r.Phase {
		case CheckpointTransitionPending, CheckpointPendingRole, CheckpointRoleRunning:
		case CheckpointWaiting:
			if r.Waiting == nil || r.Waiting.Kind != "operator_pause" {
				problems = append(problems, "pending fan-out adoption may wait only at an operator pause")
			}
		default:
			problems = append(problems, "pending fan-out adoption is not at a recoverable role boundary")
		}
	}
	if r.DelegationJoin != nil && (r.Phase != CheckpointWaiting || r.Waiting == nil || r.Waiting.Kind != "delegation_join") {
		problems = append(problems, "delegation_join state requires a delegation_join waiting checkpoint")
	}
	if r.Retry != nil {
		roleState := r.State.RoleStates[r.Retry.Role]
		if r.Retry.Role != r.State.CurrentRole || roleState.Visits < 1 ||
			r.Retry.VisitID != roleVisitID(r.State.RunID, r.Retry.Role, roleState.Visits) ||
			r.Retry.ExecutionKey == "" || r.Retry.ExecutionKey != r.ActiveExecutionKey ||
			r.Retry.AttemptID == "" || r.Retry.Number < 1 ||
			r.Retry.RecoveryNumber < 0 || r.Retry.Number != r.Retry.RecoveryNumber+1 ||
			r.Retry.MaxRecoveries < 0 || r.Retry.MaxRecoveries > maxRoleRecoveryRetries ||
			r.Retry.RecoveryNumber > r.Retry.MaxRecoveries {
			problems = append(problems, "retry checkpoint identity or counters are invalid")
		}
		if len(r.Retry.Observations) > 64 || r.Retry.ConsecutiveNoProgress < 0 || r.Retry.ConsecutiveNoProgress > 64 {
			problems = append(problems, "retry no-progress ledger exceeds its bound")
		}
		for _, observation := range r.Retry.Observations {
			if observation.Hash == "" || observation.Count < 1 || observation.Count > 64 {
				problems = append(problems, "retry observation count is invalid")
			}
		}
		if r.Phase != CheckpointRoleRunning && (r.Phase != CheckpointWaiting || r.Waiting == nil) {
			problems = append(problems, fmt.Sprintf("retry state requires role_running or waiting checkpoint (phase=%q waiting=%t)", r.Phase, r.Waiting != nil))
		}
		if r.Retry.MutationPossible && r.Phase == CheckpointWaiting && r.Waiting != nil && r.Waiting.Kind == "role_retry" {
			problems = append(problems, "ambiguous mutation cannot be checkpointed for automatic retry")
		}
		if len(r.Retry.UnsafeMutationIDs) > 128 || len(r.Retry.ProvenNoEffectIDs) > 128 {
			problems = append(problems, "retry mutation ledger exceeds bounded action count")
		}
		for _, mutationID := range append(append([]string(nil), r.Retry.UnsafeMutationIDs...), r.Retry.ProvenNoEffectIDs...) {
			if safeMutationID(mutationID) == "" {
				problems = append(problems, "retry mutation ledger contains a non-hash action id")
				break
			}
		}
	}
	if len(r.RetryHistory) > 32 {
		problems = append(problems, "retry history exceeds bounded visit count")
	}
	if r.Waiting != nil && r.Waiting.Kind == "role_retry" &&
		(r.Retry == nil || !r.Waiting.SafeToRetry || r.Retry.NextAt.IsZero()) {
		problems = append(problems, "role retry wait requires a safe scheduled retry checkpoint")
	}

	if len(problems) != 0 {
		return &ContractError{Problems: problems}
	}
	return nil
}

func validFanOutAdoptionSource(a FanOutAdoptionState, state RunState, graph *CompiledGraph) bool {
	developerNodeID, hasDeveloperNode := graph.NodeIDForRole(RoleDeveloper)
	return a.RunID == state.RunID && a.Task == state.CurrentTask && a.WorkflowID == state.WorkflowID &&
		a.WorkflowFingerprint != "" && a.WorkflowFingerprint == state.WorkflowFingerprint &&
		a.WorkspaceID != "" && a.WorkspaceID == state.WorkspaceID && a.ParentRole == RolePlanner &&
		a.ParentVisit > 0 && a.ParentExecutionKey == roleExecutionKey(state.RunID, a.ParentRole, a.ParentVisit) &&
		a.JoinID == "join:"+a.ParentExecutionKey &&
		a.ChildID == a.Task.ID+":"+string(FanOutImplementation) &&
		a.ChildExecutionKey == a.ParentExecutionKey+":fanout:"+string(FanOutImplementation) &&
		a.DelegationID == "graph:join:"+a.ParentExecutionKey+":"+string(FanOutImplementation) &&
		a.DeliveryNumber >= 0 && a.DeliveryKey == fmt.Sprintf("%s:%d", a.DelegationID, a.DeliveryNumber) &&
		a.DeliveryAttemptID == fmt.Sprintf("%s:delivery:%d", a.ChildExecutionKey, a.DeliveryNumber) &&
		a.AttemptID == retryAttemptID(a.ChildExecutionKey, a.AttemptNumber) && a.AttemptNumber > 0 &&
		a.MaxRecoveries >= 0 && a.RecoveryNumber >= 0 && a.RecoveryNumber <= a.MaxRecoveries &&
		a.AttemptNumber == a.RecoveryNumber+1 && a.AttemptNumber <= a.MaxRecoveries+1 &&
		a.DeliveryNumber <= a.RecoveryNumber &&
		a.CommandEventID == event.CommandEventID(a.DeliveryKey) &&
		a.CorrelationID == fmt.Sprintf("%s:%s:delivery:%d", state.RunID, a.ChildExecutionKey, a.DeliveryNumber) && a.ExpectedRole == RoleDeveloper && hasDeveloperNode &&
		a.ExpectedNodeID == developerNodeID && a.ExpectedVisit > 0 &&
		sameProposalSet(a.Proposals, a.Result.Proposals)
}

func (j DelegationJoinState) Validate(runID string, currentRole Role) error {
	if strings.TrimSpace(j.JoinID) == "" || strings.TrimSpace(j.ParentExecutionKey) == "" ||
		j.ParentRole != currentRole || j.ParentStartedAt.IsZero() || j.ParentFinishedAt.IsZero() ||
		j.StartedAt.IsZero() || j.Deadline.IsZero() || j.FailurePolicy != "wait_for_all_then_fail" {
		return errors.New("delegation join identity or policy is invalid")
	}
	if len(j.Children) != 3 {
		return errors.New("delegation join requires exactly 3 children")
	}
	seen := make(map[string]struct{}, len(j.Children))
	seenLanes := make(map[FanOutLane]struct{}, len(j.Children))
	expectedRoles := map[FanOutLane]Role{FanOutResearch: RolePlanner, FanOutImplementation: RoleDeveloper, FanOutReview: RoleReviewer}
	for _, child := range j.Children {
		if expectedRoles[child.Lane] == "" || child.Role != expectedRoles[child.Lane] {
			return fmt.Errorf("delegation join child %q has invalid lane-role binding", child.ChildID)
		}
		if strings.TrimSpace(child.ChildID) == "" || strings.TrimSpace(child.JoinID) == "" || strings.TrimSpace(child.ExecutionKey) == "" ||
			strings.TrimSpace(child.DelegationID) == "" || strings.TrimSpace(child.DeliveryKey) == "" ||
			strings.TrimSpace(child.CommandEventID) == "" || strings.TrimSpace(child.CorrelationID) == "" ||
			child.Task.ID == "" || child.Deadline.IsZero() {
			return errors.New("delegation join child identity is incomplete")
		}
		if child.JoinID != j.JoinID {
			return fmt.Errorf("delegation join child %q has mismatched join identity", child.ChildID)
		}
		if _, ok := seenLanes[child.Lane]; ok {
			return fmt.Errorf("delegation join lane %q is duplicated", child.Lane)
		}
		seenLanes[child.Lane] = struct{}{}
		if _, ok := seen[child.DeliveryKey]; ok {
			return fmt.Errorf("delegation join delivery key %q is duplicated", child.DeliveryKey)
		}
		seen[child.DeliveryKey] = struct{}{}
		if child.Status != "" && !child.Status.Terminal() && child.Status != event.OutcomeAccepted && child.Status != event.OutcomeProgress {
			return fmt.Errorf("delegation join child %q has invalid status %q", child.ChildID, child.Status)
		}
		if child.Result != nil && child.Result.FanOut != nil {
			return fmt.Errorf("delegation join child %q cannot nest a fan-out plan", child.ChildID)
		}
		if child.TerminalFailure != nil {
			failure := child.TerminalFailure
			if !child.Status.Terminal() || !validTerminalRoleFailure(child.Role, *failure) ||
				(child.Retry != nil && (failure.AttemptID != child.Retry.AttemptID || failure.RecoveryNumber != child.Retry.RecoveryNumber || failure.MaxRecoveries != child.Retry.MaxRecoveries)) {
				return fmt.Errorf("delegation join child %q has invalid terminal failure summary", child.ChildID)
			}
		}
		if child.Retry != nil {
			state := child.Retry
			if state.Role != child.Role || state.ExecutionKey != child.ExecutionKey || state.VisitID != child.ExecutionKey ||
				state.AttemptID != retryAttemptID(state.VisitID, state.Number) || state.Number != state.RecoveryNumber+1 ||
				state.RecoveryNumber < 0 || state.MaxRecoveries < 0 || state.MaxRecoveries > maxRoleRecoveryRetries ||
				state.RecoveryNumber > state.MaxRecoveries || child.DeliveryAttemptID != fmt.Sprintf("%s:delivery:%d", child.ExecutionKey, child.DeliveryNumber) ||
				child.DeliveryNumber < 0 || child.DeliveryKey != fmt.Sprintf("%s:%d", child.DelegationID, child.DeliveryNumber) ||
				child.CommandEventID != event.CommandEventID(child.DeliveryKey) ||
				child.CorrelationID != fmt.Sprintf("%s:%s:delivery:%d", runID, child.ExecutionKey, child.DeliveryNumber) {
				return fmt.Errorf("delegation join child %q retry identity or counters are invalid", child.ChildID)
			}
			if len(state.UnsafeMutationIDs) > 128 || len(state.ProvenNoEffectIDs) > 128 || len(state.Progress) > 24 || len(state.Corrections) > 16 || len(state.Observations) > 64 {
				return fmt.Errorf("delegation join child %q retry ledger exceeds its bound", child.ChildID)
			}
		}
	}
	return nil
}

// StoredEvent is one atomically-enqueued canonical event.
type StoredEvent struct {
	Event    event.Event
	Revision int64
}

// DurableRunStore owns atomic run checkpoints and their event outbox.
type DurableRunStore interface {
	Create(context.Context, DurableRun, []event.Event) (DurableRun, error)
	Load(context.Context, string) (DurableRun, error)
	Checkpoint(context.Context, int64, DurableRun, []event.Event) (DurableRun, error)
	PendingEvents(context.Context, string, int) ([]StoredEvent, error)
	MarkEventsPublished(context.Context, []string) error
	RequestCancellation(context.Context, string, string) error
	CancellationRequest(context.Context, string) (string, bool, error)
	// RequestPause, PauseRequest, and ClearPauseRequest are purely additive
	// operator-pause operations (Phase 2 Milestone 9). They are intentionally
	// separate from RequestCancellation/CancellationRequest, whose signatures
	// and behavior are frozen: a pause, unlike a cancel, must remain
	// resumable and never forces a terminal transition.
	RequestPause(context.Context, string, string) error
	PauseRequest(context.Context, string) (string, bool, error)
	ClearPauseRequest(context.Context, string) error
	Close() error
}

// IdempotentRunAcceptor atomically creates an accepted run and, when present,
// binds its idempotency key to the durable record.
type IdempotentRunAcceptor interface {
	AcceptRun(context.Context, DurableRun, []event.Event) (DurableRun, bool, error)
}

// NonterminalRunLister supplies bounded startup-recovery candidates.
type NonterminalRunLister interface {
	ListNonterminalRuns(context.Context, int) ([]DurableRun, error)
}

// EventPublisher is the narrow canonical event-store operation used by outbox
// reconciliation.
type EventPublisher interface {
	Store(context.Context, event.Event) error
}

// DelegationDispatcher publishes one already-checkpointed graph delegation.
// Implementations must make Dispatch idempotent by Command.IdempotencyKey.
type DelegationDispatcher interface {
	Dispatch(context.Context, string, event.Command) error
}

// DelegationOutcomeSource exposes durable, explicitly acknowledged outcomes.
// The graph runtime acknowledges an outcome only after its state and events are
// checkpointed. MarkOutcomeConsumed must be idempotent for an already-consumed
// event because recovery may repeat the call after an interrupted checkpoint.
type DelegationOutcomeSource interface {
	PendingOutcomes(context.Context, string) ([]event.Outcome, error)
	MarkOutcomeConsumed(context.Context, string) error
}

// DurableDelegationOptions enables graph-owned delegated role execution.
type DurableDelegationOptions struct {
	Subject            string
	Dispatcher         DelegationDispatcher
	Outcomes           DelegationOutcomeSource
	Deadline           time.Duration
	RequireWorkspaceID bool
}

// ExecutionClaim is exclusive ownership of one run.
type ExecutionClaim interface {
	Release() error
}

// RunClaimer prevents two processes from advancing the same run.
type RunClaimer interface {
	Acquire(context.Context, string) (ExecutionClaim, error)
}

var (
	// ErrRunNotFound identifies a missing durable run.
	ErrRunNotFound = errors.New("multiagent: durable run not found")
	// ErrRunExists identifies a duplicate run creation attempt.
	ErrRunExists = errors.New("multiagent: durable run already exists")
	// ErrRevisionConflict identifies a stale checkpoint writer.
	ErrRevisionConflict = errors.New("multiagent: durable revision conflict")
	// ErrRunClaimed identifies another active execution owner.
	ErrRunClaimed = errors.New("multiagent: durable run is already claimed")
	// ErrRunAlreadyTerminal identifies an operator-pause request against a
	// run that has already reached a terminal status; pausing a finished run
	// is a no-op that must not be silently accepted.
	ErrRunAlreadyTerminal = errors.New("multiagent: durable run is already terminal")
)

// RunWaitingError is a non-terminal result: external authority or manual
// reconciliation must act before safe resume.
type RunWaitingError struct {
	RunID  string
	Kind   string
	Reason string
}

func (e *RunWaitingError) Error() string {
	return fmt.Sprintf("multiagent: run %q waiting for %s: %s", e.RunID, e.Kind, e.Reason)
}

// RecoveryFailedError reports a durable state that cannot be advanced safely.
type RecoveryFailedError struct {
	RunID string
	Cause error
}

func (e *RecoveryFailedError) Error() string {
	return fmt.Sprintf("multiagent: recover run %q: %v", e.RunID, e.Cause)
}

// Unwrap exposes the state or persistence failure.
func (e *RecoveryFailedError) Unwrap() error {
	return e.Cause
}

func roleExecutionKey(runID string, role Role, visit int) string {
	return fmt.Sprintf("%s:%s:%d", runID, role, visit)
}
