// Package session provides the session manager for Prizm V20.
//
// A session tracks a conversation between a user and an agent on a channel.
// Sessions persist across messages, compact when they grow too large, and
// optionally resume after idle timeout.
//
// V20 uses truncation compaction: when a session exceeds MaxContextMessages,
// the oldest messages are removed. Full Remembrance-based summarization
// comes in V21.
package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// Session represents an active conversation between a user and an agent.
type Session struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	Channel     string    `json:"channel"`    // discord, telegram, webchat
	ChannelID   string    `json:"channel_id"` // e.g., Discord channel ID
	UserID      string    `json:"user_id"`    // e.g., Discord user ID
	StartedAt   time.Time `json:"started_at"`
	LastActive  time.Time `json:"last_active"`
	CompactedAt time.Time `json:"compacted_at,omitempty"`

	// Messages holds the conversation history (in order, oldest first).
	Messages []Message `json:"messages"`
}

// Message is a single message within a session.
type Message struct {
	SessionID string    `json:"session_id,omitempty"`
	ID        string    `json:"id"`
	Role      string    `json:"role"` // "user", "agent", "system"
	Content   string    `json:"content"`
	AgentID   string    `json:"agent_id"` // which agent sent this (for agent role)
	Timestamp time.Time `json:"timestamp"`
	Archived  bool      `json:"archived,omitempty"`
}

