// Package main: Mango review subscriber with feedback injection.
//
// When a file-mutating tool succeeds, the tool loop publishes prizm.review.requested.
// This subscriber delegates the review to the mango agent, then listens for
// mango.task.completed events and sends the review result back to the Discord
// channel where the original work happened.
//
// V77: This closes the feedback loop — Mango reviews, Lumi sees the result.
package main

import (
	ctxcontext "context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/delegation"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/nats-io/nats.go"
)

// mangoReviewer listens for review requests and delegates them to the mango agent.
type mangoReviewer struct {
	nc          *nats.Conn
	deleg       *delegation.Engine
	cfg         *orchestrator.Config
	bot         *discordbot.BotAdapter
	reviewStore *reviewResultStore // V77: stores review results for prompt injection
	reviewCh    chan reviewRequest
	subs        []*nats.Subscription // V78: NATS subscriptions for graceful teardown
	done        chan struct{}         // V78: signals processReviews to stop
	wg          sync.WaitGroup       // V78: tracks in-flight reviews
}

type reviewRequest struct {
	AgentID       string   `json:"agent_id"`
	FilesChanged  []string `json:"files_changed"`
	TaskDesc      string   `json:"task_description"`
	ToolName      string   `json:"tool_name,omitempty"`
	SessionID     string   `json:"session_id,omitempty"`
	CorrelationID string   `json:"correlation_id,omitempty"`
	ChannelID     string   `json:"channel_id,omitempty"`
}

// startMangoReviewer subscribes to review events and delegates to mango.
func startMangoReviewer(nc *nats.Conn, deleg *delegation.Engine, cfg *orchestrator.Config, bot *discordbot.BotAdapter, store *reviewResultStore) (*mangoReviewer, error) {
	if nc == nil {
		return nil, fmt.Errorf("mango reviewer requires NATS connection")
	}
	if deleg == nil {
		return nil, fmt.Errorf("mango reviewer requires delegation engine")
	}

	mangoConfigured := false
	for _, a := range cfg.Agents {
		if a.ID == "mango" {
			mangoConfigured = true
			break
		}
	}
	if !mangoConfigured {
		log.Printf("[MANGO-REVIEW] mango agent not configured, reviewer disabled")
		return nil, nil
	}

	mr := &mangoReviewer{
		nc:          nc,
		deleg:       deleg,
		cfg:         cfg,
		bot:         bot,
		reviewStore: store,
		reviewCh:    make(chan reviewRequest, 16),
		done:        make(chan struct{}),
	}

	// Subscribe to review request events
	sub, err := nc.Subscribe("prizm.review.requested", mr.handleMsg)
	if err != nil {
		return nil, fmt.Errorf("subscribe review.requested: %w", err)
	}
	mr.subs = append(mr.subs, sub)

	// Subscribe to mango task completion events for feedback injection
	taskSub, err := nc.Subscribe("mango.task.completed", mr.handleTaskCompleted)
	if err != nil {
		log.Printf("[MANGO-REVIEW] WARN: could not subscribe to mango.task.completed: %v", err)
	} else {
		log.Printf("[MANGO-REVIEW] listening for mango.task.completed (feedback injection)")
		mr.subs = append(mr.subs, taskSub)
	}

	// Subscribe to review completed events for audit logging
	reviewSub, err := nc.Subscribe("prizm.review.completed", func(msg *nats.Msg) {
		log.Printf("[MANGO-REVIEW] review completed event: %s", string(msg.Data))
	})
	if err != nil {
		log.Printf("[MANGO-REVIEW] WARN: could not subscribe to prizm.review.completed: %v", err)
	} else {
		mr.subs = append(mr.subs, reviewSub)
	}

	go mr.processReviews()

	log.Printf("[MANGO-REVIEW] watching prizm.review.requested (delegating to mango)")
	return mr, nil
}

func (mr *mangoReviewer) handleMsg(msg *nats.Msg) {
	var req reviewRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		log.Printf("[MANGO-REVIEW] bad review.requested event: %v", err)
		return
	}

	select {
	case mr.reviewCh <- req:
	default:
		log.Printf("[MANGO-REVIEW] review channel full, dropping request for %s", req.TaskDesc)
	}
}

