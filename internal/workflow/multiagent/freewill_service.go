package multiagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/oklog/ulid/v2"
)

var ErrFreewillDisabled = errors.New("multiagent: Freewill scheduling is disabled")
var ErrFreewillEventNotAllowed = errors.New("multiagent: Freewill event kind or identity is not allowed")
var ErrFreewillWakeScopeMismatch = errors.New("multiagent: Freewill wake identity does not match configured host scope")

// FreewillSelectionInput contains host-verified identity and resumable
// candidates. The provider cannot change the wake scope or agent identity.
type FreewillSelectionInput struct {
	RunID            string
	CorrelationID    string
	AgentID          string
	Scope            freewill.Scope
	Objective        string
	ResumeCandidates []freewill.CandidateIntention
	Budget           freewill.ResourceBudget
	RecoveryFeedback string
	RecoveryCode     string
	ProjectContext   freewill.ProjectContextSnapshot
	PriorReflection  *FreewillReflectionEvidence
}

// FreewillReflectionEvidence is host-retrieved advisory material from an exact
// owner/project/workspace/agent-scoped prior task reflection.
type FreewillReflectionEvidence struct {
	Binding     freewill.ReflectionBinding `json:"binding"`
	OwnerID     string                     `json:"owner_id"`
	ProjectID   string                     `json:"project_id"`
	Workspace   string                     `json:"workspace"`
	AgentID     string                     `json:"agent_id"`
	TaskID      string                     `json:"task_id"`
	Hypothesis  string                     `json:"hypothesis"`
	Uncertainty string                     `json:"uncertainty"`
	Alignment   string                     `json:"alignment"`
}

// FreewillSelectionEvidence is provider metadata, not model output.
type FreewillSelectionEvidence struct {
	Provider                    string
	Model                       string
	Usage                       cost.TokenUsage
	Objective                   string
	ProjectContextManifest      *freewill.ProjectContextManifest
	ProjectContextEgressAllowed bool
	ReflectionBindings          map[string]freewill.ReflectionBinding
	PreferenceInfluence         FreewillPreferenceSelectionMetadata
}

// FreewillPreferenceInfluence is bounded, host-validated but explicitly
// unverified hypothesis material for the optional phase-two choice only.
type FreewillPreferenceInfluence struct {
	HypothesisID       string   `json:"hypothesis_id"`
	HypothesisRevision string   `json:"hypothesis_revision"`
	Hypothesis         string   `json:"hypothesis"`
	Uncertainty        string   `json:"uncertainty"`
	SourcePairRefs     []string `json:"source_pair_refs"`
	Strength           string   `json:"strength"`
	Verification       string   `json:"verification"`
}

// FreewillPreferenceSelectionMetadata persists provenance only, never the
// preference text or challenge content.
type FreewillPreferenceSelectionMetadata struct {
	Considered         bool     `json:"considered"`
	Eligible           bool     `json:"eligible"`
	Reason             string   `json:"reason"`
	HypothesisID       string   `json:"hypothesis_id,omitempty"`
	HypothesisRevision string   `json:"hypothesis_revision,omitempty"`
	SourcePairRefs     []string `json:"source_pair_refs,omitempty"`
}

// FreewillPreferenceInfluenceSource is implemented by the local CLI adapter.
type FreewillPreferenceInfluenceSource interface {
	CurrentPreferenceInfluence(context.Context, string, string, string, string) (FreewillPreferenceInfluence, string, bool)
}

// FreewillProjectContextSource is retained to decode pre-manifest durable runs.
type FreewillProjectContextSource struct {
	ID         string    `json:"id"`
	Path       string    `json:"path,omitempty"`
	Revision   string    `json:"revision"`
	Digest     string    `json:"digest"`
	ObservedAt time.Time `json:"observed_at"`
	Confidence string    `json:"confidence"`
}

type verifiedFreewillResume struct {
	candidate        freewill.CandidateIntention
	predecessorRunID string
}

// FreewillSelectionProvider performs exactly one no-tools selection request.
type FreewillSelectionProvider interface {
	SelectFreewill(context.Context, FreewillSelectionInput) (freewill.Selection, FreewillSelectionEvidence, error)
}

type FreewillWakeServiceConfig struct {
	Enabled                bool
	SchedulingEnabled      bool
	EventKinds             []string
	ExecutionEnabled       bool
	AgentID                string
	Scope                  freewill.Scope
	Objective              string
	StrategyID             string
	StrategyVersion        string
	ExpectedPatchSHA256    string
	Owner                  string
	LeaseFor               time.Duration
	Budget                 freewill.ResourceBudget
	OperatorControl        *FreewillOperatorControl
	ProjectContext         func(context.Context) (freewill.ProjectContextSnapshot, error)
	PriorReflection        func(context.Context) (*FreewillReflectionEvidence, error)
	PriorReflectionOwnerID string
}

// FreewillWakeService composes the existing durable runtime with a configured
// selector. It accepts only configured timer/event triggers and never advances
// an ordinary workflow role.
type FreewillWakeService struct {
	runtime  *DurableRuntime
	store    DurableRunStore
	selector FreewillSelectionProvider
	config   FreewillWakeServiceConfig
	clock    func() time.Time
	control  *FreewillOperatorControl
}

