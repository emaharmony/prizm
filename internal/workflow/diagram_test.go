package workflow

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
)

func TestAgentTopology(t *testing.T) {
	agents := []orchestrator.AgentConfig{
		{ID: "lumi", Role: "lead", Provider: "ollama", Model: "glm-5.1:cloud", Primary: true, Capabilities: []string{"plan", "delegate", "review"}},
		{ID: "mango", Role: "coder", Provider: "ollama", Model: "deepseek-v4-pro:cloud", Capabilities: []string{"code", "test"}},
	}

	var buf bytes.Buffer
	cfg := DefaultConfig()
	AgentTopology(&buf, agents, cfg)

	output := buf.String()
	if !strings.Contains(output, "<svg") {
		t.Error("expected SVG output")
	}
	if !strings.Contains(output, "LUMI") {
		t.Error("expected LUMI agent in output")
	}
	if !strings.Contains(output, "MANGO") {
		t.Error("expected MANGO agent in output")
	}
	if !strings.Contains(output, "lead") {
		t.Error("expected lead role in output")
	}
	if !strings.Contains(output, "coder") {
		t.Error("expected coder role in output")
	}
	if !strings.Contains(output, "★") {
		t.Error("expected primary marker in output")
	}
}

func TestAgentTopology_DarkTheme(t *testing.T) {
	agents := []orchestrator.AgentConfig{
		{ID: "lumi", Role: "lead", Provider: "ollama", Model: "glm-5.1:cloud"},
	}

	var buf bytes.Buffer
	cfg := DefaultConfig()
	cfg.DarkTheme = true
	AgentTopology(&buf, agents, cfg)

	output := buf.String()
	if !strings.Contains(output, "#0a0a0f") {
		t.Error("expected dark background color")
	}
}

func TestAgentTopology_LightTheme(t *testing.T) {
	agents := []orchestrator.AgentConfig{
		{ID: "lumi", Role: "lead", Provider: "ollama", Model: "glm-5.1:cloud"},
	}

	var buf bytes.Buffer
	cfg := DefaultConfig()
	cfg.DarkTheme = false
	AgentTopology(&buf, agents, cfg)

	output := buf.String()
	if !strings.Contains(output, "#ffffff") {
		t.Error("expected light background color")
	}
}

func TestAgentTopology_ManyAgents(t *testing.T) {
	agents := []orchestrator.AgentConfig{
		{ID: "lumi", Role: "lead", Provider: "ollama", Model: "glm-5.1:cloud", Primary: true},
		{ID: "mango", Role: "coder", Provider: "ollama", Model: "deepseek-v4-pro:cloud"},
		{ID: "junie", Role: "developer", Provider: "ollama", Model: "qwen3:4b"},
		{ID: "navii", Role: "researcher", Provider: "ollama", Model: "qwen3-vl:235b-cloud"},
		{ID: "kirbii", Role: "orchestrator", Provider: "ollama", Model: "glm-5.1:cloud"},
	}

	var buf bytes.Buffer
	cfg := DefaultConfig()
	cfg.Width = 1200
	cfg.Height = 800
	AgentTopology(&buf, agents, cfg)

	output := buf.String()
	if !strings.Contains(output, "KIRBII") {
		t.Error("expected KIRBII agent in output")
	}
}

func TestFeedbackLoops(t *testing.T) {
	var buf bytes.Buffer
	cfg := DefaultConfig()
	FeedbackLoops(&buf, cfg)

	output := buf.String()
	if !strings.Contains(output, "<svg") {
		t.Error("expected SVG output")
	}
	if !strings.Contains(output, "LUMI") {
		t.Error("expected LUMI in feedback loops")
	}
	if !strings.Contains(output, "MANGO") {
		t.Error("expected MANGO in feedback loops")
	}
	if !strings.Contains(output, "Pre-Dev Architecture Check") {
		t.Error("expected Loop 1 label")
	}
	if !strings.Contains(output, "Mid-Dev Correctness Check") {
		t.Error("expected Loop 2 label")
	}
	if !strings.Contains(output, "Post-Dev Vulnerability Analysis") {
		t.Error("expected Loop 3 label")
	}
	if !strings.Contains(output, "Legend") {
		t.Error("expected legend in output")
	}
}

