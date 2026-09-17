// Package discordbot provides the Discord bot adapter for Prizm V20.
//
// Unlike the existing webhook-based discord adapter (which only posts
// run summaries), this adapter implements a full Discord bot gateway:
//   - Inbound: Receives Discord messages → publishes `<agent-id>.channel.received` events
//   - Outbound: Subscribes to `<agent-id>.channel.sent` events → sends Discord messages
//
// This is the adapter that makes Prizm live on Discord — you can talk to
// it and it talks back.
//
// Uses discordgo (bwmarrin/discordgo) for the Discord gateway connection.
package discordbot

import (
	"context"
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

// MessageLimit is Discord's maximum plain message/edit size.
const MessageLimit = 2000

// MessageChunkLimit leaves room for prefixes/suffixes and avoids sending
// chunks right at Discord's hard limit.
const MessageChunkLimit = 1900

// BotAdapter connects Prizm to Discord as a bot for bidirectional messaging.
type BotAdapter struct {
	token    string
	session  *discordgo.Session
	handlers []MessageHandler

	mu             sync.RWMutex
	ready          bool
	ctx            context.Context
	cancel         context.CancelFunc
	buttonHandlers []ButtonHandler
}

// MessageHandler processes an incoming Discord message.
// Implementations typically route the message to the agent router.
type MessageHandler func(msg *InboundMessage)

// InboundMessage represents a Discord message coming into Prizm.
type InboundMessage struct {
	ChannelID string // Discord channel ID
	UserID    string // Discord user ID
	UserName  string // Discord username
	Content   string // Message content
	GuildID   string // Discord server ID (empty for DMs)
	IsDM      bool   // True if this is a direct message
	IsBot     bool   // True if this message is from another bot
	MessageID string // Discord message ID (for replies)
}

// OutboundMessage represents a message going from Prizm to Discord.
type OutboundMessage struct {
	ChannelID string          // Discord channel to send to
	Content   string          // Message content
	IsReply   bool            // True if this should be a reply
	ReplyTo   string          // Message ID to reply to (if IsReply)
	Buttons   []MessageButton // Optional interactive buttons (rendered as one action row)
}

// MessageButton is a discordgo-free description of an interactive button, so
// callers can request buttons without importing discordgo. Style mirrors Discord's
// button styles: 1 primary, 2 secondary, 3 success, 4 danger.
type MessageButton struct {
	Label    string
	Style    int
	CustomID string
}

// ButtonHandler is called when a user clicks an interactive button, with the
// button's custom ID, the clicking user's id/name, and the channel ID.
type ButtonHandler func(customID, userID, userName, channelID string)

// NewBotAdapter creates a new Discord bot adapter with the given bot token.
func NewBotAdapter(token string) *BotAdapter {
	return &BotAdapter{
		token: token,
	}
}

// Name returns the adapter name.
func (b *BotAdapter) Name() string {
	return "discord-bot"
}

// Start connects to Discord and begins listening for messages.
// Blocks until the connection is established or an error occurs.
func (b *BotAdapter) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	dg, err := discordgo.New("Bot " + b.token)
	if err != nil {
		return fmt.Errorf("discord-bot: create session: %w", err)
	}
	b.session = dg

	// Register Discord event handlers
	dg.AddHandler(b.onReady)
	dg.AddHandler(b.onMessageCreate)
	dg.AddHandler(b.onInteractionCreate)

	// We need message content intent for reading messages
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages

	// Open connection
	if err := dg.Open(); err != nil {
		return fmt.Errorf("discord-bot: open connection: %w", err)
	}

	// Wait for context cancellation
	<-b.ctx.Done()

	// Clean shutdown
	dg.Close()
	return nil
}

// Stop disconnects from Discord.
func (b *BotAdapter) Stop() error {
	if b.cancel != nil {
		b.cancel()
	}
	if b.session != nil {
		return b.session.Close()
	}
	return nil
}

// OnMessage registers a handler for incoming Discord messages.
func (b *BotAdapter) OnMessage(handler MessageHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = append(b.handlers, handler)
}

// OnButton registers a handler for interactive button clicks.
func (b *BotAdapter) OnButton(handler ButtonHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buttonHandlers = append(b.buttonHandlers, handler)
}

// onInteractionCreate routes message-component (button) interactions to the
// registered button handlers and acknowledges the interaction so the Discord
// client doesn't show "interaction failed".
func (b *BotAdapter) onInteractionCreate(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	if ic.Type != discordgo.InteractionMessageComponent {
		return
	}
	customID := ic.MessageComponentData().CustomID
	userID, userName := "", ""
	if ic.Member != nil && ic.Member.User != nil {
		userID, userName = ic.Member.User.ID, ic.Member.User.Username
	} else if ic.User != nil {
		userID, userName = ic.User.ID, ic.User.Username
	}
	log.Printf("[DISCORD] interaction: customID=%q user=%q(%s) channel=%s", customID, userName, userID, ic.ChannelID)

	// Disable all buttons on the message immediately so the user can't double-click
	if ic.ChannelID != "" && ic.Message.ID != "" {
		empty := []discordgo.MessageComponent{}
		_, _ = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			ID:         ic.Message.ID,
			Channel:    ic.ChannelID,
			Components: &empty,
		})
	}

	// Respond with a ephemeral confirmation showing which button was clicked
	action := decodeButtonAction(customID)
	_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("✅ **%s** — processing your decision...", action),
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})

	b.mu.RLock()
	hs := append([]ButtonHandler(nil), b.buttonHandlers...)
	b.mu.RUnlock()
	log.Printf("[DISCORD] dispatching to %d button handlers", len(hs))
	for _, h := range hs {
		h(customID, userID, userName, ic.ChannelID)
	}
}

