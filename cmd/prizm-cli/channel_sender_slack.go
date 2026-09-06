// Package main provides a Slack adapter that wraps slack.BotAdapter
// to satisfy the ChannelSender interface.
package main

import (
	"github.com/emaharmony/prizm/internal/adapter/builtin/slack"
)

// slackSender wraps a slack.BotAdapter to satisfy ChannelSender.
type slackSender struct {
	bot *slack.BotAdapter
}

func (ss *slackSender) Send(channelID, content string) error {
	return ss.bot.Send(&slack.OutboundMessage{
		ChannelID: channelID,
		Content:   content,
	})
}

func (ss *slackSender) SendPlaceholder(channelID, content string) (string, error) {
	// Slack doesn't have a native placeholder concept; send and return empty ID
	err := ss.bot.Send(&slack.OutboundMessage{
		ChannelID: channelID,
		Content:   content,
	})
	return "", err
}

func (ss *slackSender) EditMessage(channelID, messageID, content string) error {
	return ss.bot.EditMessage(channelID, messageID, content)
}

func (ss *slackSender) Typing(channelID string) error {
	// Slack doesn't have an explicit typing indicator API
	return nil
}