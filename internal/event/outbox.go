package event

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	prismsqlite "github.com/emaharmony/prizm/internal/sqlite"
)

type DeliveryState string

const (
	DeliveryPending        DeliveryState = "pending"
	DeliveryClaimed        DeliveryState = "claimed"
	DeliveryDelivered      DeliveryState = "delivered"
	DeliveryTerminalFailed DeliveryState = "terminal_failed"
)

type Delivery struct {
	Command      Command
	Subject      string
	State        DeliveryState
	Attempts     int
	LastError    string
	NextAttempt  time.Time
	LeaseExpires time.Time
}

type CommandPublisher interface {
	Publish(context.Context, string, []byte) error
}

type SQLiteOutbox struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteOutbox(path string) (*SQLiteOutbox, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open(prismsqlite.DriverName, path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("event outbox: open: %w", err)
	}
	s := &SQLiteOutbox{db: db, now: time.Now}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS event_outbox (
		idempotency_key TEXT PRIMARY KEY, event_id TEXT NOT NULL UNIQUE, subject TEXT NOT NULL,
		command_json BLOB NOT NULL, state TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '', next_attempt TEXT NOT NULL, lease_expires TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
		CREATE INDEX IF NOT EXISTS idx_event_outbox_claim ON event_outbox(state,next_attempt,lease_expires);
		CREATE TABLE IF NOT EXISTS command_outcomes (
			event_id TEXT PRIMARY KEY, delivery_key TEXT NOT NULL, command_event_id TEXT NOT NULL,
			run_id TEXT NOT NULL, correlation_id TEXT NOT NULL, sequence INTEGER NOT NULL,
			status TEXT NOT NULL, outcome_json BLOB NOT NULL, terminal INTEGER NOT NULL, consumed INTEGER NOT NULL DEFAULT 0,
			UNIQUE(delivery_key,sequence));
		CREATE UNIQUE INDEX IF NOT EXISTS idx_command_one_terminal ON command_outcomes(delivery_key) WHERE terminal=1;
		CREATE INDEX IF NOT EXISTS idx_command_report ON command_outcomes(run_id,correlation_id,sequence);`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("event outbox: schema: %w", err)
	}
	if _, alterErr := db.Exec(`ALTER TABLE command_outcomes ADD COLUMN consumed INTEGER NOT NULL DEFAULT 0`); alterErr != nil && !strings.Contains(strings.ToLower(alterErr.Error()), "duplicate column name") {
		db.Close()
		return nil, fmt.Errorf("event outbox: migrate outcomes: %w", alterErr)
	}
	return s, nil
}

func (s *SQLiteOutbox) Close() error { return s.db.Close() }

// Accept persists a command once. Reusing a key with different content fails closed.
func (s *SQLiteOutbox) Accept(ctx context.Context, subject string, cmd Command) (bool, error) {
	if subject == "" {
		return false, errors.New("event outbox: subject is required")
	}
	if err := cmd.Validate(); err != nil {
		return false, err
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return false, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `INSERT INTO event_outbox(idempotency_key,event_id,subject,command_json,state,next_attempt,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(idempotency_key) DO NOTHING`, cmd.IdempotencyKey, cmd.EventID, subject, b, DeliveryPending, now, now, now)
	if err != nil {
		return false, fmt.Errorf("event outbox: accept: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}
	var existing []byte
	var existingSubject string
	if err := s.db.QueryRowContext(ctx, `SELECT command_json,subject FROM event_outbox WHERE idempotency_key=?`, cmd.IdempotencyKey).Scan(&existing, &existingSubject); err != nil {
		return false, err
	}
	if existingSubject != subject || string(existing) != string(b) {
		return false, errors.New("event outbox: idempotency key reused with different command")
	}
	return false, nil
}

// Claim atomically leases the oldest eligible delivery. Expired claims are replayable.
func (s *SQLiteOutbox) Claim(ctx context.Context, lease time.Duration) (*Delivery, error) {
	if lease <= 0 {
		return nil, errors.New("event outbox: lease must be positive")
	}
	now := s.now().UTC()
	expires := now.Add(lease)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var key string
	err = tx.QueryRowContext(ctx, `SELECT idempotency_key FROM event_outbox
		WHERE (state=? AND next_attempt<=?) OR (state=? AND lease_expires<=?)
		ORDER BY created_at LIMIT 1`, DeliveryPending, now.Format(time.RFC3339Nano), DeliveryClaimed, now.Format(time.RFC3339Nano)).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE event_outbox SET state=?,attempts=attempts+1,lease_expires=?,updated_at=? WHERE idempotency_key=? AND ((state=? AND next_attempt<=?) OR (state=? AND lease_expires<=?))`, DeliveryClaimed, expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), key, DeliveryPending, now.Format(time.RFC3339Nano), DeliveryClaimed, now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil
	}
	d, err := scanDelivery(tx.QueryRowContext(ctx, `SELECT command_json,subject,state,attempts,last_error,next_attempt,lease_expires FROM event_outbox WHERE idempotency_key=?`, key))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *SQLiteOutbox) MarkDelivered(ctx context.Context, key string) error {
	return s.transition(ctx, key, DeliveryDelivered, "")
}

