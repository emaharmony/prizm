// cmd_health.go implements the `prizm health` command (V1).
//
// Health check connects to the NATS event bus and verifies the PRIZM stream
// exists. This is useful for diagnosing connectivity issues before running
// a full agent pipeline.
//
// If the bus is reachable, it prints stream info and emits a health event.
// If the bus is unreachable, it exits with an error.
package main

import (
	"fmt"
	"os"

	"github.com/nats-io/nats.go"

	"github.com/emaharmony/prizm/internal/event"
)

// executeHealth connects to the NATS bus, checks the PRIZM stream,
// and prints stream metadata (message count, bytes, subjects).
// On success, it also emits a system_health event to the bus.
func executeHealth(busURL string) {
	nc, err := nats.Connect(busURL, nats.Name("prizm-health-check"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ NATS bus unreachable: %v\n", err)
		os.Exit(1)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ JetStream unavailable: %v\n", err)
		os.Exit(1)
	}

	info, err := js.StreamInfo("PRIZM")
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ PRIZM stream not found: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  ✅ Prizm Bus Health Check")
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  NATS URL:    %s\n", busURL)
	fmt.Printf("  Stream:      PRIZM\n")
	fmt.Printf("  Messages:    %d\n", info.State.Msgs)
	fmt.Printf("  Bytes:       %d\n", info.State.Bytes)
	fmt.Printf("  Subjects:    %v\n", info.Config.Subjects)
	fmt.Println("═══════════════════════════════════════════")

	// Emit a health event so the bus knows we checked in
	evt := event.NewEvent(event.V1EventTypes.SystemHealth, "prizm-cli", map[string]any{
		"nats_url":    busURL,
		"stream_msgs": info.State.Msgs,
		"status":      "healthy",
	})
	data, _ := evt.ToJSON()
	js.Publish(event.V1EventTypes.SystemHealth, data)
	fmt.Printf("  Health event emitted: %s\n", evt.ID)
}
