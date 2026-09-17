package commitments

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store persists commitments to SQLite.
type Store struct {
	db  *sql.DB
	mu  sync.Mutex
	now func() time.Time
}

// NewStore creates a new commitment store from an existing DB connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// NewStoreFromPath opens a SQLite database at the given path and returns
// an initialized store. The caller is responsible for closing the DB.
func NewStoreFromPath(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open commitments db: %w", err)
	}
	store := &Store{db: db, now: time.Now}
	if err := store.Init(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init commitments: %w", err)
	}
	return store, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Init creates the commitments table if it doesn't exist.
func (s *Store) Init() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS commitments (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			sensitivity TEXT NOT NULL,
			source TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			reason TEXT NOT NULL,
			suggested_text TEXT DEFAULT '',
			dedupe_key TEXT DEFAULT '',
			confidence REAL DEFAULT 0,
			earliest_due_ms INTEGER DEFAULT 0,
			latest_due_ms INTEGER DEFAULT 0,
			timezone TEXT DEFAULT '',
			agent_id TEXT NOT NULL,
			session_key TEXT NOT NULL,
			channel TEXT NOT NULL,
			sender_id TEXT DEFAULT '',
			source_message_id TEXT DEFAULT '',
			source_run_id TEXT DEFAULT '',
			created_at_ms INTEGER DEFAULT 0,
			updated_at_ms INTEGER DEFAULT 0,
			expires_at_ms INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_commitments_status ON commitments(status);
		CREATE INDEX IF NOT EXISTS idx_commitments_due ON commitments(earliest_due_ms);
		CREATE INDEX IF NOT EXISTS idx_commitments_session ON commitments(session_key);
	`)
	return err
}

// Upsert inserts or replaces a commitment.
func (s *Store) Upsert(r CommitmentRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.CreatedAtMs == 0 {
		r.CreatedAtMs = s.now().UnixMilli()
	}
	r.UpdatedAtMs = s.now().UnixMilli()

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO commitments
		(id, kind, sensitivity, source, status, reason, suggested_text, dedupe_key,
		 confidence, earliest_due_ms, latest_due_ms, timezone, agent_id, session_key,
		 channel, sender_id, source_message_id, source_run_id, created_at_ms, updated_at_ms, expires_at_ms)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, r.ID, r.Kind, r.Sensitivity, r.Source, r.Status, r.Reason, r.SuggestedText,
		r.DedupeKey, r.Confidence, r.EarliestDueMs, r.LatestDueMs, r.Timezone,
		r.AgentID, r.SessionKey, r.Channel, r.SenderID, r.SourceMessageID,
		r.SourceRunID, r.CreatedAtMs, r.UpdatedAtMs, r.ExpiresAtMs)
	return err
}

// ListPending returns all commitments with status 'pending'.
func (s *Store) ListPending() ([]CommitmentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT id, kind, sensitivity, source, status, reason,
		suggested_text, dedupe_key, confidence, earliest_due_ms, latest_due_ms, timezone,
		agent_id, session_key, channel, sender_id, source_message_id, source_run_id,
		created_at_ms, updated_at_ms, expires_at_ms
		FROM commitments WHERE status = 'pending' ORDER BY earliest_due_ms ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// ListDue returns pending commitments whose due window has arrived.
func (s *Store) ListDue(now time.Time) ([]CommitmentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	nowMs := now.UnixMilli()
	rows, err := s.db.Query(`SELECT id, kind, sensitivity, source, status, reason,
		suggested_text, dedupe_key, confidence, earliest_due_ms, latest_due_ms, timezone,
		agent_id, session_key, channel, sender_id, source_message_id, source_run_id,
		created_at_ms, updated_at_ms, expires_at_ms
		FROM commitments WHERE status = 'pending' AND earliest_due_ms <= ?
		ORDER BY earliest_due_ms ASC`, nowMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// ListPendingForScope returns pending commitments for a specific conversation scope.
func (s *Store) ListPendingForScope(scope CommitmentScope) ([]CommitmentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT id, kind, sensitivity, source, status, reason,
		suggested_text, dedupe_key, confidence, earliest_due_ms, latest_due_ms, timezone,
		agent_id, session_key, channel, sender_id, source_message_id, source_run_id,
		created_at_ms, updated_at_ms, expires_at_ms
		FROM commitments WHERE status = 'pending' AND agent_id = ? AND session_key = ?
		ORDER BY earliest_due_ms ASC`, scope.AgentID, scope.SessionKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// UpdateStatus changes the status of a commitment.
func (s *Store) UpdateStatus(id string, status CommitmentStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`UPDATE commitments SET status = ?, updated_at_ms = ? WHERE id = ?`,
		status, s.now().UnixMilli(), id)
	return err
}

// ExpireOld marks commitments past their due window as expired.
func (s *Store) ExpireOld(now time.Time, maxAgeHours int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	nowMs := now.UnixMilli()
	result, err := s.db.Exec(`UPDATE commitments SET status = 'expired', updated_at_ms = ?
		WHERE status = 'pending' AND latest_due_ms > 0 AND latest_due_ms < ?`,
		s.now().UnixMilli(), nowMs)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	return int(count), nil
}

// HasDedupe checks if a commitment with the same dedupe key exists.
func (s *Store) HasDedupe(dedupeKey string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if dedupeKey == "" {
		return false, nil
	}
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM commitments WHERE dedupe_key = ? AND status = 'pending'`,
		dedupeKey).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func scanRows(rows *sql.Rows) ([]CommitmentRecord, error) {
	var records []CommitmentRecord
	for rows.Next() {
		var r CommitmentRecord
		err := rows.Scan(&r.ID, &r.Kind, &r.Sensitivity, &r.Source, &r.Status, &r.Reason,
			&r.SuggestedText, &r.DedupeKey, &r.Confidence, &r.EarliestDueMs, &r.LatestDueMs,
			&r.Timezone, &r.AgentID, &r.SessionKey, &r.Channel, &r.SenderID,
			&r.SourceMessageID, &r.SourceRunID, &r.CreatedAtMs, &r.UpdatedAtMs, &r.ExpiresAtMs)
		if err != nil {
			return nil, fmt.Errorf("scan commitment: %w", err)
		}
		records = append(records, r)
	}
	return records, nil
}

// ToJSON serializes a commitment record for logging/events.
func (r CommitmentRecord) ToJSON() string {
	b, _ := json.Marshal(r)
	return string(b)
}