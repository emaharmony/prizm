package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

const dashboardTaskTerminalSubject = "prizm.dashboard.task.terminal"

type dashboardTerminalPayload struct {
	RunID       string `json:"run_id"`
	Status      string `json:"status"`
	ReportRef   string `json:"report_ref"`
	RequesterID string `json:"requester_id"`
	SessionID   string `json:"session_id"`
}

// dashboardTaskNotificationPublisher turns a durable outbox delivery into a
// run-scoped canonical event. SQLiteEventStore.Store is idempotent by event
// ID, so a crash after storing and before marking delivery simply replays the
// same fact.
type dashboardTaskNotificationPublisher struct{ runDir string }

func (p dashboardTaskNotificationPublisher) Publish(ctx context.Context, subject string, data []byte) error {
	if subject != dashboardTaskTerminalSubject {
		return fmt.Errorf("dashboard notification: unsupported subject %q", subject)
	}
	var payload dashboardTerminalPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("dashboard notification: decode payload: %w", err)
	}
	if payload.RunID == "" || payload.RequesterID == "" || payload.SessionID == "" || payload.ReportRef != "multiagent_report.json" || !multiagent.RunStatus(payload.Status).Terminal() {
		return errors.New("dashboard notification: incomplete or mismatched terminal payload")
	}
	eventType := event.EventDashboardTaskFailed
	if payload.Status == string(multiagent.RunStatusCompleted) {
		eventType = event.EventDashboardTaskCompleted
	}
	store, err := event.NewSQLiteEventStore(filepath.Join(p.runDir, payload.RunID, "multiagent.db"))
	if err != nil {
		return fmt.Errorf("dashboard notification: open run event store: %w", err)
	}
	defer store.Close()
	key := "dashboard-terminal-" + payload.RunID
	digest := sha256.Sum256([]byte(key))
	return store.Store(ctx, event.Event{
		ID: event.CommandEventID(key), Type: eventType, Source: "prizm-dashboard-notification-outbox",
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano), CorrelationID: "corr_" + hex.EncodeToString(digest[:12]),
		Payload: map[string]any{
			"run_id": payload.RunID, "status": payload.Status, "report_ref": payload.ReportRef,
			"requester_id": payload.RequesterID, "session_id": payload.SessionID,
		},
		Metadata: event.EventMetadata{RunID: payload.RunID, SessionID: payload.SessionID, Outcome: payload.Status},
	})
}

type dashboardTaskNotificationService struct {
	runDir         string
	outbox         *event.SQLiteOutbox
	dispatcher     event.Dispatcher
	controller     *referenceMultiAgentController
	resumeRecovery func(context.Context, string) error
	mu             sync.Mutex
	recoveryMu     sync.Mutex
	recovering     map[string]bool
	recoveryWG     sync.WaitGroup
}

func newDashboardTaskNotificationService(runDir, dataDir string) (*dashboardTaskNotificationService, error) {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = runDir
	}
	path := filepath.Join(dataDir, "dashboard-notifications.db")
	outbox, err := event.NewSQLiteOutbox(path)
	if err != nil {
		return nil, fmt.Errorf("open dashboard notification outbox: %w", err)
	}
	publisher := dashboardTaskNotificationPublisher{runDir: runDir}
	return &dashboardTaskNotificationService{
		runDir: runDir, outbox: outbox,
		dispatcher: event.Dispatcher{Outbox: outbox, Publisher: publisher, Lease: 30 * time.Second, MaxAttempts: 5, RetryAfter: time.Second, DeferTerminalOnPublishFailure: true},
		recovering: make(map[string]bool),
	}, nil
}

func (s *dashboardTaskNotificationService) Close() error { return s.outbox.Close() }

