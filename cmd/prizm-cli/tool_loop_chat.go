package main

import (
	stdctx "context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/guard"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/plan"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/session"
	"github.com/emaharmony/prizm/internal/tool"
)

const maxChatToolIterations = 100 // V70: raised from 10 to allow full research+execution cycles
const chatToolLoopTimeout = 2 * time.Minute
const approvalWaitTimeout = 60 * time.Second // V74: how long to block for interactive tool approval

// chatModelInfo reports which (provider, model) actually produced a tool-chat
// response. FailoverProvider can silently fail over to a different target
// than the one the agent is configured with (e.g. a quota-exhausted cloud
// model dropping to a local last-resort model); this is how the caller finds
// out, since the request's nominal agentCfg.Model never changes.
type chatModelInfo struct {
	Model         string
	Provider      string
	UsedFallback  bool
	LocalFallback bool
}

// runToolLoopChat handles native tool calling via ChatProvider.
// The LLM returns structured tool_calls, which are executed and fed back
// as tool role messages. No text parsing needed.
func (cc *conversationContext) runToolLoopChat(
	parentCtx stdctx.Context,
	messages []provider.ChatMessage,
	chatTools []provider.ChatTool,
	agentCfg *orchestrator.AgentConfig,
	channelID string,
	placeholderMsgID string,
	runID string,
) (string, []toolCallSummary, chatModelInfo, error) {
	ctx := parentCtx

	var summaries []toolCallSummary
	currentMessages := make([]provider.ChatMessage, len(messages))
	copy(currentMessages, messages)

	nudgeInjected := false // only inject the wrap-up nudge once
	var lastContent string // track last content-bearing response for fallback
	var modelInfo chatModelInfo

	for i := 0; i < maxChatToolIterations; i++ {
		log.Printf("[TOOL-CHAT] iteration %d/%d", i+1, maxChatToolIterations)

		// After 50 iterations, inject a nudge message telling the model to wrap up.
		// This prevents infinite tool-calling loops where the model keeps requesting
		// tools without ever producing a final content response.
		// Only inject ONCE to avoid token waste and model confusion.
		if i >= 50 && !nudgeInjected {
			nudgeMsg := "You have already used several tools. Please provide your final answer now based on the information you have gathered. Do not call any more tools."
			currentMessages = append(currentMessages, provider.ChatMessage{
				Role:    "system",
				Content: nudgeMsg,
			})
			nudgeInjected = true
		}

		// After 80 iterations, remove tools from the request entirely.
		// The model MUST give a final answer now.
		toolsForThisIteration := chatTools
		forceFinal := false
		if i >= 80 {
			toolsForThisIteration = []provider.ChatTool{} // explicit empty slice: no tools allowed
			forceFinal = true
			log.Printf("[TOOL-CHAT] iteration %d: removing tools from request to force final answer", i+1)
		}

		// Call the LLM with current messages
		response, err := cc.callChatLLM(ctx, currentMessages, toolsForThisIteration, agentCfg)
		if err != nil {
			return "", summaries, modelInfo, fmt.Errorf("LLM call failed in chat tool loop iteration %d: %w", i+1, err)
		}

		// Record which (provider, model) actually answered — FailoverProvider
		// may have silently substituted a fallback target for the one this
		// agent is configured with.
		usedFallback, _ := response.Raw["used_fallback"].(bool)
		localFallback, _ := response.Raw["local_fallback"].(bool)
		modelInfo = chatModelInfo{
			Model:         response.Model,
			Provider:      response.Provider,
			UsedFallback:  usedFallback,
			LocalFallback: localFallback,
		}

		// Track content-bearing responses for fallback
		if response.Content != "" {
			lastContent = response.Content
		}

		// No tool calls — this is the final response
		if !response.HasToolCalls() {
			log.Printf("[TOOL-CHAT] iteration %d: final response (%d chars)", i+1, len(response.Content))
			return response.Content, summaries, modelInfo, nil
		}

		// If tools were removed (forceFinal) but model still generated tool_calls,
		// treat the content as the final answer. Ignore hallucinated tool calls.
		if forceFinal {
			if response.Content != "" {
				log.Printf("[TOOL-CHAT] iteration %d: forceFinal — model still requested tools, using content (%d chars)", i+1, len(response.Content))
				return response.Content, summaries, modelInfo, nil
			}
			// No content and no valid tool calls — use last known content
			if lastContent != "" {
				log.Printf("[TOOL-CHAT] iteration %d: forceFinal — no content, using lastContent (%d chars)", i+1, len(lastContent))
				return lastContent, summaries, modelInfo, nil
			}
			log.Printf("[TOOL-CHAT] iteration %d: forceFinal — no content and no lastContent, returning fallback", i+1)
			return "I've gathered information but had trouble composing a final response. Please ask again.", summaries, modelInfo, nil
		}

		// Process tool calls
		log.Printf("[TOOL-CHAT] iteration %d: model requests %d tool calls", i+1, len(response.ToolCalls))

		// Add the assistant's message (with tool_calls) to the conversation
		currentMessages = append(currentMessages, provider.ChatMessage{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		})

		// Execute each tool call and feed results back
		for _, tc := range response.ToolCalls {
			toolResult, summary := cc.executeChatTool(ctx, tc, agentCfg, channelID, runID)

			// Build the tool result message
			currentMessages = append(currentMessages, provider.ChatMessage{
				Role:    "tool",
				Content: toolResult,
				ToolID:  tc.ID,
			})

			summaries = append(summaries, summary)

			// V76: Fire review event for file-mutating tools.
			if summary.Status == "success" {
				cc.publishReviewEvent(tc.Function.Name, tc.Function.Arguments, agentCfg.ID)
			}

			// Track successful tool results as potential fallback content.
			if summary.Status == "success" || summary.Status == "approval_needed" {
				lastContent = summary.Result
			}
		}

		// V73: Re-inject plan state after plan-changing tool calls
		for _, tc := range response.ToolCalls {
			switch tc.Function.Name {
			case "plan_create", "plan_update", "plan_reopen", "plan_abandon":
				if cc.planMgr != nil {
					if plans, err := cc.planMgr.LoadPlans(); err == nil {
						activePlan := plan.ActivePlan(plans)
						if activePlan != nil {
							planMsg := plan.FormatPlanForPrompt(activePlan)
							currentMessages = append(currentMessages, provider.ChatMessage{
								Role:    "system",
								Content: "Plan state updated:\n" + planMsg,
							})
							log.Printf("[TOOL-CHAT] iteration %d: re-injected plan %s after %s", i+1, activePlan.ID, tc.Function.Name)
						}
					}
				}
				break // only inject once per iteration
			}
		}
	}

	// Max iterations reached — synthesize a final answer from gathered data.
	// Instead of returning raw tool results, make one final LLM call
	// with no tools available, asking the model to synthesize what it found.
	log.Printf("[TOOL-CHAT] max iterations (%d) reached, synthesizing final answer", maxChatToolIterations)

	// Build a synthesis prompt from the tool summaries
	var summaryTexts []string
	for _, s := range summaries {
		if s.Status == "success" || s.Status == "approval_needed" {
			summaryTexts = append(summaryTexts, fmt.Sprintf("- %s: %s", s.Tool, s.Result))
		} else {
			summaryTexts = append(summaryTexts, fmt.Sprintf("- %s: (error: %s)", s.Tool, s.Error))
		}
	}

	if len(summaryTexts) == 0 {
		return "", summaries, modelInfo, fmt.Errorf("chat tool loop exceeded max iterations (%d) with no successful tool results", maxChatToolIterations)
	}

	synthesisPrompt := fmt.Sprintf("You have gathered the following information using tools but reached the iteration limit. "+
		"Please provide a comprehensive answer based on this data:\n\n%s", strings.Join(summaryTexts, "\n"))

	// Make a final LLM call with no tools
	synthesisMessages := append(currentMessages, provider.ChatMessage{
		Role:    "system",
		Content: synthesisPrompt,
	})

	synthesisResp, err := cc.callChatLLM(ctx, synthesisMessages, []provider.ChatTool{}, agentCfg)
	if err != nil {
		log.Printf("[TOOL-CHAT] synthesis call failed: %v", err)
		// Fall back to lastContent if synthesis fails
		if lastContent != "" {
			return lastContent, summaries, modelInfo, nil
		}
		return "", summaries, modelInfo, fmt.Errorf("chat tool loop exceeded max iterations (%d) and synthesis failed", maxChatToolIterations)
	}

	usedFallback, _ := synthesisResp.Raw["used_fallback"].(bool)
	localFallback, _ := synthesisResp.Raw["local_fallback"].(bool)
	modelInfo = chatModelInfo{
		Model:         synthesisResp.Model,
		Provider:      synthesisResp.Provider,
		UsedFallback:  usedFallback,
		LocalFallback: localFallback,
	}

	if synthesisResp.Content != "" {
		log.Printf("[TOOL-CHAT] synthesis produced %d chars", len(synthesisResp.Content))
		return synthesisResp.Content, summaries, modelInfo, nil
	}

	// Synthesis produced no content — use lastContent
	if lastContent != "" {
		return lastContent, summaries, modelInfo, nil
	}
	return "", summaries, modelInfo, fmt.Errorf("chat tool loop exceeded max iterations (%d)", maxChatToolIterations)
}

