package orchestrator

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFreewillFeatureSwitchesDefaultOffAndRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Freewill.Enabled || cfg.Freewill.Scheduling.Enabled {
		t.Fatal("Freewill feature and scheduler must default off")
	}

	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	decoded, err := LoadConfigFromBytes(encoded)
	if err != nil {
		t.Fatalf("reload default config: %v", err)
	}
	if decoded.Freewill.Enabled || decoded.Freewill.Scheduling.Enabled {
		t.Fatal("Freewill flags changed during default config round trip")
	}
	if decoded.Shell.MasterUserID != cfg.Shell.MasterUserID || len(decoded.Shell.Allowlists) != len(cfg.Shell.Allowlists) {
		t.Fatal("adding Freewill schema changed existing Freedom/Free Mode configuration")
	}
}

func TestFreewillConfigFlagsAreIndependentAndDoNotActivateRuntime(t *testing.T) {
	featureOnly, err := LoadConfigFromBytes([]byte("freewill:\n  enabled: true\n"))
	if err != nil {
		t.Fatalf("load feature-only config: %v", err)
	}
	if !featureOnly.Freewill.Enabled || featureOnly.Freewill.Scheduling.Enabled {
		t.Fatalf("feature and scheduling flags must remain independent: %#v", featureOnly.Freewill)
	}

	schedulingOnly, err := LoadConfigFromBytes([]byte("freewill:\n  scheduling:\n    enabled: true\n"))
	if err != nil {
		t.Fatalf("load scheduling-only config: %v", err)
	}
	if schedulingOnly.Freewill.Enabled || !schedulingOnly.Freewill.Scheduling.Enabled {
		t.Fatalf("scheduling flag must not enable feature flag: %#v", schedulingOnly.Freewill)
	}
}

func TestEnabledFreewillRequiresTrustedAgentProjectAndSchedule(t *testing.T) {
	agents := []AgentConfig{{ID: "selector", Role: "planner", Provider: "ollama", Model: "glm-5.3:cloud"}}
	projects := []ProjectConfig{{ID: "project-a", RepoPath: `D:\projects\project-a`}}
	config := FreewillConfig{Enabled: true, AgentID: "selector"}
	if err := config.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("enabled Freewill accepted a missing host project binding")
	}
	config = FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project-a", Scheduling: FreewillSchedulingConfig{Enabled: true}}
	if err := config.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("enabled Freewill scheduling accepted a missing cron expression")
	}
	config.Scheduling.Schedule = "*/15 * * * *"
	if err := config.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("valid host-bound Freewill config rejected: %v", err)
	}
	if err := config.ValidateHostBinding(agents, nil); err == nil {
		t.Fatal("enabled Freewill accepted an unregistered project")
	}
}