// enqueueTerminal stores one dashboard notification command for a terminal
// accepted run. Its payload intentionally contains no task prompt or model
// output; the dashboard follows report_ref through the existing run API.
func (s *dashboardTaskNotificationService) enqueueTerminal(ctx context.Context, state multiagent.RunState) error {
	identity := state.RequestIdentity
	if !state.Status.Terminal() || identity == nil || identity.ResultDestination != "dashboard" {
		return nil
	}
	payload := dashboardTerminalPayload{
		RunID: state.RunID, Status: string(state.Status), ReportRef: "multiagent_report.json",
		RequesterID: identity.RequesterID, SessionID: identity.SessionID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	key := "dashboard-terminal-" + state.RunID
	digest := sha256.Sum256([]byte(key))
	command := event.Command{
		EventID: event.CommandEventID(key), Type: dashboardTaskTerminalSubject,
		RunID: state.RunID, TaskID: state.CurrentTask.ID,
		CorrelationID: "corr_" + hex.EncodeToString(digest[:12]),
		CausationID:   state.RunID, IdempotencyKey: key,
		SchemaVersion: event.CommandSchemaVersion, Payload: encoded,
	}
	_, err = s.outbox.Accept(ctx, dashboardTaskTerminalSubject, command)
	return err
}

func (s *dashboardTaskNotificationService) dispatchPending(ctx context.Context) error {
	for {
		processed, err := s.dispatcher.DispatchOne(ctx)
		if err != nil {
			return err
		}
		if !processed {
			return nil
		}
	}
}

// Start runs recovery and notification delivery independently of graph-role
// delegation configuration. The runtime's durable claim serializes each run
// against API Resume and other service instances.
func (s *dashboardTaskNotificationService) Start(ctx context.Context, configPath string) {
	s.controller = newReferenceMultiAgentController(s.runDir, configPath)
	s.resumeRecovery = s.controller.resumeSync
	defer s.recoveryWG.Wait()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.scan(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[WARN] durable task lifecycle scan failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *dashboardTaskNotificationService) scan(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	locator := multiagent.RunLocator{Root: s.runDir}
	runs, err := locator.ListRuns(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, summary := range runs {
		runtime, runStore, eventStore, openErr := locator.OpenInspection(ctx, summary.RunID)
		if openErr != nil {
			if firstErr == nil {
				firstErr = openErr
			}
			continue
		}
		record, inspectErr := runtime.Inspect(ctx, summary.RunID)
		var candidates []multiagent.RecoveryCandidate
		proposalDecisionReady := false
		if inspectErr == nil && !record.State.Status.Terminal() && record.State.RequestIdentity != nil && record.State.RequestIdentity.ResultDestination == "dashboard" {
			// Candidate enumeration is store-wide, while this runtime was opened
			// for one run's compiled graph. Keep the page large enough that a
			// dashboard-origin run is not starved by older unrelated records;
			// filter by the current trusted identity and run ID below.
			candidates, inspectErr = runtime.ListRecoveryCandidates(ctx, time.Now().UTC(), 100000)
			if inspectErr == nil && record.Phase == multiagent.CheckpointWaiting && record.Waiting != nil && record.Waiting.Kind == "proposal_approval" && record.Waiting.SafeToRetry {
				storedApproval, approvalErr := approval.NewStore(s.runDir).Load(summary.RunID, record.Waiting.ApprovalID)
				if approvalErr != nil {
					inspectErr = fmt.Errorf("load persisted proposal decision for run %s: %w", summary.RunID, approvalErr)
				} else {
					proposalDecisionReady = storedApproval.Status == approval.StatusApproved || storedApproval.Status == approval.StatusDenied
				}
			}
		}
		if inspectErr == nil && record.State.Status.Terminal() && record.State.RequestIdentity != nil && record.State.RequestIdentity.ResultDestination == "dashboard" {
			manifest, manifestErr := loadReferenceManifest(s.runDir, summary.RunID)
			if manifestErr == nil {
				// A crash may occur after the terminal checkpoint and before the
				// normal driver publishes its report. Rebuild the deterministic
				// terminal report from durable state/events before announcing it.
				manifestErr = writeReferenceReport(ctx, s.runDir, manifest, &referenceRuntime{runtime: runtime, store: runStore, events: eventStore}, record.State)
			}
			if manifestErr != nil {
				inspectErr = manifestErr
			}
		}
		_ = runStore.Close()
		_ = eventStore.Close()
		if inspectErr != nil {
			if firstErr == nil {
				firstErr = inspectErr
			}
			continue
		}
		state := record.State
		if state.RequestIdentity == nil || state.RequestIdentity.ResultDestination != "dashboard" {
			continue
		}
		if state.Status.Terminal() {
			if err := s.enqueueTerminal(ctx, state); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if proposalDecisionReady {
			// Approval files are durable before the API schedules Resume. If the
			// process exits in that gap, the next service tick observes the exact
			// persisted decision and resumes through the ordinary run claim.
			s.scheduleRecovery(ctx, state.RunID)
			continue
		}
		for _, candidate := range candidates {
			if candidate.RunID == state.RunID && candidate.Ready {
				s.scheduleRecovery(ctx, candidate.RunID)
			}
		}
	}
	if err := s.dispatchPending(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *dashboardTaskNotificationService) scheduleRecovery(ctx context.Context, runID string) {
	s.recoveryMu.Lock()
	if s.recovering[runID] {
		s.recoveryMu.Unlock()
		return
	}
	s.recovering[runID] = true
	s.recoveryWG.Add(1)
	s.recoveryMu.Unlock()
	go func() {
		defer s.recoveryWG.Done()
		defer func() {
			s.recoveryMu.Lock()
			delete(s.recovering, runID)
			s.recoveryMu.Unlock()
		}()
		resume := s.resumeRecovery
		if resume == nil && s.controller != nil {
			resume = s.controller.resumeSync
		}
		if resume == nil {
			return
		}
		if err := resume(ctx, runID); err != nil && !errors.Is(err, multiagent.ErrRunClaimed) {
			// A missing/broken provider configuration must still turn an
			// accepted, safely resumable request into a terminal failure. The
			// inspection runtime has no provider dependency and can persist that
			// outcome through the same checkpoint/claim path.
			manifest, manifestErr := loadReferenceManifest(s.runDir, runID)
			if manifestErr == nil {
				var rt *referenceRuntime
				rt, manifestErr = openReferenceRuntimeWithInteraction(s.runDir, manifest, unavailableRoleRunner{}, nil, nil)
				if manifestErr == nil {
					_, manifestErr = rt.runtime.Resume(ctx, runID)
					_ = rt.close()
				}
			}
			if manifestErr != nil {
				log.Printf("[WARN] durable task recovery for run %s ended: %v (fallback: %v)", runID, err, manifestErr)
			} else {
				log.Printf("[WARN] durable task recovery for run %s fell back after live runtime error: %v", runID, err)
			}
		}
	}()
}
