// Plain-node test for the Milestone 5 directional-edge-animation invariant:
// the SVG geometry multiagent-graph.js's buildGraphViewModel produces for
// each edge (internal/dashboard/static/multiagent-run.html's
// `stroke-dasharray`/`stroke-dashoffset` "marching ants" animation on
// `.ma-edge--active`) must start at the edge's *source* node's anchor point
// and end at the edge's *destination* node's anchor point. The CSS
// animation moves the dash pattern toward the shape's own end point
// (negative stroke-dashoffset), so if the geometry's point order ever drifted
// from source->destination, the directional animation would silently point
// backward. This is a pure-JS, DOM-free check of that point order — no
// rendering, no CSS involved.
//
// Run directly with:
//   node internal/dashboard/static/tests/multiagent-animation.test.js

const assert = require('assert');
const path = require('path');
const gm = require(path.join(__dirname, '..', 'multiagent-graph.js'));

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

// Re-derive the same anchor-point math multiagent-graph.js uses internally
// (its `anchorPoint` helper is not exported), from the exported
// NODE_POSITIONS/EDGE_ANCHORS tables, so this test can independently compute
// "where the source/destination anchor *should* be" without depending on
// buildGraphViewModel's own geometry to check itself.
function anchorPoint(pos, side) {
  const halfW = pos.box.w / 2, halfH = pos.box.h / 2;
  switch (side) {
    case 'left': return { x: pos.cx - halfW, y: pos.cy };
    case 'right': return { x: pos.cx + halfW, y: pos.cy };
    case 'top': return { x: pos.cx, y: pos.cy - halfH };
    case 'bottom': return { x: pos.cx, y: pos.cy + halfH };
    default: return { x: pos.cx, y: pos.cy };
  }
}

// Parses a `d` attribute of the exact shape multiagent-graph.js produces for
// a curved (loopback) edge: "M x0 y0 Q cx cy x2 y2".
function parseCurvePath(d) {
  const m = /^M\s*([\d.-]+)\s+([\d.-]+)\s+Q\s*([\d.-]+)\s+([\d.-]+)\s+([\d.-]+)\s+([\d.-]+)$/.exec(d.trim());
  assert.ok(m, `expected a "M x y Q cx cy x y" path, got: ${d}`);
  return {
    start: { x: parseFloat(m[1]), y: parseFloat(m[2]) },
    control: { x: parseFloat(m[3]), y: parseFloat(m[4]) },
    end: { x: parseFloat(m[5]), y: parseFloat(m[6]) }
  };
}

// A full reference-workflow graph payload (shape matches
// multiagent.DashboardRunSnapshot.graph / RunGraph) covering every edge in
// EDGE_ANCHORS, including both loopback (curved) edges.
const fullGraph = {
  workflow_id: 'reference-workflow',
  workflow_version: 1,
  nodes: [
    { id: 'node:planner', kind: 'role', role: 'planner', label: 'Planner', status: 'completed', is_current: false, visits: 1 },
    { id: 'node:developer', kind: 'role', role: 'developer', label: 'Developer', status: 'running', is_current: true, visits: 2 },
    { id: 'node:tester', kind: 'role', role: 'tester', label: 'Tester', status: 'not_started', is_current: false, visits: 0 },
    { id: 'node:reviewer', kind: 'role', role: 'reviewer', label: 'Reviewer', status: 'not_started', is_current: false, visits: 0 },
    { id: 'node:terminal:completed', kind: 'terminal', terminal: 'completed', label: 'Completed', status: 'not_started', is_current: false },
    { id: 'node:terminal:failed', kind: 'terminal', terminal: 'failed', label: 'Failed', status: 'not_started', is_current: false },
    { id: 'node:terminal:cancelled', kind: 'terminal', terminal: 'cancelled', label: 'Cancelled', status: 'not_started', is_current: false }
  ],
  edges: [
    { id: 'edge:planner:plan_ready', from: 'planner', outcome: 'plan_ready', to: 'developer', label: 'Plan ready', status: 'active', is_loopback: false },
    { id: 'edge:developer:implementation_ready', from: 'developer', outcome: 'implementation_ready', to: 'tester', label: 'Implementation ready', status: 'available', is_loopback: false },
    { id: 'edge:tester:tests_passed', from: 'tester', outcome: 'tests_passed', to: 'reviewer', label: 'Tests passed', status: 'available', is_loopback: false },
    { id: 'edge:tester:tests_failed', from: 'tester', outcome: 'tests_failed', to: 'developer', label: 'Tests failed', status: 'previously_traversed', is_loopback: true, traversal_count: 1, max_traversals: 2 },
    { id: 'edge:reviewer:changes_requested', from: 'reviewer', outcome: 'changes_requested', to: 'developer', label: 'Changes requested', status: 'available', is_loopback: true, traversal_count: 0, max_traversals: 1 },
    { id: 'edge:reviewer:review_approved', from: 'reviewer', outcome: 'review_approved', terminal: 'completed', label: 'Review approved', status: 'available', is_loopback: false },
    { id: 'edge:developer:terminal_failure', from: 'developer', outcome: 'terminal_failure', terminal: 'failed', label: 'Terminal failure', status: 'available', is_loopback: false },
    { id: 'edge:planner:cancelled', from: 'planner', outcome: 'cancelled', terminal: 'cancelled', label: 'Cancelled', status: 'available', is_loopback: false }
  ]
};

