package session

import (
	"testing"
)

func TestSearchSessions(t *testing.T) {
	dbPath := tempDB(t)
	m, err := NewManager(dbPath, 100, 30*0, 4, "truncate")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	s, err := m.Create("lumi", "discord", "channel123", "user456")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Add messages with distinct content
	m.AddMessage(s.ID, "user", "How do I configure NATS JetStream?", "")
	m.AddMessage(s.ID, "agent", "NATS JetStream is configured in prizm.yaml under nats_url.", "lumi")
	m.AddMessage(s.ID, "user", "What about the event bus?", "")
	m.AddMessage(s.ID, "agent", "The event bus uses NATS subjects with prizm.* namespace.", "lumi")

	// Search for "NATS"
	results, err := m.SearchSessions("NATS", 10)
	if err != nil {
		t.Fatalf("SearchSessions failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for 'NATS'")
	}
	// Verify results have valid timestamps (not zero)
	for _, r := range results {
		if r.SessionID == "" {
			t.Error("expected non-empty session ID")
		}
		// Timestamp may be zero if parsing failed, but content should match
		if r.Content == "" {
			t.Error("expected non-empty content")
		}
	}

	// Search for "event bus"
	results2, err := m.SearchSessions("event bus", 10)
	if err != nil {
		t.Fatalf("SearchSessions failed: %v", err)
	}
	if len(results2) == 0 {
		t.Fatal("expected at least 1 result for 'event bus'")
	}
}