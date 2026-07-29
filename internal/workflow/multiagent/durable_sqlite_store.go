package multiagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/emaharmony/prism/internal/event"
	prismsqlite "github.com/emaharmony/prism/internal/sqlite"
)

// SQLiteDurableRunStore persists multi-agent checkpoints in Prism's existing
// SQLite infrastructure. The caller should pass the normal Prism database path;
// this store creates only package-owned tables.
type SQLiteDurableRunStore struct {
	db *sql.DB
}

// NewSQLiteDurableRunStore opens or creates the package-owned durable tables.
func NewSQLiteDurableRunStore(dbPath string) (*SQLiteDurableRunStore, error) {
	if dbPath == "" {
		return nil, errors.New("multiagent: sqlite database path is required")
	}
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("multiagent store: create directory: %w", err)
		}
	}
	db, err := sql.Open(
		prismsqlite.DriverName,
		dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
	)
	if err != nil {
		return nil, fmt.Errorf("multiagent store: open: %w", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("multiagent store: enable WAL: %w", err)
	}
	const schema = `
CREATE TABLE IF NOT EXISTS multiagent_runs (
    run_id TEXT PRIMARY KEY,
    schema_version INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    record_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS multiagent_outbox (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    run_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    event_json TEXT NOT NULL,
    published_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_multiagent_outbox_pending
    ON multiagent_outbox(run_id, published_at, sequence);
CREATE TABLE IF NOT EXISTS multiagent_cancellations (
    run_id TEXT PRIMARY KEY,
    reason TEXT NOT NULL,
    requested_at TEXT NOT NULL
);
`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("multiagent store: create schema: %w", err)
	}
	return &SQLiteDurableRunStore{db: db}, nil
}

// Create atomically inserts revision one and its lifecycle events.
func (s *SQLiteDurableRunStore) Create(
	ctx context.Context,
	record DurableRun,
	events []event.Event,
) (DurableRun, error) {
	record.Revision = 1
	data, err := json.Marshal(record)
	if err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: marshal create: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: begin create: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
INSERT INTO multiagent_runs(run_id, schema_version, revision, record_json, updated_at)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(run_id) DO NOTHING`,
		record.State.RunID,
		record.SchemaVersion,
		record.Revision,
		string(data),
		record.State.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: insert run: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: create rows affected: %w", err)
	}
	if affected != 1 {
		return DurableRun{}, fmt.Errorf("%w: %s", ErrRunExists, record.State.RunID)
	}
	if err := enqueueEvents(ctx, tx, record.State.RunID, record.Revision, events); err != nil {
		return DurableRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: commit create: %w", err)
	}
	return record, nil
}

// Load returns the latest committed run revision.
func (s *SQLiteDurableRunStore) Load(ctx context.Context, runID string) (DurableRun, error) {
	var data string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT record_json FROM multiagent_runs WHERE run_id = ?`,
		runID,
	).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return DurableRun{}, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	}
	if err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: load run: %w", err)
	}
	var record DurableRun
	if err := json.Unmarshal([]byte(data), &record); err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: decode run %q: %w", runID, err)
	}
	return record, nil
}

