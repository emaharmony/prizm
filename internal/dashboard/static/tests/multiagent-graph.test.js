// Plain-node test for internal/dashboard/static/multiagent-graph.js.
// No package.json, no npm, no build step: run directly with
//   node internal/dashboard/static/tests/multiagent-graph.test.js
// Exits non-zero (via an uncaught assertion error) on any failure.

const assert = require('assert');
const path = require('path');
const gm = require(path.join(__dirname, '..', 'multiagent-graph.js'));

// ---- Fixture: reference workflow mid-run ---------------------------------
// planner completed; developer running (current), visits=2/local_iterations=1;
// tester not_started; one previously-traversed loop edge tester->developer
// with traversal_count=1/max_traversals=2. Shape matches
// multiagent.DashboardRunSnapshot.graph (RunGraph) exactly.
const fixtureGraph = {
  workflow_id: 'reference-workflow',
  workflow_version: 1,
  current_node_id: 'node:developer',
  active_edge_id: 'edge:planner:plan_ready',
  run_paused: false,
  budget_exhausted: false,
  nodes: [
    { id: 'node:planner', kind: 'role', role: 'planner', label: 'Planner', status: 'completed',
      is_current: false, visits: 1, local_iterations: 0, max_visits: 1, max_local_iterations: 3 },
    { id: 'node:developer', kind: 'role', role: 'developer', label: 'Developer', status: 'running',
      is_current: true, visits: 2, local_iterations: 1, max_visits: 4, max_local_iterations: 8 },
    { id: 'node:tester', kind: 'role', role: 'tester', label: 'Tester', status: 'not_started',
      is_current: false, visits: 0, local_iterations: 0, max_visits: 3, max_local_iterations: 5 },
    { id: 'node:reviewer', kind: 'role', role: 'reviewer', label: 'Reviewer', status: 'not_started',
      is_current: false, visits: 0, local_iterations: 0, max_visits: 2, max_local_iterations: 4 },
    { id: 'node:terminal:completed', kind: 'terminal', terminal: 'completed', label: 'Completed',
      status: 'not_started', is_current: false, visits: 0, local_iterations: 0 },
    { id: 'node:terminal:failed', kind: 'terminal', terminal: 'failed', label: 'Failed',
      status: 'not_started', is_current: false, visits: 0, local_iterations: 0 },
    { id: 'node:terminal:cancelled', kind: 'terminal', terminal: 'cancelled', label: 'Cancelled',
      status: 'not_started', is_current: false, visits: 0, local_iterations: 0 }
  ],
  edges: [
    { id: 'edge:planner:plan_ready', from: 'planner', outcome: 'plan_ready', to: 'developer',
      label: 'Plan ready', status: 'previously_traversed', is_loopback: false, traversal_count: 1 },
    { id: 'edge:developer:implementation_ready', from: 'developer', outcome: 'implementation_ready', to: 'tester',
      label: 'Implementation ready', status: 'available', is_loopback: false, traversal_count: 0 },
    { id: 'edge:tester:tests_passed', from: 'tester', outcome: 'tests_passed', to: 'reviewer',
      label: 'Tests passed', status: 'available', is_loopback: false, traversal_count: 0 },
    { id: 'edge:tester:tests_failed', from: 'tester', outcome: 'tests_failed', to: 'developer',
      label: 'Tests failed', status: 'previously_traversed', is_loopback: true, traversal_count: 1, max_traversals: 2 },
    { id: 'edge:reviewer:changes_requested', from: 'reviewer', outcome: 'changes_requested', to: 'developer',
      label: 'Changes requested', status: 'available', is_loopback: true, traversal_count: 0, max_traversals: 1 },
    { id: 'edge:reviewer:review_approved', from: 'reviewer', outcome: 'review_approved', terminal: 'completed',
      label: 'Review approved', status: 'available', is_loopback: false, traversal_count: 0 },
    { id: 'edge:developer:terminal_failure', from: 'developer', outcome: 'terminal_failure', terminal: 'failed',
      label: 'Terminal failure', status: 'available', is_loopback: false, traversal_count: 0 },
    { id: 'edge:planner:cancelled', from: 'planner', outcome: 'cancelled', terminal: 'cancelled',
      label: 'Cancelled', status: 'available', is_loopback: false, traversal_count: 0 }
  ]
};

