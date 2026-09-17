package main

import (
	"testing"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/orchestrator"
)

type finalReportBot struct {
	sent []*discordbot.OutboundMessage
}

func (b *finalReportBot) Typing(channelID string) error { return nil }
func (b *finalReportBot) Send(msg *discordbot.OutboundMessage) error {
	b.sent = append(b.sent, msg)
	return nil
}
func (b *finalReportBot) SendPlaceholder(channelID, content string) (string, error) {
	return "", nil
}
func (b *finalReportBot) EditMessage(channelID, messageID, content string) error {
	return nil
}
func (b *finalReportBot) SelfID() string { return "bot-self" }
func (b *finalReportBot) SendAudio(channelID string, audio []byte) error {
	return nil
}
func (b *finalReportBot) GetRecentMessages(channelID string, limit int) []discordbot.RecentMessage {
	return nil
}

func TestSendFinalReportIncludesStatusRunAndMessage(t *testing.T) {
	bot := &finalReportBot{}
	cc := &conversationContext{bot: bot, sender: &discordSender{bot: bot}}

	cc.sendFinalReport("chan-1", "timed_out", "run_123", "Task timed out.")

	if len(bot.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(bot.sent))
	}
	got := bot.sent[0].Content
	want := "Task TIMED_OUT (run run_123): Task timed out."
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestServeLLMTimeoutUsesConfig(t *testing.T) {
	cfg := &orchestrator.Config{}
	cfg.Prizm.LLMTimeoutSeconds = 42

	if got := serveLLMTimeout(cfg).Seconds(); got != 42 {
		t.Fatalf("timeout seconds = %v, want 42", got)
	}
}
