// Plain-node test for the Milestone 7 hardening/audit pass over the
// multi-agent run page's resilience scenarios: browser refresh, connection
// loss/reconnect, deleted/unavailable runs, unsupported workflow versions,
// and the debounced-reconciliation safety net. No package.json, no npm, no
// build step: run directly with
//   node internal/dashboard/static/tests/multiagent-resilience.test.js
// Exits non-zero (via an uncaught assertion error) on any failure.
//
// This file deliberately does not re-test what multiagent-live.test.js,
// multiagent-graph.test.js, and multiagent-inspector.test.js already cover
// (dedup/cursor ordering, per-event-type patch derivation, id builders,
// budget/graph view models, inspector field extraction) -- see the
// Milestone 7 report for the full correctness-matrix mapping. This file
// covers the scenarios that needed either new pure logic (extracted here so
// it's actually unit-testable) or a fresh fixture-driven check of existing
// logic that had no prior regression test.

const assert = require('assert');
const path = require('path');
const gm = require(path.join(__dirname, '..', 'multiagent-graph.js'));
const live = require(path.join(__dirname, '..', 'multiagent-live.js'));
const insp = require(path.join(__dirname, '..', 'multiagent-inspector.js'));

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
// Matrix item 2: browser refresh mid-run -- no leftover state to reset.
// ===========================================================================
// The page's live-reducer state (streamState, lastSnapshot, selection,
// allEvents...) all live as plain `let`/`const` variables inside
// multiagent-run.html's top-level IIFE, and a real browser refresh reloads
// that whole script from scratch -- so by construction there is nothing to
// explicitly reset. What IS worth verifying directly: createStreamState()
// (the one piece of stream bookkeeping state connect() rebuilds on every
// load()/reconnect) never accidentally shares mutable state (e.g. a single
// module-level Set) across separate calls, which would be exactly the kind
// of leftover-state bug a refresh-without-reset would produce.

test('createStreamState: two independent calls never share the same Set instance (no leftover state across a fresh load())', () => {
  const a = live.createStreamState();
  const b = live.createStreamState();
  assert.notStrictEqual(a.seen, b.seen, 'each stream state must own its own Set');
  live.markApplied(a, '01HXAMPLE0000000000000001');
  assert.strictEqual(b.seen.has('01HXAMPLE0000000000000001'), false, 'marking one stream state applied must not leak into a sibling state');
  assert.strictEqual(b.cursor, '', 'a fresh stream state must start with an empty cursor regardless of any other state\'s cursor');
});

test('grep guard: multiagent-run.html and its pure modules never reference sessionStorage/localStorage (a refresh must reset entirely via page reload, not persisted state)', () => {
  const fs = require('fs');
  const files = ['multiagent-run.html', 'multiagent-graph.js', 'multiagent-live.js', 'multiagent-inspector.js', 'multiagent-replay.js'];
  files.forEach(name => {
    const contents = fs.readFileSync(path.join(__dirname, '..', name), 'utf8');
    assert.ok(!/sessionStorage|localStorage/.test(contents), `${name} must not persist multi-agent run state across a refresh`);
  });
});

// ===========================================================================
// Matrix item 3: onopen fires on first connect too -- must not double-
// reconcile on startup, but must reconcile on every real reconnect.
// ===========================================================================

test('shouldReconcileOnOpen: the very first open (hasOpenedOnce=false) does not trigger a reconcile', () => {
  assert.strictEqual(live.shouldReconcileOnOpen(false), false);
});

test('shouldReconcileOnOpen: every subsequent open (hasOpenedOnce=true, i.e. a real reconnect) triggers a reconcile', () => {
  assert.strictEqual(live.shouldReconcileOnOpen(true), true);
});

test('shouldReconcileOnOpen: simulated connect() sequence -- first open skips, second (reconnect) fires exactly once', () => {
  let hasOpenedOnce = false;
  let reconcileCalls = 0;
  function onOpen() {
    if (live.shouldReconcileOnOpen(hasOpenedOnce)) reconcileCalls++;
    hasOpenedOnce = true;
  }
  onOpen(); // first connect, right after load()'s own fresh fetch
  assert.strictEqual(reconcileCalls, 0, 'first open must not cause a redundant reconcile fetch on top of load()\'s own snapshot fetch');
  onOpen(); // a reconnect
  assert.strictEqual(reconcileCalls, 1);
  onOpen(); // another reconnect
  assert.strictEqual(reconcileCalls, 2);
});

