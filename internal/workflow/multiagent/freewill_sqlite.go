package multiagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	prismsqlite "github.com/emaharmony/prizm/internal/sqlite"
)

var ErrFreewillActorAdmissionConflict = errors.New("multiagent: Freewill actor admission conflicts with an active intention")

var _ FreewillActorAdmissionStore = (*SQLiteDurableRunStore)(nil)

// OpenExistingSQLiteDurableRunStoreReadOnly opens an existing durable store
// without creating tables or enabling write-capable workflow paths.
func OpenExistingSQLiteDurableRunStoreReadOnly(dbPath string) (*SQLiteDurableRunStore, error) {
	if dbPath == "" {
		return nil, errors.New("multiagent: sqlite database path is required")
	}
	db, err := sql.Open(prismsqlite.DriverName, dbPath+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("multiagent store: open read-only: %w", err)
	}
	return &SQLiteDurableRunStore{db: db}, nil
}

func (s *SQLiteDurableRunStore) ensureFreewillTables(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS multiagent_freewill_controls (
 control_key TEXT PRIMARY KEY, agent_id TEXT NOT NULL, project_id TEXT NOT NULL,
 workspace TEXT NOT NULL, halted INTEGER NOT NULL, generation INTEGER NOT NULL,
 actor TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS multiagent_freewill_admissions (
 actor_key TEXT PRIMARY KEY, run_id TEXT NOT NULL, agent_id TEXT NOT NULL,
 project_id TEXT NOT NULL, workspace TEXT NOT NULL, updated_at TEXT NOT NULL
);`)
	if err != nil {
		return fmt.Errorf("multiagent store: create Freewill tables: %w", err)
	}
	return nil
}

func (s *SQLiteDurableRunStore) LoadFreewillOperatorControl(ctx context.Context, agentID string, scope freewill.Scope) (FreewillOperatorControlState, error) {
	if err := s.ensureFreewillTables(ctx); err != nil {
		return FreewillOperatorControlState{}, err
	}
	var state FreewillOperatorControlState
	var halted int
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT agent_id, project_id, workspace, halted, generation, actor, updated_at FROM multiagent_freewill_controls WHERE control_key = ?`, freewillOperatorControlKey(agentID, scope)).Scan(&state.AgentID, &state.Scope.ProjectID, &state.Scope.Workspace, &halted, &state.Generation, &state.Actor, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return FreewillOperatorControlState{AgentID: agentID, Scope: scope}, nil
	}
	if err != nil {
		return FreewillOperatorControlState{}, err
	}
	state.Halted = halted != 0
	state.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return state, nil
}

