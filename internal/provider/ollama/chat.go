package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/provider"
)

// ChatProvider implements provider.ChatProvider using Ollama's /api/chat endpoint
// with native tool calling support.
//
// Unlike the completion-based Provider which uses /api/generate with flat prompts,
// ChatProvider uses structured messages (system/user/assistant/tool) and passes
// tool schemas via the `tools` parameter, letting the model generate structured
// tool_calls instead of text-based JSON.
type ChatProvider struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewChatProvider creates an OllamaChatProvider with sensible defaults.
// If baseURL is empty it defaults to http://localhost:11434.
func NewChatProvider(baseURL string) *ChatProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	return &ChatProvider{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: DefaultHTTPTimeout, Transport: provider.DefaultTransport},
	}
}

// ---------- request / response types for /api/chat ----------

// ollamaFunction is the Ollama wire format for a function definition.
// Shared between chat.go and tools.go.
type ollamaFunction struct {
	Type     string `json:"type"` // always "function"
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type chatRequest struct {
	Model    string           `json:"model"`
	Messages []ollamaMessage  `json:"messages"`
	Tools    []ollamaFunction `json:"tools,omitempty"`
	Stream   bool             `json:"stream"`
	Think    *bool            `json:"think,omitempty"`
	Options  generateOptions  `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type chatResponse struct {
	Message struct {
		Role      string           `json:"role"`
		Content   string           `json:"content"`
		Thinking  string           `json:"thinking,omitempty"`
		ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	Model           string `json:"model"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	TotalDuration   int64  `json:"total_duration"`
	Done            bool   `json:"done"`
	Error           string `json:"error,omitempty"`
}

// ---------- ChatProvider interface ----------

// ChatGenerate sends a chat completion request to Ollama and returns the result.
// The response may contain Content (final text), ToolCalls (tool requests), or both.
func (cp *ChatProvider) ChatGenerate(ctx context.Context, req provider.ChatGenerateRequest) (provider.ChatGenerateResponse, error) {
	start := time.Now()

	// Convert provider types to Ollama wire format
	ollamaMsgs := make([]ollamaMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		om := ollamaMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
		// Convert tool calls if present
		if len(msg.ToolCalls) > 0 {
			om.ToolCalls = make([]ollamaToolCall, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				om.ToolCalls[i] = ollamaToolCall{
					Function: ollamaFunctionCall{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}
		}
		ollamaMsgs = append(ollamaMsgs, om)
	}

	// Convert tools to Ollama function format
	ollamaTools := make([]ollamaFunction, 0, len(req.Tools))
	for _, t := range req.Tools {
		ollamaTools = append(ollamaTools, ollamaFunction{
			Type: "function",
			Function: struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				Parameters  map[string]any `json:"parameters"`
			}{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			},
		})
	}

	body := chatRequest{
		Model:    req.Model,
		Messages: ollamaMsgs,
		Tools:    ollamaTools,
		Stream:   false,
		// Disable thinking, matching the /api/generate path (ollama.go).
		// Reasoning-hybrid models (e.g. GLM, DeepSeek-R1, Qwen3) can absorb
		// tool-call intent into a hidden reasoning channel instead of the
		// structured tool_calls field when thinking is left at its default,
		// producing a plain-text response with no tool call at all.
		Think: boolPtr(false),
		Options: generateOptions{
			Temperature: req.Temperature,
			NumPredict:  req.MaxTokens,
		},
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return provider.ChatGenerateResponse{}, fmt.Errorf("ollama/chat: marshal request: %w", err)
	}

	// Send request with retry for transient errors (502, 503, 504, 429).
	var resp *http.Response
	var respBody []byte
	for attempt := 0; attempt < 4; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cp.BaseURL+"/api/chat", bytes.NewReader(bodyBytes))
		if err != nil {
			return provider.ChatGenerateResponse{}, fmt.Errorf("ollama/chat: create request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err = cp.HTTPClient.Do(httpReq)
		if err != nil {
			if attempt < 3 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return provider.ChatGenerateResponse{}, fmt.Errorf("ollama/chat: %w", err)
		}

		respBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			if attempt < 3 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return provider.ChatGenerateResponse{}, fmt.Errorf("ollama/chat: read response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			bodyStr := strings.TrimSpace(string(respBody))
			// A 429 caused by an account-level usage quota (as opposed to a
			// transient per-request rate limit) won't clear up within a few
			// seconds of backoff — retrying here just burns time. Fail fast
			// and let FailoverProvider skip this target for the rest of the
			// session instead of retrying it.
			if resp.StatusCode == http.StatusTooManyRequests && isQuotaExhausted(bodyStr) {
				return provider.ChatGenerateResponse{}, fmt.Errorf("ollama/chat: HTTP %d: %s: %w", resp.StatusCode, bodyStr, provider.ErrQuotaExhausted)
			}
			// Retry on transient errors
			if isRetryableStatus(resp.StatusCode) && attempt < 3 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return provider.ChatGenerateResponse{}, fmt.Errorf("ollama/chat: HTTP %d: %s", resp.StatusCode, bodyStr)
		}
		break // success
	}

	var oResp chatResponse
	if err := json.Unmarshal(respBody, &oResp); err != nil {
		return provider.ChatGenerateResponse{}, fmt.Errorf("ollama/chat: unmarshal response: %w", err)
	}

	if oResp.Error != "" {
		return provider.ChatGenerateResponse{}, fmt.Errorf("ollama/chat: %s", oResp.Error)
	}

	// Diagnostic: if the model returned thinking content despite think:false,
	// or returned no tool_calls while thinking is non-trivial, that's a clear
	// signal tool-call intent may be getting absorbed into the reasoning
	// channel instead of the structured tool_calls field.
	if len(oResp.Message.Thinking) > 0 {
		log.Printf("ollama/chat: model=%s returned thinking content (%d chars), tool_calls=%d", req.Model, len(oResp.Message.Thinking), len(oResp.Message.ToolCalls))
	}

	latency := time.Since(start).Milliseconds()

	promptTokens := oResp.PromptEvalCount
	outputTokens := oResp.EvalCount
	if promptTokens <= 0 {
		promptTokens = estimateTokens(req.Messages)
	}
	if outputTokens <= 0 {
		outputTokens = len(oResp.Message.Content) / 4
	}

	// Convert Ollama tool calls to provider types
	toolCalls := make([]provider.ToolCall, 0, len(oResp.Message.ToolCalls))
	for i, tc := range oResp.Message.ToolCalls {
		toolCalls = append(toolCalls, provider.ToolCall{
			ID: fmt.Sprintf("tc_%d_%d", start.UnixNano(), i),
			Function: provider.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}

	return provider.ChatGenerateResponse{
		Content:      oResp.Message.Content,
		ToolCalls:    toolCalls,
		Model:        oResp.Model,
		Provider:     Name + "-chat",
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

// isQuotaExhausted reports whether a 429 response body indicates an
// account-level usage quota was hit (Ollama Cloud's phrasing: "you have
// reached your weekly usage limit"), as opposed to a transient per-request
// rate limit that's worth retrying.
func isQuotaExhausted(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "usage limit") || strings.Contains(lower, "quota")
}

// estimateTokens provides a rough token estimate for chat messages.
// Used when Ollama doesn't return prompt_eval_count.
func estimateTokens(messages []provider.ChatMessage) int {
	total := 0
	for _, msg := range messages {
		total += len(msg.Content) / 4
		for _, tc := range msg.ToolCalls {
			total += len(tc.Function.Name)
			for k, v := range tc.Function.Arguments {
				total += len(k)
				if s, ok := v.(string); ok {
					total += len(s) / 4
				}
			}
		}
	}
	return total
}

// Compile-time interface check.
var _ provider.ChatProvider = (*ChatProvider)(nil)

// isRetryableStatus returns true for transient HTTP status codes worth retrying.
func isRetryableStatus(code int) bool {
	return code == 429 || code == 502 || code == 503 || code == 504
}
