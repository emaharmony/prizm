package event_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

func TestNewID(t *testing.T) {
	id := event.NewID()
	if !strings.HasPrefix(id, "evt_") {
		t.Errorf("expected ID to start with 'evt_', got %s", id)
	}
	// ULID should be 26 chars, so evt_ + 26 = 30
	if len(id) != 30 {
		t.Errorf("expected ID length 30 (evt_ + 26 ULID chars), got %d", len(id))
	}
}

func TestNewIDUniqueness(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := event.NewID()
		if ids[id] {
			t.Errorf("duplicate ID generated: %s", id)
		}
		ids[id] = true
	}
}

func TestNewCorrelationID(t *testing.T) {
	id := event.NewCorrelationID()
	if !strings.HasPrefix(id, "corr_") {
		t.Errorf("expected correlation ID to start with 'corr_', got %s", id)
	}
}

func TestNewRunID(t *testing.T) {
	id := event.NewRunID()
	if !strings.HasPrefix(id, "run_") {
		t.Errorf("expected run ID to start with 'run_', got %s", id)
	}
}

func TestNewSessionID(t *testing.T) {
	id := event.NewSessionID()
	if !strings.HasPrefix(id, "sess_") {
		t.Errorf("expected session ID to start with 'sess_', got %s", id)
	}
}

func TestNewEvent(t *testing.T) {
	evt := event.NewEvent(event.V1EventTypes.TaskCreated, "prizm-cli", map[string]any{
		"task": "test task",
	})

	if evt.Type != "prizm.task.created" {
		t.Errorf("expected type 'prizm.task.created', got %s", evt.Type)
	}
	if evt.Source != "prizm-cli" {
		t.Errorf("expected source 'prizm-cli', got %s", evt.Source)
	}
	if evt.ID == "" {
		t.Error("expected non-empty ID")
	}
	if evt.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
	// Verify timestamp is valid RFC3339
	if _, err := time.Parse(time.RFC3339Nano, evt.Timestamp); err != nil {
		t.Errorf("timestamp is not valid RFC3339Nano: %v", err)
	}
	task, ok := evt.Payload["task"].(string)
	if !ok || task != "test task" {
		t.Errorf("expected payload.task='test task', got %v", evt.Payload["task"])
	}
}

func TestEventWithCorrelationID(t *testing.T) {
	evt := event.NewEvent(event.V1EventTypes.TaskStarted, "prizm-cli", nil)
	evt = evt.WithCorrelationID("corr_test123")
	if evt.CorrelationID != "corr_test123" {
		t.Errorf("expected correlation_id='corr_test123', got %s", evt.CorrelationID)
	}
}

func TestEventWithParentID(t *testing.T) {
	evt := event.NewEvent(event.V1EventTypes.AgentStarted, "prizm-cli", nil)
	evt = evt.WithParentID("evt_parent123")
	if evt.ParentID != "evt_parent123" {
		t.Errorf("expected parent_id='evt_parent123', got %s", evt.ParentID)
	}
}

func TestEventWithMetadata(t *testing.T) {
	evt := event.NewEvent(event.V1EventTypes.TaskCreated, "prizm-cli", nil)
	evt = evt.WithMetadata(event.EventMetadata{
		RunID:     "run_abc",
		SessionID: "sess_xyz",
		Project:   "prizm",
		Agent:     "lumi",
	})
	if evt.Metadata.RunID != "run_abc" {
		t.Errorf("expected run_id='run_abc', got %s", evt.Metadata.RunID)
	}
	if evt.Metadata.Agent != "lumi" {
		t.Errorf("expected agent='lumi', got %s", evt.Metadata.Agent)
	}
}

