// Plain-node test for internal/dashboard/static/multiagent-inspector.js.
// No package.json, no npm, no build step: run directly with
//   node internal/dashboard/static/tests/multiagent-inspector.test.js
// Exits non-zero (via an uncaught assertion error) on any failure.

const assert = require('assert');
const path = require('path');
const insp = require(path.join(__dirname, '..', 'multiagent-inspector.js'));
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

// ---- Fixture: a DashboardRunSnapshot-shaped object, matching real field
// names from dashboard_snapshot.go / graph_projection.go / state.go /
// handoff.go / budget_visualization.go / durable_types.go exactly. ----------

const fixtureSnapshot = {
  schema_version: 1,
  run_id: 'run-123',
  workflow_id: 'reference-workflow',
  workflow_version: 1,
  status: 'running',
  current_role: 'developer',
  completed_roles: ['planner'],
  latest_handoff: {
    id: 'handoff-1',
    run_id: 'run-123',
    source_role: 'planner',
    destination_role: 'developer',
    task_ref: 'task-1',
    objective: 'Implement the login form',
    artifacts: [{ kind: 'file', uri: 'file:///plan.md' }],
    evidence: [{ kind: 'validation', uri: 'file:///validation.json' }],
    outcome: 'plan_ready',
    reason: 'Plan reviewed and ready for implementation',
    validation_results: [],
    unresolved_issues: [{ id: 'issue-1', summary: 'Edge case not covered', blocking: false }],
    notes: 'This should never be rendered by the inspector.',
    created_at: '2026-07-30T00:05:00Z'
  },
  role_states: {
    planner: {
      role: 'planner', status: 'completed', visits: 1, local_iterations: 0, retries: 0,
      token_usage: { prompt_tokens: 100, completion_tokens: 50, total_tokens: 150 },
      elapsed: 45 * 1e9, // 45s in nanoseconds
      last_outcome: 'plan_ready',
      updated_at: '2026-07-30T00:05:00Z',
      validation_status: 'passed',
      approval_status: 'not_required'
    },
    developer: {
      role: 'developer', status: 'running', visits: 2, local_iterations: 1, retries: 0,
      token_usage: { prompt_tokens: 200, completion_tokens: 80, total_tokens: 280 },
      elapsed: 12 * 1e9,
      updated_at: '2026-07-30T00:10:00Z',
      last_error: 'a transient tool timeout, no secrets here'
    }
  },
  transition_count: 3,
  loop_traversals: { tester_to_developer: 0, reviewer_to_developer: 0 },
  budget_usage: { tokens: { total_tokens: 430 }, total_local_iterations: 1, retries: 0, repeated_failures: 0, elapsed: 57 * 1e9 },
  graph: {
    workflow_id: 'reference-workflow',
    workflow_version: 1,
    current_node_id: 'node:developer',
    active_edge_id: 'edge:planner:plan_ready',
    run_paused: false,
    budget_exhausted: false,
    nodes: [
      { id: 'node:planner', kind: 'role', role: 'planner', label: 'Planner', status: 'completed', is_current: false, visits: 1, local_iterations: 0, max_visits: 1 },
      { id: 'node:developer', kind: 'role', role: 'developer', label: 'Developer', status: 'running', is_current: true, visits: 2, local_iterations: 1, max_visits: 4, max_local_iterations: 8 },
      { id: 'node:terminal:completed', kind: 'terminal', terminal: 'completed', label: 'Completed', status: 'not_started', is_current: false, visits: 0, local_iterations: 0 }
    ],
    edges: [
      { id: 'edge:planner:plan_ready', from: 'planner', outcome: 'plan_ready', to: 'developer', label: 'Plan ready', status: 'previously_traversed', is_loopback: false, traversal_count: 1 },
      { id: 'edge:tester:tests_failed', from: 'tester', outcome: 'tests_failed', to: 'developer', label: 'Tests failed', status: 'exhausted', is_loopback: true, traversal_count: 2, max_traversals: 2 }
    ]
  },
  budgets: {
    transitions: { used: 3, limit: 12, level: 'normal' },
    tokens: { used: 430, limit: 60000, level: 'normal' },
    local_iterations: { used: 1, limit: 24, level: 'normal' },
    repeated_failures: { used: 0, limit: 2, level: 'normal' },
    elapsed: { used: 57, limit: 2700, level: 'normal' },
    role_visits: {
      planner: { used: 1, limit: 1, level: 'exhausted' },
      developer: { used: 2, limit: 4, level: 'approaching' }
    },
    loop_traversals: {
      tester_to_developer: { used: 2, limit: 2, level: 'exhausted' },
      reviewer_to_developer: { used: 0, limit: 1, level: 'normal' }
    }
  },
  waiting: null,
  event_cursor: '01HXAMPLE0000000000000009',
  recent_events: [
    { id: '01HXAMPLE0000000000000001', type: live.EVENT_TYPES.RUN_CREATED, timestamp: '2026-07-30T00:00:00Z', payload: { run_id: 'run-123', workflow_id: 'reference-workflow', status: 'created' } },
    { id: '01HXAMPLE0000000000000002', type: live.EVENT_TYPES.ROLE_ENTERED, timestamp: '2026-07-30T00:00:05Z', payload: { run_id: 'run-123', role: 'planner', status: 'running', visit: 1 } },
    { id: '01HXAMPLE0000000000000003', type: live.EVENT_TYPES.ROLE_COMPLETED, timestamp: '2026-07-30T00:00:45Z', payload: { run_id: 'run-123', role: 'planner', status: 'completed', outcome: 'plan_ready' } },
    { id: '01HXAMPLE0000000000000004', type: live.EVENT_TYPES.TRANSITION_SELECTED, timestamp: '2026-07-30T00:00:46Z', payload: { run_id: 'run-123', source_role: 'planner', outcome: 'plan_ready', destination_role: 'developer', transition_count: 1 } },
    { id: '01HXAMPLE0000000000000005', type: live.EVENT_TYPES.ROLE_ENTERED, timestamp: '2026-07-30T00:00:47Z', payload: { run_id: 'run-123', role: 'developer', status: 'running', visit: 1 } }
  ]
};

