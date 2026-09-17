package main

import (
	"fmt"
	"strings"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/plan"
)

// buildPlanApprovalButtons creates Discord buttons for approving/rejecting a plan.
// Button custom IDs follow the pattern: "plan:APPROVE:PLAN_ID" or "plan:REJECT:PLAN_ID"
func buildPlanApprovalButtons(planID string) []discordbot.MessageButton {
	return []discordbot.MessageButton{
		{Label: "✅ Approve", Style: 3, CustomID: fmt.Sprintf("plan:APPROVE:%s", planID)},
		{Label: "❌ Reject", Style: 4, CustomID: fmt.Sprintf("plan:REJECT:%s", planID)},
	}
}

// decodePlanButtonID parses a plan button custom ID.
// Returns (planID, action, ok). action is "approve" or "reject".
func decodePlanButtonID(customID string) (planID, action string, ok bool) {
	parts := strings.SplitN(customID, ":", 3)
	if len(parts) != 3 || parts[0] != "plan" {
		return "", "", false
	}
	return parts[2], strings.ToLower(parts[1]), true
}

// formatPlanMessage creates a Discord message body for a plan that needs approval.
func formatPlanMessage(p *plan.Plan) discordbot.OutboundMessage {
	completed, total := plan.StepProgress(p)

	isAuto := p.Status == plan.StatusAutoProceed

	if isAuto {
		content := fmt.Sprintf("📋 **Plan %s created (auto-proceed)** — %s\n", p.ID, p.Title)
		content += fmt.Sprintf("Status: %s | Progress: %d/%d steps\n", p.Status, completed, total)
		if p.Reasoning != "" {
			content += fmt.Sprintf("Why: %s\n", p.Reasoning)
		}
		if p.Scope != "" {
			content += fmt.Sprintf("Out of scope: %s\n", p.Scope)
		}
		if len(p.Steps) > 0 {
			content += "\n**Steps:**\n"
			for _, s := range p.Steps {
				check := "⬜"
				if s.Status == plan.StepCompleted {
					check = "✅"
				} else if s.Status == plan.StepInProgress {
					check = "🔄"
				} else if s.Status == plan.StepBlocked {
					check = "🚫"
				} else if s.Status == plan.StepSkipped {
					check = "⏭️"
				}
				content += fmt.Sprintf("%s %s: %s\n", check, s.ID, s.Title)
			}
		}
		return discordbot.OutboundMessage{Content: content}
	}

	content := fmt.Sprintf("📝 **Plan %s needs approval** — %s\n", p.ID, p.Title)
	content += fmt.Sprintf("Approval: %s | Status: %s | Progress: %d/%d steps\n", p.ApprovalLevel, p.Status, completed, total)
	if p.Reasoning != "" {
		content += fmt.Sprintf("Why: %s\n", p.Reasoning)
	}
	if p.Scope != "" {
		content += fmt.Sprintf("Out of scope: %s\n", p.Scope)
	}
	if len(p.Steps) > 0 {
		content += "\n**Steps:**\n"
		for _, s := range p.Steps {
			check := "⬜"
			if s.Status == plan.StepCompleted {
				check = "✅"
			} else if s.Status == plan.StepInProgress {
				check = "🔄"
			} else if s.Status == plan.StepBlocked {
				check = "🚫"
			} else if s.Status == plan.StepSkipped {
				check = "⏭️"
			}
			content += fmt.Sprintf("%s %s: %s\n", check, s.ID, s.Title)
		}
	}

	return discordbot.OutboundMessage{
		Content: content,
		Buttons: buildPlanApprovalButtons(p.ID),
	}
}