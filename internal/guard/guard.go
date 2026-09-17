// Package guard implements the V32 Guard Rail — a lightweight check layer
// that enforces process rules before code execution.
//
// The guard rail is NOT another LLM call for every message. It's a fast
// rule-based check that runs before the tool loop:
//   - Does a plan exist for this task? (Plan-First enforcement)
//   - Is this a code-mutation tool that requires plan approval?
//   - Is this a trivial change that can auto-proceed?
//
// Only when the guard rail is uncertain does it optionally call the local
// guard model (qwen3.5:9b) for classification. Most checks are deterministic.
//
// Design principle: The guard rail should add <5ms latency to the happy path.
// LLM calls are the exception, not the rule.
package guard

import (
	"fmt"
	"strings"
	"time"

	contextpkg "github.com/emaharmony/prizm/internal/context"
	"github.com/emaharmony/prizm/internal/plan"
)

// Decision represents the guard rail's decision.
type Decision string

const (
	// Proceed means the action is allowed.
	Proceed Decision = "proceed"
	// Block means the action is blocked — a plan is required first.
	Block Decision = "block"
	// Warn means the action is allowed but with a warning.
	Warn Decision = "warn"
	// Defer means the guard rail can't decide locally — needs LLM classification.
	Defer Decision = "defer"
)

// CheckResult is the output of a guard rail check.
type CheckResult struct {
	Decision      Decision           `json:"decision"`
	Reason        string             `json:"reason"`
	PlanID        string             `json:"plan_id,omitempty"`
	ApprovalLevel plan.ApprovalLevel `json:"approval_level,omitempty"`
	Warning       string             `json:"warning,omitempty"`
}

// CodeMutationTools lists tool names that mutate code/files.
// These require an active plan before execution.
var CodeMutationTools = map[string]bool{
	"write_file":                true,
	"write_file_proposal":       true,
	"create_directory":          true,
	"create_directory_proposal": true,
	"edit_file":                 true,
	"git_add":                   true,
	"git_commit":                true,
	"git_push":                  true,
	"shell_exec":                true,
	"set_active_task":           true,
	"record_decision":           true,
	"update_context":            true,
}

// ReadTools lists tool names that are read-only.
// These never require a plan.
var ReadTools = map[string]bool{
	"read_project":      true,
	"search_files":      true,
	"project_overview":  true,
	"git_status":        true,
	"git_log":           true,
	"git_diff":          true,
	"git_branch_list":   true,
	"clear_active_task": true,
	"unblock":           true,
}

// Guard checks process rules before tool execution.
type Guard struct {
	planMgr    *plan.Manager
	ctxBuilder *contextpkg.Builder
}

// NewGuard creates a guard rail with the given plan manager.
func NewGuard(planMgr *plan.Manager, ctxBuilder *contextpkg.Builder) *Guard {
	return &Guard{
		planMgr:    planMgr,
		ctxBuilder: ctxBuilder,
	}
}

// CheckToolExecution decides whether a tool call should proceed.
// This is the main entry point for the guard rail.
func (g *Guard) CheckToolExecution(toolName string, args map[string]any) CheckResult {
	// Read-only tools always proceed
	if ReadTools[toolName] {
		return CheckResult{
			Decision: Proceed,
			Reason:   fmt.Sprintf("tool %q is read-only", toolName),
		}
	}

	// Non-mutation tools that aren't in either list — proceed with warning
	if !CodeMutationTools[toolName] {
		return CheckResult{
			Decision: Proceed,
			Reason:   fmt.Sprintf("tool %q is not classified as mutation", toolName),
		}
	}

	// Code mutation tools require an active plan
	plans, err := g.planMgr.LoadPlans()
	if err != nil {
		// If we can't load plans, warn but don't block
		return CheckResult{
			Decision: Warn,
			Reason:   fmt.Sprintf("could not load plans: %v", err),
			Warning:  "Plan system unavailable — proceeding without plan check",
		}
	}

	activePlan := plan.ActivePlan(plans)
	if activePlan == nil {
		// No active plan — block the mutation
		return CheckResult{
			Decision: Block,
			Reason:   "no active plan — create a plan before executing code mutations",
		}
	}

	// Plan exists — check if it can proceed
	if !plan.CanProceed(activePlan) {
		return CheckResult{
			Decision:      Block,
			Reason:        fmt.Sprintf("active plan %q is %s — needs approval before execution", activePlan.ID, activePlan.Status),
			PlanID:        activePlan.ID,
			ApprovalLevel: activePlan.ApprovalLevel,
		}
	}

	// Plan is approved or auto-proceed — allow execution
	return CheckResult{
		Decision:      Proceed,
		Reason:        fmt.Sprintf("active plan %q allows execution", activePlan.ID),
		PlanID:        activePlan.ID,
		ApprovalLevel: activePlan.ApprovalLevel,
	}
}

