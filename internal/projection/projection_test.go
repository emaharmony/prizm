package projection

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

// testProjection is a minimal Projection implementation for testing.
type testProjection struct {
	name      string
	subscribe []string
	state     map[string]any
	applied   []string // tracks which event types were applied
}

func (t *testProjection) Name() string             { return t.name }
func (t *testProjection) Subscribe() []string      { return t.subscribe }
func (t *testProjection) Snapshot() map[string]any { return t.state }

func (t *testProjection) Apply(evt event.Event) error {
	t.applied = append(t.applied, evt.Type)
	if t.state == nil {
		t.state = make(map[string]any)
	}
	t.state["last_event"] = evt.Type
	t.state["count"] = len(t.applied)
	return nil
}

func TestProjectionInterface(t *testing.T) {
	p := &testProjection{name: "test", subscribe: []string{"prizm.task.created"}}
	if p.Name() != "test" {
		t.Errorf("Name() = %q, want %q", p.Name(), "test")
	}
	subs := p.Subscribe()
	if len(subs) != 1 || subs[0] != "prizm.task.created" {
		t.Errorf("Subscribe() = %v, want [prizm.task.created]", subs)
	}
}

func TestRunnerNew(t *testing.T) {
	p1 := &testProjection{name: "proj1", subscribe: []string{"prizm.task.created"}}
	p2 := &testProjection{name: "proj2", subscribe: []string{"prizm.task.started"}}

	r := NewRunner(p1, p2)
	if len(r.projections) != 2 {
		t.Errorf("NewRunner() projections = %d, want 2", len(r.projections))
	}
}

func TestRunnerList(t *testing.T) {
	p1 := &testProjection{name: "alpha", subscribe: []string{"prizm.task.created"}}
	p2 := &testProjection{name: "beta", subscribe: []string{"prizm.task.started"}}

	r := NewRunner(p1, p2)
	names := r.List()
	if len(names) != 2 {
		t.Errorf("List() = %v, want 2 names", names)
	}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("List() = %v, want [alpha beta]", names)
	}
}

