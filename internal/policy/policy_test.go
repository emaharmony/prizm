package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ─── Decision Model Tests ────────────────────────────────────────────────────────

func TestDecisionConstants(t *testing.T) {
	if DecisionAllowed != "allowed" {
		t.Errorf("DecisionAllowed = %q, want %q", DecisionAllowed, "allowed")
	}
	if DecisionDenied != "denied" {
		t.Errorf("DecisionDenied = %q, want %q", DecisionDenied, "denied")
	}
	if DecisionRequiresApproval != "requires_approval" {
		t.Errorf("DecisionRequiresApproval = %q, want %q", DecisionRequiresApproval, "requires_approval")
	}
}

func TestSeverityConstants(t *testing.T) {
	if SeverityInfo != "info" {
		t.Errorf("SeverityInfo = %q, want %q", SeverityInfo, "info")
	}
	if SeverityWarning != "warning" {
		t.Errorf("SeverityWarning = %q, want %q", SeverityWarning, "warning")
	}
	if SeverityCritical != "critical" {
		t.Errorf("SeverityCritical = %q, want %q", SeverityCritical, "critical")
	}
}

func TestPolicyDecisionJSONRoundTrip(t *testing.T) {
	d := PolicyDecision{
		Decision:         DecisionAllowed,
		Reason:           "Test reason",
		RuleID:           "allow_test",
		Severity:         SeverityInfo,
		RequiresApproval: false,
		Metadata:         map[string]any{"key": "value"},
		EvaluationID:     "eval_123",
		MatchedRuleIndex: 0,
	}

	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal PolicyDecision: %v", err)
	}

	var got PolicyDecision
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal PolicyDecision: %v", err)
	}

	if got.Decision != d.Decision {
		t.Errorf("Decision = %q, want %q", got.Decision, d.Decision)
	}
	if got.RuleID != d.RuleID {
		t.Errorf("RuleID = %q, want %q", got.RuleID, d.RuleID)
	}
	if got.EvaluationID != d.EvaluationID {
		t.Errorf("EvaluationID = %q, want %q", got.EvaluationID, d.EvaluationID)
	}
}

// ─── Rule Loading Tests ───────────────────────────────────────────────────────────

func TestLoadFromYAMLValid(t *testing.T) {
	yaml := `policies:
  - id: allow_echo
    description: Allow echo tool
    match:
      action: tool.execute
      resource.name: echo
    decision: allowed
    reason: "Echo tool is safe."
`
	rules, err := LoadFromYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadFromYAML: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(rules))
	}
	if rules[0].ID != "allow_echo" {
		t.Errorf("rule ID = %q, want %q", rules[0].ID, "allow_echo")
	}
	if rules[0].Decision != DecisionAllowed {
		t.Errorf("rule Decision = %q, want %q", rules[0].Decision, DecisionAllowed)
	}
}

