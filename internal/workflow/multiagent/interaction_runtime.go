package multiagent

// interaction_runtime.go contains the domain-neutral adapter execution loop.
// It deliberately lives beside the graph runtime: the graph chooses durable
// workflow phases while this loop owns the adapter boundary and its safety
// invariants.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	prizmAdapter "github.com/emaharmony/prizm/internal/adapter"
	"github.com/emaharmony/prizm/internal/event"
)

// InteractionLane is the cascade level used for one adapter decision.
type InteractionLane string

const (
	LaneStrategic InteractionLane = "strategic"
	LaneTactical  InteractionLane = "tactical"
	LaneReflex    InteractionLane = "reflex"
)

// InteractionPhase is a bounded lane in an interaction plan.
type InteractionPhase struct {
	Lane         InteractionLane
	Cadence      time.Duration
	Interrupts   []string
	MaxActions   int
	Verification []string
}

// InteractionPlan contains deterministic safety limits for one run.
type InteractionPlan struct {
	Phases      []InteractionPhase
	MaxActions  int
	MaxRetries  int
	Deadman     time.Duration
	RunDeadline time.Duration
}

func (p InteractionPlan) normalized() InteractionPlan {
	if len(p.Phases) == 0 {
		p.Phases = []InteractionPhase{{Lane: LaneStrategic, MaxActions: 1}, {Lane: LaneTactical, MaxActions: 4}, {Lane: LaneReflex, MaxActions: 16}}
	}
	if p.MaxActions <= 0 {
		p.MaxActions = 32
	}
	if p.MaxRetries < 0 {
		p.MaxRetries = 0
	}
	if p.Deadman <= 0 {
		p.Deadman = 30 * time.Second
	}
	return p
}

// InteractionRunRequest starts or resumes an adapter run.
type InteractionRunRequest struct {
	RunID       string
	Goal        string
	Observation prizmAdapter.ObservationRequest
	Plan        InteractionPlan
}

// InteractionDecisionSelector chooses one action from the adapter's legal set.
type InteractionDecisionSelector interface {
	Select(context.Context, prizmAdapter.Observation, []prizmAdapter.Capability, InteractionPhase) (prizmAdapter.Action, error)
}

// InteractionDecisionSelectorFunc adapts a function to the selector seam.
type InteractionDecisionSelectorFunc func(context.Context, prizmAdapter.Observation, []prizmAdapter.Capability, InteractionPhase) (prizmAdapter.Action, error)

func (f InteractionDecisionSelectorFunc) Select(ctx context.Context, obs prizmAdapter.Observation, legal []prizmAdapter.Capability, phase InteractionPhase) (prizmAdapter.Action, error) {
	return f(ctx, obs, legal, phase)
}

// InteractionAuthorization is the policy result for one proposed action.
type InteractionAuthorization string

const (
	InteractionAllowed         InteractionAuthorization = "allowed"
	InteractionApprovalPending InteractionAuthorization = "approval_pending"
	InteractionDenied          InteractionAuthorization = "denied"
)

// InteractionPolicy is the existing policy/approval authority adapted to the
// interaction loop. It does not execute actions.
type InteractionPolicy interface {
	Authorize(context.Context, string, prizmAdapter.Action, prizmAdapter.Observation) (InteractionAuthorization, string, error)
}

// InteractionPolicyFunc adapts a function to InteractionPolicy.
type InteractionPolicyFunc func(context.Context, string, prizmAdapter.Action, prizmAdapter.Observation) (InteractionAuthorization, string, error)

func (f InteractionPolicyFunc) Authorize(ctx context.Context, runID string, action prizmAdapter.Action, obs prizmAdapter.Observation) (InteractionAuthorization, string, error) {
	return f(ctx, runID, action, obs)
}

// AllowInteractionPolicy is the default policy for deterministic simulators.
// Production composition should provide the existing policy and approval path.
type AllowInteractionPolicy struct{}

func (AllowInteractionPolicy) Authorize(context.Context, string, prizmAdapter.Action, prizmAdapter.Observation) (InteractionAuthorization, string, error) {
	return InteractionAllowed, "default simulator policy", nil
}

