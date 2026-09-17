// Package orchestrator provides the persistent daemon that runs Prizm as a
// live service. It manages agent lifecycles, routes messages, tracks sessions,
// and wires the event bus to registered actions.
//
// V20: The orchestrator is the brain of `prizm serve`. It starts the embedded
// NATS server, registers agents from config, sets up the session manager,
// agent router, and adapter connections, then runs until interrupted.
package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/event"
)

// Orchestrator manages the persistent Prizm service lifecycle.
type Orchestrator struct {
	mu sync.RWMutex

	// Config is the loaded Prizm configuration.
	Config *Config

	// Agent registry holds all registered agents.
	Agents *agent.Registry

	// EventStore persists events to SQLite.
	EventStore event.EventStore

	// NATS connection for publishing/subscribing.
	natsURL string
	cleanup func() // cleanup for embedded NATS

	// Health tracking
	startedAt time.Time
	healthy   bool

	// Shutdown signal
	ctx    context.Context
	cancel context.CancelFunc
}

// New creates a new Orchestrator from the given configuration.
func New(cfg *Config) (*Orchestrator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("orchestrator: invalid config: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Orchestrator{
		Config:  cfg,
		Agents:  agent.NewRegistry(),
		natsURL: cfg.Prizm.NATSURL,
		ctx:     ctx,
		cancel:  cancel,
		healthy: false,
	}, nil
}

// Start launches the orchestrator: embedded NATS, agent registration,
// session manager, and adapter connections. Blocks until shutdown.
func (o *Orchestrator) Start() error {
	o.mu.Lock()
	o.startedAt = time.Now()

	// Start embedded NATS if no external URL provided
	natsURL := o.natsURL
	if natsURL == "" {
		url, cleanup, err := bus.StartEmbeddedBus(0)
		if err != nil {
			o.mu.Unlock()
			return fmt.Errorf("orchestrator: start embedded bus: %w", err)
		}
		natsURL = url
		o.cleanup = cleanup
	}
	o.natsURL = natsURL

	// Agents are registered externally (by cmd_serve.go calling cfg.RegisterAgents)
	o.healthy = true
	o.mu.Unlock()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	return o.shutdown()
}

// shutdown performs graceful shutdown: drain events, close connections.
func (o *Orchestrator) shutdown() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.healthy = false
	o.cancel()

	if o.EventStore != nil {
		o.EventStore.Close()
	}

	if o.cleanup != nil {
		o.cleanup()
	}

	return nil
}

// Status returns the current orchestrator status for health checks.
func (o *Orchestrator) Status() *Status {
	o.mu.RLock()
	defer o.mu.RUnlock()

	agents := o.Agents.List()

	return &Status{
		Healthy:   o.healthy,
		StartedAt: o.startedAt,
		Uptime:    time.Since(o.startedAt),
		Agents:    agents,
		NATSURL:   o.natsURL,
	}
}

// Status represents the orchestrator's current state.
type Status struct {
	Healthy   bool          `json:"healthy"`
	StartedAt time.Time     `json:"started_at"`
	Uptime    time.Duration `json:"uptime"`
	Agents    []string      `json:"agents"`
	NATSURL   string        `json:"nats_url"`
}

// GetAgent returns the config for a specific agent by ID.
func (o *Orchestrator) GetAgent(id string) (*AgentConfig, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	for _, agent := range o.Config.Agents {
		if agent.ID == id {
			return &agent, nil
		}
	}
	return nil, fmt.Errorf("agent %q not found", id)
}

// Workflows returns the list of workflow names.
// Currently returns empty until workflow configs are added.
func (o *Orchestrator) Workflows() []string {
	return []string{}
}