// decodeButtonAction extracts the action (approve/changes/reject) from a custom ID
// for display purposes. Returns "button" if parsing fails.
func decodeButtonAction(customID string) string {
	parts := strings.SplitN(customID, ":", 4)
	if len(parts) >= 3 {
		switch parts[2] {
		case "approve":
			return "Approved"
		case "changes":
			return "Changes requested"
		case "reject":
			return "Rejected"
		}
	}
	return "Button clicked"
}

// Send sends a message to a Discord channel.
func (b *BotAdapter) Send(msg *OutboundMessage) error {
	if b.session == nil {
		return fmt.Errorf("discord-bot: not connected")
	}

	content := msg.Content

	// Discord message length limit is 2000 characters
	// Split long messages into multiple sends
	if len(content) <= MessageLimit {
		return b.sendMessage(msg.ChannelID, content, msg, true)
	}

	// Split at the last newline before the chunk limit. Buttons (if any) attach
	// only to the final chunk so the action row appears under the full message.
	chunks := splitMessage(content, MessageChunkLimit)
	for i, chunk := range chunks {
		last := i == len(chunks)-1
		if err := b.sendMessage(msg.ChannelID, chunk, msg, last); err != nil {
			return fmt.Errorf("discord-bot: send chunk %d: %w", i, err)
		}
	}
	return nil
}

// sendMessage sends a single message to a Discord channel. withButtons controls
// whether msg.Buttons are rendered on this send (true only for the last chunk).
func (b *BotAdapter) sendMessage(channelID, content string, msg *OutboundMessage, withButtons bool) error {
	if withButtons && len(msg.Buttons) > 0 {
		return b.sendWithButtons(channelID, content, msg)
	}
	if msg.IsReply && msg.ReplyTo != "" {
		ref := &discordgo.MessageReference{
			MessageID: msg.ReplyTo,
			ChannelID: channelID,
		}
		_, err := b.session.ChannelMessageSendReply(channelID, content, ref)
		return err
	}
	_, err := b.session.ChannelMessageSend(channelID, content)
	return err
}

// sendWithButtons sends a message with a single action row of buttons.
func (b *BotAdapter) sendWithButtons(channelID, content string, msg *OutboundMessage) error {
	row := discordgo.ActionsRow{}
	for _, btn := range msg.Buttons {
		row.Components = append(row.Components, discordgo.Button{
			Label:    btn.Label,
			Style:    discordgo.ButtonStyle(btn.Style),
			CustomID: btn.CustomID,
		})
	}
	send := &discordgo.MessageSend{
		Content:    content,
		Components: []discordgo.MessageComponent{row},
	}
	if msg.IsReply && msg.ReplyTo != "" {
		send.Reference = &discordgo.MessageReference{MessageID: msg.ReplyTo, ChannelID: channelID}
	}
	_, err := b.session.ChannelMessageSendComplex(channelID, send)
	return err
}

// onReady handles the Discord ready event.
func (b *BotAdapter) onReady(s *discordgo.Session, r *discordgo.Ready) {
	b.mu.Lock()
	b.ready = true
	b.mu.Unlock()
	log.Printf("discord-bot: connected as %s#%s", s.State.User.Username, s.State.User.Discriminator)
}

// SelfID returns the bot's own Discord user ID, used for mention detection.
func (b *BotAdapter) SelfID() string {
	if b.session == nil || b.session.State == nil || b.session.State.User == nil {
		return ""
	}
	return b.session.State.User.ID
}

// onMessageCreate handles incoming Discord messages.
func (b *BotAdapter) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore messages from the bot itself
	if m.Author.ID == s.State.User.ID {
		return
	}

	// Ignore empty messages
	content := strings.TrimSpace(m.Content)
	if content == "" {
		return
	}

	inbound := &InboundMessage{
		ChannelID: m.ChannelID,
		UserID:    m.Author.ID,
		UserName:  m.Author.Username,
		Content:   content,
		GuildID:   m.GuildID,
		IsDM:      m.GuildID == "",
		IsBot:     m.Author.Bot,
		MessageID: m.ID,
	}

	// Notify all registered handlers
	b.mu.RLock()
	handlers := make([]MessageHandler, len(b.handlers))
	copy(handlers, b.handlers)
	b.mu.RUnlock()

	for _, handler := range handlers {
		handler(inbound)
	}
}

