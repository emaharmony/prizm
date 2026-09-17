// Package anthropic streaming support — Server-Sent Events for the Messages API.
//
// Anthropic SSE uses typed events: message_start, content_block_start,
// content_block_delta, content_block_stop, message_delta, message_stop.
// We extract text from content_block_delta events and batch into 50ms intervals.
package anthropic

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
	"github.com/emaharmony/prizm/internal/sse"
)

// GenerateStream calls the Anthropic Messages API with streaming enabled.
func (p *Provider) GenerateStream(ctx context.Context, req provider.GenerateRequest) (<-chan provider.TokenChunk, error) {
	anthReq := messagesRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Messages:    []anthropicMessage{{Role: "user", Content: req.Prompt}},
		Temperature: req.Temperature,
		Stream:      true,
	}

	// V76: Use system blocks with cache_control for prompt caching (same as sync path)
	if req.System != "" {
		anthReq.System = []systemBlock{
			{
				Type:         "text",
				Text:         req.System,
				CacheControl: &cacheControl{Type: "ephemeral"},
			},
		}
	}

	body, err := json.Marshal(anthReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.APIKey)
	httpReq.Header.Set("anthropic-version", APIVersion)

	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: request failed: %w", err)
	}

	// Handle retryable errors consistently with sync path
	if resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		return nil, retry.NewRetryableError(fmt.Errorf("anthropic: rate limited (429)"))
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		resp.Body.Close()
		return nil, retry.NewRetryableError(fmt.Errorf("anthropic: service unavailable (503)"))
	}
	if resp.StatusCode == http.StatusBadGateway {
		resp.Body.Close()
		return nil, retry.NewRetryableError(fmt.Errorf("anthropic: bad gateway (502)"))
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic: API error (%d): %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan provider.TokenChunk, 64)
	go p.readSSEStream(resp, ch)
	return ch, nil
}

// ---------- Anthropic SSE event types ----------

// contentBlockDelta is the content_block_delta event payload.
type contentBlockDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

// messageDeltaEvent carries the stop_reason at the end of a stream.
type messageDeltaEvent struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
}

// ---------- SSE reader ----------

// readSSEStream reads Anthropic's typed SSE events and batches tokens.
func (p *Provider) readSSEStream(resp *http.Response, ch chan<- provider.TokenChunk) {
	defer close(ch)
	defer resp.Body.Close()

	var tokens []string
	var index int
	var stopReason string
	lastBatch := time.Now()
	batchInterval := 50 * time.Millisecond

	decoder := sse.NewDecoder(resp.Body)

	for {
		ev, err := decoder.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			ch <- provider.TokenChunk{Error: fmt.Errorf("anthropic: read SSE: %w", err), Finished: true}
			return
		}

		switch ev.Event {
		case "content_block_delta":
			var delta contentBlockDelta
			if err := json.Unmarshal(ev.Data, &delta); err != nil {
				ch <- provider.TokenChunk{Error: fmt.Errorf("anthropic: decode delta: %w", err), Finished: true}
				return
			}
			if delta.Delta.Type == "text_delta" && delta.Delta.Text != "" {
				tokens = append(tokens, delta.Delta.Text)
				index++

				if time.Since(lastBatch) >= batchInterval {
					batchTokens := make([]string, len(tokens))
					copy(batchTokens, tokens)
					ch <- provider.TokenChunk{Tokens: batchTokens, Index: index - len(tokens)}
					tokens = tokens[:0]
					lastBatch = time.Now()
				}
			}

		case "message_delta":
			// Capture stop_reason from message_delta event
			var msgDelta messageDeltaEvent
			if err := json.Unmarshal(ev.Data, &msgDelta); err == nil && msgDelta.Delta.StopReason != "" {
				stopReason = msgDelta.Delta.StopReason
			}

		case "message_stop":
			if len(tokens) > 0 {
				batchTokens := make([]string, len(tokens))
				copy(batchTokens, tokens)
				ch <- provider.TokenChunk{
					Tokens:   batchTokens,
					Index:    index - len(tokens),
					Finished: true,
					Raw:      map[string]any{"stop_reason": stopReason},
				}
			} else {
				ch <- provider.TokenChunk{Finished: true, Raw: map[string]any{"stop_reason": stopReason}}
			}
			return

		case "error":
			// Drain remaining body on error to avoid connection pool issues
			io.Copy(io.Discard, resp.Body)
			ch <- provider.TokenChunk{
				Error:    fmt.Errorf("anthropic: stream error: %s", string(ev.Data)),
				Finished: true,
			}
			return
		}
	}

	// Stream ended without message_stop
	if len(tokens) > 0 {
		ch <- provider.TokenChunk{Tokens: tokens, Index: index - len(tokens), Finished: true}
	} else {
		ch <- provider.TokenChunk{Finished: true}
	}
}

// Compile-time interface check.
var _ provider.StreamingProvider = (*Provider)(nil)