// callChatLLM makes a single LLM call in the chat tool loop context.
func (cc *conversationContext) callChatLLM(
	ctx stdctx.Context,
	messages []provider.ChatMessage,
	chatTools []provider.ChatTool,
	agentCfg *orchestrator.AgentConfig,
) (provider.ChatGenerateResponse, error) {
	chatProv, err := cc.providers.GetChatProviderForAgent(agentCfg.ID, agentCfg.Model)
	if err != nil {
		return provider.ChatGenerateResponse{}, fmt.Errorf("get chat provider: %w", err)
	}

	req := provider.ChatGenerateRequest{
		RunID:       "chat-tool-loop",
		Agent:       agentCfg.ID,
		Model:       agentCfg.Model,
		Messages:    messages,
		Tools:       chatTools,
		Temperature: 0.7,
		MaxTokens:   4096,
	}

	return chatProv.ChatGenerate(ctx, req)
}

// executeChatTool executes a single tool call from the chat-based tool loop.
func (cc *conversationContext) executeChatTool(
	ctx stdctx.Context,
	tc provider.ToolCall,
	agentCfg *orchestrator.AgentConfig,
	channelID string,
	runID string,
) (string, toolCallSummary) {
	if err := cc.checkChannelToolAccess(tc.Function.Name, channelID); err != nil {
		return err.Error(), toolCallSummary{
			Tool:   tc.Function.Name,
			Input:  tc.Function.Arguments,
			Status: "error",
			Error:  err.Error(),
		}
	}

	// V32: Guard rail — check if plan exists for code mutations
	if cc.guardian != nil {
		guardResult := cc.guardian.CheckToolExecution(tc.Function.Name, tc.Function.Arguments)
		if guardResult.Decision == guard.Block {
			return guardResult.Reason, toolCallSummary{
				Tool:   tc.Function.Name,
				Input:  tc.Function.Arguments,
				Status: "blocked_by_guard",
				Error:  guardResult.Reason,
			}
		}
	}

	input := make(map[string]any, len(tc.Function.Arguments)+1)
	for k, v := range tc.Function.Arguments {
		input[k] = v
	}
	if runID != "" {
		input["_run_id"] = runID
	}
	if channelID != "" {
		input["_channel_id"] = channelID
	}

	result, execErr := cc.toolExec.ExecuteWithPolicy(ctx, tc.Function.Name, agentCfg.ID, "prizm", runID, input)

	status := "success"
	resultStr := ""
	if execErr != nil {
		status = "error"
		resultStr = execErr.Error()
	} else {
		// Convert ToolResult.Output to string
		// IMPORTANT: Check for known content keys first to avoid
		// Go's random map iteration picking "path" or "size" before "content".
		if result.Success {
			if decision, _ := result.Output["policy_decision"].(string); decision == string(tool.PolicyRequiresApproval) {
				// V74: Interactive tool approval — block for human response
				approvalID, ok := result.Output["approval_id"].(string)
				if ok && approvalID != "" {
					// Register a waiter for the Discord button handler to signal
					ch := cc.registerApprovalWaiter(runID, approvalID)

					// Block with 60s timeout
					select {
					case outcome := <-ch:
						if outcome.Approved {
							return outcome.Message, toolCallSummary{
								Tool:   tc.Function.Name,
								Input:  tc.Function.Arguments,
								Status: "success",
								Result: truncateStr(outcome.Message, 200),
							}
						}
						return outcome.Message, toolCallSummary{
							Tool:   tc.Function.Name,
							Input:  tc.Function.Arguments,
							Status: "error",
							Result: truncateStr(outcome.Message, 200),
						}
					case <-time.After(approvalWaitTimeout):
						return "approval timed out (60s) — tool not executed", toolCallSummary{
							Tool:   tc.Function.Name,
							Input:  tc.Function.Arguments,
							Status: "error",
							Result: "approval timed out (60s)",
						}
					}
				}
				// Fallback: no approval store or empty ID (CLI mode, no Discord bot)
				status = "approval_needed"
				resultStr = formatApprovalOutput(result.Output)
			}
			// Priority: content > output > result > text > message > body
			if resultStr == "" {
				contentKeys := []string{"content", "output", "result", "text", "message", "body"}
				for _, key := range contentKeys {
					if v, exists := result.Output[key]; exists {
						if s, ok := v.(string); ok && s != "" {
							resultStr = s
							break
						}
						// Also handle non-string content by JSON-encoding
						if resultStr == "" {
							if b, jsonErr := json.Marshal(v); jsonErr == nil {
								resultStr = string(b)
								break
							}
						}
					}
				}
			}
			// Fallback: if no known content key, JSON-marshal the whole output
			if resultStr == "" {
				resultBytes, jsonErr := json.Marshal(result.Output)
				if jsonErr != nil {
					resultStr = fmt.Sprintf("%v", result.Output)
				} else {
					resultStr = string(resultBytes)
				}
			}
		} else {
			status = "error"
			resultStr = result.Error
		}
	}

	summary := toolCallSummary{
		Tool:   tc.Function.Name,
		Input:  tc.Function.Arguments,
		Status: status,
		Result: truncateStr(resultStr, 200),
	}
	if execErr != nil {
		summary.Error = execErr.Error()
	}

	return resultStr, summary
}

