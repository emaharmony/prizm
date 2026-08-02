package multiagent

import (
	"context"
	"errors"
	"testing"

	"github.com/emaharmony/prism/internal/event"
)

// TestDurableRuntimeEmitsBudgetWarningOneTraversalBeforeLoopLimit exercises
// the tester->developer correction loop against validDefinition's
// MaxTesterToDeveloperLoops = 3: the warning must fire exactly once, on the
// failure that brings the loop's traversal count to max-1 (2 of 3) — never
// on the first failure (1 of 3), and never again on the failure that reaches
// the limit itself (3 of 3, still allowed) or the one that finally exceeds
// it (4 of 3, terminal). MaxRepeatedFailure is raised so that budget alone
// (not the unrelated repeated-failure budget) determines the outcome.
func TestDurableRuntimeEmitsBudgetWarningOneTraversalBeforeLoopLimit(t *testing.T) {
	env := newDurableTestEnvironment(t)
	definition := validDefinition()
	definition.Budgets.MaxRepeatedFailure = 100
	// validDefinition's per-role MaxLocalIterations (3) is sized for the
	// reference happy path, not four developer/tester visits; raise it so
	// role_max_local_iterations cannot trip before the loop budget does.
	for i := range definition.Roles {
		if definition.Roles[i].Role == RoleDeveloper || definition.Roles[i].Role == RoleTester {
			definition.Roles[i].MaxLocalIterations = 10
		}
	}

	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner: {{result: scriptedResult(OutcomePlanReady)}},
		RoleDeveloper: {
			{result: scriptedResult(OutcomeImplementationReady)},
			{result: scriptedResult(OutcomeImplementationReady)},
			{result: scriptedResult(OutcomeImplementationReady)},
			{result: scriptedResult(OutcomeImplementationReady)},
		},
		RoleTester: {
			{result: scriptedResult(OutcomeTestsFailed)},
			{result: scriptedResult(OutcomeTestsFailed)},
			{result: scriptedResult(OutcomeTestsFailed)},
			{result: scriptedResult(OutcomeTestsFailed)},
		},
	}}
	runtime := newDurableRuntimeForTest(
		t, definition, runner, env.store, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-loop-warning"

	state, err := runtime.Run(context.Background(), request)
	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected loop budget exhaustion, got %v", err)
	}
	if budgetErr.Budget != "max_tester_to_developer_loops" {
		t.Fatalf("exhaustion budget = %q, want max_tester_to_developer_loops", budgetErr.Budget)
	}
	if state.Status != RunStatusBudgetExhausted {
		t.Fatalf("terminal status = %q", state.Status)
	}
	if got := state.LoopTraversals.Get(LoopTesterToDeveloper); got != 3 {
		t.Fatalf("recorded loop traversals = %d, want 3 (the 4th failure never applies its transition)", got)
	}

	warnings := queryEventsForTest(
		t, env.events, request.RunID, event.EventMultiAgentBudgetWarning)
	if len(warnings) != 1 {
		t.Fatalf("budget warning events = %d, want exactly 1", len(warnings))
	}
	warning := warnings[0]
	if got := warning.Payload["budget"]; got != "tester_to_developer_loop" {
		t.Fatalf("warning budget = %v, want tester_to_developer_loop", got)
	}
	if got := asInt(t, warning.Payload["used"]); got != 2 {
		t.Fatalf("warning used = %v, want 2 (one before the limit of 3)", got)
	}
	if got := asInt(t, warning.Payload["limit"]); got != 3 {
		t.Fatalf("warning limit = %v, want 3", got)
	}
	if got := warning.Payload["role"]; got != string(RoleTester) {
		t.Fatalf("warning role = %v, want tester", got)
	}

	exhausted := queryEventsForTest(
		t, env.events, request.RunID, event.EventMultiAgentBudgetExhausted)
	if len(exhausted) != 1 {
		t.Fatalf("budget exhausted events = %d, want exactly 1", len(exhausted))
	}
	if warning.ID == exhausted[0].ID {
		t.Fatal("warning and exhaustion must be distinct events")
	}

	// The warning must be recorded strictly before the exhaustion event in
	// the run's canonical event history.
	all, err := env.events.Query(context.Background(), event.EventFilter{RunID: request.RunID, Limit: 1000})
	if err != nil {
		t.Fatalf("query all events: %v", err)
	}
	warnIndex, exhaustIndex := -1, -1
	for i, evt := range all {
		if evt.ID == warning.ID {
			warnIndex = i
		}
		if evt.ID == exhausted[0].ID {
			exhaustIndex = i
		}
	}
	if warnIndex == -1 || exhaustIndex == -1 || warnIndex >= exhaustIndex {
		t.Fatalf("warning (index %d) must precede exhaustion (index %d)", warnIndex, exhaustIndex)
	}
}

func asInt(t *testing.T, v any) int {
	t.Helper()
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		t.Fatalf("unexpected numeric payload type %T (%v)", v, v)
		return 0
	}
}
