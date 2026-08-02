// validator.go implements PR2 of the Phase 3 "Developer-Authored Workflow
// Graph Runtime" initiative: the business-rule validation that turns a
// loaded (but not yet business-rule-checked) WorkflowDefinition
// (schema_types.go, loader.go) into a Diagnostics list describing every
// structural, routing, cycle, governance, and budget problem it contains.
//
// This is deliberately a sibling of, not a replacement for, the existing
// Definition.Validate() in config.go: that method enforces Phase 1's fixed
// four-role/eight-outcome contract on the *old* Definition type.
// ValidateDefinition enforces the analogous invariants — no self-approval,
// no unbounded loops, no dangling references, sane budgets — on the *new*,
// open-vocabulary WorkflowDefinition schema, where roles/outcomes/node IDs
// are developer-chosen strings instead of a hardcoded Go enum. The two
// validators do not share code because their underlying types have no
// structural overlap, but validateGovernance's self-approval check and
// validateBudgets' Limit-sentinel handling are intentionally written to
// mirror config.go's validateRoleConfig/validateLimit conventions.
//
// Every pass collects every finding and never stops at the first error,
// matching the "collect everything" precedent already established by
// Definition.Validate. idx (a *PositionIndex) may be nil — every lookup
// method on PositionIndex is nil-safe, so diagnostics simply omit
// line/column (while still carrying NodeID/EdgeID/FieldPath context) when
// there is no source file behind the definition being validated (e.g. an
// in-memory definition constructed by a test or by the future compiler's
// own callers).
package multiagent

import (
	"fmt"
	"strings"
)

// ValidateDefinition runs all five validation passes (structural, routing,
// cycles, governance, budgets) and returns every finding in that fixed
// order, concatenated — it never short-circuits on the first error. This is
// equivalent to ValidateDefinitionWithProfiles(def, idx, nil): the "known
// validation profiles" governance check is skipped whenever no live
// validation.Registry-equivalent list is available, which keeps this
// function usable from CI/tests with zero running services.
func ValidateDefinition(def WorkflowDefinition, idx *PositionIndex) Diagnostics {
	return ValidateDefinitionWithProfiles(def, idx, nil)
}

// ValidateDefinitionWithProfiles is ValidateDefinition plus an optional
// check that every validation-profile name referenced by the definition
// (node-level spec.nodes[i].validation.profiles and workflow-wide
// spec.defaults.validation.profiles) is a member of knownProfiles.
// knownProfiles may be nil to skip that specific check entirely — the
// governance pass otherwise runs unaffected. A non-nil-but-empty slice is
// treated as "no profiles are known," so every referenced profile is
// flagged; callers that have no registry available at all should pass nil,
// not an empty slice, to skip the check.
func ValidateDefinitionWithProfiles(def WorkflowDefinition, idx *PositionIndex, knownProfiles []string) Diagnostics {
	var diags Diagnostics
	diags = append(diags, validateStructural(def, idx)...)
	diags = append(diags, validateRouting(def, idx)...)
	diags = append(diags, validateCycles(def, idx)...)
	diags = append(diags, validateGovernanceWithProfiles(def, idx, knownProfiles)...)
	diags = append(diags, validateSchemaBudgets(def, idx)...)
	return attachPositions(diags, idx)
}

// attachPositions fills in Line/Column for every diagnostic that doesn't
// already have one, by looking up (in priority order) its NodeID, then its
// EdgeID, then its FieldPath against idx. This lets every validate* pass
// construct Diagnostic values without threading position lookups through
// every call site — idx's lookup methods are nil-safe, so this is a no-op
// (every diagnostic simply keeps Line/Column at zero) when idx is nil.
func attachPositions(diags Diagnostics, idx *PositionIndex) Diagnostics {
	for i := range diags {
		d := &diags[i]
		if d.Line > 0 {
			continue
		}
		if d.NodeID != "" {
			if pos, ok := idx.LookupNode(d.NodeID); ok {
				d.Line, d.Column = pos.Line, pos.Column
				continue
			}
		}
		if d.EdgeID != "" {
			if pos, ok := idx.LookupEdge(d.EdgeID); ok {
				d.Line, d.Column = pos.Line, pos.Column
				continue
			}
		}
		if d.FieldPath != "" {
			if pos, ok := idx.Lookup(d.FieldPath); ok {
				d.Line, d.Column = pos.Line, pos.Column
			}
		}
	}
	return diags
}

// ---------------------------------------------------------------------
// Structural
// ---------------------------------------------------------------------

