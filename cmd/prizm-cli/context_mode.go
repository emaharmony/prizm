package main

import (
	"github.com/emaharmony/prizm/internal/context"
	"github.com/emaharmony/prizm/internal/orchestrator"
)

// buildContextString produces the workspace context string for the system prompt.
// In "open_book" mode, only file summaries are injected and the model is told
// to use read_file for details. In "full" mode (default), all context files
// are loaded into the prompt.
func buildContextString(
	ctxBuilder *context.Builder,
	cfg *orchestrator.Config,
	agentCfg *orchestrator.AgentConfig,
) string {
	if ctxBuilder == nil || len(agentCfg.Context) == 0 {
		return ""
	}

	budget := cfg.Prizm.ContextTokenBudget
	if budget <= 0 {
		budget = 128000
	}

	// Build context without soul/identity (already in Layer 1)
	otherContexts := make([]string, 0, len(agentCfg.Context))
	for _, c := range agentCfg.Context {
		if c != "soul" && c != "identity" {
			otherContexts = append(otherContexts, c)
		}
	}

	if len(otherContexts) == 0 {
		return ""
	}

	// Resolve context mode: agent override > global config > default "full"
	mode := resolveContextMode(cfg, agentCfg)

	builder := context.NewBuilder(ctxBuilder.WorkspaceRoot).
		WithNamedContexts(otherContexts)

	if mode == "open_book" {
		// Open book: inject index only, model loads files on demand
		injected, err := builder.BuildIndex()
		if err != nil {
			return ""
		}
		return injected.FormattedString
	}

	// Full mode: inject all context files (default behavior)
	builder = builder.WithTokenBudget(budget)
	injected, err := builder.BuildCached()
	if err != nil {
		return ""
	}
	return injected.FormattedString
}

// resolveContextMode determines which context injection mode to use.
// Priority: agent override > global config > default "full".
func resolveContextMode(cfg *orchestrator.Config, agentCfg *orchestrator.AgentConfig) string {
	// Agent-level override
	if agentCfg.ContextMode != "" {
		return agentCfg.ContextMode
	}
	// Global config
	if cfg != nil && cfg.Prizm.ContextMode != "" {
		return cfg.Prizm.ContextMode
	}
	return "full"
}