// Package provider defines the LLM provider interface for Prizm.
//
// Provider implementations live in sub-packages:
//   - provider/mock     — deterministic mock for testing
//   - provider/ollama    — local Ollama instances (/api/generate)
//   - provider/openai    — OpenAI and OpenAI-compatible APIs
//   - provider/chain     — provider chaining with tier-based fallback
//
// Import the sub-package directly to use a provider:
//
//	import "github.com/emaharmony/prizm/internal/provider/mock"
//	p := mock.New()
package provider

import (
	"context"
	"errors"
)

// Provider is the interface for LLM generation backends.
type Provider interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
}

// ErrQuotaExhausted indicates a provider/model has hit an account-level
// usage quota (e.g. "weekly usage limit reached"), as opposed to a
// transient rate limit. Implementations should wrap their returned error
// with this (fmt.Errorf("...: %w", ErrQuotaExhausted)) when they detect
// this condition, so FailoverProvider knows not to retry the same target
// and to skip it for the rest of the process's lifetime — retrying
// immediately (or on the next message) can't succeed since the quota
// won't reset until the provider's own billing cycle does.
var ErrQuotaExhausted = errors.New("provider: quota exhausted")

// GenerateRequest is the input to a provider's Generate call.
type GenerateRequest struct {
	RunID         string  `json:"run_id"`
	CorrelationID string  `json:"correlation_id"`
	Agent         string  `json:"agent"`
	Project       string  `json:"project"`
	Task          string  `json:"task"`
	Prompt        string  `json:"prompt"`
	System        string  `json:"system,omitempty"` // V76: System prompt for cache_control support
	Model         string  `json:"model"`
	Temperature   float64 `json:"temperature"`
	MaxTokens     int     `json:"max_tokens"`
}

// GenerateResponse is the result from a provider's Generate call.
type GenerateResponse struct {
	Text         string         `json:"text"`
	Model        string         `json:"model"`
	Provider     string         `json:"provider"`
	LatencyMS    int64          `json:"latency_ms"`
	PromptTokens int            `json:"prompt_tokens"`
	OutputTokens int            `json:"output_tokens"`
	Raw          map[string]any `json:"raw,omitempty"`
}

// ProviderTier classifies providers by cost.
type ProviderTier string

const (
	TierFree ProviderTier = "free"
	TierPaid ProviderTier = "paid"
)

// TokenChunk represents a batch of tokens from a streaming LLM response.
type TokenChunk struct {
	Tokens   []string
	Index    int
	Finished bool
	Error    error
	Raw      map[string]any `json:"raw,omitempty"` // provider-specific metadata (e.g., stop_reason)
}

// StreamingProvider extends Provider with streaming generation capability.
type StreamingProvider interface {
	Provider
	GenerateStream(ctx context.Context, req GenerateRequest) (<-chan TokenChunk, error)
}

// NamedProvider is an optional interface for providers that have a name.
type NamedProvider interface {
	Provider
	Name() string
}

// TieredProvider is an optional interface for cost classification.
type TieredProvider interface {
	Provider
	Tier() ProviderTier
}
