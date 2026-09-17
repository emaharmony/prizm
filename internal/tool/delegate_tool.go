package tool

import (
	"context"
	"fmt"
)

// DelegateTool lets an agent delegate a task to another agent mid-conversation.
// This is the ad-hoc "hey Mango, look at this" path — not the structured
// workflow V2 pipeline. The agent calls this tool, the delegation engine
// creates a task and publishes it to NATS, and the delegated agent picks it up.
//
// The result is a task ID that the agent can reference. The actual result
// from the delegated agent comes back asynchronously via NATS.
type DelegateTool struct {
	Delegator   Delegator
	CallingAgent string // ID of the agent that owns this tool instance
}

// Delegator is the minimal interface needed from the delegation engine.
type Delegator interface {
	Delegate(ctx context.Context, delegatedBy, delegatedTo string, taskType, description string, contextData map[string]any) (taskID string, err error)
}

// NewDelegateTool creates a delegate tool backed by the given delegator.
// callingAgent is the ID of the agent that will be delegating (e.g., "lumi").
func NewDelegateTool(d Delegator, callingAgent string) *DelegateTool {
	return &DelegateTool{Delegator: d, CallingAgent: callingAgent}
}

func (t *DelegateTool) Name() string { return "delegate" }

func (t *DelegateTool) Description() string {
	return "Delegate a task to another agent. Use when you want to hand work to a sub-agent (e.g., Mango for code review, Scout for research). Returns a task ID. The delegated agent will process asynchronously."
}

func (t *DelegateTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"agent":       {Type: "string", Description: "The agent ID to delegate to (e.g., 'mango', 'scout', 'coder')", Required: true},
			"task_type":   {Type: "string", Description: "Type of task: 'review', 'research', 'implement', 'fix', 'analyze'", Required: true},
			"description": {Type: "string", Description: "Clear description of what the agent should do", Required: true},
			"files":       {Type: "string", Description: "Comma-separated list of files to review or modify (optional)", Required: false},
			"context":     {Type: "string", Description: "Additional context for the task (optional)", Required: false},
		},
		Output: ParamSpec{Type: "object", Description: "Task ID and status"},
	}
}

func (t *DelegateTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	if t.Delegator == nil {
		return ToolResult{Success: false, Error: "delegation not available in this environment"}, nil
	}

	agent, _ := input["agent"].(string)
	if agent == "" {
		return ToolResult{Success: false, Error: "delegate requires an 'agent' parameter"}, nil
	}

	taskType, _ := input["task_type"].(string)
	if taskType == "" {
		return ToolResult{Success: false, Error: "delegate requires a 'task_type' parameter"}, nil
	}

	description, _ := input["description"].(string)
	if description == "" {
		return ToolResult{Success: false, Error: "delegate requires a 'description' parameter"}, nil
	}

	// Build context data from optional fields
	contextData := map[string]any{}
	if files, ok := input["files"].(string); ok && files != "" {
		contextData["files"] = files
	}
	if ctxStr, ok := input["context"].(string); ok && ctxStr != "" {
		contextData["notes"] = ctxStr
	}

	// Use the configured calling agent ID
	caller := t.CallingAgent
	if caller == "" {
		caller = "lumi" // fallback
	}
	taskID, err := t.Delegator.Delegate(ctx, caller, agent, taskType, description, contextData)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("delegation failed: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"task_id":  taskID,
			"agent":    agent,
			"status":   "delegated",
			"message":  fmt.Sprintf("Task %s delegated to %s. The agent will process it asynchronously.", taskID, agent),
		},
	}, nil
}