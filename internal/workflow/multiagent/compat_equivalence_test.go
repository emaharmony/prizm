package multiagent

import (
	"errors"
	"testing"
)

// TestCompatAdaptDefinitionBehavioralEquivalence asserts that
// CompatAdaptDefinition's CompiledGraph produces the same Resolve()/LoopFor()
// results the deleted TransitionResolver/hardcoded correctionLoop path did,
// for every valid (role, outcome) pair the Phase 1 reference flow declares.
// Per the Phase 3 plan, equivalence here means BEHAVIORAL agreement, not a
// byte-identical fingerprint with a YAML-compiled graph: compat.go's compat
// path and compiler.go's authored-YAML path have no principled reason to
// serialize identically.
func TestCompatAdaptDefinitionBehavioralEquivalence(t *testing.T) {
	definition := DefaultReferenceDefinition()
	graph, err := CompatAdaptDefinition(definition)
	if err != nil {
		t.Fatalf("adapt definition: %v", err)
	}

	// Every declared transition must resolve to exactly the destination
	// role/terminal condition the legacy Definition declares.
	for _, rule := range definition.Transitions {
		got, err := graph.Resolve(rule.From, rule.Outcome)
		if err != nil {
			t.Fatalf("resolve(%s, %s): %v", rule.From, rule.Outcome, err)
		}
		if got.To != rule.To || got.Terminal != rule.Terminal {
			t.Fatalf("resolve(%s, %s) = %#v, want to=%q terminal=%q",
				rule.From, rule.Outcome, got, rule.To, rule.Terminal)
		}
	}

	// The two Phase 1 loop edges must be reachable via LoopFor with the
	// correct, budget-derived MaxTraversals and fail-on-exhaustion policy.
	testerTransition, err := graph.Resolve(RoleTester, OutcomeTestsFailed)
	if err != nil {
		t.Fatalf("resolve tester loop: %v", err)
	}
	testerLoop, ok := graph.LoopFor(testerTransition)
	if !ok {
		t.Fatal("expected tester->developer loop to be present")
	}
	if testerLoop.Kind != LoopTesterToDeveloper {
		t.Fatalf("tester loop kind = %q, want %q", testerLoop.Kind, LoopTesterToDeveloper)
	}
	if testerLoop.MaxTraversals != definition.Budgets.MaxTesterToDeveloperLoops {
		t.Fatalf("tester loop max traversals = %d, want %d",
			testerLoop.MaxTraversals, definition.Budgets.MaxTesterToDeveloperLoops)
	}
	if testerLoop.OnExhausted != LoopExhaustedFail {
		t.Fatalf("tester loop on_exhausted = %q, want %q", testerLoop.OnExhausted, LoopExhaustedFail)
	}

	reviewerTransition, err := graph.Resolve(RoleReviewer, OutcomeChangesRequested)
	if err != nil {
		t.Fatalf("resolve reviewer loop: %v", err)
	}
	reviewerLoop, ok := graph.LoopFor(reviewerTransition)
	if !ok {
		t.Fatal("expected reviewer->developer loop to be present")
	}
	if reviewerLoop.Kind != LoopReviewerToDeveloper {
		t.Fatalf("reviewer loop kind = %q, want %q", reviewerLoop.Kind, LoopReviewerToDeveloper)
	}
	if reviewerLoop.MaxTraversals != definition.Budgets.MaxReviewerToDeveloperLoops {
		t.Fatalf("reviewer loop max traversals = %d, want %d",
			reviewerLoop.MaxTraversals, definition.Budgets.MaxReviewerToDeveloperLoops)
	}
	if reviewerLoop.OnExhausted != LoopExhaustedFail {
		t.Fatalf("reviewer loop on_exhausted = %q, want %q", reviewerLoop.OnExhausted, LoopExhaustedFail)
	}

	// A non-loop transition must never be misreported as a loop.
	plannerTransition, err := graph.Resolve(RolePlanner, OutcomePlanReady)
	if err != nil {
		t.Fatalf("resolve planner transition: %v", err)
	}
	if _, ok := graph.LoopFor(plannerTransition); ok {
		t.Fatal("planner->developer must not be reported as a loop")
	}

	// Role/outcome vocabulary must agree with the legacy Definition's.
	for _, role := range Phase1Roles() {
		if !graph.HasRole(role) {
			t.Errorf("HasRole(%s) = false, want true", role)
		}
	}
	if graph.HasRole(Role("not-a-role")) {
		t.Error("HasRole(not-a-role) = true, want false")
	}
	if !graph.OutcomeValidFor(RoleTester, OutcomeTestsFailed) {
		t.Error("OutcomeValidFor(tester, tests_failed) = false, want true")
	}
	if graph.OutcomeValidFor(RolePlanner, OutcomeTestsFailed) {
		t.Error("OutcomeValidFor(planner, tests_failed) = true, want false")
	}

	// Identity/versioning fields.
	if graph.WorkflowID() != definition.ID {
		t.Errorf("WorkflowID() = %q, want %q", graph.WorkflowID(), definition.ID)
	}
	if graph.RegistryVersion() != definition.Version {
		t.Errorf("RegistryVersion() = %d, want %d", graph.RegistryVersion(), definition.Version)
	}
	if graph.Fingerprint() == "" {
		t.Error("Fingerprint() is empty, want a non-empty deterministic fingerprint")
	}

	// An invalid (role, outcome) combination must fail the same way it did
	// through the deleted TransitionResolver.
	_, resolveErr := graph.Resolve(RolePlanner, OutcomeTestsPassed)
	var transitionErr *InvalidTransitionError
	if !errors.As(resolveErr, &transitionErr) {
		t.Errorf("expected *InvalidTransitionError, got %T: %v", resolveErr, resolveErr)
	}
}
