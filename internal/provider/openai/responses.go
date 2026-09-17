package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/retry"
)

// ResponsesProvider calls OpenAI's Responses API.
type ResponsesProvider struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	TierVal    provider.ProviderTier
}

// NewResponses creates an OpenAI Responses API provider.
func NewResponses(apiKey string) *ResponsesProvider {
	return &ResponsesProvider{
		APIKey:  apiKey,
		BaseURL: "https://api.openai.com/v1",
		HTTPClient: &http.Client{
			Timeout:   120 * time.Second,
			Transport: DefaultTransport,
		},
		TierVal: TierPaid,
	}
}

// NewResponsesWithBaseURL creates a Responses API provider with a custom base URL.
func NewResponsesWithBaseURL(apiKey, baseURL string) *ResponsesProvider {
	return &ResponsesProvider{
		APIKey:     apiKey,
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 120 * time.Second, Transport: DefaultTransport},
		TierVal:    TierPaid,
	}
}

func (p *ResponsesProvider) Name() string { return "openai_responses" }

func (p *ResponsesProvider) Tier() provider.ProviderTier { return p.TierVal }

type responsesRequest struct {
	Model           string  `json:"model"`
	Input           string  `json:"input"`
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"max_output_tokens,omitempty"`
}

type responsesResponse struct {
	ID         string                `json:"id"`
	Model      string                `json:"model"`
	OutputText string                `json:"output_text"`
	Output     []responsesOutputItem `json:"output"`
	Usage      responsesUsage        `json:"usage"`
}

type responsesOutputItem struct {
	Type    string                   `json:"type"`
	Role    string                   `json:"role"`
	Content []responsesOutputContent `json:"content"`
}

type responsesOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Generate calls the OpenAI Responses API synchronously.
func (p *ResponsesProvider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	start := time.Now()

	apiReq := responsesRequest{
		Model:           req.Model,
		Input:           req.Prompt,
		Temperature:     req.Temperature,
		MaxOutputTokens: req.MaxTokens,
	}
	body, err := json.Marshal(apiReq)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("openai responses: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("openai responses: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("openai responses: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return provider.GenerateResponse{}, retry.NewRetryableError(fmt.Errorf("openai responses: rate limited (429)"))
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return provider.GenerateResponse{}, retry.NewRetryableError(fmt.Errorf("openai responses: service unavailable (503)"))
	}
	if resp.StatusCode == http.StatusBadGateway {
		return provider.GenerateResponse{}, retry.NewRetryableError(fmt.Errorf("openai responses: bad gateway (502)"))
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return provider.GenerateResponse{}, fmt.Errorf("openai responses: API error (%d): %s", resp.StatusCode, string(respBody))
	}

	var apiResp responsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("openai responses: decode response: %w", err)
	}
	text := apiResp.OutputText
	if text == "" {
		text = collectResponsesText(apiResp.Output)
	}
	if text == "" {
		return provider.GenerateResponse{}, fmt.Errorf("openai responses: no output text in response")
	}
	model := apiResp.Model
	if model == "" {
		model = req.Model
	}

	return provider.GenerateResponse{
		Text:         text,
		Model:        model,
		Provider:     "openai_responses",
		LatencyMS:    time.Since(start).Milliseconds(),
		PromptTokens: apiResp.Usage.InputTokens,
		OutputTokens: apiResp.Usage.OutputTokens,
		Raw: map[string]any{
			"id":            apiResp.ID,
			"model":         model,
			"input_tokens":  apiResp.Usage.InputTokens,
			"output_tokens": apiResp.Usage.OutputTokens,
			"total_tokens":  apiResp.Usage.TotalTokens,
		},
	}, nil
}

func collectResponsesText(items []responsesOutputItem) string {
	var b strings.Builder
	for _, item := range items {
		for _, content := range item.Content {
			if content.Type == "output_text" || content.Text != "" {
				b.WriteString(content.Text)
			}
		}
	}
	return b.String()
}

var _ provider.Provider = (*ResponsesProvider)(nil)
