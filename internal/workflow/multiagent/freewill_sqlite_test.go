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
	if _, err := store.Create(ctx, freewillAdmissionRecord("new", "agent", scope, freewill.StateWaking), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "old", "agent", scope); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "new", "agent", scope); err != nil {
		t.Fatalf("terminal run must release admission: %v", err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "new", "agent", scope); err != nil {
		t.Fatalf("same run must be idempotent: %v", err)
	}
}

func TestFreewillAdmissionLifecycleReuseRules(t *testing.T) {
	ctx := context.Background()
	for _, lifecycle := range []freewill.State{freewill.StateDenied, freewill.StateHalted} {
		t.Run(string(lifecycle), func(t *testing.T) {
			store := newSQLiteDurableStoreForTest(t)
			scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
			if _, err := store.Create(ctx, freewillAdmissionRecord("old", "agent", scope, lifecycle), nil); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Create(ctx, freewillAdmissionRecord("new", "agent", scope, freewill.StateWaking), nil); err != nil {
				t.Fatal(err)
			}
			if err := store.EnsureFreewillActorAdmission(ctx, "old", "agent", scope); err != nil {
				t.Fatal(err)
			}
			if err := store.EnsureFreewillActorAdmission(ctx, "new", "agent", scope); err != nil {
				t.Fatalf("inactive %s run must release admission: %v", lifecycle, err)
			}
		})
	}
	t.Run("paused_without_receipt_remains_active", func(t *testing.T) {
		store := newSQLiteDurableStoreForTest(t)
		scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
		if _, err := store.Create(ctx, freewillAdmissionRecord("old", "agent", scope, freewill.StatePaused), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(ctx, freewillAdmissionRecord("new", "agent", scope, freewill.StateWaking), nil); err != nil {
			t.Fatal(err)
		}
		if err := store.EnsureFreewillActorAdmission(ctx, "old", "agent", scope); err != nil {
			t.Fatal(err)
		}
		if err := store.EnsureFreewillActorAdmission(ctx, "new", "agent", scope); !errors.Is(err, ErrFreewillActorAdmissionConflict) {
			t.Fatalf("paused run without receipt = %v", err)
		}
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
	if freewillAdmissionActive(record) {
		t.Fatal("paused run with a valid approval receipt must be releasable")
	}
}

func TestFreewillAdmissionPersistsAcrossStoreRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "freewill.db")
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	store, err := NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, freewillAdmissionRecord("old", "agent", scope, freewill.StateSleeping), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, freewillAdmissionRecord("new", "agent", scope, freewill.StateWaking), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "old", "agent", scope); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureFreewillActorAdmission(ctx, "new", "agent", scope); err != nil {
		t.Fatalf("persisted terminal admission was not reusable: %v", err)
	}
}

func TestFreewillReadOnlyOpenerDoesNotMigrateSchema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "freewill.db")
	store, err := NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, freewillAdmissionRecord("run", "agent", freewill.Scope{ProjectID: "p", Workspace: "w"}, freewill.StateSleeping), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenExistingSQLiteDurableRunStoreReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if _, err := readOnly.ListFreewillRuns(ctx, "", 10); err != nil {
		t.Fatalf("read-only listing should not migrate: %v", err)
	}
	if _, err := readOnly.LoadFreewillOperatorControl(ctx, "agent", freewill.Scope{ProjectID: "p", Workspace: "w"}); err == nil {
		t.Fatal("read-only control load unexpectedly attempted a schema mutation")
	}
}

