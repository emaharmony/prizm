// Package mcp adapts external Model Context Protocol (MCP) tool servers into
// Prizm's tool.Tool abstraction, so remote MCP tools register into the standard
// tool.Registry and inherit Prizm's whole execution substrate — policy gating,
// approval, path/write enforcement, and audit events — with no engine change.
//
// This package is transport-agnostic: it depends only on the Client interface
// (ListTools/CallTool). A concrete transport (stdio JSON-RPC, streamable HTTP) is
// a separate, swappable implementation, which keeps the schema-mapping and
// tool-adapter logic unit-testable without spawning a subprocess or a network.
package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/emaharmony/prizm/internal/tool"
)

// ToolDef is an MCP tool definition as returned by `tools/list`.
type ToolDef struct {
	Name        string
	Description string
	InputSchema map[string]any // JSON Schema object: {type, properties, required}
}

// CallResult is the outcome of an MCP `tools/call`.
type CallResult struct {
	Content string         // flattened text content
	IsError bool           // MCP isError flag
	Raw     map[string]any // full result for callers that want structure
}

// Client is the minimal MCP surface Prizm needs from a server. A concrete
// transport implements this; tests provide a mock.
type Client interface {
	ListTools(ctx context.Context) ([]ToolDef, error)
	CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error)
}

// ToolName builds the namespaced Prizm tool name for an MCP tool, so tools from
// different servers can't collide and are easy to identify/policy-scope.
func ToolName(server, tool string) string {
	return "mcp_" + sanitize(server) + "_" + sanitize(tool)
}

func sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// mappedTool adapts one MCP tool to the tool.Tool interface.
type mappedTool struct {
	client Client
	server string
	def    ToolDef
}

func (m *mappedTool) Name() string { return ToolName(m.server, m.def.Name) }

func (m *mappedTool) Description() string {
	d := m.def.Description
	if d == "" {
		d = m.def.Name
	}
	return fmt.Sprintf("[MCP:%s] %s", m.server, d)
}

func (m *mappedTool) Schema() tool.ToolSchema { return SchemaFromJSON(m.def.InputSchema) }

// Execute proxies the call to the MCP server and maps the result to a ToolResult.
// An MCP error (isError) becomes Success=false rather than a Go error, so the
// agent sees a normal failed-tool result and can recover.
func (m *mappedTool) Execute(ctx context.Context, input map[string]any) (tool.ToolResult, error) {
	res, err := m.client.CallTool(ctx, m.def.Name, input)
	if err != nil {
		return tool.ToolResult{Success: false, Error: fmt.Sprintf("mcp call %q: %v", m.def.Name, err)}, nil
	}
	out := map[string]any{"content": res.Content}
	if res.Raw != nil {
		out["raw"] = res.Raw
	}
	tr := tool.ToolResult{Success: !res.IsError, Output: out}
	if res.IsError {
		tr.Error = res.Content
	}
	return tr, nil
}

// SchemaFromJSON maps an MCP JSON-Schema input object to a Prizm ToolSchema.
// Unknown/missing fields degrade gracefully to an empty input set.
func SchemaFromJSON(js map[string]any) tool.ToolSchema {
	schema := tool.ToolSchema{Input: map[string]tool.ParamSpec{}, Output: tool.ParamSpec{Type: "object", Description: "MCP tool result"}}
	props, _ := js["properties"].(map[string]any)
	required := map[string]bool{}
	if reqList, ok := js["required"].([]any); ok {
		for _, r := range reqList {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	for name, raw := range props {
		spec := tool.ParamSpec{Type: "string", Required: required[name]}
		if p, ok := raw.(map[string]any); ok {
			if t, ok := p["type"].(string); ok && t != "" {
				spec.Type = t
			}
			if d, ok := p["description"].(string); ok {
				spec.Description = d
			}
		}
		schema.Input[name] = spec
	}
	return schema
}

// RegisterServer lists the server's tools and registers a policy-gated wrapper for
// each into reg. Returns the registered tool names. A failure to register one tool
// (e.g. a name collision) is collected and reported, but does not abort the rest.
func RegisterServer(ctx context.Context, reg *tool.Registry, server string, client Client) ([]string, error) {
	if reg == nil || client == nil {
		return nil, fmt.Errorf("mcp: nil registry or client")
	}
	defs, err := client.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp: list tools from %q: %w", server, err)
	}
	var registered []string
	var errs []string
	for _, d := range defs {
		mt := &mappedTool{client: client, server: server, def: d}
		if rerr := reg.Register(mt); rerr != nil {
			errs = append(errs, rerr.Error())
			continue
		}
		registered = append(registered, mt.Name())
	}
	if len(errs) > 0 {
		return registered, fmt.Errorf("mcp: %d tool(s) not registered: %s", len(errs), strings.Join(errs, "; "))
	}
	return registered, nil
}
