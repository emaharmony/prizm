package multiagent

import (
	"context"
	"fmt"
	"time"

	"github.com/emaharmony/prism/internal/cost"
	"github.com/emaharmony/prism/internal/event"
	"github.com/emaharmony/prism/internal/validation"
)

// LoopKind identifies a bounded correction edge in the Phase 1 flow.
type LoopKind string

const (
	LoopTesterToDeveloper   LoopKind = "tester_to_developer"
	LoopReviewerToDeveloper LoopKind = "reviewer_to_developer"
)

// LoopTraversalCounts records completed correction-loop traversals.
type LoopTraversalCounts struct {
	TesterToDeveloper   int `json:"tester_to_developer"`
	ReviewerToDeveloper int `json:"reviewer_to_developer"`
}

// Get returns the recorded traversals for kind.
func (c LoopTraversalCounts) Get(kind LoopKind) int {
	switch kind {
	case LoopTesterToDeveloper:
		return c.TesterToDeveloper
	case LoopReviewerToDeveloper:
		return c.ReviewerToDeveloper
	default:
		return 0
	}
}

func (c *LoopTraversalCounts) increment(kind LoopKind) {
	switch kind {
	case LoopTesterToDeveloper:
		c.TesterToDeveloper++
	case LoopReviewerToDeveloper:
		c.ReviewerToDeveloper++
	}
}

// RunView is the intentionally limited state visible to a role runner.
// It contains no mutation methods and no references to supervisor-owned maps.
type RunView struct {
	RunID           string
	WorkflowID      string
	Task            TaskReference
	WorkspaceID     string
	CurrentRole     Role
	ExecutionKey    string
	Visit           int
	TransitionCount int
	LoopTraversals  LoopTraversalCounts
	BudgetUsage     BudgetUsage
	IncomingHandoff *Handoff
}

// RoleRunRequest is the complete, read-only input for one role visit.
type RoleRunRequest struct {
	Run        RunView
	RoleConfig RoleConfig
}

// HandoffDraft contains role-produced context without routing authority.
// The supervisor assigns source, destination, outcome, run, and timestamp.
type HandoffDraft struct {
	ID                string
	TaskRef           string
	Objective         string
	Artifacts         []ArtifactRef
	Evidence          []ArtifactRef
	Reason            string
	ValidationResults []validation.Result
	UnresolvedIssues  []Issue
	Notes             string
}

// ExecutionMetadata is observational data returned by a runner.
type ExecutionMetadata struct {
	AgentRef         string
	Provider         string
	Model            string
	WorkspaceID      string
	ToolCalls        int
	DeniedToolCalls  int
	ValidationStatus string
	ApprovalStatus   string
	Attempt          int
	StartedAt        time.Time
	FinishedAt       time.Time
}

// RoleRunResult is the bounded result of one role visit.
type RoleRunResult struct {
	Outcome         TransitionOutcome
	OutgoingHandoff *HandoffDraft
	TokenUsage      cost.TokenUsage
	LocalIterations int
	Retries         int
	Metadata        ExecutionMetadata
}

// RoleRunner executes bounded local work for one configured role. It cannot
// mutate global state or choose a destination role.
type RoleRunner interface {
	RunRole(context.Context, RoleRunRequest) (RoleRunResult, error)
}

// EventSink consumes canonical Prism events emitted by the supervisor.
type EventSink interface {
	Emit(event.Event)
}

// EventSinkFunc adapts a function to EventSink.
type EventSinkFunc func(event.Event)

// Emit implements EventSink.
func (f EventSinkFunc) Emit(evt event.Event) {
	if f != nil {
		f(evt)
	}
}

// RunRequest supplies identity and task context for a new in-memory run.
type RunRequest struct {
	RunID string
	Task  TaskReference
}

// ResolvedTransition is a supervisor-selected edge or terminal result.
type ResolvedTransition struct {
	From     Role              `json:"from"`
	Outcome  TransitionOutcome `json:"outcome"`
	To       Role              `json:"to,omitempty"`
	Terminal TerminalCondition `json:"terminal,omitempty"`
}

// InvalidTransitionError reports a role/outcome pair with no declared route.
type InvalidTransitionError struct {
	Role    Role
	Outcome TransitionOutcome
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("multiagent: no transition from role %q for outcome %q", e.Role, e.Outcome)
}

// RoleExecutionError reports a failed or invalid runner execution.
type RoleExecutionError struct {
	Role  Role
	Cause error
}

func (e *RoleExecutionError) Error() string {
	return fmt.Sprintf("multiagent: role %q execution failed: %v", e.Role, e.Cause)
}

// Unwrap exposes the underlying runner or contract error.
func (e *RoleExecutionError) Unwrap() error {
	return e.Cause
}

// BudgetExceededError identifies the deterministic ceiling that stopped a run.
type BudgetExceededError struct {
	Budget string
	Role   Role
	Used   int
	Limit  int
}

func (e *BudgetExceededError) Error() string {
	if e.Role != "" {
		return fmt.Sprintf(
			"multiagent: budget %q exhausted for role %q: used %d, limit %d",
			e.Budget,
			e.Role,
			e.Used,
			e.Limit,
		)
	}
	return fmt.Sprintf("multiagent: budget %q exhausted: used %d, limit %d", e.Budget, e.Used, e.Limit)
}
