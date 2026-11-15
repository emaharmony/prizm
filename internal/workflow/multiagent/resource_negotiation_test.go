package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

func TestDurableResourceExtensionWaitsForScopedOwnerDecisionAndResumesSavedTransition(t *testing.T) {
	env := newDurableTestEnvironment(t)
	definition := validDefinition()
	definition.Budgets.MaxTransitions = Limit(2)
	planner := scriptedResult(OutcomePlanReady)
	planner.OutgoingHandoff.ResourceExtensionRequest = &ResourceExtensionIntent{Reason: "Need one more role to complete verification."}
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner:   {{result: planner}},
		RoleDeveloper: {{result: scriptedResult(OutcomeImplementationReady)}},
	}}
	options := resourceNegotiationTestOptions("parent-extension-accept")
	runtime := newDurableRuntimeForTest(t, definition, runner, env.store, env.claimer, env.events, options)
	request := testRunRequest()
	request.RunID = "run-resource-extension-accept"

	paused, err := runtime.Run(context.Background(), request)
	if err == nil || paused.Status != RunStatusPaused {
		t.Fatalf("run state=%q err=%v, want paused owner decision", paused.Status, err)
	}
	stored, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("load pending run: %v", err)
	}
	if stored.Phase != CheckpointTransitionPending || stored.PendingTransition == nil || stored.Waiting == nil ||
		stored.Waiting.Kind != "resource_extension" || stored.ResourceNegotiation == nil ||
		stored.ResourceNegotiation.Allowance != 1 || !resourceRequestPending(stored) {
		t.Fatalf("pending resource checkpoint = %#v", stored)
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal pending resource checkpoint: %v", err)
	}
	var roundTripped DurableRun
	if err := json.Unmarshal(encoded, &roundTripped); err != nil {
		t.Fatalf("unmarshal pending resource checkpoint: %v", err)
	}
	if roundTripped.ResourceNegotiation == nil || roundTripped.ResourceNegotiation.Config.ParentRunID != options.ResourceNegotiation.ParentRunID ||
		roundTripped.ResourceNegotiation.Config.AgentID != options.ResourceNegotiation.AgentID ||
		roundTripped.ResourceNegotiation.Config.ProjectID != options.ResourceNegotiation.ProjectID ||
		roundTripped.ResourceNegotiation.Config.Workspace != options.ResourceNegotiation.Workspace ||
		roundTripped.ResourceNegotiation.Config.GrantGeneration != options.ResourceNegotiation.GrantGeneration {
		t.Fatalf("resource scope lost on durable JSON roundtrip: %#v", roundTripped.ResourceNegotiation)
	}

	reopenedStore, err := NewSQLiteDurableRunStore(env.dbPath)
	if err != nil {
		t.Fatalf("reopen durable SQLite store: %v", err)
	}
	defer reopenedStore.Close()
	if got := runnerCalls(runner); !reflect.DeepEqual(got, []Role{RolePlanner}) {
		t.Fatalf("roles before owner decision=%v, want only planner", got)
	}
	req, err := runtime.InspectResourceExtension(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("inspect request: %v", err)
	}
	if req.ParentRunID != "parent-extension-accept" || req.CurrentAllowance != 1 || req.RequestedIncrement != 1 || req.HardCeiling != 2 ||
		req.FromRole != RolePlanner || req.NextRole != RoleDeveloper || req.CheckpointRevision != stored.Revision || req.Status != "pending" {
		t.Fatalf("resource request = %#v", req)
	}
	requestedEvents := queryEventsForTest(t, env.events, request.RunID, event.EventMultiAgentResourceExtensionRequested)
	if len(requestedEvents) != 1 || requestedEvents[0].Payload["request_id"] != req.RequestID {
		t.Fatalf("resource request events = %#v", requestedEvents)
	}

	// Reconstruct the runtime over the durable store, then prove a generic
	// recovery Resume cannot bypass the pending decision.
	restarted := newDurableRuntimeForTest(t, definition, runner, reopenedStore, newMemoryRunClaimer(), env.events, options)
	if _, err := restarted.Resume(context.Background(), request.RunID); err == nil {
		t.Fatal("generic Resume bypassed a pending owner decision")
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, []Role{RolePlanner}) {
		t.Fatalf("role runner called during pending recovery: %v", got)
	}

	decision, err := restarted.DecideResourceExtension(context.Background(), request.RunID, req.RequestID, req.CheckpointRevision, "accept", "synthetic-owner", "Approved one extra role.")
	if err != nil || decision.Allowance != 2 || decision.Decision != "accept" {
		t.Fatalf("accept decision=%#v err=%v", decision, err)
	}
	replayed, err := restarted.DecideResourceExtension(context.Background(), request.RunID, req.RequestID, req.CheckpointRevision, "accept", "synthetic-owner", "Approved one extra role.")
	if err != nil || replayed != decision {
		t.Fatalf("idempotent replay=%#v err=%v, want %#v", replayed, err, decision)
	}
	resumed, resumeErr := restarted.Resume(context.Background(), request.RunID)
	var budgetErr *BudgetExceededError
	if !errors.As(resumeErr, &budgetErr) || resumed.Status != RunStatusBudgetExhausted || budgetErr.Limit != 2 || budgetErr.Used != 3 {
		t.Fatalf("post-accept state=%q err=%v, want soft allowance 2 exhausted before role 3", resumed.Status, resumeErr)
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, []Role{RolePlanner, RoleDeveloper}) {
		t.Fatalf("roles after accepted extension=%v, want planner and one developer", got)
	}
	final, err := reopenedStore.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("load resumed run: %v", err)
	}
	if final.ResourceNegotiation.Allowance != 2 || final.ResourceNegotiation.Config.HardCeiling != 2 || final.PendingTransition != nil || final.Waiting != nil {
		t.Fatalf("final resource state=%#v", final.ResourceNegotiation)
	}
}

