package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/plan"
	"github.com/emaharmony/prizm/internal/provider"
)

// doomDetector tracks recent tool calls to detect infinite loops.
type doomDetector struct {
	calls []doomCall
	limit int // number of identical calls before triggering doom
}

type doomCall struct {
	toolName string
	argsJSON string
}

// checkDoom returns true if the last `limit` tool calls are identical
// (same tool name + same arguments), indicating a doom loop.
func (d *doomDetector) checkDoom(toolName string, args map[string]any) bool {
	argsJSON, _ := json.Marshal(args)
	call := doomCall{toolName: toolName, argsJSON: string(argsJSON)}

	d.calls = append(d.calls, call)
	if len(d.calls) > d.limit {
		d.calls = d.calls[1:]
	}

	// All recent calls must be identical
	if len(d.calls) < d.limit {
		return false
	}
	for _, c := range d.calls {
		if c.toolName != call.toolName || c.argsJSON != call.argsJSON {
			return false
		}
	}
	return true
}

// runToolLoopAgentic executes a multi-turn tool loop with doom detection
// and no hard iteration cap. The loop continues until:
//   - The model produces a final text response (no tool calls)
//   - Doom loop detected (same tool + same args repeated)
//   - Context cancelled
//   - Token budget exceeded (optional)
//
// Safety mechanisms:
//   - Doom detection: if the last N tool calls are identical, pause and ask the model to explain
//   - Nudge: after 50 iterations, tell the model to wrap up
//   - Hard limit: 200 iterations maximum (safety valve)
//   - Post-compaction guard: if the model repeats a call after context compression, abort
func (cc *conversationContext) runToolLoopAgentic(
	parentCtx context.Context,
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

	nudgeInjected := false
	var lastContent string
	var modelInfo chatModelInfo
	doom := doomDetector{limit: 3} // 3 identical calls = doom loop
	hardLimit := 200               // absolute safety valve
	iterationCount := 0

	// V73: Context budget management
	contextTokens := contextTokensForAgentModel(agentCfg.Model)
	ctxBudget := defaultContextBudget(contextTokens)
	ctxBudget.compressThreshold = 0.50 // Compress at 50% to leave room for LLM response + tool results
	ctxBudget.warnThreshold = 0.40     // Warn at 40%

	// V73: Plan step nudge tracking
	lastPlanUpdateIteration := 0
	planNudged := false

	// V75: No-write detector — tracks whether the agent has produced any write
	// output in recent iterations. If stuck in a read-loop, inject a nudge.
	noWriteNudgeInjected := false

	for {
		iterationCount++
		if iterationCount > hardLimit {
			log.Printf("[AGENTIC-LOOP] hard limit (%d) reached, synthesizing final answer", hardLimit)
			return cc.synthesizeFinalAnswer(ctx, currentMessages, summaries, modelInfo, agentCfg)
		}

		log.Printf("[AGENTIC-LOOP] iteration %d", iterationCount)

		// V73: Compress context before LLM call to prevent overflow
		ctxBudget.checkAndCompress(currentMessages, iterationCount)

		// V73: Plan step nudge — if we have an active plan but haven't updated
		// any steps in 5+ iterations, remind the model to execute
		if cc.planMgr != nil && !planNudged && iterationCount-lastPlanUpdateIteration >= 5 && iterationCount > 3 {
			if plans, err := cc.planMgr.LoadPlans(); err == nil {
				activePlan := plan.ActivePlan(plans)
				if activePlan != nil {
					completed, total := plan.StepProgress(activePlan)
					if completed < total {
						nudge := fmt.Sprintf("Reminder: You have an active plan (%s) with %d/%d steps completed. Use plan_update to mark the current step, then execute it.", activePlan.ID, completed, total)
						currentMessages = append(currentMessages, provider.ChatMessage{
							Role:    "system",
							Content: nudge,
						})
						planNudged = true
						log.Printf("[AGENTIC-LOOP] iteration %d: plan step nudge for %s (%d/%d)", iterationCount, activePlan.ID, completed, total)
					}
				}
			}
		}

		// V75: No-write detector — if the agent has made 10+ iterations without
		// any write/mutation tool call, inject a nudge to produce output.
		// This catches models that read extensively but never write.
		if iterationCount >= 10 && !noWriteNudgeInjected {
			hasWrittenRecently := false
			lookbackStart := iterationCount - 10
			if lookbackStart < 0 {
				lookbackStart = 0
			}
			for idx := lookbackStart; idx < len(summaries); idx++ {
				if isWriteTool(summaries[idx].Tool) {
					hasWrittenRecently = true
					break
				}
			}
			if !hasWrittenRecently {
				currentMessages = append(currentMessages, provider.ChatMessage{
					Role:    "system",
					Content: "You have been researching for " + fmt.Sprintf("%d", iterationCount) + " iterations without producing output. Your task requires a deliverable. Stop researching and write the file or produce the result now.",
				})
				noWriteNudgeInjected = true
				log.Printf("[AGENTIC-LOOP] iteration %d: no-write nudge injected (no write tools in last %d iterations)", iterationCount, iterationCount-lookbackStart)
			}
		}

		// Nudge after 50 iterations
		if iterationCount >= 50 && !nudgeInjected {
			nudgeMsg := "You have already used many tool calls. Please provide your final answer now based on the information you have gathered. Do not call any more tools unless absolutely necessary."
			currentMessages = append(currentMessages, provider.ChatMessage{
				Role:    "system",
				Content: nudgeMsg,
			})
			nudgeInjected = true
		}

		// Strip tools after 80 iterations (force final answer)
		toolsForThisIteration := chatTools
		forceFinal := false
		if iterationCount >= 80 {
			toolsForThisIteration = []provider.ChatTool{}
			forceFinal = true
			log.Printf("[AGENTIC-LOOP] iteration %d: removing tools to force final answer", iterationCount)
		}

		// Call the LLM
		response, err := cc.callChatLLM(ctx, currentMessages, toolsForThisIteration, agentCfg)
		if err != nil {
			return "", summaries, modelInfo, fmt.Errorf("LLM call failed in agentic loop iteration %d: %w", iterationCount, err)
		}

		// Track model info
		usedFallback, _ := response.Raw["used_fallback"].(bool)
		localFallback, _ := response.Raw["local_fallback"].(bool)
		modelInfo = chatModelInfo{
			Model:         response.Model,
			Provider:      response.Provider,
			UsedFallback:  usedFallback,
			LocalFallback: localFallback,
		}

		// Track content for fallback
		if response.Content != "" {
			lastContent = response.Content
		}

		// No tool calls — final response
		if !response.HasToolCalls() {
			// Empty response — model choked, nudge and retry
			if len(response.Content) == 0 && iterationCount < 80 {
				log.Printf("[AGENTIC-LOOP] iteration %d: empty response, nudging model to continue", iterationCount)
				currentMessages = append(currentMessages, provider.ChatMessage{
					Role:    "system",
					Content: "You returned an empty response. This is not acceptable. Either use a tool to continue your task, or provide a substantive text response summarizing what you've done and what you recommend next.",
				})
				continue
			}
			log.Printf("[AGENTIC-LOOP] iteration %d: final response (%d chars)", iterationCount, len(response.Content))
			return response.Content, summaries, modelInfo, nil
		}

		// Force final — ignore hallucinated tool calls
		if forceFinal {
			if response.Content != "" {
				log.Printf("[AGENTIC-LOOP] iteration %d: forceFinal — using content (%d chars)", iterationCount, len(response.Content))
				return response.Content, summaries, modelInfo, nil
			}
			if lastContent != "" {
				log.Printf("[AGENTIC-LOOP] iteration %d: forceFinal — using lastContent (%d chars)", iterationCount, len(lastContent))
				return lastContent, summaries, modelInfo, nil
			}
			return "I've gathered information but had trouble composing a final response. Please ask again.", summaries, modelInfo, nil
		}

		// Process tool calls
		log.Printf("[AGENTIC-LOOP] iteration %d: model requests %d tool calls", iterationCount, len(response.ToolCalls))

		// Add assistant message with tool_calls
		currentMessages = append(currentMessages, provider.ChatMessage{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		})

		for _, tc := range response.ToolCalls {
			// Doom detection
			if doom.checkDoom(tc.Function.Name, tc.Function.Arguments) {
				log.Printf("[AGENTIC-LOOP] DOOM LOOP DETECTED: tool %q called %d times with identical args", tc.Function.Name, doom.limit)
				// Inject a system message asking the model to break out
				currentMessages = append(currentMessages, provider.ChatMessage{
					Role:    "system",
					Content: fmt.Sprintf("You have called %q with the same arguments %d times in a row. This suggests you are stuck in a loop. Stop calling this tool and provide a final answer based on what you already know.", tc.Function.Name, doom.limit),
				})
				// Reset doom detector so we give the model one more chance
				doom.calls = nil
				// Continue the loop — don't abort, let the model self-correct
				break
			}

			toolResult, summary := cc.executeChatTool(ctx, tc, agentCfg, channelID, runID)

			// V73: Cap tool result size to prevent context bloat
			maxToolResultSize := 15000 // 15KB hard cap per tool result
			if len(toolResult) > maxToolResultSize {
				originalSize := len(toolResult)
				toolResult = toolResult[:maxToolResultSize] + fmt.Sprintf("\n\n[... %d more bytes omitted. Use read_file with max_lines to get specific sections.]", originalSize-maxToolResultSize)
				log.Printf("[AGENTIC-LOOP] iteration %d: truncated tool result from %d to %d bytes", iterationCount, originalSize, maxToolResultSize)
			}

			currentMessages = append(currentMessages, provider.ChatMessage{
				Role:    "tool",
				Content: toolResult,
				ToolID:  tc.ID,
			})

			summaries = append(summaries, summary)

			// V77: Publish review event for file-mutating tools
			if summary.Status == "success" {
				cc.publishReviewEvent(tc.Function.Name, tc.Function.Arguments, agentCfg.ID)
			}

			if summary.Status == "success" || summary.Status == "approval_needed" {
				lastContent = fmt.Sprintf("Based on the information I gathered: %s", summary.Result)
			}
		}

		// V73: Re-inject plan state after plan-changing tool calls
		planChanged := false
		for _, tc := range response.ToolCalls {
			switch tc.Function.Name {
			case "plan_create", "plan_update", "plan_reopen", "plan_abandon":
				planChanged = true
				lastPlanUpdateIteration = iterationCount
				planNudged = false // Reset nudge so it can fire again if needed
			}
		}
		if planChanged && cc.planMgr != nil {
			if plans, err := cc.planMgr.LoadPlans(); err == nil {
				activePlan := plan.ActivePlan(plans)
				if activePlan != nil {
					planMsg := plan.FormatPlanForPrompt(activePlan)
					currentMessages = append(currentMessages, provider.ChatMessage{
						Role:    "system",
						Content: "Plan state updated:\n" + planMsg,
					})
					log.Printf("[AGENTIC-LOOP] iteration %d: re-injected plan %s after tool call", iterationCount, activePlan.ID)

					// V73: Plan completion detection
					completed, total := plan.StepProgress(activePlan)
					if completed > 0 && completed == total {
						currentMessages = append(currentMessages, provider.ChatMessage{
							Role:    "system",
							Content: fmt.Sprintf("All %d steps of plan %s are completed. Provide your final summary of what was accomplished.", total, activePlan.ID),
						})
						log.Printf("[AGENTIC-LOOP] iteration %d: plan %s completed (%d/%d steps)", iterationCount, activePlan.ID, completed, total)
					}
				}
			}
		}

		// V72: Check context budget and compress if needed
		ctxBudget.checkAndCompress(currentMessages, iterationCount)
	}
}

