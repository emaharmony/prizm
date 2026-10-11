package v2

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
)

// DiscordFormatter formats workflow messages for Discord.
// These are posted to #manager-room for feedback gates and reports.
type DiscordFormatter struct{}

// AgentGlyphs is an optional, overridable decoration map: agent name → emoji used
// in plan/report formatting. It is purely cosmetic and defaults to a generic robot
// for any agent not listed, so no persona is hardcoded into the formatting logic.
// Projects can extend or replace this map to match their own roster.
var AgentGlyphs = map[string]string{}

// agentGlyph returns the decoration for an agent name, defaulting to 🤖.
func agentGlyph(agent string) string {
	if g, ok := AgentGlyphs[agent]; ok {
		return g
	}
	return "🤖"
}

// FormatPlanForApproval formats a plan for the FEEDBACK_PRE gate. approvers comes
// from the gate config, so the message names whichever approvers the project
// configured rather than any hardcoded persona.
func FormatPlanForApproval(state *WorkflowState, approvers []string) string {
	var sb strings.Builder

	sb.WriteString("## 📋 Plan Review Required\n\n")
	sb.WriteString(fmt.Sprintf("**Workflow:** %s\n", state.RunID))
	if len(approvers) > 0 {
		sb.WriteString(fmt.Sprintf("**Approvers:** %s\n", strings.Join(approvers, ", ")))
	}
	sb.WriteString(fmt.Sprintf("**Requested:** %s\n\n", time.Now().UTC().Format(time.RFC3339)))

	// Assumptions summary
	if len(state.Assumptions) > 0 {
		sb.WriteString("### Key Assumptions\n")
		open := 0
		for _, a := range state.Assumptions {
			if a.Status != "addressed" {
				open++
				emoji := "🔴"
				if a.Confidence >= 0.7 {
					emoji = "🟢"
				} else if a.Confidence >= 0.4 {
					emoji = "🟡"
				}
				sb.WriteString(fmt.Sprintf("- %s [%s] %s (conf: %.1f)\n", emoji, a.Criticality, a.Statement, a.Confidence))
			}
		}
		if open == 0 {
			sb.WriteString("All assumptions resolved ✅\n")
		}
		sb.WriteString("\n")
	}

	// Confidence summary
	sb.WriteString("### Confidence Matrix\n")
	minConf := state.GetMinConfidence()
	confEmoji := "🟢"
	if minConf < 0.5 {
		confEmoji = "🔴"
	} else if minConf < 0.7 {
		confEmoji = "🟡"
	}
	sb.WriteString(fmt.Sprintf("Overall: %s %.0f%%\n", confEmoji, minConf*100))
	for domain, cd := range state.ConfidenceMatrix {
		dEmoji := "🟢"
		if cd.Score < 0.5 {
			dEmoji = "🔴"
		} else if cd.Score < 0.7 {
			dEmoji = "🟡"
		}
		sb.WriteString(fmt.Sprintf("- %s %s: %.0f%%\n", dEmoji, domain, cd.Score*100))
	}
	sb.WriteString("\n")

	// Plan tasks
	if state.Plan != nil && len(state.Plan.Tasks) > 0 {
		sb.WriteString("### Task Breakdown\n")
		for _, task := range state.Plan.Tasks {
			agentEmoji := agentGlyph(task.Agent)
			riskBadge := ""
			switch task.RiskLevel {
			case "high":
				riskBadge = " ⚠️ HIGH RISK"
			case "medium":
				riskBadge = " 🟡 medium"
			}
			sb.WriteString(fmt.Sprintf("- **%s** %s → %s%s\n", task.ID, agentEmoji, task.Description, riskBadge))
			if task.SuccessCriteria != "" {
				sb.WriteString(fmt.Sprintf("  Done when: %s\n", task.SuccessCriteria))
			}
			if len(task.DependsOn) > 0 {
				sb.WriteString(fmt.Sprintf("  Depends on: %s\n", strings.Join(task.DependsOn, ", ")))
			}
		}
		sb.WriteString("\n")
	}

	// Risk mitigations
	if state.Plan != nil && len(state.Plan.RiskMitigations) > 0 {
		sb.WriteString("### Risk Mitigations\n")
		for _, rm := range state.Plan.RiskMitigations {
			sb.WriteString(fmt.Sprintf("- **%s**: %s → task %s\n", rm.Domain, rm.Strategy, rm.TaskID))
		}
		sb.WriteString("\n")
	}

	// Approval instructions
	sb.WriteString("### Approval\n")
	sb.WriteString("Reply with one of:\n")
	sb.WriteString("- `approve " + state.RunID + "` — approve the plan\n")
	sb.WriteString("- `changes " + state.RunID + " : {feedback}` — request changes\n")
	sb.WriteString("- `reject " + state.RunID + " : {reason}` — reject the plan\n")

	return sb.String()
}