func (s *SQLiteOutbox) Fail(ctx context.Context, key string, cause error, maxAttempts int, retryAfter time.Duration) (DeliveryState, error) {
	if maxAttempts < 1 {
		return "", errors.New("event outbox: max attempts must be positive")
	}
	var attempts int
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT attempts,state FROM event_outbox WHERE idempotency_key=?`, key).Scan(&attempts, &state); err != nil {
		return "", err
	}
	if DeliveryState(state) != DeliveryClaimed {
		return "", errors.New("event outbox: delivery is not claimed")
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
		if len(msg) > 512 {
			msg = msg[:512]
		}
	}
	if attempts >= maxAttempts {
		return DeliveryTerminalFailed, s.terminalFailure(ctx, key, msg, OutcomeFailed)
	}
	next := s.now().UTC().Add(retryAfter).Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE event_outbox SET state=?,last_error=?,next_attempt=?,lease_expires='',updated_at=? WHERE idempotency_key=? AND state=?`, DeliveryPending, msg, next, s.now().UTC().Format(time.RFC3339Nano), key, DeliveryClaimed)
	return DeliveryPending, err
}

func (s *SQLiteOutbox) terminalFailure(ctx context.Context, key, msg string, status OutcomeStatus) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var b []byte
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT command_json,state FROM event_outbox WHERE idempotency_key=?`, key).Scan(&b, &state); err != nil {
		return err
	}
	if DeliveryState(state) != DeliveryClaimed {
		return errors.New("event outbox: delivery is not claimed")
	}
	// A publisher can report an uncertain delivery after the worker has
	// already durably emitted a terminal fact. That fact proves completion,
	// so do not synthesize a competing terminal event.
	var workerEventID string
	err = tx.QueryRowContext(ctx, `SELECT event_id FROM command_outcomes WHERE delivery_key=? AND terminal=1`, key).Scan(&workerEventID)
	if err == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE event_outbox SET state=?,last_error='',lease_expires='',updated_at=? WHERE idempotency_key=? AND state=?`, DeliveryDelivered, s.now().UTC().Format(time.RFC3339Nano), key, DeliveryClaimed); err != nil {
			return err
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Acceptance proves only that a worker started. It is not a terminal
	// acknowledgement: leave the command pending so expiry/redelivery can
	// recover a crashed worker and produce one trusted terminal outcome.
	var acceptedEventID string
	err = tx.QueryRowContext(ctx, `SELECT event_id FROM command_outcomes WHERE delivery_key=? AND status=?`, key, OutcomeAccepted).Scan(&acceptedEventID)
	if err == nil {
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE event_outbox SET state=?,last_error=?,next_attempt=?,lease_expires='',updated_at=? WHERE idempotency_key=? AND state=?`, DeliveryPending, msg, now, now, key, DeliveryClaimed); err != nil {
			return err
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var cmd Command
	if err := json.Unmarshal(b, &cmd); err != nil {
		return err
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM command_outcomes WHERE delivery_key=?`, key).Scan(&sequence); err != nil {
		return err
	}
	now := s.now().UTC()
	payload, _ := json.Marshal(map[string]string{"error": msg})
	out := Outcome{EventID: outcomeEventID(key, status), CommandEventID: cmd.EventID, RunID: cmd.RunID, TaskID: cmd.TaskID, DelegationID: cmd.DelegationID, CorrelationID: cmd.CorrelationID, CausationID: cmd.EventID, DeliveryKey: key, Status: status, Sequence: sequence, OccurredAt: now, Payload: payload}
	if _, err := recordOutcomeTx(ctx, tx, cmd, out); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE event_outbox SET state=?,last_error=?,lease_expires='',updated_at=? WHERE idempotency_key=? AND state=?`, DeliveryTerminalFailed, msg, now.Format(time.RFC3339Nano), key, DeliveryClaimed)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("event outbox: delivery is not claimed")
	}
	return tx.Commit()
}

func (s *SQLiteOutbox) transition(ctx context.Context, key string, to DeliveryState, msg string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE event_outbox SET state=?,last_error=?,lease_expires='',updated_at=? WHERE idempotency_key=? AND state=?`, to, msg, s.now().UTC().Format(time.RFC3339Nano), key, DeliveryClaimed)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("event outbox: delivery is not claimed")
	}
	return nil
}

