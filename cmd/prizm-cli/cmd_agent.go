// cmd_agent.go implements the `prizm agent` subcommands (V13).
//
// Agents are named LLM-backed entities with declared capabilities. The CLI
// commands here let you inspect which agents are registered and what they
// can do. Agents are registered programmatically (not from YAML manifests
// yet), so the list is determined by what's built into the binary.
//
// Commands:
//
//	prizm agent list          — Show all registered agents
//	prizm agent show <name>   — Show agent details and capabilities
package main

import (
	"fmt"
	"os"

	"github.com/emaharmony/prizm/internal/agent"
)

// newAgentRegistry creates a registry with built-in agents.
// Currently registers a planner, coder, and reviewer as examples.
// In production, agents would be registered based on configuration.
func newAgentRegistry() *agent.Registry {
	reg := agent.NewRegistry()
	reg.Register(&agent.Agent{
		Name:    "planner",
		Version: "1.0.0",
		Role:    "planning",
		Capabilities: []agent.AgentCapability{
			{Action: "plan_task", Description: "Break down a task into steps"},
		},
		ProviderName: "mock",
		Model:        "mock-model",
	}) //nolint:errcheck // built-in, known good
	reg.Register(&agent.Agent{
		Name:    "coder",
		Version: "1.0.0",
		Role:    "implementation",
		Capabilities: []agent.AgentCapability{
			{Action: "write_code", Description: "Write implementation code"},
			{Action: "fix_code", Description: "Fix bugs in existing code"},
		},
		ProviderName: "mock",
		Model:        "mock-model",
	}) //nolint:errcheck // built-in, known good
	reg.Register(&agent.Agent{
		Name:    "reviewer",
		Version: "1.0.0",
		Role:    "review",
		Capabilities: []agent.AgentCapability{
			{Action: "review_code", Description: "Review code for quality and safety"},
		},
		ProviderName: "mock",
		Model:        "mock-model",
	}) //nolint:errcheck // built-in, known good
	return reg
}

// executeAgentList shows all registered agents with their role and
// capability count.
func executeAgentList() {
	reg := newAgentRegistry()
	names := reg.List()

	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  Prizm V13 Agents")
	fmt.Println("═══════════════════════════════════════════")
	if len(names) == 0 {
		fmt.Println("  (no agents registered)")
	}
	for _, name := range names {
		a, _ := reg.Resolve(name)
		fmt.Printf("  %-20s %-15s v%-5s  %d capabilit%s\n",
			name, a.Role, a.Version, len(a.Capabilities), plural(len(a.Capabilities)))
	}
	fmt.Println("═══════════════════════════════════════════")
}

// executeAgentShow displays detailed info about one agent: role, version,
// provider, model, and declared capabilities.
func executeAgentShow(name string) {
	reg := newAgentRegistry()
	a, err := reg.Resolve(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Agent:    %s\n", a.Name)
	fmt.Printf("  Version:  %s\n", a.Version)
	fmt.Printf("  Role:     %s\n", a.Role)
	fmt.Printf("  Provider: %s\n", a.ProviderName)
	fmt.Printf("  Model:    %s\n", a.Model)
	fmt.Println("═══════════════════════════════════════════")

	if len(a.Capabilities) > 0 {
		fmt.Println("  Capabilities:")
		for _, c := range a.Capabilities {
			fmt.Printf("    %-20s %s\n", c.Action, c.Description)
		}
	} else {
		fmt.Println("  (no capabilities)")
	}
	fmt.Println("═══════════════════════════════════════════")
}
