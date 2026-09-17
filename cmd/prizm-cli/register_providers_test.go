package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
)

// The provider registry is keyed by model ID, so two agents sharing a model
// with DIFFERENT providers would silently overwrite each other (last wins).
// registerProviders must fail closed at startup instead.
func TestRegisterProviders_ConflictingProviderSameModel(t *testing.T) {
	cfg := &orchestrator.Config{
		Agents: []orchestrator.AgentConfig{
			{ID: "a", Provider: "ollama", Model: "llama3.2"},
			{ID: "b", Provider: "claude_code", Model: "llama3.2"},
		},
	}
	reg := provider.NewProviderRegistry()
	err := registerProviders(cfg, reg)
	if err == nil {
		t.Fatal("expected error for two agents sharing a model with different providers, got nil")
	}
	if !strings.Contains(err.Error(), "already registered with provider") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// Two agents on the same provider+model must be allowed (they share one
// registration — the common local-Ollama setup in prizm.yaml.example).
func TestRegisterProviders_SameProviderSameModelAllowed(t *testing.T) {
	cfg := &orchestrator.Config{
		Agents: []orchestrator.AgentConfig{
			{ID: "astraea", Provider: "ollama", Model: "llama3.2"},
			{ID: "forge", Provider: "ollama", Model: "llama3.2"},
		},
	}
	reg := provider.NewProviderRegistry()
	if err := registerProviders(cfg, reg); err != nil {
		t.Fatalf("same provider+model should be allowed, got: %v", err)
	}
	if _, err := reg.Get("llama3.2"); err != nil {
		t.Fatalf("model should be registered: %v", err)
	}
}
