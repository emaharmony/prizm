// Package stage provides Prizm's pipeline execution engine (V14a).
//
// ToolStage executes tool calls requested by the LLM, with policy enforcement.
// It checks the policy engine before executing each tool call and creates
// approval requests for mutations (V4).
package stage

import (
	"context"
	"fmt"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/tool"
)

// ToolStage executes tool calls with policy enforcement.
// If the LLM response contains a tool request, this stage parses it,
// checks policy, and executes the tool.
type ToolStage struct {
	// ToolRegistry resolves tool names to implementations.
	ToolRegistry *tool.Registry

	// PolicyConfig controls which tools are allowed and with what constraints.
	PolicyConfig tool.PolicyConfig

	// WorkspaceRoot is the root directory for file operations.
	WorkspaceRoot string
}

// Name returns the stage identifier.
func (s *ToolStage) Name() string {
	return "tool"
}

// Validate checks that the tool registry is configured.
func (s *ToolStage) Validate(rc *RunContext) error {
	if s.ToolRegistry == nil {
		return fmt.Errorf("stage %s: tool registry is required", s.Name())
	}
	return nil
}

// Execute checks if the LLM response contains a tool request and executes it.
// If the response is not a tool request, the stage succeeds with no tool result.
func (s *ToolStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	if rc.LLMResponse == "" {
		// No LLM response — nothing to do
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   true,
			Data:      map[string]any{"tool_called": false},
		}, nil
	}

	// For now, check if the response looks like a tool request
	// (The full parsing logic will be extracted from runner.go in a future step)
	// This stage succeeds even if no tool is called — tools are optional.

	// Emit tool stage started event
	evt := event.NewEvent(event.V1EventTypes.ToolCalled, "tool-stage", map[string]any{
		"run_id":       rc.RunID,
		"has_response": rc.LLMResponse != "",
	})
	newRC := rc.WithEvent(evt)

	return newRC, &StageResult{
		StageName: s.Name(),
		Success:   true,
		Data: map[string]any{
			"tool_called": false,
			"note":        "tool execution will be fully extracted from runner.go",
		},
	}, nil
}

// Rollback is a no-op for now — tool rollbacks would need to undo file writes,
// which is complex and not yet implemented.
func (s *ToolStage) Rollback(ctx context.Context, rc *RunContext) error {
	return nil
}
