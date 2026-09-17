// Package tool provides the tool registry, policy engine, and built-in tools
// for Prizm V3's controlled tool execution system.
//
// Tools are the only way an agent can affect the outside world. Every tool
// call goes through a deterministic policy check before execution, and every
// decision is recorded as an event in the audit log.
package tool

import "context"

// Tool is the interface that every Prizm tool must implement.
// Built-in tools (echo, list_dir, read_file, write_file_dry_run) implement
// this, and future custom tools will too.
type Tool interface {
	// Name returns the unique tool identifier (e.g. "echo", "read_file").
	Name() string
	// Description returns a human-readable description of what the tool does.
	Description() string
	// Schema returns the tool's input/output schema for validation and docs.
	Schema() ToolSchema
	// Execute runs the tool with the given input and returns a result.
	Execute(ctx context.Context, input map[string]any) (ToolResult, error)
}

// ToolSchema describes a tool's input parameters and output shape.
type ToolSchema struct {
	Input  map[string]ParamSpec `json:"input"`
	Output ParamSpec            `json:"output"`
}

// ParamSpec describes a single parameter or output field.
type ParamSpec struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
}

// ToolResult is the universal result type returned by every tool execution.
type ToolResult struct {
	Success bool           `json:"success"`
	Output  map[string]any `json:"output"`
	Error   string         `json:"error,omitempty"`
}

// PolicyDecision represents the outcome of a policy evaluation for a tool call.
type PolicyDecision string

const (
	// PolicyApproved means the tool call is allowed to execute.
	PolicyApproved PolicyDecision = "approved"
	// PolicyDenied means the tool call is blocked.
	PolicyDenied PolicyDecision = "denied"
	// PolicyRequiresApproval means the call needs human approval (not used in V3).
	PolicyRequiresApproval PolicyDecision = "requires_approval"
)
