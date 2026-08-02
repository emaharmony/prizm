package multiagent

import (
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prism/internal/cost"
)

// RunStateSchemaVersion is the current serialized multi-agent state version.
const RunStateSchemaVersion = 1

// TaskReference identifies the current Prism task without embedding persistence
// implementation details in the workflow contract.
type TaskReference struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
}

// RoleState tracks the explicit lifecycle and accounting for one logical role.
type RoleState struct {
	Role             Role                `json:"role"`
	Status           RoleExecutionStatus `json:"status"`
	Visits           int                 `json:"visits"`
	LocalIterations  int                 `json:"local_iterations"`
	Retries          int                 `json:"retries"`
	TokenUsage       cost.TokenUsage     `json:"token_usage"`
	Elapsed          time.Duration       `json:"elapsed"`
	LastOutcome      TransitionOutcome   `json:"last_outcome,omitempty"`
	EnteredAt        *time.Time          `json:"entered_at,omitempty"`
	UpdatedAt        time.Time           `json:"updated_at"`
	CompletedAt      *time.Time          `json:"completed_at,omitempty"`
	LastError        string              `json:"last_error,omitempty"`
	LastExecutionKey string              `json:"last_execution_key,omitempty"`
	WorkspaceID      string              `json:"workspace_id,omitempty"`
	Artifacts        []ArtifactRef       `json:"artifacts,omitempty"`
	ValidationStatus string              `json:"validation_status,omitempty"`
	ApprovalStatus   string              `json:"approval_status,omitempty"`
}

// BudgetUsage is the accumulated, persisted usage against BudgetLimits.
type BudgetUsage struct {
	Tokens               cost.TokenUsage `json:"tokens"`
	TotalLocalIterations int             `json:"total_local_iterations"`
	Retries              int             `json:"retries"`
	RepeatedFailures     int             `json:"repeated_failures"`
	Elapsed              time.Duration   `json:"elapsed"`
}

// TerminalOutcome records the typed reason a run can no longer transition.
type TerminalOutcome struct {
	Condition TerminalCondition `json:"condition"`
	Reason    string            `json:"reason"`
	At        time.Time         `json:"at"`
}

