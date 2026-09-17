// Plain-node test for internal/dashboard/static/multiagent-live.js.
// No package.json, no npm, no build step: run directly with
//   node internal/dashboard/static/tests/multiagent-live.test.js
// Exits non-zero (via an uncaught assertion error) on any failure.

const assert = require('assert');
const path = require('path');
const live = require(path.join(__dirname, '..', 'multiagent-live.js'));

let failures = 0;
function test(name, fn) {
  try {
    fn();
    console.log('ok - ' + name);
  } catch (err) {
    failures++;
    console.error('FAIL - ' + name);
    console.error(err && err.stack || err);
  }
}

// ---- Event type vocabulary ------------------------------------------------

test('ALL_EVENT_TYPES lists exactly the 19 canonical multi-agent event types from internal/event/multiagent.go', () => {
  const want = [
    'prizm.workflow.multi_agent.run.created',
    'prizm.workflow.multi_agent.run.started',
    'prizm.workflow.multi_agent.role.entered',
    'prizm.workflow.multi_agent.role.iteration.started',
    'prizm.workflow.multi_agent.role.iteration.completed',
    'prizm.workflow.multi_agent.role.completed',
    'prizm.workflow.multi_agent.handoff.created',
    'prizm.workflow.multi_agent.transition.selected',
    'prizm.workflow.multi_agent.loop.traversal.recorded',
    'prizm.workflow.multi_agent.budget.warning',
    'prizm.workflow.multi_agent.budget.exhausted',
    'prizm.workflow.multi_agent.run.paused',
    'prizm.workflow.multi_agent.run.resumed',
    'prizm.workflow.multi_agent.run.completed',
    'prizm.workflow.multi_agent.run.failed',
    'prizm.workflow.multi_agent.run.cancelled',
    'prizm.workflow.multi_agent.recovery.started',
    'prizm.workflow.multi_agent.recovery.completed',
    'prizm.workflow.multi_agent.recovery.failed'
  ];
  assert.strictEqual(live.ALL_EVENT_TYPES.length, 19);
  assert.deepStrictEqual(live.ALL_EVENT_TYPES.slice().sort(), want.slice().sort());
});

// ---- Id builders ------------------------------------------------------

test('id builders exactly match graph_projection.go\'s nodeID/terminalNodeID/edgeID format', () => {
  assert.strictEqual(live.roleNodeId('developer'), 'node:developer');
  assert.strictEqual(live.terminalNodeId('completed'), 'node:terminal:completed');
  assert.strictEqual(live.edgeIdFor('tester', 'tests_failed'), 'edge:tester:tests_failed');
});

// ---- Dedup / cursor ordering -------------------------------------------

test('dedup: replaying the same event id twice only applies it once', () => {
  const state = live.createStreamState();
  const id = '01HXAMPLE0000000000000001';
  assert.strictEqual(live.shouldApply(state, id), true);
  live.markApplied(state, id);
  assert.strictEqual(live.shouldApply(state, id), false, 'replayed id must not be applied again');
});

test('cursor ordering: an out-of-order-arrival event (id <= current cursor) is ignored', () => {
  const state = live.createStreamState();
  const newer = '01HXAMPLE0000000000000005';
  const older = '01HXAMPLE0000000000000002';
  live.markApplied(state, newer);
  assert.strictEqual(state.cursor, newer);
  assert.strictEqual(live.shouldApply(state, older), false, 'an id at/behind the cursor must be ignored even if never seen');
});

test('cursor ordering: an event with no id (e.g. "heartbeat" framing) is never applied', () => {
  const state = live.createStreamState();
  assert.strictEqual(live.shouldApply(state, ''), false);
  assert.strictEqual(live.shouldApply(state, undefined), false);
});

test('markApplied advances the cursor only forward, never backward', () => {
  const state = live.createStreamState();
  live.markApplied(state, '01HXAMPLE0000000000000005');
  live.markApplied(state, '01HXAMPLE0000000000000003'); // out-of-order id still gets marked seen
  assert.strictEqual(state.cursor, '01HXAMPLE0000000000000005', 'cursor must not regress');
});

// ---- Representative per-event-type patch derivation --------------------

test('role.entered: narrow patch marks the role node running + current with its own visit number', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.ROLE_ENTERED, {
    run_id: 'run-1', workflow_id: 'reference-workflow', role: 'developer', status: 'running', visit: 3
  }, { timestamp: '2026-07-30T00:00:00Z' });
  assert.deepStrictEqual(patches, [
    { kind: 'graphNode', nodeId: 'node:developer', status: 'running', isCurrent: true, visits: 3 }
  ]);
});

test('role.iteration.started: narrow patch updates only the current-visit local iteration count', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.ROLE_ITERATION_STARTED, {
    run_id: 'run-1', workflow_id: 'reference-workflow', role: 'developer', status: 'running', iteration: 2
  }, {});
  assert.deepStrictEqual(patches, [
    { kind: 'roleLocalIterations', nodeId: 'node:developer', iteration: 2 }
  ]);
});

