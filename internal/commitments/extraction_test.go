package commitments

import (
	"testing"
	"time"
)

func TestParseExtraction_Valid(t *testing.T) {
	response := `{
  "items": [
    {
      "action": "commitment",
      "kind": "care_check_in",
      "sensitivity": "personal",
      "source": "inferred_user_context",
      "reason": "Ema said he'd look into the deployment issue tomorrow",
      "suggested_text": "Hey Ema, did you get a chance to look into the deployment issue?",
      "dedupe_key": "deploy-issue-followup",
      "confidence": 0.85,
      "due_window": {
        "earliest": "tomorrow",
        "latest": "in 2 days",
        "timezone": "America/New_York"
      }
    },
    {
      "action": "skip",
      "kind": "open_loop",
      "sensitivity": "routine",
      "source": "inferred_user_context",
      "reason": "Not a commitment",
      "confidence": 0.2,
      "due_window": {}
    }
  ]
}`

	items, err := ParseExtraction(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item (skip filtered), got %d", len(items))
	}
	if items[0].Kind != "care_check_in" {
		t.Errorf("expected kind 'care_check_in', got %s", items[0].Kind)
	}
	if items[0].Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", items[0].Confidence)
	}
}

func TestParseExtraction_Empty(t *testing.T) {
	response := `{"items": []}`
	items, err := ParseExtraction(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestParseExtraction_MarkdownFence(t *testing.T) {
	response := "```json\n{\"items\": [{\"action\": \"commitment\", \"kind\": \"deadline_check\", \"sensitivity\": \"routine\", \"source\": \"agent_promise\", \"reason\": \"test\", \"confidence\": 0.9, \"due_window\": {\"earliest\": \"tomorrow\"}}]}\n```"
	items, err := ParseExtraction(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestItemToRecord(t *testing.T) {
	item := ExtractionItem{
		Action:         "commitment",
		Kind:          "event_check_in",
		Sensitivity:   "routine",
		Source:        "inferred_user_context",
		Reason:        "Follow up on PR review",
		SuggestedText: "Did the PR get reviewed?",
		DedupeKey:     "pr-review-followup",
		Confidence:    0.75,
		DueWindow: DueWindow{
			Earliest: "tomorrow",
			Latest:   "in 2 days",
			Timezone: "America/New_York",
		},
	}

	scope := CommitmentScope{
		AgentID:    "lumi",
		SessionKey: "test-session",
		Channel:    "discord",
		SenderID:   "164169326142816256",
	}

	now := time.Now()
	record, err := ItemToRecord(item, scope, now)
	if err != nil {
		t.Fatalf("conversion error: %v", err)
	}

	if record.Kind != KindEventCheckIn {
		t.Errorf("expected kind KindEventCheckIn, got %s", record.Kind)
	}
	if record.Status != StatusPending {
		t.Errorf("expected status pending, got %s", record.Status)
	}
	if record.AgentID != "lumi" {
		t.Errorf("expected agent 'lumi', got %s", record.AgentID)
	}
	if record.EarliestDueMs <= now.UnixMilli() {
		t.Error("expected earliest due to be in the future")
	}
	if record.LatestDueMs <= record.EarliestDueMs {
		t.Error("expected latest due to be after earliest due")
	}
}

func TestItemToRecord_InvalidKind(t *testing.T) {
	item := ExtractionItem{
		Action: "commitment",
		Kind:   "unknown_kind",
		Sensitivity: "routine",
		Source: "inferred_user_context",
		Reason: "test",
		Confidence: 0.5,
		DueWindow: DueWindow{Earliest: "tomorrow"},
	}

	scope := CommitmentScope{AgentID: "lumi", SessionKey: "test", Channel: "discord"}
	_, err := ItemToRecord(item, scope, time.Now())
	if err == nil {
		t.Error("expected error for invalid kind")
	}
}

func TestParseTime_Relative(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		input string
		valid bool
	}{
		{"tomorrow", true},
		{"next week", true},
		{"in 3 days", true},
		{"in 2 hours", true},
		{"", false},
		{"2026-07-28T12:00:00Z", true}, // ISO 8601
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			ms := parseTime(tt.input, now)
			if tt.valid && ms == 0 {
				t.Errorf("expected non-zero for %q", tt.input)
			}
			if !tt.valid && ms != 0 {
				t.Errorf("expected zero for %q", tt.input)
			}
		})
	}
}

func TestExtractionPrompt(t *testing.T) {
	prompt := ExtractionPrompt("I'll check on that tomorrow", "Great, let me know what you find!", "America/New_York")
	if !contains(prompt, "I'll check on that tomorrow") {
		t.Error("expected user text in prompt")
	}
	if !contains(prompt, "America/New_York") {
		t.Error("expected timezone in prompt")
	}
	if !contains(prompt, "commitment") {
		t.Error("expected 'commitment' in prompt")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}