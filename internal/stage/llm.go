// Package stage provides Prizm's pipeline execution engine (V14a).
//
// LLMStage builds the prompt, calls the LLM provider, and collects the response.
// It's the core of every run — the "thinking" stage.
//
// If the provider supports StreamingProvider, the LLMStage will use
// GenerateStream() to emit token events in real time. Otherwise it
// falls back to the synchronous Generate() path.
//
// V21-5: If a StreamCallback is set on RunContext, tokens are delivered
// in real-time via the callback (e.g., to Discord for streaming edits).
// This decouples streaming delivery from any specific channel.
package stage

import (
	"context"
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/provider"
)

// LLMStage calls the LLM provider with the task prompt and collects the response.
// It supports both synchronous and streaming providers.
type LLMStage struct {
	// DryRun controls whether to skip the actual LLM call.
	// If true, only the prompt is built (no provider call).
	DryRun bool
}

// Name returns the stage identifier.
func (s *LLMStage) Name() string {
	return "llm"
}

// Validate checks that a provider and model are configured.
func (s *LLMStage) Validate(rc *RunContext) error {
	if rc.Provider == nil {
		return fmt.Errorf("stage %s: provider is required", s.Name())
	}
	if rc.Model == "" {
		return fmt.Errorf("stage %s: model is required", s.Name())
	}
	return nil
}

