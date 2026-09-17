// Plain-node test for internal/dashboard/static/multiagent-replay.js.
// No package.json, no npm, no build step: run directly with
//   node internal/dashboard/static/tests/multiagent-replay.test.js
// Exits non-zero (via an uncaught assertion error) on any failure.

const assert = require('assert');
const path = require('path');
const replay = require(path.join(__dirname, '..', 'multiagent-replay.js'));
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

// ---- Fixture: a terminal DashboardRunSnapshot for the reference workflow,
// plus its FULL chronological event history (recent_events), matching the
// end-to-end reference scenario named in this milestone's spec:
//   Planner -> Developer -> Tester(fail) -> Developer -> Tester(pass) ->
//   Reviewer(changes_requested) -> Developer -> Tester(pass) ->
//   Reviewer(approve) -> Complete
// Event ids are fixed-width zero-padded strings so plain lexicographic sort
// is also chronological order, exactly like real ULIDs. ---------------------

const T = live.EVENT_TYPES;

function eid(n) { return '01HXREPLAY' + String(n).padStart(3, '0'); }
function ev(n, type, payload, timestamp) {
  return { id: eid(n), type, timestamp: timestamp || ('2026-07-30T00:' + String(n).padStart(2, '0') + ':00Z'), payload };
}

const orderedEvents = [
  ev(1, T.RUN_CREATED, { run_id: 'run-replay', workflow_id: 'reference-workflow', status: 'created' }),
  ev(2, T.RUN_STARTED, { run_id: 'run-replay', workflow_id: 'reference-workflow', status: 'running' }),
  ev(3, T.ROLE_ENTERED, { role: 'planner', status: 'running', visit: 1 }),
  ev(4, T.ROLE_COMPLETED, { role: 'planner', status: 'completed', outcome: 'plan_ready' }),
  ev(5, T.HANDOFF_CREATED, { handoff_id: 'h1', source_role: 'planner', destination_role: 'developer', outcome: 'plan_ready' }),
  ev(6, T.TRANSITION_SELECTED, { source_role: 'planner', outcome: 'plan_ready', destination_role: 'developer', transition_count: 1 }),
  ev(7, T.ROLE_ENTERED, { role: 'developer', status: 'running', visit: 1 }),
  ev(8, T.ROLE_COMPLETED, { role: 'developer', status: 'completed', outcome: 'implementation_ready' }),
  ev(9, T.TRANSITION_SELECTED, { source_role: 'developer', outcome: 'implementation_ready', destination_role: 'tester', transition_count: 2 }),
  ev(10, T.ROLE_ENTERED, { role: 'tester', status: 'running', visit: 1 }),
  ev(11, T.ROLE_COMPLETED, { role: 'tester', status: 'completed', outcome: 'tests_failed' }),
  ev(12, T.TRANSITION_SELECTED, { source_role: 'tester', outcome: 'tests_failed', destination_role: 'developer', transition_count: 3 }),
  ev(13, T.LOOP_TRAVERSAL_RECORDED, { source_role: 'tester', outcome: 'tests_failed', destination_role: 'developer', transition_count: 3 }),
  ev(14, T.ROLE_ENTERED, { role: 'developer', status: 'running', visit: 2 }),
  ev(15, T.ROLE_COMPLETED, { role: 'developer', status: 'completed', outcome: 'implementation_ready' }),
  ev(16, T.TRANSITION_SELECTED, { source_role: 'developer', outcome: 'implementation_ready', destination_role: 'tester', transition_count: 4 }),
  ev(17, T.ROLE_ENTERED, { role: 'tester', status: 'running', visit: 2 }),
  ev(18, T.ROLE_COMPLETED, { role: 'tester', status: 'completed', outcome: 'tests_passed' }),
  ev(19, T.TRANSITION_SELECTED, { source_role: 'tester', outcome: 'tests_passed', destination_role: 'reviewer', transition_count: 5 }),
  ev(20, T.ROLE_ENTERED, { role: 'reviewer', status: 'running', visit: 1 }),
  ev(21, T.ROLE_COMPLETED, { role: 'reviewer', status: 'completed', outcome: 'changes_requested' }),
  ev(22, T.BUDGET_WARNING, { budget: 'role_max_local_iterations', used: 3, limit: 4, role: 'developer' }),
  ev(23, T.TRANSITION_SELECTED, { source_role: 'reviewer', outcome: 'changes_requested', destination_role: 'developer', transition_count: 6 }),
  ev(24, T.LOOP_TRAVERSAL_RECORDED, { source_role: 'reviewer', outcome: 'changes_requested', destination_role: 'developer', transition_count: 6 }),
  ev(25, T.ROLE_ENTERED, { role: 'developer', status: 'running', visit: 3 }),
  ev(26, T.ROLE_COMPLETED, { role: 'developer', status: 'completed', outcome: 'implementation_ready' }),
  ev(27, T.TRANSITION_SELECTED, { source_role: 'developer', outcome: 'implementation_ready', destination_role: 'tester', transition_count: 7 }),
  ev(28, T.ROLE_ENTERED, { role: 'tester', status: 'running', visit: 3 }),
  ev(29, T.ROLE_COMPLETED, { role: 'tester', status: 'completed', outcome: 'tests_passed' }),
  ev(30, T.TRANSITION_SELECTED, { source_role: 'tester', outcome: 'tests_passed', destination_role: 'reviewer', transition_count: 8 }),
  ev(31, T.ROLE_ENTERED, { role: 'reviewer', status: 'running', visit: 2 }),
  ev(32, T.ROLE_COMPLETED, { role: 'reviewer', status: 'completed', outcome: 'review_approved' }),
  ev(33, T.TRANSITION_SELECTED, { source_role: 'reviewer', outcome: 'review_approved', terminal_condition: 'completed', transition_count: 9 }),
  ev(34, T.RUN_COMPLETED, { status: 'completed', terminal_condition: 'completed', reason: 'All roles finished' })
];