func TestDurableResourceExtensionRejectLeavesAllowanceAndDoesNotRunNextRole(t *testing.T) {
	env := newDurableTestEnvironment(t)
	definition := validDefinition()
	definition.Budgets.MaxTransitions = Limit(2)
	planner := scriptedResult(OutcomePlanReady)
	planner.OutgoingHandoff.ResourceExtensionRequest = &ResourceExtensionIntent{Reason: "Need one more role to complete verification."}
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{RolePlanner: {{result: planner}}}}
	options := resourceNegotiationTestOptions("parent-extension-reject")
	runtime := newDurableRuntimeForTest(t, definition, runner, env.store, env.claimer, env.events, options)
	request := testRunRequest()
	request.RunID = "run-resource-extension-reject"
	if state, err := runtime.Run(context.Background(), request); err == nil || state.Status != RunStatusPaused {
		t.Fatalf("run state=%q err=%v, want pending decision", state.Status, err)
	}
	pending, err := runtime.InspectResourceExtension(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("inspect request: %v", err)
	}
	restarted := newDurableRuntimeForTest(t, definition, runner, env.store, newMemoryRunClaimer(), env.events, options)
	decision, err := restarted.DecideResourceExtension(context.Background(), request.RunID, pending.RequestID, pending.CheckpointRevision, "reject", "synthetic-owner", "Keep the original allowance.")
	if err != nil || decision.Allowance != 1 || decision.Decision != "reject" {
		t.Fatalf("reject decision=%#v err=%v", decision, err)
	}
	stored, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("load rejected run: %v", err)
	}
	if stored.State.Status != RunStatusBudgetExhausted || stored.ResourceNegotiation.Allowance != 1 || stored.PendingTransition != nil || stored.Waiting != nil {
		t.Fatalf("rejected run state=%#v resource=%#v", stored.State, stored.ResourceNegotiation)
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, []Role{RolePlanner}) {
		t.Fatalf("role runner called after rejection: %v", got)
	}
	if _, err := restarted.DecideResourceExtension(context.Background(), request.RunID, pending.RequestID, pending.CheckpointRevision, "accept", "synthetic-owner", "conflicting replay"); err == nil {
		t.Fatal("conflicting decision replay was accepted")
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, []Role{RolePlanner}) {
		t.Fatalf("role runner called by conflicting replay: %v", got)
	}
}

func TestDurableResourceExtensionRequiresAgentRequestAndConfiguredHardCeiling(t *testing.T) {
	env := newDurableTestEnvironment(t)
	definition := validDefinition()
	definition.Budgets.MaxTransitions = Limit(2)
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner: {{result: scriptedResult(OutcomePlanReady)}},
	}}
	options := resourceNegotiationTestOptions("parent-no-request")
	runtime := newDurableRuntimeForTest(t, definition, runner, env.store, env.claimer, env.events, options)
	request := testRunRequest()
	request.RunID = "run-resource-extension-no-agent-request"
	state, err := runtime.Run(context.Background(), request)
	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) || state.Status != RunStatusBudgetExhausted {
		t.Fatalf("no-request state=%q err=%v, want normal exhaustion", state.Status, err)
	}
	stored, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("load no-request run: %v", err)
	}
	if stored.ResourceNegotiation.Allowance != 1 || len(stored.ResourceNegotiation.Requests) != 0 || resourceRequestPending(stored) {
		t.Fatalf("no-agent-request resource state=%#v", stored.ResourceNegotiation)
	}
	if got := runnerCalls(runner); !reflect.DeepEqual(got, []Role{RolePlanner}) {
		t.Fatalf("roles after no-request allowance=%v, want planner only", got)
	}

	bad := options
	badConfig := *options.ResourceNegotiation
	badConfig.HardCeiling = 4
	bad.ResourceNegotiation = &badConfig
	if _, err := NewDurableRuntime(mustAdaptGraph(t, definition), runner, env.store, newMemoryRunClaimer(), env.events, bad); err == nil {
		t.Fatal("runtime accepted a hard ceiling different from the immutable compiled graph")
	}
}

func resourceNegotiationTestOptions(parentRunID string) DurableRuntimeOptions {
	return DurableRuntimeOptions{ResourceNegotiation: &ResourceNegotiationConfig{
		Enabled: true, InitialAllowance: 1, Increment: 1, HardCeiling: 2,
		OwnerID: "synthetic-owner", ParentRunID: parentRunID, AgentID: "agent-test",
		ProjectID: "project-test", Workspace: "C:/synthetic/project", GrantGeneration: 7,
	}}
}
