package multiagent

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

func TestDefaultReferenceDefinitionSafetyCeilings(t *testing.T) {
	definition := DefaultReferenceDefinition()
	if err := definition.Validate(); err != nil {
		t.Fatalf("default reference definition: %v", err)
	}
	if definition.ID != ReferenceWorkflowID || definition.Budgets.MaxTransitions != 12 {
		t.Fatalf("identity/budget = %q/%d", definition.ID, definition.Budgets.MaxTransitions)
	}
	if definition.Budgets.MaxTesterToDeveloperLoops != 2 ||
		definition.Budgets.MaxReviewerToDeveloperLoops != 1 ||
		definition.Budgets.MaxTokens != 60_000 ||
		definition.Budgets.MaxDuration != 45*time.Minute {
		t.Fatalf("safety budgets = %#v", definition.Budgets)
	}
	wantVisits := map[Role]Limit{
		RolePlanner: 1, RoleDeveloper: 4, RoleTester: 3, RoleReviewer: 2,
	}
	if !reflect.DeepEqual(definition.Budgets.MaxVisitsPerRole, wantVisits) {
		t.Fatalf("role visits = %v, want %v", definition.Budgets.MaxVisitsPerRole, wantVisits)
	}
	for _, role := range definition.Roles {
		if role.Approval.Required {
			t.Fatalf("role-level self approval must not be used: %#v", role)
		}
		if role.Role != RoleDeveloper {
			for _, toolName := range role.AllowedTools {
				if strings.Contains(toolName, "write") || strings.Contains(toolName, "create_directory") {
					t.Fatalf("read-oriented role %s received mutation tool %q", role.Role, toolName)
				}
			}
		}
	}
}