// The terminal snapshot replay is entered from: graph SHAPE (ids/labels/
// limits) is real; graph.nodes[].status/visits/etc. reflect the actual end
// state a full backend reconciliation would produce (used only to seed
// buildInitialReplayState's topology + as the "real final snapshot" jump-
// to-end target -- deliberately NOT derived by walking the same patches
// buildReplayStateAtStep uses, so the two computations are independent).
const terminalSnapshot = {
  schema_version: 1,
  run_id: 'run-replay',
  workflow_id: 'reference-workflow',
  workflow_version: 1,
  status: 'completed',
  current_role: '',
  latest_handoff: { id: 'h1', source_role: 'planner', destination_role: 'developer', outcome: 'plan_ready', created_at: '2026-07-30T00:05:00Z' },
  role_states: {
    planner: { role: 'planner', status: 'completed', visits: 1, local_iterations: 0 },
    developer: { role: 'developer', status: 'completed', visits: 3, local_iterations: 0 },
    tester: { role: 'tester', status: 'completed', visits: 3, local_iterations: 0 },
    reviewer: { role: 'reviewer', status: 'completed', visits: 2, local_iterations: 0 }
  },
  terminal_outcome: { condition: 'completed', reason: 'All roles finished', at: '2026-07-30T00:34:00Z' },
  waiting: null,
  cancellation_reason: '',
  budgets: {
    transitions: { used: 9, limit: 30, level: 'normal' },
    tokens: { used: 12000, limit: 60000, level: 'normal' },
    local_iterations: { used: 9, limit: 40, level: 'normal' },
    repeated_failures: { used: 0, limit: 2, level: 'normal' },
    elapsed: { used: 2040, limit: 3600, level: 'normal' },
    role_visits: {
      planner: { used: 1, limit: 2, level: 'normal' },
      developer: { used: 3, limit: 4, level: 'approaching' },
      tester: { used: 3, limit: 4, level: 'approaching' },
      reviewer: { used: 2, limit: 3, level: 'normal' }
    },
    loop_traversals: {
      tester_to_developer: { used: 1, limit: 2, level: 'approaching' },
      reviewer_to_developer: { used: 1, limit: 1, level: 'exhausted' }
    }
  },
  graph: {
    workflow_id: 'reference-workflow',
    workflow_version: 1,
    current_node_id: 'node:terminal:completed',
    active_edge_id: 'edge:reviewer:review_approved',
    run_paused: false,
    budget_exhausted: false,
    nodes: [
      { id: 'node:planner', kind: 'role', role: 'planner', label: 'Planner', status: 'completed', is_current: false, visits: 1, local_iterations: 0, max_visits: 2, max_local_iterations: 3 },
      { id: 'node:developer', kind: 'role', role: 'developer', label: 'Developer', status: 'completed', is_current: false, visits: 3, local_iterations: 0, max_visits: 4, max_local_iterations: 8 },
      { id: 'node:tester', kind: 'role', role: 'tester', label: 'Tester', status: 'completed', is_current: false, visits: 3, local_iterations: 0, max_visits: 4, max_local_iterations: 5 },
      { id: 'node:reviewer', kind: 'role', role: 'reviewer', label: 'Reviewer', status: 'completed', is_current: false, visits: 2, local_iterations: 0, max_visits: 3, max_local_iterations: 4 },
      { id: 'node:terminal:completed', kind: 'terminal', terminal: 'completed', label: 'Completed', status: 'completed', is_current: true, visits: 0, local_iterations: 0 },
      { id: 'node:terminal:failed', kind: 'terminal', terminal: 'failed', label: 'Failed', status: 'not_started', is_current: false, visits: 0, local_iterations: 0 },
      { id: 'node:terminal:cancelled', kind: 'terminal', terminal: 'cancelled', label: 'Cancelled', status: 'not_started', is_current: false, visits: 0, local_iterations: 0 }
    ],
    edges: [
      { id: 'edge:planner:plan_ready', from: 'planner', outcome: 'plan_ready', to: 'developer', label: 'Plan ready', status: 'previously_traversed', is_loopback: false, traversal_count: 1 },
      { id: 'edge:developer:implementation_ready', from: 'developer', outcome: 'implementation_ready', to: 'tester', label: 'Implementation ready', status: 'previously_traversed', is_loopback: false, traversal_count: 3 },
      { id: 'edge:tester:tests_passed', from: 'tester', outcome: 'tests_passed', to: 'reviewer', label: 'Tests passed', status: 'previously_traversed', is_loopback: false, traversal_count: 2 },
      // Ground truth: this loopback edge only traversed once and is NOT
      // exhausted (max_traversals: 2) -- correctly 'previously_traversed'.
      // The patch-accumulated replay state instead leaves this 'active'
      // (see the "known limitation" test below), because no later patch
      // ever restates its true post-traversal status.
      { id: 'edge:tester:tests_failed', from: 'tester', outcome: 'tests_failed', to: 'developer', label: 'Tests failed', status: 'previously_traversed', is_loopback: true, traversal_count: 1, max_traversals: 2 },
      // Ground truth: this loopback edge traversed once and its single
      // allowed traversal is now used up -- correctly 'exhausted'. Again,
      // patch-accumulated replay state instead leaves this 'active'.
      { id: 'edge:reviewer:changes_requested', from: 'reviewer', outcome: 'changes_requested', to: 'developer', label: 'Changes requested', status: 'exhausted', is_loopback: true, traversal_count: 1, max_traversals: 1 },
      { id: 'edge:reviewer:review_approved', from: 'reviewer', outcome: 'review_approved', terminal: 'completed', label: 'Review approved', status: 'previously_traversed', is_loopback: false, traversal_count: 1 },
      { id: 'edge:developer:terminal_failure', from: 'developer', outcome: 'terminal_failure', terminal: 'failed', label: 'Terminal failure', status: 'available', is_loopback: false, traversal_count: 0 },
      { id: 'edge:planner:cancelled', from: 'planner', outcome: 'cancelled', terminal: 'cancelled', label: 'Cancelled', status: 'available', is_loopback: false, traversal_count: 0 }
    ]
  },
  recent_events: orderedEvents
};

