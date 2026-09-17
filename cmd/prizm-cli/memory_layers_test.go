package main

import (
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/session"
)

func TestEphemeralMemoryLayerFeedsTextAndNativeChat(t *testing.T) {
	cfg := orchestrator.DefaultConfig()
	cc := &conversationContext{
		cfg:              cfg,
		staticSystemText: "## Static\n",
		staticSystemChat: "## Static\n",
	}
	agentCfg := &orchestrator.AgentConfig{ID: "lumi", Role: "assistant"}
	sess := &session.Session{
		ID:        "sess-1",
		AgentID:   "lumi",
		StartedAt: time.Now().UTC(),
		Messages: []session.Message{
			{ID: "msg-1", Role: "user", Content: "the current local topic is blue widgets", Timestamp: time.Now().UTC()},
		},
	}

	withMemory := cloneSessionWithSystemMemory(sess, "Remembrance says the old project was red widgets.")
	textPrompt := cc.buildPrompt(withMemory, agentCfg, "", nil)
	chatMessages := cc.buildMessages(withMemory, agentCfg)

	if !strings.Contains(textPrompt, "Remembrance says the old project was red widgets.") {
		t.Fatalf("text prompt did not include memory layer: %s", textPrompt)
	}
	if !strings.Contains(textPrompt, "the current local topic is blue widgets") {
		t.Fatalf("text prompt did not include exact local transcript: %s", textPrompt)
	}

	foundMemory := false
	foundLocal := false
	for _, msg := range chatMessages {
		if strings.Contains(msg.Content, "Remembrance says the old project was red widgets.") {
			foundMemory = true
		}
		if strings.Contains(msg.Content, "the current local topic is blue widgets") {
			foundLocal = true
		}
	}
	if !foundMemory || !foundLocal {
		t.Fatalf("native chat messages missing memory=%v local=%v: %#v", foundMemory, foundLocal, chatMessages)
	}
}

func TestEphemeralMemoryLayerKeepsLocalTranscriptAfterRemembrance(t *testing.T) {
	cfg := orchestrator.DefaultConfig()
	cc := &conversationContext{
		cfg:              cfg,
		staticSystemText: "## Static\n",
		staticSystemChat: "## Static\n",
	}
	agentCfg := &orchestrator.AgentConfig{ID: "lumi", Role: "assistant"}
	sess := &session.Session{
		ID:        "sess-1",
		AgentID:   "lumi",
		StartedAt: time.Now().UTC(),
		Messages: []session.Message{
			{ID: "msg-1", Role: "user", Content: "the current local topic is blue widgets", Timestamp: time.Now().UTC()},
		},
	}

	withMemory := cloneSessionWithSystemMemory(sess, "Remembrance says the stale topic was red widgets.")
	textPrompt := cc.buildPrompt(withMemory, agentCfg, "", nil)
	remIdx := strings.Index(textPrompt, "stale topic was red widgets")
	localIdx := strings.Index(textPrompt, "current local topic is blue widgets")
	if remIdx < 0 {
		t.Fatalf("expected Remembrance memory in prompt: %s", textPrompt)
	}
	if localIdx < 0 {
		t.Fatalf("expected local transcript in prompt: %s", textPrompt)
	}
	// V79: Cache-safe injection appends system-reminder to the user message,
	// so both local and Remembrance content are in the same message block.
	// The user's original content comes first, then the system-reminder.
	// Verify the system-reminder tag is present.
	if !strings.Contains(textPrompt, "<system-reminder>") {
		t.Fatalf("expected <system-reminder> tag in prompt: %s", textPrompt)
	}
	if !strings.Contains(textPrompt, "</system-reminder>") {
		t.Fatalf("expected closing </system-reminder> tag in prompt: %s", textPrompt)
	}
}

func TestContinuityWindowCalendarWeekStartsMonday(t *testing.T) {
	cfg := orchestrator.DefaultConfig()
	cfg.Sessions.RecallWindowMode = "calendar_week"
	cfg.Sessions.RecallTimezone = "UTC"

	now := time.Date(2026, 6, 12, 15, 30, 0, 0, time.UTC) // Friday
	window := continuityWindowFor(cfg, now)
	want := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	if !window.recallStart.Equal(want) || !window.weekStart.Equal(want) {
		t.Fatalf("window = %#v, want %s", window, want)
	}
}

func TestLocalSummaryCurationHeuristics(t *testing.T) {
	if !shouldIngestLocalSummary("Local rolling summary\n- user: remember that Prizm prefers owner-agent continuity") {
		t.Fatal("expected durable preference summary to be curated")
	}
	if shouldIngestLocalSummary("Local rolling summary\n- user: hi\n- lumi: hello") {
		t.Fatal("ordinary greeting summary should not be curated")
	}
	if got := localSummaryImportance("we decided to use calendar week recall"); got < 0.8 {
		t.Fatalf("decision summary importance = %v, want high", got)
	}
}
