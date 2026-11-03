package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

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

// formatFreewillNotification renders one enriched notification as a
// decision-ready brief: project, goal, reasoning, capabilities, score context,
// and the alternatives the selector considered — everything a human needs
// before approving, without opening any other surface.
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
		fmt.Fprintf(&b, "🧠 **Freewill proposal — needs your decision**\n")
		fmt.Fprintf(&b, "**Project:** `%s`", n.Payload["project_id"])
		if ws, _ := n.Payload["workspace"].(string); ws != "" {
			fmt.Fprintf(&b, " · `%s`", ws)
		}
		b.WriteString("\n")
		if agent, _ := n.Payload["agent_id"].(string); agent != "" {
			fmt.Fprintf(&b, "**Proposed by:** `%s`\n", agent)
		}
		fmt.Fprintf(&b, "**What:** %s\n", title)
		if goal, _ := n.Payload["selected_goal"].(string); goal != "" {
			fmt.Fprintf(&b, "**Goal:** %s\n", goal)
		}
		// The chosen candidate's full reasoning, why it was chosen over the
		// alternatives, and what the alternatives scored — full transparency.
		cands, _ := n.Payload["candidates"].([]any)
		choiceID, _ := n.Payload["choice_id"].(string)
		for _, rc := range cands {
			c, ok := rc.(map[string]any)
			if !ok || c["candidate_id"] != choiceID {
				continue
			}
			if reason, _ := c["reason"].(string); reason != "" {
				fmt.Fprintf(&b, "**Why (selector reasoning):**\n%s\n", fwBullets(reason))
			}
			if caps, ok := c["capabilities"].([]any); ok && len(caps) > 0 {
				parts := make([]string, 0, len(caps))
				for _, cp := range caps {
					parts = append(parts, fmt.Sprint(cp))
				}
				fmt.Fprintf(&b, "**Requested capabilities:** %s\n", strings.Join(parts, ", "))
			}
			break
		}
		if len(cands) > 1 {
			fmt.Fprintf(&b, "**Alternatives considered:**")
			parts := make([]string, 0, len(cands))
			for _, rc := range cands {
				c, ok := rc.(map[string]any)
				if !ok || c["candidate_id"] == choiceID {
					continue
				}
				t, _ := c["title"].(string)
				sc, _ := c["score"].(float64)
				parts = append(parts, fmt.Sprintf("%s (%d)", t, int(sc)))
			}
			fmt.Fprintf(&b, " %s\n", strings.Join(parts, " · "))
		}
		if score, ok := n.Payload["selected_score"].(float64); ok {
			fmt.Fprintf(&b, "**Score:** %d/100 (advisory — the human decides)\n", int(score))
		}
		fmt.Fprintf(&b, "**Approving:** marks your decision; nothing executes — proposals are decision-only in this runtime.\n")
		fmt.Fprintf(&b, "Approval id `%s` · dashboard: `/freewill.html`", approvalID)
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

// fwBullets renders the selector's run-together reasoning clauses as bullets
// by splitting on sentence-ish boundaries; falls back to wrapped text.
func fwBullets(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	parts := fwSplitClauses(reason)
	if len(parts) <= 1 {
		return "- " + reason
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(p))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// fwSplitClauses breaks a GLM reasoning blob into sentences where punctuation
// exists; otherwise splits camel-ish/verb boundaries conservatively on
// uppercase-letter patterns ("…libraries Requires a small…" → two clauses).
func fwSplitClauses(s string) []string {
	var out []string
	cur := strings.Builder{}
	runes := []rune(s)
	for i, r := range runes {
		cur.WriteRune(r)
		if r == '.' || r == '!' || r == '?' {
			if i+1 < len(runes) && runes[i+1] == ' ' {
				out = append(out, cur.String())
				cur.Reset()
			}
		} else if r == ' ' && i+1 < len(runes) && unicode.IsUpper(runes[i+1]) && len(cur.String()) > 12 {
			// New clause likely starts ("… Requires …", "… Improves …")
			text := strings.TrimSpace(cur.String())
			if text != "" {
				out = append(out, text)
			}
			cur.Reset()
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	return out
}

