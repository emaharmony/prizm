// Package gemini provides an LLM provider for Google's Gemini generateContent API.
//
// Design decisions:
//   - Raw HTTP, no SDK. The Google GenAI Go SDK is heavy and opinionated.
//   - Uses API key as query parameter (Google's auth model for generateContent).
//     Note: This exposes the key in URLs/logs. This is Google's required API design,
//     not our choice. See https://ai.google.dev/gemini-api/docs/api-key
//   - Supports both gemini and gemini-pro model families.
//   - ChainProvider handles tier-based paid guard; this provider just reports its tier.
package gemini

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

const (
	// DefaultBaseURL is the Gemini API endpoint.
	DefaultBaseURL = "https://generativelanguage.googleapis.com"

	// Name is the provider identifier.
	Name = "gemini"
)

// Provider calls Google's Gemini generateContent API using raw HTTP.
type Provider struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	TierVal    provider.ProviderTier
}

// New creates a new Gemini provider with the given API key.
func New(apiKey string) *Provider {
	return &Provider{
		APIKey:  apiKey,
		BaseURL: DefaultBaseURL,
		HTTPClient: &http.Client{
			Timeout:   120 * time.Second,
			Transport: provider.DefaultTransport,
		},
		TierVal: provider.TierPaid,
	}
}

// NewWithBaseURL creates a provider with a custom base URL (for Vertex AI).
func NewWithBaseURL(apiKey, baseURL string) *Provider {
	return &Provider{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 120 * time.Second, Transport: provider.DefaultTransport},
		TierVal:    provider.TierPaid,
	}
}

// Name returns the provider name.
func (p *Provider) Name() string { return Name }

// Tier returns the provider cost tier.
func (p *Provider) Tier() provider.ProviderTier { return p.TierVal }

// ---------- Gemini request/response types ----------

type generateContentRequest struct {
	Contents         []geminiContent   `json:"contents"`
	GenerationConfig *generationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type generationConfig struct {
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
}

type generateContentResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata *geminiUsage      `json:"usageMetadata,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
}

type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// ---------- Generate ----------

// Generate calls the Gemini generateContent API synchronously.
func (p *Provider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	start := time.Now()

	gemReq := generateContentRequest{
		Contents: []geminiContent{
			{Role: "user", Parts: []geminiPart{{Text: req.Prompt}}},
		},
		GenerationConfig: &generationConfig{
			Temperature:     req.Temperature,
			MaxOutputTokens: req.MaxTokens,
		},
	}

	body, err := json.Marshal(gemReq)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("gemini: marshal request: %w", err)
	}

	// Gemini uses the API key as a query parameter
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", p.BaseURL, req.Model, p.APIKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("gemini: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("gemini: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return provider.GenerateResponse{}, retry.NewRetryableError(fmt.Errorf("gemini: rate limited (429)"))
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return provider.GenerateResponse{}, retry.NewRetryableError(fmt.Errorf("gemini: service unavailable (503)"))
	}
	if resp.StatusCode == http.StatusBadGateway {
		return provider.GenerateResponse{}, retry.NewRetryableError(fmt.Errorf("gemini: bad gateway (502)"))
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return provider.GenerateResponse{}, fmt.Errorf("gemini: API error (%d): %s", resp.StatusCode, string(respBody))
	}

	var gemResp generateContentResponse
	if err := json.NewDecoder(resp.Body).Decode(&gemResp); err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("gemini: decode response: %w", err)
	}

	if len(gemResp.Candidates) == 0 {
		return provider.GenerateResponse{}, fmt.Errorf("gemini: no candidates in response")
	}

	// Extract text from candidate content parts
	text := ""
	for _, part := range gemResp.Candidates[0].Content.Parts {
		text += part.Text
	}

	if text == "" {
		return provider.GenerateResponse{}, fmt.Errorf("gemini: no text in candidate")
	}

	promptTokens := 0
	outputTokens := 0
	if gemResp.UsageMetadata != nil {
		promptTokens = gemResp.UsageMetadata.PromptTokenCount
		outputTokens = gemResp.UsageMetadata.CandidatesTokenCount
	}

	duration := time.Since(start).Milliseconds()

	return provider.GenerateResponse{
		Text:         text,
		Model:        req.Model,
		Provider:     Name,
		LatencyMS:    duration,
		PromptTokens: promptTokens,
		OutputTokens: outputTokens,
		Raw: map[string]any{
			"finish_reason": gemResp.Candidates[0].FinishReason,
			"model":         req.Model,
			"prompt_tokens": promptTokens,
			"output_tokens": outputTokens,
		},
	}, nil
}

// IsRetryableError checks if a Gemini error is retryable.
func IsRetryableError(err error) bool {
	return retry.IsRetryable(err)
}

// Compile-time interface check.
var _ provider.Provider = (*Provider)(nil)
