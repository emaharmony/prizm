package multiagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/subagent"
)

const maxRoleRecoveryRetries = 10

// FailureOrigin is a bounded source tag for diagnostics. Values are written
// only by trusted execution boundaries and unknown is the compatibility default.
type FailureOrigin string

const (
	FailureOriginUnknown      FailureOrigin = "unknown"
	FailureOriginToolExecutor FailureOrigin = "tool_executor"
	FailureOriginProviderCall FailureOrigin = "provider_call"
	FailureOriginRoleSchema   FailureOrigin = "role_schema"
	FailureOriginGovernance   FailureOrigin = "governance_gate"
	FailureOriginFanOutJoin   FailureOrigin = "fanout_join"
)

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

// RoleExecutionFailure is a safe typed summary returned by the trusted graph
// worker. It contains no raw model/provider error text.
type RoleExecutionFailure struct {
	DeliveryAttemptID string          `json:"delivery_attempt_id"`
	AttemptID         string          `json:"attempt_id"`
	Class             string          `json:"class"`
	Code              string          `json:"code"`
	Origin            FailureOrigin   `json:"failure_origin,omitempty"`
	CorrectionField   string          `json:"correction_field,omitempty"`
	ExpectedType      string          `json:"expected_type,omitempty"`
	AllowedValues     []string        `json:"allowed_values,omitempty"`
	RetryAfter        time.Duration   `json:"retry_after,omitempty"`
	ProviderStatus    int             `json:"provider_status,omitempty"`
	Retryable         bool            `json:"retryable"`
	Usage             cost.TokenUsage `json:"usage,omitempty"`
}

// ClassifyRoleExecutionFailure returns only allowlisted retry categories.
// Unknown failures remain nonretryable and do not expose their error string.
func ClassifyRoleExecutionFailure(err error) RoleExecutionFailure {
	class, code, retryAfter, retryable := classifyRetryableRoleFailure(err)
	providerStatus := safeProviderStatus(err)
	if !retryable {
		class, code = classifySafeNonRetryableRoleFailure(err)
		return RoleExecutionFailure{Class: class, Code: code, Origin: failureOriginForError(err), RetryAfter: safeProviderRetryAfter(err), ProviderStatus: providerStatus}
	}
	failure := RoleExecutionFailure{Class: safeRetryCode(class), Code: safeRetryCode(code), Origin: failureOriginForError(err), RetryAfter: retryAfter, ProviderStatus: providerStatus, Retryable: true}
	var schema *SchemaCorrectionError
	if errors.As(err, &schema) && schema != nil {
		failure.CorrectionField, failure.ExpectedType, failure.AllowedValues = safeSchemaCorrectionDetails(schema.Role, schema.Code, schema.Fields, schema.AllowedValues)
	}
	return failure
}

// ClassifyRoleExecutionFailureWithResult classifies the terminal error. The
// result may contain recovery facts from earlier turns in the same visit, so
// it is deliberately not used to replace an unknown or typed terminal cause.
func ClassifyRoleExecutionFailureWithResult(err error, _ RoleRunResult) RoleExecutionFailure {
	return ClassifyRoleExecutionFailure(err)
}

func failureOriginForError(err error) FailureOrigin {
	var schema *SchemaCorrectionError
	if errors.As(err, &schema) && schema != nil {
		return FailureOriginRoleSchema
	}
	var structured *StructuredOutputError
	if errors.As(err, &structured) && structured != nil {
		return FailureOriginRoleSchema
	}
	var governance *GovernanceError
	if errors.As(err, &governance) && governance != nil {
		return FailureOriginGovernance
	}
	var safeFailure *subagent.SafeFailureError
	if errors.As(err, &safeFailure) && safeFailure != nil {
		if safeFailure.Tool != "" {
			return FailureOriginToolExecutor
		}
		if strings.HasPrefix(safeFailure.Code, "provider_") {
			return FailureOriginProviderCall
		}
	}
	return FailureOriginUnknown
}

func safeProviderStatus(err error) int {
	var failure *subagent.SafeFailureError
	if errors.As(err, &failure) && failure != nil && failure.ProviderStatus >= 100 && failure.ProviderStatus <= 599 {
		return failure.ProviderStatus
	}
	return 0
}

func safeProviderRetryAfter(err error) time.Duration {
	var failure *subagent.SafeFailureError
	if errors.As(err, &failure) && failure != nil && failure.RetryAfter() > 0 && failure.RetryAfter() <= 365*24*time.Hour {
		return failure.RetryAfter()
	}
	return 0
}

