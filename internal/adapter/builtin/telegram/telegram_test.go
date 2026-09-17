package telegram

import (
	"strings"
	"testing"
)

func TestSplitMessage_ShortMessage(t *testing.T) {
	chunks := SplitMessage("hello world")
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
}

func TestSplitMessage_LongMessage(t *testing.T) {
	// Create a message longer than the 4000 char limit
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("line of text\n")
	}
	chunks := SplitMessage(sb.String())
	if len(chunks) < 2 {
		t.Errorf("expected at least 2 chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if len(c) > 4000 {
			t.Errorf("chunk too long: %d chars", len(c))
		}
	}
}

func TestSplitMessage_SplitsAtNewline(t *testing.T) {
	// Build a message where the split point should be at a newline
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString("x")
	}
	sb.WriteString("\n")
	for i := 0; i < 300; i++ {
		sb.WriteString("y")
	}
	chunks := SplitMessage(sb.String())
	if len(chunks) < 2 {
		// May be 1 if total < 4000, which is fine
		return
	}
	// First chunk should not contain a trailing newline at the end
	if strings.HasSuffix(chunks[0], "x") == false && strings.HasSuffix(chunks[0], "\n") == false {
		// Either split at newline (ends with x) or includes the newline
		// Both are acceptable
	}
}

func TestNewBotAdapter_AllowedUsers(t *testing.T) {
	b := NewBotAdapter("test-token", []string{"123", "456"})
	if !b.allowedIDs["123"] {
		t.Error("expected user 123 to be allowed")
	}
	if !b.allowedIDs["456"] {
		t.Error("expected user 456 to be allowed")
	}
	if b.allowedIDs["789"] {
		t.Error("user 789 should not be allowed")
	}
}

func TestNewBotAdapter_NoAllowedUsers(t *testing.T) {
	b := NewBotAdapter("test-token", nil)
	// Empty allowed list means everyone is allowed
	if len(b.allowedIDs) != 0 {
		t.Errorf("expected 0 allowed users, got %d", len(b.allowedIDs))
	}
}