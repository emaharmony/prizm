package multiagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/freewill"
)

var ErrFreewillActorAdmissionConflict = errors.New("multiagent: Freewill actor admission conflicts with an active intention")

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
	if err != nil { return fmt.Errorf("multiagent store: create Freewill tables: %w", err) }
	return nil
}

func (s *SQLiteDurableRunStore) LoadFreewillOperatorControl(ctx context.Context, agentID string, scope freewill.Scope) (FreewillOperatorControlState, error) {
	if err := s.ensureFreewillTables(ctx); err != nil { return FreewillOperatorControlState{}, err }
	var state FreewillOperatorControlState
	var halted int
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT agent_id, project_id, workspace, halted, generation, actor, updated_at FROM multiagent_freewill_controls WHERE control_key = ?`, freewillOperatorControlKey(agentID, scope)).Scan(&state.AgentID, &state.Scope.ProjectID, &state.Scope.Workspace, &halted, &state.Generation, &state.Actor, &updated)
	if errors.Is(err, sql.ErrNoRows) { return FreewillOperatorControlState{AgentID: agentID, Scope: scope}, nil }
	if err != nil { return FreewillOperatorControlState{}, err }
	state.Halted = halted != 0
	state.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return state, nil
}

func (s *SQLiteDurableRunStore) SetFreewillOperatorHalt(ctx context.Context, agentID string, scope freewill.Scope, halted bool, actor string, now time.Time) (FreewillOperatorControlState, error) {
	if err := s.ensureFreewillTables(ctx); err != nil { return FreewillOperatorControlState{}, err }
	key := freewillOperatorControlKey(agentID, scope)
	_, err := s.db.ExecContext(ctx, `INSERT INTO multiagent_freewill_controls(control_key,agent_id,project_id,workspace,halted,generation,actor,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(control_key) DO UPDATE SET halted=excluded.halted,generation=multiagent_freewill_controls.generation+1,actor=excluded.actor,updated_at=excluded.updated_at`, key, agentID, scope.ProjectID, scope.Workspace, boolInt(halted), 1, actor, now.UTC().Format(time.RFC3339Nano))
	if err != nil { return FreewillOperatorControlState{}, err }
	return s.LoadFreewillOperatorControl(ctx, agentID, scope)
}

func boolInt(v bool) int { if v { return 1 }; return 0 }

func (s *SQLiteDurableRunStore) EnsureFreewillActorAdmission(ctx context.Context, runID, agentID string, scope freewill.Scope) error {
	if err := s.ensureFreewillTables(ctx); err != nil { return err }
	key := freewillActorAdmissionKey(agentID)
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT run_id FROM multiagent_freewill_admissions WHERE actor_key = ?`, key).Scan(&existing)
	if err == nil && existing != runID { return ErrFreewillActorAdmissionConflict }
	if err != nil && !errors.Is(err, sql.ErrNoRows) { return err }
	_, err = s.db.ExecContext(ctx, `INSERT INTO multiagent_freewill_admissions(actor_key,run_id,agent_id,project_id,workspace,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(actor_key) DO UPDATE SET run_id=excluded.run_id,updated_at=excluded.updated_at`, key, runID, agentID, scope.ProjectID, scope.Workspace, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *SQLiteDurableRunStore) ListFreewillRuns(ctx context.Context, afterRunID string, limit int) ([]DurableRun, error) {
	if limit <= 0 || limit > 1000 { limit = 100 }
	rows, err := s.db.QueryContext(ctx, `SELECT record_json FROM multiagent_runs WHERE run_id > ? ORDER BY run_id LIMIT ?`, afterRunID, limit)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []DurableRun
	for rows.Next() {
		var raw string; if err := rows.Scan(&raw); err != nil { return nil, err }
		var record DurableRun; if err := json.Unmarshal([]byte(raw), &record); err != nil { return nil, err }
		if record.Kind == DurableRunKindFreewill { out = append(out, record) }
	}
	return out, rows.Err()
}
