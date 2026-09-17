package agentactivity

import (
	"testing"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/event"
)

func TestAgentActivityName(t *testing.T) {
	p := New()
	if p.Name() != "agent_activity" {
		t.Errorf("Name() = %q, want agent_activity", p.Name())
	}
}

func TestAgentActivitySubscribe(t *testing.T) {
	p := New()
	subs := p.Subscribe()
	expected := []string{
		agent.EventAgentDelegated,
		agent.EventAgentCompleted,
		agent.EventAgentFailed,
	}
	if len(subs) != len(expected) {
		t.Fatalf("Subscribe() count = %d, want %d", len(subs), len(expected))
	}
	for i, s := range subs {
		if s != expected[i] {
			t.Errorf("Subscribe()[%d] = %q, want %q", i, s, expected[i])
		}
	}
}

func TestAgentActivityDelegated(t *testing.T) {
	p := New()
	evt := event.NewEvent(agent.EventAgentDelegated, "workflow-runner", map[string]any{
		"agent_name": "coder",
		"subtask":    "implement feature X",
	})
	if err := p.Apply(evt); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	snap := p.Snapshot()
	agents, ok := snap["agents"].(map[string]any)
	if !ok {
		t.Fatal("snapshot agents is not a map")
	}
	coder, ok := agents["coder"].(map[string]any)
	if !ok {
		t.Fatal("coder entry is not a map")
	}
	if coder["delegated"] != 1 {
		t.Errorf("coder delegated = %v, want 1", coder["delegated"])
	}
	if snap["total_delegations"] != 1 {
		t.Errorf("total_delegations = %v, want 1", snap["total_delegations"])
	}
}

func TestAgentActivityCompleted(t *testing.T) {
	p := New()
	evt := event.NewEvent(agent.EventAgentCompleted, "agent-coder", map[string]any{
		"agent_name": "coder",
		"subtask":    "implement feature X",
		"output":     "done",
	})
	if err := p.Apply(evt); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	snap := p.Snapshot()
	agents, _ := snap["agents"].(map[string]any)
	coder, _ := agents["coder"].(map[string]any)
	if coder["completed"] != 1 {
		t.Errorf("coder completed = %v, want 1", coder["completed"])
	}
	if snap["total_completions"] != 1 {
		t.Errorf("total_completions = %v, want 1", snap["total_completions"])
	}
}

func TestAgentActivityFailed(t *testing.T) {
	p := New()
	evt := event.NewEvent(agent.EventAgentFailed, "agent-coder", map[string]any{
		"agent_name": "coder",
		"subtask":    "implement feature X",
		"error":      "timeout",
	})
	if err := p.Apply(evt); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	snap := p.Snapshot()
	agents, _ := snap["agents"].(map[string]any)
	coder, _ := agents["coder"].(map[string]any)
	if coder["failed"] != 1 {
		t.Errorf("coder failed = %v, want 1", coder["failed"])
	}
	if snap["total_failures"] != 1 {
		t.Errorf("total_failures = %v, want 1", snap["total_failures"])
	}
}

func TestAgentActivityMultipleAgents(t *testing.T) {
	p := New()

	// Coder gets 2 delegations, 1 completion
	p.Apply(event.NewEvent(agent.EventAgentDelegated, "workflow", map[string]any{"agent_name": "coder"}))
	p.Apply(event.NewEvent(agent.EventAgentDelegated, "workflow", map[string]any{"agent_name": "coder"}))
	p.Apply(event.NewEvent(agent.EventAgentCompleted, "agent-coder", map[string]any{"agent_name": "coder"}))

	// Reviewer gets 1 delegation, 1 completion
	p.Apply(event.NewEvent(agent.EventAgentDelegated, "workflow", map[string]any{"agent_name": "reviewer"}))
	p.Apply(event.NewEvent(agent.EventAgentCompleted, "agent-reviewer", map[string]any{"agent_name": "reviewer"}))

	snap := p.Snapshot()
	if snap["total_delegations"] != 3 {
		t.Errorf("total_delegations = %v, want 3", snap["total_delegations"])
	}
	if snap["total_completions"] != 2 {
		t.Errorf("total_completions = %v, want 2", snap["total_completions"])
	}

	agents, _ := snap["agents"].(map[string]any)
	coder, _ := agents["coder"].(map[string]any)
	if coder["delegated"] != 2 {
		t.Errorf("coder delegated = %v, want 2", coder["delegated"])
	}
}

func TestAgentActivityMissingAgentName(t *testing.T) {
	p := New()
	// Event without agent_name should be silently skipped
	evt := event.NewEvent(agent.EventAgentDelegated, "workflow", map[string]any{})
	if err := p.Apply(evt); err != nil {
		t.Fatalf("Apply with missing agent_name should not error: %v", err)
	}
	snap := p.Snapshot()
	if snap["total_delegations"] != 0 {
		t.Errorf("expected 0 delegations, got %v", snap["total_delegations"])
	}
}