// Execute calls the LLM provider and collects the response.
// If the provider implements StreamingProvider, it uses streaming.
// Otherwise, it falls back to synchronous generation.
func (s *LLMStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	// Emit LLM started event
	evt := event.NewEvent(event.V1EventTypes.AgentStarted, rc.Agent, map[string]any{
		"run_id":    rc.RunID,
		"agent":     rc.Agent,
		"model":     rc.Model,
		"provider":  rc.ProviderName,
		"dry_run":   s.DryRun,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	newRC := rc.WithEvent(evt)

	if s.DryRun {
		// Dry run — just build the prompt, don't call the LLM
		return newRC, &StageResult{
			StageName: s.Name(),
			Success:   true,
			Data: map[string]any{
				"dry_run":      true,
				"model":        rc.Model,
				"provider":     rc.ProviderName,
				"prompt_built": true,
			},
		}, nil
	}

	// Check if the provider supports streaming
	if sp, ok := rc.Provider.(provider.StreamingProvider); ok {
		return s.executeStreaming(ctx, newRC, sp)
	}

	// Synchronous path
	return s.executeSync(ctx, newRC)
}

// executeSync calls Generate() and waits for the full response.
func (s *LLMStage) executeSync(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	start := time.Now()

	resp, err := rc.Provider.Generate(ctx, provider.GenerateRequest{
		Prompt:      rc.Task,
		Model:       rc.Model,
		Temperature: rc.Temperature,
		MaxTokens:   rc.MaxTokens,
		Task:        rc.Task,
		Agent:       rc.Agent,
		Project:     rc.Project,
		RunID:       rc.RunID,
	})
	duration := time.Since(start).Milliseconds()

	if err != nil {
		// LLM call failed
		failEvt := event.NewEvent(event.V1EventTypes.AgentFailed, rc.Agent, map[string]any{
			"run_id":      rc.RunID,
			"agent":       rc.Agent,
			"model":       rc.Model,
			"provider":    rc.ProviderName,
			"error":       err.Error(),
			"duration_ms": duration,
		})
		newRC := rc.WithEvent(failEvt)

		return newRC, &StageResult{
			StageName: s.Name(),
			Success:   false,
			Error:     fmt.Sprintf("LLM call failed: %v", err),
		}, nil
	}

	// LLM completed successfully
	completeEvt := event.NewEvent(event.V1EventTypes.AgentCompleted, rc.Agent, map[string]any{
		"run_id":        rc.RunID,
		"agent":         rc.Agent,
		"model":         resp.Model,
		"provider":      resp.Provider,
		"latency_ms":    resp.LatencyMS,
		"prompt_tokens": resp.PromptTokens,
		"output_tokens": resp.OutputTokens,
		"duration_ms":   duration,
	})
	newRC := rc.WithEvent(completeEvt).WithLLMResponse(resp.Text)

	return newRC, &StageResult{
		StageName: s.Name(),
		Success:   true,
		Data: map[string]any{
			"response":      resp.Text,
			"model":         resp.Model,
			"provider":      resp.Provider,
			"latency_ms":    resp.LatencyMS,
			"prompt_tokens": resp.PromptTokens,
			"output_tokens": resp.OutputTokens,
			"duration_ms":   duration,
		},
	}, nil
}

// executeStreaming calls GenerateStream() and collects token chunks.
// If a StreamCallback is set on RunContext, tokens are delivered
// in real-time via the callback (e.g., to Discord for streaming edits).
// Otherwise, tokens are accumulated silently for CLI/API use.
func (s *LLMStage) executeStreaming(ctx context.Context, rc *RunContext, sp provider.StreamingProvider) (*RunContext, *StageResult, error) {
	start := time.Now()

	ch, err := sp.GenerateStream(ctx, provider.GenerateRequest{
		Prompt:      rc.Task,
		Model:       rc.Model,
		Temperature: rc.Temperature,
		MaxTokens:   rc.MaxTokens,
		Task:        rc.Task,
		Agent:       rc.Agent,
		Project:     rc.Project,
		RunID:       rc.RunID,
	})
	if err != nil {
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   false,
			Error:     fmt.Sprintf("streaming failed to start: %v", err),
		}, nil
	}

	var fullResponse string
	var totalTokens int
	var tokenBatches int
	var callbackErr error

	for chunk := range ch {
		if chunk.Error != nil {
			// Stream error
			failEvt := event.NewEvent(event.V1EventTypes.AgentFailed, rc.Agent, map[string]any{
				"run_id": rc.RunID,
				"agent":  rc.Agent,
				"error":  chunk.Error.Error(),
			})
			newRC := rc.WithEvent(failEvt)
			return newRC, &StageResult{
				StageName: s.Name(),
				Success:   false,
				Error:     fmt.Sprintf("streaming error: %v", chunk.Error),
			}, nil
		}

		for _, token := range chunk.Tokens {
			fullResponse += token

			// Deliver token to callback if set (e.g., Discord streaming)
			if rc.StreamCallback != nil {
				if err := rc.StreamCallback(token, totalTokens, chunk.Finished); err != nil {
					// Callback error — stop streaming but preserve what we have
					callbackErr = err
					break
				}
			}
		}
		totalTokens += len(chunk.Tokens)
		tokenBatches++

		if callbackErr != nil {
			break
		}

		if chunk.Finished {
			break
		}
	}

	duration := time.Since(start).Milliseconds()

	// Emit completion event (this IS persisted to events.jsonl)
	resultData := map[string]any{
		"response":      fullResponse,
		"model":         rc.Model,
		"provider":      rc.ProviderName,
		"duration_ms":   duration,
		"output_tokens": totalTokens,
		"streamed":      true,
		"token_batches": tokenBatches,
	}

	if callbackErr != nil {
		resultData["callback_error"] = callbackErr.Error()
	}

	completeEvt := event.NewEvent(event.V1EventTypes.AgentCompleted, rc.Agent, map[string]any{
		"run_id":        rc.RunID,
		"agent":         rc.Agent,
		"model":         rc.Model,
		"provider":      rc.ProviderName,
		"latency_ms":    duration,
		"output_tokens": totalTokens,
		"streamed":      true,
		"token_batches": tokenBatches,
		"duration_ms":   duration,
	})
	newRC := rc.WithEvent(completeEvt).WithLLMResponse(fullResponse)

	return newRC, &StageResult{
		StageName: s.Name(),
		Success:   true,
		Data:      resultData,
	}, nil
}

// Rollback is a no-op — LLM calls can't be undone.
func (s *LLMStage) Rollback(ctx context.Context, rc *RunContext) error {
	return nil
}