func TestRunnerRunFromEvents(t *testing.T) {
	p := &testProjection{
		name:      "task_tracker",
		subscribe: []string{"prizm.task.created", "prizm.task.started"},
	}

	r := NewRunner(p)
	events := []event.Event{
		event.NewEvent("prizm.task.created", "test", map[string]any{"task": "hello"}),
		event.NewEvent("prizm.task.started", "test", map[string]any{"task": "hello"}),
	}

	dir := t.TempDir()
	err := r.RunFromEvents(events, dir)
	if err != nil {
		t.Fatalf("RunFromEvents() error = %v", err)
	}

	// Check that the projection was applied to both events
	if len(p.applied) != 2 {
		t.Errorf("applied events = %d, want 2", len(p.applied))
	}

	// Check that snapshot was written
	path := filepath.Join(dir, "projections", "task_tracker.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var snapshot map[string]any
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if snapshot["last_event"] != "prizm.task.started" {
		t.Errorf("snapshot last_event = %v, want prizm.task.started", snapshot["last_event"])
	}
}

func TestRunnerSubscriptionFiltering(t *testing.T) {
	taskProj := &testProjection{
		name:      "task_only",
		subscribe: []string{"prizm.task.created"},
	}
	toolProj := &testProjection{
		name:      "tool_only",
		subscribe: []string{"prizm.tool.requested"},
	}

	r := NewRunner(taskProj, toolProj)
	events := []event.Event{
		event.NewEvent("prizm.task.created", "test", nil),
		event.NewEvent("prizm.tool.requested", "test", nil),
		event.NewEvent("prizm.task.started", "test", nil), // not subscribed by either
	}

	dir := t.TempDir()
	err := r.RunFromEvents(events, dir)
	if err != nil {
		t.Fatalf("RunFromEvents() error = %v", err)
	}

	// task_only should only see task.created, not task.started
	if len(taskProj.applied) != 1 {
		t.Errorf("task_only applied = %d, want 1", len(taskProj.applied))
	}
	if len(taskProj.applied) > 0 && taskProj.applied[0] != "prizm.task.created" {
		t.Errorf("task_only first event = %v, want prizm.task.created", taskProj.applied[0])
	}

	// tool_only should only see tool.requested
	if len(toolProj.applied) != 1 {
		t.Errorf("tool_only applied = %d, want 1", len(toolProj.applied))
	}
}

func TestRunnerWildcardSubscription(t *testing.T) {
	allProj := &testProjection{
		name:      "all_events",
		subscribe: []string{"*"},
	}

	r := NewRunner(allProj)
	events := []event.Event{
		event.NewEvent("prizm.task.created", "test", nil),
		event.NewEvent("prizm.tool.requested", "test", nil),
		event.NewEvent("prizm.approval.granted", "test", nil),
	}

	dir := t.TempDir()
	err := r.RunFromEvents(events, dir)
	if err != nil {
		t.Fatalf("RunFromEvents() error = %v", err)
	}

	// Wildcard should catch all 3 events
	if len(allProj.applied) != 3 {
		t.Errorf("all_events applied = %d, want 3", len(allProj.applied))
	}
}

func TestRunnerRunFromJSONL(t *testing.T) {
	// Create a temporary events.jsonl file
	dir := t.TempDir()
	eventsFile := filepath.Join(dir, "events.jsonl")

	events := []event.Event{
		event.NewEvent("prizm.task.created", "test", map[string]any{"task": "hello"}).WithCorrelationID("corr_test"),
		event.NewEvent("prizm.task.started", "test", map[string]any{"task": "hello"}).WithCorrelationID("corr_test"),
	}

	// Write events as JSONL
	f, err := os.Create(eventsFile)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	for _, evt := range events {
		data, _ := json.Marshal(evt)
		f.Write(data)
		f.Write([]byte("\n"))
	}
	f.Close()

	p := &testProjection{
		name:      "from_file",
		subscribe: []string{"prizm.task.created", "prizm.task.started"},
	}

	r := NewRunner(p)
	runDir := filepath.Join(dir, "runs", "run_test")
	os.MkdirAll(runDir, 0755)

	err = r.Run(eventsFile, runDir)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(p.applied) != 2 {
		t.Errorf("applied events = %d, want 2", len(p.applied))
	}
}

func TestRunnerEmptyEvents(t *testing.T) {
	p := &testProjection{
		name:      "empty_test",
		subscribe: []string{"prizm.task.created"},
	}

	r := NewRunner(p)
	dir := t.TempDir()

	err := r.RunFromEvents([]event.Event{}, dir)
	if err != nil {
		t.Fatalf("RunFromEvents() with empty events error = %v", err)
	}

	// Should still write an empty snapshot
	path := filepath.Join(dir, "projections", "empty_test.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var snapshot map[string]any
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	// Snapshot should have count = 0 (no events applied)
	if snapshot["count"] != nil {
		t.Errorf("snapshot count = %v, want nil (no events)", snapshot["count"])
	}
}

func TestRunnerProjectionOrder(t *testing.T) {
	// Events should be applied in order
	p := &testProjection{
		name:      "order_test",
		subscribe: []string{"*"},
	}

	events := []event.Event{
		event.NewEvent("prizm.task.created", "test", nil),
		event.NewEvent("prizm.task.started", "test", nil),
		event.NewEvent("prizm.task.completed", "test", nil),
	}

	r := NewRunner(p)
	dir := t.TempDir()
	err := r.RunFromEvents(events, dir)
	if err != nil {
		t.Fatalf("RunFromEvents() error = %v", err)
	}

	if len(p.applied) != 3 {
		t.Fatalf("applied = %d, want 3", len(p.applied))
	}

	if p.applied[0] != "prizm.task.created" ||
		p.applied[1] != "prizm.task.started" ||
		p.applied[2] != "prizm.task.completed" {
		t.Errorf("applied order = %v, want [created, started, completed]", p.applied)
	}
}