// ---- Id correlation (event payload -> node/edge id) ----------------------

test('resolveEventTarget: role.entered resolves to the role node id', () => {
  const target = insp.resolveEventTarget(live.EVENT_TYPES.ROLE_ENTERED, { role: 'developer' });
  assert.deepStrictEqual(target, { nodeId: 'node:developer', edgeId: null, isHandoff: false });
});

test('resolveEventTarget: transition.selected (role destination) resolves both edge and node', () => {
  const target = insp.resolveEventTarget(live.EVENT_TYPES.TRANSITION_SELECTED, {
    source_role: 'tester', outcome: 'tests_failed', destination_role: 'developer'
  });
  assert.deepStrictEqual(target, { nodeId: 'node:developer', edgeId: 'edge:tester:tests_failed', isHandoff: false });
});

test('resolveEventTarget: transition.selected (terminal destination) resolves the terminal node, not a role node', () => {
  const target = insp.resolveEventTarget(live.EVENT_TYPES.TRANSITION_SELECTED, {
    source_role: 'reviewer', outcome: 'review_approved', terminal_condition: 'completed'
  });
  assert.deepStrictEqual(target, { nodeId: 'node:terminal:completed', edgeId: 'edge:reviewer:review_approved', isHandoff: false });
});

test('resolveEventTarget: handoff.created resolves to the destination role node and flags isHandoff', () => {
  const target = insp.resolveEventTarget(live.EVENT_TYPES.HANDOFF_CREATED, {
    source_role: 'planner', destination_role: 'developer', handoff_id: 'h1', task_ref: 't1', outcome: 'plan_ready'
  });
  assert.deepStrictEqual(target, { nodeId: 'node:developer', edgeId: null, isHandoff: true });
});

