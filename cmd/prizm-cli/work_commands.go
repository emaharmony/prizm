package main

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/workflow/v2"
	"github.com/emaharmony/prizm/internal/workstart"
)

type pendingWorkStart struct {
	Request        workstart.Request
	Recommendation string
	CreatedAt      time.Time
}

func (cc *conversationContext) handlePrizmCommand(msg *discordbot.InboundMessage) bool {
	command, rest, ok := parsePrizmCommand(msg.Content)
	if !ok {
		return false
	}

	switch command {
	case "work", "start":
		cc.handleWorkStartCommand(msg, command, rest)
	case "delegate", "status", "stop", "cancel":
		if cc.crossCoord == nil {
			cc.sendWorkCommandReply(msg.ChannelID, "Cross-Prizm commands are not configured in this instance.")
			return true
		}
		return cc.handleCrossPrizmCommand(msg)
	default:
		cc.sendWorkCommandReply(msg.ChannelID, "Unknown Prizm command. Use `prizm work`, `prizm delegate`, `prizm status`, or `prizm stop`.")
	}
	return true
}

func (cc *conversationContext) handleWorkStartCommand(msg *discordbot.InboundMessage, command, rest string) {
	req := parseWorkStartRequest(command, rest)
	req.Channel = msg.ChannelID
	req.Source = "discord"
	req.RequestedBy = msg.UserID
	if req.Project == "" && req.RepoPath == "" {
		req.RequireLocation = true
	}
	if strings.TrimSpace(req.Prompt) == "" {
		cc.sendWorkCommandReply(msg.ChannelID, "Missing task. Example: `prizm work project:GudEats task:start a Vite React app`")
		return
	}
	cc.resolveAndStartWork(msg.ChannelID, req)
}

func (cc *conversationContext) handlePendingWorkStartReply(msg ChannelMessage) bool {
	key := workPendingKey(msg.UserID, msg.ChannelID)

	cc.pendingWorkMu.Lock()
	pending, ok := cc.pendingWork[key]
	if ok && time.Since(pending.CreatedAt) > 24*time.Hour {
		delete(cc.pendingWork, key)
		ok = false
	}
	cc.pendingWorkMu.Unlock()
	if !ok {
		return false
	}

	location, handled := parseWorkLocationReply(msg.Content, pending.Recommendation)
	if !handled {
		return false
	}
	if location == "" {
		cc.sendWorkCommandReply(msg.ChannelID, "Reply with `use recommended` or `path:<absolute path inside a configured write root>`.")
		return true
	}

	req := pending.Request
	req.RepoPath = location
	req.Bootstrap = true

	res, err := workstart.Resolve(cc.cfg, req)
	if err != nil {
		cc.sendWorkCommandReply(msg.ChannelID, fmt.Sprintf("That path is not allowed: %v", err))
		return true
	}
	if res.NeedsLocation {
		cc.storePendingWork(key, req, res.Recommendation)
		cc.sendLocationQuestion(msg.ChannelID, res)
		return true
	}

	cc.pendingWorkMu.Lock()
	delete(cc.pendingWork, key)
	cc.pendingWorkMu.Unlock()

	cc.publishWorkflowStart(msg.ChannelID, res)
	return true
}

func (cc *conversationContext) handleWorkflowFeedbackCommand(msg *discordbot.InboundMessage) bool {
	trimmed := strings.TrimSpace(msg.Content)
	decision, workflowID, reviewer, notes := v2.ParseReviewCommand(trimmed)
	if decision != "" {
		if !strings.HasPrefix(workflowID, "gl-") {
			return false
		}
		if reviewer == "" {
			reviewer = firstNonEmptyCommandArg(msg.UserName, msg.UserID, "discord")
		}
		return cc.publishWorkflowFeedback(msg.ChannelID, map[string]any{
			"type":        "review_response",
			"decision":    decision,
			"workflow_id": workflowID,
			"reviewer":    reviewer,
			"notes":       notes,
		})
	}

	decision, workflowID, notes = v2.ParseApprovalCommand(trimmed)
	if decision == "" {
		return false
	}
	if !strings.HasPrefix(workflowID, "gl-") {
		return false
	}
	reviewer = firstNonEmptyCommandArg(msg.UserName, msg.UserID, "discord")
	return cc.publishWorkflowFeedback(msg.ChannelID, map[string]any{
		"type":        "feedback_response",
		"decision":    decision,
		"workflow_id": workflowID,
		"reviewer":    reviewer,
		"notes":       notes,
	})
}

