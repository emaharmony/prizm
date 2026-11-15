package multiagent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/policy"
)

type freewillApprovalEvaluatorStub struct {
	decision policy.PolicyDecision
}

func (s freewillApprovalEvaluatorStub) Evaluate(policy.PolicyRequest) policy.PolicyDecision {
	return s.decision
}

func TestFreewillApprovalPolicyAllowsAndRequiresApprovalRemainExecutionDisabled(t *testing.T) {
	for _, decision := range []policy.Decision{policy.DecisionAllowed, policy.DecisionRequiresApproval} {
		t.Run(string(decision), func(t *testing.T) {
			env, _, service, record, roleRunner := newFreewillApprovalTestService(t, decision)

			proposed, err := service.RequestApproval(context.Background(), record.State.RunID)
			if err != nil {
				t.Fatalf("request approval: %v", err)
			}
			if proposed.Freewill.Lifecycle != freewill.StateApprovalPending || proposed.Freewill.PolicyDecision != string(decision) ||
				!proposed.Freewill.ExecutionDisabled || proposed.Freewill.ApprovalRef == "" {
				t.Fatalf("proposal state = %#v", proposed.Freewill)
			}

			granted, err := service.Decide(context.Background(), record.State.RunID, proposed.Freewill.ApprovalRef, "grant", "owner", "")
			if err != nil {
				t.Fatalf("grant proposal: %v", err)
			}
			if granted.Freewill.Lifecycle != freewill.StatePaused || granted.Freewill.PauseReason != "execution_disabled" || !granted.Freewill.ExecutionDisabled {
				t.Fatalf("grant crossed decision-only boundary: %#v", granted.Freewill)
			}
			if calls := runnerCalls(roleRunner); len(calls) != 0 {
				t.Fatalf("approval gate executed workflow roles: %v", calls)
			}
			assertFreewillApprovalEventsDoNotExecute(t, env.events, record.State.RunID)
		})
	}
}

func TestFreewillApprovalServiceListsBoundedStatusPage(t *testing.T) {
	_, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
	statuses, err := service.List(context.Background(), 500)
	if err != nil {
		t.Fatalf("list Freewill status: %v", err)
	}
	if len(statuses) != 1 || statuses[0].RunID != record.State.RunID || statuses[0].Lifecycle != record.Freewill.Lifecycle {
		t.Fatalf("listed statuses=%#v, want one projection for %s", statuses, record.State.RunID)
	}
}

func TestFreewillDecisionOnlyGrantDoesNotGainExecutionWhenDispatcherIsEnabledLater(t *testing.T) {
	_, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
	pending, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	granted, err := service.Decide(context.Background(), record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", "")
	if err != nil {
		t.Fatalf("grant decision-only approval: %v", err)
	}
	if granted.Freewill.ExecutionDispatch != nil {
		t.Fatalf("decision-only grant unexpectedly wrote execution marker: %#v", granted.Freewill.ExecutionDispatch)
	}

	dispatcher := &freewillExecutionDispatcherStub{}
	service.SetExecutionDispatcher(dispatcher)
	if _, err := service.DispatchApproved(context.Background(), record.State.RunID); !errors.Is(err, ErrFreewillApprovalStale) {
		t.Fatalf("dispatch old decision-only grant after enablement = %v, want stale approval", err)
	}
	if len(dispatcher.calls) != 0 {
		t.Fatalf("enabling execution dispatched an earlier decision-only grant: %#v", dispatcher.calls)
	}
}

func TestFreewillApprovalPolicyDenialIsFinalAndQueryable(t *testing.T) {
	env, _, service, record, roleRunner := newFreewillApprovalTestService(t, policy.DecisionDenied)

	denied, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("evaluate denied proposal: %v", err)
	}
	if denied.Freewill.Lifecycle != freewill.StateDenied || denied.Freewill.PolicyDecision != string(policy.DecisionDenied) ||
		denied.Freewill.PauseReason != "policy_denied" || !denied.Freewill.ExecutionDisabled || denied.Freewill.ApprovalRef != "" {
		t.Fatalf("policy denial was not final and execution-disabled: %#v", denied.Freewill)
	}

	status, err := service.Status(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("query denied proposal: %v", err)
	}
	if status.Lifecycle != freewill.StateDenied || status.PolicyDecision != string(policy.DecisionDenied) ||
		status.PauseReason != "policy_denied" || !status.ExecutionDisabled || status.ApprovalID != "" {
		t.Fatalf("queryable status lost policy denial: %#v", status)
	}
	if calls := runnerCalls(roleRunner); len(calls) != 0 {
		t.Fatalf("denied proposal executed workflow roles: %v", calls)
	}
	assertFreewillApprovalEventsDoNotExecute(t, env.events, record.State.RunID)
	events, err := env.events.Query(context.Background(), event.EventFilter{RunID: record.State.RunID, Limit: 100})
	if err != nil {
		t.Fatalf("query denial event: %v", err)
	}
	for _, current := range events {
		if current.Type == event.EventFreewillPolicyDenied {
			return
		}
	}
	t.Fatal("policy denial event was not persisted")
}

