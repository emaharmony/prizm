// Package echo provides a minimal built-in adapter for testing and demos.
// It echoes input back as output, similar to the echo tool.
package echo

import (
	"context"
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/adapter"
)

// EchoAdapter is a minimal adapter that echoes input back as output.
type EchoAdapter struct{}

// Name returns the adapter name.
func (e *EchoAdapter) Name() string { return "echo" }

// Version returns the adapter version.
func (e *EchoAdapter) Version() string { return "1.0" }

// Capabilities returns what this adapter can do.
func (e *EchoAdapter) Capabilities() []adapter.Capability {
	return []adapter.Capability{
		{
			Action:      "echo",
			Description: "Echo input back as output",
		},
	}
}

// Execute runs the echo action.
func (e *EchoAdapter) Execute(ctx context.Context, action string, input map[string]any) (*adapter.Result, error) {
	switch action {
	case "echo", "":
		return &adapter.Result{
			Success: true,
			Output:  input,
		}, nil
	default:
		return nil, fmt.Errorf("echo adapter: unknown action %q", action)
	}
}

// Observe makes the echo adapter usable as a deterministic interaction
// endpoint in graph runs. The observation is intentionally opaque and
// stateless so it remains useful in tests and demos.
func (e *EchoAdapter) Observe(ctx context.Context, _ adapter.ObservationRequest) (*adapter.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &adapter.Observation{
		ID:         fmt.Sprintf("echo-%d", now.UnixNano()),
		CapturedAt: now,
		Data:       map[string]any{"adapter": e.Name(), "ready": true},
	}, nil
}

// LegalActions exposes the echo capability as the complete legal action set.
func (e *EchoAdapter) LegalActions(ctx context.Context, _ *adapter.Observation) ([]adapter.Capability, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return e.Capabilities(), nil
}

// ValidateAction rejects actions before they reach Execute.
func (e *EchoAdapter) ValidateAction(ctx context.Context, action string, _ map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if action != "echo" {
		return fmt.Errorf("echo adapter: action %q is not legal", action)
	}
	return nil
}

// Neutralize is a no-op because echo has no external side effects.
func (e *EchoAdapter) Neutralize(ctx context.Context, _ string) error {
	return ctx.Err()
}

// Health reports that the echo adapter is always ready.
func (e *EchoAdapter) Health(ctx context.Context) (*adapter.HealthResult, error) {
	return &adapter.HealthResult{
		Ready:   true,
		Message: "echo adapter is always ready",
	}, nil
}
