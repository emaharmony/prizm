// definition_store.go implements PR6 of the Phase 3 "Developer-Authored
// Workflow Graph Runtime" initiative: an immutable, versioned registry for
// compiled WorkflowDefinitions, backed by Prism's existing SQLite
// infrastructure (mirroring durable_sqlite_store.go's connection/pragma
// conventions).
//
// DefinitionStore is insert-only: every row is immutable by construction
// (workflow_id, version) is the primary key, and Register never UPDATEs an
// existing row. Register computes the next version as MAX(version)+1 for the
// workflow_id inside the same transaction as the fingerprint pre-check and
// the insert, so "what version is this?" is always a property of what is
// already durably stored, never caller-supplied. This is what lets a
// DurableRun pin (workflow_id, version) once at start and have that pin
// survive every later registration of a newer version for the same
// workflow_id — the concrete mechanism satisfying "updating a definition
// must not alter active/historical runs" (see the Phase 3 plan's Definition
// registry/storage section, and RunState.Validate's WorkflowVersion equality
// check in state.go).
//
// Storage decision — recompile-on-read, not rehydrate-from-JSON: CompiledGraph
// has unexported fields and no constructor other than Compile/
// CompatAdaptDefinition, so there are exactly two ways to reconstruct a
// *CompiledGraph from a stored row: (a) store CompiledGraphView JSON and add
// a new package-internal "rehydrate a CompiledGraph from its view" path, or
// (b) store only definition_json (the canonical WorkflowDefinition) and call
// Compile again on every read. This file takes (b). Compile is a pure,
// deterministic function of its WorkflowDefinition input — PR3's own
// fingerprint-determinism test already establishes that 100 repeated
// compiles of identical input produce identical output — so recompiling on
// read costs a little CPU (validating and rebuilding a graph that is
// realistically at most a few hundred nodes/edges) in exchange for avoiding
// an entire second construction path for CompiledGraph that would need to be
// proven equivalent to Compile's own. getWhere defensively re-checks that
// the recompiled graph's own Fingerprint() still matches the fingerprint
// this row was stored under, so any divergence (a future change to Compile's
// output shape, a hand-edited row, etc.) surfaces as a clear error instead of
// silently serving a graph that doesn't match its own recorded identity.
// Consequently there is no compiled_json column at all in this schema —
// nothing in this file ever reads one, so persisting it would just be an
// unused, driftable duplicate of definition_json's content.
package multiagent

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

	prismsqlite "github.com/emaharmony/prism/internal/sqlite"
)

// ErrDefinitionUnchanged is returned by Register (alongside the existing
// RegisteredDefinition, not a zero value) when the definition's compiled
// fingerprint already matches a previously registered version for the same
// workflow_id — distinguishable from a hard failure via errors.Is, so
// callers (CLI, API) can tell "nothing new was registered" apart from a real
// error.
var ErrDefinitionUnchanged = errors.New("multiagent: definition unchanged, no new version registered")

// ErrDefinitionNotFound is returned by Get/GetByFingerprint/Latest when no
// row matches the request.
var ErrDefinitionNotFound = errors.New("multiagent: definition not found")

// RegisteredDefinition is one immutable, versioned row of the definition
// registry, together with its recompiled CompiledGraph.
type RegisteredDefinition struct {
	WorkflowID    string
	Version       int64
	UserVersion   string
	SchemaVersion string
	Fingerprint   string
	Definition    WorkflowDefinition
	Graph         *CompiledGraph
	SourceRef     string
	CreatedAt     time.Time
}

// DefinitionSummary is the cheap, list-friendly view of one registered
// definition version: everything ListVersions needs to render a version
// picker, without the cost of decoding+recompiling the full definition_json
// for every row. ListVersions returns this type rather than
// []RegisteredDefinition (with Definition/Graph left at their zero value) so
// a caller can never mistake an intentionally-cheap summary for a fully
// resolved, executable RegisteredDefinition.
type DefinitionSummary struct {
	WorkflowID    string
	Version       int64
	UserVersion   string
	SchemaVersion string
	Fingerprint   string
	SourceRef     string
	CreatedAt     time.Time
}

