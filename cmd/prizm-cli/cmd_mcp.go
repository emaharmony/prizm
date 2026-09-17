// Package main implements the `prizm mcp` subcommand: inspect the external MCP
// (Model Context Protocol) tool servers configured in prizm.yaml, so a user can
// verify their setup (and approval posture) before `prizm serve` connects them.
//
// Usage:
//
//	prizm mcp [--config prizm.yaml] [--json]
//
// It is read-only — it reports configuration, not live connections. Each enabled
// server's tools register as mcp_<name>_<tool> at serve startup and run through
// the policy engine (approval-required unless mcp_auto_approve is set).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/tool/mcp"
)

type mcpServerView struct {
	Name        string `json:"name"`
	Command     string `json:"command"`
	Args        string `json:"args,omitempty"`
	Enabled     bool   `json:"enabled"`
	AutoApprove bool   `json:"auto_approve"`
}

// mcpServerViews maps config to the display/JSON view (pure, testable).
func mcpServerViews(cfg *orchestrator.Config) []mcpServerView {
	if cfg == nil {
		return nil
	}
	out := make([]mcpServerView, 0, len(cfg.MCPServers))
	for _, s := range cfg.MCPServers {
		out = append(out, mcpServerView{
			Name:        s.Name,
			Command:     strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " ")),
			Args:        strings.Join(s.Args, " "),
			Enabled:     s.Enabled,
			AutoApprove: cfg.MCPAutoApprove,
		})
	}
	return out
}

// renderMCPServers formats the configured MCP servers (pure, testable).
func renderMCPServers(views []mcpServerView) string {
	var b strings.Builder
	b.WriteString("🔌 MCP servers\n")
	b.WriteString(strings.Repeat("─", 60) + "\n")
	if len(views) == 0 {
		b.WriteString("  (none configured — add an mcp_servers: block to prizm.yaml)\n")
		return b.String()
	}
	for _, v := range views {
		state := "enabled"
		glyph := "🟢"
		if !v.Enabled {
			state, glyph = "disabled", "⚪"
		}
		fmt.Fprintf(&b, "  %s %-12s %s\n", glyph, v.Name, state)
		fmt.Fprintf(&b, "       cmd: %s\n", v.Command)
	}
	b.WriteString(strings.Repeat("─", 60) + "\n")
	approval := "approval-required"
	if len(views) > 0 && views[0].AutoApprove {
		approval = "AUTO-APPROVED (mcp_auto_approve: true)"
	}
	fmt.Fprintf(&b, "%d server(s). Tools register as mcp_<name>_<tool> · execution: %s\n", len(views), approval)
	return b.String()
}

// executeMCP is the `prizm mcp` entry point. With a "probe <name>" subcommand it
// live-connects that server and lists its tools; otherwise it lists configuration.
func executeMCP(args []string) {
	if len(args) >= 1 && args[0] == "probe" {
		probeArgs := args[1:]
		var serverName string
		if len(probeArgs) >= 1 && !strings.HasPrefix(probeArgs[0], "-") {
			serverName = probeArgs[0]
			probeArgs = probeArgs[1:]
		}
		mcpProbe(serverName, probeArgs)
		return
	}

	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	configPath := fs.String("config", "prizm.yaml", "Path to prizm.yaml configuration file")
	asJSON := fs.Bool("json", false, "Emit the server list as JSON")
	fs.Parse(args)

	cfg, err := orchestrator.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ load config: %v\n", err)
		os.Exit(1)
	}
	views := mcpServerViews(cfg)
	if *asJSON {
		data, mErr := json.MarshalIndent(views, "", "  ")
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", mErr)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}
	fmt.Print(renderMCPServers(views))
}

// renderProbedTools formats the tools discovered from a live MCP server (pure).
func renderProbedTools(server string, tools []mcp.ToolDef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🔌 probe %s — %d tool(s)\n", server, len(tools))
	b.WriteString(strings.Repeat("─", 60) + "\n")
	if len(tools) == 0 {
		b.WriteString("  (server exposed no tools)\n")
		return b.String()
	}
	for _, t := range tools {
		fmt.Fprintf(&b, "  • %-24s %s\n", mcp.ToolName(server, t.Name), t.Description)
	}
	return b.String()
}

// mcpProbe live-connects a configured MCP server and lists its tools.
func mcpProbe(serverName string, args []string) {
	fs := flag.NewFlagSet("mcp probe", flag.ExitOnError)
	configPath := fs.String("config", "prizm.yaml", "Path to prizm.yaml configuration file")
	timeout := fs.Int("timeout", 30, "Connect/handshake timeout in seconds")
	fs.Parse(args)

	if serverName == "" {
		fmt.Fprintln(os.Stderr, "❌ usage: prizm mcp probe <server-name>")
		os.Exit(1)
	}
	cfg, err := orchestrator.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ load config: %v\n", err)
		os.Exit(1)
	}
	var spec *mcp.ServerSpec
	for _, s := range cfg.MCPServers {
		if s.Name == serverName {
			spec = &mcp.ServerSpec{Name: s.Name, Command: s.Command, Args: s.Args, Env: s.Env, Enabled: s.Enabled}
			break
		}
	}
	if spec == nil {
		fmt.Fprintf(os.Stderr, "❌ no MCP server named %q in %s\n", serverName, *configPath)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()
	tools, perr := mcp.ProbeServer(ctx, *spec, mcp.ProcessClientFactory)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "❌ probe %s: %v\n", serverName, perr)
		os.Exit(1)
	}
	fmt.Print(renderProbedTools(serverName, tools))
}
