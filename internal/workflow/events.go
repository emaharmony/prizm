package workflow

// V7EventTypes defines the event types emitted by the workflow runtime.
var V7EventTypes = struct {
	WorkflowStarted       string
	WorkflowCompleted     string
	WorkflowFailed        string
	WorkflowPaused        string
	WorkflowResumed       string
	WorkflowStepStarted   string
	WorkflowStepCompleted string
	WorkflowStepFailed    string
	WorkflowStepSkipped   string
}{
	WorkflowStarted:       "prizm.workflow.started",
	WorkflowCompleted:     "prizm.workflow.completed",
	WorkflowFailed:        "prizm.workflow.failed",
	WorkflowPaused:        "prizm.workflow.paused",
	WorkflowResumed:       "prizm.workflow.resumed",
	WorkflowStepStarted:   "prizm.workflow.step.started",
	WorkflowStepCompleted: "prizm.workflow.step.completed",
	WorkflowStepFailed:    "prizm.workflow.step.failed",
	WorkflowStepSkipped:   "prizm.workflow.step.skipped",
}