// DefinitionStore persists immutable, versioned WorkflowDefinitions in
// Prism's existing SQLite infrastructure.
type DefinitionStore struct {
	db *sql.DB
}

// NewDefinitionStore opens or creates the package-owned definitions table,
// mirroring NewSQLiteDurableRunStore's connection/pragma conventions
// (durable_sqlite_store.go): same DSN shape (busy_timeout + WAL), same
// mkdir-parent-then-open-then-create-schema sequence, same error wrapping.
//
// One deliberate deviation: SetMaxOpenConns(1). Register's correctness
// depends on a read-then-conditional-insert sequence (check fingerprint,
// compute MAX(version)+1, insert) staying atomic within this process; a
// pooled multi-connection *sql.DB can hand a second concurrent Register call
// a different connection mid-sequence, and two overlapping "deferred"
// transactions can both observe the same MAX(version) before either commits.
// The bounded retry loop in Register already makes that race merely
// inefficient rather than incorrect (a losing INSERT fails, the caller
// retries and recomputes MAX(version)+1), but serializing onto a single
// connection removes the race entirely for concurrent callers within this
// process, at a cost that is irrelevant for a definitions registry (writes
// are rare: one per `graph run --file` or POST .../definitions call, never a
// hot path). busy_timeout continues to cover cross-process contention (e.g.
// two separate `prism` invocations sharing one database file).
func NewDefinitionStore(dbPath string) (*DefinitionStore, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, errors.New("multiagent: definition store database path is required")
	}
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("multiagent definition store: create directory: %w", err)
		}
	}
	db, err := sql.Open(
		prismsqlite.DriverName,
		dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
	)
	if err != nil {
		return nil, fmt.Errorf("multiagent definition store: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("multiagent definition store: enable WAL: %w", err)
	}
	const schema = `
CREATE TABLE IF NOT EXISTS multiagent_definitions (
    workflow_id      TEXT NOT NULL,
    version          INTEGER NOT NULL,
    user_version     TEXT NOT NULL,
    schema_version   TEXT NOT NULL,
    fingerprint      TEXT NOT NULL,
    definition_json  TEXT NOT NULL,
    source_ref       TEXT,
    created_at       TEXT NOT NULL,
    PRIMARY KEY (workflow_id, version)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_multiagent_definitions_fingerprint
    ON multiagent_definitions(workflow_id, fingerprint);
`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("multiagent definition store: create schema: %w", err)
	}
	return &DefinitionStore{db: db}, nil
}

// Close releases the SQLite connection.
func (s *DefinitionStore) Close() error {
	return s.db.Close()
}

