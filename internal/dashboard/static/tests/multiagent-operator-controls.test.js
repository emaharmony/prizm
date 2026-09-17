// Plain-node test for Milestone 9 Part B of the multi-agent run page:
// operator control-bar gating (internal/dashboard/static/multiagent-
// operator.js's computeOperatorControlsState), the loop-budget stall/
// near-exhaustion pure helpers (budgetWarningEdgeId, remainingAttemptsText,
// deriveNearExhaustedEdges, announceDeltas), the WaitingState kind label
// helper, and the new multiagent-live.js `edgeNearExhausted` narrow patch
// for the two known tester/reviewer correction-loop budget warnings.
//
// No package.json, no npm, no build step: run directly with
//   node internal/dashboard/static/tests/multiagent-operator-controls.test.js
// Exits non-zero (via an uncaught assertion error) on any failure.

const assert = require('assert');
const path = require('path');
const op = require(path.join(__dirname, '..', 'multiagent-operator.js'));
const live = require(path.join(__dirname, '..', 'multiagent-live.js'));

let failures = 0;
function test(name, fn) {
  try {
    fn();
    console.log('ok - ' + name);
  } catch (err) {
    failures++;
    console.error('FAIL - ' + name);
    console.error((err && err.stack) || err);
  }
}

// ===========================================================================
// computeOperatorControlsState: pure function of (snapshot.status,
// replayActive, runUnavailable). Mirrors internal/workflow/multiagent/
// types.go's RunStatus vocabulary exactly (created/running/paused/
// completed/failed/cancelled/budget_exhausted).
// ===========================================================================

test('running, not replay, not unavailable: Pause visible+enabled; Resume and Cancel differ (Cancel visible for any non-terminal run)', () => {
  const state = op.computeOperatorControlsState({ status: 'running' }, false, false);
  assert.strictEqual(state.pause.visible, true);
  assert.strictEqual(state.pause.enabled, true);
  assert.strictEqual(state.resume.visible, false);
  assert.strictEqual(state.cancel.visible, true);
  assert.strictEqual(state.cancel.enabled, true);
  assert.strictEqual(state.blockedReason, null);
});

test('paused: Resume visible+enabled, Pause hidden, Cancel still visible+enabled (non-terminal)', () => {
  const state = op.computeOperatorControlsState({ status: 'paused' }, false, false);
  assert.strictEqual(state.pause.visible, false);
  assert.strictEqual(state.resume.visible, true);
  assert.strictEqual(state.resume.enabled, true);
  assert.strictEqual(state.cancel.visible, true);
  assert.strictEqual(state.cancel.enabled, true);
});

test('paused with an operator_pause wait: Resume is still gated purely by status, not by waiting.kind (DurableRuntime.Resume handles every wait kind uniformly)', () => {
  const state = op.computeOperatorControlsState({ status: 'paused', waiting: { kind: 'operator_pause' } }, false, false);
  assert.strictEqual(state.resume.visible, true);
  assert.strictEqual(state.resume.enabled, true);
});

test('paused with an approval wait: Resume is still offered (same uniform gating)', () => {
  const state = op.computeOperatorControlsState({ status: 'paused', waiting: { kind: 'approval' } }, false, false);
  assert.strictEqual(state.resume.visible, true);
});

test('created: not running, not paused -- Pause and Resume both hidden, Cancel visible (non-terminal)', () => {
  const state = op.computeOperatorControlsState({ status: 'created' }, false, false);
  assert.strictEqual(state.pause.visible, false);
  assert.strictEqual(state.resume.visible, false);
  assert.strictEqual(state.cancel.visible, true);
});

['completed', 'failed', 'cancelled', 'budget_exhausted'].forEach(status => {
  test(`terminal status "${status}": all three controls hidden`, () => {
    const state = op.computeOperatorControlsState({ status }, false, false);
    assert.strictEqual(state.pause.visible, false);
    assert.strictEqual(state.resume.visible, false);
    assert.strictEqual(state.cancel.visible, false);
  });
});

test('replay active: a visible button (Cancel, running status) stays visible but becomes disabled with a replay-specific reason', () => {
  const state = op.computeOperatorControlsState({ status: 'running' }, true, false);
  assert.strictEqual(state.pause.visible, true);
  assert.strictEqual(state.pause.enabled, false);
  assert.ok(/replay/i.test(state.pause.disabledReason || ''));
  assert.strictEqual(state.cancel.visible, true);
  assert.strictEqual(state.cancel.enabled, false);
  assert.strictEqual(state.blockedReason, state.pause.disabledReason);
});

