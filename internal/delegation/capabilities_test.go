package delegation

import (
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
)

func TestCanDelegate_PrimaryAgent(t *testing.T) {
	delegator := &orchestrator.AgentConfig{
		ID:       "lumi",
		Role:     "lead",
		Provider: "ollama",
		Model:    "glm-5.1:cloud",
		Primary:  true,
	}
	target := &orchestrator.AgentConfig{
		ID:    "mango",
		Role:  "coder",
		Model: "deepseek-v4-pro:cloud",
	}

	// Primary agent can always delegate
	err := CanDelegate(delegator, target, "code_implementation")
	if err != nil {
		t.Errorf("primary agent should be able to delegate: %v", err)
	}
}

func TestCanDelegate_WithDelegateCapability(t *testing.T) {
	delegator := &orchestrator.AgentConfig{
		ID:           "lumi",
		Role:         "lead",
		Provider:     "ollama",
		Model:        "glm-5.1:cloud",
		Capabilities: []string{"plan", "delegate", "review"},
	}
	target := &orchestrator.AgentConfig{
		ID:           "mango",
		Role:         "coder",
		Provider:     "ollama",
		Model:        "deepseek-v4-pro:cloud",
		Capabilities: []string{"code", "test", "report"},
	}

	err := CanDelegate(delegator, target, "code_implementation")
	if err != nil {
		t.Errorf("agent with delegate capability should be able to delegate: %v", err)
	}
}

func TestCanDelegate_WithoutDelegateCapability(t *testing.T) {
	delegator := &orchestrator.AgentConfig{
		ID:           "researcher",
		Role:         "researcher",
		Provider:     "ollama",
		Model:        "qwen3-vl:235b-cloud",
		Capabilities: []string{"search", "summarize"},
	}
	target := &orchestrator.AgentConfig{
		ID:           "mango",
		Role:         "coder",
		Provider:     "ollama",
		Model:        "deepseek-v4-pro:cloud",
		Capabilities: []string{"code", "test", "report"},
	}

	err := CanDelegate(delegator, target, "code_implementation")
	if err == nil {
		t.Error("agent without delegate capability should not be able to delegate")
	}
}

func TestCanDelegate_TargetCannotHandleTaskType(t *testing.T) {
	delegator := &orchestrator.AgentConfig{
		ID:           "lumi",
		Role:         "lead",
		Provider:     "ollama",
		Model:        "glm-5.1:cloud",
		Capabilities: []string{"plan", "delegate", "review"},
	}
	target := &orchestrator.AgentConfig{
		ID:           "researcher",
		Role:         "researcher",
		Provider:     "ollama",
		Model:        "qwen3-vl:235b-cloud",
		Capabilities: []string{"search", "summarize"},
	}

	err := CanDelegate(delegator, target, "code_implementation")
	if err == nil {
		t.Error("researcher should not be able to handle code tasks")
	}
}

func TestCanDelegate_GeneralTaskType(t *testing.T) {
	delegator := &orchestrator.AgentConfig{
		ID:           "lumi",
		Role:         "lead",
		Provider:     "ollama",
		Model:        "glm-5.1:cloud",
		Capabilities: []string{"plan", "delegate"},
	}
	target := &orchestrator.AgentConfig{
		ID:           "mango",
		Role:         "coder",
		Provider:     "ollama",
		Model:        "deepseek-v4-pro:cloud",
		Capabilities: []string{"code"},
	}

	// "general" tasks can be handled by any agent with capabilities
	err := CanDelegate(delegator, target, "general")
	if err != nil {
		t.Errorf("general tasks should be delegatable to agent with capabilities: %v", err)
	}
}

func TestCanDelegate_RoleDefaults(t *testing.T) {
	// Agent with no explicit capabilities should use role defaults
	delegator := &orchestrator.AgentConfig{
		ID:       "lumi",
		Role:     "lead",
		Provider: "ollama",
		Model:    "glm-5.1:cloud",
		// No explicit capabilities — should get role defaults
	}

	// "lead" role defaults: plan, delegate, review
	if !hasCapability(delegator, CapDelegate) {
		t.Error("lead role should have delegate capability by default")
	}
	if !hasCapability(delegator, CapPlan) {
		t.Error("lead role should have plan capability by default")
	}
}

func TestCanDelegate_RoleDefaults_Coder(t *testing.T) {
	target := &orchestrator.AgentConfig{
		ID:       "mango",
		Role:     "coder",
		Provider: "ollama",
		Model:    "deepseek-v4-pro:cloud",
		// No explicit capabilities — should get role defaults
	}

	// "coder" role defaults: code, test, report
	if !hasCapability(target, CapCode) {
		t.Error("coder role should have code capability by default")
	}
	if !hasCapability(target, CapTest) {
		t.Error("coder role should have test capability by default")
	}
}