// Register inserts a new immutable version of wf/graph for graph.WorkflowID().
// graph must already be the result of a successful Compile(wf, ...) — Register
// reuses graph.Fingerprint() directly rather than recomputing it, per the
// package's "the compiled graph's own fingerprint is the only fingerprint"
// convention.
//
// If a row with the same fingerprint already exists for this workflow_id,
// Register returns that EXISTING RegisteredDefinition (not a zero value)
// together with ErrDefinitionUnchanged, instead of creating a redundant
// version — callers that only care about "do I have a version to run" should
// treat ErrDefinitionUnchanged as success, not failure (errors.Is).
//
// The version number is computed as MAX(version)+1 for workflow_id INSIDE
// the same transaction as the insert, never caller-supplied, so overwriting
// an existing version is a schema-level impossibility (the primary key would
// reject it) and racing the version number is a liveness concern handled by
// a bounded retry loop, not a correctness one.
func (s *DefinitionStore) Register(
	ctx context.Context,
	wf WorkflowDefinition,
	graph *CompiledGraph,
	sourceRef string,
) (RegisteredDefinition, error) {
	if graph == nil {
		return RegisteredDefinition{}, errors.New("multiagent definition store: compiled graph is required to register a definition")
	}
	workflowID := strings.TrimSpace(graph.WorkflowID())
	if workflowID == "" {
		return RegisteredDefinition{}, errors.New("multiagent definition store: compiled graph has no workflow id")
	}
	fingerprint := graph.Fingerprint()
	defJSON, err := json.Marshal(wf)
	if err != nil {
		return RegisteredDefinition{}, fmt.Errorf("multiagent definition store: marshal definition: %w", err)
	}

	const maxAttempts = 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		reg, unchanged, err := s.tryRegister(ctx, workflowID, fingerprint, wf, graph, defJSON, sourceRef)
		switch {
		case err == nil && unchanged:
			return reg, ErrDefinitionUnchanged
		case err == nil:
			return reg, nil
		case ctx.Err() != nil:
			return RegisteredDefinition{}, ctx.Err()
		case isUniqueConstraintErr(err) && attempt < maxAttempts:
			lastErr = err
			continue
		default:
			return RegisteredDefinition{}, err
		}
	}
	return RegisteredDefinition{}, fmt.Errorf(
		"multiagent definition store: register %q: exhausted retries: %w", workflowID, lastErr,
	)
}

// tryRegister is one attempt of Register's check-fingerprint /
// compute-next-version / insert sequence, wrapped in its own transaction. It
// returns (registered, unchanged, err): unchanged is true only when err is
// nil and an existing row already carried this exact fingerprint.
func (s *DefinitionStore) tryRegister(
	ctx context.Context,
	workflowID, fingerprint string,
	wf WorkflowDefinition,
	graph *CompiledGraph,
	defJSON []byte,
	sourceRef string,
) (RegisteredDefinition, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RegisteredDefinition{}, false, fmt.Errorf("multiagent definition store: begin register: %w", err)
	}
	defer tx.Rollback()

	var existingVersion int64
	var existingDefJSON, existingCreatedAt, existingSourceRef sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT version, definition_json, created_at, source_ref
FROM multiagent_definitions WHERE workflow_id = ? AND fingerprint = ?`,
		workflowID, fingerprint,
	).Scan(&existingVersion, &existingDefJSON, &existingCreatedAt, &existingSourceRef)
	switch {
	case err == nil:
		var existingWF WorkflowDefinition
		if uerr := json.Unmarshal([]byte(existingDefJSON.String), &existingWF); uerr != nil {
			return RegisteredDefinition{}, false, fmt.Errorf("multiagent definition store: decode existing definition: %w", uerr)
		}
		createdAt, _ := time.Parse(time.RFC3339Nano, existingCreatedAt.String)
		reg := RegisteredDefinition{
			WorkflowID:    workflowID,
			Version:       existingVersion,
			UserVersion:   graph.WorkflowVersion(),
			SchemaVersion: graph.SchemaVersion(),
			Fingerprint:   fingerprint,
			Definition:    existingWF,
			Graph:         stampRegistryVersion(graph, existingVersion),
			SourceRef:     existingSourceRef.String,
			CreatedAt:     createdAt,
		}
		if cerr := tx.Commit(); cerr != nil {
			return RegisteredDefinition{}, false, fmt.Errorf("multiagent definition store: commit unchanged-lookup: %w", cerr)
		}
		return reg, true, nil
	case errors.Is(err, sql.ErrNoRows):
		// Not previously registered under this fingerprint — fall through to
		// insert a new version.
	default:
		return RegisteredDefinition{}, false, fmt.Errorf("multiagent definition store: check fingerprint: %w", err)
	}

	var nextVersion int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM multiagent_definitions WHERE workflow_id = ?`,
		workflowID,
	).Scan(&nextVersion); err != nil {
		return RegisteredDefinition{}, false, fmt.Errorf("multiagent definition store: compute next version: %w", err)
	}

	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
