// Package toolloop provides a channel-agnostic tool execution loop.
//
// The core algorithm (call LLM → parse tool calls → execute tools → feed results back → repeat)
// is the same regardless of whether output goes to Discord, a terminal, or an API response.
// Callers implement the Sink interface for channel-specific behavior.
package toolloop

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/tool")

// ModelInfo reports which (provider, model) actually produced a response.
// FailoverProvider can silently fail over to a different target.
type ModelInfo struct {
	Model         string
	Provider      string
	UsedFallback  bool
	LocalFallback bool
}

// CallSummary records the outcome of a single tool call within the loop.
type CallSummary struct {
	Tool   string         `json:"tool"`
	Input  map[string]any `json:"input,omitempty"`
	Status string         `json:"status"` // "success", "error", "blocked_by_guard", "approval_needed"
	Result string         `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// Config controls the tool loop's iteration behavior.
type Config struct {
	// MaxIterations is the maximum number of LLM rounds before forcing a final answer.
	MaxIterations int

	// NudgeAfter is the iteration threshold after which a "wrap up now" system
	// message is injected. Set to 0 to disable nudging.
	NudgeAfter int

	// NudgeMessage is the system message injected to encourage the model to
	// provide a final answer. Empty uses a sensible default.
	NudgeMessage string

	// ForceFinalAfter is the iteration threshold after which tools are removed
	// from the request entirely, forcing the model to give a final answer.
	ForceFinalAfter int

	// Timeout is the per-loop context timeout. 0 means no timeout (use parent context).
	Timeout time.Duration

	// ExtraInputKeys are additional key-value pairs added to every tool call input
	// (e.g., _run_id, _channel_id).
	ExtraInputKeys map[string]string

	// RunID is the correlation ID for this tool loop invocation.
	RunID string
}

// DefaultConfig returns sensible defaults for a production tool loop.
func DefaultConfig() Config {
	return Config{
		MaxIterations:   100,
		NudgeAfter:      50,
		NudgeMessage:    "You have already used several tools. Please provide your final answer now based on the information you have gathered. Do not call any more tools.",
		ForceFinalAfter: 80,
		Timeout:         2 * time.Minute,
	}
}

// CLIConfig returns defaults suitable for interactive terminal chat
// (fewer iterations, earlier nudge, shorter timeout).
func CLIConfig() Config {
	return Config{
		MaxIterations:   10,
		NudgeAfter:      3,
		NudgeMessage:    "You have already used several tools. Please provide your final answer now based on the information you have gathered. Do not call any more tools.",
		ForceFinalAfter: 8,
		Timeout:         2 * time.Minute,
	}
}

// Sink is the channel-specific interface for tool loop output.
// Discord implements this by editing messages; CLI implements by printing to terminal;
// API invoke implements by storing results.
type Sink interface {
	// OnToolCall reports a tool call starting. Args may be nil.
	OnToolCall(name string, args map[string]any)

	// OnToolResult reports a tool call completing.
	OnToolResult(name string, result string, summary CallSummary)

	// OnProgress reports partial response content (e.g., Discord message edits).
	OnProgress(content string)

	// OnComplete delivers the final response.
	OnComplete(content string, modelInfo ModelInfo)

	// OnError reports a fatal error in the loop.
	OnError(err error)
}

// Hooks are optional callbacks for channel-specific behavior during the loop.
// The loop calls these at specific points; if a hook is nil, it's skipped.
type Hooks struct {
	// OnToolExecuted is called after each tool execution, before adding the result
	// to the message list. Use for review events, plan re-injection, etc.
	// Returns true if the message list should be modified (e.g., plan state reinjected).
	OnToolExecuted func(ctx context.Context, tc provider.ToolCall, summary CallSummary) []provider.ChatMessage

	// CheckToolAccess is called before executing a tool. Returns an error if the
	// tool should be blocked for this channel/context.
	CheckToolAccess func(toolName string) error

	// OnApprovalNeeded is called when a tool requires human approval.
	// Returns the approval result (approved/rejected) and an optional message.
	OnApprovalNeeded func(ctx context.Context, approvalID string) (approved bool, message string)
}

// ToolExecutor is the interface for executing tool calls within the loop.
// This abstracts away the specific tool execution implementation so the loop
// doesn't depend on cmd/prizm-cli internals.
type ToolExecutor interface {
	ExecuteWithPolicy(ctx context.Context, toolName, agentID, source, runID string, input map[string]any) (tool.ToolResult, error)
}

// ChatLoopResult holds the result of a ChatProvider tool loop.
type ChatLoopResult struct {
	Content   string
	Summaries []CallSummary
	ModelInfo ModelInfo
}

// RunChatLoop executes a multi-turn ChatProvider tool loop.
//
// It calls the LLM, processes any tool_calls in the response, executes tools
// via the ToolExecutor, feeds results back, and repeats until the model
// provides a final content response with no tool calls.
//
// The Sink receives channel-specific callbacks for display/output.
// The Config controls iteration limits, nudge thresholds, and timeouts.
func RunChatLoop(
	ctx context.Context,
	messages []provider.ChatMessage,
	chatTools []provider.ChatTool,
	chatProv provider.ChatProvider,
	agentCfg *orchestrator.AgentConfig,
	toolExec ToolExecutor,
	sink Sink,
	cfg Config,
	hooks *Hooks,
) (*ChatLoopResult, error) {
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	var summaries []CallSummary
	currentMessages := make([]provider.ChatMessage, len(messages))
	copy(currentMessages, messages)

	nudgeInjected := false
	var lastContent string
	var modelInfo ModelInfo

	for i := 0; i < cfg.MaxIterations; i++ {
		log.Printf("[TOOL-LOOP] iteration %d/%d", i+1, cfg.MaxIterations)

		// Nudge after threshold
		if cfg.NudgeAfter > 0 && i >= cfg.NudgeAfter && !nudgeInjected {
			msg := cfg.NudgeMessage
			if msg == "" {
				msg = "You have already used several tools. Please provide your final answer now based on the information you have gathered. Do not call any more tools."
			}
			currentMessages = append(currentMessages, provider.ChatMessage{
				Role:    "system",
				Content: msg,
			})
			nudgeInjected = true
		}

		// Force final: remove tools
		toolsForThisIteration := chatTools
		forceFinal := false
		if cfg.ForceFinalAfter > 0 && i >= cfg.ForceFinalAfter {
			toolsForThisIteration = []provider.ChatTool{}
			forceFinal = true
			log.Printf("[TOOL-LOOP] iteration %d: removing tools to force final answer", i+1)
		}

		// Call LLM
		req := provider.ChatGenerateRequest{
			RunID:    fmt.Sprintf("tool-loop-%d", i),
			Agent:    agentCfg.ID,
			Model:    agentCfg.Model,
			Messages: currentMessages,
			Tools:    toolsForThisIteration,
		}
		response, err := chatProv.ChatGenerate(ctx, req)
		if err != nil {
			sink.OnError(fmt.Errorf("LLM call failed iteration %d: %w", i+1, err))
			return nil, fmt.Errorf("LLM call failed iteration %d: %w", i+1, err)
		}

		// Track model info (failover detection)
		usedFallback, _ := response.Raw["used_fallback"].(bool)
		localFallback, _ := response.Raw["local_fallback"].(bool)
		modelInfo = ModelInfo{
			Model:         response.Model,
			Provider:      response.Provider,
			UsedFallback:  usedFallback,
			LocalFallback: localFallback,
		}

		if response.Content != "" {
			lastContent = response.Content
			sink.OnProgress(response.Content)
		}

		// No tool calls — final response
		if !response.HasToolCalls() {
			log.Printf("[TOOL-LOOP] iteration %d: final response (%d chars)", i+1, len(response.Content))
			sink.OnComplete(response.Content, modelInfo)
			return &ChatLoopResult{
				Content:   response.Content,
				Summaries: summaries,
				ModelInfo: modelInfo,
			}, nil
		}

		// forceFinal: model hallucinated tool calls after tools removed
		if forceFinal {
			if response.Content != "" {
				log.Printf("[TOOL-LOOP] iteration %d: forceFinal — using content (%d chars)", i+1, len(response.Content))
				sink.OnComplete(response.Content, modelInfo)
				return &ChatLoopResult{
					Content:   response.Content,
					Summaries: summaries,
					ModelInfo: modelInfo,
				}, nil
			}
			if lastContent != "" {
				log.Printf("[TOOL-LOOP] iteration %d: forceFinal — using lastContent (%d chars)", i+1, len(lastContent))
				sink.OnComplete(lastContent, modelInfo)
				return &ChatLoopResult{
					Content:   lastContent,
					Summaries: summaries,
					ModelInfo: modelInfo,
				}, nil
			}
			sink.OnComplete("I've gathered information but had trouble composing a final response. Please ask again.", modelInfo)
			return &ChatLoopResult{
				Content:   "I've gathered information but had trouble composing a final response. Please ask again.",
				Summaries: summaries,
				ModelInfo: modelInfo,
			}, nil
		}

		// Process tool calls
		log.Printf("[TOOL-LOOP] iteration %d: model requests %d tool calls", i+1, len(response.ToolCalls))

		currentMessages = append(currentMessages, provider.ChatMessage{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		})

		for _, tc := range response.ToolCalls {
			sink.OnToolCall(tc.Function.Name, tc.Function.Arguments)

			// Check channel tool access
			if hooks != nil && hooks.CheckToolAccess != nil {
				if err := hooks.CheckToolAccess(tc.Function.Name); err != nil {
					result := err.Error()
					summary := CallSummary{
						Tool:   tc.Function.Name,
						Input:  tc.Function.Arguments,
						Status: "error",
						Error:  err.Error(),
					}
					currentMessages = append(currentMessages, provider.ChatMessage{
						Role:    "tool",
						Content: result,
						ToolID:  tc.ID,
					})
					summaries = append(summaries, summary)
					sink.OnToolResult(tc.Function.Name, result, summary)
					continue
				}
			}

			result, summary := executeTool(ctx, tc, agentCfg, toolExec, cfg)

			currentMessages = append(currentMessages, provider.ChatMessage{
				Role:    "tool",
				Content: result,
				ToolID:  tc.ID,
			})
			summaries = append(summaries, summary)

			sink.OnToolResult(tc.Function.Name, result, summary)

			// Track successful results as fallback content
			if summary.Status == "success" || summary.Status == "approval_needed" {
				lastContent = summary.Result
			}

			// Call post-execution hook (review events, plan re-injection, etc.)
			if hooks != nil && hooks.OnToolExecuted != nil {
				extraMessages := hooks.OnToolExecuted(ctx, tc, summary)
				if len(extraMessages) > 0 {
					currentMessages = append(currentMessages, extraMessages...)
				}
			}
		}
	}

	// Max iterations reached — synthesize final answer
	log.Printf("[TOOL-LOOP] max iterations (%d) reached, synthesizing final answer", cfg.MaxIterations)
	return synthesizeFinalAnswer(ctx, currentMessages, summaries, lastContent, modelInfo, chatProv, agentCfg, sink)
}

// executeTool runs a single tool call and returns the result string and summary.
func executeTool(
	ctx context.Context,
	tc provider.ToolCall,
	agentCfg *orchestrator.AgentConfig,
	toolExec ToolExecutor,
	cfg Config,
) (string, CallSummary) {
	input := make(map[string]any, len(tc.Function.Arguments)+len(cfg.ExtraInputKeys))
	for k, v := range tc.Function.Arguments {
		input[k] = v
	}
	for k, v := range cfg.ExtraInputKeys {
		input[k] = v
	}

	result, err := toolExec.ExecuteWithPolicy(ctx, tc.Function.Name, agentCfg.ID, "prizm", cfg.RunID, input)

	if err != nil {
		return fmt.Sprintf("Error executing tool: %v", err), CallSummary{
			Tool:   tc.Function.Name,
			Input:  tc.Function.Arguments,
			Status: "error",
			Error:  err.Error(),
		}
	}

	if !result.Success {
		return fmt.Sprintf("Tool error: %s", result.Error), CallSummary{
			Tool:   tc.Function.Name,
			Input:  tc.Function.Arguments,
			Status: "error",
			Error:  result.Error,
		}
	}

	// Handle approval-needed
	if decision, _ := result.Output["policy_decision"].(string); decision == string(tool.PolicyRequiresApproval) {
		status := "approval_needed"
		resultStr := formatToolOutput(result.Output)
		return resultStr, CallSummary{
			Tool:   tc.Function.Name,
			Input:  tc.Function.Arguments,
			Status: status,
			Result: truncateStr(resultStr, 200),
		}
	}

	resultStr := formatToolOutput(result.Output)
	return resultStr, CallSummary{
		Tool:   tc.Function.Name,
		Input:  tc.Function.Arguments,
		Status: "success",
		Result: truncateStr(resultStr, 200),
	}
}

// synthesizeFinalAnswer makes one final LLM call with no tools, asking the model
// to synthesize what it found from tool results.
func synthesizeFinalAnswer(
	ctx context.Context,
	currentMessages []provider.ChatMessage,
	summaries []CallSummary,
	lastContent string,
	modelInfo ModelInfo,
	chatProv provider.ChatProvider,
	agentCfg *orchestrator.AgentConfig,
	sink Sink,
) (*ChatLoopResult, error) {
	var summaryTexts []string
	for _, s := range summaries {
		if s.Status == "success" || s.Status == "approval_needed" {
			summaryTexts = append(summaryTexts, fmt.Sprintf("- %s: %s", s.Tool, s.Result))
		} else {
			summaryTexts = append(summaryTexts, fmt.Sprintf("- %s: (error: %s)", s.Tool, s.Error))
		}
	}

	if len(summaryTexts) == 0 {
		err := fmt.Errorf("tool loop exceeded max iterations with no successful tool results")
		sink.OnError(err)
		return nil, err
	}

	synthesisPrompt := fmt.Sprintf("You have gathered the following information using tools but reached the iteration limit. "+
		"Please provide a comprehensive answer based on this data:\n\n%s", strings.Join(summaryTexts, "\n"))

	synthesisMessages := append(currentMessages, provider.ChatMessage{
		Role:    "system",
		Content: synthesisPrompt,
	})

	synthesisResp, err := chatProv.ChatGenerate(ctx, provider.ChatGenerateRequest{
		RunID:    "tool-loop-synthesis",
		Agent:    agentCfg.ID,
		Model:    agentCfg.Model,
		Messages: synthesisMessages,
		Tools:    []provider.ChatTool{},
	})
	if err != nil {
		log.Printf("[TOOL-LOOP] synthesis call failed: %v", err)
		if lastContent != "" {
			sink.OnComplete(lastContent, modelInfo)
			return &ChatLoopResult{Content: lastContent, Summaries: summaries, ModelInfo: modelInfo}, nil
		}
		sink.OnError(err)
		return nil, err
	}

	usedFallback, _ := synthesisResp.Raw["used_fallback"].(bool)
	localFallback, _ := synthesisResp.Raw["local_fallback"].(bool)
	modelInfo = ModelInfo{
		Model:         synthesisResp.Model,
		Provider:      synthesisResp.Provider,
		UsedFallback:  usedFallback,
		LocalFallback: localFallback,
	}

	if synthesisResp.Content != "" {
		log.Printf("[TOOL-LOOP] synthesis produced %d chars", len(synthesisResp.Content))
		sink.OnComplete(synthesisResp.Content, modelInfo)
		return &ChatLoopResult{Content: synthesisResp.Content, Summaries: summaries, ModelInfo: modelInfo}, nil
	}

	if lastContent != "" {
		sink.OnComplete(lastContent, modelInfo)
		return &ChatLoopResult{Content: lastContent, Summaries: summaries, ModelInfo: modelInfo}, nil
	}

	err = fmt.Errorf("tool loop exceeded max iterations and synthesis produced no content")
	sink.OnError(err)
	return nil, err
}

// formatToolOutput extracts the most useful content from a tool result's output map.
func formatToolOutput(output map[string]any) string {
	// Priority: content > output > result > text > message > body
	contentKeys := []string{"content", "output", "result", "text", "message", "body"}
	for _, key := range contentKeys {
		if v, exists := output[key]; exists {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
			if b, err := json.Marshal(v); err == nil {
				return string(b)
			}
		}
	}
	// Fallback: JSON-marshal everything
	resultBytes, err := json.Marshal(output)
	if err != nil {
		return fmt.Sprintf("%v", output)
	}
	return string(resultBytes)
}

// truncateStr truncates a string to maxLen characters, appending "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}