package action

import (
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

func TestMatchTriggerExact(t *testing.T) {
	tests := []struct {
		pattern  string
		event    string
		expected bool
	}{
		{"lumi.agent.output", "lumi.agent.output", true},
		{"lumi.agent.output", "mango.agent.output", false},
		{"prizm.cost.tracked", "prizm.cost.tracked", true},
		{"prizm.cost.tracked", "prizm.cost.reported", false},
	}

	for _, tt := range tests {
		got := matchTrigger(tt.pattern, tt.event)
		if got != tt.expected {
			t.Errorf("matchTrigger(%q, %q) = %v, want %v", tt.pattern, tt.event, got, tt.expected)
		}
	}
}

func TestMatchTriggerSingleWildcard(t *testing.T) {
	tests := []struct {
		pattern  string
		event    string
		expected bool
	}{
		{"*.tool.completed", "lumi.tool.completed", true},
		{"*.tool.completed", "mango.tool.completed", true},
		{"*.tool.completed", "prizm1.tool.completed", true},
		{"*.tool.completed", "prizm.tool.completed", true},
		{"*.tool.completed", "lumi.agent.completed", false},
		{"*.agent.output", "lumi.agent.output", true},
		{"*.agent.output", "support-bot.agent.output", true},
	}

	for _, tt := range tests {
		got := matchTrigger(tt.pattern, tt.event)
		if got != tt.expected {
			t.Errorf("matchTrigger(%q, %q) = %v, want %v", tt.pattern, tt.event, got, tt.expected)
		}
	}
}

func TestMatchTriggerDoubleWildcard(t *testing.T) {
	tests := []struct {
		pattern  string
		event    string
		expected bool
	}{
		{"**.failed", "lumi.agent.failed", true},
		{"**.failed", "mango.tool.failed", true},
		{"**.failed", "prizm.cost.failed", true},
		{"**.failed", "lumi.agent.completed", false},
		{"**.output", "lumi.agent.output", true},
		{"**.output", "mango.agent.output", true},
		{"**.completed", "prizm.task.completed", true},
	}

	for _, tt := range tests {
		got := matchTrigger(tt.pattern, tt.event)
		if got != tt.expected {
			t.Errorf("matchTrigger(%q, %q) = %v, want %v", tt.pattern, tt.event, got, tt.expected)
		}
	}
}

func TestMatchTriggerMixedWildcard(t *testing.T) {
	tests := []struct {
		pattern  string
		event    string
		expected bool
	}{
		{"lumi.*.completed", "lumi.agent.completed", true},
		{"lumi.*.completed", "lumi.tool.completed", true},
		{"lumi.*.completed", "mango.agent.completed", false},
		{"prizm.**.tracked", "prizm.cost.tracked", true},
		{"prizm.**.tracked", "prizm.agent.cost.tracked", true},
	}

	for _, tt := range tests {
		got := matchTrigger(tt.pattern, tt.event)
		if got != tt.expected {
			t.Errorf("matchTrigger(%q, %q) = %v, want %v", tt.pattern, tt.event, got, tt.expected)
		}
	}
}

func TestRegistryProcessEvent(t *testing.T) {
	reg := NewRegistry()

	// Register a handler
	var capturedEvent event.Event
	handlerCalled := 0
	reg.RegisterHandler("prizm.cost.track", func(evt event.Event, action Action) error {
		capturedEvent = evt
		handlerCalled++
		return nil
	})

	// Register an action
	reg.RegisterAction(Action{
		Trigger: "*.tool.completed",
		Action:  "prizm.cost.track",
		Enabled: true,
	})

	// Fire an event
	evt := event.Event{
		Type: "lumi.tool.completed",
		Payload: map[string]any{
			"tool_name": "read_file",
			"agent":     "lumi",
		},
	}

	triggered, errs := reg.ProcessEvent(evt)
	if triggered != 1 {
		t.Errorf("expected 1 action triggered, got %d", triggered)
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if handlerCalled != 1 {
		t.Errorf("expected handler called once, got %d", handlerCalled)
	}
	if capturedEvent.Type != "lumi.tool.completed" {
		t.Errorf("expected event type 'lumi.tool.completed', got %q", capturedEvent.Type)
	}
}

func TestRegistryDisabledAction(t *testing.T) {
	reg := NewRegistry()

	handlerCalled := 0
	reg.RegisterHandler("test.action", func(evt event.Event, action Action) error {
		handlerCalled++
		return nil
	})

	reg.RegisterAction(Action{
		Trigger: "*.tool.completed",
		Action:  "test.action",
		Enabled: false, // Disabled
	})

	evt := event.Event{Type: "lumi.tool.completed"}
	triggered, _ := reg.ProcessEvent(evt)

	if triggered != 0 {
		t.Errorf("expected 0 actions triggered for disabled action, got %d", triggered)
	}
	if handlerCalled != 0 {
		t.Error("expected handler not called for disabled action")
	}
}

func TestRegistryNoHandler(t *testing.T) {
	reg := NewRegistry()

	reg.RegisterAction(Action{
		Trigger: "*.tool.completed",
		Action:  "nonexistent.handler",
		Enabled: true,
	})

	evt := event.Event{Type: "lumi.tool.completed"}
	_, errs := reg.ProcessEvent(evt)

	if len(errs) != 1 {
		t.Errorf("expected 1 error for missing handler, got %d", len(errs))
	}
}

func TestRegistryList(t *testing.T) {
	reg := NewRegistry()

	reg.RegisterAction(Action{Trigger: "*.tool.completed", Action: "prizm.cost.track", Enabled: true})
	reg.RegisterAction(Action{Trigger: "lumi.agent.output", Action: "remembrance.gate.extract", Enabled: true})

	actions := reg.List()
	if len(actions) != 2 {
		t.Errorf("expected 2 actions, got %d", len(actions))
	}
}

func TestRegisterActionValidation(t *testing.T) {
	reg := NewRegistry()

	err := reg.RegisterAction(Action{Trigger: "", Action: "test.handler"})
	if err == nil {
		t.Error("expected error for empty trigger")
	}

	err = reg.RegisterAction(Action{Trigger: "*.tool.completed", Action: ""})
	if err == nil {
		t.Error("expected error for empty action")
	}
}

func TestRegisterHandlerDuplicate(t *testing.T) {
	reg := NewRegistry()

	handler := func(evt event.Event, action Action) error { return nil }

	err := reg.RegisterHandler("test.action", handler)
	if err != nil {
		t.Errorf("unexpected error on first register: %v", err)
	}

	err = reg.RegisterHandler("test.action", handler)
	if err == nil {
		t.Error("expected error for duplicate handler")
	}
}
