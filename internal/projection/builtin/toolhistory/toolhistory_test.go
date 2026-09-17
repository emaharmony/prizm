package toolhistory

import (
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

func TestToolHistoryProjection_Name(t *testing.T) {
	p := New()
	if p.Name() != "tool_history" {
		t.Errorf("Name() = %q, want %q", p.Name(), "tool_history")
	}
}

func TestToolHistoryProjection_Subscribe(t *testing.T) {
	p := New()
	subs := p.Subscribe()
	if len(subs) != 6 {
		t.Fatalf("Subscribe() length = %d, want 6", len(subs))
	}
}

func TestToolHistoryProjection_FullLifecycle(t *testing.T) {
	p := New()

	// Tool requested
	p.Apply(event.NewEvent("prizm.tool.requested", "test", map[string]any{
		"tool_name": "echo", "policy_decision": "allowed",
	}))
	// Tool approved
	p.Apply(event.NewEvent("prizm.tool.approved", "test", map[string]any{
		"tool_name": "echo",
	}))
	// Tool started
	p.Apply(event.NewEvent("prizm.tool.started", "test", map[string]any{
		"tool_name": "echo",
	}))
	// Tool completed
	p.Apply(event.NewEvent("prizm.tool.completed", "test", map[string]any{
		"tool_name": "echo", "result": "hello world",
	}))

	snap := p.Snapshot()
	calls := snap["calls"].([]map[string]any)
	if len(calls) != 1 {
		t.Fatalf("calls length = %d, want 1", len(calls))
	}
	if calls[0]["tool_name"] != "echo" {
		t.Errorf("tool_name = %v, want echo", calls[0]["tool_name"])
	}
	if calls[0]["status"] != "completed" {
		t.Errorf("status = %v, want completed", calls[0]["status"])
	}

	summary := snap["summary"].(map[string]int)
	if summary["total"] != 1 {
		t.Errorf("summary total = %d, want 1", summary["total"])
	}
	if summary["succeeded"] != 1 {
		t.Errorf("summary succeeded = %d, want 1", summary["succeeded"])
	}
}

func TestToolHistoryProjection_Denied(t *testing.T) {
	p := New()

	p.Apply(event.NewEvent("prizm.tool.requested", "test", map[string]any{
		"tool_name": "run_command", "policy_decision": "denied",
	}))
	p.Apply(event.NewEvent("prizm.tool.denied", "test", map[string]any{
		"tool_name": "run_command",
	}))

	snap := p.Snapshot()
	calls := snap["calls"].([]map[string]any)
	if calls[0]["status"] != "denied" {
		t.Errorf("status = %v, want denied", calls[0]["status"])
	}

	summary := snap["summary"].(map[string]int)
	if summary["denied"] != 1 {
		t.Errorf("summary denied = %d, want 1", summary["denied"])
	}
}

func TestToolHistoryProjection_MultipleCalls(t *testing.T) {
	p := New()

	// First tool call (echo)
	p.Apply(event.NewEvent("prizm.tool.requested", "test", map[string]any{
		"tool_name": "echo",
	}))
	p.Apply(event.NewEvent("prizm.tool.approved", "test", map[string]any{
		"tool_name": "echo",
	}))
	p.Apply(event.NewEvent("prizm.tool.completed", "test", map[string]any{
		"tool_name": "echo",
	}))

	// Second tool call (read_file)
	p.Apply(event.NewEvent("prizm.tool.requested", "test", map[string]any{
		"tool_name": "read_file",
	}))
	p.Apply(event.NewEvent("prizm.tool.approved", "test", map[string]any{
		"tool_name": "read_file",
	}))
	p.Apply(event.NewEvent("prizm.tool.completed", "test", map[string]any{
		"tool_name": "read_file",
	}))

	snap := p.Snapshot()
	calls := snap["calls"].([]map[string]any)
	if len(calls) != 2 {
		t.Fatalf("calls length = %d, want 2", len(calls))
	}
	if calls[0]["tool_name"] != "echo" {
		t.Errorf("first call tool_name = %v, want echo", calls[0]["tool_name"])
	}
	if calls[1]["tool_name"] != "read_file" {
		t.Errorf("second call tool_name = %v, want read_file", calls[1]["tool_name"])
	}

	summary := snap["summary"].(map[string]int)
	if summary["total"] != 2 {
		t.Errorf("summary total = %d, want 2", summary["total"])
	}
}

func TestToolHistoryProjection_FailedCall(t *testing.T) {
	p := New()

	p.Apply(event.NewEvent("prizm.tool.requested", "test", map[string]any{
		"tool_name": "read_file",
	}))
	p.Apply(event.NewEvent("prizm.tool.started", "test", map[string]any{
		"tool_name": "read_file",
	}))
	p.Apply(event.NewEvent("prizm.tool.failed", "test", map[string]any{
		"tool_name": "read_file", "error": "file not found",
	}))

	snap := p.Snapshot()
	calls := snap["calls"].([]map[string]any)
	if calls[0]["status"] != "failed" {
		t.Errorf("status = %v, want failed", calls[0]["status"])
	}
	if calls[0]["error"] != "file not found" {
		t.Errorf("error = %v, want 'file not found'", calls[0]["error"])
	}

	summary := snap["summary"].(map[string]int)
	if summary["failed"] != 1 {
		t.Errorf("summary failed = %d, want 1", summary["failed"])
	}
}

func TestToolHistoryProjection_IgnoresUnrelatedEvents(t *testing.T) {
	p := New()
	p.Apply(event.NewEvent("prizm.task.created", "test", nil))

	snap := p.Snapshot()
	summary := snap["summary"].(map[string]int)
	if summary["total"] != 0 {
		t.Errorf("summary total = %d, want 0", summary["total"])
	}
}
