// Package openai streaming support — Server-Sent Events (SSE) for OpenAI-compatible APIs.
//
// GenerateStream reads SSE events from the OpenAI response and batches tokens
// into 50ms intervals as per V14a design. Token events go to a separate NATS
// stream (PRIZM_TOKENS) and are NOT written to events.jsonl.
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

// GenerateStream calls the OpenAI chat completion API with streaming enabled.
// It returns a channel of TokenChunk batches. The caller reads from the
// channel until it's closed (successful completion) or an error is sent.
func (p *Provider) GenerateStream(ctx context.Context, req provider.GenerateRequest) (<-chan provider.TokenChunk, error) {
	chatReq := chatCompletionRequest{
		Model:       req.Model,
		Messages:    []chatMessage{{Role: "user", Content: req.Prompt}},
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		return nil, retry.NewRetryableError(fmt.Errorf("openai: rate limited (429)"))
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		resp.Body.Close()
		return nil, retry.NewRetryableError(fmt.Errorf("openai: service unavailable (503)"))
	}
	if resp.StatusCode == http.StatusBadGateway {
		resp.Body.Close()
		return nil, retry.NewRetryableError(fmt.Errorf("openai: bad gateway (502)"))
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("openai: API error (%d): %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan provider.TokenChunk, 64)
	go p.readSSEStream(resp, ch)

	return ch, nil
}

// readSSEStream reads Server-Sent Events from the OpenAI response
// and batches tokens into 50ms intervals.
func (p *Provider) readSSEStream(resp *http.Response, ch chan<- provider.TokenChunk) {
	defer close(ch)
	defer resp.Body.Close()

	var tokens []string
	var index int
	lastBatch := time.Now()
	batchInterval := 50 * time.Millisecond

	decoder := json.NewDecoder(resp.Body)
	for decoder.More() {
		var line struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}

		if err := decoder.Decode(&line); err != nil {
			ch <- provider.TokenChunk{Error: fmt.Errorf("openai: decode SSE: %w", err), Finished: true}
			return
		}

		for _, choice := range line.Choices {
			if choice.Delta.Content != "" {
				tokens = append(tokens, choice.Delta.Content)
				index++

				if time.Since(lastBatch) >= batchInterval {
					batchTokens := make([]string, len(tokens))
					copy(batchTokens, tokens)
					ch <- provider.TokenChunk{
						Tokens:   batchTokens,
						Index:    index - len(tokens),
						Finished: false,
					}
					tokens = tokens[:0]
					lastBatch = time.Now()
				}
			}

			if choice.FinishReason != nil && *choice.FinishReason == "stop" {
				if len(tokens) > 0 {
					batchTokens := make([]string, len(tokens))
					copy(batchTokens, tokens)
					ch <- provider.TokenChunk{
						Tokens:   batchTokens,
						Index:    index - len(tokens),
						Finished: true,
					}
				} else {
					ch <- provider.TokenChunk{Finished: true}
				}
				return
			}
		}
	}

	// Stream ended without finish_reason
	if len(tokens) > 0 {
		ch <- provider.TokenChunk{Tokens: tokens, Index: index - len(tokens), Finished: true}
	} else {
		ch <- provider.TokenChunk{Finished: true}
	}
}