test('resolveEventTarget: run.completed resolves to the matching terminal node', () => {
  const target = insp.resolveEventTarget(live.EVENT_TYPES.RUN_COMPLETED, { status: 'completed', terminal_condition: 'completed' });
  assert.deepStrictEqual(target, { nodeId: 'node:terminal:completed', edgeId: null, isHandoff: false });
});

test('resolveEventTarget: budget.exhausted with a role resolves to that role\'s node', () => {
  const target = insp.resolveEventTarget(live.EVENT_TYPES.BUDGET_EXHAUSTED, { budget: 'max_visits_per_role', role: 'tester' });
  assert.deepStrictEqual(target, { nodeId: 'node:tester', edgeId: null, isHandoff: false });
});

test('resolveEventTarget: an event type with no clean target resolves to nulls, not a guess', () => {
  const target = insp.resolveEventTarget(live.EVENT_TYPES.RUN_PAUSED, { status: 'paused', reason: 'operator paused' });
  assert.deepStrictEqual(target, { nodeId: null, edgeId: null, isHandoff: false });
});

// ---- Timeline windowing / pagination --------------------------------------

test('buildTimelineWindow: sorts newest-first by ULID and returns a bounded window', () => {
  const win = insp.buildTimelineWindow(fixtureSnapshot.recent_events, 3);
  assert.strictEqual(win.total, 5);
  assert.strictEqual(win.revealed, 3);
  assert.strictEqual(win.remaining, 2);
  assert.deepStrictEqual(win.rows.map(r => r.id), [
    '01HXAMPLE0000000000000005',
    '01HXAMPLE0000000000000004',
    '01HXAMPLE0000000000000003'
  ]);
});

test('buildTimelineWindow: revealedCount defaults to DEFAULT_WINDOW_SIZE and clamps to total', () => {
  const win = insp.buildTimelineWindow(fixtureSnapshot.recent_events);
  assert.strictEqual(win.revealed, 5);
  assert.strictEqual(win.remaining, 0);
});

test('nextRevealCount: "Load older" reveals exactly the next batch, never exceeding total', () => {
  assert.strictEqual(insp.nextRevealCount(3, 5, 3), 5);
  assert.strictEqual(insp.nextRevealCount(0, 200, 75), 75);
  assert.strictEqual(insp.nextRevealCount(150, 200, 75), 200);
});

test('given a large synthetic history (500 events), the window never exceeds the requested size', () => {
  const many = [];
  for (let i = 0; i < 500; i++) {
    many.push({ id: '01HZ' + String(i).padStart(22, '0'), type: live.EVENT_TYPES.ROLE_ITERATION_STARTED, timestamp: '2026-07-30T00:00:00Z', payload: { role: 'developer', iteration: i } });
  }
  const first = insp.buildTimelineWindow(many, 75);
  assert.strictEqual(first.rows.length, 75);
  assert.strictEqual(first.remaining, 425);
  const revealed2 = insp.nextRevealCount(first.revealed, first.total);
  const second = insp.buildTimelineWindow(many, revealed2);
  assert.strictEqual(second.rows.length, 150);
  assert.strictEqual(second.remaining, 350);
});

// ---- Event row content -----------------------------------------------------

test('buildEventRow: transition.selected produces a human-readable type label and transition summary', () => {
  const row = insp.buildEventRow(fixtureSnapshot.recent_events[3]);
  assert.strictEqual(row.typeLabel, 'Transition selected');
  assert.strictEqual(row.transitionLabel, 'Planner → Developer (Plan ready)');
  assert.strictEqual(row.countLabel, 'Total transitions 1');
  assert.strictEqual(row.edgeId, 'edge:planner:plan_ready');
  assert.strictEqual(row.nodeId, 'node:developer');
});