const byNodeId = (state, id) => state.graph.nodes.find(n => n.id === id);
const byEdgeId = (state, id) => state.graph.edges.find(e => e.id === id);

// ---- Terminal-status gating -----------------------------------------------

test('isTerminalRunStatus matches RunStatus.Terminal() from internal/workflow/multiagent/types.go', () => {
  assert.strictEqual(replay.isTerminalRunStatus('completed'), true);
  assert.strictEqual(replay.isTerminalRunStatus('failed'), true);
  assert.strictEqual(replay.isTerminalRunStatus('cancelled'), true);
  assert.strictEqual(replay.isTerminalRunStatus('budget_exhausted'), true);
  assert.strictEqual(replay.isTerminalRunStatus('running'), false);
  assert.strictEqual(replay.isTerminalRunStatus('paused'), false);
  assert.strictEqual(replay.isTerminalRunStatus('created'), false);
  assert.strictEqual(replay.isTerminalRunStatus(undefined), false);
});

// ---- Chronological sort ----------------------------------------------------

test('sortEventsChronological: arbitrary input order always sorts to ascending id order', () => {
  const shuffled = [orderedEvents[5], orderedEvents[0], orderedEvents[33], orderedEvents[12], orderedEvents[2], orderedEvents[20]];
  const sorted = replay.sortEventsChronological(shuffled);
  const ids = sorted.map(e => e.id);
  const expected = ids.slice().sort();
  assert.deepStrictEqual(ids, expected, 'ULID string sort must equal ascending order');
  assert.strictEqual(sorted[0].id, orderedEvents[0].id);
  assert.strictEqual(sorted[sorted.length - 1].id, orderedEvents[33].id);
});

