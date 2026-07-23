package event

// Multi-agent workflow event types extend Prism's canonical event vocabulary.
// They use the existing Event envelope and event store.
const (
	EventMultiAgentRunCreated         = "prism.workflow.multi_agent.run.created"
	EventMultiAgentRunStarted         = "prism.workflow.multi_agent.run.started"
	EventMultiAgentRoleEntered        = "prism.workflow.multi_agent.role.entered"
	EventMultiAgentRoleIterationStart = "prism.workflow.multi_agent.role.iteration.started"
	EventMultiAgentRoleIterationDone  = "prism.workflow.multi_agent.role.iteration.completed"
	EventMultiAgentRoleCompleted      = "prism.workflow.multi_agent.role.completed"
	EventMultiAgentHandoffCreated     = "prism.workflow.multi_agent.handoff.created"
	EventMultiAgentTransitionSelected = "prism.workflow.multi_agent.transition.selected"
	EventMultiAgentLoopTraversal      = "prism.workflow.multi_agent.loop.traversal.recorded"
	EventMultiAgentBudgetWarning      = "prism.workflow.multi_agent.budget.warning"
	EventMultiAgentBudgetExhausted    = "prism.workflow.multi_agent.budget.exhausted"
	EventMultiAgentRunPaused          = "prism.workflow.multi_agent.run.paused"
	EventMultiAgentRunResumed         = "prism.workflow.multi_agent.run.resumed"
	EventMultiAgentRunCompleted       = "prism.workflow.multi_agent.run.completed"
	EventMultiAgentRunFailed          = "prism.workflow.multi_agent.run.failed"
	EventMultiAgentRunCancelled       = "prism.workflow.multi_agent.run.cancelled"
)

// MultiAgentRunEventPayload is shared by run lifecycle events.
type MultiAgentRunEventPayload struct {
	RunID             string `json:"run_id"`
	WorkflowID        string `json:"workflow_id"`
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
	TerminalCondition string `json:"terminal_condition,omitempty"`
}

// MultiAgentRoleEventPayload describes role lifecycle and iteration events.
type MultiAgentRoleEventPayload struct {
	RunID        string      `json:"run_id"`
	WorkflowID   string      `json:"workflow_id"`
	Role         string      `json:"role"`
	Status       string      `json:"status"`
	Visit        int         `json:"visit,omitempty"`
	Iteration    int         `json:"iteration,omitempty"`
	Outcome      string      `json:"outcome,omitempty"`
	TokenUsage   *TokenUsage `json:"token_usage,omitempty"`
	Error        string      `json:"error,omitempty"`
	ArtifactURIs []string    `json:"artifact_uris,omitempty"`
}

// MultiAgentHandoffEventPayload identifies a structured role handoff.
type MultiAgentHandoffEventPayload struct {
	RunID           string `json:"run_id"`
	WorkflowID      string `json:"workflow_id"`
	HandoffID       string `json:"handoff_id"`
	SourceRole      string `json:"source_role"`
	DestinationRole string `json:"destination_role"`
	TaskRef         string `json:"task_ref"`
	Outcome         string `json:"outcome"`
}

// MultiAgentTransitionEventPayload records a supervisor-selected transition.
type MultiAgentTransitionEventPayload struct {
	RunID             string `json:"run_id"`
	WorkflowID        string `json:"workflow_id"`
	SourceRole        string `json:"source_role"`
	DestinationRole   string `json:"destination_role,omitempty"`
	Outcome           string `json:"outcome"`
	TerminalCondition string `json:"terminal_condition,omitempty"`
	TransitionCount   int    `json:"transition_count"`
}

// MultiAgentBudgetEventPayload records warnings and terminal exhaustion.
type MultiAgentBudgetEventPayload struct {
	RunID      string `json:"run_id"`
	WorkflowID string `json:"workflow_id"`
	Budget     string `json:"budget"`
	Used       int    `json:"used"`
	Limit      int    `json:"limit"`
	Role       string `json:"role,omitempty"`
	Reason     string `json:"reason,omitempty"`
}
