package multiagent

import (
	"context"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/adapter"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/retry"
)

func TestInteractionPlanForNodeUsesExecutionMetadata(t *testing.T) {
	node := CompiledNode{
		RoleConfig: RoleConfig{
			Retry:      retry.RetryConfig{MaxRetries: 2},
			TimeBudget: 45 * time.Second,
		},
		Execution: &SchemaExecutionPolicy{
			Lane:         string(LaneStrategic),
			Cadence:      "250ms",
			Interrupts:   []string{"danger"},
			MaxActions:   interactionSchemaLimit(3),
			Verification: []string{"state_changed"},
		},
	}
	plan := interactionPlanForNode(node)
	if len(plan.Phases) != 1 || plan.Phases[0].Lane != LaneStrategic {
		t.Fatalf("phases = %#v", plan.Phases)
	}
	phase := plan.Phases[0]
	if phase.Cadence != 250*time.Millisecond || phase.MaxActions != 3 || plan.MaxActions != 3 {
		t.Fatalf("limits = phase %#v plan %#v", phase, plan)
	}
	if plan.MaxRetries != 2 || plan.RunDeadline != 45*time.Second || plan.Deadman != 45*time.Second {
		t.Fatalf("role limits = %#v", plan)
	}
}

func TestInteractionPlanForNodeDefaultsToOneAction(t *testing.T) {
	plan := interactionPlanForNode(CompiledNode{Execution: &SchemaExecutionPolicy{Lane: string(LaneReflex)}})
	if plan.MaxActions != 1 || len(plan.Phases) != 1 || plan.Phases[0].MaxActions != 1 {
		t.Fatalf("plan = %#v, want one bounded action", plan)
	}
}

func TestDurableRuntimeRunsInteractionForAuthoredExecutionNode(t *testing.T) {
	definition := baseDef()
	definition.Spec.Nodes[0].Execution = &SchemaExecutionPolicy{Lane: string(LaneReflex), MaxActions: interactionSchemaLimit(1)}
	graph, _, err := Compile(definition, nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	env := newDurableTestEnvironment(t)
	adapter := &fakeInteractionAdapter{observations: []adapter.Observation{{ID: "obs-terminal", Terminal: true}}}
	scheduler, err := NewInteractionScheduler(adapter, nil, InteractionSchedulerOptions{Store: NewMemoryInteractionRunStore()})
	if err != nil {
		t.Fatalf("NewInteractionScheduler() error = %v", err)
	}
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		Role("start"): {{result: RoleRunResult{Outcome: TransitionOutcome("ok"), LocalIterations: 1}}},
	}}
	runtime, err := NewDurableRuntime(graph, runner, env.store, env.claimer, env.events, DurableRuntimeOptions{Interaction: scheduler})
	if err != nil {
		t.Fatalf("NewDurableRuntime() error = %v", err)
	}
	state, err := runtime.Run(context.Background(), RunRequest{RunID: "run-interaction-graph", Task: TaskReference{ID: "task", Description: "exercise adapter"}})
	if err != nil || state.Status != RunStatusCompleted {
		t.Fatalf("Run() state=%#v err=%v", state, err)
	}
	if events := queryEventsForTest(t, env.events, state.RunID, event.EventInteractionObserved); len(events) != 1 {
		t.Fatalf("interaction observed events = %d, want 1", len(events))
	}
}

func interactionSchemaLimit(value int) *SchemaLimit {
	limit := SchemaLimit(value)
	return &limit
}