func TestDelegationFlow(t *testing.T) {
	agents := []orchestrator.AgentConfig{
		{ID: "lumi", Role: "lead", Provider: "ollama", Model: "glm-5.1:cloud", Primary: true},
		{ID: "mango", Role: "coder", Provider: "ollama", Model: "deepseek-v4-pro:cloud"},
	}

	var buf bytes.Buffer
	cfg := DefaultConfig()
	DelegationFlow(&buf, agents, cfg)

	output := buf.String()
	if !strings.Contains(output, "<svg") {
		t.Error("expected SVG output")
	}
	if !strings.Contains(output, "Delegation Flow") {
		t.Error("expected title")
	}
	if !strings.Contains(output, "LLMStage") {
		t.Error("expected LLMStage in pipeline")
	}
	if !strings.Contains(output, "created") {
		t.Error("expected task lifecycle states")
	}
	if !strings.Contains(output, "Approve?") {
		t.Error("expected approval gate diamond")
	}
}

func TestApprovalGate(t *testing.T) {
	var buf bytes.Buffer
	cfg := DefaultConfig()
	ApprovalGate(&buf, cfg)

	output := buf.String()
	if !strings.Contains(output, "<svg") {
		t.Error("expected SVG output")
	}
	if !strings.Contains(output, "Approval Gate Flow") {
		t.Error("expected title")
	}
	if !strings.Contains(output, "RequestApproval()") {
		t.Error("expected RequestApproval in output")
	}
	if !strings.Contains(output, "GrantApproval()") {
		t.Error("expected GrantApproval in output")
	}
	if !strings.Contains(output, "DenyApproval()") {
		t.Error("expected DenyApproval in output")
	}
	if !strings.Contains(output, "approval.requested") {
		t.Error("expected event names in output")
	}
}

func TestEventFlow(t *testing.T) {
	agents := []orchestrator.AgentConfig{
		{ID: "lumi", Role: "lead", Provider: "ollama", Model: "glm-5.1:cloud"},
		{ID: "mango", Role: "coder", Provider: "ollama", Model: "deepseek-v4-pro:cloud"},
	}

	var buf bytes.Buffer
	cfg := DefaultConfig()
	EventFlow(&buf, agents, cfg)

	output := buf.String()
	if !strings.Contains(output, "<svg") {
		t.Error("expected SVG output")
	}
	if !strings.Contains(output, "Event Flow Topology") {
		t.Error("expected title")
	}
	if !strings.Contains(output, "NATS Event Bus") {
		t.Error("expected NATS bus in output")
	}
	if !strings.Contains(output, "lumi.*") {
		t.Error("expected lumi namespace")
	}
	if !strings.Contains(output, "prizm.task.created") {
		t.Error("expected system events")
	}
}

func TestGenerateWorkflow(t *testing.T) {
	agents := []orchestrator.AgentConfig{
		{ID: "lumi", Role: "lead", Provider: "ollama", Model: "glm-5.1:cloud"},
	}

	types := []string{"topology", "agents", "feedback", "feedback-loops", "delegation", "approval", "events", "unknown"}
	for _, dt := range types {
		var buf bytes.Buffer
		cfg := DefaultConfig()
		GenerateWorkflow(&buf, dt, agents, cfg)

		output := buf.String()
		if !strings.Contains(output, "<svg") {
			t.Errorf("expected SVG output for type %q", dt)
		}
	}
}

func TestGenerateWorkflowWithCapabilities(t *testing.T) {
	agents := []orchestrator.AgentConfig{
		{ID: "lumi", Role: "lead", Provider: "ollama", Model: "glm-5.1:cloud", Capabilities: []string{"plan", "delegate", "review"}},
		{ID: "mango", Role: "coder", Provider: "ollama", Model: "deepseek-v4-pro:cloud", Capabilities: []string{"code", "test"}},
	}

	var buf bytes.Buffer
	cfg := DefaultConfig()
	GenerateWorkflowWithCapabilities(&buf, agents, nil, cfg)

	output := buf.String()
	if !strings.Contains(output, "<svg") {
		t.Error("expected SVG output")
	}
	if !strings.Contains(output, "LUMI") {
		t.Error("expected LUMI in output")
	}
}
