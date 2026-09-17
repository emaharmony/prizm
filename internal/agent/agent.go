// Package agent provides Prizm's agent model and registry (V13).
//
// An Agent is a named LLM-backed entity with declared capabilities. Agents
// collaborate through the event stream — they don't message each other
// directly. Policy controls who can delegate to whom.
//
// The V1 placeholder agent (RunPlaceholder) still exists for backward
// compatibility. V13 adds the Agent struct and Registry for real
// multi-agent orchestration.
//
// Key concept: agents are registered, not spawned. You define them upfront
// (name, role, capabilities, provider, model) and then workflows delegate
// subtasks to them. This keeps multi-agent workflow-bound and policy-controlled.
package agent

import (
	"fmt"
	"sync"
)

// Agent represents a named LLM-backed agent with declared capabilities.
//
// An agent is NOT a running process. It's a configuration: "there exists an
// agent named 'coder' that can write code, uses the ollama provider, and
// runs on deepseek-v4-pro." When a workflow delegates a subtask, the
// delegation handler resolves the agent and calls its LLM provider.
//
// Agent names must be alphanumeric + hyphens (same rule as adapters in V9).
// This prevents ambiguity in policy action format: agent.<name>.<action>.
type Agent struct {
	Name         string            // e.g., "coder", "reviewer", "planner"
	Version      string            // e.g., "1.0.0"
	Role         string            // e.g., "implementation", "review", "planning"
	Capabilities []AgentCapability // what this agent can do
	ProviderName string            // e.g., "ollama", "mock"
	Model        string            // e.g., "deepseek-v4-pro:cloud"
}

// AgentCapability declares what an agent can do.
// This is similar to adapter.Capability but for LLM-backed actions.
type AgentCapability struct {
	Action      string // e.g., "write_code", "review_code", "plan_task"
	Description string // human-readable
}

// Validate checks that an Agent has required fields.
// Name must be non-empty and contain only alphanumeric + hyphens.
// Version, Role, and at least one Capability are required.
func (a *Agent) Validate() error {
	if a.Name == "" {
		return fmt.Errorf("agent: name is required")
	}
	if !isValidAgentName(a.Name) {
		return fmt.Errorf("agent: name %q must be alphanumeric + hyphens only", a.Name)
	}
	if a.Version == "" {
		return fmt.Errorf("agent: version is required")
	}
	if a.Role == "" {
		return fmt.Errorf("agent: role is required")
	}
	if len(a.Capabilities) == 0 {
		return fmt.Errorf("agent: at least one capability is required")
	}
	return nil
}

// isValidAgentName enforces the alphanumeric + hyphens naming rule.
// No dots allowed — prevents ambiguity in policy action format.
func isValidAgentName(name string) bool {
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return len(name) > 0
}

// Registry holds registered agents. Thread-safe.
//
// Usage:
//
//	reg := agent.NewRegistry()
//	reg.Register(&agent.Agent{Name: "coder", ...})
//	a, err := reg.Resolve("coder")
type Registry struct {
	agents map[string]*Agent
	mu     sync.RWMutex
}

// NewRegistry creates an empty agent registry.
func NewRegistry() *Registry {
	return &Registry{
		agents: make(map[string]*Agent),
	}
}

// Register adds an agent to the registry. Returns an error if validation
// fails or an agent with the same name already exists.
func (r *Registry) Register(a *Agent) error {
	if err := a.Validate(); err != nil {
		return fmt.Errorf("agent register: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.agents[a.Name]; exists {
		return fmt.Errorf("agent register: %q already registered", a.Name)
	}

	r.agents[a.Name] = a
	return nil
}

// Resolve looks up an agent by name. Returns an error if not found.
func (r *Registry) Resolve(name string) (*Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	a, ok := r.agents[name]
	if !ok {
		return nil, fmt.Errorf("agent %q not found", name)
	}
	return a, nil
}

// List returns all registered agent names in sorted order.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.agents))
	for name := range r.agents {
		names = append(names, name)
	}
	// Sort for deterministic output
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[i] > names[j] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return names
}

// Capabilities returns all declared capabilities for an agent.
// Returns an error if the agent is not found.
func (r *Registry) Capabilities(name string) ([]AgentCapability, error) {
	a, err := r.Resolve(name)
	if err != nil {
		return nil, err
	}
	return a.Capabilities, nil
}