func (s *SQLiteDurableRunStore) SetFreewillOperatorHalt(ctx context.Context, agentID string, scope freewill.Scope, halted bool, actor string, now time.Time) (FreewillOperatorControlState, error) {
	if err := s.ensureFreewillTables(ctx); err != nil {
		return FreewillOperatorControlState{}, err
	}
	key := freewillOperatorControlKey(agentID, scope)
	_, err := s.db.ExecContext(ctx, `INSERT INTO multiagent_freewill_controls(control_key,agent_id,project_id,workspace,halted,generation,actor,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(control_key) DO UPDATE SET halted=excluded.halted,generation=multiagent_freewill_controls.generation+1,actor=excluded.actor,updated_at=excluded.updated_at WHERE multiagent_freewill_controls.halted != excluded.halted`, key, agentID, scope.ProjectID, scope.Workspace, boolInt(halted), 1, actor, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return FreewillOperatorControlState{}, err
	}
	return s.LoadFreewillOperatorControl(ctx, agentID, scope)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// AcceptFreewillWake atomically persists a scheduled wake, its idempotency
// key and outbox events, and the actor's single-run admission slot.
func (s *SQLiteDurableRunStore) AcceptFreewillWake(
	ctx context.Context,
	record DurableRun,
	events []event.Event,
	wake FreewillWakeRequest,
) (DurableRun, bool, error) {
	if err := wake.validate(); err != nil {
		return DurableRun{}, false, err
	}
	if record.Kind != DurableRunKindFreewill || record.Freewill == nil ||
		record.State.RunID != wake.RunID || !freewillWakeMatches(record, wake) {
		return DurableRun{}, false, ErrRequestIdentityConflict
	}
	if err := record.Validate(nil); err != nil {
		return DurableRun{}, false, err
	}
	if err := s.ensureFreewillTables(ctx); err != nil {
		return DurableRun{}, false, err
	}
	key := runAcceptanceKey(record)
	if key == "" {
		return DurableRun{}, false, errors.New("multiagent store: Freewill wake idempotency key is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DurableRun{}, false, fmt.Errorf("multiagent store: begin Freewill wake acceptance: %w", err)
	}
	defer tx.Rollback()
	accepted, created, err := s.acceptRunTx(ctx, tx, record, events, key)
	if err != nil {
		return DurableRun{}, false, err
	}
	if accepted.State.RunID != wake.RunID || !freewillWakeMatches(accepted, wake) {
		return accepted, false, ErrRequestIdentityConflict
	}
	if err := ensureFreewillActorAdmissionTx(ctx, tx, accepted.State.RunID, wake.AgentID, wake.Scope); err != nil {
		return accepted, false, err
	}
	if err := tx.Commit(); err != nil {
		return DurableRun{}, false, fmt.Errorf("multiagent store: commit Freewill wake acceptance: %w", err)
	}
	return accepted, created, nil
}

// ReconcileFreewillWakeAdmission verifies same-run ownership before recovery.
// A valid but unowned legacy wake is durably halted with its audit event in the
// same transaction; the other run's admission slot is never changed.
func (s *SQLiteDurableRunStore) ReconcileFreewillWakeAdmission(
	ctx context.Context,
	runID, agentID string,
	scope freewill.Scope,
	now time.Time,
) (DurableRun, bool, error) {
	if err := s.ensureFreewillTables(ctx); err != nil {
		return DurableRun{}, false, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DurableRun{}, false, fmt.Errorf("multiagent store: begin Freewill wake admission reconciliation: %w", err)
	}
	defer tx.Rollback()

	var raw string
	err = tx.QueryRowContext(ctx, `SELECT record_json FROM multiagent_runs WHERE run_id = ?`, runID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DurableRun{}, false, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	}
	if err != nil {
		return DurableRun{}, false, err
	}
	var record DurableRun
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return DurableRun{}, false, fmt.Errorf("%w: decode Freewill wake: %v", ErrFreewillActorAdmissionAmbiguous, err)
	}
	if record.State.RunID != runID || record.Kind != DurableRunKindFreewill || record.Freewill == nil ||
		record.Phase != CheckpointFreewillWake || record.State.RequestIdentity == nil ||
		record.State.RequestIdentity.RequesterID != agentID || record.Freewill.Scope != scope ||
		!record.Freewill.ExecutionDisabled || record.Validate(nil) != nil {
		return record, false, ErrFreewillActorAdmissionAmbiguous
	}
	if record.Freewill.Lifecycle != freewill.StateWaking && record.Freewill.Lifecycle != freewill.StateSelecting {
		return record, false, errors.New("multiagent: Freewill wake is not recoverable")
	}
	if record.Freewill.LeaseOwner != "" && record.Freewill.LeaseUntil.After(now) {
		return record, false, ErrFreewillWakeLeaseHeld
	}
	if record.Freewill.SelectionNextAt.After(now) {
		return record, false, ErrFreewillWakeNotDue
	}

	key := freewillActorAdmissionKey(agentID)
	var existing, existingAgent, existingProject, existingWorkspace string
	err = tx.QueryRowContext(ctx, `SELECT run_id, agent_id, project_id, workspace FROM multiagent_freewill_admissions WHERE actor_key = ?`, key).
		Scan(&existing, &existingAgent, &existingProject, &existingWorkspace)
	failureCode := ""
	if errors.Is(err, sql.ErrNoRows) {
		failureCode = freewillAdmissionMissingCode
	} else if err != nil {
		return record, false, err
	} else {
		if existing == "" || existingAgent != agentID || existingProject == "" || existingWorkspace == "" {
			return record, false, ErrFreewillActorAdmissionAmbiguous
		}
		ownerScope := freewill.Scope{ProjectID: existingProject, Workspace: existingWorkspace}
		owner, err := loadFreewillAdmissionRun(ctx, tx, existing, existingAgent, ownerScope)
		if err != nil {
			return record, false, err
		}
		if existing == runID {
			if owner.State.RunID != runID || ownerScope != scope {
				return record, false, ErrFreewillActorAdmissionAmbiguous
			}
			return record, false, nil
		}
		failureCode = freewillAdmissionOtherOwnerCode
	}
	if failureCode == "" {
		return record, false, ErrFreewillActorAdmissionAmbiguous
	}
	if err := freewill.ValidateTransition(record.Freewill.Lifecycle, freewill.StateHalted); err != nil {
		return record, false, err
	}
	record.Freewill.Lifecycle = freewill.StateHalted
	record.Freewill.ExecutionDisabled = true
	record.Freewill.PauseReason = freewillAdmissionReconciledReason
	record.Freewill.SelectionFailureClass = freewillAdmissionReconciliationClass
	record.Freewill.SelectionFailureCode = failureCode
	record.Freewill.LeaseOwner = ""
	record.Freewill.LeaseUntil = time.Time{}
	record.Freewill.SelectionNextAt = time.Time{}
	record.Freewill.SelectionFeedback = ""
	record.State.UpdatedAt = now
	if err := record.Validate(nil); err != nil {
		return record, false, err
	}
	expectedRevision := record.Revision
	record.Revision = expectedRevision + 1
	data, err := json.Marshal(record)
	if err != nil {
		return DurableRun{}, false, fmt.Errorf("multiagent store: marshal Freewill admission reconciliation: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE multiagent_runs
SET schema_version = ?, revision = ?, record_json = ?, updated_at = ?
WHERE run_id = ? AND revision = ?`,
		record.SchemaVersion, record.Revision, string(data), now.Format(time.RFC3339Nano), runID, expectedRevision)
	if err != nil {
		return DurableRun{}, false, fmt.Errorf("multiagent store: update Freewill admission reconciliation: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return DurableRun{}, false, err
	}
	if updated != 1 {
		return DurableRun{}, false, fmt.Errorf("%w: run %s expected revision %d", ErrRevisionConflict, runID, expectedRevision)
	}
	evt := freewillLeaseEvent(event.EventFreewillSelectionHalted, record, map[string]any{
		"attempt": record.Freewill.LeaseAttempt, "recovery_count": record.Freewill.SelectionRecoveryNumber,
		"failure_class": record.Freewill.SelectionFailureClass, "failure_code": record.Freewill.SelectionFailureCode,
		"reason": record.Freewill.PauseReason,
	})
	if err := enqueueEvents(ctx, tx, runID, record.Revision, []event.Event{evt}); err != nil {
		return DurableRun{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return DurableRun{}, false, fmt.Errorf("multiagent store: commit Freewill admission reconciliation: %w", err)
	}
	return record, true, nil
}

func (s *SQLiteDurableRunStore) EnsureFreewillActorAdmission(ctx context.Context, runID, agentID string, scope freewill.Scope) error {
	if err := s.ensureFreewillTables(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("multiagent store: begin Freewill admission: %w", err)
	}
	defer tx.Rollback()
	if err := ensureFreewillActorAdmissionTx(ctx, tx, runID, agentID, scope); err != nil {
		return err
	}
	return tx.Commit()
}

// ensureFreewillActorAdmissionTx returns ActorAdmissionConflict only after
// verifying that the persisted slot points to a well-formed active run owned
// by the recorded actor and scope. Corrupt or missing receipts stay ambiguous.
func ensureFreewillActorAdmissionTx(ctx context.Context, tx *sql.Tx, runID, agentID string, scope freewill.Scope) error {
	if _, err := loadFreewillAdmissionRun(ctx, tx, runID, agentID, scope); err != nil {
		return err
	}
	key := freewillActorAdmissionKey(agentID)
	var existing, existingAgent, existingProject, existingWorkspace string
	err := tx.QueryRowContext(ctx, `SELECT run_id, agent_id, project_id, workspace FROM multiagent_freewill_admissions WHERE actor_key = ?`, key).
		Scan(&existing, &existingAgent, &existingProject, &existingWorkspace)
	if errors.Is(err, sql.ErrNoRows) {
		return freewillInsertAdmissionTx(ctx, tx, key, runID, agentID, scope)
	}
	if err != nil {
		return err
	}
	priorScope := freewill.Scope{ProjectID: existingProject, Workspace: existingWorkspace}
	if existingAgent != agentID || existing == "" || existingProject == "" || existingWorkspace == "" {
		return ErrFreewillActorAdmissionAmbiguous
	}
	prior, err := loadFreewillAdmissionRun(ctx, tx, existing, existingAgent, priorScope)
	if err != nil {
		return err
	}
	if existing == runID {
		if scope != priorScope {
			return ErrFreewillActorAdmissionAmbiguous
		}
		return nil
	}
	if freewillAdmissionActive(prior) {
		return ErrFreewillActorAdmissionConflict
	}
	return freewillReplaceAdmissionTx(ctx, tx, key, runID, agentID, scope)
}

func loadFreewillAdmissionRun(ctx context.Context, tx *sql.Tx, runID, agentID string, scope freewill.Scope) (DurableRun, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT record_json FROM multiagent_runs WHERE run_id = ?`, runID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DurableRun{}, ErrFreewillActorAdmissionAmbiguous
	}
	if err != nil {
		return DurableRun{}, err
	}
	var record DurableRun
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return DurableRun{}, fmt.Errorf("%w: decode Freewill admission run: %v", ErrFreewillActorAdmissionAmbiguous, err)
	}
	if record.State.RunID != runID || record.Kind != DurableRunKindFreewill || record.Freewill == nil ||
		record.State.RequestIdentity == nil || record.State.RequestIdentity.RequesterID != agentID ||
		record.Freewill.Scope != scope || !record.Freewill.ExecutionDisabled || record.Validate(nil) != nil {
		return DurableRun{}, ErrFreewillActorAdmissionAmbiguous
	}
	switch record.Freewill.Lifecycle {
	case freewill.StateWaking, freewill.StateSelecting, freewill.StateProposed, freewill.StateApprovalPending,
		freewill.StateRunning, freewill.StateSleeping, freewill.StateDenied, freewill.StateHalted, freewill.StatePaused:
	default:
		return DurableRun{}, ErrFreewillActorAdmissionAmbiguous
	}
	return record, nil
}

func freewillInsertAdmissionTx(ctx context.Context, tx *sql.Tx, key, runID, agentID string, scope freewill.Scope) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO multiagent_freewill_admissions(actor_key,run_id,agent_id,project_id,workspace,updated_at) VALUES(?,?,?,?,?,?)`,
		key, runID, agentID, scope.ProjectID, scope.Workspace, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func freewillReplaceAdmissionTx(ctx context.Context, tx *sql.Tx, key, runID, agentID string, scope freewill.Scope) error {
	result, err := tx.ExecContext(ctx, `UPDATE multiagent_freewill_admissions SET run_id=?,agent_id=?,project_id=?,workspace=?,updated_at=? WHERE actor_key=?`,
		runID, agentID, scope.ProjectID, scope.Workspace, time.Now().UTC().Format(time.RFC3339Nano), key)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrFreewillActorAdmissionAmbiguous
	}
	return nil
}

func (s *SQLiteDurableRunStore) ListFreewillRuns(ctx context.Context, afterRunID string, limit int) ([]DurableRun, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT record_json FROM multiagent_runs WHERE run_id > ? ORDER BY run_id LIMIT ?`, afterRunID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DurableRun
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var record DurableRun
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return nil, err
		}
		if record.Kind == DurableRunKindFreewill {
			out = append(out, record)
		}
	}
	return out, rows.Err()
}