// CheckMessageIntent classifies whether a user message intends code mutation.
// This is the fast path — keyword heuristics, no LLM.
// Returns true if the message likely intends code work.
func CheckMessageIntent(message string) bool {
	codeIntentKeywords := []string{
		"implement", "build", "create", "write", "add", "fix", "patch",
		"refactor", "update", "change", "modify", "delete", "remove",
		"deploy", "release", "commit", "push", "merge",
		"make a", "set up", "set up a",
	}

	readIntentKeywords := []string{
		"read", "show", "list", "what", "how", "why", "explain",
		"status", "check", "tell me", "summarize", "review",
		"what is", "what are", "who", "where",
	}

	lower := strings.ToLower(message)

	// Check if message has read intent
	for _, kw := range readIntentKeywords {
		if strings.Contains(lower, kw) {
			// Has read intent — but could also have code intent
			for _, ckw := range codeIntentKeywords {
				if strings.Contains(lower, ckw) {
					return true // Mixed — assume code intent
				}
			}
			return false // Pure read intent
		}
	}

	// Check for code intent keywords
	for _, kw := range codeIntentKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}

	return false
}

// FormatGuardResult formats a check result for LLM prompt injection.
func FormatGuardResult(result CheckResult) string {
	switch result.Decision {
	case Proceed:
		if result.PlanID != "" {
			return fmt.Sprintf("✅ Proceeding under plan %s (approval: %s)", result.PlanID, result.ApprovalLevel)
		}
		return "✅ Proceeding — no plan required for this action"
	case Block:
		if result.PlanID != "" {
			return fmt.Sprintf("⛔ Blocked — plan %s is %s. Create or approve a plan first.", result.PlanID, result.Reason)
		}
		return fmt.Sprintf("⛔ Blocked — %s", result.Reason)
	case Warn:
		return fmt.Sprintf("⚠️ Warning — %s (proceeding anyway)", result.Warning)
	case Defer:
		return "🔄 Deferring to guard model for classification..."
	default:
		return fmt.Sprintf("❓ Unknown decision: %s", result.Decision)
	}
}

// FormatPlansSummary formats all active plans for prompt injection.
func FormatPlansSummary(plans []plan.Plan) string {
	active := plan.ActivePlan(plans)
	if active == nil {
		return "No active plan. Code mutations will be blocked until a plan is created."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Active plan: %s (%s)\n", active.ID, active.Title))
	sb.WriteString(fmt.Sprintf("  Status: %s | Approval: %s\n", active.Status, active.ApprovalLevel))
	if active.Reasoning != "" {
		sb.WriteString(fmt.Sprintf("  Why: %s\n", active.Reasoning))
	}
	if active.Scope != "" {
		sb.WriteString(fmt.Sprintf("  Scope: %s\n", active.Scope))
	}
	sb.WriteString(fmt.Sprintf("  Can proceed: %v\n", plan.CanProceed(active)))
	return sb.String()
}

// IsMutationTool checks if a tool name is classified as a code mutation tool.
func IsMutationTool(toolName string) bool {
	return CodeMutationTools[toolName]
}

// IsReadTool checks if a tool name is classified as a read-only tool.
func IsReadTool(toolName string) bool {
	return ReadTools[toolName]
}

// GuardHistory tracks guard rail decisions for self-improvement (Phase 4).
type GuardHistory struct {
	Entries []GuardEntry `json:"entries"`
}

// GuardEntry records a single guard rail decision.
type GuardEntry struct {
	Timestamp time.Time `json:"timestamp"`
	ToolName  string    `json:"tool_name"`
	Decision  Decision  `json:"decision"`
	Reason    string    `json:"reason"`
	PlanID    string    `json:"plan_id,omitempty"`
	Message   string    `json:"message,omitempty"` // Original user message (if intent check)
}