test('sortEventsChronological does not mutate its input array', () => {
  const input = [orderedEvents[10], orderedEvents[0]];
  const inputCopy = input.slice();
  replay.sortEventsChronological(input);
  assert.deepStrictEqual(input, inputCopy);
});

// ---- Baseline ("jump to beginning") ----------------------------------------

test('buildInitialReplayState: every role node not_started, every edge available, no current node/edge, zero counters', () => {
  const state = replay.buildInitialReplayState(terminalSnapshot);
  assert.strictEqual(state.status, replay.BASELINE_RUN_STATUS);
  assert.strictEqual(state.graph.current_node_id, '');
  assert.strictEqual(state.graph.active_edge_id, '');
  assert.strictEqual(state.graph.run_paused, false);
  assert.strictEqual(state.graph.budget_exhausted, false);
  assert.strictEqual(state.graph.nodes.length, terminalSnapshot.graph.nodes.length);
  state.graph.nodes.forEach(n => {
    assert.strictEqual(n.status, 'not_started', n.id + ' should start not_started');
    assert.strictEqual(n.is_current, false);
    assert.strictEqual(n.visits, 0);
    assert.strictEqual(n.local_iterations, 0);
  });
  assert.strictEqual(state.graph.edges.length, terminalSnapshot.graph.edges.length);
  state.graph.edges.forEach(e => {
    assert.strictEqual(e.status, 'available', e.id + ' should start available');
    assert.strictEqual(e.traversal_count, 0);
  });
  // Topology (ids/limits) preserved verbatim from the source graph, not
  // reinvented.
  assert.strictEqual(byNodeId(state, 'node:developer').max_visits, 4);
  assert.strictEqual(byEdgeId(state, 'edge:tester:tests_failed').max_traversals, 2);
});

