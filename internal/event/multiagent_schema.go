package event

func init() {
	// Register multi-agent payload contracts with the existing opt-in event
	// validator. This extends the canonical schema map rather than creating a
	// second validation or event system.
	Schemas[EventMultiAgentRunCreated] = Schema{
		Required: []string{"run_id", "workflow_id", "status"},
	}
	Schemas[EventMultiAgentRunStarted] = Schema{
		Required: []string{"run_id", "workflow_id", "status"},
	}
	Schemas[EventMultiAgentRoleEntered] = Schema{
		Required: []string{"run_id", "workflow_id", "role", "status", "visit"},
	}
	Schemas[EventMultiAgentRoleIterationStart] = Schema{
		Required: []string{"run_id", "workflow_id", "role", "status", "iteration"},
	}
	Schemas[EventMultiAgentRoleIterationDone] = Schema{
		Required: []string{"run_id", "workflow_id", "role", "status", "iteration"},
		Optional: []string{
			"outcome",
			"token_usage",
			"error",
			"artifact_uris",
			"agent_ref",
			"provider",
			"model",
			"duration_ms",
			"tool_calls",
			"denied_tool_calls",
			"validation_status",
			"approval_status",
		},
	}
	Schemas[EventMultiAgentRoleCompleted] = Schema{
		Required: []string{"run_id", "workflow_id", "role", "status", "outcome"},
		Optional: []string{
			"token_usage",
			"error",
			"artifact_uris",
			"agent_ref",
			"provider",
			"model",
			"duration_ms",
			"tool_calls",
			"denied_tool_calls",
			"validation_status",
			"approval_status",
		},
	}
	Schemas[EventMultiAgentHandoffCreated] = Schema{
		Required: []string{
			"run_id",
			"workflow_id",
			"handoff_id",
			"source_role",
			"destination_role",
			"task_ref",
			"outcome",
		},
	}
	Schemas[EventMultiAgentTransitionSelected] = Schema{
		Required: []string{"run_id", "workflow_id", "source_role", "outcome", "transition_count"},
		Optional: []string{"destination_role", "terminal_condition"},
	}
	Schemas[EventMultiAgentLoopTraversal] = Schema{
		Required: []string{"run_id", "workflow_id", "source_role", "outcome", "transition_count"},
		Optional: []string{"destination_role", "terminal_condition"},
	}
	Schemas[EventMultiAgentBudgetWarning] = Schema{
		Required: []string{"run_id", "workflow_id", "budget", "used", "limit"},
		Optional: []string{"role", "reason"},
	}
	Schemas[EventMultiAgentBudgetExhausted] = Schema{
		Required: []string{"run_id", "workflow_id", "budget", "used", "limit"},
		Optional: []string{"role", "reason"},
	}
	Schemas[EventMultiAgentRunPaused] = Schema{
		Required: []string{"run_id", "workflow_id", "status"},
		Optional: []string{"reason"},
	}
	Schemas[EventMultiAgentRunResumed] = Schema{
		Required: []string{"run_id", "workflow_id", "status"},
		Optional: []string{"reason"},
	}
	Schemas[EventMultiAgentRunCompleted] = Schema{
		Required: []string{"run_id", "workflow_id", "status", "terminal_condition"},
		Optional: []string{"reason"},
	}
	Schemas[EventMultiAgentRunFailed] = Schema{
		Required: []string{"run_id", "workflow_id", "status", "terminal_condition", "reason"},
	}
	Schemas[EventMultiAgentRunCancelled] = Schema{
		Required: []string{"run_id", "workflow_id", "status", "terminal_condition", "reason"},
	}
}
