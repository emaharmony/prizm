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
func startFreewillDiscordNotifications(nc *nats.Conn, bot discordBotClient, channelID string, store *multiagent.SQLiteDurableRunStore) {
	if nc == nil || bot == nil || channelID == "" {
		return
	}
	enricher := &freewillRunEnricher{store: store}
	subscribeFreewillEvent(nc, bot, channelID, event.EventFreewillApprovalProposed, enricher)
	subscribeFreewillEvent(nc, bot, channelID, event.EventFreewillApprovalDecided, enricher)
	subscribeFreewillEvent(nc, bot, channelID, event.EventFreewillPolicyDenied, enricher)
	subscribeFreewillEvent(nc, bot, channelID, event.EventFreewillSelectionHalted, enricher)
}

func subscribeFreewillEvent(nc *nats.Conn, bot discordBotClient, channelID, subject string, e *freewillRunEnricher) {
	_, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		content := formatFreewillEvent(subject, msg.Data, e)
		if content == "" {
			return
		}
		if err := bot.Send(&discordbot.OutboundMessage{ChannelID: channelID, Content: content}); err != nil {
			log.Printf("[FREEWILL-NOTIFY] WARN send Discord notification: %v", err)
		}
	})
	if err != nil {
		log.Printf("[FREEWILL-NOTIFY] WARN subscribe %s: %v", subject, err)
	}
}

// freewillRunEnricher loads the durable run record for a run_id so Discord
// messages show human titles/scores instead of opaque candidate IDs.
type freewillRunEnricher struct {
	store *multiagent.SQLiteDurableRunStore
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

func formatFreewillEvent(subject string, raw []byte, e *freewillRunEnricher) string {
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		log.Printf("[FREEWILL-NOTIFY] WARN invalid event on %s: %v", subject, err)
		return ""
	}
	runID, _ := p["run_id"].(string)
	title, score, reason := e.chosen(runID)
	switch subject {
	case event.EventFreewillApprovalProposed:
		if title == "" {
			return fmt.Sprintf("🧠 **Freewill proposal** (`%s`)\nRun: `%s` — see dashboard for details", shortID(p["approval_id"]), runID)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "🧠 **Freewill proposal awaits your decision**\n")
		fmt.Fprintf(&b, "**Idea:** %s\n", title)
		fmt.Fprintf(&b, "**Score:** %d/100 (advisory, chosen independently)\n", score)
		if reason != "" {
			fmt.Fprintf(&b, "**Why:** %s\n", fwTruncate(reason, 240))
		}
		fmt.Fprintf(&b, "Decide at the Freewill dashboard (`/freewill.html`) — approval id `%s`", shortID(p["approval_id"]))
		return b.String()
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
		return fmt.Sprintf("%s **Freewill proposal %s**\n%s", emoji, decision, detail)
	case event.EventFreewillPolicyDenied:
		return fmt.Sprintf("🛡️ **Freewill policy denied a proposal**\nRule: `%s`\nRun: `%s`", p["policy_rule_id"], runID)
	case event.EventFreewillSelectionHalted:
		return fmt.Sprintf("⛔ **Freewill selection halted**\nRun: `%s`", runID)
	}
	return ""
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
