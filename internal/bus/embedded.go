// Package bus provides NATS integration and embedded bus support for Prizm.
//
// V14d adds the --embedded-bus flag for getting started without installing
// NATS separately. StartEmbeddedBus launches an in-process NATS server,
// eliminating the "install NATS first" friction for new users.
package bus

import (
	"fmt"
	"net"
	"strconv"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// EmbeddedBus wraps an in-process NATS server for development use.
// It eliminates the need for an external NATS installation.
type EmbeddedBus struct {
	//lint:ignore U1000 retained for the exported development wrapper contract
	server *natsserver.Server
	//lint:ignore U1000 retained for the exported development wrapper contract
	port int
}

// StartEmbeddedBus starts an in-process NATS server on the given port.
// Returns the NATS URL for clients to connect to and a cleanup function.
//
// Usage:
//
//	url, cleanup, err := bus.StartEmbeddedBus(4222)
//	if err != nil { ... }
//	defer cleanup()
//	// Connect with: nats.Connect(url)
func StartEmbeddedBus(port int) (string, func(), error) {
	// Find an available port if 0 is specified
	if port == 0 {
		var err error
		port, err = getAvailablePort()
		if err != nil {
			return "", nil, fmt.Errorf("embedded bus: find port: %w", err)
		}
	}

	cfg := &natsserver.Options{
		// Bind the embedded bus to loopback only. Cross-Prizm instances on the
		// same host connect via the returned nats://127.0.0.1 URL; exposing the
		// bus to the network is a deliberate future step using an external NATS.
		Host:           "127.0.0.1",
		Port:           port,
		NoLog:          true,
		NoSigs:         true,
		MaxControlLine: 4096,
	}

	srv, err := natsserver.NewServer(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("embedded bus: create server: %w", err)
	}

	go srv.Start()

	// Wait for server to be ready
	if !srv.ReadyForConnections(5 * 1e9) { // 5 seconds
		srv.Shutdown()
		return "", nil, fmt.Errorf("embedded bus: server did not start in time")
	}

	natsURL := fmt.Sprintf("nats://127.0.0.1:%d", port)
	cleanup := func() {
		srv.Shutdown()
	}

	return natsURL, cleanup, nil
}

// ConnectToBus creates a NATS connection to the given URL.
// This is a convenience function for connecting to either an embedded
// or external NATS server.
func ConnectToBus(url string) (*nats.Conn, error) {
	nc, err := nats.Connect(url, nats.Timeout(15*time.Second), nats.MaxReconnects(10))
	if err != nil {
		return nil, fmt.Errorf("bus: connect: %w", err)
	}
	return nc, nil
}

// getAvailablePort finds an available TCP port.
func getAvailablePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	return port, nil
}

// ParsePort converts a string port to an integer, with a default.
func ParsePort(portStr string, defaultPort int) int {
	if portStr == "" {
		return defaultPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return defaultPort
	}
	return port
}
