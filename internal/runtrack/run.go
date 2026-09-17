// Package runtrack provides lightweight run tracking for the V20 conversation pipeline.
// A "Run" represents a single LLM invocation within a Discord conversation.
// It tracks the lifecycle of an LLM call: creation, execution, completion/error.
//
// This is a minimal V20 implementation. V21 will extend this with:
//   - NATS event publishing
//   - Multi-step orchestration (agent delegation)
//   - Cost budgeting and enforcement
//   - /cancel command integration
package runtrack

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/rs/xid"
)

// DefaultTimeout is the maximum duration for a single LLM call.
const DefaultTimeout = 20 * time.Minute

// DefaultRunResponseTokens is the default response token cap for a single tracked LLM call.
const DefaultRunResponseTokens = 4096

// DefaultMaxTokens is kept as a compatibility alias for older callers.
const DefaultMaxTokens = DefaultRunResponseTokens

// Run represents a single LLM invocation within a conversation.
type Run struct {
	ID        string
	AgentID   string
	SessionID string
	StartedAt time.Time
	Deadline  time.Time
	MaxTokens int
	Model     string
	Provider  string

	// Cancel allows the caller to abort this run.
	// V21: Wire to /cancel Discord command.
	Cancel context.CancelFunc
}

// EventLogger writes structured run events as JSON to stdout.
// In V21, these same event types will be published to NATS.
type EventLogger struct{}

// Log emits a structured event to stdout as JSON.
func (e *EventLogger) Log(eventType, runID, agentID string, metadata map[string]any) {
	entry := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"event":     eventType,
		"run_id":    runID,
		"agent_id":  agentID,
	}
	for k, v := range metadata {
		entry[k] = v
	}
	// Simple structured log — V21 will publish to NATS with the same event types.
	// These map 1:1 to NATS subjects: run.started → prizm.run.started, etc.
	log.Printf("[RUN] event=%s run=%s agent=%s", eventType, runID, agentID)
}

// CancelRegistry maps session IDs to their cancel functions.
// This allows /cancel commands (V21) to abort in-flight LLM calls.
type CancelRegistry struct {
	mu      sync.RWMutex
	cancels map[string]context.CancelFunc // sessionID → cancel
}

// NewCancelRegistry creates a new cancel registry.
func NewCancelRegistry() *CancelRegistry {
	return &CancelRegistry{
		cancels: make(map[string]context.CancelFunc),
	}
}

// Register stores a cancel function for a session.
func (r *CancelRegistry) Register(sessionID string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels[sessionID] = cancel
}

// Unregister removes a session's cancel function.
func (r *CancelRegistry) Unregister(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancels, sessionID)
}

// Cancel aborts an in-flight LLM call for a session. Returns true if a cancel was found.
func (r *CancelRegistry) Cancel(sessionID string) bool {
	r.mu.RLock()
	cancel, ok := r.cancels[sessionID]
	r.mu.RUnlock()
	if ok {
		cancel()
	}
	return ok
}

// NewRun creates a new Run with a unique ID and the default timeout deadline.
func NewRun(agentID, sessionID, model, provider string) *Run {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	// Detach the context — the caller uses cancel directly.
	// The context is used inside the LLM call, not stored on the Run.
	_ = ctx

	now := time.Now()
	return &Run{
		ID:        xid.New().String(),
		AgentID:   agentID,
		SessionID: sessionID,
		StartedAt: now,
		Deadline:  now.Add(DefaultTimeout),
		MaxTokens: DefaultRunResponseTokens,
		Model:     model,
		Provider:  provider,
		Cancel:    cancel,
	}
}

// NewRunWithContext creates a Run with a custom context (for testing or timeout overrides).
func NewRunWithContext(ctx context.Context, agentID, sessionID, model, provider string) *Run {
	now := time.Now()
	return &Run{
		ID:        xid.New().String(),
		AgentID:   agentID,
		SessionID: sessionID,
		StartedAt: now,
		Deadline:  now.Add(DefaultTimeout),
		MaxTokens: DefaultRunResponseTokens,
		Model:     model,
		Provider:  provider,
		Cancel:    func() {},
	}
}

// Elapsed returns the time since the run started.
func (r *Run) Elapsed() time.Duration {
	return time.Since(r.StartedAt)
}

// String returns a human-readable summary of the run.
func (r *Run) String() string {
	return fmt.Sprintf("Run(id=%s agent=%s model=%s elapsed=%s)",
		r.ID, r.AgentID, r.Model, r.Elapsed().Round(time.Millisecond))
}
