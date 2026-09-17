package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
)

func TestSummarizeConfig(t *testing.T) {
	cfg := &orchestrator.Config{
		Prizm: orchestrator.PrizmConfig{InstanceID: "prizm", WorkflowConfig: "wf.yaml"},
		Agents: []orchestrator.AgentConfig{
			{ID: "lead", Primary: true},
			{ID: "coder"},
		},
		Channels:    []orchestrator.ChannelConfig{{Type: "discord"}},
		Projects:    []orchestrator.ProjectConfig{{}},
		MCPServers:  []orchestrator.MCPServerConfig{{Name: "fs", Enabled: true}},
		Autopatch:   orchestrator.AutopatchConfig{Enabled: true, Mode: "pr"},
		Remembrance: orchestrator.RemembranceConfig{Enabled: true},
	}
	s := summarizeConfig(cfg)
	if s.Agents != 2 || s.PrimaryAgent != "lead" {
		t.Fatalf("agents/primary wrong: %+v", s)
	}
	if s.Channels != 1 || s.Projects != 1 || s.MCPServers != 1 {
		t.Fatalf("counts wrong: %+v", s)
	}
	if s.AutopatchMode != "pr" || !s.Remembrance || s.WorkflowFile != "wf.yaml" {
		t.Fatalf("fields wrong: %+v", s)
	}
}

func TestSummarizeConfigDefaults(t *testing.T) {
	// Autopatch disabled → empty mode; no primary; autopatch enabled w/o mode → "propose".
	s := summarizeConfig(&orchestrator.Config{
		Agents:    []orchestrator.AgentConfig{{ID: "solo"}},
		Autopatch: orchestrator.AutopatchConfig{Enabled: true}, // no mode
	})
	if s.PrimaryAgent != "" {
		t.Fatalf("no primary expected, got %q", s.PrimaryAgent)
	}
	if s.AutopatchMode != "propose" {
		t.Fatalf("enabled-no-mode should default to propose, got %q", s.AutopatchMode)
	}
}

func TestRenderConfigSummary(t *testing.T) {
	out := renderConfigSummary(configSummary{
		InstanceID: "prizm", Agents: 2, PrimaryAgent: "lead", Channels: 1,
		MCPServers: 1, AutopatchMode: "pr", Remembrance: true, WorkflowFile: "wf.yaml",
	})
	for _, want := range []string{"is valid", "prizm", "primary: lead", "mcp servers: 1", "autopatch:   pr", "remembrance: enabled", "wf.yaml"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	// Defaults: no primary, autopatch disabled, built-in workflow.
	out2 := renderConfigSummary(configSummary{Agents: 1})
	for _, want := range []string{"none marked primary", "autopatch:   disabled", "built-in default gated loop", "remembrance: disabled"} {
		if !strings.Contains(out2, want) {
			t.Fatalf("default render missing %q:\n%s", want, out2)
		}
	}
}