func TestFreewillAdmissionRejectsActiveAndAmbiguousRuns(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	if _, err := store.Create(ctx, freewillAdmissionRecord("active", "agent", scope, freewill.StateWaking), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, freewillAdmissionRecord("other", "agent", scope, freewill.StateWaking), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "active", "agent", scope); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "other", "agent", scope); !errors.Is(err, ErrFreewillActorAdmissionConflict) {
		t.Fatalf("active conflict = %v", err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "missing", "other-agent", scope); !errors.Is(err, ErrFreewillActorAdmissionAmbiguous) {
		t.Fatalf("missing target run = %v", err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "other", "agent", scope); !errors.Is(err, ErrFreewillActorAdmissionConflict) {
		t.Fatalf("expected active conflict after unrelated admission, got %v", err)
	}

	if _, err := store.Create(ctx, freewillAdmissionRecord("replacement", "ghost-agent", scope, freewill.StateWaking), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "INSERT INTO multiagent_freewill_admissions(actor_key,run_id,agent_id,project_id,workspace,updated_at) VALUES(?,?,?,?,?,?)", freewillActorAdmissionKey("ghost-agent"), "ghost", "ghost-agent", scope.ProjectID, scope.Workspace, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "replacement", "ghost-agent", scope); !errors.Is(err, ErrFreewillActorAdmissionAmbiguous) {
		t.Fatalf("missing prior run = %v", err)
	}
}

func TestFreewillAdmissionConcurrentOneWinner(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	for _, runID := range []string{"a", "b"} {
		if _, err := store.Create(ctx, freewillAdmissionRecord(runID, "agent", scope, freewill.StateWaking), nil); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, runID := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); errs <- store.EnsureFreewillActorAdmission(ctx, id, "agent", scope) }(runID)
	}
	wg.Wait()
	close(errs)
	var successes int
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent admission successes = %d, want 1", successes)
	}
}

func TestFreewillOperatorHaltUnchangedDoesNotAdvanceGeneration(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	now := time.Unix(100, 0).UTC()
	one, err := store.SetFreewillOperatorHalt(ctx, "agent", scope, true, "actor", now)
	if err != nil {
		t.Fatal(err)
	}
	two, err := store.SetFreewillOperatorHalt(ctx, "agent", scope, true, "different", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if two.Generation != one.Generation || !two.UpdatedAt.Equal(one.UpdatedAt) || two.Actor != one.Actor {
		t.Fatalf("unchanged halt mutated receipt: %#v -> %#v", one, two)
	}
	three, err := store.SetFreewillOperatorHalt(ctx, "agent", scope, false, "different", now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if three.Generation != one.Generation+1 || three.Halted {
		t.Fatalf("changed halt = %#v", three)
	}
}

func freewillAdmissionRecord(runID, agentID string, scope freewill.Scope, lifecycle freewill.State) DurableRun {
	now := time.Now().UTC()
	return DurableRun{SchemaVersion: DurableRunSchemaVersion, Kind: DurableRunKindFreewill, State: RunState{
		SchemaVersion: RunStateSchemaVersion, RunID: runID,
		RequestIdentity: &RequestIdentity{RequesterID: agentID, SessionID: runID, Source: "freewill_scheduler", IdempotencyKey: "dedupe-" + runID, AcceptedAt: now},
		WorkflowID:      FreewillWorkflowID, WorkflowVersion: 1, CurrentTask: TaskReference{ID: "freewill-wake"}, Status: RunStatusCreated, CreatedAt: now, UpdatedAt: now,
	}, Phase: CheckpointFreewillWake, Freewill: &FreewillRunState{
		Lifecycle: lifecycle, Scope: scope, WakeID: runID, Trigger: "scheduled_tick", DedupeKey: "dedupe-" + runID,
		CorrelationID: runID, ExecutionDisabled: true,
	}}
}

func TestFreewillWakeAcceptanceRollsBackRunKeyAndOutboxOnAdmissionConflict(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	if _, err := store.Create(ctx, freewillAdmissionRecord("active", "agent", scope, freewill.StateWaking), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "active", "agent", scope); err != nil {
		t.Fatal(err)
	}
	wake, record := freewillWakeAcceptanceFixture("blocked", "agent", scope, "dedupe-blocked")
	if _, _, err := store.AcceptFreewillWake(ctx, record, nil, wake); !errors.Is(err, ErrFreewillActorAdmissionConflict) {
		t.Fatalf("accept conflict = %v", err)
	}
	if _, err := store.Load(ctx, wake.RunID); err == nil {
		t.Fatal("conflicting wake left a durable run behind")
	}
	var keys, outbox int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM multiagent_acceptance_keys WHERE run_id = ?", wake.RunID).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM multiagent_outbox WHERE run_id = ?", wake.RunID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if keys != 0 || outbox != 0 {
		t.Fatalf("conflicting wake left key/outbox rows: %d/%d", keys, outbox)
	}
}

func TestFreewillWakeAcceptanceIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	wake, record := freewillWakeAcceptanceFixture("accepted", "agent", scope, "dedupe-accepted")
	first, created, err := store.AcceptFreewillWake(ctx, record, nil, wake)
	if err != nil || !created {
		t.Fatalf("first accept = (%v, %v), want created", err, created)
	}
	second, created, err := store.AcceptFreewillWake(ctx, record, nil, wake)
	if err != nil || created {
		t.Fatalf("repeat accept = (%v, %v), want idempotent existing", err, created)
	}
	if first.State.RunID != second.State.RunID {
		t.Fatalf("idempotent run changed: %q to %q", first.State.RunID, second.State.RunID)
	}
	if _, err := store.Create(ctx, freewillAdmissionRecord("other", "agent", scope, freewill.StateWaking), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "other", "agent", scope); !errors.Is(err, ErrFreewillActorAdmissionConflict) {
		t.Fatalf("accepted run did not reserve actor slot: %v", err)
	}
}

func TestFreewillWakeAcceptanceConcurrentOneWinner(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(runID string) {
			defer wg.Done()
			wake, record := freewillWakeAcceptanceFixture(runID, "agent", scope, "dedupe-"+runID)
			_, _, err := store.AcceptFreewillWake(ctx, record, nil, wake)
			errs <- err
		}(id)
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent wake acceptances succeeded %d times, want one", successes)
	}
	runs, err := store.ListFreewillRuns(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("persisted Freewill runs = %d, want only admitted winner", len(runs))
	}
}