// validateStructural checks the basic shape of the graph: unique node/edge
// IDs, a valid entry node, no dangling edge references, at least one
// terminal node reachable from the entry node, and that every node's type
// is recognized.
//
// A note on SchemaEdge.To: schema_types.go's SchemaEdge has no separate
// "this edge terminates here" flag distinct from To — its own doc comment
// says an edge routes "to another node (or implicitly terminal via the
// target node's own type)". That means the only way to route into a
// terminal state is for To to name a node whose Type is "terminal"; there is
// no schema-level way to leave To empty and mean "this edge ends the run."
// Consequently this validator treats an empty To exactly like an unknown
// node reference (structural.dangling-edge-ref), not as a legitimate
// terminal shorthand.
func validateStructural(def WorkflowDefinition, idx *PositionIndex) Diagnostics {
	var diags Diagnostics

	nodeIDCount := make(map[string]int, len(def.Spec.Nodes))
	for _, n := range def.Spec.Nodes {
		nodeIDCount[n.ID]++
	}
	nodeByID := make(map[string]SchemaNode, len(def.Spec.Nodes))
	for _, n := range def.Spec.Nodes {
		if _, exists := nodeByID[n.ID]; !exists {
			nodeByID[n.ID] = n
		}
		if nodeIDCount[n.ID] > 1 {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "structural.duplicate-node-id",
				Message:  fmt.Sprintf("node id %q is declared more than once", n.ID),
				NodeID:   n.ID,
			})
		}
		if t := normalizedNodeType(n); t != "role" && t != "terminal" {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "structural.invalid-node-type",
				Message:  fmt.Sprintf("node %q has type %q, want \"role\" or \"terminal\"", n.ID, n.Type),
				NodeID:   n.ID,
			})
		}
	}
	nodeIDs := collectNodeIDs(def)

	edgeIDCount := make(map[string]int, len(def.Spec.Edges))
	for _, e := range def.Spec.Edges {
		edgeIDCount[e.ID]++
	}
	for _, e := range def.Spec.Edges {
		if edgeIDCount[e.ID] > 1 {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "structural.duplicate-edge-id",
				Message:  fmt.Sprintf("edge id %q is declared more than once", e.ID),
				EdgeID:   e.ID,
			})
		}

		switch {
		case e.From == "":
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "structural.dangling-edge-ref",
				Message:  fmt.Sprintf("edge %q has no source node (from is required)", e.ID),
				EdgeID:   e.ID,
			})
		default:
			if _, ok := nodeByID[e.From]; !ok {
				suggestion, _ := suggestClosest(e.From, nodeIDs)
				diags = append(diags, Diagnostic{
					Severity:   SeverityError,
					Rule:       "structural.dangling-edge-ref",
					Message:    fmt.Sprintf("edge %q references unknown source node %q", e.ID, e.From),
					EdgeID:     e.ID,
					Suggestion: suggestion,
				})
			}
		}

		switch {
		case e.To == "":
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "structural.dangling-edge-ref",
				Message:  fmt.Sprintf("edge %q has no destination node (to is required)", e.ID),
				EdgeID:   e.ID,
			})
		default:
			if _, ok := nodeByID[e.To]; !ok {
				suggestion, _ := suggestClosest(e.To, nodeIDs)
				diags = append(diags, Diagnostic{
					Severity:   SeverityError,
					Rule:       "structural.dangling-edge-ref",
					Message:    fmt.Sprintf("edge %q references unknown destination node %q", e.ID, e.To),
					EdgeID:     e.ID,
					Suggestion: suggestion,
				})
			}
		}
	}

	entryOK := false
	switch {
	case strings.TrimSpace(def.Spec.EntryNode) == "":
		diags = append(diags, Diagnostic{
			Severity:  SeverityError,
			Rule:      "structural.entry-node-missing",
			Message:   "spec.entryNode is required",
			FieldPath: "spec.entryNode",
		})
	default:
		if _, ok := nodeByID[def.Spec.EntryNode]; ok {
			entryOK = true
		} else {
			suggestion, _ := suggestClosest(def.Spec.EntryNode, nodeIDs)
			diags = append(diags, Diagnostic{
				Severity:   SeverityError,
				Rule:       "structural.entry-node-unknown",
				Message:    fmt.Sprintf("spec.entryNode %q does not match any declared node", def.Spec.EntryNode),
				FieldPath:  "spec.entryNode",
				Suggestion: suggestion,
			})
		}
	}

	var terminalIDs []string
	for _, n := range def.Spec.Nodes {
		if normalizedNodeType(n) == "terminal" {
			terminalIDs = append(terminalIDs, n.ID)
		}
	}

	adjacency := make(map[string][]string, len(nodeIDs))
	reverseAdjacency := make(map[string][]string, len(nodeIDs))
	for _, e := range def.Spec.Edges {
		if e.From == "" || e.To == "" {
			continue
		}
		if _, ok := nodeByID[e.From]; !ok {
			continue
		}
		if _, ok := nodeByID[e.To]; !ok {
			continue
		}
		adjacency[e.From] = append(adjacency[e.From], e.To)
		reverseAdjacency[e.To] = append(reverseAdjacency[e.To], e.From)
	}

	var reachable map[string]bool
	if entryOK {
		reachable = bfs(def.Spec.EntryNode, adjacency)
	}

	switch {
	case len(terminalIDs) == 0:
		diags = append(diags, Diagnostic{
			Severity: SeverityError,
			Rule:     "structural.no-terminal-path",
			Message:  `workflow declares no node with type "terminal"`,
		})
	case entryOK:
		reachedTerminal := false
		for _, tid := range terminalIDs {
			if reachable[tid] {
				reachedTerminal = true
				break
			}
		}
		if !reachedTerminal {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "structural.no-terminal-path",
				Message:  fmt.Sprintf("no terminal node is reachable from entry node %q", def.Spec.EntryNode),
				NodeID:   def.Spec.EntryNode,
			})
		}
	}

	// Unreachable/dead-node analysis requires a valid entry point to reason
	// about reachability from; when the entry node itself is missing/unknown
	// (already reported above), we cannot meaningfully classify any other
	// node as reachable or not, so this step is skipped rather than guessed.
	if entryOK {
		canReachTerminal := make(map[string]bool, len(nodeIDs))
		for _, tid := range terminalIDs {
			canReachTerminal[tid] = true
			for id := range bfs(tid, reverseAdjacency) {
				canReachTerminal[id] = true
			}
		}

		for _, id := range nodeIDs {
			if reachable[id] {
				continue
			}
			if canReachTerminal[id] {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Rule:     "structural.unreachable-node",
					Message:  fmt.Sprintf("node %q is not reachable from entry node %q", id, def.Spec.EntryNode),
					NodeID:   id,
				})
			} else {
				diags = append(diags, Diagnostic{
					Severity: SeverityError,
					Rule:     "structural.dead-node",
					Message:  fmt.Sprintf("node %q is unreachable from entry node %q and has no path to any terminal node", id, def.Spec.EntryNode),
					NodeID:   id,
				})
			}
		}
	}

	return diags
}

