package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
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

type resumeLineageSelector struct {
	inputs []FreewillSelectionInput
}

func (s *resumeLineageSelector) SelectFreewill(_ context.Context, input FreewillSelectionInput) (freewill.Selection, FreewillSelectionEvidence, error) {
	s.inputs = append(s.inputs, input)
	if len(input.ResumeCandidates) != 1 {
		return freewill.Selection{}, FreewillSelectionEvidence{}, fmt.Errorf("resume candidates = %d, want 1", len(input.ResumeCandidates))
	}
	resume := input.ResumeCandidates[0]
	resume.Request.Goal = "Model attempted to replace the host-verified resume request."
	candidates := make([]freewill.CandidateIntention, 0, 4)
	for i := 0; i < 3; i++ {
		request := resume.Request
		request.RequestID = fmt.Sprintf("fresh-request-%d", i)
		request.CandidateSource = freewill.SourceNew
		request.Title = fmt.Sprintf("Fresh task %d", i)
		request.Goal = fmt.Sprintf("Review bounded topic %d", i)
		request.ReasonSummary = "A separate synthetic read-only task."
		request.IdempotencyKey = fmt.Sprintf("fresh-idempotency-%d", i)
		request.CorrelationID = input.CorrelationID
		candidates = append(candidates, freewill.CandidateIntention{CandidateID: fmt.Sprintf("fresh-candidate-%d", i), Request: request,
			Score: 50, ScoreRationale: "A bounded, scoped and reversible research task."})
	}
	candidates = append(candidates, resume)
	return freewill.Selection{Candidates: candidates, ChoiceID: resume.CandidateID,
		ReasonSummary: "Continue the previously approved intention as a fresh proposal."}, FreewillSelectionEvidence{}, nil
}

