package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/emaharmony/prizm/internal/tool"
)

// register.go wires declared MCP servers into a live tool.Registry at startup.
// The client factory is injected so the orchestration (initialize → list →
// register) is unit-testable with a fake client; the production factory spawns
// stdio subprocesses.

// ServerSpec is a transport-neutral description of an MCP server to connect to.
type ServerSpec struct {
	Name    string
	Command string
	Args    []string
	Env     []string
	Enabled bool
}

// InitClient is anything that can perform the MCP handshake before use.
type InitClient interface {
	Client
	Initialize(ctx context.Context) error
}

// ClientFactory builds an initialized-capable client for a server spec.
type ClientFactory func(ctx context.Context, spec ServerSpec) (InitClient, error)

// ProcessClientFactory is the production factory: it spawns the server as a stdio
// subprocess. Callers pass this to RegisterServers in serve mode.
func ProcessClientFactory(ctx context.Context, spec ServerSpec) (InitClient, error) {
	return NewProcessClient(ctx, spec.Command, spec.Args, spec.Env)
}

// RegisterResult reports the outcome for one server.
type RegisterResult struct {
	Server string
	Tools  []string
	Err    error
}

// ProbeServer connects to a single server via factory, performs the handshake,
// and returns its tool definitions WITHOUT registering them — a live connectivity
// check for `prizm mcp probe`. The factory is injected so the connect→init→list
// flow is unit-testable with a fake client.
func ProbeServer(ctx context.Context, spec ServerSpec, factory ClientFactory) ([]ToolDef, error) {
	if strings.TrimSpace(spec.Name) == "" || strings.TrimSpace(spec.Command) == "" {
		return nil, fmt.Errorf("mcp server needs a name and command")
	}
	client, err := factory(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if c, ok := client.(interface{ Close() error }); ok {
		defer c.Close()
	}
	if err := client.Initialize(ctx); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	defs, err := client.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	return defs, nil
}

// RegisterServers connects to each enabled server via factory, performs the MCP
// handshake, and registers its tools into reg. One server failing does not abort
// the others — each result carries its own error. Returns a result per attempted
// (enabled) server.
func RegisterServers(ctx context.Context, reg *tool.Registry, specs []ServerSpec, factory ClientFactory) []RegisterResult {
	var out []RegisterResult
	for _, spec := range specs {
		if !spec.Enabled {
			continue
		}
		if strings.TrimSpace(spec.Name) == "" || strings.TrimSpace(spec.Command) == "" {
			out = append(out, RegisterResult{Server: spec.Name, Err: fmt.Errorf("mcp server needs a name and command")})
			continue
		}
		client, err := factory(ctx, spec)
		if err != nil {
			out = append(out, RegisterResult{Server: spec.Name, Err: fmt.Errorf("connect: %w", err)})
			continue
		}
		if err := client.Initialize(ctx); err != nil {
			out = append(out, RegisterResult{Server: spec.Name, Err: fmt.Errorf("initialize: %w", err)})
			continue
		}
		names, err := RegisterServer(ctx, reg, spec.Name, client)
		out = append(out, RegisterResult{Server: spec.Name, Tools: names, Err: err})
	}
	return out
}