const fixtureBudgets = {
  transitions: { used: 3, limit: 12, level: 'normal' },
  tokens: { used: 9000, limit: 60000, level: 'normal' },
  local_iterations: { used: 4, limit: 24, level: 'normal' },
  repeated_failures: { used: 0, limit: 2, level: 'normal' },
  elapsed: { used: 300, limit: 2700, level: 'normal' },
  role_visits: {
    planner: { used: 1, limit: 1, level: 'exhausted' },
    developer: { used: 2, limit: 4, level: 'normal' },
    tester: { used: 0, limit: 3, level: 'normal' },
    reviewer: { used: 0, limit: 2, level: 'normal' }
  },
  loop_traversals: {
    tester_to_developer: { used: 1, limit: 2, level: 'approaching' },
    reviewer_to_developer: { used: 0, limit: 1, level: 'normal' }
  }
};

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

const byId = (list, id) => list.find(x => x.id === id);

test('buildGraphViewModel: node DOM ids exactly match backend graph.nodes[].id', () => {
  const vm = gm.buildGraphViewModel(fixtureGraph);
  fixtureGraph.nodes.forEach(sourceNode => {
    const vmNode = byId(vm.nodes, sourceNode.id);
    assert.ok(vmNode, `expected a view-model node for ${sourceNode.id}`);
    assert.strictEqual(vmNode.id, sourceNode.id);
  });
});

test('buildGraphViewModel: edge DOM ids exactly match backend graph.edges[].id', () => {
  const vm = gm.buildGraphViewModel(fixtureGraph);
  fixtureGraph.edges.forEach(sourceEdge => {
    const vmEdge = byId(vm.edges, sourceEdge.id);
    assert.ok(vmEdge, `expected a view-model edge for ${sourceEdge.id}`);
    assert.strictEqual(vmEdge.id, sourceEdge.id);
  });
});

test('buildGraphViewModel: developer node is running, current, with correct visit/iteration text', () => {
  const vm = gm.buildGraphViewModel(fixtureGraph);
  const developer = byId(vm.nodes, 'node:developer');
  assert.strictEqual(developer.status, 'running');
  assert.strictEqual(developer.isCurrent, true);
  assert.strictEqual(developer.visitsText, 'Visits 2 / 4');
  assert.strictEqual(developer.showLocalIterations, true);
  assert.strictEqual(developer.localIterationsText, 'Local iterations 1 / 8');
});

test('buildGraphViewModel: planner is completed and not current; tester is not_started', () => {
  const vm = gm.buildGraphViewModel(fixtureGraph);
  const planner = byId(vm.nodes, 'node:planner');
  const tester = byId(vm.nodes, 'node:tester');
  assert.strictEqual(planner.status, 'completed');
  assert.strictEqual(planner.isCurrent, false);
  assert.strictEqual(tester.status, 'not_started');
  assert.strictEqual(tester.isCurrent, false);
  // Non-current role node must not surface a local-iterations line.
  assert.strictEqual(planner.showLocalIterations, false);
  assert.strictEqual(planner.localIterationsText, '');
});

test('buildGraphViewModel: terminal nodes never show a Visits line', () => {
  const vm = gm.buildGraphViewModel(fixtureGraph);
  const completed = byId(vm.nodes, 'node:terminal:completed');
  assert.strictEqual(completed.visitsText, '');
});