// formatReviewerList renders configured reviewer names as a human-readable phrase,
// with the first marked (required). Falls back to a generic phrase when no
// reviewers are configured, so nothing is hardcoded to a specific roster.
func formatReviewerList(reviewers []string) string {
	if len(reviewers) == 0 {
		return "the configured reviewers"
	}
	parts := make([]string, len(reviewers))
	for i, r := range reviewers {
		if i == 0 {
			parts[i] = fmt.Sprintf("**%s** (required)", r)
		} else {
			parts[i] = fmt.Sprintf("**%s**", r)
		}
	}
	return strings.Join(parts, " and ")
}

// FormatReviewPackage formats a review package for the FEEDBACK_POST gate.
// requiredReviewers comes from the gate config, so the message names whichever
// reviewers the project configured rather than any hardcoded persona.
func FormatReviewPackage(state *WorkflowState, gitDiff string, requiredReviewers []string) string {
	var sb strings.Builder

	sb.WriteString("## 🔍 Post-Execution Review Required\n\n")
	sb.WriteString(fmt.Sprintf("**Workflow:** %s\n\n", state.RunID))

	// Verification status — objective proof the committed code builds and its
	// tests pass, so reviewers aren't relying on the model's say-so.
	if v := state.Verification; v != nil {
		status, emoji := "passed", "✅"
		if !v.Passed {
			status, emoji = "FAILED", "❌"
		}
		sb.WriteString(fmt.Sprintf("### Verification\n%s `%s` — %s (exit %d, %d attempt(s))\n\n",
			emoji, v.Profile, status, v.ExitCode, v.Attempts))
	}

	// Completed tasks
	if state.Plan != nil {
		sb.WriteString("### Completed Tasks\n")
		for _, task := range state.Plan.Tasks {
			statusEmoji := "✅"
			if task.Status == "failed" {
				statusEmoji = "❌"
			} else if task.Status == "pending" {
				statusEmoji = "⏳"
			}
			sb.WriteString(fmt.Sprintf("- %s **%s** → %s (%s)\n", statusEmoji, task.ID, task.Description, task.Status))
			if task.Artifacts != nil {
				if task.Artifacts.CommitHash != "" {
					sb.WriteString(fmt.Sprintf("  Commit: `%s`\n", task.Artifacts.CommitHash))
				}
				if task.Artifacts.BranchName != "" {
					sb.WriteString(fmt.Sprintf("  Branch: `%s`\n", task.Artifacts.BranchName))
				}
				if task.Artifacts.PRURL != "" {
					sb.WriteString(fmt.Sprintf("  PR: %s\n", task.Artifacts.PRURL))
				}
				if len(task.Artifacts.FilePaths) > 0 {
					sb.WriteString(fmt.Sprintf("  Files: %s\n", strings.Join(task.Artifacts.FilePaths, ", ")))
				}
			}
		}
		sb.WriteString("\n")
	}

	// Git diff (truncated)
	if gitDiff != "" {
		sb.WriteString("### Changes (git diff --stat)\n")
		sb.WriteString("```\n")
		if len(gitDiff) > 2000 {
			sb.WriteString(gitDiff[:2000])
			sb.WriteString("\n... [truncated]\n")
		} else {
			sb.WriteString(gitDiff)
		}
		sb.WriteString("```\n\n")
	}

	// Review instructions
	sb.WriteString("### Review\n")
	sb.WriteString("Reviewers needed: " + formatReviewerList(requiredReviewers) + "\n")
	sb.WriteString("6 dimensions: code_quality, task_completion, regression_check, test_coverage, documentation, git_hygiene\n\n")
	sb.WriteString("Reply with:\n")
	sb.WriteString("- `review_approve " + state.RunID + "` — all dimensions pass\n")
	sb.WriteString("- `review_approve " + state.RunID + " notes: {observations}` — approve with notes\n")
	sb.WriteString("- `review_changes " + state.RunID + " : {specific issues}` — send back for fixes\n")

	return sb.String()
}