func NewFreewillWakeService(runtime *DurableRuntime, store DurableRunStore, selector FreewillSelectionProvider, cfg FreewillWakeServiceConfig, clock func() time.Time) (*FreewillWakeService, error) {
	if runtime == nil || store == nil || selector == nil {
		return nil, errors.New("multiagent: Freewill runtime, store, and selector are required")
	}
	if strings.TrimSpace(cfg.AgentID) == "" || strings.TrimSpace(cfg.Scope.ProjectID) == "" || strings.TrimSpace(cfg.Scope.Workspace) == "" || strings.TrimSpace(cfg.Owner) == "" || cfg.LeaseFor <= 0 {
		return nil, errors.New("multiagent: Freewill agent, trusted scope, owner, and lease are required")
	}
	if (cfg.StrategyID == "") != (cfg.StrategyVersion == "") || (cfg.StrategyID == "") != (cfg.ExpectedPatchSHA256 == "") || strings.TrimSpace(cfg.StrategyID) != cfg.StrategyID || strings.TrimSpace(cfg.StrategyVersion) != cfg.StrategyVersion || len(cfg.StrategyID) > 96 || len(cfg.StrategyVersion) > 64 || (cfg.ExpectedPatchSHA256 != "" && !validFreewillHash(cfg.ExpectedPatchSHA256)) {
		return nil, errors.New("multiagent: Freewill strategy binding is invalid")
	}
	if err := freewill.ValidateSelectionObjective(cfg.Objective); err != nil {
		return nil, errors.New("multiagent: Freewill selection objective is invalid")
	}
	if len(cfg.EventKinds) > 16 {
		return nil, errors.New("multiagent: Freewill event allowlist exceeds its limit")
	}
	seenEventKinds := make(map[string]struct{}, len(cfg.EventKinds))
	for _, kind := range cfg.EventKinds {
		if !validFreewillEventKind(kind) {
			return nil, ErrFreewillEventNotAllowed
		}
		if _, exists := seenEventKinds[kind]; exists {
			return nil, ErrFreewillEventNotAllowed
		}
		seenEventKinds[kind] = struct{}{}
	}
	cfg.EventKinds = append([]string(nil), cfg.EventKinds...)
	if clock == nil {
		clock = time.Now
	}
	control := cfg.OperatorControl
	if control == nil {
		var err error
		control, err = NewFreewillOperatorControl(store, clock)
		if err != nil {
			return nil, err
		}
	}
	return &FreewillWakeService{runtime: runtime, store: store, selector: selector, config: cfg, clock: clock, control: control}, nil
}

// HandleScheduledTick derives idempotency and scope from host configuration.
// The NATS message supplies only a scheduler timestamp, never authority.
func (s *FreewillWakeService) HandleScheduledTick(ctx context.Context, firedAt time.Time) (DurableRun, error) {
	if !s.config.Enabled || !s.config.SchedulingEnabled {
		return DurableRun{}, ErrFreewillDisabled
	}
	if err := ensureFreewillNotHalted(ctx, s.store, s.config.AgentID, s.config.Scope); err != nil {
		return DurableRun{}, err
	}
	if firedAt.IsZero() {
		return DurableRun{}, errors.New("multiagent: scheduled Freewill tick requires a timestamp")
	}
	minute := firedAt.UTC().Truncate(time.Minute)
	identity := s.config.AgentID + "\x00" + s.config.Scope.ProjectID + "\x00" + s.config.Scope.Workspace + "\x00scheduled_tick\x00" + minute.Format(time.RFC3339)
	digest := sha256.Sum256([]byte(identity))
	runID := "freewill-" + hex.EncodeToString(digest[:16])
	wake := FreewillWakeRequest{
		RunID: runID, AgentID: s.config.AgentID, Scope: s.config.Scope,
		Trigger: "scheduled_tick", DedupeKey: "freewill:" + hex.EncodeToString(digest[:]),
		CorrelationID: runID, StrategyID: s.config.StrategyID, StrategyVersion: s.config.StrategyVersion, ExpectedPatchSHA256: s.config.ExpectedPatchSHA256,
	}
	accepted, created, err := s.runtime.AcceptFreewillWake(ctx, wake)
	if err != nil {
		return DurableRun{}, err
	}
	if !created {
		return accepted, nil
	}
	return s.ProcessWake(ctx, accepted.State.RunID)
}

// HasWakeSources reports whether the enabled service has at least one
// configured source. It is used by the serve composition root to avoid
// installing subscriptions when every trigger is disabled.
func (s *FreewillWakeService) HasWakeSources() bool {
	return s != nil && s.config.Enabled && (s.config.SchedulingEnabled || s.eventWakeEnabled())
}

