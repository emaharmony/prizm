// Package openai provides an LLM provider for OpenAI's chat completion API
// and OpenAI-compatible endpoints (Together AI, Groq, Azure, etc.).
//
// Design decisions:
//   - Raw HTTP, no SDK. The OpenAI Go SDK is 2MB. We need ~200 lines.
//   - Server-Sent Events (SSE) for streaming. No WebSockets.
//   - Tier-based paid guard. Providers have a Tier (Free/Paid) and the
//     chain skips paid providers unless --allow-paid-fallback is set.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/retry"
)

// TierPaid indicates a paid cloud API provider.
const TierPaid = provider.TierPaid

// DefaultTransport re-exports the shared provider transport for backward compatibility.
// Prefer using provider.DefaultTransport directly.
var DefaultTransport = provider.DefaultTransport

// Provider calls OpenAI's chat completion API using raw HTTP.
// No SDK dependency — just net/http and encoding/json.
//
// It supports both synchronous Generate() and streaming GenerateStream().
// The streaming implementation uses Server-Sent Events (SSE) with
// 50ms batching for token events.
type Provider struct {
	APIKey     string
	BaseURL    string // defaults to https://api.openai.com/v1
	HTTPClient *http.Client
	TierVal    provider.ProviderTier // free or paid
}

// New creates a new OpenAI provider with the given API key.
// The base URL defaults to https://api.openai.com/v1 but can be overridden
// for OpenAI-compatible endpoints (Azure, Together AI, etc.).
func New(apiKey string) *Provider {
	return &Provider{
		APIKey:  apiKey,
		BaseURL: "https://api.openai.com/v1",
		HTTPClient: &http.Client{
			Timeout:   120 * time.Second,
			Transport: DefaultTransport,
		},
		TierVal: TierPaid,
	}
}

// NewWithBaseURL creates a provider with a custom base URL.
// Use this for OpenAI-compatible endpoints (Azure, Together AI, Ollama OpenAI mode).
func NewWithBaseURL(apiKey, baseURL string) *Provider {
	return &Provider{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 120 * time.Second, Transport: DefaultTransport},
		TierVal:    TierPaid,
	}
}

// Name returns the provider name.
func (p *Provider) Name() string { return "openai" }

// Tier returns the provider tier (Free or Paid).
func (p *Provider) Tier() provider.ProviderTier { return p.TierVal }

// ---------- request / response types ----------

type chatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   chatCompletionUsage    `json:"usage"`
}

type chatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ---------- Generate ----------

// Generate calls the OpenAI chat completion API synchronously.
func (p *Provider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	start := time.Now()

	chatReq := chatCompletionRequest{
		Model:       req.Model,
		Messages:    []chatMessage{{Role: "user", Content: req.Prompt}},
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      false,
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("openai: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("openai: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return provider.GenerateResponse{}, retry.NewRetryableError(fmt.Errorf("openai: rate limited (429)"))
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return provider.GenerateResponse{}, retry.NewRetryableError(fmt.Errorf("openai: service unavailable (503)"))
	}
	if resp.StatusCode == http.StatusBadGateway {
		return provider.GenerateResponse{}, retry.NewRetryableError(fmt.Errorf("openai: bad gateway (502)"))
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return provider.GenerateResponse{}, fmt.Errorf("openai: API error (%d): %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("openai: decode response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return provider.GenerateResponse{}, fmt.Errorf("openai: no choices in response")
	}

	duration := time.Since(start).Milliseconds()

	return provider.GenerateResponse{
		Text:         chatResp.Choices[0].Message.Content,
		Model:        chatResp.Model,
		Provider:     "openai",
		LatencyMS:    duration,
		PromptTokens: chatResp.Usage.PromptTokens,
		OutputTokens: chatResp.Usage.CompletionTokens,
		Raw: map[string]any{
			"id":                chatResp.ID,
			"finish_reason":     chatResp.Choices[0].FinishReason,
			"model":             chatResp.Model,
			"prompt_tokens":     chatResp.Usage.PromptTokens,
			"completion_tokens": chatResp.Usage.CompletionTokens,
		},
	}, nil
}

// IsRetryableError checks if an error from the OpenAI provider is retryable.
func IsRetryableError(err error) bool {
	return retry.IsRetryable(err)
}

// Compile-time interface checks.
var (
	_ provider.Provider          = (*Provider)(nil)
	_ provider.StreamingProvider = (*Provider)(nil)
)
