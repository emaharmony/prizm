package session

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateAndGet(t *testing.T) {
	dbPath := tempDB(t)
	m := newTestManager(t, dbPath)
	defer m.Close()

	s, err := m.Create("lumi", "discord", "channel123", "user456")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if s.AgentID != "lumi" {
		t.Errorf("expected agent 'lumi', got %q", s.AgentID)
	}
	if s.Channel != "discord" {
		t.Errorf("expected channel 'discord', got %q", s.Channel)
	}
	if s.ChannelID != "channel123" {
		t.Errorf("expected channel_id 'channel123', got %q", s.ChannelID)
	}
	if s.UserID != "user456" {
		t.Errorf("expected user_id 'user456', got %q", s.UserID)
	}

	// Get by ID
	got, err := m.Get(s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != s.ID {
		t.Errorf("expected session ID %q, got %q", s.ID, got.ID)
	}
}

func TestFindActive(t *testing.T) {
	dbPath := tempDB(t)
	m := newTestManager(t, dbPath)
	defer m.Close()

	// No session exists yet
	found, err := m.FindActive("discord", "channel123", "user456")
	if err != nil {
		t.Fatalf("FindActive: %v", err)
	}
	if found != nil {
		t.Error("expected nil for no active session")
	}

	// Create a session
	s, err := m.Create("lumi", "discord", "channel123", "user456")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Should find it
	found, err = m.FindActive("discord", "channel123", "user456")
	if err != nil {
		t.Fatalf("FindActive: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find active session")
	}
	if found.ID != s.ID {
		t.Errorf("expected session ID %q, got %q", s.ID, found.ID)
	}

	// Different user should not find it
	found2, err := m.FindActive("discord", "channel123", "other_user")
	if err != nil {
		t.Fatalf("FindActive other user: %v", err)
	}
	if found2 != nil {
		t.Error("expected nil for different user")
	}
}

func TestAddMessage(t *testing.T) {
	dbPath := tempDB(t)
	m := newTestManager(t, dbPath)
	defer m.Close()

	s, err := m.Create("lumi", "discord", "channel123", "user456")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	msg, err := m.AddMessage(s.ID, "user", "Hello!", "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if msg.Role != "user" {
		t.Errorf("expected role 'user', got %q", msg.Role)
	}
	if msg.Content != "Hello!" {
		t.Errorf("expected content 'Hello!', got %q", msg.Content)
	}

	// Add agent response
	msg2, err := m.AddMessage(s.ID, "agent", "Hi there!", "lumi")
	if err != nil {
		t.Fatalf("AddMessage agent: %v", err)
	}
	if msg2.AgentID != "lumi" {
		t.Errorf("expected agent_id 'lumi', got %q", msg2.AgentID)
	}

	// Verify messages are in the session
	got, err := m.Get(s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(got.Messages))
	}
}