// HandleEvent accepts only exact event kinds configured by the host. The
// canonical event ID and type form a durable idempotency key; message payload
// content never becomes selector instructions or authority.
func (s *FreewillWakeService) HandleEvent(ctx context.Context, incoming event.Event) (DurableRun, error) {
	if !s.config.Enabled || !s.eventWakeEnabled() {
		return DurableRun{}, ErrFreewillDisabled
	}
	if err := ensureFreewillNotHalted(ctx, s.store, s.config.AgentID, s.config.Scope); err != nil {
		return DurableRun{}, err
	}
	if !validFreewillEventKind(incoming.Type) || !validFreewillEventID(incoming.ID) || !s.allowsEventKind(incoming.Type) {
		return DurableRun{}, ErrFreewillEventNotAllowed
	}
	identity := s.config.AgentID + "\x00" + s.config.Scope.ProjectID + "\x00" + s.config.Scope.Workspace + "\x00event\x00" + incoming.Type + "\x00" + incoming.ID
	digest := sha256.Sum256([]byte(identity))
	runID := "freewill-" + hex.EncodeToString(digest[:16])
	wake := FreewillWakeRequest{
		RunID: runID, AgentID: s.config.AgentID, Scope: s.config.Scope,
		Trigger: "event:" + incoming.Type, DedupeKey: "freewill:" + hex.EncodeToString(digest[:]),
		CorrelationID: runID, StrategyID: s.config.StrategyID, StrategyVersion: s.config.StrategyVersion,
		ExpectedPatchSHA256: s.config.ExpectedPatchSHA256,
	}
	accepted, created, err := s.runtime.AcceptFreewillWake(ctx, wake)
	if err != nil {
		return DurableRun{}, err
	}
	if !created {
		return accepted, nil
	}
	return s.ProcessWake(ctx, accepted.State.RunID)
}

func (s *FreewillWakeService) eventWakeEnabled() bool {
	return len(s.config.EventKinds) > 0
}

func (s *FreewillWakeService) allowsEventKind(kind string) bool {
	for _, configured := range s.config.EventKinds {
		if configured == kind {
			return true
		}
	}
	return false
}

func validFreewillEventKind(kind string) bool {
	if len(kind) < len("prizm.a") || len(kind) > 192 || !strings.HasPrefix(kind, "prizm.") || strings.TrimSpace(kind) != kind {
		return false
	}
	for _, part := range strings.Split(kind, ".") {
		if part == "" {
			return false
		}
		for _, char := range part {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
				return false
			}
		}
	}
	return true
}

func validFreewillEventID(id string) bool {
	if !strings.HasPrefix(id, "evt_") || len(id) != len("evt_")+ulid.EncodedSize {
		return false
	}
	_, err := ulid.ParseStrict(strings.TrimPrefix(id, "evt_"))
	return err == nil
}

