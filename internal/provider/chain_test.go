package provider_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/mock"
	"github.com/emaharmony/prizm/internal/provider/ollama"
	"github.com/emaharmony/prizm/internal/provider/openai"
)

// mockProvider implements provider.Provider for chain tests.
type mockProvider struct {
	response provider.GenerateResponse
	err      error
	tier     provider.ProviderTier
}

func (m *mockProvider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	if m.err != nil {
		return provider.GenerateResponse{}, m.err
	}
	return m.response, nil
}

func (m *mockProvider) Name() string                { return "mock" }
func (m *mockProvider) Tier() provider.ProviderTier { return m.tier }

func TestChainProvider_SingleProvider_Success(t *testing.T) {
	success := &mockProvider{
		response: provider.GenerateResponse{Text: "hello", Provider: "mock"},
	}
	chain := provider.NewChainProvider(success)
	resp, err := chain.Generate(context.Background(), provider.GenerateRequest{Prompt: "test"})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.Text != "hello" {
		t.Errorf("Text = %q, want hello", resp.Text)
	}
}

func TestChainProvider_FallbackToNext(t *testing.T) {
	failing := &mockProvider{
		err: fmt.Errorf("503 service unavailable"),
	}
	success := &mockProvider{
		response: provider.GenerateResponse{Text: "fallback response", Provider: "backup"},
	}
	chain := provider.NewChainProvider(failing, success)
	chain.AllowPaid = true

	resp, err := chain.Generate(context.Background(), provider.GenerateRequest{Prompt: "test"})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.Text != "fallback response" {
		t.Errorf("Text = %q, want fallback response", resp.Text)
	}
}

func TestChainProvider_PaidProviderBlocked(t *testing.T) {
	paid := &mockProvider{
		response: provider.GenerateResponse{Text: "paid response"},
		tier:     provider.TierPaid,
	}
	chain := provider.NewChainProvider(paid)
	chain.AllowPaid = false

	_, err := chain.Generate(context.Background(), provider.GenerateRequest{Prompt: "test"})
	if err == nil {
		t.Fatal("expected error when all providers are blocked")
	}
}

func TestChainProvider_PaidProviderAllowed(t *testing.T) {
	paid := &mockProvider{
		response: provider.GenerateResponse{Text: "paid response"},
		tier:     provider.TierPaid,
	}
	chain := provider.NewChainProvider(paid)
	chain.AllowPaid = true

	resp, err := chain.Generate(context.Background(), provider.GenerateRequest{Prompt: "test"})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.Text != "paid response" {
		t.Errorf("Text = %q, want paid response", resp.Text)
	}
}

func TestChainProvider_NonRetryableStops(t *testing.T) {
	failing := &mockProvider{
		err: fmt.Errorf("forbidden"),
	}
	success := &mockProvider{
		response: provider.GenerateResponse{Text: "should not reach"},
	}
	chain := provider.NewChainProvider(failing, success)
	chain.AllowPaid = true

	_, err := chain.Generate(context.Background(), provider.GenerateRequest{Prompt: "test"})
	if err == nil {
		t.Fatal("expected error for non-retryable failure")
	}
}

func TestChainProvider_AllProvidersFail(t *testing.T) {
	f1 := &mockProvider{err: fmt.Errorf("503 unavailable")}
	f2 := &mockProvider{err: fmt.Errorf("429 rate limited")}
	chain := provider.NewChainProvider(f1, f2)
	chain.AllowPaid = true

	_, err := chain.Generate(context.Background(), provider.GenerateRequest{Prompt: "test"})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
}

func TestChainProvider_EmptyChain(t *testing.T) {
	chain := provider.NewChainProvider()
	chain.AllowPaid = true

	_, err := chain.Generate(context.Background(), provider.GenerateRequest{Prompt: "test"})
	if err == nil {
		t.Fatal("expected error for empty chain")
	}
}

func TestChainProvider_Tier(t *testing.T) {
	free := &mockProvider{tier: provider.TierFree}
	paid := &mockProvider{tier: provider.TierPaid}

	chain1 := provider.NewChainProvider(free)
	if chain1.Tier() != provider.TierFree {
		t.Error("chain with only free provider should be TierFree")
	}

	chain2 := provider.NewChainProvider(free, paid)
	if chain2.Tier() != provider.TierPaid {
		t.Error("chain with paid provider should be TierPaid")
	}

	chain3 := provider.NewChainProvider()
	if chain3.Tier() != provider.TierFree {
		t.Error("empty chain should be TierFree")
	}
}

func TestChainProvider_MixedPaidFree(t *testing.T) {
	free := &mockProvider{
		response: provider.GenerateResponse{Text: "free response"},
		tier:     provider.TierFree,
	}
	paid := &mockProvider{
		response: provider.GenerateResponse{Text: "paid response"},
		tier:     provider.TierPaid,
	}

	chain := provider.NewChainProvider(free, paid)
	chain.AllowPaid = false

	resp, err := chain.Generate(context.Background(), provider.GenerateRequest{Prompt: "test"})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.Text != "free response" {
		t.Errorf("Text = %q, want free response", resp.Text)
	}
}

func TestStartLatencyMeasurer(t *testing.T) {
	m := mock.New()
	measurer := provider.NewStartLatencyMeasurer(m, "mock")
	resp, err := measurer.Generate(context.Background(), provider.GenerateRequest{Prompt: "test"})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.Text == "" {
		t.Error("expected non-empty text")
	}
	if measurer.Name() != "mock" {
		t.Errorf("Name() = %q, want mock", measurer.Name())
	}
}

func TestChainWithRealOpenAI(t *testing.T) {
	// Verify that openai.Provider works in a chain
	p := openai.New("test-key")
	chain := provider.NewChainProvider(p)
	chain.AllowPaid = true

	// Don't actually call the API — just verify the chain accepts it
	if chain.Tier() != provider.TierPaid {
		t.Error("chain with OpenAI should be TierPaid")
	}
}

func TestChainWithRealOllama(t *testing.T) {
	// Verify that ollama.Provider works in a chain
	p := ollama.New("http://localhost:11434")
	chain := provider.NewChainProvider(p)

	// Ollama is free tier
	// Note: ollama.Provider doesn't implement TieredProvider, so chain treats it as free
	if len(chain.Providers) != 1 {
		t.Errorf("chain should have 1 provider, got %d", len(chain.Providers))
	}
}