// ---------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------

// validateRouting checks outcome-routing semantics: an edge's outcome must
// be one its source node actually declares, competing edges from the same
// (node, outcome) pair must be disambiguated by priority (or are flagged),
// no edge may leave a terminal node, and every declared outcome must either
// be routed or be explicitly allowed to go unhandled.
func validateRouting(def WorkflowDefinition, idx *PositionIndex) Diagnostics {
	var diags Diagnostics
	nodeByID := buildNodeByID(def)

	for _, e := range def.Spec.Edges {
		src, ok := nodeByID[e.From]
		if !ok {
			continue // dangling "from" already reported by validateStructural
		}
		if containsString(src.AllowedOutcomes, e.When.Outcome) {
			continue
		}
		suggestion, _ := suggestClosest(e.When.Outcome, src.AllowedOutcomes)
		diags = append(diags, Diagnostic{
			Severity:   SeverityError,
			Rule:       "routing.outcome-not-allowed",
			Message:    fmt.Sprintf("edge %q routes outcome %q from node %q, which is not in that node's allowedOutcomes", e.ID, e.When.Outcome, e.From),
			NodeID:     e.From,
			EdgeID:     e.ID,
			Suggestion: suggestion,
		})
	}

	// Ambiguous transitions / priority tie-breaks. Two or more edges sharing
	// the same (From, Outcome) pair are fine only if every one of them has a
	// distinct Priority (the highest wins deterministically at compile/run
	// time) — that case is a warning ("a tie-break is in play, make sure you
	// meant this"). If any two share both (From, Outcome) AND the same
	// Priority (including two edges that both omit it, defaulting to 0),
	// each of those colliding edges gets its own error diagnostic — one
	// diagnostic per edge (not one combined diagnostic for the pair), with
	// the sibling edge IDs named in the message, since Diagnostic only has a
	// single EdgeID field.
	order, groups := groupEdgesByFromOutcome(def.Spec.Edges)
	for _, key := range order {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		priorityCount := make(map[int]int, len(group))
		for _, e := range group {
			priorityCount[e.Priority]++
		}
		if len(priorityCount) == len(group) {
			// every edge in the group has a distinct priority: a real, but
			// deterministic, tie-break.
			ids := edgeIDList(group)
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Rule:     "routing.tie-break-used",
				Message:  fmt.Sprintf("edges %s all route from node %q on outcome %q; the highest-priority edge wins deterministically", strings.Join(ids, ", "), key.from, key.outcome),
				NodeID:   key.from,
				EdgeID:   group[0].ID,
			})
			continue
		}
		for _, e := range group {
			if priorityCount[e.Priority] <= 1 {
				continue
			}
			siblings := edgeIDList(group)
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "routing.ambiguous-transition",
				Message:  fmt.Sprintf("edge %q is ambiguous with %s: multiple edges route from node %q on outcome %q with the same priority (%d)", e.ID, strings.Join(removeString(siblings, e.ID), ", "), key.from, key.outcome, e.Priority),
				NodeID:   key.from,
				EdgeID:   e.ID,
			})
		}
	}

	for _, e := range def.Spec.Edges {
		src, ok := nodeByID[e.From]
		if !ok {
			continue
		}
		if normalizedNodeType(src) == "terminal" {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "routing.transition-from-terminal",
				Message:  fmt.Sprintf("edge %q leaves node %q, which is a terminal node", e.ID, e.From),
				NodeID:   e.From,
				EdgeID:   e.ID,
			})
		}
	}

	edgesByFrom := make(map[string][]SchemaEdge, len(def.Spec.Edges))
	for _, e := range def.Spec.Edges {
		edgesByFrom[e.From] = append(edgesByFrom[e.From], e)
	}
	defaultFail := def.Spec.Defaults.Failure.DefaultOnUnhandledOutcome == "fail"
	for _, n := range def.Spec.Nodes {
		if normalizedNodeType(n) != "role" {
			continue
		}
		for _, oc := range n.AllowedOutcomes {
			routed := false
			for _, e := range edgesByFrom[n.ID] {
				if e.When.Outcome == oc {
					routed = true
					break
				}
			}
			if routed || defaultFail {
				continue
			}
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "routing.unhandled-outcome",
				Message:  fmt.Sprintf("node %q declares allowed outcome %q with no routing edge, and spec.defaults.failure.defaultOnUnhandledOutcome is not \"fail\"", n.ID, oc),
				NodeID:   n.ID,
			})
		}
	}

	return diags
}