func (s *SQLiteOutbox) Get(ctx context.Context, key string) (*Delivery, error) {
	return scanDelivery(s.db.QueryRowContext(ctx, `SELECT command_json,subject,state,attempts,last_error,next_attempt,lease_expires FROM event_outbox WHERE idempotency_key=?`, key))
}

// RecordOutcome appends accepted/progress facts and exactly one terminal fact.
// A late result from an older delivery key cannot overwrite a newer retry.
func (s *SQLiteOutbox) RecordOutcome(ctx context.Context, outcome Outcome) (bool, error) {
	if err := outcome.Validate(); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var commandBytes []byte
	err = tx.QueryRowContext(ctx, `SELECT command_json FROM event_outbox WHERE idempotency_key=?`, outcome.DeliveryKey).Scan(&commandBytes)
	if err != nil {
		return false, err
	}
	var cmd Command
	if err := json.Unmarshal(commandBytes, &cmd); err != nil {
		return false, err
	}
	if outcome.DelegationID != "" {
		var latestKey string
		err = tx.QueryRowContext(ctx, `SELECT idempotency_key FROM event_outbox WHERE command_json->>'$.delegation_id'=? ORDER BY created_at DESC LIMIT 1`, outcome.DelegationID).Scan(&latestKey)
		if err == nil && latestKey != outcome.DeliveryKey {
			return false, errors.New("event outbox: stale delegation outcome")
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	inserted, err := recordOutcomeTx(ctx, tx, cmd, outcome)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return inserted, nil
}

func recordOutcomeTx(ctx context.Context, tx *sql.Tx, cmd Command, outcome Outcome) (bool, error) {
	if outcome.CommandEventID != cmd.EventID || outcome.RunID != cmd.RunID || outcome.TaskID != cmd.TaskID || outcome.DelegationID != cmd.DelegationID || outcome.JoinID != cmd.JoinID || outcome.Lane != cmd.Lane || outcome.CorrelationID != cmd.CorrelationID || outcome.DeliveryKey != cmd.IdempotencyKey || outcome.CausationID != cmd.EventID {
		return false, errors.New("event outbox: outcome identity does not match command")
	}
	if outcome.Status == OutcomeProgress {
		var accepted int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM command_outcomes WHERE delivery_key=? AND status=?`, outcome.DeliveryKey, OutcomeAccepted).Scan(&accepted); err != nil {
			return false, err
		}
		if accepted == 0 {
			return false, errors.New("event outbox: progress requires accepted outcome")
		}
	}
	b, err := json.Marshal(outcome)
	if err != nil {
		return false, err
	}
	var existing []byte
	if err := tx.QueryRowContext(ctx, `SELECT outcome_json FROM command_outcomes WHERE event_id=?`, outcome.EventID).Scan(&existing); err == nil {
		if string(existing) != string(b) {
			return false, errors.New("event outbox: outcome event id reused with different content")
		}
		return false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	terminal := 0
	if outcome.Status.Terminal() {
		terminal = 1
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO command_outcomes(event_id,delivery_key,command_event_id,run_id,correlation_id,sequence,status,outcome_json,terminal) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(event_id) DO NOTHING`, outcome.EventID, outcome.DeliveryKey, outcome.CommandEventID, outcome.RunID, outcome.CorrelationID, outcome.Sequence, outcome.Status, b, terminal)
	if err != nil {
		return false, fmt.Errorf("event outbox: outcome: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return true, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT outcome_json FROM command_outcomes WHERE event_id=?`, outcome.EventID).Scan(&existing); err != nil {
		return false, err
	}
	if string(existing) != string(b) {
		return false, errors.New("event outbox: outcome event id reused with different content")
	}
	return false, nil
}

func outcomeEventID(key string, status OutcomeStatus) string {
	return CommandEventID(key + ":" + string(status))
}

type CommandTrace struct {
	Command  Command       `json:"command"`
	Subject  string        `json:"subject"`
	State    DeliveryState `json:"delivery_state"`
	Attempts int           `json:"attempts"`
	Outcomes []Outcome     `json:"outcomes"`
}

// Report returns the complete correlated delivery trace for a run.
func (s *SQLiteOutbox) Report(ctx context.Context, runID string) ([]CommandTrace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT command_json,subject,state,attempts FROM event_outbox WHERE command_json->>'$.run_id'=? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var report []CommandTrace
	for rows.Next() {
		var b []byte
		var tr CommandTrace
		var state string
		if err := rows.Scan(&b, &tr.Subject, &state, &tr.Attempts); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &tr.Command); err != nil {
			return nil, err
		}
		tr.State = DeliveryState(state)
		orows, err := s.db.QueryContext(ctx, `SELECT outcome_json FROM command_outcomes WHERE command_event_id=? ORDER BY sequence`, tr.Command.EventID)
		if err != nil {
			return nil, err
		}
		for orows.Next() {
			var ob []byte
			var out Outcome
			if err := orows.Scan(&ob); err != nil {
				orows.Close()
				return nil, err
			}
			if err := json.Unmarshal(ob, &out); err != nil {
				orows.Close()
				return nil, err
			}
			tr.Outcomes = append(tr.Outcomes, out)
		}
		orows.Close()
		report = append(report, tr)
	}
	return report, rows.Err()
}

// PendingOutcomes returns durable facts not yet handed to the workflow runtime.
func (s *SQLiteOutbox) PendingOutcomes(ctx context.Context, runID string) ([]Outcome, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT outcome_json FROM command_outcomes WHERE run_id=? AND consumed=0 ORDER BY rowid`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var outcomes []Outcome
	for rows.Next() {
		var b []byte
		var out Outcome
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, err
		}
		outcomes = append(outcomes, out)
	}
	return outcomes, rows.Err()
}

func (s *SQLiteOutbox) MarkOutcomeConsumed(ctx context.Context, eventID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE command_outcomes SET consumed=1 WHERE event_id=? AND consumed=0`, eventID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var consumed int
	if err := s.db.QueryRowContext(ctx, `SELECT consumed FROM command_outcomes WHERE event_id=?`, eventID).Scan(&consumed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("event outbox: outcome not found")
		}
		return err
	}
	if consumed == 0 {
		return errors.New("event outbox: outcome acknowledgement was not persisted")
	}
	return nil
}

type rowScanner interface{ Scan(...any) error }

func scanDelivery(r rowScanner) (*Delivery, error) {
	var b []byte
	var d Delivery
	var state, next, lease string
	if err := r.Scan(&b, &d.Subject, &state, &d.Attempts, &d.LastError, &next, &lease); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &d.Command); err != nil {
		return nil, err
	}
	d.State = DeliveryState(state)
	d.NextAttempt, _ = time.Parse(time.RFC3339Nano, next)
	d.LeaseExpires, _ = time.Parse(time.RFC3339Nano, lease)
	return &d, nil
}

