// Package ollama provides an LLM provider for local Ollama instances.
//
// OllamaProvider calls Ollama's native /api/generate endpoint using raw HTTP.
// No SDK dependency — just net/http and encoding/json.
//
// For OpenAI-compatible Ollama mode, use the openai package with
// NewWithBaseURL("http://localhost:11434/v1") instead.
package ollama

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
)

// Name is the provider name reported in responses.
const Name = "ollama"

// DefaultBaseURL is the default Ollama API endpoint.
const DefaultBaseURL = "http://localhost:11434"

// DefaultHTTPTimeout is the timeout for individual HTTP calls.
// Local inference can be slow, especially for large models.
const DefaultHTTPTimeout = 20 * time.Minute

// Provider generates completions via a local Ollama instance.
// It also embeds ChatProvider for native tool calling support (V30).
type Provider struct {
	BaseURL    string
	HTTPClient *http.Client
	chat       *ChatProvider // embedded chat provider for /api/chat
}

// New creates an OllamaProvider with sensible defaults.
// If baseURL is empty it defaults to http://localhost:11434.
// The returned Provider also implements ChatProvider via interface assertion.
func New(baseURL string) *Provider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	p := &Provider{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: DefaultHTTPTimeout, Transport: provider.DefaultTransport},
	}
	p.chat = &ChatProvider{
		BaseURL:    p.BaseURL,
		HTTPClient: p.HTTPClient,
	}
	return p
}

// ---------- request / response types for /api/generate ----------

type generateRequest struct {
	Model   string          `json:"model"`
	Prompt  string          `json:"prompt"`
	Stream  bool            `json:"stream"`
	Think   *bool           `json:"think,omitempty"`
	Options generateOptions `json:"options,omitempty"`
}

type generateOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

type generateResponse struct {
	Response        string `json:"response"`
	Model           string `json:"model"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	TotalDuration   int64  `json:"total_duration"`
	Done            bool   `json:"done"`
	Error           string `json:"error,omitempty"`
}

// ---------- Provider interface ----------

// Generate sends a completion request to Ollama and returns the result.
func (o *Provider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	start := time.Now()

	body := generateRequest{
		Model:  req.Model,
		Prompt: req.Prompt,
		Stream: false,
		Think:  boolPtr(false), // disable thinking for simple generate calls
		Options: generateOptions{
			Temperature: req.Temperature,
			NumPredict:  req.MaxTokens,
		},
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/generate", bytes.NewReader(bodyBytes))
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("ollama: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.HTTPClient.Do(httpReq)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("ollama: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyStr := strings.TrimSpace(string(respBody))
		if resp.StatusCode == http.StatusTooManyRequests && isQuotaExhausted(bodyStr) {
			return provider.GenerateResponse{}, fmt.Errorf("ollama: HTTP %d: %w", resp.StatusCode, provider.ErrQuotaExhausted)
		}
		return provider.GenerateResponse{}, fmt.Errorf("ollama: HTTP %d", resp.StatusCode)
	}

	var oResp generateResponse
	if err := json.Unmarshal(respBody, &oResp); err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("ollama: unmarshal response: %w", err)
	}

	if oResp.Error != "" {
		return provider.GenerateResponse{}, fmt.Errorf("ollama: model request failed")
	}

	publicText, err := sanitizeGenerateText(oResp.Response)
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("ollama: no public answer: %w", err)
	}

	latency := time.Since(start).Milliseconds()

	promptTokens := oResp.PromptEvalCount
	outputTokens := oResp.EvalCount
	if promptTokens <= 0 {
		promptTokens = len(req.Prompt) / 4
	}
	if outputTokens <= 0 {
		outputTokens = len(oResp.Response) / 4
	}

	return provider.GenerateResponse{
		Text:         publicText,
		Model:        oResp.Model,
		Provider:     Name,
		LatencyMS:    latency,
		PromptTokens: promptTokens,
		OutputTokens: outputTokens,
		Raw: map[string]any{
			"prompt_eval_count": oResp.PromptEvalCount,
			"eval_count":        oResp.EvalCount,
			"total_duration_ns": oResp.TotalDuration,
		},
	}, nil
}

// sanitizeGenerateText removes explicit private-reasoning containers from the
// public completion channel. Markdown code spans are preserved so examples and
// generated source containing literal tags are not rewritten.
func sanitizeGenerateText(text string) (string, error) {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	lowerTrimmed := strings.ToLower(trimmed)
	if strings.HasPrefix(lowerTrimmed, "<think>") && !strings.Contains(lowerTrimmed, "</think>") {
		return "", fmt.Errorf("unterminated <think> block")
	}

	lower := strings.ToLower(text)
	var out strings.Builder
	start := 0
	inFence, inCode := false, false
	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], "```") {
			inFence = !inFence
			i += 3
			continue
		}
		if !inFence && text[i] == '`' {
			inCode = !inCode
			i++
			continue
		}
		if !inFence && !inCode && strings.HasPrefix(lower[i:], "<think>") {
			endRel := strings.Index(lower[i+len("<think>"):], "</think>")
			if endRel < 0 {
				return "", fmt.Errorf("unterminated <think> block")
			}
			out.WriteString(text[start:i])
			i += len("<think>") + endRel + len("</think>")
			start = i
			continue
		}
		i++
	}
	out.WriteString(text[start:])
	return strings.TrimSpace(out.String()), nil
}

// ChatGenerate delegates to the embedded ChatProvider.
// This makes the Ollama Provider satisfy the ChatProvider interface
// via interface assertion: prov.(provider.ChatProvider) will succeed.
func (o *Provider) ChatGenerate(ctx context.Context, req provider.ChatGenerateRequest) (provider.ChatGenerateResponse, error) {
	return o.chat.ChatGenerate(ctx, req)
}

// Name returns the provider name for diagnostics and provider chains.
func (o *Provider) Name() string {
	return Name
}

// Tier returns free because Ollama runs locally by default.
func (o *Provider) Tier() provider.ProviderTier {
	return provider.TierFree
}

// boolPtr returns a pointer to the given bool value.
func boolPtr(b bool) *bool {
	return &b
}

// Compile-time interface checks.
var _ provider.Provider = (*Provider)(nil)
var _ provider.ChatProvider = (*Provider)(nil)
var _ provider.NamedProvider = (*Provider)(nil)
var _ provider.TieredProvider = (*Provider)(nil)