func TestFreewillApprovalEditedRequestInvalidatesPriorFingerprint(t *testing.T) {
	_, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)

	pending, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	changed, err := service.store.Load(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("load proposed record: %v", err)
	}
	changed.Freewill.Candidates[0].Request.Goal = "A changed request after approval was proposed"
	changed, err = service.store.Checkpoint(context.Background(), changed.Revision, changed, nil)
	if err != nil {
		t.Fatalf("persist edited request fixture: %v", err)
	}

	if _, err := service.Decide(context.Background(), record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", ""); !errors.Is(err, ErrFreewillApprovalStale) {
		t.Fatalf("grant for edited request = %v, want stale approval", err)
	}
	item, err := service.approvals.Load(record.State.RunID, pending.Freewill.ApprovalRef)
	if err != nil {
		t.Fatalf("load approval: %v", err)
	}
	if item.Status != approval.StatusPending {
		t.Fatalf("stale approval was changed to %q", item.Status)
	}
}

type freewillExecutionDispatcherStub struct {
	calls []FreewillExecutionRequest
	err   error
}

type blockingFreewillExecutionDispatcher struct {
	started chan FreewillExecutionRequest
	release chan struct{}
}

func TestExecutionEnabledSelectorCapabilitiesAreHostBound(t *testing.T) {
	selection := freewill.Selection{Candidates: []freewill.CandidateIntention{{
		Request: freewill.IntentionRequest{RequestedCapabilities: []string{"shell", "network"}},
	}}}
	decisionOnly := bindFreewillExecutionCapabilities(selection, false)
	if strings.Join(decisionOnly.Candidates[0].Request.RequestedCapabilities, ",") != "shell,network" {
		t.Fatalf("decision-only selection changed without execution opt-in: %#v", decisionOnly)
	}
	bounded := bindFreewillExecutionCapabilities(selection, true)
	if len(bounded.Candidates[0].Request.RequestedCapabilities) != 1 || bounded.Candidates[0].Request.RequestedCapabilities[0] != "code" {
		t.Fatalf("execution opt-in did not host-bind the exact supported capability: %#v", bounded)
	}
}

func (s *freewillExecutionDispatcherStub) Start(_ context.Context, request FreewillExecutionRequest) (FreewillExecutionResult, error) {
	s.calls = append(s.calls, request)
	if s.err != nil {
		return FreewillExecutionResult{}, s.err
	}
	return FreewillExecutionResult{RunID: request.RunID, ReportRef: "multiagent_report.json"}, nil
}

func (d *blockingFreewillExecutionDispatcher) Start(ctx context.Context, request FreewillExecutionRequest) (FreewillExecutionResult, error) {
	select {
	case d.started <- request:
	case <-ctx.Done():
		return FreewillExecutionResult{}, ctx.Err()
	}
	select {
	case <-d.release:
		return FreewillExecutionResult{RunID: request.RunID, ReportRef: "multiagent_report.json"}, nil
	case <-ctx.Done():
		return FreewillExecutionResult{}, ctx.Err()
	}
}

