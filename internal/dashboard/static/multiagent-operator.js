/*
 * Pure (DOM-free) view-model logic for Milestone 9 Part B of the multi-agent
 * run page (internal/dashboard/static/multiagent-run.html): operator
 * control-bar gating (pause/resume/cancel visibility+enablement) and the
 * loop-budget stall / near-exhaustion visualization (edge id mapping,
 * remaining-attempts text, and the authoritative reconciliation-derived
 * near-exhausted set).
 *
 * Like multiagent-graph.js / multiagent-live.js / multiagent-inspector.js /
 * multiagent-replay.js, this file has no import/export syntax so it works
 * unmodified as a plain browser <script> (window.PrizmMultiAgentOperator)
 * AND as a CommonJS module for the plain-node tests under
 * internal/dashboard/static/tests/ (no npm, no build step). It depends on
 * multiagent-live.js (edgeIdFor, the stable edge id builder) and
 * multiagent-replay.js (isTerminalRunStatus, which already mirrors
 * internal/workflow/multiagent/types.go's RunStatus.Terminal() exactly)
 * rather than re-deriving either, matching multiagent-inspector.js's
 * dependency-injection convention.
 *
 * Design principle carried over from every other pure module in this
 * feature: this file never invents a severity/threshold judgement. The
 * "near exhausted" edge set is derived from one of two sources, and both
 * only ever restate a value the backend already computed or the event
 * payload already carries verbatim:
 *   - deriveNearExhaustedEdges reads DIRECTLY off
 *     snapshot.budgets.loop_traversals[*].level -- the exact same
 *     server-computed severity field buildBudgetViewModel/budgetWarnings
 *     already trust elsewhere, never a locally re-derived threshold.
 *   - remainingCount reads DIRECTLY off one EventMultiAgentBudgetWarning
 *     event's own used/limit fields (verified against
 *     internal/event/multiagent.go's MultiAgentBudgetEventPayload and
 *     durable_budget_warning_test.go's exact payload assertions:
 *     payload.budget === "tester_to_developer_loop" /
 *     "reviewer_to_developer_loop").
 */