test('buildInitialReplayState: budget limits are preserved but usage is zeroed', () => {
  const state = replay.buildInitialReplayState(terminalSnapshot);
  assert.strictEqual(state.budgets.transitions.used, 0);
  assert.strictEqual(state.budgets.transitions.limit, 30);
  assert.strictEqual(state.budgets.role_visits.developer.used, 0);
  assert.strictEqual(state.budgets.role_visits.developer.limit, 4);
  assert.strictEqual(state.budgets.loop_traversals.reviewer_to_developer.used, 0);
});

test('buildReplayStateAtStep with stepIndex 0 equals the seeded baseline (jump-to-beginning)', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const state = replay.buildReplayStateAtStep(initial, sorted, 0, live.derivePatches);
  assert.strictEqual(state.status, replay.BASELINE_RUN_STATUS);
  assert.strictEqual(byNodeId(state, 'node:planner').status, 'not_started');
  assert.deepStrictEqual(state.recent_events, []);
});

// ---- Stepping forward -------------------------------------------------

test('stepping forward: after run.created + run.started, run status reflects the events but no role has entered yet', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const state = replay.buildReplayStateAtStep(initial, sorted, 2, live.derivePatches);
  assert.strictEqual(state.status, 'running');
  assert.strictEqual(byNodeId(state, 'node:planner').status, 'not_started');
});

test('stepping forward: after planner.role.entered (step 3), planner is running/current with visit 1', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const state = replay.buildReplayStateAtStep(initial, sorted, 3, live.derivePatches);
  const planner = byNodeId(state, 'node:planner');
  assert.strictEqual(planner.status, 'running');
  assert.strictEqual(planner.is_current, true);
  assert.strictEqual(planner.visits, 1);
  assert.strictEqual(state.graph.current_node_id, 'node:planner');
});

test('stepping forward: after the planner->developer transition (step 6), the edge is active and developer is current (but not yet "running" -- role.entered has not fired yet at this exact step)', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const state = replay.buildReplayStateAtStep(initial, sorted, 6, live.derivePatches);
  assert.strictEqual(byEdgeId(state, 'edge:planner:plan_ready').status, 'active');
  assert.strictEqual(state.graph.active_edge_id, 'edge:planner:plan_ready');
  assert.strictEqual(state.graph.current_node_id, 'node:developer');
  const developer = byNodeId(state, 'node:developer');
  assert.strictEqual(developer.is_current, true);
  assert.strictEqual(developer.status, 'not_started', 'role.entered has not fired for developer at this exact step yet');
});

test('stepping forward: after developer.role.entered (step 7), developer is running/current with visit 1', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const state = replay.buildReplayStateAtStep(initial, sorted, 7, live.derivePatches);
  const developer = byNodeId(state, 'node:developer');
  assert.strictEqual(developer.status, 'running');
  assert.strictEqual(developer.is_current, true);
  assert.strictEqual(developer.visits, 1);
});

test('stepping forward: after the tester-fail loop transition (step 12), the loopback edge is active and developer is current again', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const state = replay.buildReplayStateAtStep(initial, sorted, 12, live.derivePatches);
  assert.strictEqual(byEdgeId(state, 'edge:tester:tests_failed').status, 'active');
  assert.strictEqual(state.graph.current_node_id, 'node:developer');
  assert.strictEqual(byNodeId(state, 'node:tester').status, 'completed');
  assert.strictEqual(byNodeId(state, 'node:tester').is_current, false);
});

test('stepping forward: after developer re-enters for its second visit (step 14), visits increments to 2', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const state = replay.buildReplayStateAtStep(initial, sorted, 14, live.derivePatches);
  assert.strictEqual(byNodeId(state, 'node:developer').visits, 2);
});

