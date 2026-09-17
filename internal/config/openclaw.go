// Package config reads OpenClaw configuration and creates Prizm provider instances.
// It bridges between the OpenClaw config format and Prizm's provider interface.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/anthropic"
	"github.com/emaharmony/prizm/internal/provider/gemini"
	"github.com/emaharmony/prizm/internal/provider/ollama"
	"github.com/emaharmony/prizm/internal/provider/openai"
)

// OpenClawProviderConfig represents a single provider entry in openclaw.json.
type OpenClawProviderConfig struct {
	API     string                `json:"api"`
	APIKey  string                `json:"apiKey"`
	BaseURL string                `json:"baseUrl"`
	Models  []OpenClawModelConfig `json:"models"`
}

// OpenClawModelConfig represents a model entry within a provider.
type OpenClawModelConfig struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	ContextWindow int                `json:"contextWindow"`
	Input         []string           `json:"input"`
	Reasoning     bool               `json:"reasoning"`
	MaxTokens     int                `json:"maxTokens"`
	Cost          OpenClawCostConfig `json:"cost"`
}

// OpenClawCostConfig represents per-token pricing.
type OpenClawCostConfig struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// OpenClawModelsConfig represents the models section of openclaw.json.
type OpenClawModelsConfig struct {
	Mode      string                            `json:"mode"`
	Providers map[string]OpenClawProviderConfig `json:"providers"`
}

// OpenClawConfig represents the relevant parts of openclaw.json.
type OpenClawConfig struct {
	Models OpenClawModelsConfig `json:"models"`
}

// LoadFromOpenClaw reads an OpenClaw config file and builds a ProviderRegistry.
// It discovers providers, creates Provider instances, and registers all models.
func LoadFromOpenClaw(configPath string) (*provider.ProviderRegistry, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read openclaw config %s: %w", configPath, err)
	}

	var config OpenClawConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse openclaw config: %w", err)
	}

	registry := provider.NewProviderRegistry()

	for providerName, providerCfg := range config.Models.Providers {
		p, err := createProvider(providerName, providerCfg)
		if err != nil {
			// Skip providers we can't create (e.g., missing API keys for non-local)
			continue
		}

		for _, modelCfg := range providerCfg.Models {
			info := provider.ModelInfo{
				ID:            modelCfg.ID,
				Name:          modelCfg.Name,
				ContextWindow: modelCfg.ContextWindow,
				InputTypes:    modelCfg.Input,
				Reasoning:     modelCfg.Reasoning,
				ProviderName:  providerName,
				Cost: provider.ModelCost{
					InputPer1K:  modelCfg.Cost.Input,
					OutputPer1K: modelCfg.Cost.Output,
					CacheRead:   modelCfg.Cost.CacheRead,
					CacheWrite:  modelCfg.Cost.CacheWrite,
				},
			}
			registry.Register(modelCfg.ID, p, info)
		}
	}

	return registry, nil
}

// createProvider creates a Provider instance from an OpenClaw provider config.
func createProvider(providerName string, cfg OpenClawProviderConfig) (provider.Provider, error) {
	switch cfg.API {
	case "ollama":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return ollama.New(baseURL), nil

	case "openai":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("openai provider requires an API key")
		}
		if cfg.BaseURL != "" {
			return openai.NewWithBaseURL(cfg.APIKey, cfg.BaseURL), nil
		}
		return openai.New(cfg.APIKey), nil

	case "anthropic":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("anthropic provider requires an API key")
		}
		if cfg.BaseURL != "" {
			return anthropic.NewWithBaseURL(cfg.APIKey, cfg.BaseURL), nil
		}
		return anthropic.New(cfg.APIKey), nil

	case "gemini":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("gemini provider requires an API key")
		}
		if cfg.BaseURL != "" {
			return gemini.NewWithBaseURL(cfg.APIKey, cfg.BaseURL), nil
		}
		return gemini.New(cfg.APIKey), nil

	default:
		return nil, fmt.Errorf("unknown provider API: %s", cfg.API)
	}
}

// FindOpenClawConfig searches for the OpenClaw config file in this order:
// 1. explicit path (if provided)
// 2. OPENCLAW_CONFIG environment variable
// 3. ~/.openclaw/openclaw.json (default)
func FindOpenClawConfig(explicitPath string) string {
	if explicitPath != "" {
		return explicitPath
	}

	if envPath := os.Getenv("OPENCLAW_CONFIG"); envPath != "" {
		return envPath
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(homeDir, ".openclaw", "openclaw.json")
}
