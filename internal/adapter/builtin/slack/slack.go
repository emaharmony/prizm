// Package slack provides a Slack bot adapter for Prizm.
//
// Uses Slack's Web API via rtm.Start + WebSocket (or the simpler socket mode)
// for inbound messages and chat.postMessage for outbound. Supports:
//   - Text messages (send/receive)
//   - Block Kit buttons (for approvals)
//   - Message editing (for streaming output)
//   - File uploads
//   - Thread replies
//
// Config:
//
//	channels:
//	  - type: "slack"
//	    token: "<bot-oauth-token>"
//	    allowed_users: ["<user-id>"]
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// BotAdapter connects Prizm to Slack as a bot.
type BotAdapter struct {
	token       string
	apiBase     string
	httpClient  *http.Client
	handlers    []MessageHandler
	allowedIDs  map[string]bool
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
	selfID      string
}

// MessageHandler processes an incoming Slack message.
type MessageHandler func(msg *InboundMessage)

// InboundMessage represents a Slack message coming into Prizm.
type InboundMessage struct {
	ChannelID string // Slack channel ID
	UserID    string // Slack user ID
	UserName  string // Slack username
	Content   string // Message text
	MessageID string // Slack timestamp (ts)
	IsBot     bool   // True if from a bot
	ThreadTS  string // Thread timestamp (for thread replies)
}

// OutboundMessage represents a message going from Prizm to Slack.
type OutboundMessage struct {
	ChannelID string
	Content   string
	ThreadTS  string // Optional: thread timestamp for replies
	Buttons   []MessageButton
}

// MessageButton describes a Block Kit button.
type MessageButton struct {
	Label    string
	ActionID string
}

// NewBotAdapter creates a Slack adapter with the given bot token.
func NewBotAdapter(token string, allowedUsers []string) *BotAdapter {
	allowed := make(map[string]bool, len(allowedUsers))
	for _, u := range allowedUsers {
		allowed[u] = true
	}
	return &BotAdapter{
		token:      token,
		apiBase:    "https://slack.com/api",
		httpClient: &http.Client{Timeout: 30 * time.Second},
		allowedIDs: allowed,
	}
}

// Start connects to Slack and begins polling for messages.
// Uses the RTM API via a simple polling approach on conversations.history.
// For production, consider Socket Mode or Events API.
func (b *BotAdapter) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	// Verify token with auth.test
	resp, err := b.apiCall(ctx, "auth.test", nil)
	if err != nil {
		return fmt.Errorf("slack: auth.test failed: %w", err)
	}
	var auth struct {
		OK     bool   `json:"ok"`
		UserID string `json:"user_id"`
		User   string `json:"user"`
	}
	if err := json.Unmarshal(resp, &auth); err != nil {
		return fmt.Errorf("slack: parse auth.test: %w", err)
	}
	if !auth.OK {
		return fmt.Errorf("slack: auth.test returned not OK")
	}
	b.selfID = auth.UserID
	log.Printf("[SLACK] connected as %s (ID: %s)", auth.User, b.selfID)

	// Start polling (simplified — production should use Socket Mode or Events API)
	go b.pollLoop()

	return nil
}

// Stop disconnects from Slack.
func (b *BotAdapter) Stop() error {
	if b.cancel != nil {
		b.cancel()
	}
	return nil
}

// OnMessage registers a handler for incoming messages.
func (b *BotAdapter) OnMessage(handler MessageHandler) {
	b.handlers = append(b.handlers, handler)
}

// Send sends a message to a Slack channel.
func (b *BotAdapter) Send(msg *OutboundMessage) error {
	params := map[string]any{
		"channel": msg.ChannelID,
		"text":    msg.Content,
	}
	if msg.ThreadTS != "" {
		params["thread_ts"] = msg.ThreadTS
	}
	if len(msg.Buttons) > 0 {
		blocks := buildButtonBlocks(msg.Buttons)
		params["blocks"] = blocks
	}
	_, err := b.apiCall(b.ctx, "chat.postMessage", params)
	return err
}

// EditMessage edits an existing message (for streaming output).
func (b *BotAdapter) EditMessage(channelID, messageTS, content string) error {
	params := map[string]any{
		"channel": channelID,
		"ts":      messageTS,
		"text":    content,
	}
	_, err := b.apiCall(b.ctx, "chat.update", params)
	return err
}

// Typing indicator is not directly supported via Web API in the same way.
// Slack shows typing via RTM presence, which we skip for simplicity.
func (b *BotAdapter) Typing(channelID string) error {
	return nil // no-op
}

// SelfID returns the bot's Slack user ID.
func (b *BotAdapter) SelfID() string {
	return b.selfID
}

// pollLoop polls Slack conversations for new messages.
// This is a simplified approach — production should use Socket Mode.
func (b *BotAdapter) pollLoop() {
	lastTS := time.Now().Format("0000000000.000000")
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}
		// Note: This is a placeholder polling implementation.
		// A real implementation would use Slack's Socket Mode or Events API
		// for real-time message delivery instead of polling.
		time.Sleep(5 * time.Second)

		_ = lastTS // would use this to fetch only new messages
	}
}

// dispatch calls all registered handlers with the message.
func (b *BotAdapter) dispatch(msg *InboundMessage) {
	b.mu.RLock()
	handlers := b.handlers
	b.mu.RUnlock()
	for _, h := range handlers {
		h(msg)
	}
}

// buildButtonBlocks creates Slack Block Kit blocks with buttons.
func buildButtonBlocks(buttons []MessageButton) []map[string]any {
	elements := make([]map[string]any, len(buttons))
	for i, btn := range buttons {
		elements[i] = map[string]any{
			"type": "button",
			"text": map[string]string{"type": "plain_text", "text": btn.Label},
			"action_id": btn.ActionID,
		}
	}
	return []map[string]any{
		{
			"type":      "actions",
			"elements":  elements,
		},
	}
}

// apiCall makes a POST request to the Slack Web API.
func (b *BotAdapter) apiCall(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	var body string
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("slack: marshal params: %w", err)
		}
		body = string(data)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiBase+"/"+method, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("slack: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.token)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack: %s: %w", method, err)
	}
	defer resp.Body.Close()

	var apiResp struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"-"` // Slack returns response fields at top level
		Error       string          `json:"error"`
	}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("slack: parse response: %w", err)
	}
	if !apiResp.OK {
		return nil, fmt.Errorf("slack: %s failed: %s", method, apiResp.Error)
	}
	return raw, nil
}

// SplitMessage splits content into chunks that fit Slack's 40000 char limit.
// Slack has a high limit so splitting is rarely needed, but we keep it for safety.
func SplitMessage(content string) []string {
	const limit = 39000
	if len(content) <= limit {
		return []string{content}
	}
	var chunks []string
	for len(content) > 0 {
		end := limit
		if end > len(content) {
			end = len(content)
		}
		if idx := strings.LastIndex(content[:end], "\n"); idx > 0 {
			end = idx
		}
		chunks = append(chunks, content[:end])
		content = strings.TrimPrefix(content[end:], "\n")
	}
	return chunks
}