func freewillWakeAcceptanceFixture(runID, agentID string, scope freewill.Scope, dedupe string) (FreewillWakeRequest, DurableRun) {
	now := time.Now().UTC()
	wake := FreewillWakeRequest{RunID: runID, AgentID: agentID, Scope: scope, Trigger: "scheduled_tick", DedupeKey: dedupe, CorrelationID: runID}
	record := DurableRun{
		SchemaVersion: DurableRunSchemaVersion, Kind: DurableRunKindFreewill,
		State: RunState{SchemaVersion: RunStateSchemaVersion, RunID: runID,
			RequestIdentity: &RequestIdentity{RequesterID: agentID, SessionID: runID, Source: "freewill_scheduler", IdempotencyKey: dedupe, AcceptedAt: now},
			WorkflowID:      FreewillWorkflowID, WorkflowVersion: 1, CurrentTask: TaskReference{ID: "freewill-wake"}, Status: RunStatusCreated, CreatedAt: now, UpdatedAt: now},
		Phase: CheckpointFreewillWake,
		Freewill: &FreewillRunState{Lifecycle: freewill.StateWaking, Scope: scope,
			SelectionAttemptLimit: maxFreewillSelectionAttempts, WakeID: runID, Trigger: wake.Trigger,
			DedupeKey: dedupe, CorrelationID: runID, ExecutionDisabled: true},
	}
	return wake, record
}

type recoveryAdmissionStore struct {
	*SQLiteDurableRunStore
	conflictRunID string
	conflictErr   error
}

func (s *recoveryAdmissionStore) EnsureFreewillActorAdmission(ctx context.Context, runID, agentID string, scope freewill.Scope) error {
	if runID == s.conflictRunID {
		return s.conflictErr
	}
	return s.SQLiteDurableRunStore.EnsureFreewillActorAdmission(ctx, runID, agentID, scope)
}

type sleepingFreewillSelector struct{ calls int }

func (s *sleepingFreewillSelector) SelectFreewill(context.Context, FreewillSelectionInput) (freewill.Selection, FreewillSelectionEvidence, error) {
	s.calls++
	return freewill.Selection{SleepReason: "wait"}, FreewillSelectionEvidence{}, nil
}