// ProcessWake resumes only a Freewill record that is waiting in the wake
// checkpoint. A crash after acceptance or selection start is recoverable by
// the same scheduler scan after the fenced lease expires.
func (s *FreewillWakeService) ProcessWake(ctx context.Context, runID string) (DurableRun, error) {
	if !s.HasWakeSources() {
		return DurableRun{}, ErrFreewillDisabled
	}
	// Bind the durable wake to the exact host configuration before claiming it.
	// This avoids changing lease/revision state for a run from another scope.
	preflight, err := s.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	if err := s.validateConfiguredWake(preflight); err != nil {
		return preflight, err
	}
	claimed, lease, err := s.runtime.ClaimFreewillWake(ctx, runID, s.config.Owner, s.config.LeaseFor)
	if err != nil {
		if errors.Is(err, ErrFreewillWakeAdmissionReconciled) {
			return claimed, err
		}
		return DurableRun{}, err
	}
	selectionCtx, stopRenewal, currentLease := s.maintainLease(ctx, lease)
	providerCtx, cancelProvider := context.WithCancel(selectionCtx)
	unregisterSelection, err := s.control.RegisterActiveSelection(s.config.AgentID, s.config.Scope, cancelProvider)
	if err != nil {
		cancelProvider()
		stopRenewal()
		_, _ = s.runtime.ReleaseFreewillWake(context.Background(), lease)
		return DurableRun{}, err
	}
	committed := false
	defer func() {
		unregisterSelection()
		cancelProvider()
		stopRenewal()
		if !committed {
			_, _ = s.runtime.ReleaseFreewillWake(context.Background(), lease)
		}
	}()
	record, err := s.store.Load(ctx, runID)
	if err != nil {
		return DurableRun{}, err
	}
	if err := s.validateConfiguredWake(record); err != nil {
		return record, err
	}
	if record.Freewill.Lifecycle == freewill.StateWaking {
		record, err = s.runtime.BeginFreewillSelection(selectionCtx, lease)
		if err != nil {
			return record, err
		}
	}
	if record.Freewill.Lifecycle != freewill.StateSelecting {
		return record, errors.New("multiagent: Freewill wake is not awaiting selection")
	}
	if err := ensureFreewillNotHalted(selectionCtx, s.store, s.config.AgentID, s.config.Scope); err != nil {
		return record, err
	}
	verifiedResumes := s.verifiedResumeCandidates(ctx, record)
	resumes := make([]freewill.CandidateIntention, 0, len(verifiedResumes))
	verified := make(map[string]struct{}, len(verifiedResumes))
	resumePredecessors := make(map[string]string, len(verifiedResumes))
	for _, resume := range verifiedResumes {
		resumes = append(resumes, resume.candidate)
		verified[resume.candidate.CandidateID] = struct{}{}
		resumePredecessors[resume.candidate.CandidateID] = resume.predecessorRunID
	}
	if err := ensureFreewillNotHalted(providerCtx, s.store, s.config.AgentID, s.config.Scope); err != nil {
		return record, err
	}
	var projectContext freewill.ProjectContextSnapshot
	var priorReflection *FreewillReflectionEvidence
	reflectionBindings := make(map[string]freewill.ReflectionBinding)
	if s.config.PriorReflection != nil {
		priorReflection, err = s.config.PriorReflection(providerCtx)
		if err != nil {
			return record, errors.New("multiagent: prior Freewill reflection is unavailable")
		}
		if priorReflection != nil {
			if err := validateFreewillReflectionEvidence(*priorReflection, s.config.Scope, s.config.AgentID, s.config.PriorReflectionOwnerID); err != nil {
				return record, errors.New("multiagent: prior Freewill reflection binding is invalid")
			}
			reflectionBindings[priorReflection.Binding.ID] = priorReflection.Binding
		}
	}
	if s.config.ProjectContext != nil {
		projectContext, err = s.config.ProjectContext(providerCtx)
		if err != nil {
			return record, fmt.Errorf("multiagent: Freewill project context is unavailable")
		}
		if projectContext.ProjectID != s.config.Scope.ProjectID || projectContext.Digest == "" || len(projectContext.Sources) > 16 {
			return record, errors.New("multiagent: Freewill project context binding is invalid")
		}
		if err := validateFreewillContextInput(FreewillSelectionInput{Scope: s.config.Scope, ProjectContext: projectContext}); err != nil {
			return record, errors.New("multiagent: Freewill project context snapshot is invalid")
		}
		projectSources := hasFreewillSelectableContextSources(projectContext)
		if !projectSources || !projectContext.SelectorEgressAllowed {
			reason := "Project context is unavailable; defer selection until authorized project evidence exists."
			if projectSources && !projectContext.SelectorEgressAllowed {
				reason = "Project context sharing with this remote selector is not authorized; defer selection."
			}
			selection := freewill.Selection{SleepReason: reason}
			manifest := projectContext.Manifest()
			evidence := FreewillSelectionEvidence{ProjectContextManifest: &manifest, ProjectContextEgressAllowed: projectContext.SelectorEgressAllowed}
			lease = currentLease()
			record, err = s.runtime.CommitFreewillSelectionWithEvidence(ctx, lease, selection, verified, "", evidence)
			if err != nil {
				return record, err
			}
			committed = true
			return record, nil
		}
	}
	selection, evidence, err := s.selector.SelectFreewill(providerCtx, FreewillSelectionInput{
		RunID: runID, CorrelationID: record.Freewill.CorrelationID, AgentID: s.config.AgentID,
		Scope: s.config.Scope, Objective: s.config.Objective, ResumeCandidates: resumes, Budget: s.config.Budget,
		RecoveryFeedback: freewillSelectionRecoveryFeedback(record.Freewill.SelectionFailureCode, projectContext),
		RecoveryCode:     record.Freewill.SelectionFailureCode,
		ProjectContext:   cloneFreewillProjectContextSnapshot(projectContext),
		PriorReflection:  cloneFreewillReflectionEvidence(priorReflection),
	})
	evidence.Objective = s.config.Objective
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
			return record, err
		}
		failed, failureErr := s.runtime.RecordFreewillSelectionFailure(ctx, lease, err)
		if failureErr != nil {
			return failed, failureErr
		}
		return failed, err
	}
	if s.config.ProjectContext != nil {
		manifest := projectContext.Manifest()
		evidence.ProjectContextManifest = &manifest
		evidence.ProjectContextEgressAllowed = projectContext.SelectorEgressAllowed
	}
	for index := range selection.Candidates {
		selected := selection.Candidates[index]
		if selected.Request.CandidateSource != freewill.SourceResume {
			if selected.Request.ReflectionID != "" {
				binding, ok := reflectionBindings[selected.Request.ReflectionID]
				if !ok || priorReflection == nil || selected.Request.CandidateSource != freewill.SourceNew && selected.Request.CandidateSource != freewill.SourceReflection {
					return s.recordSelectionFailure(ctx, lease, errors.New("multiagent: Freewill selector cited an unavailable reflection"))
				}
				selected.Request.CandidateSource = freewill.SourceReflection
				selected.Request.ReflectionID = binding.ID
				selected.Request.ReflectionRevision = binding.Revision
				selected.Request.ReflectionSource = binding.Source
				selection.Candidates[index] = selected
			} else if selected.Request.CandidateSource == freewill.SourceReflection {
				return s.recordSelectionFailure(ctx, lease, errors.New("multiagent: Freewill reflection candidate omitted its host citation"))
			}
			continue
		}
		for _, resume := range verifiedResumes {
			if selected.CandidateID == resume.candidate.CandidateID {
				// The selector chooses a host-supplied resume by ID. Restore the
				// canonical request from durable storage; model output cannot alter
				// its identity or predecessor lineage.
				selection.Candidates[index] = resume.candidate
				break
			}
		}
	}
	reflectionCandidateCount := 0
	for _, candidate := range selection.Candidates {
		if candidate.Request.CandidateSource == freewill.SourceReflection {
			reflectionCandidateCount++
		}
	}
	if reflectionCandidateCount > 1 {
		return s.recordSelectionFailure(ctx, lease, errors.New("multiagent: Freewill selector supplied multiple reflection candidates"))
	}
	evidence.ReflectionBindings = reflectionBindings
	selection, err = normalizeFreewillRoadmapSelection(selection, projectContext)
	if err != nil {
		failed, failureErr := s.runtime.RecordFreewillSelectionFailure(ctx, lease, err)
		if failureErr != nil {
			return failed, failureErr
		}
		return failed, err
	}
	selection = bindFreewillExecutionCapabilities(selection, s.config.ExecutionEnabled)
	if err := ensureFreewillNotHalted(ctx, s.store, s.config.AgentID, s.config.Scope); err != nil {
		return record, err
	}
	if err := providerCtx.Err(); err != nil {
		return record, err
	}
	stopRenewal()
	lease = currentLease()
	resumeFromRunID := ""
	if predecessorRunID := resumePredecessors[selection.ChoiceID]; predecessorRunID != "" {
		resumeFromRunID = predecessorRunID
	}
	record, err = s.runtime.CommitFreewillSelectionWithEvidence(ctx, lease, selection, verified, resumeFromRunID, evidence)
	if err != nil {
		return record, err
	}
	committed = true
	return record, nil
}

