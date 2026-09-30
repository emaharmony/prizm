package multiagent

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------

// hasDiagnostic reports whether diags contains an entry matching rule
// exactly, and (when non-empty) matching nodeID/edgeID exactly too. Pass ""
// for nodeID/edgeID to skip that filter.
func hasDiagnostic(diags Diagnostics, rule, nodeID, edgeID string) bool {
	for _, d := range diags {
		if d.Rule != rule {
			continue
		}
		if nodeID != "" && d.NodeID != nodeID {
			continue
		}
		if edgeID != "" && d.EdgeID != edgeID {
			continue
		}
		return true
	}
	return false
}

// findDiagnostic returns the first diagnostic matching rule (and, when
// non-empty, nodeID/edgeID), plus whether one was found.
func findDiagnostic(diags Diagnostics, rule, nodeID, edgeID string) (Diagnostic, bool) {
	for _, d := range diags {
		if d.Rule != rule {
			continue
		}
		if nodeID != "" && d.NodeID != nodeID {
			continue
		}
		if edgeID != "" && d.EdgeID != edgeID {
			continue
		}
		return d, true
	}
	return Diagnostic{}, false
}

func severityOf(diags Diagnostics, rule string) (DiagnosticSeverity, bool) {
	for _, d := range diags {
		if d.Rule == rule {
			return d.Severity, true
		}
	}
	return "", false
}

// node/edge construction shorthands used throughout this file.

func roleNode(id string, outcomes ...string) SchemaNode {
	return SchemaNode{ID: id, Type: "role", AgentProfile: "profile-" + id, AllowedOutcomes: outcomes}
}

func terminalNode(id, condition string) SchemaNode {
	return SchemaNode{ID: id, Type: "terminal", TerminalCondition: condition}
}

// baseDef returns a minimal, independently valid two-node workflow
// (start --ok--> end) that individual tests copy and mutate to isolate
// exactly one violation at a time.
func baseDef() WorkflowDefinition {
	return WorkflowDefinition{
		APIVersion: SchemaAPIVersion,
		Kind:       SchemaKind,
		Metadata:   WorkflowMetadata{Name: "base", ID: "base", Version: "1.0.0"},
		Spec: WorkflowSpec{
			EntryNode: "start",
			Nodes: []SchemaNode{
				roleNode("start", "ok"),
				terminalNode("end", "completed"),
			},
			Edges: []SchemaEdge{
				edge("start-end", "start", "end", "ok"),
			},
		},
	}
}

func TestValidateDefinitionBaseFixtureIsClean(t *testing.T) {
	diags := ValidateDefinition(baseDef(), nil)
	if diags.HasErrors() {
		t.Fatalf("baseDef should have zero error diagnostics, got %v", diags)
	}
}

// ---------------------------------------------------------------------
// structural.*
// ---------------------------------------------------------------------

func TestStructuralEntryNodeMissing(t *testing.T) {
	def := baseDef()
	def.Spec.EntryNode = ""
	diags := validateStructural(def, nil)
	if !hasDiagnostic(diags, "structural.entry-node-missing", "", "") {
		t.Fatalf("expected structural.entry-node-missing, got %v", diags)
	}
}

func TestStructuralEntryNodeUnknown(t *testing.T) {
	def := baseDef()
	def.Spec.EntryNode = "strt" // typo of "start"
	diags := validateStructural(def, nil)
	d, ok := findDiagnostic(diags, "structural.entry-node-unknown", "", "")
	if !ok {
		t.Fatalf("expected structural.entry-node-unknown, got %v", diags)
	}
	if d.Suggestion != "start" {
		t.Errorf("suggestion = %q, want %q", d.Suggestion, "start")
	}
}

func TestStructuralDuplicateNodeID(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes = append(def.Spec.Nodes, roleNode("start", "ok"))
	diags := validateStructural(def, nil)
	if !hasDiagnostic(diags, "structural.duplicate-node-id", "start", "") {
		t.Fatalf("expected structural.duplicate-node-id for 'start', got %v", diags)
	}
}

func TestStructuralDuplicateEdgeID(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes = append(def.Spec.Nodes, roleNode("other", "ok"))
	def.Spec.Edges = append(def.Spec.Edges, edge("start-end", "start", "other", "ok"))
	diags := validateStructural(def, nil)
	if !hasDiagnostic(diags, "structural.duplicate-edge-id", "", "start-end") {
		t.Fatalf("expected structural.duplicate-edge-id for 'start-end', got %v", diags)
	}
}