test('stepping forward: after the whole run completes (final step), run status/terminal outcome/terminal node all reflect completion', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const state = replay.buildReplayStateAtStep(initial, sorted, sorted.length, live.derivePatches);
  assert.strictEqual(state.status, 'completed');
  assert.deepStrictEqual(state.terminal_outcome, { condition: 'completed', reason: 'All roles finished', at: orderedEvents[33].timestamp });
  const terminalNode = byNodeId(state, 'node:terminal:completed');
  assert.strictEqual(terminalNode.status, 'completed');
  assert.strictEqual(terminalNode.is_current, true);
  assert.strictEqual(state.recent_events.length, sorted.length);
});

// ---- Stepping backward ------------------------------------------------

test('stepping backward from step N recomputes the correct state at step N-1', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  // Step 7 (developer.role.entered applied): developer is running/current.
  const atN = replay.buildReplayStateAtStep(initial, sorted, 7, live.derivePatches);
  assert.strictEqual(byNodeId(atN, 'node:developer').status, 'running');
  // Stepping "backward" is just recomputing at stepIndex - 1: developer
  // should revert to not_started (role.entered has not applied yet), while
  // the earlier planner->developer transition's isCurrent flag still holds
  // (that patch landed at step 6, still <= N-1).
  const atNMinus1 = replay.buildReplayStateAtStep(initial, sorted, 6, live.derivePatches);
  assert.strictEqual(byNodeId(atNMinus1, 'node:developer').status, 'not_started');
  assert.strictEqual(byNodeId(atNMinus1, 'node:developer').is_current, true);
  assert.strictEqual(byNodeId(atNMinus1, 'node:developer').visits, 0);
});

test('stepping backward all the way to step 0 reproduces the exact baseline', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const baseline = replay.buildReplayStateAtStep(initial, sorted, 0, live.derivePatches);
  const steppedBackToZero = replay.buildReplayStateAtStep(initial, sorted, 20, live.derivePatches);
  // Not the same object/step, just confirming 0 always reproduces the true
  // baseline regardless of what step you stepped back FROM.
  const rebaselined = replay.buildReplayStateAtStep(initial, sorted, 0, live.derivePatches);
  assert.deepStrictEqual(rebaselined, baseline);
  assert.notDeepStrictEqual(steppedBackToZero, baseline);
});

// ---- Jump to end uses the REAL final snapshot, not accumulated patches ----

test('known limitation: patch-accumulated end state differs from the real final snapshot for edge statuses driven by loop.traversal.recorded (deliberate, not a bug)', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const accumulated = replay.buildReplayStateAtStep(initial, sorted, sorted.length, live.derivePatches);

  // Real ground truth (terminalSnapshot, authored independently above):
  // both loopback edges resolve to their true post-traversal status.
  assert.strictEqual(byEdgeId(terminalSnapshot, 'edge:tester:tests_failed').status, 'previously_traversed');
  assert.strictEqual(byEdgeId(terminalSnapshot, 'edge:reviewer:changes_requested').status, 'exhausted');

  // Patch-accumulated replay state: derivePatches' edgeActive patch never
  // reverts the PREVIOUSLY active edge (see multiagent-live.js's
  // applyEdgeActive comment) because the exact new traversal_count for that
  // edge isn't in any event payload -- loop.traversal.recorded and the
  // sibling transition.selected are reconciliation-only for that field.
  // Both loopback edges are therefore still marked 'active' by patch-only
  // accumulation, which is a real, expected divergence from ground truth --
  // exactly the class of imprecision multiagent-replay.js's module doc
  // comment documents as self-correcting only at jump-to-end.
  assert.strictEqual(byEdgeId(accumulated, 'edge:tester:tests_failed').status, 'active');
  assert.notStrictEqual(byEdgeId(accumulated, 'edge:tester:tests_failed').status, byEdgeId(terminalSnapshot, 'edge:tester:tests_failed').status);
});

