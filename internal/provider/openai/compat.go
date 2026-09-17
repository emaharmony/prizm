// Package openai compatibility wrappers — thin constructors for OpenAI-compatible APIs.
//
// These providers all speak the OpenAI /v1/chat/completions format. They're
// just NewWithBaseURL with the right defaults. No separate implementation needed.
package openai

import (
	"net/http"
	"time"

	"github.com/emaharmony/prizm/internal/provider"
)

// ---------- Together AI ----------

const togetherBaseURL = "https://api.together.xyz/v1"

// NewTogetherProvider creates a provider for Together AI's OpenAI-compatible API.
func NewTogetherProvider(apiKey string) *Provider {
	return &Provider{
		APIKey:     apiKey,
		BaseURL:    togetherBaseURL,
		HTTPClient: &http.Client{Timeout: 120 * time.Second, Transport: DefaultTransport},
		TierVal:    TierPaid,
	}
}

// ---------- Groq ----------

const groqBaseURL = "https://api.groq.com/openai/v1"

// NewGroqProvider creates a provider for Groq's OpenAI-compatible API.
func NewGroqProvider(apiKey string) *Provider {
	return &Provider{
		APIKey:     apiKey,
		BaseURL:    groqBaseURL,
		HTTPClient: &http.Client{Timeout: 120 * time.Second, Transport: DefaultTransport},
		TierVal:    TierPaid,
	}
}

// ---------- Azure OpenAI ----------

// NewAzureProvider creates a provider for Azure OpenAI.
// The baseURL should be in the format: https://{resource}.openai.azure.com/openai/deployments/{deployment}/
func NewAzureProvider(apiKey, baseURL string) *Provider {
	return &Provider{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 120 * time.Second, Transport: DefaultTransport},
		TierVal:    TierPaid,
	}
}

// ---------- Ollama OpenAI-compatible mode ----------

const ollamaOpenAIBaseURL = "http://localhost:11434/v1"

// NewOllamaCompatProvider creates a provider for Ollama's OpenAI-compatible mode.
// This uses /v1/chat/completions instead of /api/generate.
// For the native Ollama provider, use the ollama package instead.
func NewOllamaCompatProvider() *Provider {
	return &Provider{
		APIKey:     "ollama", // Ollama doesn't require an API key
		BaseURL:    ollamaOpenAIBaseURL,
		HTTPClient: &http.Client{Timeout: 20 * time.Minute, Transport: DefaultTransport}, // Local inference can be slow
		TierVal:    provider.TierFree,
	}
}
