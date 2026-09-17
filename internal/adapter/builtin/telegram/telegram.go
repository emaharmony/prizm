// Package telegram provides a Telegram bot adapter for Prizm.
//
// Uses the Telegram Bot API via long-polling (getUpdates) for inbound messages
// and sendMessage/editMessageText for outbound. Supports:
//   - Text messages (send/receive)
//   - Inline keyboard buttons (for approvals)
//   - Message editing (for streaming output)
//   - Markdown formatting
//
// Config:
//
//	channels:
//	  - type: "telegram"
//	    token: "<bot-token-from-botfather>"
//	    allowed_users: ["<user-id>"]
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// BotAdapter connects Prizm to Telegram as a bot.
type BotAdapter struct {
	token       string
	apiBase     string
	httpClient  *http.Client
	handlers    []MessageHandler
	allowedIDs  map[string]bool // allowed user IDs (DM pairing)
	offset      int64            // last update offset for getUpdates
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
	selfID      string
}

// MessageHandler processes an incoming Telegram message.
type MessageHandler func(msg *InboundMessage)

// InboundMessage represents a Telegram message coming into Prizm.
type InboundMessage struct {
	ChatID    string // Telegram chat ID (as string for consistency)
	UserID    string // Telegram user ID
	UserName  string // Telegram username
	Content   string // Message text
	MessageID string // Telegram message ID
	IsBot     bool   // True if from another bot
}

// OutboundMessage represents a message going from Prizm to Telegram.
type OutboundMessage struct {
	ChatID   string
	Content  string
	ReplyTo  string // Optional reply-to message ID
	Buttons  []MessageButton
}

// MessageButton describes an inline keyboard button.
type MessageButton struct {
	Label    string
	Callback string // callback_data
}

// NewBotAdapter creates a Telegram adapter with the given bot token.
func NewBotAdapter(token string, allowedUsers []string) *BotAdapter {
	allowed := make(map[string]bool, len(allowedUsers))
	for _, u := range allowedUsers {
		allowed[u] = true
	}
	return &BotAdapter{
		token:      token,
		apiBase:    "https://api.telegram.org/bot" + token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		allowedIDs: allowed,
	}
}

// Start connects to Telegram and begins polling for updates.
func (b *BotAdapter) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	// Get bot info to verify token
	resp, err := b.apiCall(ctx, "getMe", nil)
	if err != nil {
		return fmt.Errorf("telegram: getMe failed: %w", err)
	}
	var me struct {
		OK     bool `json:"ok"`
		Result struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &me); err != nil {
		return fmt.Errorf("telegram: parse getMe: %w", err)
	}
	if !me.OK {
		return fmt.Errorf("telegram: getMe returned not OK")
	}
	b.selfID = fmt.Sprintf("%d", me.Result.ID)
	log.Printf("[TELEGRAM] connected as @%s (ID: %s)", me.Result.Username, b.selfID)

	// Start polling in background
	go b.pollLoop()

	return nil
}

// Stop disconnects from Telegram.
func (b *BotAdapter) Stop() error {
	if b.cancel != nil {
		b.cancel()
	}
	return nil
}

// OnMessage registers a handler for incoming messages.
func (b *BotAdapter) OnMessage(handler MessageHandler) {
	b.mu.Lock()
	b.handlers = append(b.handlers, handler)
	b.mu.Unlock()
}

// Send sends a message to a Telegram chat.
func (b *BotAdapter) Send(msg *OutboundMessage) error {
	params := map[string]any{
		"chat_id":    msg.ChatID,
		"text":       msg.Content,
		"parse_mode": "Markdown",
	}
	if msg.ReplyTo != "" {
		params["reply_to_message_id"] = msg.ReplyTo
	}
	if len(msg.Buttons) > 0 {
		keyboard := make([][]map[string]string, len(msg.Buttons))
		for i, btn := range msg.Buttons {
			keyboard[i] = []map[string]string{
				{"text": btn.Label, "callback_data": btn.Callback},
			}
		}
		params["reply_markup"] = map[string]any{
			"inline_keyboard": keyboard,
		}
	}
	_, err := b.apiCall(b.ctx, "sendMessage", params)
	return err
}

// EditMessage edits an existing message (for streaming output).
func (b *BotAdapter) EditMessage(chatID, messageID, content string) error {
	params := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       content,
		"parse_mode": "Markdown",
	}
	_, err := b.apiCall(b.ctx, "editMessageText", params)
	return err
}

// Typing sends a "typing" indicator (chat action).
func (b *BotAdapter) Typing(chatID string) error {
	params := map[string]any{
		"chat_id": chatID,
		"action":  "typing",
	}
	_, err := b.apiCall(b.ctx, "sendChatAction", params)
	return err
}

// SelfID returns the bot's Telegram user ID.
func (b *BotAdapter) SelfID() string {
	return b.selfID
}