func TestCompaction(t *testing.T) {
	dbPath := tempDB(t)
	m := newTestManager(t, dbPath, WithMaxContext(5))
	defer m.Close()

	s, err := m.Create("lumi", "discord", "channel123", "user456")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Add 7 messages (exceeds max of 5)
	for i := 0; i < 7; i++ {
		_, err := m.AddMessage(s.ID, "user", "message", "")
		if err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	// Should be compacted to 5 messages
	got, err := m.Get(s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Messages) > 5 {
		t.Errorf("expected at most 5 messages after compaction, got %d", len(got.Messages))
	}
}

func TestFindActiveResumesAfterIdleWhenPersistent(t *testing.T) {
	dbPath := tempDB(t)
	m, err := NewManager(
		dbPath,
		100,
		time.Nanosecond,
		4,
		"truncate",
		WithPersistence(true),
		WithResumeAfterIdle(true),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	s, err := m.Create("lumi", "discord", "channel123", "user456")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	time.Sleep(time.Millisecond)

	m2, err := NewManager(
		dbPath,
		100,
		time.Nanosecond,
		4,
		"truncate",
		WithPersistence(true),
		WithResumeAfterIdle(true),
	)
	if err != nil {
		t.Fatalf("NewManager reopen: %v", err)
	}
	defer m2.Close()

	found, err := m2.FindActive("discord", "channel123", "user456")
	if err != nil {
		t.Fatalf("FindActive: %v", err)
	}
	if found == nil || found.ID != s.ID {
		t.Fatalf("expected persisted session %q, got %#v", s.ID, found)
	}
}

func TestFindActiveExpiresWhenPersistenceDisabled(t *testing.T) {
	dbPath := tempDB(t)
	m, err := NewManager(dbPath, 100, time.Nanosecond, 4, "truncate")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()
	if _, err := m.Create("lumi", "discord", "channel123", "user456"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	time.Sleep(time.Millisecond)

	found, err := m.FindActive("discord", "channel123", "user456")
	if err != nil {
		t.Fatalf("FindActive: %v", err)
	}
	if found != nil {
		t.Fatalf("expected expired session to be nil, got %s", found.ID)
	}
}

func TestCompactionArchivesInsteadOfDeleting(t *testing.T) {
	dbPath := tempDB(t)
	m, err := NewManager(
		dbPath,
		2,
		30*time.Minute,
		4,
		"summarize",
		WithPersistence(true),
		WithKeepArchivedMessages(true),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	s, err := m.Create("lumi", "discord", "channel123", "user456")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < 4; i++ {
		if _, err := m.AddMessage(s.ID, "user", "message", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	got, err := m.Get(s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Messages) != 3 { // 2 kept + 1 continuity summary (V76 summarize strategy)
		t.Fatalf("expected 3 active messages after summarize compaction (2 kept + 1 continuity), got %d", len(got.Messages))
	}

	var archived, total int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ? AND archived = 1", s.ID).Scan(&archived); err != nil {
		t.Fatalf("count archived: %v", err)
	}
	if err := m.db.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", s.ID).Scan(&total); err != nil {
		t.Fatalf("count total: %v", err)
	}
	if archived != 3 || total != 6 { // 3 archived (incl continuity re-compaction) + 3 active = 6
		t.Fatalf("archived=%d total=%d, want archived=3 total=6", archived, total)
	}
}

func TestOwnerAgentContinuityMergesLegacyChannelSessions(t *testing.T) {
	dbPath := tempDB(t)
	m, err := NewManager(
		dbPath,
		100,
		30*time.Minute,
		4,
		"summarize",
		WithPersistence(true),
		WithResumeAfterIdle(true),
		WithKeepArchivedMessages(true),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	legacyA, err := m.Create("lumi", "discord", "channel-a", "discord-user-1")
	if err != nil {
		t.Fatalf("Create legacyA: %v", err)
	}
	if _, err := m.AddMessage(legacyA.ID, "user", "remember that the topic is blue widgets", ""); err != nil {
		t.Fatalf("AddMessage legacyA: %v", err)
	}
	if _, err := m.AddMessage(legacyA.ID, "agent", "I will remember blue widgets.", "lumi"); err != nil {
		t.Fatalf("AddMessage legacyA agent: %v", err)
	}

	legacyB, err := m.Create("lumi", "discord", "channel-b", "owner-ema")
	if err != nil {
		t.Fatalf("Create legacyB: %v", err)
	}
	if _, err := m.AddMessage(legacyB.ID, "user", "what did I ask about earlier?", ""); err != nil {
		t.Fatalf("AddMessage legacyB: %v", err)
	}

	found, err := m.FindOwnerAgent("lumi", "discord", "channel-b", "owner-ema", []string{"discord-user-1"}, 7*24*time.Hour, 20)
	if err != nil {
		t.Fatalf("FindOwnerAgent: %v", err)
	}
	if found == nil {
		t.Fatal("expected owner-agent session")
	}
	if found.ID != legacyB.ID {
		t.Fatalf("expected newest writable session %s, got %s", legacyB.ID, found.ID)
	}
	if !messagesContain(found.Messages, "blue widgets") {
		t.Fatalf("expected merged legacy transcript to include blue widgets, got %#v", found.Messages)
	}
}

func TestOwnerAgentContinuityIsolatesOwners(t *testing.T) {
	dbPath := tempDB(t)
	m, err := NewManager(dbPath, 100, 30*time.Minute, 4, "truncate", WithPersistence(true), WithResumeAfterIdle(true))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	ownerA, err := m.Create("lumi", "discord", "channel-a", "owner-a")
	if err != nil {
		t.Fatalf("Create ownerA: %v", err)
	}
	if _, err := m.AddMessage(ownerA.ID, "user", "private owner a topic", ""); err != nil {
		t.Fatalf("AddMessage ownerA: %v", err)
	}

	ownerB, err := m.Create("lumi", "discord", "channel-b", "owner-b")
	if err != nil {
		t.Fatalf("Create ownerB: %v", err)
	}
	if _, err := m.AddMessage(ownerB.ID, "user", "owner b topic", ""); err != nil {
		t.Fatalf("AddMessage ownerB: %v", err)
	}

	found, err := m.FindOwnerAgent("lumi", "discord", "channel-b", "owner-b", nil, 7*24*time.Hour, 20)
	if err != nil {
		t.Fatalf("FindOwnerAgent: %v", err)
	}
	if found == nil {
		t.Fatal("expected owner-b session")
	}
	if messagesContain(found.Messages, "private owner a topic") {
		t.Fatalf("owner-b transcript leaked owner-a message: %#v", found.Messages)
	}
	if !messagesContain(found.Messages, "owner b topic") {
		t.Fatalf("expected owner-b transcript, got %#v", found.Messages)
	}
}

func TestOwnerAgentContinuityIsolatesAgents(t *testing.T) {
	dbPath := tempDB(t)
	m, err := NewManager(dbPath, 100, 30*time.Minute, 4, "truncate", WithPersistence(true), WithResumeAfterIdle(true))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	lumi, err := m.Create("lumi", "discord", "channel-a", "owner-a")
	if err != nil {
		t.Fatalf("Create lumi: %v", err)
	}
	if _, err := m.AddMessage(lumi.ID, "user", "lumi-only topic", ""); err != nil {
		t.Fatalf("AddMessage lumi: %v", err)
	}

	codex, err := m.Create("codex", "discord", "channel-a", "owner-a")
	if err != nil {
		t.Fatalf("Create codex: %v", err)
	}
	if _, err := m.AddMessage(codex.ID, "user", "codex topic", ""); err != nil {
		t.Fatalf("AddMessage codex: %v", err)
	}

	found, err := m.FindOwnerAgent("codex", "discord", "channel-a", "owner-a", nil, 7*24*time.Hour, 20)
	if err != nil {
		t.Fatalf("FindOwnerAgent: %v", err)
	}
	if found == nil {
		t.Fatal("expected codex session")
	}
	if messagesContain(found.Messages, "lumi-only topic") {
		t.Fatalf("codex transcript leaked lumi message: %#v", found.Messages)
	}
}

func TestOwnerAgentContinuityAddsLocalSummaryForOlderRecentMessages(t *testing.T) {
	dbPath := tempDB(t)
	m, err := NewManager(dbPath, 100, 30*time.Minute, 4, "summarize", WithPersistence(true), WithResumeAfterIdle(true))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	s, err := m.Create("lumi", "discord", "channel-a", "owner-a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, text := range []string{"older topic one", "older topic two", "newer topic"} {
		if _, err := m.AddMessage(s.ID, "user", text, ""); err != nil {
			t.Fatalf("AddMessage %q: %v", text, err)
		}
		time.Sleep(time.Millisecond)
	}

	weekStart := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	if _, err := m.UpdateLocalSummary("owner-a", "lumi", nil, weekStart, weekStart, 1); err != nil {
		t.Fatalf("UpdateLocalSummary: %v", err)
	}

	found, err := m.FindOwnerAgentSince("lumi", "discord", "channel-a", "owner-a", nil, weekStart, weekStart, 1)
	if err != nil {
		t.Fatalf("FindOwnerAgent: %v", err)
	}
	if found == nil {
		t.Fatal("expected session")
	}
	if len(found.Messages) < 2 || found.Messages[0].Role != "system" {
		t.Fatalf("expected local rolling summary system message, got %#v", found.Messages)
	}
	if !strings.Contains(found.Messages[0].Content, "older topic one") {
		t.Fatalf("expected summary to include older topic, got %q", found.Messages[0].Content)
	}
	if !messagesContain(found.Messages, "newer topic") {
		t.Fatalf("expected exact recent transcript to include newest topic")
	}
}

func TestOwnerAgentContinuityUsesPersistedLocalSummary(t *testing.T) {
	dbPath := tempDB(t)
	m, err := NewManager(dbPath, 100, 30*time.Minute, 4, "summarize", WithPersistence(true), WithResumeAfterIdle(true))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	s, err := m.Create("lumi", "discord", "channel-a", "owner-a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, text := range []string{"remember the durable older topic", "another older topic", "new exact topic"} {
		if _, err := m.AddMessage(s.ID, "user", text, ""); err != nil {
			t.Fatalf("AddMessage %q: %v", text, err)
		}
		time.Sleep(time.Millisecond)
	}

	weekStart := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	summary, err := m.UpdateLocalSummary("owner-a", "lumi", nil, weekStart, weekStart, 1)
	if err != nil {
		t.Fatalf("UpdateLocalSummary: %v", err)
	}
	if summary == nil || !strings.Contains(summary.Summary, "durable older topic") {
		t.Fatalf("expected persisted summary with older topic, got %#v", summary)
	}

	found, err := m.FindOwnerAgentSince("lumi", "discord", "channel-a", "owner-a", nil, weekStart, weekStart, 1)
	if err != nil {
		t.Fatalf("FindOwnerAgentSince: %v", err)
	}
	if found == nil {
		t.Fatal("expected session")
	}
	if len(found.Messages) < 2 || found.Messages[0].Role != "system" {
		t.Fatalf("expected persisted local summary as first system message, got %#v", found.Messages)
	}
	if !strings.Contains(found.Messages[0].Content, "durable older topic") {
		t.Fatalf("expected persisted summary content, got %q", found.Messages[0].Content)
	}
	if !messagesContain(found.Messages, "new exact topic") {
		t.Fatalf("expected exact recent transcript")
	}
}

func TestOwnerAgentContinuityCalendarCutoffExcludesPriorWeek(t *testing.T) {
	dbPath := tempDB(t)
	m, err := NewManager(dbPath, 100, 30*time.Minute, 4, "truncate", WithPersistence(true), WithResumeAfterIdle(true))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	s, err := m.Create("lumi", "discord", "channel-a", "owner-a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	oldMsg, err := m.AddMessage(s.ID, "user", "prior week topic", "")
	if err != nil {
		t.Fatalf("AddMessage old: %v", err)
	}
	newMsg, err := m.AddMessage(s.ID, "user", "current week topic", "")
	if err != nil {
		t.Fatalf("AddMessage new: %v", err)
	}
	weekStart := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	if _, err := m.db.Exec("UPDATE messages SET timestamp = ? WHERE id = ?", weekStart.Add(-time.Hour), oldMsg.ID); err != nil {
		t.Fatalf("backdate old message: %v", err)
	}
	if _, err := m.db.Exec("UPDATE messages SET timestamp = ? WHERE id = ?", weekStart.Add(time.Hour), newMsg.ID); err != nil {
		t.Fatalf("date new message: %v", err)
	}

	found, err := m.FindOwnerAgentSince("lumi", "discord", "channel-a", "owner-a", nil, weekStart, weekStart, 20)
	if err != nil {
		t.Fatalf("FindOwnerAgentSince: %v", err)
	}
	if messagesContain(found.Messages, "prior week topic") {
		t.Fatalf("prior week message leaked into current week recall: %#v", found.Messages)
	}
	if !messagesContain(found.Messages, "current week topic") {
		t.Fatalf("expected current week message, got %#v", found.Messages)
	}
}

func messagesContain(messages []Message, text string) bool {
	for _, msg := range messages {
		if strings.Contains(msg.Content, text) {
			return true
		}
	}
	return false
}

func TestList(t *testing.T) {
	dbPath := tempDB(t)
	m := newTestManager(t, dbPath)
	defer m.Close()

	_, err := m.Create("lumi", "discord", "ch1", "u1")
	if err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	_, err = m.Create("mango", "discord", "ch2", "u2")
	if err != nil {
		t.Fatalf("Create 2: %v", err)
	}

	ids := m.List()
	if len(ids) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(ids))
	}
}

// Helpers

type managerOption struct {
	maxContext int
}

type optionFunc func(*managerOption)

func WithMaxContext(n int) optionFunc {
	return func(o *managerOption) { o.maxContext = n }
}

func newTestManager(t *testing.T, dbPath string, opts ...optionFunc) *Manager {
	t.Helper()
	opt := managerOption{maxContext: 100}
	for _, o := range opts {
		o(&opt)
	}

	m, err := NewManager(dbPath, opt.maxContext, 30*time.Minute, 4, "truncate")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func tempDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "test_sessions.db")
}
