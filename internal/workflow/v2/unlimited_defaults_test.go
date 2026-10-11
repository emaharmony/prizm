package v2

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEngineRunZeroPhaseIterationsMeansUnlimitedAndInterruptible(t *testing.T) {
	cfg := &WorkflowConfig{
		Name: "unlimited-default",
		Phases: []PhaseConfig{{
			Name: "PROBE", Type: "probe", MaxIterations: 0,
			Gate: GateConfig{Type: "assumption_threshold", Threshold: 2},
		}},
	}
	engine := NewEngine(cfg, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	state, err := engine.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want deadline exceeded", err)
	}
	if state.PhaseStates["PROBE"].Iterations == 0 {
		t.Fatal("zero max_iterations skipped the phase instead of running until cancellation")
	}
}
