// Package provider implements Prizm's LLM provider interface.
//
// V14c adds provider chaining with tier-based fallback. The ChainProvider
// tries providers in order, skipping paid providers unless --allow-paid-fallback
// is set. This gives Prizm resilience: if Ollama is down, fall back to OpenAI.
package provider

import (
	"context"
	"fmt"

	"github.com/emaharmony/prizm/internal/retry"
)

// ChainProvider tries multiple providers in order with tier-based fallback.
// If the first provider fails with a retryable error, it tries the next one.
// If the first provider succeeds, it returns immediately.
//
// Paid providers are skipped unless AllowPaid is set to true.
type ChainProvider struct {
	Providers []Provider
	AllowPaid bool
}

// NewChainProvider creates a provider chain with the given providers.
// Providers are tried in order. Set AllowPaid to true to include
// paid providers in the fallback chain.
func NewChainProvider(providers ...Provider) *ChainProvider {
	return &ChainProvider{
		Providers: providers,
		AllowPaid: false,
	}
}

// Name returns "chain" as the provider name.
func (c *ChainProvider) Name() string {
	return "chain"
}

// Tier returns the tier of the chain based on its providers.
func (c *ChainProvider) Tier() ProviderTier {
	if len(c.Providers) == 0 {
		return TierFree
	}
	for _, p := range c.Providers {
		if tiered, ok := p.(TieredProvider); ok && tiered.Tier() == TierPaid {
			return TierPaid
		}
	}
	return TierFree
}

// Generate tries each provider in order until one succeeds.
func (c *ChainProvider) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	var lastErr error
	attempted := 0

	for _, p := range c.Providers {
		if tiered, ok := p.(TieredProvider); ok {
			if tiered.Tier() == TierPaid && !c.AllowPaid {
				continue
			}
		}

		attempted++
		resp, err := p.Generate(ctx, req)
		if err == nil {
			return resp, nil
		}

		if !retry.IsRetryable(err) {
			providerName := "unknown"
			if named, ok := p.(NamedProvider); ok {
				providerName = named.Name()
			}
			return GenerateResponse{}, fmt.Errorf("provider %s: %w", providerName, err)
		}

		lastErr = err
	}

	if attempted == 0 {
		return GenerateResponse{}, fmt.Errorf("chain: no providers available (allow_paid=%v)", c.AllowPaid)
	}

	return GenerateResponse{}, fmt.Errorf("chain: all %d providers failed: %w", attempted, lastErr)
}

// StartLatencyMeasurer wraps a provider and measures the time from request
// to first response byte.
type StartLatencyMeasurer struct {
	Inner Provider
	name  string
}

// NewStartLatencyMeasurer creates a latency measurer with an explicit name.
func NewStartLatencyMeasurer(inner Provider, name string) *StartLatencyMeasurer {
	return &StartLatencyMeasurer{Inner: inner, name: name}
}

// Generate measures the latency of the inner provider.
func (m *StartLatencyMeasurer) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	return m.Inner.Generate(ctx, req)
}

// Name returns the provider name.
func (m *StartLatencyMeasurer) Name() string {
	return m.name
}
