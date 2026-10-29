package event

func init() {
	Schemas[EventFreewillWakeClaimed] = Schema{Required: []string{"run_id", "wake_id", "attempt", "lease_until"}}
	Schemas[EventFreewillWakeReleased] = Schema{Required: []string{"run_id", "wake_id", "attempt"}, Optional: []string{"reason"}}
	Schemas[EventFreewillSelectionStarted] = Schema{Required: []string{"run_id", "wake_id", "attempt"}}
	Schemas[EventFreewillSelectionCommitted] = Schema{Required: []string{"run_id", "wake_id", "attempt", "lifecycle", "candidate_count", "choice_id"}, Optional: []string{"candidate_ids", "candidate_budgets", "reason_summary", "sleep_reason", "outcome", "provider", "model", "prompt_tokens", "completion_tokens", "total_tokens", "recovery_count"}}
	Schemas[EventFreewillSelectionRetryScheduled] = Schema{Required: []string{"run_id", "wake_id", "attempt", "recovery_count", "max_recoveries", "failure_class", "failure_code", "next_at"}}
	Schemas[EventFreewillSelectionHalted] = Schema{Required: []string{"run_id", "wake_id", "attempt", "recovery_count", "failure_class", "failure_code", "reason"}}
	Schemas[EventFreewillApprovalProposed] = Schema{Required: []string{"run_id", "request_id", "approval_id", "request_fingerprint", "execution_disabled"}}
	Schemas[EventFreewillApprovalDecided] = Schema{Required: []string{"run_id", "request_id", "approval_id", "decision", "lifecycle", "execution_disabled"}, Optional: []string{"policy_rule_id"}}
	Schemas[EventFreewillApprovalReconciled] = Schema{Required: []string{"run_id", "request_id", "approval_id", "decision", "lifecycle", "execution_disabled"}, Optional: []string{"policy_rule_id", "reason_code"}}
	Schemas[EventFreewillPolicyDenied] = Schema{Required: []string{"run_id", "request_id", "policy_rule_id", "reason_code", "execution_disabled"}}
	Schemas[EventProposalApplyFailed] = Schema{
		Required: []string{"run_id", "proposal_id", "approval_id", "apply_key", "execution_key", "workspace_id", "reason"},
		Optional: []string{"reconcile_code", "observed_tree", "inspection_stage", "inspection_category", "inspection_exit_code"},
	}
	// Register multi-agent payload contracts with the existing opt-in event
	// validator. This extends the canonical schema map rather than creating a
	// second validation or event system.
	Schemas[EventMultiAgentRunCreated] = Schema{
		Required: []string{"run_id", "workflow_id", "status"},
	}
	Schemas[EventProposalApplyFailed] = Schema{
		Required: []string{"run_id", "proposal_id", "approval_id", "apply_key", "execution_key", "workspace_id", "reason"},
		Optional: []string{"reconcile_code", "observed_tree"},
	}
	for _, eventType := range []string{EventMultiAgentFanoutStarted, EventMultiAgentFanoutChildDispatched, EventMultiAgentFanoutChildCompleted, EventMultiAgentFanoutJoined} {
		Schemas[eventType] = Schema{Required: []string{"run_id", "join_id"}, Optional: []string{"lane", "role", "status", "reason", "child_id", "delivery_key", "child_summaries", "attempt_id", "failure_class", "failure_code", "failure_origin", "failure_reason", "retryable", "recovery_number", "max_recoveries", "correction_field", "expected_type", "allowed_values"}}
	}
	Schemas[EventMultiAgentRetryScheduled] = Schema{
		Required: []string{"run_id", "workflow_id", "role", "execution_key", "visit_id", "attempt_id", "attempt", "recovery_number", "next_at", "failure_class", "failure_code"},
		Optional: []string{"failure_origin", "failure_reason", "correction_field", "expected_type", "allowed_values", "provider_status", "retry_after_ms"},
	}
	Schemas[EventMultiAgentRetryAttemptStarted] = Schema{Required: []string{"run_id", "workflow_id", "role", "execution_key", "visit_id", "attempt_id", "attempt", "recovery_number"}}
	Schemas[EventMultiAgentRetryExhausted] = Schema{Required: []string{"run_id", "workflow_id", "role", "execution_key", "visit_id", "attempt_id", "attempt", "recovery_number", "failure_class", "failure_code"}, Optional: []string{"failure_origin", "provider_status", "retry_after_ms"}}
	Schemas[EventMultiAgentToolDiagnostic] = Schema{Required: []string{"run_id", "workflow_id", "role", "execution_key", "attempt_id", "tool", "status", "sequence"}, Optional: []string{"error_code", "duration_ms", "safe_ref", "observation_hash", "mutation_possible", "mutation_id", "no_effects_proven"}}
	for _, eventType := range []string{EventDashboardTaskCompleted, EventDashboardTaskFailed} {
		Schemas[eventType] = Schema{Required: []string{"run_id", "status", "report_ref", "requester_id", "session_id"}, Optional: []string{"idempotency_key"}}
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
		Optional: []string{"attempt_id", "attempt", "recovery_number", "max_recoveries", "failure_class", "failure_code", "failure_origin", "failure_reason", "retryable", "retry_exhausted", "provider_status", "retry_after_ms"},
	}
	Schemas[EventMultiAgentRunCancelled] = Schema{
		Required: []string{"run_id", "workflow_id", "status", "terminal_condition", "reason"},
	}
	Schemas[EventMultiAgentRecoveryStarted] = Schema{
		Required: []string{"run_id", "workflow_id", "status"},
		Optional: []string{"reason"},
	}
	Schemas[EventMultiAgentRecoveryCompleted] = Schema{
		Required: []string{"run_id", "workflow_id", "status"},
		Optional: []string{"reason", "terminal_condition"},
	}
	Schemas[EventMultiAgentRecoveryFailed] = Schema{
		Required: []string{"run_id", "workflow_id", "status", "reason"},
		Optional: []string{"terminal_condition"},
	}
	for _, eventType := range []string{EventValidationStarted, EventValidationCompleted, EventValidationFailed} {
		Schemas[eventType] = Schema{
			Required: []string{"run_id", "workflow_id", "role", "profile", "status"},
			Optional: []string{"error"},
		}
	}
	for _, eventType := range []string{
		EventInteractionObserved,
		EventInteractionDecisionSelected,
		EventInteractionActionValidated,
		EventInteractionActionRejected,
		EventInteractionActionExecuted,
		EventInteractionActionFailed,
		EventInteractionVerification,
		EventInteractionNeutralized,
		EventInteractionApprovalPaused,
		EventInteractionResumed,
	} {
		Schemas[eventType] = Schema{
			Required: []string{"run_id", "adapter", "step"},
			Optional: []string{"action", "lane", "reason", "success", "error", "observation_id"},
		}
	}
	for _, eventType := range []string{
		EventReflectionRequested,
		EventReflectionCompleted,
		EventReflectionReplanRequested,
		EventReflectionMemoryAccepted,
		EventReflectionMemoryRejected,
		EventReflectionFailed,
	} {
		Schemas[eventType] = Schema{
			Required: []string{"run_id", "trigger"},
			Optional: []string{"role", "verdict", "confidence", "reason", "status"},
		}
	}
}
