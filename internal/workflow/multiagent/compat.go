// compat.go is PR4's Phase 1 compatibility bridge. It converts the legacy
// Definition/RoleConfig/TransitionRule shape (config.go) — the shape
// embedded in every historical run manifest and produced by
// DefaultReferenceDefinition()/ApplyReferenceOverrides() — directly into a
// *CompiledGraph, WITHOUT going through WorkflowDefinition/YAML/the
// loader/the validator/PR3's Compile() at all. CompiledGraph is the single
// convergence point the supervisor executes: old embedded Definition JSON ->
// CompatAdaptDefinition -> CompiledGraph; new authored YAML -> loader ->
// validator -> compiler.Compile -> CompiledGraph.
package multiagent

import (
	"strconv"
)

// compatSchemaVersion marks a CompiledGraph as adapted from the legacy
// Definition shape rather than compiled from an authored WorkflowDefinition.
// It is deliberately distinct from SchemaAPIVersion (schema_types.go), which
// only ever describes an authored YAML/JSON document's apiVersion field —
// a legacy Definition has no such field.
const compatSchemaVersion = "multiagent.prism.dev/phase1-definition"

// CompatAdaptDefinition builds a CompiledGraph directly from the legacy
// Definition/RoleConfig/TransitionRule shape. def must already satisfy
// Definition.Validate() — every existing call site (RunLocator.readManifest,
// cmd/prism-cli's loadReferenceManifest) already calls Validate() before
// constructing a runtime from a manifest — this function does not re-validate,
// it adapts.
//
// Exported (capital C), unlike the lowercase name sketched in the Phase 3
// plan's prose: cmd/prism-cli (package main, a different package) must call
// this directly at its runtime-construction call site
// (cmd_workflow_multiagent.go's openReferenceRuntime), and an unexported
// function cannot cross that package boundary. See the PR4 report for this
// deviation.
//
// Node/edge IDs are produced by calling graph_projection.go's existing
// nodeID/terminalNodeID/edgeID functions directly — the same functions
// compiler.go's buildCompiledGraph calls for the authored-YAML path — so a
// compat-adapted graph's stable IDs are byte-identical to what
// graph_projection.go's own Definition-based BuildRunGraph already produces
// for the same Definition. That equality is not just aesthetic: it is what
// makes LoopKind(edgeID(RoleTester, OutcomeTestsFailed)) (the
// LoopTesterToDeveloper var, supervisor_types.go) the correct lookup key for
// both this function's loops map and CompiledGraph.LoopFor's reconstruction
// of that same key from a ResolvedTransition — see the "Critical correctness
// point" in the PR4 task and loop_traversal_legacy_json_test.go.
func CompatAdaptDefinition(def Definition) (*CompiledGraph, error) {
	nodeOrder := make([]string, 0, len(def.Roles))
	nodes := make(map[string]CompiledNode, len(def.Roles))
	outcomeIndex := make(map[Role]map[TransitionOutcome]bool, len(def.Roles))

	for _, cfg := range def.Roles {
		id := nodeID(cfg.Role)
		allowed := allowedOutcomesForRole(def, cfg.Role)
		nodeOrder = append(nodeOrder, id)
		nodes[id] = CompiledNode{
			ID:              id,
			Role:            cfg.Role,
			Kind:            "role",
			DisplayName:     humanizeIdentifier(string(cfg.Role)),
			RoleConfig:      cloneRoleConfig(cfg),
			AllowedOutcomes: allowed,
		}
		set := make(map[TransitionOutcome]bool, len(allowed))
		for _, oc := range allowed {
			set[oc] = true
		}
		outcomeIndex[cfg.Role] = set
	}

	var terminalNodeIDs []string
	seenTerminal := make(map[TerminalCondition]bool, 4)
	edgeOrder := make([]string, 0, len(def.Transitions))
	edges := make(map[string]CompiledEdge, len(def.Transitions))
	outgoing := make(map[transitionKey]ResolvedTransition, len(def.Transitions))

	for _, rule := range def.Transitions {
		id := edgeID(rule.From, rule.Outcome)
		edgeOrder = append(edgeOrder, id)
		edges[id] = CompiledEdge{
			ID:       id,
			From:     rule.From,
			Outcome:  rule.Outcome,
			To:       rule.To,
			Terminal: rule.Terminal,
			Label:    humanizeIdentifier(string(rule.Outcome)),
		}
		outgoing[transitionKey{role: rule.From, outcome: rule.Outcome}] = ResolvedTransition{
			From:     rule.From,
			Outcome:  rule.Outcome,
			To:       rule.To,
			Terminal: rule.Terminal,
		}

		if rule.Terminal != "" && !seenTerminal[rule.Terminal] {
			seenTerminal[rule.Terminal] = true
			tid := terminalNodeID(rule.Terminal)
			terminalNodeIDs = append(terminalNodeIDs, tid)
			nodeOrder = append(nodeOrder, tid)
			nodes[tid] = CompiledNode{
				ID:                tid,
				Kind:              "terminal",
				DisplayName:       humanizeIdentifier(string(rule.Terminal)),
				TerminalCondition: rule.Terminal,
			}
		}
	}

	// Build EXACTLY the two Phase 1 loops, keyed by the edgeID-based
	// LoopTesterToDeveloper/LoopReviewerToDeveloper vars (supervisor_types.go)
	// — NOT the pre-Phase-3 literal string constants — so CompiledGraph.LoopFor
	// (which always reconstructs LoopKind(edgeID(transition.From,
	// transition.Outcome)) regardless of which path produced the graph) finds
	// them. MaxTraversals/OnExhausted mirror supervisor.go's pre-PR4
	// checkLoopBudget/correctionLoop exactly: both Phase 1 loops fail (never
	// route_to) on exhaustion, and their limits come from the legacy
	// Definition.Budgets fields that only this function reads from now on.
	loopOrder := []LoopKind{LoopTesterToDeveloper, LoopReviewerToDeveloper}
	loops := map[LoopKind]CompiledLoop{
		LoopTesterToDeveloper: {
			Kind:          LoopTesterToDeveloper,
			EdgeID:        string(LoopTesterToDeveloper),
			MaxTraversals: def.Budgets.MaxTesterToDeveloperLoops,
			OnExhausted:   LoopExhaustedFail,
		},
		LoopReviewerToDeveloper: {
			Kind:          LoopReviewerToDeveloper,
			EdgeID:        string(LoopReviewerToDeveloper),
			MaxTraversals: def.Budgets.MaxReviewerToDeveloperLoops,
			OnExhausted:   LoopExhaustedFail,
		},
	}

	// Phase 1's entry role is hardcoded to RolePlanner throughout this
	// package (Supervisor.newRunState sets CurrentRole: RolePlanner directly,
	// unconditionally — PR4 deliberately leaves that control flow unchanged,
	// see the PR4 report); Definition.Validate requires every Phase1Roles()
	// entry, including RolePlanner, to have a configuration, so this is
	// always resolvable for any def that has passed Validate().
	entryNodeID := nodeID(RolePlanner)

	graph := &CompiledGraph{
		workflowID:      def.ID,
		workflowVersion: strconv.Itoa(def.Version),
		schemaVersion:   compatSchemaVersion,
		entryNodeID:     entryNodeID,
		nodeOrder:       nodeOrder,
		edgeOrder:       edgeOrder,
		loopOrder:       loopOrder,
		nodes:           nodes,
		edges:           edges,
		outgoing:        outgoing,
		outcomeIndex:    outcomeIndex,
		terminalNodeIDs: terminalNodeIDs,
		loops:           loops,
		budgets:         cloneBudgetLimits(def.Budgets),
		registryVersion: def.Version,
	}
	graph.fingerprint = computeFingerprint(graph)
	return graph, nil
}

// allowedOutcomesForRole derives the distinct outcomes role's declared
// transitions produce, in first-seen Transitions order. The legacy Definition
// shape has no explicit per-role "allowed outcomes" declaration (unlike
// SchemaNode.AllowedOutcomes in the new authored schema) — CompiledGraph's
// outcomeIndex/HasOutcome/OutcomeValidFor need one, so this derives it from
// the same source of truth Definition.ValidateTransition already trusts:
// which outcomes role's own transition rules actually declare.
func allowedOutcomesForRole(def Definition, role Role) []TransitionOutcome {
	seen := make(map[TransitionOutcome]bool)
	var outcomes []TransitionOutcome
	for _, rule := range def.Transitions {
		if rule.From != role || seen[rule.Outcome] {
			continue
		}
		seen[rule.Outcome] = true
		outcomes = append(outcomes, rule.Outcome)
	}
	return outcomes
}
