package multiagent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/retry"
)

// ReferenceWorkflowID is the single supported Phase 1 product workflow.
const ReferenceWorkflowID = "multi-agent-software-task"

// ReferenceWorkflowInput is the user-facing, repository-scoped task contract.
// Role and budget overrides may only narrow the built-in workflow.
type ReferenceWorkflowInput struct {
	Objective          string                   `json:"objective"`
	Workspace          string                   `json:"workspace"`
	Constraints        []string                 `json:"constraints,omitempty"`
	AcceptanceCriteria []string                 `json:"acceptance_criteria,omitempty"`
	RoleProfiles       map[Role]string          `json:"role_profiles,omitempty"`
	Budgets            ReferenceBudgetOverrides `json:"budgets,omitempty"`
}

// ReferenceBudgetOverrides contains optional ceilings. A supplied value must
// be positive and no greater than the corresponding built-in safety limit.
type ReferenceBudgetOverrides struct {
	MaxTransitions              *Limit         `json:"max_transitions,omitempty"`
	MaxVisitsPerRole            map[Role]Limit `json:"max_visits_per_role,omitempty"`
	MaxLocalIterations          *Limit         `json:"max_local_iterations,omitempty"`
	MaxRetries                  *Limit         `json:"max_retries,omitempty"`
	MaxTokens                   *Limit         `json:"max_tokens,omitempty"`
	MaxRepeatedFailure          *Limit         `json:"max_repeated_failures,omitempty"`
	MaxTesterToDeveloperLoops   *Limit         `json:"max_tester_to_developer_loops,omitempty"`
	MaxReviewerToDeveloperLoops *Limit         `json:"max_reviewer_to_developer_loops,omitempty"`
	MaxDuration                 string         `json:"max_duration,omitempty"`
}

// Validate rejects incomplete or ambiguous product input.
func (i ReferenceWorkflowInput) Validate() error {
	var problems []string
	if strings.TrimSpace(i.Objective) == "" {
		problems = append(problems, "objective is required")
	}
	if strings.TrimSpace(i.Workspace) == "" {
		problems = append(problems, "workspace is required")
	}
	for role, profile := range i.RoleProfiles {
		if !role.Valid() {
			problems = append(problems, fmt.Sprintf("unknown role profile override %q", role))
		}
		if strings.TrimSpace(profile) == "" {
			problems = append(problems, fmt.Sprintf("role profile override %q is empty", role))
		}
	}
	if len(problems) != 0 {
		return &ContractError{Problems: problems}
	}
	return nil
}

// DefaultReferenceDefinition returns the fixed Phase 1 graph and conservative
// safety limits. Call ApplyReferenceOverrides before execution.
func DefaultReferenceDefinition() Definition {
	role := func(
		name Role,
		agentRef string,
		iterations Limit,
		tokens Limit,
		duration time.Duration,
		capability string,
		tools []string,
	) RoleConfig {
		retryConfig := retry.DefaultRetryConfig()
		retryConfig.MaxRetries = 1
		return RoleConfig{
			Role:               name,
			AgentRef:           agentRef,
			AllowedTools:       append([]string(nil), tools...),
			Capabilities:       []string{capability},
			MaxLocalIterations: iterations,
			TokenBudget:        tokens,
			TimeBudget:         duration,
			Retry:              retryConfig,
		}
	}

	readTools := []string{
		"list_dir", "read_file", "read_project", "search_files",
		"project_overview", "git_status", "git_log", "git_diff",
	}
	planner := role(RolePlanner, "planner", 3, 8_000, 10*time.Minute, "plan", readTools)
	developerTools := append(append([]string(nil), readTools...),
		"write_file_dry_run", "write_file_proposal", "create_directory_proposal")
	developer := role(RoleDeveloper, "developer", 8, 30_000, 30*time.Minute, "code", developerTools)
	developer.Retry.MaxRetries = 2
	tester := role(RoleTester, "tester", 5, 12_000, 15*time.Minute, "test", readTools)
	tester.ValidationProfiles = []string{"go_test_all"}
	reviewer := role(RoleReviewer, "reviewer", 4, 10_000, 10*time.Minute, "review", readTools)

	return Definition{
		ID:      ReferenceWorkflowID,
		Version: 1,
		Roles:   []RoleConfig{planner, developer, tester, reviewer},
		Transitions: []TransitionRule{
			{From: RolePlanner, Outcome: OutcomePlanReady, To: RoleDeveloper},
			{From: RoleDeveloper, Outcome: OutcomeImplementationReady, To: RoleTester},
			{From: RoleTester, Outcome: OutcomeTestsPassed, To: RoleReviewer},
			{From: RoleTester, Outcome: OutcomeTestsFailed, To: RoleDeveloper},
			{From: RoleReviewer, Outcome: OutcomeChangesRequested, To: RoleDeveloper},
			{From: RoleReviewer, Outcome: OutcomeReviewApproved, Terminal: TerminalConditionCompleted},
			{From: RoleDeveloper, Outcome: OutcomeTerminalFailure, Terminal: TerminalConditionFailed},
			{From: RolePlanner, Outcome: OutcomeCancelled, Terminal: TerminalConditionCancelled},
		},
		Budgets: BudgetLimits{
			MaxTransitions: 12,
			MaxVisitsPerRole: map[Role]Limit{
				RolePlanner:   1,
				RoleDeveloper: 4,
				RoleTester:    3,
				RoleReviewer:  2,
			},
			MaxLocalIterations:          24,
			MaxRetries:                  4,
			MaxTokens:                   60_000,
			MaxRepeatedFailure:          2,
			MaxTesterToDeveloperLoops:   2,
			MaxReviewerToDeveloperLoops: 1,
			MaxDuration:                 45 * time.Minute,
		},
	}
}

