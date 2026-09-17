package main

import (
	"github.com/emaharmony/prizm/internal/orchestrator"
)

// defaultConversationPostfix is the minimal fallback when an agent has no
// SOUL.md (no identity content) AND no explicit conversation_postfix AND no
// channel personality. This only applies to bare agents with no workspace
// personality files — it should never override or compete with SOUL.md.
const defaultConversationPostfix = "Stay present in the conversation. Be engaged and responsive."

// resolveConversationPostfix picks the "How You Respond" directive for a
// message, with SOUL.md as the highest authority:
//
//  1. If the agent has SOUL.md identity content loaded (hasSoulContent),
//     SOUL.md is the sole personality authority — return "" to signal
//     that no postfix should be injected. The model follows SOUL.md.
//  2. If no SOUL.md, use the agent's explicit conversation_postfix (if set).
//  3. If no postfix, use the channel's Personality directive (if set).
//  4. If none of the above, use defaultConversationPostfix.
//
// This ensures SOUL.md always takes precedence over config-level personality
// directives. Config and channel personality are fallbacks for agents that
// don't have a SOUL.md.
func resolveConversationPostfix(agentCfg *orchestrator.AgentConfig, channelRole *orchestrator.ChannelRole, hasSoulContent bool) string {
	// SOUL.md is the constitutional document — it always wins.
	if hasSoulContent {
		return ""
	}
	if agentCfg != nil && agentCfg.ConversationPostfix != "" {
		return agentCfg.ConversationPostfix
	}
	if channelRole != nil {
		if directive := orchestrator.PersonalityDirective(channelRole.Personality); directive != "" {
			return directive
		}
	}
	return defaultConversationPostfix
}