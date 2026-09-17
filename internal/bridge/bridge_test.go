package bridge

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/bus"
	"github.com/nats-io/nats.go"
)

func TestBridge_NewBridge(t *testing.T) {
	b := NewBridge(nil)
	if b == nil {
		t.Fatal("expected non-nil bridge")
	}
	if len(b.Remotes()) != 0 {
		t.Error("expected no remotes on new bridge")
	}
}

func TestBridge_ConnectDisabled(t *testing.T) {
	b := NewBridge(nil)

	err := b.Connect(RemoteConfig{
		ID:      "test-remote",
		NATSURL: "nats://localhost:4222",
		Enabled: false,
	})
	if err != nil {
		t.Errorf("expected no error for disabled remote, got: %v", err)
	}
	if len(b.Remotes()) != 0 {
		t.Error("disabled remote should not be in remotes list")
	}
}

func TestBridge_ConnectMissingID(t *testing.T) {
	b := NewBridge(nil)

	err := b.Connect(RemoteConfig{
		NATSURL: "nats://localhost:4222",
		Enabled: true,
	})
	if err == nil {
		t.Error("expected error for missing ID")
	}
}

func TestBridge_ConnectMissingURL(t *testing.T) {
	b := NewBridge(nil)

	err := b.Connect(RemoteConfig{
		ID:      "test-remote",
		Enabled: true,
	})
	if err == nil {
		t.Error("expected error for missing NATS URL")
	}
}

func TestBridge_ConnectDuplicateID(t *testing.T) {
	b := NewBridge(nil)

	// Connect to embedded NATS for the first connection
	nc, cleanup := startTestNATS(t)
	defer cleanup()

	err := b.Connect(RemoteConfig{
		ID:      "test-remote",
		NATSURL: nc.ConnectedUrl(),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("expected successful connection, got: %v", err)
	}
	defer b.Disconnect("test-remote")

	// Try connecting with the same ID
	err = b.Connect(RemoteConfig{
		ID:      "test-remote",
		NATSURL: nc.ConnectedUrl(),
		Enabled: true,
	})
	if err == nil {
		t.Error("expected error for duplicate ID")
	}
}

func TestBridge_DisconnectNonExistent(t *testing.T) {
	b := NewBridge(nil)

	err := b.Disconnect("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent remote")
	}
}

func TestBridge_PublishLocalNoConnection(t *testing.T) {
	b := NewBridge(nil)

	err := b.PublishLocal("test.subject", map[string]any{"key": "value"})
	if err == nil {
		t.Error("expected error when local NATS is nil")
	}
}

func TestBridge_PublishLocal(t *testing.T) {
	nc, cleanup := startTestNATS(t)
	defer cleanup()

	b := NewBridge(nc)

	err := b.PublishLocal("test.subject", map[string]any{"key": "value"})
	if err != nil {
		t.Fatalf("expected successful publish, got: %v", err)
	}
}

func TestBridge_EventsChannel(t *testing.T) {
	b := NewBridge(nil)
	ch := b.Events()
	if ch == nil {
		t.Error("expected non-nil events channel")
	}
}

func TestBridge_Remotes(t *testing.T) {
	b := NewBridge(nil)
	remotes := b.Remotes()
	if remotes == nil {
		t.Error("expected non-nil remotes list")
	}
}

