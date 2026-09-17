// Package main implements the `prizm config` subcommand: validate prizm.yaml and
// print a summary of what Prizm understood from it (agents, channels, MCP servers,
// autopatch mode, workflow). Loading already validates structurally, so a load
// error is a clear, focused config-validation failure.
//
// Usage:
//
//	prizm config [--config prizm.yaml] [--json]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emaharmony/prizm/internal/orchestrator"
)

// configSummary is the validated, human/JSON view of a loaded config.
type configSummary struct {
	InstanceID   string `json:"instance_id"`
	Agents       int    `json:"agents"`
	PrimaryAgent string `json:"primary_agent,omitempty"`
	Channels     int    `json:"channels"`
	Projects     int    `json:"projects"`
	// IsolatedProjects counts projects with worktree_isolation enabled (V56).
	IsolatedProjects int    `json:"isolated_projects,omitempty"`
	MCPServers       int    `json:"mcp_servers"`
	AutopatchMode    string `json:"autopatch_mode,omitempty"`
	WorkflowFile     string `json:"workflow_file,omitempty"`
	Remembrance      bool   `json:"remembrance_enabled"`
}

// summarizeConfig builds the summary from a loaded config (pure, testable).
func summarizeConfig(cfg *orchestrator.Config) configSummary {
	s := configSummary{
		InstanceID:   cfg.Prizm.InstanceID,
		Agents:       len(cfg.Agents),
		Channels:     len(cfg.Channels),
		Projects:     len(cfg.Projects),
		MCPServers:   len(cfg.MCPServers),
		WorkflowFile: cfg.Prizm.WorkflowConfig,
		Remembrance:  cfg.Remembrance.Enabled,
	}
	for _, a := range cfg.Agents {
		if a.Primary {
			s.PrimaryAgent = a.ID
			break
		}
	}
	for _, p := range cfg.Projects {
		if p.WorktreeIsolation {
			s.IsolatedProjects++
		}
	}
	if cfg.Autopatch.Enabled {
		mode := cfg.Autopatch.Mode
		if mode == "" {
			mode = "propose"
		}
		s.AutopatchMode = mode
	}
	return s
}

// renderConfigSummary formats the summary (pure, testable).
func renderConfigSummary(s configSummary) string {
	var b strings.Builder
	b.WriteString("✅ prizm.yaml is valid\n")
	b.WriteString(strings.Repeat("─", 50) + "\n")
	fmt.Fprintf(&b, "  instance:    %s\n", orEmptyDash(s.InstanceID))
	primary := s.PrimaryAgent
	if primary == "" {
		primary = "(none marked primary)"
	}
	fmt.Fprintf(&b, "  agents:      %d (primary: %s)\n", s.Agents, primary)
	fmt.Fprintf(&b, "  channels:    %d\n", s.Channels)
	if s.IsolatedProjects > 0 {
		fmt.Fprintf(&b, "  projects:    %d (%d worktree-isolated)\n", s.Projects, s.IsolatedProjects)
	} else {
		fmt.Fprintf(&b, "  projects:    %d\n", s.Projects)
	}
	fmt.Fprintf(&b, "  mcp servers: %d\n", s.MCPServers)
	autopatch := s.AutopatchMode
	if autopatch == "" {
		autopatch = "disabled"
	}
	fmt.Fprintf(&b, "  autopatch:   %s\n", autopatch)
	fmt.Fprintf(&b, "  remembrance: %s\n", enabledWord(s.Remembrance))
	if s.WorkflowFile != "" {
		fmt.Fprintf(&b, "  workflow:    %s\n", s.WorkflowFile)
	} else {
		b.WriteString("  workflow:    built-in default gated loop\n")
	}
	return b.String()
}

func orEmptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func enabledWord(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

// executeConfig is the `prizm config` entry point. It dispatches the positional
// subcommands `import` and `wizard`; with no subcommand it validates and
// summarizes an existing prizm.yaml.
func executeConfig(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "import":
			runConfigImport(args[1:])
			return
		case "wizard", "init":
			runConfigWizard(args[1:])
			return
		}
	}

	fs := flag.NewFlagSet("config", flag.ExitOnError)
	configPath := fs.String("config", "prizm.yaml", "Path to prizm.yaml configuration file")
	asJSON := fs.Bool("json", false, "Emit the summary as JSON")
	fs.Parse(args)

	cfg, err := orchestrator.LoadConfig(*configPath)
	if err != nil {
		// LoadConfig validates structurally, so this is a config-validation failure.
		fmt.Fprintf(os.Stderr, "❌ %s is invalid: %v\n", *configPath, err)
		os.Exit(1)
	}
	summary := summarizeConfig(cfg)
	if *asJSON {
		data, mErr := json.MarshalIndent(summary, "", "  ")
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", mErr)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}
	fmt.Print(renderConfigSummary(summary))
}