func TestStructuralDanglingEdgeRefFrom(t *testing.T) {
	def := baseDef()
	def.Spec.Edges[0].From = "nope"
	diags := validateStructural(def, nil)
	if !hasDiagnostic(diags, "structural.dangling-edge-ref", "", "start-end") {
		t.Fatalf("expected structural.dangling-edge-ref for bad 'from', got %v", diags)
	}
}

func TestStructuralDanglingEdgeRefTo(t *testing.T) {
	def := baseDef()
	def.Spec.Edges[0].To = "nope"
	diags := validateStructural(def, nil)
	if !hasDiagnostic(diags, "structural.dangling-edge-ref", "", "start-end") {
		t.Fatalf("expected structural.dangling-edge-ref for bad 'to', got %v", diags)
	}
}

func TestStructuralDanglingEdgeRefEmptyTo(t *testing.T) {
	def := baseDef()
	def.Spec.Edges[0].To = ""
	diags := validateStructural(def, nil)
	if !hasDiagnostic(diags, "structural.dangling-edge-ref", "", "start-end") {
		t.Fatalf("expected structural.dangling-edge-ref for empty 'to', got %v", diags)
	}
}

func TestStructuralNoTerminalPath(t *testing.T) {
	def := WorkflowDefinition{
		Spec: WorkflowSpec{
			EntryNode: "start",
			Nodes:     []SchemaNode{roleNode("start", "ok")},
			Edges:     nil,
		},
	}
	diags := validateStructural(def, nil)
	if !hasDiagnostic(diags, "structural.no-terminal-path", "", "") {
		t.Fatalf("expected structural.no-terminal-path, got %v", diags)
	}
}

func TestStructuralUnreachableNodeIsWarning(t *testing.T) {
	def := baseDef()
	// "orphan" is a role node that can still reach a terminal (it has an
	// edge to "end"), but nothing routes into it from the entry node.
	def.Spec.Nodes = append(def.Spec.Nodes, roleNode("orphan", "ok"))
	def.Spec.Edges = append(def.Spec.Edges, edge("orphan-end", "orphan", "end", "ok"))
	diags := validateStructural(def, nil)
	d, ok := findDiagnostic(diags, "structural.unreachable-node", "orphan", "")
	if !ok {
		t.Fatalf("expected structural.unreachable-node for 'orphan', got %v", diags)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", d.Severity)
	}
}

func TestStructuralDeadNodeIsError(t *testing.T) {
	def := baseDef()
	// "orphan" is neither reachable from entry nor able to reach any
	// terminal node (it has no outgoing edges at all) — genuinely dead.
	def.Spec.Nodes = append(def.Spec.Nodes, roleNode("orphan", "ok"))
	diags := validateStructural(def, nil)
	d, ok := findDiagnostic(diags, "structural.dead-node", "orphan", "")
	if !ok {
		t.Fatalf("expected structural.dead-node for 'orphan', got %v", diags)
	}
	if d.Severity != SeverityError {
		t.Errorf("severity = %q, want error", d.Severity)
	}
}

func TestStructuralInvalidNodeType(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].Type = "bogus"
	diags := validateStructural(def, nil)
	if !hasDiagnostic(diags, "structural.invalid-node-type", "start", "") {
		t.Fatalf("expected structural.invalid-node-type for 'start', got %v", diags)
	}
}

// ---------------------------------------------------------------------
// routing.*
// ---------------------------------------------------------------------

func TestRoutingOutcomeNotAllowed(t *testing.T) {
	def := baseDef()
	def.Spec.Edges[0].When.Outcome = "unexpected"
	diags := validateRouting(def, nil)
	d, ok := findDiagnostic(diags, "routing.outcome-not-allowed", "start", "start-end")
	if !ok {
		t.Fatalf("expected routing.outcome-not-allowed, got %v", diags)
	}
	_ = d
}

func TestRoutingOutcomeNotAllowedSuggestsClosest(t *testing.T) {
	def := baseDef()
	def.Spec.Edges[0].When.Outcome = "okk" // typo of "ok"
	diags := validateRouting(def, nil)
	d, ok := findDiagnostic(diags, "routing.outcome-not-allowed", "start", "start-end")
	if !ok {
		t.Fatalf("expected routing.outcome-not-allowed, got %v", diags)
	}
	if d.Suggestion != "ok" {
		t.Errorf("suggestion = %q, want %q", d.Suggestion, "ok")
	}
}

