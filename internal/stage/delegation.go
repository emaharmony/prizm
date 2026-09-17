package stage

import (
	"context"
	"log"
	"regexp"
	"strings"

	"github.com/emaharmony/prizm/internal/delegation"
	"github.com/emaharmony/prizm/internal/orchestrator"
)

// delegationPattern matches delegation intent markers in LLM output.
// Matches patterns like: [DELEGATE: mango] or [DELEGATE: mango | code_implementation]
// Agent IDs must match config validation: [a-z0-9]([a-z0-9-]*[a-z0-9])?
var delegationPattern = regexp.MustCompile(`\[DELEGATE:\s*([a-z0-9][a-z0-9-]*[a-z0-9]|[a-z0-9])\s*(?:\|([^]]+))?\]`)

// DelegationStage detects delegation intent in LLM output and delegates
// tasks to other agents via the delegation engine.
//
// The stage scans the LLM response for delegation markers like:
//
//	[DELEGATE: mango] Implement the X feature
//	[DELEGATE: mango | code_implementation] Fix the bug in Y
//
// When a marker is found, the stage:
// 1. Creates a task via the delegation engine
// 2. Publishes a <agent>.task.created event to NATS
// 3. Strips the marker from the response (user doesn't see it)
//
// If no delegation marker is found, the stage is a no-op (pass-through).
type DelegationStage struct {
	// Engine is the delegation engine for creating tasks.
	Engine *delegation.Engine

	// AgentConfigs maps agent IDs to their configurations.
	// Used to check delegation capabilities (can the delegator delegate?
	// can the target handle the task type?).
	AgentConfigs map[string]*orchestrator.AgentConfig

	// StripMarkers controls whether delegation markers are removed from
	// the LLM response before passing to the next stage.
	StripMarkers bool
}

// Name returns the stage identifier.
func (s *DelegationStage) Name() string {
	return "delegation"
}

// Rollback is a no-op for delegation — tasks are already published.
func (s *DelegationStage) Rollback(ctx context.Context, rc *RunContext) error {
	return nil
}

// Validate checks that the delegation engine is available.
// If no engine is set, the stage skips (no-op).
func (s *DelegationStage) Validate(rc *RunContext) error {
	return nil // Engine is optional — no-op if nil
}

// Execute scans the LLM response for delegation markers and delegates tasks.
func (s *DelegationStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	if s.Engine == nil || rc.LLMResponse == "" {
		// No engine or no response — pass through
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   true,
			Data:      map[string]any{"skipped": true, "reason": "no_engine"},
		}, nil
	}

	// Scan for delegation markers
	matches := delegationPattern.FindAllStringSubmatch(rc.LLMResponse, -1)
	if len(matches) == 0 {
		// No delegation intent — pass through
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   true,
			Data:      map[string]any{"delegations": 0},
		}, nil
	}

	// Process each delegation marker
	delegatedTasks := make([]string, 0, len(matches))
	matchIndices := delegationPattern.FindAllStringSubmatchIndex(rc.LLMResponse, -1)
	cleanedResponse := rc.LLMResponse

	for i, match := range matches {
		agentID := match[1]  // e.g., "mango"
		taskType := match[2] // e.g., "code_implementation" (optional)

		if taskType == "" {
			taskType = "general"
		}
		taskType = strings.TrimSpace(taskType)
		agentID = strings.TrimSpace(agentID)

		// Check delegation capabilities
		if s.AgentConfigs != nil {
			delegator, ok := s.AgentConfigs[rc.Agent]
			if !ok {
				log.Printf("[DELEGATION] unknown delegator %q, skipping delegation", rc.Agent)
				continue
			}
			target, ok := s.AgentConfigs[agentID]
			if !ok {
				log.Printf("[DELEGATION] unknown target agent %q, skipping delegation", agentID)
				continue
			}
			if err := delegation.CanDelegate(delegator, target, taskType); err != nil {
				log.Printf("[DELEGATION] capability check failed: %v", err)
				continue
			}
		}

		// Extract description using match position (not full-text search)
		description := extractDelegationDescriptionAt(rc.LLMResponse, matchIndices[i])

		// Delegate the task
		contextData := map[string]any{
			"source_run_id": rc.RunID,
			"source_agent":  rc.Agent,
			"session_id":    rc.SessionID,
		}

		delegateTask, err := s.Engine.Delegate(ctx, rc.Agent, agentID, taskType, description, contextData)
		if err != nil {
			log.Printf("[DELEGATION] failed to delegate to %s: %v", agentID, err)
			continue
		}

		delegatedTasks = append(delegatedTasks, delegateTask.ID)

		// Event publishing is handled by the delegation engine via NATS.
		// No need to add to RunContext.Events — the engine publishes
		// <agent>.task.created directly.

		log.Printf("[DELEGATION] %s delegated task %s to %s (type: %s)", rc.Agent, delegateTask.ID, agentID, taskType)
	}

	// Strip delegation markers from the response
	if s.StripMarkers {
		cleanedResponse = delegationPattern.ReplaceAllString(cleanedResponse, "")
		rc.LLMResponse = cleanedResponse
	}

	return rc, &StageResult{
		StageName: s.Name(),
		Success:   true,
		Data: map[string]any{
			"delegations":        len(delegatedTasks),
			"delegated_task_ids": delegatedTasks,
		},
	}, nil
}

// extractDelegationDescriptionAt extracts the task description from
// the text following the delegation marker at the given match position.
// It uses the match indices from FindAllStringSubmatchIndex to avoid
// re-searching the full text, which could produce wrong descriptions
// when multiple markers are present.
func extractDelegationDescriptionAt(text string, matchIdx []int) string {
	// matchIdx[1] is the end position of the full match
	markerEnd := matchIdx[1]
	if markerEnd >= len(text) {
		return "Delegated task"
	}

	// Get remaining text until end of line
	remaining := text[markerEnd:]
	end := len(remaining)
	for i, c := range remaining {
		if c == '\n' {
			end = i
			break
		}
	}

	desc := remaining[:end]
	if len(desc) > 200 {
		desc = desc[:200] + "..."
	}

	if desc == "" {
		return "Delegated task"
	}

	// Clean up leading space
	if len(desc) > 0 && desc[0] == ' ' {
		desc = desc[1:]
	}

	return desc
}