// InteractionVerifier checks the environment after an action.
type InteractionVerifier interface {
	Verify(context.Context, prizmAdapter.Observation, prizmAdapter.Action, prizmAdapter.ActionResult, []string) error
}

// InteractionVerifierFunc adapts a function to InteractionVerifier.
type InteractionVerifierFunc func(context.Context, prizmAdapter.Observation, prizmAdapter.Action, prizmAdapter.ActionResult, []string) error

func (f InteractionVerifierFunc) Verify(ctx context.Context, obs prizmAdapter.Observation, action prizmAdapter.Action, result prizmAdapter.ActionResult, checks []string) error {
	return f(ctx, obs, action, result, checks)
}

// InteractionRunStatus identifies the persisted adapter loop state.
type InteractionRunStatus string

const (
	InteractionRunning   InteractionRunStatus = "running"
	InteractionPaused    InteractionRunStatus = "paused"
	InteractionCompleted InteractionRunStatus = "completed"
	InteractionFailed    InteractionRunStatus = "failed"
)

// InteractionRunRecord is the inspectable recovery record for one adapter run.
type InteractionRunRecord struct {
	RunID           string                     `json:"run_id"`
	Goal            string                     `json:"goal,omitempty"`
	Adapter         string                     `json:"adapter"`
	Status          InteractionRunStatus       `json:"status"`
	Step            int                        `json:"step"`
	PhaseIndex      int                        `json:"phase_index"`
	PhaseActions    map[InteractionLane]int    `json:"phase_actions,omitempty"`
	LastObservation *prizmAdapter.Observation  `json:"last_observation,omitempty"`
	LastAction      *prizmAdapter.Action       `json:"last_action,omitempty"`
	LastResult      *prizmAdapter.ActionResult `json:"last_result,omitempty"`
	LastError       string                     `json:"last_error,omitempty"`
	LastActionAt    *time.Time                 `json:"last_action_at,omitempty"`
	Events          []event.Event              `json:"events,omitempty"`
	UpdatedAt       time.Time                  `json:"updated_at"`
}

// InteractionRunStore persists recovery records.
type InteractionRunStore interface {
	Load(context.Context, string) (InteractionRunRecord, error)
	Save(context.Context, InteractionRunRecord) error
}

var ErrInteractionRunNotFound = errors.New("multiagent: interaction run not found")

// MemoryInteractionRunStore is a concurrency-safe test and embedding store.
type MemoryInteractionRunStore struct {
	mu      sync.Mutex
	records map[string]InteractionRunRecord
}

func NewMemoryInteractionRunStore() *MemoryInteractionRunStore {
	return &MemoryInteractionRunStore{records: make(map[string]InteractionRunRecord)}
}

func (s *MemoryInteractionRunStore) Load(_ context.Context, runID string) (InteractionRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[runID]
	if !ok {
		return InteractionRunRecord{}, ErrInteractionRunNotFound
	}
	return cloneInteractionRecord(record), nil
}

func (s *MemoryInteractionRunStore) Save(_ context.Context, record InteractionRunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[string]InteractionRunRecord)
	}
	s.records[record.RunID] = cloneInteractionRecord(record)
	return nil
}

// JSONInteractionRunStore is a simple durable store for restart recovery.
type JSONInteractionRunStore struct {
	Directory string
}

func (s JSONInteractionRunStore) path(runID string) (string, error) {
	if strings.TrimSpace(runID) == "" || filepath.Base(runID) != runID || strings.Contains(runID, "..") {
		return "", errors.New("multiagent: invalid interaction run id")
	}
	return filepath.Join(s.Directory, runID+".json"), nil
}

func (s JSONInteractionRunStore) Load(_ context.Context, runID string) (InteractionRunRecord, error) {
	path, err := s.path(runID)
	if err != nil {
		return InteractionRunRecord{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return InteractionRunRecord{}, ErrInteractionRunNotFound
	}
	if err != nil {
		return InteractionRunRecord{}, err
	}
	var record InteractionRunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return InteractionRunRecord{}, fmt.Errorf("multiagent: decode interaction run: %w", err)
	}
	return record, nil
}