func formatTokenBudgetReport(state *WorkflowState) string {
	if state == nil {
		return ""
	}
	state.mu.RLock()
	prompt := state.TotalPromptTokens
	completion := state.TotalCompletionTokens
	localPrompt := state.LocalPromptTokens
	localCompletion := state.LocalCompletionTokens
	max := state.MaxTotalTokens
	type phaseUsage struct {
		name       string
		tokens     int
		maxTokens  int
		iterations int
	}
	phases := make([]phaseUsage, 0, len(state.PhaseStates))
	for name, ps := range state.PhaseStates {
		if ps == nil {
			continue
		}
		phases = append(phases, phaseUsage{name: name, tokens: ps.PromptTokens + ps.CompletionTokens, maxTokens: ps.MaxTokens, iterations: ps.Iterations})
	}
	state.mu.RUnlock()

	total := prompt + completion
	if max == 0 {
		max = DefaultRunTokenCeiling
	}
	var sb strings.Builder
	sb.WriteString("### Token Budget\n")
	sb.WriteString(fmt.Sprintf("- Total: %d tokens (prompt %d / completion %d)\n", total, prompt, completion))
	if localPrompt+localCompletion > 0 {
		sb.WriteString(fmt.Sprintf("- Local fallback (excluded from ceiling): %d tokens (prompt %d / completion %d)\n", localPrompt+localCompletion, localPrompt, localCompletion))
	}
	switch {
	case max == UnlimitedTokens:
		sb.WriteString("- Ceiling: unlimited\n")
	case max > 0:
		remaining := max - total
		if remaining < 0 {
			remaining = 0
		}
		percent := float64(total) / float64(max) * 100
		sb.WriteString(fmt.Sprintf("- Ceiling: %d tokens (remaining %d, %.1f%% used)\n", max, remaining, percent))
	}
	sort.Slice(phases, func(i, j int) bool { return phases[i].name < phases[j].name })
	if len(phases) > 0 {
		sb.WriteString("- Per phase:\n")
		for _, p := range phases {
			if p.tokens == 0 && p.maxTokens == 0 && p.iterations == 0 {
				continue
			}
			if p.maxTokens > 0 {
				pct := float64(p.tokens) / float64(p.maxTokens) * 100
				sb.WriteString(fmt.Sprintf("  - %s: %d/%d tokens (%.1f%%)\n", p.name, p.tokens, p.maxTokens, pct))
			} else {
				sb.WriteString(fmt.Sprintf("  - %s: %d tokens\n", p.name, p.tokens))
			}
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

// FormatFinalReport formats the REPORT phase output for Discord.
func FormatFinalReport(state *WorkflowState) string {
	var sb strings.Builder

	sb.WriteString("## 🌟 Natural Gates Workflow Complete\n\n")
	sb.WriteString(fmt.Sprintf("**Workflow:** %s\n", state.RunID))
	sb.WriteString(fmt.Sprintf("**Status:** %s\n", state.Status))
	sb.WriteString(fmt.Sprintf("**Duration:** %s → %s\n\n", state.StartedAt, state.CompletedAt))

	sb.WriteString(formatTokenBudgetReport(state))
	// Phase summary
	sb.WriteString("### Phase Summary\n")
	for _, phaseCfg := range DefaultConfig().Phases {
		if ps, ok := state.PhaseStates[phaseCfg.Name]; ok {
			emoji := "✅"
			if ps.Status == PhaseStatusFallback {
				emoji = "⚠️"
			} else if ps.Status == PhaseStatusFailed {
				emoji = "❌"
			}
			sb.WriteString(fmt.Sprintf("%s %s — %d iterations", emoji, phaseCfg.Name, ps.Iterations))
			if ps.GateResult != nil {
				sb.WriteString(fmt.Sprintf(" (gate: %.2f)", ps.GateResult.Score))
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n")

	// Assumptions resolved
	if len(state.Assumptions) > 0 {
		resolved := 0
		for _, a := range state.Assumptions {
			if a.Status == "addressed" {
				resolved++
			}
		}
		sb.WriteString(fmt.Sprintf("### Assumptions: %d/%d resolved\n", resolved, len(state.Assumptions)))
	}
	sb.WriteString("\n")

	// Confidence
	minConf := state.GetMinConfidence()
	sb.WriteString(fmt.Sprintf("### Final Confidence: %.0f%%\n", minConf*100))
	for domain, cd := range state.ConfidenceMatrix {
		sb.WriteString(fmt.Sprintf("- %s: %.0f%%\n", domain, cd.Score*100))
	}
	sb.WriteString("\n")

	// Plan results
	if state.Plan != nil && len(state.Plan.Tasks) > 0 {
		sb.WriteString("### Task Results\n")
		for _, task := range state.Plan.Tasks {
			emoji := "✅"
			if task.Status == "failed" {
				emoji = "❌"
			}
			sb.WriteString(fmt.Sprintf("%s **%s** → %s (%s)\n", emoji, task.ID, task.Description, task.Status))
		}
		sb.WriteString("\n")
	}

	// Verification — objective proof the committed code builds and its tests pass.
	if v := state.Verification; v != nil {
		status, emoji := "passed", "✅"
		if !v.Passed {
			status, emoji = "FAILED", "❌"
		}
		sb.WriteString(fmt.Sprintf("### Verification\n%s `%s` — %s (exit %d, %d attempt(s))\n\n",
			emoji, v.Profile, status, v.ExitCode, v.Attempts))
	}

	// Delegations — what was handed to other agents and how it resolved.
	if len(state.Delegations) > 0 {
		sb.WriteString("### Delegations\n")
		for _, d := range state.Delegations {
			emoji := "✅"
			switch d.Status {
			case "failed", "timed_out":
				emoji = "❌"
			case "sent", "acknowledged", "in_progress":
				emoji = "⏳"
			}
			line := fmt.Sprintf("%s **%s** → %s (%s)", emoji, d.TaskID, d.Agent, d.Status)
			if d.RetryCount > 0 {
				line += fmt.Sprintf(", %d retries", d.RetryCount)
			}
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	// Feedback summary
	if state.Feedback != nil {
		if state.Feedback.PreExecution != nil && state.Feedback.PreExecution.Status == "approved" {
			sb.WriteString(fmt.Sprintf("Plan approved by %s\n", state.Feedback.PreExecution.Approver))
		}
		if state.Feedback.PostExecution != nil {
			sb.WriteString("Post-execution review:\n")
			for reviewer, rs := range state.Feedback.PostExecution.Reviewers {
				sb.WriteString(fmt.Sprintf("- %s: %s\n", reviewer, rs.Status))
			}
		}
	}

	// Report sections
	if state.Report != nil && state.Report.Content != "" {
		// This formatter feeds Discord and the durable report artifact, so keep
		// it as a final public-output boundary even for manually built states.
		if public, err := agent.PublicResponseText(state.Report.Content); err == nil && public != "" {
			sb.WriteString("\n### Report Content\n")
			sb.WriteString(public)
		}
	}

	return sb.String()
}

// ParseApprovalCommand parses a Discord approval command.
// Returns: decision (approved|changes_requested|rejected), workflowID, notes.
func ParseApprovalCommand(text string) (decision, workflowID, notes string) {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)

	if strings.HasPrefix(lower, "approve ") {
		rest := strings.TrimPrefix(lower, "approve ")
		// Check for "with notes:" suffix
		if idx := strings.Index(rest, " with notes:"); idx != -1 {
			workflowID = strings.TrimSpace(rest[:idx])
			notes = strings.TrimSpace(text[idx+13:])
		} else {
			workflowID = strings.TrimSpace(rest)
		}
		return "approved", workflowID, notes
	}

	if strings.HasPrefix(lower, "changes ") {
		rest := strings.TrimPrefix(lower, "changes ")
		if idx := strings.Index(rest, " :"); idx != -1 {
			workflowID = strings.TrimSpace(rest[:idx])
			notes = strings.TrimSpace(text[idx+2:])
		} else {
			workflowID = strings.TrimSpace(rest)
		}
		return "changes_requested", workflowID, notes
	}

	if strings.HasPrefix(lower, "reject ") {
		rest := strings.TrimPrefix(lower, "reject ")
		if idx := strings.Index(rest, " :"); idx != -1 {
			workflowID = strings.TrimSpace(rest[:idx])
			notes = strings.TrimSpace(text[idx+2:])
		} else {
			workflowID = strings.TrimSpace(rest)
		}
		return "rejected", workflowID, notes
	}

	return "", "", ""
}

// ParseReviewCommand parses a Discord review command.
// Returns: decision (approved|changes_requested), workflowID, reviewer, notes.
func ParseReviewCommand(text string) (decision, workflowID, reviewer, notes string) {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)

	if strings.HasPrefix(lower, "review_approve ") {
		rest := strings.TrimPrefix(lower, "review_approve ")
		if idx := strings.Index(rest, " notes:"); idx != -1 {
			workflowID = strings.TrimSpace(rest[:idx])
			notes = strings.TrimSpace(text[idx+7:])
		} else {
			workflowID = strings.TrimSpace(rest)
		}
		// Reviewer is inferred from the message sender (handled by caller)
		return "approved", workflowID, "", notes
	}

	if strings.HasPrefix(lower, "review_changes ") {
		rest := strings.TrimPrefix(lower, "review_changes ")
		if idx := strings.Index(rest, " :"); idx != -1 {
			workflowID = strings.TrimSpace(rest[:idx])
			notes = strings.TrimSpace(text[idx+2:])
		} else {
			workflowID = strings.TrimSpace(rest)
		}
		return "changes_requested", workflowID, "", notes
	}

	return "", "", "", ""
}

// WorkflowStateJSON returns the full state as formatted JSON (for debugging/dashboard).
func WorkflowStateJSON(state *WorkflowState) string {
	state.mu.RLock()
	defer state.mu.RUnlock()
	data, _ := json.MarshalIndent(state, "", "  ")
	return string(data)
}
