package multiagent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/cost"
)

const maxRoleRecoveryRetries = 10

var (
	ErrRequestIdentityConflict = errors.New("multiagent: request idempotency identity conflicts with accepted run")
	ErrRetryBudgetExhausted    = errors.New("multiagent: role visit recovery budget exhausted")
	ErrStaleRetryAttempt       = errors.New("multiagent: stale role retry attempt")
)

type RetryDeadlineError struct{}

func (RetryDeadlineError) Error() string { return "retry_deadline_exceeded" }

// RequestIdentity is trusted origin and delivery metadata captured before a
// durable run begins. It must not contain credentials or request content.
type RequestIdentity struct {
	RequesterID       string    `json:"requester_id,omitempty"`
	SessionID         string    `json:"session_id,omitempty"`
	Source            string    `json:"source,omitempty"`
	ResultDestination string    `json:"result_destination,omitempty"`
	IdempotencyKey    string    `json:"idempotency_key,omitempty"`
	AcceptedAt        time.Time `json:"accepted_at,omitempty"`
}

// RetryAttempt identifies one model/worker attempt within a stable role visit.
// Number includes the initial attempt; RecoveryNumber counts only retries.
type RetryAttempt struct {
	VisitID        string
	AttemptID      string
	Number         int
	RecoveryNumber int
	Remaining      int
	NextAt         time.Time
}

// RoleVisitRetryHandle is the only way an executing role may debit the shared
// durable recovery budget or append compact progress/correction facts.
type RoleVisitRetryHandle interface {
	CurrentAttempt() RetryAttempt
	NoProgressCount(expectedAttemptID, observationHash string) (int, error)
	ConsumeRecovery(expectedAttemptID, failureCode string) (RetryAttempt, error)
	RecordProgress(expectedAttemptID, kind, safeRef, observationHash string) error
	RecordCorrection(expectedAttemptID, errorClass, code string) error
	RecordCorrectionFeedback(expectedAttemptID, errorClass, code, feedback string) error
	ConfirmNoEffects(expectedAttemptID, mutationID string) error
}

// ToolDiagnostic contains only bounded metadata safe for durable event
// storage. It must never include arguments, prompt text, or tool output.
type ToolDiagnostic struct {
	RunID              string
	TaskID             string
	Role               Role
	ExecutionKey       string
	AttemptID          string
	MutationID         string
	NoProgressEligible bool
	Tool               string
	Status             string
	ErrorCode          string
	Sequence           int
	Duration           time.Duration
	SafeRef            string
	ObservationHash    string
	MutationPossible   bool
	NoEffectsProven    bool
}

// ToolEventSink records bounded tool execution diagnostics at the durable
// runtime boundary.
type ToolEventSink func(ToolDiagnostic) error

// RetryState is the durable compact ledger for one stable role visit.
type RetryState struct {
	VisitID                   string             `json:"visit_id"`
	ExecutionKey              string             `json:"execution_key"`
	Role                      Role               `json:"role"`
	AttemptID                 string             `json:"attempt_id"`
	Number                    int                `json:"number"`
	RecoveryNumber            int                `json:"recovery_number"`
	MaxRecoveries             int                `json:"max_recoveries"`
	NextAt                    time.Time          `json:"next_at,omitempty"`
	FailureClass              string             `json:"failure_class,omitempty"`
	FailureCode               string             `json:"failure_code,omitempty"`
	RecoveryFeedback          string             `json:"recovery_feedback,omitempty"`
	MutationPossible          bool               `json:"mutation_possible,omitempty"`
	UnresolvedMutationFailure bool               `json:"unresolved_mutation_failure,omitempty"`
	MutationLedgerReady       bool               `json:"mutation_ledger_ready,omitempty"`
	UnattributedMutation      bool               `json:"unattributed_mutation,omitempty"`
	UnsafeMutationIDs         []string           `json:"unsafe_mutation_ids,omitempty"`
	ProvenNoEffectIDs         []string           `json:"proven_no_effect_ids,omitempty"`
	ReplaySafe                bool               `json:"replay_safe,omitempty"`
	AccumulatedUsage          cost.TokenUsage    `json:"accumulated_usage,omitempty"`
	AccumulatedIterations     int                `json:"accumulated_iterations,omitempty"`
	Progress                  []RetryProgress    `json:"progress,omitempty"`
	Corrections               []RetryCorrection  `json:"corrections,omitempty"`
	Observations              []RetryObservation `json:"observations,omitempty"`
	ConsecutiveNoProgress     int                `json:"consecutive_no_progress,omitempty"`
	LastObservationHash       string             `json:"last_observation_hash,omitempty"`
}

type RetryObservation struct {
	Hash  string `json:"hash"`
	Count int    `json:"count"`
}

// RetryProgress records a bounded reference/hash pair, never a tool result.
type RetryProgress struct {
	Kind            string `json:"kind"`
	SafeRef         string `json:"safe_ref,omitempty"`
	ObservationHash string `json:"observation_hash,omitempty"`
}

// RetryCorrection stores only the classified schema/tool correction code.
type RetryCorrection struct {
	ErrorClass string `json:"error_class"`
	Code       string `json:"code"`
	Feedback   string `json:"feedback,omitempty"`
}

// RecoveryCandidate tells a supervised scanner whether automatic Resume is
// safe now, and why a nonterminal run still needs attention.
type RecoveryCandidate struct {
	RunID       string
	Kind        string
	Phase       CheckpointPhase
	WaitingKind string
	DueAt       time.Time
	Ready       bool
	Ambiguous   bool
}

func roleVisitID(runID string, role Role, visit int) string {
	return fmt.Sprintf("%s:%s:%d", runID, role, visit)
}

func retryAttemptID(visitID string, number int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:attempt:%d", visitID, number)))
	return "ra_" + hex.EncodeToString(sum[:12])
}

func roleRetryLimit(node CompiledNode) int {
	limit := maxRoleRecoveryRetries
	if node.Retry != nil && node.Retry.MaxRetries < limit {
		limit = node.Retry.MaxRetries
	}
	if limit < 0 {
		return 0
	}
	return limit
}

func roleRetryDelay(visitID string, recoveryNumber int, retryAfter time.Duration) time.Duration {
	delay := 5 * time.Second
	for i := 1; i < recoveryNumber && delay < 60*time.Second; i++ {
		delay *= 2
	}
	if delay > 60*time.Second {
		delay = 60 * time.Second
	}
	seed := sha256.Sum256([]byte(fmt.Sprintf("%s:jitter:%d", visitID, recoveryNumber)))
	jitter := time.Duration((uint16(seed[0])<<8|uint16(seed[1]))%501) * time.Millisecond
	delay += jitter
	if retryAfter > delay {
		delay = retryAfter
	}
	return delay
}

func cloneRequestIdentity(identity *RequestIdentity) *RequestIdentity {
	if identity == nil {
		return nil
	}
	copy := *identity
	return &copy
}

func equalRequestIdentity(a, b *RequestIdentity) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.RequesterID == b.RequesterID && a.SessionID == b.SessionID &&
		a.Source == b.Source && a.ResultDestination == b.ResultDestination &&
		a.IdempotencyKey == b.IdempotencyKey
}
