package multiagent

import (
	"testing"
)

// ---------------------------------------------------------------------
// Compile success: shape assertions
// ---------------------------------------------------------------------

// TestCompileComprehensiveValidDefSucceeds compiles PR2's own "zero errors"
// baseline fixture (comprehensiveValidDef, validator_test.go) and asserts
// the resulting CompiledGraph's shape matches what that fixture declares:
// 6 nodes (4 role, 2 terminal), 6 edges, entry node "planner", both
// terminal nodes present, and 4 loop-bearing edges (the two genuine back
// edges plus the two SCC-forced "forward" edges — see
// TestCycleValidationSCCGranularityAffectsPR1ReferenceFixture in
// validator_test.go for why comprehensiveValidDef annotates all four).
func TestCompileComprehensiveValidDefSucceeds(t *testing.T) {
	def := comprehensiveValidDef()
	graph, diags, err := Compile(def, nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile returned error: %v (diags: %v)", err, diags)
	}
	if diags.HasErrors() {
		t.Fatalf("expected zero error diagnostics, got %v", diags)
	}
	if graph == nil {
		t.Fatal("expected a non-nil CompiledGraph")
	}

	nodes := graph.Nodes()
	if len(nodes) != 6 {
		t.Errorf("len(Nodes()) = %d, want 6", len(nodes))
	}
	edges := graph.Edges()
	if len(edges) != 6 {
		t.Errorf("len(Edges()) = %d, want 6", len(edges))
	}

	if got, want := graph.EntryNodeID(), nodeID(RolePlanner); got != want {
		t.Errorf("EntryNodeID() = %q, want %q", got, want)
	}

	terminals := graph.TerminalNodeIDs()
	if len(terminals) != 2 {
		t.Fatalf("len(TerminalNodeIDs()) = %d, want 2: %v", len(terminals), terminals)
	}
	wantTerminals := map[string]bool{
		terminalNodeID(TerminalConditionCompleted): true,
		terminalNodeID(TerminalConditionFailed):    true,
	}
	for _, id := range terminals {
		if !wantTerminals[id] {
			t.Errorf("unexpected terminal node id %q", id)
		}
	}

	// 4 of the 6 edges carry a loop: policy (devToTester, testerToDev,
	// testerToReviewer, reviewerToDev); plan-to-dev and
	// reviewer-to-completed do not.
	wantLoopEdges := map[string]bool{
		edgeID(RoleDeveloper, OutcomeImplementationReady): true,
		edgeID(RoleTester, OutcomeTestsFailed):            true,
		edgeID(RoleTester, OutcomeTestsPassed):            true,
		edgeID(RoleReviewer, OutcomeChangesRequested):     true,
	}
	loopCount := 0
	for _, e := range edges {
		loop, ok := graph.LoopFor(ResolvedTransition{From: e.From, Outcome: e.Outcome, To: e.To, Terminal: e.Terminal})
		if !ok {
			continue
		}
		loopCount++
		if !wantLoopEdges[loop.EdgeID] {
			t.Errorf("unexpected loop on edge %q", loop.EdgeID)
		}
		if loop.EdgeID != e.ID {
			t.Errorf("loop.EdgeID = %q, want %q", loop.EdgeID, e.ID)
		}
		if string(loop.Kind) != loop.EdgeID {
			t.Errorf("loop.Kind = %q, want it to equal the owning edge's stable ID %q", loop.Kind, loop.EdgeID)
		}
	}
	if loopCount != 4 {
		t.Errorf("loopCount = %d, want 4", loopCount)
	}
}

// TestCompileNodeIDsMatchGraphProjection asserts Compile calls the actual
// existing graph_projection.go nodeID/terminalNodeID/edgeID functions
// (rather than reimplementing the ID scheme): every compiled node/edge ID
// must be byte-identical to what those functions produce directly.
func TestCompileNodeIDsMatchGraphProjection(t *testing.T) {
	graph, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	for _, n := range graph.Nodes() {
		var want string
		if n.Kind == "terminal" {
			want = terminalNodeID(n.TerminalCondition)
		} else {
			want = nodeID(n.Role)
		}
		if n.ID != want {
			t.Errorf("node %+v: ID = %q, want %q (from graph_projection.go)", n, n.ID, want)
		}
	}
	for _, e := range graph.Edges() {
		want := edgeID(e.From, e.Outcome)
		if e.ID != want {
			t.Errorf("edge %+v: ID = %q, want %q (from graph_projection.go)", e, e.ID, want)
		}
	}
}

// ---------------------------------------------------------------------
// Compile failure
// ---------------------------------------------------------------------

