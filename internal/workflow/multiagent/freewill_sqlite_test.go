package multiagent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/freewill"
)

func TestFreewillAdmissionReusesTerminalRunAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	if _, err := store.Create(ctx, freewillAdmissionRecord("old", "agent", scope, freewill.StateSleeping), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "old", "agent", scope); err != nil { t.Fatal(err) }
	if err := store.EnsureFreewillActorAdmission(ctx, "new", "agent", scope); err != nil { t.Fatalf("terminal run must release admission: %v", err) }
	if err := store.EnsureFreewillActorAdmission(ctx, "new", "agent", scope); err != nil { t.Fatalf("same run must be idempotent: %v", err) }
}

func TestFreewillAdmissionLifecycleReuseRules(t *testing.T) {
	ctx := context.Background()
	for _, lifecycle := range []freewill.State{freewill.StateDenied, freewill.StateHalted} {
		t.Run(string(lifecycle), func(t *testing.T) {
			store := newSQLiteDurableStoreForTest(t)
			scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
			if _, err := store.Create(ctx, freewillAdmissionRecord("old", "agent", scope, lifecycle), nil); err != nil { t.Fatal(err) }
			if err := store.EnsureFreewillActorAdmission(ctx, "old", "agent", scope); err != nil { t.Fatal(err) }
			if err := store.EnsureFreewillActorAdmission(ctx, "new", "agent", scope); err != nil { t.Fatalf("inactive %s run must release admission: %v", lifecycle, err) }
		})
	}
	t.Run("paused_without_receipt_remains_active", func(t *testing.T) {
		store := newSQLiteDurableStoreForTest(t)
		scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
		if _, err := store.Create(ctx, freewillAdmissionRecord("old", "agent", scope, freewill.StatePaused), nil); err != nil { t.Fatal(err) }
		if err := store.EnsureFreewillActorAdmission(ctx, "old", "agent", scope); err != nil { t.Fatal(err) }
		if err := store.EnsureFreewillActorAdmission(ctx, "new", "agent", scope); !errors.Is(err, ErrFreewillActorAdmissionConflict) { t.Fatalf("paused run without receipt = %v", err) }
	})
}

func TestFreewillPausedAdmissionWithValidReceiptCanBeReused(t *testing.T) {
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	request := freewill.IntentionRequest{RequestID: "request", AgentID: "agent", Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: "title", Goal: "goal", ReasonSummary: "reason", RequestedCapabilities: []string{"read_only"}, Scope: scope, IdempotencyKey: "idem", CorrelationID: "corr"}
	candidate := freewill.CandidateIntention{CandidateID: "candidate", Request: request}
	fingerprint, _ := freewillRequestFingerprint(request)
	record := freewillAdmissionRecord("paused", "agent", scope, freewill.StatePaused)
	record.Freewill.ChoiceID = candidate.CandidateID
	record.Freewill.Candidates = []freewill.CandidateIntention{candidate}
	record.Freewill.ApprovalRef = "approval"
	record.Freewill.ApprovalFingerprint = fingerprint
	record.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: "approval", RequestFingerprint: fingerprint, Decision: FreewillApprovalGranted}
	if freewillAdmissionActive(record) { t.Fatal("paused run with a valid approval receipt must be releasable") }
}

func TestFreewillAdmissionPersistsAcrossStoreRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "freewill.db")
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	store, err := NewSQLiteDurableRunStore(dbPath)
	if err != nil { t.Fatal(err) }
	if _, err := store.Create(ctx, freewillAdmissionRecord("old", "agent", scope, freewill.StateSleeping), nil); err != nil { t.Fatal(err) }
	if err := store.EnsureFreewillActorAdmission(ctx, "old", "agent", scope); err != nil { t.Fatal(err) }
	if err := store.Close(); err != nil { t.Fatal(err) }
	store, err = NewSQLiteDurableRunStore(dbPath)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	if err := store.EnsureFreewillActorAdmission(ctx, "new", "agent", scope); err != nil { t.Fatalf("persisted terminal admission was not reusable: %v", err) }
}

