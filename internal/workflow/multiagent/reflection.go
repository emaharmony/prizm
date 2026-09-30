package multiagent

import (
	"context"
	"time"

	prizmAdapter "github.com/emaharmony/prizm/internal/adapter"
)

type ReflectionTrigger string

const (
	ReflectionTerminal         ReflectionTrigger = "terminal"
	ReflectionFailure          ReflectionTrigger = "failure"
	ReflectionVerificationFail ReflectionTrigger = "verification_failure"
	ReflectionInterruption     ReflectionTrigger = "interruption"
	ReflectionRecovery         ReflectionTrigger = "recovery"
	ReflectionApprovalResume   ReflectionTrigger = "approval_resume"
)

type MemoryCandidate struct {
	Summary   string   `json:"summary"`
	Content   string   `json:"content"`
	Category  string   `json:"category"`
	KeyTopics []string `json:"key_topics,omitempty"`
}

type ReflectionInput struct {
	Trigger      ReflectionTrigger          `json:"trigger"`
	SourceRole   Role                       `json:"source_role"`
	Outcome      TransitionOutcome          `json:"outcome"`
	Error        string                     `json:"error,omitempty"`
	Goal         string                     `json:"goal,omitempty"`
	Evidence     []ArtifactRef              `json:"evidence,omitempty"`
	Observation  *prizmAdapter.Observation  `json:"observation,omitempty"`
	Action       *prizmAdapter.Action       `json:"action,omitempty"`
	ActionResult *prizmAdapter.ActionResult `json:"action_result,omitempty"`
	ReplanCount  int                        `json:"replan_count"`
}

type ReflectionRequest struct {
	Run        RunView
	RoleConfig RoleConfig
	Input      ReflectionInput
}

type ReflectionResult struct {
	Verdict         string           `json:"verdict"`
	Confidence      float64          `json:"confidence"`
	FailureClass    string           `json:"failure_class"`
	Evidence        []ArtifactRef    `json:"evidence,omitempty"`
	LessonCandidate *MemoryCandidate `json:"lesson_candidate,omitempty"`
	ReplanRequested bool             `json:"replan_requested"`
	ReplanReason    string           `json:"replan_reason,omitempty"`
}

type ReflectionRunner interface {
	Reflect(context.Context, ReflectionRequest) (ReflectionResult, error)
}

type ReflectionRunnerFunc func(context.Context, ReflectionRequest) (ReflectionResult, error)

func (f ReflectionRunnerFunc) Reflect(ctx context.Context, request ReflectionRequest) (ReflectionResult, error) {
	return f(ctx, request)
}

type ReflectionMemorySink interface {
	StoreReflectionMemory(context.Context, string, string, string, string, []string) error
}

type ReflectionRecord struct {
	Trigger      ReflectionTrigger `json:"trigger"`
	SourceRole   Role              `json:"source_role"`
	ExecutionKey string            `json:"execution_key"`
	Result       ReflectionResult  `json:"result"`
	MemoryStatus string            `json:"memory_status,omitempty"`
	ReplanCount  int               `json:"replan_count"`
	CreatedAt    time.Time         `json:"created_at"`
}

func cloneReflectionRecord(record *ReflectionRecord) *ReflectionRecord {
	if record == nil {
		return nil
	}
	clone := *record
	clone.Result.Evidence = cloneArtifactRefs(record.Result.Evidence)
	if record.Result.LessonCandidate != nil {
		candidate := *record.Result.LessonCandidate
		candidate.KeyTopics = append([]string(nil), candidate.KeyTopics...)
		clone.Result.LessonCandidate = &candidate
	}
	return &clone
}
