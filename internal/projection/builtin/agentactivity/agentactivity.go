// Package agentactivity provides the agent_activity projection (V13).
//
// This projection tracks per-agent activity counts from the event stream.
// It subscribes to all agent event types (delegated, completed, failed)
// and maintains a running count for each agent.
//
// Output format:
//
//	{
//	  "agents": {
//	    "planner": { "delegated": 3, "completed": 2, "failed": 1 },
//	    "coder":   { "delegated": 5, "completed": 4, "failed": 1 }
//	  },
//	  "total_delegations": 8,
//	  "total_completions": 6,
//	  "total_failures": 2
//	}
//
// This projection is useful for:
// - Understanding which agents are most active in a run
// - Detecting agents that fail frequently (may need different model/prompt)
// - Dashboard display of agent activity (V11)
package agentactivity

import (
	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/event"
)

// AgentActivitySnapshot is the output of the agent_activity projection.
type AgentActivitySnapshot struct {
	Agents           map[string]AgentCounts `json:"agents"`
	TotalDelegations int                    `json:"total_delegations"`
	TotalCompletions int                    `json:"total_completions"`
	TotalFailures    int                    `json:"total_failures"`
}

// AgentCounts tracks delegation/completion/failure counts for one agent.
type AgentCounts struct {
	Delegated int `json:"delegated"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// AgentActivityProjection tracks per-agent activity from the event stream.
type AgentActivityProjection struct {
	snapshot AgentActivitySnapshot
}

// New creates a new AgentActivityProjection.
func New() *AgentActivityProjection {
	return &AgentActivityProjection{
		snapshot: AgentActivitySnapshot{
			Agents: make(map[string]AgentCounts),
		},
	}
}

// Name returns the projection name for registration and CLI queries.
func (p *AgentActivityProjection) Name() string {
	return "agent_activity"
}

// Subscribe returns the event types this projection cares about.
// We subscribe to all agent events to track the full delegation lifecycle.
func (p *AgentActivityProjection) Subscribe() []string {
	return []string{
		agent.EventAgentDelegated,
		agent.EventAgentCompleted,
		agent.EventAgentFailed,
	}
}

// Apply processes an event and updates the projection state.
func (p *AgentActivityProjection) Apply(evt event.Event) error {
	agentName, _ := evt.Payload["agent_name"].(string)
	if agentName == "" {
		return nil // skip events without an agent name
	}

	counts := p.snapshot.Agents[agentName]

	switch evt.Type {
	case agent.EventAgentDelegated:
		counts.Delegated++
		p.snapshot.TotalDelegations++
	case agent.EventAgentCompleted:
		counts.Completed++
		p.snapshot.TotalCompletions++
	case agent.EventAgentFailed:
		counts.Failed++
		p.snapshot.TotalFailures++
	}

	p.snapshot.Agents[agentName] = counts
	return nil
}

// Snapshot returns the current projection state for serialization.
func (p *AgentActivityProjection) Snapshot() map[string]any {
	result := make(map[string]any)
	agents := make(map[string]any)
	for name, counts := range p.snapshot.Agents {
		agents[name] = map[string]any{
			"delegated": counts.Delegated,
			"completed": counts.Completed,
			"failed":    counts.Failed,
		}
	}
	result["agents"] = agents
	result["total_delegations"] = p.snapshot.TotalDelegations
	result["total_completions"] = p.snapshot.TotalCompletions
	result["total_failures"] = p.snapshot.TotalFailures
	return result
}