// handleTaskCompleted listens for mango.task.completed events and sends
// the review result back to the Discord channel where the work happened.
func (mr *mangoReviewer) handleTaskCompleted(msg *nats.Msg) {
	var payload map[string]any
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		log.Printf("[MANGO-REVIEW] bad mango.task.completed event: %v", err)
		return
	}

	taskID, _ := payload["task_id"].(string)
	status, _ := payload["status"].(string)
	result, _ := payload["result"].(string)

	// Only process review tasks — require review_type == "post_mutation" explicitly
	// so that unrelated mango completions are never misrouted as review feedback.
	contextData, _ := payload["context_data"].(map[string]any)
	if contextData == nil {
		log.Printf("[MANGO-REVIEW] skipping task %s — no context_data", taskID)
		return
	}
	if reviewType, ok := contextData["review_type"].(string); !ok || reviewType != "post_mutation" {
		log.Printf("[MANGO-REVIEW] skipping non-review task %s (review_type: %v)", taskID, contextData["review_type"])
		return
	}

	log.Printf("[MANGO-REVIEW] mango review task %s completed (status: %s)", taskID, status)

	// If we have a result and a bot, format it and send to Discord
	if result != "" && mr.bot != nil {
		// Try to find the channel ID from task context data
		channelID := ""
		if contextData, ok := payload["context_data"].(map[string]any); ok {
			if ch, ok := contextData["channel_id"].(string); ok {
				channelID = ch
			}
		}

		// Fall back to first configured channel
		if channelID == "" {
			for _, ch := range mr.cfg.Channels {
				if len(ch.Channels) > 0 {
					channelID = ch.Channels[0]
					break
				}
			}
		}

		if channelID != "" {
			feedback := formatReviewFeedback(taskID, status, result)
			if err := mr.bot.Send(&discordbot.OutboundMessage{
				ChannelID: channelID,
				Content:   feedback,
			}); err != nil {
				log.Printf("[MANGO-REVIEW] failed to send feedback to Discord: %v", err)
			} else {
				log.Printf("[MANGO-REVIEW] sent review feedback to channel %s", channelID)
			}

			// V77: Store review result for prompt injection
			if mr.reviewStore != nil {
				mr.reviewStore.Add(channelID, ReviewResult{
					TaskID:      taskID,
					Reviewer:    "mango",
					Decision:    extractDecision(result),
					FilesChanged: extractFilesChanged(payload),
					ChannelID:   channelID,
				})
				log.Printf("[MANGO-REVIEW] stored review result for prompt injection (channel: %s)", channelID)
			}
		}
	}
}

// extractDecision attempts to parse the review decision from a Mango result string.
func extractDecision(result string) string {
	result = strings.TrimSpace(result)
	if strings.Contains(result, `"fail"`) {
		return "fail"
	}
	if strings.Contains(result, `"pass"`) {
		return "pass"
	}
	return "unknown"
}

// extractFilesChanged extracts file paths from the task context data.
func extractFilesChanged(payload map[string]any) []string {
	if contextData, ok := payload["context_data"].(map[string]any); ok {
		if files, ok := contextData["files_changed"].([]interface{}); ok {
			result := make([]string, 0, len(files))
			for _, f := range files {
				if s, ok := f.(string); ok {
					result = append(result, s)
				}
			}
			return result
		}
	}
	return nil
}

