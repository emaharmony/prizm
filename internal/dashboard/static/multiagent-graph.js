/*
 * Pure (DOM-free) view-model logic for the multi-agent execution graph and
 * budget panels (internal/dashboard/static/multiagent-run.html).
 *
 * This file is intentionally framework-free and has no import/export syntax
 * so it works unmodified as a plain browser <script> (its exports become
 * `window.PrizmMultiAgentGraph`) AND as a CommonJS module for the plain-node
 * tests under internal/dashboard/static/tests/ (no npm, no build step).
 *
 * It knows the *fixed* Phase 1 reference workflow topology (planner ->
 * developer -> tester -> reviewer, with tests_failed/changes_requested
 * correction loops back to developer, and three terminal outcomes). It does
 * NOT implement a generic graph-layout algorithm: unrecognized node ids fall
 * back to a simple overflow row so the page never crashes on an unexpected
 * workflow, but only the reference topology gets a hand-tuned layout.
 */
(function (root) {
  'use strict';

  // ---- Counter label vocabulary -------------------------------------
  // Each of these names exactly one counting concept in the UI. The word
  // "iteration" must never be reused for a different concept than
  // LOCAL_ITERATIONS (per-node, current-visit local iteration count).
  var COUNTER_LABELS = {
    VISITS: 'Visits',                     // per-node, all-time visit count
    LOCAL_ITERATIONS: 'Local iterations', // per-node, current visit only
    TRAVERSALS: 'Traversals',             // per-edge traversal count
    TOTAL_TRANSITIONS: 'Total transitions' // run-level transition_count
  };

  // ---- Fixed reference-topology layout --------------------------------
  var CANVAS_WIDTH = 1040;
  var CANVAS_BASE_HEIGHT = 460;
  var ROLE_NODE = { w: 172, h: 96 };
  var TERMINAL_NODE = { w: 152, h: 68 };

  // Centers, in canvas px. Main flow left-to-right on the top row; terminal
  // outcomes sit directly beneath the role node that produces them so the
  // (non-loopback) edge to each is a short vertical line.
  var NODE_POSITIONS = {
    'node:planner':            { cx: 130, cy: 110, box: ROLE_NODE },
    'node:developer':          { cx: 400, cy: 110, box: ROLE_NODE },
    'node:tester':             { cx: 670, cy: 110, box: ROLE_NODE },
    'node:reviewer':           { cx: 910, cy: 110, box: ROLE_NODE },
    'node:terminal:completed': { cx: 910, cy: 380, box: TERMINAL_NODE },
    'node:terminal:failed':    { cx: 400, cy: 380, box: TERMINAL_NODE },
    'node:terminal:cancelled': { cx: 130, cy: 380, box: TERMINAL_NODE }
  };

  // Anchor sides + (for loopbacks) how far the curve dips below the main
  // row, keyed by the edge's own stable id. Edges with no entry here (e.g.
  // an edge from a future, non-reference workflow) fall back to a plain
  // center-to-center straight line rather than crashing.
  var EDGE_ANCHORS = {
    'edge:planner:plan_ready':             { from: 'right',  to: 'left' },
    'edge:developer:implementation_ready': { from: 'right',  to: 'left' },
    'edge:tester:tests_passed':            { from: 'right',  to: 'left' },
    'edge:reviewer:review_approved':       { from: 'bottom', to: 'top' },
    'edge:developer:terminal_failure':     { from: 'bottom', to: 'top' },
    'edge:planner:cancelled':              { from: 'bottom', to: 'top' },
    'edge:tester:tests_failed':            { from: 'bottom', to: 'bottom', loopDepth: 90 },
    'edge:reviewer:changes_requested':     { from: 'bottom', to: 'bottom', loopDepth: 180 }
  };

  function humanize(value) {
    if (!value) return '';
    var spaced = String(value).replace(/_/g, ' ');
    return spaced.charAt(0).toUpperCase() + spaced.slice(1);
  }

  function edgeSourceNodeId(edge) {
    return 'node:' + edge.from;
  }

  function edgeTargetNodeId(edge) {
    if (edge.to) return 'node:' + edge.to;
    if (edge.terminal) return 'node:terminal:' + edge.terminal;
    return '';
  }

  function anchorPoint(pos, side) {
    var halfW = pos.box.w / 2, halfH = pos.box.h / 2;
    switch (side) {
      case 'left': return { x: pos.cx - halfW, y: pos.cy };
      case 'right': return { x: pos.cx + halfW, y: pos.cy };
      case 'top': return { x: pos.cx, y: pos.cy - halfH };
      case 'bottom': return { x: pos.cx, y: pos.cy + halfH };
      default: return { x: pos.cx, y: pos.cy };
    }
  }

  // quadraticPoint evaluates a quadratic bezier B(t) for P0, control C, P2.
  function quadraticPoint(p0, c, p2, t) {
    var mt = 1 - t;
    return {
      x: mt * mt * p0.x + 2 * t * mt * c.x + t * t * p2.x,
      y: mt * mt * p0.y + 2 * t * mt * c.y + t * t * p2.y
    };
  }

  function nodePosition(nodeId, fallbackIndexRef) {
    var pos = NODE_POSITIONS[nodeId];
    if (pos) return pos;
    // Defensive fallback for a node id this fixed layout doesn't know about:
    // stack it in an overflow row below the reference topology rather than
    // failing to render.
    var index = fallbackIndexRef.count++;
    return { cx: 130 + index * 220, cy: 560, box: ROLE_NODE, fallback: true };
  }

  /**
   * buildGraphViewModel derives a plain-object render model from one
   * RunGraph JSON payload (snapshot.graph). No DOM access.
   */
  function buildGraphViewModel(graph) {
    graph = graph || {};
    var nodes = Array.isArray(graph.nodes) ? graph.nodes : [];
    var edges = Array.isArray(graph.edges) ? graph.edges : [];

    var fallbackIndexRef = { count: 0 };
    var positionsById = {};
    var anyFallback = false;

    var vmNodes = nodes.map(function (node) {
      var pos = nodePosition(node.id, fallbackIndexRef);
      positionsById[node.id] = pos;
      if (pos.fallback) anyFallback = true;

      var isRole = node.kind === 'role';
      var visitsText = isRole
        ? COUNTER_LABELS.VISITS + ' ' + (node.visits || 0) +
          (node.max_visits != null ? ' / ' + node.max_visits : '')
        : '';
      var showLocalIterations = isRole && !!node.is_current;
      var localIterationsText = showLocalIterations
        ? COUNTER_LABELS.LOCAL_ITERATIONS + ' ' + (node.local_iterations || 0) +
          (node.max_local_iterations != null ? ' / ' + node.max_local_iterations : '')
        : '';

      var statusLabel = humanize(node.status);
      var ariaParts = [node.label || node.id, node.kind === 'role' ? 'role' : 'terminal outcome', statusLabel];
      if (node.is_current) ariaParts.push('current node');
      if (visitsText) ariaParts.push(visitsText);
      if (localIterationsText) ariaParts.push(localIterationsText);

      return {
        id: node.id,
        kind: node.kind,
        role: node.role || '',
        terminal: node.terminal || '',
        label: node.label || node.id,
        status: node.status,
        statusLabel: statusLabel,
        statusClass: 'ma-node--' + (node.status || 'unknown'),
        isCurrent: !!node.is_current,
        visits: node.visits || 0,
        maxVisits: node.max_visits != null ? node.max_visits : null,
        localIterations: node.local_iterations || 0,
        maxLocalIterations: node.max_local_iterations != null ? node.max_local_iterations : null,
        visitsText: visitsText,
        showLocalIterations: showLocalIterations,
        localIterationsText: localIterationsText,
        x: pos.cx - pos.box.w / 2,
        y: pos.cy - pos.box.h / 2,
        width: pos.box.w,
        height: pos.box.h,
        cx: pos.cx,
        cy: pos.cy,
        ariaLabel: ariaParts.filter(Boolean).join(', ')
      };
    });

    var vmEdges = edges.map(function (edge) {
      var sourceId = edgeSourceNodeId(edge);
      var targetId = edgeTargetNodeId(edge);
      var sourcePos = positionsById[sourceId] || NODE_POSITIONS[sourceId];
      var targetPos = positionsById[targetId] || NODE_POSITIONS[targetId];
      if (!sourcePos || !targetPos) return null; // unresolved endpoint: skip rather than crash

      var anchors = EDGE_ANCHORS[edge.id] || { from: 'center', to: 'center' };
      var p0 = anchorPoint(sourcePos, anchors.from);
      var p2 = anchorPoint(targetPos, anchors.to);

      var isCurve = !!edge.is_loopback && anchors.loopDepth;
      var geometry;
      var labelPoint;
      if (isCurve) {
        var mid = { x: (p0.x + p2.x) / 2, y: (p0.y + p2.y) / 2 };
        var control = { x: mid.x, y: mid.y + anchors.loopDepth };
        geometry = { curve: true, x1: p0.x, y1: p0.y, x2: p2.x, y2: p2.y, cx: control.x, cy: control.y,
          path: 'M ' + p0.x + ' ' + p0.y + ' Q ' + control.x + ' ' + control.y + ' ' + p2.x + ' ' + p2.y };
        labelPoint = quadraticPoint(p0, control, p2, 0.5);
        labelPoint = { x: labelPoint.x, y: labelPoint.y + 14 };
      } else {
        geometry = { curve: false, x1: p0.x, y1: p0.y, x2: p2.x, y2: p2.y };
        var horizontal = Math.abs(p0.y - p2.y) < Math.abs(p0.x - p2.x);
        labelPoint = horizontal
          ? { x: (p0.x + p2.x) / 2, y: (p0.y + p2.y) / 2 - 8 }
          : { x: (p0.x + p2.x) / 2 + 10, y: (p0.y + p2.y) / 2 };
      }

      var label = edge.label || humanize(edge.outcome);
      var statusLabel = humanize(edge.status);
      var showTraversals = edge.max_traversals != null || edge.is_loopback;
      var traversalText = showTraversals
        ? COUNTER_LABELS.TRAVERSALS + ' ' + (edge.traversal_count || 0) +
          (edge.max_traversals != null ? ' / ' + edge.max_traversals : '')
        : '';

      var targetLabel = edge.to ? humanize(edge.to) : humanize(edge.terminal);
      var ariaParts = [humanize(edge.from) + ' to ' + targetLabel + ' on ' + label, statusLabel];
      if (traversalText) ariaParts.push(traversalText);

      return {
        id: edge.id,
        from: edge.from,
        to: edge.to || '',
        terminal: edge.terminal || '',
        outcome: edge.outcome,
        label: label,
        status: edge.status,
        statusLabel: statusLabel,
        statusClass: 'ma-edge--' + (edge.status || 'unknown'),
        isLoopback: !!edge.is_loopback,
        traversalCount: edge.traversal_count || 0,
        maxTraversals: edge.max_traversals != null ? edge.max_traversals : null,
        traversalText: traversalText,
        geometry: geometry,
        labelPoint: labelPoint,
        ariaLabel: ariaParts.filter(Boolean).join(', ')
      };
    }).filter(Boolean);

    return {
      workflowId: graph.workflow_id || '',
      workflowVersion: graph.workflow_version || 0,
      currentNodeId: graph.current_node_id || '',
      activeEdgeId: graph.active_edge_id || '',
      runPaused: !!graph.run_paused,
      budgetExhausted: !!graph.budget_exhausted,
      canvasWidth: CANVAS_WIDTH,
      canvasHeight: anyFallback ? 620 : CANVAS_BASE_HEIGHT,
      nodes: vmNodes,
      edges: vmEdges
    };
  }

  // ---- Budget panel view model -----------------------------------------

  function pct(used, limit) {
    if (limit == null || limit <= 0) return 0;
    return Math.max(0, Math.min(100, Math.round((used / limit) * 100)));
  }

  function dimensionEntry(key, label, dim) {
    dim = dim || { used: 0, limit: null, level: 'normal' };
    var unlimited = dim.limit == null;
    return {
      key: key,
      label: label,
      used: dim.used || 0,
      limit: unlimited ? null : dim.limit,
      level: dim.level || 'normal',
      unlimited: unlimited,
      pct: unlimited ? 0 : pct(dim.used || 0, dim.limit),
      ariaLabel: label + ': ' + (dim.used || 0) +
        (unlimited ? ' used, unlimited, ' + (dim.level || 'normal')
          : ' of ' + dim.limit + ' used, ' + (dim.level || 'normal'))
    };
  }

  var LOOP_LABELS = {
    tester_to_developer: 'Tester → Developer',
    reviewer_to_developer: 'Reviewer → Developer'
  };

  /**
   * buildBudgetViewModel derives a flat, ordered list of dimension bar
   * entries from one BudgetVisualization JSON payload (snapshot.budgets).
   * No DOM access.
   */
  function buildBudgetViewModel(budgets) {
    budgets = budgets || {};
    var entries = [
      dimensionEntry('transitions', COUNTER_LABELS.TOTAL_TRANSITIONS, budgets.transitions),
      dimensionEntry('tokens', 'Tokens', budgets.tokens),
      dimensionEntry('local_iterations', 'Total local iterations', budgets.local_iterations),
      dimensionEntry('repeated_failures', 'Repeated failures', budgets.repeated_failures),
      dimensionEntry('elapsed', 'Elapsed time (s)', budgets.elapsed)
    ];

    var roleVisits = budgets.role_visits || {};
    Object.keys(roleVisits).sort().forEach(function (role) {
      entries.push(dimensionEntry('role_visits:' + role, COUNTER_LABELS.VISITS + ' — ' + humanize(role), roleVisits[role]));
    });

    var loopTraversals = budgets.loop_traversals || {};
    Object.keys(loopTraversals).sort().forEach(function (loop) {
      var label = COUNTER_LABELS.TRAVERSALS + ' — ' + (LOOP_LABELS[loop] || humanize(loop));
      entries.push(dimensionEntry('loop_traversals:' + loop, label, loopTraversals[loop]));
    });

    return entries;
  }

  // ---- Milestone 7: workflow-definition version/identity compatibility --
  //
  // Phase 1 has exactly one reference workflow definition; this frontend
  // build's fixed NODE_POSITIONS/EDGE_ANCHORS tables (above) and
  // multiagent-live.js's derivePatches switch were both written against it.
  // Neither needs a compatibility framework to stay safe against an
  // unexpected workflow_id/schema_version/workflow_version: unrecognized
  // node ids already fall back to a plain overflow row (nodePosition above)
  // and unrecognized edge ids already fall back to a straight center-to-
  // center line (buildGraphViewModel above) or are skipped if an endpoint
  // can't be resolved at all, so the page never crashes. What's missing is
  // only a *visible* signal: without it, a mismatch renders "successfully"
  // but silently, which risks an operator trusting a graph that may not
  // actually reflect the real workflow shape. checkSnapshotCompatibility
  // fills that gap with a proportionate, non-blocking warning -- it never
  // withholds rendering and never attempts to reconcile/translate between
  // versions.
  var EXPECTED_WORKFLOW_ID = 'multi-agent-software-task';
  var EXPECTED_SCHEMA_VERSION = 1;
  var EXPECTED_WORKFLOW_VERSION = 1;

  /**
   * checkSnapshotCompatibility compares one DashboardRunSnapshot's
   * workflow_id/schema_version/workflow_version against the values this
   * frontend build was written against. Returns a human-readable warning
   * string when any of the three does not match (and was actually present
   * on the snapshot -- a missing/zero field is not treated as a mismatch,
   * since older or partially-populated payloads must not falsely warn),
   * or null when everything matches (the common case today, since Phase 1
   * has only one workflow).
   */
  function checkSnapshotCompatibility(snapshot) {
    snapshot = snapshot || {};
    var mismatches = [];
    if (snapshot.workflow_id && snapshot.workflow_id !== EXPECTED_WORKFLOW_ID) {
      mismatches.push('workflow "' + snapshot.workflow_id + '" (this page was built for "' + EXPECTED_WORKFLOW_ID + '")');
    }
    if (snapshot.schema_version != null && snapshot.schema_version !== EXPECTED_SCHEMA_VERSION) {
      mismatches.push('dashboard schema version ' + snapshot.schema_version + ' (expected ' + EXPECTED_SCHEMA_VERSION + ')');
    }
    if (snapshot.workflow_version != null && snapshot.workflow_version !== EXPECTED_WORKFLOW_VERSION) {
      mismatches.push('workflow definition version ' + snapshot.workflow_version + ' (expected ' + EXPECTED_WORKFLOW_VERSION + ')');
    }
    if (!mismatches.length) return null;
    return 'This run uses an unexpected ' + mismatches.join(', and ') +
      '. The graph below may be incomplete or laid out incorrectly.';
  }

  var api = {
    COUNTER_LABELS: COUNTER_LABELS,
    NODE_POSITIONS: NODE_POSITIONS,
    EDGE_ANCHORS: EDGE_ANCHORS,
    EXPECTED_WORKFLOW_ID: EXPECTED_WORKFLOW_ID,
    EXPECTED_SCHEMA_VERSION: EXPECTED_SCHEMA_VERSION,
    EXPECTED_WORKFLOW_VERSION: EXPECTED_WORKFLOW_VERSION,
    humanize: humanize,
    buildGraphViewModel: buildGraphViewModel,
    buildBudgetViewModel: buildBudgetViewModel,
    checkSnapshotCompatibility: checkSnapshotCompatibility
  };

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  } else {
    root.PrizmMultiAgentGraph = api;
  }
})(typeof window !== 'undefined' ? window : globalThis);