func TestTaskTypeToCapability(t *testing.T) {
	tests := []struct {
		taskType string
		want     string
	}{
		{"code", CapCode},
		{"code_implementation", CapCode},
		{"code_fix", CapCode},
		{"code_refactor", CapCode},
		{"test", CapTest},
		{"test_write", CapTest},
		{"review", CapReview},
		{"code_review", CapReview},
		{"plan", CapPlan},
		{"approve", CapApprove},
		{"research", CapSearch},
		{"report", CapReport},
		{"general", ""},
		{"unknown_type", ""},
	}

	for _, tt := range tests {
		got := taskTypeToCapability(tt.taskType)
		if got != tt.want {
			t.Errorf("taskTypeToCapability(%q) = %q, want %q", tt.taskType, got, tt.want)
		}
	}
}

func TestCanDelegate_UnknownTaskType(t *testing.T) {
	delegator := &orchestrator.AgentConfig{
		ID:           "lumi",
		Role:         "lead",
		Provider:     "ollama",
		Model:        "glm-5.1:cloud",
		Capabilities: []string{"plan", "delegate"},
	}
	target := &orchestrator.AgentConfig{
		ID:           "mango",
		Role:         "coder",
		Provider:     "ollama",
		Model:        "deepseek-v4-pro:cloud",
		Capabilities: []string{"code"},
	}

	// Unknown task types are DENIED by default for security
	err := CanDelegate(delegator, target, "unknown_task_type")
	if err == nil {
		t.Error("unknown task types should be denied by default")
	}
}
func TestValidateCapabilities_Known(t *testing.T) {
	agent := &orchestrator.AgentConfig{
		ID:           "lumi",
		Role:         "lead",
		Capabilities: []string{"plan", "delegate", "review"},
	}
	if err := ValidateCapabilities(agent); err != nil {
		t.Errorf("expected valid capabilities to pass: %v", err)
	}
}

func TestValidateCapabilities_Unknown(t *testing.T) {
	agent := &orchestrator.AgentConfig{
		ID:           "lumi",
		Role:         "lead",
		Capabilities: []string{"plan", "delegate", "unknown_cap"},
	}
	if err := ValidateCapabilities(agent); err == nil {
		t.Error("expected unknown capability to be rejected")
	}
}

func TestValidateRole_Known(t *testing.T) {
	for _, role := range []string{"orchestrator", "lead", "developer", "coder", "researcher"} {
		agent := &orchestrator.AgentConfig{ID: "test", Role: role}
		if err := ValidateRole(agent); err != nil {
			t.Errorf("expected role %q to be valid: %v", role, err)
		}
	}
}

func TestValidateRole_Unknown(t *testing.T) {
	agent := &orchestrator.AgentConfig{ID: "test", Role: "hacker"}
	if err := ValidateRole(agent); err == nil {
		t.Error("expected unknown role to be rejected")
	}
}

func TestValidateRole_Empty(t *testing.T) {
	agent := &orchestrator.AgentConfig{ID: "test", Role: ""}
	if err := ValidateRole(agent); err == nil {
		t.Error("expected empty role to be rejected")
	}
}

func TestCanDelegate_NilDelegator(t *testing.T) {
	target := &orchestrator.AgentConfig{ID: "mango", Role: "coder"}
	err := CanDelegate(nil, target, "code")
	if err == nil {
		t.Error("expected error for nil delegator")
	}
}

func TestCanDelegate_NilTarget(t *testing.T) {
	delegator := &orchestrator.AgentConfig{ID: "lumi", Role: "lead", Primary: true}
	err := CanDelegate(delegator, nil, "code")
	if err == nil {
		t.Error("expected error for nil target")
	}
}

func TestCanDelegate_PrimaryBypassesDelegatorOnly(t *testing.T) {
	// Primary can delegate, but target must still have the capability
	primary := &orchestrator.AgentConfig{
		ID:      "lumi",
		Role:    "lead",
		Primary: true,
	}
	researcher := &orchestrator.AgentConfig{
		ID:           "researcher",
		Role:         "researcher",
		Capabilities: []string{"search", "summarize"},
	}

	// Primary CAN delegate code task, but researcher CAN'T handle it
	err := CanDelegate(primary, researcher, "code_implementation")
	if err == nil {
		t.Error("primary should not bypass target capability check")
	}

	// Primary CAN delegate research task to researcher
	err = CanDelegate(primary, researcher, "research")
	if err != nil {
		t.Errorf("primary should be able to delegate research to researcher: %v", err)
	}
}