// Checkpoint atomically compare-and-swaps state and appends its outbox events.
func (s *SQLiteDurableRunStore) Checkpoint(
	ctx context.Context,
	expectedRevision int64,
	record DurableRun,
	events []event.Event,
) (DurableRun, error) {
	record.Revision = expectedRevision + 1
	data, err := json.Marshal(record)
	if err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: marshal checkpoint: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: begin checkpoint: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE multiagent_runs
SET schema_version = ?, revision = ?, record_json = ?, updated_at = ?
WHERE run_id = ? AND revision = ?`,
		record.SchemaVersion,
		record.Revision,
		string(data),
		record.State.UpdatedAt.UTC().Format(time.RFC3339Nano),
		record.State.RunID,
		expectedRevision,
	)
	if err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: update checkpoint: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: checkpoint rows affected: %w", err)
	}
	if affected != 1 {
		return DurableRun{}, fmt.Errorf(
			"%w: run %s expected revision %d",
			ErrRevisionConflict,
			record.State.RunID,
			expectedRevision,
		)
	}
	if err := enqueueEvents(ctx, tx, record.State.RunID, record.Revision, events); err != nil {
		return DurableRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return DurableRun{}, fmt.Errorf("multiagent store: commit checkpoint: %w", err)
	}
	return record, nil
}

// PendingEvents loads unpublished events in deterministic checkpoint order.
func (s *SQLiteDurableRunStore) PendingEvents(
	ctx context.Context,
	runID string,
	limit int,
) ([]StoredEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT revision, event_json
FROM multiagent_outbox
WHERE run_id = ? AND published_at IS NULL
ORDER BY sequence
LIMIT ?`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("multiagent store: query outbox: %w", err)
	}
	defer rows.Close()
	var result []StoredEvent
	for rows.Next() {
		var revision int64
		var data string
		if err := rows.Scan(&revision, &data); err != nil {
			return nil, fmt.Errorf("multiagent store: scan outbox: %w", err)
		}
		var evt event.Event
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return nil, fmt.Errorf("multiagent store: decode outbox event: %w", err)
		}
		result = append(result, StoredEvent{Event: evt, Revision: revision})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("multiagent store: iterate outbox: %w", err)
	}
	return result, nil
}

// MarkEventsPublished acknowledges canonical event-store persistence.
func (s *SQLiteDurableRunStore) MarkEventsPublished(
	ctx context.Context,
	eventIDs []string,
) error {
	if len(eventIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("multiagent store: begin outbox ack: %w", err)
	}
	defer tx.Rollback()
	publishedAt := time.Now().UTC().Format(time.RFC3339Nano)
	for _, eventID := range eventIDs {
		if _, err := tx.ExecContext(ctx, `
UPDATE multiagent_outbox SET published_at = ?
WHERE event_id = ? AND published_at IS NULL`, publishedAt, eventID); err != nil {
			return fmt.Errorf("multiagent store: acknowledge event %q: %w", eventID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("multiagent store: commit outbox ack: %w", err)
	}
	return nil
}

// RequestCancellation persists an operator request without taking the
// execution claim. The active owner observes it before the next role.
func (s *SQLiteDurableRunStore) RequestCancellation(
	ctx context.Context,
	runID string,
	reason string,
) error {
	result, err := s.db.ExecContext(ctx, `
INSERT INTO multiagent_cancellations(run_id, reason, requested_at)
SELECT run_id, ?, ? FROM multiagent_runs WHERE run_id = ?
ON CONFLICT(run_id) DO UPDATE SET
    reason = excluded.reason,
    requested_at = excluded.requested_at
`, reason, time.Now().UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return fmt.Errorf("multiagent store: request cancellation: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("multiagent store: cancellation rows affected: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	}
	return nil
}

// CancellationRequest returns the latest persisted request, if any.
func (s *SQLiteDurableRunStore) CancellationRequest(
	ctx context.Context,
	runID string,
) (string, bool, error) {
	var reason string
	err := s.db.QueryRowContext(ctx,
		`SELECT reason FROM multiagent_cancellations WHERE run_id = ?`, runID,
	).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("multiagent store: load cancellation request: %w", err)
	}
	return reason, true, nil
}

// Close releases the SQLite connection.
func (s *SQLiteDurableRunStore) Close() error {
	return s.db.Close()
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func enqueueEvents(
	ctx context.Context,
	tx sqlExecutor,
	runID string,
	revision int64,
	events []event.Event,
) error {
	for _, evt := range events {
		data, err := json.Marshal(evt)
		if err != nil {
			return fmt.Errorf("multiagent store: marshal outbox event %q: %w", evt.ID, err)
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO multiagent_outbox(event_id, run_id, revision, event_json, created_at)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(event_id) DO NOTHING`,
			evt.ID,
			runID,
			revision,
			string(data),
			time.Now().UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("multiagent store: enqueue event %q: %w", evt.ID, err)
		}
	}
	return nil
}
