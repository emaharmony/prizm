package slack

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

func TestNewBotAdapter_AllowedUsers(t *testing.T) {
	b := NewBotAdapter("test-token", []string{"U123", "U456"})
	if !b.allowedIDs["U123"] {
		t.Error("expected user U123 to be allowed")
	}
	if b.allowedIDs["U999"] {
		t.Error("user U999 should not be allowed")
	}
}

func TestBuildButtonBlocks(t *testing.T) {
	buttons := []MessageButton{
		{Label: "Approve", ActionID: "approve"},
		{Label: "Reject", ActionID: "reject"},
	}
	blocks := buildButtonBlocks(buttons)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	elements, ok := blocks[0]["elements"].([]map[string]any)
	if !ok || len(elements) != 2 {
		t.Fatalf("expected 2 elements, got %v", blocks[0]["elements"])
	}
}

func TestSplitMessage_LongMessage(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		sb.WriteString("abcdefghij\n") // 11 chars * 5000 = 55000 chars
	}
	chunks := SplitMessage(sb.String())
	if len(chunks) < 2 {
		t.Errorf("expected at least 2 chunks, got %d", len(chunks))
	}
}