// ---------------------------------------------------------------------
// Cycles
// ---------------------------------------------------------------------

// validateCycles finds every strongly-connected component (via Tarjan's
// algorithm, tarjanSCC below) and every self-edge, and requires that any
// edge participating in a cycle carry a bounded loop policy that can
// actually terminate the loop.
func validateCycles(def WorkflowDefinition, idx *PositionIndex) Diagnostics {
	var diags Diagnostics
	nodeIDs := collectNodeIDs(def)
	nodeByID := buildNodeByID(def)
	sccs := tarjanSCC(nodeIDs, def.Spec.Edges)

	sccIndexOf := make(map[string]int, len(nodeIDs))
	for i, scc := range sccs {
		for _, id := range scc {
			sccIndexOf[id] = i
		}
	}

	// isCyclicEdge reports whether e is "inside" a cycle: either a self-edge
	// (From == To) or an edge whose From and To both belong to the same
	// multi-node SCC. Returns the SCC index for convenience (-1 if not
	// cyclic, or if either endpoint is dangling and was already reported by
	// validateStructural).
	isCyclicEdge := func(e SchemaEdge) (int, bool) {
		if e.From == "" || e.To == "" {
			return -1, false
		}
		fromIdx, fromOK := sccIndexOf[e.From]
		toIdx, toOK := sccIndexOf[e.To]
		if !fromOK || !toOK {
			return -1, false
		}
		if e.From == e.To {
			return fromIdx, true
		}
		if fromIdx == toIdx && len(sccs[fromIdx]) > 1 {
			return fromIdx, true
		}
		return -1, false
	}

	var cyclicEdges []SchemaEdge
	for _, e := range def.Spec.Edges {
		sccIdx, cyclic := isCyclicEdge(e)
		if !cyclic {
			continue
		}
		cyclicEdges = append(cyclicEdges, e)

		if e.Loop == nil || e.Loop.MaxTraversals <= 0 {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "cycle.unbounded-loop-edge",
				Message:  fmt.Sprintf("edge %q participates in a cycle but has no bounded loop policy (loop.maxTraversals must be > 0)", e.ID),
				EdgeID:   e.ID,
				NodeID:   e.From,
			})
			continue
		}

		switch e.Loop.OnExhausted {
		case "fail":
			// Exhaustion fails the run; nothing further to check.
		case "route_to":
			if _, ok := nodeByID[e.Loop.RouteTo]; !ok {
				suggestion, _ := suggestClosest(e.Loop.RouteTo, nodeIDs)
				diags = append(diags, Diagnostic{
					Severity:   SeverityError,
					Rule:       "cycle.invalid-exhaustion-route",
					Message:    fmt.Sprintf("edge %q loop.routeTo %q is not a known node id", e.ID, e.Loop.RouteTo),
					EdgeID:     e.ID,
					NodeID:     e.From,
					Suggestion: suggestion,
				})
				break
			}
			if targetIdx, ok := sccIndexOf[e.Loop.RouteTo]; ok && targetIdx == sccIdx {
				diags = append(diags, Diagnostic{
					Severity: SeverityError,
					Rule:     "cycle.invalid-exhaustion-route",
					Message:  fmt.Sprintf("edge %q loop.routeTo %q stays inside the same cycle it is meant to escape", e.ID, e.Loop.RouteTo),
					EdgeID:   e.ID,
					NodeID:   e.From,
				})
			}
		default:
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "cycle.invalid-exhaustion-behavior",
				Message:  fmt.Sprintf("edge %q loop.onExhausted = %q is invalid: must be \"fail\" or \"route_to\"", e.ID, e.Loop.OnExhausted),
				EdgeID:   e.ID,
				NodeID:   e.From,
			})
		}
	}

	// Cross-reference: two loop-participating edges sharing (From, Outcome)
	// are already flagged as an error by validateRouting's
	// routing.ambiguous-transition (or a warning via routing.tie-break-used,
	// when disambiguated by priority). This pass re-flags the same edges
	// with a cycle-specific rule name so a diagnostic list makes it obvious
	// the ambiguity sits inside a loop (its exit is non-deterministic), not
	// just that two edges collide in the abstract. To avoid double-counting
	// the same root cause as two separate *blocking* errors, this is always
	// a warning here, regardless of whether validateRouting judged it an
	// error (equal priority) or a warning (distinct priority) — the
	// authoritative blocking diagnostic is validateRouting's.
	order, groups := groupEdgesByFromOutcome(cyclicEdges)
	for _, key := range order {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		ids := edgeIDList(group)
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Rule:     "cycle.non-deterministic-loop",
			Message:  fmt.Sprintf("loop edges %s all route from node %q on outcome %q inside the same cycle, making the loop's exit non-deterministic", strings.Join(ids, ", "), key.from, key.outcome),
			NodeID:   key.from,
			EdgeID:   group[0].ID,
		})
	}

	return diags
}

