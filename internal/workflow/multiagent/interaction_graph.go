package multiagent

import "time"

// interactionPlanForNode translates authored graph execution metadata into
// the bounded adapter cascade. Invalid durations are treated as zero here;
// graph validation remains the authority that rejects malformed definitions.
func interactionPlanForNode(node CompiledNode) InteractionPlan {
	plan := InteractionPlan{}
	if node.Execution == nil {
		return plan
	}
	execution := node.Execution
	lane := InteractionLane(execution.Lane)
	if lane == "" {
		lane = LaneTactical
	}
	phase := InteractionPhase{
		Lane:         lane,
		Cadence:      parseInteractionDuration(execution.Cadence),
		Interrupts:   append([]string(nil), execution.Interrupts...),
		Verification: append([]string(nil), execution.Verification...),
		MaxActions:   1,
	}
	if execution.MaxActions != nil && *execution.MaxActions > 0 {
		phase.MaxActions = int(*execution.MaxActions)
	}
	plan.MaxActions = phase.MaxActions
	plan.Phases = []InteractionPhase{phase}
	if node.RoleConfig.Retry.MaxRetries > 0 {
		plan.MaxRetries = node.RoleConfig.Retry.MaxRetries
	}
	if node.RoleConfig.TimeBudget > 0 && node.RoleConfig.TimeBudget != UnlimitedDuration {
		plan.RunDeadline = node.RoleConfig.TimeBudget
		plan.Deadman = node.RoleConfig.TimeBudget
	}
	return plan
}

func parseInteractionDuration(value string) time.Duration {
	if value == "" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		return 0
	}
	return duration
}