(function (root, factory) {
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = factory(require('./multiagent-live.js'), require('./multiagent-replay.js'));
  } else {
    root.PrizmMultiAgentOperator = factory(root.PrizmMultiAgentLive, root.PrizmMultiAgentReplay);
  }
})(typeof window !== 'undefined' ? window : globalThis, function (live, replay) {
  'use strict';

  // ---- Operator control bar gating ---------------------------------------
  //
  // internal/workflow/multiagent/types.go's RunStatus has no separate
  // "waiting" value: an operator_pause/approval/execution_reconciliation
  // wait is always represented as RunStatus "paused" (see
  // durable_runtime.go's pauseForApproval / the operator-pause path, which
  // both set record.State.Status = RunStatusPaused before setting
  // record.Waiting). So "paused" alone is both necessary and sufficient to
  // gate Resume -- DurableRuntime.Resume handles every wait kind uniformly
  // once the run is in that status, so this never needs to branch on
  // `waiting.kind` to decide button gating.
  var RUN_STATUS_RUNNING = 'running';
  var RUN_STATUS_PAUSED = 'paused';

  var REPLAY_BLOCK_REASON = 'Operator controls are unavailable while viewing replay mode — actions only ever apply to the live run, never to a historical replay view.';
  var UNAVAILABLE_BLOCK_REASON = 'Operator controls are unavailable: this run is no longer reachable.';

  /**
   * computeOperatorControlsState decides, purely from a DashboardRunSnapshot-
   * shaped object's `status` field plus the page's own replay/unavailable
   * flags, which of the three operator-control buttons should be visible and
   * enabled right now. Returns { pause, resume, cancel, blockedReason },
   * where each button entry is { visible, enabled, disabledReason }.
   */
  function computeOperatorControlsState(snapshot, replayActive, runUnavailable) {
    var status = (snapshot && snapshot.status) || '';
    var terminal = replay.isTerminalRunStatus(status);
    var blockedReason = replayActive ? REPLAY_BLOCK_REASON : (runUnavailable ? UNAVAILABLE_BLOCK_REASON : null);

    function entry(visible, unmetReason) {
      visible = !!visible;
      var enabled = visible && !blockedReason;
      return {
        visible: visible,
        enabled: enabled,
        disabledReason: enabled ? null : (blockedReason || (visible ? unmetReason : null))
      };
    }

    return {
      pause: entry(!terminal && status === RUN_STATUS_RUNNING, 'Pause is only available while the run is actively running.'),
      resume: entry(!terminal && status === RUN_STATUS_PAUSED, 'Resume is only available while the run is paused or waiting.'),
      cancel: entry(!terminal, null),
      blockedReason: blockedReason
    };
  }

  // ---- Waiting-state kind labels ------------------------------------------
  //
  // WaitingState.Kind is a bare backend string ("operator_pause" /
  // "approval" / "execution_reconciliation", per
  // internal/workflow/multiagent/durable_runtime.go). This is presentation
  // only -- it never changes behavior -- but a short purpose-built label
  // reads more clearly than the raw kind string, so known kinds get one. An
  // unrecognized future kind still renders sensibly (a humanized version of
  // the raw string) rather than crashing or showing nothing.
  var WAITING_KIND_LABELS = {
    operator_pause: 'Paused by operator',
    approval: 'Waiting on approval',
    execution_reconciliation: 'Reconciling execution state'
  };

  function waitingKindLabel(kind) {
    if (!kind) return 'Unknown';
    return WAITING_KIND_LABELS[kind] || String(kind).replace(/_/g, ' ');
  }

  // ---- Loop-budget stall / near-exhaustion visualization -----------------
  //
  // The only two BudgetExceededError.Budget / EventMultiAgentBudgetWarning
  // `budget` payload values with an unambiguous mapping to a single graph
  // edge (verified against durable_budget_warning_test.go's exact payload
  // assertions and multiagent-graph.js's EDGE_ANCHORS: the tester
  // "tests_failed" and reviewer "changes_requested" correction loops).
  // Every other budget dimension (role visits, local iterations, ...) has
  // no such mapping and is intentionally left alone, matching
  // multiagent-live.js's own BUDGET_WARNING doc comment.
  var LOOP_BUDGET_EDGE_SOURCE = {
    tester_to_developer_loop: { role: 'tester', outcome: 'tests_failed' },
    reviewer_to_developer_loop: { role: 'reviewer', outcome: 'changes_requested' }
  };

  function budgetWarningEdgeId(payload) {
    payload = payload || {};
    var mapping = LOOP_BUDGET_EDGE_SOURCE[payload.budget];
    return mapping ? live.edgeIdFor(mapping.role, mapping.outcome) : null;
  }

  function remainingCount(used, limit) {
    if (typeof used !== 'number' || typeof limit !== 'number') return null;
    return Math.max(0, limit - used);
  }

  /**
   * remainingAttemptsText renders the exact wording this milestone's spec
   * asks for ("1 attempt remaining"), always computed as limit - used --
   * never hardcoded, never a threshold judgement of its own.
   */
  function remainingAttemptsText(remaining) {
    if (remaining == null || Number.isNaN(remaining)) return '';
    return remaining === 1 ? '1 attempt remaining' : remaining + ' attempts remaining';
  }

  // A precomputed loopKey -> edgeId table. budgets.loop_traversals' own key
  // vocabulary ("tester_to_developer" / "reviewer_to_developer", per
  // multiagent-graph.js's LOOP_LABELS) is distinct from the *_loop-suffixed
  // budget.warning event payload's `budget` string handled by
  // LOOP_BUDGET_EDGE_SOURCE above -- two different backend vocabularies for
  // the same two loops, both mapped here to the one real edge id each.
  var LOOP_KEY_EDGE_ID = {
    tester_to_developer: live.edgeIdFor('tester', 'tests_failed'),
    reviewer_to_developer: live.edgeIdFor('reviewer', 'changes_requested')
  };

  var NEAR_EXHAUSTED_LEVELS = { approaching: true, critical: true };

  /**
   * deriveNearExhaustedEdges is the authoritative, self-correcting source of
   * the near-exhausted edge set: derived fresh on every full snapshot
   * render from snapshot.budgets.loop_traversals, trusting only the
   * server-computed `level` field (never re-deriving a threshold from
   * used/limit itself). Returns a plain { edgeId: remainingCountOrNull }
   * map. A loop dimension already at 'exhausted' level is deliberately
   * excluded -- that already has its own distinct, pre-existing
   * `.ma-edge--exhausted` treatment (GraphEdge.Status), and this
   * milestone's spec is additive to that, not a replacement for it.
   */
  function deriveNearExhaustedEdges(loopTraversals) {
    loopTraversals = loopTraversals || {};
    var out = {};
    Object.keys(loopTraversals).forEach(function (loopKey) {
      var dim = loopTraversals[loopKey] || {};
      var edgeId = LOOP_KEY_EDGE_ID[loopKey];
      if (!edgeId || !NEAR_EXHAUSTED_LEVELS[dim.level]) return;
      out[edgeId] = remainingCount(dim.used, dim.limit);
    });
    return out;
  }

  // ---- aria-live announcement dedup ---------------------------------------
  //
  // createAnnounceTracker/announceDeltas mirror multiagent-live.js's
  // createStreamState/shouldApply+markApplied bookkeeping pattern (an
  // explicit, caller-owned plain object, never module-level shared state):
  // only report an edge the FIRST time it becomes near-exhausted, or when
  // its remaining-count actually changes -- never on every reconciliation
  // tick that happens to still find it near-exhausted at the same count. An
  // edge that drops out of the near-exhausted set (e.g. after a full
  // reconciliation shows the loop is no longer approaching/critical) is
  // removed from the tracker so a later re-entry announces again.
  function createAnnounceTracker() {
    return {};
  }

  function announceDeltas(tracker, nearExhaustedMap) {
    tracker = tracker || {};
    nearExhaustedMap = nearExhaustedMap || {};
    var changed = [];
    Object.keys(nearExhaustedMap).forEach(function (edgeId) {
      var remaining = nearExhaustedMap[edgeId];
      if (tracker[edgeId] !== remaining) {
        changed.push({ edgeId: edgeId, remaining: remaining });
        tracker[edgeId] = remaining;
      }
    });
    Object.keys(tracker).forEach(function (edgeId) {
      if (!(edgeId in nearExhaustedMap)) delete tracker[edgeId];
    });
    return changed;
  }

  var api = {
    computeOperatorControlsState: computeOperatorControlsState,
    waitingKindLabel: waitingKindLabel,
    budgetWarningEdgeId: budgetWarningEdgeId,
    remainingCount: remainingCount,
    remainingAttemptsText: remainingAttemptsText,
    deriveNearExhaustedEdges: deriveNearExhaustedEdges,
    createAnnounceTracker: createAnnounceTracker,
    announceDeltas: announceDeltas
  };

  return api;
});