func TestLoadFromYAMLInvalidYAML(t *testing.T) {
	_, err := LoadFromYAML([]byte("::invalid yaml:::["))
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestLoadFromYAMLMissingID(t *testing.T) {
	yaml := `policies:
  - description: No ID rule
    match:
      action: tool.execute
    decision: allowed
    reason: "Missing ID"
`
	_, err := LoadFromYAML([]byte(yaml))
	if err == nil {
		t.Error("expected error for missing rule ID, got nil")
	}
}

func TestLoadFromYAMLMissingDecision(t *testing.T) {
	yaml := `policies:
  - id: no_decision
    description: No decision
    match:
      action: tool.execute
    reason: "Missing decision"
`
	_, err := LoadFromYAML([]byte(yaml))
	if err == nil {
		t.Error("expected error for missing decision, got nil")
	}
}

func TestLoadFromYAMLMissingReason(t *testing.T) {
	yaml := `policies:
  - id: no_reason
    description: No reason
    match:
      action: tool.execute
    decision: allowed
`
	_, err := LoadFromYAML([]byte(yaml))
	if err == nil {
		t.Error("expected error for missing reason, got nil")
	}
}

func TestLoadFromDirValid(t *testing.T) {
	dir := t.TempDir()
	yaml := `policies:
  - id: allow_echo
    description: Allow echo tool
    match:
      action: tool.execute
      resource.name: echo
    decision: allowed
    reason: "Echo tool is safe."
`
	if err := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	count, err := reg.LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
	if len(reg.Rules()) != 1 {
		t.Errorf("rules = %d, want 1", len(reg.Rules()))
	}
}

func TestLoadFromDirDuplicateID(t *testing.T) {
	dir := t.TempDir()
	yaml1 := `policies:
  - id: allow_echo
    description: First
    match:
      action: tool.execute
      resource.name: echo
    decision: allowed
    reason: "First"
`
	yaml2 := `policies:
  - id: allow_echo
    description: Second
    match:
      action: tool.execute
      resource.name: echo
    decision: denied
    reason: "Second"
`
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(yaml1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.yaml"), []byte(yaml2), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	_, err := reg.LoadFromDir(dir)
	if err == nil {
		t.Error("expected error for duplicate rule ID, got nil")
	}
}

func TestLoadFromDirSkipsNonYAML(t *testing.T) {
	dir := t.TempDir()
	yaml := `policies:
  - id: allow_echo
    description: Allow echo tool
    match:
      action: tool.execute
      resource.name: echo
    decision: allowed
    reason: "Echo tool is safe."
`
	if err := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a policy"), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	count, err := reg.LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

// ─── Registry Tests ───────────────────────────────────────────────────────────────

func TestRegistryRegisterAndRules(t *testing.T) {
	reg := NewRegistry()
	rule := PolicyRule{
		ID:          "test_rule",
		Description: "Test",
		Match:       MatchSpec{Action: "tool.execute"},
		Decision:    DecisionAllowed,
		Reason:      "Test rule",
	}

	if err := reg.Register(rule); err != nil {
		t.Fatalf("Register: %v", err)
	}

	rules := reg.Rules()
	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	if rules[0].ID != "test_rule" {
		t.Errorf("rule ID = %q, want %q", rules[0].ID, "test_rule")
	}
}

func TestRegistryDuplicateID(t *testing.T) {
	reg := NewRegistry()
	rule := PolicyRule{
		ID:       "dup_rule",
		Match:    MatchSpec{Action: "test"},
		Decision: DecisionAllowed,
		Reason:   "First",
	}

	if err := reg.Register(rule); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	if err := reg.Register(rule); err == nil {
		t.Error("expected error for duplicate ID, got nil")
	}
}

func TestRegistryFindByID(t *testing.T) {
	reg := NewRegistry()
	rule := PolicyRule{
		ID:       "find_me",
		Match:    MatchSpec{Action: "test"},
		Decision: DecisionDenied,
		Reason:   "Found",
	}
	reg.Register(rule)

	found, err := reg.FindByID("find_me")
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.ID != "find_me" {
		t.Errorf("found ID = %q, want %q", found.ID, "find_me")
	}

	_, err = reg.FindByID("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent ID, got nil")
	}
}

// ─── MatchSpec Tests ──────────────────────────────────────────────────────────────

func TestMatchSpecExactAction(t *testing.T) {
	spec := MatchSpec{Action: "tool.execute"}
	req := PolicyRequest{Action: "tool.execute"}
	if !spec.Matches(req) {
		t.Error("expected match for exact action")
	}
}

func TestMatchSpecNonMatchingAction(t *testing.T) {
	spec := MatchSpec{Action: "tool.execute"}
	req := PolicyRequest{Action: "mutation.apply"}
	if spec.Matches(req) {
		t.Error("expected no match for different action")
	}
}

func TestMatchSpecWildcardEmptyFields(t *testing.T) {
	spec := MatchSpec{} // empty = wildcard
	req := PolicyRequest{Action: "anything", Resource: Resource{Name: "whatever"}}
	if !spec.Matches(req) {
		t.Error("empty spec should match anything")
	}
}

func TestMatchSpecResourceName(t *testing.T) {
	spec := MatchSpec{Action: "tool.execute", ResourceName: "read_file"}
	req := PolicyRequest{Action: "tool.execute", Resource: Resource{Name: "read_file"}}
	if !spec.Matches(req) {
		t.Error("expected match for resource name")
	}

	req2 := PolicyRequest{Action: "tool.execute", Resource: Resource{Name: "write_file"}}
	if spec2 := spec; spec2.Matches(req2) {
		t.Error("expected no match for different resource name")
	}
}

func TestMatchSpecContextMode(t *testing.T) {
	spec := MatchSpec{Action: "dispatch.run", ContextMode: "live"}
	req := PolicyRequest{Action: "dispatch.run", Context: Context{Mode: "live"}}
	if !spec.Matches(req) {
		t.Error("expected match for context mode")
	}

	req2 := PolicyRequest{Action: "dispatch.run", Context: Context{Mode: "paper"}}
	if spec.Matches(req2) {
		t.Error("expected no match for different context mode")
	}
}

func TestMatchSpecMultipleFields(t *testing.T) {
	spec := MatchSpec{
		Action:       "tool.execute",
		ResourceName: "run_command",
	}
	req := PolicyRequest{
		Action:   "tool.execute",
		Resource: Resource{Name: "run_command"},
	}
	if !spec.Matches(req) {
		t.Error("expected match for multiple fields")
	}

	req2 := PolicyRequest{
		Action:   "tool.execute",
		Resource: Resource{Name: "read_file"},
	}
	if spec.Matches(req2) {
		t.Error("expected no match when resource name differs")
	}
}

// ─── Evaluator Tests ──────────────────────────────────────────────────────────────

func TestEvaluatorAllowRule(t *testing.T) {
	reg := NewRegistry()
	reg.Register(PolicyRule{
		ID:       "allow_echo",
		Match:    MatchSpec{Action: "tool.execute", ResourceName: "echo"},
		Decision: DecisionAllowed,
		Reason:   "Echo tool is safe.",
	})

	eval := NewEvaluator(reg)
	req := PolicyRequest{
		Action:   "tool.execute",
		Resource: Resource{Name: "echo"},
	}

	decision := eval.Evaluate(req)
	if decision.Decision != DecisionAllowed {
		t.Errorf("decision = %q, want %q", decision.Decision, DecisionAllowed)
	}
	if decision.RuleID != "allow_echo" {
		t.Errorf("rule_id = %q, want %q", decision.RuleID, "allow_echo")
	}
}

func TestEvaluatorDenyRule(t *testing.T) {
	reg := NewRegistry()
	reg.Register(PolicyRule{
		ID:       "deny_shell",
		Match:    MatchSpec{Action: "tool.execute", ResourceName: "run_command"},
		Decision: DecisionDenied,
		Reason:   "Shell execution is not supported.",
		Severity: SeverityCritical,
	})

	eval := NewEvaluator(reg)
	req := PolicyRequest{
		Action:   "tool.execute",
		Resource: Resource{Name: "run_command"},
	}

	decision := eval.Evaluate(req)
	if decision.Decision != DecisionDenied {
		t.Errorf("decision = %q, want %q", decision.Decision, DecisionDenied)
	}
	if decision.Severity != SeverityCritical {
		t.Errorf("severity = %q, want %q", decision.Severity, SeverityCritical)
	}
}

func TestEvaluatorRequiresApproval(t *testing.T) {
	reg := NewRegistry()
	reg.Register(PolicyRule{
		ID:       "require_approval_file_write",
		Match:    MatchSpec{Action: "mutation.apply", ResourceType: "file"},
		Decision: DecisionRequiresApproval,
		Reason:   "File mutations require operator approval.",
	})

	eval := NewEvaluator(reg)
	req := PolicyRequest{
		Action:   "mutation.apply",
		Resource: Resource{Type: "file"},
	}

	decision := eval.Evaluate(req)
	if decision.Decision != DecisionRequiresApproval {
		t.Errorf("decision = %q, want %q", decision.Decision, DecisionRequiresApproval)
	}
	if !decision.RequiresApproval {
		t.Error("RequiresApproval should be true")
	}
}

func TestEvaluatorDefaultDeny(t *testing.T) {
	reg := NewRegistry()
	// No rules registered

	eval := NewEvaluator(reg)
	req := PolicyRequest{
		Action:   "tool.execute",
		Resource: Resource{Name: "unknown_tool"},
	}

	decision := eval.Evaluate(req)
	if decision.Decision != DecisionDenied {
		t.Errorf("default decision = %q, want %q", decision.Decision, DecisionDenied)
	}
	if decision.RuleID != "default" {
		t.Errorf("rule_id = %q, want %q", decision.RuleID, "default")
	}
}

func TestEvaluatorDefaultAllow(t *testing.T) {
	reg := NewRegistry()
	eval := NewEvaluator(reg)
	eval.SetDefault(DecisionAllowed, "Default allow for testing.")

	req := PolicyRequest{
		Action:   "tool.execute",
		Resource: Resource{Name: "unknown_tool"},
	}

	decision := eval.Evaluate(req)
	if decision.Decision != DecisionAllowed {
		t.Errorf("default decision = %q, want %q", decision.Decision, DecisionAllowed)
	}
}

func TestEvaluatorFirstMatchWins(t *testing.T) {
	reg := NewRegistry()
	reg.Register(PolicyRule{
		ID:       "allow_all_execute",
		Match:    MatchSpec{Action: "tool.execute"},
		Decision: DecisionAllowed,
		Reason:   "All tool execution allowed.",
	})
	reg.Register(PolicyRule{
		ID:       "deny_run_command",
		Match:    MatchSpec{Action: "tool.execute", ResourceName: "run_command"},
		Decision: DecisionDenied,
		Reason:   "Shell execution denied.",
	})

	eval := NewEvaluator(reg)

	// "allow_all_execute" is registered first, so run_command matches it first
	req := PolicyRequest{
		Action:   "tool.execute",
		Resource: Resource{Name: "run_command"},
	}

	decision := eval.Evaluate(req)
	if decision.RuleID != "allow_all_execute" {
		t.Errorf("rule_id = %q, want %q (first-match)", decision.RuleID, "allow_all_execute")
	}
	if decision.Decision != DecisionAllowed {
		t.Errorf("decision = %q, want %q (first-match)", decision.Decision, DecisionAllowed)
	}
}

func TestEvaluatorSeverityDefaultsToInfo(t *testing.T) {
	reg := NewRegistry()
	reg.Register(PolicyRule{
		ID:       "test_rule",
		Match:    MatchSpec{Action: "test"},
		Decision: DecisionAllowed,
		Reason:   "Test.",
		// No Severity specified
	})

	eval := NewEvaluator(reg)
	req := PolicyRequest{Action: "test"}
	decision := eval.Evaluate(req)

	if decision.Severity != SeverityInfo {
		t.Errorf("default severity = %q, want %q", decision.Severity, SeverityInfo)
	}
}

func TestEvaluatorReasonPreserved(t *testing.T) {
	reg := NewRegistry()
	reg.Register(PolicyRule{
		ID:       "test_rule",
		Match:    MatchSpec{Action: "test"},
		Decision: DecisionDenied,
		Reason:   "Specific denial reason here.",
	})

	eval := NewEvaluator(reg)
	req := PolicyRequest{Action: "test"}
	decision := eval.Evaluate(req)

	if decision.Reason != "Specific denial reason here." {
		t.Errorf("reason = %q, want specific reason preserved", decision.Reason)
	}
}

// ─── Event Emission Tests ─────────────────────────────────────────────────────────

type mockEmitter struct {
	events []struct {
		eventType string
		source    string
		payload   map[string]any
	}
}

func (m *mockEmitter) Emit(eventType, source string, payload map[string]any) {
	m.events = append(m.events, struct {
		eventType string
		source    string
		payload   map[string]any
	}{eventType, source, payload})
}

func TestEvaluatorEmitsRequestedEvent(t *testing.T) {
	reg := NewRegistry()
	emitter := &mockEmitter{}
	eval := NewEvaluator(reg)
	eval.SetEmitter(emitter)

	eval.Evaluate(PolicyRequest{Action: "test"})
	found := false
	for _, e := range emitter.events {
		if e.eventType == "prizm.policy.requested" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected prizm.policy.requested event")
	}
}

func TestEvaluatorEmitsEvaluatedEvent(t *testing.T) {
	reg := NewRegistry()
	reg.Register(PolicyRule{
		ID:       "test_rule",
		Match:    MatchSpec{Action: "test"},
		Decision: DecisionAllowed,
		Reason:   "Test.",
	})
	emitter := &mockEmitter{}
	eval := NewEvaluator(reg)
	eval.SetEmitter(emitter)

	eval.Evaluate(PolicyRequest{Action: "test"})

	evalFound := false
	allowFound := false
	for _, e := range emitter.events {
		if e.eventType == "prizm.policy.evaluated" {
			evalFound = true
		}
		if e.eventType == "prizm.policy.allowed" {
			allowFound = true
		}
	}
	if !evalFound {
		t.Error("expected prizm.policy.evaluated event")
	}
	if !allowFound {
		t.Error("expected prizm.policy.allowed event")
	}
}

func TestEvaluatorEmitsDeniedEvent(t *testing.T) {
	reg := NewRegistry()
	emitter := &mockEmitter{}
	eval := NewEvaluator(reg) // default deny
	eval.SetEmitter(emitter)

	eval.Evaluate(PolicyRequest{Action: "unknown"})

	evalFound := false
	deniedFound := false
	for _, e := range emitter.events {
		if e.eventType == "prizm.policy.evaluated" {
			evalFound = true
		}
		if e.eventType == "prizm.policy.denied" {
			deniedFound = true
		}
	}
	if !evalFound {
		t.Error("expected prizm.policy.evaluated event")
	}
	if !deniedFound {
		t.Error("expected prizm.policy.denied event")
	}
}

func TestEvaluatorEmitsApprovalRequiredEvent(t *testing.T) {
	reg := NewRegistry()
	reg.Register(PolicyRule{
		ID:       "require_approval",
		Match:    MatchSpec{Action: "mutation.apply"},
		Decision: DecisionRequiresApproval,
		Reason:   "Needs approval.",
	})
	emitter := &mockEmitter{}
	eval := NewEvaluator(reg)
	eval.SetEmitter(emitter)

	eval.Evaluate(PolicyRequest{Action: "mutation.apply"})

	approvalFound := false
	for _, e := range emitter.events {
		if e.eventType == "prizm.policy.approval_required" {
			approvalFound = true
		}
	}
	if !approvalFound {
		t.Error("expected prizm.policy.approval_required event")
	}
}

func TestEvaluatorCorrelationID(t *testing.T) {
	reg := NewRegistry()
	reg.Register(PolicyRule{
		ID:       "test_rule",
		Match:    MatchSpec{Action: "test"},
		Decision: DecisionAllowed,
		Reason:   "Test.",
	})
	emitter := &mockEmitter{}
	eval := NewEvaluator(reg)
	eval.SetEmitter(emitter)

	decision := eval.Evaluate(PolicyRequest{Action: "test"})

	if decision.EvaluationID == "" {
		t.Error("expected non-empty evaluation_id")
	}
}

// ─── Artifacts Tests ──────────────────────────────────────────────────────────────

func TestWriteAndLoadPolicyArtifact(t *testing.T) {
	dir := t.TempDir()

	decision := PolicyDecision{
		Decision:         DecisionAllowed,
		Reason:           "Test reason",
		RuleID:           "allow_test",
		Severity:         SeverityInfo,
		RequiresApproval: false,
		Metadata:         map[string]any{},
		EvaluationID:     "eval_artifact_test",
	}

	if err := WritePolicyArtifact(dir, decision); err != nil {
		t.Fatalf("WritePolicyArtifact: %v", err)
	}

	loaded, err := LoadPolicyArtifact(dir, "eval_artifact_test")
	if err != nil {
		t.Fatalf("LoadPolicyArtifact: %v", err)
	}

	if loaded.Decision != DecisionAllowed {
		t.Errorf("decision = %q, want %q", loaded.Decision, DecisionAllowed)
	}
	if loaded.RuleID != "allow_test" {
		t.Errorf("rule_id = %q, want %q", loaded.RuleID, "allow_test")
	}
	if loaded.EvaluationID != "eval_artifact_test" {
		t.Errorf("evaluation_id = %q, want %q", loaded.EvaluationID, "eval_artifact_test")
	}
}

func TestLoadPolicyArtifactNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadPolicyArtifact(dir, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent artifact, got nil")
	}
}

func TestLoadFromDirNonexistent(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.LoadFromDir("/nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent dir, got nil")
	}
}

