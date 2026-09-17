package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/emaharmony/prizm/internal/retry"
)

// exhaustedTargets tracks (provider, model) pairs that have hit an
// account-level usage quota (ErrQuotaExhausted), for the lifetime of this
// process. Package-level and shared across every FailoverProvider instance,
// since quota exhaustion is a fact about the (provider, model) pair itself,
// not about any one FailoverProvider object — instances may be constructed
// fresh per call in some code paths. Cleared only by a process restart
// ("the end of the session"); there is deliberately no time-based expiry,
// since a weekly quota won't reset on a schedule this process can predict.
var (
	exhaustedMu      sync.Mutex
	exhaustedTargets = map[string]bool{}
)

func targetKey(t FailoverTarget) string {
	return t.ProviderName + ":" + t.Model
}

func isTargetExhausted(t FailoverTarget) bool {
	exhaustedMu.Lock()
	defer exhaustedMu.Unlock()
	return exhaustedTargets[targetKey(t)]
}

func markTargetExhausted(t FailoverTarget) {
	exhaustedMu.Lock()
	defer exhaustedMu.Unlock()
	if !exhaustedTargets[targetKey(t)] {
		exhaustedTargets[targetKey(t)] = true
		log.Printf("provider/failover: %s/%s hit its usage quota — skipping it for the rest of this session", t.ProviderName, t.Model)
	}
}

// FailoverTarget identifies one concrete provider/model attempt.
type FailoverTarget struct {
	Provider     Provider
	ProviderName string
	Model        string
}

// FailoverProvider applies an ordered model chain to one agent. It supports
// both native chat providers and text-only providers such as Codex and Claude
// Code, translating their JSON tool requests into ChatGenerate responses.
type FailoverProvider struct {
	targets []FailoverTarget
}

func NewFailoverProvider(targets []FailoverTarget) *FailoverProvider {
	return &FailoverProvider{targets: append([]FailoverTarget(nil), targets...)}
}

func (p *FailoverProvider) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	var lastErr error
	for index, target := range p.targets {
		if isTargetExhausted(target) {
			continue
		}
		for attempt := 0; attempt < 2; attempt++ {
			if err := ctx.Err(); err != nil {
				return GenerateResponse{}, err
			}
			request := req
			request.Model = target.Model
			resp, err := target.Provider.Generate(ctx, request)
			if err == nil {
				return annotateGenerate(resp, target, index > 0), nil
			}
			lastErr = err
			if errors.Is(err, ErrQuotaExhausted) {
				markTargetExhausted(target)
				break
			}
			if !retry.IsRetryable(err) {
				log.Printf("provider/failover: %s/%s failed (%v) — not retryable, trying next target", target.ProviderName, target.Model, err)
				break
			}
			log.Printf("provider/failover: %s/%s attempt %d failed (%v), retrying", target.ProviderName, target.Model, attempt+1, err)
		}
	}
	if lastErr == nil {
		return GenerateResponse{}, fmt.Errorf("all model targets are exhausted (quota) or none configured")
	}
	return GenerateResponse{}, fmt.Errorf("all model targets failed: %w", lastErr)
}

func (p *FailoverProvider) ChatGenerate(ctx context.Context, req ChatGenerateRequest) (ChatGenerateResponse, error) {
	var lastErr error
	compacted := false
	for index, target := range p.targets {
		if isTargetExhausted(target) {
			continue
		}
		for attempt := 0; attempt < 2; attempt++ {
			if err := ctx.Err(); err != nil {
				return ChatGenerateResponse{}, err
			}
			request := req
			request.Model = target.Model
			if compacted {
				request = compactChatRequest(request)
			}
			resp, err := callChatTarget(ctx, target, request)
			if err == nil {
				return annotateChat(resp, target, index > 0, compacted), nil
			}
			lastErr = err
			if errors.Is(err, ErrQuotaExhausted) {
				markTargetExhausted(target)
				break
			}
			if isContextLimit(err) {
				compacted = true
			}
			if !retry.IsRetryable(err) {
				log.Printf("provider/failover: %s/%s failed (%v) — not retryable, trying next target", target.ProviderName, target.Model, err)
				break
			}
			log.Printf("provider/failover: %s/%s attempt %d failed (%v), retrying", target.ProviderName, target.Model, attempt+1, err)
		}
	}
	if lastErr == nil {
		return ChatGenerateResponse{}, fmt.Errorf("all model targets are exhausted (quota) or none configured")
	}
	return ChatGenerateResponse{}, fmt.Errorf("all model targets failed: %w", lastErr)
}