func TestFreewillGrantDispatchesOnceAndPersistsChildReportLink(t *testing.T) {
	_, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
	dispatcher := &freewillExecutionDispatcherStub{}
	service.SetExecutionDispatcher(dispatcher)
	pending, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	granted, err := service.Decide(context.Background(), record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", "")
	if err != nil {
		t.Fatalf("grant intention: %v", err)
	}
	if len(dispatcher.calls) != 1 || granted.Freewill.ExecutionDispatch == nil || granted.Freewill.ExecutionDispatch.Status != "accepted" {
		t.Fatalf("dispatch was not durably accepted exactly once: calls=%d state=%#v", len(dispatcher.calls), granted.Freewill.ExecutionDispatch)
	}
	call := dispatcher.calls[0]
	if call.RunID != freewillExecutionChildRunID(call.Identity.RequesterID, call.Identity.SessionID, call.Identity.IdempotencyKey) ||
		call.ParentRunID != record.State.RunID || call.ApprovalID != pending.Freewill.ApprovalRef {
		t.Fatalf("child request did not use stable exact-approval identity: %#v", call)
	}
	status, err := service.Status(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("read execution status: %v", err)
	}
	if status.ExecutionStatus != "accepted" || status.ChildRunID != call.RunID || status.ExecutionReportRef != "multiagent_report.json" || !status.ExecutionDisabled {
		t.Fatalf("execution linkage/report not exposed with disabled parent authority: %#v", status)
	}
	if _, err := service.DispatchApproved(context.Background(), record.State.RunID); err != nil {
		t.Fatalf("duplicate dispatch recovery: %v", err)
	}
	if len(dispatcher.calls) != 1 {
		t.Fatalf("accepted dispatch was repeated: %d calls", len(dispatcher.calls))
	}
}

func TestFreewillExecutionGrantPersistsAdmissionBeforeDispatcherStart(t *testing.T) {
	env, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
	dispatcher := &blockingFreewillExecutionDispatcher{started: make(chan FreewillExecutionRequest, 1), release: make(chan struct{}, 1)}
	service.SetExecutionDispatcher(dispatcher)
	pending, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	admission := env.store
	if err := admission.EnsureFreewillActorAdmission(context.Background(), record.State.RunID, "agent-1", freewill.Scope{ProjectID: "project-1", Workspace: "workspace-1"}); err != nil {
		t.Fatalf("reserve initial actor admission: %v", err)
	}
	type decisionResult struct {
		record DurableRun
		err    error
	}
	decisionDone := make(chan decisionResult, 1)
	go func() {
		granted, decideErr := service.Decide(context.Background(), record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", "")
		decisionDone <- decisionResult{record: granted, err: decideErr}
	}()
	defer func() {
		select {
		case dispatcher.release <- struct{}{}:
		default:
		}
	}()

	var childRequest FreewillExecutionRequest
	select {
	case childRequest = <-dispatcher.started:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not start after the grant checkpoint")
	}
	checkpointed, err := env.store.Load(context.Background(), record.State.RunID)
	if err != nil || checkpointed.Freewill.ApprovalReceipt == nil ||
		checkpointed.Freewill.ApprovalReceipt.Decision != FreewillApprovalGranted ||
		checkpointed.Freewill.ExecutionDispatch == nil || checkpointed.Freewill.ExecutionDispatch.Status != "dispatching" ||
		checkpointed.Freewill.ExecutionDispatch.ChildRunID != childRequest.RunID ||
		checkpointed.Freewill.ExecutionDispatch.ReportRef != "multiagent_report.json" {
		t.Fatalf("grant checkpoint did not persist dispatch intent atomically: state=%#v err=%v", checkpointed.Freewill, err)
	}
	events, err := env.events.Query(context.Background(), event.EventFilter{RunID: record.State.RunID, Limit: 100})
	if err != nil {
		t.Fatalf("query grant/dispatch marker events: %v", err)
	}
	dispatchMarkerEventFound := false
	for _, current := range events {
		if current.Type == event.EventFreewillExecutionDispatching && current.Payload["child_run_id"] == childRequest.RunID {
			dispatchMarkerEventFound = true
			break
		}
	}
	if !dispatchMarkerEventFound {
		t.Fatal("atomic grant checkpoint omitted the stable-child dispatching event")
	}
	wake, wakeRecord := freewillWakeAcceptanceFixture("next-after-grant", "agent-1",
		freewill.Scope{ProjectID: "project-1", Workspace: "workspace-1"}, "next-after-grant-dedupe")
	if _, _, err := env.store.AcceptFreewillWake(context.Background(), wakeRecord, nil, wake); !errors.Is(err, ErrFreewillActorAdmissionConflict) {
		t.Fatalf("new wake while granted child is dispatching = %v, want actor-admission conflict", err)
	}
	dispatcher.release <- struct{}{}
	select {
	case result := <-decisionDone:
		if result.err != nil || result.record.Freewill.ExecutionDispatch == nil ||
			result.record.Freewill.ExecutionDispatch.Status != "accepted" ||
			result.record.Freewill.ExecutionDispatch.ChildRunID != childRequest.RunID {
			t.Fatalf("grant dispatch result = state %#v err=%v child=%q", result.record.Freewill.ExecutionDispatch, result.err, childRequest.RunID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("grant decision did not finish after dispatcher release")
	}
}

func TestFreewillAcceptedChildResumeFailsClosedAfterOperatorHalt(t *testing.T) {
	_, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
	dispatcher := &freewillExecutionDispatcherStub{}
	service.SetExecutionDispatcher(dispatcher)
	pending, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Decide(context.Background(), record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", ""); err != nil {
		t.Fatal(err)
	}
	control, err := NewFreewillOperatorControl(service.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.SetHalted(context.Background(), "agent-1", freewill.Scope{ProjectID: "project-1", Workspace: "workspace-1"}, true, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateAcceptedExecutionResume(context.Background(), record.State.RunID); !errors.Is(err, ErrFreewillHalted) {
		t.Fatalf("accepted child resume after halt = %v, want ErrFreewillHalted", err)
	}
}

func TestFreewillAcceptedChildResumeRejectsNonacceptedDispatch(t *testing.T) {
	_, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
	dispatcher := &freewillExecutionDispatcherStub{}
	service.SetExecutionDispatcher(dispatcher)
	pending, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Decide(context.Background(), record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", ""); err != nil {
		t.Fatal(err)
	}
	accepted, err := service.store.Load(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	accepted.Freewill.ExecutionDispatch.Status = "dispatching"
	if _, err := service.store.Checkpoint(context.Background(), accepted.Revision, accepted, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateAcceptedExecutionResume(context.Background(), record.State.RunID); !errors.Is(err, ErrFreewillApprovalStale) {
		t.Fatalf("nonaccepted dispatch resume=%v, want ErrFreewillApprovalStale", err)
	}
}

func TestFreewillRecoveryQuarantineIsDurableAndFencedAcrossRuntimeAttempts(t *testing.T) {
	ctx := context.Background()
	env, firstRuntime, first, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
	claimRoot := filepath.Join(t.TempDir(), "claims")
	firstRuntime.claimer = FileRunClaimer{Root: claimRoot}
	dispatcher := &freewillExecutionDispatcherStub{}
	first.SetExecutionDispatcher(dispatcher)
	pending, err := first.RequestApproval(ctx, record.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := first.Decide(ctx, record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", "")
	if err != nil || accepted.Freewill.ExecutionDispatch == nil || accepted.Freewill.ExecutionDispatch.Status != "accepted" {
		t.Fatalf("create accepted dispatch: dispatch=%#v err=%v", accepted.Freewill.ExecutionDispatch, err)
	}

	secondEvents, err := event.NewSQLiteEventStore(env.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondEvents.Close()
	secondStore, err := NewSQLiteDurableRunStore(env.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	secondRuntime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, secondStore,
		FileRunClaimer{Root: claimRoot}, secondEvents)
	second, err := NewFreewillApprovalService(secondRuntime, secondStore, first.approvals,
		freewillApprovalEvaluatorStub{decision: policy.PolicyDecision{Decision: policy.DecisionRequiresApproval}}, "freewill-policy")
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		record DurableRun
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, service := range []*FreewillApprovalService{first, second} {
		go func(service *FreewillApprovalService) {
			<-start
			quarantined, quarantineErr := service.QuarantineExecutionRecovery(ctx, record.State.RunID, "child_mutation_possible")
			results <- result{record: quarantined, err: quarantineErr}
		}(service)
	}
	close(start)
	firstResult, secondResult := <-results, <-results
	if firstResult.err != nil && !errors.Is(firstResult.err, ErrRunClaimed) {
		t.Fatalf("first runtime quarantine: %v", firstResult.err)
	}
	if secondResult.err != nil && !errors.Is(secondResult.err, ErrRunClaimed) {
		t.Fatalf("second runtime quarantine: %v", secondResult.err)
	}
	if firstResult.err != nil && secondResult.err != nil {
		t.Fatalf("both runtime attempts were fenced without a quarantine: first=%v second=%v", firstResult.err, secondResult.err)
	}
	// A process that lost the first claim observes the durable transition and
	// treats the exact replay as an idempotent no-op.
	if _, err := second.QuarantineExecutionRecovery(ctx, record.State.RunID, "child_mutation_possible"); err != nil {
		t.Fatalf("replay quarantine after claim handoff: %v", err)
	}
	projected, err := secondStore.Load(ctx, record.State.RunID)
	if err != nil || projected.Freewill.ExecutionDispatch.Status != "recovery_uncertain" ||
		projected.Freewill.ExecutionDispatch.RecoveryReason != "child_mutation_possible" ||
		projected.Freewill.ExecutionDispatch.ChildRunID != accepted.Freewill.ExecutionDispatch.ChildRunID ||
		projected.Freewill.ApprovalReceipt == nil || projected.Freewill.ApprovalReceipt.ApprovalID != accepted.Freewill.ApprovalReceipt.ApprovalID {
		t.Fatalf("quarantine changed approval/child identity or was not durable: dispatch=%#v approval=%#v err=%v",
			projected.Freewill.ExecutionDispatch, projected.Freewill.ApprovalReceipt, err)
	}
	events, err := secondEvents.Query(ctx, event.EventFilter{RunID: record.State.RunID, Type: event.EventFreewillExecutionRecoveryUncertain, Limit: 10})
	if err != nil || len(events) != 1 {
		t.Fatalf("quarantine audit event count=%d err=%v want=1", len(events), err)
	}
	if err := secondStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secondEvents.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, err := NewSQLiteDurableRunStore(env.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedStore.Close()
	reopenedEvents, err := event.NewSQLiteEventStore(env.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedEvents.Close()
	reopened, err := reopenedStore.Load(ctx, record.State.RunID)
	if err != nil || reopened.Freewill.ExecutionDispatch.Status != "recovery_uncertain" ||
		reopened.Freewill.ExecutionDispatch.RecoveryReason != "child_mutation_possible" ||
		reopened.Freewill.ExecutionDispatch.ChildRunID != accepted.Freewill.ExecutionDispatch.ChildRunID {
		t.Fatalf("quarantine did not survive SQLite reopen: dispatch=%#v err=%v", reopened.Freewill.ExecutionDispatch, err)
	}
	reopenedAudit, err := reopenedEvents.Query(ctx, event.EventFilter{RunID: record.State.RunID, Type: event.EventFreewillExecutionRecoveryUncertain, Limit: 10})
	if err != nil || len(reopenedAudit) != 1 {
		t.Fatalf("quarantine audit after SQLite reopen count=%d err=%v want=1", len(reopenedAudit), err)
	}
}

func TestFreewillRecoveryQuarantineRepairsOnlyWithHostTerminalEvidence(t *testing.T) {
	for _, test := range []struct {
		name           string
		reason         string
		wantResolution bool
	}{
		{name: "report evidence can be repaired", reason: "terminal_report_invalid", wantResolution: true},
		{name: "possible external mutation remains unresolved", reason: "child_mutation_possible", wantResolution: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			_, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
			dispatcher := &freewillExecutionDispatcherStub{}
			service.SetExecutionDispatcher(dispatcher)
			pending, err := service.RequestApproval(ctx, record.State.RunID)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := service.Decide(ctx, record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", "")
			if err != nil {
				t.Fatal(err)
			}
			quarantined, err := service.QuarantineExecutionRecovery(ctx, record.State.RunID, test.reason)
			if err != nil || quarantined.Freewill.ExecutionDispatch.Status != "recovery_uncertain" {
				t.Fatalf("quarantine: dispatch=%#v err=%v", quarantined.Freewill.ExecutionDispatch, err)
			}
			result := FreewillExecutionTerminalResult{ChildRunID: accepted.Freewill.ExecutionDispatch.ChildRunID,
				ChildStatus: RunStatusFailed, ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64),
				ValidationSummary: "failed", FailureCode: "validation_failed"}
			resolved, err := service.ReconcileExecutionTerminal(ctx, record.State.RunID, result)
			if !test.wantResolution {
				if err == nil || resolved.Freewill.ExecutionDispatch.Status != "recovery_uncertain" ||
					resolved.Revision != quarantined.Revision {
					t.Fatalf("terminal report cleared mutation quarantine: dispatch=%#v revision=%d want=%d err=%v",
						resolved.Freewill.ExecutionDispatch, resolved.Revision, quarantined.Revision, err)
				}
				return
			}
			if err != nil || resolved.Freewill.ExecutionDispatch.Status != "execution_failed" ||
				resolved.Freewill.ExecutionDispatch.RecoveryReason != "" || resolved.Freewill.ExecutionDispatch.ReportSHA256 != result.ReportSHA256 {
				t.Fatalf("valid host evidence did not repair quarantine: dispatch=%#v err=%v", resolved.Freewill.ExecutionDispatch, err)
			}
			if len(dispatcher.calls) != 1 {
				t.Fatalf("repair restarted child dispatch: calls=%d want=1", len(dispatcher.calls))
			}
		})
	}
}

func TestFreewillTerminalChildProjectionIsVisibleAndIdempotent(t *testing.T) {
	for _, test := range []struct {
		name              string
		childStatus       RunStatus
		validationSummary string
		failureCode       string
		wantStatus        string
		wantEvent         string
	}{
		{name: "completed", childStatus: RunStatusCompleted, validationSummary: "passed", wantStatus: "completed", wantEvent: event.EventFreewillExecutionCompleted},
		{name: "failed", childStatus: RunStatusFailed, validationSummary: "failed", failureCode: "validation_failed", wantStatus: "execution_failed", wantEvent: event.EventFreewillExecutionFailed},
		{name: "cancelled", childStatus: RunStatusCancelled, validationSummary: "not_run", failureCode: "child_cancelled", wantStatus: "execution_cancelled", wantEvent: event.EventFreewillExecutionCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			env, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
			dispatcher := &freewillExecutionDispatcherStub{}
			service.SetExecutionDispatcher(dispatcher)
			pending, err := service.RequestApproval(context.Background(), record.State.RunID)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := service.Decide(context.Background(), record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", "")
			if err != nil {
				t.Fatalf("grant intention: %v", err)
			}
			result := FreewillExecutionTerminalResult{ChildRunID: accepted.Freewill.ExecutionDispatch.ChildRunID,
				ChildStatus: test.childStatus, ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64),
				ValidationSummary: test.validationSummary, FailureCode: test.failureCode}
			terminal, err := service.ReconcileExecutionTerminal(context.Background(), record.State.RunID, result)
			if err != nil {
				t.Fatalf("persist child terminal outcome: %v", err)
			}
			if terminal.Freewill.ExecutionDispatch.Status != test.wantStatus || terminal.Freewill.ExecutionDispatch.ChildStatus != string(test.childStatus) {
				t.Fatalf("parent outcome = %#v", terminal.Freewill.ExecutionDispatch)
			}
			status, err := service.Status(context.Background(), record.State.RunID)
			if err != nil {
				t.Fatalf("read Freewill status: %v", err)
			}
			if status.ExecutionStatus != test.wantStatus || status.ChildRunID != result.ChildRunID || status.ChildStatus != string(test.childStatus) ||
				status.ExecutionReportRef != result.ReportRef || status.ExecutionReportSHA256 != result.ReportSHA256 || status.ValidationSummary != test.validationSummary ||
				status.ExecutionFailureCode != test.failureCode {
				t.Fatalf("status omitted terminal report evidence: %#v", status)
			}
			revision := terminal.Revision
			replayed, err := service.ReconcileExecutionTerminal(context.Background(), record.State.RunID, result)
			if err != nil || replayed.Revision != revision {
				t.Fatalf("same terminal replay changed parent: revision=%d want=%d err=%v", replayed.Revision, revision, err)
			}
			conflict := result
			conflict.ReportSHA256 = strings.Repeat("b", 64)
			if _, err := service.ReconcileExecutionTerminal(context.Background(), record.State.RunID, conflict); !errors.Is(err, ErrRequestIdentityConflict) {
				t.Fatalf("conflicting terminal replay = %v", err)
			}
			persisted, err := env.store.Load(context.Background(), record.State.RunID)
			if err != nil || persisted.Freewill.ExecutionDispatch.ReportSHA256 != result.ReportSHA256 {
				t.Fatalf("conflicting replay overwrote durable report digest: state=%#v err=%v", persisted.Freewill.ExecutionDispatch, err)
			}
			events, err := env.events.Query(context.Background(), event.EventFilter{RunID: record.State.RunID, Type: test.wantEvent, Limit: 10})
			if err != nil || len(events) != 1 {
				t.Fatalf("terminal event count=%d err=%v want=1", len(events), err)
			}
			if _, err := service.DispatchApproved(context.Background(), record.State.RunID); err != nil {
				t.Fatalf("recovery after terminal outcome: %v", err)
			}
			if len(dispatcher.calls) != 1 {
				t.Fatalf("terminal outcome was redispatched: calls=%d", len(dispatcher.calls))
			}
		})
	}
}

type freewillTerminalFactSinkStub struct {
	attempts []FreewillTerminalFact
	err      error
}

func (s *freewillTerminalFactSinkStub) CaptureFreewillTerminalFact(_ context.Context, fact FreewillTerminalFact) error {
	s.attempts = append(s.attempts, fact)
	return s.err
}

func TestFreewillTerminalFactSinkRetriesAfterDurableTerminalWithoutDuplicateEvent(t *testing.T) {
	env, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
	dispatcher := &freewillExecutionDispatcherStub{}
	service.SetExecutionDispatcher(dispatcher)
	pending, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.Decide(context.Background(), record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", "")
	if err != nil {
		t.Fatal(err)
	}
	result := FreewillExecutionTerminalResult{ChildRunID: accepted.Freewill.ExecutionDispatch.ChildRunID,
		ChildStatus: RunStatusCompleted, ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("c", 64),
		ValidationSummary: "passed"}
	sink := &freewillTerminalFactSinkStub{err: errors.New("local store temporarily unavailable")}
	service.SetTerminalFactSink(sink)
	terminal, err := service.ReconcileExecutionTerminal(context.Background(), record.State.RunID, result)
	if err == nil || terminal.Freewill.ExecutionDispatch.Status != "completed" {
		t.Fatalf("capture failure changed terminal outcome: status=%q err=%v", terminal.Freewill.ExecutionDispatch.Status, err)
	}
	persisted, err := env.store.Load(context.Background(), record.State.RunID)
	if err != nil || persisted.Freewill.ExecutionDispatch.ReportSHA256 != result.ReportSHA256 {
		t.Fatalf("terminal record not durable before sink retry: record=%#v err=%v", persisted.Freewill.ExecutionDispatch, err)
	}
	sink.err = nil
	if _, err := service.ReconcileExecutionTerminal(context.Background(), record.State.RunID, result); err != nil {
		t.Fatalf("identical terminal replay did not retry fact capture: %v", err)
	}
	if err := service.RetryTerminalFactCapture(context.Background(), record.State.RunID); err != nil {
		t.Fatalf("startup replay did not retry durable fact capture: %v", err)
	}
	if len(sink.attempts) != 2 || sink.attempts[0] != sink.attempts[1] {
		t.Fatalf("fact retry changed binding/evidence: %#v", sink.attempts)
	}
	events, err := env.events.Query(context.Background(), event.EventFilter{RunID: record.State.RunID, Type: event.EventFreewillExecutionCompleted, Limit: 10})
	if err != nil || len(events) != 1 {
		t.Fatalf("terminal event count=%d err=%v want=1", len(events), err)
	}
}

func TestFreewillDenialNeverDispatches(t *testing.T) {
	_, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
	dispatcher := &freewillExecutionDispatcherStub{}
	service.SetExecutionDispatcher(dispatcher)
	pending, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	denied, err := service.Decide(context.Background(), record.State.RunID, pending.Freewill.ApprovalRef, "deny", "owner", "not this task")
	if err != nil {
		t.Fatalf("deny intention: %v", err)
	}
	if len(dispatcher.calls) != 0 || denied.Freewill.ExecutionDispatch != nil || denied.Freewill.Lifecycle != freewill.StateDenied {
		t.Fatalf("denied intention reached dispatcher: calls=%d state=%#v", len(dispatcher.calls), denied.Freewill)
	}
}

func TestFreewillDispatchRecoveryReusesStableChildIdentity(t *testing.T) {
	env, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)
	dispatcher := &freewillExecutionDispatcherStub{err: errors.New("simulated lost child-start response")}
	service.SetExecutionDispatcher(dispatcher)
	pending, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	failed, err := service.Decide(context.Background(), record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", "")
	if err != nil || failed.Freewill.ExecutionDispatch == nil || failed.Freewill.ExecutionDispatch.Status != "failed" {
		t.Fatalf("lost response was not recorded for retry: state=%#v err=%v", failed.Freewill.ExecutionDispatch, err)
	}

	// Recompose the service over the same durable stores to model process restart.
	runtime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, env.store, env.claimer, env.events)
	restarted, err := NewFreewillApprovalService(runtime, env.store, service.approvals, freewillApprovalEvaluatorStub{decision: policy.PolicyDecision{Decision: policy.DecisionRequiresApproval}}, "freewill-policy")
	if err != nil {
		t.Fatalf("recompose approval service: %v", err)
	}
	dispatcher.err = nil
	restarted.SetExecutionDispatcher(dispatcher)
	accepted, err := restarted.DispatchApproved(context.Background(), record.State.RunID)
	if err != nil || accepted.Freewill.ExecutionDispatch == nil || accepted.Freewill.ExecutionDispatch.Status != "accepted" {
		t.Fatalf("recovery dispatch failed: state=%#v err=%v", accepted.Freewill.ExecutionDispatch, err)
	}
	if len(dispatcher.calls) != 2 || dispatcher.calls[0].RunID != dispatcher.calls[1].RunID || dispatcher.calls[0].Identity.IdempotencyKey != dispatcher.calls[1].Identity.IdempotencyKey {
		t.Fatalf("restart did not retry the same child identity: %#v", dispatcher.calls)
	}
}

func TestFreewillApprovalServiceRejectsMissingPolicy(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runtime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, env.store, env.claimer, env.events)
	if _, err := NewFreewillApprovalService(runtime, env.store, approval.NewStore(filepath.Join(t.TempDir(), "runs")), nil, "freewill-policy"); err == nil {
		t.Fatal("approval service composed without a policy evaluator")
	}
}

func newFreewillApprovalTestService(t *testing.T, decision policy.Decision) (durableTestEnvironment, *DurableRuntime, *FreewillApprovalService, DurableRun, *scriptedRunner) {
	t.Helper()
	env := newDurableTestEnvironment(t)
	roleRunner := &scriptedRunner{}
	runtime := newDurableRuntimeForTest(t, validDefinition(), roleRunner, env.store, env.claimer, env.events)
	approvalStore := approval.NewStore(filepath.Join(t.TempDir(), "runs"))
	evaluator := freewillApprovalEvaluatorStub{decision: policy.PolicyDecision{Decision: decision, RuleID: "test-rule"}}
	service, err := NewFreewillApprovalService(runtime, env.store, approvalStore, evaluator, "freewill-policy")
	if err != nil {
		t.Fatalf("compose Freewill approval service: %v", err)
	}
	request := freewill.IntentionRequest{
		RequestID: "request-1", AgentID: "agent-1", Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew,
		Title: "Read-only review", Goal: "Review a synthetic project", ReasonSummary: "The review is bounded and read-only.",
		RequestedCapabilities: []string{"memory.read"}, Scope: freewill.Scope{ProjectID: "project-1", Workspace: "workspace-1"},
		IdempotencyKey: "idem-1", CorrelationID: "corr-1",
	}
	now := time.Now().UTC()
	record := DurableRun{
		SchemaVersion: DurableRunSchemaVersion, Kind: DurableRunKindFreewill, Phase: CheckpointFreewillWake,
		State: RunState{RunID: "run-freewill-approval", WorkflowID: FreewillWorkflowID, WorkflowVersion: 1,
			RequestIdentity: &RequestIdentity{RequesterID: request.AgentID, IdempotencyKey: request.IdempotencyKey}, CreatedAt: now, UpdatedAt: now},
		Freewill: &FreewillRunState{Lifecycle: freewill.StateProposed, Scope: request.Scope, WakeID: "wake-1", Trigger: "scheduled",
			DedupeKey: request.IdempotencyKey, CorrelationID: request.CorrelationID, Candidates: []freewill.CandidateIntention{{CandidateID: "candidate-1", Request: request}},
			ChoiceID: "candidate-1", ReasonSummary: "Selected the bounded read-only review.", ExecutionDisabled: true},
	}
	if _, err := env.store.Create(context.Background(), record, nil); err != nil {
		t.Fatalf("create proposed Freewill record: %v", err)
	}
	return env, runtime, service, record, roleRunner
}

func assertFreewillApprovalEventsDoNotExecute(t *testing.T, store event.EventStore, runID string) {
	t.Helper()
	events, err := store.Query(context.Background(), event.EventFilter{RunID: runID, Limit: 100})
	if err != nil {
		t.Fatalf("query Freewill events: %v", err)
	}
	for _, current := range events {
		if current.Type == event.EventMultiAgentFanoutChildDispatched || current.Type == event.EventMultiAgentToolDiagnostic || strings.Contains(current.Type, ".tool.") ||
			strings.Contains(current.Type, ".role.") || strings.Contains(current.Type, ".action.") || strings.Contains(current.Type, "proposal.apply_") ||
			current.Type == "prizm.workflow.proposal.apply_started" || current.Type == "prizm.workflow.proposal.apply_completed" ||
			current.Type == "prizm.workflow.role.completed" || current.Type == "prizm.workflow.role.started" || current.Type == "prizm.workflow.action.started" ||
			current.Type == "prizm.workflow.action.completed" {
			t.Fatalf("policy/approval gate emitted execution event %q", current.Type)
		}
	}
}
