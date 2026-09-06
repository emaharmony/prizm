// Package main provides a Telegram adapter that wraps telegram.BotAdapter
// to satisfy the ChannelSender interface.
package main

import (
	"github.com/emaharmony/prizm/internal/adapter/builtin/telegram"
)

// telegramSender wraps a telegram.BotAdapter to satisfy ChannelSender.
type telegramSender struct {
	bot *telegram.BotAdapter
}

func (ts *telegramSender) Send(channelID, content string) error {
	return ts.bot.Send(&telegram.OutboundMessage{
		ChatID:  channelID,
		Content: content,
	})
}

func (ts *telegramSender) SendPlaceholder(channelID, content string) (string, error) {
	// Telegram doesn't have a native placeholder concept; send and return empty ID
	err := ts.bot.Send(&telegram.OutboundMessage{
		ChatID:  channelID,
		Content: content,
	})
	return "", err
}

func (ts *telegramSender) EditMessage(channelID, messageID, content string) error {
	return ts.bot.EditMessage(channelID, messageID, content)
}

func (ts *telegramSender) Typing(channelID string) error {
	return ts.bot.Typing(channelID)
}