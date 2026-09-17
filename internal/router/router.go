// Package router routes incoming messages to the appropriate agent.
//
// Routing logic:
//  1. Direct address: "Lumi, fix this" → route to agent named "lumi"
//  2. @mention: "@Mango write tests" → route to agent named "mango"
//  3. Default fallback: No address detected → route to primary agent
//
// The primary agent is determined by the config (primary: true) or
// falls back to the first agent in the config. The primary agent decides
// what to do with unaddressed messages — it may handle them directly
// or delegate to another agent.
package router

import (
	"regexp"
	"strings"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/orchestrator"
)

// Router determines which agent should handle an incoming message.
type Router struct {
	registry  *agent.Registry
	primaryID string // ID of the primary/default agent
	agentIDs  []string
}

// New creates a new Router with the given agent registry and config.
func New(registry *agent.Registry, cfg *orchestrator.Config) *Router {
	primary := cfg.PrimaryAgent()
	primaryID := ""
	if primary != nil {
		primaryID = primary.ID
	}

	agentIDs := make([]string, 0)
	for _, a := range cfg.Agents {
		agentIDs = append(agentIDs, a.ID)
	}

	return &Router{
		registry:  registry,
		primaryID: primaryID,
		agentIDs:  agentIDs,
	}
}

// RouteResult contains the routing decision.
type RouteResult struct {
	// AgentID is the resolved agent to handle the message.
	AgentID string

	// Method describes how the routing decision was made:
	// "direct", "mention", "primary"
	Method string

	// CleanedContent is the message content with the address prefix removed.
	// For direct address ("Lumi, fix this"), the prefix "Lumi, " is removed.
	// For @mention ("@Mango write tests"), the "@Mango " is removed.
	// For primary routing, the content is unchanged.
	CleanedContent string
}

// directAddressPattern matches "AgentName, " or "AgentName " at the start
// of a message. Case-insensitive. Examples:
//
//	"Lumi, fix this"     → match "Lumi, "
//	"Mango write tests"  → match "Mango "
var directAddressPattern = regexp.MustCompile(`(?i)^([a-z0-9][a-z0-9-]*)[,\s]+`)

// mentionPattern matches @Mention at any position in the message.
var mentionPattern = regexp.MustCompile(`(?i)@([a-z0-9][a-z0-9-]*)`)

// Route determines which agent should handle the given message content.
// Priority order:
//  1. Direct address at start: "Lumi, fix this" → route to named agent
//  2. @mention: "@Mango write tests" → route to named agent
//  3. Default: route to primary agent
//
// Direct address takes precedence over @mention because "Lumi, what about @Mango's code?"
// is addressed to Lumi with an @mention in the body, not to Mango.
func (r *Router) Route(content string) *RouteResult {
	// 1. Check for direct address at start of message
	if match := directAddressPattern.FindStringSubmatch(content); len(match) > 1 {
		addressedID := strings.ToLower(match[1])
		if r.isKnownAgent(addressedID) {
			cleaned := directAddressPattern.ReplaceAllString(content, "")
			cleaned = strings.TrimSpace(cleaned)
			return &RouteResult{
				AgentID:        addressedID,
				Method:         "direct",
				CleanedContent: cleaned,
			}
		}
	}

	// 2. Check for @mention anywhere in message
	if match := mentionPattern.FindStringSubmatch(content); len(match) > 1 {
		mentionedID := strings.ToLower(match[1])
		if r.isKnownAgent(mentionedID) {
			cleaned := mentionPattern.ReplaceAllString(content, "")
			cleaned = strings.TrimSpace(cleaned)
			return &RouteResult{
				AgentID:        mentionedID,
				Method:         "mention",
				CleanedContent: cleaned,
			}
		}
	}

	// 3. Default: route to primary agent
	return &RouteResult{
		AgentID:        r.primaryID,
		Method:         "primary",
		CleanedContent: strings.TrimSpace(content),
	}
}

// isKnownAgent checks if an agent ID is registered.
func (r *Router) isKnownAgent(id string) bool {
	for _, known := range r.agentIDs {
		if strings.EqualFold(known, id) {
			return true
		}
	}
	return false
}