func TestRoutingTieBreakUsedWarning(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes = append(def.Spec.Nodes, terminalNode("alt-end", "completed"))
	def.Spec.Nodes[0].AllowedOutcomes = []string{"ok"}
	e1 := edge("e1", "start", "end", "ok")
	e1.Priority = 1
	e2 := edge("e2", "start", "alt-end", "ok")
	e2.Priority = 2
	def.Spec.Edges = []SchemaEdge{e1, e2}

	diags := validateRouting(def, nil)
	d, ok := findDiagnostic(diags, "routing.tie-break-used", "start", "")
	if !ok {
		t.Fatalf("expected routing.tie-break-used, got %v", diags)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", d.Severity)
	}
}

func TestRoutingAmbiguousTransitionError(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes = append(def.Spec.Nodes, terminalNode("alt-end", "completed"))
	def.Spec.Nodes[0].AllowedOutcomes = []string{"ok"}
	e1 := edge("e1", "start", "end", "ok") // priority defaults to 0
	e2 := edge("e2", "start", "alt-end", "ok")
	def.Spec.Edges = []SchemaEdge{e1, e2}

	diags := validateRouting(def, nil)
	d1, ok1 := findDiagnostic(diags, "routing.ambiguous-transition", "start", "e1")
	d2, ok2 := findDiagnostic(diags, "routing.ambiguous-transition", "start", "e2")
	if !ok1 || !ok2 {
		t.Fatalf("expected routing.ambiguous-transition for both e1 and e2, got %v", diags)
	}
	if d1.Severity != SeverityError || d2.Severity != SeverityError {
		t.Errorf("expected error severity for both, got %q and %q", d1.Severity, d2.Severity)
	}
}

func TestRoutingTransitionFromTerminal(t *testing.T) {
	def := baseDef()
	def.Spec.Edges = append(def.Spec.Edges, edge("end-start", "end", "start", "ok"))
	diags := validateRouting(def, nil)
	if !hasDiagnostic(diags, "routing.transition-from-terminal", "end", "end-start") {
		t.Fatalf("expected routing.transition-from-terminal, got %v", diags)
	}
}

func TestRoutingUnhandledOutcome(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].AllowedOutcomes = []string{"ok", "retry"} // "retry" has no edge
	diags := validateRouting(def, nil)
	if !hasDiagnostic(diags, "routing.unhandled-outcome", "start", "") {
		t.Fatalf("expected routing.unhandled-outcome, got %v", diags)
	}
}

func TestRoutingUnhandledOutcomeAllowedWhenDefaultFail(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].AllowedOutcomes = []string{"ok", "retry"}
	def.Spec.Defaults.Failure.DefaultOnUnhandledOutcome = "fail"
	diags := validateRouting(def, nil)
	if hasDiagnostic(diags, "routing.unhandled-outcome", "start", "") {
		t.Fatalf("did not expect routing.unhandled-outcome when defaultOnUnhandledOutcome is \"fail\", got %v", diags)
	}
}

// ---------------------------------------------------------------------
// cycle.* (unbounded-loop-edge and invalid-exhaustion-route already have
// dedicated positive fixtures in cycle_detection_test.go; this file covers
// the remaining two rule IDs.)
// ---------------------------------------------------------------------

func TestCycleInvalidExhaustionBehavior(t *testing.T) {
	loop := edge("b-a", "b", "a", "back")
	loop.Loop = &SchemaLoopPolicy{MaxTraversals: 3, OnExhausted: "explode"}
	def := buildLinearThenCycleDef(loop)

	diags := validateCycles(def, nil)
	if !hasDiagnostic(diags, "cycle.invalid-exhaustion-behavior", "", "b-a") {
		t.Fatalf("expected cycle.invalid-exhaustion-behavior, got %v", diags)
	}
}

