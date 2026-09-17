package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	ctxcontext "context"
	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/remembrance"
)

// handleAgentMessage processes an incoming message from another bot agent.
// Agent messages bypass injection defense and rate limiting (trusted peers).
//
// Key difference from handleDiscordMessage: NO placeholder messages.
// Placeholders ("✧ ...") are visible to other bots and trigger false responses.
// Instead, we use only the typing indicator, then send the complete response.
//
//lint:ignore U1000 retained for peer-agent routing integration
func (cc *conversationContext) handleAgentMessage(msg *discordbot.InboundMessage) {
	// Frame the message as coming from a peer agent
	framedContent := fmt.Sprintf("[Message from agent %s]: %s", msg.UserName, msg.Content)

	// P-009: Agent loop detection — don't respond to error messages
	// from other agents that would trigger more error responses.
	content := strings.ToLower(strings.TrimSpace(msg.Content))
	errorPatterns := []string{
		"something went wrong",
		"i tried to use a tool",
		"had trouble processing",
		"ai service returned an error",
	}
	for _, pattern := range errorPatterns {
		if strings.Contains(content, pattern) {
			log.Printf("[AGENT-LOOP] suppressing response to error message from %s (matched %q)", msg.UserName, pattern)
			return
		}
	}

	// Emit agent-to-agent event
	cc.publishEvent("prizm.agent.message_received", map[string]any{
		"from_agent": msg.UserName,
		"from_id":    msg.UserID,
		"channel_id": msg.ChannelID,
	})

	log.Printf("[AGENT] processing agent message from %s (%s) in channel %s", msg.UserName, msg.UserID, msg.ChannelID)

	// Route the message
	result := cc.router.Route(framedContent)

	externalOwner := "agent:" + msg.UserID
	sess, ownerID, err := getOrCreateSessionForMessage(cc.sessMgr, cc.cfg, result.AgentID, "discord", msg.ChannelID, externalOwner)
	if err != nil {
		log.Printf("[ERROR] load agent session: %v", err)
		return
	}

	// Add the framed agent message to the session
	_, err = cc.sessMgr.AddMessage(sess.ID, "user", framedContent, "")
	if err != nil {
		log.Printf("[ERROR] add agent message: %v", err)
		return
	}

	// Look up agent config and provider
	agentCfg := cc.findAgentConfig(result.AgentID)
	if agentCfg == nil {
		log.Printf("[ERROR] no config for agent %s", result.AgentID)
		return
	}

	// Create run context with longer timeout for agent-to-agent
	runCtx, runCancel := ctxcontext.WithTimeout(ctxcontext.Background(), 120*time.Second)
	defer runCancel()

	// Typing indicator only — NO placeholder message
	if err := cc.bot.Typing(msg.ChannelID); err != nil {
		log.Printf("[WARN] typing indicator failed: %v", err)
	}

	// Build the full prompt — agent messages always use "agent" state action
	promptSession := sess
	prompt := cc.buildPrompt(promptSession, agentCfg, "agent", nil) // Agent messages have no channel context

	// Inject Remembrance context
	if cc.remClient != nil {
		cacheKey := fmt.Sprintf("%s:%s", agentCfg.ID, sess.ID)
		remCtx := cc.remCache.Get(cacheKey)
		if remCtx == nil {
			var remCtxErr error
			remCtx, remCtxErr = cc.remClient.BuildContextWithOptions(remembrance.BuildContextRequest{
				Task:               framedContent,
				ProjectID:          "prizm",
				AgentID:            agentCfg.ID,
				OwnerID:            ownerID,
				LocalRecentSummary: localRecentSummary(sess),
				ChannelContext:     "agent",
				MaxTokens:          remembrance.DefaultContextMaxTokens,
			})
			if remCtxErr != nil {
				log.Printf("[REMEMBRANCE] context build failed: %v", remCtxErr)
			} else if remCtx != nil {
				cc.remCache.Set(cacheKey, remCtx)
			}
		}
		if remCtx != nil {
			if memoryBlock := remembranceMemoryBlock(remCtx); memoryBlock != "" {
				promptSession = cloneSessionWithSystemMemory(sess, memoryBlock)
				prompt = cc.buildPrompt(promptSession, agentCfg, "agent", nil)
				log.Printf("[REMEMBRANCE] injected %d memory sources into agent prompt (markdown)", len(remCtx.SelectedMemories))
			}
		}
	}

	// Append tool instructions
	if cc.toolExec != nil {
		toolInfos := cc.toolExec.Registry.ListWithDescriptions()
		if len(toolInfos) > 0 {
			prompt += agent.BuildToolPromptSuffix(toolInfos, cc.ctxBuilder.WorkspaceRoot, cc.toolPolicy.ReadAllowedPaths()...)
		}
	}

	// Execute LLM with tool loop support
	var response string

	if cc.toolExec != nil {
		// Check if the provider supports native tool calling (ChatProvider)
		_, chatErr := cc.providers.GetChatProviderForAgent(agentCfg.ID, agentCfg.Model)
		supportsChat := chatErr == nil

		if supportsChat {
			// Native tool calling path
			messages := cc.buildMessages(promptSession, agentCfg)
			chatTools := cc.buildChatTools()
			log.Printf("[AGENT-TOOL-CHAT] entering native tool loop with %d tools", len(chatTools))

			finalResponse, toolSummaries, modelInfo, toolErr := cc.runToolLoopChat(
				runCtx,
				messages,
				chatTools,
				agentCfg,
				msg.ChannelID,
				"", // NO placeholder — critical for agent-to-agent
				"",
			)
			if toolErr != nil {
				log.Printf("[AGENT-TOOL-CHAT] tool loop failed: %v", toolErr)
				finalResponse = "I had trouble with that request — the AI service returned an error. Please try again in a moment."
			}
			if modelInfo.UsedFallback && modelInfo.Model != "" {
				log.Printf("[AGENT-TOOL-CHAT] response answered by fallback target %s/%s (configured model was %s/%s, local_fallback=%v)",
					modelInfo.Provider, modelInfo.Model, agentCfg.Provider, agentCfg.Model, modelInfo.LocalFallback)
			}
			response = finalResponse

			for _, ts := range toolSummaries {
				log.Printf("[AGENT-TOOL-CHAT] %s: %s", ts.Tool, ts.Status)
				toolMsg := fmt.Sprintf("[Tool: %s] %s", ts.Tool, ts.Status)
				if ts.Error != "" {
					toolMsg += " — " + ts.Error
				}
				cc.sessMgr.AddMessage(sess.ID, "tool", toolMsg, ts.Tool)
			}
		} else {
			// Text-based tool calling path — first do a direct LLM call, then check for tool intent
			llmProvider, provErr := cc.providers.GetForAgent(agentCfg.ID, agentCfg.Model)
			if provErr != nil {
				log.Printf("[ERROR] no provider for model %s: %v", agentCfg.Model, provErr)
				return
			}
			resp, genErr := llmProvider.Generate(runCtx, provider.GenerateRequest{
				Prompt: prompt,
				Model:  agentCfg.Model,
				Agent:  agentCfg.ID,
				RunID:  sess.ID,
			})
			if genErr != nil {
				log.Printf("[ERROR] agent Generate failed: %v", genErr)
				return
			}
			response = resp.Text

			// Check if LLM requested a tool
			parsed := agent.ParseAgentOutputWithFallback(response)
			if parsed.Type == agent.ResponseToolRequest {
				log.Printf("[AGENT-TOOL] LLM requested tool %q", parsed.ToolName)
				finalResponse, toolSummaries, toolErr := cc.runToolLoop(
					runCtx,
					prompt,
					agentCfg,
					msg.ChannelID,
					"", // NO placeholder
					"",
				)
				if toolErr != nil {
					log.Printf("[AGENT-TOOL] tool loop failed: %v", toolErr)
				} else if finalResponse != "" {
					response = finalResponse
				}
				for _, ts := range toolSummaries {
					toolMsg := fmt.Sprintf("[Tool: %s] %s", ts.Tool, ts.Status)
					if ts.Error != "" {
						toolMsg += " — " + ts.Error
					}
					cc.sessMgr.AddMessage(sess.ID, "tool", toolMsg, ts.Tool)
				}
			}
		}
	} else {
		// No tools — direct LLM call
		llmProvider, provErr := cc.providers.GetForAgent(agentCfg.ID, agentCfg.Model)
		if provErr != nil {
			log.Printf("[ERROR] no provider for model %s: %v", agentCfg.Model, provErr)
			return
		}

		// Try ChatProvider first
		if chatProv, isChat := llmProvider.(provider.ChatProvider); isChat {
			messages := cc.buildMessages(promptSession, agentCfg)
			chatResp, chatErr := chatProv.ChatGenerate(runCtx, provider.ChatGenerateRequest{
				Messages: messages,
				Model:    agentCfg.Model,
				Agent:    agentCfg.ID,
				RunID:    sess.ID,
			})
			if chatErr != nil {
				log.Printf("[ERROR] agent ChatGenerate failed: %v", chatErr)
				return
			}
			response = chatResp.Content
		} else {
			resp, genErr := llmProvider.Generate(runCtx, provider.GenerateRequest{
				Prompt: prompt,
				Model:  agentCfg.Model,
				Agent:  agentCfg.ID,
				RunID:  sess.ID,
			})
			if genErr != nil {
				log.Printf("[ERROR] agent Generate failed: %v", genErr)
				return
			}
			response = resp.Text
		}
	}

	if response == "" {
		log.Printf("[WARN] agent produced empty response")
		return
	}

	// Save assistant response to session
	cc.sessMgr.AddMessage(sess.ID, "agent", response, agentCfg.ID)

	// Send the response — split if needed, NO placeholder editing
	chunks := discordbot.SplitMessage(response, discordbot.MessageLimit)
	for _, chunk := range chunks {
		if err := cc.bot.Send(&discordbot.OutboundMessage{ChannelID: msg.ChannelID, Content: chunk}); err != nil {
			log.Printf("[ERROR] failed to send agent response chunk: %v", err)
		}
	}

	// Publish agent response event
	cc.publishEvent("prizm.agent.message_sent", map[string]any{
		"from_agent": agentCfg.ID,
		"to_channel": msg.ChannelID,
	})

	log.Printf("[AGENT] sent response to agent %s in channel %s (%d chars)", msg.UserName, msg.ChannelID, len(response))

	enqueueLocalMemoryUpdate(cc.sessMgr, cc.cfg, cc.remClient, cc.remSem, cc.remCache, ownerID, externalOwner, agentCfg.ID, sess.ID, "")
}

