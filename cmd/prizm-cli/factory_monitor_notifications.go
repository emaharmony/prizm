package main

import (
	"encoding/json"
	"log"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/factorymonitor"
	"github.com/nats-io/nats.go"
)

func startFactoryMonitorNotifications(nc *nats.Conn, bot discordBotClient, channelID string) {
	if nc == nil || bot == nil || channelID == "" {
		return
	}
	subscribeFactoryMonitorTaskEvent(nc, bot, channelID, factorymonitor.EventStatusChanged)
	subscribeFactoryMonitorTaskEvent(nc, bot, channelID, factorymonitor.EventStatusStuck)
	_, err := nc.Subscribe(factorymonitor.EventStatusDigest, func(msg *nats.Msg) {
		var snap factorymonitor.Snapshot
		if err := json.Unmarshal(msg.Data, &snap); err != nil {
			log.Printf("[FACTORY-MONITOR] WARN invalid digest event: %v", err)
			return
		}
		sendFactoryMonitorMessage(bot, channelID, factorymonitor.FormatDigestMessage(snap))
	})
	if err != nil {
		log.Printf("[FACTORY-MONITOR] WARN subscribe %s: %v", factorymonitor.EventStatusDigest, err)
	}
}

func subscribeFactoryMonitorTaskEvent(nc *nats.Conn, bot discordBotClient, channelID, subject string) {
	_, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		var st factorymonitor.TaskStatus
		if err := json.Unmarshal(msg.Data, &st); err != nil {
			log.Printf("[FACTORY-MONITOR] WARN invalid task event on %s: %v", subject, err)
			return
		}
		sendFactoryMonitorMessage(bot, channelID, factorymonitor.FormatTaskMessage(subject, st))
	})
	if err != nil {
		log.Printf("[FACTORY-MONITOR] WARN subscribe %s: %v", subject, err)
	}
}

func sendFactoryMonitorMessage(bot discordBotClient, channelID, content string) {
	if content == "" {
		return
	}
	if err := bot.Send(&discordbot.OutboundMessage{ChannelID: channelID, Content: content}); err != nil {
		log.Printf("[FACTORY-MONITOR] WARN send Discord notification: %v", err)
	}
}