func TestEventJSONRoundTrip(t *testing.T) {
	original := event.NewEvent(event.V1EventTypes.AgentOutput, "prizm-cli", map[string]any{
		"status":  "completed",
		"summary": "Task executed",
	})
	original = original.WithCorrelationID("corr_test")
	original = original.WithParentID("evt_parent")
	original = original.WithMetadata(event.EventMetadata{
		RunID:   "run_test",
		Project: "prizm",
		Agent:   "lumi",
	})

	data, err := original.ToJSON()
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}

	parsed, err := event.EventFromBytes(data)
	if err != nil {
		t.Fatalf("failed to unmarshal event: %v", err)
	}

	if parsed.ID != original.ID {
		t.Errorf("ID mismatch: %s != %s", parsed.ID, original.ID)
	}
	if parsed.Type != original.Type {
		t.Errorf("Type mismatch: %s != %s", parsed.Type, original.Type)
	}
	if parsed.CorrelationID != original.CorrelationID {
		t.Errorf("CorrelationID mismatch: %s != %s", parsed.CorrelationID, original.CorrelationID)
	}
	if parsed.ParentID != original.ParentID {
		t.Errorf("ParentID mismatch: %s != %s", parsed.ParentID, original.ParentID)
	}
	if parsed.Metadata.RunID != original.Metadata.RunID {
		t.Errorf("Metadata.RunID mismatch: %s != %s", parsed.Metadata.RunID, original.Metadata.RunID)
	}
}

func TestV1EventTypes(t *testing.T) {
	types := []struct {
		name     string
		expected string
	}{
		{"TaskCreated", "prizm.task.created"},
		{"TaskStarted", "prizm.task.started"},
		{"TaskCompleted", "prizm.task.completed"},
		{"TaskFailed", "prizm.task.failed"},
		{"MemoryContextRequested", "prizm.memory.context_requested"},
		{"MemoryContextBuilt", "prizm.memory.context_built"},
		{"MemoryContextFailed", "prizm.memory.context_failed"},
		{"AgentStarted", "prizm.agent.started"},
		{"AgentOutput", "prizm.agent.output"},
		{"AgentCompleted", "prizm.agent.completed"},
		{"AgentFailed", "prizm.agent.failed"},
		{"ToolCalled", "prizm.tool.called"},
		{"ToolResult", "prizm.tool.result"},
		{"ToolFailed", "prizm.tool.failed"},
		{"SystemHealth", "prizm.system.health"},
	}

	for _, tc := range types {
		// Access via reflection-like approach using the V1EventTypes struct
		var actual string
		switch tc.name {
		case "TaskCreated":
			actual = event.V1EventTypes.TaskCreated
		case "TaskStarted":
			actual = event.V1EventTypes.TaskStarted
		case "TaskCompleted":
			actual = event.V1EventTypes.TaskCompleted
		case "TaskFailed":
			actual = event.V1EventTypes.TaskFailed
		case "MemoryContextRequested":
			actual = event.V1EventTypes.MemoryContextRequested
		case "MemoryContextBuilt":
			actual = event.V1EventTypes.MemoryContextBuilt
		case "MemoryContextFailed":
			actual = event.V1EventTypes.MemoryContextFailed
		case "AgentStarted":
			actual = event.V1EventTypes.AgentStarted
		case "AgentOutput":
			actual = event.V1EventTypes.AgentOutput
		case "AgentCompleted":
			actual = event.V1EventTypes.AgentCompleted
		case "AgentFailed":
			actual = event.V1EventTypes.AgentFailed
		case "ToolCalled":
			actual = event.V1EventTypes.ToolCalled
		case "ToolResult":
			actual = event.V1EventTypes.ToolResult
		case "ToolFailed":
			actual = event.V1EventTypes.ToolFailed
		case "SystemHealth":
			actual = event.V1EventTypes.SystemHealth
		default:
			t.Errorf("unknown event type: %s", tc.name)
		}
		if actual != tc.expected {
			t.Errorf("%s: expected %s, got %s", tc.name, tc.expected, actual)
		}
	}
}

func TestEventNoSecretsInPayload(t *testing.T) {
	evt := event.NewEvent(event.V1EventTypes.TaskCreated, "prizm-cli", map[string]any{
		"task":    "test",
		"project": "prizm",
	})
	// Verify no common secret keys appear in JSON
	data, _ := json.Marshal(evt)
	s := string(data)
	forbidden := []string{"password", "secret", "api_key", "token", "credential"}
	for _, f := range forbidden {
		if strings.Contains(strings.ToLower(s), f) {
			t.Errorf("event JSON contains forbidden key: %s", f)
		}
	}
}
