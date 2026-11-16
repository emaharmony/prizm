package multiagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/policy"
)

var ErrFreewillApprovalStale = errors.New("multiagent: Freewill approval no longer matches the current request")
var ErrFreewillDispatchNotAccepted = errors.New("multiagent: Freewill child dispatch is not yet accepted")

func selectedFreewillCandidate(state *FreewillRunState) (freewill.CandidateIntention, bool) {
	if state == nil {
		return freewill.CandidateIntention{}, false
	}
	for _, candidate := range state.Candidates {
		if candidate.CandidateID == state.ChoiceID {
			return candidate, true
		}
	}
	return freewill.CandidateIntention{}, false
}

func freewillRequestFingerprint(request freewill.IntentionRequest) (string, freewill.IntentionRequest) {
	request.RequestedCapabilities = append([]string(nil), request.RequestedCapabilities...)
	sort.Strings(request.RequestedCapabilities)
	data, _ := json.Marshal(request)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), request
}

func freewillRunAgent(record DurableRun) (string, bool) {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.State.RequestIdentity == nil || record.State.RequestIdentity.RequesterID == "" {
		return "", false
	}
	return record.State.RequestIdentity.RequesterID, true
}

func freewillAdmissionActive(record DurableRun) bool {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil {
		return false
	}
	switch record.Freewill.Lifecycle {
	case freewill.StateWaking, freewill.StateSelecting, freewill.StateProposed, freewill.StateApprovalPending, freewill.StateRunning:
		return true
	case freewill.StateSleeping, freewill.StateDenied, freewill.StateHalted:
		return false
	case freewill.StatePaused:
		if !validFreewillApprovalReceipt(record.Freewill, FreewillApprovalGranted) {
			return true
		}
		dispatch := record.Freewill.ExecutionDispatch
		if dispatch == nil {
			return false
		}
		switch dispatch.Status {
		case "completed", "execution_failed", "execution_cancelled", "mutation_no_effect_reconciled":
			return false
		default:
			// Dispatching, accepted, retryable failed, and unknown states keep
			// the actor slot until recovery records a durable terminal outcome.
			return true
		}
	default:
		return true
	}
}

func validFreewillApprovalReceipt(state *FreewillRunState, decision FreewillApprovalDecision) bool {
	if state == nil || state.ApprovalReceipt == nil || state.ApprovalReceipt.ApprovalID == "" ||
		state.ApprovalRef != state.ApprovalReceipt.ApprovalID || state.ApprovalFingerprint != state.ApprovalReceipt.RequestFingerprint ||
		state.ApprovalGeneration != state.ApprovalReceipt.ControlGeneration {
		return false
	}
	selected, ok := selectedFreewillCandidate(state)
	if !ok {
		return false
	}
	fingerprint, _ := freewillRequestFingerprint(selected.Request)
	if fingerprint != state.ApprovalReceipt.RequestFingerprint {
		return false
	}
	return state.ApprovalReceipt.Decision == decision
}

type FreewillPolicyEvaluator interface {
	Evaluate(policy.PolicyRequest) policy.PolicyDecision
}

type FreewillApprovalStore interface {
	Load(string, string) (*approval.Approval, error)
	List(string) ([]*approval.Approval, error)
	Save(*approval.Approval) error
}

// FreewillExecutionDispatcher admits one already-approved intention into the
// existing governed workflow. Start must use the supplied stable request
// identity so a retry after a lost parent checkpoint returns the same child.
type FreewillExecutionDispatcher interface {
	Start(context.Context, FreewillExecutionRequest) (FreewillExecutionResult, error)
}

type FreewillExecutionRequest struct {
	ParentRunID        string
	ApprovalID         string
	RequestFingerprint string
	ApprovalGeneration uint64
	RunID              string
	Identity           *RequestIdentity
	Candidate          freewill.CandidateIntention
}

type FreewillExecutionResult struct {
	RunID     string
	ReportRef string
}

// FreewillExecutionTerminalResult is derived by the host from the durable
// child run and its validated terminal report, never from selector output.
type FreewillExecutionTerminalResult struct {
	ChildRunID        string
	ChildStatus       RunStatus
	ReportRef         string
	ReportSHA256      string
	ValidationSummary string
	FailureCode       string
}

// FreewillTerminalFact contains only host-validated terminal outcome fields.
// A sink may persist it locally after the outcome is durable; it must not
// reinterpret selector output or read the child report body.
type FreewillTerminalFact struct {
	ProjectID         string
	Workspace         string
	AgentID           string
	IntentionID       string
	ParentRunID       string
	ChildRunID        string
	Status            string
	ReportRef         string
	ReportSHA256      string
	ValidationSummary string
	FailureCode       string
	RecordedAt        time.Time
}

// FreewillTerminalFactSink is an optional local persistence edge. The sink
// must be idempotent because an identical terminal replay retries capture.
type FreewillTerminalFactSink interface {
	CaptureFreewillTerminalFact(context.Context, FreewillTerminalFact) error
}

// FreewillTerminalDiarySink captures a model-authored retrospective only after
// the host terminal fact has been durably captured. The two records remain
// separate and the retrospective sink must preserve its model provenance.
type FreewillTerminalDiarySink interface {
	CaptureFreewillTerminalRetrospective(context.Context, FreewillTerminalFact) error
}

// FreewillTaskAppraisalSink captures a qualitative task self-report after the host receipt is durable.
type FreewillTaskAppraisalSink interface {
	Capture(context.Context, FreewillTerminalFact) error
}

// FreewillTaskReflectionSink records one bounded hypothesis after the task appraisal is durable.
type FreewillTaskReflectionSink interface {
	ReflectTask(context.Context, FreewillTerminalFact) error
}

// FreewillPreferenceHypothesisSink records a separate current-preference hypothesis from exact task evidence.
type FreewillPreferenceHypothesisSink interface {
	DerivePreference(context.Context, FreewillTerminalFact) error
}

// FreewillApprovalService evaluates configured policy and stores a human
// decision for the selected intention. It has no task, tool, or executor.
type FreewillApprovalService struct {
	runtime                  *DurableRuntime
	store                    DurableRunStore
	approvals                FreewillApprovalStore
	policy                   FreewillPolicyEvaluator
	dispatcher               FreewillExecutionDispatcher
	factSink                 FreewillTerminalFactSink
	diarySink                FreewillTerminalDiarySink
	taskAppraisalSink        FreewillTaskAppraisalSink
	taskReflectionSink       FreewillTaskReflectionSink
	preferenceHypothesisSink FreewillPreferenceHypothesisSink
	factMu                   sync.Mutex
	factDone                 map[string]struct{}
	actor                    string
	readOnly                 bool
}

// SetExecutionDispatcher enables the explicit FW-P2 path in a composed
// runtime. The default service remains decision-only when no dispatcher is
// configured.
func (s *FreewillApprovalService) SetExecutionDispatcher(dispatcher FreewillExecutionDispatcher) {
	if s != nil && !s.readOnly {
		s.dispatcher = dispatcher
	}
}

// SetTerminalFactSink enables optional post-terminal local fact capture. The
// default remains off when no sink is configured.
func (s *FreewillApprovalService) SetTerminalFactSink(sink FreewillTerminalFactSink) {
	if s != nil && !s.readOnly {
		s.factSink = sink
	}
}

// SetTerminalDiarySink enables optional local retrospective capture after the
// host fact. It is unset by default and cannot alter the terminal outcome.
func (s *FreewillApprovalService) SetTerminalDiarySink(sink FreewillTerminalDiarySink) {
	if s == nil {
		return
	}
	s.factMu.Lock()
	s.diarySink = sink
	s.factMu.Unlock()
}

func (s *FreewillApprovalService) SetTaskAppraisalSink(sink FreewillTaskAppraisalSink) {
	if s != nil && !s.readOnly {
		s.taskAppraisalSink = sink
	}
}

func (s *FreewillApprovalService) SetTaskReflectionSink(sink FreewillTaskReflectionSink) {
	if s != nil && !s.readOnly {
		s.taskReflectionSink = sink
	}
}