// ===========================================================================
// Matrix item 6: stale snapshot -- the debounced full-snapshot reconciliation
// must be a genuine safety net, i.e. every inspector/view-model builder is a
// pure function of the CURRENT snapshot alone, never of prior patch history,
// so a fresh reconcile() always fully corrects any drift regardless of what
// narrow live patches did before it.
// ===========================================================================

test('buildGraphViewModel: two calls with different graphs never leak state between them (a corrected snapshot always fully overrides a stale one)', () => {
  const drifted = {
    workflow_id: 'multi-agent-software-task', workflow_version: 1,
    nodes: [{ id: 'node:developer', kind: 'role', role: 'developer', label: 'Developer', status: 'failed', is_current: true, visits: 9, local_iterations: 9 }],
    edges: []
  };
  const corrected = {
    workflow_id: 'multi-agent-software-task', workflow_version: 1,
    nodes: [{ id: 'node:developer', kind: 'role', role: 'developer', label: 'Developer', status: 'running', is_current: true, visits: 2, local_iterations: 1 }],
    edges: []
  };
  gm.buildGraphViewModel(drifted); // simulates a wrong intermediate state from a narrow live patch
  const vm = gm.buildGraphViewModel(corrected); // simulates the debounced reconciliation fetch
  const dev = vm.nodes.find(n => n.id === 'node:developer');
  assert.strictEqual(dev.status, 'running', 'reconciliation must fully replace stale/wrong status, not blend with it');
  assert.strictEqual(dev.visits, 2);
});

test('buildRunInspectorData / buildNodeInspectorData: outputs depend only on the snapshot argument, never on a previous call (no hidden accumulator)', () => {
  const snapshotA = { run_id: 'run-a', status: 'running', graph: { nodes: [{ id: 'node:developer', kind: 'role', role: 'developer', label: 'Developer', status: 'failed', visits: 9 }] } };
  const snapshotB = { run_id: 'run-b', status: 'completed', graph: { nodes: [{ id: 'node:developer', kind: 'role', role: 'developer', label: 'Developer', status: 'completed', visits: 2 }] } };
  insp.buildRunInspectorData(snapshotA, Date.now());
  insp.buildNodeInspectorData(snapshotA, 'node:developer');
  const runData = insp.buildRunInspectorData(snapshotB, Date.now());
  const nodeData = insp.buildNodeInspectorData(snapshotB, 'node:developer');
  assert.strictEqual(runData.runId, 'run-b');
  assert.strictEqual(runData.status, 'completed');
  assert.strictEqual(nodeData.status, 'completed');
  assert.strictEqual(nodeData.visits, 2);
});

// ===========================================================================
// Matrix item 9: waiting-approval run renders correctly in the Run Inspector.
// ===========================================================================

test('buildRunInspectorData: a populated waiting field (approval-blocked run) is surfaced verbatim, not omitted or reinterpreted', () => {
  const snapshot = {
    run_id: 'run-waiting-1', status: 'waiting_approval', workflow_id: 'multi-agent-software-task',
    waiting: { kind: 'approval', reason: 'reviewer approval required before merge', safe_to_retry: true },
    graph: { nodes: [], edges: [] }, budgets: {}
  };
  const data = insp.buildRunInspectorData(snapshot, Date.now());
  assert.deepStrictEqual(data.waiting, { kind: 'approval', reason: 'reviewer approval required before merge', safe_to_retry: true });
});

test('buildRunInspectorData: waiting is null (not fabricated) when the snapshot carries no waiting state', () => {
  const data = insp.buildRunInspectorData({ run_id: 'run-1', status: 'running', graph: { nodes: [], edges: [] }, budgets: {} }, Date.now());
  assert.strictEqual(data.waiting, null);
});

// ===========================================================================
// Matrix item 8: paused run -- overlay renders and the graph/budget view
// models make no assumption that a run is always progressing.
// ===========================================================================

test('buildGraphViewModel: run_paused / budget_exhausted overlays are restated verbatim as booleans, independent of node/edge state', () => {
  const paused = gm.buildGraphViewModel({ workflow_id: 'multi-agent-software-task', run_paused: true, budget_exhausted: false, nodes: [], edges: [] });
  assert.strictEqual(paused.runPaused, true);
  assert.strictEqual(paused.budgetExhausted, false);

  const exhausted = gm.buildGraphViewModel({ workflow_id: 'multi-agent-software-task', run_paused: false, budget_exhausted: true, nodes: [], edges: [] });
  assert.strictEqual(exhausted.runPaused, false);
  assert.strictEqual(exhausted.budgetExhausted, true);
});