// ─── Explain Tests ────────────────────────────────────────────────────────────────

func TestExplainMatchingRule(t *testing.T) {
	reg := NewRegistry()
	reg.Register(PolicyRule{
		ID:       "allow_echo",
		Match:    MatchSpec{Action: "tool.execute", ResourceName: "echo"},
		Decision: DecisionAllowed,
		Reason:   "Echo tool is safe.",
	})

	eval := NewEvaluator(reg)
	explanation := eval.Explain(PolicyRequest{
		Action:   "tool.execute",
		Resource: Resource{Name: "echo"},
	})

	if explanation == "" {
		t.Error("expected non-empty explanation")
	}
	// Should mention rule ID and decision
	if !contains(explanation, "allow_echo") {
		t.Errorf("explanation should mention rule ID, got: %s", explanation)
	}
	if !contains(explanation, "allowed") {
		t.Errorf("explanation should mention decision, got: %s", explanation)
	}
}

func TestExplainDefault(t *testing.T) {
	reg := NewRegistry()
	eval := NewEvaluator(reg)
	explanation := eval.Explain(PolicyRequest{
		Action:   "tool.execute",
		Resource: Resource{Name: "unknown"},
	})

	if !contains(explanation, "No matching rule") {
		t.Errorf("explanation should mention default, got: %s", explanation)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
