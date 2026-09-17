// Package projection provides state projections over Prizm event streams.
package projection

// V10 Projection Event Types
// These events are emitted when projections are computed.
const (
	EventTypeProjectionStarted   = "prizm.projection.started"
	EventTypeProjectionCompleted = "prizm.projection.completed"
	EventTypeProjectionFailed    = "prizm.projection.failed"
)
