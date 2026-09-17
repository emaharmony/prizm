/*
 * Pure (DOM-free) live-event reducer logic for the multi-agent run page
 * (internal/dashboard/static/multiagent-run.html), Milestone 4.
 *
 * Like multiagent-graph.js, this file has no import/export syntax so it
 * works unmodified as a plain browser <script> (window.PrizmMultiAgentLive)
 * AND as a CommonJS module for the plain-node tests under
 * internal/dashboard/static/tests/ (no npm, no build step).
 *
 * Design principle: the graph UI is a projection of canonical runtime
 * state, never a second source of truth. This module NEVER re-derives
 * GraphNode.Status / GraphEdge.Status / Budget.Level from thresholds or
 * business logic — every patch either restates a field the event payload
 * already carries verbatim, or a structural fact implied by definition
 * (e.g. "this is now THE active edge" because RunGraph.ActiveEdgeID is, by
 * construction, the edge id of the most recently selected transition). Any
 * event type without a clean, unambiguous per-event patch returns an empty
 * patch list; the page's debounced full-snapshot reconciliation (calling
 * window.applyMultiAgentSnapshot) is the safety net that guarantees the UI
 * never permanently drifts from backend-derived truth.
 */
(function (root) {
  'use strict';

  // ---- Canonical event type vocabulary -----------------------------------
  // String values copied verbatim from internal/event/multiagent.go. Do not
  // reformat/rename without updating that file's constants too.
  var EVENT_TYPES = {
    RUN_CREATED:              'prizm.workflow.multi_agent.run.created',
    RUN_STARTED:              'prizm.workflow.multi_agent.run.started',
    ROLE_ENTERED:             'prizm.workflow.multi_agent.role.entered',
    ROLE_ITERATION_STARTED:   'prizm.workflow.multi_agent.role.iteration.started',
    ROLE_ITERATION_COMPLETED: 'prizm.workflow.multi_agent.role.iteration.completed',
    ROLE_COMPLETED:           'prizm.workflow.multi_agent.role.completed',
    HANDOFF_CREATED:          'prizm.workflow.multi_agent.handoff.created',
    TRANSITION_SELECTED:      'prizm.workflow.multi_agent.transition.selected',
    LOOP_TRAVERSAL_RECORDED:  'prizm.workflow.multi_agent.loop.traversal.recorded',
    BUDGET_WARNING:           'prizm.workflow.multi_agent.budget.warning',
    BUDGET_EXHAUSTED:         'prizm.workflow.multi_agent.budget.exhausted',
    RUN_PAUSED:               'prizm.workflow.multi_agent.run.paused',
    RUN_RESUMED:              'prizm.workflow.multi_agent.run.resumed',
    RUN_COMPLETED:            'prizm.workflow.multi_agent.run.completed',
    RUN_FAILED:               'prizm.workflow.multi_agent.run.failed',
    RUN_CANCELLED:            'prizm.workflow.multi_agent.run.cancelled',
    RECOVERY_STARTED:         'prizm.workflow.multi_agent.recovery.started',
    RECOVERY_COMPLETED:       'prizm.workflow.multi_agent.recovery.completed',
    RECOVERY_FAILED:          'prizm.workflow.multi_agent.recovery.failed'
  };

  var ALL_EVENT_TYPES = Object.keys(EVENT_TYPES).map(function (key) { return EVENT_TYPES[key]; });

  // ---- Stable id builders -------------------------------------------------
  // Mirror graph_projection.go's nodeID/terminalNodeID/edgeID exactly so
  // patches target the same DOM ids M3's renderer assigns from
  // graph.nodes[].id / graph.edges[].id (verified by multiagent-graph.test.js).
  function roleNodeId(role) { return 'node:' + role; }
  function terminalNodeId(condition) { return 'node:terminal:' + condition; }
  function edgeIdFor(sourceRole, outcome) { return 'edge:' + sourceRole + ':' + outcome; }

  // Milestone 9b: the only two BudgetExceededError.Budget /
  // EventMultiAgentBudgetWarning `budget` payload values with an
  // unambiguous mapping to a single graph edge (verified against
  // durable_budget_warning_test.go's exact payload assertions: warning
  // payload.budget === "tester_to_developer_loop" /
  // "reviewer_to_developer_loop"). Every other budget dimension (role
  // visits, local iterations, ...) still has no such mapping -- see the
  // BUDGET_WARNING case below, unchanged from Milestone 4 for those.
  var LOOP_BUDGET_TO_EDGE = {
    tester_to_developer_loop: edgeIdFor('tester', 'tests_failed'),
    reviewer_to_developer_loop: edgeIdFor('reviewer', 'changes_requested')
  };

  // ---- Dedup / cursor bookkeeping -----------------------------------------

  function createStreamState() {
    return { seen: new Set(), cursor: '' };
  }

  // shouldApply reports whether event id should be processed: not already
  // seen, and not lexicographically <= the current cursor (ULIDs sort
  // correctly as plain strings). Ids without a value (e.g. "connected" /
  // "heartbeat" framing, which carry no id: line) are never applied.
  function shouldApply(state, id) {
    if (!id) return false;
    if (state.seen.has(id)) return false;
    if (state.cursor && id <= state.cursor) return false;
    return true;
  }

  // markApplied records id as seen and advances the cursor if id is newer.
  function markApplied(state, id) {
    state.seen.add(id);
    if (!state.cursor || id > state.cursor) state.cursor = id;
  }

  // ---- Patch derivation -----------------------------------------------

  /**
   * derivePatches maps one canonical multi-agent event (type + payload) to a
   * small list of narrow, unambiguous DOM patch descriptors. meta.timestamp
   * is the event envelope's own `timestamp` field (used only for the
   * terminal-outcome patch's `at`).
   */
  function derivePatches(eventType, payload, meta) {
    payload = payload || {};
    meta = meta || {};
    var T = EVENT_TYPES;
    var patches = [];

    function addRunStatus() {
      if (payload.status) patches.push({ kind: 'runStatus', status: payload.status });
    }

    function addTerminalOutcomeIfPresent() {
      if (!payload.terminal_condition) return;
      patches.push({
        kind: 'terminalOutcome',
        condition: payload.terminal_condition,
        reason: payload.reason || '',
        at: meta.timestamp || ''
      });
      patches.push({
        kind: 'graphNode',
        nodeId: terminalNodeId(payload.terminal_condition),
        status: payload.terminal_condition,
        isCurrent: true
      });
    }

    switch (eventType) {
      case T.RUN_CREATED:
      case T.RUN_STARTED:
      case T.RECOVERY_STARTED:
        addRunStatus();
        break;

      case T.RECOVERY_COMPLETED:
      case T.RECOVERY_FAILED:
        addRunStatus();
        addTerminalOutcomeIfPresent();
        break;

      case T.ROLE_ENTERED: {
        if (!payload.role) break;
        var enteredPatch = { kind: 'graphNode', nodeId: roleNodeId(payload.role), status: 'running', isCurrent: true };
        if (typeof payload.visit === 'number') enteredPatch.visits = payload.visit;
        patches.push(enteredPatch);
        break;
      }

      case T.ROLE_ITERATION_STARTED:
      case T.ROLE_ITERATION_COMPLETED:
        if (payload.role && typeof payload.iteration === 'number') {
          patches.push({ kind: 'roleLocalIterations', nodeId: roleNodeId(payload.role), iteration: payload.iteration });
        }
        break;

      case T.ROLE_COMPLETED:
        if (payload.role) {
          patches.push({ kind: 'graphNode', nodeId: roleNodeId(payload.role), status: 'completed' });
        }
        break;

      case T.HANDOFF_CREATED:
        // No handoff UI exists in M3's renderer to patch narrowly (dedup
        // via shouldApply/markApplied still happens in the caller);
        // reconciliation is the sole source of truth for this event's
        // effect.
        break;

      case T.TRANSITION_SELECTED:
        if (payload.source_role && payload.outcome) {
          patches.push({ kind: 'edgeActive', edgeId: edgeIdFor(payload.source_role, payload.outcome) });
          if (payload.destination_role) {
            patches.push({ kind: 'nodeCurrentFlag', nodeId: roleNodeId(payload.destination_role) });
          } else if (payload.terminal_condition) {
            patches.push({ kind: 'nodeCurrentFlag', nodeId: terminalNodeId(payload.terminal_condition) });
          }
        }
        break;

      case T.LOOP_TRAVERSAL_RECORDED:
        // Fires alongside transition.selected for the same edge (see
        // supervisor.go's applyTransition). transitionPayload only carries
        // the run-level transition_count, not this edge's own new
        // traversal_count/max, so the traversal number/level update is
        // reconciliation-only; the edge-active highlight is already covered
        // by the sibling transition.selected event.
        break;

      case T.BUDGET_WARNING: {
        // BudgetExceededError.Budget values (e.g. "max_visits_per_role",
        // "role_max_local_iterations") have no documented mapping anywhere
        // in the backend to BudgetVisualization's dimension keys
        // ("transitions", "tokens", "role_visits:<role>",
        // "loop_traversals:<loop>", ...). Inventing one here would risk
        // silently mislabeling a budget row, so those remain
        // reconciliation-only, unchanged from Milestone 4.
        //
        // Milestone 9b: the two tester/reviewer correction-loop budget
        // values ARE an unambiguous, well-defined single-edge mapping
        // though (see LOOP_BUDGET_TO_EDGE above), so those get a narrow,
        // additive patch that only restates the event's own
        // budget/used/limit fields verbatim -- never a computed severity
        // threshold (the edge's true near-exhaustion *level* is still only
        // trusted from the next reconciliation's
        // budgets.loop_traversals[*].level, per
        // multiagent-operator.js's deriveNearExhaustedEdges).
        var nearEdgeId = LOOP_BUDGET_TO_EDGE[payload.budget];
        if (nearEdgeId && typeof payload.used === 'number' && typeof payload.limit === 'number') {
          patches.push({
            kind: 'edgeNearExhausted',
            edgeId: nearEdgeId,
            used: payload.used,
            limit: payload.limit,
            remaining: Math.max(0, payload.limit - payload.used)
          });
        }
        break;
      }

      case T.BUDGET_EXHAUSTED:
        // Same per-dimension ambiguity as budget.warning for the row
        // itself, but the event's own type unambiguously means the
        // run-level "budget exhausted" overlay (RunGraph.BudgetExhausted)
        // is now true — that much is a direct restatement of the event
        // type, not a threshold computation.
        patches.push({ kind: 'overlay', overlay: 'exhausted', active: true });
        break;

      case T.RUN_PAUSED:
        addRunStatus();
        patches.push({ kind: 'overlay', overlay: 'paused', active: true });
        break;

      case T.RUN_RESUMED:
        addRunStatus();
        patches.push({ kind: 'overlay', overlay: 'paused', active: false });
        break;

      case T.RUN_COMPLETED:
      case T.RUN_FAILED:
      case T.RUN_CANCELLED:
        addRunStatus();
        addTerminalOutcomeIfPresent();
        break;

      default:
        break;
    }

    return patches;
  }

  // ---- Milestone 7: "deleted/unavailable run" resilience -----------------
  //
  // The debounced reconciliation fetch (multiagent-run.html's reconcile())
  // is the one place that already distinguishes a genuine 404 (the run's
  // manifest/database no longer exist -- see writeMultiAgentRunError in
  // internal/api/server.go, which maps os.ErrNotExist to 404 and everything
  // else to 500) from any other failure (network blip, 500, 503). Per the
  // spec: a single failed fetch must never be treated as permanent -- only
  // a run that keeps coming back 404, specifically, on consecutive attempts
  // is genuinely gone. Any non-404 failure resets the streak to 0, since it
  // says nothing about whether the run still exists.
  var NOT_FOUND_UNAVAILABLE_THRESHOLD = 2;

  // isNotFoundErrorMessage matches the exact "(<status>)" suffix
  // app.js's request() appends to a thrown Error's message (see
  // multiagent-run.html's own classifyError, which uses the same pattern
  // for the initial-load 404 case).
  function isNotFoundErrorMessage(message) {
    return /\(404\)/.test(message || '');
  }

  // nextNotFoundStreak folds one reconciliation attempt's outcome into the
  // running consecutive-404 count.
  function nextNotFoundStreak(previousStreak, errorMessage) {
    return isNotFoundErrorMessage(errorMessage) ? (previousStreak || 0) + 1 : 0;
  }

  // shouldTreatAsUnavailable reports whether the run should now be treated
  // as permanently gone: banner shown, live updates stopped, no further
  // automatic retries (a manual page reload is the only way back, matching
  // the initial-load 404 page's own messaging).
  function shouldTreatAsUnavailable(streak) {
    return (streak || 0) >= NOT_FOUND_UNAVAILABLE_THRESHOLD;
  }

  // ---- Milestone 7: onopen double-reconciliation guard --------------------
  //
  // EventSource fires onopen identically for the very first successful
  // connect and for every later reconnect -- the API gives no way to tell
  // them apart directly. shouldReconcileOnOpen is the pure decision the
  // caller folds its own hasOpenedOnce bookkeeping through: skip on the
  // first open (load() just fetched a fresh snapshot moments ago, so
  // reconciling again immediately would be a redundant, wasted fetch on
  // every single page load), reconcile on every open after that (a
  // reconnect can follow a backend restart with partially durable in-flight
  // events, and only a full snapshot fetch can safely resynchronize after
  // that).
  function shouldReconcileOnOpen(hasOpenedOnce) {
    return !!hasOpenedOnce;
  }

  var api = {
    EVENT_TYPES: EVENT_TYPES,
    ALL_EVENT_TYPES: ALL_EVENT_TYPES,
    roleNodeId: roleNodeId,
    terminalNodeId: terminalNodeId,
    edgeIdFor: edgeIdFor,
    createStreamState: createStreamState,
    shouldApply: shouldApply,
    markApplied: markApplied,
    derivePatches: derivePatches,
    NOT_FOUND_UNAVAILABLE_THRESHOLD: NOT_FOUND_UNAVAILABLE_THRESHOLD,
    isNotFoundErrorMessage: isNotFoundErrorMessage,
    nextNotFoundStreak: nextNotFoundStreak,
    shouldTreatAsUnavailable: shouldTreatAsUnavailable,
    shouldReconcileOnOpen: shouldReconcileOnOpen
  };

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  } else {
    root.PrizmMultiAgentLive = api;
  }
})(typeof window !== 'undefined' ? window : globalThis);