func TestCycleNonDeterministicLoopWarning(t *testing.T) {
	// Two loop edges both route from "b" on outcome "back" into the same
	// cycle {a,b}: routing.ambiguous-transition (error) fires from
	// validateRouting; validateCycles separately re-flags the pair with its
	// own warning-severity rule so the diagnostic list makes the cycle
	// connection explicit.
	loop1 := edge("b-a-1", "b", "a", "back")
	loop1.Loop = &SchemaLoopPolicy{MaxTraversals: 3, OnExhausted: "fail"}
	loop2 := edge("b-a-2", "b", "a", "back")
	loop2.Loop = &SchemaLoopPolicy{MaxTraversals: 3, OnExhausted: "fail"}

	def := buildLinearThenCycleDef(loop1)
	def.Spec.Edges = append(def.Spec.Edges, loop2)

	diags := validateCycles(def, nil)
	d, ok := findDiagnostic(diags, "cycle.non-deterministic-loop", "b", "")
	if !ok {
		t.Fatalf("expected cycle.non-deterministic-loop, got %v", diags)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning (routing.ambiguous-transition already carries the blocking error)", d.Severity)
	}
}

// ---------------------------------------------------------------------
// governance.*
// ---------------------------------------------------------------------

func TestGovernanceSelfApproval(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].Approval = &SchemaApprovalPolicy{Required: true, Approvers: []string{"start"}}
	diags := validateGovernanceWithProfiles(def, nil, nil)
	if !hasDiagnostic(diags, "governance.self-approval", "start", "") {
		t.Fatalf("expected governance.self-approval, got %v", diags)
	}
}

func TestGovernanceUnknownApproverWarning(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].Approval = &SchemaApprovalPolicy{Required: true, Approvers: []string{"nobody"}}
	diags := validateGovernanceWithProfiles(def, nil, nil)
	d, ok := findDiagnostic(diags, "governance.unknown-approver", "start", "")
	if !ok {
		t.Fatalf("expected governance.unknown-approver, got %v", diags)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", d.Severity)
	}
}

func TestGovernanceInvalidToolList(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].AllowedTools = []string{"shell", ""}
	diags := validateGovernanceWithProfiles(def, nil, nil)
	if !hasDiagnostic(diags, "governance.invalid-tool-list", "start", "") {
		t.Fatalf("expected governance.invalid-tool-list for an empty entry, got %v", diags)
	}
}

func TestGovernanceDuplicateCapability(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].Capabilities = []string{"code-edit", "code-edit"}
	diags := validateGovernanceWithProfiles(def, nil, nil)
	if !hasDiagnostic(diags, "governance.duplicate-capability", "start", "") {
		t.Fatalf("expected governance.duplicate-capability, got %v", diags)
	}
}

func TestGovernanceMissingAgentProfile(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].AgentProfile = ""
	diags := validateGovernanceWithProfiles(def, nil, nil)
	if !hasDiagnostic(diags, "governance.missing-agent-profile", "start", "") {
		t.Fatalf("expected governance.missing-agent-profile, got %v", diags)
	}
}

func TestGovernanceUnknownValidationProfile(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].Validation = &SchemaValidationPolicy{Profiles: []string{"strict-lint"}}
	diags := validateGovernanceWithProfiles(def, nil, []string{"basic-lint", "security-scan"})
	d, ok := findDiagnostic(diags, "governance.unknown-validation-profile", "start", "")
	if !ok {
		t.Fatalf("expected governance.unknown-validation-profile, got %v", diags)
	}
	if d.Severity != SeverityError {
		t.Errorf("severity = %q, want error (a supplied registry is authoritative)", d.Severity)
	}
}

func TestGovernanceUnknownValidationProfileSkippedWhenRegistryNil(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].Validation = &SchemaValidationPolicy{Profiles: []string{"anything-goes"}}
	diags := validateGovernanceWithProfiles(def, nil, nil)
	if hasDiagnostic(diags, "governance.unknown-validation-profile", "start", "") {
		t.Fatalf("did not expect governance.unknown-validation-profile check to run when knownProfiles is nil, got %v", diags)
	}
}

// ---------------------------------------------------------------------
// budget.*
// ---------------------------------------------------------------------

func schemaLimit(v int) *SchemaLimit {
	l := SchemaLimit(v)
	return &l
}

func TestBudgetInvalidLimitZero(t *testing.T) {
	def := baseDef()
	def.Spec.Budgets.MaxTokens = schemaLimit(0)
	diags := validateSchemaBudgets(def, nil)
	if !hasDiagnostic(diags, "budget.invalid-limit", "", "") {
		t.Fatalf("expected budget.invalid-limit for maxTokens=0, got %v", diags)
	}
}

func TestBudgetInvalidLimitNegativeNotSentinel(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].MaxVisits = schemaLimit(-5)
	diags := validateSchemaBudgets(def, nil)
	if !hasDiagnostic(diags, "budget.invalid-limit", "start", "") {
		t.Fatalf("expected budget.invalid-limit for maxVisits=-5, got %v", diags)
	}
}

