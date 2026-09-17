/*
 * Pure (DOM-free) view-model logic for Milestone 6 of the multi-agent run
 * page (internal/dashboard/static/multiagent-run.html): the click-to-inspect
 * Run/Node/Edge/Handoff inspector panel and the event timeline.
 *
 * Like multiagent-graph.js and multiagent-live.js, this file has no
 * import/export syntax so it works unmodified as a plain browser <script>
 * (window.PrizmMultiAgentInspector) AND as a CommonJS module for the plain-
 * node tests under internal/dashboard/static/tests/ (no npm, no build step).
 * It depends on multiagent-graph.js (humanize, COUNTER_LABELS) and
 * multiagent-live.js (the stable node/edge id builders + the canonical event
 * type vocabulary) rather than re-deriving either.
 *
 * Design principle carried over from Milestone 4: this module never
 * fabricates data. Every inspector field either restates a value the
 * DashboardRunSnapshot/ReferenceEventSummary payload already carries
 * verbatim, or a structural fact computed from fields that are genuinely
 * present (e.g. run elapsed time derived from recent_events timestamps,
 * since the snapshot carries no created_at/updated_at of its own). A field
 * with no real backing data is omitted, never invented.
 */
(function (root, factory) {
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = factory(require('./multiagent-graph.js'), require('./multiagent-live.js'));
  } else {
    root.PrizmMultiAgentInspector = factory(root.PrizmMultiAgentGraph, root.PrizmMultiAgentLive);
  }
})(typeof window !== 'undefined' ? window : globalThis, function (gm, live) {
  'use strict';

  var humanize = gm.humanize;

  // ---- Event type -> human label -----------------------------------------
  // Reuses gm.humanize on the event type's tail (everything after
  // "multi_agent."), e.g. 'prizm.workflow.multi_agent.role.iteration.started'
  // -> 'Role iteration started'. Falls back to the raw type string if it
  // doesn't match the expected dotted-constant shape, so an unrecognized
  // future event type still renders something instead of crashing.
  function eventTypeLabel(type) {
    if (!type) return 'Unknown event';
    var parts = String(type).split('.');
    var idx = parts.indexOf('multi_agent');
    var tail = idx >= 0 && idx + 1 < parts.length ? parts.slice(idx + 1) : parts;
    return humanize(tail.join('_'));
  }

  // ---- Event payload -> node/edge id correlation --------------------------
  // Mirrors derivePatches' per-event-type switch in multiagent-live.js, but
  // answers a different question: not "what DOM patch does this imply" but
  // "which single node or edge, if any, does this historical event concern"
  // for click-to-highlight correlation. Reuses live.roleNodeId /
  // live.terminalNodeId / live.edgeIdFor so ids always match
  // graph_projection.go's nodeID/terminalNodeID/edgeID exactly.
  function resolveEventTarget(eventType, payload) {
    payload = payload || {};
    var T = live.EVENT_TYPES;
    switch (eventType) {
      case T.ROLE_ENTERED:
      case T.ROLE_ITERATION_STARTED:
      case T.ROLE_ITERATION_COMPLETED:
      case T.ROLE_COMPLETED:
        return payload.role
          ? { nodeId: live.roleNodeId(payload.role), edgeId: null, isHandoff: false }
          : { nodeId: null, edgeId: null, isHandoff: false };

      case T.HANDOFF_CREATED:
        return {
          nodeId: payload.destination_role ? live.roleNodeId(payload.destination_role) : null,
          edgeId: null,
          isHandoff: true
        };

      case T.TRANSITION_SELECTED:
      case T.LOOP_TRAVERSAL_RECORDED: {
        var edgeId = (payload.source_role && payload.outcome)
          ? live.edgeIdFor(payload.source_role, payload.outcome) : null;
        var nodeId = payload.destination_role
          ? live.roleNodeId(payload.destination_role)
          : (payload.terminal_condition ? live.terminalNodeId(payload.terminal_condition) : null);
        return { nodeId: nodeId, edgeId: edgeId, isHandoff: false };
      }

      case T.RUN_COMPLETED:
      case T.RUN_FAILED:
      case T.RUN_CANCELLED:
      case T.RECOVERY_COMPLETED:
      case T.RECOVERY_FAILED:
        return {
          nodeId: payload.terminal_condition ? live.terminalNodeId(payload.terminal_condition) : null,
          edgeId: null,
          isHandoff: false
        };

      case T.BUDGET_WARNING:
      case T.BUDGET_EXHAUSTED:
        return {
          nodeId: payload.role ? live.roleNodeId(payload.role) : null,
          edgeId: null,
          isHandoff: false
        };

      default:
        return { nodeId: null, edgeId: null, isHandoff: false };
    }
  }

  // ---- Timeline row view model --------------------------------------------
  // Builds one display row from a ReferenceEventSummary-shaped object
  // ({id, type, timestamp, payload}) -- the exact shape of every entry in
  // DashboardRunSnapshot.recent_events (reference_workflow.go's
  // ReferenceEventSummary), which despite the field's name is the run's
  // FULL, untruncated event history (dashboard_snapshot.go's doc comment on
  // BuildDashboardSnapshot).
  function buildEventRow(evt) {
    evt = evt || {};
    var payload = evt.payload || {};
    var target = resolveEventTarget(evt.type, payload);

    var role = payload.role || payload.source_role || '';
    var transitionLabel = '';
    if (payload.source_role && payload.outcome) {
      var destLabel = payload.destination_role
        ? humanize(payload.destination_role)
        : (payload.terminal_condition ? humanize(payload.terminal_condition) : '');
      transitionLabel = humanize(payload.source_role) + ' → ' + (destLabel || '?') +
        ' (' + humanize(payload.outcome) + ')';
    }

    // Distinct-label discipline (matches multiagent-graph.js's
    // COUNTER_LABELS): a traversal/transition count is never relabeled as
    // an "iteration", and vice versa.
    var countLabel = '';
    if (typeof payload.iteration === 'number') {
      countLabel = gm.COUNTER_LABELS.LOCAL_ITERATIONS + ' ' + payload.iteration;
    } else if (typeof payload.transition_count === 'number') {
      countLabel = gm.COUNTER_LABELS.TOTAL_TRANSITIONS + ' ' + payload.transition_count;
    } else if (typeof payload.visit === 'number') {
      countLabel = gm.COUNTER_LABELS.VISITS + ' ' + payload.visit;
    }

    return {
      id: evt.id || '',
      timestamp: evt.timestamp || '',
      typeRaw: evt.type || '',
      typeLabel: eventTypeLabel(evt.type),
      role: role,
      roleLabel: role ? humanize(role) : '',
      transitionLabel: transitionLabel,
      outcome: payload.outcome || '',
      outcomeLabel: payload.outcome ? humanize(payload.outcome) : '',
      countLabel: countLabel,
      status: payload.status || '',
      statusLabel: payload.status ? humanize(payload.status) : '',
      reason: payload.reason || '',
      nodeId: target.nodeId,
      edgeId: target.edgeId,
      isHandoff: !!target.isHandoff
    };
  }

  // ---- Timeline windowing / pagination ------------------------------------
  // recent_events is the run's complete history and can be large for a
  // long-running workflow; it is already fully in memory (no new network
  // request), so "pagination" here is purely an in-memory array-slice
  // operation over a newest-first sort, never a re-fetch.
  var DEFAULT_WINDOW_SIZE = 75;

  function sortEventsNewestFirst(events) {
    return (events || []).slice().sort(function (a, b) {
      var idA = (a && a.id) || '';
      var idB = (b && b.id) || '';
      if (idA === idB) return 0;
      return idA < idB ? 1 : -1; // ULIDs sort correctly as plain strings
    });
  }

  /**
   * buildTimelineWindow returns the bounded, newest-first slice of events to
   * render right now, plus enough bookkeeping for a "Load older" control.
   * revealedCount defaults to DEFAULT_WINDOW_SIZE. Pure: no DOM, no mutation
   * of the input array.
   */
  function buildTimelineWindow(events, revealedCount) {
    var sorted = sortEventsNewestFirst(events);
    var count = Math.max(0, Math.min(
      typeof revealedCount === 'number' ? revealedCount : DEFAULT_WINDOW_SIZE,
      sorted.length
    ));
    return {
      rows: sorted.slice(0, count).map(buildEventRow),
      total: sorted.length,
      revealed: count,
      remaining: sorted.length - count
    };
  }

  // nextRevealCount computes the new revealedCount after a "Load older"
  // click, never exceeding the total available (already-in-memory) events.
  function nextRevealCount(currentRevealed, total, batchSize) {
    batchSize = batchSize || DEFAULT_WINDOW_SIZE;
    var next = (typeof currentRevealed === 'number' ? currentRevealed : 0) + batchSize;
    return Math.min(typeof total === 'number' ? total : 0, next);
  }

  // ---- Inspector data selection: Run --------------------------------------

  var WARNING_LEVELS = { approaching: true, critical: true, exhausted: true };

  // budgetWarnings surfaces every budget dimension at approaching/critical/
  // exhausted severity as a flat, ordered warning list. Mirrors
  // buildBudgetViewModel's dimension set exactly so nothing is silently
  // missed and nothing is invented.
  function budgetWarnings(budgets) {
    budgets = budgets || {};
    var warnings = [];
    function check(key, label, dim) {
      if (dim && WARNING_LEVELS[dim.level]) {
        warnings.push({ key: key, label: label, level: dim.level, used: dim.used, limit: dim.limit != null ? dim.limit : null });
      }
    }
    check('transitions', gm.COUNTER_LABELS.TOTAL_TRANSITIONS, budgets.transitions);
    check('tokens', 'Tokens', budgets.tokens);
    check('local_iterations', 'Total local iterations', budgets.local_iterations);
    check('repeated_failures', 'Repeated failures', budgets.repeated_failures);
    check('elapsed', 'Elapsed time', budgets.elapsed);
    var roleVisits = budgets.role_visits || {};
    Object.keys(roleVisits).sort().forEach(function (role) {
      check('role_visits:' + role, gm.COUNTER_LABELS.VISITS + ' — ' + humanize(role), roleVisits[role]);
    });
    var loopTraversals = budgets.loop_traversals || {};
    Object.keys(loopTraversals).sort().forEach(function (loop) {
      check('loop_traversals:' + loop, gm.COUNTER_LABELS.TRAVERSALS + ' — ' + humanize(loop), loopTraversals[loop]);
    });
    return warnings;
  }

  // runElapsedMs derives run duration from timestamps that are genuinely
  // present on the snapshot. Neither ReferenceRunSnapshot nor
  // DashboardRunSnapshot carries the run's own created_at/updated_at
  // (RunState has them, but reference_workflow.go's BuildReferenceRunSnapshot
  // does not surface them) -- so this uses the earliest/latest timestamps
  // across recent_events (the full history) instead, extended to
  // terminal_outcome.at when the run has finished, or to "now" (nowMs) when
  // it is still in flight. Returns null when there is no timestamp data at
  // all (e.g. an empty event history), rather than fabricating a duration.
  function runElapsedMs(snapshot, nowMs) {
    var events = (snapshot && snapshot.recent_events) || [];
    var first = null;
    var last = null;
    events.forEach(function (evt) {
      var t = evt && evt.timestamp ? Date.parse(evt.timestamp) : NaN;
      if (Number.isNaN(t)) return;
      if (first === null || t < first) first = t;
      if (last === null || t > last) last = t;
    });
    if (first === null) return null;
    var endMs = last;
    var terminalAt = snapshot && snapshot.terminal_outcome && snapshot.terminal_outcome.at
      ? Date.parse(snapshot.terminal_outcome.at) : NaN;
    if (!Number.isNaN(terminalAt)) {
      endMs = Math.max(endMs, terminalAt);
    } else if (typeof nowMs === 'number') {
      endMs = Math.max(endMs, nowMs);
    }
    return Math.max(0, endMs - first);
  }

  /**
   * buildRunInspectorData extracts the Run Inspector's fields from one
   * DashboardRunSnapshot. Deliberately omitted (checked, not present in this
   * read model): an objective/task-description field (RunState.CurrentTask
   * exists server-side but is not part of ReferenceRunSnapshot), and any
   * aggregate tool-call count (BudgetUsage has no such field -- only
   * per-role-iteration event payloads carry tool_calls/denied_tool_calls,
   * which is timeline-row information, not a run-level aggregate).
   */
  function buildRunInspectorData(snapshot, nowMs) {
    snapshot = snapshot || {};
    var budgets = snapshot.budgets || {};
    return {
      kind: 'run',
      runId: snapshot.run_id || '',
      workflowId: snapshot.workflow_id || '',
      status: snapshot.status || '',
      statusLabel: humanize(snapshot.status),
      currentRole: snapshot.current_role || '',
      currentRoleLabel: snapshot.current_role ? humanize(snapshot.current_role) : '',
      elapsedMs: runElapsedMs(snapshot, nowMs),
      transitionsBudget: budgets.transitions || null,
      tokensBudget: budgets.tokens || null,
      waiting: snapshot.waiting || null,
      warnings: budgetWarnings(budgets),
      terminalOutcome: snapshot.terminal_outcome || null,
      cancellationReason: snapshot.cancellation_reason || ''
    };
  }

  // ---- Inspector data selection: Node / Edge -------------------------------

  function stripNodePrefix(nodeId) {
    if (typeof nodeId !== 'string') return { role: '', terminal: '' };
    if (nodeId.indexOf('node:terminal:') === 0) return { role: '', terminal: nodeId.slice('node:terminal:'.length) };
    if (nodeId.indexOf('node:') === 0) return { role: nodeId.slice('node:'.length), terminal: '' };
    return { role: '', terminal: '' };
  }

  function findGraphNode(snapshot, nodeId) {
    var nodes = (snapshot && snapshot.graph && snapshot.graph.nodes) || [];
    for (var i = 0; i < nodes.length; i++) {
      if (nodes[i].id === nodeId) return nodes[i];
    }
    return null;
  }

  function findGraphEdge(snapshot, edgeId) {
    var edges = (snapshot && snapshot.graph && snapshot.graph.edges) || [];
    for (var i = 0; i < edges.length; i++) {
      if (edges[i].id === edgeId) return edges[i];
    }
    return null;
  }

  /**
   * buildNodeInspectorData joins one GraphNode (snapshot.graph.nodes[]) with
   * its matching RoleState (snapshot.role_states[role]), when the node is a
   * role node. Deliberately omitted: a "tool permissions" row -- no field on
   * RoleState, GraphNode, or WaitingState carries per-role tool permissions
   * in the current data model; inventing one was explicitly disallowed by
   * this milestone's spec. Duration is only populated when RoleState.elapsed
   * is a real, present value (nanoseconds -> ms here), never derived from
   * unrelated timestamps.
   */
  function buildNodeInspectorData(snapshot, nodeId) {
    var graphNode = findGraphNode(snapshot, nodeId);
    if (!graphNode) return null;
    var parts = stripNodePrefix(nodeId);
    var roleState = parts.role ? ((snapshot && snapshot.role_states) || {})[parts.role] : null;

    var data = {
      kind: 'node',
      nodeId: nodeId,
      label: graphNode.label || nodeId,
      nodeKind: graphNode.kind,
      role: graphNode.role || '',
      terminal: graphNode.terminal || '',
      status: graphNode.status,
      statusLabel: humanize(graphNode.status),
      isCurrent: !!graphNode.is_current,
      visits: graphNode.visits || 0,
      maxVisits: graphNode.max_visits != null ? graphNode.max_visits : null,
      localIterations: graphNode.local_iterations || 0,
      maxLocalIterations: graphNode.max_local_iterations != null ? graphNode.max_local_iterations : null,
      durationMs: null,
      lastOutcome: '',
      lastOutcomeLabel: '',
      validationStatus: '',
      validationStatusLabel: '',
      approvalStatus: '',
      approvalStatusLabel: '',
      lastError: ''
    };

    if (roleState) {
      if (typeof roleState.elapsed === 'number') data.durationMs = Math.round(roleState.elapsed / 1e6);
      if (roleState.last_outcome) {
        data.lastOutcome = roleState.last_outcome;
        data.lastOutcomeLabel = humanize(roleState.last_outcome);
      }
      if (roleState.validation_status) {
        data.validationStatus = roleState.validation_status;
        data.validationStatusLabel = humanize(roleState.validation_status);
      }
      if (roleState.approval_status) {
        data.approvalStatus = roleState.approval_status;
        data.approvalStatusLabel = humanize(roleState.approval_status);
      }
      if (roleState.last_error) data.lastError = roleState.last_error;
    }

    return data;
  }

  /**
   * buildEdgeInspectorData extracts the Edge Inspector's fields directly
   * from one GraphEdge (snapshot.graph.edges[]). Deliberately omitted: a
   * prose "why this transition was selected" reason -- graph_projection.go's
   * GraphEdge carries only outcome/status/traversal accounting, not the
   * workflow Definition's TransitionRule the original design sketch assumed
   * would be available client-side. What IS available (outcome, label,
   * status, loop/exhaustion state) is shown; nothing is invented to fill the
   * gap.
   */
  function buildEdgeInspectorData(snapshot, edgeId) {
    var graphEdge = findGraphEdge(snapshot, edgeId);
    if (!graphEdge) return null;
    return {
      kind: 'edge',
      edgeId: edgeId,
      from: graphEdge.from,
      fromLabel: humanize(graphEdge.from),
      to: graphEdge.to || '',
      toLabel: graphEdge.to ? humanize(graphEdge.to) : '',
      terminal: graphEdge.terminal || '',
      terminalLabel: graphEdge.terminal ? humanize(graphEdge.terminal) : '',
      outcome: graphEdge.outcome,
      outcomeLabel: humanize(graphEdge.outcome),
      status: graphEdge.status,
      statusLabel: humanize(graphEdge.status),
      isLoopback: !!graphEdge.is_loopback,
      traversalCount: graphEdge.traversal_count || 0,
      maxTraversals: graphEdge.max_traversals != null ? graphEdge.max_traversals : null,
      isExhausted: graphEdge.status === 'exhausted'
    };
  }

  /**
   * buildHandoffInspectorData extracts the Handoff Inspector's fields from
   * one Handoff object (snapshot.latest_handoff), using the real JSON field
   * names from internal/workflow/multiagent/handoff.go: source_role,
   * destination_role, objective, artifacts, evidence, outcome (labeled
   * "decision" per this milestone's spec), reason, unresolved_issues,
   * validation_results, created_at. Handoff.Notes is deliberately NOT
   * surfaced: it is documented as supplemental freeform text an agent may
   * have written, outside the structured contract fields this milestone's
   * spec enumerates, and this module errs toward omission for any field
   * whose content isn't a bounded, structured value.
   */
  function buildHandoffInspectorData(handoff) {
    if (!handoff) return null;
    return {
      kind: 'handoff',
      id: handoff.id || '',
      sourceRole: handoff.source_role || '',
      sourceRoleLabel: humanize(handoff.source_role),
      destinationRole: handoff.destination_role || '',
      destinationRoleLabel: humanize(handoff.destination_role),
      objective: handoff.objective || '',
      artifacts: handoff.artifacts || [],
      evidence: handoff.evidence || [],
      decision: handoff.outcome || '',
      decisionLabel: handoff.outcome ? humanize(handoff.outcome) : '',
      reason: handoff.reason || '',
      unresolvedIssues: handoff.unresolved_issues || [],
      validationResults: handoff.validation_results || [],
      timestamp: handoff.created_at || ''
    };
  }

  // ---- Secret-safety guard --------------------------------------------
  //
  // isSecretLikeKey flags a field name that looks like it holds a raw
  // credential/token value, e.g. "token", "api_key", "secret", "password",
  // "credential". It deliberately does NOT flag legitimate LLM token-COUNT
  // fields (token_usage, prompt_tokens, completion_tokens, total_tokens,
  // tokensBudget, etc.) -- those are numeric usage aggregates the spec
  // explicitly asks the Run Inspector to show, not secret material.
  var SECRET_KEY_PATTERN = /^(token|apikey|api[_-]?key|secret|password|passwd|credential|credentials|authtoken|auth[_-]?token|accesstoken|access[_-]?token|refreshtoken|refresh[_-]?token|privatekey|private[_-]?key|sessiontoken|session[_-]?token|cookie)$/i;
  // Legitimate LLM token-COUNT aggregates: explicit allow-list of exact
  // names only (never a bare "token"/"tokens", which stays flagged above).
  var USAGE_COUNT_EXACT_ALLOW = /^(token_usage|tokenusage|prompt_tokens|completion_tokens|total_tokens|tokensbudget|tokensused|tokensbudgetused)$/i;

  function isSecretLikeKey(key) {
    if (!key) return false;
    if (USAGE_COUNT_EXACT_ALLOW.test(key)) return false;
    return SECRET_KEY_PATTERN.test(key);
  }

  // collectKeysDeep walks a plain object/array tree and returns every object
  // key encountered, for a static assertion (in tests) that no inspector
  // builder's output ever includes a secret-shaped key.
  function collectKeysDeep(value, keys) {
    keys = keys || [];
    if (Array.isArray(value)) {
      value.forEach(function (item) { collectKeysDeep(item, keys); });
    } else if (value && typeof value === 'object') {
      Object.keys(value).forEach(function (k) {
        keys.push(k);
        collectKeysDeep(value[k], keys);
      });
    }
    return keys;
  }

  var api = {
    DEFAULT_WINDOW_SIZE: DEFAULT_WINDOW_SIZE,
    eventTypeLabel: eventTypeLabel,
    resolveEventTarget: resolveEventTarget,
    buildEventRow: buildEventRow,
    sortEventsNewestFirst: sortEventsNewestFirst,
    buildTimelineWindow: buildTimelineWindow,
    nextRevealCount: nextRevealCount,
    budgetWarnings: budgetWarnings,
    runElapsedMs: runElapsedMs,
    buildRunInspectorData: buildRunInspectorData,
    stripNodePrefix: stripNodePrefix,
    findGraphNode: findGraphNode,
    findGraphEdge: findGraphEdge,
    buildNodeInspectorData: buildNodeInspectorData,
    buildEdgeInspectorData: buildEdgeInspectorData,
    buildHandoffInspectorData: buildHandoffInspectorData,
    isSecretLikeKey: isSecretLikeKey,
    collectKeysDeep: collectKeysDeep
  };

  return api;
});