// LocalSummary is Prizm's deterministic short-term summary for older recent
// local conversation. It is separate from Remembrance semantic memory.
type LocalSummary struct {
	OwnerID          string    `json:"owner_id"`
	AgentID          string    `json:"agent_id"`
	WeekStart        time.Time `json:"week_start"`
	Summary          string    `json:"summary"`
	SourceMessageIDs []string  `json:"source_message_ids"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Manager handles session lifecycle: create, get, update, compact, reset.
type Manager struct {
	mu       sync.RWMutex
	db       *sql.DB
	sessions map[string]*Session // in-memory cache, keyed by session ID

	// Configuration
	maxContextMessages int
	idleTimeout        time.Duration
	dailyResetHour     int
	compactionStrategy string // "truncate" (V20) or "summarize" (V21)
	persistence        bool
	resumeAfterIdle    bool
	keepArchived       bool
}

var idCounter atomic.Uint64

// ManagerOption customizes session persistence behavior.
type ManagerOption func(*Manager)

// WithPersistence enables durable session resume semantics.
func WithPersistence(enabled bool) ManagerOption {
	return func(m *Manager) { m.persistence = enabled }
}

// WithResumeAfterIdle makes FindActive return the latest session even after the
// idle timeout. A zero or negative idle timeout also disables idle expiry.
func WithResumeAfterIdle(enabled bool) ManagerOption {
	return func(m *Manager) { m.resumeAfterIdle = enabled }
}

// WithKeepArchivedMessages preserves compacted messages in SQLite by marking
// them archived instead of deleting them.
func WithKeepArchivedMessages(enabled bool) ManagerOption {
	return func(m *Manager) { m.keepArchived = enabled }
}

// NewManager creates a session manager with the given configuration.
func NewManager(dbPath string, maxContextMessages int, idleTimeout time.Duration, dailyResetHour int, compactionStrategy string, opts ...ManagerOption) (*Manager, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("session: open db: %w", err)
	}

	// Enable WAL mode for concurrent reads
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("session: set WAL mode: %w", err)
	}

	m := &Manager{
		db:                 db,
		sessions:           make(map[string]*Session),
		maxContextMessages: maxContextMessages,
		idleTimeout:        idleTimeout,
		dailyResetHour:     dailyResetHour,
		compactionStrategy: compactionStrategy,
	}
	for _, opt := range opts {
		opt(m)
	}

	if err := m.migrate(ctx(context.Background())); err != nil {
		db.Close()
		return nil, fmt.Errorf("session: migrate: %w", err)
	}

	return m, nil
}

// ctx is a helper to create a background context.
func ctx(bg context.Context) context.Context { return bg }

// migrate creates the sessions and messages tables.
func (m *Manager) migrate(_ context.Context) error {
	if _, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			channel TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			started_at DATETIME NOT NULL,
			last_active DATETIME NOT NULL,
			compacted_at DATETIME
		);
		CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			agent_id TEXT NOT NULL DEFAULT '',
			timestamp DATETIME NOT NULL,
			archived INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE TABLE IF NOT EXISTS local_summaries (
			owner_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			week_start DATETIME NOT NULL,
			summary TEXT NOT NULL,
			source_message_ids TEXT NOT NULL DEFAULT '[]',
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (owner_id, agent_id, week_start)
		);
		CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, timestamp);
		CREATE INDEX IF NOT EXISTS idx_sessions_channel_user ON sessions(channel, channel_id, user_id);
		CREATE INDEX IF NOT EXISTS idx_sessions_agent_user_active ON sessions(agent_id, user_id, last_active);
		CREATE INDEX IF NOT EXISTS idx_local_summaries_owner_agent ON local_summaries(owner_id, agent_id, week_start);
	`); err != nil {
		return err
	}
	hasArchived, err := m.columnExists("messages", "archived")
	if err != nil {
		return err
	}
	if !hasArchived {
		if _, err := m.db.Exec("ALTER TABLE messages ADD COLUMN archived INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	if _, err := m.db.Exec("CREATE INDEX IF NOT EXISTS idx_messages_session_archived ON messages(session_id, archived, timestamp)"); err != nil {
		return err
	}
	// V76: FTS5 full-text search on message content for session search.
	// Using a contentless FTS5 table (no external-content pattern) to avoid
	// the need for sync triggers. Content is duplicated in the FTS index
	// but auto-maintained by SQLite. Falls back gracefully if FTS5 unavailable.
	if _, err := m.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			session_id UNINDEXED,
			message_id UNINDEXED,
			role UNINDEXED,
			content,
			timestamp UNINDEXED
		);
	`); err != nil {
		log.Printf("[SESSION] FTS5 not available, session search disabled: %v", err)
		// Non-fatal — FTS5 may not be compiled into all SQLite builds
	}
	return nil
}

func (m *Manager) columnExists(table, column string) (bool, error) {
	rows, err := m.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Create creates a new session for a user on a channel with a specific agent.
func (m *Manager) Create(agentID, channel, channelID, userID string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	s := &Session{
		ID:         generateSessionID(),
		AgentID:    agentID,
		Channel:    channel,
		ChannelID:  channelID,
		UserID:     userID,
		StartedAt:  now,
		LastActive: now,
		Messages:   []Message{},
	}

	// Persist to SQLite
	_, err := m.db.Exec(
		"INSERT INTO sessions (id, agent_id, channel, channel_id, user_id, started_at, last_active) VALUES (?, ?, ?, ?, ?, ?, ?)",
		s.ID, s.AgentID, s.Channel, s.ChannelID, s.UserID, s.StartedAt, s.LastActive,
	)
	if err != nil {
		return nil, fmt.Errorf("session: insert: %w", err)
	}

	m.sessions[s.ID] = s
	return s, nil
}

// Get retrieves a session by ID.
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.RLock()
	if s, ok := m.sessions[id]; ok {
		m.mu.RUnlock()
		return s, nil
	}
	m.mu.RUnlock()

	// Try loading from SQLite
	return m.loadFromDB(id)
}

// FindActive finds an active session for a user on a channel.
// Returns nil if no active session exists.
func (m *Manager) FindActive(channel, channelID, userID string) (*Session, error) {
	m.mu.RLock()
	for _, s := range m.sessions {
		if s.Channel == channel && s.ChannelID == channelID && s.UserID == userID {
			// Check if session has timed out
			if m.sessionExpired(s.LastActive) {
				m.mu.RUnlock()
				return nil, nil
			}
			m.mu.RUnlock()
			return s, nil
		}
	}
	m.mu.RUnlock()

	// Try loading from SQLite
	row := m.db.QueryRow(
		"SELECT id, agent_id, channel, channel_id, user_id, started_at, last_active, compacted_at FROM sessions WHERE channel = ? AND channel_id = ? AND user_id = ? ORDER BY last_active DESC LIMIT 1",
		channel, channelID, userID,
	)

	var s Session
	var compactedAt sql.NullTime
	err := row.Scan(&s.ID, &s.AgentID, &s.Channel, &s.ChannelID, &s.UserID, &s.StartedAt, &s.LastActive, &compactedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: find active: %w", err)
	}

	// Check idle timeout
	if m.sessionExpired(s.LastActive) {
		return nil, nil
	}

	// Load messages
	msgs, err := m.loadMessages(s.ID)
	if err != nil {
		return nil, err
	}
	s.Messages = msgs

	m.mu.Lock()
	m.sessions[s.ID] = &s
	m.mu.Unlock()

	return &s, nil
}

// FindActiveWithin finds the newest session for (channel, channelID, userID) and
// applies the caller-supplied maxIdle window instead of the manager's global
// idle/persistence settings. maxIdle <= 0 means no idle limit. Returns nil when
// none match or the newest is older than maxIdle.
//
// This exists for the Agent Invocation API's stateful conversations, which want a
// long, self-contained idle window (e.g. 36h) decoupled from the built-in chat
// pipeline's IdleTimeoutMinutes.
func (m *Manager) FindActiveWithin(channel, channelID, userID string, maxIdle time.Duration) (*Session, error) {
	stale := func(lastActive time.Time) bool {
		return maxIdle > 0 && time.Since(lastActive) > maxIdle
	}

	m.mu.RLock()
	var newest *Session
	for _, s := range m.sessions {
		if s.Channel == channel && s.ChannelID == channelID && s.UserID == userID {
			if newest == nil || s.LastActive.After(newest.LastActive) {
				newest = s
			}
		}
	}
	m.mu.RUnlock()
	if newest != nil {
		if stale(newest.LastActive) {
			return nil, nil
		}
		return newest, nil
	}

	// Not cached — load the newest matching row from SQLite.
	row := m.db.QueryRow(
		"SELECT id, agent_id, channel, channel_id, user_id, started_at, last_active, compacted_at FROM sessions WHERE channel = ? AND channel_id = ? AND user_id = ? ORDER BY last_active DESC LIMIT 1",
		channel, channelID, userID,
	)
	var s Session
	var compactedAt sql.NullTime
	err := row.Scan(&s.ID, &s.AgentID, &s.Channel, &s.ChannelID, &s.UserID, &s.StartedAt, &s.LastActive, &compactedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: find active within: %w", err)
	}
	if stale(s.LastActive) {
		return nil, nil
	}

	msgs, err := m.loadMessages(s.ID)
	if err != nil {
		return nil, err
	}
	s.Messages = msgs

	m.mu.Lock()
	m.sessions[s.ID] = &s
	m.mu.Unlock()

	return &s, nil
}

// FindOwnerAgent finds the newest session for an owner+agent continuity scope
// and assembles recent local transcript from matching legacy channel sessions at
// read time. The returned session ID is the writable latest session; old rows are
// not rewritten or deleted.
func (m *Manager) FindOwnerAgent(agentID, channel, channelID, ownerID string, aliases []string, recallWindow time.Duration, verbatimLimit int) (*Session, error) {
	cutoff := time.Time{}
	if recallWindow > 0 {
		cutoff = time.Now().UTC().Add(-recallWindow)
	}
	return m.FindOwnerAgentSince(agentID, channel, channelID, ownerID, aliases, cutoff, cutoff, verbatimLimit)
}

// FindOwnerAgentSince is the cutoff-based form of FindOwnerAgent. recallStart
// bounds transcript lookup; summaryWeekStart selects the persisted local summary.
func (m *Manager) FindOwnerAgentSince(agentID, channel, channelID, ownerID string, aliases []string, recallStart, summaryWeekStart time.Time, verbatimLimit int) (*Session, error) {
	m.mu.RLock()
	var newest *Session
	for _, s := range m.sessions {
		if s.AgentID == agentID && aliasMatches(s.UserID, ownerID, aliases) {
			if newest == nil || s.LastActive.After(newest.LastActive) {
				newest = s
			}
		}
	}
	m.mu.RUnlock()
	if newest != nil {
		if m.sessionExpired(newest.LastActive) {
			return nil, nil
		}
		if err := m.refreshOwnerAgentMessages(newest, agentID, ownerID, aliases, recallStart, summaryWeekStart, verbatimLimit); err != nil {
			return nil, err
		}
		return newest, nil
	}

	query, args := ownerAgentSessionQuery(agentID, ownerID, aliases)
	row := m.db.QueryRow(query, args...)

	var s Session
	var compactedAt sql.NullTime
	err := row.Scan(&s.ID, &s.AgentID, &s.Channel, &s.ChannelID, &s.UserID, &s.StartedAt, &s.LastActive, &compactedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: find owner-agent: %w", err)
	}
	if compactedAt.Valid {
		s.CompactedAt = compactedAt.Time
	}
	if m.sessionExpired(s.LastActive) {
		return nil, nil
	}

	if err := m.refreshOwnerAgentMessages(&s, agentID, ownerID, aliases, recallStart, summaryWeekStart, verbatimLimit); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[s.ID] = &s
	m.mu.Unlock()

	return &s, nil
}

// GetOrCreateOwnerAgent returns a continuity-scoped session, creating a new
// owner-keyed row with current channel metadata when none exists.
func (m *Manager) GetOrCreateOwnerAgent(agentID, channel, channelID, ownerID string, aliases []string, recallWindow time.Duration, verbatimLimit int) (*Session, error) {
	cutoff := time.Time{}
	if recallWindow > 0 {
		cutoff = time.Now().UTC().Add(-recallWindow)
	}
	return m.GetOrCreateOwnerAgentSince(agentID, channel, channelID, ownerID, aliases, cutoff, cutoff, verbatimLimit)
}

// GetOrCreateOwnerAgentSince returns a cutoff-scoped session, creating one when
// none exists.
func (m *Manager) GetOrCreateOwnerAgentSince(agentID, channel, channelID, ownerID string, aliases []string, recallStart, summaryWeekStart time.Time, verbatimLimit int) (*Session, error) {
	s, err := m.FindOwnerAgentSince(agentID, channel, channelID, ownerID, aliases, recallStart, summaryWeekStart, verbatimLimit)
	if err != nil {
		return nil, err
	}
	if s != nil {
		return s, nil
	}
	return m.Create(agentID, channel, channelID, ownerID)
}

func aliasMatches(userID, ownerID string, aliases []string) bool {
	if userID == ownerID {
		return true
	}
	for _, a := range aliases {
		if userID == a {
			return true
		}
	}
	return false
}

func ownerAgentSessionQuery(agentID, ownerID string, aliases []string) (string, []any) {
	values := normalizeAliases(ownerID, aliases)
	placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
	args := []any{agentID}
	for _, v := range values {
		args = append(args, v)
	}
	return fmt.Sprintf("SELECT id, agent_id, channel, channel_id, user_id, started_at, last_active, compacted_at FROM sessions WHERE agent_id = ? AND user_id IN (%s) ORDER BY last_active DESC LIMIT 1", placeholders), args
}

func normalizeAliases(ownerID string, aliases []string) []string {
	seen := map[string]bool{}
	var values []string
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	add(ownerID)
	for _, a := range aliases {
		add(a)
	}
	return values
}

func (m *Manager) refreshOwnerAgentMessages(s *Session, agentID, ownerID string, aliases []string, recallStart, summaryWeekStart time.Time, verbatimLimit int) error {
	msgs, err := m.loadOwnerAgentMessages(agentID, ownerID, aliases, recallStart, summaryWeekStart, verbatimLimit)
	if err != nil {
		return err
	}
	s.Messages = msgs
	return nil
}

// AddMessage adds a message to a session and persists it.
func (m *Manager) AddMessage(sessionID, role, content, agentID string) (*Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session: not found: %s", sessionID)
	}

	msg := Message{
		SessionID: sessionID,
		ID:        generateMessageID(),
		Role:      role,
		Content:   content,
		AgentID:   agentID,
		Timestamp: time.Now().UTC(),
	}

	s.Messages = append(s.Messages, msg)
	s.LastActive = time.Now().UTC()

	// Persist message
	_, err := m.db.Exec(
		"INSERT INTO messages (id, session_id, role, content, agent_id, timestamp) VALUES (?, ?, ?, ?, ?, ?)",
		msg.ID, sessionID, msg.Role, msg.Content, msg.AgentID, msg.Timestamp,
	)
	if err != nil {
		return nil, fmt.Errorf("session: insert message: %w", err)
	}

	// V76: Also insert into FTS5 index for session search
	m.db.Exec("INSERT INTO messages_fts (session_id, message_id, role, content, timestamp) VALUES (?, ?, ?, ?, ?)",
		sessionID, msg.ID, msg.Role, msg.Content, msg.Timestamp) // non-fatal if FTS unavailable


	// Update last_active
	_, err = m.db.Exec(
		"UPDATE sessions SET last_active = ? WHERE id = ?",
		s.LastActive, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("session: update last_active: %w", err)
	}

	// Compact if needed
	if len(s.Messages) > m.maxContextMessages {
		if err := m.compact(s); err != nil {
			// Log compaction error but don't fail the message add
			// In production this would go to structured logging
			fmt.Printf("session: compaction warning: %v\n", err)
		}
	}

	return &msg, nil
}

func (m *Manager) sessionExpired(lastActive time.Time) bool {
	if m.persistence || m.resumeAfterIdle || m.idleTimeout <= 0 {
		return false
	}
	return time.Since(lastActive) > m.idleTimeout
}

// compact trims the in-memory prompt window when the session exceeds the budget.
// When archival is enabled, older rows are preserved in SQLite and excluded from
// prompt loading by archived=1.
func (m *Manager) compact(s *Session) error {
	if m.compactionStrategy != "truncate" && m.compactionStrategy != "summarize" {
		return nil
	}

	var current []Message
	for _, msg := range s.Messages {
		if msg.SessionID == "" || msg.SessionID == s.ID {
			current = append(current, msg)
		}
	}

	// Keep only the most recent MaxContextMessages messages for the writable
	// session. Owner-agent merged legacy rows are read-time context only.
	excess := len(current) - m.maxContextMessages
	if excess <= 0 {
		return nil
	}

	removed := current[:excess]
	removeIDs := make(map[string]bool, len(removed))
	for _, msg := range removed {
		removeIDs[msg.ID] = true
	}

	// V76: For "summarize" strategy, build a SessionMemory from the removed messages
	// and inject it as the first message in the kept set. This gives the agent a
	// structured continuity anchor after compaction.
	var continuityMsg *Message
	if m.compactionStrategy == "summarize" && len(removed) > 0 {
		mem := BuildSessionMemoryFromMessages(removed)
		// Use the oldest removed message's timestamp so the continuity message
		// stays first when messages are ordered by timestamp ASC on reload.
		oldestTs := removed[0].Timestamp
		for _, r := range removed[1:] {
			if r.Timestamp.Before(oldestTs) {
				oldestTs = r.Timestamp
			}
		}
		continuityMsg = &Message{
			ID:        "continuity-" + s.ID + "-" + oldestTs.Format("20060102-150405") + "-" + fmt.Sprintf("%d", time.Now().UnixNano()%1000000),
			SessionID: s.ID,
			Role:      "system",
			Content:   mem.Format(),
			Timestamp: oldestTs,
		}
	}

	kept := s.Messages[:0]
	for _, msg := range s.Messages {
		if !removeIDs[msg.ID] {
			kept = append(kept, msg)
		}
	}

	// Prepend the continuity summary if we built one
	if continuityMsg != nil {
		kept = append([]Message{*continuityMsg}, kept...)
	}

	s.Messages = kept
	s.CompactedAt = time.Now().UTC()

	for _, msg := range removed {
		if m.keepArchived || m.persistence {
			if _, err := m.db.Exec("UPDATE messages SET archived = 1 WHERE id = ?", msg.ID); err != nil {
				return fmt.Errorf("session: archive message %s: %w", msg.ID, err)
			}
		} else {
			if _, err := m.db.Exec("DELETE FROM messages WHERE id = ?", msg.ID); err != nil {
				return fmt.Errorf("session: delete message %s: %w", msg.ID, err)
			}
		}
	}

	// Store the continuity message in the DB if we built one
	if continuityMsg != nil {
		if _, err := m.db.Exec(
			"INSERT INTO messages (id, session_id, role, content, agent_id, timestamp, archived) VALUES (?, ?, ?, ?, ?, ?, 0)",
			continuityMsg.ID, s.ID, continuityMsg.Role, continuityMsg.Content, "", continuityMsg.Timestamp,
		); err != nil {
			log.Printf("[SESSION] failed to store continuity message: %v (non-fatal)", err)
		}
	}

	// Update compacted_at
	if _, err := m.db.Exec("UPDATE sessions SET compacted_at = ? WHERE id = ?", s.CompactedAt, s.ID); err != nil {
		return fmt.Errorf("session: update compacted_at: %w", err)
	}

	return nil
}

// Close shuts down the session manager and closes the database.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.db.Close()
}

// loadFromDB loads a session from SQLite by ID.
func (m *Manager) loadFromDB(id string) (*Session, error) {
	row := m.db.QueryRow(
		"SELECT id, agent_id, channel, channel_id, user_id, started_at, last_active, compacted_at FROM sessions WHERE id = ?",
		id,
	)

	var s Session
	var compactedAt sql.NullTime
	err := row.Scan(&s.ID, &s.AgentID, &s.Channel, &s.ChannelID, &s.UserID, &s.StartedAt, &s.LastActive, &compactedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session: not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("session: load: %w", err)
	}

	msgs, err := m.loadMessages(s.ID)
	if err != nil {
		return nil, err
	}
	s.Messages = msgs

	return &s, nil
}

// loadMessages loads non-archived prompt messages for a session from SQLite.
func (m *Manager) loadMessages(sessionID string) ([]Message, error) {
	rows, err := m.db.Query(
		"SELECT id, role, content, agent_id, timestamp, archived FROM messages WHERE session_id = ? AND archived = 0 ORDER BY timestamp ASC",
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("session: load messages: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var msg Message
		var archived int
		if err := rows.Scan(&msg.ID, &msg.Role, &msg.Content, &msg.AgentID, &msg.Timestamp, &archived); err != nil {
			return nil, fmt.Errorf("session: scan message: %w", err)
		}
		msg.SessionID = sessionID
		msg.Archived = archived != 0
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

func (m *Manager) loadOwnerAgentMessages(agentID, ownerID string, aliases []string, recallStart, summaryWeekStart time.Time, verbatimLimit int) ([]Message, error) {
	values := normalizeAliases(ownerID, aliases)
	placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
	args := []any{agentID}
	for _, v := range values {
		args = append(args, v)
	}
	args = append(args, recallStart)
	limit := verbatimLimit
	if limit <= 0 {
		limit = m.maxContextMessages
	}
	if limit <= 0 {
		limit = 40
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
		SELECT message_id, session_id, role, content, message_agent_id, timestamp, archived FROM (
			SELECT m.id AS message_id, m.session_id, m.role, m.content, m.agent_id AS message_agent_id, m.timestamp, m.archived
			FROM messages m
			JOIN sessions s ON s.id = m.session_id
			WHERE s.agent_id = ? AND s.user_id IN (%s) AND m.archived = 0 AND m.timestamp >= ?
			ORDER BY m.timestamp DESC
			LIMIT ?
		) ORDER BY timestamp ASC`, placeholders)

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("session: load owner-agent messages: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var msg Message
		var archived int
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Content, &msg.AgentID, &msg.Timestamp, &archived); err != nil {
			return nil, fmt.Errorf("session: scan owner-agent message: %w", err)
		}
		msg.Archived = archived != 0
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	summary, err := m.loadLocalSummary(ownerID, agentID, summaryWeekStart)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(summary) != "" {
		msgs = append([]Message{{
			ID:        "local-summary",
			Role:      "system",
			Content:   summary,
			Timestamp: time.Now().UTC(),
		}}, msgs...)
	}
	return msgs, nil
}