test('run unavailable: every visible button is disabled with an unavailable-specific reason', () => {
  const state = op.computeOperatorControlsState({ status: 'paused' }, false, true);
  assert.strictEqual(state.resume.visible, true);
  assert.strictEqual(state.resume.enabled, false);
  assert.ok(/no longer reachable/i.test(state.resume.disabledReason || ''));
});

test('a button that is not visible for this status has no disabledReason leaking a stale block message when nothing is blocked', () => {
  const state = op.computeOperatorControlsState({ status: 'completed' }, false, false);
  assert.strictEqual(state.pause.disabledReason, null);
});

test('a null/empty snapshot is treated as an unrecognized status: nothing running/paused, only Cancel-for-non-terminal logic could apply, but "" is not terminal so Cancel would show -- verify no throw and sane defaults', () => {
  assert.doesNotThrow(() => op.computeOperatorControlsState(null, false, false));
  const state = op.computeOperatorControlsState(null, false, false);
  assert.strictEqual(state.pause.visible, false);
  assert.strictEqual(state.resume.visible, false);
});

// ===========================================================================
// waitingKindLabel
// ===========================================================================

test('waitingKindLabel: known kinds get a purpose-built label', () => {
  assert.strictEqual(op.waitingKindLabel('operator_pause'), 'Paused by operator');
  assert.strictEqual(op.waitingKindLabel('approval'), 'Waiting on approval');
  assert.strictEqual(op.waitingKindLabel('execution_reconciliation'), 'Reconciling execution state');
});

test('waitingKindLabel: an unrecognized future kind still renders something sensible instead of crashing or showing nothing', () => {
  assert.strictEqual(op.waitingKindLabel('some_future_kind'), 'some future kind');
  assert.strictEqual(op.waitingKindLabel(''), 'Unknown');
  assert.strictEqual(op.waitingKindLabel(null), 'Unknown');
});

// ===========================================================================
// Loop-budget stall / near-exhaustion pure helpers
// ===========================================================================

test('budgetWarningEdgeId: the two known loop budget values map to the correct, real edge ids (verified against multiagent-live.js edgeIdFor)', () => {
  assert.strictEqual(op.budgetWarningEdgeId({ budget: 'tester_to_developer_loop' }), 'edge:tester:tests_failed');
  assert.strictEqual(op.budgetWarningEdgeId({ budget: 'reviewer_to_developer_loop' }), 'edge:reviewer:changes_requested');
});

test('budgetWarningEdgeId: any other budget value (role visits, local iterations, ...) maps to null -- never invented', () => {
  assert.strictEqual(op.budgetWarningEdgeId({ budget: 'role_max_local_iterations' }), null);
  assert.strictEqual(op.budgetWarningEdgeId({ budget: 'max_visits_per_role' }), null);
  assert.strictEqual(op.budgetWarningEdgeId({}), null);
  assert.strictEqual(op.budgetWarningEdgeId(undefined), null);
});

test('remainingCount: always limit - used, floored at 0, null when either input is not a number', () => {
  assert.strictEqual(op.remainingCount(3, 4), 1);
  assert.strictEqual(op.remainingCount(4, 4), 0);
  assert.strictEqual(op.remainingCount(5, 4), 0, 'never negative');
  assert.strictEqual(op.remainingCount(3, null), null);
  assert.strictEqual(op.remainingCount(undefined, 4), null);
});

test('remainingAttemptsText: exact spec wording, singular vs. plural, derived not hardcoded', () => {
  assert.strictEqual(op.remainingAttemptsText(1), '1 attempt remaining');
  assert.strictEqual(op.remainingAttemptsText(0), '0 attempts remaining');
  assert.strictEqual(op.remainingAttemptsText(3), '3 attempts remaining');
  assert.strictEqual(op.remainingAttemptsText(null), '');
});

test('deriveNearExhaustedEdges: trusts only the server-computed level field, never a locally re-derived threshold', () => {
  const map = op.deriveNearExhaustedEdges({
    tester_to_developer: { used: 3, limit: 4, level: 'approaching' },
    reviewer_to_developer: { used: 2, limit: 5, level: 'normal' }
  });
  assert.deepStrictEqual(map, { 'edge:tester:tests_failed': 1 });
});

test('deriveNearExhaustedEdges: a loop already at "exhausted" level is excluded -- that has its own pre-existing .ma-edge--exhausted treatment already', () => {
  const map = op.deriveNearExhaustedEdges({
    tester_to_developer: { used: 4, limit: 4, level: 'exhausted' }
  });
  assert.deepStrictEqual(map, {});
});

test('deriveNearExhaustedEdges: "critical" level is also treated as near-exhausted, not just "approaching"', () => {
  const map = op.deriveNearExhaustedEdges({
    reviewer_to_developer: { used: 4, limit: 5, level: 'critical' }
  });
  assert.deepStrictEqual(map, { 'edge:reviewer:changes_requested': 1 });
});