func (cc *conversationContext) maybeStartDetectedWork(msg ChannelMessage, sanitizedContent string) bool {
	if cc.cfg == nil {
		return false
	}
	channelRole := cc.cfg.ResolveChannelRoleConfig(msg.ChannelID)
	if !looksLikeWorkStartRequest(sanitizedContent, channelRole) {
		return false
	}
	req := workstart.Request{
		Prompt:          strings.TrimSpace(sanitizedContent),
		Channel:         msg.ChannelID,
		Source:          "discord_auto",
		RequestedBy:     msg.UserID,
		RequireLocation: true,
	}
	cc.resolveAndStartWork(msg.ChannelID, req)
	return true
}

func (cc *conversationContext) resolveAndStartWork(channelID string, req workstart.Request) {
	res, err := workstart.Resolve(cc.cfg, req)
	if err != nil {
		cc.sendWorkCommandReply(channelID, fmt.Sprintf("I cannot start that workflow: %v", err))
		return
	}
	if res.NeedsLocation {
		key := workPendingKey(req.RequestedBy, channelID)
		cc.storePendingWork(key, req, res.Recommendation)
		cc.sendLocationQuestion(channelID, res)
		return
	}
	cc.publishWorkflowStart(channelID, res)
}

func (cc *conversationContext) publishWorkflowStart(channelID string, res *workstart.Resolution) {
	if cc.natsConn == nil {
		cc.sendWorkCommandReply(channelID, "I cannot start the workflow because NATS is not connected.")
		return
	}
	req := res.Request
	req.Project = res.ProjectID
	req.RepoPath = res.RepoPath
	if req.Channel == "" {
		req.Channel = channelID
	}
	req.Bootstrap = req.Bootstrap || res.Project == nil

	payload, err := json.Marshal(req)
	if err != nil {
		cc.sendWorkCommandReply(channelID, fmt.Sprintf("I could not encode the workflow request: %v", err))
		return
	}
	if err := cc.natsConn.Publish("prizm.workflow.start", payload); err != nil {
		cc.sendWorkCommandReply(channelID, fmt.Sprintf("I could not publish the workflow request: %v", err))
		return
	}
	_ = cc.natsConn.FlushTimeout(2 * time.Second)

	target := res.ProjectID
	if target == "" {
		target = filepath.Base(res.RepoPath)
	}
	cc.sendWorkCommandReply(channelID, fmt.Sprintf("Workflow start requested for `%s` at `%s`. Plan approval will be posted in this channel.", target, res.RepoPath))
}

func (cc *conversationContext) publishWorkflowFeedback(channelID string, payload map[string]any) bool {
	if cc.natsConn == nil {
		cc.sendWorkCommandReply(channelID, "I cannot send workflow feedback because NATS is not connected.")
		return true
	}
	data, err := json.Marshal(payload)
	if err != nil {
		cc.sendWorkCommandReply(channelID, fmt.Sprintf("I could not encode workflow feedback: %v", err))
		return true
	}
	if err := cc.natsConn.Publish("prizm.workflow.feedback.response", data); err != nil {
		cc.sendWorkCommandReply(channelID, fmt.Sprintf("I could not publish workflow feedback: %v", err))
		return true
	}
	_ = cc.natsConn.FlushTimeout(2 * time.Second)
	cc.sendWorkCommandReply(channelID, fmt.Sprintf("Workflow feedback recorded for `%s`.", payload["workflow_id"]))
	return true
}

func (cc *conversationContext) storePendingWork(key string, req workstart.Request, recommendation string) {
	cc.pendingWorkMu.Lock()
	defer cc.pendingWorkMu.Unlock()
	if cc.pendingWork == nil {
		cc.pendingWork = make(map[string]pendingWorkStart)
	}
	cc.pendingWork[key] = pendingWorkStart{
		Request:        req,
		Recommendation: recommendation,
		CreatedAt:      time.Now(),
	}
}

