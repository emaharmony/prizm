package commitments

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestFormatPendingForPrompt(t *testing.T) {
	now := time.Now()
	records := []CommitmentRecord{
		{
			Kind:          KindCareCheckIn,
			Reason:        "Ema said he'd look into the deployment issue",
			SuggestedText:  "Did you get a chance to look into the deployment issue?",
			EarliestDueMs: now.Add(48 * time.Hour).UnixMilli(),
			Confidence:    0.85,
		},
		{
			Kind:          KindDeadlineCheck,
			Reason:        "PR review deadline approaching",
			SuggestedText:  "The PR review is due soon — should I check the status?",
			EarliestDueMs: now.Add(-1 * time.Hour).UnixMilli(), // 1h overdue
			Confidence:    0.9,
		},
	}

	prompt := FormatPendingForPrompt(records, now)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "Pending Commitments") {
		t.Error("expected 'Pending Commitments' header")
	}
	if !strings.Contains(prompt, "deployment issue") {
		t.Error("expected first commitment reason")
	}
	if !strings.Contains(prompt, "PR review") {
		t.Error("expected second commitment reason")
	}
	if !strings.Contains(prompt, "overdue") {
		t.Error("expected overdue indicator for past-due commitment")
	}
	if !strings.Contains(prompt, "in 1 days") {
		t.Errorf("expected future due time containing 'in 2', got prompt: %s", prompt)
	}
}

func TestFormatPendingForPrompt_Empty(t *testing.T) {
	prompt := FormatPendingForPrompt(nil, time.Now())
	if prompt != "" {
		t.Error("expected empty prompt for no records")
	}
}

func TestDeliver(t *testing.T) {
	db, _ := sql.Open("sqlite", ":memory:")
	defer db.Close()

	store := NewStore(db)
	store.Init()

	now := time.Now()

	// Insert a due commitment
	r := CommitmentRecord{
		ID:            "deliver-001",
		Kind:          KindOpenLoop,
		Sensitivity:   SensitivityRoutine,
		Source:        SourceAgentPromise,
		Status:        StatusPending,
		Reason:        "Follow up on the architecture discussion",
		SuggestedText:  "Did we settle on the architecture approach?",
		Confidence:    0.8,
		EarliestDueMs: now.Add(-1 * time.Hour).UnixMilli(), // 1h ago — due now
		LatestDueMs:   now.Add(23 * time.Hour).UnixMilli(),
		AgentID:       "lumi",
		SessionKey:    "test-session",
		Channel:       "discord",
	}
	store.Upsert(r)

	scope := CommitmentScope{
		AgentID:    "lumi",
		SessionKey: "test-session",
		Channel:    "discord",
	}

	cfg := DefaultDeliveryConfig()
	prompt, err := Deliver(store, scope, cfg, now)
	if err != nil {
		t.Fatalf("deliver error: %v", err)
	}
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "architecture discussion") {
		t.Error("expected commitment text in prompt")
	}

	// Verify it was marked as sent
	pending, _ := store.ListPending()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after delivery, got %d", len(pending))
	}
}

func TestDeliver_NotYetDue(t *testing.T) {
	db, _ := sql.Open("sqlite", ":memory:")
	defer db.Close()
	store := NewStore(db)
	store.Init()

	now := time.Now()

	// Insert a commitment due in the future
	r := CommitmentRecord{
		ID:            "future-001",
		Kind:          KindDeadlineCheck,
		Sensitivity:   SensitivityRoutine,
		Source:        SourceInferredUser,
		Status:        StatusPending,
		Reason:        "Deadline next week",
		Confidence:    0.7,
		EarliestDueMs: now.Add(7 * 24 * time.Hour).UnixMilli(),
		LatestDueMs:   now.Add(8 * 24 * time.Hour).UnixMilli(),
		AgentID:       "lumi",
		SessionKey:    "test",
		Channel:       "discord",
	}
	store.Upsert(r)

	scope := CommitmentScope{AgentID: "lumi", SessionKey: "test", Channel: "discord"}
	cfg := DefaultDeliveryConfig()

	prompt, err := Deliver(store, scope, cfg, now)
	if err != nil {
		t.Fatalf("deliver error: %v", err)
	}
	if prompt != "" {
		t.Error("expected empty prompt for not-yet-due commitment")
	}
}

func TestDeliver_Disabled(t *testing.T) {
	cfg := DefaultDeliveryConfig()
	cfg.Enabled = false
	prompt, err := Deliver(nil, CommitmentScope{}, cfg, time.Now())
	if err != nil {
		t.Fatalf("deliver error: %v", err)
	}
	if prompt != "" {
		t.Error("expected empty prompt when disabled")
	}
}