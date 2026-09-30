package multiagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/adapter"
	"github.com/emaharmony/prizm/internal/event"
)

type fakeInteractionAdapter struct {
	observations []adapter.Observation
	index        int
	failures     int
	executions   int
	neutralized  []string
	block        bool
}

func (f *fakeInteractionAdapter) Name() string    { return "fake-environment" }
func (f *fakeInteractionAdapter) Version() string { return "1" }
func (f *fakeInteractionAdapter) Capabilities() []adapter.Capability {
	return []adapter.Capability{{Action: "advance"}, {Action: "stop"}}
}
func (f *fakeInteractionAdapter) Health(context.Context) (*adapter.HealthResult, error) {
	return &adapter.HealthResult{Ready: true}, nil
}
func (f *fakeInteractionAdapter) Observe(context.Context, adapter.ObservationRequest) (*adapter.Observation, error) {
	if len(f.observations) == 0 {
		return &adapter.Observation{ID: "empty", Terminal: true}, nil
	}
	idx := f.index
	if idx >= len(f.observations) {
		idx = len(f.observations) - 1
	}
	obs := f.observations[idx]
	f.index++
	return &obs, nil
}
func (f *fakeInteractionAdapter) LegalActions(context.Context, *adapter.Observation) ([]adapter.Capability, error) {
	return f.Capabilities(), nil
}
func (f *fakeInteractionAdapter) ValidateAction(_ context.Context, action string, _ map[string]any) error {
	if action == "invalid" {
		return errors.New("invalid action")
	}
	return nil
}
func (f *fakeInteractionAdapter) Execute(_ context.Context, action string, _ map[string]any) (*adapter.Result, error) {
	f.executions++
	if action == "stop" {
		return &adapter.Result{Success: true}, nil
	}
	if f.failures > 0 {
		f.failures--
		return &adapter.Result{Success: false, Error: "transient failure"}, nil
	}
	return &adapter.Result{Success: true, Output: map[string]any{"step": f.executions}}, nil
}

func (f *fakeInteractionAdapter) executeWithContext(ctx context.Context, action string) (*adapter.Result, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.Execute(ctx, action, nil)
}
func (f *fakeInteractionAdapter) Neutralize(_ context.Context, reason string) error {
	f.neutralized = append(f.neutralized, reason)
	return nil
}

func selectAdvance(context.Context, adapter.Observation, []adapter.Capability, InteractionPhase) (adapter.Action, error) {
	return adapter.Action{Name: "advance"}, nil
}

