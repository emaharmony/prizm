package multiagent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/retry"
)

// Definition is the stable, serializable contract consumed by a future
// supervisor. It describes valid roles and transitions but performs no
// execution.
type Definition struct {
	ID                   string           `json:"id"`
	Version              int              `json:"version"`
	Roles                []RoleConfig     `json:"roles"`
	Transitions          []TransitionRule `json:"transitions"`
	Budgets              BudgetLimits     `json:"budgets"`
	AllowSelfTransitions bool             `json:"allow_self_transitions,omitempty"`
}

// RoleConfig binds one logical role to an existing Prizm agent or profile.
type RoleConfig struct {
	Role               Role                `json:"role"`
	AgentRef           string              `json:"agent_ref"`
	AllowedTools       []string            `json:"allowed_tools,omitempty"`
	Capabilities       []string            `json:"capabilities,omitempty"`
	MaxLocalIterations Limit               `json:"max_local_iterations"`
	TokenBudget        Limit               `json:"token_budget"`
	TimeBudget         time.Duration       `json:"time_budget"`
	Retry              retry.RetryConfig   `json:"retry"`
	Approval           ApprovalRequirement `json:"approval"`
	ValidationProfiles []string            `json:"validation_profiles,omitempty"`
}

// ApprovalRequirement describes workflow-level approval routing. The approval
// package remains the authority for recording and resolving human decisions.
type ApprovalRequirement struct {
	Required  bool   `json:"required"`
	Approvers []Role `json:"approvers,omitempty"`
}

// BudgetLimits bounds the whole run. Per-role limits in RoleConfig may be
// stricter; neither layer may widen the other at execution time.
type BudgetLimits struct {
	MaxTransitions              Limit          `json:"max_transitions"`
	MaxVisitsPerRole            map[Role]Limit `json:"max_visits_per_role"`
	MaxLocalIterations          Limit          `json:"max_local_iterations"`
	MaxRetries                  Limit          `json:"max_retries"`
	MaxTokens                   Limit          `json:"max_tokens"`
	MaxRepeatedFailure          Limit          `json:"max_repeated_failures"`
	MaxTesterToDeveloperLoops   Limit          `json:"max_tester_to_developer_loops"`
	MaxReviewerToDeveloperLoops Limit          `json:"max_reviewer_to_developer_loops"`
	MaxDuration                 time.Duration  `json:"max_duration"`
}

// TransitionRule maps a typed role outcome to either another role or a
// terminal condition. Exactly one of To or Terminal must be set.
type TransitionRule struct {
	From     Role              `json:"from"`
	Outcome  TransitionOutcome `json:"outcome"`
	To       Role              `json:"to,omitempty"`
	Terminal TerminalCondition `json:"terminal,omitempty"`
}

// ContractError reports one or more deterministic contract violations.
type ContractError struct {
	Problems []string
}

func (e *ContractError) Error() string {
	return "multiagent: invalid contract: " + strings.Join(e.Problems, "; ")
}

// Validate enforces Definition invariants without executing a workflow.
func (d Definition) Validate() error {
	var problems []string

	if strings.TrimSpace(d.ID) == "" {
		problems = append(problems, "definition id is required")
	}
	if d.Version <= 0 {
		problems = append(problems, "definition version must be positive")
	}
	if len(d.Roles) == 0 {
		problems = append(problems, "at least one role configuration is required")
	}

	roleConfigs := make(map[Role]RoleConfig, len(d.Roles))
	for i, cfg := range d.Roles {
		prefix := fmt.Sprintf("roles[%d]", i)
		if !cfg.Role.Valid() {
			problems = append(problems, fmt.Sprintf("%s: unknown role %q", prefix, cfg.Role))
		} else if _, exists := roleConfigs[cfg.Role]; exists {
			problems = append(problems, fmt.Sprintf("%s: duplicate role %q", prefix, cfg.Role))
		}
		roleConfigs[cfg.Role] = cfg
		validateRoleConfig(prefix, cfg, &problems)
	}

	for _, role := range Phase1Roles() {
		if _, exists := roleConfigs[role]; !exists {
			problems = append(problems, fmt.Sprintf("required role %q has no configuration", role))
		}
	}

	validateBudgets(d.Budgets, roleConfigs, &problems)

	if len(d.Transitions) == 0 {
		problems = append(problems, "at least one transition rule is required")
	}
	seenTransitions := make(map[string]struct{}, len(d.Transitions))
	for i, rule := range d.Transitions {
		prefix := fmt.Sprintf("transitions[%d]", i)
		validateTransitionRule(prefix, rule, roleConfigs, d.AllowSelfTransitions, &problems)
		key := string(rule.From) + "\x00" + string(rule.Outcome)
		if _, exists := seenTransitions[key]; exists {
			problems = append(problems, fmt.Sprintf("%s: duplicate route for role %q and outcome %q", prefix, rule.From, rule.Outcome))
		}
		seenTransitions[key] = struct{}{}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return &ContractError{Problems: problems}
	}
	return nil
}

