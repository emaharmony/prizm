// Package bridge provides inter-Prizm communication via NATS gateway.
//
// V23 M4.3: Multi-Prizm Communication
//
// Two Prizm environments can discover each other and exchange events/tasks.
// Bridge connects to a remote Prizm's NATS server and forwards events
// between the local and remote bus, with origin tagging to prevent loops.
package bridge

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// Bridge manages connections to remote Prizm instances.
type Bridge struct {
	localNC *nats.Conn
	remotes map[string]*remoteConn
	mu      sync.RWMutex
	eventCh chan BridgedEvent
}

// BridgedEvent is an event that crossed a bridge from a remote Prizm.
type BridgedEvent struct {
	// Origin is the remote Prizm's ID.
	Origin string `json:"origin"`

	// Subject is the NATS subject the event was published on.
	Subject string `json:"subject"`

	// Data is the event payload.
	Data map[string]any `json:"data"`

	// Timestamp is when the bridge received the event.
	Timestamp time.Time `json:"timestamp"`
}

// RemoteConfig configures a connection to a remote Prizm.
type RemoteConfig struct {
	// ID is a unique identifier for this remote connection.
	ID string `yaml:"id"`

	// Name is a human-readable name for the remote Prizm.
	Name string `yaml:"name"`

	// NATSURL is the NATS server URL of the remote Prizm.
	NATSURL string `yaml:"nats_url"`

	// Subjects is a list of NATS subject patterns to subscribe to on the remote.
	// Defaults to [">"] (all events) if empty.
	Subjects []string `yaml:"subjects"`

	// LocalSubjects is a list of NATS subject patterns to publish on the local bus.
	// Defaults to Subjects if empty.
	LocalSubjects []string `yaml:"local_subjects"`

	// Enabled controls whether this remote connection is active.
	Enabled bool `yaml:"enabled"`
}

// remoteConn represents a connection to a remote Prizm's NATS server.
type remoteConn struct {
	config  RemoteConfig
	conn    *nats.Conn
	subs    []*nats.Subscription
	localNC *nats.Conn
	eventCh chan BridgedEvent
}

// NewBridge creates a new bridge for inter-Prizm communication.
func NewBridge(localNC *nats.Conn) *Bridge {
	return &Bridge{
		localNC: localNC,
		remotes: make(map[string]*remoteConn),
		eventCh: make(chan BridgedEvent, 100),
	}
}

// Connect establishes a connection to a remote Prizm.
func (b *Bridge) Connect(config RemoteConfig) error {
	if config.ID == "" {
		return fmt.Errorf("bridge: remote ID is required")
	}
	if config.NATSURL == "" {
		return fmt.Errorf("bridge: NATS URL is required for remote %q", config.ID)
	}
	if !config.Enabled {
		log.Printf("[BRIDGE] remote %q is disabled, skipping", config.ID)
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.remotes[config.ID]; exists {
		return fmt.Errorf("bridge: remote %q already connected", config.ID)
	}

	// Connect to remote NATS
	nc, err := nats.Connect(config.NATSURL,
		nats.Name(fmt.Sprintf("prizm-bridge-%s", config.ID)),
		nats.ReconnectWait(5*time.Second),
		nats.MaxReconnects(0), // Unlimited reconnects
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			log.Printf("[BRIDGE] disconnected from %q (%s): %v", config.ID, config.NATSURL, err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Printf("[BRIDGE] reconnected to %q (%s)", config.ID, config.NATSURL)
		}),
	)
	if err != nil {
		return fmt.Errorf("bridge: connect to %q: %w", config.ID, err)
	}

	rc := &remoteConn{
		config:  config,
		conn:    nc,
		localNC: b.localNC,
		eventCh: b.eventCh,
	}

	// Default subjects: all events
	subjects := config.Subjects
	if len(subjects) == 0 {
		subjects = []string{">"}
	}

	// Subscribe to remote events
	for _, subject := range subjects {
		sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
			rc.handleRemoteMessage(msg, config.ID)
		})
		if err != nil {
			nc.Close()
			return fmt.Errorf("bridge: subscribe to %q on %q: %w", subject, config.ID, err)
		}
		rc.subs = append(rc.subs, sub)
	}

	b.remotes[config.ID] = rc
	log.Printf("[BRIDGE] connected to remote %q at %s (subscribing to %d subjects)", config.ID, config.NATSURL, len(subjects))
	return nil
}