func (m *Manager) loadLocalSummary(ownerID, agentID string, weekStart time.Time) (string, error) {
	if weekStart.IsZero() {
		return "", nil
	}
	var summary string
	err := m.db.QueryRow(
		"SELECT summary FROM local_summaries WHERE owner_id = ? AND agent_id = ? AND week_start = ?",
		ownerID, agentID, weekStart,
	).Scan(&summary)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("session: load local summary: %w", err)
	}
	return summary, nil
}

// UpdateLocalSummary deterministically updates the persisted summary for older
// current-window messages outside the exact verbatim window.
func (m *Manager) UpdateLocalSummary(ownerID, agentID string, aliases []string, recallStart, weekStart time.Time, verbatimLimit int) (*LocalSummary, error) {
	values := normalizeAliases(ownerID, aliases)
	placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
	args := []any{agentID}
	for _, v := range values {
		args = append(args, v)
	}
	args = append(args, recallStart)

	query := fmt.Sprintf(`
		SELECT m.id, m.role, m.content, m.agent_id, m.timestamp
		FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE s.agent_id = ? AND s.user_id IN (%s) AND m.archived = 0 AND m.timestamp >= ?
		ORDER BY m.timestamp DESC`, placeholders)

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("session: load summary source messages: %w", err)
	}
	defer rows.Close()

	type source struct {
		id      string
		role    string
		content string
		agentID string
		ts      time.Time
	}
	var all []source
	for rows.Next() {
		var s source
		if err := rows.Scan(&s.id, &s.role, &s.content, &s.agentID, &s.ts); err != nil {
			return nil, fmt.Errorf("session: scan summary source message: %w", err)
		}
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if verbatimLimit <= 0 {
		verbatimLimit = m.maxContextMessages
	}
	if verbatimLimit <= 0 {
		verbatimLimit = 40
	}
	if len(all) <= verbatimLimit {
		return m.clearLocalSummary(ownerID, agentID, weekStart)
	}

	older := all[verbatimLimit:]
	for i, j := 0, len(older)-1; i < j; i, j = i+1, j-1 {
		older[i], older[j] = older[j], older[i]
	}

	var lines []string
	var ids []string
	for _, msg := range older {
		content := strings.TrimSpace(msg.content)
		if content == "" || msg.role == "tool" {
			continue
		}
		if len(content) > 220 {
			content = content[:220] + "..."
		}
		speaker := msg.role
		if msg.role == "agent" && msg.agentID != "" {
			speaker = msg.agentID
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", speaker, content))
		ids = append(ids, msg.id)
		if len(lines) >= 24 {
			break
		}
	}
	if len(lines) == 0 {
		return m.clearLocalSummary(ownerID, agentID, weekStart)
	}

	summary := "Local rolling summary of older current-week conversation. Exact recent transcript has priority over this summary:\n" + strings.Join(lines, "\n")
	encodedIDs, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("session: marshal summary source ids: %w", err)
	}
	now := time.Now().UTC()
	if _, err := m.db.Exec(`
		INSERT INTO local_summaries (owner_id, agent_id, week_start, summary, source_message_ids, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner_id, agent_id, week_start) DO UPDATE SET
			summary = excluded.summary,
			source_message_ids = excluded.source_message_ids,
			updated_at = excluded.updated_at`,
		ownerID, agentID, weekStart, summary, string(encodedIDs), now,
	); err != nil {
		return nil, fmt.Errorf("session: upsert local summary: %w", err)
	}
	return &LocalSummary{
		OwnerID:          ownerID,
		AgentID:          agentID,
		WeekStart:        weekStart,
		Summary:          summary,
		SourceMessageIDs: ids,
		UpdatedAt:        now,
	}, nil
}