// RoleConfig returns the configuration for a logical role.
func (d Definition) RoleConfig(role Role) (RoleConfig, bool) {
	for _, cfg := range d.Roles {
		if cfg.Role == role {
			return cfg, true
		}
	}
	return RoleConfig{}, false
}

// ValidateTransition verifies that a selected transition is declared by the
// definition. It does not select the transition.
func (d Definition) ValidateTransition(from Role, outcome TransitionOutcome, to Role, terminal TerminalCondition) error {
	if !from.Valid() {
		return fmt.Errorf("multiagent: unknown source role %q", from)
	}
	if !outcome.Valid() {
		return fmt.Errorf("multiagent: unsupported outcome %q", outcome)
	}
	if !outcome.ValidFor(from) {
		return fmt.Errorf("multiagent: outcome %q is invalid for role %q", outcome, from)
	}
	for _, rule := range d.Transitions {
		if rule.From == from && rule.Outcome == outcome && rule.To == to && rule.Terminal == terminal {
			return nil
		}
	}
	return fmt.Errorf(
		"multiagent: transition from %q with outcome %q to %q terminal %q is not declared",
		from,
		outcome,
		to,
		terminal,
	)
}

func validateRoleConfig(prefix string, cfg RoleConfig, problems *[]string) {
	if strings.TrimSpace(cfg.AgentRef) == "" {
		*problems = append(*problems, prefix+": agent_ref is required")
	}
	validateLimit(prefix+".max_local_iterations", cfg.MaxLocalIterations, problems)
	validateLimit(prefix+".token_budget", cfg.TokenBudget, problems)
	if !validDuration(cfg.TimeBudget) {
		*problems = append(*problems, prefix+".time_budget must be positive or -1 (unlimited)")
	}
	validateRetry(prefix+".retry", cfg.Retry, problems)
	validateStringSet(prefix+".allowed_tools", cfg.AllowedTools, problems)
	validateStringSet(prefix+".capabilities", cfg.Capabilities, problems)
	validateStringSet(prefix+".validation_profiles", cfg.ValidationProfiles, problems)

	seenApprovers := make(map[Role]struct{}, len(cfg.Approval.Approvers))
	if cfg.Approval.Required && len(cfg.Approval.Approvers) == 0 {
		*problems = append(*problems, prefix+": required approval must name at least one approver role")
	}
	for _, approver := range cfg.Approval.Approvers {
		if !approver.Valid() {
			*problems = append(*problems, fmt.Sprintf("%s: unknown approver role %q", prefix, approver))
		}
		if approver == cfg.Role {
			*problems = append(*problems, fmt.Sprintf("%s: role %q cannot approve its own work", prefix, cfg.Role))
		}
		if _, exists := seenApprovers[approver]; exists {
			*problems = append(*problems, fmt.Sprintf("%s: duplicate approver role %q", prefix, approver))
		}
		seenApprovers[approver] = struct{}{}
	}
}

func validateBudgets(b BudgetLimits, roles map[Role]RoleConfig, problems *[]string) {
	validateLimit("budgets.max_transitions", b.MaxTransitions, problems)
	validateLimit("budgets.max_local_iterations", b.MaxLocalIterations, problems)
	validateLimit("budgets.max_retries", b.MaxRetries, problems)
	validateLimit("budgets.max_tokens", b.MaxTokens, problems)
	validateLimit("budgets.max_repeated_failures", b.MaxRepeatedFailure, problems)
	validateLimit("budgets.max_tester_to_developer_loops", b.MaxTesterToDeveloperLoops, problems)
	validateLimit("budgets.max_reviewer_to_developer_loops", b.MaxReviewerToDeveloperLoops, problems)
	if !validDuration(b.MaxDuration) {
		*problems = append(*problems, "budgets.max_duration must be positive or -1 (unlimited)")
	}
	for role, limit := range b.MaxVisitsPerRole {
		if _, exists := roles[role]; !exists {
			*problems = append(*problems, fmt.Sprintf("budgets.max_visits_per_role references unknown role %q", role))
		}
		validateLimit(fmt.Sprintf("budgets.max_visits_per_role[%q]", role), limit, problems)
	}
	for role := range roles {
		if _, exists := b.MaxVisitsPerRole[role]; !exists {
			*problems = append(*problems, fmt.Sprintf("budgets.max_visits_per_role has no limit for role %q", role))
		}
	}
}