// ApplyReferenceOverrides returns a validated copy of the reference workflow.
// Overrides cannot add roles, transitions, tools, capabilities, or budget.
func ApplyReferenceOverrides(
	base Definition,
	input ReferenceWorkflowInput,
) (Definition, error) {
	if err := input.Validate(); err != nil {
		return Definition{}, err
	}
	definition := cloneDefinition(base)
	var problems []string
	for index := range definition.Roles {
		if profile, ok := input.RoleProfiles[definition.Roles[index].Role]; ok {
			definition.Roles[index].AgentRef = strings.TrimSpace(profile)
		}
	}
	developer, _ := definition.RoleConfig(RoleDeveloper)
	reviewer, _ := definition.RoleConfig(RoleReviewer)
	if developer.AgentRef == reviewer.AgentRef {
		problems = append(problems,
			"reviewer agent profile must be distinct from developer agent profile")
	}

	applyLimitOverride := func(name string, override *Limit, target *Limit) {
		if override == nil {
			return
		}
		if *override <= 0 {
			problems = append(problems, name+" must be positive")
			return
		}
		if *override > *target {
			problems = append(problems, fmt.Sprintf(
				"%s %d exceeds safety ceiling %d", name, *override, *target))
			return
		}
		*target = *override
	}
	overrides := input.Budgets
	applyLimitOverride("max_transitions", overrides.MaxTransitions, &definition.Budgets.MaxTransitions)
	applyLimitOverride("max_local_iterations", overrides.MaxLocalIterations, &definition.Budgets.MaxLocalIterations)
	applyLimitOverride("max_retries", overrides.MaxRetries, &definition.Budgets.MaxRetries)
	applyLimitOverride("max_tokens", overrides.MaxTokens, &definition.Budgets.MaxTokens)
	applyLimitOverride("max_repeated_failures", overrides.MaxRepeatedFailure, &definition.Budgets.MaxRepeatedFailure)
	applyLimitOverride(
		"max_tester_to_developer_loops",
		overrides.MaxTesterToDeveloperLoops,
		&definition.Budgets.MaxTesterToDeveloperLoops,
	)
	applyLimitOverride(
		"max_reviewer_to_developer_loops",
		overrides.MaxReviewerToDeveloperLoops,
		&definition.Budgets.MaxReviewerToDeveloperLoops,
	)
	for role, limit := range overrides.MaxVisitsPerRole {
		ceiling, ok := definition.Budgets.MaxVisitsPerRole[role]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("max_visits_per_role contains unknown role %q", role))
		case limit <= 0:
			problems = append(problems, fmt.Sprintf("max_visits_per_role.%s must be positive", role))
		case limit > ceiling:
			problems = append(problems, fmt.Sprintf(
				"max_visits_per_role.%s %d exceeds safety ceiling %d", role, limit, ceiling))
		default:
			definition.Budgets.MaxVisitsPerRole[role] = limit
		}
	}
	if overrides.MaxDuration != "" {
		duration, err := time.ParseDuration(overrides.MaxDuration)
		switch {
		case err != nil:
			problems = append(problems, "max_duration must be a Go duration such as 30m")
		case duration <= 0:
			problems = append(problems, "max_duration must be positive")
		case duration > definition.Budgets.MaxDuration:
			problems = append(problems, fmt.Sprintf(
				"max_duration %s exceeds safety ceiling %s",
				duration, definition.Budgets.MaxDuration,
			))
		default:
			definition.Budgets.MaxDuration = duration
		}
	}
	if len(problems) != 0 {
		return Definition{}, &ContractError{Problems: problems}
	}
	if err := definition.Validate(); err != nil {
		return Definition{}, err
	}
	return definition, nil
}