// tarjanSCC computes every strongly-connected component of the directed
// graph formed by nodeIDs and edges (edges with an endpoint outside nodeIDs,
// or an empty From/To, are ignored — those are dangling references already
// reported by validateStructural). Every node appears in exactly one
// returned component, including nodes with no edges at all (a trivial
// singleton SCC). The classic recursive formulation is used; expected graph
// sizes for hand-authored workflows are small enough (the compiler's own
// planned hard cap is 500 nodes) that recursion depth is not a concern.
func tarjanSCC(nodeIDs []string, edges []SchemaEdge) [][]string {
	valid := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		valid[id] = true
	}
	adjacency := make(map[string][]string, len(nodeIDs))
	for _, e := range edges {
		if e.From == "" || e.To == "" || !valid[e.From] || !valid[e.To] {
			continue
		}
		adjacency[e.From] = append(adjacency[e.From], e.To)
	}

	st := &tarjanState{
		adjacency: adjacency,
		index:     make(map[string]int, len(nodeIDs)),
		low:       make(map[string]int, len(nodeIDs)),
		onStack:   make(map[string]bool, len(nodeIDs)),
	}
	for _, id := range nodeIDs {
		if _, visited := st.index[id]; !visited {
			st.strongconnect(id)
		}
	}
	return st.sccs
}

// tarjanState carries the mutable bookkeeping Tarjan's algorithm needs
// across recursive calls to strongconnect.
type tarjanState struct {
	adjacency map[string][]string
	index     map[string]int
	low       map[string]int
	onStack   map[string]bool
	stack     []string
	counter   int
	sccs      [][]string
}

func (s *tarjanState) strongconnect(v string) {
	s.index[v] = s.counter
	s.low[v] = s.counter
	s.counter++
	s.stack = append(s.stack, v)
	s.onStack[v] = true

	for _, w := range s.adjacency[v] {
		if _, visited := s.index[w]; !visited {
			s.strongconnect(w)
			if s.low[w] < s.low[v] {
				s.low[v] = s.low[w]
			}
		} else if s.onStack[w] {
			if s.index[w] < s.low[v] {
				s.low[v] = s.index[w]
			}
		}
	}

	if s.low[v] == s.index[v] {
		var scc []string
		for {
			n := len(s.stack) - 1
			w := s.stack[n]
			s.stack = s.stack[:n]
			s.onStack[w] = false
			scc = append(scc, w)
			if w == v {
				break
			}
		}
		s.sccs = append(s.sccs, scc)
	}
}

