package api

import "context"

// MultiAgentController is the narrow, transport-facing seam the mutating
// multi-agent run routes (POST .../resume, .../cancel, .../pause) call
// through. It intentionally knows nothing about agents, providers, tools, or
// how a live runtime is assembled — that composition lives in cmd/prism-cli,
// which implements this interface by reusing its existing,
// already-tested run-opening helpers (openLiveReferenceRuntime for Resume,
// RunLocator.OpenInspection for Cancel/Pause). Keeping the interface this
// narrow lets the API package depend only on the shape of the operation, not
// on how it is wired.
type MultiAgentController interface {
	// Resume re-invokes the role runner for a paused/waiting run. Because a
	// resume can run for a long time (it may execute one or more further
	// roles), implementations are expected to return once the resume has
	// been durably kicked off rather than blocking for the full run — the
	// caller observes progress and completion via the existing SSE stream.
	Resume(ctx context.Context, runID string) error
	// Cancel persists an operator cancellation request. An active owner
	// observes it at the next role boundary; an idle run becomes terminal
	// immediately.
	Cancel(ctx context.Context, runID, reason string) error
	// Pause persists an operator pause request. Unlike Cancel, a paused run
	// never becomes terminal and remains resumable.
	Pause(ctx context.Context, runID, reason string) error
}
