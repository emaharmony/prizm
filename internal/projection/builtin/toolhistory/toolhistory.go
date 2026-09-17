// Package toolhistory provides a projection that tracks tool call history.
//
// It subscribes to prizm.tool.* events and produces a snapshot showing
// all tool calls in a run, their statuses, policy decisions, and results,
// along with an aggregate summary.
//
// Example snapshot:
//
//	{
//	  "calls": [
//	    {
//	      "tool_name": "echo",
//	      "status": "completed",
//	      "policy_decision": "allowed",
//	      "event_id": "evt_01K..."
//	    }
//	  ],
//	  "summary": {
//	    "total": 1,
//	    "approved": 1,
//	    "denied": 0,
//	    "succeeded": 1,
//	    "failed": 0
//	  }
//	}
package toolhistory

import (
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/projection"
)

// ToolCallEntry represents a single tool call in the projection state.
type ToolCallEntry struct {
	ToolName       string `json:"tool_name"`
	Status         string `json:"status"`
	PolicyDecision string `json:"policy_decision,omitempty"`
	Result         string `json:"result,omitempty"`
	Error          string `json:"error,omitempty"`
	EventID        string `json:"event_id"`
}

// ToolHistoryProjection tracks tool call history.
//
// It reads tool lifecycle events and produces a snapshot showing:
//   - calls: ordered list of tool calls with status, policy, result
//   - summary: aggregate counts (total, approved, denied, succeeded, failed)
//
// The status transitions are:
//
//	tool.requested → new entry, status: "requested"
//	tool.approved  → policy approved, status: "approved"
//	tool.denied     → policy denied, status: "denied"
//	tool.started   → execution started, status: "started"
//	tool.completed → execution succeeded, status: "completed"
//	tool.failed    → execution failed, status: "failed"
type ToolHistoryProjection struct {
	calls []ToolCallEntry
}

// New creates a new ToolHistoryProjection.
func New() *ToolHistoryProjection {
	return &ToolHistoryProjection{
		calls: make([]ToolCallEntry, 0),
	}
}

// Name returns the projection name.
func (t *ToolHistoryProjection) Name() string {
	return "tool_history"
}

// Subscribe returns the event types this projection cares about.
func (t *ToolHistoryProjection) Subscribe() []string {
	return []string{
		event.V3EventTypes.ToolRequested,
		event.V3EventTypes.ToolApproved,
		event.V3EventTypes.ToolDenied,
		event.V3EventTypes.ToolStarted,
		event.V3EventTypes.ToolCompleted,
		event.V3EventTypes.ToolFailed,
	}
}

// Apply processes a single tool event and updates the projection state.
func (t *ToolHistoryProjection) Apply(evt event.Event) error {
	switch evt.Type {
	case event.V3EventTypes.ToolRequested:
		entry := ToolCallEntry{
			ToolName:       strFromPayload(evt, "tool_name"),
			Status:         "requested",
			PolicyDecision: strFromPayload(evt, "policy_decision"),
			EventID:        evt.ID,
		}
		t.calls = append(t.calls, entry)

	case event.V3EventTypes.ToolApproved:
		t.updateLastCall(evt, "approved", func(e *ToolCallEntry) {
			e.PolicyDecision = "allowed"
		})

	case event.V3EventTypes.ToolDenied:
		t.updateLastCall(evt, "denied", func(e *ToolCallEntry) {
			e.PolicyDecision = "denied"
		})

	case event.V3EventTypes.ToolStarted:
		t.updateLastCall(evt, "started", nil)

	case event.V3EventTypes.ToolCompleted:
		t.updateLastCall(evt, "completed", func(e *ToolCallEntry) {
			e.Result = strFromPayload(evt, "result")
		})

	case event.V3EventTypes.ToolFailed:
		t.updateLastCall(evt, "failed", func(e *ToolCallEntry) {
			e.Error = strFromPayload(evt, "error")
		})
	}

	return nil
}

// Snapshot returns the current projection state as a serializable map.
func (t *ToolHistoryProjection) Snapshot() map[string]any {
	summary := map[string]int{
		"total":     len(t.calls),
		"approved":  0,
		"denied":    0,
		"succeeded": 0,
		"failed":    0,
	}

	callsMap := make([]map[string]any, len(t.calls))
	for i, call := range t.calls {
		callsMap[i] = map[string]any{
			"tool_name":       call.ToolName,
			"status":          call.Status,
			"policy_decision": call.PolicyDecision,
			"result":          call.Result,
			"error":           call.Error,
			"event_id":        call.EventID,
		}

		// Update summary counts
		switch call.Status {
		case "approved", "completed":
			summary["approved"]++
			if call.Status == "completed" {
				summary["succeeded"]++
			}
		case "denied":
			summary["denied"]++
		case "failed":
			summary["failed"]++
		}
	}

	return map[string]any{
		"calls":   callsMap,
		"summary": summary,
	}
}

// updateLastCall finds the most recent call for a tool and updates it.
// Tool events are paired: requested → approved/denied → started → completed/failed.
// We find the last call matching the tool_name from the event.
func (t *ToolHistoryProjection) updateLastCall(evt event.Event, status string, extra func(*ToolCallEntry)) {
	toolName := strFromPayload(evt, "tool_name")

	// Find the last call matching this tool name
	for i := len(t.calls) - 1; i >= 0; i-- {
		if t.calls[i].ToolName == toolName {
			t.calls[i].Status = status
			if extra != nil {
				extra(&t.calls[i])
			}
			return
		}
	}
}

// strFromPayload extracts a string value from an event payload.
func strFromPayload(evt event.Event, key string) string {
	if v, ok := evt.Payload[key].(string); ok {
		return v
	}
	return ""
}

// compile-time check
var _ projection.Projection = (*ToolHistoryProjection)(nil)