// ---------------------------------------------------------------------
// Governance
// ---------------------------------------------------------------------

// validateGovernanceWithProfiles checks approval/tooling/profile shape
// rules. It is the implementation ValidateDefinitionWithProfiles calls;
// ValidateDefinition itself only ever passes knownProfiles == nil.
func validateGovernanceWithProfiles(def WorkflowDefinition, idx *PositionIndex, knownProfiles []string) Diagnostics {
	var diags Diagnostics
	nodeIDs := collectNodeIDs(def)

	var knownProfileSet map[string]bool
	if knownProfiles != nil {
		knownProfileSet = make(map[string]bool, len(knownProfiles))
		for _, p := range knownProfiles {
			knownProfileSet[p] = true
		}
	}
	checkProfiles := func(nodeID, fieldPath string, profiles []string) {
		if knownProfileSet == nil {
			return
		}
		for _, p := range profiles {
			if p == "" || knownProfileSet[p] {
				continue
			}
			suggestion, _ := suggestClosest(p, knownProfiles)
			diags = append(diags, Diagnostic{
				Severity:   SeverityError,
				Rule:       "governance.unknown-validation-profile",
				Message:    fmt.Sprintf("validation profile %q is not a known profile", p),
				NodeID:     nodeID,
				FieldPath:  fieldPath,
				Suggestion: suggestion,
			})
		}
	}

	for i, n := range def.Spec.Nodes {
		fieldPrefix := fmt.Sprintf("spec.nodes[%d]", i)

		if n.Approval != nil {
			for _, appr := range n.Approval.Approvers {
				if appr == "" {
					continue
				}
				if appr == n.ID {
					diags = append(diags, Diagnostic{
						Severity: SeverityError,
						Rule:     "governance.self-approval",
						Message:  fmt.Sprintf("node %q cannot appear in its own approvers list", n.ID),
						NodeID:   n.ID,
					})
				}
				// Approvers may legitimately name an identity/role that has
				// no corresponding graph node (e.g. an external human
				// approver never modeled as a node), so an unmatched entry
				// is only ever a warning, not a hard error.
				if !containsString(nodeIDs, appr) {
					suggestion, _ := suggestClosest(appr, nodeIDs)
					diags = append(diags, Diagnostic{
						Severity:   SeverityWarning,
						Rule:       "governance.unknown-approver",
						Message:    fmt.Sprintf("approver %q does not match any known node id (this is fine if it names an external identity)", appr),
						NodeID:     n.ID,
						Suggestion: suggestion,
					})
				}
			}
		}

		diags = append(diags, validateNodeStringList(n.ID, fieldPrefix+".allowedTools", "allowedTools", n.AllowedTools, "governance.invalid-tool-list")...)
		diags = append(diags, validateNodeStringList(n.ID, fieldPrefix+".capabilities", "capabilities", n.Capabilities, "governance.duplicate-capability")...)

		if normalizedNodeType(n) == "role" && strings.TrimSpace(n.AgentProfile) == "" {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "governance.missing-agent-profile",
				Message:  fmt.Sprintf("node %q is a role node and must declare an agentProfile (resolution against a live agent.Registry happens at run time, not here)", n.ID),
				NodeID:   n.ID,
			})
		}

		if n.Validation != nil {
			checkProfiles(n.ID, fieldPrefix+".validation.profiles", n.Validation.Profiles)
		}
	}

	checkProfiles("", "spec.defaults.validation.profiles", def.Spec.Defaults.Validation.Profiles)

	return diags
}

// validateNodeStringList is a shape-only check for a node's AllowedTools or
// Capabilities list: every entry must be non-empty (after trimming), and no
// exact duplicate values are allowed within the same node's list. It never
// checks membership against any live tool/policy/capability registry — that
// authority stays with internal/tool and internal/policy at run time, per
// the Phase 3 plan's governance section.
//
// Resolution of an ambiguity in the task spec: "AllowedTools/Capabilities...
// — governance.invalid-tool-list/governance.duplicate-capability" is read as
// one rule name per *field* (not one rule per *check type*): every problem
// found in AllowedTools (empty entry or duplicate) is reported under
// governance.invalid-tool-list, and every problem found in Capabilities
// (empty entry or duplicate) is reported under governance.duplicate-
// capability. Both are warning-severity shape checks.
func validateNodeStringList(nodeID, fieldPath, fieldName string, values []string, rule string) Diagnostics {
	var diags Diagnostics
	seen := make(map[string]bool, len(values))
	for i, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			diags = append(diags, Diagnostic{
				Severity:  SeverityWarning,
				Rule:      rule,
				Message:   fmt.Sprintf("%s[%d] must not be empty", fieldName, i),
				NodeID:    nodeID,
				FieldPath: fmt.Sprintf("%s[%d]", fieldPath, i),
			})
			continue
		}
		if seen[trimmed] {
			diags = append(diags, Diagnostic{
				Severity:  SeverityWarning,
				Rule:      rule,
				Message:   fmt.Sprintf("%s contains duplicate value %q", fieldName, trimmed),
				NodeID:    nodeID,
				FieldPath: fieldPath,
			})
		}
		seen[trimmed] = true
	}
	return diags
}