type Dispatcher struct {
	Outbox      *SQLiteOutbox
	Publisher   CommandPublisher
	Lease       time.Duration
	MaxAttempts int
	RetryAfter  time.Duration
	// DeferTerminalOnPublishFailure keeps an uncertain transport delivery
	// recoverable until its deadline. Use it when a trusted worker can have
	// received the command even though the publisher returned an error.
	DeferTerminalOnPublishFailure bool
}

func (d Dispatcher) DispatchOne(ctx context.Context) (bool, error) {
	if d.Outbox == nil || d.Publisher == nil {
		return false, errors.New("event outbox: dispatcher dependencies are required")
	}
	item, err := d.Outbox.Claim(ctx, d.Lease)
	if err != nil || item == nil {
		return false, err
	}
	if !item.Command.Deadline.IsZero() && !d.Outbox.now().Before(item.Command.Deadline) {
		failErr := d.Outbox.terminalFailure(ctx, item.Command.IdempotencyKey, "command deadline exceeded", OutcomeTimedOut)
		return true, failErr
	}
	if err := d.Publisher.Publish(ctx, item.Subject, item.Command.Payload); err != nil {
		maxAttempts := d.MaxAttempts
		if d.DeferTerminalOnPublishFailure {
			maxAttempts = int(^uint(0) >> 1)
		}
		_, ferr := d.Outbox.Fail(ctx, item.Command.IdempotencyKey, err, maxAttempts, d.RetryAfter)
		if ferr != nil {
			return true, ferr
		}
		return true, err
	}
	return true, d.Outbox.MarkDelivered(ctx, item.Command.IdempotencyKey)
}
