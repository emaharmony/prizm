package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/nats-io/nats.go"
)

// extractNames pulls a deduplicated, ordered list of approver/reviewer names from
// the named payload keys (each a JSON array of strings after the NATS round-trip).
func extractNames(payload map[string]any, keys ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		raw, ok := payload[k].([]any)
		if !ok {
			continue
		}
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// gateAlert builds a prominent "action needed" banner that @-mentions the named
// approvers/reviewers so they are pinged when a gate pauses, rather than relying on
// them watching the channel. resolve maps an approver name to a Discord user ID;
// names it can't resolve fall back to a plain @name. Returns "" when no names.
func gateAlert(phase string, names []string, resolve func(string) string) string {
	if len(names) == 0 {
		return ""
	}
	who := make([]string, 0, len(names))
	for _, n := range names {
		if id := resolve(n); id != "" {
			who = append(who, "<@"+id+">")
		} else {
			who = append(who, "@"+n)
		}
	}
	action := "approval"
	if phase == "FEEDBACK_POST" {
		action = "review"
	}
	return fmt.Sprintf("🔔 **ACTION NEEDED** at `%s` — %s, your %s is needed.",
		phase, strings.Join(who, " "), action)
}

// gitRunner runs a git command in a repo and returns its stdout. Injected for tests.
type gitRunner func(repo string, args ...string) (string, error)

// execGit runs git against a repo via the system git binary.
func execGit(repo string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	return string(out), err
}

// diffPreview returns a bounded `git diff --stat` of the work this run produced,
// for the human approver at FEEDBACK_POST. It diffs the current branch against its
// merge-base with the default branch (the changes introduced this run); if no base
// is found it falls back to the last commit. Returns "" on any failure so the
// notifier degrades gracefully.
func diffPreview(repoPath string, runGit gitRunner) string {
	if repoPath == "" {
		return ""
	}
	base := ""
	for _, ref := range []string{"origin/HEAD", "origin/main", "origin/master", "main", "master"} {
		if mb, err := runGit(repoPath, "merge-base", "HEAD", ref); err == nil && strings.TrimSpace(mb) != "" {
			base = strings.TrimSpace(mb)
			break
		}
	}
	var stat string
	if base != "" {
		stat, _ = runGit(repoPath, "diff", "--stat", base+"...HEAD")
	}
	if strings.TrimSpace(stat) == "" {
		stat, _ = runGit(repoPath, "show", "--stat", "--oneline", "HEAD")
	}
	stat = strings.TrimSpace(stat)
	if stat == "" {
		return ""
	}
	const maxLen = 1500
	if len(stat) > maxLen {
		stat = stat[:maxLen] + "\n... [truncated]"
	}
	return stat
}

func startWorkflowFeedbackNotifier(nc *nats.Conn, bot discordBotClient, cfg *orchestrator.Config) error {
	if nc == nil || bot == nil {
		return nil
	}
	_, err := nc.Subscribe("prizm.workflow.feedback.requested", func(msg *nats.Msg) {
		log.Printf("[WORKFLOW-NOTIFY] received feedback.requested event")
		var evt struct {
			Type      string         `json:"type"`
			Payload   map[string]any `json:"payload"`
			Timestamp string         `json:"timestamp"`
		}
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			log.Printf("[WORKFLOW-NOTIFY] bad feedback.requested event: %v", err)
			return
		}
		payload := evt.Payload
		channelID, _ := payload["channel"].(string)
		if channelID == "" {
			channelID = fallbackWorkflowChannel(cfg)
		}
		if channelID == "" {
			log.Printf("[WORKFLOW-NOTIFY] feedback.requested has no channel")
			return
		}

		// Collect all target channels: the project channel plus any extra
		// feedback channels from config (e.g. manager-room mirror).
		targetChannels := feedbackChannels(cfg, channelID)

		runID, _ := payload["run_id"].(string)
		phase, _ := payload["phase"].(string)
		body, _ := payload["package"].(string)
		if strings.TrimSpace(body) == "" {
			body = fmt.Sprintf("Workflow `%s` is waiting at `%s`.", runID, phase)
		}
		content := cleanForDiscord(body)
		if runID != "" && !strings.Contains(content, runID) {
			content = fmt.Sprintf("Workflow `%s` is waiting at `%s`.\n\n%s", runID, phase, content)
		}
		// Diff preview: at the post-execution review gate, show the approver exactly
		// what changed (best-effort; appended only if not already present).
		if phase == "FEEDBACK_POST" && !strings.Contains(content, "git diff --stat") {
			if repo, _ := payload["repo_path"].(string); repo != "" {
				if d := diffPreview(repo, execGit); d != "" {
					content += "\n\n**Changes (git diff --stat):**\n```\n" + d + "\n```"
				}
			}
		}
		// Gate-needs-you: ping the named approvers/reviewers at the top so they act
		// promptly instead of having to watch the channel.
		if alert := gateAlert(phase, extractNames(payload, "approvers", "required_reviewers"), discordIDResolver(cfg)); alert != "" {
			content = alert + "\n\n" + content
		}
		// Rich approval card: render interactive buttons for the gate so approvers
		// can click instead of typing commands (the typed commands still work).
		for _, chID := range targetChannels {
			out := &discordbot.OutboundMessage{ChannelID: chID, Content: content}
			for _, btn := range buildFeedbackButtons(phase, runID) {
				out.Buttons = append(out.Buttons, discordbot.MessageButton{Label: btn.Label, Style: int(btn.Style), CustomID: btn.CustomID})
			}
			if err := bot.Send(out); err != nil {
				log.Printf("[WORKFLOW-NOTIFY] send failed for channel %s: %v", chID, err)
			} else {
				log.Printf("[WORKFLOW-NOTIFY] sent feedback card to channel %s (run=%s, phase=%s, buttons=%d)", chID, runID, phase, len(out.Buttons))
			}
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe workflow feedback notifier: %w", err)
	}
	log.Printf("[WORKFLOW-NOTIFY] subscribed to prizm.workflow.feedback.requested")
	return nil
}

// feedbackChannels returns the deduplicated list of channels to send feedback
// cards to: the primary channel plus any extra feedback mirror channels
// configured via channel_roles with role "manager-room".
func feedbackChannels(cfg *orchestrator.Config, primary string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	add(primary)
	if cfg != nil {
		for _, role := range cfg.ChannelRoles {
			if role.Role == "manager-room" && role.ID != primary {
				add(role.ID)
			}
		}
	}
	return out
}

// discordIDResolver returns a function mapping an approver/owner name to its first
// configured Discord user ID (via cfg.Users aliases), or "" when not found.
func discordIDResolver(cfg *orchestrator.Config) func(string) string {
	return func(name string) string {
		if cfg == nil {
			return ""
		}
		for _, u := range cfg.Users {
			if u.ID == name || u.DisplayName == name {
				if ids := u.Aliases["discord"]; len(ids) > 0 {
					return ids[0]
				}
			}
		}
		return ""
	}
}

func fallbackWorkflowChannel(cfg *orchestrator.Config) string {
	if cfg == nil {
		return ""
	}
	if p := cfg.DefaultProject(); p != nil && p.Channel != "" {
		return p.Channel
	}
	for _, role := range cfg.ChannelRoles {
		if role.Role == "build-room" && role.ID != "" {
			return role.ID
		}
	}
	return ""
}
