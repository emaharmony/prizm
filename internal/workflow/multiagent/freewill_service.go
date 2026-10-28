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
	"github.com/emaharmony/prizm/internal/freewill"
)

var ErrFreewillDisabled = errors.New("multiagent: Freewill scheduling is disabled")
var ErrFreewillWakeScopeMismatch = errors.New("multiagent: Freewill wake identity does not match configured host scope")

// FreewillSelectionInput contains host-verified identity and resumable
// candidates. The provider cannot change the wake scope or agent identity.
type FreewillSelectionInput struct {
	RunID            string
	CorrelationID    string
	AgentID          string
	Scope            freewill.Scope
	ResumeCandidates []freewill.CandidateIntention
	Budget           freewill.ResourceBudget
	RecoveryFeedback string
}

// FreewillSelectionEvidence is provider metadata, not model output.
type FreewillSelectionEvidence struct {
	Provider string
	Model    string
	Usage    cost.TokenUsage
}

// FreewillSelectionProvider performs exactly one no-tools selection request.
type FreewillSelectionProvider interface {
	SelectFreewill(context.Context, FreewillSelectionInput) (freewill.Selection, FreewillSelectionEvidence, error)
}

type FreewillWakeServiceConfig struct {
	Enabled           bool
	SchedulingEnabled bool
	AgentID           string
	Scope             freewill.Scope
	Owner             string
	LeaseFor          time.Duration
	Budget            freewill.ResourceBudget
	OperatorControl   *FreewillOperatorControl
}

// FreewillWakeService composes the existing durable runtime with a configured
// selector. It accepts only scheduler ticks and never advances an ordinary
// workflow role.
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
		CorrelationID: runID,
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

// ProcessWake resumes only a Freewill record that is waiting in the wake
// checkpoint. A crash after acceptance or selection start is recoverable by
// the same scheduler scan after the fenced lease expires.
func (s *FreewillWakeService) ProcessWake(ctx context.Context, runID string) (DurableRun, error) {
	if !s.config.Enabled || !s.config.SchedulingEnabled {
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
	_, lease, err := s.runtime.ClaimFreewillWake(ctx, runID, s.config.Owner, s.config.LeaseFor)
	if err != nil {
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
	resumes := s.verifiedResumeCandidates(ctx, record)
	if err := ensureFreewillNotHalted(providerCtx, s.store, s.config.AgentID, s.config.Scope); err != nil {
		return record, err
	}
	selection, evidence, err := s.selector.SelectFreewill(providerCtx, FreewillSelectionInput{
		RunID: runID, CorrelationID: record.Freewill.CorrelationID, AgentID: s.config.AgentID,
		Scope: s.config.Scope, ResumeCandidates: resumes, Budget: s.config.Budget,
		RecoveryFeedback: freewillSelectionRecoveryFeedback(record.Freewill.SelectionFailureCode),
	})
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
	if err := ensureFreewillNotHalted(ctx, s.store, s.config.AgentID, s.config.Scope); err != nil {
		return record, err
	}
	if err := providerCtx.Err(); err != nil {
		return record, err
	}
	stopRenewal()
	lease = currentLease()
	verified := make(map[string]struct{}, len(resumes))
	for _, candidate := range resumes {
		verified[candidate.CandidateID] = struct{}{}
	}
	record, err = s.runtime.CommitFreewillSelectionWithEvidence(ctx, lease, selection, verified, evidence)
	if err != nil {
		return record, err
	}
	committed = true
	return record, nil
}

func (s *FreewillWakeService) validateConfiguredWake(record DurableRun) error {
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil || record.State.RequestIdentity == nil ||
		record.State.RequestIdentity.RequesterID != s.config.AgentID || record.Freewill.Scope != s.config.Scope {
		return ErrFreewillWakeScopeMismatch
	}
	return nil
}

func freewillSelectionRecoveryFeedback(code string) string {
	switch code {
	case "invalid_idea_lines":
		return "Return only three lines, each beginning exactly IDEA: followed by a short decision-only objective. No preamble, bullets, explanation, JSON, paths, authority, tools, network, secrets, approval, or execution requests."
	case "invalid_selection_schema", "empty_selection_response", "selection_tool_request", "invalid_selection_json", "unknown_selection_field", "selection_candidate_count", "selection_contract_invalid":
		return "Every response, even sleep/resume, must include exactly 3 fresh candidates. Each has title, goal, reason_summary, requested_capabilities, score 0-100, score_factors, score_rationale; budget optional. Factors: scope_fit, feasibility, impact, novelty, readiness. New: include reason_summary; host selects top (first tie). Resume: include all 3, set supplied resume_candidate_id and reason_summary. Sleep: include all 3 and sleep_reason only. Never omit candidates. Host binds IDs/scope. No tools/extra fields."
	case "provider_transient":
		return "The previous provider attempt failed transiently. Retry the same selection request and return the required schema."
	default:
		return ""
	}
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

func (s *FreewillWakeService) verifiedResumeCandidates(ctx context.Context, current DurableRun) []freewill.CandidateIntention {
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
		for _, candidate := range record.Freewill.Candidates {
			if candidate.Request.CandidateSource != freewill.SourceNew {
				continue
			}
			candidate.Request.CandidateSource = freewill.SourceResume
			return []freewill.CandidateIntention{candidate}
		}
	}
	return nil
}

func (s *FreewillWakeService) RecoverPending(ctx context.Context, now time.Time, pageSize int) error {
	if !s.config.Enabled || !s.config.SchedulingEnabled {
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
