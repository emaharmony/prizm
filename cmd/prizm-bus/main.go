package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/emaharmony/prizm/internal/event"
)

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)
	log.Println("prizm: starting event bus...")

	// ── Embedded NATS Server ──────────────────────────────────────
	opts := &server.Options{
		ServerName: "prizm-local",
		Port:       4222,
		NoLog:      false,
		Debug:      false,
		Trace:      false,
		JetStream:  true,
		StoreDir:   "./prizm-data",
	}

	ns, err := server.NewServer(opts)
	if err != nil {
		log.Fatalf("prizm: failed to create NATS server: %v", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		log.Fatalf("prizm: NATS server did not start in time")
	}
	log.Printf("prizm: NATS server running on %s", ns.ClientURL())

	// ── Connect as a client ───────────────────────────────────────
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		log.Fatalf("prizm: failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	// ── Initialize JetStream ──────────────────────────────────────
	js, err := nc.JetStream()
	if err != nil {
		log.Fatalf("prizm: failed to init JetStream: %v", err)
	}

	// Create the PRIZM stream — all events flow through here
	streamName := "PRIZM"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:      streamName,
		Subjects:  []string{"prizm.>"},
		Retention: nats.LimitsPolicy,
		MaxMsgs:   1000000,
		MaxBytes:  1024 * 1024 * 1024,
		MaxAge:    7 * 24 * time.Hour,
		Storage:   nats.FileStorage,
	})
	if err != nil {
		log.Fatalf("prizm: failed to create stream: %v", err)
	}
	log.Printf("prizm: stream '%s' created (subjects: prizm.>)", streamName)

	// ── Subscribe: catch-all logger ────────────────────────────────
	logSub, err := js.Subscribe("prizm.>", func(msg *nats.Msg) {
		evt, err := event.EventFromBytes(msg.Data)
		if err != nil {
			log.Printf("  ⚡ [%s] (invalid event: %v)", msg.Subject, err)
			return
		}
		log.Printf("  💎 [%s] id=%s source=%s", evt.Type, shortEventID(evt.ID), evt.Source)
	}, nats.Durable("logger"), nats.DeliverAll())
	if err != nil {
		log.Fatalf("prizm: failed to create logger subscription: %v", err)
	}
	defer logSub.Unsubscribe()

	// ── Subscribe: agent decision handler ──────────────────────────
	decisionSub, err := js.Subscribe("prizm.agent.decision", func(msg *nats.Msg) {
		evt, err := event.EventFromBytes(msg.Data)
		if err != nil {
			return
		}
		action, _ := evt.Payload["action"].(string)
		confidence, _ := evt.Payload["confidence"].(float64)
		log.Printf("  🧠 AGENT DECISION: action=%s confidence=%.0f%% source=%s", action, confidence*100, evt.Source)

		// React: emit a follow-up event (demonstrates event chain)
		if action == "spawn_coder" {
			spawnEvt := event.NewEvent("prizm.agent.spawned", "prizm-bus", map[string]any{
				"agent_type": "coder",
				"reason":     "agent decision requested spawn",
			})
			spawnEvt.CorrelationID = evt.CorrelationID
			spawnEvt.ParentID = evt.ID
			data, _ := spawnEvt.ToJSON()
			js.Publish("prizm.agent.spawned", data)
		}
	}, nats.Durable("decision-handler"), nats.DeliverAll())
	if err != nil {
		log.Fatalf("prizm: failed to create decision subscription: %v", err)
	}
	defer decisionSub.Unsubscribe()

	// ── Subscribe: memory store handler ────────────────────────────
	memorySub, err := js.Subscribe("prizm.memory.stored", func(msg *nats.Msg) {
		evt, err := event.EventFromBytes(msg.Data)
		if err != nil {
			return
		}
		category, _ := evt.Payload["category"].(string)
		tier, _ := evt.Payload["tier"].(string)
		log.Printf("  📝 MEMORY STORED: category=%s tier=%s", category, tier)
	}, nats.Durable("memory-store"), nats.DeliverAll())
	if err != nil {
		log.Fatalf("prizm: failed to create memory subscription: %v", err)
	}
	defer memorySub.Unsubscribe()

	// ── Publish V1 test events ─────────────────────────────────────
	log.Println("prizm: publishing V1 test events...")
	correlationID := event.NewCorrelationID()
	sessionID := event.NewSessionID()
	metadata := event.EventMetadata{
		RunID:     event.NewRunID(),
		SessionID: sessionID,
		Project:   "prizm",
		Agent:     "lumi",
	}

	// Event 1: A message arrives from Discord
	evt1 := event.NewEvent("prizm.channel.received", "discord", map[string]any{
		"channel":    "discord",
		"channel_id": "1491622581348864162",
		"sender":     "ema",
		"text":       "deploy the new feature",
	})
	evt1.CorrelationID = correlationID
	evt1.Metadata = metadata
	data1, _ := evt1.ToJSON()
	js.Publish("prizm.channel.received", data1)

	time.Sleep(100 * time.Millisecond)

	// Event 2: Agent makes a decision (fan-out reaction)
	evt2 := event.NewEvent("prizm.agent.decision", "lumi", map[string]any{
		"reasoning":  "User wants deployment, spawning coder agent",
		"action":     "spawn_coder",
		"confidence": 0.92,
	})
	evt2.CorrelationID = correlationID
	evt2.ParentID = evt1.ID
	evt2.Metadata = metadata
	data2, _ := evt2.ToJSON()
	js.Publish("prizm.agent.decision", data2)

	time.Sleep(100 * time.Millisecond)

	// Event 3: Memory is stored
	evt3 := event.NewEvent("prizm.memory.stored", "lumi", map[string]any{
		"category": "decision",
		"tier":     "session",
		"content":  "Ema requested deployment of new feature",
	})
	evt3.CorrelationID = correlationID
	evt3.ParentID = evt2.ID
	evt3.Metadata = metadata
	data3, _ := evt3.ToJSON()
	js.Publish("prizm.memory.stored", data3)

	log.Println("prizm: 3 test events published (V1 event schema)")
	log.Println("prizm: event bus running. press ctrl+c to stop.")

	// ── Print stream stats ────────────────────────────────────────
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			info, err := js.StreamInfo(streamName)
			if err != nil {
				continue
			}
			log.Printf("  📊 stream: %d messages, %d bytes", info.State.Msgs, info.State.Bytes)
		}
	}()

	// ── Wait for shutdown signal ───────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("prizm: shutting down...")
	logSub.Unsubscribe()
	decisionSub.Unsubscribe()
	memorySub.Unsubscribe()
	nc.Close()
	ns.Shutdown()
	log.Println("prizm: stopped.")
}

func shortEventID(id string) string {
	if len(id) <= 20 {
		return id
	}
	return id[:20]
}