func TestBridge_FullRoundTrip(t *testing.T) {
	// Start two NATS servers: local and remote
	localNC, localCleanup := startTestNATS(t)
	defer localCleanup()

	remoteNC, remoteCleanup := startTestNATS(t)
	defer remoteCleanup()

	// Create bridge with local connection
	bridge := NewBridge(localNC)

	// Connect to remote
	err := bridge.Connect(RemoteConfig{
		ID:       "remote-prizm",
		NATSURL:  remoteNC.ConnectedUrl(),
		Subjects: []string{">"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("failed to connect to remote: %v", err)
	}
	defer bridge.Close()

	// Give subscriptions time to propagate
	time.Sleep(100 * time.Millisecond)

	// Subscribe on local bus to receive forwarded events
	received := make(chan map[string]any, 10)
	_, err = localNC.Subscribe(">", func(msg *nats.Msg) {
		var data map[string]any
		if err := json.Unmarshal(msg.Data, &data); err == nil {
			// Only count bridged events
			if data["_bridged"] == true {
				select {
				case received <- data:
				default:
				}
			}
		}
	})
	if err != nil {
		t.Fatalf("failed to subscribe: %v", err)
	}

	// Give local subscription time to register
	localNC.Flush()
	time.Sleep(50 * time.Millisecond)

	// Publish on remote bus
	err = remoteNC.Publish("remote.agent.started", []byte(`{"agent":"mango","status":"running"}`))
	if err != nil {
		t.Fatalf("failed to publish: %v", err)
	}
	remoteNC.Flush()

	// Wait for the bridge to forward the event
	select {
	case data := <-received:
		if data["_origin"] != "remote-prizm" {
			t.Errorf("expected origin remote-prizm, got %v", data["_origin"])
		}
		if data["_bridged"] != true {
			t.Error("expected _bridged=true")
		}
		if data["agent"] != "mango" {
			t.Errorf("expected agent=mango, got %v", data["agent"])
		}
	case <-time.After(5 * time.Second):
		t.Error("timeout waiting for bridged event")
	}
}

func TestBridge_LoopPrevention(t *testing.T) {
	localNC, localCleanup := startTestNATS(t)
	defer localCleanup()

	remoteNC, remoteCleanup := startTestNATS(t)
	defer remoteCleanup()

	bridge := NewBridge(localNC)

	err := bridge.Connect(RemoteConfig{
		ID:       "loop-test",
		NATSURL:  remoteNC.ConnectedUrl(),
		Subjects: []string{">"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer bridge.Close()

	// Publish an event with the same origin as our remote
	// This should be dropped (loop prevention)
	payload, _ := json.Marshal(map[string]any{
		"_origin": "loop-test",
		"message": "this should be dropped",
	})
	remoteNC.Publish("test.loop", payload)

	// The event should be dropped because origin matches
	time.Sleep(500 * time.Millisecond)

	// Verify no events came through
	select {
	case <-bridge.Events():
		t.Error("expected event to be dropped due to loop prevention")
	default:
		// Good — event was dropped
	}
}

// Helper to start a test NATS server
func startTestNATS(t *testing.T) (*nats.Conn, func()) {
	t.Helper()

	// Use embedded NATS from bus package
	url, cleanup, err := startEmbeddedNATS()
	if err != nil {
		t.Fatalf("failed to start embedded NATS: %v", err)
	}

	nc, err := nats.Connect(url)
	if err != nil {
		cleanup()
		t.Fatalf("failed to connect to embedded NATS: %v", err)
	}

	return nc, func() {
		nc.Close()
		cleanup()
	}
}

// We need to expose the embedded NATS startup. Use the bus package.
func startEmbeddedNATS() (string, func(), error) {
	// Import from bus package
	return bus.StartEmbeddedBus(0)
}

func TestBridge_Close(t *testing.T) {
	b := NewBridge(nil)
	b.Close()
	// Should not panic
}

func TestBridge_HandleRemoteMessage_InvalidJSON(t *testing.T) {
	// This tests the error path for invalid JSON in handleRemoteMessage
	// We need a local NATS to forward to
	localNC, localCleanup := startTestNATS(t)
	defer localCleanup()

	remoteNC, remoteCleanup := startTestNATS(t)
	defer remoteCleanup()

	bridge := NewBridge(localNC)

	err := bridge.Connect(RemoteConfig{
		ID:       "invalid-json-test",
		NATSURL:  remoteNC.ConnectedUrl(),
		Subjects: []string{">"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer bridge.Close()

	// Publish invalid JSON
	remoteNC.Publish("test.invalid", []byte(`not json`))

	// Give it time to process
	time.Sleep(500 * time.Millisecond)

	// Should not crash, just log
}
