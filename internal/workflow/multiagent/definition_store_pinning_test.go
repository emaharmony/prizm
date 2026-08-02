package multiagent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prism/internal/event"
)

// pinningTestRunner is a minimal RoleRunner for the acceptance test below: it
// always returns the single outcome ("done") the "worker" role's only edge
// routes to a terminal completed condition, needing no real agent/provider
// wiring.
type pinningTestRunner struct{}

func (pinningTestRunner) RunRole(context.Context, RoleRunRequest) (RoleRunResult, error) {
	return RoleRunResult{Outcome: TransitionOutcome("done"), LocalIterations: 1}, nil
}

// openDurableRunPieces opens a fresh SQLiteDurableRunStore + SQLiteEventStore
// + DurableRuntime against dbPath/graph, mirroring exactly what a real
// process restart does: brand new *sql.DB handles, nothing held over in
// memory from a previous call.
func openDurableRunPieces(
	t *testing.T,
	dbPath, claimRoot string,
	graph *CompiledGraph,
) (*SQLiteDurableRunStore, *event.SQLiteEventStore, *DurableRuntime) {
	t.Helper()
	runStore, err := NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatalf("open durable run store: %v", err)
	}
	eventStore, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		_ = runStore.Close()
		t.Fatalf("open event store: %v", err)
	}
	runtime, err := NewDurableRuntime(
		graph, pinningTestRunner{}, runStore, FileRunClaimer{Root: claimRoot}, eventStore, DurableRuntimeOptions{},
	)
	if err != nil {
		_ = runStore.Close()
		_ = eventStore.Close()
		t.Fatalf("new durable runtime: %v", err)
	}
	return runStore, eventStore, runtime
}