func cloneFreewillReflectionEvidence(input *FreewillReflectionEvidence) *FreewillReflectionEvidence {
	if input == nil {
		return nil
	}
	copy := *input
	return &copy
}

func (s *FreewillWakeService) recordSelectionFailure(ctx context.Context, lease FreewillWakeLease, cause error) (DurableRun, error) {
	failed, failureErr := s.runtime.RecordFreewillSelectionFailure(ctx, lease, cause)
	if failureErr != nil {
		return failed, failureErr
	}
	return failed, cause
}

func validateFreewillReflectionEvidence(input FreewillReflectionEvidence, scope freewill.Scope, agentID string, ownerID ...string) error {
	if input.Binding.ID == "" || len(input.Binding.ID) > 128 || input.Binding.Source != "prizm:freewill-task-reflection" || len(input.Binding.Revision) != 64 || input.OwnerID == "" || input.ProjectID != scope.ProjectID || input.Workspace != scope.Workspace || input.AgentID != agentID || input.TaskID == "" || len(input.TaskID) > 256 || input.Hypothesis == "" || len(input.Hypothesis) > 700 || input.Uncertainty == "" || len(input.Uncertainty) > 300 || (input.Alignment != "agreement" && input.Alignment != "disagreement" && input.Alignment != "unknown") || scope.ProjectID == "" || scope.Workspace == "" {
		return errors.New("invalid bounded reflection evidence")
	}
	if len(ownerID) > 0 && input.OwnerID != ownerID[0] {
		return errors.New("reflection owner binding mismatch")
	}
	for _, char := range input.Binding.Revision {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return errors.New("invalid reflection revision")
		}
	}
	return nil
}

func cloneFreewillProjectContextSnapshot(snapshot freewill.ProjectContextSnapshot) freewill.ProjectContextSnapshot {
	return snapshot.Clone()
}

// normalizeFreewillRoadmapSelection rebinds every fresh candidate at the host
// service boundary. Selector implementations are untrusted, so action IDs and
// grounding markers are rechecked against the original sealed snapshot before
// the selection can be committed.
func normalizeFreewillRoadmapSelection(selection freewill.Selection, snapshot freewill.ProjectContextSnapshot) (freewill.Selection, error) {
	if len(snapshot.RoadmapActions) == 0 {
		for index := range selection.Candidates {
			if selection.Candidates[index].ProjectActionID != "" {
				return freewill.Selection{}, errors.New("multiagent: Freewill selector supplied an unavailable roadmap action")
			}
			selection.Candidates[index].ProjectGrounding = ""
		}
		return selection, nil
	}
	for index := range selection.Candidates {
		candidate := &selection.Candidates[index]
		if candidate.Request.CandidateSource == freewill.SourceResume {
			continue
		}
		if err := bindFreewillProjectAction(snapshot, candidate); err != nil {
			return freewill.Selection{}, errors.New("multiagent: Freewill selector supplied an invalid roadmap action")
		}
	}
	return enforceFreewillRoadmapChoice(selection, snapshot), nil
}

func bindFreewillExecutionCapabilities(selection freewill.Selection, executionEnabled bool) freewill.Selection {
	if !executionEnabled {
		return selection
	}
	for index := range selection.Candidates {
		// This is host configuration, not selector authority. The selector has
		// no tools; the owner still approves each exact proposal before dispatch.
		selection.Candidates[index].Request.RequestedCapabilities = []string{"code"}
	}
	return selection
}

func (s *FreewillWakeService) validateConfiguredWake(record DurableRun) error {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.State.RequestIdentity == nil ||
		record.State.RequestIdentity.RequesterID != s.config.AgentID || record.Freewill.Scope != s.config.Scope {
		return ErrFreewillWakeScopeMismatch
	}
	return nil
}

func FreewillSelectionRecoveryFeedback(code string, snapshot freewill.ProjectContextSnapshot) string {
	return freewillSelectionRecoveryFeedback(code, snapshot)
}

