package orchestrator

import (
	"os"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	loop := []string{"", "127.0.0.1", "localhost", "::1", "[::1]", "127.0.0.5"}
	for _, h := range loop {
		if !IsLoopbackHost(h) {
			t.Errorf("IsLoopbackHost(%q) = false, want true", h)
		}
	}
	notLoop := []string{"0.0.0.0", "192.168.1.10", "example.com", "10.0.0.1"}
	for _, h := range notLoop {
		if IsLoopbackHost(h) {
			t.Errorf("IsLoopbackHost(%q) = true, want false", h)
		}
	}
}

func TestBindAddrDefaultsToLoopback(t *testing.T) {
	c := &Config{}
	if got := c.BindAddr(8322); got != "127.0.0.1:8322" {
		t.Errorf("BindAddr default = %q, want 127.0.0.1:8322", got)
	}
	c.Prizm.BindHost = "0.0.0.0"
	if got := c.BindAddr(8322); got != "0.0.0.0:8322" {
		t.Errorf("BindAddr = %q, want 0.0.0.0:8322", got)
	}
}

func TestValidate_NonLoopbackRequiresToken(t *testing.T) {
	c := DefaultConfig()
	c.Prizm.BindHost = "0.0.0.0"
	c.API.AuthToken = ""
	if err := c.Validate(); err == nil {
		t.Fatal("expected error: non-loopback bind without auth token")
	}

	// With a token it validates.
	c.API.AuthToken = "tok"
	if err := c.Validate(); err != nil {
		t.Fatalf("expected valid config with token, got: %v", err)
	}
}

func TestValidate_LoopbackNeedsNoToken(t *testing.T) {
	c := DefaultConfig() // BindHost defaults to 127.0.0.1
	if err := c.Validate(); err != nil {
		t.Fatalf("loopback bind without token should be valid, got: %v", err)
	}
}

func TestResolveAuthToken_EnvWins(t *testing.T) {
	const envName = "PRIZM_TEST_API_TOKEN"
	t.Setenv(envName, "from-env")
	a := APIServerConfig{AuthToken: "inline", AuthTokenEnv: envName}
	if got := a.ResolveAuthToken(); got != "from-env" {
		t.Errorf("ResolveAuthToken = %q, want from-env", got)
	}

	// Empty env falls back to inline.
	os.Unsetenv(envName)
	if got := a.ResolveAuthToken(); got != "inline" {
		t.Errorf("ResolveAuthToken fallback = %q, want inline", got)
	}
}