// TestDefinitionRegistry_RunResumeStaysPinnedAcrossRestartAndNewerRegistration
// is PR6's core acceptance gate (see the Phase 3 plan's PR sequence, PR6's
// gate): "register -> run -> resume round-trip survives a process restart
// and resumes against the exact pinned version even after a newer version is
// registered."
//
// The test: (1) registers v1 of a workflow and drives a run to completion
// against it, recording the run's pinned WorkflowVersion/WorkflowFingerprint;
// (2) closes every handle (store, event store, definition store) to
// simulate a process exit; (3) registers a structurally different v2 of the
// SAME workflow_id; (4) simulates a process restart by reopening the
// definition store and the run's store/event store from disk with entirely
// fresh handles, resolves the run's pin via DefinitionStore.Get(workflowID,
// 1) — deliberately NOT Latest() — and confirms Resume() still succeeds and
// still reports WorkflowVersion 1; (5) as a negative control, confirms that
// resuming the SAME run against v2's (now-Latest) compiled graph fails,
// proving the pin is load-bearing rather than incidental.
func TestDefinitionRegistry_RunResumeStaysPinnedAcrossRestartAndNewerRegistration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	definitionsDBPath := filepath.Join(root, "definitions.db")
	runsRoot := filepath.Join(root, "runs")
	const workflowID = "wf-pin-acceptance"
	const runID = "run-pin-acceptance-1"
	runDBPath := filepath.Join(runsRoot, runID, "multiagent.db")

	// --- Step 1: register v1, run it to completion. ---
	defStore, err := NewDefinitionStore(definitionsDBPath)
	if err != nil {
		t.Fatalf("open definition store: %v", err)
	}
	defV1 := testStoreWorkflowDefinition(workflowID, "1.0.0", "v1 content")
	graphV1 := compileForTest(t, defV1)
	regV1, err := defStore.Register(ctx, defV1, graphV1, "test:v1")
	if err != nil {
		t.Fatalf("register v1: %v", err)
	}
	if regV1.Version != 1 {
		t.Fatalf("v1 version = %d, want 1", regV1.Version)
	}

	runStore, eventStore, runtime := openDurableRunPieces(t, runDBPath, runsRoot, regV1.Graph)
	state, err := runtime.Run(ctx, RunRequest{RunID: runID, Task: TaskReference{ID: "task-pin-1"}})
	if err != nil {
		t.Fatalf("run against v1: %v", err)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("run status = %q, want completed", state.Status)
	}
	if state.WorkflowVersion != 1 {
		t.Fatalf("run pinned WorkflowVersion = %d, want 1", state.WorkflowVersion)
	}
	if state.WorkflowFingerprint != regV1.Fingerprint {
		t.Fatalf("run pinned fingerprint = %q, want %q", state.WorkflowFingerprint, regV1.Fingerprint)
	}

	// --- Step 2: simulate a process exit — close every handle. ---
	if err := runStore.Close(); err != nil {
		t.Fatalf("close run store: %v", err)
	}
	if err := eventStore.Close(); err != nil {
		t.Fatalf("close event store: %v", err)
	}
	if err := defStore.Close(); err != nil {
		t.Fatalf("close definition store: %v", err)
	}

	// --- Step 3: register a structurally different v2 for the SAME
	// workflow_id, from a fresh definition store handle. ---
	defStoreForV2, err := NewDefinitionStore(definitionsDBPath)
	if err != nil {
		t.Fatalf("reopen definition store to register v2: %v", err)
	}
	defV2 := testStoreWorkflowDefinition(workflowID, "2.0.0", "v2 content -- genuinely different")
	graphV2 := compileForTest(t, defV2)
	regV2, err := defStoreForV2.Register(ctx, defV2, graphV2, "test:v2")
	if err != nil {
		t.Fatalf("register v2: %v", err)
	}
	if regV2.Version != 2 {
		t.Fatalf("v2 version = %d, want 2", regV2.Version)
	}
	if regV2.Fingerprint == regV1.Fingerprint {
		t.Fatal("v1 and v2 must have different fingerprints for this test to be meaningful")
	}
	if err := defStoreForV2.Close(); err != nil {
		t.Fatalf("close definition store after registering v2: %v", err)
	}

	// --- Step 4: simulate a process restart. Reopen the definition store
	// AND the run's store/event store with entirely fresh handles (nothing
	// held over in memory), resolve the run's PIN explicitly by version (1),
	// not Latest (which is now 2), and resume. ---
	defStoreAfterRestart, err := NewDefinitionStore(definitionsDBPath)
	if err != nil {
		t.Fatalf("reopen definition store after restart: %v", err)
	}
	pinned, err := defStoreAfterRestart.Get(ctx, workflowID, regV1.Version)
	if err != nil {
		t.Fatalf("resolve pinned v1 after restart: %v", err)
	}
	if pinned.Fingerprint != regV1.Fingerprint {
		t.Fatalf("pinned fingerprint after restart = %q, want %q", pinned.Fingerprint, regV1.Fingerprint)
	}

	runStoreResume, eventStoreResume, runtimeResume := openDurableRunPieces(t, runDBPath, runsRoot, pinned.Graph)
	resumed, err := runtimeResume.Resume(ctx, runID)
	if err != nil {
		t.Fatalf("resume against pinned v1 after restart: %v", err)
	}
	if resumed.Status != RunStatusCompleted {
		t.Fatalf("resumed status = %q, want completed", resumed.Status)
	}
	if resumed.WorkflowVersion != 1 {
		t.Fatalf("resumed run WorkflowVersion = %d, want 1 (still pinned to v1 despite v2 being registered)", resumed.WorkflowVersion)
	}
	if resumed.WorkflowFingerprint != regV1.Fingerprint {
		t.Fatalf("resumed run fingerprint = %q, want v1's %q", resumed.WorkflowFingerprint, regV1.Fingerprint)
	}
	if err := runStoreResume.Close(); err != nil {
		t.Fatalf("close resumed run store: %v", err)
	}
	if err := eventStoreResume.Close(); err != nil {
		t.Fatalf("close resumed event store: %v", err)
	}
	if err := defStoreAfterRestart.Close(); err != nil {
		t.Fatalf("close definition store after resume: %v", err)
	}

	// --- Step 5: negative control. Resolving via Latest() now returns v2;
	// attempting to resume the SAME (already-terminal) run against v2's
	// compiled graph must fail rather than silently succeed — proving the
	// pin in step 4 was load-bearing, not incidental (RunState.Validate
	// rejects a WorkflowVersion mismatch: 1 recorded vs. 2 on the graph
	// passed in).
	defStoreForLatest, err := NewDefinitionStore(definitionsDBPath)
	if err != nil {
		t.Fatalf("reopen definition store for latest check: %v", err)
	}
	latest, err := defStoreForLatest.Latest(ctx, workflowID)
	if err != nil {
		t.Fatalf("resolve latest: %v", err)
	}
	if latest.Version != 2 {
		t.Fatalf("latest version = %d, want 2", latest.Version)
	}

	runStoreWrong, eventStoreWrong, runtimeWrong := openDurableRunPieces(t, runDBPath, runsRoot, latest.Graph)
	if _, err := runtimeWrong.Resume(ctx, runID); err == nil {
		t.Fatal("resuming a v1-pinned run against v2's (latest) compiled graph must fail, not silently succeed")
	} else {
		var recoveryErr *RecoveryFailedError
		if !errors.As(err, &recoveryErr) {
			t.Fatalf("resume-against-wrong-version error = %v (%T), want *RecoveryFailedError", err, err)
		}
	}
	if err := runStoreWrong.Close(); err != nil {
		t.Fatalf("close wrong-version run store: %v", err)
	}
	if err := eventStoreWrong.Close(); err != nil {
		t.Fatalf("close wrong-version event store: %v", err)
	}
	if err := defStoreForLatest.Close(); err != nil {
		t.Fatalf("close definition store after negative control: %v", err)
	}
}
