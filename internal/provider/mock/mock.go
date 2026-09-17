// Package mock provides a deterministic mock LLM provider for testing.
//
// MockProvider always succeeds with predictable output. FailingMockProvider
// always returns an error. ToolRequestMockProvider returns a JSON tool request.
//
// All three implement the provider.Provider interface and the
// provider.StreamingProvider interface.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/provider"
)

// Name is the provider name reported by MockProvider.
const Name = "mock"

// MockProvider always succeeds with deterministic output.
// Used for testing — no external dependencies.
type MockProvider struct {
	shouldFail bool
}

// New creates a MockProvider that always succeeds.
func New() *MockProvider {
	return &MockProvider{shouldFail: false}
}

// NewFailing creates a MockProvider that always returns an error.
func NewFailing() *MockProvider {
	return &MockProvider{shouldFail: true}
}

// Generate returns a deterministic response or an error depending on configuration.
func (m *MockProvider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	if m.shouldFail {
		return provider.GenerateResponse{}, fmt.Errorf("mock provider configured to fail")
	}

	start := time.Now()

	// Simulate a brief processing delay
	select {
	case <-ctx.Done():
		return provider.GenerateResponse{}, ctx.Err()
	case <-time.After(10 * time.Millisecond):
	}

	latency := time.Since(start).Milliseconds()

	return provider.GenerateResponse{
		Text:         fmt.Sprintf("Mock response for task '%s' by agent '%s' in project '%s'.", req.Task, req.Agent, req.Project),
		Model:        "mock-model",
		Provider:     Name,
		LatencyMS:    latency,
		PromptTokens: len(req.Prompt) / 4,
		OutputTokens: 50,
		Raw: map[string]any{
			"mock": true,
			"request_echo": map[string]string{
				"run_id":  req.RunID,
				"agent":   req.Agent,
				"project": req.Project,
				"task":    req.Task,
			},
		},
	}, nil
}

// ToolRequestProvider returns JSON simulating an LLM that requests a tool call.
// This is used for V3 tool execution tests.
type ToolRequestProvider struct {
	toolName  string
	toolInput map[string]any
}

// NewToolRequest creates a provider that returns a tool_request JSON response.
func NewToolRequest(toolName string, toolInput map[string]any) *ToolRequestProvider {
	return &ToolRequestProvider{
		toolName:  toolName,
		toolInput: toolInput,
	}
}

// Generate returns a JSON string representing a tool request.
func (p *ToolRequestProvider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	start := time.Now()
	select {
	case <-ctx.Done():
		return provider.GenerateResponse{}, ctx.Err()
	case <-time.After(10 * time.Millisecond):
	}
	latency := time.Since(start).Milliseconds()

	inputJSON, _ := json.Marshal(p.toolInput)
	text := fmt.Sprintf(`{"type": "tool_request", "tool": "%s", "input": %s}`, p.toolName, string(inputJSON))

	return provider.GenerateResponse{
		Text:         text,
		Model:        "mock-model",
		Provider:     Name,
		LatencyMS:    latency,
		PromptTokens: len(req.Prompt) / 4,
		OutputTokens: 50,
	}, nil
}

// GenerateStream simulates streaming output by splitting the response into
// word-sized chunks with small delays.
func (m *MockProvider) GenerateStream(ctx context.Context, req provider.GenerateRequest) (<-chan provider.TokenChunk, error) {
	if m.shouldFail {
		return nil, fmt.Errorf("mock provider configured to fail")
	}

	ch := make(chan provider.TokenChunk, 64)
	go func() {
		defer close(ch)
		response := fmt.Sprintf("Mock response for task '%s' by agent '%s' in project '%s'.", req.Task, req.Agent, req.Project)
		words := strings.Fields(response)
		for i, word := range words {
			select {
			case <-ctx.Done():
				ch <- provider.TokenChunk{Error: ctx.Err(), Finished: false}
				return
			default:
			}

			ch <- provider.TokenChunk{
				Tokens:   []string{word + " "},
				Index:    i,
				Finished: i == len(words)-1,
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return ch, nil
}

// Compile-time interface checks.
var (
	_ provider.Provider          = (*MockProvider)(nil)
	_ provider.StreamingProvider = (*MockProvider)(nil)
	_ provider.Provider          = (*ToolRequestProvider)(nil)
)