func TestFreewillResumeLineagePersistsAcrossSQLiteReopenAndLaterTick(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "freewill-resume-lineage.db")
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		_ = events.Close()
		t.Fatal(err)
	}
	closeStores := func() {
		if err := store.Close(); err != nil {
			t.Errorf("close durable store: %v", err)
		}
		if err := events.Close(); err != nil {
			t.Errorf("close event store: %v", err)
		}
	}
	defer closeStores()

	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	request := freewill.IntentionRequest{RequestID: "resume-request", AgentID: "agent", Origin: freewill.OriginAgent,
		CandidateSource: freewill.SourceNew, Title: "Approved task", Goal: "Continue bounded research across sessions",
		ReasonSummary: "The owner approved this scoped research objective.", RequestedCapabilities: []string{"read_only"},
		Scope: scope, IdempotencyKey: "resume-idem", CorrelationID: "resume-correlation"}
	candidate := freewill.CandidateIntention{CandidateID: "resume-candidate", Request: request, Score: 82,
		ScoreRationale: "Matches the scoped research goal and has low execution risk."}
	alternativeRequest := request
	alternativeRequest.RequestID = "unselected-request"
	alternativeRequest.Title = "Unselected alternative"
	alternativeRequest.Goal = "Review another bounded topic"
	alternativeRequest.IdempotencyKey = "unselected-idem"
	alternative := freewill.CandidateIntention{CandidateID: "unselected-candidate", Request: alternativeRequest, Score: 90,
		ScoreRationale: "A scored alternative that has no owner approval."}
	fingerprint, _ := freewillRequestFingerprint(request)
	prior := freewillAdmissionRecord("freewill-prior-cycle", "agent", scope, freewill.StatePaused)
	prior.Freewill.PauseReason = "execution_disabled"
	prior.Freewill.ChoiceID = candidate.CandidateID
	prior.Freewill.Candidates = []freewill.CandidateIntention{alternative, candidate}
	prior.Freewill.ApprovalRef = "prior-approval"
	prior.Freewill.ApprovalFingerprint = fingerprint
	prior.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: prior.Freewill.ApprovalRef,
		RequestFingerprint: fingerprint, Decision: FreewillApprovalGranted, ControlGeneration: 0}
	if _, err := store.Create(ctx, prior, nil); err != nil {
		t.Fatalf("persist granted predecessor: %v", err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, prior.State.RunID, "agent", scope); err != nil {
		t.Fatalf("persist predecessor actor slot: %v", err)
	}
	closeStores()

	events, err = event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	runtime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, store, newMemoryRunClaimer(), events,
		DurableRuntimeOptions{Clock: func() time.Time { return now }})
	control, err := NewFreewillOperatorControl(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	selector := &resumeLineageSelector{}
	service, err := NewFreewillWakeService(runtime, store, selector, FreewillWakeServiceConfig{
		Enabled: true, SchedulingEnabled: true, AgentID: "agent", Scope: scope,
		Objective: "Choose a safe bounded task.", Owner: "resume-lineage-test", LeaseFor: time.Minute,
		OperatorControl: control,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := service.HandleScheduledTick(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("process later scheduled tick: %v", err)
	}
	if len(selector.inputs) != 1 || len(selector.inputs[0].ResumeCandidates) != 1 ||
		selector.inputs[0].ResumeCandidates[0].CandidateID != candidate.CandidateID {
		t.Fatalf("selector did not receive only the verified predecessor choice: %#v", selector.inputs)
	}
	if cycle.Freewill.ResumeFromRunID != prior.State.RunID || cycle.Freewill.ApprovalRef != "" ||
		cycle.Freewill.ApprovalReceipt != nil || cycle.Freewill.ApprovalGeneration != 0 || cycle.Freewill.ExecutionDispatch != nil {
		t.Fatalf("new cycle lineage or authority = %#v", cycle.Freewill)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := events.Close(); err != nil {
		t.Fatal(err)
	}
	events, err = event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Load(ctx, cycle.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Freewill.ResumeFromRunID != prior.State.RunID || persisted.Freewill.ApprovalRef != "" ||
		persisted.Freewill.ApprovalReceipt != nil || persisted.Freewill.ExecutionDispatch != nil {
		t.Fatalf("reopened new cycle lost lineage or gained predecessor authority: %#v", persisted.Freewill)
	}
	selected, ok := selectedFreewillCandidate(persisted.Freewill)
	if !ok || selected.Request.Goal != request.Goal || selected.Request.RequestID != request.RequestID {
		t.Fatalf("model-modified resume candidate replaced canonical predecessor request: %#v", selected)
	}
	statusReader, err := NewFreewillApprovalStatusReader(store, approval.NewStore(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	status, err := statusReader.Status(ctx, cycle.State.RunID)
	if err != nil || status.ResumeFromRunID != prior.State.RunID || status.ApprovalID != "" || status.ChildRunID != "" {
		t.Fatalf("reopened status lineage/authority = %#v err=%v", status, err)
	}
	committedEvents, err := events.Query(ctx, event.EventFilter{RunID: cycle.State.RunID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	foundLineage := false
	for _, current := range committedEvents {
		if current.Type == event.EventFreewillSelectionCommitted && current.Payload["resume_from_run_id"] == prior.State.RunID {
			foundLineage = true
		}
	}
	if !foundLineage {
		t.Fatal("selection-committed outbox event omitted host-authored resume lineage")
	}
}

func TestFreewillPausedGrantedExecutionAdmissionFollowsDispatchTerminality(t *testing.T) {
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	request := freewill.IntentionRequest{RequestID: "request", AgentID: "agent", Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: "title", Goal: "goal", ReasonSummary: "reason", RequestedCapabilities: []string{"code"}, Scope: scope, IdempotencyKey: "idem", CorrelationID: "corr"}
	candidate := freewill.CandidateIntention{CandidateID: "candidate", Request: request}
	fingerprint, _ := freewillRequestFingerprint(request)
	base := freewillAdmissionRecord("paused", "agent", scope, freewill.StatePaused)
	base.Freewill.ChoiceID = candidate.CandidateID
	base.Freewill.Candidates = []freewill.CandidateIntention{candidate}
	base.Freewill.ApprovalRef = "approval"
	base.Freewill.ApprovalFingerprint = fingerprint
	base.Freewill.ApprovalGeneration = 1
	base.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: "approval", RequestFingerprint: fingerprint,
		Decision: FreewillApprovalGranted, ControlGeneration: 1}

	for _, status := range []string{"dispatching", "accepted", "failed"} {
		t.Run(status, func(t *testing.T) {
			record := base
			freewillState := *base.Freewill
			record.Freewill = &freewillState
			record.Freewill.ExecutionDispatch = &FreewillExecutionDispatch{Status: status}
			if status == "accepted" {
				record.Freewill.ExecutionDispatch.ChildRunID = "run_aaaaaaaaaaaaaaaaaaaa"
				record.Freewill.ExecutionDispatch.ReportRef = "multiagent_report.json"
			}
			if status == "failed" {
				record.Freewill.ExecutionDispatch.FailureCode = "workflow_start_failed"
			}
			if !freewillAdmissionActive(record) {
				t.Fatalf("paused granted %s dispatch released actor admission before terminal projection", status)
			}
		})
	}
	for _, status := range []string{"completed", "execution_failed", "execution_cancelled"} {
		t.Run(status, func(t *testing.T) {
			record := base
			freewillState := *base.Freewill
			record.Freewill = &freewillState
			record.Freewill.ExecutionDispatch = &FreewillExecutionDispatch{Status: status}
			if freewillAdmissionActive(record) {
				t.Fatalf("paused granted %s dispatch kept actor admission after terminal projection", status)
			}
		})
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

func TestFreewillAcceptedChildKeepsSQLiteAdmissionUntilTerminalProjection(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	now := time.Now().UTC()
	request := freewill.IntentionRequest{RequestID: "approved-request", AgentID: "agent", Origin: freewill.OriginAgent,
		CandidateSource: freewill.SourceNew, Title: "approved task", Goal: "continue accepted child", ReasonSummary: "bounded recovery fixture",
		RequestedCapabilities: []string{"code"}, Scope: scope, IdempotencyKey: "approved-idem", CorrelationID: "approved-corr"}
	candidate := freewill.CandidateIntention{CandidateID: "approved-candidate", Request: request}
	fingerprint, _ := freewillRequestFingerprint(request)
	parent := freewillAdmissionRecord("approved-parent", "agent", scope, freewill.StatePaused)
	parent.Freewill.Candidates = []freewill.CandidateIntention{candidate}
	parent.Freewill.ChoiceID = candidate.CandidateID
	parent.Freewill.ApprovalRef = "approved-grant"
	parent.Freewill.ApprovalFingerprint = fingerprint
	parent.Freewill.ApprovalGeneration = 1
	parent.Freewill.ApprovalReceipt = &FreewillApprovalReceipt{ApprovalID: "approved-grant", RequestFingerprint: fingerprint,
		Decision: FreewillApprovalGranted, ControlGeneration: 1}
	parent.Freewill.ExecutionDispatch = &FreewillExecutionDispatch{Status: "accepted", ChildRunID: "run_aaaaaaaaaaaaaaaaaaaa",
		ReportRef: "multiagent_report.json", UpdatedAt: now}
	if err := parent.Validate(nil); err != nil {
		t.Fatalf("accepted parent fixture is invalid: %v", err)
	}
	if _, err := store.Create(ctx, parent, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFreewillActorAdmission(ctx, parent.State.RunID, "agent", scope); err != nil {
		t.Fatal(err)
	}

	wake, wakeRecord := freewillWakeAcceptanceFixture("next-wake", "agent", scope, "next-wake-dedupe")
	if _, _, err := store.AcceptFreewillWake(ctx, wakeRecord, nil, wake); !errors.Is(err, ErrFreewillActorAdmissionConflict) {
		t.Fatalf("accepted child did not block next scheduled wake: %v", err)
	}
	if _, err := store.Load(ctx, wake.RunID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("blocked wake left a run behind: %v", err)
	}
	var keys, outbox int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM multiagent_acceptance_keys WHERE run_id = ?", wake.RunID).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM multiagent_outbox WHERE run_id = ?", wake.RunID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if keys != 0 || outbox != 0 {
		t.Fatalf("blocked wake left key/outbox rows: %d/%d", keys, outbox)
	}

	terminal, err := store.Load(ctx, parent.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	terminal.Freewill.ExecutionDispatch = &FreewillExecutionDispatch{Status: "completed", ChildRunID: parent.Freewill.ExecutionDispatch.ChildRunID,
		ReportRef: "multiagent_report.json", ChildStatus: string(RunStatusCompleted), ValidationSummary: "passed",
		ReportSHA256: strings.Repeat("a", 64), UpdatedAt: now}
	terminal.State.UpdatedAt = now.Add(time.Second)
	if err := terminal.Validate(nil); err != nil {
		t.Fatalf("terminal parent fixture is invalid: %v", err)
	}
	if _, err := store.Checkpoint(ctx, terminal.Revision, terminal, nil); err != nil {
		t.Fatalf("persist terminal child projection: %v", err)
	}
	accepted, created, err := store.AcceptFreewillWake(ctx, wakeRecord, nil, wake)
	if err != nil || !created || accepted.State.RunID != wake.RunID {
		t.Fatalf("wake after terminal projection = run %q created=%t err=%v", accepted.State.RunID, created, err)
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

func TestFreewillWakePersistsHostConfiguredStrategyBeforeSelection(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteDurableStoreForTest(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	wake, record := freewillWakeAcceptanceFixture("strategy-wake", "agent", scope, "dedupe-strategy-wake")
	wake.StrategyID, wake.StrategyVersion, wake.ExpectedPatchSHA256 = "baseline", "v1", strings.Repeat("a", 64)
	record.Freewill.StrategyID, record.Freewill.StrategyVersion, record.Freewill.ExpectedPatchSHA256 = wake.StrategyID, wake.StrategyVersion, wake.ExpectedPatchSHA256
	accepted, created, err := store.AcceptFreewillWake(ctx, record, nil, wake)
	if err != nil || !created || accepted.Freewill.StrategyID != "baseline" || accepted.Freewill.StrategyVersion != "v1" || accepted.Freewill.ExpectedPatchSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("strategy binding was not persisted at admission: created=%v run=%#v err=%v", created, accepted.Freewill, err)
	}
	reopened, err := store.Load(ctx, wake.RunID)
	if err != nil || reopened.Freewill.StrategyID != "baseline" || reopened.Freewill.StrategyVersion != "v1" || reopened.Freewill.ExpectedPatchSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("strategy binding did not survive reopen: state=%#v err=%v", reopened.Freewill, err)
	}
	conflict := wake
	conflict.StrategyID, conflict.StrategyVersion, conflict.ExpectedPatchSHA256 = "alternative", "v2", strings.Repeat("b", 64)
	if _, _, err := store.AcceptFreewillWake(ctx, record, nil, conflict); !errors.Is(err, ErrRequestIdentityConflict) {
		t.Fatalf("changed strategy binding on replay was not rejected: %v", err)
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
			DedupeKey: dedupe, CorrelationID: runID, StrategyID: wake.StrategyID, StrategyVersion: wake.StrategyVersion, ExecutionDisabled: true},
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
	scored := `{"candidates":[{"idea":"Map dependencies","score":72,"factors":["scope_fit"],"rationale":"This maps direct dependencies and identifies risky coupling that merits focused future review."},{"idea":"Document service boundaries","score":88,"factors":["impact"],"rationale":"Clear ownership notes help contributors route future work and avoid accidental cross-domain changes."},{"idea":"Review test coverage gaps","score":61,"factors":["feasibility"],"rationale":"A focused gap list can guide later tests while keeping the review read only and within scope."}],"sleep":false}`
	chat := &scoredFreewillChatStub{responses: []provider.ChatGenerateResponse{
		{Content: scored, Provider: "ollama", Model: "glm-5.3-flash:cloud", PromptTokens: 11, OutputTokens: 7},
		{Content: `{"choice_id":"candidate-freewill-choice-reason-1","reason":"Write a README to explain the service boundaries."}`, Provider: "ollama", Model: "glm-5.3-flash:cloud", PromptTokens: 5, OutputTokens: 3},
	}}
	selector := ChatFreewillSelector{Provider: chat, AgentID: "agent", Model: "glm-5.3-flash:cloud", PreferenceInfluenceEnabled: true,
		PreferenceInfluenceOwnerID: "owner", PreferenceInfluenceSource: fixedPreferenceInfluenceSource{ok: true, advisory: FreewillPreferenceInfluence{
			HypothesisID: "hypothesis-id", HypothesisRevision: "hypothesis-revision", Hypothesis: "A weak synthetic hypothesis.", Uncertainty: "Only two synthetic pairs.",
			SourcePairRefs: []string{"outcome-a@rev-a;report-a@rev-a", "outcome-b@rev-b;report-b@rev-b"}, Strength: "low", Verification: "unverified"}}}
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
	if !result.Freewill.PreferenceSelection.Considered || !result.Freewill.PreferenceSelection.Eligible || result.Freewill.PreferenceSelection.HypothesisID != "hypothesis-id" || len(result.Freewill.PreferenceSelection.SourcePairRefs) != 2 {
		t.Fatalf("preference selection provenance was not committed: %#v", result.Freewill.PreferenceSelection)
	}
	replay, err := env.store.Load(ctx, accepted.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Freewill.ChoiceID != result.Freewill.ChoiceID || replay.Freewill.ReasonSummary != result.Freewill.ReasonSummary || !replay.Freewill.ExecutionDisabled || !reflect.DeepEqual(replay.Freewill.PreferenceSelection, result.Freewill.PreferenceSelection) {
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

type sleepingFreewillSelector struct {
	calls  int
	runIDs []string
}

func (s *sleepingFreewillSelector) SelectFreewill(_ context.Context, input FreewillSelectionInput) (freewill.Selection, FreewillSelectionEvidence, error) {
	s.calls++
	s.runIDs = append(s.runIDs, input.RunID)
	return freewill.Selection{SleepReason: "wait"}, FreewillSelectionEvidence{}, nil
}

func newFreewillRecoveryServiceForTest(t *testing.T, env durableTestEnvironment, selector FreewillSelectionProvider) *FreewillWakeService {
	t.Helper()
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	runtime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, env.store, env.claimer, env.events)
	control, err := NewFreewillOperatorControl(env.store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewFreewillWakeService(runtime, env.store, selector, FreewillWakeServiceConfig{
		Enabled: true, SchedulingEnabled: true, AgentID: "agent", Scope: scope,
		Objective: "Choose a safe read-only next action.", Owner: "recovery-test", LeaseFor: time.Minute,
		OperatorControl: control,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type roadmapBypassSelector struct{}

func (roadmapBypassSelector) SelectFreewill(_ context.Context, input FreewillSelectionInput) (freewill.Selection, FreewillSelectionEvidence, error) {
	makeCandidate := func(id, title, goal string, score int, actionID, grounding string) freewill.CandidateIntention {
		request := freewill.IntentionRequest{RequestID: "request-" + id, AgentID: input.AgentID, Origin: freewill.OriginAgent,
			CandidateSource: freewill.SourceNew, Title: title, Goal: goal, ReasonSummary: "Synthetic selector output.",
			RequestedCapabilities: []string{"decision_only"}, Scope: input.Scope, IdempotencyKey: "idem-" + id, CorrelationID: input.CorrelationID}
		return freewill.CandidateIntention{CandidateID: id, Request: request, Score: score, ScoreFactors: []string{"scope_fit"},
			ScoreRationale: "The model score is advisory.", BasisRefs: []string{"project:readme"}, Uncertainty: "The source may be incomplete.",
			ProjectActionID: actionID, ProjectGrounding: grounding}
	}
	candidates := []freewill.CandidateIntention{
		makeCandidate("guitar", "Recommend guitar strings", "Compare bass-guitar string gauges", 99, "", "roadmap_action"),
		makeCandidate("forged-action", "Recommend guitar strings", "Ignore the roadmap", 91, "project:roadmap:M2", "exploration"),
		makeCandidate("explore", "Investigate profile flow", "Explore implementation options", 61, "", ""),
	}
	return freewill.Selection{Candidates: candidates, ChoiceID: candidates[0].CandidateID, ReasonSummary: "Selector chose the highest-scored idea."}, FreewillSelectionEvidence{}, nil
}

func TestFreewillWakeServiceRebindsInjectedSelectorRoadmapActions(t *testing.T) {
	ctx := context.Background()
	env := newDurableTestEnvironment(t)
	service := newFreewillRecoveryServiceForTest(t, env, roadmapBypassSelector{})
	snapshot := freewill.NewProjectContextSnapshot("p", service.config.Objective, time.Now().UTC())
	snapshot.Sources = append(snapshot.Sources, freewill.ProjectContextSource{ID: "project:readme", Path: "README.md",
		ObservedAt: snapshot.CapturedAt, Confidence: "direct_file_observation",
		Content: "| M2 | Profiles + Auth — user provisioning, role-based profiles | Next |"})
	snapshot.RoadmapActions = []freewill.ProjectRoadmapAction{{ID: "project:roadmap:M2",
		Text: "Profiles + Auth — user provisioning, role-based profiles", SourceID: "project:readme"}}
	snapshot.Seal()
	service.config.ProjectContext = func(context.Context) (freewill.ProjectContextSnapshot, error) { return snapshot, nil }
	result, err := service.HandleScheduledTick(ctx, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatalf("handle adversarial injected-selector tick: %v", err)
	}
	if result.Freewill == nil || result.Freewill.Lifecycle != freewill.StateSleeping || result.Freewill.ChoiceID != "" ||
		len(result.Freewill.Candidates) != 3 || result.Freewill.ApprovalRef != "" || !result.Freewill.ExecutionDisabled {
		t.Fatalf("injected selector's unsupported choice crossed host admissibility: %#v", result.Freewill)
	}
	if got := result.Freewill.Candidates[0]; got.ProjectGrounding != "exploration" || got.ProjectActionID != "" {
		t.Fatalf("host trusted selector grounding metadata: %#v", got)
	}
	if got := result.Freewill.Candidates[1]; got.ProjectGrounding != "roadmap_action" || got.ProjectActionID != "project:roadmap:M2" ||
		got.Request.Title != snapshot.RoadmapActions[0].Text || got.Request.Goal != snapshot.RoadmapActions[0].Text || strings.Contains(strings.ToLower(got.Request.Goal), "guitar") {
		t.Fatalf("host did not replace injected freeform action text: %#v", got)
	}
	events, err := env.events.Query(ctx, event.EventFilter{RunID: result.State.RunID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, evt := range events {
		if evt.Type == event.EventFreewillApprovalProposed || evt.Type == event.EventFreewillApprovalDecided ||
			evt.Type == event.EventMultiAgentFanoutChildDispatched || evt.Type == event.EventMultiAgentToolDiagnostic || strings.Contains(evt.Type, ".tool.") {
			t.Fatalf("injected selector's exploratory choice emitted effect event %q", evt.Type)
		}
	}
}

func TestFreewillWakeDefersWithoutRemoteProjectContextOptIn(t *testing.T) {
	ctx := context.Background()
	env := newDurableTestEnvironment(t)
	selector := &sleepingFreewillSelector{}
	service := newFreewillRecoveryServiceForTest(t, env, selector)
	snapshot := freewill.NewProjectContextSnapshot("p", service.config.Objective, time.Now().UTC())
	snapshot.Sources = append(snapshot.Sources, freewill.ProjectContextSource{ID: "project:readme", Path: "README.md", Content: "synthetic project facts",
		ObservedAt: snapshot.CapturedAt, Confidence: "direct_file_observation"})
	snapshot.SelectorEgressAllowed = false
	snapshot.Seal()
	service.config.ProjectContext = func(context.Context) (freewill.ProjectContextSnapshot, error) { return snapshot, nil }
	result, err := service.HandleScheduledTick(ctx, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatalf("handle remote-context-denied tick: %v", err)
	}
	if selector.calls != 0 || result.Freewill.Lifecycle != freewill.StateSleeping || !strings.Contains(result.Freewill.SleepReason, "sharing") {
		t.Fatalf("selector egress was not blocked: calls=%d run=%#v", selector.calls, result.Freewill)
	}
	manifest := result.Freewill.ProjectContextManifest
	if manifest == nil || manifest.Version != freewill.ProjectContextManifestVersion || manifest.ProjectID != "p" || manifest.SnapshotHash != snapshot.Digest ||
		!manifest.CapturedAt.Equal(snapshot.CapturedAt) || result.Freewill.ProjectContextEgressAllowed || len(manifest.Sources) != 3 {
		t.Fatalf("versioned local manifest was not persisted: %#v", result.Freewill)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("persisted manifest invalid: %v", err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || strings.Contains(string(encoded), "synthetic project facts") {
		t.Fatalf("durable manifest contains raw content: %s err=%v", encoded, err)
	}
	events, err := env.events.Query(ctx, event.EventFilter{RunID: result.State.RunID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var foundManifestEvent bool
	for _, evt := range events {
		if evt.Type != event.EventFreewillSelectionCommitted {
			continue
		}
		payload, err := json.Marshal(evt.Payload)
		if err != nil || strings.Contains(string(payload), "synthetic project facts") {
			t.Fatalf("selection event copied raw project text: %s err=%v", payload, err)
		}
		encodedManifest, err := json.Marshal(evt.Payload["project_context_manifest"])
		var eventManifest freewill.ProjectContextManifest
		if err != nil || json.Unmarshal(encodedManifest, &eventManifest) != nil || !reflect.DeepEqual(eventManifest, *manifest) {
			t.Fatalf("selection event manifest differs from run manifest: payload=%s run=%#v", encodedManifest, manifest)
		}
		foundManifestEvent = true
	}
	if !foundManifestEvent {
		t.Fatal("selection event did not persist the project context manifest")
	}
	oldManifest := manifest.Clone()
	for index := range snapshot.Sources {
		if snapshot.Sources[index].ID == "project:readme" {
			snapshot.Sources[index].Content = "changed safe project facts"
		}
	}
	snapshot.Seal()
	updated := snapshot.Manifest()
	if updated.SnapshotHash == oldManifest.SnapshotHash || projectContextManifestRevision(updated, "project:readme") == projectContextManifestRevision(oldManifest, "project:readme") {
		t.Fatalf("safe source change did not refresh manifest revisions: old=%#v updated=%#v", oldManifest, updated)
	}
	if err := env.store.Close(); err != nil {
		t.Fatalf("close durable store: %v", err)
	}
	reopened, err := NewSQLiteDurableRunStore(env.dbPath)
	if err != nil {
		t.Fatalf("reopen durable store: %v", err)
	}
	defer reopened.Close()
	reloaded, err := reopened.Load(ctx, result.State.RunID)
	if err != nil {
		t.Fatalf("load manifest after reopen: %v", err)
	}
	if !reflect.DeepEqual(reloaded.Freewill.ProjectContextManifest, &oldManifest) {
		t.Fatalf("source refresh rewrote earlier durable manifest: got=%#v want=%#v", reloaded.Freewill.ProjectContextManifest, oldManifest)
	}
}

func projectContextManifestRevision(manifest freewill.ProjectContextManifest, id string) string {
	for _, source := range manifest.Sources {
		if source.ID == id {
			return source.Revision
		}
	}
	return ""
}

func TestFreewillWakeSleepsWhenAuthorizedProjectContextIsMissing(t *testing.T) {
	ctx := context.Background()
	env := newDurableTestEnvironment(t)
	selector := &sleepingFreewillSelector{}
	service := newFreewillRecoveryServiceForTest(t, env, selector)
	snapshot := freewill.NewProjectContextSnapshot("p", service.config.Objective, time.Now().UTC())
	snapshot.Seal()
	service.config.ProjectContext = func(context.Context) (freewill.ProjectContextSnapshot, error) { return snapshot, nil }
	result, err := service.HandleScheduledTick(ctx, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatalf("handle context-missing tick: %v", err)
	}
	if selector.calls != 0 || result.Freewill.Lifecycle != freewill.StateSleeping || !strings.Contains(result.Freewill.SleepReason, "unavailable") {
		t.Fatalf("missing context did not defer safely: calls=%d run=%#v", selector.calls, result.Freewill)
	}
}

func queryFreewillRunEventsForTest(t *testing.T, env durableTestEnvironment, runID string) []event.Event {
	t.Helper()
	events, err := env.events.Query(context.Background(), event.EventFilter{RunID: runID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func assertFreewillNoActionEventsForTest(t *testing.T, events []event.Event) {
	t.Helper()
	for _, ev := range events {
		if ev.Type == event.EventMultiAgentToolDiagnostic || ev.Type == event.V1EventTypes.ToolCalled ||
			ev.Type == event.V3EventTypes.ToolRequested || ev.Type == event.EventFreewillApprovalDecided ||
			ev.Type == event.EventFreewillApprovalReconciled || ev.Type == event.EventProposalApplied ||
			ev.Type == event.EventInteractionActionExecuted {
			t.Fatalf("Freewill reconciliation crossed decision-only boundary: %s", ev.Type)
		}
	}
}

func TestFreewillRecoveryHaltsWakeWithoutActorAdmission(t *testing.T) {
	ctx := context.Background()
	env := newDurableTestEnvironment(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	_, orphan := freewillWakeAcceptanceFixture("orphan-missing", "agent", scope, "dedupe-orphan-missing")
	created, err := env.store.Create(ctx, orphan, nil)
	if err != nil {
		t.Fatal(err)
	}
	selector := &sleepingFreewillSelector{}
	service := newFreewillRecoveryServiceForTest(t, env, selector)
	if err := service.RecoverPending(ctx, time.Now().Add(time.Minute), 10); err != nil {
		t.Fatalf("recover orphan: %v", err)
	}
	reconciled, err := env.store.Load(ctx, orphan.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Freewill.Lifecycle != freewill.StateHalted ||
		reconciled.Freewill.SelectionFailureClass != freewillAdmissionReconciliationClass ||
		reconciled.Freewill.SelectionFailureCode != freewillAdmissionMissingCode ||
		reconciled.Freewill.PauseReason != freewillAdmissionReconciledReason ||
		!reconciled.Freewill.ExecutionDisabled || reconciled.Freewill.LeaseOwner != "" ||
		!reconciled.Freewill.LeaseUntil.IsZero() || !reconciled.Freewill.SelectionNextAt.IsZero() {
		t.Fatalf("orphan reconciliation state = %#v", reconciled.Freewill)
	}
	if reconciled.Revision != created.Revision+1 {
		t.Fatalf("reconciled revision = %d, want one atomic checkpoint after %d", reconciled.Revision, created.Revision)
	}
	if selector.calls != 0 {
		t.Fatalf("orphan provider calls = %d, want 0", selector.calls)
	}
	events := queryFreewillRunEventsForTest(t, env, orphan.State.RunID)
	halted := 0
	for _, ev := range events {
		if ev.Type == event.EventFreewillSelectionHalted {
			halted++
			if ev.Payload["failure_code"] != freewillAdmissionMissingCode ||
				ev.Payload["failure_class"] != freewillAdmissionReconciliationClass ||
				ev.Payload["reason"] != freewillAdmissionReconciledReason {
				t.Fatalf("reconciliation event metadata = %#v", ev.Payload)
			}
		}
		if ev.Type == event.EventFreewillWakeClaimed || ev.Type == event.EventFreewillSelectionStarted ||
			ev.Type == event.EventFreewillSelectionCommitted {
			t.Fatalf("orphan advanced past reconciliation: %s", ev.Type)
		}
	}
	if halted != 1 {
		t.Fatalf("selection halted events = %d, want exactly 1", halted)
	}
	assertFreewillNoActionEventsForTest(t, events)
}

func TestFreewillRecoveryHaltsOrphanOwnedByOtherRunAndNeverReplaysAfterRelease(t *testing.T) {
	ctx := context.Background()
	env := newDurableTestEnvironment(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	_, orphan := freewillWakeAcceptanceFixture("a-orphan", "agent", scope, "dedupe-a-orphan")
	if _, err := env.store.Create(ctx, orphan, nil); err != nil {
		t.Fatal(err)
	}
	wake, owner := freewillWakeAcceptanceFixture("b-owner", "agent", scope, "dedupe-b-owner")
	if _, _, err := env.store.AcceptFreewillWake(ctx, owner, nil, wake); err != nil {
		t.Fatalf("accept slot owner: %v", err)
	}
	selector := &sleepingFreewillSelector{}
	service := newFreewillRecoveryServiceForTest(t, env, selector)
	if err := service.RecoverPending(ctx, time.Now().Add(time.Minute), 10); err != nil {
		t.Fatalf("recover orphan and valid owner: %v", err)
	}
	reconciled, err := env.store.Load(ctx, orphan.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := env.store.Load(ctx, owner.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Freewill.Lifecycle != freewill.StateHalted ||
		reconciled.Freewill.SelectionFailureCode != freewillAdmissionOtherOwnerCode {
		t.Fatalf("conflicting orphan state = %#v", reconciled.Freewill)
	}
	if processed.Freewill.Lifecycle != freewill.StateSleeping {
		t.Fatalf("same-run admitted crash recovery lifecycle = %s, want sleeping", processed.Freewill.Lifecycle)
	}
	if selector.calls != 1 || len(selector.runIDs) != 1 || selector.runIDs[0] != owner.State.RunID {
		t.Fatalf("selector calls for runs = %#v, want only %q", selector.runIDs, owner.State.RunID)
	}
	var slotOwner string
	if err := env.store.db.QueryRowContext(ctx, "SELECT run_id FROM multiagent_freewill_admissions WHERE actor_key = ?", freewillActorAdmissionKey("agent")).Scan(&slotOwner); err != nil {
		t.Fatal(err)
	}
	if slotOwner != owner.State.RunID {
		t.Fatalf("reconciliation changed another run's actor slot to %q", slotOwner)
	}
	if _, err := env.store.Create(ctx, freewillAdmissionRecord("c-terminal", "agent", scope, freewill.StateSleeping), nil); err != nil {
		t.Fatal(err)
	}
	if err := env.store.EnsureFreewillActorAdmission(ctx, "c-terminal", "agent", scope); err != nil {
		t.Fatalf("release old slot by admitting terminal replacement: %v", err)
	}
	if err := service.RecoverPending(ctx, time.Now().Add(2*time.Minute), 10); err != nil {
		t.Fatalf("recover after slot release: %v", err)
	}
	if selector.calls != 1 {
		t.Fatalf("orphan replayed after slot release; selector calls = %d", selector.calls)
	}
	assertFreewillNoActionEventsForTest(t, queryFreewillRunEventsForTest(t, env, orphan.State.RunID))
}

func TestFreewillRecoveryLeavesMalformedAdmissionAmbiguousWithoutRewrite(t *testing.T) {
	ctx := context.Background()
	env := newDurableTestEnvironment(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	_, orphan := freewillWakeAcceptanceFixture("ambiguous-orphan", "agent", scope, "dedupe-ambiguous-orphan")
	created, err := env.store.Create(ctx, orphan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.ensureFreewillTables(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = env.store.db.ExecContext(ctx, `INSERT INTO multiagent_freewill_admissions(actor_key,run_id,agent_id,project_id,workspace,updated_at) VALUES(?,?,?,?,?,?)`,
		freewillActorAdmissionKey("agent"), "missing-owner", "agent", scope.ProjectID, scope.Workspace, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	selector := &sleepingFreewillSelector{}
	service := newFreewillRecoveryServiceForTest(t, env, selector)
	if err := service.RecoverPending(ctx, time.Now().Add(time.Minute), 10); !errors.Is(err, ErrFreewillActorAdmissionAmbiguous) {
		t.Fatalf("malformed owner result = %v, want ambiguous admission", err)
	}
	unchanged, err := env.store.Load(ctx, orphan.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Freewill.Lifecycle != freewill.StateWaking || unchanged.Revision != created.Revision {
		t.Fatalf("ambiguous orphan was rewritten: lifecycle=%s revision=%d", unchanged.Freewill.Lifecycle, unchanged.Revision)
	}
	if selector.calls != 0 {
		t.Fatalf("ambiguous orphan provider calls = %d, want 0", selector.calls)
	}
	if events := queryFreewillRunEventsForTest(t, env, orphan.State.RunID); len(events) != 0 {
		t.Fatalf("ambiguous orphan emitted events: %v", events)
	}
}

func TestFreewillAdmissionReconciliationRollsBackStateAndHistoryTogether(t *testing.T) {
	ctx := context.Background()
	env := newDurableTestEnvironment(t)
	scope := freewill.Scope{ProjectID: "p", Workspace: "w"}
	_, orphan := freewillWakeAcceptanceFixture("orphan-atomic", "agent", scope, "dedupe-orphan-atomic")
	created, err := env.store.Create(ctx, orphan, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.store.db.ExecContext(ctx, `CREATE TRIGGER fail_freewill_reconciliation_event BEFORE INSERT ON multiagent_outbox
WHEN NEW.run_id = 'orphan-atomic'
BEGIN SELECT RAISE(ABORT, 'synthetic reconciliation event failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	selector := &sleepingFreewillSelector{}
	service := newFreewillRecoveryServiceForTest(t, env, selector)
	if err := service.RecoverPending(ctx, time.Now().Add(time.Minute), 10); err == nil {
		t.Fatal("expected transactional event failure")
	}
	unchanged, err := env.store.Load(ctx, orphan.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Freewill.Lifecycle != freewill.StateWaking || unchanged.Revision != created.Revision {
		t.Fatalf("failed reconciliation changed run state: lifecycle=%s revision=%d", unchanged.Freewill.Lifecycle, unchanged.Revision)
	}
	if events := queryFreewillRunEventsForTest(t, env, orphan.State.RunID); len(events) != 0 {
		t.Fatalf("failed reconciliation published history: %v", events)
	}
	if _, err := env.store.db.ExecContext(ctx, "DROP TRIGGER fail_freewill_reconciliation_event"); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverPending(ctx, time.Now().Add(2*time.Minute), 10); err != nil {
		t.Fatalf("recover after removing synthetic failure: %v", err)
	}
	reconciled, err := env.store.Load(ctx, orphan.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Freewill.Lifecycle != freewill.StateHalted || selector.calls != 0 {
		t.Fatalf("recovered state=%s selector calls=%d", reconciled.Freewill.Lifecycle, selector.calls)
	}
	events := queryFreewillRunEventsForTest(t, env, orphan.State.RunID)
	if len(events) != 1 || events[0].Type != event.EventFreewillSelectionHalted {
		t.Fatalf("atomic reconciliation history = %v", events)
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
	case freewillFlashIdeaSystemPrompt:
		s.ideaCalls++
		if s.ideaCalls != 1 || len(req.Tools) != 0 {
			return provider.ChatGenerateResponse{}, errors.New("unexpected phase-one request")
		}
		return s.ideaResponse, nil
	case freewillFlashChoiceSystemPrompt:
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
	scored := `{"candidates":[{"idea":"Map dependency ownership","score":72,"factors":["scope_fit"],"rationale":"This maps dependencies and identifies risky coupling that merits focused future review."},{"idea":"Document service boundaries","score":88,"factors":["impact"],"rationale":"Clear ownership notes help contributors route future work and avoid accidental cross-domain changes."},{"idea":"Review coverage gaps","score":61,"factors":["feasibility"],"rationale":"A focused gap list can guide later tests while keeping the review read only and within scope."}],"sleep":false}`
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
	if req.Model != model || req.MaxTokens != freewillChoiceMaxOutputTokens || len(req.Tools) != 0 || len(req.Messages) != 2 || req.OutputSchema == nil {
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
		factors, factorsOK := candidate["factors"].([]any)
		if !idOK || !titleOK || !scoreOK || !factorsOK || len(factors) == 0 || id != fmt.Sprintf("candidate-%s-%d", runID, i+1) || title != titles[i] || score != scores[i] {
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