// Typing sends the "typing" indicator to a Discord channel.
// The indicator lasts for ~10 seconds or until a message is sent.
func (b *BotAdapter) Typing(channelID string) error {
	if b.session == nil {
		return fmt.Errorf("discord-bot: not connected")
	}
	return b.session.ChannelTyping(channelID)
}

// SendPlaceholder sends an initial message that can be edited later.
// SendPlaceholder sends a typing indicator and returns an empty message ID.
// Previously sent a visible placeholder message ("✧ ..."), but this caused issues
// where other bots and users would see and respond to the placeholder before it was edited.
// Now we only show the Discord "typing..." indicator, then send the complete response
// as a new message when ready.
func (b *BotAdapter) SendPlaceholder(channelID, content string) (string, error) {
	if b.session == nil {
		return "", fmt.Errorf("discord-bot: not connected")
	}
	// Send typing indicator instead of a placeholder message
	if err := b.session.ChannelTyping(channelID); err != nil {
		log.Printf("[WARN] typing indicator failed: %v", err)
	}
	// Return empty string so all EditMessage calls become no-ops
	// and the response is sent as a new message via Send()
	return "", nil
}

// EditMessage edits an existing Discord message with new content.
// Used for streaming: update the placeholder message as tokens arrive.
func (b *BotAdapter) EditMessage(channelID, messageID, content string) error {
	if b.session == nil {
		return fmt.Errorf("discord-bot: not connected")
	}
	_, err := b.session.ChannelMessageEdit(channelID, messageID, content)
	return err
}

// IsReady returns whether the bot has connected to Discord.
func (b *BotAdapter) IsReady() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ready
}

// RecentMessage holds a minimal Discord message snapshot for context injection.
type RecentMessage struct {
	AuthorName string
	AuthorID   string
	Content    string
	Timestamp  string
}

// GetRecentMessages fetches up to limit messages from a Discord channel, oldest-first.
// It skips messages from the bot itself. Returns an empty slice on error.
func (b *BotAdapter) GetRecentMessages(channelID string, limit int) []RecentMessage {
	if b.session == nil {
		return nil
	}
	msgs, err := b.session.ChannelMessages(channelID, limit, "", "", "")
	if err != nil {
		log.Printf("[DISCORD] failed to fetch recent messages: %v", err)
		return nil
	}
	// Reverse to oldest-first
	result := make([]RecentMessage, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Author.ID == b.SelfID() {
			continue // skip own messages
		}
		result = append(result, RecentMessage{
			AuthorName: m.Author.Username,
			AuthorID:   m.Author.ID,
			Content:    m.Content,
			Timestamp:  m.Timestamp.Format("2006-01-02 15:04"),
		})
	}
	return result
}

// SplitMessage splits a long message into chunks at the last newline before
// maxLen bytes. If there are no newlines, it splits at maxLen without cutting a
// UTF-8 rune. Exported for testing.
func SplitMessage(content string, maxLen int) []string {
	return splitMessage(content, maxLen)
}

// splitMessage splits a long message into chunks at the last newline before
// maxLen bytes. If there are no newlines, it splits at maxLen without cutting a
// UTF-8 rune.
func splitMessage(content string, maxLen int) []string {
	if maxLen <= 0 {
		maxLen = MessageChunkLimit
	}
	if len(content) <= maxLen {
		return []string{content}
	}

	var chunks []string
	for len(content) > maxLen {
		// Find the last newline before maxLen, using a UTF-8-safe boundary.
		splitAt := safeSplitIndex(content, maxLen)
		lastNewline := strings.LastIndex(content[:splitAt], "\n")
		if lastNewline > 0 {
			splitAt = lastNewline + 1
		}
		chunks = append(chunks, content[:splitAt])
		content = content[splitAt:]
	}
	if len(content) > 0 {
		chunks = append(chunks, content)
	}
	return chunks
}

// safeSplitIndex finds a UTF-8-safe split point at or before maxLen.
// It walks backwards from maxLen to avoid cutting a multi-byte rune in half.
func safeSplitIndex(content string, maxLen int) int {
	if maxLen >= len(content) {
		return len(content)
	}

	splitAt := maxLen
	for splitAt > 0 && !utf8.RuneStart(content[splitAt]) {
		splitAt--
	}
	if splitAt > 0 {
		return splitAt
	}

	// Fallback: skip the first rune entirely
	_, size := utf8.DecodeRuneInString(content)
	return size
}

// SendAudio sends an audio file to a Discord channel as a voice message.
// The audio data should be a WAV file. It's sent as an attachment with
// the flag 8192 (IS_VOICE_MESSAGE) so Discord renders it as a voice clip.
func (b *BotAdapter) SendAudio(channelID string, audio []byte) error {
	if b.session == nil {
		return fmt.Errorf("discord-bot: not connected")
	}

	// Create a reader for the audio data
	audioReader := bytes.NewReader(audio)

	// Send as a file attachment with voice message flag
	files := []*discordgo.File{
		{
			Name:        "voice.wav",
			ContentType: "audio/wav",
			Reader:      audioReader,
		},
	}

	_, err := b.session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Files: files,
		Flags: 8192, // IS_VOICE_MESSAGE
	})
	return err
}