func TestReferenceOverridesOnlyNarrowAuthority(t *testing.T) {
	transitions := Limit(8)
	tokens := Limit(40_000)
	definition, err := ApplyReferenceOverrides(DefaultReferenceDefinition(), ReferenceWorkflowInput{
		Objective: "fix a bounded bug",
		Workspace: "/repo",
		RoleProfiles: map[Role]string{
			RoleDeveloper: "lumi",
		},
		Budgets: ReferenceBudgetOverrides{
			MaxTransitions: &transitions,
			MaxTokens:      &tokens,
			MaxVisitsPerRole: map[Role]Limit{
				RoleDeveloper: 3,
			},
			MaxDuration: "30m",
		},
	})
	if err != nil {
		t.Fatalf("apply narrowing overrides: %v", err)
	}
	developer, _ := definition.RoleConfig(RoleDeveloper)
	if developer.AgentRef != "lumi" || definition.Budgets.MaxTransitions != 8 ||
		definition.Budgets.MaxVisitsPerRole[RoleDeveloper] != 3 ||
		definition.Budgets.MaxDuration != 30*time.Minute {
		t.Fatalf("effective definition = %#v", definition)
	}

	widened := Limit(13)
	_, err = ApplyReferenceOverrides(DefaultReferenceDefinition(), ReferenceWorkflowInput{
		Objective: "fix a bounded bug",
		Workspace: "/repo",
		Budgets:   ReferenceBudgetOverrides{MaxTransitions: &widened},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds safety ceiling") {
		t.Fatalf("expected widening rejection, got %v", err)
	}

	_, err = ApplyReferenceOverrides(DefaultReferenceDefinition(), ReferenceWorkflowInput{
		Objective: "fix a bounded bug",
		Workspace: "/repo",
		RoleProfiles: map[Role]string{
			RoleDeveloper: "same-agent", RoleReviewer: "same-agent",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "must be distinct") {
		t.Fatalf("expected self-review rejection, got %v", err)
	}
}

func TestReferenceWorkflowDeterministicEndToEnd(t *testing.T) {
	env := newDurableTestEnvironment(t)
	withWorkspace := func(outcome TransitionOutcome) RoleRunResult {
		result := scriptedResult(outcome)
		result.Metadata.WorkspaceID = "workspace-reference"
		result.Metadata.ValidationStatus = "passed"
		return result
	}
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner: {{result: withWorkspace(OutcomePlanReady)}},
		RoleDeveloper: {
			{result: withWorkspace(OutcomeImplementationReady)},
			{result: withWorkspace(OutcomeImplementationReady)},
			{result: withWorkspace(OutcomeImplementationReady)},
		},
		RoleTester: {
			{result: withWorkspace(OutcomeTestsFailed)},
			{result: withWorkspace(OutcomeTestsPassed)},
			{result: withWorkspace(OutcomeTestsPassed)},
		},
		RoleReviewer: {
			{result: withWorkspace(OutcomeChangesRequested)},
			{result: withWorkspace(OutcomeReviewApproved)},
		},
	}}
	runtime := newDurableRuntimeForTest(
		t, DefaultReferenceDefinition(), runner, env.store, env.claimer, env.events)
	request := RunRequest{
		RunID: "run-reference-e2e",
		Task: TaskReference{
			ID: "task-reference-e2e", Description: "fix the bounded test fixture",
		},
	}
	state, err := runtime.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("run reference workflow: %v", err)
	}
	if state.Status != RunStatusCompleted || state.WorkspaceID != "workspace-reference" {
		t.Fatalf("terminal state = %#v", state)
	}
	if state.RoleStates[RoleDeveloper].Visits != 3 ||
		state.RoleStates[RoleTester].Visits != 3 ||
		state.RoleStates[RoleReviewer].Visits != 2 ||
		state.LoopTraversals.TesterToDeveloper != 1 ||
		state.LoopTraversals.ReviewerToDeveloper != 1 {
		t.Fatalf("loop accounting = roles %#v loops %#v", state.RoleStates, state.LoopTraversals)
	}

	events, err := env.events.Query(context.Background(), event.EventFilter{
		RunID: request.RunID, Limit: 500,
	})
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	input := ReferenceWorkflowInput{
		Objective: "fix the bounded test fixture", Workspace: "/repo",
	}
	report := BuildReferenceRunReport(input, state, events)
	if report.FinalStatus != RunStatusCompleted ||
		report.ReviewOutcome != OutcomeReviewApproved ||
		len(report.LoopHistory) != 2 {
		t.Fatalf("report = %#v", report)
	}
	snapshot := BuildReferenceRunSnapshot(state, events[len(events)-3:])
	if len(snapshot.CompletedRoles) != 4 || len(snapshot.RecentEvents) != 3 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	calls := runnerCalls(runner)
	resumed, err := runtime.Resume(context.Background(), request.RunID)
	if err != nil || resumed.Status != RunStatusCompleted {
		t.Fatalf("resume terminal workflow: state %#v err %v", resumed, err)
	}
	if !reflect.DeepEqual(runnerCalls(runner), calls) {
		t.Fatal("terminal resume re-executed a role")
	}
}

func TestDurableRuntimeCancelPersistsTerminalState(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runtime := newDurableRuntimeForTest(
		t, DefaultReferenceDefinition(), happyDurableRunner(), env.store, env.claimer, env.events)
	request := RunRequest{
		RunID: "run-reference-cancel",
		Task:  TaskReference{ID: "task-reference-cancel", Description: "cancel me"},
	}
	state := runtime.supervisor.newRunState(request)
	record, err := env.store.Create(context.Background(), DurableRun{
		SchemaVersion: DurableRunSchemaVersion,
		State:         state,
		Phase:         CheckpointPendingRole,
	}, nil)
	if err != nil || record.Revision != 1 {
		t.Fatalf("create pending run: record %#v err %v", record, err)
	}

	cancelled, err := runtime.Cancel(context.Background(), request.RunID, "operator requested stop")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != RunStatusCancelled ||
		cancelled.CancellationReason != "operator requested stop" {
		t.Fatalf("cancelled state = %#v", cancelled)
	}
	stored, err := runtime.Inspect(context.Background(), request.RunID)
	if err != nil || stored.Phase != CheckpointTerminal ||
		stored.State.Status != RunStatusCancelled {
		t.Fatalf("stored cancellation = %#v, err %v", stored, err)
	}
	resumed, err := runtime.Resume(context.Background(), request.RunID)
	if err != nil || resumed.Status != RunStatusCancelled {
		t.Fatalf("resume cancellation = %#v, err %v", resumed, err)
	}
}
