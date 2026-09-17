/*
 * Pure (DOM-free) execution-replay state logic for the multi-agent run page
 * (internal/dashboard/static/multiagent-run.html), Milestone 8.
 *
 * Like multiagent-graph.js / multiagent-live.js / multiagent-inspector.js,
 * this file has no import/export syntax so it works unmodified as a plain
 * browser <script> (window.PrizmMultiAgentReplay) AND as a CommonJS module
 * for the plain-node tests under internal/dashboard/static/tests/ (no npm,
 * no build step).
 *
 * Design: replay NEVER re-executes an agent or tool and NEVER makes a
 * network request. It works entirely from data the page already fetched
 * once on load -- a completed/failed/cancelled/budget_exhausted run's full,
 * untruncated event history (DashboardRunSnapshot.recent_events, per
 * Milestone 2/6's doc comments) -- sorted chronologically (event ids are
 * ULIDs, so a plain string sort is a correct chronological sort) and
 * stepped through locally.
 *
 * Each step re-applies multiagent-live.js's derivePatches (the exact same
 * pure event -> patch function the live SSE reducer uses) to an isolated,
 * plain-object "replay state" that mirrors one DashboardRunSnapshot's shape
 * -- never to the live DOM or to multiagent-run.html's own `lastSnapshot`.
 * This module deliberately takes derivePatches as a parameter rather than
 * requiring multiagent-live.js directly, so it has no load-order dependency
 * (mirrors multiagent-inspector.js's factory-injection pattern for
 * multiagent-graph.js/multiagent-live.js) and so tests can substitute a
 * stub.
 *
 * Known, deliberate limitation (see buildReplayStateAtStep's doc comment
 * and multiagent-live.js's derivePatches comments): a few event types only
 * do dedup/bookkeeping in the live reducer and rely on a full-snapshot
 * reconciliation to reflect their true effect -- e.g. handoff.created,
 * budget.warning, and loop.traversal.recorded return an EMPTY patch list.
 * Live mode is fine because a debounced snapshot re-fetch corrects it
 * moments later; replay has no per-step "ask the backend for ground truth
 * at this point in history" endpoint (out of scope for this milestone), so
 * mid-replay state for the fields those events would affect (budget
 * dimension usage/level, the handoff panel, exact edge traversal counts)
 * can lag behind what actually happened at that historical instant. This
 * self-corrects the moment replay reaches "jump to end", which renders the
 * REAL final snapshot rather than the patch-accumulated state -- see
 * buildReplayStateAtStep's doc comment and multiagent-run.html's
 * replayJumpToEnd.
 */
