package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestDashboardTaskNotificationOutboxPersistsOneRunScopedFact(t *testing.T) {
	ctx := context.Background()
	runDir := t.TempDir()
	const runID = "run_notification_test"
	if err := os.MkdirAll(filepath.Join(runDir, runID), 0o755); err != nil {
		t.Fatal(err)
	}
	service, err := newDashboardTaskNotificationService(runDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })

	state := multiagent.RunState{
		RunID: runID, Status: multiagent.RunStatusCompleted,
		CurrentTask:     multiagent.TaskReference{ID: "task-safe"},
		RequestIdentity: &multiagent.RequestIdentity{RequesterID: "api", SessionID: "sess-safe", ResultDestination: "dashboard"},
	}
	if err := service.enqueueTerminal(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := service.enqueueTerminal(ctx, state); err != nil {
		t.Fatal(err)
	}
	key := "dashboard-terminal-" + runID
	delivery, err := service.outbox.Get(ctx, key)
	if err != nil || delivery.State != event.DeliveryPending {
		t.Fatalf("delivery before dispatch = %+v, err=%v", delivery, err)
	}
	if err := service.dispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
	delivery, err = service.outbox.Get(ctx, key)
	if err != nil || delivery.State != event.DeliveryDelivered {
		t.Fatalf("delivery after dispatch = %+v, err=%v", delivery, err)
	}

	store, err := event.NewSQLiteEventStore(filepath.Join(runDir, runID, "multiagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Query(ctx, event.EventFilter{RunID: runID, Type: event.EventDashboardTaskCompleted, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("completed notification count = %d, want 1", len(events))
	}
	if events[0].Payload["requester_id"] != "api" || events[0].Payload["session_id"] != "sess-safe" || events[0].Payload["report_ref"] != "multiagent_report.json" {
		t.Fatalf("notification payload = %+v", events[0].Payload)
	}
	if _, hasPrompt := events[0].Payload["description"]; hasPrompt {
		t.Fatalf("notification leaked task content: %+v", events[0].Payload)
	}
}

func TestDashboardTaskNotificationPublisherIsIdempotentAcrossDispatchCrash(t *testing.T) {
	ctx := context.Background()
	runDir := t.TempDir()
	const runID = "run_notification_replay"
	if err := os.MkdirAll(filepath.Join(runDir, runID), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(dashboardTerminalPayload{
		RunID: runID, Status: string(multiagent.RunStatusFailed), ReportRef: "multiagent_report.json",
		RequesterID: "api", SessionID: "sess-replay",
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher := dashboardTaskNotificationPublisher{runDir: runDir}
	for i := 0; i < 2; i++ { // models crash after Store but before MarkDelivered
		if err := publisher.Publish(ctx, dashboardTaskTerminalSubject, data); err != nil {
			t.Fatal(err)
		}
	}
	store, err := event.NewSQLiteEventStore(filepath.Join(runDir, runID, "multiagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Query(ctx, event.EventFilter{RunID: runID, Type: event.EventDashboardTaskFailed, Limit: 10})
	if err != nil || len(events) != 1 {
		t.Fatalf("failed notification events = %d, err=%v; want one", len(events), err)
	}
}

func TestDurableTaskLifecycleStartsWithoutGraphDelegationFlag(t *testing.T) {
	t.Setenv(graphRoleDelegationRequestedEnv, "")
	service, err := newDashboardTaskNotificationService(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.Start(ctx, "")
	if service.controller == nil {
		t.Fatal("durable task lifecycle did not initialize when graph role delegation is disabled")
	}
}

func TestDurableTaskLifecycleRecoversPersistedProposalDecisionAfterRestart(t *testing.T) {
	ctx := context.Background()
	fixture := newProposalControllerFixture(t, "run-proposal-decision-restart")
	stored, err := fixture.runtime.store.Load(ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	stored.State.RequestIdentity = &multiagent.RequestIdentity{RequesterID: "api", SessionID: "sess-restart", ResultDestination: "dashboard", AcceptedAt: time.Now().UTC()}
	if _, err := fixture.runtime.store.Checkpoint(ctx, stored.Revision, stored, nil); err != nil {
		t.Fatalf("persist dashboard origin: %v", err)
	}
	proposal, err := approval.NewStore(fixture.runDir).Load(fixture.runID, fixture.approval)
	if err != nil {
		t.Fatal(err)
	}
	if err := proposal.Approve("dashboard-operator"); err != nil {
		t.Fatal(err)
	}
	if err := approval.NewStore(fixture.runDir).Save(proposal); err != nil {
		t.Fatal(err)
	}

	// This service instance models startup after the API persisted the decision
	// but before its detached Resume goroutine was dispatched.
	service, err := newDashboardTaskNotificationService(fixture.runDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	service.controller = newReferenceMultiAgentController(fixture.runDir, "")
	resumed := make(chan string, 1)
	service.resumeRecovery = func(ctx context.Context, runID string) error {
		record, err := fixture.runtime.store.Load(ctx, runID)
		if err != nil {
			return err
		}
		record.Waiting.SafeToRetry = false // models an ambiguous apply checkpoint
		if _, err := fixture.runtime.store.Checkpoint(ctx, record.Revision, record, nil); err != nil {
			return err
		}
		resumed <- runID
		return nil
	}
	if err := service.scan(ctx); err != nil {
		t.Fatalf("startup recovery scan: %v", err)
	}
	service.recoveryWG.Wait()
	select {
	case got := <-resumed:
		if got != fixture.runID {
			t.Fatalf("recovery resumed %q, want exact run %q", got, fixture.runID)
		}
	default:
		t.Fatal("startup scan did not queue the persisted approval decision for recovery")
	}
	if err := service.scan(ctx); err != nil {
		t.Fatalf("ambiguous checkpoint scan: %v", err)
	}
	service.recoveryWG.Wait()
	select {
	case duplicate := <-resumed:
		t.Fatalf("unsafe approved wait was automatically retried for %q", duplicate)
	default:
	}
	decision, err := approval.NewStore(fixture.runDir).Load(fixture.runID, fixture.approval)
	if err != nil || decision.Status != approval.StatusApproved {
		t.Fatalf("approval decision after restart scan = %v, err=%v", decision, err)
	}
}