// ReferenceTaskDescription renders structured user input into the bounded task
// description consumed by existing role prompts.
func ReferenceTaskDescription(input ReferenceWorkflowInput) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(input.Objective))
	appendItems := func(label string, items []string) {
		if len(items) == 0 {
			return
		}
		builder.WriteString("\n\n")
		builder.WriteString(label)
		for _, item := range items {
			if item = strings.TrimSpace(item); item != "" {
				builder.WriteString("\n- ")
				builder.WriteString(item)
			}
		}
	}
	appendItems("Constraints:", input.Constraints)
	appendItems("Acceptance criteria:", input.AcceptanceCriteria)
	return builder.String()
}

// ReferenceRunSnapshot is the stable inspection representation used by CLI
// text and JSON output.
type ReferenceRunSnapshot struct {
	RunID           string                  `json:"run_id"`
	WorkflowID      string                  `json:"workflow_id"`
	Status          RunStatus               `json:"status"`
	CurrentRole     Role                    `json:"current_role,omitempty"`
	WorkspaceID     string                  `json:"workspace_id,omitempty"`
	CompletedRoles  []Role                  `json:"completed_roles"`
	LatestHandoff   *Handoff                `json:"latest_handoff,omitempty"`
	RoleStates      map[Role]RoleState      `json:"role_states"`
	TransitionCount int                     `json:"transition_count"`
	LoopTraversals  LoopTraversalCounts     `json:"loop_traversals"`
	BudgetUsage     BudgetUsage             `json:"budget_usage"`
	TerminalOutcome *TerminalOutcome        `json:"terminal_outcome,omitempty"`
	RecentEvents    []ReferenceEventSummary `json:"recent_events"`
}

// ReferenceEventSummary is the safe event subset shown by inspection.
type ReferenceEventSummary struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// BuildReferenceRunSnapshot derives a deterministic inspection view.
func BuildReferenceRunSnapshot(state RunState, events []event.Event) ReferenceRunSnapshot {
	completed := make([]Role, 0, len(state.RoleStates))
	roleStates := make(map[Role]RoleState, len(state.RoleStates))
	for role, roleState := range state.RoleStates {
		roleStates[role] = cloneRoleState(roleState)
		if roleState.Status == RoleStatusCompleted {
			completed = append(completed, role)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		return roleOrder(completed[i]) < roleOrder(completed[j])
	})
	recent := make([]ReferenceEventSummary, 0, len(events))
	for _, evt := range events {
		recent = append(recent, ReferenceEventSummary{
			ID: evt.ID, Type: evt.Type, Timestamp: evt.Timestamp, Payload: evt.Payload,
		})
	}
	return ReferenceRunSnapshot{
		RunID:           state.RunID,
		WorkflowID:      state.WorkflowID,
		Status:          state.Status,
		CurrentRole:     state.CurrentRole,
		WorkspaceID:     state.WorkspaceID,
		CompletedRoles:  completed,
		LatestHandoff:   cloneHandoff(state.LatestHandoff),
		RoleStates:      roleStates,
		TransitionCount: state.TransitionCount,
		LoopTraversals:  state.LoopTraversals,
		BudgetUsage:     state.BudgetUsage,
		TerminalOutcome: cloneTerminalOutcome(state.TerminalOutcome),
		RecentEvents:    recent,
	}
}

