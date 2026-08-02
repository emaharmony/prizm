package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emaharmony/prism/internal/event"
)

// Run directory layout constants. RunLocator follows the same on-disk
// convention the CLI already produces (cmd/prism-cli/cmd_workflow_multiagent.go):
// <Root>/<runID>/multiagent_manifest.json and <Root>/<runID>/multiagent.db.
const (
	runManifestFileName      = "multiagent_manifest.json"
	runDatabaseFileName      = "multiagent.db"
	runManifestSchemaVersion = 1
)

// RunSummary is the minimal listing view of one durable multi-agent run.
type RunSummary struct {
	RunID      string    `json:"run_id"`
	WorkflowID string    `json:"workflow_id"`
	Status     RunStatus `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// RunLocator discovers and opens durable multi-agent runs persisted under a
// shared root directory (the CLI's existing runDir). It promotes the
// locate/open logic already duplicated in cmd_workflow_multiagent.go
// (loadReferenceManifest/openReferenceRuntime) into this package;
// cmd_workflow_multiagent.go itself is left unmodified in this milestone.
type RunLocator struct {
	Root string

	// DefinitionStore resolves a registry-backed run's pinned
	// (WorkflowID, WorkflowVersion) into a *CompiledGraph (PR6). Nil is
	// valid and matches the pre-PR6 zero-value-disables convention used
	// elsewhere in this package: a nil store means OpenInspection can only
	// open legacy, CompatAdaptDefinition-backed runs (every run this package
	// supported before PR6) and returns a clear error for a registry-backed
	// one instead of a nil-pointer panic. When set, OpenInspection opens its
	// own *DefinitionStore transiently for a run whose manifest carries a
	// DefinitionDBPath instead, so this field only needs to be set by a
	// caller (e.g. `prism serve`) that wants to reuse one already-open store
	// rather than paying the open/close cost per inspection.
	DefinitionStore *DefinitionStore
}

// runManifest is the subset of the CLI's referenceWorkflowManifest this
// package needs to open a run for inspection. It intentionally decodes only
// the fields required here rather than importing cmd/prism-cli's manifest
// type: cmd/prism-cli already imports this package, so the reverse import
// would be circular. json.Unmarshal ignores manifest fields not declared
// here (e.g. "input"), so this stays forward-compatible with the CLI's
// fuller manifest.
//
// WorkflowVersion/DefinitionDBPath are PR6 additions, additive and
// omitempty: a pre-PR6 manifest decodes with WorkflowVersion == 0, which
// readManifest/OpenInspection treat as "legacy, CompatAdaptDefinition-backed
// run" — the exact behavior every manifest had before these two fields
// existed. A registry-backed run (PR6's `graph run`) sets WorkflowVersion to
// its registry-assigned integer version and leaves Definition at its zero
// value; the two are mutually exclusive by construction, and WorkflowVersion
// > 0 is the discriminator readManifest/OpenInspection branch on.
type runManifest struct {
	SchemaVersion    int        `json:"schema_version"`
	RunID            string     `json:"run_id"`
	WorkflowID       string     `json:"workflow_id"`
	Definition       Definition `json:"definition"`
	WorkflowVersion  int64      `json:"workflow_version,omitempty"`
	DefinitionDBPath string     `json:"definition_db_path,omitempty"`
}

// registryBacked reports whether m describes a PR6 registry-backed run
// (WorkflowVersion > 0) rather than a legacy CompatAdaptDefinition-backed
// one.
func (m runManifest) registryBacked() bool {
	return m.WorkflowVersion > 0
}

func (l RunLocator) manifestPath(runID string) string {
	return filepath.Join(l.Root, runID, runManifestFileName)
}

func (l RunLocator) databasePath(runID string) string {
	return filepath.Join(l.Root, runID, runDatabaseFileName)
}

// ListRuns scans Root for run directories and returns a status summary for
// each recognized run. A directory without a manifest is silently skipped
// (Root may contain unrelated entries); a directory with a manifest that
// fails to decode, fails validation, or whose database fails to load is
// also skipped rather than failing the entire scan — one damaged run must
// not hide the rest of an operator's run list. ListRuns performs no writes:
// it never creates a database file for a run that does not already have
// one.
func (l RunLocator) ListRuns(ctx context.Context) ([]RunSummary, error) {
	if strings.TrimSpace(l.Root) == "" {
		return nil, errors.New("multiagent: run locator root is required")
	}
	entries, err := os.ReadDir(l.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("multiagent: list run directory %s: %w", l.Root, err)
	}

	var summaries []RunSummary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runID := entry.Name()
		manifest, err := l.readManifest(runID)
		if err != nil {
			continue
		}
		record, err := l.loadRecord(ctx, runID)
		if err != nil {
			continue
		}
		summaries = append(summaries, RunSummary{
			RunID:      record.State.RunID,
			WorkflowID: firstNonEmptyString(record.State.WorkflowID, manifest.WorkflowID),
			Status:     record.State.Status,
			CreatedAt:  record.State.CreatedAt,
			UpdatedAt:  record.State.UpdatedAt,
		})
	}
	return summaries, nil
}

// OpenInspection opens a run for read-only inspection: it returns a durable
// runtime seeded with a runner that always fails if invoked, since
// inspection-only callers use it exclusively for Inspect/Cancel, never for
// live Run/Resume execution requiring a real agent registry.
//
// Deviation from the originally sketched two-value-plus-error return: the
// underlying *SQLiteDurableRunStore is not otherwise reachable from the
// returned *DurableRuntime (it is held on an unexported field, and
// DurableRuntime exposes no Close method — this milestone must not modify
// durable_runtime.go to add one). Returning only (*DurableRuntime,
// *event.SQLiteEventStore, error) would leave the store's SQLite connection
// unclosable by the caller. The store is returned as a third value so
// callers can close both stores deterministically.
func (l RunLocator) OpenInspection(
	ctx context.Context,
	runID string,
) (*DurableRuntime, *SQLiteDurableRunStore, *event.SQLiteEventStore, error) {
	if strings.TrimSpace(l.Root) == "" {
		return nil, nil, nil, errors.New("multiagent: run locator root is required")
	}
	manifest, err := l.readManifest(runID)
	if err != nil {
		return nil, nil, nil, err
	}

	dbPath := l.databasePath(runID)
	store, err := openExistingDurableStore(dbPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("multiagent: open durable store for run %q: %w", runID, err)
	}
	eventStore, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, fmt.Errorf("multiagent: open event store for run %q: %w", runID, err)
	}

	// Two convergence paths onto the same *CompiledGraph, matching compat.go's
	// "single convergence point" design: a legacy run's manifest carries an
	// embedded Definition (CompatAdaptDefinition, unchanged since PR4 — a
	// signature retarget, not a logic rewrite, and readManifest already
	// called manifest.Definition.Validate() for this case, so this does not
	// re-validate); a PR6 registry-backed run's manifest instead carries
	// (WorkflowID, WorkflowVersion, DefinitionDBPath) and resolves its exact
	// pinned version through a DefinitionStore — deliberately Get(id,
	// version), never Latest, so resuming a run after a newer version has
	// been registered still executes against the version it was started
	// with (see definition_store.go's doc and
	// definition_store_pinning_test.go's acceptance test).
	var graph *CompiledGraph
	if manifest.registryBacked() {
		graph, err = l.resolveRegistryGraph(ctx, runID, manifest)
		if err != nil {
			_ = store.Close()
			_ = eventStore.Close()
			return nil, nil, nil, err
		}
	} else {
		graph, err = CompatAdaptDefinition(manifest.Definition)
		if err != nil {
			_ = store.Close()
			_ = eventStore.Close()
			return nil, nil, nil, fmt.Errorf("multiagent: adapt workflow definition for run %q: %w", runID, err)
		}
	}

	runtime, err := NewDurableRuntime(
		graph,
		noopRoleRunner{},
		store,
		FileRunClaimer{Root: l.Root},
		eventStore,
		DurableRuntimeOptions{},
	)
	if err != nil {
		_ = store.Close()
		_ = eventStore.Close()
		return nil, nil, nil, err
	}
	return runtime, store, eventStore, nil
}

// resolveRegistryGraph resolves a registry-backed manifest's pinned
// (WorkflowID, WorkflowVersion) to a *CompiledGraph. It prefers l.DefinitionStore
// when the caller configured one (avoids a per-inspection open/close); when
// not configured, it opens the manifest's own DefinitionDBPath transiently —
// the graph value returned does not hold a reference to the store's SQLite
// connection, so closing the store immediately after Get is safe.
func (l RunLocator) resolveRegistryGraph(ctx context.Context, runID string, manifest runManifest) (*CompiledGraph, error) {
	store := l.DefinitionStore
	if store == nil {
		if strings.TrimSpace(manifest.DefinitionDBPath) == "" {
			return nil, fmt.Errorf(
				"multiagent: run %q is registry-backed but no definition store is configured and its manifest has no definition_db_path", runID,
			)
		}
		opened, err := NewDefinitionStore(manifest.DefinitionDBPath)
		if err != nil {
			return nil, fmt.Errorf("multiagent: open definition store for run %q: %w", runID, err)
		}
		defer opened.Close()
		store = opened
	}
	reg, err := store.Get(ctx, manifest.WorkflowID, manifest.WorkflowVersion)
	if err != nil {
		return nil, fmt.Errorf("multiagent: resolve registry definition for run %q: %w", runID, err)
	}
	return reg.Graph, nil
}

// Definition returns the workflow Definition recorded in a run's manifest.
// Milestone 2's dashboard snapshot API needs the Definition (roles,
// transitions, budget ceilings) alongside the DurableRun OpenInspection
// already exposes, since BuildDashboardSnapshot takes Definition as an
// explicit parameter (dashboard_snapshot.go) rather than deriving it from
// DurableRun, which does not carry it. This is a thin, read-only accessor
// over the same manifest OpenInspection already reads — no new I/O pattern.
//
// Known PR6 limitation (dashboard UI integration is explicitly out of scope
// for Phase 3 — see the plan's scope note): for a registry-backed run this
// returns a zero-value Definition, since that run has no legacy Definition
// embedded in its manifest at all. A caller that needs the actual graph for
// a registry-backed run should use OpenInspection (which resolves the real
// CompiledGraph through the definition registry) rather than this accessor.
func (l RunLocator) Definition(runID string) (Definition, error) {
	if strings.TrimSpace(l.Root) == "" {
		return Definition{}, errors.New("multiagent: run locator root is required")
	}
	manifest, err := l.readManifest(runID)
	if err != nil {
		return Definition{}, err
	}
	return manifest.Definition, nil
}

func (l RunLocator) readManifest(runID string) (runManifest, error) {
	data, err := os.ReadFile(l.manifestPath(runID))
	if err != nil {
		return runManifest{}, fmt.Errorf("multiagent: read manifest for run %q: %w", runID, err)
	}
	var manifest runManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return runManifest{}, fmt.Errorf("multiagent: decode manifest for run %q: %w", runID, err)
	}
	if manifest.SchemaVersion != runManifestSchemaVersion || manifest.RunID != runID {
		return runManifest{}, fmt.Errorf("multiagent: unsupported or inconsistent manifest for run %q", runID)
	}
	// A PR6 registry-backed run's manifest carries a zero-value Definition by
	// construction (its CompiledGraph comes from a DefinitionStore lookup,
	// not an embedded legacy Definition) — Definition.Validate() would
	// reject that zero value, so it only runs for a legacy manifest.
	if !manifest.registryBacked() {
		if err := manifest.Definition.Validate(); err != nil {
			return runManifest{}, fmt.Errorf("multiagent: invalid workflow definition for run %q: %w", runID, err)
		}
	}
	return manifest, nil
}

func (l RunLocator) loadRecord(ctx context.Context, runID string) (DurableRun, error) {
	store, err := openExistingDurableStore(l.databasePath(runID))
	if err != nil {
		return DurableRun{}, err
	}
	defer store.Close()
	return store.Load(ctx, runID)
}

// openExistingDurableStore opens a durable run store without creating a
// fresh empty database: NewSQLiteDurableRunStore auto-creates its schema on
// open, which is the right behavior for the CLI writing a new run but wrong
// for a read-only locator inspecting a run that is expected to already
// exist. Stat-checking first turns "no such run" into a clear error instead
// of a silently-created empty database.
func openExistingDurableStore(dbPath string) (*SQLiteDurableRunStore, error) {
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			// Wrapped with %w (unlike a plain fmt.Errorf) so callers — notably
			// the API's not-found classification — can use errors.Is(err,
			// os.ErrNotExist) uniformly across every "run does not exist"
			// path this package produces, not just the manifest-read path.
			return nil, fmt.Errorf("multiagent: run database not found at %s: %w", dbPath, err)
		}
		return nil, fmt.Errorf("multiagent: inspect run database at %s: %w", dbPath, err)
	}
	return NewSQLiteDurableRunStore(dbPath)
}

// noopRoleRunner is the package-local, inspection-only RoleRunner: it exists
// so RunLocator can build a DurableRuntime for Inspect/Cancel without wiring
// live agent execution. It mirrors cmd/prism-cli's unavailableRoleRunner
// (package main), which cannot be reused here since it lives in a package
// this one must not depend on.
type noopRoleRunner struct{}

func (noopRoleRunner) RunRole(context.Context, RoleRunRequest) (RoleRunResult, error) {
	return RoleRunResult{}, errors.New("multiagent: execution is unavailable in inspection mode")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
