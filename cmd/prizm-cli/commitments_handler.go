package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/emaharmony/prizm/internal/commitments"
	"github.com/emaharmony/prizm/internal/provider"
)

// extractCommitments runs a background model call to extract follow-up
// commitments from the conversation. This is fire-and-forget — if it fails,
// the conversation is unaffected. Extracted commitments are persisted to
// the commitments store for later delivery.
//
// The extraction uses a lightweight model call with a focused prompt. It's
// called after the response is sent so it doesn't add latency to the
// conversation.
func (cc *conversationContext) extractCommitments(userText, assistantText, agentID, sessionKey, channel, senderID string) {
	if cc.commitStore == nil {
		return
	}

	agentCfg := cc.findAgentConfig(agentID)
	if agentCfg == nil {
		return
	}

	// Get a provider for the agent's model
	chatProv, err := cc.providers.GetChatProviderForAgent(agentCfg.ID, agentCfg.Model)
	if err != nil {
		log.Printf("[COMMITMENTS] no provider for extraction: %v", err)
		return
	}

	// Build extraction prompt
	timezone := "America/New_York" // default; could be derived from user config
	prompt := commitments.ExtractionPrompt(userText, assistantText, timezone)

	// Timeout for extraction — don't let it run forever
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Call the model
	resp, err := chatProv.ChatGenerate(ctx, provider.ChatGenerateRequest{
		RunID: fmt.Sprintf("commit-extract-%d", time.Now().UnixNano()),
		Agent: agentCfg.ID,
		Model: agentCfg.Model,
		Messages: []provider.ChatMessage{
			{Role: "system", Content: "You are a commitment extraction assistant. Extract follow-up commitments from the conversation. Return only JSON."},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.3,
		MaxTokens:   1024,
	})
	if err != nil {
		log.Printf("[COMMITMENTS] extraction model call failed: %v", err)
		return
	}

	if resp.Content == "" {
		return
	}

	// Parse the response
	items, err := commitments.ParseExtraction(resp.Content)
	if err != nil {
		log.Printf("[COMMITMENTS] parse failed: %v", err)
		return
	}

	if len(items) == 0 {
		return // no commitments found — normal
	}

	// Convert to records and persist
	scope := commitments.CommitmentScope{
		AgentID:    agentID,
		SessionKey: sessionKey,
		Channel:    channel,
		SenderID:   senderID,
	}

	now := time.Now()
	saved := 0
	for _, item := range items {
		if item.Confidence < 0.5 {
			continue // skip low-confidence extractions
		}

		record, err := commitments.ItemToRecord(item, scope, now)
		if err != nil {
			continue
		}

		// Check dedupe
		has, _ := cc.commitStore.HasDedupe(record.DedupeKey)
		if has {
			continue // already tracked
		}

		if err := cc.commitStore.Upsert(record); err != nil {
			log.Printf("[COMMITMENTS] upsert failed: %v", err)
			continue
		}
		saved++
	}

	if saved > 0 {
		log.Printf("[COMMITMENTS] extracted %d commitment(s) from conversation", saved)
	}
}

// deliverCommitments checks for due commitments and returns a prompt
// section to inject into the system prompt. Called before each response.
func (cc *conversationContext) deliverCommitments(agentID, sessionKey, channel string) string {
	if cc.commitStore == nil {
		return ""
	}

	scope := commitments.CommitmentScope{
		AgentID:    agentID,
		SessionKey: sessionKey,
		Channel:    channel,
	}

	cfg := commitments.DefaultDeliveryConfig()
	prompt, err := commitments.Deliver(cc.commitStore, scope, cfg, time.Now())
	if err != nil {
		log.Printf("[COMMITMENTS] delivery failed: %v", err)
		return ""
	}

	return prompt
}