package memory

import (
	"context"
	"strings"
	"testing"
)

// TestAutoExtract_GateRejects verifies that AutoExtract returns nil when the gate
// rejects the turn, without calling the store.
func TestAutoExtract_GateRejects(t *testing.T) {
	// We can't easily mock the GateExtractor (it calls HTTP), but we can
	// test with a nil gate to verify the nil-safety path.
	a := &AutoExtractor{Gate: nil, Store: nil, Events: nil}
	if err := a.AutoExtract(context.Background(), ConversationTurn{
		UserMessage:   "hello",
		AgentResponse: "hi",
	}); err != nil {
		t.Errorf("expected nil error for nil autoextractor, got %v", err)
	}
}

// TestBuildTurnText verifies the conversation turn concatenation.
func TestBuildTurnText(t *testing.T) {
	turn := ConversationTurn{
		UserMessage:   "What is Go?",
		AgentResponse:  "Go is a programming language.",
		ToolSummaries:  []string{"[Tool: web_search] success"},
		AgentID:        "lumi",
		SessionID:      "sess-1",
	}
	text := buildTurnText(turn)
	if !strings.Contains(text, "User: What is Go?") {
		t.Error("expected User message in text")
	}
	if !strings.Contains(text, "Agent: Go is a programming language.") {
		t.Error("expected Agent response in text")
	}
	if !strings.Contains(text, "Tools used: [Tool: web_search] success") {
		t.Error("expected tool summaries in text")
	}
}

// TestBuildTurnText_Empty verifies empty turn produces empty text.
func TestBuildTurnText_Empty(t *testing.T) {
	text := buildTurnText(ConversationTurn{})
	if text != "" {
		t.Errorf("expected empty text for empty turn, got %q", text)
	}
}

// TestBuildTurnText_PartialTurn verifies that a turn with only a user message works.
func TestBuildTurnText_PartialTurn(t *testing.T) {
	text := buildTurnText(ConversationTurn{UserMessage: "hello"})
	if !strings.Contains(text, "User: hello") {
		t.Errorf("expected 'User: hello' in text, got %q", text)
	}
	if strings.Contains(text, "Agent:") {
		t.Error("did not expect Agent section for partial turn")
	}
}

// TestNormalizeCategory_NewTypes verifies the 4-type taxonomy.
func TestNormalizeCategory_NewTypes(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"user", "user"},
		{"feedback", "feedback"},
		{"project", "project"},
		{"reference", "reference"},
		{"USER", "user"},
		{"User Preferences", "user"},
	}
	for _, tt := range tests {
		got := normalizeCategory(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeCategory(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// TestNormalizeCategory_BackwardCompat verifies old types map to new.
func TestNormalizeCategory_BackwardCompat(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"decision", "project"},
		{"preference", "user"},
		{"observation", "feedback"},
		{"fact", "reference"},
	}
	for _, tt := range tests {
		got := normalizeCategory(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeCategory(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// TestTruncateText verifies the truncateText helper.
func TestTruncateText(t *testing.T) {
	if got := truncateText("hello", 10); got != "hello" {
		t.Errorf("truncateText(hello, 10) = %q, want %q", got, "hello")
	}
	if got := truncateText("hello world", 5); got != "hello..." {
		t.Errorf("truncateText(hello world, 5) = %q, want %q", got, "hello...")
	}
}