func TestInteractionSchedulerRunsInspectableLoop(t *testing.T) {
	fake := &fakeInteractionAdapter{observations: []adapter.Observation{{ID: "obs-1"}, {ID: "obs-2", Terminal: true}}}
	scheduler, err := NewInteractionScheduler(fake, InteractionDecisionSelectorFunc(selectAdvance), InteractionSchedulerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := scheduler.Run(context.Background(), InteractionRunRequest{RunID: "run-loop", Goal: "advance state", Plan: InteractionPlan{MaxActions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != InteractionCompleted || record.Step != 1 {
		t.Fatalf("unexpected record: %+v", record)
	}
	for _, typ := range []string{event.EventInteractionObserved, event.EventInteractionDecisionSelected, event.EventInteractionActionValidated, event.EventInteractionActionExecuted, event.EventInteractionVerification} {
		found := false
		for _, evt := range record.Events {
			if evt.Type == typ {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing inspectable event %q", typ)
		}
	}
}

func TestInteractionSchedulerRejectsIllegalAction(t *testing.T) {
	fake := &fakeInteractionAdapter{observations: []adapter.Observation{{ID: "obs-1"}}}
	scheduler, _ := NewInteractionScheduler(fake, InteractionDecisionSelectorFunc(func(context.Context, adapter.Observation, []adapter.Capability, InteractionPhase) (adapter.Action, error) {
		return adapter.Action{Name: "unknown"}, nil
	}), InteractionSchedulerOptions{})
	record, err := scheduler.Run(context.Background(), InteractionRunRequest{RunID: "run-illegal", Plan: InteractionPlan{MaxActions: 1}})
	if err == nil || !strings.Contains(err.Error(), "illegal") {
		t.Fatalf("expected illegal action error, got %v", err)
	}
	if record.Status != InteractionFailed || fake.executions != 0 {
		t.Fatalf("illegal action was executed: %+v", record)
	}
}

func TestInteractionSchedulerApprovalPauseAndResume(t *testing.T) {
	fake := &fakeInteractionAdapter{observations: []adapter.Observation{{ID: "obs-1"}, {ID: "obs-2"}, {ID: "obs-3", Terminal: true}}}
	store := NewMemoryInteractionRunStore()
	checks := 0
	policy := InteractionPolicyFunc(func(context.Context, string, adapter.Action, adapter.Observation) (InteractionAuthorization, string, error) {
		checks++
		if checks == 1 {
			return InteractionApprovalPending, "operator approval required", nil
		}
		return InteractionAllowed, "approved", nil
	})
	scheduler, _ := NewInteractionScheduler(fake, InteractionDecisionSelectorFunc(selectAdvance), InteractionSchedulerOptions{Store: store, Policy: policy})
	paused, err := scheduler.Run(context.Background(), InteractionRunRequest{RunID: "run-approval", Plan: InteractionPlan{MaxActions: 1}})
	if err != nil || paused.Status != InteractionPaused || fake.executions != 0 {
		t.Fatalf("expected approval pause, record=%+v err=%v", paused, err)
	}
	resumed, err := scheduler.Run(context.Background(), InteractionRunRequest{RunID: "run-approval", Plan: InteractionPlan{MaxActions: 1}})
	if err != nil || resumed.Status != InteractionCompleted || fake.executions != 1 {
		t.Fatalf("expected resumed completion, record=%+v err=%v", resumed, err)
	}
}

func TestInteractionSchedulerRetriesAndNeutralizes(t *testing.T) {
	fake := &fakeInteractionAdapter{observations: []adapter.Observation{{ID: "obs-1"}, {ID: "obs-2", Terminal: true}}, failures: 1}
	scheduler, _ := NewInteractionScheduler(fake, InteractionDecisionSelectorFunc(selectAdvance), InteractionSchedulerOptions{})
	record, err := scheduler.Run(context.Background(), InteractionRunRequest{RunID: "run-retry", Plan: InteractionPlan{MaxActions: 1, MaxRetries: 1}})
	if err != nil || record.Status != InteractionCompleted || fake.executions != 2 {
		t.Fatalf("expected bounded retry completion, record=%+v err=%v", record, err)
	}
	if len(fake.neutralized) != 1 {
		t.Fatalf("expected one neutralization before retry, got %v", fake.neutralized)
	}
}

func TestInteractionSchedulerDenialStopsBeforeExecution(t *testing.T) {
	fake := &fakeInteractionAdapter{observations: []adapter.Observation{{ID: "obs-1"}}}
	policy := InteractionPolicyFunc(func(context.Context, string, adapter.Action, adapter.Observation) (InteractionAuthorization, string, error) {
		return InteractionDenied, "policy denied", nil
	})
	scheduler, _ := NewInteractionScheduler(fake, InteractionDecisionSelectorFunc(selectAdvance), InteractionSchedulerOptions{Policy: policy})
	record, err := scheduler.Run(context.Background(), InteractionRunRequest{RunID: "run-denied", Plan: InteractionPlan{MaxActions: 1}})
	if err == nil || !strings.Contains(err.Error(), "denied") || record.Status != InteractionFailed || fake.executions != 0 {
		t.Fatalf("expected policy denial before execution, record=%+v err=%v", record, err)
	}
}

func TestInteractionSchedulerDeadmanNeutralizes(t *testing.T) {
	fake := &fakeInteractionAdapter{observations: []adapter.Observation{{ID: "obs-1"}}, block: true}
	// Override Execute through a small wrapper so the fake can block without
	// changing the legacy Adapter contract used by the other scenarios.
	blocking := &blockingInteractionAdapter{fakeInteractionAdapter: fake}
	scheduler, _ := NewInteractionScheduler(blocking, InteractionDecisionSelectorFunc(selectAdvance), InteractionSchedulerOptions{})
	record, err := scheduler.Run(context.Background(), InteractionRunRequest{RunID: "run-deadman", Plan: InteractionPlan{MaxActions: 1, Deadman: 10 * time.Millisecond}})
	if err == nil || record.Status != InteractionFailed || len(fake.neutralized) == 0 {
		t.Fatalf("expected deadman failure and neutralization, record=%+v err=%v", record, err)
	}
}

type blockingInteractionAdapter struct{ *fakeInteractionAdapter }

func (a *blockingInteractionAdapter) Execute(ctx context.Context, action string, input map[string]any) (*adapter.Result, error) {
	return a.executeWithContext(ctx, action)
}

func TestJSONInteractionRunStoreRestoresState(t *testing.T) {
	dir := t.TempDir()
	store := JSONInteractionRunStore{Directory: dir}
	record := InteractionRunRecord{RunID: "restart-safe", Adapter: "fake", Status: InteractionPaused, Step: 2, UpdatedAt: time.Now().UTC()}
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), "restart-safe")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RunID != record.RunID || loaded.Step != record.Step || loaded.Status != record.Status {
		t.Fatalf("state was not restored: %+v", loaded)
	}
}
