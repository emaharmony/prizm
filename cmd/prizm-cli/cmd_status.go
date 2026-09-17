// Package main implements the `prizm status` subcommand.
//
// Usage:
//
//	prizm status [--config prizm.yaml]
//
// Shows the current state of a running Prizm instance:
// agents, sessions, Discord connection, uptime.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/emaharmony/prizm/internal/orchestrator"
)

func executeStatus(args []string) {
	statusCmd := flag.NewFlagSet("status", flag.ExitOnError)
	configPath := statusCmd.String("config", "prizm.yaml", "Path to prizm.yaml configuration file")
	statusCmd.Parse(args)

	// Load configuration (just to show what's configured)
	cfg, err := orchestrator.LoadConfig(*configPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: config file %q not found\n", *configPath)
		} else {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		}
		os.Exit(1)
	}

	fmt.Println("🔮 Prizm Status")
	fmt.Println()

	// Agents
	fmt.Printf("Agents (%d):\n", len(cfg.Agents))
	for _, a := range cfg.Agents {
		primary := ""
		if a.Primary {
			primary = " [primary]"
		}
		fmt.Printf("  %-15s %-25s provider=%-10s model=%s%s\n",
			a.ID, a.Role, a.Provider, a.Model, primary)
	}

	// Channels
	fmt.Printf("\nChannels (%d):\n", len(cfg.Channels))
	for _, ch := range cfg.Channels {
		fmt.Printf("  %-12s token=%s... channels=%d\n",
			ch.Type, ch.Token[:min(8, len(ch.Token))], len(ch.Channels))
	}

	// Sessions
	fmt.Printf("\nSessions:\n")
	fmt.Printf("  max_context_messages: %d\n", cfg.Sessions.MaxContextMessages)
	fmt.Printf("  idle_timeout: %dm\n", cfg.Sessions.IdleTimeoutMinutes)
	fmt.Printf("  compaction: %s\n", cfg.Sessions.CompactionStrategy)
	fmt.Printf("  daily_reset_hour: %d\n", cfg.Sessions.DailyResetHour)

	// Actions
	if len(cfg.Actions) > 0 {
		fmt.Printf("\nActions (%d):\n", len(cfg.Actions))
		for _, a := range cfg.Actions {
			enabled := "enabled"
			if !a.Enabled {
				enabled = "disabled"
			}
			fmt.Printf("  %-30s → %-30s (%s)\n", a.Trigger, a.Action, enabled)
		}
	}

	// Note: Live status (uptime, Discord ready) requires a running instance
	// and is available via the health endpoint at http://localhost:8321/health
	fmt.Println()
	fmt.Printf("Live status: http://localhost:%d/health\n", cfg.Prizm.Port)
	fmt.Println("Note: Run 'prizm serve' to start the daemon.")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