(function (root) {
  'use strict';

  // ---- Terminal run-status vocabulary -------------------------------------
  // Mirrors internal/workflow/multiagent/types.go's RunStatus.Terminal()
  // exactly: replay is only ever offered for a run that has reached one of
  // these -- an active/paused/waiting run has no "final state" yet, and
  // more live events could still arrive for it.
  var TERMINAL_RUN_STATUSES = ['completed', 'failed', 'cancelled', 'budget_exhausted'];

  function isTerminalRunStatus(status) {
    return TERMINAL_RUN_STATUSES.indexOf(status) !== -1;
  }

  // BASELINE_RUN_STATUS is a real, valid RunStatus value (RunStatusCreated
  // in the Go source) used for the seeded "nothing has happened yet" replay
  // state -- not an invented status string.
  var BASELINE_RUN_STATUS = 'created';

  function cloneJSON(value) {
    return value == null ? value : JSON.parse(JSON.stringify(value));
  }

  // ---- Baseline (step 0) state construction --------------------------------

  // zeroBudgetDimension resets one BudgetVisualization dimension's `used`
  // counter to 0 while preserving its `limit` (a static workflow-definition
  // ceiling, known upfront -- not itself derived from runtime events, so
  // carrying it forward is not "duplicating backend threshold logic", it is
  // restating a config value verbatim). `level` is left unset deliberately:
  // multiagent-graph.js's buildBudgetViewModel already defaults a missing
  // level to 'normal' (dim.level || 'normal'), so this reuses that existing
  // fallback instead of this module inventing its own severity judgement.
  function zeroBudgetDimension(sourceDim) {
    sourceDim = sourceDim || {};
    var dim = { used: 0 };
    if (sourceDim.limit != null) dim.limit = sourceDim.limit;
    return dim;
  }

  function buildInitialBudgets(sourceBudgets) {
    sourceBudgets = sourceBudgets || {};
    var out = {
      transitions: zeroBudgetDimension(sourceBudgets.transitions),
      tokens: zeroBudgetDimension(sourceBudgets.tokens),
      local_iterations: zeroBudgetDimension(sourceBudgets.local_iterations),
      repeated_failures: zeroBudgetDimension(sourceBudgets.repeated_failures),
      elapsed: zeroBudgetDimension(sourceBudgets.elapsed),
      role_visits: {},
      loop_traversals: {}
    };
    Object.keys(sourceBudgets.role_visits || {}).forEach(function (role) {
      out.role_visits[role] = zeroBudgetDimension(sourceBudgets.role_visits[role]);
    });
    Object.keys(sourceBudgets.loop_traversals || {}).forEach(function (loop) {
      out.loop_traversals[loop] = zeroBudgetDimension(sourceBudgets.loop_traversals[loop]);
    });
    return out;
  }

  /**
   * buildInitialReplayState derives the seeded "nothing has happened yet"
   * replay baseline from one (terminal) DashboardRunSnapshot: every role
   * node not_started, every edge available, no current node, no active
   * edge, all counters zero. The node/edge SHAPE itself (which ids exist,
   * their labels, max_visits/max_traversals ceilings) is taken verbatim
   * from snapshot.graph -- this only resets the dynamic/mutable fields, it
   * never re-derives the topology the backend already computed.
   */
  function buildInitialReplayState(snapshot) {
    snapshot = snapshot || {};
    var graph = snapshot.graph || {};

    var nodes = (graph.nodes || []).map(function (n) {
      return {
        id: n.id,
        kind: n.kind,
        role: n.role || '',
        terminal: n.terminal || '',
        label: n.label || n.id,
        status: 'not_started',
        is_current: false,
        visits: 0,
        local_iterations: 0,
        max_visits: n.max_visits != null ? n.max_visits : null,
        max_local_iterations: n.max_local_iterations != null ? n.max_local_iterations : null
      };
    });

    var edges = (graph.edges || []).map(function (e) {
      return {
        id: e.id,
        from: e.from,
        to: e.to || '',
        terminal: e.terminal || '',
        outcome: e.outcome,
        label: e.label || '',
        status: 'available',
        is_loopback: !!e.is_loopback,
        traversal_count: 0,
        max_traversals: e.max_traversals != null ? e.max_traversals : null
      };
    });

    return {
      schema_version: snapshot.schema_version || 1,
      run_id: snapshot.run_id || '',
      workflow_id: snapshot.workflow_id || graph.workflow_id || '',
      workflow_version: snapshot.workflow_version || graph.workflow_version || 0,
      status: BASELINE_RUN_STATUS,
      current_role: '',
      completed_roles: [],
      latest_handoff: null,
      role_states: {},
      transition_count: 0,
      loop_traversals: {},
      waiting: null,
      cancellation_reason: '',
      terminal_outcome: null,
      budgets: buildInitialBudgets(snapshot.budgets),
      graph: {
        workflow_id: graph.workflow_id || '',
        workflow_version: graph.workflow_version || 0,
        current_node_id: '',
        active_edge_id: '',
        run_paused: false,
        budget_exhausted: false,
        nodes: nodes,
        edges: edges
      },
      recent_events: []
    };
  }

  // ---- Chronological ordering ----------------------------------------------
  // Event ids are ULIDs -- lexicographic string sort is a correct
  // chronological sort. Ascending (oldest first), the opposite order from
  // multiagent-inspector.js's sortEventsNewestFirst (which the timeline
  // panel uses), since replay steps forward through history in the order it
  // actually happened.
  function sortEventsChronological(events) {
    return (events || []).slice().sort(function (a, b) {
      var idA = (a && a.id) || '';
      var idB = (b && b.id) || '';
      if (idA === idB) return 0;
      return idA < idB ? -1 : 1;
    });
  }

  // ---- Patch application (plain-object analogue of multiagent-run.html's
  // DOM-coupled applyPatches/applyGraphNode/applyEdgeActive/etc.) -----------

  function findNode(graph, nodeId) {
    var nodes = (graph && graph.nodes) || [];
    for (var i = 0; i < nodes.length; i++) {
      if (nodes[i].id === nodeId) return nodes[i];
    }
    return null;
  }

  function findEdge(graph, edgeId) {
    var edges = (graph && graph.edges) || [];
    for (var i = 0; i < edges.length; i++) {
      if (edges[i].id === edgeId) return edges[i];
    }
    return null;
  }

  // setCurrentNode restates RunGraph.CurrentNodeID's single-valued nature
  // directly from the patch (which always names exactly one node id) --
  // never a guess, since only one node can be "current" at a time by
  // construction.
  function setCurrentNode(graph, nodeId) {
    (graph.nodes || []).forEach(function (n) { n.is_current = (n.id === nodeId); });
    graph.current_node_id = nodeId;
  }

  /**
   * applyPatchToReplayState mutates `state` (a buildInitialReplayState()-
   * shaped object) in place per one multiagent-live.js derivePatches()
   * patch descriptor. Same patch vocabulary and the same "never re-derive a
   * threshold-based GraphNode/GraphEdge status" discipline as the DOM-
   * coupled live reducer, just writing into a plain JS object instead of
   * live DOM nodes.
   *
   * Note (edgeActive): mirrors the live DOM reducer's applyEdgeActive
   * exactly, including its own known limitation -- when a new edge becomes
   * active, the *previously* active edge is intentionally left marked
   * 'active' rather than guessed at, because its true next status
   * (previously_traversed vs. exhausted) depends on that edge's own
   * traversal_count/max_traversals, which this event's payload does not
   * carry. Live mode's debounced reconciliation corrects this within
   * ~250ms; replay has no equivalent per-step correction, so this can be
   * visible for longer mid-replay. It always resolves correctly at jump-to-
   * end (the real final snapshot), which is the same self-correction
   * boundary the module doc comment above describes for handoff.created /
   * budget.warning / loop.traversal.recorded.
   */
  function applyPatchToReplayState(state, patch) {
    switch (patch.kind) {
      case 'runStatus':
        state.status = patch.status;
        break;

      case 'overlay':
        if (patch.overlay === 'paused') state.graph.run_paused = !!patch.active;
        else if (patch.overlay === 'exhausted') state.graph.budget_exhausted = !!patch.active;
        break;

      case 'terminalOutcome':
        state.terminal_outcome = { condition: patch.condition, reason: patch.reason || '', at: patch.at || '' };
        break;

      case 'graphNode': {
        var node = findNode(state.graph, patch.nodeId);
        if (!node) break;
        if (patch.status) node.status = patch.status;
        if (typeof patch.visits === 'number') node.visits = patch.visits;
        if (patch.isCurrent) setCurrentNode(state.graph, patch.nodeId);
        break;
      }

      case 'roleLocalIterations': {
        var iterNode = findNode(state.graph, patch.nodeId);
        if (iterNode) iterNode.local_iterations = patch.iteration;
        break;
      }

      case 'edgeActive': {
        var edge = findEdge(state.graph, patch.edgeId);
        if (edge) {
          edge.status = 'active';
          state.graph.active_edge_id = patch.edgeId;
        }
        break;
      }

      case 'nodeCurrentFlag':
        setCurrentNode(state.graph, patch.nodeId);
        break;

      default:
        break;
    }
  }

  /**
   * buildReplayStateAtStep computes the replay state after exactly
   * `stepIndex` chronologically-sorted events have been applied (0 = the
   * seeded "nothing has happened yet" baseline; sortedEvents.length = every
   * event applied). Always recomputes forward from `initialState` -- no
   * reverse/inverse patches, no cached intermediate snapshots -- since a
   * bounded reference-workflow run's event count is small and this is a
   * pure in-memory array walk, not I/O; caching would be premature
   * optimization for this milestone's scope.
   *
   * `derivePatchesFn` is injected (multiagent-run.html passes
   * multiagent-live.js's derivePatches) rather than required at module load
   * time, matching multiagent-inspector.js's dependency-injection pattern
   * and keeping this module free-standing for tests.
   *
   * The result of walking every event via patches is NOT the same thing as
   * the run's real authoritative final snapshot (see the module doc
   * comment's "known limitation" section) -- callers that want the true end
   * state (e.g. a "jump to end" control) should render the real final
   * snapshot directly instead of calling this with stepIndex ==
   * sortedEvents.length.
   */
  function buildReplayStateAtStep(initialState, sortedEvents, stepIndex, derivePatchesFn) {
    var events = sortedEvents || [];
    var clamped = Math.max(0, Math.min(typeof stepIndex === 'number' ? stepIndex : 0, events.length));
    var state = cloneJSON(initialState) || {};
    if (!state.graph) state.graph = { nodes: [], edges: [] };

    for (var i = 0; i < clamped; i++) {
      var evt = events[i] || {};
      var payload = evt.payload || {};
      var patches = derivePatchesFn(evt.type, payload, { timestamp: evt.timestamp || '' }) || [];
      for (var p = 0; p < patches.length; p++) {
        applyPatchToReplayState(state, patches[p]);
      }
    }

    state.recent_events = events.slice(0, clamped);
    return state;
  }

  // ---- Playback speed -------------------------------------------------
  // Plain data: given a speed multiplier, compute the millisecond interval
  // between auto-advance steps. 1x is a comfortable "one event roughly
  // every 1.2s" pace for a small reference-workflow run; other multipliers
  // scale that base interval directly.
  var SPEED_OPTIONS = [0.5, 1, 2, 4];
  var BASE_STEP_INTERVAL_MS = 1200;

  function intervalForSpeed(speed) {
    var s = typeof speed === 'number' && speed > 0 ? speed : 1;
    return Math.round(BASE_STEP_INTERVAL_MS / s);
  }

  var api = {
    TERMINAL_RUN_STATUSES: TERMINAL_RUN_STATUSES,
    isTerminalRunStatus: isTerminalRunStatus,
    BASELINE_RUN_STATUS: BASELINE_RUN_STATUS,
    buildInitialBudgets: buildInitialBudgets,
    buildInitialReplayState: buildInitialReplayState,
    sortEventsChronological: sortEventsChronological,
    applyPatchToReplayState: applyPatchToReplayState,
    buildReplayStateAtStep: buildReplayStateAtStep,
    SPEED_OPTIONS: SPEED_OPTIONS,
    BASE_STEP_INTERVAL_MS: BASE_STEP_INTERVAL_MS,
    intervalForSpeed: intervalForSpeed
  };

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  } else {
    root.PrizmMultiAgentReplay = api;
  }
})(typeof window !== 'undefined' ? window : globalThis);
