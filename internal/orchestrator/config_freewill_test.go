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
