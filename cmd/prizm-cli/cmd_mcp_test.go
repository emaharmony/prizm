package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/tool/mcp"
)

func TestMCPServerViews(t *testing.T) {
	cfg := &orchestrator.Config{
		MCPAutoApprove: true,
		MCPServers: []orchestrator.MCPServerConfig{
			{Name: "fs", Command: "npx", Args: []string{"-y", "server-filesystem", "/repo"}, Enabled: true},
			{Name: "off", Command: "other", Enabled: false},
		},
	}
	views := mcpServerViews(cfg)
	if len(views) != 2 {
		t.Fatalf("expected 2 views, got %d", len(views))
	}
	if views[0].Name != "fs" || !views[0].Enabled || !views[0].AutoApprove {
		t.Fatalf("fs view wrong: %+v", views[0])
	}
	if !strings.Contains(views[0].Command, "npx") || !strings.Contains(views[0].Command, "/repo") {
		t.Fatalf("command should include args: %q", views[0].Command)
	}
	if views[1].Enabled {
		t.Fatal("second server should be disabled")
	}
	if mcpServerViews(nil) != nil {
		t.Fatal("nil config should yield nil views")
	}
}

func TestRenderMCPServers(t *testing.T) {
	out := renderMCPServers([]mcpServerView{
		{Name: "fs", Command: "npx server-filesystem /repo", Enabled: true, AutoApprove: true},
		{Name: "off", Command: "other", Enabled: false},
	})
	for _, want := range []string{"MCP servers", "fs", "enabled", "off", "disabled", "mcp_<name>_<tool>", "AUTO-APPROVED", "2 server(s)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	// Empty config → helpful guidance, and approval posture defaults to required.
	if !strings.Contains(renderMCPServers(nil), "none configured") {
		t.Fatalf("empty render should guide the user")
	}
	reqd := renderMCPServers([]mcpServerView{{Name: "x", Enabled: true, AutoApprove: false}})
	if !strings.Contains(reqd, "approval-required") {
		t.Fatalf("non-auto-approve should show approval-required:\n%s", reqd)
	}
}

func TestRenderProbedTools(t *testing.T) {
	out := renderProbedTools("fs", []mcp.ToolDef{
		{Name: "read_file", Description: "read a file"},
		{Name: "write_file", Description: "write a file"},
	})
	for _, want := range []string{"probe fs", "2 tool(s)", "mcp_fs_read_file", "read a file", "mcp_fs_write_file"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(renderProbedTools("x", nil), "no tools") {
		t.Fatalf("empty probe should note no tools")
	}
}