// RunState is the complete logical state required to persist and resume a
// multi-agent run. PR2 defines the contract; persistence mechanics arrive in
// the recovery phase.
type RunState struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	WorkflowID    string `json:"workflow_id"`
	// WorkflowVersion is, going forward, a registry-assigned internal
	// version (CompiledGraph.RegistryVersion) rather than a user-facing
	// number — see CompiledGraph.registryVersion's doc (compiler.go). It
	// stays an int, additive to the two new fields below, so every
	// historical record (which stamped this from the legacy Definition.Version
	// int field) keeps decoding and comparing correctly via
	// CompatAdaptDefinition, which sets registryVersion to that same
	// Definition.Version value.
	WorkflowVersion int `json:"workflow_version"`
	// WorkflowUserVersion is the new, additive, human-facing version string
	// (CompiledGraph.WorkflowVersion()) — empty for any record persisted
	// before this field existed.
	WorkflowUserVersion string `json:"workflow_user_version,omitempty"`
	// WorkflowFingerprint is the new, additive content fingerprint
	// (CompiledGraph.Fingerprint()) pinning the exact compiled graph this run
	// was created against — empty for any record persisted before this field
	// existed. See RunState.Validate for how it is compared.
	WorkflowFingerprint string              `json:"workflow_fingerprint,omitempty"`
	CurrentRole         Role                `json:"current_role"`
	CurrentTask         TaskReference       `json:"current_task"`
	WorkspaceID         string              `json:"workspace_id,omitempty"`
	Status              RunStatus           `json:"status"`
	RoleStates          map[Role]RoleState  `json:"role_states"`
	TransitionCount     int                 `json:"transition_count"`
	LoopTraversals      LoopTraversalCounts `json:"loop_traversals"`
	BudgetUsage         BudgetUsage         `json:"budget_usage"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
	CompletedAt         *time.Time          `json:"completed_at,omitempty"`
	LatestHandoff       *Handoff            `json:"latest_handoff,omitempty"`
	LatestCompletedRole Role                `json:"latest_completed_role,omitempty"`
	CancellationReason  string              `json:"cancellation_reason,omitempty"`
	TerminalOutcome     *TerminalOutcome    `json:"terminal_outcome,omitempty"`
}

// Validate enforces serialized-state invariants against a compiled graph. It
// performs no persistence or transition.
//
// Before Phase 3 this took the legacy Definition directly; it now takes the
// CompiledGraph every path (authored-YAML-compiled or
// CompatAdaptDefinition-adapted) converges on. The workflow-version-equality
// check is extended, not replaced: it keeps comparing the existing int
// WorkflowVersion field against graph.RegistryVersion() (backward compatible
// by construction — see CompiledGraph.registryVersion's doc), and
// additionally compares WorkflowFingerprint against graph.Fingerprint() only
// when both sides are non-empty, so a record persisted before the fingerprint
// field existed (empty on the state side) or a graph with no registry
// fingerprint yet (empty on the graph side, impossible in practice since
// CompatAdaptDefinition/Compile always set one, but defensive regardless)
// never hard-fails on that half of the check.
func (s RunState) Validate(graph *CompiledGraph) error {
	var problems []string
	if s.SchemaVersion != RunStateSchemaVersion {
		problems = append(problems, fmt.Sprintf("state schema_version must be %d", RunStateSchemaVersion))
	}
	if strings.TrimSpace(s.RunID) == "" {
		problems = append(problems, "state run_id is required")
	}
	if s.WorkflowID != graph.WorkflowID() {
		problems = append(problems, fmt.Sprintf("state workflow_id %q does not match definition %q", s.WorkflowID, graph.WorkflowID()))
	}
	if s.WorkflowVersion != graph.RegistryVersion() {
		problems = append(
			problems,
			fmt.Sprintf("state workflow_version %d does not match definition version %d", s.WorkflowVersion, graph.RegistryVersion()),
		)
	}
	if s.WorkflowFingerprint != "" && graph.Fingerprint() != "" && s.WorkflowFingerprint != graph.Fingerprint() {
		problems = append(problems, fmt.Sprintf(
			"state workflow_fingerprint %q does not match compiled graph fingerprint %q",
			s.WorkflowFingerprint, graph.Fingerprint(),
		))
	}
	if _, exists := graph.RoleConfig(s.CurrentRole); !exists {
		problems = append(problems, fmt.Sprintf("state references unknown current role %q", s.CurrentRole))
	}
	if strings.TrimSpace(s.CurrentTask.ID) == "" {
		problems = append(problems, "state current_task.id is required")
	}
	if !s.Status.Valid() {
		problems = append(problems, fmt.Sprintf("state has unknown run status %q", s.Status))
	}
	if s.TransitionCount < 0 {
		problems = append(problems, "state transition_count must be non-negative")
	}
	if s.CreatedAt.IsZero() {
		problems = append(problems, "state created_at is required")
	}
	if s.UpdatedAt.IsZero() {
		problems = append(problems, "state updated_at is required")
	}
	if !s.CreatedAt.IsZero() && !s.UpdatedAt.IsZero() && s.UpdatedAt.Before(s.CreatedAt) {
		problems = append(problems, "state updated_at must not precede created_at")
	}

	for _, node := range graph.Nodes() {
		if node.Kind != "role" {
			continue
		}
		roleState, exists := s.RoleStates[node.Role]
		if !exists {
			problems = append(problems, fmt.Sprintf("state has no role state for %q", node.Role))
			continue
		}
		validateRoleState(node.Role, roleState, s.WorkspaceID, graph, &problems)
	}
	for role := range s.RoleStates {
		if _, exists := graph.RoleConfig(role); !exists {
			problems = append(problems, fmt.Sprintf("state contains unknown role state %q", role))
		}
	}

	validateBudgetUsage(s.BudgetUsage, &problems)
	// PR6 fix (see the definition registry acceptance test): the pre-PR6
	// check here was the hardcoded, Phase-1-only s.LatestCompletedRole.Valid()
	// (types.go's 4-role switch), which rejected every durable run for any
	// authored WorkflowDefinition using role names outside that fixed
	// vocabulary — handoff.go's own Validate already generalized the
	// equivalent check to graph.HasRole/graph.HasOutcome when CompiledGraph
	// absorbed the legacy TransitionResolver (see handoff.go's "Role/outcome/
	// route legitimacy, generalized" comment); this call site and
	// validateRoleState's LastOutcome check below were the two spots that
	// generalization missed, so a non-legacy-vocabulary graph could compile
	// and run in-memory (Supervisor.Run) but could never actually pass a
	// DurableRuntime checkpoint's RunState.Validate call. Fixed the same way
	// handoff.go already does it: consult the compiled graph's own
	// per-definition vocabulary instead of the hardcoded switch.
	if s.LatestCompletedRole != "" && !graph.HasRole(s.LatestCompletedRole) {
		problems = append(problems, fmt.Sprintf("state has unknown latest_completed_role %q", s.LatestCompletedRole))
	}

	validateLoopTraversalCounts(s.LoopTraversals, &problems)

	if s.LatestHandoff != nil {
		if s.LatestHandoff.RunID != s.RunID {
			problems = append(problems, "state latest_handoff.run_id does not match state run_id")
		}
		if err := s.LatestHandoff.Validate(graph); err != nil {
			problems = append(problems, err.Error())
		}
	}

	if s.Status.Terminal() {
		if s.TerminalOutcome == nil {
			problems = append(problems, "terminal state requires terminal_outcome")
		} else {
			validateTerminalOutcome(s.Status, *s.TerminalOutcome, &problems)
		}
		if s.CompletedAt == nil || s.CompletedAt.IsZero() {
			problems = append(problems, "terminal state requires completed_at")
		}
	} else {
		if s.TerminalOutcome != nil {
			problems = append(problems, "non-terminal state must not have terminal_outcome")
		}
		if s.CompletedAt != nil {
			problems = append(problems, "non-terminal state must not have completed_at")
		}
	}

	if s.Status == RunStatusCancelled && strings.TrimSpace(s.CancellationReason) == "" {
		problems = append(problems, "cancelled state requires cancellation_reason")
	}
	if s.Status != RunStatusCancelled && s.CancellationReason != "" {
		problems = append(problems, "non-cancelled state must not have cancellation_reason")
	}
	if len(problems) > 0 {
		return &ContractError{Problems: problems}
	}
	return nil
}

func validateRoleState(role Role, state RoleState, runWorkspaceID string, graph *CompiledGraph, problems *[]string) {
	if state.Role != role {
		*problems = append(*problems, fmt.Sprintf("role state key %q does not match role field %q", role, state.Role))
	}
	if !state.Status.Valid() {
		*problems = append(*problems, fmt.Sprintf("role %q has unknown status %q", role, state.Status))
	}
	if state.Visits < 0 {
		*problems = append(*problems, fmt.Sprintf("role %q visits must be non-negative", role))
	}
	if state.LocalIterations < 0 {
		*problems = append(*problems, fmt.Sprintf("role %q local_iterations must be non-negative", role))
	}
	if state.Retries < 0 {
		*problems = append(*problems, fmt.Sprintf("role %q retries must be non-negative", role))
	}
	if state.TokenUsage.PromptTokens < 0 ||
		state.TokenUsage.CompletionTokens < 0 ||
		state.TokenUsage.TotalTokens < 0 ||
		state.TokenUsage.EstimatedCostUsd < 0 {
		*problems = append(*problems, fmt.Sprintf("role %q token usage must be non-negative", role))
	}
	if state.Elapsed < 0 {
		*problems = append(*problems, fmt.Sprintf("role %q elapsed must be non-negative", role))
	}
	// See RunState.Validate's LatestCompletedRole fix above for why this
	// consults the compiled graph's own outcome vocabulary (graph.HasOutcome)
	// rather than the hardcoded, Phase-1-only TransitionOutcome.Valid().
	if state.LastOutcome != "" && !graph.HasOutcome(state.LastOutcome) {
		*problems = append(*problems, fmt.Sprintf("role %q has unsupported last_outcome %q", role, state.LastOutcome))
	}
	if state.WorkspaceID != "" {
		switch {
		case runWorkspaceID == "":
			*problems = append(*problems, fmt.Sprintf("role %q workspace_id requires state workspace_id", role))
		case state.WorkspaceID != runWorkspaceID:
			*problems = append(*problems, fmt.Sprintf(
				"role %q workspace_id %q does not match state workspace_id %q",
				role, state.WorkspaceID, runWorkspaceID,
			))
		}
	}
	if state.UpdatedAt.IsZero() {
		for i, artifact := range state.Artifacts {
			if !artifact.Kind.Valid() || strings.TrimSpace(artifact.URI) == "" {
				*problems = append(*problems, fmt.Sprintf("role %q artifact[%d] is invalid", role, i))
			}
		}
		switch state.ValidationStatus {
		case "", "not_required", "passed", "failed", "unavailable":
		default:
			*problems = append(*problems, fmt.Sprintf(
				"role %q has unknown validation_status %q", role, state.ValidationStatus))
		}
		switch state.ApprovalStatus {
		case "", string(ApprovalNotRequired), string(ApprovalPending),
			string(ApprovalApproved), string(ApprovalDenied):
		default:
			*problems = append(*problems, fmt.Sprintf(
				"role %q has unknown approval_status %q", role, state.ApprovalStatus))
		}
		*problems = append(*problems, fmt.Sprintf("role %q updated_at is required", role))
	}
}

func validateBudgetUsage(usage BudgetUsage, problems *[]string) {
	if usage.Tokens.PromptTokens < 0 ||
		usage.Tokens.CompletionTokens < 0 ||
		usage.Tokens.TotalTokens < 0 ||
		usage.Tokens.EstimatedCostUsd < 0 {
		*problems = append(*problems, "state budget token usage must be non-negative")
	}
	if usage.TotalLocalIterations < 0 {
		*problems = append(*problems, "state budget total_local_iterations must be non-negative")
	}
	if usage.Retries < 0 {
		*problems = append(*problems, "state budget retries must be non-negative")
	}
	if usage.RepeatedFailures < 0 {
		*problems = append(*problems, "state budget repeated_failures must be non-negative")
	}
	if usage.Elapsed < 0 {
		*problems = append(*problems, "state budget elapsed must be non-negative")
	}
}

func validateTerminalOutcome(status RunStatus, outcome TerminalOutcome, problems *[]string) {
	if !outcome.Condition.Valid() {
		*problems = append(*problems, fmt.Sprintf("state has unknown terminal condition %q", outcome.Condition))
		return
	}
	if expected := outcome.Condition.runStatus(); expected != status {
		*problems = append(
			*problems,
			fmt.Sprintf("terminal condition %q requires run status %q, got %q", outcome.Condition, expected, status),
		)
	}
	if strings.TrimSpace(outcome.Reason) == "" {
		*problems = append(*problems, "terminal outcome reason is required")
	}
	if outcome.At.IsZero() {
		*problems = append(*problems, "terminal outcome timestamp is required")
	}
}