test('known limitation: budget dimension usage never updates from patches alone, even at the final step', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const accumulated = replay.buildReplayStateAtStep(initial, sorted, sorted.length, live.derivePatches);
  assert.strictEqual(accumulated.budgets.transitions.used, 0, 'budget.warning/budget.exhausted never restate a dimension\'s used count (see multiagent-live.js)');
  assert.strictEqual(terminalSnapshot.budgets.transitions.used, 9, 'ground truth DOES have real usage -- this is exactly the gap jump-to-end (real snapshot) closes');
});

test('known limitation: handoff.created has no visible replay-state effect from patches alone', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const sorted = replay.sortEventsChronological(orderedEvents);
  const afterHandoff = replay.buildReplayStateAtStep(initial, sorted, 5, live.derivePatches);
  assert.strictEqual(afterHandoff.latest_handoff, null, 'handoff.created returns an empty patch list; only jump-to-end (real snapshot) exposes latest_handoff');
  assert.strictEqual(terminalSnapshot.latest_handoff.id, 'h1');
});

// "Jump to end" in multiagent-run.html renders lastSnapshot directly rather
// than calling buildReplayStateAtStep(..., sortedEvents.length, ...) for
// exactly the reason demonstrated above -- this module doesn't (and
// shouldn't) simulate that DOM-level branch, but the two tests above prove
// the underlying premise: the real snapshot and the accumulated-patch state
// CAN and DO differ, so choosing the real snapshot for jump-to-end is
// strictly more accurate, not merely simpler.

// ---- Playback speed --------------------------------------------------------

test('intervalForSpeed: higher speed multiplier yields a shorter interval, proportionally', () => {
  assert.strictEqual(replay.intervalForSpeed(1), replay.BASE_STEP_INTERVAL_MS);
  assert.strictEqual(replay.intervalForSpeed(2), replay.BASE_STEP_INTERVAL_MS / 2);
  assert.strictEqual(replay.intervalForSpeed(4), replay.BASE_STEP_INTERVAL_MS / 4);
  assert.strictEqual(replay.intervalForSpeed(0.5), replay.BASE_STEP_INTERVAL_MS / 0.5);
});

test('intervalForSpeed: invalid/missing speed falls back to 1x', () => {
  assert.strictEqual(replay.intervalForSpeed(0), replay.BASE_STEP_INTERVAL_MS);
  assert.strictEqual(replay.intervalForSpeed(-3), replay.BASE_STEP_INTERVAL_MS);
  assert.strictEqual(replay.intervalForSpeed(undefined), replay.BASE_STEP_INTERVAL_MS);
  assert.strictEqual(replay.intervalForSpeed(NaN), replay.BASE_STEP_INTERVAL_MS);
});

test('SPEED_OPTIONS lists the documented multipliers in ascending order', () => {
  assert.deepStrictEqual(replay.SPEED_OPTIONS, [0.5, 1, 2, 4]);
});

// ---- Isolation: buildReplayStateAtStep never mutates its inputs ----------

test('buildReplayStateAtStep does not mutate initialState or the source events (replay state must stay isolated from live data)', () => {
  const initial = replay.buildInitialReplayState(terminalSnapshot);
  const initialSnapshotBefore = JSON.stringify(initial);
  const eventsBefore = JSON.stringify(orderedEvents);
  const sorted = replay.sortEventsChronological(orderedEvents);
  replay.buildReplayStateAtStep(initial, sorted, sorted.length, live.derivePatches);
  assert.strictEqual(JSON.stringify(initial), initialSnapshotBefore, 'initialState must not be mutated by stepping');
  assert.strictEqual(JSON.stringify(orderedEvents), eventsBefore, 'source events must not be mutated by stepping');
});

if (failures > 0) {
  console.error(`\n${failures} test(s) failed.`);
  process.exit(1);
} else {
  console.log('\nAll tests passed.');
}