func (s *FreewillApprovalService) SetPreferenceHypothesisSink(sink FreewillPreferenceHypothesisSink) {
	if s != nil && !s.readOnly {
		s.preferenceHypothesisSink = sink
	}
}

func freewillExecutionStatus(dispatch *FreewillExecutionDispatch) string {
	if dispatch == nil {
		return ""
	}
	return dispatch.Status
}

func freewillExecutionRunID(dispatch *FreewillExecutionDispatch) string {
	if dispatch == nil {
		return ""
	}
	return dispatch.ChildRunID
}

func freewillExecutionReportRef(dispatch *FreewillExecutionDispatch) string {
	if dispatch == nil {
		return ""
	}
	return dispatch.ReportRef
}

func freewillExecutionChildStatus(dispatch *FreewillExecutionDispatch) string {
	if dispatch == nil {
		return ""
	}
	return dispatch.ChildStatus
}

func freewillExecutionReportSHA256(dispatch *FreewillExecutionDispatch) string {
	if dispatch == nil {
		return ""
	}
	return dispatch.ReportSHA256
}

func freewillExecutionValidationSummary(dispatch *FreewillExecutionDispatch) string {
	if dispatch == nil {
		return ""
	}
	return dispatch.ValidationSummary
}

func freewillExecutionFailureCode(dispatch *FreewillExecutionDispatch) string {
	if dispatch == nil {
		return ""
	}
	return dispatch.FailureCode
}