func (m *Manager) clearLocalSummary(ownerID, agentID string, weekStart time.Time) (*LocalSummary, error) {
	if weekStart.IsZero() {
		return nil, nil
	}
	if _, err := m.db.Exec(
		"DELETE FROM local_summaries WHERE owner_id = ? AND agent_id = ? AND week_start = ?",
		ownerID, agentID, weekStart,
	); err != nil {
		return nil, fmt.Errorf("session: clear local summary: %w", err)
	}
	return nil, nil
}

// GetLocalSummary retrieves a persisted local summary for tests and diagnostics.
func (m *Manager) GetLocalSummary(ownerID, agentID string, weekStart time.Time) (*LocalSummary, error) {
	var summary, encodedIDs string
	var updatedAt time.Time
	err := m.db.QueryRow(
		"SELECT summary, source_message_ids, updated_at FROM local_summaries WHERE owner_id = ? AND agent_id = ? AND week_start = ?",
		ownerID, agentID, weekStart,
	).Scan(&summary, &encodedIDs, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: get local summary: %w", err)
	}
	var ids []string
	if err := json.Unmarshal([]byte(encodedIDs), &ids); err != nil {
		return nil, fmt.Errorf("session: decode local summary ids: %w", err)
	}
	return &LocalSummary{
		OwnerID:          ownerID,
		AgentID:          agentID,
		WeekStart:        weekStart,
		Summary:          summary,
		SourceMessageIDs: ids,
		UpdatedAt:        updatedAt,
	}, nil
}