test('buildEventRow: role.entered uses "Visits" as its own counter label, not "Local iterations"', () => {
  const row = insp.buildEventRow(fixtureSnapshot.recent_events[1]);
  assert.strictEqual(row.countLabel, 'Visits 1');
  assert.ok(!row.countLabel.startsWith('Local iterations'));
});

// ---- Inspector data selection: Run -----------------------------------------

test('buildRunInspectorData: surfaces run identity, current role, and budget snapshots that are actually present', () => {
  const data = insp.buildRunInspectorData(fixtureSnapshot, Date.parse('2026-07-30T00:01:00Z'));
  assert.strictEqual(data.runId, 'run-123');
  assert.strictEqual(data.status, 'running');
  assert.strictEqual(data.currentRoleLabel, 'Developer');
  assert.strictEqual(data.transitionsBudget.used, 3);
  assert.strictEqual(data.tokensBudget.used, 430);
});

test('buildRunInspectorData: elapsed duration is derived from recent_events timestamps, extended to "now" while non-terminal', () => {
  const data = insp.buildRunInspectorData(fixtureSnapshot, Date.parse('2026-07-30T00:01:00Z'));
  // first event 00:00:00, "now" passed in as 00:01:00 -> 60000ms
  assert.strictEqual(data.elapsedMs, 60000);
});

test('buildRunInspectorData: returns null elapsed when there is no event history to derive it from (never fabricated)', () => {
  const data = insp.buildRunInspectorData({ run_id: 'r', status: 'created', recent_events: [] }, Date.now());
  assert.strictEqual(data.elapsedMs, null);
});

test('buildRunInspectorData: warnings list includes every dimension at approaching/critical/exhausted, in a stable set', () => {
  const data = insp.buildRunInspectorData(fixtureSnapshot, Date.now());
  const keys = data.warnings.map(w => w.key).sort();
  assert.deepStrictEqual(keys, ['loop_traversals:tester_to_developer', 'role_visits:developer', 'role_visits:planner']);
});

// ---- Inspector data selection: Node ----------------------------------------

test('buildNodeInspectorData: joins GraphNode with its RoleState (visits, duration, last outcome, validation/approval)', () => {
  const data = insp.buildNodeInspectorData(fixtureSnapshot, 'node:planner');
  assert.strictEqual(data.label, 'Planner');
  assert.strictEqual(data.status, 'completed');
  assert.strictEqual(data.visits, 1);
  assert.strictEqual(data.durationMs, 45000);
  assert.strictEqual(data.lastOutcomeLabel, 'Plan ready');
  assert.strictEqual(data.validationStatusLabel, 'Passed');
  assert.strictEqual(data.approvalStatusLabel, 'Not required');
});

test('buildNodeInspectorData: a terminal node has no role-state join and no localIterations text expected', () => {
  const data = insp.buildNodeInspectorData(fixtureSnapshot, 'node:terminal:completed');
  assert.strictEqual(data.nodeKind, 'terminal');
  assert.strictEqual(data.durationMs, null);
  assert.strictEqual(data.role, '');
});

test('buildNodeInspectorData: unknown node id returns null rather than a fabricated placeholder', () => {
  assert.strictEqual(insp.buildNodeInspectorData(fixtureSnapshot, 'node:nonexistent'), null);
});

test('buildNodeInspectorData: never includes a "tool permissions" field (no such data exists on RoleState)', () => {
  const data = insp.buildNodeInspectorData(fixtureSnapshot, 'node:developer');
  assert.strictEqual(Object.prototype.hasOwnProperty.call(data, 'toolPermissions'), false);
  assert.strictEqual(Object.prototype.hasOwnProperty.call(data, 'tool_permissions'), false);
});

// ---- Inspector data selection: Edge ----------------------------------------

test('buildEdgeInspectorData: extracts outcome/source/destination/traversal fields exactly as graph_projection.go computed them', () => {
  const data = insp.buildEdgeInspectorData(fixtureSnapshot, 'edge:tester:tests_failed');
  assert.strictEqual(data.fromLabel, 'Tester');
  assert.strictEqual(data.toLabel, 'Developer');
  assert.strictEqual(data.isLoopback, true);
  assert.strictEqual(data.traversalCount, 2);
  assert.strictEqual(data.maxTraversals, 2);
  assert.strictEqual(data.isExhausted, true);
});

