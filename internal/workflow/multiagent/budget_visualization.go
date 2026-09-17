package multiagent

import "time"

// budgetApproachingPercent and budgetCriticalPercent are the product default
// thresholds for BudgetLevel, expressed as integer percentages of a
// dimension's configured limit. They are deliberately unexported: tuning
// them is a product decision, not part of this package's public contract.
const (
	budgetApproachingPercent = 75
	budgetCriticalPercent    = 90
)

// BudgetLevel is a coarse, dashboard-facing severity for one budget
// dimension, derived from Used against Limit.
type BudgetLevel string

const (
	BudgetNormal      BudgetLevel = "normal"
	BudgetApproaching BudgetLevel = "approaching"
	BudgetCritical    BudgetLevel = "critical"
	// BudgetExhaustedLevel is named with a "Level" suffix (rather than
	// BudgetExhausted) to avoid colliding with RunGraph.BudgetExhausted,
	// which is a run-level overlay flag, not a BudgetLevel value.
	BudgetExhaustedLevel BudgetLevel = "exhausted"
)

// BudgetDimension is one measured quantity against its configured ceiling.
// Limit is nil when the dimension is explicitly unlimited (Limit ==
// Unlimited); in that case Level is always BudgetNormal.
type BudgetDimension struct {
	Used  int         `json:"used"`
	Limit *int        `json:"limit,omitempty"`
	Level BudgetLevel `json:"level"`
}

// BudgetVisualization is the complete, dashboard-facing rendering of a run's
// budget consumption against its configured BudgetLimits.
type BudgetVisualization struct {
	Transitions      BudgetDimension              `json:"transitions"`
	Tokens           BudgetDimension              `json:"tokens"`
	LocalIterations  BudgetDimension              `json:"local_iterations"`
	RepeatedFailures BudgetDimension              `json:"repeated_failures"`
	Elapsed          BudgetDimension              `json:"elapsed"` // seconds
	RoleVisits       map[Role]BudgetDimension     `json:"role_visits"`
	LoopTraversals   map[LoopKind]BudgetDimension `json:"loop_traversals"`
}

// BuildBudgetVisualization derives the dashboard's budget rendering from
// persisted usage and the workflow's configured ceilings.
//
// Deviation from the originally sketched signature (usage, limits, loops,
// elapsed): neither BudgetUsage (state.go) nor any other parameter carries
// the run's total transition count or per-role visit counts — those live
// only on RunState (TransitionCount) and RoleState (Visits), which this
// function otherwise has no reason to depend on. transitionCount and
// roleVisits are added as explicit parameters; callers (BuildDashboardSnapshot)
// extract them from RunState, keeping this function itself free of any
// direct RunState/RoleState dependency.
func BuildBudgetVisualization(
	usage BudgetUsage,
	limits BudgetLimits,
	loops LoopTraversalCounts,
	elapsed time.Duration,
	transitionCount int,
	roleVisits map[Role]int,
) BudgetVisualization {
	viz := BudgetVisualization{
		Transitions:      limitDimension(transitionCount, limits.MaxTransitions),
		Tokens:           limitDimension(usage.Tokens.TotalTokens, limits.MaxTokens),
		LocalIterations:  limitDimension(usage.TotalLocalIterations, limits.MaxLocalIterations),
		RepeatedFailures: limitDimension(usage.RepeatedFailures, limits.MaxRepeatedFailure),
		Elapsed:          durationDimension(elapsed, limits.MaxDuration),
		RoleVisits:       make(map[Role]BudgetDimension, len(roleVisits)),
		LoopTraversals: map[LoopKind]BudgetDimension{
			LoopTesterToDeveloper:   limitDimension(loops.Get(LoopTesterToDeveloper), limits.MaxTesterToDeveloperLoops),
			LoopReviewerToDeveloper: limitDimension(loops.Get(LoopReviewerToDeveloper), limits.MaxReviewerToDeveloperLoops),
		},
	}
	for role, visits := range roleVisits {
		limit, ok := limits.MaxVisitsPerRole[role]
		if !ok {
			limit = Unlimited
		}
		viz.RoleVisits[role] = limitDimension(visits, limit)
	}
	return viz
}

// limitDimension renders one Used/Limit pair using the Limit sentinel
// contract: Unlimited (-1) always renders as {Limit: nil, Level: Normal}.
func limitDimension(used int, limit Limit) BudgetDimension {
	if limit == Unlimited {
		return BudgetDimension{Used: used, Limit: nil, Level: BudgetNormal}
	}
	value := int(limit)
	return BudgetDimension{Used: used, Limit: &value, Level: budgetLevel(used, value)}
}

// durationDimension renders elapsed/maxDuration in whole seconds.
// UnlimitedDuration (-1) always renders as {Limit: nil, Level: Normal}.
func durationDimension(elapsed, maxDuration time.Duration) BudgetDimension {
	used := int(elapsed.Seconds())
	if maxDuration == UnlimitedDuration {
		return BudgetDimension{Used: used, Limit: nil, Level: BudgetNormal}
	}
	limitSeconds := int(maxDuration.Seconds())
	return BudgetDimension{Used: used, Limit: &limitSeconds, Level: budgetLevel(used, limitSeconds)}
}

// budgetLevel classifies used against a positive limit using integer
// arithmetic so boundary percentages (75%, 90%, 100%) are exact regardless
// of floating-point rounding.
func budgetLevel(used, limit int) BudgetLevel {
	if limit <= 0 {
		// A non-positive limit cannot express a meaningful percentage; treat
		// it as unlimited rather than divide by zero or report a false
		// "exhausted" state. Definition.Validate() prevents this in
		// practice for any persisted workflow definition.
		return BudgetNormal
	}
	switch {
	case used >= limit:
		return BudgetExhaustedLevel
	case used*100 >= limit*budgetCriticalPercent:
		return BudgetCritical
	case used*100 >= limit*budgetApproachingPercent:
		return BudgetApproaching
	default:
		return BudgetNormal
	}
}
