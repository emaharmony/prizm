package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/provider"
)

type fixedDelegateProvider string

func (p fixedDelegateProvider) Generate(context.Context, provider.GenerateRequest) (provider.GenerateResponse, error) {
	return provider.GenerateResponse{Text: string(p)}, nil
}

func TestExecuteDelegatePublishesOnlyPublicOutput(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     string
		preserve string
	}{
		{
			name:     "closed reasoning and fenced code",
			response: "Public <think>PRIVATE_CANARY</think> result\n```diff\n-<think>literal patch data</think>\n+kept\n```",
			want:     "Public  result",
			preserve: "<think>literal patch data</think>",
		},
		{
			name:     "unterminated reasoning",
			response: "Public <think>PRIVATE_CANARY",
			want:     "The delegated model did not return a public answer.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := agent.NewRegistry()
			if err := registry.Register(&agent.Agent{
				Name: "reviewer", Version: "1", Role: "review", ProviderName: "fixed",
				Capabilities: []agent.AgentCapability{{Action: "review", Description: "Review"}},
			}); err != nil {
				t.Fatal(err)
			}
			var completed map[string]any
			result, err := ExecuteDelegate(context.Background(), DelegationConfig{
				AgentRegistry: registry,
				NewProvider:   func(string) (provider.Provider, error) { return fixedDelegateProvider(tt.response), nil },
			}, Step{ID: "delegate-1", Input: map[string]any{"agent": "reviewer", "task": "review"}}, nil,
				func(eventType, _ string, payload map[string]any) {
					if eventType == agent.EventAgentCompleted {
						completed = payload
					}
				})
			if err != nil {
				t.Fatalf("ExecuteDelegate: %v", err)
			}
			eventOutput, _ := completed["output"].(string)
			if strings.Contains(result.Output, "PRIVATE_CANARY") || strings.Contains(eventOutput, "PRIVATE_CANARY") {
				t.Fatalf("private reasoning leaked: result=%q event=%q", result.Output, eventOutput)
			}
			if tt.preserve == "" {
				if result.Output != tt.want || eventOutput != tt.want {
					t.Fatalf("unterminated reasoning was not failed closed: result=%q event=%q", result.Output, eventOutput)
				}
			} else if !strings.Contains(result.Output, tt.want) || !strings.Contains(eventOutput, tt.want) {
				t.Fatalf("public delegation answer missing: result=%q event=%q", result.Output, eventOutput)
			}
			if tt.preserve != "" && !strings.Contains(result.Output, tt.preserve) {
				t.Fatalf("fenced code payload was changed: %q", result.Output)
			}
		})
	}
}
