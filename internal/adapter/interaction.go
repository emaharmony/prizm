package adapter

import (
	"context"
	"time"
)

// ObservationRequest controls an adapter observation without exposing the
// adapter's domain model to the runtime.
type ObservationRequest struct {
	// Since is an optional cursor for adapters that can return incremental state.
	Since time.Time `json:"since,omitempty"`
	// IncludeRaw permits callers to request the adapter's raw capture when it is
	// useful for verification or replay. Adapters may ignore it.
	IncludeRaw bool `json:"include_raw,omitempty"`
}

// Observation is a transport-neutral snapshot. Data is intentionally opaque:
// the adapter owns its schema and the workflow only records and routes it.
type Observation struct {
	ID         string         `json:"id,omitempty"`
	CapturedAt time.Time      `json:"captured_at"`
	Data       map[string]any `json:"data,omitempty"`
	Raw        []byte         `json:"raw,omitempty"`
	Terminal   bool           `json:"terminal,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// ObservationProvider is an optional extension for adapters that expose
// environment state to a state-machine run.
type ObservationProvider interface {
	Observe(context.Context, ObservationRequest) (*Observation, error)
}

// LegalActionProvider returns the actions currently legal for an observation.
// It is separate from Capabilities because capabilities are static while legal
// actions may depend on the current environment state.
type LegalActionProvider interface {
	LegalActions(context.Context, *Observation) ([]Capability, error)
}

// ActionValidator is an optional fail-closed validation seam before Execute.
type ActionValidator interface {
	ValidateAction(context.Context, string, map[string]any) error
}

// Neutralizer is implemented by adapters that can safely return their
// environment to a neutral state after timeout, cancellation, or error.
type Neutralizer interface {
	Neutralize(context.Context, string) error
}

// InteractionAdapter is the complete environment seam used by adaptive
// workflows. Existing Adapter implementations remain valid and can opt into
// only the extensions they support.
type InteractionAdapter interface {
	Adapter
	ObservationProvider
	LegalActionProvider
	ActionValidator
	Neutralizer
}