func formatReviewFeedback(taskID, status, result string) string {
	var sb strings.Builder
	sb.WriteString("📋 **Mango Review** (task: `")
	if len(taskID) > 8 {
		if len(taskID) > 8 {
			sb.WriteString(taskID[:8])
		} else {
			sb.WriteString(taskID)
		}
	} else {
		sb.WriteString(taskID)
	}
	sb.WriteString("`)\n")
	sb.WriteString("Status: ")
	if status == "completed" {
		sb.WriteString("✅ ")
	} else {
		sb.WriteString("⚠️ ")
	}
	sb.WriteString(status)
	sb.WriteString("\n")

	// Try to extract decision from result
	result = strings.TrimSpace(result)
	if strings.Contains(result, `"decision"`) || strings.Contains(result, `"pass"`) || strings.Contains(result, `"fail"`) {
		lines := strings.Split(result, "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.Contains(trimmed, "decision") || strings.Contains(trimmed, "issues") || strings.Contains(trimmed, "suggestions") {
				sb.WriteString(trimmed)
				sb.WriteString("\n")
			}
		}
	} else {
		if len(result) > 500 {
			sb.WriteString(result[:500])
			sb.WriteString("...")
		} else {
			sb.WriteString(result)
		}
	}

	return sb.String()
}

// Close gracefully shuts down the mango reviewer: unsubscribes from NATS,
// signals the review processor to stop, and waits for in-flight reviews to complete.
func (mr *mangoReviewer) Close() {
	// Unsubscribe from all NATS subscriptions
	for _, sub := range mr.subs {
		if err := sub.Unsubscribe(); err != nil {
			log.Printf("[MANGO-REVIEW] unsubscribe error: %v", err)
		}
	}

	// Signal processReviews to stop accepting new work
	close(mr.done)

	// Wait for in-flight reviews to finish (up to 10 seconds)
	done := make(chan struct{})
	go func() {
		mr.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.Printf("[MANGO-REVIEW] all in-flight reviews completed")
	case <-time.After(10 * time.Second):
		log.Printf("[MANGO-REVIEW] timeout waiting for in-flight reviews")
	}

	log.Printf("[MANGO-REVIEW] shutdown complete")
}

func (mr *mangoReviewer) processReviews() {
	for req := range mr.reviewCh {
		mr.wg.Add(1)
		mr.review(req)
		mr.wg.Done()
	}
}

func (mr *mangoReviewer) review(req reviewRequest) {
	ctx, cancel := ctxcontext.WithTimeout(ctxcontext.Background(), 5*time.Minute)
	defer cancel()

	filesStr := strings.Join(req.FilesChanged, ", ")
	if filesStr == "" {
		filesStr = "unknown files"
	}

	reviewPrompt := fmt.Sprintf(
		"Review the following change:\n\n"+
			"Tool: %s\n"+
			"Files changed: %s\n"+
			"Context: %s\n\n"+
			"Check for: correctness, safety, regressions, and whether the change accomplishes its stated purpose.\n"+
			"Report findings as structured JSON: {\"decision\": \"pass\"|\"fail\", \"issues\": [...], \"suggestions\": [...]}",
		req.ToolName, filesStr, req.TaskDesc,
	)

	contextData := map[string]any{
		"files_changed":  req.FilesChanged,
		"tool_name":      req.ToolName,
		"session_id":     req.SessionID,
		"correlation_id": req.CorrelationID,
		"review_type":    "post_mutation",
		"channel_id":     req.ChannelID,
	}

	task, err := mr.deleg.Delegate(ctx, "lumi", "mango", "review_code", reviewPrompt, contextData)
	if err != nil {
		log.Printf("[MANGO-REVIEW] failed to delegate review task: %v", err)
		mr.publishReviewCompleted(req, "delegation_failed", err.Error())
		return
	}

	log.Printf("[MANGO-REVIEW] delegated review task %s to mango (files: %s)", task.ID, filesStr)
	mr.publishReviewCompleted(req, "delegated", fmt.Sprintf("task_id: %s", task.ID))
}

func (mr *mangoReviewer) publishReviewCompleted(req reviewRequest, status, detail string) {
	payload := map[string]any{
		"reviewer":      "mango",
		"status":        status,
		"detail":        detail,
		"files_changed": req.FilesChanged,
		"agent_id":      req.AgentID,
		"channel_id":    req.ChannelID,
		"v":             1,
	}
	data, _ := json.Marshal(payload)
	if err := mr.nc.Publish("prizm.review.completed", data); err != nil {
		log.Printf("[MANGO-REVIEW] failed to publish review.completed: %v", err)
	}
}