func (s JSONInteractionRunStore) Save(_ context.Context, record InteractionRunRecord) error {
	path, err := s.path(record.RunID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Directory, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// InteractionScheduler runs the bounded adapter loop.
type InteractionScheduler struct {
	adapter  prizmAdapter.Adapter
	selector InteractionDecisionSelector
	policy   InteractionPolicy
	verifier InteractionVerifier
	store    InteractionRunStore
	events   EventSink
	now      func() time.Time
}

type InteractionSchedulerOptions struct {
	Policy   InteractionPolicy
	Verifier InteractionVerifier
	Store    InteractionRunStore
	Events   EventSink
	Clock    func() time.Time
}

func NewInteractionScheduler(a prizmAdapter.Adapter, selector InteractionDecisionSelector, options InteractionSchedulerOptions) (*InteractionScheduler, error) {
	if a == nil {
		return nil, errors.New("multiagent: interaction adapter is required")
	}
	if selector == nil {
		return nil, errors.New("multiagent: interaction decision selector is required")
	}
	if options.Policy == nil {
		options.Policy = AllowInteractionPolicy{}
	}
	if options.Store == nil {
		options.Store = NewMemoryInteractionRunStore()
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	return &InteractionScheduler{adapter: a, selector: selector, policy: options.Policy, verifier: options.Verifier, store: options.Store, events: options.Events, now: options.Clock}, nil
}

// Run advances a new or paused interaction run until an action budget, pause,
// terminal observation, cancellation, or failure is reached.
func (s *InteractionScheduler) Run(ctx context.Context, request InteractionRunRequest) (InteractionRunRecord, error) {
	if strings.TrimSpace(request.RunID) == "" {
		return InteractionRunRecord{}, errors.New("multiagent: interaction run id is required")
	}
	plan := request.Plan.normalized()
	health, healthErr := s.adapter.Health(ctx)
	if healthErr != nil {
		return InteractionRunRecord{}, healthErr
	}
	if health == nil || !health.Ready {
		if health == nil {
			return InteractionRunRecord{}, errors.New("multiagent: interaction adapter is not ready")
		}
		return InteractionRunRecord{}, fmt.Errorf("multiagent: interaction adapter is not ready: %s", health.Message)
	}
	record, err := s.store.Load(ctx, request.RunID)
	if errors.Is(err, ErrInteractionRunNotFound) {
		record = InteractionRunRecord{RunID: request.RunID, Goal: request.Goal, Adapter: s.adapter.Name(), Status: InteractionRunning, PhaseActions: map[InteractionLane]int{}}
	} else if err != nil {
		return InteractionRunRecord{}, err
	} else if record.Status == InteractionCompleted || record.Status == InteractionFailed {
		return record, nil
	} else {
		record.Status = InteractionRunning
		s.emit(&record, event.EventInteractionResumed, map[string]any{"reason": "resume from checkpoint"})
	}
	if record.PhaseActions == nil {
		record.PhaseActions = map[InteractionLane]int{}
	}
	if plan.RunDeadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, plan.RunDeadline)
		defer cancel()
	}
	for record.Step < plan.MaxActions {
		if err := ctx.Err(); err != nil {
			s.neutralize(ctx, &record, "context ended: "+err.Error())
			record.Status, record.LastError = InteractionFailed, err.Error()
			_ = s.store.Save(ctx, record)
			return record, err
		}
		phase := plan.Phases[record.PhaseIndex%len(plan.Phases)]
		if phase.Lane == "" {
			phase.Lane = LaneTactical
		}
		if phase.MaxActions > 0 && record.PhaseActions[phase.Lane] >= phase.MaxActions {
			record.PhaseIndex = (record.PhaseIndex + 1) % len(plan.Phases)
			continue
		}
		if phase.Cadence > 0 && record.LastActionAt != nil {
			wait := record.LastActionAt.Add(phase.Cadence).Sub(s.now())
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					continue
				case <-timer.C:
				}
			}
		}
		obs, err := s.observe(ctx, request.Observation)
		if err != nil {
			s.neutralize(ctx, &record, "observation failed")
			return s.fail(ctx, record, err)
		}
		record.LastObservation = &obs
		s.emit(&record, event.EventInteractionObserved, map[string]any{"observation_id": obs.ID, "lane": phase.Lane})
		if obs.Terminal {
			record.Status = InteractionCompleted
			return s.save(ctx, record)
		}
		legal, err := s.legalActions(ctx, &obs)
		if err != nil {
			s.neutralize(ctx, &record, "legal action discovery failed")
			return s.fail(ctx, record, err)
		}
		action, err := s.selector.Select(ctx, obs, legal, phase)
		if err != nil {
			s.neutralize(ctx, &record, "decision selection failed")
			return s.fail(ctx, record, err)
		}
		if !containsCapability(legal, action.Name) {
			s.emit(&record, event.EventInteractionActionRejected, map[string]any{"action": action.Name, "reason": "action is not legal"})
			return s.fail(ctx, record, fmt.Errorf("multiagent: illegal adapter action %q", action.Name))
		}
		s.emit(&record, event.EventInteractionDecisionSelected, map[string]any{"action": action.Name, "lane": phase.Lane})
		auth, reason, err := s.policy.Authorize(ctx, request.RunID, action, obs)
		if err != nil {
			return s.fail(ctx, record, err)
		}
		if auth == InteractionApprovalPending {
			record.Status, record.LastError = InteractionPaused, reason
			s.emit(&record, event.EventInteractionApprovalPaused, map[string]any{"action": action.Name, "reason": reason})
			return s.save(ctx, record)
		}
		if auth != InteractionAllowed {
			s.emit(&record, event.EventInteractionActionRejected, map[string]any{"action": action.Name, "reason": reason})
			return s.fail(ctx, record, fmt.Errorf("multiagent: action denied: %s", reason))
		}
		if err := s.validate(ctx, action); err != nil {
			s.emit(&record, event.EventInteractionActionRejected, map[string]any{"action": action.Name, "reason": err.Error()})
			return s.fail(ctx, record, err)
		}
		s.emit(&record, event.EventInteractionActionValidated, map[string]any{"action": action.Name})
		result, execErr := s.executeWithRetry(ctx, &record, action, plan.MaxRetries, plan.Deadman)
		record.LastAction = &action
		record.LastResult = &result
		now := s.now().UTC()
		record.LastActionAt = &now
		record.Step++
		record.PhaseActions[phase.Lane]++
		if execErr != nil || !result.Success {
			reason := result.Error
			if execErr != nil {
				reason = execErr.Error()
			}
			record.LastError = reason
			if strings.TrimSpace(reason) == "" {
				reason = "adapter action failed"
			}
			return s.fail(ctx, record, errors.New(reason))
		}
		s.emit(&record, event.EventInteractionActionExecuted, map[string]any{"action": action.Name, "success": true})
		if s.verifier != nil {
			if err := s.verifier.Verify(ctx, obs, action, result, phase.Verification); err != nil {
				s.emit(&record, event.EventInteractionVerification, map[string]any{"action": action.Name, "success": false, "error": err.Error()})
				s.neutralize(ctx, &record, "verification failed")
				return s.fail(ctx, record, err)
			}
		}
		s.emit(&record, event.EventInteractionVerification, map[string]any{"action": action.Name, "success": true})
		if interrupted(obs, phase.Interrupts) {
			record.PhaseIndex = 0
		} else {
			record.PhaseIndex = (record.PhaseIndex + 1) % len(plan.Phases)
		}
		if err := s.store.Save(ctx, record); err != nil {
			return record, err
		}
	}
	record.Status = InteractionCompleted
	return s.save(ctx, record)
}

