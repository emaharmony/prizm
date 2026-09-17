package stage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/delegation"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/task"
)

// TestDelegationStage_CapabilityCheck verifies that capability checks
// prevent unauthorized delegation.
func TestDelegationStage_CapabilityCheck(t *testing.T) {
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	engine := delegation.NewEngine(store, nil)

	// Agent configs with capabilities
	agentConfigs := map[string]*orchestrator.AgentConfig{
		"lumi": {
			ID:           "lumi",
			Role:         "lead",
			Provider:     "ollama",
			Model:        "glm-5.1:cloud",
			Primary:      true,
			Capabilities: []string{"plan", "delegate", "review"},
		},
		"mango": {
			ID:           "mango",
			Role:         "coder",
			Provider:     "ollama",
			Model:        "deepseek-v4-pro:cloud",
			Capabilities: []string{"code", "test", "report"},
		},
		"researcher": {
			ID:           "researcher",
			Role:         "researcher",
			Provider:     "ollama",
			Model:        "qwen3-vl:235b-cloud",
			Capabilities: []string{"search", "summarize"},
		},
	}

	stage := &DelegationStage{Engine: engine, StripMarkers: true, AgentConfigs: agentConfigs}

	// Test 1: Primary agent can delegate code task to mango
	rc := &RunContext{
		RunID:       "test-cap-1",
		Task:        "delegate code",
		Agent:       "lumi",
		LLMResponse: "I'll delegate this. [DELEGATE: mango | code] Write the function",
	}

	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["delegations"] != 1 {
		t.Errorf("expected 1 delegation, got %v", result.Data["delegations"])
	}
}

// TestDelegationStage_CapabilityCheck_Blocked verifies that a researcher
// without "delegate" capability cannot delegate.
func TestDelegationStage_CapabilityCheck_Blocked(t *testing.T) {
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	engine := delegation.NewEngine(store, nil)

	agentConfigs := map[string]*orchestrator.AgentConfig{
		"researcher": {
			ID:           "researcher",
			Role:         "researcher",
			Provider:     "ollama",
			Model:        "qwen3-vl:235b-cloud",
			Capabilities: []string{"search", "summarize"}, // No "delegate"
		},
		"mango": {
			ID:           "mango",
			Role:         "coder",
			Provider:     "ollama",
			Model:        "deepseek-v4-pro:cloud",
			Capabilities: []string{"code", "test", "report"},
		},
	}

	stage := &DelegationStage{Engine: engine, StripMarkers: true, AgentConfigs: agentConfigs}

	// Researcher tries to delegate — should be blocked
	rc := &RunContext{
		RunID:       "test-cap-blocked",
		Task:        "delegate code",
		Agent:       "researcher",
		LLMResponse: "Let me delegate. [DELEGATE: mango | code] Write the function",
	}

	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("stage should still succeed, just skip the delegation: %s", result.Error)
	}
	// Delegation should be skipped due to capability check
	if result.Data["delegations"] != 0 {
		t.Errorf("expected 0 delegations (blocked by capability), got %v", result.Data["delegations"])
	}
}

// TestDelegationStage_CapabilityCheck_WrongTarget verifies that
// a researcher cannot receive a code task.
func TestDelegationStage_CapabilityCheck_WrongTarget(t *testing.T) {
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	engine := delegation.NewEngine(store, nil)

	agentConfigs := map[string]*orchestrator.AgentConfig{
		"lumi": {
			ID:           "lumi",
			Role:         "lead",
			Provider:     "ollama",
			Model:        "glm-5.1:cloud",
			Primary:      false, // NOT primary — must have delegate capability
			Capabilities: []string{"plan", "delegate", "review"},
		},
		"researcher": {
			ID:           "researcher",
			Role:         "researcher",
			Provider:     "ollama",
			Model:        "qwen3-vl:235b-cloud",
			Capabilities: []string{"search", "summarize"}, // No "code"
		},
	}

	stage := &DelegationStage{Engine: engine, StripMarkers: true, AgentConfigs: agentConfigs}

	// Lumi tries to delegate a code task to researcher — should be blocked
	rc := &RunContext{
		RunID:       "test-cap-wrong",
		Task:        "delegate code",
		Agent:       "lumi",
		LLMResponse: "Let me delegate. [DELEGATE: researcher | code] Write the function",
	}

	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("stage should still succeed: %s", result.Error)
	}
	// Delegation should be skipped because researcher can't handle code tasks
	if result.Data["delegations"] != 0 {
		t.Errorf("expected 0 delegations (researcher can't handle code), got %v", result.Data["delegations"])
	}
}

// TestDelegationStage_CapabilityCheck_ResearchTask verifies that
// a researcher CAN receive a research task.
func TestDelegationStage_CapabilityCheck_ResearchTask(t *testing.T) {
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	engine := delegation.NewEngine(store, nil)

	agentConfigs := map[string]*orchestrator.AgentConfig{
		"lumi": {
			ID:           "lumi",
			Role:         "lead",
			Provider:     "ollama",
			Model:        "glm-5.1:cloud",
			Primary:      true,
			Capabilities: []string{"plan", "delegate", "review"},
		},
		"researcher": {
			ID:           "researcher",
			Role:         "researcher",
			Provider:     "ollama",
			Model:        "qwen3-vl:235b-cloud",
			Capabilities: []string{"search", "summarize"},
		},
	}

	stage := &DelegationStage{Engine: engine, StripMarkers: true, AgentConfigs: agentConfigs}

	// Lumi delegates a research task to researcher — should succeed
	rc := &RunContext{
		RunID:       "test-cap-research",
		Task:        "delegate research",
		Agent:       "lumi",
		LLMResponse: "Let me delegate. [DELEGATE: researcher | research] Find the docs",
	}

	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["delegations"] != 1 {
		t.Errorf("expected 1 delegation, got %v", result.Data["delegations"])
	}
}

// TestDelegationStage_CapabilityCheck_NilConfigs verifies that nil
// AgentConfigs means no capability checks (permissive).
func TestDelegationStage_CapabilityCheck_NilConfigs(t *testing.T) {
	store, err := task.NewStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	engine := delegation.NewEngine(store, nil)

	// No AgentConfigs — all delegations allowed
	stage := &DelegationStage{Engine: engine, StripMarkers: true, AgentConfigs: nil}

	rc := &RunContext{
		RunID:       "test-nil-configs",
		Task:        "delegate",
		Agent:       "lumi",
		LLMResponse: "Let me delegate. [DELEGATE: mango | code] Write it",
	}

	_, result, err := stage.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["delegations"] != 1 {
		t.Errorf("expected 1 delegation (nil configs = permissive), got %v", result.Data["delegations"])
	}
}
