package event

import (
	"testing"
)

func TestValidate_ValidEvent(t *testing.T) {
	evt := Event{
		Type:    "prizm.task.created",
		Payload: map[string]any{"task": "test"},
	}
	if err := Validate(evt); err != nil {
		t.Errorf("expected valid event, got error: %v", err)
	}
}

func TestValidate_MissingRequired(t *testing.T) {
	evt := Event{
		Type:    "prizm.task.created",
		Payload: map[string]any{},
	}
	err := Validate(evt)
	if err == nil {
		t.Fatal("expected validation error for missing required field")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if ve.EventType != "prizm.task.created" {
		t.Errorf("expected event type 'prizm.task.created', got %q", ve.EventType)
	}
	if len(ve.Missing) != 1 || ve.Missing[0] != "task" {
		t.Errorf("expected missing ['task'], got %v", ve.Missing)
	}
}

func TestValidate_UnknownEventType(t *testing.T) {
	evt := Event{
		Type:    "prizm.custom.event",
		Payload: map[string]any{},
	}
	if err := Validate(evt); err != nil {
		t.Errorf("unknown event types should pass validation, got: %v", err)
	}
}

func TestValidate_LLMCompleted(t *testing.T) {
	evt := Event{
		Type: "prizm.llm.completed",
		Payload: map[string]any{
			"provider": "ollama",
			"model":    "llama3",
		},
	}
	if err := Validate(evt); err != nil {
		t.Errorf("expected valid LLM event, got: %v", err)
	}
}

func TestValidate_LLMCompleted_MissingProvider(t *testing.T) {
	evt := Event{
		Type: "prizm.llm.completed",
		Payload: map[string]any{
			"model": "llama3",
		},
	}
	err := Validate(evt)
	if err == nil {
		t.Fatal("expected validation error for missing provider")
	}
}

func TestValidate_ApprovalRequested(t *testing.T) {
	evt := Event{
		Type: "prizm.approval.requested",
		Payload: map[string]any{
			"approval_id":   "apr_123",
			"mutation_type": "write_file",
		},
	}
	if err := Validate(evt); err != nil {
		t.Errorf("expected valid approval event, got: %v", err)
	}
}

func TestValidate_ToolCompleted(t *testing.T) {
	evt := Event{
		Type: "prizm.tool.completed",
		Payload: map[string]any{
			"tool_name": "read_file",
			"result":    "file contents",
		},
	}
	if err := Validate(evt); err != nil {
		t.Errorf("expected valid tool event, got: %v", err)
	}
}

func TestValidate_WorkflowStarted(t *testing.T) {
	evt := Event{
		Type: "prizm.workflow.started",
		Payload: map[string]any{
			"workflow_id": "wf_123",
			"name":        "analyze",
		},
	}
	if err := Validate(evt); err != nil {
		t.Errorf("expected valid workflow event, got: %v", err)
	}
}

func TestValidate_CostTracked(t *testing.T) {
	evt := Event{
		Type: "prizm.cost.tracked",
		Payload: map[string]any{
			"provider": "openai",
			"model":    "gpt-4o",
		},
	}
	if err := Validate(evt); err != nil {
		t.Errorf("expected valid cost event, got: %v", err)
	}
}

func TestValidationError_Error(t *testing.T) {
	ve := &ValidationError{
		EventType: "prizm.task.created",
		Missing:   []string{"task", "provider"},
	}
	msg := ve.Error()
	if msg != "event prizm.task.created: missing required fields: task, provider" {
		t.Errorf("unexpected error message: %q", msg)
	}
}
