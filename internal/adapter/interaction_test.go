package adapter

import (
	"context"
	"testing"
	"time"
)

type fakeInteractionAdapter struct{}

func (fakeInteractionAdapter) Name() string    { return "fake-environment" }
func (fakeInteractionAdapter) Version() string { return "1.0.0" }
func (fakeInteractionAdapter) Capabilities() []Capability {
	return []Capability{{Action: "advance"}}
}
func (fakeInteractionAdapter) Execute(context.Context, string, map[string]any) (*Result, error) {
	return &Result{Success: true}, nil
}
func (fakeInteractionAdapter) Health(context.Context) (*HealthResult, error) {
	return &HealthResult{Ready: true}, nil
}
func (fakeInteractionAdapter) Observe(context.Context, ObservationRequest) (*Observation, error) {
	return &Observation{CapturedAt: time.Now().UTC(), Data: map[string]any{"step": 1}}, nil
}
func (fakeInteractionAdapter) LegalActions(context.Context, *Observation) ([]Capability, error) {
	return []Capability{{Action: "advance"}}, nil
}
func (fakeInteractionAdapter) ValidateAction(context.Context, string, map[string]any) error {
	return nil
}
func (fakeInteractionAdapter) Neutralize(context.Context, string) error { return nil }

func TestInteractionAdapterContract(t *testing.T) {
	var adapter InteractionAdapter = fakeInteractionAdapter{}
	observation, err := adapter.Observe(context.Background(), ObservationRequest{})
	if err != nil || observation.Data["step"] != 1 {
		t.Fatalf("observation = %#v, err = %v", observation, err)
	}
	actions, err := adapter.LegalActions(context.Background(), observation)
	if err != nil || len(actions) != 1 || actions[0].Action != "advance" {
		t.Fatalf("actions = %#v, err = %v", actions, err)
	}
	if err := adapter.ValidateAction(context.Background(), actions[0].Action, nil); err != nil {
		t.Fatalf("validate action: %v", err)
	}
	if err := adapter.Neutralize(context.Background(), "test"); err != nil {
		t.Fatalf("neutralize: %v", err)
	}
}