func formatApprovalOutput(output map[string]any) string {
	approvalID, _ := output["approval_id"].(string)
	runID, _ := output["run_id"].(string)
	targetPath, _ := output["target_path"].(string)
	instruction, _ := output["instruction"].(string)
	parts := []string{"This action requires human approval."}
	if approvalID != "" {
		parts = append(parts, "Approval ID: "+approvalID)
	}
	if runID != "" {
		parts = append(parts, "Run ID: "+runID)
	}
	if targetPath != "" {
		parts = append(parts, "Target: "+targetPath)
	}
	if instruction != "" {
		parts = append(parts, instruction)
	}
	return strings.Join(parts, "\n")
}

// buildMessages constructs a ChatMessage array from session history and workspace context.
// This replaces buildPrompt for ChatProvider — the conversation is structured messages
// instead of a flat string.
func (cc *conversationContext) buildMessages(sess *session.Session, agentCfg *orchestrator.AgentConfig) []provider.ChatMessage {
	var messages []provider.ChatMessage

	// Static system content (cached, built once at startup)
	var systemContent string
	systemContent += cc.staticSystemChat

	// Dynamic session awareness
	sessionAge := time.Since(sess.StartedAt).Round(time.Second)
	sessionMsgCount := len(sess.Messages)
	systemContent += fmt.Sprintf("\n[Session: %d messages, started %v ago]\n", sessionMsgCount, sessionAge)

	// --- Working state injection ---
	if cc.stateMgr != nil {
		statePrompt := cc.stateMgr.FormatStateForPrompt()
		if statePrompt != "" {
			systemContent += "\n" + statePrompt + "\n"
		}
	}

	// --- Active plan injection ---
	if cc.planMgr != nil {
		if plans, err := cc.planMgr.LoadPlans(); err == nil {
			activePlan := plan.ActivePlan(plans)
			if activePlan != nil {
				systemContent += "\n" + plan.FormatPlanForPrompt(activePlan) + "\n"
				// V73: Add explicit plan execution instructions
				completed, total := plan.StepProgress(activePlan)
				if completed < total {
					systemContent += "\nYou have an active plan with " + fmt.Sprintf("%d/%d", completed, total) + " steps completed. " +
						"Execute the next pending step now using plan_update to mark it in_progress, " +
						"then use the appropriate tools to complete it. " +
						"After completing a step, mark it completed and proceed to the next. " +
						"Do not ask for confirmation — proceed autonomously.\n"
				}
			}
		}
	}

	messages = append(messages, provider.ChatMessage{
		Role:    "system",
		Content: systemContent,
	})

	// --- Conversation history ---
	for _, msg := range sess.Messages {
		switch msg.Role {
		case "user":
			messages = append(messages, provider.ChatMessage{
				Role:    "user",
				Content: msg.Content,
			})
		case "agent":
			messages = append(messages, provider.ChatMessage{
				Role:    "assistant",
				Content: msg.Content,
			})
		case "system":
			messages = append(messages, provider.ChatMessage{
				Role:    "system",
				Content: msg.Content,
			})
		}
	}

	return messages
}

// buildChatTools converts the tool registry into ChatTool format for ChatProvider.
func (cc *conversationContext) buildChatTools() []provider.ChatTool {
	toolInfos := cc.toolExec.Registry.ListWithDescriptions()
	chatTools := make([]provider.ChatTool, 0, len(toolInfos))

	for _, ti := range toolInfos {
		params := map[string]any{
			"type":       "object",
			"properties": make(map[string]any),
		}
		required := make([]string, 0)

		for pname, spec := range ti.Schema.Input {
			props := map[string]any{
				"type":        spec.Type,
				"description": spec.Description,
			}
			params["properties"].(map[string]any)[pname] = props
			if spec.Required {
				required = append(required, pname)
			}
		}

		if len(required) > 0 {
			params["required"] = required
		}

		chatTools = append(chatTools, provider.ChatTool{
			Type: "function",
			Function: provider.FunctionDef{
				Name:        ti.Name,
				Description: ti.Description,
				Parameters:  params,
			},
		})
	}

	return chatTools
}