func callChatTarget(ctx context.Context, target FailoverTarget, req ChatGenerateRequest) (ChatGenerateResponse, error) {
	if chat, ok := target.Provider.(ChatProvider); ok {
		return chat.ChatGenerate(ctx, req)
	}
	resp, err := target.Provider.Generate(ctx, GenerateRequest{
		RunID:         req.RunID,
		CorrelationID: req.CorrelationID,
		Agent:         req.Agent,
		Model:         target.Model,
		Prompt:        flattenChatRequest(req),
		Temperature:   req.Temperature,
		MaxTokens:     req.MaxTokens,
	})
	if err != nil {
		return ChatGenerateResponse{}, err
	}
	return textToChatResponse(resp)
}

func flattenChatRequest(req ChatGenerateRequest) string {
	var b strings.Builder
	b.WriteString("Conversation:\n")
	for _, message := range req.Messages {
		fmt.Fprintf(&b, "[%s] %s\n", message.Role, message.Content)
	}
	if len(req.Tools) > 0 {
		b.WriteString("\nAvailable Prizm tools:\n")
		for _, tool := range req.Tools {
			schema, _ := json.Marshal(tool.Function.Parameters)
			fmt.Fprintf(&b, "- %s: %s Parameters: %s\n", tool.Function.Name, tool.Function.Description, schema)
		}
	}
	b.WriteString("\nRespond with JSON only: either {\"type\":\"final\",\"content\":\"...\"} or {\"type\":\"tool_request\",\"tool\":\"tool_name\",\"input\":{...}}. Request one tool at a time.\n")
	return b.String()
}

type textToolEnvelope struct {
	Type    string         `json:"type"`
	Content string         `json:"content"`
	Tool    string         `json:"tool"`
	Input   map[string]any `json:"input"`
}

func textToChatResponse(resp GenerateResponse) (ChatGenerateResponse, error) {
	envelope, ok := parseTextToolEnvelope(resp.Text)
	chat := ChatGenerateResponse{Model: resp.Model, Provider: resp.Provider, LatencyMS: resp.LatencyMS, PromptTokens: resp.PromptTokens, OutputTokens: resp.OutputTokens, Raw: resp.Raw}
	if !ok {
		chat.Content = resp.Text
		return chat, nil
	}
	if envelope.Type == "tool_request" && envelope.Tool != "" {
		chat.ToolCalls = []ToolCall{{ID: "text-tool-1", Function: FunctionCall{Name: envelope.Tool, Arguments: envelope.Input}}}
		return chat, nil
	}
	if envelope.Type == "final" {
		chat.Content = envelope.Content
		return chat, nil
	}
	chat.Content = resp.Text
	return chat, nil
}

func parseTextToolEnvelope(text string) (textToolEnvelope, bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(text), "```json"), "```"))
	var envelope textToolEnvelope
	if json.Unmarshal([]byte(trimmed), &envelope) == nil && envelope.Type != "" {
		return envelope, true
	}
	marker := strings.Index(text, `"type"`)
	if marker < 0 {
		return textToolEnvelope{}, false
	}
	start := strings.LastIndex(text[:marker], "{")
	if start < 0 {
		return textToolEnvelope{}, false
	}
	decoder := json.NewDecoder(bytes.NewBufferString(text[start:]))
	if decoder.Decode(&envelope) == nil && envelope.Type == "tool_request" && envelope.Tool != "" {
		return envelope, true
	}
	return textToolEnvelope{}, false
}
func compactChatRequest(req ChatGenerateRequest) ChatGenerateRequest {
	if len(req.Messages) <= 9 {
		return req
	}
	compact := make([]ChatMessage, 0, 9)
	for _, message := range req.Messages {
		if message.Role == "system" {
			compact = append(compact, message)
			break
		}
	}
	start := len(req.Messages) - 8
	compact = append(compact, req.Messages[start:]...)
	req.Messages = compact
	return req
}

func annotateGenerate(resp GenerateResponse, target FailoverTarget, fallback bool) GenerateResponse {
	resp.Model = target.Model
	resp.Provider = target.ProviderName
	resp.Raw = annotateRaw(resp.Raw, target, fallback, false)
	return resp
}

func annotateChat(resp ChatGenerateResponse, target FailoverTarget, fallback, compacted bool) ChatGenerateResponse {
	resp.Model = target.Model
	resp.Provider = target.ProviderName
	resp.Raw = annotateRaw(resp.Raw, target, fallback, compacted)
	return resp
}

func annotateRaw(raw map[string]any, target FailoverTarget, fallback, compacted bool) map[string]any {
	if raw == nil {
		raw = map[string]any{}
	}
	raw["selected_model"] = target.Model
	raw["selected_provider"] = target.ProviderName
	raw["used_fallback"] = fallback
	raw["context_compacted"] = compacted
	raw["local_fallback"] = fallback && target.ProviderName == "ollama" && !strings.Contains(target.Model, ":cloud")
	return raw
}

func isContextLimit(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "context length") || strings.Contains(message, "context window") || strings.Contains(message, "too many tokens") || strings.Contains(message, "maximum context")
}
