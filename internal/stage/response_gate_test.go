package stage

import (
	"testing"
)

func TestShouldRespond_ManagerRoom(t *testing.T) {
	// Manager-room always responds fully
	tests := []string{
		"hey",
		"+1",
		"lol",
		"what's the status of prizm?",
		"fix the bug",
		"",
	}
	for _, msg := range tests {
		result := ShouldRespond(msg, "manager-room")
		if result != RespondFully {
			t.Errorf("ShouldRespond(%q, manager-room) = %v, want RespondFully", msg, result)
		}
	}
}

func TestShouldRespond_BuildRoom(t *testing.T) {
	// Build-room always responds fully
	result := ShouldRespond("agent error report", "build-room")
	if result != RespondFully {
		t.Errorf("ShouldRespond in build-room = %v, want RespondFully", result)
	}
}

func TestShouldRespond_FunChannel(t *testing.T) {
	// Fun channel always responds fully
	result := ShouldRespond("haha nice one", "fun")
	if result != RespondFully {
		t.Errorf("ShouldRespond in fun = %v, want RespondFully", result)
	}
}

func TestShouldRespond_Questions(t *testing.T) {
	// Questions always get full responses
	questions := []string{
		"what's the status?",
		"how does this work?",
		"why did you do that?",
		"can you help me?",
		"should I deploy this?",
	}
	for _, q := range questions {
		result := ShouldRespond(q, "general")
		if result != RespondFully {
			t.Errorf("ShouldRespond(%q, general) = %v, want RespondFully", q, result)
		}
	}
}

func TestShouldRespond_TechIntent(t *testing.T) {
	// Tech messages always get full responses
	techMessages := []string{
		"the bug is in the tool loop",
		"let me check the code",
		"I'll fix the error",
		"review the PR",
		"deploy the new feature",
	}
	for _, msg := range techMessages {
		result := ShouldRespond(msg, "general")
		if result != RespondFully {
			t.Errorf("ShouldRespond(%q, general) = %v, want RespondFully", msg, result)
		}
	}
}

func TestShouldRespond_LowSignal(t *testing.T) {
	// Low-signal messages in unknown channels get light responses
	lowSignals := []string{
		"+1",
		"lol",
		"nice",
		"ok",
		"gg",
		"rip",
		"smh",
	}
	for _, msg := range lowSignals {
		result := ShouldRespond(msg, "general")
		if result != RespondLightly {
			t.Errorf("ShouldRespond(%q, general) = %v, want RespondLightly", msg, result)
		}
	}
}

func TestShouldRespond_ShortImportant(t *testing.T) {
	// Short but important messages get full responses if they match tech intent
	shortImportant := []string{
		"yes",
		"no",
		"stop",
		"done",
	}
	for _, msg := range shortImportant {
		result := ShouldRespond(msg, "general")
		// These are short but don't match tech intent or low-signal, so RespondLightly
		if result != RespondLightly {
			t.Errorf("ShouldRespond(%q, general) = %v, want RespondLightly", msg, result)
		}
	}

	// Short words that match tech intent get full responses
	shortTech := []string{
		"go",
		"fix",
		"add",
		"new",
	}
	for _, msg := range shortTech {
		result := ShouldRespond(msg, "general")
		if result != RespondFully {
			t.Errorf("ShouldRespond(%q, general) = %v, want RespondFully", msg, result)
		}
	}
}

func TestShouldRespond_Empty(t *testing.T) {
	result := ShouldRespond("", "general")
	if result != Skip {
		t.Errorf("ShouldRespond(empty, general) = %v, want Skip", result)
	}
}

func TestShouldRespond_Emoji(t *testing.T) {
	// Pure emoji messages get light responses
	emojiMessages := []string{
		"🔥🔥🔥",
		"👍",
		"😂😂",
	}
	for _, msg := range emojiMessages {
		result := ShouldRespond(msg, "general")
		if result != RespondLightly {
			t.Errorf("ShouldRespond(%q, general) = %v, want RespondLightly", msg, result)
		}
	}
}

func TestShouldRespond_MixedEmojiText(t *testing.T) {
	// Messages with mixed emoji and text get full responses
	result := ShouldRespond("check out this code 🔥", "general")
	if result != RespondFully {
		t.Errorf("ShouldRespond(mixed emoji+text, general) = %v, want RespondFully", result)
	}
}

func TestShouldRespond_LongerMessages(t *testing.T) {
	// Longer messages in unknown channels get full responses
	result := ShouldRespond("I was thinking about the architecture and how we could refactor the pipeline", "general")
	if result != RespondFully {
		t.Errorf("ShouldRespond(longer message, general) = %v, want RespondFully", result)
	}
}

func TestContainsQuestion(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"what's the status?", true},
		{"how does this work?", true},
		{"why?", true},
		{"can you help me?", true},
		{"should I deploy this?", true},
		{"this is a statement", false},
		{"the bug is fixed", false},
		{"What time is it?", true},  // case insensitive
		{"HOW do I do this?", true}, // case insensitive
	}
	for _, tt := range tests {
		result := containsQuestion(tt.input)
		if result != tt.expected {
			t.Errorf("containsQuestion(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

func TestShouldRespond_ShortTechWords(t *testing.T) {
	// Short tech words should get full responses even at ≤4 chars
	techWords := []string{
		"new feature",
		"add this",
		"run the test",
		"fix it",
		"git status",
		"log in",
		"set config",
	}
	for _, msg := range techWords {
		result := ShouldRespond(msg, "general")
		if result != RespondFully {
			t.Errorf("ShouldRespond(%q, general) = %v, want RespondFully", msg, result)
		}
	}
}

func TestIsMostlyEmoji(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"🔥🔥🔥", true},
		{"👍", true},
		{"hello 🔥", false},
		{"just text", false},
		{"", false},
	}
	for _, tt := range tests {
		result := isMostlyEmoji(tt.input)
		if result != tt.expected {
			t.Errorf("isMostlyEmoji(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}
