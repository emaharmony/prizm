// Package runstatus provides a projection that tracks run lifecycle state.
//
// It subscribes to prizm.task.* events and produces a snapshot showing
// the current status of the run, along with metadata like task description,
// project, agent, timing, and error information.
//
// Example snapshot:
//
//	{
//	  "status": "completed",
//	  "task": "Test README accuracy",
//	  "project": "prizm",
//	  "agent": "lumi",
//	  "started_at": "2026-05-12T23:26:25.027941Z",
//	  "completed_at": "2026-05-12T23:26:25.123456Z",
//	  "duration_ms": 96,
//	  "error": ""
//	}
package runstatus

import (
	"fmt"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/projection"
)

// RunStatusProjection tracks the lifecycle state of a run.
//
// It reads task lifecycle events and produces a snapshot showing:
//   - status: current lifecycle status (created, running, completed, failed)
//   - task: the task description
//   - project: the project name
//   - agent: the agent name
//   - started_at: when the run started (ISO 8601)
//   - completed_at: when the run finished (if applicable)
//   - duration_ms: how long the run took (in milliseconds)
//   - error: error message if the run failed
type RunStatusProjection struct {
	status      string
	task        string
	project     string
	agent       string
	startedAt   string
	completedAt string
	durationMs  int64
	errorMsg    string
}

// New creates a new RunStatusProjection.
func New() *RunStatusProjection {
	return &RunStatusProjection{
		status: "unknown", // unknown until we see an event
	}
}

// Name returns the projection name. This becomes the filename: projections/run_status.json
func (r *RunStatusProjection) Name() string {
	return "run_status"
}

// Subscribe returns the event types this projection cares about.
// We subscribe to all task lifecycle events to track run status.
func (r *RunStatusProjection) Subscribe() []string {
	return []string{
		event.V1EventTypes.TaskCreated,
		event.V1EventTypes.TaskStarted,
		event.V1EventTypes.TaskCompleted,
		event.V1EventTypes.TaskFailed,
	}
}

// Apply processes a single event and updates the projection state.
// Each task lifecycle event transitions the status:
//
//	task.created   → status: "created"
//	task.started   → status: "running"
//	task.completed → status: "completed"
//	task.failed    → status: "failed"
//
// Apply is idempotent: applying the same event twice produces the same state.
func (r *RunStatusProjection) Apply(evt event.Event) error {
	switch evt.Type {
	case event.V1EventTypes.TaskCreated:
		r.status = "created"
		r.extractMetadata(evt)
	case event.V1EventTypes.TaskStarted:
		r.status = "running"
		r.startedAt = evt.Timestamp
		r.extractMetadata(evt)
	case event.V1EventTypes.TaskCompleted:
		r.status = "completed"
		r.completedAt = evt.Timestamp
		r.extractMetadata(evt)
		// Calculate duration if we have start time
		r.computeDuration()
	case event.V1EventTypes.TaskFailed:
		r.status = "failed"
		r.completedAt = evt.Timestamp
		if msg, ok := evt.Payload["error"].(string); ok {
			r.errorMsg = msg
		}
		r.extractMetadata(evt)
		r.computeDuration()
	default:
		// Ignore events we don't care about
	}
	return nil
}

// Snapshot returns the current projection state as a serializable map.
func (r *RunStatusProjection) Snapshot() map[string]any {
	return map[string]any{
		"status":       r.status,
		"task":         r.task,
		"project":      r.project,
		"agent":        r.agent,
		"started_at":   r.startedAt,
		"completed_at": r.completedAt,
		"duration_ms":  r.durationMs,
		"error":        r.errorMsg,
	}
}

// extractMetadata pulls task, project, and agent from event payload or metadata.
func (r *RunStatusProjection) extractMetadata(evt event.Event) {
	if task, ok := evt.Payload["task"].(string); ok && task != "" {
		r.task = task
	}
	if project, ok := evt.Payload["project"].(string); ok && project != "" {
		r.project = project
	}
	if agent, ok := evt.Payload["agent"].(string); ok && agent != "" {
		r.agent = agent
	}
	// Also check metadata fields
	if evt.Metadata.Project != "" {
		r.project = evt.Metadata.Project
	}
	if evt.Metadata.Agent != "" {
		r.agent = evt.Metadata.Agent
	}
}

// computeDuration is a placeholder for future timestamp parsing.
// Currently, duration_ms is set from the run orchestrator's summary.
// Projections can be extended to parse ISO 8601 timestamps.
func (r *RunStatusProjection) computeDuration() {
	// Duration calculation from timestamps is not implemented yet.
	// The run orchestrator sets duration in summary.json.
	// Projections focus on state, not timing computations.
}

// compile-time check
var _ projection.Projection = (*RunStatusProjection)(nil)

// ValidateName checks that a projection name is valid.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("projection name cannot be empty")
	}
	// Must be lowercase alphanumeric with hyphens only
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return fmt.Errorf("projection name %q must be lowercase alphanumeric with hyphens only", name)
		}
	}
	return nil
}