func freewillSelectionRecoveryFeedback(code string, snapshots ...freewill.ProjectContextSnapshot) string {
	switch code {
	case "invalid_scored_ideas":
		return "Repeat this block exactly three times:\nIDEA: Map project dependencies\nSCORE: 72\nFACTORS: scope_fit: 9/10\nRATIONALE: This maps direct dependencies for a bounded review.\nFACTORS optional; keep rationale specific and add no preamble, footer, paths, authority, or execution requests."
	case "invalid_scored_ideas_count":
		return "Return exactly three scored blocks. Each block needs IDEA, SCORE, optional FACTORS, and specific RATIONALE on separate labeled lines. Add no other candidates or prose."
	case "invalid_scored_ideas_size":
		return "Keep the entire response under 4096 bytes while returning exactly three scored blocks with IDEA, SCORE, optional FACTORS, and specific RATIONALE. Remove preamble and extra prose."
	case "invalid_scored_ideas_idea_shape":
		return "Keep each IDEA concise (160 characters maximum) and plain text. Do not use paths, URLs, brackets, backslashes, or traversal markers; return exactly three scored blocks."
	case "invalid_scored_ideas_duplicate":
		return "Make the IDEA text distinct in all three scored blocks. Keep each concise and preserve the required labels and specific rationale."
	case "invalid_scored_ideas_structure":
		return "Use uppercase labels in this order in each of exactly three blocks: IDEA, SCORE, optional FACTORS, then RATIONALE. Put each field on its own line and add no preamble, footer, or extra fields."
	case "invalid_scored_ideas_score":
		return "Each of exactly three blocks needs SCORE as one integer from 0 to 100 on its own line. Keep IDEA, optional FACTORS, and specific RATIONALE in the required labeled order; no extra fields or prose."
	case "invalid_scored_ideas_factors":
		return "Return one JSON object matching the supplied schema. Each of exactly three candidates needs a nonempty factors array using only scope_fit, feasibility, impact, novelty, or readiness. Do not add ratings or annotations."
	case "invalid_scored_ideas_rationale":
		return "Give each of exactly three blocks a specific RATIONALE of at least six words, after IDEA, SCORE, and optional FACTORS. Keep it decision-only and avoid extra fields or prose."
	case "invalid_scored_ideas_unsafe":
		return "Return exactly three bounded decision-only ideas with IDEA, SCORE, optional FACTORS, and specific RATIONALE. Do not include paths, authority, approval, tools, network, secrets, or execution requests."
	case "invalid_scored_ideas_json_size_or_encoding", "invalid_scored_ideas_json_duplicate_key", "invalid_scored_ideas_json_trailing_content", "invalid_scored_ideas_json_syntax", "invalid_scored_ideas_json_wrong_shape":
		return "Return exactly three IDEA/SCORE/FACTORS/RATIONALE text blocks in the requested order. No JSON, code fence, or extra prose."
	case "invalid_scored_ideas_json_unknown_field":
		return "Return JSON matching the supplied schema: exactly three candidates or an empty list with sleep=true. Use only idea, score, factors, rationale, basis_refs, uncertainty, and project_action_id. No aliases or extra fields."
	case "invalid_idea_lines":
		return "Return only three lines, each beginning exactly IDEA: followed by a short decision-only objective. No preamble, bullets, explanation, JSON, paths, authority, tools, network, secrets, approval, or execution requests."
	case "invalid_selection_schema", "empty_selection_response", "selection_tool_request", "invalid_selection_json", "unknown_selection_field", "selection_candidate_count", "selection_contract_invalid":
		return "Every response, even sleep/resume, must include exactly 3 fresh candidates. Each has title, goal, reason_summary, requested_capabilities, score 0-100, score_factors, score_rationale; budget optional. Factors: scope_fit, feasibility, impact, novelty, readiness. New: include reason_summary; host selects top (first tie). Resume: include all 3, set supplied resume_candidate_id and reason_summary. Sleep: include all 3 and sleep_reason only. Never omit candidates. Host binds IDs/scope. No tools/extra fields."
	case "invalid_project_basis":
		if sourceID := freewillSelectionCorrectionSourceID(snapshots); sourceID != "" {
			return "For each of exactly three ideas, cite only supplied source ID " + sourceID + " in BASIS_REFS and add concise UNCERTAINTY; do not invent refs. Return NONE if unsupported."
		}
		return "For each of exactly three ideas, cite an exact supplied project source ID in BASIS_REFS and add concise UNCERTAINTY; do not invent refs. Return NONE if unsupported."
	case "provider_transient":
		return "The previous provider attempt failed transiently. Retry the same selection request and return the required schema."
	default:
		return ""
	}
}