test('derivePatches: run.paused / run.resumed never assume a specific prior status -- both just restate the payload\'s own status and toggle the overlay', () => {
  const pausedPatches = live.derivePatches(live.EVENT_TYPES.RUN_PAUSED, { status: 'paused' }, {});
  const resumedPatches = live.derivePatches(live.EVENT_TYPES.RUN_RESUMED, { status: 'running' }, {});
  assert.deepStrictEqual(pausedPatches, [{ kind: 'runStatus', status: 'paused' }, { kind: 'overlay', overlay: 'paused', active: true }]);
  assert.deepStrictEqual(resumedPatches, [{ kind: 'runStatus', status: 'running' }, { kind: 'overlay', overlay: 'paused', active: false }]);
});

// ===========================================================================
// Matrix item 11: deleted/unavailable run -- distinguishing a single
// transient failure from a genuinely-gone run, without retrying forever.
// ===========================================================================

test('isNotFoundErrorMessage: matches app.js\'s request() error message format exactly ("... (404)")', () => {
  assert.strictEqual(live.isNotFoundErrorMessage('run not found (404)'), true);
  assert.strictEqual(live.isNotFoundErrorMessage('Internal Server Error (500)'), false);
  assert.strictEqual(live.isNotFoundErrorMessage('Service Unavailable (503)'), false);
  assert.strictEqual(live.isNotFoundErrorMessage('The service did not respond in time. You can retry safely.'), false);
  assert.strictEqual(live.isNotFoundErrorMessage(''), false);
  assert.strictEqual(live.isNotFoundErrorMessage(undefined), false);
});

test('nextNotFoundStreak: consecutive 404s accumulate the streak', () => {
  let streak = 0;
  streak = live.nextNotFoundStreak(streak, 'run not found (404)');
  assert.strictEqual(streak, 1);
  streak = live.nextNotFoundStreak(streak, 'run not found (404)');
  assert.strictEqual(streak, 2);
});

test('nextNotFoundStreak: a non-404 failure (network blip, 500, 503) always resets the streak to 0, never counted toward permanent unavailability', () => {
  let streak = live.nextNotFoundStreak(0, 'run not found (404)');
  assert.strictEqual(streak, 1);
  streak = live.nextNotFoundStreak(streak, 'The service did not respond in time. You can retry safely.');
  assert.strictEqual(streak, 0, 'a transient network failure must not be conflated with the run being gone, and must not preserve a partial 404 streak either');
});

test('shouldTreatAsUnavailable: a single failed fetch (streak=1) is NOT enough to declare the run permanently gone', () => {
  assert.strictEqual(live.shouldTreatAsUnavailable(1), false);
});

test('shouldTreatAsUnavailable: reaching NOT_FOUND_UNAVAILABLE_THRESHOLD consecutive 404s IS treated as permanent unavailability', () => {
  assert.strictEqual(live.shouldTreatAsUnavailable(live.NOT_FOUND_UNAVAILABLE_THRESHOLD), true);
  assert.strictEqual(live.shouldTreatAsUnavailable(live.NOT_FOUND_UNAVAILABLE_THRESHOLD + 1), true);
});

test('end-to-end streak simulation: one 404 keeps retrying, a second consecutive 404 latches unavailable, and a later hypothetical success is ignored once latched (markRunUnavailable never un-latches)', () => {
  let streak = 0;
  let unavailable = false;

  function attempt(errorMessage) {
    if (unavailable) return; // mirrors multiagent-run.html's reconcile() guard
    if (errorMessage === null) {
      streak = 0;
      return;
    }
    streak = live.nextNotFoundStreak(streak, errorMessage);
    if (live.shouldTreatAsUnavailable(streak)) unavailable = true;
  }

  attempt('run not found (404)');
  assert.strictEqual(unavailable, false, 'a single 404 must not latch unavailable yet');
  attempt('run not found (404)');
  assert.strictEqual(unavailable, true, 'a second consecutive 404 must latch unavailable');
  attempt(null); // a hypothetical later success must be ignored once latched
  assert.strictEqual(unavailable, true, 'unavailable must never un-latch itself');
});

