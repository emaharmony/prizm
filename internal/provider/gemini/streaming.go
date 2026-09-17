// Package gemini streaming support — Server-Sent Events for generateContent.
//
// Gemini's streamGenerateContent endpoint returns SSE events with
// generateContentResponse chunks. We extract text from candidates[0].content.parts
// and batch tokens into 50ms intervals.
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
	"github.com/emaharmony/prizm/internal/sse"
)

// GenerateStream calls the Gemini streamGenerateContent API with streaming enabled.
func (p *Provider) GenerateStream(ctx context.Context, req provider.GenerateRequest) (<-chan provider.TokenChunk, error) {
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
		return nil, fmt.Errorf("gemini: marshal request: %w", err)
	}

	// streamGenerateContent uses alt=sse query parameter
	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s", p.BaseURL, req.Model, p.APIKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gemini: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: request failed: %w", err)
	}

	// Handle retryable errors consistently with sync path
	if resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		return nil, retry.NewRetryableError(fmt.Errorf("gemini: rate limited (429)"))
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		resp.Body.Close()
		return nil, retry.NewRetryableError(fmt.Errorf("gemini: service unavailable (503)"))
	}
	if resp.StatusCode == http.StatusBadGateway {
		resp.Body.Close()
		return nil, retry.NewRetryableError(fmt.Errorf("gemini: bad gateway (502)"))
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("gemini: API error (%d): %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan provider.TokenChunk, 64)
	go p.readSSEStream(resp, ch)
	return ch, nil
}

// readSSEStream reads Gemini's SSE events and batches tokens into 50ms intervals.
func (p *Provider) readSSEStream(resp *http.Response, ch chan<- provider.TokenChunk) {
	defer close(ch)
	defer resp.Body.Close()

	var tokens []string
	var index int
	lastBatch := time.Now()
	batchInterval := 50 * time.Millisecond

	decoder := sse.NewDecoder(resp.Body)

	for {
		data, err := decoder.NextData()
		if err != nil {
			if err == io.EOF {
				break
			}
			ch <- provider.TokenChunk{Error: fmt.Errorf("gemini: read SSE: %w", err), Finished: true}
			return
		}

		var chunk generateContentResponse
		if err := json.Unmarshal(data, &chunk); err != nil {
			ch <- provider.TokenChunk{Error: fmt.Errorf("gemini: decode chunk: %w", err), Finished: true}
			return
		}

		for _, candidate := range chunk.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					tokens = append(tokens, part.Text)
					index++

					if time.Since(lastBatch) >= batchInterval {
						batchTokens := make([]string, len(tokens))
						copy(batchTokens, tokens)
						ch <- provider.TokenChunk{Tokens: batchTokens, Index: index - len(tokens)}
						tokens = tokens[:0]
						lastBatch = time.Now()
					}
				}
			}

			// Check for finish
			if candidate.FinishReason == "STOP" {
				if len(tokens) > 0 {
					batchTokens := make([]string, len(tokens))
					copy(batchTokens, tokens)
					ch <- provider.TokenChunk{
						Tokens:   batchTokens,
						Index:    index - len(tokens),
						Finished: true,
						Raw:      map[string]any{"finish_reason": "STOP"},
					}
				} else {
					ch <- provider.TokenChunk{Finished: true, Raw: map[string]any{"finish_reason": "STOP"}}
				}
				return
			}

			// SAFETY finish means content was filtered — signal as error, not clean completion
			if candidate.FinishReason == "SAFETY" {
				io.Copy(io.Discard, resp.Body) // drain remaining body
				if len(tokens) > 0 {
					batchTokens := make([]string, len(tokens))
					copy(batchTokens, tokens)
					ch <- provider.TokenChunk{
						Tokens:   batchTokens,
						Index:    index - len(tokens),
						Finished: true,
						Raw:      map[string]any{"finish_reason": "SAFETY"},
					}
				} else {
					ch <- provider.TokenChunk{
						Finished: true,
						Raw:      map[string]any{"finish_reason": "SAFETY"},
					}
				}
				return
			}
		}
	}

	// Stream ended
	if len(tokens) > 0 {
		ch <- provider.TokenChunk{Tokens: tokens, Index: index - len(tokens), Finished: true}
	} else {
		ch <- provider.TokenChunk{Finished: true}
	}
}

// Compile-time interface check.
var _ provider.StreamingProvider = (*Provider)(nil)
