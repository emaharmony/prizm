package main

import (
	"github.com/emaharmony/prizm/internal/orchestrator"
)

// resolveAgentLoop determines which tool loop mode to use for an agent.
// Priority: agent config override > global prizm config > default ("classic").
func resolveAgentLoop(cfg *orchestrator.Config, agentCfg *orchestrator.AgentConfig) string {
	// Agent-level override takes priority
	if agentCfg.AgentLoop != "" {
		return agentCfg.AgentLoop
	}
	// Global config
	if cfg != nil && cfg.Prizm.AgentLoop != "" {
		return cfg.Prizm.AgentLoop
	}
	// Default
	return "classic"
}