const vm = gm.buildGraphViewModel(fullGraph);
const byId = (list, id) => list.find(x => x.id === id);

fullGraph.edges.forEach(sourceEdge => {
  test(`${sourceEdge.id}: SVG shape's point order runs source -> destination (required for correct dash-flow direction)`, () => {
    const vmEdge = byId(vm.edges, sourceEdge.id);
    assert.ok(vmEdge, `expected a view-model edge for ${sourceEdge.id}`);

    const sourceNodeId = 'node:' + sourceEdge.from;
    const targetNodeId = sourceEdge.to ? 'node:' + sourceEdge.to : 'node:terminal:' + sourceEdge.terminal;
    const sourcePos = gm.NODE_POSITIONS[sourceNodeId];
    const targetPos = gm.NODE_POSITIONS[targetNodeId];
    assert.ok(sourcePos, `no fixed layout position for ${sourceNodeId}`);
    assert.ok(targetPos, `no fixed layout position for ${targetNodeId}`);

    const anchors = gm.EDGE_ANCHORS[sourceEdge.id] || { from: 'center', to: 'center' };
    const expectedStart = anchorPoint(sourcePos, anchors.from);
    const expectedEnd = anchorPoint(targetPos, anchors.to);

    let actualStart, actualEnd;
    if (vmEdge.geometry.curve) {
      const parsed = parseCurvePath(vmEdge.geometry.path);
      actualStart = parsed.start;
      actualEnd = parsed.end;
      // The path's own control point must also be the one the geometry
      // object reports (sanity check that geometry.path and geometry.cx/cy
      // describe the same curve, not two independently-drifted values).
      assert.strictEqual(parsed.control.x, vmEdge.geometry.cx);
      assert.strictEqual(parsed.control.y, vmEdge.geometry.cy);
    } else {
      actualStart = { x: vmEdge.geometry.x1, y: vmEdge.geometry.y1 };
      actualEnd = { x: vmEdge.geometry.x2, y: vmEdge.geometry.y2 };
    }

    assert.deepStrictEqual(actualStart, expectedStart,
      `${sourceEdge.id}: shape's start point must be the SOURCE (${sourceEdge.from}) node's anchor, not the destination's`);
    assert.deepStrictEqual(actualEnd, expectedEnd,
      `${sourceEdge.id}: shape's end point must be the DESTINATION (${sourceEdge.to || sourceEdge.terminal}) node's anchor, not the source's`);
  });
});

test('loopback edges (tester->developer, reviewer->developer) are curves whose direction visibly bends backward across the fixed layout, not a straight line', () => {
  const shallow = byId(vm.edges, 'edge:tester:tests_failed');
  const deep = byId(vm.edges, 'edge:reviewer:changes_requested');
  [shallow, deep].forEach(edge => {
    assert.strictEqual(edge.geometry.curve, true, `${edge.id} must render as a curved path so its loop direction is visually distinct from a straight edge`);
    // tester (cx 670) -> developer (cx 400) and reviewer (cx 910) -> developer
    // (cx 400) both travel right-to-left on screen: the source anchor's x
    // must be greater than the destination anchor's x, confirming the dash
    // animation (which follows the shape's own start->end order) will
    // visibly flow right-to-left, i.e. "backward" against the main
    // left-to-right flow -- exactly the case a static glow alone would be
    // ambiguous about.
    assert.ok(edge.geometry.x1 > edge.geometry.x2, `${edge.id}: expected the loop's start x to be right of its end x`);
  });
});

if (failures > 0) {
  console.error(`\n${failures} test(s) failed.`);
  process.exit(1);
} else {
  console.log('\nAll tests passed.');
}
