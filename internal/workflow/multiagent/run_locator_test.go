package multiagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prism/internal/event"
)

// seedLocatorRun writes a real, durable run to root/<runID>/, using the same
// on-disk layout the CLI produces: a manifest file plus a durable-run SQLite
// database populated by actually driving DurableRuntime.Run to completion.
// It returns the terminal RunState so callers can assert against it.
func seedLocatorRun(t *testing.T, root, runID string) RunState {
	t.Helper()
	definition := validDefinition()
	dbPath := filepath.Join(root, runID, "multiagent.db")

	store, err := NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatalf("new durable store: %v", err)
	}
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	runtime := newDurableRuntimeForTest(
		t, definition, happyDurableRunner(), store, FileRunClaimer{Root: root}, events)

	request := testRunRequest()
	request.RunID = runID
	state, err := runtime.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	if err := events.Close(); err != nil {
		t.Fatalf("close seed events: %v", err)
	}

	writeLocatorManifest(t, root, runID, definition)
	return state
}

func writeLocatorManifest(t *testing.T, root, runID string, definition Definition) {
	t.Helper()
	manifest := runManifest{
		SchemaVersion: runManifestSchemaVersion,
		RunID:         runID,
		WorkflowID:    definition.ID,
		Definition:    definition,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, runID, runManifestFileName), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func TestRunLocatorListRunsReturnsSeededRuns(t *testing.T) {
	root := t.TempDir()
	state := seedLocatorRun(t, root, "run-locator-list")

	locator := RunLocator{Root: root}
	summaries, err := locator.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %d, want 1: %+v", len(summaries), summaries)
	}
	got := summaries[0]
	if got.RunID != state.RunID || got.WorkflowID != state.WorkflowID || got.Status != state.Status {
		t.Fatalf("summary = %+v, want run/workflow/status matching %+v", got, state)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("summary timestamps = %+v, want both populated", got)
	}
}

func TestRunLocatorListRunsSkipsDirectoriesWithoutManifest(t *testing.T) {
	root := t.TempDir()
	seedLocatorRun(t, root, "run-locator-real")
	if err := os.MkdirAll(filepath.Join(root, "not-a-run-dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray-file.txt"), []byte("noise"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	locator := RunLocator{Root: root}
	summaries, err := locator.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(summaries) != 1 || summaries[0].RunID != "run-locator-real" {
		t.Fatalf("summaries = %+v, want exactly run-locator-real", summaries)
	}
}

func TestRunLocatorListRunsSkipsCorruptManifestWithoutFailingScan(t *testing.T) {
	root := t.TempDir()
	seedLocatorRun(t, root, "run-locator-good")

	corruptRunID := "run-locator-corrupt"
	if err := os.MkdirAll(filepath.Join(root, corruptRunID), 0o755); err != nil {
		t.Fatalf("mkdir corrupt run: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, corruptRunID, runManifestFileName),
		[]byte("{not valid json"),
		0o644,
	); err != nil {
		t.Fatalf("write corrupt manifest: %v", err)
	}

	locator := RunLocator{Root: root}
	summaries, err := locator.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(summaries) != 1 || summaries[0].RunID != "run-locator-good" {
		t.Fatalf("summaries = %+v, want exactly run-locator-good", summaries)
	}
}

func TestRunLocatorListRunsOnMissingRootReturnsEmpty(t *testing.T) {
	locator := RunLocator{Root: filepath.Join(t.TempDir(), "does-not-exist")}
	summaries, err := locator.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns on missing root: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("summaries = %+v, want none", summaries)
	}
}

func TestRunLocatorOpenInspectionSucceedsForSeededRun(t *testing.T) {
	root := t.TempDir()
	state := seedLocatorRun(t, root, "run-locator-open")

	locator := RunLocator{Root: root}
	runtime, store, events, err := locator.OpenInspection(context.Background(), "run-locator-open")
	if err != nil {
		t.Fatalf("OpenInspection: %v", err)
	}
	defer store.Close()
	defer events.Close()

	record, err := runtime.Inspect(context.Background(), "run-locator-open")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if record.State.RunID != state.RunID || record.State.Status != state.Status {
		t.Fatalf("inspected state = %+v, want to match seeded state %+v", record.State, state)
	}

	// An inspection-only runtime must never be able to execute a role.
	_, runErr := runtime.Run(context.Background(), RunRequest{
		RunID: "run-locator-open-should-not-execute",
		Task:  TaskReference{ID: "task-x"},
	})
	if runErr == nil {
		t.Fatal("expected inspection-only runtime to refuse execution, got nil error")
	}
}

func TestRunLocatorOpenInspectionMissingManifestReturnsClearError(t *testing.T) {
	root := t.TempDir()
	locator := RunLocator{Root: root}

	_, _, _, err := locator.OpenInspection(context.Background(), "run-that-does-not-exist")
	if err == nil {
		t.Fatal("expected a clear error for a missing manifest, got nil")
	}
}

func TestRunLocatorOpenInspectionMissingDatabaseReturnsClearError(t *testing.T) {
	root := t.TempDir()
	runID := "run-locator-missing-db"
	if err := os.MkdirAll(filepath.Join(root, runID), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLocatorManifest(t, root, runID, validDefinition())
	// Deliberately do not create multiagent.db.

	locator := RunLocator{Root: root}
	_, _, _, err := locator.OpenInspection(context.Background(), runID)
	if err == nil {
		t.Fatal("expected a clear error for a missing database, got nil")
	}
}

func TestRunLocatorOpenInspectionCorruptDatabaseReturnsClearError(t *testing.T) {
	root := t.TempDir()
	runID := "run-locator-corrupt-db"
	if err := os.MkdirAll(filepath.Join(root, runID), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLocatorManifest(t, root, runID, validDefinition())
	if err := os.WriteFile(
		filepath.Join(root, runID, runDatabaseFileName),
		[]byte("this is not a sqlite database"),
		0o644,
	); err != nil {
		t.Fatalf("write corrupt database: %v", err)
	}

	locator := RunLocator{Root: root}
	_, _, _, err := locator.OpenInspection(context.Background(), runID)
	if err == nil {
		t.Fatal("expected a clear error for a corrupt database, got nil")
	}
}

func TestRunLocatorRequiresRoot(t *testing.T) {
	var locator RunLocator
	if _, err := locator.ListRuns(context.Background()); err == nil {
		t.Fatal("expected ListRuns to reject an empty root")
	}
	if _, _, _, err := locator.OpenInspection(context.Background(), "any-run"); err == nil {
		t.Fatal("expected OpenInspection to reject an empty root")
	}
}
