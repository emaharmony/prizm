// Package main provides a Slack adapter that wraps slack.BotAdapter
// to satisfy the ChannelSender interface.
package main

import (
	"log"

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

func (ss *slackSender) SendWithButtons(channelID string, buttons []ActionButton, content string) error {
	// Slack supports Block Kit buttons — future implementation
	log.Printf("[SLACK] SendWithButtons: buttons not yet implemented, sending plain text")
	return ss.bot.Send(&slack.OutboundMessage{
		ChannelID: channelID,
		Content:   content,
	})
}

func (ss *slackSender) SendAudio(channelID string, audio []byte) error {
	// Slack doesn't support direct audio messages
	log.Printf("[SLACK] SendAudio: audio not supported on Slack, skipping")
	return nil
}

func (ss *slackSender) SupportsButtons() bool { return false }
func (ss *slackSender) SupportsAudio() bool   { return false }