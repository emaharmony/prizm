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
	Kind        string    `json:"kind"`
	Reason      string    `json:"reason"`
	SafeToRetry bool      `json:"safe_to_retry"`
	Since       time.Time `json:"since"`
}

// PersistedFailure is bounded diagnostic information. It never contains
// prompts, credentials, or provider output.
type PersistedFailure struct {
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// DurableRun is the atomic persistence envelope around canonical RunState.
type DurableRun struct {
	SchemaVersion             int                `json:"schema_version"`
	Revision                  int64              `json:"revision"`
	State                     RunState           `json:"state"`
	Phase                     CheckpointPhase    `json:"phase"`
	PendingTransition         *PendingTransition `json:"pending_transition,omitempty"`
	ActiveExecutionKey        string             `json:"active_execution_key,omitempty"`
	LastCompletedExecutionKey string             `json:"last_completed_execution_key,omitempty"`
	Waiting                   *WaitingState      `json:"waiting,omitempty"`
	Failure                   *PersistedFailure  `json:"failure,omitempty"`
}

// Validate rejects corrupt, internally contradictory, or future state.
func (r DurableRun) Validate(definition Definition) error {
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
	if err := r.State.Validate(definition); err != nil {
		problems = append(problems, err.Error())
	}

	switch r.Phase {
	case CheckpointPendingRole:
		if r.State.Status != RunStatusRunning {
			problems = append(problems, "pending_role checkpoint requires running run state")
		}
		if r.PendingTransition != nil || r.Waiting != nil {
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
	case CheckpointTerminal:
		if !r.State.Status.Terminal() {
			problems = append(problems, "terminal checkpoint requires terminal run state")
		}
	}

	if len(problems) != 0 {
		return &ContractError{Problems: problems}
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
	Close() error
}

// EventPublisher is the narrow canonical event-store operation used by outbox
// reconciliation.
type EventPublisher interface {
	Store(context.Context, event.Event) error
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