// TestCompileInvalidDefReturnsCompilationFailedError reuses one of PR2's own
// rule-triggering fixtures (an empty EntryNode, which validateStructural
// flags as structural.entry-node-missing) and asserts Compile rejects it
// with a nil graph, an errors-containing Diagnostics, and a
// *CompilationFailedError.
func TestCompileInvalidDefReturnsCompilationFailedError(t *testing.T) {
	def := baseDef()
	def.Spec.EntryNode = ""

	graph, diags, err := Compile(def, nil, CompileOptions{})
	if graph != nil {
		t.Errorf("expected nil graph on compilation failure, got %+v", graph)
	}
	if !diags.HasErrors() {
		t.Fatalf("expected error diagnostics, got %v", diags)
	}
	if !hasDiagnostic(diags, "structural.entry-node-missing", "", "") {
		t.Errorf("expected structural.entry-node-missing among diagnostics, got %v", diags)
	}
	var cfErr *CompilationFailedError
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
	cfErr, ok := err.(*CompilationFailedError)
	if !ok {
		t.Fatalf("expected *CompilationFailedError, got %T: %v", err, err)
	}
	if !cfErr.Diagnostics.HasErrors() {
		t.Errorf("CompilationFailedError.Diagnostics should carry the error diagnostics")
	}
	var ge GraphError = cfErr
	if ge.Code() != "compilation_failed" {
		t.Errorf("Code() = %q, want %q", ge.Code(), "compilation_failed")
	}
}

// TestCompilePR1ReferenceFixtureFailsUnderSCCWideRule documents, at the
// compiler layer, the same known PR2 finding
// TestCycleValidationSCCGranularityAffectsPR1ReferenceFixture documents at
// the validator layer: PR1's own testdata/schema/valid/software-delivery.yaml
// does NOT compile as-is, because the SCC-wide cycle.unbounded-loop-edge
// rule requires a loop: policy on dev-to-tester and tester-to-reviewer too
// (both forward edges inside the same 3-node SCC), which that fixture omits.
// This is intentional, not a bug — see compiler.go's package doc and the
// PR2/PR3 reports.
func TestCompilePR1ReferenceFixtureFailsUnderSCCWideRule(t *testing.T) {
	def, idx, loadDiags, err := LoadDefinition("testdata/schema/valid/software-delivery.yaml")
	if err != nil {
		t.Fatalf("LoadDefinition: %v", err)
	}
	if loadDiags.HasErrors() {
		t.Fatalf("unexpected load diagnostics: %v", loadDiags)
	}

	graph, diags, err := Compile(def, idx, CompileOptions{})
	if graph != nil {
		t.Errorf("expected nil graph, got %+v", graph)
	}
	if !diags.HasErrors() {
		t.Fatalf("expected error diagnostics, got %v", diags)
	}
	if _, ok := err.(*CompilationFailedError); !ok {
		t.Fatalf("expected *CompilationFailedError, got %T: %v", err, err)
	}
	if !hasDiagnostic(diags, "cycle.unbounded-loop-edge", "developer", "dev-to-tester") {
		t.Errorf("expected cycle.unbounded-loop-edge for dev-to-tester, got %v", diags)
	}
}

// ---------------------------------------------------------------------
// Fingerprint
// ---------------------------------------------------------------------

