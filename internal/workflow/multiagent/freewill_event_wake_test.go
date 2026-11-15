package multiagent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
)

func TestFreewillEventWakeAllowlistAndDedupeSurviveStoreReopen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "freewill-event-wake.db")
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		_ = events.Close()
		t.Fatal(err)
	}
	selector := &sleepingFreewillSelector{}
	service := newEventWakeServiceForTest(t, events, store, selector)

	if _, err := service.HandleEvent(ctx, event.NewEvent("prizm.agent.completed", "test", nil)); !errors.Is(err, ErrFreewillEventNotAllowed) {
		t.Fatalf("unlisted event was not rejected: %v", err)
	}
	if _, err := service.HandleEvent(ctx, event.Event{ID: "evt_invalid", Type: "prizm.task.completed"}); !errors.Is(err, ErrFreewillEventNotAllowed) {
		t.Fatalf("malformed canonical event ID was not rejected: %v", err)
	}
	if selector.calls != 0 {
		t.Fatalf("rejected event invoked selector %d times", selector.calls)
	}

	incoming := event.NewEvent("prizm.task.completed", "test", map[string]any{"task": "synthetic"})
	first, err := service.HandleEvent(ctx, incoming)
	if err != nil {
		t.Fatalf("first event wake: %v", err)
	}
	if first.Freewill == nil || first.Freewill.Lifecycle != freewill.StateSleeping || first.Freewill.ChoiceID != "" || first.Freewill.ApprovalRef != "" || !first.Freewill.ExecutionDisabled {
		t.Fatalf("event wake did not remain a decision-only sleep: %#v", first.Freewill)
	}
	if selector.calls != 1 || len(selector.runIDs) != 1 || selector.runIDs[0] != first.State.RunID {
		t.Fatalf("first event selector calls/run IDs = %d/%v", selector.calls, selector.runIDs)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := events.Close(); err != nil {
		t.Fatal(err)
	}

	events, err = event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		_ = events.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = events.Close()
	})
	service = newEventWakeServiceForTest(t, events, store, selector)
	duplicate, err := service.HandleEvent(ctx, incoming)
	if err != nil {
		t.Fatalf("duplicate event after store reopen: %v", err)
	}
	if duplicate.State.RunID != first.State.RunID || selector.calls != 1 {
		t.Fatalf("reopened duplicate changed run or selected again: first=%s duplicate=%s calls=%d", first.State.RunID, duplicate.State.RunID, selector.calls)
	}

	storedEvents, err := events.Query(ctx, event.EventFilter{RunID: first.State.RunID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range storedEvents {
		if stored.Type == event.EventFreewillApprovalProposed || stored.Type == event.EventFreewillApprovalDecided || stored.Type == event.EventFreewillApprovalReconciled || stored.Type == event.EventMultiAgentToolDiagnostic || stored.Type == event.V1EventTypes.ToolCalled || stored.Type == event.V3EventTypes.ToolRequested {
			t.Fatalf("decision-only event wake emitted authority/tool event %q", stored.Type)
		}
	}
}

func newEventWakeServiceForTest(t *testing.T, events *event.SQLiteEventStore, store *SQLiteDurableRunStore, selector FreewillSelectionProvider) *FreewillWakeService {
	t.Helper()
	runtime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, store, newMemoryRunClaimer(), events)
	control, err := NewFreewillOperatorControl(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewFreewillWakeService(runtime, store, selector, FreewillWakeServiceConfig{
		Enabled: true, SchedulingEnabled: false, EventKinds: []string{"prizm.task.completed"},
		AgentID: "synthetic-selector", Scope: freewill.Scope{ProjectID: "synthetic-project", Workspace: "synthetic-workspace"},
		Objective: "Choose a safe read-only follow-up.", Owner: "synthetic-owner", LeaseFor: time.Minute,
		OperatorControl: control,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