func (s *InteractionScheduler) observe(ctx context.Context, req prizmAdapter.ObservationRequest) (prizmAdapter.Observation, error) {
	provider, ok := s.adapter.(prizmAdapter.ObservationProvider)
	if !ok {
		return prizmAdapter.Observation{}, errors.New("multiagent: adapter does not provide observations")
	}
	obs, err := provider.Observe(ctx, req)
	if err != nil {
		return prizmAdapter.Observation{}, err
	}
	if obs == nil {
		return prizmAdapter.Observation{}, errors.New("multiagent: adapter returned nil observation")
	}
	return *obs, nil
}

func (s *InteractionScheduler) legalActions(ctx context.Context, obs *prizmAdapter.Observation) ([]prizmAdapter.Capability, error) {
	if provider, ok := s.adapter.(prizmAdapter.LegalActionProvider); ok {
		return provider.LegalActions(ctx, obs)
	}
	return s.adapter.Capabilities(), nil
}

func (s *InteractionScheduler) validate(ctx context.Context, action prizmAdapter.Action) error {
	if structured, ok := s.adapter.(prizmAdapter.StructuredInteractionAdapter); ok {
		return structured.ValidateActionRequest(ctx, action)
	}
	if validator, ok := s.adapter.(prizmAdapter.ActionValidator); ok {
		return validator.ValidateAction(ctx, action.Name, action.Input)
	}
	return nil
}

