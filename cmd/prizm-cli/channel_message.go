// Package main provides channel-agnostic message handling.
//
// Instead of duplicating the full conversation pipeline for each adapter
// (Discord, Telegram, Slack), we extract it into handleMessage which
// operates on ChannelMessage — a platform-independent representation.
//
// Each adapter just translates its native message type to ChannelMessage
// and calls handleMessage. Platform-specific features (Discord buttons,
// Telegram inline keyboards, Slack Block Kit) stay in their adapter wrappers.
package main

// Platform identifies which channel adapter a message came from.
type Platform string

const (
	PlatformDiscord  Platform = "discord"
	PlatformTelegram Platform = "telegram"
	PlatformSlack    Platform = "slack"
)

// ChannelMessage is the platform-independent message representation.
// All adapters translate their native InboundMessage to this struct
// before passing it to the conversation pipeline.
type ChannelMessage struct {
	Platform  Platform // Which adapter sent this message
	ChannelID string   // Channel/chat/group ID
	UserID    string   // User ID on the platform
	UserName  string   // Display name
	Content   string   // Message text content
	MessageID string   // Platform message ID (for replies/edits)
	IsBot     bool     // True if from a bot
	IsDM      bool     // True if direct message
	GuildID   string   // Server/workspace ID (Discord: guild, Slack: team)
	ThreadTS  string   // Thread timestamp (Slack), empty for others
	Metadata  map[string]any // Platform-specific extra data
}

// ChannelSender is the interface adapters implement to send responses.
// Each adapter wraps its platform-specific bot client to satisfy this interface.
type ChannelSender interface {
	// Send delivers a text message to a channel.
	Send(channelID, content string) error
	// SendPlaceholder delivers a placeholder message and returns its ID for editing.
	SendPlaceholder(channelID, content string) (string, error)
	// EditMessage edits an existing message.
	EditMessage(channelID, messageID, content string) error
	// Typing shows a typing indicator in the channel.
	Typing(channelID string) error
	// SendWithButtons sends a message with interactive buttons (approval cards, etc).
	// Platforms that don't support buttons should log a warning and send plain text instead.
	SendWithButtons(channelID string, buttons []ActionButton, content string) error
	// SendAudio sends an audio/voice message to the channel.
	// Platforms that don't support audio should log a warning and skip.
	SendAudio(channelID string, audio []byte) error
	// SupportsButtons returns true if the platform supports interactive buttons.
	SupportsButtons() bool
	// SupportsAudio returns true if the platform supports audio/voice messages.
	SupportsAudio() bool
}

// ActionButton represents a clickable button in a message.
type ActionButton struct {
	Label    string
	CustomID string
	Style    string // "primary", "danger", "secondary"
}

// channelResponse is what the pipeline sends back to the adapter.
type channelResponse struct {
	Content   string // Response text
	ChannelID string // Which channel to send to
}