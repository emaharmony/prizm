package v2

// V2 event type constants for observability.
// Every significant state change emits an event that can be consumed
// by dashboards, monitors, and other agents.
const (
	EventPhaseEntered       = "workflow.phase.entered"
	EventPhaseIteration     = "workflow.phase.iteration"
	EventPhaseGateCheck     = "workflow.phase.gate_check"
	EventPhaseGatePassed    = "workflow.phase.gate_passed"
	EventPhaseGateFailed    = "workflow.phase.gate_failed"
	EventPhaseExited        = "workflow.phase.exited"
	EventWorkflowStarted    = "workflow.started"
	EventWorkflowPaused     = "workflow.paused.waiting_approval"
	EventWorkflowResumed    = "workflow.resumed"
	EventWorkflowCompleted  = "workflow.completed"
	EventWorkflowBlocked    = "workflow.blocked"
	EventTaskDelegated      = "workflow.task.delegated"
	EventTaskCompleted      = "workflow.task.completed"
	EventTaskFailed         = "workflow.task.failed"
	EventTaskTimedOut       = "workflow.task.timed_out"
	EventAgentCreated       = "workflow.agent.created"
	EventFeedbackRequested  = "workflow.feedback.requested"
	EventFeedbackReceived   = "workflow.feedback.received"
	EventAssumptionAdded    = "workflow.assumption.added"
	EventAssumptionResolved = "workflow.assumption.resolved"
	EventConfidenceUpdated  = "workflow.confidence.updated"
	EventPlanTaskAdded      = "workflow.plan.task_added"
)

// LogEmitter is a simple EventEmitter that logs events.
type LogEmitter struct{}

func (l *LogEmitter) Emit(eventType string, payload map[string]any) {
	// In production, this would publish to NATS and log
	// For now, just log to stdout
}

// NATSEmitter publishes engine events to NATS so the dashboard SSE stream and
// other observers can follow phase transitions, gate checks, pauses, and
// iterate loop-backs in real time.
type NATSEmitter struct {
	pub     *NATSPublisher
	subject string // base subject, e.g. "prizm.workflow"
}

// NewNATSEmitter builds a NATS-backed event emitter. A nil publisher yields a
// no-op emitter so callers without NATS still run.
func NewNATSEmitter(pub *NATSPublisher, subject string) *NATSEmitter {
	if subject == "" {
		subject = "prizm.workflow"
	}
	return &NATSEmitter{pub: pub, subject: subject}
}

func (e *NATSEmitter) Emit(eventType string, payload map[string]any) {
	if e == nil || e.pub == nil {
		return
	}
	_ = e.pub.PublishEvent(e.subject+"."+eventType, eventType, payload)
}