INSERT INTO multiagent_definitions(workflow_id, version, user_version, schema_version, fingerprint, definition_json, source_ref, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		workflowID, nextVersion, graph.WorkflowVersion(), graph.SchemaVersion(), fingerprint,
		string(defJSON), nullableString(sourceRef), now.Format(time.RFC3339Nano),
	)
	if err != nil {
		// Could be a genuine concurrent conflict (another writer took this
		// exact version, or registered this exact fingerprint, between our
		// SELECT above and this INSERT). Propagated as-is; Register's retry
		// loop recognizes a UNIQUE constraint failure and retries from
		// scratch, which re-runs the fingerprint check first.
		return RegisteredDefinition{}, false, fmt.Errorf("multiagent definition store: insert definition: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RegisteredDefinition{}, false, fmt.Errorf("multiagent definition store: commit register: %w", err)
	}

	reg := RegisteredDefinition{
		WorkflowID:    workflowID,
		Version:       nextVersion,
		UserVersion:   graph.WorkflowVersion(),
		SchemaVersion: graph.SchemaVersion(),
		Fingerprint:   fingerprint,
		Definition:    wf,
		Graph:         stampRegistryVersion(graph, nextVersion),
		SourceRef:     sourceRef,
		CreatedAt:     now,
	}
	return reg, false, nil
}

// Get returns the exact registered version (workflowID, version).
func (s *DefinitionStore) Get(ctx context.Context, workflowID string, version int64) (RegisteredDefinition, error) {
	return s.getWhere(
		ctx,
		"workflow_id = ? AND version = ?",
		[]any{workflowID, version},
		fmt.Sprintf("workflow %q version %d", workflowID, version),
	)
}

// GetByFingerprint returns the registered version of workflowID whose
// compiled fingerprint exactly matches fingerprint.
func (s *DefinitionStore) GetByFingerprint(ctx context.Context, workflowID, fingerprint string) (RegisteredDefinition, error) {
	return s.getWhere(
		ctx,
		"workflow_id = ? AND fingerprint = ?",
		[]any{workflowID, fingerprint},
		fmt.Sprintf("workflow %q fingerprint %q", workflowID, fingerprint),
	)
}

// Latest returns the highest-numbered registered version of workflowID.
func (s *DefinitionStore) Latest(ctx context.Context, workflowID string) (RegisteredDefinition, error) {
	return s.getWhere(
		ctx,
		"workflow_id = ? AND version = (SELECT MAX(version) FROM multiagent_definitions WHERE workflow_id = ?)",
		[]any{workflowID, workflowID},
		fmt.Sprintf("workflow %q", workflowID),
	)
}

