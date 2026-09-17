package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/emaharmony/prizm/internal/event"
)

// AgentConfig defines an agent that subscribes to events and processes them.
type AgentConfig struct {
	Name       string   `json:"name"`
	Subscribes []string `json:"subscribes"`
	Model      string   `json:"model,omitempty"`
	MaxPending int      `json:"max_pending,omitempty"`
}

// AgentRuntime connects to the Prizm bus, subscribes to events,
// and dispatches them to registered agent handlers.
type AgentRuntime struct {
	nc      *nats.Conn
	js      nats.JetStreamContext
	config  AgentConfig
	handler func(event.Event)
	mu      sync.Mutex
	pending int
}

var (
	busURL    = flag.String("bus", "nats://localhost:4222", "Prizm bus URL")
	agentName = flag.String("name", "", "Agent name (required)")
	subs      = flag.String("subs", "", "Comma-separated event subjects to subscribe to")
	model     = flag.String("model", "", "LLM model to use for this agent")
)

func main() {
	flag.Parse()

	if *agentName == "" {
		log.Fatal("prizm-agent: -name is required")
	}

	config := AgentConfig{
		Name:       *agentName,
		Subscribes: parseSubs(*subs),
		Model:      *model,
		MaxPending: 100,
	}

	if len(config.Subscribes) == 0 {
		config.Subscribes = []string{"prizm.>"}
		log.Printf("prizm-agent: no subscriptions specified, defaulting to prizm.>")
	}

	log.SetFlags(log.Ltime | log.Lshortfile)
	log.Printf("prizm-agent: starting agent '%s'...", config.Name)

	// ── Connect to NATS ──────────────────────────────────────────
	nc, err := nats.Connect(*busURL, nats.Name(fmt.Sprintf("agent-%s", config.Name)))
	if err != nil {
		log.Fatalf("prizm-agent: failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		log.Fatalf("prizm-agent: failed to init JetStream: %v", err)
	}

	log.Printf("prizm-agent: connected to %s", *busURL)

	// ── Create Agent Runtime ──────────────────────────────────────
	runtime := &AgentRuntime{
		nc:     nc,
		js:     js,
		config: config,
		handler: func(evt event.Event) {
			// Default handler: log the event and emit a processed event
			log.Printf("  🔮 [%s] received %s from %s", config.Name, evt.Type, evt.Source)
		},
		mu:      sync.Mutex{},
		pending: 0,
	}

	// ── Subscribe to configured subjects ───────────────────────────
	for _, subject := range config.Subscribes {
		durableName := fmt.Sprintf("agent-%s-%s", config.Name, sanitizeSubject(subject))

		sub, err := js.Subscribe(subject, runtime.handleMessage, nats.Durable(durableName))
		if err != nil {
			log.Fatalf("prizm-agent: failed to subscribe to %s: %v", subject, err)
		}
		defer sub.Unsubscribe()
		log.Printf("prizm-agent: subscribed to %s (durable=%s)", subject, durableName)
	}

	// ── Publish agent.started event ────────────────────────────────
	runtime.publish("prizm.agent.started", map[string]any{
		"agent_name": config.Name,
		"model":      config.Model,
		"subscribes": config.Subscribes,
	})

	log.Printf("prizm-agent: '%s' running. press ctrl+c to stop.", config.Name)

	// ── Wait for shutdown ──────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Periodic heartbeat
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			runtime.mu.Lock()
			pending := runtime.pending
			runtime.mu.Unlock()
			runtime.publish("prizm.agent.heartbeat", map[string]any{
				"agent_name": config.Name,
				"pending":    pending,
				"status":     "running",
			})
		}
	}()

	<-sigCh

	log.Printf("prizm-agent: '%s' shutting down...", config.Name)
	runtime.publish("prizm.agent.stopped", map[string]any{
		"agent_name": config.Name,
		"status":     "graceful_shutdown",
	})
	time.Sleep(500 * time.Millisecond) // Flush pending messages
}

func (r *AgentRuntime) handleMessage(msg *nats.Msg) {
	evt, err := event.EventFromBytes(msg.Data)
	if err != nil {
		log.Printf("  ⚠️ [%s] invalid event: %v", r.config.Name, err)
		return
	}

	r.mu.Lock()
	r.pending++
	pending := r.pending
	r.mu.Unlock()

	// The pending counter is intentionally loose/observational:
	// it tracks how many events are being processed concurrently.
	// The handler runs without the lock held since it may take time.
	// This is fine for V1 observability — it's not used for flow control.

	// Call the handler
	r.handler(evt)

	r.mu.Lock()
	r.pending--
	r.mu.Unlock()

	// Emit agent.decision event (agents process events and produce decisions)
	// This is a placeholder — real agents will have custom logic
	log.Printf("  🔮 [%s] processed %s (pending=%d)", r.config.Name, evt.Type, pending-1)
}

func (r *AgentRuntime) publish(eventType string, payload map[string]any) event.Event {
	evt := event.NewEvent(eventType, r.config.Name, payload)
	data, err := evt.ToJSON()
	if err != nil {
		log.Printf("  ⚠️ [%s] failed to marshal event: %v", r.config.Name, err)
		return evt
	}
	if _, err := r.js.Publish(eventType, data); err != nil {
		log.Printf("  ⚠️ [%s] failed to publish %s: %v", r.config.Name, eventType, err)
	}
	return evt
}

// sanitizeSubject replaces NATS-invalid characters for durable consumer names.
func sanitizeSubject(s string) string {
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, "*", "all")
	s = strings.ReplaceAll(s, ">", "all")
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

func parseSubs(s string) []string {
	if s == "" {
		return nil
	}
	subs := strings.Split(s, ",")
	result := make([]string, 0, len(subs))
	for _, sub := range subs {
		sub = strings.TrimSpace(sub)
		if sub != "" {
			result = append(result, sub)
		}
	}
	return result
}
