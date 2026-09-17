package orchestrator

import (
	"testing"
)

func TestShellConfigParsing(t *testing.T) {
	yaml := `
prizm:
  instance_id: test
  port: 8321
agents:
  - id: lumi
    role: lead
    provider: ollama
    model: test-model
shell:
  master_user_id: "123456789"
  allowlists:
    tier_1:
      - "go build*"
      - "go test*"
    tier_2:
      - "npm *"
      - "node *"
    tier_3:
      - "*"
  defaults:
    timeout_seconds: 60
    max_output_bytes: 20480
    blocked_patterns:
      - "shutdown*"
      - "reboot*"
channel_roles:
  - id: "111"
    role: manager-room
    mode: free
    shell_policy: tier_3
    tools: all
  - id: "222"
    role: build-room
    mode: gated
    shell_policy: tier_1
    tools: all
`

	cfg, err := LoadConfigFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Check Shell config
	if cfg.Shell.MasterUserID != "123456789" {
		t.Errorf("expected master_user_id '123456789', got %q", cfg.Shell.MasterUserID)
	}

	if len(cfg.Shell.Allowlists) != 3 {
		t.Errorf("expected 3 allowlist tiers, got %d", len(cfg.Shell.Allowlists))
	}

	if len(cfg.Shell.Allowlists["tier_1"]) != 2 {
		t.Errorf("expected 2 tier_1 patterns, got %d", len(cfg.Shell.Allowlists["tier_1"]))
	}

	if cfg.Shell.Defaults.TimeoutSeconds != 60 {
		t.Errorf("expected timeout_seconds 60, got %d", cfg.Shell.Defaults.TimeoutSeconds)
	}

	if cfg.Shell.Defaults.MaxOutputBytes != 20480 {
		t.Errorf("expected max_output_bytes 20480, got %d", cfg.Shell.Defaults.MaxOutputBytes)
	}

	if len(cfg.Shell.Defaults.BlockedPatterns) != 2 {
		t.Errorf("expected 2 blocked patterns, got %d", len(cfg.Shell.Defaults.BlockedPatterns))
	}

	// Check ChannelRole mode and shell_policy
	role1 := cfg.ResolveChannelRoleConfig("111")
	if role1 == nil {
		t.Fatal("expected channel role for id 111")
	}
	if role1.Mode != "free" {
		t.Errorf("expected mode 'free', got %q", role1.Mode)
	}
	if role1.ShellPolicy != "tier_3" {
		t.Errorf("expected shell_policy 'tier_3', got %q", role1.ShellPolicy)
	}

	role2 := cfg.ResolveChannelRoleConfig("222")
	if role2 == nil {
		t.Fatal("expected channel role for id 222")
	}
	if role2.Mode != "gated" {
		t.Errorf("expected mode 'gated', got %q", role2.Mode)
	}
	if role2.ShellPolicy != "tier_1" {
		t.Errorf("expected shell_policy 'tier_1', got %q", role2.ShellPolicy)
	}
}

func TestShellConfigDefaults(t *testing.T) {
	yaml := `
prizm:
  instance_id: test
  port: 8321
agents:
  - id: lumi
    role: lead
    provider: ollama
    model: test-model
`

	cfg, err := LoadConfigFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Default shell config should have sensible defaults
	if cfg.Shell.Defaults.TimeoutSeconds != 30 {
		t.Errorf("expected default timeout_seconds 30, got %d", cfg.Shell.Defaults.TimeoutSeconds)
	}

	if cfg.Shell.Defaults.MaxOutputBytes != 10240 {
		t.Errorf("expected default max_output_bytes 10240, got %d", cfg.Shell.Defaults.MaxOutputBytes)
	}

	if len(cfg.Shell.Defaults.BlockedPatterns) == 0 {
		t.Error("expected default blocked patterns to be non-empty")
	}
}

func TestChannelRoleDefaultMode(t *testing.T) {
	yaml := `
prizm:
  instance_id: test
  port: 8321
agents:
  - id: lumi
    role: lead
    provider: ollama
    model: test-model
channel_roles:
  - id: "333"
    role: fun
    tools: none
`

	cfg, err := LoadConfigFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	role := cfg.ResolveChannelRoleConfig("333")
	if role == nil {
		t.Fatal("expected channel role for id 333")
	}

	// Mode should default to empty (gated)
	if role.Mode != "" {
		t.Errorf("expected default mode '', got %q", role.Mode)
	}

	// ShellPolicy should default to empty
	if role.ShellPolicy != "" {
		t.Errorf("expected default shell_policy '', got %q", role.ShellPolicy)
	}
}