// findPrimaryAgent returns the ID of the primary agent, or the first agent if none is primary.
//
//lint:ignore U1000 retained for multi-agent routing integration
func (cc *conversationContext) findPrimaryAgent() string {
	for i := range cc.cfg.Agents {
		if cc.cfg.Agents[i].Primary {
			return cc.cfg.Agents[i].ID
		}
	}
	if len(cc.cfg.Agents) > 0 {
		return cc.cfg.Agents[0].ID
	}
	return "prizm1"
}

// isAgentBot checks if a Discord user ID is in any agent's listen_to_agents list.
func isAgentBot(agents []orchestrator.AgentConfig, userID string) bool {
	for i := range agents {
		for _, botID := range agents[i].ListenToAgents {
			if botID == userID {
				return true
			}
		}
	}
	return false
}

// handlePlanApproval processes "approve P-XXX" or "reject P-XXX" commands from Discord.
// Returns true if the message was handled (was a plan approval/rejection command).
//
// Security: Only users in the configured admin channel (manager-room) can approve/reject plans.
// The channel role is resolved from config, not from the message content.
func (cc *conversationContext) handlePlanApproval(msg *discordbot.InboundMessage) bool {
	trimmed := strings.TrimSpace(msg.Content)
	parts := strings.Fields(trimmed)
	if len(parts) < 2 {
		return false
	}

	action := strings.ToLower(parts[0])
	planID := parts[1]

	// Validate plan ID format
	if !strings.HasPrefix(planID, "P-") {
		return false // Not a plan command
	}

	// Authorization: plan approval is only allowed in the manager-room channel.
	// This prevents random users in other channels from approving/rejecting plans.
	channelRole := cc.cfg.ResolveChannelRole(msg.ChannelID)
	if channelRole != "manager-room" {
		log.Printf("[PLAN] WARN: plan approval attempted in non-manager channel %q (role=%q) by %s", msg.ChannelID, channelRole, msg.UserName)
		return false
	}

	var resultMsg string
	switch action {
	case "approve":
		if err := cc.planMgr.ApprovePlan(planID, msg.UserName); err != nil {
			log.Printf("[PLAN] ERROR: failed to approve plan %s: %v", planID, err)
			resultMsg = fmt.Sprintf("\u274c Could not approve plan %s: %v", planID, err)
		} else {
			log.Printf("[PLAN] approved %s by %s", planID, msg.UserName)
			resultMsg = fmt.Sprintf("\u2705 Plan %s approved by %s. Proceeding with execution.", planID, msg.UserName)
		}
	case "reject":
		if err := cc.planMgr.AbandonPlan(planID); err != nil {
			log.Printf("[PLAN] ERROR: failed to reject plan %s: %v", planID, err)
			resultMsg = fmt.Sprintf("\u274c Could not reject plan %s: %v", planID, err)
		} else {
			log.Printf("[PLAN] rejected %s by %s", planID, msg.UserName)
			resultMsg = fmt.Sprintf("\u274c Plan %s rejected by %s. Will not proceed.", planID, msg.UserName)
		}
	default:
		return false // Not an approval command
	}

	if cc.bot != nil {
		cc.bot.Send(&discordbot.OutboundMessage{
			ChannelID: msg.ChannelID,
			Content:   resultMsg,
		})
	}
	return true
}