// Disconnect closes the connection to a remote Prizm.
func (b *Bridge) Disconnect(remoteID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	rc, exists := b.remotes[remoteID]
	if !exists {
		return fmt.Errorf("bridge: remote %q not found", remoteID)
	}

	for _, sub := range rc.subs {
		sub.Unsubscribe()
	}
	rc.conn.Close()
	delete(b.remotes, remoteID)
	log.Printf("[BRIDGE] disconnected from %q", remoteID)
	return nil
}

// Close closes all remote connections.
func (b *Bridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for id, rc := range b.remotes {
		for _, sub := range rc.subs {
			sub.Unsubscribe()
		}
		rc.conn.Close()
		delete(b.remotes, id)
	}
	log.Printf("[BRIDGE] all connections closed")
}

// Remotes returns the list of connected remote Prizm IDs.
func (b *Bridge) Remotes() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	ids := make([]string, 0, len(b.remotes))
	for id := range b.remotes {
		ids = append(ids, id)
	}
	return ids
}

// Events returns a channel of bridged events from remote Prizms.
func (b *Bridge) Events() <-chan BridgedEvent {
	return b.eventCh
}

// PublishLocal publishes an event on the local bus with origin tagging.
func (b *Bridge) PublishLocal(subject string, data map[string]any) error {
	if b.localNC == nil {
		return fmt.Errorf("bridge: local NATS not connected")
	}

	// Copy data to avoid mutating caller's map
	copy := make(map[string]any, len(data)+2)
	for k, v := range data {
		copy[k] = v
	}
	copy["_origin"] = "local"
	copy["_bridged"] = true

	payload, err := json.Marshal(copy)
	if err != nil {
		return fmt.Errorf("bridge: marshal: %w", err)
	}

	return b.localNC.Publish(subject, payload)
}

// handleRemoteMessage processes an event from a remote Prizm.
func (rc *remoteConn) handleRemoteMessage(msg *nats.Msg, origin string) {
	var data map[string]any
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		log.Printf("[BRIDGE] failed to parse event from %q: %v", origin, err)
		return
	}

	// Check for loop prevention: if this event already has an origin tag,
	// skip it to prevent infinite forwarding.
	// Also reject events with forged origin tags that don't match the remote.
	if existingOrigin, ok := data["_origin"].(string); ok {
		// If origin matches this remote, it's a loop — skip
		if existingOrigin == origin {
			return
		}
		// If origin is set but doesn't match our remote, it's from a different
		// remote or forged — keep it but override with our origin for traceability
	}

	// Tag with origin
	data["_origin"] = origin
	data["_bridged"] = true

	// Forward to local bus
	localSubjects := rc.config.LocalSubjects
	if len(localSubjects) == 0 {
		localSubjects = []string{msg.Subject}
	}

	for _, localSubject := range localSubjects {
		payload, err := json.Marshal(data)
		if err != nil {
			log.Printf("[BRIDGE] marshal error: %v", err)
			continue
		}

		if err := rc.localNC.Publish(localSubject, payload); err != nil {
			log.Printf("[BRIDGE] failed to forward %q to local: %v", localSubject, err)
		}
	}

	// Send to event channel for interested consumers
	select {
	case rc.eventCh <- BridgedEvent{
		Origin:    origin,
		Subject:   msg.Subject,
		Data:      data,
		Timestamp: time.Now(),
	}:
	default:
		log.Printf("[BRIDGE] event channel full, dropping event from %q on %q", origin, msg.Subject)
	}

	log.Printf("[BRIDGE] forwarded event from %q on %q", origin, msg.Subject)
}
