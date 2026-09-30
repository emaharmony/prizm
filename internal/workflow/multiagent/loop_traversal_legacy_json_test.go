package multiagent

import (
	"encoding/json"
	"testing"
)

// TestLoopTraversalCountsLegacyJSONMigratesToCompiledGraphLoopKeys is the
// single most important new test in PR4 (Phase 3 supervisor integration). It
// proves, end-to-end, that a pre-Phase-3 persisted LoopTraversalCounts blob
// ({"tester_to_developer": N, "reviewer_to_developer": M} — the only shape
// that has ever existed on disk before this change) decodes into Counts
// entries keyed by the SAME edgeID-based LoopKind values
// CompiledGraph.LoopFor produces for a resumed run's transitions, via
// CompatAdaptDefinition (the path every historical Phase 1 run resumes
// through).
//
// If this test fails, resuming any historical multi-agent run silently
// resets its correction-loop budget counters to zero: Counts.Get(loop.Kind)
// would look up a key the legacy-shape decode never populated, defeating the
// entire point of the loop budget check without producing any error.
func TestLoopTraversalCountsLegacyJSONMigratesToCompiledGraphLoopKeys(t *testing.T) {
	legacyJSON := []byte(`{"tester_to_developer": 2, "reviewer_to_developer": 1}`)

	var counts LoopTraversalCounts
	if err := json.Unmarshal(legacyJSON, &counts); err != nil {
		t.Fatalf("unmarshal legacy loop traversal counts: %v", err)
	}
	if got := counts.Get(LoopTesterToDeveloper); got != 2 {
		t.Fatalf("migrated tester count = %d, want 2", got)
	}
	if got := counts.Get(LoopReviewerToDeveloper); got != 1 {
		t.Fatalf("migrated reviewer count = %d, want 1", got)
	}

	definition := DefaultReferenceDefinition()
	graph, err := CompatAdaptDefinition(definition)
	if err != nil {
		t.Fatalf("adapt definition: %v", err)
	}

	// The core assertion: build a ResolvedTransition matching the
	// tester->developer edge, ask LoopFor for its CompiledLoop, and confirm
	// Counts.Get(loop.Kind) — using the SAME key LoopFor just produced, not a
	// key this test invents independently — returns the migrated historical
	// count.
	testerTransition, err := graph.Resolve(RoleTester, OutcomeTestsFailed)
	if err != nil {
		t.Fatalf("resolve tester loop transition: %v", err)
	}
	testerLoop, ok := graph.LoopFor(testerTransition)
	if !ok {
		t.Fatal("expected tester->developer loop to be present on the compiled graph")
	}
	if got := counts.Get(testerLoop.Kind); got != 2 {
		t.Fatalf("Counts.Get(loop.Kind) = %d, want 2 (the migrated historical count); loop.Kind=%q",
			got, testerLoop.Kind)
	}

	reviewerTransition, err := graph.Resolve(RoleReviewer, OutcomeChangesRequested)
	if err != nil {
		t.Fatalf("resolve reviewer loop transition: %v", err)
	}
	reviewerLoop, ok := graph.LoopFor(reviewerTransition)
	if !ok {
		t.Fatal("expected reviewer->developer loop to be present on the compiled graph")
	}
	if got := counts.Get(reviewerLoop.Kind); got != 1 {
		t.Fatalf("Counts.Get(loop.Kind) = %d, want 1 (the migrated historical count); loop.Kind=%q",
			got, reviewerLoop.Kind)
	}

	// Round trip: re-marshaling migrated Counts must use the new {"counts":
	// {...}} shape, and decoding that back must reproduce the same values
	// (the migration is idempotent — resuming a run twice must not lose or
	// duplicate counts).
	reencoded, err := json.Marshal(counts)
	if err != nil {
		t.Fatalf("marshal migrated counts: %v", err)
	}
	var roundTripped LoopTraversalCounts
	if err := json.Unmarshal(reencoded, &roundTripped); err != nil {
		t.Fatalf("unmarshal re-encoded counts: %v", err)
	}
	if roundTripped.Get(LoopTesterToDeveloper) != 2 || roundTripped.Get(LoopReviewerToDeveloper) != 1 {
		t.Fatalf("round-tripped counts = %#v, want tester=2 reviewer=1", roundTripped)
	}

	// A full historical RunState-shaped JSON blob (embedding the old
	// loop_traversals shape, exactly as it appears inside a persisted
	// DurableRun record) must also decode correctly end-to-end, proving the
	// migration works through the real embedding path, not just in
	// isolation.
	historicalStateJSON := []byte(`{
		"schema_version": 1,
		"run_id": "run-historical",
		"workflow_id": "multi-agent-software-task",
		"workflow_version": 1,
		"current_role": "developer",
		"current_task": {"id": "task-historical"},
		"status": "running",
		"role_states": {},
		"transition_count": 3,
		"loop_traversals": {"tester_to_developer": 2, "reviewer_to_developer": 1},
		"budget_usage": {"tokens": {}, "total_local_iterations": 0, "retries": 0, "repeated_failures": 0, "elapsed": 0},
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:01Z"
	}`)
	var historicalState RunState
	if err := json.Unmarshal(historicalStateJSON, &historicalState); err != nil {
		t.Fatalf("unmarshal historical run state: %v", err)
	}
	if got := historicalState.LoopTraversals.Get(testerLoop.Kind); got != 2 {
		t.Fatalf("historical state migrated tester count = %d, want 2", got)
	}
	if got := historicalState.LoopTraversals.Get(reviewerLoop.Kind); got != 1 {
		t.Fatalf("historical state migrated reviewer count = %d, want 1", got)
	}
}
