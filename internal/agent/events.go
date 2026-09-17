// V13 agent event types.
//
// These events track the lifecycle of agent delegation within a run.
// Every delegation (workflow step → agent) emits these events, making
// multi-agent orchestration fully observable and auditable.
//
// Event flow for a delegation:
//  1. agent.delegated  — workflow delegates a subtask to an agent
//  2. (agent executes, may emit tool events via V3)
//  3. agent.completed — agent finishes the subtask successfully
//     OR
//  3. agent.failed     — agent fails the subtask
//
// agent.registered is emitted when an agent is added to the registry
// at the start of a run.
package agent

import "github.com/emaharmony/prizm/internal/event"

// V13 agent event type constants.
const (
	// EventAgentRegistered is emitted when an agent is added to the registry.
	// Payload: agent_name, role, version, capabilities
	EventAgentRegistered = "agent.registered"

	// EventAgentDelegated is emitted when a subtask is delegated to an agent.
	// Payload: agent_name, delegated_by, subtask, step_id
	EventAgentDelegated = "agent.delegated"

	// EventAgentCompleted is emitted when an agent finishes a subtask.
	// Payload: agent_name, subtask, output (summary), duration_ms
	EventAgentCompleted = "agent.completed"

	// EventAgentFailed is emitted when an agent fails a subtask.
	// Payload: agent_name, subtask, error, duration_ms
	EventAgentFailed = "agent.failed"

	// --- V76 Context & Delegation Events ---

	// EventContextRequested is emitted when a conversation turn needs compressed context.
	// Payload: task_description, agent_id
	EventContextRequested = "prizm.context.requested"

	// EventContextBuilt is emitted when the context agent finishes compression.
	// Payload: compressed_text, agent_id, tokens_used
	EventContextBuilt = "prizm.context.built"

	// EventMemoryExtractRequested is emitted when a conversation turn should be auto-extracted.
	// Payload: session_id, agent_id, user_message, agent_response
	EventMemoryExtractRequested = "prizm.memory.extract.requested"

	// EventMemoryExtractCompleted is emitted when memory extraction finishes.
	// Payload: session_id, categories_extracted, files_written
	EventMemoryExtractCompleted = "prizm.memory.extract.completed"

	// EventReviewRequested is emitted when code changes need automatic review.
	// Payload: agent_id, files_changed, task_description
	EventReviewRequested = "prizm.review.requested"

	// EventReviewCompleted is emitted when a code review finishes.
	// Payload: reviewer_id, decision, issues, suggestions
	EventReviewCompleted = "prizm.review.completed"

	// EventArchitectureCheckRequested is emitted for pre-dev architecture validation.
	// Payload: agent_id, task_description, proposed_approach
	EventArchitectureCheckRequested = "prizm.architecture.check.requested"

	// EventArchitectureCheckCompleted is emitted when architecture check finishes.
	// Payload: decision, concerns, approved
	EventArchitectureCheckCompleted = "prizm.architecture.check.completed"

	// EventVulnerabilityCheckRequested is emitted for post-dev security analysis.
	// Payload: agent_id, files_changed, task_description
	EventVulnerabilityCheckRequested = "prizm.vulnerability.check.requested"

	// EventVulnerabilityCheckCompleted is emitted when vulnerability analysis finishes.
	// Payload: reviewer_id, score, issues, recommendations
	EventVulnerabilityCheckCompleted = "prizm.vulnerability.check.completed"
)

// NewAgentRegisteredEvent creates an event for when an agent is registered.
func NewAgentRegisteredEvent(agentName, role, version string, capabilities []string) event.Event {
	return event.NewEvent(EventAgentRegistered, "agent-registry", map[string]any{
		"agent_name":   agentName,
		"role":         role,
		"version":      version,
		"capabilities": capabilities,
	})
}

// NewAgentDelegatedEvent creates an event for when a subtask is delegated.
func NewAgentDelegatedEvent(agentName, delegatedBy, subtask, stepID string) event.Event {
	return event.NewEvent(EventAgentDelegated, "workflow-runner", map[string]any{
		"agent_name":   agentName,
		"delegated_by": delegatedBy,
		"subtask":      subtask,
		"step_id":      stepID,
	})
}

// NewAgentCompletedEvent creates an event for when an agent completes a subtask.
func NewAgentCompletedEvent(agentName, subtask, output string, durationMs int64) event.Event {
	return event.NewEvent(EventAgentCompleted, "agent-"+agentName, map[string]any{
		"agent_name":  agentName,
		"subtask":     subtask,
		"output":      output,
		"duration_ms": durationMs,
	})
}

