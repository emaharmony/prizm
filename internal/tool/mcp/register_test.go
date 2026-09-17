package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/emaharmony/prizm/internal/tool"
)

type fakeInitClient struct {
	mockClient
	initErr  error
	initDone bool
}

func (f *fakeInitClient) Initialize(_ context.Context) error {
	f.initDone = true
	return f.initErr
}

func TestRegisterServersHappyPath(t *testing.T) {
	reg := tool.NewRegistry()
	factory := func(_ context.Context, spec ServerSpec) (InitClient, error) {
		return &fakeInitClient{mockClient: mockClient{tools: []ToolDef{
			{Name: "read_file", Description: "r"},
			{Name: "write_file", Description: "w"},
		}}}, nil
	}
	specs := []ServerSpec{
		{Name: "fs", Command: "npx", Enabled: true},
		{Name: "disabled", Command: "npx", Enabled: false}, // skipped
	}
	results := RegisterServers(context.Background(), reg, specs, factory)
	if len(results) != 1 {
		t.Fatalf("expected 1 result (disabled skipped), got %d", len(results))
	}
	if results[0].Err != nil || len(results[0].Tools) != 2 {
		t.Fatalf("expected 2 tools registered, got %+v", results[0])
	}
	// Tools are actually in the registry under the namespaced names.
	names := reg.List()
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["mcp_fs_read_file"] || !found["mcp_fs_write_file"] {
		t.Fatalf("expected namespaced mcp tools registered, got %v", names)
	}
}

func TestRegisterServersHandlesFailures(t *testing.T) {
	reg := tool.NewRegistry()
	factory := func(_ context.Context, spec ServerSpec) (InitClient, error) {
		switch spec.Name {
		case "connfail":
			return nil, errors.New("spawn failed")
		case "initfail":
			return &fakeInitClient{initErr: errors.New("handshake failed")}, nil
		default:
			return &fakeInitClient{mockClient: mockClient{tools: []ToolDef{{Name: "ok"}}}}, nil
		}
	}
	specs := []ServerSpec{
		{Name: "connfail", Command: "x", Enabled: true},
		{Name: "initfail", Command: "x", Enabled: true},
		{Name: "good", Command: "x", Enabled: true},
		{Name: "", Command: "x", Enabled: true}, // invalid (no name)
	}
	results := RegisterServers(context.Background(), reg, specs, factory)
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	byServer := map[string]RegisterResult{}
	for _, r := range results {
		byServer[r.Server] = r
	}
	if byServer["connfail"].Err == nil || byServer["initfail"].Err == nil {
		t.Fatalf("connect/init failures should be reported: %+v", results)
	}
	if byServer["good"].Err != nil || len(byServer["good"].Tools) != 1 {
		t.Fatalf("good server should register despite siblings failing: %+v", byServer["good"])
	}
	if byServer[""].Err == nil {
		t.Fatalf("server without a name should error")
	}
}

func TestProbeServer(t *testing.T) {
	factory := func(_ context.Context, spec ServerSpec) (InitClient, error) {
		return &fakeInitClient{mockClient: mockClient{tools: []ToolDef{
			{Name: "read_file", Description: "r"}, {Name: "write_file", Description: "w"},
		}}}, nil
	}
	tools, err := ProbeServer(context.Background(), ServerSpec{Name: "fs", Command: "npx"}, factory)
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "read_file" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
}

func TestProbeServerErrors(t *testing.T) {
	// invalid spec
	if _, err := ProbeServer(context.Background(), ServerSpec{Name: "", Command: "x"}, nil); err == nil {
		t.Fatal("expected error for nameless server")
	}
	// connect failure
	failFactory := func(_ context.Context, _ ServerSpec) (InitClient, error) { return nil, errors.New("spawn fail") }
	if _, err := ProbeServer(context.Background(), ServerSpec{Name: "x", Command: "y"}, failFactory); err == nil {
		t.Fatal("expected connect error")
	}
	// init failure
	initFail := func(_ context.Context, _ ServerSpec) (InitClient, error) {
		return &fakeInitClient{initErr: errors.New("handshake fail")}, nil
	}
	if _, err := ProbeServer(context.Background(), ServerSpec{Name: "x", Command: "y"}, initFail); err == nil {
		t.Fatal("expected init error")
	}
}