// ReferenceRunReport is the structured Phase 1 completion artifact.
type ReferenceRunReport struct {
	RunID                 string               `json:"run_id"`
	OriginalObjective     string               `json:"original_objective"`
	PlanSummary           string               `json:"plan_summary"`
	ImplementationSummary string               `json:"implementation_summary"`
	Artifacts             []ArtifactRef        `json:"artifacts"`
	TestsExecuted         []string             `json:"tests_executed"`
	TestOutcome           string               `json:"test_outcome"`
	ReviewOutcome         TransitionOutcome    `json:"review_outcome,omitempty"`
	LoopHistory           []ReferenceLoopEntry `json:"loop_history"`
	BudgetUsage           BudgetUsage          `json:"budget_usage"`
	Warnings              []string             `json:"warnings"`
	FinalStatus           RunStatus            `json:"final_status"`
	TerminalOutcome       *TerminalOutcome     `json:"terminal_outcome,omitempty"`
}

// ReferenceLoopEntry is one canonical correction traversal.
type ReferenceLoopEntry struct {
	Source      Role              `json:"source"`
	Destination Role              `json:"destination"`
	Outcome     TransitionOutcome `json:"outcome"`
	Count       int               `json:"count"`
}

// BuildReferenceRunReport derives the final report from durable state and
// canonical events. It does not depend on provider output or paid model calls.
func BuildReferenceRunReport(
	input ReferenceWorkflowInput,
	state RunState,
	events []event.Event,
) ReferenceRunReport {
	planner := state.RoleStates[RolePlanner]
	developer := state.RoleStates[RoleDeveloper]
	tester := state.RoleStates[RoleTester]
	reviewer := state.RoleStates[RoleReviewer]

	report := ReferenceRunReport{
		RunID:             state.RunID,
		OriginalObjective: strings.TrimSpace(input.Objective),
		PlanSummary: fmt.Sprintf(
			"Planner completed %d visit(s) with outcome %s.", planner.Visits, planner.LastOutcome),
		ImplementationSummary: fmt.Sprintf(
			"Developer completed %d visit(s) with outcome %s.", developer.Visits, developer.LastOutcome),
		TestOutcome:     firstNonEmpty(tester.ValidationStatus, string(tester.LastOutcome)),
		ReviewOutcome:   reviewer.LastOutcome,
		BudgetUsage:     state.BudgetUsage,
		FinalStatus:     state.Status,
		TerminalOutcome: cloneTerminalOutcome(state.TerminalOutcome),
	}
	if tester.Visits > 0 {
		report.TestsExecuted = []string{"configured validation profiles"}
	}
	seenArtifacts := make(map[string]bool)
	for _, role := range Phase1Roles() {
		for _, artifact := range state.RoleStates[role].Artifacts {
			key := string(artifact.Kind) + "\x00" + artifact.URI
			if !seenArtifacts[key] {
				seenArtifacts[key] = true
				report.Artifacts = append(report.Artifacts, artifact)
			}
		}
	}
	for _, evt := range events {
		if evt.Type != event.EventMultiAgentLoopTraversal {
			continue
		}
		report.LoopHistory = append(report.LoopHistory, ReferenceLoopEntry{
			Source:      Role(payloadString(evt.Payload, "source_role")),
			Destination: Role(payloadString(evt.Payload, "destination_role")),
			Outcome:     TransitionOutcome(payloadString(evt.Payload, "outcome")),
			Count:       payloadInt(evt.Payload, "transition_count"),
		})
	}
	if state.Status == RunStatusPaused {
		report.Warnings = append(report.Warnings, "run is paused and requires an external decision or reconciliation")
	}
	if state.Status == RunStatusBudgetExhausted {
		report.Warnings = append(report.Warnings, "a configured safety budget was exhausted")
	}
	if state.CancellationReason != "" {
		report.Warnings = append(report.Warnings, "run cancelled: "+state.CancellationReason)
	}
	return report
}

func cloneRoleState(state RoleState) RoleState {
	cloned := state
	cloned.EnteredAt = cloneTimePointer(state.EnteredAt)
	cloned.CompletedAt = cloneTimePointer(state.CompletedAt)
	cloned.Artifacts = cloneArtifactRefs(state.Artifacts)
	return cloned
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func roleOrder(role Role) int {
	for index, candidate := range Phase1Roles() {
		if role == candidate {
			return index
		}
	}
	return len(Phase1Roles())
}

func cloneTerminalOutcome(outcome *TerminalOutcome) *TerminalOutcome {
	if outcome == nil {
		return nil
	}
	cloned := *outcome
	return &cloned
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "not_recorded"
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func payloadInt(payload map[string]any, key string) int {
	switch value := payload[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
