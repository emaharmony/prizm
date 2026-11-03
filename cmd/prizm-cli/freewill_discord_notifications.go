package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

// freewillDiscordSink renders curated, source-enriched freewill lifecycle
// notifications as Discord cards. It implements
// multiagent.FreewillNotificationSink; delivery durability lives in
// FreewillNotificationService (durable outbox + canonical-event reconciler),
// so the sink only formats and sends.
type freewillDiscordSink struct {
	bot          discordBotClient
	channelID    string
	allowUserIDs []string
}

func newFreewillDiscordSink(bot discordBotClient, channelID string, allowUserIDs []string) *freewillDiscordSink {
	return &freewillDiscordSink{bot: bot, channelID: channelID, allowUserIDs: allowUserIDs}
}

// NotifyFreewill satisfies multiagent.FreewillNotificationSink.
func (s *freewillDiscordSink) NotifyFreewill(ctx context.Context, n multiagent.FreewillNotification) error {
	content, buttons := formatFreewillNotification(n, s.allowUserIDs)
	if content == "" {
		return nil
	}
	if err := s.bot.Send(&discordbot.OutboundMessage{ChannelID: s.channelID, Content: content, Buttons: buttons}); err != nil {
		log.Printf("[FREEWILL-NOTIFY] WARN send Discord notification: %v", err)
		return err // dispatcher retries with backoff
	}
	return nil
}

// Freewill buttons share the Discord custom-ID namespace; prefix mirrors the
// file-approval scheme ("prizmapprove:<aID>:<runID>:<action>").
const (
	freewillButtonPrefix = "freewillbtn"
	discordDenyReason    = "denied via Discord freewill button"
)

func encodeFreewillButtonID(approvalID, runID, action string) string {
	return strings.Join([]string{freewillButtonPrefix, approvalID, runID, action}, ":")
}