func safeStatusValue(status int) int {
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func safeRetryAfterMillis(delay time.Duration) int64 {
	if delay <= 0 || delay > 365*24*time.Hour {
		return 0
	}
	return delay.Milliseconds()
}

// sanitizeTerminalRoleFailure accepts only known failure pairs and schema
// identifiers owned by the role contract. Worker payloads are never copied
// directly into durable or public diagnostics.
func sanitizeTerminalRoleFailure(role Role, failure RoleExecutionFailure) TerminalRoleFailure {
	class, code := failure.Class, failure.Code
	if !knownRoleFailurePair(class, code) {
		class, code = "unknown", "unknown"
	}
	field, expected, values := safeSchemaCorrectionDetails(role, code,
		[]string{failure.CorrectionField}, failure.AllowedValues)
	return TerminalRoleFailure{
		Class: class, Code: code, Origin: safeFailureOrigin(class, code, failure.Origin), CorrectionField: field, ExpectedType: expected,
		AllowedValues: values, Retryable: failure.Retryable && knownRoleFailurePair(failure.Class, failure.Code),
		ProviderStatus: safeStatusValue(failure.ProviderStatus), RetryAfterMS: safeRetryAfterMillis(failure.RetryAfter),
	}
}

func knownRoleFailurePair(class, code string) bool {
	switch class {
	case "schema_correction":
		switch code {
		case "invalid_json_syntax", "invalid_response_schema", "missing_required_fields", "invalid_field_type",
			"invalid_field_value", "invalid_structured_output", "invalid_proposal_diff":
			return true
		}
	case "provider_transient":
		return code == "provider_transient" || code == "provider_turn_failed"
	case "provider_rejected":
		return code == "provider_auth_rejected" || code == "provider_http_rejected" || code == "provider_quota_exhausted"
	case "execution_transient":
		return code == "deadline_exceeded" || code == "execution_error"
	case "execution":
		switch code {
		case "backend_bind_failed", "progress_checkpoint_failed", "schema_recovery_failed",
			"tool_diagnostic_checkpoint_failed", "no_effects_checkpoint_failed", "tool_recovery_failed",
			"no_progress_checkpoint_failed", "no_progress_recovery_failed":
			return true
		}
	case "tool":
		switch code {
		case "patch_validation_failed", "permission_denied", "not_found", "invalid_input", "tool_execution_error":
			return true
		}
	case "governance":
		switch code {
		case "policy", "tool_scope", "workspace_scope", "approval", "capability", "approval_required",
			"invalid_read_coverage", "invalid_proposal_context":
			return true
		}
	case "cancellation":
		return code == "cancelled"
	case "unknown":
		return code == "unknown"
	}
	return false
}

func validTerminalRoleFailure(role Role, failure TerminalRoleFailure) bool {
	if !knownRoleFailurePair(failure.Class, failure.Code) || !knownTerminalFailureReason(failure.Reason) {
		return false
	}
	clean := sanitizeTerminalRoleFailure(role, RoleExecutionFailure{
		Class: failure.Class, Code: failure.Code, Origin: failure.Origin, CorrectionField: failure.CorrectionField,
		ExpectedType: failure.ExpectedType, AllowedValues: failure.AllowedValues, Retryable: failure.Retryable,
		ProviderStatus: failure.ProviderStatus, RetryAfter: time.Duration(failure.RetryAfterMS) * time.Millisecond,
	})
	return clean.Class == failure.Class && clean.Code == failure.Code &&
		(failure.Origin == "" || clean.Origin == failure.Origin) && clean.Retryable == failure.Retryable &&
		clean.CorrectionField == failure.CorrectionField && clean.ExpectedType == failure.ExpectedType &&
		equalStrings(clean.AllowedValues, failure.AllowedValues) && failure.RecoveryNumber >= 0 &&
		failure.MaxRecoveries >= 0 && failure.RecoveryNumber <= failure.MaxRecoveries &&
		(failure.AttemptID == "" || validAttemptIdentity(failure.AttemptID))
}

func safeFailureOrigin(class, code string, origin FailureOrigin) FailureOrigin {
	switch origin {
	case FailureOriginToolExecutor:
		if class == "tool" && (code == "tool_execution_error" || code == "patch_validation_failed" || code == "permission_denied" || code == "not_found" || code == "invalid_input") {
			return origin
		}
	case FailureOriginProviderCall:
		if (class == "provider_transient" && (code == "provider_transient" || code == "provider_turn_failed")) || class == "provider_rejected" {
			return origin
		}
	case FailureOriginRoleSchema:
		if class == "schema_correction" {
			return origin
		}
	case FailureOriginGovernance:
		if class == "governance" {
			return origin
		}
	case FailureOriginFanOutJoin:
		if class == "orchestration" && code == "delegation_join_failed" {
			return origin
		}
	}
	return FailureOriginUnknown
}

func correctionFailureClass(errorClass, code string) string {
	code = safeRetryCode(code)
	switch errorClass {
	case "tool":
		if knownRoleFailurePair("tool", code) {
			return "tool"
		}
	case "schema":
		if knownRoleFailurePair("schema_correction", code) {
			return "schema_correction"
		}
	}
	return "unknown"
}

func knownTerminalFailureReason(reason string) bool {
	switch reason {
	case "not_retryable", "retry_state_unavailable", "recovery_limit_reached", "unsafe_replay_state",
		"deadline_would_expire", "retry_not_scheduled", "unmatched_outcome":
		return true
	default:
		return false
	}
}

func validAttemptIdentity(value string) bool {
	if len(value) != 27 || !strings.HasPrefix(value, "ra_") {
		return false
	}
	for _, r := range value[3:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// classifySafeNonRetryableRoleFailure retains only server-defined diagnostic
// codes. It never serializes arbitrary error strings or unknown SafeFailure
// codes across the durable worker boundary.
func classifySafeNonRetryableRoleFailure(err error) (string, string) {
	if err == nil {
		return "unknown", "unknown"
	}
	if errors.Is(err, context.Canceled) {
		return "cancellation", "cancelled"
	}
	var governance *GovernanceError
	if errors.As(err, &governance) {
		switch governance.Kind {
		case "policy", "tool_scope", "workspace_scope", "approval", "capability":
			return "governance", governance.Kind
		default:
			return "unknown", "unknown"
		}
	}
	var approval *ApprovalRequiredError
	if errors.As(err, &approval) {
		return "governance", "approval_required"
	}
	var safeFailure *subagent.SafeFailureError
	if errors.As(err, &safeFailure) && safeFailure != nil {
		switch safeFailure.Code {
		case "patch_validation_failed", "permission_denied", "not_found", "invalid_input":
			return "tool", safeFailure.Code
		case "provider_auth_rejected", "provider_http_rejected", "provider_quota_exhausted":
			return "provider_rejected", safeFailure.Code
		case "backend_bind_failed", "progress_checkpoint_failed", "schema_recovery_failed",
			"tool_diagnostic_checkpoint_failed", "no_effects_checkpoint_failed",
			"tool_recovery_failed", "invalid_read_coverage", "no_progress_checkpoint_failed",
			"no_progress_recovery_failed", "invalid_proposal_context":
			class := "execution"
			if safeFailure.Code == "invalid_read_coverage" || safeFailure.Code == "invalid_proposal_context" {
				class = "governance"
			}
			return class, safeFailure.Code
		default:
			return "unknown", "unknown"
		}
	}
	return "unknown", "unknown"
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
	FailureOrigin             FailureOrigin      `json:"failure_origin,omitempty"`
	ProviderStatus            int                `json:"provider_status,omitempty"`
	RetryAfter                time.Duration      `json:"retry_after,omitempty"`
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
	ErrorClass      string        `json:"error_class"`
	Code            string        `json:"code"`
	FailureOrigin   FailureOrigin `json:"failure_origin,omitempty"`
	CorrectionField string        `json:"correction_field,omitempty"`
	ExpectedType    string        `json:"expected_type,omitempty"`
	AllowedValues   []string      `json:"allowed_values,omitempty"`
	Reason          string        `json:"reason,omitempty"`
	Feedback        string        `json:"feedback,omitempty"`
}

func appendScheduledRetryCorrection(state *RetryState, role Role, failure RoleExecutionFailure) RetryCorrection {
	if state == nil {
		return RetryCorrection{}
	}
	safe := sanitizeTerminalRoleFailure(role, failure)
	feedback := roleFailureFeedback(role, safe.Code, safe.CorrectionField, safe.ExpectedType, safe.AllowedValues)
	correction := RetryCorrection{
		ErrorClass: safe.Class, Code: safe.Code, CorrectionField: safe.CorrectionField,
		FailureOrigin: safe.Origin,
		ExpectedType:  safe.ExpectedType, AllowedValues: append([]string(nil), safe.AllowedValues...),
		Reason: "retry_scheduled", Feedback: feedback,
	}
	state.Corrections = append(state.Corrections, correction)
	if len(state.Corrections) > 16 {
		state.Corrections = append([]RetryCorrection(nil), state.Corrections[len(state.Corrections)-16:]...)
	}
	return correction
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