func TestFreewillReadOnlyOpenerDoesNotMigrateSchema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "freewill.db")
	store, err := NewSQLiteDurableRunStore(dbPath)
	if err != nil { t.Fatal(err) }
	if _, err := store.Create(ctx, freewillAdmissionRecord("run", "agent", freewill.Scope{ProjectID: "p", Workspace: "w"}, freewill.StateSleeping), nil); err != nil { t.Fatal(err) }
	if err := store.Close(); err != nil { t.Fatal(err) }
	readOnly, err := OpenExistingSQLiteDurableRunStoreReadOnly(dbPath)
	if err != nil { t.Fatal(err) }
	defer readOnly.Close()
	if _, err := readOnly.ListFreewillRuns(ctx, "", 10); err != nil { t.Fatalf("read-only listing should not migrate: %v", err) }
	if _, err := readOnly.LoadFreewillOperatorControl(ctx, "agent", freewill.Scope{ProjectID: "p", Workspace: "w"}); err == nil { t.Fatal("read-only control load unexpectedly attempted a schema mutation") }
}

func TestFreewillAdmissionRejectsActiveAndAmbiguousRuns(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	if _, err := store.Create(ctx, freewillAdmissionRecord("active", "agent", scope, freewill.StateWaking), nil); err != nil { t.Fatal(err) }
	if err := store.EnsureFreewillActorAdmission(ctx, "active", "agent", scope); err != nil { t.Fatal(err) }
	if err := store.EnsureFreewillActorAdmission(ctx, "other", "agent", scope); !errors.Is(err, ErrFreewillActorAdmissionConflict) { t.Fatalf("active conflict = %v", err) }
	if err := store.EnsureFreewillActorAdmission(ctx, "missing", "other-agent", scope); err != nil { t.Fatal(err) }
	if err := store.EnsureFreewillActorAdmission(ctx, "other", "agent", scope); !errors.Is(err, ErrFreewillActorAdmissionConflict) { t.Fatalf("expected active conflict after unrelated admission, got %v", err) }

	if err := store.EnsureFreewillActorAdmission(ctx, "ghost", "ghost-agent", scope); err != nil { t.Fatal(err) }
	if err := store.EnsureFreewillActorAdmission(ctx, "replacement", "ghost-agent", scope); !errors.Is(err, ErrFreewillActorAdmissionAmbiguous) { t.Fatalf("missing prior run = %v", err) }
}

func TestFreewillAdmissionConcurrentOneWinner(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, runID := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); errs <- store.EnsureFreewillActorAdmission(ctx, id, "agent", scope) }(runID)
	}
	wg.Wait(); close(errs)
	var successes int
	for err := range errs { if err == nil { successes++ } }
	if successes != 1 { t.Fatalf("concurrent admission successes = %d, want 1", successes) }
}

func TestFreewillOperatorHaltUnchangedDoesNotAdvanceGeneration(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	now := time.Unix(100, 0).UTC()
	one, err := store.SetFreewillOperatorHalt(ctx, "agent", scope, true, "actor", now)
	if err != nil { t.Fatal(err) }
	two, err := store.SetFreewillOperatorHalt(ctx, "agent", scope, true, "different", now.Add(time.Hour))
	if err != nil { t.Fatal(err) }
	if two.Generation != one.Generation || !two.UpdatedAt.Equal(one.UpdatedAt) || two.Actor != one.Actor { t.Fatalf("unchanged halt mutated receipt: %#v -> %#v", one, two) }
	three, err := store.SetFreewillOperatorHalt(ctx, "agent", scope, false, "different", now.Add(2*time.Hour))
	if err != nil { t.Fatal(err) }
	if three.Generation != one.Generation+1 || three.Halted { t.Fatalf("changed halt = %#v", three) }
}

func freewillAdmissionRecord(runID, agentID string, scope freewill.Scope, lifecycle freewill.State) DurableRun {
	now := time.Now().UTC()
	return DurableRun{SchemaVersion: DurableRunSchemaVersion, Kind: DurableRunKindFreewill, State: RunState{RunID: runID, RequestIdentity: &RequestIdentity{RequesterID: agentID}, CreatedAt: now, UpdatedAt: now}, Phase: CheckpointFreewillWake, Freewill: &FreewillRunState{Lifecycle: lifecycle, Scope: scope, WakeID: runID}}
}
