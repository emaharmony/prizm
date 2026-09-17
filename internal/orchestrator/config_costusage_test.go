package orchestrator

import "testing"

// Validate defaults the new config-driven knobs so an empty config is usable.
func TestValidateDefaultsCostUsageKnobs(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}
	if cfg.Prizm.RunsDir != "runs" {
		t.Errorf("runs_dir default = %q, want runs", cfg.Prizm.RunsDir)
	}
	if cfg.API.MaxRequestBytes != 1<<20 {
		t.Errorf("max_request_bytes default = %d, want %d", cfg.API.MaxRequestBytes, 1<<20)
	}
	if cfg.API.MaxWorkspaceFileBytes != 4<<20 {
		t.Errorf("max_workspace_file_bytes default = %d, want %d", cfg.API.MaxWorkspaceFileBytes, 4<<20)
	}
}

// A blank runs_dir / zero body cap is backfilled rather than rejected.
func TestValidateBackfillsEmptyKnobs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Prizm.RunsDir = ""
	cfg.API.MaxRequestBytes = 0
	cfg.API.MaxWorkspaceFileBytes = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("empty knobs should backfill: %v", err)
	}
	if cfg.Prizm.RunsDir != "runs" || cfg.API.MaxRequestBytes != 1<<20 || cfg.API.MaxWorkspaceFileBytes != 4<<20 {
		t.Errorf("knobs not backfilled: %+v / %d / %d", cfg.Prizm.RunsDir, cfg.API.MaxRequestBytes, cfg.API.MaxWorkspaceFileBytes)
	}
}

func TestValidateRejectsBadKnobs(t *testing.T) {
	cases := map[string]func(*Config){
		"negative max_request_bytes": func(c *Config) { c.API.MaxRequestBytes = -1 },
		"negative workspace cap":     func(c *Config) { c.API.MaxWorkspaceFileBytes = -1 },
		"negative pricing":           func(c *Config) { c.Cost.Pricing = map[string]ModelPricing{"m": {Input: -1}} },
		"unknown usage range":        func(c *Config) { c.Usage.Windows = []UsageWindowConfig{{Range: "decade"}} },
		"bad usage bucket duration":  func(c *Config) { c.Usage.Windows = []UsageWindowConfig{{Range: "day", Bucket: "banana"}} },
		"non-positive usage bucket":  func(c *Config) { c.Usage.Windows = []UsageWindowConfig{{Range: "day", Bucket: "0s"}} },
		"bad usage window duration":  func(c *Config) { c.Usage.Windows = []UsageWindowConfig{{Range: "week", Window: "lots"}} },
	}
	for name, mutate := range cases {
		cfg := DefaultConfig()
		mutate(cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}

// CostPricingOverrides maps config pricing into the cost package's type, and is
// nil when nothing is configured.
func TestCostPricingOverrides(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.CostPricingOverrides() != nil {
		t.Error("expected nil overrides for empty pricing")
	}
	cfg.Cost.Pricing = map[string]ModelPricing{"gpt-4o": {Input: 0.005, Output: 0.02}}
	got := cfg.CostPricingOverrides()
	if got["gpt-4o"].Input != 0.005 || got["gpt-4o"].Output != 0.02 {
		t.Errorf("override mapping = %+v", got["gpt-4o"])
	}
}

// A valid usage window override passes validation.
func TestValidateAcceptsUsageWindows(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Usage.Windows = []UsageWindowConfig{
		{Range: "week", Window: "336h", Bucket: "12h"},
		{Range: "session", Bucket: "1m"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid usage windows rejected: %v", err)
	}
}
