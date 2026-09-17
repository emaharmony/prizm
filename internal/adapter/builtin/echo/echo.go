// Package echo provides a minimal built-in adapter for testing and demos.
// It echoes input back as output, similar to the echo tool.
package echo

import (
	"context"
	"fmt"

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

// Health reports that the echo adapter is always ready.
func (e *EchoAdapter) Health(ctx context.Context) (*adapter.HealthResult, error) {
	return &adapter.HealthResult{
		Ready:   true,
		Message: "echo adapter is always ready",
	}, nil
}
