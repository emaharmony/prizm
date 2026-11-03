package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/ollama"
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

type categoryRecoverySelector struct {
	calls  int
	inputs []FreewillSelectionInput
}

func (s *categoryRecoverySelector) SelectFreewill(_ context.Context, input FreewillSelectionInput) (freewill.Selection, FreewillSelectionEvidence, error) {
	s.calls++
	s.inputs = append(s.inputs, input)
	if s.calls == 1 {
		return freewill.Selection{}, FreewillSelectionEvidence{}, &SchemaCorrectionError{
			Role: RolePlanner, Code: "invalid_scored_ideas_score", Cause: errors.New("synthetic raw response sentinel"),
		}
	}
	return freewill.Selection{SleepReason: "No safe set is available."}, FreewillSelectionEvidence{}, nil
}

func TestFreewillCategoryFeedbackPersistsAcrossDurableRetry(t *testing.T) {
	ctx := context.Background()
	env := newDurableTestEnvironment(t)
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	runtime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, env.store, env.claimer, env.events,
		DurableRuntimeOptions{Clock: clock})
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	control, err := NewFreewillOperatorControl(env.store, clock)
	if err != nil {
		t.Fatal(err)
	}
	selector := &categoryRecoverySelector{}
	service, err := NewFreewillWakeService(runtime, env.store, selector, FreewillWakeServiceConfig{
		Enabled: true, SchedulingEnabled: true, AgentID: "agent", Scope: scope,
		Objective: "Choose a safe read-only next action.", Owner: "category-retry-test", LeaseFor: time.Minute,
		OperatorControl: control,
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	wake := FreewillWakeRequest{
		RunID: "freewill-category-retry", AgentID: "agent", Scope: scope, Trigger: "scheduled_tick",
		DedupeKey: "dedupe-category-retry", CorrelationID: "corr-category-retry",
	}
	accepted, _, err := runtime.AcceptFreewillWake(ctx, wake)
	if err != nil {
		t.Fatalf("accept test wake: %v", err)
	}
	failed, err := service.ProcessWake(ctx, accepted.State.RunID)
	if err == nil {
		t.Fatal("first categorized schema failure unexpectedly succeeded")
	}
	wantCode := "invalid_scored_ideas_score"
	wantFeedback := freewillSelectionRecoveryFeedback(wantCode)
	if failed.Freewill.SelectionFailureCode != wantCode || failed.Freewill.SelectionFeedback != wantFeedback ||
		failed.Freewill.SelectionRecoveryNumber != 1 || !failed.Freewill.ExecutionDisabled {
		t.Fatalf("durable retry state lost categorized correction or safety: %#v", failed.Freewill)
	}
	if strings.Contains(failed.Freewill.SelectionFeedback, "synthetic raw response sentinel") || strings.Contains(err.Error(), "synthetic raw response sentinel") {
		t.Fatalf("raw response sentinel escaped through failure or feedback")
	}
	events, err := env.events.Query(ctx, event.EventFilter{RunID: wake.RunID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range events {
		if strings.Contains(fmt.Sprint(current.Payload), "synthetic raw response sentinel") {
			t.Fatalf("raw response sentinel was persisted in event %s", current.Type)
		}
		if current.Type == event.EventFreewillApprovalProposed || current.Type == event.EventFreewillApprovalDecided ||
			current.Type == event.EventFreewillPolicyDenied {
			t.Fatalf("selection retry crossed a proposal or policy boundary: %s", current.Type)
		}
	}
	now = failed.Freewill.SelectionNextAt.Add(time.Second)
	succeeded, err := service.ProcessWake(ctx, accepted.State.RunID)
	if err != nil {
		t.Fatalf("process retry: %v", err)
	}
	if len(selector.inputs) != 2 || selector.inputs[1].RecoveryFeedback != wantFeedback {
		t.Fatalf("next wake did not receive persisted category feedback: %#v", selector.inputs)
	}
	if succeeded.Freewill.Lifecycle != freewill.StateSleeping || !succeeded.Freewill.ExecutionDisabled ||
		succeeded.Freewill.SelectionRecoveryNumber != 1 || succeeded.Freewill.SelectionFailureCode != "" {
		t.Fatalf("successful retry crossed safety or lost recovery state: %#v", succeeded.Freewill)
	}
}

func TestFreewillChoiceReasonPersistsWithDecisionOnlyReplay(t *testing.T) {
	ctx := context.Background()
	env := newDurableTestEnvironment(t)
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	runtime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, env.store, env.claimer, env.events, DurableRuntimeOptions{Clock: clock})
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	control, err := NewFreewillOperatorControl(env.store, clock)
	if err != nil {
		t.Fatal(err)
	}
	scored := "IDEA: Map dependencies\nSCORE: 72\nFACTORS: scope_fit\nRATIONALE: This maps direct dependencies and identifies risky coupling for review.\n" +
		"IDEA: Document service boundaries\nSCORE: 88\nFACTORS: impact\nRATIONALE: Clear ownership notes help contributors route future work.\n" +
		"IDEA: Review test coverage gaps\nSCORE: 61\nFACTORS: feasibility\nRATIONALE: A focused gap list guides later tests while keeping review scoped."
	chat := &scoredFreewillChatStub{responses: []provider.ChatGenerateResponse{
		{Content: scored, Provider: "ollama", Model: "glm-5.3-flash:cloud", PromptTokens: 11, OutputTokens: 7},
		{Content: "choice: candidate-freewill-choice-reason-1\nreason: Write a README to explain the service boundaries.", Provider: "ollama", Model: "glm-5.3-flash:cloud", PromptTokens: 5, OutputTokens: 3},
	}}
	selector := ChatFreewillSelector{Provider: chat, AgentID: "agent", Model: "glm-5.3-flash:cloud"}
	service, err := NewFreewillWakeService(runtime, env.store, selector, FreewillWakeServiceConfig{Enabled: true, SchedulingEnabled: true, AgentID: "agent", Scope: scope, Objective: "Choose a useful read-only follow-up.", Owner: "choice-reason-test", LeaseFor: time.Minute, OperatorControl: control}, clock)
	if err != nil {
		t.Fatal(err)
	}
	wake := FreewillWakeRequest{RunID: "freewill-choice-reason", AgentID: "agent", Scope: scope, Trigger: "scheduled_tick", DedupeKey: "dedupe-choice-reason", CorrelationID: "corr-choice-reason"}
	accepted, _, err := runtime.AcceptFreewillWake(ctx, wake)
	if err != nil {
		t.Fatalf("accept wake: %v", err)
	}
	result, err := service.ProcessWake(ctx, accepted.State.RunID)
	if err != nil {
		t.Fatalf("process wake: %v", err)
	}
	if result.Freewill.ChoiceID != "candidate-freewill-choice-reason-1" || result.Freewill.ReasonSummary != "Write a README to explain the service boundaries." {
		t.Fatalf("model choice/reason not retained: %#v", result.Freewill)
	}
	if !result.Freewill.ExecutionDisabled || result.Freewill.SelectionUsage.PromptTokens != 16 || result.Freewill.SelectionUsage.CompletionTokens != 10 || result.Freewill.SelectionUsage.TotalTokens != 26 {
		t.Fatalf("safety or combined phase usage lost: %#v", result.Freewill)
	}
	replay, err := env.store.Load(ctx, accepted.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Freewill.ChoiceID != result.Freewill.ChoiceID || replay.Freewill.ReasonSummary != result.Freewill.ReasonSummary || !replay.Freewill.ExecutionDisabled {
		t.Fatalf("durable reload changed decision evidence: %#v", replay.Freewill)
	}
	events, err := env.events.Query(ctx, event.EventFilter{RunID: wake.RunID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == event.EventMultiAgentToolDiagnostic || ev.Type == event.V1EventTypes.ToolCalled || ev.Type == event.V3EventTypes.ToolRequested || ev.Type == event.EventFreewillApprovalDecided || ev.Type == event.EventFreewillApprovalReconciled {
			t.Fatalf("choice phase crossed decision-only boundary: %s", ev.Type)
		}
	}
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

type liveFreewillPhaseTwoChat struct {
	phaseTwo                               provider.ChatProvider
	ideaResponse                           provider.ChatGenerateResponse
	ideaCalls, choiceCalls                 int
	choiceRequest                          *provider.ChatGenerateRequest
	choicePromptTokens, choiceOutputTokens int
}

func (s *liveFreewillPhaseTwoChat) ChatGenerate(ctx context.Context, req provider.ChatGenerateRequest) (provider.ChatGenerateResponse, error) {
	if len(req.Messages) == 0 {
		return provider.ChatGenerateResponse{}, errors.New("unexpected empty Freewill request")
	}
	switch req.Messages[0].Content {
	case freewillIdeaSystemPrompt:
		s.ideaCalls++
		if s.ideaCalls != 1 || len(req.Tools) != 0 {
			return provider.ChatGenerateResponse{}, errors.New("unexpected phase-one request")
		}
		return s.ideaResponse, nil
	case freewillChoiceSystemPrompt:
		s.choiceCalls++
		if s.choiceCalls != 1 || len(req.Tools) != 0 {
			return provider.ChatGenerateResponse{}, errors.New("unexpected phase-two request")
		}
		copyReq := req
		copyReq.Messages = append([]provider.ChatMessage(nil), req.Messages...)
		s.choiceRequest = &copyReq
		response, err := s.phaseTwo.ChatGenerate(ctx, req)
		if err != nil {
			return provider.ChatGenerateResponse{}, err
		}
		s.choicePromptTokens, s.choiceOutputTokens = response.PromptTokens, response.OutputTokens
		return response, nil
	default:
		return provider.ChatGenerateResponse{}, errors.New("unexpected Freewill prompt")
	}
}

// Phase one is scripted so this opt-in test exercises one live phase-two call
// against synthetic candidates and a temporary SQLite database only.
func TestFreewillChoiceReasonPersistsWithLiveGLMPhaseTwo(t *testing.T) {
	if os.Getenv("PRIZM_FREEWILL_LIVE_CONFORMANCE") != "1" {
		t.Skip("set PRIZM_FREEWILL_LIVE_CONFORMANCE=1 to run the bounded provider conformance call")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	const model = "glm-5.3-flash:cloud"
	const objective = "Choose the most useful small read-only documentation follow-up."
	const runID = "freewill-live-phase2-conformance"
	scored := "IDEA: Map dependency ownership\nSCORE: 72\nFACTORS: scope_fit\nRATIONALE: This maps dependencies and identifies risky coupling for future review.\n" +
		"IDEA: Document service boundaries\nSCORE: 88\nFACTORS: impact\nRATIONALE: Clear ownership notes help contributors route future work.\n" +
		"IDEA: Review coverage gaps\nSCORE: 61\nFACTORS: feasibility\nRATIONALE: A focused gap list guides later tests while keeping review scoped."
	chat := &liveFreewillPhaseTwoChat{phaseTwo: ollama.NewChatProvider("http://127.0.0.1:11434"),
		ideaResponse: provider.ChatGenerateResponse{Content: scored, Provider: "ollama", Model: model, PromptTokens: 11, OutputTokens: 7}}
	env := newDurableTestEnvironment(t)
	clock := func() time.Time { return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC) }
	runtime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, env.store, env.claimer, env.events, DurableRuntimeOptions{Clock: clock})
	scope := freewill.Scope{ProjectID: "synthetic-project", Workspace: "synthetic-workspace"}
	control, err := NewFreewillOperatorControl(env.store, clock)
	if err != nil {
		t.Fatal("create isolated operator control")
	}
	service, err := NewFreewillWakeService(runtime, env.store, ChatFreewillSelector{Provider: chat, AgentID: "agent", Model: model},
		FreewillWakeServiceConfig{Enabled: true, SchedulingEnabled: true, AgentID: "agent", Scope: scope, Objective: objective,
			Owner: "live-phase-two-conformance", LeaseFor: time.Minute, OperatorControl: control}, clock)
	if err != nil {
		t.Fatal("create isolated wake service")
	}
	wake := FreewillWakeRequest{RunID: runID, AgentID: "agent", Scope: scope, Trigger: "scheduled_tick",
		DedupeKey: "dedupe-live-phase2-conformance", CorrelationID: "corr-live-phase2-conformance"}
	accepted, _, err := runtime.AcceptFreewillWake(ctx, wake)
	if err != nil {
		t.Fatal("accept synthetic wake")
	}
	result, err := service.ProcessWake(ctx, accepted.State.RunID)
	if err != nil {
		t.Fatalf("process synthetic wake failed (%T)", err)
	}
	if chat.ideaCalls != 1 || chat.choiceCalls != 1 || chat.choiceRequest == nil {
		t.Fatalf("expected one scripted phase one and one live phase two; got %d and %d", chat.ideaCalls, chat.choiceCalls)
	}
	req := chat.choiceRequest
	if req.Model != model || req.MaxTokens != freewillChoiceMaxOutputTokens || len(req.Tools) != 0 || len(req.Messages) != 2 {
		t.Fatal("phase-two request violated model, output-cap, or no-tools bounds")
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(req.Messages[1].Content), &input); err != nil {
		t.Fatal("phase-two request was not bounded JSON context")
	}
	if input["objective"] != objective {
		t.Fatal("phase-two request omitted the objective")
	}
	candidates, ok := input["candidates"].([]any)
	if !ok || len(candidates) != 3 {
		t.Fatal("phase-two request omitted the three scored candidates")
	}
	titles, scores := []string{"Map dependency ownership", "Document service boundaries", "Review coverage gaps"}, []float64{72, 88, 61}
	allowed := make(map[string]struct{}, len(titles))
	allowedIDs := make([]string, 0, len(titles))
	for i, raw := range candidates {
		candidate, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("phase-two candidate had an unexpected shape")
		}
		id, idOK := candidate["id"].(string)
		title, titleOK := candidate["title"].(string)
		score, scoreOK := candidate["score"].(float64)
		if !idOK || !titleOK || !scoreOK || id != fmt.Sprintf("candidate-%s-%d", runID, i+1) || title != titles[i] || score != scores[i] {
			t.Fatal("phase-two request changed a synthetic candidate or advisory score")
		}
		allowed[id] = struct{}{}
		allowedIDs = append(allowedIDs, id)
	}
	if chat.choicePromptTokens <= 0 || chat.choiceOutputTokens <= 0 {
		t.Fatal("provider returned no positive phase-two token metadata")
	}
	if _, ok := allowed[result.Freewill.ChoiceID]; !ok {
		t.Fatal("provider choice was outside the host allowlist")
	}
	reason := strings.TrimSpace(result.Freewill.ReasonSummary)
	if len(reason) == 0 || len(reason) > 500 || !validFreewillChoiceReason(reason, result.Freewill.ChoiceID, allowedIDs) {
		t.Fatal("persisted reason was empty, unbounded, or failed host validation")
	}
	if !result.Freewill.ExecutionDisabled || result.Freewill.Lifecycle != freewill.StateProposed {
		t.Fatal("selection crossed the execution-disabled proposal boundary")
	}
	usage := result.Freewill.SelectionUsage
	if usage.PromptTokens != 11+chat.choicePromptTokens || usage.CompletionTokens != 7+chat.choiceOutputTokens ||
		usage.TotalTokens != 18+chat.choicePromptTokens+chat.choiceOutputTokens {
		t.Fatal("durable token metadata did not include both phases")
	}
	replay, err := env.store.Load(ctx, accepted.State.RunID)
	if err != nil {
		t.Fatal("reload synthetic durable run")
	}
	if replay.Freewill.ChoiceID != result.Freewill.ChoiceID || replay.Freewill.ReasonSummary != reason || !replay.Freewill.ExecutionDisabled {
		t.Fatal("durable replay changed the decision-only evidence")
	}
	events, err := env.events.Query(ctx, event.EventFilter{RunID: wake.RunID, Limit: 100})
	if err != nil {
		t.Fatal("query synthetic event metadata")
	}
	for _, ev := range events {
		switch ev.Type {
		case event.EventFreewillApprovalProposed, event.EventFreewillApprovalDecided, event.EventFreewillApprovalReconciled,
			event.EventProposalApprovalPending, event.EventProposalApprovalGranted, event.EventProposalApprovalDenied,
			event.EventInteractionApprovalPaused, event.EventProposalApplyStarted, event.EventProposalApplied,
			event.EventProposalApplyFailed, event.EventProposalReconciled, event.EventMultiAgentFanoutChildDispatched,
			event.EventMultiAgentToolDiagnostic, event.V1EventTypes.ToolCalled, event.V1EventTypes.ToolResult,
			event.V1EventTypes.ToolFailed, event.V3EventTypes.ToolRequested, event.EventInteractionActionValidated,
			event.EventInteractionActionRejected, event.EventInteractionActionExecuted, event.EventInteractionActionFailed:
			t.Fatalf("synthetic conformance emitted forbidden event: %s", ev.Type)
		}
	}
}