//lint:ignore U1000 retained for local summary fallback integration
func (m *Manager) localSummary(agentID string, aliases []string, cutoff time.Time, recent []Message) (string, error) {
	if len(recent) == 0 {
		return "", nil
	}
	oldestRecent := recent[0].Timestamp
	placeholders := strings.TrimRight(strings.Repeat("?,", len(aliases)), ",")
	args := []any{agentID}
	for _, v := range aliases {
		args = append(args, v)
	}
	args = append(args, cutoff, oldestRecent, 12)
	query := fmt.Sprintf(`
		SELECT m.role, m.content, m.agent_id, m.timestamp
		FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE s.agent_id = ? AND s.user_id IN (%s) AND m.timestamp >= ? AND m.timestamp < ?
		ORDER BY m.timestamp DESC
		LIMIT ?`, placeholders)
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return "", fmt.Errorf("session: load local summary messages: %w", err)
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var role, content, msgAgent string
		var ts time.Time
		if err := rows.Scan(&role, &content, &msgAgent, &ts); err != nil {
			return "", err
		}
		if len(content) > 180 {
			content = content[:180] + "..."
		}
		speaker := role
		if role == "agent" && msgAgent != "" {
			speaker = msgAgent
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", speaker, content))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", nil
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return "Local rolling summary of older recent conversation. Treat exact recent transcript as higher priority:\n" + strings.Join(lines, "\n"), nil
}

// ListActive returns all sessions that are currently in memory.
func (m *Manager) ListActive() ([]*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var active []*Session
	for _, s := range m.sessions {
		active = append(active, s)
	}
	return active, nil
}

// List returns all active session IDs.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

// generateSessionID creates a unique session ID.
func generateSessionID() string {
	return fmt.Sprintf("ses_%d_%d", time.Now().UnixNano(), idCounter.Add(1))
}

// generateMessageID creates a unique message ID.
func generateMessageID() string {
	return fmt.Sprintf("msg_%d_%d", time.Now().UnixNano(), idCounter.Add(1))
}

// SearchResult is a single message match from SearchSessions.
type SearchResult struct {
	SessionID  string    `json:"session_id"`
	MessageID  string    `json:"message_id"`
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	Timestamp  time.Time `json:"timestamp"`
	Rank       float64   `json:"rank"`
}

// SearchSessions performs full-text search across all session messages using FTS5.
// Returns matching messages ranked by relevance. Returns an error if FTS5 is
// not available.
func (m *Manager) SearchSessions(query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := m.db.Query(`
		SELECT session_id, message_id, role, content, timestamp, rank
		FROM messages_fts
		WHERE messages_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("session: FTS5 search failed: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var tsStr string
		if err := rows.Scan(&r.SessionID, &r.MessageID, &r.Role, &r.Content, &tsStr, &r.Rank); err != nil {
			return nil, fmt.Errorf("session: scan search result: %w", err)
		}
		// FTS5 columns are untyped TEXT — parse timestamp string into time.Time
		if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			r.Timestamp = t
		} else if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
			r.Timestamp = t
		} else if t, err := time.Parse("2006-01-02 15:04:05", tsStr); err == nil {
			r.Timestamp = t
		}
		results = append(results, r)
	}
	return results, nil
}
