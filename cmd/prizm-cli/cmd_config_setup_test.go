package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
)

const sampleOpenClaw = `{
  "models": {
    "mode": "manual",
    "providers": {
      "openai": {
        "api": "openai",
        "apiKey": "sk-x",
        "models": [
          {"id": "gpt-4o", "name": "GPT-4o"},
          {"id": "gpt-4o-mini", "name": "GPT-4o mini"}
        ]
      },
      "anthropic": {
        "api": "anthropic",
        "models": [
          {"id": "claude-sonnet-4-6", "name": "Claude Sonnet"}
        ]
      },
      "empty": {
        "api": "ollama",
        "models": []
      }
    }
  }
}`

func TestOpenClawToExport(t *testing.T) {
	ec, err := openClawToExport([]byte(sampleOpenClaw))
	if err != nil {
		t.Fatalf("openClawToExport: %v", err)
	}
	// "empty" (no models) skipped → 2 agents.
	if len(ec.Agents) != 2 {
		t.Fatalf("want 2 agents, got %d", len(ec.Agents))
	}
	// Deterministic order: sorted by provider name → anthropic, openai.
	if ec.Agents[0].ID != "anthropic" || ec.Agents[1].ID != "openai" {
		t.Fatalf("unexpected agent order: %q, %q", ec.Agents[0].ID, ec.Agents[1].ID)
	}
	// First agent is primary; uses provider's first model.
	if !ec.Agents[0].Primary || ec.Agents[1].Primary {
		t.Fatalf("primary flag wrong: %v, %v", ec.Agents[0].Primary, ec.Agents[1].Primary)
	}
	if ec.Agents[1].Model != "gpt-4o" {
		t.Fatalf("want first model gpt-4o, got %q", ec.Agents[1].Model)
	}
	if ec.Agents[1].Provider != "openai" {
		t.Fatalf("want provider openai, got %q", ec.Agents[1].Provider)
	}
}

func TestOpenClawToExportNoProviders(t *testing.T) {
	if _, err := openClawToExport([]byte(`{"models":{"providers":{}}}`)); err == nil {
		t.Fatal("expected error for empty providers")
	}
	if _, err := openClawToExport([]byte(`not json`)); err == nil {
		t.Fatal("expected parse error")
	}
}

// TestGeneratedConfigValidates is the key guarantee: emitted YAML loads and
// validates as a real Prizm config (defaults fill in on load).
func TestGeneratedConfigValidates(t *testing.T) {
	ec, err := openClawToExport([]byte(sampleOpenClaw))
	if err != nil {
		t.Fatalf("openClawToExport: %v", err)
	}
	yamlData, err := marshalExport(ec)
	if err != nil {
		t.Fatalf("marshalExport: %v", err)
	}
	if !strings.HasPrefix(string(yamlData), "#") {
		t.Fatal("expected header comment")
	}
	cfg, err := orchestrator.LoadConfigFromBytes(yamlData)
	if err != nil {
		t.Fatalf("generated config failed to load: %v", err)
	}
	if len(cfg.Agents) != 2 {
		t.Fatalf("loaded config has %d agents, want 2", len(cfg.Agents))
	}
	if cfg.Prizm.InstanceID != "prizm" {
		t.Fatalf("instance id = %q, want prizm", cfg.Prizm.InstanceID)
	}
}

func TestWizardBuildSingleAgent(t *testing.T) {
	ec := wizardBuild(wizardAnswers{
		InstanceID:  "myprizm",
		Workspace:   "/work",
		Provider:    "ollama",
		Model:       "llama3.1",
		Remembrance: true,
	})
	if len(ec.Agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(ec.Agents))
	}
	a := ec.Agents[0]
	if a.ID != "ollama" || a.Provider != "ollama" || a.Model != "llama3.1" || !a.Primary {
		t.Fatalf("unexpected agent: %+v", a)
	}
	if !ec.Remembrance.Enabled {
		t.Fatal("remembrance should be enabled")
	}
	// Must produce a valid, loadable config.
	yamlData, err := marshalExport(ec)
	if err != nil {
		t.Fatalf("marshalExport: %v", err)
	}
	if _, err := orchestrator.LoadConfigFromBytes(yamlData); err != nil {
		t.Fatalf("wizard config failed to load: %v", err)
	}
}

func TestWizardBuildSeededAgentsGetPrimary(t *testing.T) {
	// Seeded agents with no primary flag → first is promoted to primary.
	ec := wizardBuild(wizardAnswers{
		InstanceID: "p",
		Agents: []exportAgent{
			{ID: "a", Provider: "openai", Model: "gpt-4o"},
			{ID: "b", Provider: "anthropic", Model: "claude-sonnet-4-6"},
		},
	})
	if !ec.Agents[0].Primary {
		t.Fatal("first seeded agent should be primary")
	}
}

func TestSanitizeAgentID(t *testing.T) {
	cases := map[string]string{
		"OpenAI":      "openai",
		"my provider": "my-provider",
		"  spaced  ":  "spaced",
		"":            "agent-",
		"123":         "agent-123",
		"a.b/c":       "a-b-c",
	}
	for in, want := range cases {
		if got := sanitizeAgentID(in); got != want {
			t.Errorf("sanitizeAgentID(%q) = %q, want %q", in, got, want)
		}
	}
}
