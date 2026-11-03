package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	"github.com/nats-io/nats.go"
)

// Freewill Discord notifications mirror factory_monitor_notifications.go:
// subscribe to curated lifecycle events on NATS and relay human-readable
// summaries to a Discord channel. Proposal formation and lifecycle endings
// are included; per-attempt chatter (wake.claimed, selection.started) is not.
func startFreewillDiscordNotifications(nc *nats.Conn, bot discordBotClient, channelID string, allowUserIDs []string, store *multiagent.SQLiteDurableRunStore) {
	if nc == nil || bot == nil || channelID == "" {
		return
	}
	enricher := &freewillRunEnricher{store: store, allowUserIDs: allowUserIDs}
	subscribeFreewillEvent(nc, bot, channelID, event.EventFreewillApprovalProposed, enricher)
	subscribeFreewillEvent(nc, bot, channelID, event.EventFreewillApprovalDecided, enricher)
	subscribeFreewillEvent(nc, bot, channelID, event.EventFreewillPolicyDenied, enricher)
	subscribeFreewillEvent(nc, bot, channelID, event.EventFreewillSelectionHalted, enricher)
}

func subscribeFreewillEvent(nc *nats.Conn, bot discordBotClient, channelID, subject string, e *freewillRunEnricher) {
	_, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		content, buttons := formatFreewillEvent(subject, msg.Data, e)
		if content == "" {
			return
		}
		if err := bot.Send(&discordbot.OutboundMessage{ChannelID: channelID, Content: content, Buttons: buttons}); err != nil {
			log.Printf("[FREEWILL-NOTIFY] WARN send Discord notification: %v", err)
		}
	})
	if err != nil {
		log.Printf("[FREEWILL-NOTIFY] WARN subscribe %s: %v", subject, err)
	}
}

// freewillRunEnricher loads the durable run record for a run_id so Discord
// messages show human titles/scores instead of opaque candidate IDs. It also
// carries the allowlist behind freewillbtn: Approve/Deny buttons.
type freewillRunEnricher struct {
	store        *multiagent.SQLiteDurableRunStore
	allowUserIDs []string
}

const discordDenyReason = "denied via Discord freewill button"

// Freewill buttons share the Discord custom-ID namespace; prefix mirrors the
// file-approval scheme ("prizmapprove:<aID>:<runID>:<action>").
const freewillButtonPrefix = "freewillbtn"

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
func (e *freewillRunEnricher) buttonsFor(approvalID, runID string) []discordbot.MessageButton {
	if e == nil || len(e.allowUserIDs) == 0 || approvalID == "" || runID == "" {
		return nil
	}
	return []discordbot.MessageButton{ // styles: 3 success, 4 danger
		{Label: "✅ Approve", Style: 3, CustomID: encodeFreewillButtonID(approvalID, runID, "grant")},
		{Label: "❌ Deny", Style: 4, CustomID: encodeFreewillButtonID(approvalID, runID, "deny")},
	}
}

func (e *freewillRunEnricher) run(runID string) (multiagent.DurableRun, bool) {
	if e == nil || runID == "" {
		return multiagent.DurableRun{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e.store != nil {
		rec, err := e.store.Load(ctx, runID)
		if err == nil {
			return rec, true
		}
		log.Printf("[FREEWILL-NOTIFY] WARN load run %s: %v", runID, err)
	}
	return multiagent.DurableRun{}, false
}

func (e *freewillRunEnricher) chosen(runID string) (title string, score int, reason string) {
	rec, ok := e.run(runID)
	if !ok || rec.Freewill == nil {
		return "", 0, ""
	}
	fw := rec.Freewill
	for _, c := range fw.Candidates {
		if c.CandidateID == fw.ChoiceID {
			r := fw.ReasonSummary
			if r == "" {
				r = c.Request.ReasonSummary
			}
			return c.Request.Title, c.Score, r
		}
	}
	return "", 0, fw.ReasonSummary
}

func formatFreewillEvent(subject string, raw []byte, e *freewillRunEnricher) (string, []discordbot.MessageButton) {
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		log.Printf("[FREEWILL-NOTIFY] WARN invalid event on %s: %v", subject, err)
		return "", nil
	}
	runID, _ := p["run_id"].(string)
	title, score, reason := e.chosen(runID)
	switch subject {
	case event.EventFreewillApprovalProposed:
		if title == "" {
			return fmt.Sprintf("🧠 **Freewill proposal** (`%s`)\nRun: `%s` — see dashboard for details", shortID(p["approval_id"]), runID), nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "🧠 **Freewill proposal awaits your decision**\n")
		fmt.Fprintf(&b, "**Idea:** %s\n", title)
		fmt.Fprintf(&b, "**Score:** %d/100 (advisory, chosen independently)\n", score)
		if reason != "" {
			fmt.Fprintf(&b, "**Why:** %s\n", fwTruncate(reason, 240))
		}
		fmt.Fprintf(&b, "Decide at the Freewill dashboard (`/freewill.html`) — approval id `%s`", shortID(p["approval_id"]))
		return b.String(), e.buttonsFor(shortID(p["approval_id"]), runID)
	case event.EventFreewillApprovalDecided:
		decision, _ := p["decision"].(string)
		emoji := "✅"
		if decision == "deny" {
			emoji = "🚫"
		}
		detail := title
		if detail == "" {
			detail = runID
		}
		return fmt.Sprintf("%s **Freewill proposal %s**\n%s", emoji, decision, detail), nil
	case event.EventFreewillPolicyDenied:
		return fmt.Sprintf("🛡️ **Freewill policy denied a proposal**\nRule: `%s`\nRun: `%s`", p["policy_rule_id"], runID), nil
	case event.EventFreewillSelectionHalted:
		return fmt.Sprintf("⛔ **Freewill selection halted**\nRun: `%s`", runID), nil
	}
	return "", nil
}

func shortID(v any) string {
	s, _ := v.(string)
	return s
}

func fwTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
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