// ---------------------------------------------------------------------
// Budgets
// ---------------------------------------------------------------------

// validateBudgets checks SchemaLimit values (global and per-node) against
// the positive-or-unlimited(-1) sentinel convention Limit already
// establishes in types.go, flags node-level limits that could never bind
// because a finite global ceiling is stricter, and — unconditionally,
// regardless of whether an edge was found to sit inside a cycle by
// validateCycles — rejects any loop policy whose MaxTraversals is not a
// positive integer. The overlap with validateCycles' cycle.unbounded-
// loop-edge is intentional: that check only fires for edges Tarjan actually
// found inside a cycle and only catches a *missing* Loop field entirely;
// this check fires for every edge that *has* a Loop field, anywhere in the
// graph, and specifically catches an explicit but unsafe MaxTraversals
// value (including -1, which is otherwise the valid "unlimited" sentinel
// for every other budget dimension in this schema).
func validateSchemaBudgets(def WorkflowDefinition, idx *PositionIndex) Diagnostics {
	var diags Diagnostics
	b := def.Spec.Budgets

	validateSchemaLimit("spec.budgets.maxTransitions", "spec.budgets", b.MaxTransitions, "", &diags)
	validateSchemaLimit("spec.budgets.maxTokens", "spec.budgets", b.MaxTokens, "", &diags)
	validateSchemaLimit("spec.budgets.maxLocalIterations", "spec.budgets", b.MaxLocalIterations, "", &diags)
	validateSchemaLimit("spec.budgets.maxRetries", "spec.budgets", b.MaxRetries, "", &diags)

	for i, n := range def.Spec.Nodes {
		fieldPrefix := fmt.Sprintf("spec.nodes[%d]", i)
		validateSchemaLimit(fieldPrefix+".maxVisits", fieldPrefix+".maxVisits", n.MaxVisits, n.ID, &diags)
		validateSchemaLimit(fieldPrefix+".localIterations", fieldPrefix+".localIterations", n.LocalIterations, n.ID, &diags)
		validateSchemaLimit(fieldPrefix+".tokenBudget", fieldPrefix+".tokenBudget", n.TokenBudget, n.ID, &diags)

		checkContradictoryLimit(n.ID, "maxVisits (compared against the global maxTransitions ceiling)", n.MaxVisits, b.MaxTransitions, &diags)
		checkContradictoryLimit(n.ID, "localIterations", n.LocalIterations, b.MaxLocalIterations, &diags)
		checkContradictoryLimit(n.ID, "tokenBudget", n.TokenBudget, b.MaxTokens, &diags)
	}

	for _, e := range def.Spec.Edges {
		if e.Loop == nil {
			continue
		}
		if e.Loop.MaxTraversals <= 0 {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Rule:     "budget.unsafe-unlimited-loop",
				Message:  fmt.Sprintf("edge %q loop.maxTraversals = %d is invalid: a loop budget must always be a positive integer (-1/unlimited is not allowed for loops, even though it is allowed for other budget dimensions)", e.ID, e.Loop.MaxTraversals),
				EdgeID:   e.ID,
			})
		}
	}

	return diags
}

// validateSchemaLimit reports budget.invalid-limit when l is non-nil and
// neither positive nor exactly -1 (the Unlimited sentinel, matching
// types.go's Limit convention). displayPath is used in the message;
// positionFieldPath is looked up against idx by attachPositions (it may
// differ from displayPath when the position index only has a coarser
// position available, e.g. the whole spec.budgets block rather than one of
// its individual keys).
func validateSchemaLimit(displayPath, positionFieldPath string, l *SchemaLimit, nodeID string, diags *Diagnostics) {
	if l == nil {
		return
	}
	v := int(*l)
	if v == -1 || v > 0 {
		return
	}
	*diags = append(*diags, Diagnostic{
		Severity:  SeverityError,
		Rule:      "budget.invalid-limit",
		Message:   fmt.Sprintf("%s = %d is invalid: must be positive or exactly -1 (unlimited)", displayPath, v),
		NodeID:    nodeID,
		FieldPath: positionFieldPath,
	})
}