func TestBudgetInvalidLimitAllowsUnlimitedSentinel(t *testing.T) {
	def := baseDef()
	def.Spec.Budgets.MaxTokens = schemaLimit(-1)
	diags := validateSchemaBudgets(def, nil)
	if hasDiagnostic(diags, "budget.invalid-limit", "", "") {
		t.Fatalf("did not expect budget.invalid-limit for the -1 unlimited sentinel, got %v", diags)
	}
}

func TestBudgetContradictoryLimitWarning(t *testing.T) {
	def := baseDef()
	def.Spec.Budgets.MaxTokens = schemaLimit(100)
	def.Spec.Nodes[0].TokenBudget = schemaLimit(500) // larger than the global ceiling
	diags := validateSchemaBudgets(def, nil)
	d, ok := findDiagnostic(diags, "budget.contradictory-limit", "start", "")
	if !ok {
		t.Fatalf("expected budget.contradictory-limit, got %v", diags)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", d.Severity)
	}
}

func TestBudgetContradictoryLimitNotFlaggedWhenStricter(t *testing.T) {
	def := baseDef()
	def.Spec.Budgets.MaxTokens = schemaLimit(500)
	def.Spec.Nodes[0].TokenBudget = schemaLimit(100) // stricter than global: fine
	diags := validateSchemaBudgets(def, nil)
	if hasDiagnostic(diags, "budget.contradictory-limit", "start", "") {
		t.Fatalf("did not expect budget.contradictory-limit for a stricter node limit, got %v", diags)
	}
}

func TestBudgetUnsafeUnlimitedLoop(t *testing.T) {
	def := baseDef()
	def.Spec.Edges[0].Loop = &SchemaLoopPolicy{MaxTraversals: -1, OnExhausted: "fail"}
	diags := validateSchemaBudgets(def, nil)
	if !hasDiagnostic(diags, "budget.unsafe-unlimited-loop", "", "start-end") {
		t.Fatalf("expected budget.unsafe-unlimited-loop for maxTraversals=-1, got %v", diags)
	}
}

func TestBudgetUnsafeUnlimitedLoopZero(t *testing.T) {
	def := baseDef()
	def.Spec.Edges[0].Loop = &SchemaLoopPolicy{MaxTraversals: 0, OnExhausted: "fail"}
	diags := validateSchemaBudgets(def, nil)
	if !hasDiagnostic(diags, "budget.unsafe-unlimited-loop", "", "start-end") {
		t.Fatalf("expected budget.unsafe-unlimited-loop for maxTraversals=0, got %v", diags)
	}
}

// ---------------------------------------------------------------------
// Position attachment (idx is nil-safe; when supplied, NodeID/EdgeID-tied
// diagnostics get a real line/column)
// ---------------------------------------------------------------------

func TestValidateDefinitionAttachesPositionsFromIndex(t *testing.T) {
	def, idx, diags, err := LoadDefinition("testdata/schema/valid/software-delivery.yaml")
	if err != nil {
		t.Fatalf("LoadDefinition: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %v", diags)
	}

	// Force a governance violation on a known node ("planner") so we can
	// assert its position was resolved through idx.
	def.Spec.Nodes[0].AgentProfile = ""
	got := ValidateDefinition(def, idx)
	d, ok := findDiagnostic(got, "governance.missing-agent-profile", "planner", "")
	if !ok {
		t.Fatalf("expected governance.missing-agent-profile for planner, got %v", got)
	}
	if d.Line <= 0 {
		t.Errorf("expected a resolved line number via the position index, got Line=%d", d.Line)
	}
}

func TestValidateDefinitionNilIndexOmitsPositionButKeepsContext(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].AgentProfile = ""
	diags := ValidateDefinition(def, nil)
	d, ok := findDiagnostic(diags, "governance.missing-agent-profile", "start", "")
	if !ok {
		t.Fatalf("expected governance.missing-agent-profile, got %v", diags)
	}
	if d.Line != 0 || d.Column != 0 {
		t.Errorf("expected zero-value Line/Column with a nil index, got Line=%d Column=%d", d.Line, d.Column)
	}
	if d.NodeID != "start" {
		t.Errorf("expected NodeID context to survive a nil index, got %q", d.NodeID)
	}
}