test('buildGraphViewModel: tester->developer loop edge is previously_traversed with correct traversal label', () => {
  const vm = gm.buildGraphViewModel(fixtureGraph);
  const loopEdge = byId(vm.edges, 'edge:tester:tests_failed');
  assert.strictEqual(loopEdge.status, 'previously_traversed');
  assert.strictEqual(loopEdge.statusClass, 'ma-edge--previously_traversed');
  assert.strictEqual(loopEdge.isLoopback, true);
  assert.strictEqual(loopEdge.traversalText, 'Traversals 1 / 2');
});

test('buildGraphViewModel: only the two loopback edges render as curves', () => {
  const vm = gm.buildGraphViewModel(fixtureGraph);
  const curveIds = vm.edges.filter(e => e.geometry.curve).map(e => e.id).sort();
  assert.deepStrictEqual(curveIds, ['edge:reviewer:changes_requested', 'edge:tester:tests_failed']);
});

test('buildGraphViewModel: the deeper (reviewer->developer) loop dips further than the shallow (tester->developer) loop', () => {
  const vm = gm.buildGraphViewModel(fixtureGraph);
  const shallow = byId(vm.edges, 'edge:tester:tests_failed');
  const deep = byId(vm.edges, 'edge:reviewer:changes_requested');
  assert.ok(deep.geometry.cy > shallow.geometry.cy, 'expected the reviewer loop control point to sit below the tester loop control point');
});

test('buildBudgetViewModel: covers every fixed dimension plus per-role and per-loop entries', () => {
  const entries = gm.buildBudgetViewModel(fixtureBudgets);
  const keys = entries.map(e => e.key);
  ['transitions', 'tokens', 'local_iterations', 'repeated_failures', 'elapsed'].forEach(key => {
    assert.ok(keys.includes(key), `expected a budget entry for ${key}`);
  });
  assert.ok(keys.includes('role_visits:planner'));
  assert.ok(keys.includes('loop_traversals:tester_to_developer'));
});

test('buildBudgetViewModel: an unlimited dimension is flagged unlimited, not a misleading 0% bar', () => {
  const entries = gm.buildBudgetViewModel({
    transitions: { used: 5, limit: null, level: 'normal' }
  });
  const transitions = entries.find(e => e.key === 'transitions');
  assert.strictEqual(transitions.unlimited, true);
  assert.strictEqual(transitions.limit, null);
});

test('counter labels: the four counting concepts use four distinct, non-duplicated strings', () => {
  const labels = gm.COUNTER_LABELS;
  const values = [labels.VISITS, labels.LOCAL_ITERATIONS, labels.TRAVERSALS, labels.TOTAL_TRANSITIONS];
  assert.strictEqual(new Set(values).size, 4, 'expected all four counter labels to be textually distinct');
  assert.strictEqual(labels.VISITS, 'Visits');
  assert.strictEqual(labels.LOCAL_ITERATIONS, 'Local iterations');
  assert.strictEqual(labels.TRAVERSALS, 'Traversals');
  assert.strictEqual(labels.TOTAL_TRANSITIONS, 'Total transitions');
});

test('counter labels: rendered node text uses Visits and Local iterations as distinct prefixes', () => {
  const vm = gm.buildGraphViewModel(fixtureGraph);
  const developer = byId(vm.nodes, 'node:developer');
  assert.ok(developer.visitsText.startsWith('Visits '));
  assert.ok(developer.localIterationsText.startsWith('Local iterations '));
  assert.ok(!developer.visitsText.startsWith('Local iterations'));
  assert.ok(!developer.localIterationsText.startsWith('Visits'));
});

test('counter labels: rendered edge text uses Traversals as its own prefix', () => {
  const vm = gm.buildGraphViewModel(fixtureGraph);
  const loopEdge = byId(vm.edges, 'edge:tester:tests_failed');
  assert.ok(loopEdge.traversalText.startsWith('Traversals '));
});

if (failures > 0) {
  console.error(`\n${failures} test(s) failed.`);
  process.exit(1);
} else {
  console.log('\nAll tests passed.');
}