func freewillFlashJSONSelectionRecoveryFeedback(code string, snapshot freewill.ProjectContextSnapshot) string {
	switch code {
	case "invalid_scored_ideas_json_size_or_encoding", "invalid_scored_ideas_json_duplicate_key", "invalid_scored_ideas_json_trailing_content", "invalid_scored_ideas_json_syntax", "invalid_scored_ideas_json_wrong_shape":
		return "Return one JSON object matching the supplied schema with exactly three candidates, or an empty candidates array and sleep=true. Each candidate needs idea, integer score from 0 to 100, nonempty factors array, rationale, basis_refs, uncertainty, and optional project_action_id. No aliases, prose, fences, or extra keys."
	case "invalid_scored_ideas_count":
		return "Return one JSON object matching the supplied schema with exactly three candidates, or an empty candidates array and sleep=true. Do not return one or two candidates."
	case "invalid_scored_ideas_size":
		return "Return one compact JSON object matching the supplied schema with exactly three candidates, or an empty candidates array and sleep=true. Keep each field concise and omit extra keys or prose."
	case "invalid_scored_ideas_idea_shape":
		return "Return JSON matching the supplied schema. Keep each candidate idea concise (160 characters maximum) and plain text, without paths, URLs, brackets, backslashes, or traversal markers."
	case "invalid_scored_ideas_duplicate":
		return "Return JSON matching the supplied schema with exactly three candidates whose idea strings are distinct. Preserve all required fields."
	case "invalid_scored_ideas_structure":
		return "Return one JSON object matching the supplied schema. Include the required candidates array and sleep boolean; each candidate must use the declared field names and JSON value types. Do not add prose or extra keys."
	case "invalid_scored_ideas_unsafe":
		return "Return JSON matching the supplied schema with three bounded decision-only ideas or the empty-candidate sleep form. Do not include paths, authority, approval, tools, network, secrets, or execution requests."
	case "idea_tool_request":
		return "Return only JSON matching the supplied schema. This selector has no tools; provide three bounded decision-only candidates or the empty-candidate sleep form."
	case "invalid_scored_ideas_json_unknown_field":
		return "Return JSON matching the supplied schema: exactly three candidates or an empty list with sleep=true. Use only idea, score, factors, rationale, basis_refs, uncertainty, and project_action_id. No aliases or extra fields."
	case "invalid_scored_ideas_factors":
		return "Return one JSON object matching the supplied schema. Each of exactly three candidates needs a nonempty factors array using only scope_fit, feasibility, impact, novelty, or readiness. Do not add ratings or annotations."
	case "invalid_scored_ideas_score":
		return "Return JSON matching the supplied schema. Each of exactly three candidates needs score as an integer from 0 to 100, plus the required idea, factors, rationale, basis_refs, and uncertainty fields."
	case "invalid_scored_ideas_rationale":
		return "Return JSON matching the supplied schema. Each of exactly three candidates needs a specific rationale of at least six words; preserve the other required fields."
	case "invalid_project_basis":
		if sourceID := freewillSelectionCorrectionSourceID([]freewill.ProjectContextSnapshot{snapshot}); sourceID != "" {
			return "Return JSON matching the supplied schema. Each candidate's basis_refs may contain only supplied source ID " + sourceID + "; include uncertainty and do not invent refs. Return the schema's sleep/no-op form if unsupported."
		}
		return "Return JSON matching the supplied schema. Cite exact supplied project source IDs in basis_refs, include uncertainty, and do not invent refs. Return the schema's sleep/no-op form if unsupported."
	case "invalid_project_action":
		return "Return JSON matching the supplied schema. Set project_action_id only to an exact host-supplied action ID supported by the candidate's basis_refs; omit it when no supplied action applies. Do not invent action IDs."
	case "invalid_choice":
		return "Retry the idea-selection stage: return JSON matching the supplied idea schema with exactly three candidates, or an empty candidates array and sleep=true. Preserve the required idea fields; do not return a choice object."
	default:
		if strings.HasPrefix(code, "invalid_scored_ideas_") {
			return "Return one JSON object matching the supplied schema with exactly three candidates, or an empty candidates array and sleep=true. Use the declared field names and types only; no text blocks, aliases, or extra prose."
		}
		return freewillSelectionRecoveryFeedback(code, snapshot)
	}
}

func freewillFlashSelectionRecoveryFeedback(code string, snapshot freewill.ProjectContextSnapshot) string {
	lineContract := "Return exactly three blocks, each with IDEA, integer SCORE (0-100), nonempty FACTORS from scope_fit, feasibility, impact, novelty, readiness, optional exact ACTION_ID, exact supplied BASIS_REFS, concise UNCERTAINTY, and specific RATIONALE; one field per line. No JSON, fences, preamble, extra fields, or tools. Return exactly NONE if no safe supported idea fits."
	switch code {
	case "invalid_scored_ideas_count":
		return lineContract + " Do not return one or two candidates."
	case "invalid_scored_ideas_size":
		return lineContract + " Keep each field concise and within the host's size limits."
	case "invalid_scored_ideas_idea_shape":
		return lineContract + " Keep each IDEA concise (160 characters maximum) and plain text, without paths, URLs, brackets, backslashes, or traversal markers."
	case "invalid_scored_ideas_duplicate":
		return lineContract + " Ensure all three IDEA values are distinct."
	case "invalid_scored_ideas_structure":
		return lineContract
	case "invalid_scored_ideas_unsafe":
		return lineContract + " Keep the ideas decision-only; do not include paths, authority, approval, network, secrets, or execution requests."
	case "idea_tool_request":
		return lineContract + " This selector has no tools."
	case "invalid_scored_ideas_factors":
		return lineContract + " FACTORS must contain one or more unique bare labels, with no ratings or annotations."
	case "invalid_scored_ideas_score":
		return lineContract + " Each SCORE must be an integer from 0 to 100."
	case "invalid_scored_ideas_rationale":
		return lineContract + " Each RATIONALE must be specific and contain at least six words."
	case "invalid_project_basis":
		if sourceID := freewillSelectionCorrectionSourceID([]freewill.ProjectContextSnapshot{snapshot}); sourceID != "" {
			return lineContract + " Each BASIS_REFS field may contain only supplied source ID " + sourceID + "; do not invent refs."
		}
		return lineContract + " Cite exact supplied project source IDs in BASIS_REFS; do not invent refs."
	case "invalid_project_action":
		return lineContract + " Use ACTION_ID only for an exact supplied action supported by BASIS_REFS; do not invent action IDs."
	case "invalid_choice":
		return lineContract + " Retry the idea-selection stage; do not return a choice object."
	default:
		if strings.HasPrefix(code, "invalid_scored_ideas_") {
			return lineContract
		}
		return freewillSelectionRecoveryFeedback(code, snapshot)
	}
}

