package main

import (
	ctxcontext "context"
	"fmt"
	"log"
	"strings"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/crossprizm"
)

func (cc *conversationContext) handleCrossPrizmCommand(msg *discordbot.InboundMessage) bool {
	command, rest, ok := parsePrizmCommand(msg.Content)
	if !ok {
		return false
	}

	switch command {
	case "delegate":
		cc.handleCrossPrizmDelegate(msg, rest)
	case "status":
		cc.handleCrossPrizmStatus(msg, rest)
	case "stop", "cancel":
		cc.handleCrossPrizmStop(msg, rest)
	default:
		cc.sendCrossPrizmCommandReply(msg.ChannelID, "Unknown Prizm command. Use `/prizm delegate`, `/prizm status`, or `/prizm stop`.")
	}
	return true
}

func (cc *conversationContext) handleCrossPrizmDelegate(msg *discordbot.InboundMessage, rest string) {
	args, taskText := parseCrossPrizmArgs(rest)
	if taskText == "" {
		cc.sendCrossPrizmCommandReply(msg.ChannelID, "Missing task. Example: `/prizm delegate target:factory task:run a Factory smoke check and report artifacts`")
		return
	}
	targetProfile := firstArg(args, "target", "profile")
	if targetProfile == "" {
		targetProfile = "generic"
	}

	req := crossprizm.DelegateRequest{
		TargetProfile:  targetProfile,
		TargetInstance: firstArg(args, "to", "instance"),
		LeaderInstance: firstArg(args, "leader"),
		Task:           taskText,
		Priority:       firstArg(args, "priority"),
		Context: map[string]any{
			"source":     "discord_command",
			"channel_id": msg.ChannelID,
			"user_id":    msg.UserID,
			"user_name":  msg.UserName,
		},
	}
	sent, err := cc.crossCoord.Delegate(ctxcontext.Background(), req)
	if err != nil {
		log.Printf("[CROSS-PRIZM] delegate command failed: %v", err)
		cc.sendCrossPrizmCommandReply(msg.ChannelID, fmt.Sprintf("Cross-Prizm delegation failed: %v", err))
		return
	}
	cc.sendCrossPrizmCommandReply(msg.ChannelID, fmt.Sprintf("Delegated over NATS to `%s` using `%s` profile. Thread `%s`.", sent.To, targetProfile, sent.CorrelationID))
}

func (cc *conversationContext) handleCrossPrizmStatus(msg *discordbot.InboundMessage, rest string) {
	args, taskValue := parseCrossPrizmArgs(rest)
	taskID := firstNonEmptyCommandArg(firstArg(args, "task", "task_id", "thread", "thread_id"), taskValue)
	target := crossPrizmTarget(cc, args)
	if taskID == "" || target == "" {
		cc.sendCrossPrizmCommandReply(msg.ChannelID, "Missing target or task id. Example: `/prizm status target:generic task:cross-corr-abc123`")
		return
	}
	sent, err := cc.crossCoord.RequestStatus(target, taskID)
	if err != nil {
		log.Printf("[CROSS-PRIZM] status command failed: %v", err)
		cc.sendCrossPrizmCommandReply(msg.ChannelID, fmt.Sprintf("Cross-Prizm status request failed: %v", err))
		return
	}
	cc.sendCrossPrizmCommandReply(msg.ChannelID, fmt.Sprintf("Requested status from `%s` for `%s`. Correlation `%s`.", target, taskID, sent.CorrelationID))
}

func (cc *conversationContext) handleCrossPrizmStop(msg *discordbot.InboundMessage, rest string) {
	args, taskValue := parseCrossPrizmArgs(rest)
	taskID := firstNonEmptyCommandArg(firstArg(args, "task", "task_id", "thread", "thread_id"), taskValue)
	target := crossPrizmTarget(cc, args)
	if taskID == "" || target == "" {
		cc.sendCrossPrizmCommandReply(msg.ChannelID, "Missing target or task id. Example: `/prizm stop target:generic task:cross-corr-abc123`")
		return
	}
	sent, err := cc.crossCoord.Cancel(target, taskID)
	if err != nil {
		log.Printf("[CROSS-PRIZM] stop command failed: %v", err)
		cc.sendCrossPrizmCommandReply(msg.ChannelID, fmt.Sprintf("Cross-Prizm stop request failed: %v", err))
		return
	}
	cc.sendCrossPrizmCommandReply(msg.ChannelID, fmt.Sprintf("Sent stop request to `%s` for `%s`. Correlation `%s`.", target, taskID, sent.CorrelationID))
}

func (cc *conversationContext) sendCrossPrizmCommandReply(channelID, content string) {
	if cc.bot == nil {
		return
	}
	if err := cc.bot.Send(&discordbot.OutboundMessage{ChannelID: channelID, Content: content}); err != nil {
		log.Printf("[CROSS-PRIZM] failed to send command reply: %v", err)
	}
}

func parsePrizmCommand(content string) (string, string, bool) {
	trimmed := strings.TrimSpace(content)
	lower := strings.ToLower(trimmed)
	var rest string
	switch {
	case strings.HasPrefix(lower, "/prizm "):
		rest = strings.TrimSpace(trimmed[len("/prizm "):])
	case lower == "/prizm":
		return "", "", true
	case strings.HasPrefix(lower, "prizm "):
		rest = strings.TrimSpace(trimmed[len("prizm "):])
	case lower == "prizm":
		return "", "", true
	default:
		return "", "", false
	}
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return "", "", true
	}
	command := strings.ToLower(parts[0])
	return command, strings.TrimSpace(rest[len(parts[0]):]), true
}

func parseCrossPrizmArgs(rest string) (map[string]string, string) {
	args := map[string]string{}
	taskText := ""
	fields := strings.Fields(rest)
	for i := 0; i < len(fields); i++ {
		part := fields[i]
		if strings.HasPrefix(strings.ToLower(part), "task:") {
			value := strings.TrimPrefix(part, part[:len("task:")])
			remaining := []string{}
			if value != "" {
				remaining = append(remaining, value)
			}
			remaining = append(remaining, fields[i+1:]...)
			taskText = strings.TrimSpace(strings.Join(remaining, " "))
			break
		}
		key, value, ok := strings.Cut(part, ":")
		if ok {
			args[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	return args, taskText
}

func crossPrizmTarget(cc *conversationContext, args map[string]string) string {
	if target := firstArg(args, "to", "instance"); target != "" {
		return target
	}
	profileName := firstArg(args, "target", "profile")
	if profileName == "" {
		profileName = "generic"
	}
	if profile, ok := cc.crossCoord.ResolveProfile(profileName); ok {
		return profile.InstanceID
	}
	return ""
}

func firstArg(args map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(args[strings.ToLower(key)]); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyCommandArg(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
