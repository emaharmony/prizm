package runstatus

import (
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

func TestRunStatusProjection_Name(t *testing.T) {
	p := New()
	if p.Name() != "run_status" {
		t.Errorf("Name() = %q, want %q", p.Name(), "run_status")
	}
}

func TestRunStatusProjection_Subscribe(t *testing.T) {
	p := New()
	subs := p.Subscribe()
	expected := []string{
		event.V1EventTypes.TaskCreated,
		event.V1EventTypes.TaskStarted,
		event.V1EventTypes.TaskCompleted,
		event.V1EventTypes.TaskFailed,
	}
	if len(subs) != len(expected) {
		t.Fatalf("Subscribe() length = %d, want %d", len(subs), len(expected))
	}
	for i, s := range subs {
		if s != expected[i] {
			t.Errorf("Subscribe()[%d] = %q, want %q", i, s, expected[i])
		}
	}
}

func TestRunStatusProjection_LifecycleTransitions(t *testing.T) {
	p := New()

	// Task created
	p.Apply(event.NewEvent("prizm.task.created", "test", map[string]any{
		"task": "hello world", "project": "prizm", "agent": "lumi",
	}))
	snap := p.Snapshot()
	if snap["status"] != "created" {
		t.Errorf("after created: status = %v, want created", snap["status"])
	}
	if snap["task"] != "hello world" {
		t.Errorf("after created: task = %v, want hello world", snap["task"])
	}

	// Task started
	p.Apply(event.NewEvent("prizm.task.started", "test", map[string]any{
		"task": "hello world", "project": "prizm", "agent": "lumi",
	}))
	snap = p.Snapshot()
	if snap["status"] != "running" {
		t.Errorf("after started: status = %v, want running", snap["status"])
	}

	// Task completed
	p.Apply(event.NewEvent("prizm.task.completed", "test", map[string]any{
		"task": "hello world", "project": "prizm", "agent": "lumi",
	}))
	snap = p.Snapshot()
	if snap["status"] != "completed" {
		t.Errorf("after completed: status = %v, want completed", snap["status"])
	}
}

func TestRunStatusProjection_FailedRun(t *testing.T) {
	p := New()

	p.Apply(event.NewEvent("prizm.task.created", "test", map[string]any{
		"task": "failing task",
	}))
	p.Apply(event.NewEvent("prizm.task.started", "test", map[string]any{
		"task": "failing task",
	}))
	p.Apply(event.NewEvent("prizm.task.failed", "test", map[string]any{
		"task": "failing task", "error": "something went wrong",
	}))

	snap := p.Snapshot()
	if snap["status"] != "failed" {
		t.Errorf("status = %v, want failed", snap["status"])
	}
	if snap["error"] != "something went wrong" {
		t.Errorf("error = %v, want 'something went wrong'", snap["error"])
	}
}

func TestRunStatusProjection_Idempotency(t *testing.T) {
	p := New()

	// Apply the same created event twice
	evt := event.NewEvent("prizm.task.created", "test", map[string]any{
		"task": "idempotent test",
	})
	p.Apply(evt)
	p.Apply(evt)

	snap := p.Snapshot()
	if snap["status"] != "created" {
		t.Errorf("after duplicate apply: status = %v, want created", snap["status"])
	}
}

func TestRunStatusProjection_IgnoresUnrelatedEvents(t *testing.T) {
	p := New()

	// Apply an unrelated event type
	p.Apply(event.NewEvent("prizm.tool.requested", "test", nil))

	snap := p.Snapshot()
	if snap["status"] != "unknown" {
		t.Errorf("after unrelated event: status = %v, want unknown", snap["status"])
	}
}

func TestRunStatusProjection_MetadataFromPayload(t *testing.T) {
	p := New()

	evt := event.NewEvent("prizm.task.created", "test", map[string]any{
		"task": "metadata test", "project": "myproject", "agent": "assistant",
	})

	p.Apply(evt)
	snap := p.Snapshot()

	if snap["task"] != "metadata test" {
		t.Errorf("task = %v, want metadata test", snap["task"])
	}
	if snap["project"] != "myproject" {
		t.Errorf("project = %v, want myproject", snap["project"])
	}
	if snap["agent"] != "assistant" {
		t.Errorf("agent = %v, want assistant", snap["agent"])
	}
}