// pollLoop continuously polls Telegram for updates.
func (b *BotAdapter) pollLoop() {
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}
		params := map[string]any{
			"offset":  b.offset,
			"timeout": 30, // long-poll: wait up to 30s for updates
		}
		resp, err := b.apiCall(b.ctx, "getUpdates", params)
		if err != nil {
			log.Printf("[TELEGRAM] poll error: %v", err)
			time.Sleep(5 * time.Second) // back off on error
			continue
		}

		var result struct {
			OK     bool `json:"ok"`
			Result []struct {
				UpdateID      int64 `json:"update_id"`
				Message       *struct {
					MessageID int64  `json:"message_id"`
					From      *struct {
						ID       int64  `json:"id"`
						UserName string `json:"username"`
						IsBot    bool   `json:"is_bot"`
					} `json:"from"`
					Chat *struct {
						ID int64 `json:"id"`
					} `json:"chat"`
					Text string `json:"text"`
				} `json:"message"`
				CallbackQuery *struct {
					ID      string `json:"id"`
					From    *struct {
						ID       int64  `json:"id"`
						UserName string `json:"username"`
						IsBot    bool   `json:"is_bot"`
					} `json:"from"`
					Message *struct {
						MessageID int64 `json:"message_id"`
						Chat      *struct {
							ID int64 `json:"id"`
						} `json:"chat"`
					} `json:"message"`
					Data string `json:"data"`
				} `json:"callback_query"`
			} `json:"result"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			log.Printf("[TELEGRAM] parse error: %v", err)
			continue
		}

		for _, update := range result.Result {
			b.offset = update.UpdateID + 1

			if update.Message != nil && update.Message.From != nil && update.Message.Chat != nil {
				// Check DM pairing
				userID := fmt.Sprintf("%d", update.Message.From.ID)
				if len(b.allowedIDs) > 0 && !b.allowedIDs[userID] {
					log.Printf("[TELEGRAM] ignoring message from unauthorized user %s", userID)
					continue
				}

				msg := &InboundMessage{
					ChatID:    fmt.Sprintf("%d", update.Message.Chat.ID),
					UserID:    userID,
					UserName:  update.Message.From.UserName,
					Content:   update.Message.Text,
					MessageID: fmt.Sprintf("%d", update.Message.MessageID),
					IsBot:     update.Message.From.IsBot,
				}
				b.dispatch(msg)
			}

			if update.CallbackQuery != nil {
				// Handle button callback (approval buttons)
				cb := update.CallbackQuery
				if cb.From != nil && cb.Message != nil && cb.Message.Chat != nil {
					// V76 fix: Apply allowed-users check to callback queries too
					cbUserID := fmt.Sprintf("%d", cb.From.ID)
					if len(b.allowedIDs) > 0 && !b.allowedIDs[cbUserID] {
						log.Printf("[TELEGRAM] ignoring callback from unauthorized user %s", cbUserID)
						b.apiCall(b.ctx, "answerCallbackQuery", map[string]any{
							"callback_query_id": cb.ID,
						})
						continue
					}
					// Acknowledge the callback
					b.apiCall(b.ctx, "answerCallbackQuery", map[string]any{
						"callback_query_id": cb.ID,
					})
					// Dispatch as a message with the callback data as content
					msg := &InboundMessage{
						ChatID:    fmt.Sprintf("%d", cb.Message.Chat.ID),
						UserID:    cbUserID,
						UserName:  cb.From.UserName,
						Content:   cb.Data,
						MessageID: fmt.Sprintf("%d", cb.Message.MessageID),
						IsBot:     cb.From.IsBot,
					}
					b.dispatch(msg)
				}
			}
		}
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

// apiCall makes a POST request to the Telegram Bot API.
func (b *BotAdapter) apiCall(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	var body io.Reader
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("telegram: marshal params: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiBase+"/"+method, body)
	if err != nil {
		return nil, fmt.Errorf("telegram: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: %s: %w", method, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("telegram: read response: %w", err)
	}

	var apiResp struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(data, &apiResp); err != nil {
		return nil, fmt.Errorf("telegram: parse response: %w", err)
	}
	if !apiResp.OK {
		return nil, fmt.Errorf("telegram: %s failed: %s", method, apiResp.Description)
	}
	return apiResp.Result, nil
}

// SplitMessage splits content into chunks that fit Telegram's 4096 char limit.
// Uses rune-aware truncation to avoid splitting multi-byte UTF-8.
func SplitMessage(content string) []string {
	const limit = 4000 // leave room for markdown
	runes := []rune(content)
	if len(runes) <= limit {
		return []string{content}
	}
	var chunks []string
	src := runes
	for len(src) > 0 {
		end := limit
		if end > len(src) {
			end = len(src)
		}
		// Try to split at last newline before limit
		if idx := strings.LastIndex(string(src[:end]), "\n"); idx > 0 {
			end = idx
		}
		chunks = append(chunks, string(src[:end]))
		src = []rune(strings.TrimPrefix(string(src[end:]), "\n"))
	}
	return chunks
}