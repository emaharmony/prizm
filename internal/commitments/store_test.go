package commitments

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCommitmentRecord_IsValid(t *testing.T) {
	tests := []struct {
		name   string
		record CommitmentRecord
		want   bool
	}{
		{
			name: "valid record",
			record: CommitmentRecord{
				Kind:        KindCareCheckIn,
				Sensitivity: SensitivityPersonal,
				Source:      SourceInferredUser,
				Status:      StatusPending,
				Reason:      "Ema said he'd follow up tomorrow",
				Confidence:   0.85,
			},
			want: true,
		},
		{
			name: "invalid kind",
			record: CommitmentRecord{
				Kind:        "unknown",
				Sensitivity: SensitivityRoutine,
				Source:      SourceInferredUser,
				Status:      StatusPending,
				Reason:      "test",
				Confidence:   0.5,
			},
			want: false,
		},
		{
			name: "empty reason",
			record: CommitmentRecord{
				Kind:        KindOpenLoop,
				Sensitivity: SensitivityRoutine,
				Source:      SourceInferredUser,
				Status:      StatusPending,
				Reason:      "",
				Confidence:   0.5,
			},
			want: false,
		},
		{
			name: "confidence out of range",
			record: CommitmentRecord{
				Kind:        KindDeadlineCheck,
				Sensitivity: SensitivityRoutine,
				Source:      SourceInferredUser,
				Status:      StatusPending,
				Reason:      "test",
				Confidence:   1.5,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.record.IsValid(); got != tt.want {
				t.Errorf("IsValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStore_CRUD(t *testing.T) {
	// Use in-memory SQLite
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	if err := store.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Create
	r := CommitmentRecord{
		ID:            "test-001",
		Kind:          KindCareCheckIn,
		Sensitivity:   SensitivityPersonal,
		Source:        SourceInferredUser,
		Status:        StatusPending,
		Reason:        "Ema said he'd follow up tomorrow",
		SuggestedText:  "Hey Ema, did you get a chance to look into that thing we talked about?",
		Confidence:    0.85,
		EarliestDueMs: time.Now().Add(2 * time.Hour).UnixMilli(),
		LatestDueMs:   time.Now().Add(24 * time.Hour).UnixMilli(),
		AgentID:       "lumi",
		SessionKey:    "test-session",
		Channel:       "discord",
		SenderID:      "164169326142816256",
	}
	if err := store.Upsert(r); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// List pending
	pending, err := store.ListPending()
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].ID != "test-001" {
		t.Errorf("expected ID 'test-001', got %q", pending[0].ID)
	}

	// List due (should not be due yet — earliest is 2h from now)
	due, err := store.ListDue(time.Now())
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("expected 0 due now, got %d", len(due))
	}

	// List due (2h from now — should be due)
	due, err = store.ListDue(time.Now().Add(3 * time.Hour))
	if err != nil {
		t.Fatalf("list due future: %v", err)
	}
	if len(due) != 1 {
		t.Errorf("expected 1 due in 3h, got %d", len(due))
	}

	// Update status
	if err := store.UpdateStatus("test-001", StatusSent); err != nil {
		t.Fatalf("update status: %v", err)
	}
	pending, _ = store.ListPending()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after status update, got %d", len(pending))
	}

	// Dedupe
	has, err := store.HasDedupe("test-dedupe-key")
	if err != nil {
		t.Fatalf("has dedupe: %v", err)
	}
	if has {
		t.Error("expected no dedupe for non-existent key")
	}

	// Insert a record with dedupe key
	r2 := r
	r2.ID = "test-002"
	r2.DedupeKey = "test-dedupe-key"
	r2.Status = StatusPending
	store.Upsert(r2)
	has, _ = store.HasDedupe("test-dedupe-key")
	if !has {
		t.Error("expected dedupe for existing key")
	}
}

func TestStore_ExpireOld(t *testing.T) {
	db, _ := sql.Open("sqlite", ":memory:")
	defer db.Close()

	store := NewStore(db)
	store.Init()

	// Insert a commitment with a past due window
	r := CommitmentRecord{
		ID:            "expire-001",
		Kind:          KindDeadlineCheck,
		Sensitivity:   SensitivityRoutine,
		Source:        SourceInferredUser,
		Status:        StatusPending,
		Reason:        "old deadline",
		Confidence:    0.7,
		EarliestDueMs: time.Now().Add(-48 * time.Hour).UnixMilli(),
		LatestDueMs:   time.Now().Add(-24 * time.Hour).UnixMilli(),
		AgentID:       "lumi",
		SessionKey:    "test",
		Channel:       "discord",
	}
	store.Upsert(r)

	// Expire commitments older than 72 hours
	count, err := store.ExpireOld(time.Now(), 72)
	if err != nil {
		t.Fatalf("expire old: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 expired, got %d", count)
	}

	// Verify it's now expired
	pending, _ := store.ListPending()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after expire, got %d", len(pending))
	}
}