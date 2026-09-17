package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/emaharmony/prizm/internal/tool"
)

// mockClient is an in-memory MCP server for tests.
type mockClient struct {
	tools    []ToolDef
	listErr  error
	lastName string
	lastArgs map[string]any
	result   CallResult
	callErr  error
}

func (m *mockClient) ListTools(_ context.Context) ([]ToolDef, error) {
	return m.tools, m.listErr
}
func (m *mockClient) CallTool(_ context.Context, name string, args map[string]any) (CallResult, error) {
	m.lastName, m.lastArgs = name, args
	return m.result, m.callErr
}

func TestToolNameNamespacingAndSanitize(t *testing.T) {
	if got := ToolName("FileSystem", "read-file"); got != "mcp_filesystem_read_file" {
		t.Fatalf("unexpected name: %q", got)
	}
}

func TestSchemaFromJSON(t *testing.T) {
	js := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string", "description": "file path"},
			"depth": map[string]any{"type": "number"},
		},
		"required": []any{"path"},
	}
	s := SchemaFromJSON(js)
	if len(s.Input) != 2 {
		t.Fatalf("expected 2 inputs, got %d", len(s.Input))
	}
	if !s.Input["path"].Required || s.Input["path"].Description != "file path" {
		t.Fatalf("path spec wrong: %+v", s.Input["path"])
	}
	if s.Input["depth"].Type != "number" || s.Input["depth"].Required {
		t.Fatalf("depth spec wrong: %+v", s.Input["depth"])
	}
	// Degrades gracefully on empty/odd schema.
	if got := SchemaFromJSON(nil); len(got.Input) != 0 {
		t.Fatalf("nil schema should yield empty input, got %d", len(got.Input))
	}
}

// End-to-end: register a mock MCP server into a real Registry and execute a tool
// through the registry, proving the remote tool is a first-class Prizm tool.
func TestRegisterAndExecuteThroughRegistry(t *testing.T) {
	mc := &mockClient{
		tools: []ToolDef{
			{Name: "read_file", Description: "read a file", InputSchema: map[string]any{
				"properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []any{"path"}}},
			{Name: "write_file", Description: "write a file"},
		},
		result: CallResult{Content: "hello world"},
	}
	reg := tool.NewRegistry()
	names, err := RegisterServer(context.Background(), reg, "fs", mc)
	if err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}
	if len(names) != 2 || names[0] != "mcp_fs_read_file" {
		t.Fatalf("unexpected registered names: %v", names)
	}

	// The registered tool carries the namespaced name + MCP-tagged description.
	info := reg.ListWithDescriptions()
	found := false
	for _, i := range info {
		if i.Name == "mcp_fs_read_file" {
			found = true
			if i.Description == "" || i.Description[0] != '[' {
				t.Fatalf("expected MCP-tagged description, got %q", i.Description)
			}
		}
	}
	if !found {
		t.Fatal("registered MCP tool not visible in registry")
	}

	// Execute through the registry → proxies to the mock client.
	res, err := reg.Execute(context.Background(), "mcp_fs_read_file", map[string]any{"path": "x.go"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success || res.Output["content"] != "hello world" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if mc.lastName != "read_file" || mc.lastArgs["path"] != "x.go" {
		t.Fatalf("call not proxied with original tool name/args: %q %v", mc.lastName, mc.lastArgs)
	}
}

func TestExecuteMapsMCPErrorToFailedResult(t *testing.T) {
	mc := &mockClient{result: CallResult{Content: "boom", IsError: true}}
	mt := &mappedTool{client: mc, server: "x", def: ToolDef{Name: "t"}}
	res, err := mt.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("MCP isError should not be a Go error: %v", err)
	}
	if res.Success || res.Error != "boom" {
		t.Fatalf("expected failed result with error, got %+v", res)
	}

	// A transport error also becomes a failed result, not a Go error.
	mc2 := &mockClient{callErr: errors.New("conn refused")}
	mt2 := &mappedTool{client: mc2, server: "x", def: ToolDef{Name: "t"}}
	res2, _ := mt2.Execute(context.Background(), nil)
	if res2.Success || res2.Error == "" {
		t.Fatalf("transport error should be a failed result, got %+v", res2)
	}
}

func TestRegisterServerGuards(t *testing.T) {
	if _, err := RegisterServer(context.Background(), nil, "s", &mockClient{}); err == nil {
		t.Fatal("nil registry should error")
	}
	if _, err := RegisterServer(context.Background(), tool.NewRegistry(), "s", &mockClient{listErr: errors.New("x")}); err == nil {
		t.Fatal("list error should propagate")
	}
}
