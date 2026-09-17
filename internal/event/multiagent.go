package event

// Multi-agent workflow event types extend Prizm's canonical event vocabulary.
// They use the existing Event envelope and event store.
const (
	EventMultiAgentRunCreated         = "prizm.workflow.multi_agent.run.created"
	EventMultiAgentRunStarted         = "prizm.workflow.multi_agent.run.started"
	EventMultiAgentRoleEntered        = "prizm.workflow.multi_agent.role.entered"
	EventMultiAgentRoleIterationStart = "prizm.workflow.multi_agent.role.iteration.started"
	EventMultiAgentRoleIterationDone  = "prizm.workflow.multi_agent.role.iteration.completed"
	EventMultiAgentRoleCompleted      = "prizm.workflow.multi_agent.role.completed"
	EventMultiAgentHandoffCreated     = "prizm.workflow.multi_agent.handoff.created"
	EventMultiAgentTransitionSelected = "prizm.workflow.multi_agent.transition.selected"
	EventMultiAgentLoopTraversal      = "prizm.workflow.multi_agent.loop.traversal.recorded"
	EventMultiAgentBudgetWarning      = "prizm.workflow.multi_agent.budget.warning"
	EventMultiAgentBudgetExhausted    = "prizm.workflow.multi_agent.budget.exhausted"
	EventMultiAgentRunPaused          = "prizm.workflow.multi_agent.run.paused"
	EventMultiAgentRunResumed         = "prizm.workflow.multi_agent.run.resumed"
	EventMultiAgentRunCompleted       = "prizm.workflow.multi_agent.run.completed"
	EventMultiAgentRunFailed          = "prizm.workflow.multi_agent.run.failed"
	EventMultiAgentRunCancelled       = "prizm.workflow.multi_agent.run.cancelled"
	EventMultiAgentRecoveryStarted    = "prizm.workflow.multi_agent.recovery.started"
	EventMultiAgentRecoveryCompleted  = "prizm.workflow.multi_agent.recovery.completed"
	EventMultiAgentRecoveryFailed     = "prizm.workflow.multi_agent.recovery.failed"
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
	RunID            string      `json:"run_id"`
	WorkflowID       string      `json:"workflow_id"`
	Role             string      `json:"role"`
	Status           string      `json:"status"`
	Visit            int         `json:"visit,omitempty"`
	Iteration        int         `json:"iteration,omitempty"`
	Outcome          string      `json:"outcome,omitempty"`
	TokenUsage       *TokenUsage `json:"token_usage,omitempty"`
	Error            string      `json:"error,omitempty"`
	ArtifactURIs     []string    `json:"artifact_uris,omitempty"`
	AgentRef         string      `json:"agent_ref,omitempty"`
	Provider         string      `json:"provider,omitempty"`
	Model            string      `json:"model,omitempty"`
	DurationMs       int64       `json:"duration_ms,omitempty"`
	ToolCalls        int         `json:"tool_calls,omitempty"`
	DeniedToolCalls  int         `json:"denied_tool_calls,omitempty"`
	ValidationStatus string      `json:"validation_status,omitempty"`
	ApprovalStatus   string      `json:"approval_status,omitempty"`
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