func freewillSelectionCorrectionSourceID(snapshots []freewill.ProjectContextSnapshot) string {
	if len(snapshots) == 0 {
		return ""
	}
	ids := make([]string, 0, len(snapshots[0].Sources))
	for _, source := range snapshots[0].Sources {
		if source.Path == "" && !freewill.IsOwnerAuthorizedMemorySource(source) {
			continue
		}
		if source.ID == "" || len(source.ID) > 128 {
			continue
		}
		safe := true
		for _, r := range source.ID {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ':' || r == '_' || r == '-' || r == '.') {
				safe = false
				break
			}
		}
		if safe {
			ids = append(ids, source.ID)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// maintainLease keeps a live selector call fenced without imposing a task
// deadline. Cancellation and the final renewal are joined before the caller
// can commit or release the wake.
func (s *FreewillWakeService) maintainLease(parent context.Context, initial FreewillWakeLease) (context.Context, func(), func() FreewillWakeLease) {
	ctx, cancel := context.WithCancel(parent)
	interval := s.config.LeaseFor / 3
	if interval < time.Second {
		interval = time.Second
	}
	var mu sync.Mutex
	lease := initial
	var once sync.Once
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				current := lease
				mu.Unlock()
				renewed, err := s.runtime.RenewFreewillWake(ctx, current, s.config.LeaseFor)
				if err != nil {
					cancel()
					return
				}
				mu.Lock()
				lease = renewed
				mu.Unlock()
			}
		}
	}()
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	current := func() FreewillWakeLease {
		mu.Lock()
		defer mu.Unlock()
		return lease
	}
	return ctx, stop, current
}

func (s *FreewillWakeService) verifiedResumeCandidates(ctx context.Context, current DurableRun) []verifiedFreewillResume {
	control, ok := s.store.(FreewillOperatorControlStore)
	if !ok {
		return nil
	}
	controlState, err := control.LoadFreewillOperatorControl(ctx, s.config.AgentID, s.config.Scope)
	if err != nil || controlState.Halted {
		return nil
	}
	lister, ok := s.store.(FreewillRunLister)
	if !ok {
		return nil
	}
	var records []DurableRun
	after := ""
	for {
		page, err := lister.ListFreewillRuns(ctx, after, 100)
		if err != nil || len(page) == 0 {
			break
		}
		records = append(records, page...)
		after = page[len(page)-1].State.RunID
		if len(page) < 100 {
			break
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].State.UpdatedAt.After(records[j].State.UpdatedAt) })
	for _, record := range records {
		if record.State.RunID == current.State.RunID || record.Kind != DurableRunKindFreewill || record.Freewill == nil ||
			record.Freewill.Scope != s.config.Scope || record.State.RequestIdentity == nil ||
			record.State.RequestIdentity.RequesterID != s.config.AgentID || record.Freewill.Lifecycle != freewill.StatePaused ||
			record.Freewill.ApprovalGeneration != controlState.Generation || record.Freewill.ApprovalReceipt == nil ||
			record.Freewill.ApprovalReceipt.ControlGeneration != controlState.Generation ||
			!record.Freewill.ExecutionDisabled || record.Freewill.PauseReason != "execution_disabled" ||
			!validFreewillApprovalReceipt(record.Freewill, FreewillApprovalGranted) {
			continue
		}
		candidate, ok := selectedFreewillCandidate(record.Freewill)
		if !ok || (candidate.Request.CandidateSource != freewill.SourceNew && candidate.Request.CandidateSource != freewill.SourceResume) {
			continue
		}
		candidate.Request.CandidateSource = freewill.SourceResume
		return []verifiedFreewillResume{{candidate: candidate, predecessorRunID: record.State.RunID}}
	}
	return nil
}

func (s *FreewillWakeService) RecoverPending(ctx context.Context, now time.Time, pageSize int) error {
	if !s.HasWakeSources() {
		return ErrFreewillDisabled
	}
	lister, ok := s.store.(FreewillRunLister)
	if !ok {
		return errors.New("multiagent: durable store does not support Freewill recovery listing")
	}
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 100
	}
	after := ""
	for {
		page, err := lister.ListFreewillRuns(ctx, after, pageSize)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, run := range page {
			if run.Freewill == nil || (run.Freewill.Lifecycle != freewill.StateWaking && run.Freewill.Lifecycle != freewill.StateSelecting) ||
				(run.Freewill.LeaseOwner != "" && run.Freewill.LeaseUntil.After(now)) || run.Freewill.SelectionNextAt.After(now) {
				continue
			}
			processed, processErr := s.ProcessWake(ctx, run.State.RunID)
			if errors.Is(processErr, ErrFreewillWakeAdmissionReconciled) {
				// Acceptance wrote the actor slot atomically with the run. A wake
				// without its own slot is legacy orphan history, not queued work.
				continue
			}
			if processErr != nil && !errors.Is(processErr, ErrFreewillWakeLeaseHeld) && !errors.Is(processErr, ErrFreewillWakeNotDue) && !errors.Is(processErr, ErrFreewillHalted) {
				// A failed provider call is already durably classified and either
				// scheduled or halted. Continue scanning other accepted wakes.
				if processed.Freewill != nil && (processed.Freewill.SelectionNextAt.After(now) || processed.Freewill.Lifecycle == freewill.StateHalted) {
					continue
				}
				return fmt.Errorf("recover Freewill wake: %w", processErr)
			}
		}
		after = page[len(page)-1].State.RunID
		if len(page) < pageSize {
			return nil
		}
	}
}
