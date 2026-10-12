package subagent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RetryAttempt is the runner-side view of one durable role attempt. The
// multi-agent runtime adapts its own attempt type to this package boundary.
type RetryAttempt struct {
	VisitID        string
	AttemptID      string
	Number         int
	RecoveryNumber int
	Remaining      int
	NextAt         time.Time
}

// RetryHooks lets the loop checkpoint a recoverable correction without
// importing the workflow runtime. All methods are optional for legacy runners.
type RetryHooks interface {
	CurrentAttempt() RetryAttempt
	NoProgressCount(expectedAttemptID, observationHash string) (int, error)
	ConsumeRecovery(expectedAttemptID, failureCode string) (RetryAttempt, error)
	ConfirmNoEffects(expectedAttemptID, mutationID string) error
	RecordProgress(expectedAttemptID, kind, safeRef, observationHash string) error
	RecordCorrection(expectedAttemptID, errorClass, code string) error
	RecordCorrectionFeedback(expectedAttemptID, errorClass, code, feedback string) error
}

// ToolDiagnostic contains only allowlisted metadata suitable for a durable
// event. It deliberately has no input, output, prompt, or error-message field.
type ToolDiagnostic struct {
	Type               string
	RunID              string
	TaskID             string
	CorrelationID      string
	Role               string
	ExecutionKey       string
	AttemptID          string
	MutationID         string
	NoProgressEligible bool
	NoEffectsProven    bool
	Tool               string
	Status             string
	ErrorCode          string
	Sequence           int
	Duration           time.Duration
	SafeRef            string
	ObservationHash    string
	MutationPossible   bool
}

// ToolEventSink records a safe tool outcome at the owning runtime boundary.
type ToolEventSink func(ToolDiagnostic) error

// ToolEventSinkFactory creates a correlated sink for one delegated task.
type ToolEventSinkFactory func(AgentRuntime) ToolEventSink

// NoProgressError signals that an unchanged tool observation repeated six
// times and the role should be replanned by its durable runtime.
type NoProgressError struct {
	RepeatedObservations int
}

// CorrectableToolError is emitted only when a domain tool adapter proves a
// failed operation stopped before any side effect. Its code is an allowlisted
// category, never the underlying error text.
type CorrectableToolError struct {
	Code string
}

func (e *CorrectableToolError) Error() string {
	return "subagent: tool input requires correction (code=" + safeIdentifier(e.Code) + ")"
}

func (e *NoProgressError) Error() string {
	return "subagent: repeated tool observation made no progress"
}

// SafeFailureError carries only bounded classifications across durable
// boundaries. The original provider/tool error stays inside the active loop.
type SafeFailureError struct {
	Code            string
	Tool            string
	RetryAfterDelay time.Duration
}

func (e *SafeFailureError) Error() string {
	if e.Tool != "" {
		return "subagent: tool execution failed (tool=" + safeIdentifier(e.Tool) + ", code=" + safeIdentifier(e.Code) + ")"
	}
	return "subagent: execution failed (code=" + safeIdentifier(e.Code) + ")"
}

// RetryAfter preserves provider-requested backoff while keeping provider
// response text out of role failures and durable diagnostics.
func (e *SafeFailureError) RetryAfter() time.Duration {
	if e == nil || e.RetryAfterDelay < 0 {
		return 0
	}
	return e.RetryAfterDelay
}

func safeIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return "unknown"
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "unknown"
		}
	}
	return value
}

// safeToolErrorCode maps arbitrary tool errors to a small non-sensitive set.
func safeToolErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var correction *CorrectableToolError
	if errors.As(err, &correction) {
		return "patch_validation_failed"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "deadline exceeded"), strings.Contains(message, "context deadline"):
		return "deadline_exceeded"
	case strings.Contains(message, "permission denied"), strings.Contains(message, "access denied"):
		return "permission_denied"
	case strings.Contains(message, "not found"), strings.Contains(message, "does not exist"), strings.Contains(message, "no such file"):
		return "not_found"
	case strings.Contains(message, "invalid"), strings.Contains(message, "required"), strings.Contains(message, "must be"):
		return "invalid_input"
	default:
		return "execution_error"
	}
}

// patchCorrectionFeedback is a fixed, allowlisted description of the proposal
// contract. It intentionally excludes the rejected patch and error text.
func patchCorrectionFeedback() string {
	return "The patch proposal failed validation before an approval was saved. Correct the request and try again: input must contain exactly the non-empty string fields \"patch\" and \"base_sha\"; patch must be one complete unified diff that applies cleanly, changes at least one file, and stays within this workspace's allowed write paths; base_sha must equal the trusted patch base supplied in the task. After the proposal succeeds, return the required final role JSON."
}

func toolObservationHash(tool string, input map[string]any, result, errorCode string) string {
	args, err := json.Marshal(input)
	if err != nil {
		args = []byte("invalid_arguments")
	}
	resultHash := sha256.Sum256([]byte(result))
	identity := fmt.Sprintf("%s\x00%s\x00%x\x00%s", safeIdentifier(tool), args, resultHash[:], errorCode)
	hash := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%x", hash[:])
}

func isNewProgressToolObservation(tool, observationHash string, seen map[string]struct{}) bool {
	if !isRepeatableToolObservation(tool) {
		return false
	}
	if _, exists := seen[observationHash]; exists {
		return false
	}
	seen[observationHash] = struct{}{}
	return true
}

func isRepeatableToolObservation(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "wait", "observe", "poll", "status", "get_status", "get_task_status", "await":
		return false
	default:
		return true
	}
}

func progressKind(tool string) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "read_file", "list_directory", "search", "grep", "git_status", "git_diff":
		return "evidence"
	case "write_file", "write_file_proposal", "apply_patch_proposal", "create_directory", "git_add", "git_commit":
		return "state"
	default:
		return "observation"
	}
}

func mutationPossible(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "read_file", "list_dir", "read_project", "search_files", "project_overview",
		"git_status", "git_log", "git_diff", "git_branch_list", "wait", "observe", "poll",
		"status", "get_status", "get_task_status", "await":
		return false
	default:
		return true
	}
}

func mutationIdentity(executionKey, attemptID, tool string, sequence int) string {
	identity := fmt.Sprintf("%s\x00%s\x00%d\x00%s", executionKey, attemptID, sequence, safeIdentifier(tool))
	hash := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%x", hash[:])
}
