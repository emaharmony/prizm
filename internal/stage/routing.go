// Package stage provides Prizm's pipeline execution engine.
//
// RoutingStage routes an inbound message to the appropriate agent
// and resolves the LLM provider and model configuration.
// It's the first domain-specific stage in the conversation pipeline.
package stage

import (
	"context"
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/router"
	"github.com/emaharmony/prizm/internal/session"
)

// RoutingStage routes a message to an agent and resolves the provider/model.
type RoutingStage struct {
	Router    *router.Router
	Providers *provider.ProviderRegistry
	// AgentConfigs maps agent IDs to their configuration.
	// Set by the handler before pipeline execution.
	AgentConfigs map[string]*AgentConfigSnapshot
}

// AgentConfigSnapshot captures the agent configuration needed by the pipeline.
// This avoids importing the full orchestrator.Config.
type AgentConfigSnapshot struct {
	ID       string
	Model    string
	Provider string
	Primary  bool
	Context  []string // named contexts to inject (e.g., "soul", "agents")
}

// Name returns the stage identifier.
func (s *RoutingStage) Name() string {
	return "routing"
}

// Validate checks that required dependencies are present.
func (s *RoutingStage) Validate(rc *RunContext) error {
	if s.Router == nil {
		return fmt.Errorf("stage %s: router is required", s.Name())
	}
	if s.Providers == nil {
		return fmt.Errorf("stage %s: provider registry is required", s.Name())
	}
	return nil
}

// Execute routes the message and resolves the provider.
func (s *RoutingStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	start := time.Now()

	// Route the message content to an agent
	result := s.Router.Route(rc.Task)

	// Resolve agent configuration
	agentCfg, ok := s.AgentConfigs[result.AgentID]
	if !ok {
		evt := event.NewEvent("prizm.routing.failed", "routing-stage", map[string]any{
			"run_id":   rc.RunID,
			"agent_id": result.AgentID,
			"error":    "no configuration for agent",
		})
		newRC := rc.WithEvent(evt)
		return newRC, &StageResult{
			StageName: s.Name(),
			Success:   false,
			Error:     fmt.Sprintf("no configuration for agent %s", result.AgentID),
		}, nil
	}

	// Resolve the LLM provider
	llmProvider, err := s.Providers.Get(agentCfg.Model)
	if err != nil {
		evt := event.NewEvent("prizm.routing.failed", "routing-stage", map[string]any{
			"run_id":   rc.RunID,
			"agent_id": result.AgentID,
			"model":    agentCfg.Model,
			"error":    err.Error(),
		})
		newRC := rc.WithEvent(evt)
		return newRC, &StageResult{
			StageName: s.Name(),
			Success:   false,
			Error:     fmt.Sprintf("no provider for model %s: %v", agentCfg.Model, err),
		}, nil
	}

	duration := time.Since(start).Milliseconds()

	// Emit routing completed event
	evt := event.NewEvent("prizm.routing.completed", "routing-stage", map[string]any{
		"run_id":      rc.RunID,
		"agent_id":    result.AgentID,
		"model":       agentCfg.Model,
		"provider":    agentCfg.Provider,
		"method":      result.Method,
		"clean_text":  result.CleanedContent,
		"duration_ms": duration,
	})

	// Update RunContext with routing results using copy-on-write
	newRC := rc.WithEvent(evt).
		WithAgent(result.AgentID).
		WithProvider(llmProvider, agentCfg.Provider).
		WithModel(agentCfg.Model)

	// Store routing metadata and agent config
	newRC.CleanedContent = result.CleanedContent
	newRC.RouteMethod = result.Method
	newRC.AgentConfig = agentCfg

	return newRC, &StageResult{
		StageName: s.Name(),
		Success:   true,
		Data: map[string]any{
			"agent_id":    result.AgentID,
			"model":       agentCfg.Model,
			"provider":    agentCfg.Provider,
			"method":      result.Method,
			"duration_ms": duration,
		},
	}, nil
}

// Rollback is a no-op — routing has no side effects.
func (s *RoutingStage) Rollback(ctx context.Context, rc *RunContext) error {
	return nil
}

// RouteResult holds the output of message routing.
// Re-exported from router package for convenience.
type RouteResult = router.RouteResult

// SessionManager is an interface for session operations.
// The pipeline uses this interface so it doesn't depend on the concrete
// session.Manager type directly.
type SessionManager interface {
	FindActive(channelType, channelID, userID string) (*session.Session, error)
	Create(agentID, channelType, channelID, userID string) (*session.Session, error)
	AddMessage(sessionID, role, content, agentID string) (*session.Message, error)
}

// Session represents a conversation session.
// Re-exported from session package for convenience.
type Session = session.Session
