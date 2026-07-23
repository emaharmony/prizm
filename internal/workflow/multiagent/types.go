package multiagent

import "time"

// Role identifies the logical responsibility an agent performs in a workflow.
// It is intentionally distinct from an agent or profile ID: deployments may map
// different configured agents to the same logical role.
type Role string

const (
	RolePlanner   Role = "planner"
	RoleDeveloper Role = "developer"
	RoleTester    Role = "tester"
	RoleReviewer  Role = "reviewer"
)

var phase1Roles = [...]Role{
	RolePlanner,
	RoleDeveloper,
	RoleTester,
	RoleReviewer,
}

// Phase1Roles returns the logical roles required by the Phase 1 reference flow.
func Phase1Roles() []Role {
	roles := make([]Role, len(phase1Roles))
	copy(roles, phase1Roles[:])
	return roles
}

// Valid reports whether the role is part of the Phase 1 contract.
func (r Role) Valid() bool {
	switch r {
	case RolePlanner, RoleDeveloper, RoleTester, RoleReviewer:
		return true
	default:
		return false
	}
}

// RoleExecutionStatus is the lifecycle state of one role within a run.
type RoleExecutionStatus string

const (
	RoleStatusPending   RoleExecutionStatus = "pending"
	RoleStatusRunning   RoleExecutionStatus = "running"
	RoleStatusWaiting   RoleExecutionStatus = "waiting"
	RoleStatusCompleted RoleExecutionStatus = "completed"
	RoleStatusFailed    RoleExecutionStatus = "failed"
	RoleStatusBlocked   RoleExecutionStatus = "blocked"
	RoleStatusCancelled RoleExecutionStatus = "cancelled"
)

// Valid reports whether the role execution status is recognized.
func (s RoleExecutionStatus) Valid() bool {
	switch s {
	case RoleStatusPending,
		RoleStatusRunning,
		RoleStatusWaiting,
		RoleStatusCompleted,
		RoleStatusFailed,
		RoleStatusBlocked,
		RoleStatusCancelled:
		return true
	default:
		return false
	}
}

// RunStatus is the lifecycle state of a complete multi-agent run.
type RunStatus string

const (
	RunStatusCreated         RunStatus = "created"
	RunStatusRunning         RunStatus = "running"
	RunStatusPaused          RunStatus = "paused"
	RunStatusCompleted       RunStatus = "completed"
	RunStatusFailed          RunStatus = "failed"
	RunStatusCancelled       RunStatus = "cancelled"
	RunStatusBudgetExhausted RunStatus = "budget_exhausted"
)

// Valid reports whether the run status is recognized.
func (s RunStatus) Valid() bool {
	switch s {
	case RunStatusCreated,
		RunStatusRunning,
		RunStatusPaused,
		RunStatusCompleted,
		RunStatusFailed,
		RunStatusCancelled,
		RunStatusBudgetExhausted:
		return true
	default:
		return false
	}
}

// Terminal reports whether no further role may be entered from this status.
func (s RunStatus) Terminal() bool {
	switch s {
	case RunStatusCompleted, RunStatusFailed, RunStatusCancelled, RunStatusBudgetExhausted:
		return true
	default:
		return false
	}
}

// TransitionOutcome is the typed result a role returns to Prism for routing.
// Agents return outcomes; they do not choose arbitrary destination roles.
type TransitionOutcome string

const (
	OutcomePlanReady           TransitionOutcome = "plan_ready"
	OutcomeImplementationReady TransitionOutcome = "implementation_ready"
	OutcomeTestsPassed         TransitionOutcome = "tests_passed"
	OutcomeTestsFailed         TransitionOutcome = "tests_failed"
	OutcomeReviewApproved      TransitionOutcome = "review_approved"
	OutcomeChangesRequested    TransitionOutcome = "changes_requested"
	OutcomeTerminalFailure     TransitionOutcome = "terminal_failure"
	OutcomeCancelled           TransitionOutcome = "cancelled"
)

// Valid reports whether the transition outcome is recognized.
func (o TransitionOutcome) Valid() bool {
	switch o {
	case OutcomePlanReady,
		OutcomeImplementationReady,
		OutcomeTestsPassed,
		OutcomeTestsFailed,
		OutcomeReviewApproved,
		OutcomeChangesRequested,
		OutcomeTerminalFailure,
		OutcomeCancelled:
		return true
	default:
		return false
	}
}

// ValidFor reports whether the outcome may be produced by the given role in
// the Phase 1 reference flow. Failure and cancellation may originate anywhere.
func (o TransitionOutcome) ValidFor(role Role) bool {
	switch o {
	case OutcomePlanReady:
		return role == RolePlanner
	case OutcomeImplementationReady:
		return role == RoleDeveloper
	case OutcomeTestsPassed, OutcomeTestsFailed:
		return role == RoleTester
	case OutcomeReviewApproved, OutcomeChangesRequested:
		return role == RoleReviewer
	case OutcomeTerminalFailure, OutcomeCancelled:
		return role.Valid()
	default:
		return false
	}
}

// TerminalCondition records why a run reached a terminal status.
type TerminalCondition string

const (
	TerminalConditionCompleted       TerminalCondition = "completed"
	TerminalConditionFailed          TerminalCondition = "failed"
	TerminalConditionBudgetExhausted TerminalCondition = "budget_exhausted"
	TerminalConditionCancelled       TerminalCondition = "cancelled"
)

// Valid reports whether the terminal condition is recognized.
func (c TerminalCondition) Valid() bool {
	switch c {
	case TerminalConditionCompleted,
		TerminalConditionFailed,
		TerminalConditionBudgetExhausted,
		TerminalConditionCancelled:
		return true
	default:
		return false
	}
}

func (c TerminalCondition) runStatus() RunStatus {
	switch c {
	case TerminalConditionCompleted:
		return RunStatusCompleted
	case TerminalConditionFailed:
		return RunStatusFailed
	case TerminalConditionBudgetExhausted:
		return RunStatusBudgetExhausted
	case TerminalConditionCancelled:
		return RunStatusCancelled
	default:
		return ""
	}
}

// Limit is a count or token ceiling. Positive values are bounded and -1 is the
// explicit unlimited sentinel. Zero is invalid so omitted configuration cannot
// silently disable a safety bound.
type Limit int

const Unlimited Limit = -1

// Valid reports whether the limit is positive or explicitly unlimited.
func (l Limit) Valid() bool {
	return l == Unlimited || l > 0
}

// UnlimitedDuration is the explicit no-deadline sentinel for duration limits.
// Zero is invalid for the same fail-closed reason as Limit.
const UnlimitedDuration time.Duration = -1

func validDuration(d time.Duration) bool {
	return d == UnlimitedDuration || d > 0
}