// ---------------------------------------------------------------------
// Comprehensive "valid definition" baseline
// ---------------------------------------------------------------------

// comprehensiveValidDef mirrors Phase 1's reference planner/developer/
// tester/reviewer shape (the same shape as
// testdata/schema/valid/software-delivery.yaml) with two bounded correction
// loops (tester->developer, reviewer->developer). Unlike that PR1 fixture,
// this one also declares an explicit bounded loop: policy on the two
// "forward progress" edges that fall inside the same 3-node cyclic SCC
// {developer, tester, reviewer} (dev-to-tester and tester-to-reviewer) —
// see TestCycleValidationSCCGranularityAffectsPR1ReferenceFixture below for
// why that is required by this validator's (spec-literal) SCC-wide
// unbounded-loop-edge rule, and why the unmodified PR1 fixture does not
// pass it as-is.
func comprehensiveValidDef() WorkflowDefinition {
	fwdLoop := func() *SchemaLoopPolicy { return &SchemaLoopPolicy{MaxTraversals: 999, OnExhausted: "fail"} }
	backLoop := func() *SchemaLoopPolicy { return &SchemaLoopPolicy{MaxTraversals: 3, OnExhausted: "fail"} }

	devToTester := edge("dev-to-tester", "developer", "tester", "implementation_ready")
	devToTester.Loop = fwdLoop()
	testerToDev := edge("tester-to-dev", "tester", "developer", "tests_failed")
	testerToDev.Loop = backLoop()
	testerToReviewer := edge("tester-to-reviewer", "tester", "reviewer", "tests_passed")
	testerToReviewer.Loop = fwdLoop()
	reviewerToDev := edge("reviewer-to-dev", "reviewer", "developer", "changes_requested")
	reviewerToDev.Loop = backLoop()

	return WorkflowDefinition{
		APIVersion: SchemaAPIVersion,
		Kind:       SchemaKind,
		Metadata:   WorkflowMetadata{Name: "comprehensive", ID: "comprehensive", Version: "1.0.0"},
		Spec: WorkflowSpec{
			EntryNode: "planner",
			Budgets: SchemaBudgets{
				MaxTransitions: schemaLimit(40),
				MaxTokens:      schemaLimit(200000),
			},
			Nodes: []SchemaNode{
				roleNode("planner", "plan_ready"),
				roleNode("developer", "implementation_ready"),
				roleNode("tester", "tests_passed", "tests_failed"),
				roleNode("reviewer", "review_approved", "changes_requested"),
				terminalNode("completed", "completed"),
				terminalNode("failed", "failed"),
			},
			Edges: []SchemaEdge{
				edge("plan-to-dev", "planner", "developer", "plan_ready"),
				devToTester,
				testerToDev,
				testerToReviewer,
				reviewerToDev,
				edge("reviewer-to-completed", "reviewer", "completed", "review_approved"),
			},
		},
	}
}

func TestValidateDefinitionComprehensiveFixtureZeroErrors(t *testing.T) {
	diags := ValidateDefinition(comprehensiveValidDef(), nil)
	for _, d := range diags {
		if d.Severity == SeverityError {
			t.Errorf("unexpected error diagnostic: %v", d)
		}
	}
	// "failed" is a legitimate declared terminal with nothing currently
	// routed to it in this fixture — expected to be an unreachable-node
	// *warning*, not an error; assert it shows up as exactly that so this
	// test would fail loudly if severity classification ever regressed.
	if !hasDiagnostic(diags, "structural.unreachable-node", "failed", "") {
		t.Fatalf("expected structural.unreachable-node warning for 'failed', got %v", diags)
	}
}