// NewAgentFailedEvent creates an event for when an agent fails a subtask.
func NewAgentFailedEvent(agentName, subtask, agentErr string, durationMs int64) event.Event {
	return event.NewEvent(EventAgentFailed, "agent-"+agentName, map[string]any{
		"agent_name":  agentName,
		"subtask":     subtask,
		"error":       agentErr,
		"duration_ms": durationMs,
	})
}

// --- V76 Context & Delegation Event Constructors ---

// NewContextRequestedEvent creates an event requesting context compression.
func NewContextRequestedEvent(taskDescription, agentID string) event.Event {
	return event.NewEvent(EventContextRequested, "agent-"+agentID, map[string]any{
		"task_description": taskDescription,
		"agent_id":        agentID,
	})
}

// NewContextBuiltEvent creates an event with compressed context output.
func NewContextBuiltEvent(compressedText, agentID string, tokensUsed int) event.Event {
	return event.NewEvent(EventContextBuilt, "context-agent", map[string]any{
		"compressed_text": compressedText,
		"agent_id":       agentID,
		"tokens_used":     tokensUsed,
	})
}

// NewMemoryExtractRequestedEvent creates an event requesting memory extraction.
func NewMemoryExtractRequestedEvent(sessionID, agentID, userMessage, agentResponse string) event.Event {
	return event.NewEvent(EventMemoryExtractRequested, "agent-"+agentID, map[string]any{
		"session_id":     sessionID,
		"agent_id":       agentID,
		"user_message":    userMessage,
		"agent_response":  agentResponse,
	})
}

// NewMemoryExtractCompletedEvent creates an event signaling extraction completion.
func NewMemoryExtractCompletedEvent(sessionID string, categories []string, filesWritten int) event.Event {
	return event.NewEvent(EventMemoryExtractCompleted, "memory-extractor", map[string]any{
		"session_id":     sessionID,
		"categories_extracted": categories,
		"files_written":  filesWritten,
	})
}

// NewReviewRequestedEvent creates an event requesting automatic code review.
func NewReviewRequestedEvent(agentID string, filesChanged []string, taskDescription string) event.Event {
	return event.NewEvent(EventReviewRequested, "agent-"+agentID, map[string]any{
		"agent_id":        agentID,
		"files_changed":   filesChanged,
		"task_description": taskDescription,
	})
}

// NewReviewCompletedEvent creates an event with review results.
func NewReviewCompletedEvent(reviewerID, decision string, issues, suggestions []string) event.Event {
	return event.NewEvent(EventReviewCompleted, "agent-"+reviewerID, map[string]any{
		"reviewer_id":  reviewerID,
		"decision":     decision,
		"issues":       issues,
		"suggestions":  suggestions,
	})
}

// NewArchitectureCheckRequestedEvent creates an event for pre-dev architecture validation.
func NewArchitectureCheckRequestedEvent(agentID, taskDescription, proposedApproach string) event.Event {
	return event.NewEvent(EventArchitectureCheckRequested, "agent-"+agentID, map[string]any{
		"agent_id":         agentID,
		"task_description": taskDescription,
		"proposed_approach": proposedApproach,
	})
}

// NewArchitectureCheckCompletedEvent creates an event with architecture check results.
func NewArchitectureCheckCompletedEvent(decision string, concerns []string, approved bool) event.Event {
	return event.NewEvent(EventArchitectureCheckCompleted, "architecture-checker", map[string]any{
		"decision":  decision,
		"concerns":  concerns,
		"approved":  approved,
	})
}

// NewVulnerabilityCheckRequestedEvent creates an event for post-dev vulnerability analysis.
func NewVulnerabilityCheckRequestedEvent(agentID string, filesChanged []string, taskDescription string) event.Event {
	return event.NewEvent(EventVulnerabilityCheckRequested, "agent-"+agentID, map[string]any{
		"agent_id":        agentID,
		"files_changed":   filesChanged,
		"task_description": taskDescription,
	})
}

// NewVulnerabilityCheckCompletedEvent creates an event with vulnerability analysis results.
func NewVulnerabilityCheckCompletedEvent(reviewerID string, score float64, issues, recommendations []string) event.Event {
	return event.NewEvent(EventVulnerabilityCheckCompleted, "agent-"+reviewerID, map[string]any{
		"reviewer_id":     reviewerID,
		"score":           score,
		"issues":          issues,
		"recommendations": recommendations,
	})
}
