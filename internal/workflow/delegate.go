// Package workflow provides Prizm's workflow execution engine (V7+).
//
// V13 adds the delegation step handler: when a workflow step has type "delegate",
// the runner resolves the named agent from the agent registry and calls the
// agent's LLM provider with the subtask.
//
// Delegation flow:
//  1. Workflow step type=delegate, agent="coder"
//  2. Runner resolves "coder" from agent registry
//  3. Runner calls the LLM provider with the subtask
//  4. Runner emits agent.delegated → (agent executes) → agent.completed/failed
//  5. Step output becomes available to subsequent steps
//
// All delegation goes through policy (V8). The action is "agent.delegate",
// the subject is the delegating agent (or "workflow"), and the resource
// is the target agent.
package workflow

import (
	"context"
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/policy"
	"github.com/emaharmony/prizm/internal/provider"
)

// DelegationConfig holds the configuration for executing a delegation step.
type DelegationConfig struct {
	// AgentRegistry resolves agent names to Agent structs.
	AgentRegistry *agent.Registry

	// PolicyEvaluator gates delegation with agent.delegate policy action.
	// If nil, all delegations are allowed (backward compatible).
	PolicyEvaluator *policy.Evaluator

	// NewProvider creates a provider for the agent's LLM calls.
	// This is a function so we don't import provider constructors directly.
	NewProvider func(providerName string) (provider.Provider, error)
}

// DelegationResult is the output of a successful delegation step.
type DelegationResult struct {
	AgentName  string // which agent was delegated to
	Output     string // the agent's LLM response
	DurationMs int64  // how long the delegation took
}

// ExecuteDelegate runs a delegation step: resolve the agent, check policy,
// call the agent's LLM provider, and return the result.
//
// This is the core of V13 multi-agent orchestration. It connects the
// workflow engine to the agent registry and policy engine, making
// agent-to-agent delegation safe, observable, and auditable.
func ExecuteDelegate(ctx context.Context, cfg DelegationConfig, step Step, input map[string]any, emitEvent func(string, string, map[string]any)) (*DelegationResult, error) {
	// 1. Resolve the agent
	agentName, ok := step.Input["agent"].(string)
	if !ok || agentName == "" {
		return nil, fmt.Errorf("delegate step %q: agent name required in input", step.ID)
	}

	a, err := cfg.AgentRegistry.Resolve(agentName)
	if err != nil {
		return nil, fmt.Errorf("delegate step %q: %w", step.ID, err)
	}

	// 2. Check policy (if evaluator is configured)
	if cfg.PolicyEvaluator != nil {
		req := policy.PolicyRequest{
			Action: "agent.delegate",
			Resource: policy.Resource{
				Type: "agent",
				Name: agentName,
			},
			Subject: policy.Subject{
				Type: "workflow",
				ID:   "prizm-workflow",
			},
		}
		decision := cfg.PolicyEvaluator.Evaluate(req)
		if decision.Decision == policy.DecisionDenied {
			return nil, fmt.Errorf("delegate step %q: policy denied delegation to %q: %s", step.ID, agentName, decision.Reason)
		}
		// Note: requires_approval is treated as allowed for delegation.
		// Agent-to-agent delegation doesn't create a human approval flow —
		// it's policy-gated, not approval-gated. If you want human approval,
		// add a separate approval step before the delegation step.
	}

	// 3. Emit delegation event
	subtask, _ := input["task"].(string)
	if subtask == "" {
		subtask, _ = step.Input["task"].(string)
	}

	emitEvent(agent.EventAgentDelegated, "workflow-runner", map[string]any{
		"agent_name":   agentName,
		"delegated_by": "workflow",
		"subtask":      subtask,
		"step_id":      step.ID,
	})

	// 4. Call the agent's LLM provider
	p, err := cfg.NewProvider(a.ProviderName)
	if err != nil {
		emitEvent(agent.EventAgentFailed, "workflow-runner", map[string]any{
			"agent_name":  agentName,
			"subtask":     subtask,
			"error":       fmt.Sprintf("provider error: %v", err),
			"duration_ms": 0,
		})
		return nil, fmt.Errorf("delegate step %q: provider error: %w", step.ID, err)
	}

	start := time.Now()
	resp, err := p.Generate(ctx, provider.GenerateRequest{
		Model:       a.Model,
		Prompt:      subtask,
		Temperature: 0.2,
		MaxTokens:   2048,
	})
	durationMs := time.Since(start).Milliseconds()

	if err != nil {
		emitEvent(agent.EventAgentFailed, "workflow-runner", map[string]any{
			"agent_name":  agentName,
			"subtask":     subtask,
			"error":       err.Error(),
			"duration_ms": durationMs,
		})
		return nil, fmt.Errorf("delegate step %q: LLM call failed: %w", step.ID, err)
	}

	// 5. Emit completion event
	emitEvent(agent.EventAgentCompleted, "agent-"+agentName, map[string]any{
		"agent_name":  agentName,
		"subtask":     subtask,
		"output":      resp.Text,
		"duration_ms": durationMs,
	})

	return &DelegationResult{
		AgentName:  agentName,
		Output:     resp.Text,
		DurationMs: durationMs,
	}, nil
}