// isWriteTool returns true if the tool name is a write/mutation tool that produces output.
// Used by the no-write detector to distinguish read-only loops from productive ones.
func isWriteTool(name string) bool {
	writeTools := map[string]bool{
		"write_file":                true,
		"write_file_proposal":       true,
		"write_file_dry_run":        true,
		"create_directory":          true,
		"create_directory_proposal": true,
		"plan_create":               true,
		"plan_update":               true,
		"plan_reopen":               true,
		"record_decision":           true,
		"update_context":            true,
		"add_blocked":               true,
	}
	return writeTools[name]
}

// synthesizeFinalAnswer makes a final LLM call with no tools to synthesize
// a coherent answer from the tool call summaries gathered so far.
func (cc *conversationContext) synthesizeFinalAnswer(
	ctx context.Context,
	currentMessages []provider.ChatMessage,
	summaries []toolCallSummary,
	modelInfo chatModelInfo,
	agentCfg *orchestrator.AgentConfig,
) (string, []toolCallSummary, chatModelInfo, error) {
	var summaryTexts []string
	for _, s := range summaries {
		if s.Status == "success" || s.Status == "approval_needed" {
			summaryTexts = append(summaryTexts, fmt.Sprintf("- %s: %s", s.Tool, s.Result))
		} else {
			summaryTexts = append(summaryTexts, fmt.Sprintf("- %s: (error: %s)", s.Tool, s.Error))
		}
	}

	if len(summaryTexts) == 0 {
		return "", summaries, modelInfo, fmt.Errorf("agentic loop reached hard limit with no successful tool results")
	}

	synthesisPrompt := fmt.Sprintf("You have gathered the following information using tools but reached the iteration limit. "+
		"Please provide a comprehensive answer based on this data:\n\n%s", strings.Join(summaryTexts, "\n"))

	synthesisMessages := append(currentMessages, provider.ChatMessage{
		Role:    "system",
		Content: synthesisPrompt,
	})

	synthesisResp, err := cc.callChatLLM(ctx, synthesisMessages, []provider.ChatTool{}, agentCfg)
	if err != nil {
		log.Printf("[AGENTIC-LOOP] synthesis call failed: %v", err)
		return "", summaries, modelInfo, fmt.Errorf("agentic loop reached hard limit and synthesis failed: %w", err)
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
		return synthesisResp.Content, summaries, modelInfo, nil
	}

	return "", summaries, modelInfo, fmt.Errorf("agentic loop reached hard limit and synthesis produced no content")
}
