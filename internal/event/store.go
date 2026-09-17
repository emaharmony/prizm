// Package event provides Prizm's event store and event types.
//
// V14d adds the EventStore interface with a SQLite implementation. Events are
// the source of truth in Prizm — they need ACID guarantees that flat files
// can't provide. SQLite WAL mode gives us concurrent reads with serialized
// writes, which is the correct tradeoff for Prizm's workload.
//
// JSONL remains the interchange format. SQLite is the primary store, but
// `prizm db export <run_id>` dumps SQLite to JSONL for human readability
// and debugging.
package event

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // SQLite driver
)

// EventStore persists and queries events with ACID guarantees.
type EventStore interface {
	// Store persists an event atomically.
	Store(ctx context.Context, event Event) error

	// StoreBatch persists multiple events atomically (single transaction).
	StoreBatch(ctx context.Context, events []Event) error

	// Query retrieves events matching the filter.
	Query(ctx context.Context, filter EventFilter) ([]Event, error)

	// Close releases resources.
	Close() error
}

// EventFilter controls which events to retrieve.
type EventFilter struct {
	RunID     string     // Filter by run ID
	Type      string     // Filter by event type (prefix match)
	AfterID   string     // Events after this ID (cursor-based pagination)
	Limit     int        // Maximum events to return (default 100)
	StartTime *time.Time // Events after this time
	EndTime   *time.Time // Events before this time
}

// SQLiteEventStore implements EventStore using SQLite in WAL mode.
type SQLiteEventStore struct {
	db *sql.DB
}

// NewSQLiteEventStore opens or creates a SQLite event store at the given path.
// It enables WAL mode for concurrent reads and creates the events table if needed.
func NewSQLiteEventStore(dbPath string) (*SQLiteEventStore, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("event store: create dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("event store: open: %w", err)
	}

	// Enable WAL mode for concurrent reads
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("event store: enable WAL: %w", err)
	}

	// Create events table
	createTable := `
	CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		type TEXT NOT NULL,
		timestamp TEXT NOT NULL,
		correlation_id TEXT DEFAULT '',
		parent_id TEXT DEFAULT '',
		payload TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_events_run_id ON events(run_id);
	CREATE INDEX IF NOT EXISTS idx_events_type ON events(type);
	CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);
	CREATE INDEX IF NOT EXISTS idx_events_source ON events(run_id, type);
	CREATE INDEX IF NOT EXISTS idx_events_correlation ON events(correlation_id);
	CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at);
	`
	if _, err := db.Exec(createTable); err != nil {
		db.Close()
		return nil, fmt.Errorf("event store: create table: %w", err)
	}

	return &SQLiteEventStore{db: db}, nil
}

// Store persists an event atomically.
func (s *SQLiteEventStore) Store(ctx context.Context, event Event) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("event store: marshal payload: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO events (id, run_id, type, timestamp, correlation_id, parent_id, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		event.ID, event.Metadata.RunID, event.Type, event.Timestamp,
		event.CorrelationID, event.ParentID, string(payload),
	)
	if err != nil {
		return fmt.Errorf("event store: insert: %w", err)
	}

	return nil
}

// StoreBatch persists multiple events atomically (single transaction).
func (s *SQLiteEventStore) StoreBatch(ctx context.Context, events []Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("event store: begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO events (id, run_id, type, timestamp, correlation_id, parent_id, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("event store: prepare: %w", err)
	}
	defer stmt.Close()

	for _, event := range events {
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return fmt.Errorf("event store: marshal payload for %s: %w", event.ID, err)
		}

		_, err = stmt.ExecContext(ctx,
			event.ID, event.Metadata.RunID, event.Type, event.Timestamp,
			event.CorrelationID, event.ParentID, string(payload),
		)
		if err != nil {
			return fmt.Errorf("event store: insert %s: %w", event.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("event store: commit: %w", err)
	}

	return nil
}

// Query retrieves events matching the filter.
func (s *SQLiteEventStore) Query(ctx context.Context, filter EventFilter) ([]Event, error) {
	query := "SELECT id, run_id, type, timestamp, correlation_id, parent_id, payload FROM events WHERE 1=1"
	args := []any{}

	if filter.RunID != "" {
		query += " AND run_id = ?"
		args = append(args, filter.RunID)
	}
	if filter.Type != "" {
		query += " AND type LIKE ?"
		args = append(args, filter.Type+"%")
	}
	if filter.AfterID != "" {
		query += " AND id > ?"
		args = append(args, filter.AfterID)
	}
	if filter.StartTime != nil {
		query += " AND timestamp >= ?"
		args = append(args, filter.StartTime.Format(time.RFC3339Nano))
	}
	if filter.EndTime != nil {
		query += " AND timestamp <= ?"
		args = append(args, filter.EndTime.Format(time.RFC3339Nano))
	}

	// ORDER BY id, not timestamp: event.Event.ID is a ULID (lexicographically
	// sortable and unique), so this is the correct cursor field. Two events
	// with colliding timestamps (same millisecond) still sort deterministically
	// by ID; ordering by timestamp alone does not.
	query += " ORDER BY id ASC"

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 10000 {
		limit = 10000 // cap at 10k to prevent unbounded queries
	}
	query += " LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("event store: query: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var timestamp, payloadStr string
		var correlationID, parentID sql.NullString
		var runID string

		err := rows.Scan(&event.ID, &runID, &event.Type, &timestamp,
			&correlationID, &parentID, &payloadStr)
		if err != nil {
			return nil, fmt.Errorf("event store: scan: %w", err)
		}

		event.Timestamp = timestamp
		event.CorrelationID = correlationID.String
		event.ParentID = parentID.String
		event.Metadata.RunID = runID

		if err := json.Unmarshal([]byte(payloadStr), &event.Payload); err != nil {
			// Try raw string fallback
			event.Payload = map[string]any{"raw": payloadStr}
		}

		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("event store: rows: %w", err)
	}

	return events, nil
}

// Close releases database resources.
func (s *SQLiteEventStore) Close() error {
	return s.db.Close()
}
