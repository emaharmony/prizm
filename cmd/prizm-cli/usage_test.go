package main

import (
	"strings"
	"testing"
)

func TestCommandUsageHasGroups(t *testing.T) {
	out := commandUsage()
	for _, group := range []string{
		"Run & interact", "Inspect & validate", "Observe runs",
		"Self-patching", "MCP tool servers", "Approvals & mutations", "Advanced",
	} {
		if !strings.Contains(out, group+":") {
			t.Fatalf("usage missing group %q:\n%s", group, out)
		}
	}
}

// The grouped usage must not hardcode a specific persona (de-hardcoding: command
// help should be roster-neutral so it reads correctly for any configured agents).
func TestCommandUsageNoHardcodedPersona(t *testing.T) {
	out := strings.ToLower(commandUsage())
	for _, persona := range []string{"lumi", "mango", "junie"} {
		if strings.Contains(out, persona) {
			t.Fatalf("usage hardcodes persona %q — keep command help roster-neutral:\n%s", persona, commandUsage())
		}
	}
}

// Every user-facing subcommand should be discoverable in the grouped usage.
func TestCommandUsageCoversCommands(t *testing.T) {
	out := commandUsage()
	for _, cmd := range []string{
		"prizm run", "prizm chat", "prizm serve",
		"prizm config", "prizm doctor", "prizm preview", "prizm agent", "prizm tool", "prizm validation", "prizm context",
		"prizm watch", "prizm panel", "prizm runs", "prizm cost", "prizm trace", "prizm dashboard",
		"prizm scan", "prizm mcp", "prizm skills", "prizm approval",
		"prizm health", "prizm search", "prizm projection", "prizm adapter", "prizm remembrance", "prizm version",
	} {
		if !strings.Contains(out, cmd) {
			t.Fatalf("usage does not mention %q:\n%s", cmd, out)
		}
	}
}