test('role.completed: narrow patch marks the role node completed without guessing at isCurrent', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.ROLE_COMPLETED, {
    run_id: 'run-1', workflow_id: 'reference-workflow', role: 'planner', status: 'completed', outcome: 'plan_ready'
  }, {});
  assert.deepStrictEqual(patches, [
    { kind: 'graphNode', nodeId: 'node:planner', status: 'completed' }
  ]);
});

test('transition.selected (role destination): narrow patch marks the active edge and new current node from the payload directly', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.TRANSITION_SELECTED, {
    run_id: 'run-1', workflow_id: 'reference-workflow',
    source_role: 'tester', outcome: 'tests_failed', destination_role: 'developer', transition_count: 5
  }, {});
  assert.deepStrictEqual(patches, [
    { kind: 'edgeActive', edgeId: 'edge:tester:tests_failed' },
    { kind: 'nodeCurrentFlag', nodeId: 'node:developer' }
  ]);
});

test('transition.selected (terminal destination): narrow patch targets the terminal node, not a role node', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.TRANSITION_SELECTED, {
    run_id: 'run-1', workflow_id: 'reference-workflow',
    source_role: 'reviewer', outcome: 'review_approved', terminal_condition: 'completed', transition_count: 9
  }, {});
  assert.deepStrictEqual(patches, [
    { kind: 'edgeActive', edgeId: 'edge:reviewer:review_approved' },
    { kind: 'nodeCurrentFlag', nodeId: 'node:terminal:completed' }
  ]);
});

test('run.paused: narrow patch updates the status badge and raises the paused overlay', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.RUN_PAUSED, {
    run_id: 'run-1', workflow_id: 'reference-workflow', status: 'paused', reason: 'waiting for approval'
  }, {});
  assert.deepStrictEqual(patches, [
    { kind: 'runStatus', status: 'paused' },
    { kind: 'overlay', overlay: 'paused', active: true }
  ]);
});

test('run.resumed: narrow patch updates the status badge and clears the paused overlay', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.RUN_RESUMED, {
    run_id: 'run-1', workflow_id: 'reference-workflow', status: 'running'
  }, {});
  assert.deepStrictEqual(patches, [
    { kind: 'runStatus', status: 'running' },
    { kind: 'overlay', overlay: 'paused', active: false }
  ]);
});

test('run.completed: narrow patch updates status, terminal outcome, and the terminal node together', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.RUN_COMPLETED, {
    run_id: 'run-1', workflow_id: 'reference-workflow', status: 'completed',
    terminal_condition: 'completed', reason: 'All roles finished'
  }, { timestamp: '2026-07-30T01:02:03Z' });
  assert.deepStrictEqual(patches, [
    { kind: 'runStatus', status: 'completed' },
    { kind: 'terminalOutcome', condition: 'completed', reason: 'All roles finished', at: '2026-07-30T01:02:03Z' },
    { kind: 'graphNode', nodeId: 'node:terminal:completed', status: 'completed', isCurrent: true }
  ]);
});

test('run.failed: narrow patch reaches the "failed" terminal node, not "completed"', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.RUN_FAILED, {
    run_id: 'run-1', workflow_id: 'reference-workflow', status: 'failed',
    terminal_condition: 'failed', reason: 'developer role failed'
  }, {});
  const graphPatch = patches.find(p => p.kind === 'graphNode');
  assert.deepStrictEqual(graphPatch, { kind: 'graphNode', nodeId: 'node:terminal:failed', status: 'failed', isCurrent: true });
});

test('budget.exhausted: narrow patch only raises the run-level overlay, not a specific budget row (ambiguous mapping)', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.BUDGET_EXHAUSTED, {
    run_id: 'run-1', workflow_id: 'reference-workflow', budget: 'max_visits_per_role', used: 4, limit: 4, role: 'tester'
  }, {});
  assert.deepStrictEqual(patches, [{ kind: 'overlay', overlay: 'exhausted', active: true }]);
});

// ---- Events with no clean unambiguous mapping: dedup-only ----------------

test('handoff.created: no narrow patch (no handoff UI in M3\'s renderer); reconciliation-only', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.HANDOFF_CREATED, {
    run_id: 'run-1', workflow_id: 'reference-workflow', handoff_id: 'h1',
    source_role: 'planner', destination_role: 'developer', task_ref: 't1', outcome: 'plan_ready'
  }, {});
  assert.deepStrictEqual(patches, []);
});

test('budget.warning: no narrow patch (BudgetExceededError.Budget has no documented mapping to a dimension key); reconciliation-only', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.BUDGET_WARNING, {
    run_id: 'run-1', workflow_id: 'reference-workflow', budget: 'role_max_local_iterations', used: 3, limit: 4
  }, {});
  assert.deepStrictEqual(patches, []);
});

test('loop.traversal.recorded: no narrow patch (exact new traversal_count for the edge is not in this payload); reconciliation-only', () => {
  const patches = live.derivePatches(live.EVENT_TYPES.LOOP_TRAVERSAL_RECORDED, {
    run_id: 'run-1', workflow_id: 'reference-workflow',
    source_role: 'tester', outcome: 'tests_failed', destination_role: 'developer', transition_count: 5
  }, {});
  assert.deepStrictEqual(patches, []);
});

if (failures > 0) {
  console.error(`\n${failures} test(s) failed.`);
  process.exit(1);
} else {
  console.log('\nAll tests passed.');
}