// getWhere loads exactly one row matching whereClause/args, recompiles its
// stored definition_json into a fresh *CompiledGraph (see this file's
// top-of-file doc for why recompile-on-read, not rehydrate), and defensively
// verifies the recompiled fingerprint still matches the stored one before
// returning.
func (s *DefinitionStore) getWhere(
	ctx context.Context,
	whereClause string,
	args []any,
	notFoundDesc string,
) (RegisteredDefinition, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT workflow_id, version, user_version, schema_version, fingerprint, definition_json, source_ref, created_at
FROM multiagent_definitions WHERE `+whereClause, args...)

	var (
		workflowID, userVersion, schemaVersion, fingerprint, defJSON, createdAtStr string
		version                                                                    int64
		sourceRef                                                                  sql.NullString
	)
	if err := row.Scan(
		&workflowID, &version, &userVersion, &schemaVersion, &fingerprint, &defJSON, &sourceRef, &createdAtStr,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RegisteredDefinition{}, fmt.Errorf("%w: %s", ErrDefinitionNotFound, notFoundDesc)
		}
		return RegisteredDefinition{}, fmt.Errorf("multiagent definition store: load definition: %w", err)
	}

	var wf WorkflowDefinition
	if err := json.Unmarshal([]byte(defJSON), &wf); err != nil {
		return RegisteredDefinition{}, fmt.Errorf(
			"multiagent definition store: decode definition %s v%d: %w", workflowID, version, err,
		)
	}
	graph, diags, err := Compile(wf, nil, CompileOptions{})
	if err != nil {
		return RegisteredDefinition{}, fmt.Errorf(
			"multiagent definition store: recompile definition %s v%d: %w (diagnostics: %s)",
			workflowID, version, err, diags.Error(),
		)
	}
	if graph.Fingerprint() != fingerprint {
		return RegisteredDefinition{}, fmt.Errorf(
			"multiagent definition store: recompiled fingerprint mismatch for %s v%d: stored %q, recompiled %q",
			workflowID, version, fingerprint, graph.Fingerprint(),
		)
	}

	createdAt, _ := time.Parse(time.RFC3339Nano, createdAtStr)
	return RegisteredDefinition{
		WorkflowID:    workflowID,
		Version:       version,
		UserVersion:   userVersion,
		SchemaVersion: schemaVersion,
		Fingerprint:   fingerprint,
		Definition:    wf,
		Graph:         stampRegistryVersion(graph, version),
		SourceRef:     sourceRef.String,
		CreatedAt:     createdAt,
	}, nil
}

// ListVersions returns every registered version of workflowID, oldest first,
// as cheap summaries (no definition decode/recompile — see DefinitionSummary's
// doc).
func (s *DefinitionStore) ListVersions(ctx context.Context, workflowID string) ([]DefinitionSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT version, user_version, schema_version, fingerprint, source_ref, created_at
FROM multiagent_definitions WHERE workflow_id = ? ORDER BY version`, workflowID)
	if err != nil {
		return nil, fmt.Errorf("multiagent definition store: list versions: %w", err)
	}
	defer rows.Close()

	var out []DefinitionSummary
	for rows.Next() {
		var sum DefinitionSummary
		var sourceRef sql.NullString
		var createdAtStr string
		if err := rows.Scan(&sum.Version, &sum.UserVersion, &sum.SchemaVersion, &sum.Fingerprint, &sourceRef, &createdAtStr); err != nil {
			return nil, fmt.Errorf("multiagent definition store: scan version summary: %w", err)
		}
		sum.WorkflowID = workflowID
		sum.SourceRef = sourceRef.String
		sum.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("multiagent definition store: iterate version summaries: %w", err)
	}
	return out, nil
}

// ListWorkflows returns every distinct workflow_id with at least one
// registered version, alphabetically.
func (s *DefinitionStore) ListWorkflows(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT workflow_id FROM multiagent_definitions ORDER BY workflow_id`)
	if err != nil {
		return nil, fmt.Errorf("multiagent definition store: list workflows: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("multiagent definition store: scan workflow id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("multiagent definition store: iterate workflow ids: %w", err)
	}
	return out, nil
}

// stampRegistryVersion returns a shallow copy of g with registryVersion set
// to version. CompiledGraph's internal maps/slices are safely shared between
// the two resulting instances: nothing outside this package ever mutates
// them (every exported accessor already returns an independent copy — see
// compiler.go's accessor section), so two structurally-identical
// CompiledGraph values differing only in registryVersion can safely share
// their backing maps/slices, exactly as CompatAdaptDefinition's own direct
// struct-literal construction already relies on.
func stampRegistryVersion(g *CompiledGraph, version int64) *CompiledGraph {
	cp := *g
	cp.registryVersion = int(version)
	return &cp
}

// nullableString converts an empty string to a real SQL NULL rather than
// storing an empty string for an absent source_ref, matching the schema's
// nullable TEXT column.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isUniqueConstraintErr reports whether err looks like a SQLite UNIQUE
// constraint violation. This is deliberately a driver-agnostic string check
// (SQLite's own C library produces this exact substring in its error
// message regardless of which cgo or pure-Go driver wraps it) rather than a
// type assertion against a specific driver's error type, so this file does
// not need to know or import which sql.DB driver prismsqlite.DriverName
// currently resolves to.
func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT")
}