// checkContradictoryLimit warns when a node-level limit is a finite
// positive value strictly greater than a finite positive global ceiling for
// the same budget dimension — such a node limit can never actually bind,
// since the global ceiling always triggers first. A node limit that is
// smaller (stricter) than the global ceiling is always fine and not
// checked here. Neither limit being present, or either one being the -1
// (unlimited) sentinel, is deliberately not flagged: an unlimited node
// limit paired with a finite global ceiling just means the author is
// relying entirely on the global ceiling, which is a legitimate choice, not
// a contradiction.
func checkContradictoryLimit(nodeID, dimension string, nodeLimit, globalLimit *SchemaLimit, diags *Diagnostics) {
	if nodeLimit == nil || globalLimit == nil {
		return
	}
	nv, gv := int(*nodeLimit), int(*globalLimit)
	if nv <= 0 || gv <= 0 {
		return
	}
	if nv > gv {
		*diags = append(*diags, Diagnostic{
			Severity: SeverityWarning,
			Rule:     "budget.contradictory-limit",
			Message:  fmt.Sprintf("node %q %s limit (%d) exceeds the global ceiling (%d) and could never bind", nodeID, dimension, nv, gv),
			NodeID:   nodeID,
		})
	}
}

// ---------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------

// normalizedNodeType returns n.Type, defaulting an empty value to "role".
// The loader's own normalizeDefaults already does this same defaulting for
// definitions that went through LoadDefinition, but the validator is also
// meant to be usable directly against hand-built WorkflowDefinition values
// (e.g. in tests, or a future compiler entry point that skips the file
// loader entirely), so this mirrors that default locally rather than
// assuming every caller normalized first.
func normalizedNodeType(n SchemaNode) string {
	t := strings.TrimSpace(n.Type)
	if t == "" {
		return "role"
	}
	return t
}

// collectNodeIDs returns every node ID in def.Spec.Nodes, in declaration
// order, with duplicates collapsed to their first occurrence — the
// candidate list used for suggestClosest and reachability bookkeeping.
func collectNodeIDs(def WorkflowDefinition) []string {
	ids := make([]string, 0, len(def.Spec.Nodes))
	seen := make(map[string]bool, len(def.Spec.Nodes))
	for _, n := range def.Spec.Nodes {
		if seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		ids = append(ids, n.ID)
	}
	return ids
}

// buildNodeByID indexes def.Spec.Nodes by ID, keeping the first occurrence
// of any duplicate ID (duplicates themselves are reported separately by
// validateStructural).
func buildNodeByID(def WorkflowDefinition) map[string]SchemaNode {
	m := make(map[string]SchemaNode, len(def.Spec.Nodes))
	for _, n := range def.Spec.Nodes {
		if _, exists := m[n.ID]; !exists {
			m[n.ID] = n
		}
	}
	return m
}

// containsString reports whether target is present in list.
func containsString(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

// removeString returns a copy of list with every element equal to target
// removed, preserving order. Used to render "the other edges in this
// conflict" in a diagnostic message without repeating the edge's own ID.
func removeString(list []string, target string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}

// edgeIDList extracts the ID of every edge in edges, in order.
func edgeIDList(edges []SchemaEdge) []string {
	ids := make([]string, len(edges))
	for i, e := range edges {
		ids[i] = e.ID
	}
	return ids
}

// fromOutcomeKey groups edges by their (From, Outcome) pair — the routing
// dimension along which two edges compete for the same trigger.
type fromOutcomeKey struct {
	from    string
	outcome string
}

// groupEdgesByFromOutcome groups edges by (From, When.Outcome), returning
// the group keys in first-appearance order (so callers that range over the
// result get deterministic diagnostic ordering) alongside the groups
// themselves.
func groupEdgesByFromOutcome(edges []SchemaEdge) ([]fromOutcomeKey, map[fromOutcomeKey][]SchemaEdge) {
	order := make([]fromOutcomeKey, 0, len(edges))
	groups := make(map[fromOutcomeKey][]SchemaEdge, len(edges))
	for _, e := range edges {
		k := fromOutcomeKey{from: e.From, outcome: e.When.Outcome}
		if _, exists := groups[k]; !exists {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}
	return order, groups
}

// bfs returns the set of node IDs reachable from start (inclusive) by
// following adjacency edges.
func bfs(start string, adjacency map[string][]string) map[string]bool {
	visited := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adjacency[cur] {
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
	}
	return visited
}