test('deriveNearExhaustedEdges: empty/missing input never throws', () => {
  assert.deepStrictEqual(op.deriveNearExhaustedEdges(undefined), {});
  assert.deepStrictEqual(op.deriveNearExhaustedEdges({}), {});
});

// ===========================================================================
// aria-live announcement dedup (createAnnounceTracker / announceDeltas)
// ===========================================================================

test('announceDeltas: a brand-new near-exhausted edge is reported once', () => {
  const tracker = op.createAnnounceTracker();
  const changes = op.announceDeltas(tracker, { 'edge:tester:tests_failed': 1 });
  assert.deepStrictEqual(changes, [{ edgeId: 'edge:tester:tests_failed', remaining: 1 }]);
});

test('announceDeltas: an unchanged remaining count on a later tick is NOT re-reported (no spam)', () => {
  const tracker = op.createAnnounceTracker();
  op.announceDeltas(tracker, { 'edge:tester:tests_failed': 1 });
  const second = op.announceDeltas(tracker, { 'edge:tester:tests_failed': 1 });
  assert.deepStrictEqual(second, []);
});

test('announceDeltas: a count that actually changes (e.g. one more traversal consumed) IS re-reported', () => {
  const tracker = op.createAnnounceTracker();
  op.announceDeltas(tracker, { 'edge:tester:tests_failed': 1 });
  const second = op.announceDeltas(tracker, { 'edge:tester:tests_failed': 0 });
  assert.deepStrictEqual(second, [{ edgeId: 'edge:tester:tests_failed', remaining: 0 }]);
});

test('announceDeltas: an edge that drops out of the near-exhausted set is forgotten, so a later re-entry announces again', () => {
  const tracker = op.createAnnounceTracker();
  op.announceDeltas(tracker, { 'edge:tester:tests_failed': 1 });
  op.announceDeltas(tracker, {}); // edge cleared (e.g. reconciliation shows level back to normal)
  const third = op.announceDeltas(tracker, { 'edge:tester:tests_failed': 1 });
  assert.deepStrictEqual(third, [{ edgeId: 'edge:tester:tests_failed', remaining: 1 }]);
});

test('announceDeltas: two independent edges near-exhausted at once are both reported, in one call', () => {
  const tracker = op.createAnnounceTracker();
  const changes = op.announceDeltas(tracker, {
    'edge:tester:tests_failed': 1,
    'edge:reviewer:changes_requested': 2
  });
  assert.strictEqual(changes.length, 2);
});

// ===========================================================================
// multiagent-live.js: the new `edgeNearExhausted` narrow patch, additive to
// the BUDGET_WARNING case (previously always an empty patch list).
// ===========================================================================

test('derivePatches(BUDGET_WARNING): tester_to_developer_loop produces an edgeNearExhausted patch with the real edge id and a used/limit-derived remaining count', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.BUDGET_WARNING, {
    run_id: 'run-1', workflow_id: 'reference-workflow', budget: 'tester_to_developer_loop', used: 3, limit: 4
  }, {});
  assert.deepStrictEqual(patches, [{
    kind: 'edgeNearExhausted', edgeId: 'edge:tester:tests_failed', used: 3, limit: 4, remaining: 1
  }]);
});

test('derivePatches(BUDGET_WARNING): reviewer_to_developer_loop maps to the reviewer edge', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.BUDGET_WARNING, {
    run_id: 'run-1', workflow_id: 'reference-workflow', budget: 'reviewer_to_developer_loop', used: 4, limit: 5
  }, {});
  assert.deepStrictEqual(patches, [{
    kind: 'edgeNearExhausted', edgeId: 'edge:reviewer:changes_requested', used: 4, limit: 5, remaining: 1
  }]);
});

test('derivePatches(BUDGET_WARNING): an unmapped budget dimension is still an empty patch list (Milestone 4 behavior preserved verbatim)', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.BUDGET_WARNING, {
    run_id: 'run-1', workflow_id: 'reference-workflow', budget: 'role_max_local_iterations', used: 3, limit: 4
  }, {});
  assert.deepStrictEqual(patches, []);
});

test('derivePatches(BUDGET_WARNING): a known loop budget key without numeric used/limit produces no patch (never fabricate a remaining count)', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.BUDGET_WARNING, {
    run_id: 'run-1', workflow_id: 'reference-workflow', budget: 'tester_to_developer_loop'
  }, {});
  assert.deepStrictEqual(patches, []);
});

if (failures > 0) {
  console.error(`\n${failures} test(s) failed.`);
  process.exit(1);
} else {
  console.log('\nAll tests passed.');
}