func (s *InteractionScheduler) execute(ctx context.Context, action prizmAdapter.Action) (prizmAdapter.ActionResult, error) {
	if structured, ok := s.adapter.(prizmAdapter.StructuredInteractionAdapter); ok {
		return structured.ExecuteAction(ctx, action)
	}
	result, err := s.adapter.Execute(ctx, action.Name, action.Input)
	if result == nil {
		return prizmAdapter.ActionResult{}, err
	}
	return *result, err
}

func (s *InteractionScheduler) executeWithRetry(ctx context.Context, record *InteractionRunRecord, action prizmAdapter.Action, maxRetries int, deadman time.Duration) (prizmAdapter.ActionResult, error) {
	var result prizmAdapter.ActionResult
	var err error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, deadman)
		result, err = s.execute(attemptCtx, action)
		cancel()
		if err == nil && result.Success {
			return result, nil
		}
		if attempt < maxRetries {
			s.neutralize(ctx, record, fmt.Sprintf("bounded retry %d", attempt+1))
		}
	}
	s.neutralize(ctx, record, "action failed after bounded retries")
	s.emit(record, event.EventInteractionActionFailed, map[string]any{"action": action.Name, "error": errorString(err, result.Error)})
	return result, err
}

func (s *InteractionScheduler) neutralize(ctx context.Context, record *InteractionRunRecord, reason string) {
	if neutralizer, ok := s.adapter.(prizmAdapter.Neutralizer); ok {
		neutralizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = neutralizer.Neutralize(neutralizeCtx, reason)
		cancel()
	}
	s.emit(record, event.EventInteractionNeutralized, map[string]any{"reason": reason})
}

func (s *InteractionScheduler) emit(record *InteractionRunRecord, typ string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["run_id"], payload["adapter"], payload["step"] = record.RunID, record.Adapter, record.Step
	evt := event.NewEvent(typ, "prizm-interaction-scheduler", payload)
	evt.Metadata.RunID = record.RunID
	record.Events = append(record.Events, evt)
	if s.events != nil {
		s.events.Emit(evt)
	}
}

func (s *InteractionScheduler) fail(ctx context.Context, record InteractionRunRecord, err error) (InteractionRunRecord, error) {
	record.Status, record.LastError = InteractionFailed, err.Error()
	_ = s.store.Save(ctx, record)
	return record, err
}

func (s *InteractionScheduler) save(ctx context.Context, record InteractionRunRecord) (InteractionRunRecord, error) {
	record.UpdatedAt = s.now().UTC()
	if err := s.store.Save(ctx, record); err != nil {
		return record, err
	}
	return record, nil
}

func containsCapability(legal []prizmAdapter.Capability, name string) bool {
	for _, capability := range legal {
		if capability.Action == name {
			return true
		}
	}
	return false
}

func interrupted(obs prizmAdapter.Observation, triggers []string) bool {
	if len(triggers) == 0 || obs.Metadata == nil {
		return false
	}
	value, _ := obs.Metadata["interrupt"].(string)
	for _, trigger := range triggers {
		if trigger == value {
			return true
		}
	}
	return false
}

func errorString(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

func cloneInteractionRecord(record InteractionRunRecord) InteractionRunRecord {
	clone := record
	clone.Events = append([]event.Event(nil), record.Events...)
	if record.PhaseActions != nil {
		clone.PhaseActions = make(map[InteractionLane]int, len(record.PhaseActions))
		for key, value := range record.PhaseActions {
			clone.PhaseActions[key] = value
		}
	}
	return clone
}
