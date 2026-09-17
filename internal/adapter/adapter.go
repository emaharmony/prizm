// Package adapter provides the Adapter Contract System for Prizm V9.
//
// Adapters are the only way external domain logic enters Prizm. Tools, gates,
// workflows, and policy are all internal. Adapters are the seam between
// Prizm Core and the outside world.
//
// After V9, domain integrations (trading, Roblox, OpenClaw, etc.) plug in
// through adapters instead of forks.
package adapter

import (
	"context"
	"fmt"
	"regexp"
)

// Adapter is the contract that all domain adapters must implement.
// Adapters connect Prizm to external systems (trading, publishing, deployment, etc.)
//
// Adapter names must be alphanumeric + hyphens only (no dots).
// This prevents ambiguity in the policy action format: adapter.<name>.<action>
type Adapter interface {
	// Name returns the adapter's unique identifier (e.g., "trading", "roblox-factory")
	Name() string

	// Version returns the adapter's version string
	Version() string

	// Capabilities returns what this adapter can do
	Capabilities() []Capability

	// Execute runs an action through the adapter
	Execute(ctx context.Context, action string, input map[string]any) (*Result, error)

	// Health checks if the adapter is ready
	Health(ctx context.Context) (*HealthResult, error)
}

// InputValidator is an optional interface adapters can implement for input validation.
// If implemented, the registry calls ValidateInput before Execute.
// This keeps the core Adapter contract at 5 methods while allowing opt-in validation.
type InputValidator interface {
	ValidateInput(action string, input map[string]any) error
}

// Capability describes what an adapter can do.
type Capability struct {
	// Action is the adapter action name (e.g., "evaluate", "execute", "publish")
	Action string `json:"action" yaml:"action"`

	// Description is a human-readable explanation of this capability
	Description string `json:"description" yaml:"description"`

	// InputSchema describes the expected input shape (documentation/metadata)
	InputSchema map[string]any `json:"input_schema,omitempty" yaml:"input_schema,omitempty"`

	// OutputSchema describes the expected output shape (documentation/metadata)
	OutputSchema map[string]any `json:"output_schema,omitempty" yaml:"output_schema,omitempty"`

	// RequiresApproval is an advisory hint — policy is authoritative.
	// If true, this action typically needs human approval, but policy can override.
	RequiresApproval bool `json:"requires_approval,omitempty" yaml:"requires_approval,omitempty"`
}

// Result is the output of an adapter execution.
type Result struct {
	// Success indicates whether the execution succeeded
	Success bool `json:"success"`

	// Output contains the execution result data
	Output map[string]any `json:"output,omitempty"`

	// Error describes what went wrong if Success is false
	Error string `json:"error,omitempty"`

	// Metadata contains additional information about the execution
	Metadata map[string]any `json:"metadata,omitempty"`
}

// HealthResult reports adapter readiness.
type HealthResult struct {
	// Ready indicates whether the adapter is ready to accept requests
	Ready bool `json:"ready"`

	// Message provides additional context about the health status
	Message string `json:"message,omitempty"`

	// Details contains additional health information
	Details map[string]any `json:"details,omitempty"`
}

// adapterNamePattern enforces alphanumeric + hyphens, no dots.
var adapterNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ValidateAdapterName checks that an adapter name conforms to the naming rule:
// alphanumeric + hyphens only, no dots. This prevents ambiguity in the
// policy action format: adapter.<name>.<action>
func ValidateAdapterName(name string) error {
	if name == "" {
		return fmt.Errorf("adapter name cannot be empty")
	}
	if !adapterNamePattern.MatchString(name) {
		return fmt.Errorf("adapter name %q must be lowercase alphanumeric with hyphens only (no dots, no underscores)", name)
	}
	return nil
}
