// Package agentns provides dynamic agent namespace support for Prizm V20.
//
// Instead of hardcoded "lumi.*" or "mango.*" event types, agent namespaces
// are determined by the agent's ID in the configuration. If no ID is provided,
// the system auto-generates: prizm1, prizm2, prizm3, etc.
//
// The only hardcoded namespace is "prizm.*" for system-level events.
//
// Usage:
//
//	ns := agentns.New("lumi")
//	ns.EventType("agent.started")  // → "lumi.agent.started"
//	ns.EventType("tool.completed") // → "lumi.tool.completed"
//
//	ns = agentns.New("prizm1")     // auto-generated
//	ns.EventType("agent.started")  // → "prizm1.agent.started"
package agentns

import (
	"fmt"
	"strings"
)

// Namespace represents an agent's event namespace.
type Namespace struct {
	agentID string
}

// New creates a Namespace for the given agent ID.
// The ID must be alphanumeric + hyphens (validated by the orchestrator config).
func New(agentID string) *Namespace {
	return &Namespace{agentID: agentID}
}

// EventType creates a fully-qualified event type for this agent.
// Example: ns.EventType("agent.started") → "lumi.agent.started"
func (ns *Namespace) EventType(suffix string) string {
	return ns.agentID + "." + suffix
}

// AgentStarted returns "<agent-id>.agent.started"
func (ns *Namespace) AgentStarted() string {
	return ns.EventType("agent.started")
}

// AgentOutput returns "<agent-id>.agent.output"
func (ns *Namespace) AgentOutput() string {
	return ns.EventType("agent.output")
}

// AgentCompleted returns "<agent-id>.agent.completed"
func (ns *Namespace) AgentCompleted() string {
	return ns.EventType("agent.completed")
}

// AgentFailed returns "<agent-id>.agent.failed"
func (ns *Namespace) AgentFailed() string {
	return ns.EventType("agent.failed")
}

// LLMRequested returns "<agent-id>.llm.requested"
func (ns *Namespace) LLMRequested() string {
	return ns.EventType("llm.requested")
}

// LLMCompleted returns "<agent-id>.llm.completed"
func (ns *Namespace) LLMCompleted() string {
	return ns.EventType("llm.completed")
}

// ToolRequested returns "<agent-id>.tool.requested"
func (ns *Namespace) ToolRequested() string {
	return ns.EventType("tool.requested")
}

// ToolCompleted returns "<agent-id>.tool.completed"
func (ns *Namespace) ToolCompleted() string {
	return ns.EventType("tool.completed")
}

// ContextInjected returns "<agent-id>.context.injected"
func (ns *Namespace) ContextInjected() string {
	return ns.EventType("context.injected")
}

// ChannelSent returns "<agent-id>.channel.sent"
func (ns *Namespace) ChannelSent() string {
	return ns.EventType("channel.sent")
}

// IsAgentEvent checks if an event type belongs to a specific agent.
// Example: IsAgentEvent("lumi.agent.started", "lumi") → true
func IsAgentEvent(eventType, agentID string) bool {
	return strings.HasPrefix(eventType, agentID+".")
}

// ExtractAgentID extracts the agent ID from an event type.
// Example: ExtractAgentID("lumi.agent.started") → "lumi"
// Returns "" for system events (prizm.*) or malformed events.
func ExtractAgentID(eventType string) string {
	parts := strings.SplitN(eventType, ".", 2)
	if len(parts) < 2 {
		return ""
	}
	agentID := parts[0]
	// System events use "prizm" — not an agent
	if agentID == "prizm" {
		return ""
	}
	return agentID
}

// ExtractEventSuffix extracts the event suffix after the agent ID.
// Example: ExtractEventSuffix("lumi.agent.started") → "agent.started"
func ExtractEventSuffix(eventType string) string {
	parts := strings.SplitN(eventType, ".", 2)
	if len(parts) < 2 {
		return eventType
	}
	return parts[1]
}

// AutoGenerateID generates an auto-incrementing agent ID.
// First call: prizm1, second call: prizm2, etc.
func AutoGenerateID(counter int) string {
	return fmt.Sprintf("prizm%d", counter)
}

// AllAgentEventTypes returns the complete list of event suffixes
// that every agent namespace supports.
func AllAgentEventTypes() []string {
	return []string{
		"agent.started",
		"agent.output",
		"agent.completed",
		"agent.failed",
		"llm.requested",
		"llm.completed",
		"tool.requested",
		"tool.completed",
		"context.injected",
		"channel.sent",
	}
}