// TestCycleValidationSCCGranularityAffectsPR1ReferenceFixture documents a
// real finding from implementing PR2 against PR1's own
// testdata/schema/valid/software-delivery.yaml: that fixture's
// developer/tester/reviewer nodes form a single 3-node strongly-connected
// component (developer->tester->reviewer->developer, plus the direct
// tester->developer back-edge), because Tarjan/SCC groups every mutually
// reachable node together regardless of which specific edges are the
// "real" back-edges of a correction loop. The Phase 3 plan's own validator
// rule ("every edge whose From and To are both in the same SCC must carry a
// bounded loop policy") is therefore SCC-wide, not limited to back-edges:
// it also requires the *forward* edges dev-to-tester and tester-to-reviewer
// to carry a loop: policy, even though neither is conceptually "a loop" by
// itself. The plan explicitly chose SCC-based detection over enumerating
// simple cycles ("unnecessary and combinatorially worse"), and this is the
// direct, foreseeable consequence of that choice for any workflow whose
// correction loop is not a strict two-node cycle — which includes Phase 1's
// own reference shape. This is flagged here, not silently patched around:
// PR1's fixture is unmodified (read-only per PR2's constraints), so this
// test documents the *current* behavior rather than asserting it is
// necessarily desirable — see the PR2 report for the recommendation to
// PR3/PR7.
func TestCycleValidationSCCGranularityAffectsPR1ReferenceFixture(t *testing.T) {
	def, _, loadDiags, err := LoadDefinition("testdata/schema/valid/software-delivery.yaml")
	if err != nil {
		t.Fatalf("LoadDefinition: %v", err)
	}
	if loadDiags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %v", loadDiags)
	}

	diags := validateCycles(def, nil)
	if !hasDiagnostic(diags, "cycle.unbounded-loop-edge", "developer", "dev-to-tester") {
		t.Fatalf("expected the SCC-wide rule to flag dev-to-tester (no loop: field) as unbounded, got %v — "+
			"if this now passes, the SCC/cycle detection semantics changed and the PR2 report's finding is stale", diags)
	}
	if !hasDiagnostic(diags, "cycle.unbounded-loop-edge", "tester", "tester-to-reviewer") {
		t.Fatalf("expected the SCC-wide rule to flag tester-to-reviewer (no loop: field) as unbounded, got %v", diags)
	}
}

// ---------------------------------------------------------------------
// ValidateDefinitionWithProfiles wiring
// ---------------------------------------------------------------------

func TestValidateDefinitionCallsWithProfilesWithNilRegistry(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].Validation = &SchemaValidationPolicy{Profiles: []string{"whatever"}}
	a := ValidateDefinition(def, nil)
	b := ValidateDefinitionWithProfiles(def, nil, nil)
	if len(a) != len(b) {
		t.Fatalf("ValidateDefinition should equal ValidateDefinitionWithProfiles(def, idx, nil); got %d vs %d diagnostics", len(a), len(b))
	}
}

func TestValidateDefinitionWithProfilesSuggestsClosestKnownProfile(t *testing.T) {
	def := baseDef()
	def.Spec.Nodes[0].Validation = &SchemaValidationPolicy{Profiles: []string{"strict-lintt"}}
	diags := ValidateDefinitionWithProfiles(def, nil, []string{"strict-lint"})
	d, ok := findDiagnostic(diags, "governance.unknown-validation-profile", "start", "")
	if !ok {
		t.Fatalf("expected governance.unknown-validation-profile, got %v", diags)
	}
	if d.Suggestion != "strict-lint" {
		t.Errorf("suggestion = %q, want %q", d.Suggestion, "strict-lint")
	}
}

// sanity: ensure every rule string used across this file follows the
// mandated dotted-category-prefix convention, so downstream strings.
// HasPrefix category-matching (CLI/API, later PRs) can rely on it.
func TestAllExerciseRulesFollowDottedPrefixConvention(t *testing.T) {
	rules := []string{
		"structural.entry-node-missing", "structural.entry-node-unknown",
		"structural.duplicate-node-id", "structural.duplicate-edge-id",
		"structural.dangling-edge-ref", "structural.no-terminal-path",
		"structural.unreachable-node", "structural.dead-node",
		"structural.invalid-node-type",
		"routing.outcome-not-allowed", "routing.tie-break-used",
		"routing.ambiguous-transition", "routing.transition-from-terminal",
		"routing.unhandled-outcome",
		"cycle.unbounded-loop-edge", "cycle.invalid-exhaustion-behavior",
		"cycle.invalid-exhaustion-route", "cycle.non-deterministic-loop",
		"governance.self-approval", "governance.unknown-approver",
		"governance.invalid-tool-list", "governance.duplicate-capability",
		"governance.missing-agent-profile", "governance.unknown-validation-profile",
		"budget.invalid-limit", "budget.contradictory-limit",
		"budget.unsafe-unlimited-loop",
	}
	prefixes := []string{"structural.", "routing.", "cycle.", "governance.", "budget."}
	for _, r := range rules {
		ok := false
		for _, p := range prefixes {
			if strings.HasPrefix(r, p) {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("rule %q does not use a recognized dotted category prefix", r)
		}
	}
}