func validateTransitionRule(
	prefix string,
	rule TransitionRule,
	roles map[Role]RoleConfig,
	allowSelf bool,
	problems *[]string,
) {
	if _, exists := roles[rule.From]; !exists {
		*problems = append(*problems, fmt.Sprintf("%s: unknown source role %q", prefix, rule.From))
	}
	if !rule.Outcome.Valid() {
		*problems = append(*problems, fmt.Sprintf("%s: unsupported outcome %q", prefix, rule.Outcome))
	} else if !rule.Outcome.ValidFor(rule.From) {
		*problems = append(*problems, fmt.Sprintf("%s: outcome %q is invalid for role %q", prefix, rule.Outcome, rule.From))
	}

	hasDestination := rule.To != ""
	hasTerminal := rule.Terminal != ""
	if hasDestination == hasTerminal {
		*problems = append(*problems, prefix+": exactly one of to or terminal must be set")
		return
	}
	if hasDestination {
		if _, exists := roles[rule.To]; !exists {
			*problems = append(*problems, fmt.Sprintf("%s: unknown destination role %q", prefix, rule.To))
		}
		if rule.To == rule.From && !allowSelf {
			*problems = append(*problems, fmt.Sprintf("%s: self-transition for role %q is not allowed", prefix, rule.From))
		}
		if rule.Outcome == OutcomeReviewApproved ||
			rule.Outcome == OutcomeTerminalFailure ||
			rule.Outcome == OutcomeCancelled {
			*problems = append(*problems, fmt.Sprintf("%s: outcome %q must be terminal", prefix, rule.Outcome))
		}
	}
	if hasTerminal {
		if !rule.Terminal.Valid() {
			*problems = append(*problems, fmt.Sprintf("%s: unknown terminal condition %q", prefix, rule.Terminal))
		}
		switch rule.Outcome {
		case OutcomeReviewApproved:
			if rule.Terminal != TerminalConditionCompleted {
				*problems = append(*problems, prefix+": review_approved must terminate as completed")
			}
		case OutcomeTerminalFailure:
			if rule.Terminal != TerminalConditionFailed {
				*problems = append(*problems, prefix+": terminal_failure must terminate as failed")
			}
		case OutcomeCancelled:
			if rule.Terminal != TerminalConditionCancelled {
				*problems = append(*problems, prefix+": cancelled must terminate as cancelled")
			}
		default:
			*problems = append(*problems, fmt.Sprintf("%s: outcome %q cannot terminate a run", prefix, rule.Outcome))
		}
	}
}

func validateLimit(name string, limit Limit, problems *[]string) {
	if !limit.Valid() {
		*problems = append(*problems, name+" must be positive or -1 (unlimited)")
	}
}

func validateRetry(prefix string, cfg retry.RetryConfig, problems *[]string) {
	if cfg.MaxRetries < 0 {
		*problems = append(*problems, prefix+".max_retries must be non-negative")
	}
	if cfg.BaseDelay < 0 {
		*problems = append(*problems, prefix+".base_delay must be non-negative")
	}
	if cfg.MaxDelay < 0 {
		*problems = append(*problems, prefix+".max_delay must be non-negative")
	}
	if cfg.Jitter < 0 {
		*problems = append(*problems, prefix+".jitter must be non-negative")
	}
	if cfg.BaseDelay > 0 && cfg.MaxDelay > 0 && cfg.MaxDelay < cfg.BaseDelay {
		*problems = append(*problems, prefix+".max_delay must be greater than or equal to base_delay")
	}
}

func validateStringSet(prefix string, values []string, problems *[]string) {
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			*problems = append(*problems, fmt.Sprintf("%s[%d] must not be empty", prefix, i))
			continue
		}
		if _, exists := seen[value]; exists {
			*problems = append(*problems, fmt.Sprintf("%s contains duplicate value %q", prefix, value))
		}
		seen[value] = struct{}{}
	}
}
