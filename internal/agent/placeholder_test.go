package agent_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
)

func TestPlaceholderAgentBasic(t *testing.T) {
	input := agent.PlaceholderInput{
		Task:    "Test V1 event lifecycle",
		Project: "prizm",
		Agent:   "lumi",
	}

	output := agent.RunPlaceholder(input)

	if output.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", output.Status)
	}
	if output.Summary != "V1 lifecycle task executed successfully." {
		t.Errorf("unexpected summary: %s", output.Summary)
	}
	if output.ContextReceived != false {
		t.Error("expected context_received=false for empty context")
	}
	if len(output.Actions) < 3 {
		t.Errorf("expected at least 3 actions, got %d", len(output.Actions))
	}
}

func TestPlaceholderAgentWithContext(t *testing.T) {
	input := agent.PlaceholderInput{
		Task:    "Analyze project",
		Project: "prizm",
		Agent:   "lumi",
		Context: "Previous discussion about event-driven architecture",
	}

	output := agent.RunPlaceholder(input)

	if !output.ContextReceived {
		t.Error("expected context_received=true when context provided")
	}
	if len(output.Actions) < 4 {
		t.Errorf("expected at least 4 actions with context, got %d", len(output.Actions))
	}
	// Should have "incorporated remembrance context" action
	found := false
	for _, a := range output.Actions {
		if a == "incorporated remembrance context" {
			found = true
		}
	}
	if !found {
		t.Error("expected 'incorporated remembrance context' action when context provided")
	}
}

func TestPlaceholderOutputJSON(t *testing.T) {
	input := agent.PlaceholderInput{
		Task:    "Test",
		Project: "prizm",
		Agent:   "lumi",
	}
	output := agent.RunPlaceholder(input)

	data, err := output.ToJSON()
	if err != nil {
		t.Fatalf("failed to marshal output: %v", err)
	}

	var parsed agent.PlaceholderOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}
	if parsed.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", parsed.Status)
	}
}

func TestPlaceholderAlwaysSucceeds(t *testing.T) {
	// The placeholder agent should always return "completed" status
	for i := 0; i < 10; i++ {
		input := agent.PlaceholderInput{
			Task:    "whatever",
			Project: "test",
			Agent:   "test-agent",
		}
		output := agent.RunPlaceholder(input)
		if output.Status != "completed" {
			t.Errorf("placeholder agent failed on iteration %d: %s", i, output.Status)
		}
	}
}

func TestPlaceholderWithDelay(t *testing.T) {
	input := agent.PlaceholderInput{
		Task:    "test delay",
		Project: "prizm",
		Agent:   "lumi",
	}

	delay := 50 * time.Millisecond
	start := time.Now()
	output := agent.RunPlaceholderWithDelay(input, delay)
	elapsed := time.Since(start)

	if output.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", output.Status)
	}
	if elapsed < delay {
		t.Errorf("expected delay >= %v, got %v", delay, elapsed)
	}

	// Verify output is identical to RunPlaceholder
	directOutput := agent.RunPlaceholder(input)
	if output.Status != directOutput.Status {
		t.Errorf("delayed output status %q != direct %q", output.Status, directOutput.Status)
	}
	if output.Summary != directOutput.Summary {
		t.Errorf("delayed output summary %q != direct %q", output.Summary, directOutput.Summary)
	}
}
