// Package approval provides a projection that tracks approval state transitions.
//
// It subscribes to prizm.approval.* events and produces a snapshot showing
// all approvals in a run, their current statuses, and aggregate counts.
//
// Example snapshot:
//
//	{
//	  "approvals": {
//	    "appr_01KRC7AQT3WNFK0PV7": {
//	      "status": "approved",
//	      "mutation_type": "write_file",
//	      "target_path": "src/main.go",
//	      "requested_by": "lumi",
//	      "policy_decision": "requires_approval"
//	    }
//	  },
//	  "pending_count": 0,
//	  "total_count": 1
//	}
package approval

import (
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/projection"
)

// ApprovalEntry represents a single approval in the projection state.
type ApprovalEntry struct {
	ApprovalID     string `json:"approval_id"`
	Status         string `json:"status"`
	MutationType   string `json:"mutation_type,omitempty"`
	TargetPath     string `json:"target_path,omitempty"`
	RequestedBy    string `json:"requested_by,omitempty"`
	PolicyDecision string `json:"policy_decision,omitempty"`
}

// ApprovalStateProjection tracks approval state transitions.
//
// It reads approval lifecycle events and produces a snapshot showing:
//   - approvals: map of approval_id → {status, mutation_type, target_path, ...}
//   - pending_count: how many approvals are still pending
//   - total_count: total approvals in this run
//
// The status transitions are:
//
//	approval.requested → status: "pending"
//	approval.granted   → status: "approved"
//	approval.denied     → status: "denied"
//	approval.expired    → status: "expired"
type ApprovalStateProjection struct {
	approvals map[string]*ApprovalEntry
}

// New creates a new ApprovalStateProjection.
func New() *ApprovalStateProjection {
	return &ApprovalStateProjection{
		approvals: make(map[string]*ApprovalEntry),
	}
}

// Name returns the projection name.
func (a *ApprovalStateProjection) Name() string {
	return "approval_state"
}

// Subscribe returns the event types this projection cares about.
func (a *ApprovalStateProjection) Subscribe() []string {
	return []string{
		event.V4EventTypes.ApprovalRequested,
		event.V4EventTypes.ApprovalGranted,
		event.V4EventTypes.ApprovalDenied,
		event.V4EventTypes.ApprovalExpired,
	}
}

// Apply processes a single approval event and updates the projection state.
func (a *ApprovalStateProjection) Apply(evt event.Event) error {
	// Extract approval_id from payload
	approvalID, _ := evt.Payload["approval_id"].(string)
	if approvalID == "" {
		// Try "id" as fallback
		approvalID, _ = evt.Payload["id"].(string)
	}
	if approvalID == "" {
		return nil // skip events without approval_id
	}

	switch evt.Type {
	case event.V4EventTypes.ApprovalRequested:
		entry := &ApprovalEntry{
			ApprovalID:     approvalID,
			Status:         "pending",
			MutationType:   strFromPayload(evt, "mutation_type"),
			TargetPath:     strFromPayload(evt, "target_path"),
			RequestedBy:    strFromPayload(evt, "requested_by"),
			PolicyDecision: strFromPayload(evt, "policy_decision"),
		}
		a.approvals[approvalID] = entry

	case event.V4EventTypes.ApprovalGranted:
		if entry, ok := a.approvals[approvalID]; ok {
			entry.Status = "approved"
		} else {
			a.approvals[approvalID] = &ApprovalEntry{
				ApprovalID: approvalID,
				Status:     "approved",
			}
		}

	case event.V4EventTypes.ApprovalDenied:
		if entry, ok := a.approvals[approvalID]; ok {
			entry.Status = "denied"
		} else {
			a.approvals[approvalID] = &ApprovalEntry{
				ApprovalID: approvalID,
				Status:     "denied",
			}
		}

	case event.V4EventTypes.ApprovalExpired:
		if entry, ok := a.approvals[approvalID]; ok {
			entry.Status = "expired"
		} else {
			a.approvals[approvalID] = &ApprovalEntry{
				ApprovalID: approvalID,
				Status:     "expired",
			}
		}
	}

	return nil
}

// Snapshot returns the current projection state as a serializable map.
func (a *ApprovalStateProjection) Snapshot() map[string]any {
	pendingCount := 0
	for _, entry := range a.approvals {
		if entry.Status == "pending" {
			pendingCount++
		}
	}

	// Convert approvals map to serializable format
	approvalsMap := make(map[string]any, len(a.approvals))
	for id, entry := range a.approvals {
		approvalsMap[id] = map[string]any{
			"approval_id":     entry.ApprovalID,
			"status":          entry.Status,
			"mutation_type":   entry.MutationType,
			"target_path":     entry.TargetPath,
			"requested_by":    entry.RequestedBy,
			"policy_decision": entry.PolicyDecision,
		}
	}

	return map[string]any{
		"approvals":     approvalsMap,
		"pending_count": pendingCount,
		"total_count":   len(a.approvals),
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
var _ projection.Projection = (*ApprovalStateProjection)(nil)
