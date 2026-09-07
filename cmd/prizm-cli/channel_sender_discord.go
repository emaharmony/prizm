// Package main provides a Discord adapter that wraps discordBotClient
// to satisfy the ChannelSender interface.
package main

import (
	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
)

// discordSender wraps a discordBotClient to satisfy ChannelSender.
type discordSender struct {
	bot discordBotClient
}

func (ds *discordSender) Send(channelID, content string) error {
	return ds.bot.Send(&discordbot.OutboundMessage{
		ChannelID: channelID,
		Content:   content,
	})
}

func (ds *discordSender) SendPlaceholder(channelID, content string) (string, error) {
	return ds.bot.SendPlaceholder(channelID, content)
}

func (ds *discordSender) EditMessage(channelID, messageID, content string) error {
	return ds.bot.EditMessage(channelID, messageID, content)
}

func (ds *discordSender) Typing(channelID string) error {
	return ds.bot.Typing(channelID)
}

func (ds *discordSender) SendWithButtons(channelID string, buttons []ActionButton, content string) error {
	// Convert generic ActionButtons to Discord-specific OutboundMessage
	// Discord buttons are handled via formatPlanMessage which creates OutboundMessage directly.
	// This method is a fallback for non-plan button use cases.
	return ds.bot.Send(&discordbot.OutboundMessage{
		ChannelID: channelID,
		Content:   content,
	})
}

func (ds *discordSender) SendAudio(channelID string, audio []byte) error {
	return ds.bot.SendAudio(channelID, audio)
}

func (ds *discordSender) SupportsButtons() bool { return true }
func (ds *discordSender) SupportsAudio() bool   { return true }