// TestFingerprintDeterministic compiles the same definition 100 times and
// asserts every resulting fingerprint is byte-identical — the concrete
// guard against a map-iteration-order non-determinism bug in
// computeFingerprint.
func TestFingerprintDeterministic(t *testing.T) {
	def := comprehensiveValidDef()

	graph, _, err := Compile(def, nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := graph.Fingerprint()
	if want == "" {
		t.Fatal("expected a non-empty fingerprint")
	}

	for i := 0; i < 100; i++ {
		g, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
		if err != nil {
			t.Fatalf("iteration %d: Compile: %v", i, err)
		}
		if got := g.Fingerprint(); got != want {
			t.Fatalf("iteration %d: fingerprint = %q, want %q (non-deterministic fingerprint)", i, got, want)
		}
	}
}

// TestFingerprintSensitiveToSingleFieldChange compiles two definitions that
// differ in exactly one field (one node's MaxVisits) and asserts the
// fingerprints differ.
func TestFingerprintSensitiveToSingleFieldChange(t *testing.T) {
	base := comprehensiveValidDef()
	baseGraph, _, err := Compile(base, nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile(base): %v", err)
	}

	changed := comprehensiveValidDef()
	for i := range changed.Spec.Nodes {
		if changed.Spec.Nodes[i].ID == "developer" {
			changed.Spec.Nodes[i].MaxVisits = schemaLimit(7)
		}
	}
	changedGraph, _, err := Compile(changed, nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile(changed): %v", err)
	}

	if baseGraph.Fingerprint() == changedGraph.Fingerprint() {
		t.Fatalf("expected different fingerprints for definitions differing in one node's MaxVisits, both were %q", baseGraph.Fingerprint())
	}
}

// ---------------------------------------------------------------------
// Field-mapping / accessor behavior
// ---------------------------------------------------------------------

func TestCompileRoleConfigFieldMapping(t *testing.T) {
	def := comprehensiveValidDef()
	for i := range def.Spec.Nodes {
		if def.Spec.Nodes[i].ID == "developer" {
			def.Spec.Nodes[i].AgentProfile = "developer-profile"
			def.Spec.Nodes[i].AllowedTools = []string{"bash", "edit"}
			def.Spec.Nodes[i].MaxVisits = schemaLimit(5)
			def.Spec.Nodes[i].LocalIterations = schemaLimit(10)
			def.Spec.Nodes[i].TokenBudget = schemaLimit(50000)
			def.Spec.Nodes[i].Approval = &SchemaApprovalPolicy{Required: true, Approvers: []string{"reviewer"}}
		}
	}

	graph, diags, err := Compile(def, nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v (diags: %v)", err, diags)
	}

	rc, ok := graph.RoleConfig(RoleDeveloper)
	if !ok {
		t.Fatal("expected a RoleConfig for RoleDeveloper")
	}
	if rc.AgentRef != "developer-profile" {
		t.Errorf("AgentRef = %q, want %q", rc.AgentRef, "developer-profile")
	}
	if len(rc.AllowedTools) != 2 || rc.AllowedTools[0] != "bash" {
		t.Errorf("AllowedTools = %v, want [bash edit]", rc.AllowedTools)
	}
	if rc.MaxLocalIterations != Limit(10) {
		t.Errorf("MaxLocalIterations = %v, want 10", rc.MaxLocalIterations)
	}
	if rc.TokenBudget != Limit(50000) {
		t.Errorf("TokenBudget = %v, want 50000", rc.TokenBudget)
	}
	if !rc.Approval.Required || len(rc.Approval.Approvers) != 1 || rc.Approval.Approvers[0] != RoleReviewer {
		t.Errorf("Approval = %+v, want required with approver reviewer", rc.Approval)
	}

	// node-level MaxVisits has no home on RoleConfig; it must instead land
	// on the compiled graph's budgets.MaxVisitsPerRole map.
	budgets := graph.Budgets()
	if got, want := budgets.MaxVisitsPerRole[RoleDeveloper], Limit(5); got != want {
		t.Errorf("Budgets().MaxVisitsPerRole[developer] = %v, want %v", got, want)
	}
}

func TestCompileUnknownRoleConfigNotFound(t *testing.T) {
	graph, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, ok := graph.RoleConfig(Role("does-not-exist")); ok {
		t.Error("expected RoleConfig to report not-found for an unknown role")
	}
}

func TestCompileResolveMatchesEdges(t *testing.T) {
	graph, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	transition, err := graph.Resolve(RolePlanner, OutcomePlanReady)
	if err != nil {
		t.Fatalf("Resolve(planner, plan_ready): %v", err)
	}
	if transition.To != RoleDeveloper {
		t.Errorf("Resolve(planner, plan_ready).To = %q, want %q", transition.To, RoleDeveloper)
	}

	if _, err := graph.Resolve(RolePlanner, OutcomeTestsPassed); err == nil {
		t.Error("expected an error resolving an outcome planner never declares")
	} else if _, ok := err.(*InvalidTransitionError); !ok {
		t.Errorf("expected *InvalidTransitionError, got %T", err)
	}
}

func TestCompileHasRoleHasOutcomeOutcomeValidFor(t *testing.T) {
	graph, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !graph.HasRole(RoleDeveloper) {
		t.Error("expected HasRole(developer) == true")
	}
	if graph.HasRole(Role("nope")) {
		t.Error("expected HasRole(nope) == false")
	}
	if !graph.HasOutcome(OutcomeTestsPassed) {
		t.Error("expected HasOutcome(tests_passed) == true")
	}
	if graph.HasOutcome(TransitionOutcome("nope")) {
		t.Error("expected HasOutcome(nope) == false")
	}
	if !graph.OutcomeValidFor(RoleTester, OutcomeTestsPassed) {
		t.Error("expected OutcomeValidFor(tester, tests_passed) == true")
	}
	if graph.OutcomeValidFor(RolePlanner, OutcomeTestsPassed) {
		t.Error("expected OutcomeValidFor(planner, tests_passed) == false")
	}
}

func TestBuildCompiledGraphView(t *testing.T) {
	graph, _, err := Compile(comprehensiveValidDef(), nil, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	view := BuildCompiledGraphView(graph)
	if view.WorkflowID != graph.WorkflowID() {
		t.Errorf("view.WorkflowID = %q, want %q", view.WorkflowID, graph.WorkflowID())
	}
	if view.Fingerprint != graph.Fingerprint() {
		t.Errorf("view.Fingerprint = %q, want %q", view.Fingerprint, graph.Fingerprint())
	}
	if len(view.Nodes) != len(graph.Nodes()) {
		t.Errorf("len(view.Nodes) = %d, want %d", len(view.Nodes), len(graph.Nodes()))
	}
	if len(view.Loops) != 4 {
		t.Errorf("len(view.Loops) = %d, want 4", len(view.Loops))
	}
}