func decodeFreewillButtonID(customID string) (approvalID, runID, action string, ok bool) {
	parts := strings.SplitN(customID, ":", 5)
	if len(parts) != 4 || parts[0] != freewillButtonPrefix {
		return "", "", "", false
	}
	if parts[1] == "" || parts[2] == "" || (parts[3] != "grant" && parts[3] != "deny") {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[3], true
}

// buttonsFor renders Approve/Deny only when an allowlist is configured.
func buttonsFor(allowUserIDs []string, approvalID, runID string) []discordbot.MessageButton {
	if len(allowUserIDs) == 0 || approvalID == "" || runID == "" {
		return nil
	}
	return []discordbot.MessageButton{ // styles: 3 success, 4 danger
		{Label: "✅ Approve", Style: 3, CustomID: encodeFreewillButtonID(approvalID, runID, "grant")},
		{Label: "❌ Deny", Style: 4, CustomID: encodeFreewillButtonID(approvalID, runID, "deny")},
	}
}

// formatFreewillNotification renders one enriched notification. Enrichment
// (selected_title, selected_score, reason_summary, agent/project scope) is
// written into the canonical event payload at the emit point, so cards never
// require run-store lookups.
func formatFreewillNotification(n multiagent.FreewillNotification, allowUserIDs []string) (string, []discordbot.MessageButton) {
	approvalID, _ := n.Payload["approval_ref"].(string)
	if approvalID == "" {
		approvalID, _ = n.Payload["approval_id"].(string)
	}
	runID := n.RunID
	switch n.EventType {
	case "prism.freewill.approval.proposed":
		title, _ := n.Payload["selected_title"].(string)
		if title == "" {
			return fmt.Sprintf("🧠 **Freewill proposal** (`%s`)\nRun: `%s` — see dashboard for details", approvalID, runID), nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "🧠 **Freewill proposal awaits your decision**\n")
		fmt.Fprintf(&b, "**Idea:** %s\n", title)
		if score, ok := n.Payload["selected_score"].(float64); ok {
			fmt.Fprintf(&b, "**Score:** %d/100 (advisory, chosen independently)\n", int(score))
		}
		if agent, _ := n.Payload["agent_id"].(string); agent != "" {
			fmt.Fprintf(&b, "**Agent:** `%s`", agent)
			if project, _ := n.Payload["project_id"].(string); project != "" {
				fmt.Fprintf(&b, " · project `%s`", project)
			}
			b.WriteString("\n")
		}
		if reason, _ := n.Payload["reason_summary"].(string); reason != "" {
			fmt.Fprintf(&b, "**Why:** %s\n", fwTruncate(reason, 240))
		}
		fmt.Fprintf(&b, "Decide at the Freewill dashboard (`/freewill.html`) — approval id `%s`", approvalID)
		return b.String(), buttonsFor(allowUserIDs, approvalID, runID)
	case "prism.freewill.approval.decided":
		decision, _ := n.Payload["decision"].(string)
		emoji := "✅"
		if decision == "deny" {
			emoji = "🚫"
		}
		detail, _ := n.Payload["selected_title"].(string)
		if detail == "" {
			detail = runID
		}
		return fmt.Sprintf("%s **Freewill proposal %s**\n%s", emoji, decision, detail), nil
	case "prism.freewill.approval.reconciled":
		return fmt.Sprintf("♻️ **Freewill decision reconciled** after restart\nRun: `%s`", runID), nil
	case "prism.freewill.policy.denied":
		rule, _ := n.Payload["policy_rule_id"].(string)
		return fmt.Sprintf("🛡️ **Freewill policy denied a proposal**\nRule: `%s`\nRun: `%s`", rule, runID), nil
	case "prism.freewill.selection.halted":
		return fmt.Sprintf("⛔ **Freewill selection halted**\nRun: `%s`", runID), nil
	}
	return "", nil
}

// startFreewillButtonHandler registers a Discord button handler that turns
// freewillbtn: clicks into real approval decisions through the same
// FreewillApprovalService the dashboard/API uses. Authorization is the
// configured notify_allow_user_ids; empty allowlist = buttons never render,
// and clicks (if any) are rejected.
func startFreewillButtonHandler(bot discordBotClient, allowUserIDs []string, approvals *multiagent.FreewillApprovalService) {
	if bot == nil || len(allowUserIDs) == 0 {
		return
	}
	allowed := make(map[string]struct{}, len(allowUserIDs))
	for _, id := range allowUserIDs {
		allowed[id] = struct{}{}
	}
	if onButton, ok := bot.(interface {
		OnButton(handler func(customID, userID, userName, channelID string))
	}); ok && approvals != nil {
		onButton.OnButton(func(customID, userID, userName, channelID string) {
			approvalID, runID, action, ok := decodeFreewillButtonID(customID)
			if !ok {
				return
			}
			if _, permitted := allowed[userID]; !permitted {
				log.Printf("[FREEWILL-BTN] DENIED: user %q(%s) not in allowlist", userName, userID)
				_ = bot.Send(&discordbot.OutboundMessage{ChannelID: channelID, Content: fmt.Sprintf("⛔ <@%s> is not allowed to decide freewill proposals.", userID)})
				return
			}
			actor := "discord:" + userID
			if userName != "" {
				actor = "discord:" + userName
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			reason := discordDenyReason
			if action == "grant" {
				reason = "approved via Discord freewill button"
			}
			status, err := approvals.DecideStatus(ctx, runID, approvalID, action, actor, reason)
			if err != nil {
				log.Printf("[FREEWILL-BTN] decide failed: %v", err)
				_ = bot.Send(&discordbot.OutboundMessage{ChannelID: channelID, Content: fmt.Sprintf("⚠️ Freewill decision failed: `%v`", err)})
				return
			}
			emoji := "🚫"
			if action == "grant" {
				emoji = "✅"
			}
			log.Printf("[FREEWILL-BTN] decided: run=%s approval=%s action=%s actor=%s lifecycle=%s", runID, approvalID, action, actor, status.Lifecycle)
			_ = bot.Send(&discordbot.OutboundMessage{ChannelID: channelID, Content: fmt.Sprintf("%s Freewill proposal **%s** by <@%s> — run `%s` is now `%s`.", emoji, action, userID, runID, status.Lifecycle)})
		})
	}
}

func fwTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