func TestFreewillRecoverySkipsVerifiedLegacyConflictAndContinues(t *testing.T) {
	ctx := context.Background()
	env := newDurableTestEnvironment(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	for _, id := range []string{"a-queued", "b-process"} {
		_, record := freewillWakeAcceptanceFixture(id, "agent", scope, "dedupe-"+id)
		if _, err := env.store.Create(ctx, record, nil); err != nil {
			t.Fatal(err)
		}
	}
	store := &recoveryAdmissionStore{SQLiteDurableRunStore: env.store, conflictRunID: "a-queued", conflictErr: ErrFreewillActorAdmissionConflict}
	runtime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, store, env.claimer, env.events)
	selector := &sleepingFreewillSelector{}
	control, err := NewFreewillOperatorControl(env.store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewFreewillWakeService(runtime, store, selector, FreewillWakeServiceConfig{
		Enabled: true, SchedulingEnabled: true, AgentID: "agent", Scope: scope,
		Objective: "Choose a safe read-only next action.", Owner: "recovery-test", LeaseFor: time.Minute,
		OperatorControl: control,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverPending(ctx, time.Now().Add(time.Minute), 10); err != nil {
		t.Fatalf("recovery stopped on verified conflict: %v", err)
	}
	queued, err := env.store.Load(ctx, "a-queued")
	if err != nil {
		t.Fatal(err)
	}
	processed, err := env.store.Load(ctx, "b-process")
	if err != nil {
		t.Fatal(err)
	}
	if queued.Freewill.Lifecycle != freewill.StateWaking {
		t.Fatalf("conflicted legacy row changed to %s", queued.Freewill.Lifecycle)
	}
	if processed.Freewill.Lifecycle != freewill.StateSleeping {
		t.Fatalf("later legacy row lifecycle = %s, want sleeping", processed.Freewill.Lifecycle)
	}
	if selector.calls != 1 {
		t.Fatalf("selector calls = %d, want only later row to be processed", selector.calls)
	}
}

func TestFreewillRecoveryDoesNotSkipAmbiguousLegacyAdmission(t *testing.T) {
	ctx := context.Background()
	env := newDurableTestEnvironment(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	_, record := freewillWakeAcceptanceFixture("ambiguous", "agent", scope, "dedupe-ambiguous")
	if _, err := env.store.Create(ctx, record, nil); err != nil {
		t.Fatal(err)
	}
	store := &recoveryAdmissionStore{SQLiteDurableRunStore: env.store, conflictRunID: "ambiguous", conflictErr: ErrFreewillActorAdmissionAmbiguous}
	runtime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, store, env.claimer, env.events)
	control, err := NewFreewillOperatorControl(env.store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewFreewillWakeService(runtime, store, &sleepingFreewillSelector{}, FreewillWakeServiceConfig{
		Enabled: true, SchedulingEnabled: true, AgentID: "agent", Scope: scope,
		Objective: "Choose a safe read-only next action.", Owner: "recovery-test", LeaseFor: time.Minute,
		OperatorControl: control,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverPending(ctx, time.Now().Add(time.Minute), 10); !errors.Is(err, ErrFreewillActorAdmissionAmbiguous) {
		t.Fatalf("ambiguous recovery result = %v", err)
	}
}

func TestFreewillMalformedLegacyWakeCannotReserveAdmission(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	malformed := freewillAdmissionRecord("malformed", "agent", scope, freewill.StateWaking)
	malformed.Freewill.ExecutionDisabled = false
	if _, err := store.Create(ctx, malformed, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "malformed", "agent", scope); !errors.Is(err, ErrFreewillActorAdmissionAmbiguous) {
		t.Fatalf("malformed target admission = %v", err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM multiagent_freewill_admissions WHERE actor_key = ?", freewillActorAdmissionKey("agent")).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("malformed target created %d admission rows", count)
	}
	if _, err := store.Create(ctx, freewillAdmissionRecord("valid", "agent", scope, freewill.StateWaking), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, "valid", "agent", scope); err != nil {
		t.Fatalf("valid wake could not reserve slot after malformed target: %v", err)
	}
}
