package provider

import (
	"context"
	"fmt"
	"testing"
)

func TestFailoverProviderSkipsQuotaExhaustedTargetWithoutRetry(t *testing.T) {
	primary := &failoverFake{generate: func(GenerateRequest) (GenerateResponse, error) {
		return GenerateResponse{}, fmt.Errorf("HTTP 429: weekly usage limit reached: %w", ErrQuotaExhausted)
	}}
	fallback := &failoverFake{generate: func(req GenerateRequest) (GenerateResponse, error) {
		return GenerateResponse{Text: "recovered", Model: req.Model}, nil
	}}

	p := NewFailoverProvider([]FailoverTarget{
		{Provider: primary, ProviderName: "quota-test-primary", Model: "quota-test-model-1"},
		{Provider: fallback, ProviderName: "ollama", Model: "quota-test-fallback-1"},
	})
	resp, err := p.Generate(context.Background(), GenerateRequest{Model: "quota-test-model-1", Prompt: "hello"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if primary.generateCall != 1 {
		t.Fatalf("quota-exhausted target should be attempted exactly once (no same-call retry), got %d calls", primary.generateCall)
	}
	if resp.Model != "quota-test-fallback-1" {
		t.Fatalf("expected fallback response, got %+v", resp)
	}

	// A second, separate call must skip the exhausted target entirely —
	// this is the "don't retry until the end of the session" behavior.
	resp2, err := p.Generate(context.Background(), GenerateRequest{Model: "quota-test-model-1", Prompt: "hello again"})
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if primary.generateCall != 1 {
		t.Fatalf("exhausted target should not be attempted again this session, got %d total calls", primary.generateCall)
	}
	if resp2.Model != "quota-test-fallback-1" {
		t.Fatalf("expected fallback response on second call, got %+v", resp2)
	}
}

func TestFailoverProviderAllTargetsExhaustedReturnsClearError(t *testing.T) {
	primary := &failoverFake{generate: func(GenerateRequest) (GenerateResponse, error) {
		return GenerateResponse{}, fmt.Errorf("HTTP 429: weekly usage limit reached: %w", ErrQuotaExhausted)
	}}
	p := NewFailoverProvider([]FailoverTarget{
		{Provider: primary, ProviderName: "quota-test-only", Model: "quota-test-model-2"},
	})
	if _, err := p.Generate(context.Background(), GenerateRequest{Model: "quota-test-model-2", Prompt: "hello"}); err == nil {
		t.Fatal("expected an error when the only target is exhausted")
	}
	// Second call: the target is now marked exhausted and skipped outright —
	// must still return a clear error, not a nil-formatting artifact.
	_, err := p.Generate(context.Background(), GenerateRequest{Model: "quota-test-model-2", Prompt: "hello again"})
	if err == nil {
		t.Fatal("expected an error when all targets are exhausted")
	}
	if primary.generateCall != 1 {
		t.Fatalf("exhausted target should not be retried on the second call, got %d calls", primary.generateCall)
	}
}

type failoverFake struct {
	generate     func(GenerateRequest) (GenerateResponse, error)
	chat         func(ChatGenerateRequest) (ChatGenerateResponse, error)
	generateCall int
	chatCall     int
}

func (f *failoverFake) Generate(_ context.Context, req GenerateRequest) (GenerateResponse, error) {
	f.generateCall++
	return f.generate(req)
}

func (f *failoverFake) ChatGenerate(_ context.Context, req ChatGenerateRequest) (ChatGenerateResponse, error) {
	f.chatCall++
	return f.chat(req)
}

type textOnlyFake struct {
	response GenerateResponse
}

func (f *textOnlyFake) Generate(_ context.Context, _ GenerateRequest) (GenerateResponse, error) {
	return f.response, nil
}
func TestFailoverProviderRetriesThenUsesFallback(t *testing.T) {
	primary := &failoverFake{generate: func(GenerateRequest) (GenerateResponse, error) {
		return GenerateResponse{}, fmt.Errorf("503 unavailable")
	}}
	fallback := &failoverFake{generate: func(req GenerateRequest) (GenerateResponse, error) {
		return GenerateResponse{Text: "recovered", Model: req.Model}, nil
	}}

	p := NewFailoverProvider([]FailoverTarget{
		{Provider: primary, ProviderName: "primary", Model: "primary-model"},
		{Provider: fallback, ProviderName: "ollama", Model: "qwen3.5:9b"},
	})
	resp, err := p.Generate(context.Background(), GenerateRequest{Model: "primary-model", Prompt: "hello"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if primary.generateCall != 2 || fallback.generateCall != 1 {
		t.Fatalf("calls primary=%d fallback=%d, want 2 and 1", primary.generateCall, fallback.generateCall)
	}
	if resp.Model != "qwen3.5:9b" || !resp.Raw["used_fallback"].(bool) || !resp.Raw["local_fallback"].(bool) {
		t.Fatalf("unexpected fallback response: %+v", resp)
	}
}

func TestFailoverProviderBridgesTextToolRequestToChat(t *testing.T) {
	text := &textOnlyFake{response: GenerateResponse{Text: `I will create it. {"type":"tool_request","tool":"mcp_blender_create_mesh","input":{"name":"cube"}}`}}
	p := NewFailoverProvider([]FailoverTarget{{Provider: text, ProviderName: "codex", Model: "codex"}})

	resp, err := p.ChatGenerate(context.Background(), ChatGenerateRequest{
		Model:    "codex",
		Messages: []ChatMessage{{Role: "user", Content: "Make a cube"}},
		Tools:    []ChatTool{{Function: FunctionDef{Name: "mcp_blender_create_mesh", Description: "Create a mesh"}}},
	})
	if err != nil {
		t.Fatalf("ChatGenerate: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "mcp_blender_create_mesh" {
		t.Fatalf("expected bridged Blender tool call, got %+v", resp)
	}
	if resp.ToolCalls[0].Function.Arguments["name"] != "cube" {
		t.Fatalf("unexpected arguments: %+v", resp.ToolCalls[0].Function.Arguments)
	}
}

func TestFailoverProviderCompactsContextForNextTarget(t *testing.T) {
	primary := &failoverFake{chat: func(ChatGenerateRequest) (ChatGenerateResponse, error) {
		return ChatGenerateResponse{}, fmt.Errorf("context length exceeded")
	}}
	var fallbackMessages []ChatMessage
	fallback := &failoverFake{chat: func(req ChatGenerateRequest) (ChatGenerateResponse, error) {
		fallbackMessages = req.Messages
		return ChatGenerateResponse{Content: "recovered"}, nil
	}}
	p := NewFailoverProvider([]FailoverTarget{
		{Provider: primary, ProviderName: "primary", Model: "primary-model"},
		{Provider: fallback, ProviderName: "ollama", Model: "qwen3.5:9b"},
	})
	messages := []ChatMessage{{Role: "system", Content: "system"}}
	for i := 0; i < 10; i++ {
		messages = append(messages, ChatMessage{Role: "user", Content: fmt.Sprintf("message %d", i)})
	}
	resp, err := p.ChatGenerate(context.Background(), ChatGenerateRequest{Model: "primary-model", Messages: messages})
	if err != nil {
		t.Fatalf("ChatGenerate: %v", err)
	}
	if primary.chatCall != 1 || len(fallbackMessages) != 9 || fallbackMessages[0].Role != "system" {
		t.Fatalf("primary calls=%d fallback messages=%+v", primary.chatCall, fallbackMessages)
	}
	if !resp.Raw["context_compacted"].(bool) {
		t.Fatalf("expected context_compacted metadata: %+v", resp.Raw)
	}
}
