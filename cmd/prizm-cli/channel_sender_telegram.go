// Package main provides a Telegram adapter that wraps telegram.BotAdapter
// to satisfy the ChannelSender interface.
package main

import (
	"log"

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

func (ts *telegramSender) SendWithButtons(channelID string, buttons []ActionButton, content string) error {
	// Telegram supports inline keyboards — future implementation
	// For now, send plain text since button mapping isn't wired yet
	log.Printf("[TELEGRAM] SendWithButtons: buttons not yet implemented, sending plain text")
	return ts.bot.Send(&telegram.OutboundMessage{
		ChatID:  channelID,
		Content: content,
	})
}

func (ts *telegramSender) SendAudio(channelID string, audio []byte) error {
	// Telegram supports voice messages — future implementation
	log.Printf("[TELEGRAM] SendAudio: audio not yet implemented, skipping")
	return nil
}

func (ts *telegramSender) SupportsButtons() bool { return false }
func (ts *telegramSender) SupportsAudio() bool   { return false }