func validFreewillFailureCode(code string) bool {
	if strings.TrimSpace(code) == "" || len(code) > 128 {
		return false
	}
	for _, r := range code {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

type FreewillApprovalStatus struct {
	RunID                 string                        `json:"run_id"`
	ProjectID             string                        `json:"project_id,omitempty"`
	ProjectSources        []FreewillPublicProjectSource `json:"project_sources,omitempty"`
	ResumeFromRunID       string                        `json:"resume_from_run_id,omitempty"`
	RequestID             string                        `json:"request_id,omitempty"`
	Lifecycle             freewill.State                `json:"lifecycle"`
	ChoiceID              string                        `json:"choice_id,omitempty"`
	Selected              *FreewillPublicCandidate      `json:"selected,omitempty"`
	Alternatives          []FreewillPublicCandidate     `json:"alternatives,omitempty"`
	ReasonSummary         string                        `json:"reason_summary,omitempty"`
	Provider              string                        `json:"provider,omitempty"`
	Model                 string                        `json:"model,omitempty"`
	Usage                 cost.TokenUsage               `json:"usage,omitempty"`
	ApprovalID            string                        `json:"approval_id,omitempty"`
	ApprovalStatus        string                        `json:"approval_status,omitempty"`
	RequestFingerprint    string                        `json:"request_fingerprint,omitempty"`
	PolicyDecision        string                        `json:"policy_decision,omitempty"`
	ExecutionDisabled     bool                          `json:"execution_disabled"`
	ExecutionStatus       string                        `json:"execution_status,omitempty"`
	ChildRunID            string                        `json:"child_run_id,omitempty"`
	ChildStatus           string                        `json:"child_status,omitempty"`
	ExecutionReportRef    string                        `json:"execution_report_ref,omitempty"`
	ExecutionReportSHA256 string                        `json:"execution_report_sha256,omitempty"`
	ValidationSummary     string                        `json:"validation_summary,omitempty"`
	ExecutionFailureCode  string                        `json:"execution_failure_code,omitempty"`
	PauseReason           string                        `json:"pause_reason,omitempty"`
	SelectionRecoveries   int                           `json:"selection_recoveries,omitempty"`
	SelectionFailureClass string                        `json:"selection_failure_class,omitempty"`
	SelectionFailureCode  string                        `json:"selection_failure_code,omitempty"`
	SelectionNextAt       time.Time                     `json:"selection_next_at,omitempty"`
}

type FreewillPublicCandidate struct {
	CandidateID     string                   `json:"candidate_id"`
	Title           string                   `json:"title"`
	Goal            string                   `json:"goal"`
	Reason          string                   `json:"reason_summary"`
	Source          freewill.CandidateSource `json:"candidate_source"`
	Scope           freewill.Scope           `json:"scope"`
	Capabilities    []string                 `json:"requested_capabilities"`
	Budget          freewill.ResourceBudget  `json:"budget"`
	Score           *int                     `json:"score,omitempty"`
	ScoreFactors    []string                 `json:"score_factors,omitempty"`
	ScoreRationale  string                   `json:"score_rationale,omitempty"`
	BasisRefs       []string                 `json:"basis_refs,omitempty"`
	Uncertainty     string                   `json:"uncertainty,omitempty"`
	ProjectActionID string                   `json:"project_action_id,omitempty"`
}

// FreewillPublicProjectSource is the content-free, relative-path projection of
// an authorized project source used to support a read-only candidate.
type FreewillPublicProjectSource struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func (s *FreewillApprovalService) Status(ctx context.Context, runID string) (FreewillApprovalStatus, error) {
	record, err := s.store.Load(ctx, runID)
	if err != nil {
		return FreewillApprovalStatus{}, err
	}
	return s.statusFromRecord(ctx, record)
}

// List returns a bounded page of durable Freewill status projections. The
// read-only status reader uses the same method, so the API collection remains
// available when the feature is disabled without enabling decisions or wakes.
func (s *FreewillApprovalService) List(ctx context.Context, limit int) ([]FreewillApprovalStatus, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("multiagent: Freewill status listing is unavailable")
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	lister, ok := s.store.(FreewillRunLister)
	if !ok {
		return nil, errors.New("multiagent: durable store does not support Freewill status listing")
	}
	records, err := lister.ListFreewillRuns(ctx, "", limit)
	if err != nil {
		return nil, err
	}
	statuses := make([]FreewillApprovalStatus, 0, len(records))
	for _, record := range records {
		if record.Kind != DurableRunKindFreewill || record.Freewill == nil {
			continue
		}
		status, err := s.statusFromRecord(ctx, record)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func (s *FreewillApprovalService) DecideStatus(ctx context.Context, runID, approvalID, decision, actor, reason string) (FreewillApprovalStatus, error) {
	record, err := s.Decide(ctx, runID, approvalID, decision, actor, reason)
	if err != nil {
		return FreewillApprovalStatus{}, err
	}
	return s.statusFromRecord(ctx, record)
}

// DispatchApproved retries a previously granted intention using the same
// deterministic child identity. It is safe to call after restart or duplicate
// delivery; the dispatcher must make Start idempotent for that child ID.
func (s *FreewillApprovalService) DispatchApproved(ctx context.Context, runID string) (DurableRun, error) {
	if s.dispatcher == nil {
		return DurableRun{}, errors.New("multiagent: Freewill execution is disabled")
	}
	claim, err := s.runtime.claimer.Acquire(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := s.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.State.RequestIdentity == nil ||
		!validFreewillApprovalReceipt(record.Freewill, FreewillApprovalGranted) {
		return record, ErrFreewillApprovalStale
	}
	// A granted record without a durable dispatch marker was approved while
	// execution was disabled. Enabling a dispatcher later must not expand that
	// decision into execution authority.
	if record.Freewill.ExecutionDispatch == nil {
		return record, ErrFreewillApprovalStale
	}
	if err := ensureFreewillNotHalted(ctx, s.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
		return record, err
	}
	if err := ensureFreewillApprovalGeneration(ctx, s.store, record); err != nil {
		return record, err
	}
	return s.dispatchApprovedLocked(ctx, record)
}

// ValidateAcceptedExecutionResume fails closed when operator halt or approval
// generation changed after dispatch admission. It is a read-only checkpoint
// used before startup recovery resumes a nonterminal child.
func (s *FreewillApprovalService) ValidateAcceptedExecutionResume(ctx context.Context, runID string) error {
	if s == nil || s.runtime == nil || s.store == nil {
		return errors.New("multiagent: Freewill execution recovery is unavailable")
	}
	claim, err := s.runtime.claimer.Acquire(ctx, runID)
	if err != nil {
		return err
	}
	defer claim.Release()
	record, err := s.store.Load(ctx, runID)
	if err != nil {
		return err
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.State.RequestIdentity == nil ||
		record.Freewill.ExecutionDispatch == nil || record.Freewill.ExecutionDispatch.Status != "accepted" ||
		!validFreewillApprovalReceipt(record.Freewill, FreewillApprovalGranted) {
		return ErrFreewillApprovalStale
	}
	if err := ensureFreewillNotHalted(ctx, s.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
		return err
	}
	return ensureFreewillApprovalGeneration(ctx, s.store, record)
}

// QuarantineExecutionRecovery durably fences an accepted child when host
// evidence cannot prove that resuming it is safe. It deliberately leaves the
// original approval and child identity intact and records no terminal result.
func (s *FreewillApprovalService) QuarantineExecutionRecovery(ctx context.Context, parentRunID, reason string) (DurableRun, error) {
	if s == nil || s.runtime == nil || s.store == nil || !validFreewillRecoveryReason(reason) {
		return DurableRun{}, errors.New("multiagent: Freewill recovery quarantine is invalid")
	}
	claim, err := s.runtime.claimer.Acquire(ctx, parentRunID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := s.store.Load(ctx, parentRunID)
	if err != nil {
		return DurableRun{}, err
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.Freewill.ExecutionDispatch == nil {
		return record, ErrFreewillApprovalStale
	}
	dispatch := record.Freewill.ExecutionDispatch
	if dispatch.Status == "recovery_uncertain" {
		if dispatch.RecoveryReason == reason {
			return record, nil
		}
		return record, ErrRequestIdentityConflict
	}
	if dispatch.Status != "accepted" || !validFreewillApprovalReceipt(record.Freewill, FreewillApprovalGranted) {
		return record, ErrFreewillApprovalStale
	}
	dispatch.Status = "recovery_uncertain"
	dispatch.RecoveryReason = reason
	dispatch.UpdatedAt = s.runtime.now().UTC()
	candidate, ok := selectedFreewillCandidate(record.Freewill)
	if !ok {
		return record, ErrFreewillApprovalStale
	}
	return s.saveExecutionDispatch(ctx, record, event.EventFreewillExecutionRecoveryUncertain, map[string]any{
		"request_id": candidate.Request.RequestID, "approval_id": record.Freewill.ApprovalRef,
		"child_run_id": dispatch.ChildRunID, "reason_code": reason, "execution_disabled": true,
	})
}

// ReconcileExecutionNoEffectFailure closes an accepted dispatch as failed
// after the child runtime has fenced and terminalized that attempt under the
// child's process-safe run claim. It cannot resume the child or claim success.
func (s *FreewillApprovalService) ReconcileExecutionNoEffectFailure(ctx context.Context, parentRunID string, receipt FreewillMutationNoEffectReceipt) (DurableRun, error) {
	childRunID, attemptID, mutationIDs := receipt.childRunID, receipt.attemptID, receipt.mutationIDs
	if s == nil || s.runtime == nil || s.store == nil || !validFreewillChildRunID(childRunID) ||
		!validAttemptIdentity(attemptID) || len(mutationIDs) == 0 || len(mutationIDs) > 128 {
		return DurableRun{}, errors.New("multiagent: Freewill mutation reconciliation receipt is invalid")
	}
	ids := append([]string(nil), mutationIDs...)
	sort.Strings(ids)
	for i, id := range ids {
		if !validSHA256Hex(id) || (i > 0 && ids[i-1] == id) {
			return DurableRun{}, errors.New("multiagent: Freewill mutation reconciliation ids are invalid")
		}
	}
	claim, err := s.runtime.claimer.Acquire(ctx, parentRunID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := s.store.Load(ctx, parentRunID)
	if err != nil {
		return DurableRun{}, err
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.State.RequestIdentity == nil ||
		!validFreewillApprovalReceipt(record.Freewill, FreewillApprovalGranted) || record.Freewill.ExecutionDispatch == nil {
		return record, ErrFreewillApprovalStale
	}
	dispatch := record.Freewill.ExecutionDispatch
	if dispatch.Status == "mutation_no_effect_reconciled" {
		if dispatch.ChildRunID == childRunID && dispatch.MutationAttemptID == attemptID &&
			strings.Join(dispatch.NoEffectMutationIDs, ",") == strings.Join(ids, ",") {
			return record, nil
		}
		return record, ErrRequestIdentityConflict
	}
	if dispatch.Status != "recovery_uncertain" || dispatch.RecoveryReason != "child_mutation_possible" ||
		dispatch.ChildRunID != childRunID || dispatch.ReportRef != "multiagent_report.json" {
		return record, ErrFreewillApprovalStale
	}
	dispatch.Status = "mutation_no_effect_reconciled"
	dispatch.RecoveryReason = ""
	dispatch.ChildStatus = string(RunStatusFailed)
	dispatch.ValidationSummary = ""
	dispatch.ReportSHA256 = ""
	dispatch.FailureCode = "child_mutation_no_effect_proven"
	dispatch.MutationAttemptID = attemptID
	dispatch.NoEffectMutationIDs = ids
	dispatch.UpdatedAt = s.runtime.now().UTC()
	record.Freewill.Lifecycle = freewill.StatePaused
	record.Freewill.PauseReason = "child_mutation_no_effect_proven"
	candidate, ok := selectedFreewillCandidate(record.Freewill)
	if !ok {
		return record, ErrFreewillApprovalStale
	}
	saved, err := s.saveExecutionDispatch(ctx, record, event.EventFreewillExecutionMutationNoEffectReconciled, map[string]any{
		"request_id": candidate.Request.RequestID, "approval_id": record.Freewill.ApprovalRef,
		"child_run_id": childRunID, "attempt_id": attemptID,
		"mutation_ids": ids, "reason_code": dispatch.FailureCode, "child_status": string(RunStatusFailed),
		"execution_disabled": true,
	})
	return saved, err
}

// ReconcileExecutionTerminal persists the result of the exact accepted child.
// Replays of the same result are no-ops; a conflicting terminal result cannot
// overwrite the first durable outcome.
func (s *FreewillApprovalService) ReconcileExecutionTerminal(ctx context.Context, parentRunID string, result FreewillExecutionTerminalResult) (DurableRun, error) {
	if s == nil || s.runtime == nil || s.store == nil {
		return DurableRun{}, errors.New("multiagent: Freewill execution reconciliation is unavailable")
	}
	if !result.ChildStatus.Terminal() || !result.ChildStatus.Valid() || result.ReportRef != "multiagent_report.json" ||
		!validFreewillValidationSummary(result.ValidationSummary) || !validSHA256Hex(result.ReportSHA256) {
		return DurableRun{}, errors.New("multiagent: Freewill terminal child result is invalid")
	}
	if result.ChildStatus == RunStatusCompleted {
		if result.FailureCode != "" {
			return DurableRun{}, errors.New("multiagent: completed child result has a failure code")
		}
	} else if !validFreewillFailureCode(result.FailureCode) {
		return DurableRun{}, errors.New("multiagent: failed child result requires a safe failure code")
	}
	claim, err := s.runtime.claimer.Acquire(ctx, parentRunID)
	if err != nil {
		return DurableRun{}, err
	}
	claimReleased := false
	releaseClaim := func() {
		if !claimReleased {
			claim.Release()
			claimReleased = true
		}
	}
	defer releaseClaim()
	record, err := s.store.Load(ctx, parentRunID)
	if err != nil {
		return DurableRun{}, err
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.State.RequestIdentity == nil ||
		!validFreewillApprovalReceipt(record.Freewill, FreewillApprovalGranted) || record.Freewill.ExecutionDispatch == nil {
		return record, ErrFreewillApprovalStale
	}
	dispatch := record.Freewill.ExecutionDispatch
	if dispatch.ChildRunID != result.ChildRunID || dispatch.ReportRef != result.ReportRef {
		return record, ErrRequestIdentityConflict
	}
	quarantined := dispatch.Status == "recovery_uncertain"
	if quarantined {
		switch dispatch.RecoveryReason {
		case "child_manifest_missing", "terminal_report_unavailable", "terminal_report_invalid":
			// A later host inspection may repair an evidence-only quarantine.
		case "child_mutation_possible":
			return record, errors.New("multiagent: terminal report cannot resolve possible external mutation")
		default:
			return record, ErrFreewillApprovalStale
		}
	}
	if dispatch.Status == "dispatching" {
		return record, ErrFreewillDispatchNotAccepted
	}
	status := "execution_failed"
	eventType := event.EventFreewillExecutionFailed
	if result.ChildStatus == RunStatusCompleted {
		status = "completed"
		eventType = event.EventFreewillExecutionCompleted
	} else if result.ChildStatus == RunStatusCancelled {
		status = "execution_cancelled"
		eventType = event.EventFreewillExecutionCancelled
	}
	if dispatch.Status == "completed" || dispatch.Status == "execution_failed" || dispatch.Status == "execution_cancelled" {
		if dispatch.Status == status && dispatch.ChildStatus == string(result.ChildStatus) &&
			dispatch.ValidationSummary == result.ValidationSummary && dispatch.ReportSHA256 == result.ReportSHA256 &&
			dispatch.FailureCode == result.FailureCode {
			releaseClaim()
			return record, s.captureTerminalFact(ctx, record, result)
		}
		return record, ErrRequestIdentityConflict
	}
	if dispatch.Status != "accepted" && !quarantined {
		return record, ErrFreewillApprovalStale
	}
	dispatch.Status = status
	dispatch.RecoveryReason = ""
	dispatch.ChildStatus = string(result.ChildStatus)
	dispatch.ValidationSummary = result.ValidationSummary
	dispatch.ReportSHA256 = result.ReportSHA256
	dispatch.FailureCode = result.FailureCode
	dispatch.UpdatedAt = s.runtime.now().UTC()
	candidate, ok := selectedFreewillCandidate(record.Freewill)
	if !ok {
		return record, ErrFreewillApprovalStale
	}
	saved, err := s.saveExecutionDispatch(ctx, record, eventType, map[string]any{
		"request_id": candidate.Request.RequestID, "approval_id": record.Freewill.ApprovalRef,
		"child_run_id": result.ChildRunID, "child_status": result.ChildStatus, "report_ref": result.ReportRef,
		"report_sha256": result.ReportSHA256, "validation_summary": result.ValidationSummary,
		"execution_disabled": true, "failure_code": result.FailureCode,
	})
	if err != nil {
		return saved, err
	}
	releaseClaim()
	return saved, s.captureTerminalFact(ctx, saved, result)
}

func (s *FreewillApprovalService) captureTerminalFact(ctx context.Context, record DurableRun, result FreewillExecutionTerminalResult) error {
	if s.factSink == nil {
		return nil
	}
	candidate, ok := selectedFreewillCandidate(record.Freewill)
	if !ok || record.State.RequestIdentity == nil || record.Freewill.ExecutionDispatch == nil {
		return ErrFreewillApprovalStale
	}
	s.factMu.Lock()
	defer s.factMu.Unlock()
	if _, ok := s.factDone[record.State.RunID]; ok {
		return nil
	}
	dispatch := record.Freewill.ExecutionDispatch
	fact := FreewillTerminalFact{
		ProjectID: record.Freewill.Scope.ProjectID, Workspace: record.Freewill.Scope.Workspace,
		AgentID: record.State.RequestIdentity.RequesterID, IntentionID: candidate.CandidateID,
		ParentRunID: record.State.RunID, ChildRunID: result.ChildRunID, Status: dispatch.Status,
		ReportRef: dispatch.ReportRef, ReportSHA256: dispatch.ReportSHA256,
		ValidationSummary: dispatch.ValidationSummary, FailureCode: dispatch.FailureCode,
		RecordedAt: dispatch.UpdatedAt,
	}
	if err := s.factSink.CaptureFreewillTerminalFact(ctx, fact); err != nil {
		return err
	}
	if s.diarySink != nil {
		if err := s.diarySink.CaptureFreewillTerminalRetrospective(ctx, fact); err != nil {
			return err
		}
	}
	if s.taskAppraisalSink != nil {
		if err := s.taskAppraisalSink.Capture(ctx, fact); err != nil {
			return err
		}
	}
	if s.taskReflectionSink != nil {
		if err := s.taskReflectionSink.ReflectTask(ctx, fact); err != nil {
			return err
		}
	}
	if s.preferenceHypothesisSink != nil {
		if err := s.preferenceHypothesisSink.DerivePreference(ctx, fact); err != nil {
			return err
		}
	}
	if s.factDone == nil {
		s.factDone = make(map[string]struct{})
	}
	s.factDone[record.State.RunID] = struct{}{}
	return nil
}

// RetryTerminalFactCapture retries the optional fact sink for an already
// durable terminal outcome. It neither changes that outcome nor emits another
// terminal event; the sink must use the stable parent-run capture key.
func (s *FreewillApprovalService) RetryTerminalFactCapture(ctx context.Context, parentRunID string) error {
	if s == nil || s.factSink == nil {
		return nil
	}
	if s.store == nil {
		return errors.New("multiagent: Freewill terminal fact store is unavailable")
	}
	record, err := s.store.Load(ctx, parentRunID)
	if err != nil {
		return err
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.Freewill.ExecutionDispatch == nil {
		return ErrFreewillApprovalStale
	}
	dispatch := record.Freewill.ExecutionDispatch
	if dispatch.Status != "completed" && dispatch.Status != "execution_failed" && dispatch.Status != "execution_cancelled" {
		return nil
	}
	childStatus := RunStatus(dispatch.ChildStatus)
	if !childStatus.Terminal() || !childStatus.Valid() {
		return ErrFreewillApprovalStale
	}
	return s.captureTerminalFact(ctx, record, FreewillExecutionTerminalResult{
		ChildRunID: dispatch.ChildRunID, ChildStatus: childStatus, ReportRef: dispatch.ReportRef,
		ReportSHA256: dispatch.ReportSHA256, ValidationSummary: dispatch.ValidationSummary, FailureCode: dispatch.FailureCode,
	})
}

func (s *FreewillApprovalService) statusFromRecord(ctx context.Context, record DurableRun) (FreewillApprovalStatus, error) {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil {
		return FreewillApprovalStatus{}, errors.New("multiagent: run is not a Freewill intention")
	}
	status := FreewillApprovalStatus{RunID: record.State.RunID, ResumeFromRunID: record.Freewill.ResumeFromRunID,
		ProjectID: record.Freewill.Scope.ProjectID,
		Lifecycle: record.Freewill.Lifecycle, ChoiceID: record.Freewill.ChoiceID,
		ApprovalID: record.Freewill.ApprovalRef, RequestFingerprint: record.Freewill.ApprovalFingerprint,
		PolicyDecision: record.Freewill.PolicyDecision, ExecutionDisabled: record.Freewill.ExecutionDisabled, PauseReason: record.Freewill.PauseReason,
		ExecutionStatus:       freewillExecutionStatus(record.Freewill.ExecutionDispatch),
		ChildRunID:            freewillExecutionRunID(record.Freewill.ExecutionDispatch),
		ExecutionReportRef:    freewillExecutionReportRef(record.Freewill.ExecutionDispatch),
		ChildStatus:           freewillExecutionChildStatus(record.Freewill.ExecutionDispatch),
		ExecutionReportSHA256: freewillExecutionReportSHA256(record.Freewill.ExecutionDispatch),
		ValidationSummary:     freewillExecutionValidationSummary(record.Freewill.ExecutionDispatch),
		ExecutionFailureCode:  freewillExecutionFailureCode(record.Freewill.ExecutionDispatch),
		ReasonSummary:         record.Freewill.ReasonSummary, Provider: record.Freewill.SelectionProvider, Model: record.Freewill.SelectionModel,
		Usage: record.Freewill.SelectionUsage, SelectionRecoveries: record.Freewill.SelectionRecoveryNumber,
		SelectionFailureClass: record.Freewill.SelectionFailureClass, SelectionFailureCode: record.Freewill.SelectionFailureCode,
		SelectionNextAt: record.Freewill.SelectionNextAt}
	_, status.ProjectSources = publicFreewillProjectEvidence(status.ProjectID, record.Freewill.ProjectContextManifest)
	for _, candidate := range record.Freewill.Candidates {
		public := publicFreewillCandidate(candidate)
		if candidate.CandidateID == record.Freewill.ChoiceID {
			status.RequestID = candidate.Request.RequestID
			status.Selected = &public
		} else {
			status.Alternatives = append(status.Alternatives, public)
		}
	}
	if status.ApprovalID != "" {
		item, err := s.approvals.Load(record.State.RunID, status.ApprovalID)
		if err != nil {
			return FreewillApprovalStatus{}, err
		}
		if item.RunID != record.State.RunID || item.ApprovalID != status.ApprovalID || item.SubjectType != approval.SubjectIntention || item.Intention == nil || item.Intention.RequestFingerprint != record.Freewill.ApprovalFingerprint {
			return FreewillApprovalStatus{}, ErrFreewillApprovalStale
		}
		control, ok := s.store.(FreewillOperatorControlStore)
		if !ok {
			return FreewillApprovalStatus{}, ErrFreewillControlUnavailable
		}
		controlState, err := control.LoadFreewillOperatorControl(ctx, record.State.RequestIdentity.RequesterID, record.Freewill.Scope)
		if err != nil {
			return FreewillApprovalStatus{}, err
		}
		if item.Intention.ControlGeneration != record.Freewill.ApprovalGeneration || controlState.Generation != record.Freewill.ApprovalGeneration {
			status.ApprovalStatus = "revoked"
			status.Lifecycle = freewill.StateHalted
			status.PauseReason = "operator_revoked"
			status.ExecutionDisabled = true
			return status, nil
		}
		status.ApprovalStatus = item.Status
	}
	return status, nil
}

func publicFreewillCandidate(candidate freewill.CandidateIntention) FreewillPublicCandidate {
	var score *int
	if candidate.Score >= 0 && candidate.Score <= 100 {
		value := candidate.Score
		score = &value
	}
	factors := make([]string, 0, len(candidate.ScoreFactors))
	allowed := map[string]bool{"scope_fit": true, "feasibility": true, "impact": true, "novelty": true, "readiness": true}
	seen := make(map[string]bool, len(candidate.ScoreFactors))
	for _, factor := range candidate.ScoreFactors {
		if allowed[factor] && !seen[factor] && len(factors) < 5 {
			factors = append(factors, factor)
			seen[factor] = true
		}
	}
	rationale := candidate.ScoreRationale
	if len(rationale) > 500 {
		rationale = rationale[:500]
	}
	lower := strings.ToLower(rationale)
	for _, marker := range []string{"bearer ", "authorization:", "api_key=", "apikey=", "token=", "password=", "secret="} {
		if strings.Contains(lower, marker) {
			rationale = "[REDACTED]"
			break
		}
	}
	return FreewillPublicCandidate{CandidateID: candidate.CandidateID, Title: candidate.Request.Title, Goal: candidate.Request.Goal,
		Reason: candidate.Request.ReasonSummary, Source: candidate.Request.CandidateSource, Scope: candidate.Request.Scope,
		Capabilities: append([]string(nil), candidate.Request.RequestedCapabilities...), Budget: candidate.Request.Budget,
		Score: score, ScoreFactors: factors, ScoreRationale: rationale,
		BasisRefs: append([]string(nil), candidate.BasisRefs...), Uncertainty: candidate.Uncertainty, ProjectActionID: candidate.ProjectActionID}
}

func publicFreewillProjectEvidence(projectID string, manifest *freewill.ProjectContextManifest) (string, []FreewillPublicProjectSource) {
	if manifest == nil || manifest.ProjectID != projectID || manifest.Validate() != nil {
		return "", nil
	}
	sources := make([]FreewillPublicProjectSource, 0, len(manifest.Sources))
	for _, source := range manifest.Sources {
		if strings.HasPrefix(source.ID, "project:") && source.Path != "" {
			sources = append(sources, FreewillPublicProjectSource{ID: source.ID, Path: source.Path, SHA256: source.Digest})
		}
	}
	return projectID, sources
}

func NewFreewillApprovalService(runtime *DurableRuntime, store DurableRunStore, approvals FreewillApprovalStore, evaluator FreewillPolicyEvaluator, policyActor string) (*FreewillApprovalService, error) {
	if runtime == nil || store == nil || approvals == nil || evaluator == nil || strings.TrimSpace(policyActor) == "" {
		return nil, errors.New("multiagent: Freewill approval requires runtime, run store, approval store, policy, and actor")
	}
	return &FreewillApprovalService{runtime: runtime, store: store, approvals: approvals, policy: evaluator, actor: policyActor}, nil
}

// NewFreewillApprovalStatusReader composes the existing status projection over
// an existing read-only run store. It cannot make or reconcile decisions.
func NewFreewillApprovalStatusReader(store DurableRunStore, approvals FreewillApprovalStore) (*FreewillApprovalService, error) {
	if store == nil || approvals == nil {
		return nil, errors.New("multiagent: Freewill status reader requires run and approval stores")
	}
	return &FreewillApprovalService{store: store, approvals: approvals, readOnly: true}, nil
}

// RequestApproval evaluates the existing policy engine before creating an
// intention-specific approval. Repeated calls reuse the same exact subject.
func (s *FreewillApprovalService) RequestApproval(ctx context.Context, runID string) (DurableRun, error) {
	if s.readOnly {
		return DurableRun{}, errors.New("multiagent: Freewill status reader is read-only")
	}
	claim, err := s.runtime.claimer.Acquire(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := s.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	if record.Kind == DurableRunKindFreewill && record.Freewill != nil {
		if err := ensureFreewillNotHalted(ctx, s.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
			return record, err
		}
		control, ok := s.store.(FreewillOperatorControlStore)
		if !ok {
			return record, ErrFreewillControlUnavailable
		}
		controlState, err := control.LoadFreewillOperatorControl(ctx, record.State.RequestIdentity.RequesterID, record.Freewill.Scope)
		if err != nil {
			return record, err
		}
		record.Freewill.ApprovalGeneration = controlState.Generation
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.Freewill.Lifecycle != freewill.StateProposed || record.Freewill.ChoiceID == "" {
		return record, errors.New("multiagent: Freewill run has no proposed choice")
	}
	candidate, ok := selectedFreewillCandidate(record.Freewill)
	if !ok {
		return record, errors.New("multiagent: selected Freewill candidate is missing")
	}
	if err := validateFreewillApprovalCandidate(record, candidate); err != nil {
		return record, err
	}
	fingerprint, canonicalRequest := freewillRequestFingerprint(candidate.Request)
	decision := s.policy.Evaluate(policy.PolicyRequest{
		Subject:  policy.Subject{Type: "agent", ID: record.State.RequestIdentity.RequesterID},
		Action:   "intention.propose",
		Resource: policy.Resource{Type: "intention", Name: "freewill_candidate"},
		Context:  policy.Context{Project: record.Freewill.Scope.ProjectID, Mode: "proposal"},
	})
	if decision.Decision == policy.DecisionDenied {
		record.Freewill.Lifecycle = freewill.StateDenied
		record.Freewill.PolicyDecision = string(decision.Decision)
		record.Freewill.PolicyRuleID = safePolicyRuleID(decision.RuleID)
		record.Freewill.ExecutionDisabled = true
		record.Freewill.PauseReason = "policy_denied"
		return s.saveFreewillDecision(ctx, record, event.EventFreewillPolicyDenied, map[string]any{
			"request_id": canonicalRequest.RequestID, "policy_rule_id": record.Freewill.PolicyRuleID,
			"reason_code": "policy_denied", "execution_disabled": true,
		})
	}
	if decision.Decision != policy.DecisionAllowed && decision.Decision != policy.DecisionRequiresApproval {
		return record, errors.New("multiagent: Freewill policy outcome is unsupported")
	}
	ref := approval.IntentionApprovalRef{RequestID: canonicalRequest.RequestID, RequestFingerprint: fingerprint, ControlGeneration: record.Freewill.ApprovalGeneration,
		ScopeSummary: "project:" + record.Freewill.Scope.ProjectID, Capabilities: canonicalRequest.RequestedCapabilities}
	item, err := s.findOrCreateApproval(record, ref)
	if err != nil {
		return record, err
	}
	if err := item.ValidateSubject(); err != nil || item.RunID != runID || item.ApprovalID == "" ||
		item.SubjectType != approval.SubjectIntention || item.Intention == nil || item.Intention.RequestID != canonicalRequest.RequestID ||
		item.Intention.RequestFingerprint != fingerprint || item.Intention.ControlGeneration != record.Freewill.ApprovalGeneration {
		return record, ErrFreewillApprovalStale
	}
	if item.Status == approval.StatusApproved {
		record.Freewill.Lifecycle = freewill.StatePaused
		record.Freewill.PauseReason = "execution_disabled"
	} else if item.Status == approval.StatusDenied {
		record.Freewill.Lifecycle = freewill.StateDenied
		record.Freewill.PauseReason = "human_denied"
	} else if item.Status == approval.StatusPending {
		record.Freewill.Lifecycle = freewill.StateApprovalPending
		record.Freewill.PauseReason = "awaiting_human_decision"
	} else {
		return record, ErrFreewillApprovalStale
	}
	record.Freewill.ApprovalRef = item.ApprovalID
	record.Freewill.ApprovalFingerprint = fingerprint
	approvalDecision, ok := freewillApprovalDecision(item.Status)
	if !ok {
		return record, ErrFreewillApprovalStale
	}
	record.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: item.ApprovalID, RequestFingerprint: fingerprint, Decision: approvalDecision, ControlGeneration: record.Freewill.ApprovalGeneration}
	record.Freewill.PolicyDecision = string(decision.Decision)
	record.Freewill.PolicyRuleID = safePolicyRuleID(decision.RuleID)
	record.Freewill.ExecutionDisabled = true
	return s.saveFreewillDecision(ctx, record, event.EventFreewillApprovalProposed, map[string]any{
		"request_id": canonicalRequest.RequestID, "approval_id": item.ApprovalID, "approval_status": item.Status,
		"request_fingerprint": fingerprint, "execution_disabled": true,
	})
}

// RecoverApprovalDecision closes the crash window where the existing approval
// store durably saved a human decision but the workflow checkpoint did not.
// It reads the same immutable approval subject and only mirrors that decision;
// it never creates, overwrites, or executes an approval.
func (s *FreewillApprovalService) RecoverApprovalDecision(ctx context.Context, runID string) (DurableRun, error) {
	if s.readOnly {
		return DurableRun{}, errors.New("multiagent: Freewill status reader is read-only")
	}
	claim, err := s.runtime.claimer.Acquire(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := s.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	if record.Kind == DurableRunKindFreewill && record.Freewill != nil {
		if err := ensureFreewillNotHalted(ctx, s.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
			return record, err
		}
		if err := ensureFreewillApprovalGeneration(ctx, s.store, record); err != nil {
			return record, err
		}
	}
	legacyParked := record.Kind == DurableRunKindFreewill && record.Freewill != nil && record.Freewill.Lifecycle == freewill.StatePaused && record.Freewill.PauseReason == "execution_disabled" && record.Freewill.ApprovalRef != "" && record.Freewill.ApprovalReceipt == nil
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || (record.Freewill.Lifecycle != freewill.StateApprovalPending && !legacyParked) || record.Freewill.ApprovalRef == "" {
		return record, nil
	}
	item, err := s.approvals.Load(runID, record.Freewill.ApprovalRef)
	if err != nil {
		return record, err
	}
	candidate, ok := selectedFreewillCandidate(record.Freewill)
	if !ok {
		return record, ErrFreewillApprovalStale
	}
	if err := validateFreewillApprovalCandidate(record, candidate); err != nil {
		return record, err
	}
	fingerprint, _ := freewillRequestFingerprint(candidate.Request)
	if err := item.ValidateSubject(); err != nil || item.RunID != runID || item.ApprovalID != record.Freewill.ApprovalRef ||
		item.SubjectType != approval.SubjectIntention || item.Intention == nil || item.Intention.RequestID != candidate.Request.RequestID ||
		item.Intention.RequestFingerprint != fingerprint || item.Intention.ControlGeneration != record.Freewill.ApprovalGeneration || fingerprint != record.Freewill.ApprovalFingerprint {
		return record, ErrFreewillApprovalStale
	}
	if item.Status == approval.StatusPending {
		if !legacyParked {
			return record, nil
		}
		record.Freewill.Lifecycle = freewill.StateApprovalPending
		record.Freewill.PauseReason = "awaiting_human_decision"
		record.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: item.ApprovalID, RequestFingerprint: fingerprint, Decision: FreewillApprovalPending, ControlGeneration: record.Freewill.ApprovalGeneration}
		record.Freewill.ExecutionDisabled = true
		return s.saveFreewillDecision(ctx, record, event.EventFreewillApprovalReconciled, map[string]any{
			"request_id": candidate.Request.RequestID, "approval_id": item.ApprovalID, "decision": item.Status,
			"lifecycle": string(record.Freewill.Lifecycle), "execution_disabled": true, "reason_code": "legacy_approval_reconciled",
		})
	}
	recoveryEvent := event.EventFreewillApprovalDecided
	if legacyParked {
		recoveryEvent = event.EventFreewillApprovalReconciled
	}
	if item.Status == approval.StatusApproved {
		current := s.policy.Evaluate(policy.PolicyRequest{Subject: policy.Subject{Type: "agent", ID: record.State.RequestIdentity.RequesterID},
			Action: "intention.propose", Resource: policy.Resource{Type: "intention", Name: "freewill_candidate"},
			Context: policy.Context{Project: record.Freewill.Scope.ProjectID, Mode: "proposal"}})
		if current.Decision != policy.DecisionAllowed && current.Decision != policy.DecisionRequiresApproval {
			record.Freewill.Lifecycle = freewill.StateDenied
			record.Freewill.PauseReason = "policy_denied"
			record.Freewill.ApprovalReceipt = nil
			record.Freewill.PolicyDecision = string(current.Decision)
		} else {
			record.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: item.ApprovalID, RequestFingerprint: fingerprint, Decision: FreewillApprovalGranted, ControlGeneration: record.Freewill.ApprovalGeneration}
			record.Freewill.Lifecycle = freewill.StatePaused
			record.Freewill.PauseReason = "execution_disabled"
		}
	} else if item.Status == approval.StatusDenied {
		record.Freewill.Lifecycle = freewill.StateDenied
		record.Freewill.PauseReason = "human_denied"
		record.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: item.ApprovalID, RequestFingerprint: fingerprint, Decision: FreewillApprovalDenied, ControlGeneration: record.Freewill.ApprovalGeneration}
	} else {
		return record, ErrFreewillApprovalStale
	}
	record.Freewill.ExecutionDisabled = true
	return s.saveFreewillDecision(ctx, record, recoveryEvent, map[string]any{
		"request_id": candidate.Request.RequestID, "approval_id": item.ApprovalID, "decision": item.Status,
		"lifecycle": string(record.Freewill.Lifecycle), "execution_disabled": true,
	})
}

// Decide records a human grant or denial. A configured dispatcher receives a
// grant only after the receipt is durably stored; without one, it remains a
// visibly paused, execution-disabled decision-only record.
func (s *FreewillApprovalService) Decide(ctx context.Context, runID, approvalID, decision, actor, reason string) (DurableRun, error) {
	if s.readOnly {
		return DurableRun{}, errors.New("multiagent: Freewill status reader is read-only")
	}
	if strings.TrimSpace(actor) == "" || (decision != "grant" && decision != "deny") {
		return DurableRun{}, errors.New("multiagent: Freewill decision is invalid")
	}
	claim, err := s.runtime.claimer.Acquire(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	defer claim.Release()
	record, err := s.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	if record.Kind == DurableRunKindFreewill && record.Freewill != nil {
		if err := ensureFreewillNotHalted(ctx, s.store, record.State.RequestIdentity.RequesterID, record.Freewill.Scope); err != nil {
			return record, err
		}
		if err := ensureFreewillApprovalGeneration(ctx, s.store, record); err != nil {
			return record, err
		}
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.Freewill.Lifecycle != freewill.StateApprovalPending || record.Freewill.ApprovalRef != approvalID {
		return record, ErrFreewillApprovalStale
	}
	candidate, ok := selectedFreewillCandidate(record.Freewill)
	if !ok {
		return record, ErrFreewillApprovalStale
	}
	if err := validateFreewillApprovalCandidate(record, candidate); err != nil {
		return record, err
	}
	fingerprint, _ := freewillRequestFingerprint(candidate.Request)
	if fingerprint != record.Freewill.ApprovalFingerprint {
		return record, ErrFreewillApprovalStale
	}
	item, err := s.approvals.Load(runID, approvalID)
	if err != nil {
		return record, err
	}
	if err := item.ValidateSubject(); err != nil || item.RunID != runID || item.ApprovalID != approvalID || item.SubjectType != approval.SubjectIntention || item.Intention == nil ||
		item.Intention.RequestID != candidate.Request.RequestID || item.Intention.RequestFingerprint != fingerprint || item.Intention.ControlGeneration != record.Freewill.ApprovalGeneration {
		return record, ErrFreewillApprovalStale
	}
	if decision == "grant" {
		currentPolicy := s.policy.Evaluate(policy.PolicyRequest{
			Subject: policy.Subject{Type: "agent", ID: record.State.RequestIdentity.RequesterID}, Action: "intention.propose",
			Resource: policy.Resource{Type: "intention", Name: "freewill_candidate"},
			Context:  policy.Context{Project: record.Freewill.Scope.ProjectID, Mode: "proposal"},
		})
		if currentPolicy.Decision != policy.DecisionAllowed && currentPolicy.Decision != policy.DecisionRequiresApproval {
			return record, errors.New("multiagent: Freewill policy no longer permits this request")
		}
		if item.Status == approval.StatusPending {
			if err := item.Approve(actor); err != nil {
				return record, err
			}
			if err := s.approvals.Save(item); err != nil {
				return record, err
			}
		} else if item.Status != approval.StatusApproved {
			return record, ErrFreewillApprovalStale
		}
		record.Freewill.Lifecycle = freewill.StatePaused
		record.Freewill.PauseReason = "execution_disabled"
	} else {
		if item.Status == approval.StatusPending {
			if err := item.Deny(actor, safeDecisionReason(reason)); err != nil {
				return record, err
			}
			if err := s.approvals.Save(item); err != nil {
				return record, err
			}
		} else if item.Status != approval.StatusDenied {
			return record, ErrFreewillApprovalStale
		}
		record.Freewill.Lifecycle = freewill.StateDenied
		record.Freewill.PauseReason = "human_denied"
	}
	record.Freewill.ExecutionDisabled = true
	receiptDecision := FreewillApprovalDenied
	if decision == "grant" {
		receiptDecision = FreewillApprovalGranted
	}
	record.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: approvalID, RequestFingerprint: fingerprint, Decision: receiptDecision, ControlGeneration: record.Freewill.ApprovalGeneration}
	evt := event.EventFreewillApprovalDecided
	payload := map[string]any{"request_id": candidate.Request.RequestID, "approval_id": approvalID,
		"decision": decision, "lifecycle": string(record.Freewill.Lifecycle), "execution_disabled": true}
	if record.Freewill.PolicyRuleID != "" {
		payload["policy_rule_id"] = record.Freewill.PolicyRuleID
	}
	saved, err := s.saveFreewillDecision(ctx, record, evt, payload)
	if err != nil || decision != "grant" || s.dispatcher == nil {
		return saved, err
	}
	return s.dispatchApprovedLocked(ctx, saved)
}

func freewillExecutionChildRunID(requesterID, parentRunID, approvalID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{requesterID, parentRunID, approvalID}, "\x00")))
	return "run_" + hex.EncodeToString(sum[:13])
}

func (s *FreewillApprovalService) dispatchApprovedLocked(ctx context.Context, record DurableRun) (DurableRun, error) {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.State.RequestIdentity == nil ||
		!validFreewillApprovalReceipt(record.Freewill, FreewillApprovalGranted) {
		return record, ErrFreewillApprovalStale
	}
	if dispatch := record.Freewill.ExecutionDispatch; dispatch != nil && (dispatch.Status == "accepted" || dispatch.Status == "completed" || dispatch.Status == "recovery_uncertain" ||
		dispatch.Status == "execution_failed" || dispatch.Status == "execution_cancelled") {
		return record, nil
	}
	candidate, ok := selectedFreewillCandidate(record.Freewill)
	if !ok {
		return record, ErrFreewillApprovalStale
	}
	fingerprint, _ := freewillRequestFingerprint(candidate.Request)
	if fingerprint != record.Freewill.ApprovalFingerprint {
		return record, ErrFreewillApprovalStale
	}
	identity := &RequestIdentity{RequesterID: record.State.RequestIdentity.RequesterID,
		SessionID: record.State.RunID, Source: "freewill", IdempotencyKey: record.Freewill.ApprovalRef}
	childRunID := freewillExecutionChildRunID(identity.RequesterID, identity.SessionID, identity.IdempotencyKey)
	now := s.runtime.now().UTC()
	dispatch := record.Freewill.ExecutionDispatch
	if dispatch == nil {
		return record, ErrFreewillApprovalStale
	}
	if dispatch.Status != "dispatching" && dispatch.Status != "failed" {
		return record, ErrFreewillApprovalStale
	}
	if dispatch.ChildRunID == "" {
		record.Freewill.ExecutionDispatch = &FreewillExecutionDispatch{Status: "dispatching", ChildRunID: childRunID,
			ReportRef: "multiagent_report.json", UpdatedAt: now}
		var err error
		record, err = s.saveExecutionDispatch(ctx, record, event.EventFreewillExecutionDispatching, map[string]any{
			"request_id": candidate.Request.RequestID, "approval_id": record.Freewill.ApprovalRef,
			"child_run_id": childRunID, "execution_disabled": true,
		})
		if err != nil {
			return record, err
		}
	} else if dispatch.ChildRunID != childRunID || dispatch.ReportRef != "multiagent_report.json" {
		return record, ErrRequestIdentityConflict
	} else if dispatch.Status == "failed" {
		record.Freewill.ExecutionDispatch = &FreewillExecutionDispatch{Status: "dispatching", ChildRunID: childRunID,
			ReportRef: "multiagent_report.json", UpdatedAt: now}
		var err error
		record, err = s.saveExecutionDispatch(ctx, record, event.EventFreewillExecutionDispatching, map[string]any{
			"request_id": candidate.Request.RequestID, "approval_id": record.Freewill.ApprovalRef,
			"child_run_id": childRunID, "execution_disabled": true,
		})
		if err != nil {
			return record, err
		}
	}
	result, err := s.dispatcher.Start(ctx, FreewillExecutionRequest{
		ParentRunID: record.State.RunID, ApprovalID: record.Freewill.ApprovalRef,
		RequestFingerprint: fingerprint, ApprovalGeneration: record.Freewill.ApprovalGeneration,
		RunID: childRunID, Identity: identity, Candidate: candidate,
	})
	if err != nil || result.RunID != childRunID || strings.TrimSpace(result.ReportRef) == "" {
		record.Freewill.ExecutionDispatch = &FreewillExecutionDispatch{Status: "failed", ChildRunID: childRunID,
			ReportRef: "multiagent_report.json", FailureCode: "workflow_start_failed", UpdatedAt: s.runtime.now().UTC()}
		failed, saveErr := s.saveExecutionDispatch(ctx, record, event.EventFreewillExecutionDispatchFailed, map[string]any{
			"request_id": candidate.Request.RequestID, "approval_id": record.Freewill.ApprovalRef,
			"failure_code": "workflow_start_failed", "execution_disabled": true,
		})
		if saveErr != nil {
			return record, saveErr
		}
		return failed, nil
	}
	record.Freewill.ExecutionDispatch = &FreewillExecutionDispatch{Status: "accepted", ChildRunID: childRunID,
		ReportRef: result.ReportRef, UpdatedAt: s.runtime.now().UTC()}
	return s.saveExecutionDispatch(ctx, record, event.EventFreewillExecutionDispatched, map[string]any{
		"request_id": candidate.Request.RequestID, "approval_id": record.Freewill.ApprovalRef,
		"child_run_id": childRunID, "report_ref": result.ReportRef, "execution_disabled": true,
	})
}

func (s *FreewillApprovalService) saveExecutionDispatch(ctx context.Context, record DurableRun, eventType string, payload map[string]any) (DurableRun, error) {
	if err := record.Validate(s.runtime.supervisor.graph); err != nil {
		return record, err
	}
	record.State.UpdatedAt = s.runtime.now().UTC()
	payload["run_id"] = record.State.RunID
	for key, value := range freewillEventContext(record) {
		payload[key] = value
	}
	evt := event.NewEvent(eventType, freewillWakeEventSource, payload)
	evt.Timestamp = record.State.UpdatedAt.Format(time.RFC3339Nano)
	evt.CorrelationID = record.Freewill.CorrelationID
	evt.Metadata.RunID = record.State.RunID
	saved, err := s.store.Checkpoint(ctx, record.Revision, record, []event.Event{evt})
	if err != nil {
		return record, err
	}
	if err := s.runtime.dispatchOutbox(ctx, record.State.RunID); err != nil {
		return saved, err
	}
	return saved, nil
}

func (s *FreewillApprovalService) findOrCreateApproval(record DurableRun, ref approval.IntentionApprovalRef) (*approval.Approval, error) {
	items, err := s.approvals.List(record.State.RunID)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.SubjectType == approval.SubjectIntention && item.Intention != nil && item.Intention.RequestID == ref.RequestID &&
			item.Intention.RequestFingerprint == ref.RequestFingerprint && item.Intention.ControlGeneration == ref.ControlGeneration {
			return item, nil
		}
	}
	item, err := approval.NewIntentionApproval(record.State.RunID, record.Freewill.CorrelationID,
		record.State.RequestIdentity.RequesterID, record.Freewill.Scope.ProjectID, ref,
		approval.PolicyDecision{Decision: approval.DecisionRequiresApproval, Reason: "policy_evaluated"})
	if err != nil {
		return nil, err
	}
	if err := s.approvals.Save(item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *FreewillApprovalService) saveFreewillDecision(ctx context.Context, record DurableRun, eventType string, payload map[string]any) (DurableRun, error) {
	record.State.UpdatedAt = s.runtime.now().UTC()
	dispatchMarkerWritten := false
	if s.dispatcher != nil && record.Freewill != nil && record.State.RequestIdentity != nil && record.Freewill.ExecutionDispatch == nil &&
		validFreewillApprovalReceipt(record.Freewill, FreewillApprovalGranted) {
		// Persist dispatch intent with the grant receipt. Recovery can then
		// distinguish execution-enabled approval from decision-only approval
		// even if the process stops before dispatcher.Start is called.
		childRunID := freewillExecutionChildRunID(record.State.RequestIdentity.RequesterID, record.State.RunID, record.Freewill.ApprovalRef)
		record.Freewill.ExecutionDispatch = &FreewillExecutionDispatch{Status: "dispatching", ChildRunID: childRunID,
			ReportRef: "multiagent_report.json", UpdatedAt: record.State.UpdatedAt}
		dispatchMarkerWritten = true
	}
	if err := record.Validate(s.runtime.supervisor.graph); err != nil {
		return record, err
	}
	payload["run_id"] = record.State.RunID
	for k, v := range freewillEventContext(record) {
		payload[k] = v
	}
	evt := event.NewEvent(eventType, freewillWakeEventSource, payload)
	evt.Timestamp = record.State.UpdatedAt.Format(time.RFC3339Nano)
	evt.CorrelationID = record.Freewill.CorrelationID
	evt.Metadata.RunID = record.State.RunID
	events := []event.Event{evt}
	if dispatchMarkerWritten {
		candidate, _ := selectedFreewillCandidate(record.Freewill)
		dispatchEvent := event.NewEvent(event.EventFreewillExecutionDispatching, freewillWakeEventSource, map[string]any{
			"run_id": record.State.RunID, "request_id": candidate.Request.RequestID, "approval_id": record.Freewill.ApprovalRef,
			"child_run_id": record.Freewill.ExecutionDispatch.ChildRunID, "execution_disabled": true,
		})
		dispatchEvent.Timestamp = record.State.UpdatedAt.Format(time.RFC3339Nano)
		dispatchEvent.CorrelationID = record.Freewill.CorrelationID
		dispatchEvent.Metadata.RunID = record.State.RunID
		events = append(events, dispatchEvent)
	}
	saved, err := s.store.Checkpoint(ctx, record.Revision, record, events)
	if err != nil {
		return record, err
	}
	if err := s.runtime.dispatchOutbox(ctx, record.State.RunID); err != nil {
		return saved, err
	}
	return saved, nil
}

// freewillEventContext enriches freewill lifecycle events at the source so
// downstream consumers (notification bridges, dashboards) never need to
// re-read the durable run store to render human-meaningful cards.
func freewillEventContext(record DurableRun) map[string]any {
	ctx := map[string]any{}
	if record.Freewill == nil {
		return ctx
	}
	fw := record.Freewill
	ctx["lifecycle"] = string(fw.Lifecycle)
	if fw.ChoiceID != "" {
		ctx["choice_id"] = fw.ChoiceID
	}
	if fw.ReasonSummary != "" {
		ctx["reason_summary"] = fw.ReasonSummary
	}
	if fw.ApprovalRef != "" {
		ctx["approval_ref"] = fw.ApprovalRef
	}
	if fw.PolicyRuleID != "" {
		ctx["policy_rule_id"] = fw.PolicyRuleID
	}
	ctx["project_id"] = fw.Scope.ProjectID
	ctx["workspace"] = fw.Scope.Workspace
	if len(fw.Candidates) > 0 {
		cands := make([]map[string]any, 0, len(fw.Candidates))
		for _, c := range fw.Candidates {
			entry := map[string]any{"candidate_id": c.CandidateID, "title": c.Request.Title, "score": c.Score}
			if c.Request.Goal != "" && c.Request.Goal != c.Request.Title {
				entry["goal"] = c.Request.Goal
			}
			if c.Request.ReasonSummary != "" {
				entry["reason"] = c.Request.ReasonSummary
			}
			// decision_only is the default; only surface non-default asks.
			if len(c.Request.RequestedCapabilities) > 0 && !(len(c.Request.RequestedCapabilities) == 1 && c.Request.RequestedCapabilities[0] == "decision_only") {
				entry["capabilities"] = c.Request.RequestedCapabilities
			}
			if c.ScoreRationale != "" {
				entry["rationale"] = c.ScoreRationale
			}
			if c.Request.AgentID != "" {
				ctx["agent_id"] = c.Request.AgentID
			}
			cands = append(cands, entry)
		}
		ctx["candidates"] = cands
		for _, c := range fw.Candidates {
			if c.CandidateID == fw.ChoiceID {
				ctx["selected_title"] = c.Request.Title
				ctx["selected_score"] = c.Score
				if c.Request.Goal != "" {
					ctx["selected_goal"] = c.Request.Goal
				}
				break
			}
		}
	}
	return ctx
}

func freewillApprovalDecision(status string) (FreewillApprovalDecision, bool) {
	switch status {
	case approval.StatusPending:
		return FreewillApprovalPending, true
	case approval.StatusApproved:
		return FreewillApprovalGranted, true
	case approval.StatusDenied:
		return FreewillApprovalDenied, true
	default:
		return "", false
	}
}

// validateFreewillApprovalCandidate re-checks the untrusted selection at the
// policy/approval boundary. Earlier selection validation is not sufficient:
// the durable record may have been edited or recovered independently.
func validateFreewillApprovalCandidate(record DurableRun, candidate freewill.CandidateIntention) error {
	if err := candidate.Validate(); err != nil {
		return ErrFreewillApprovalStale
	}
	if record.Freewill == nil || candidate.Request.AgentID != record.State.RequestIdentity.RequesterID || candidate.Request.Scope != record.Freewill.Scope {
		return ErrFreewillApprovalStale
	}
	return nil
}

func safePolicyRuleID(id string) string {
	if strings.TrimSpace(id) == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n\x00") {
		return "unknown"
	}
	return id
}

func safeDecisionReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "denied by reviewer"
	}
	if len(reason) > 256 {
		reason = reason[:256]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, reason)
}