// ===========================================================================
// Matrix item 12: unsupported workflow-definition version -- visible warning
// banner without blocking rendering, plus the existing unknown-node/edge
// fallback behavior (M3) still holding.
// ===========================================================================

test('checkSnapshotCompatibility: the real Phase 1 reference workflow (multi-agent-software-task, schema 1, workflow version 1) is compatible -- no false-positive warning', () => {
  const message = gm.checkSnapshotCompatibility({
    workflow_id: 'multi-agent-software-task', schema_version: 1, workflow_version: 1
  });
  assert.strictEqual(message, null);
});

test('checkSnapshotCompatibility: an unrecognized workflow_id produces a visible, non-blocking warning naming both the actual and expected id', () => {
  const message = gm.checkSnapshotCompatibility({
    workflow_id: 'some-future-workflow', schema_version: 1, workflow_version: 1
  });
  assert.ok(message, 'expected a warning message');
  assert.ok(message.includes('some-future-workflow'));
  assert.ok(message.includes(gm.EXPECTED_WORKFLOW_ID));
});

test('checkSnapshotCompatibility: a mismatched dashboard schema_version produces a warning', () => {
  const message = gm.checkSnapshotCompatibility({
    workflow_id: gm.EXPECTED_WORKFLOW_ID, schema_version: 2, workflow_version: 1
  });
  assert.ok(message);
  assert.ok(message.includes('schema version 2'));
});

test('checkSnapshotCompatibility: a mismatched workflow_version (a future revision of the reference workflow) produces a warning', () => {
  const message = gm.checkSnapshotCompatibility({
    workflow_id: gm.EXPECTED_WORKFLOW_ID, schema_version: 1, workflow_version: 2
  });
  assert.ok(message);
  assert.ok(message.includes('workflow definition version 2'));
});

test('checkSnapshotCompatibility: missing/absent version fields never produce a false-positive warning (only an actual mismatch does)', () => {
  assert.strictEqual(gm.checkSnapshotCompatibility({}), null);
  assert.strictEqual(gm.checkSnapshotCompatibility(undefined), null);
  assert.strictEqual(gm.checkSnapshotCompatibility({ workflow_id: gm.EXPECTED_WORKFLOW_ID }), null);
});

test('buildGraphViewModel: an unrecognized node id falls back to a simple overflow row instead of crashing (M3 behavior, still holding)', () => {
  const graph = {
    workflow_id: 'some-future-workflow', workflow_version: 1,
    nodes: [
      { id: 'node:planner', kind: 'role', role: 'planner', label: 'Planner', status: 'completed', visits: 1 },
      { id: 'node:reviewer_two', kind: 'role', role: 'reviewer_two', label: 'Second Reviewer', status: 'not_started', visits: 0 }
    ],
    edges: []
  };
  const vm = gm.buildGraphViewModel(graph);
  const fallbackNode = vm.nodes.find(n => n.id === 'node:reviewer_two');
  assert.ok(fallbackNode, 'the unrecognized node must still be rendered, not dropped');
  assert.strictEqual(fallbackNode.y, 560 - 96 / 2, 'expected the fallback overflow-row y position');
  // canvasHeight grows to make room for the overflow row (see
  // multiagent-graph.js's anyFallback check) -- this is the frontend's
  // visible (if subtle) signal that something outside the known reference
  // layout was encountered, on top of the explicit version banner above.
  assert.strictEqual(vm.canvasHeight, 620);
});

test('buildGraphViewModel: an edge whose endpoint is not present in nodes[] at all is skipped rather than crashing', () => {
  const graph = {
    workflow_id: 'some-future-workflow', workflow_version: 1,
    nodes: [{ id: 'node:planner', kind: 'role', role: 'planner', label: 'Planner', status: 'completed', visits: 1 }],
    edges: [
      { id: 'edge:planner:mystery_outcome', from: 'planner', outcome: 'mystery_outcome', to: 'some_role_never_listed_as_a_node', label: 'Mystery', status: 'available', is_loopback: false, traversal_count: 0 }
    ]
  };
  assert.doesNotThrow(() => gm.buildGraphViewModel(graph));
  const vm = gm.buildGraphViewModel(graph);
  assert.strictEqual(vm.edges.length, 0, 'an edge with a fully unresolved endpoint must be dropped, not rendered with garbage coordinates');
});

if (failures > 0) {
  console.error(`\n${failures} test(s) failed.`);
  process.exit(1);
} else {
  console.log('\nAll tests passed.');
}