func (cc *conversationContext) sendLocationQuestion(channelID string, res *workstart.Resolution) {
	var b strings.Builder
	b.WriteString("I need a confirmed project folder before I create files.")
	if res.Recommendation != "" {
		fmt.Fprintf(&b, "\n\nRecommended: `%s`", res.Recommendation)
		b.WriteString("\n\nReply `use recommended` to start there, or `path:<absolute path>` to choose another configured write root.")
	} else {
		b.WriteString("\n\nNo configured write root is available. Configure `prizm.write_roots`, then retry with `path:<absolute path>`.")
	}
	cc.sendWorkCommandReply(channelID, b.String())
}

func (cc *conversationContext) sendWorkCommandReply(channelID, content string) {
	if cc.bot == nil {
		log.Printf("[WORK] %s", content)
		return
	}
	if err := cc.bot.Send(&discordbot.OutboundMessage{ChannelID: channelID, Content: content}); err != nil {
		log.Printf("[WORK] failed to send reply: %v", err)
	}
}

func parseWorkStartRequest(command, rest string) workstart.Request {
	body := strings.TrimSpace(rest)
	if command == "work" {
		if fields := strings.Fields(body); len(fields) > 0 && strings.EqualFold(fields[0], "start") {
			body = strings.TrimSpace(body[len(fields[0]):])
		}
	}

	var req workstart.Request
	var promptParts []string
	fields := strings.Fields(body)
	for i := 0; i < len(fields); i++ {
		part := fields[i]
		key, value, hasKV := strings.Cut(part, ":")
		if !hasKV {
			promptParts = append(promptParts, part)
			continue
		}
		keyLower := strings.ToLower(strings.TrimSpace(key))
		switch keyLower {
		case "task", "prompt":
			remaining := []string{}
			if value != "" {
				remaining = append(remaining, value)
			}
			remaining = append(remaining, fields[i+1:]...)
			req.Prompt = strings.TrimSpace(strings.Join(remaining, " "))
			i = len(fields)
		case "project":
			req.Project = value
		case "path", "repo", "repo_path":
			req.RepoPath = value
			req.Bootstrap = true
		case "channel":
			req.Channel = value
		default:
			promptParts = append(promptParts, part)
		}
	}
	if req.Prompt == "" {
		req.Prompt = strings.TrimSpace(strings.Join(promptParts, " "))
	}
	return req
}

func parseWorkLocationReply(content, recommendation string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	lower := strings.ToLower(trimmed)
	switch lower {
	case "use recommended", "use recommendation", "recommended":
		return recommendation, true
	}
	for _, prefix := range []string{"path:", "repo_path:", "repo:"} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(trimmed[len(prefix):]), true
		}
	}
	if filepath.IsAbs(trimmed) {
		return trimmed, true
	}
	return "", false
}

func looksLikeWorkStartRequest(content string, role *orchestrator.ChannelRole) bool {
	if role == nil {
		return false
	}
	tools := strings.ToLower(strings.TrimSpace(role.Tools))
	if tools != "" && tools != "all" {
		return false
	}
	if !strings.EqualFold(role.Role, "build-room") {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(content))
	if lower == "" {
		return false
	}
	for _, question := range []string{"how do i ", "how can i ", "what is ", "why ", "should i "} {
		if strings.HasPrefix(lower, question) {
			return false
		}
	}

	hasVerb := false
	for _, verb := range []string{"build", "create", "implement", "start", "scaffold", "set up"} {
		if strings.Contains(lower, verb+" ") || strings.HasPrefix(lower, verb) {
			hasVerb = true
			break
		}
	}
	if !hasVerb {
		return false
	}
	for _, object := range []string{" app", " web app", " project", " site", " api", " service", " feature", " tool"} {
		if strings.Contains(lower, object) {
			return true
		}
	}
	return false
}

func workPendingKey(userID, channelID string) string {
	return userID + ":" + channelID
}