test('buildEdgeInspectorData: unknown edge id returns null rather than a fabricated placeholder', () => {
  assert.strictEqual(insp.buildEdgeInspectorData(fixtureSnapshot, 'edge:nonexistent:outcome'), null);
});

// ---- Inspector data selection: Handoff -------------------------------------

test('buildHandoffInspectorData: uses the real Handoff field names (source_role, destination_role, outcome as "decision", created_at as "timestamp")', () => {
  const data = insp.buildHandoffInspectorData(fixtureSnapshot.latest_handoff);
  assert.strictEqual(data.sourceRoleLabel, 'Planner');
  assert.strictEqual(data.destinationRoleLabel, 'Developer');
  assert.strictEqual(data.objective, 'Implement the login form');
  assert.strictEqual(data.decision, 'plan_ready');
  assert.strictEqual(data.decisionLabel, 'Plan ready');
  assert.strictEqual(data.timestamp, '2026-07-30T00:05:00Z');
  assert.strictEqual(data.unresolvedIssues.length, 1);
});

test('buildHandoffInspectorData: never surfaces Handoff.Notes (freeform supplemental text, out of scope for this milestone)', () => {
  const data = insp.buildHandoffInspectorData(fixtureSnapshot.latest_handoff);
  assert.strictEqual(Object.prototype.hasOwnProperty.call(data, 'notes'), false);
});

test('buildHandoffInspectorData: null handoff returns null, not a fabricated empty record', () => {
  assert.strictEqual(insp.buildHandoffInspectorData(null), null);
});

// ---- Secret-safety static assertion ----------------------------------------
//
// No inspector builder function's output should ever contain a field keyed
// like a raw credential/token value. Legitimate LLM token-COUNT fields
// (token_usage / prompt_tokens / completion_tokens / total_tokens /
// tokensBudget) are explicitly allow-listed by isSecretLikeKey since they are
// numeric usage aggregates the spec asks for, not secret material.

test('isSecretLikeKey: flags exact credential-shaped key names', () => {
  ['token', 'api_key', 'apiKey', 'secret', 'password', 'credential', 'access_token'].forEach(key => {
    assert.strictEqual(insp.isSecretLikeKey(key), true, `expected ${key} to be flagged`);
  });
});

test('isSecretLikeKey: does not flag legitimate token-usage-count field names', () => {
  ['token_usage', 'tokenUsage', 'prompt_tokens', 'completion_tokens', 'total_tokens', 'tokensBudget'].forEach(key => {
    assert.strictEqual(insp.isSecretLikeKey(key), false, `expected ${key} NOT to be flagged`);
  });
});

test('no inspector builder output (Run/Node/Edge/Handoff) contains a secret-shaped key', () => {
  const outputs = [
    insp.buildRunInspectorData(fixtureSnapshot, Date.now()),
    insp.buildNodeInspectorData(fixtureSnapshot, 'node:planner'),
    insp.buildNodeInspectorData(fixtureSnapshot, 'node:developer'),
    insp.buildEdgeInspectorData(fixtureSnapshot, 'edge:planner:plan_ready'),
    insp.buildHandoffInspectorData(fixtureSnapshot.latest_handoff)
  ];
  outputs.forEach((output, i) => {
    const keys = insp.collectKeysDeep(output);
    const offenders = keys.filter(insp.isSecretLikeKey);
    assert.deepStrictEqual(offenders, [], `output[${i}] must not contain secret-shaped keys, found: ${offenders.join(', ')}`);
  });
});

if (failures > 0) {
  console.error(`\n${failures} test(s) failed.`);
  process.exit(1);
} else {
  console.log('\nAll tests passed.');
}
