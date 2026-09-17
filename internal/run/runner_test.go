package run_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/mutation"
	mockpkg "github.com/emaharmony/prizm/internal/provider/mock"
	"github.com/emaharmony/prizm/internal/review"
	"github.com/emaharmony/prizm/internal/run"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/validation"
)

// startTestServer starts an embedded NATS server for testing.
func startTestServer(t *testing.T) (*server.Server, string) {
	t.Helper()
	s, err := server.NewServer(&server.Options{
		Port:      -1, // Random available port
		StoreDir:  t.TempDir(),
		JetStream: true,
	})
	if err != nil {
		t.Fatalf("failed to create NATS server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS server did not start in time")
	}
	url := fmt.Sprintf("nats://127.0.0.1:%d", s.Addr().(*net.TCPAddr).Port)
	return s, url
}

func unavailableMemoryURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate unavailable memory port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("failed to close unavailable memory port: %v", err)
	}
	return fmt.Sprintf("http://%s", addr)
}

func TestV1LifecycleWithoutMemory(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test V1 event lifecycle",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V1 run failed: %v", err)
	}

	if result.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", result.Status)
	}
	if result.RunID == "" {
		t.Error("expected non-empty run_id")
	}
	if result.EventCount < 6 {
		// task.created, task.started, agent.started, agent.output, agent.completed, task.completed
		t.Errorf("expected at least 6 events, got %d", result.EventCount)
	}

	// Verify events.jsonl exists and is valid
	eventsPath := filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("failed to read events.jsonl: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 6 {
		t.Errorf("expected at least 6 lines in events.jsonl, got %d", len(lines))
	}

	// Verify each line is valid JSON with correct structure
	for i, line := range lines {
		var evt event.Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Errorf("line %d: invalid JSON: %v", i, err)
			continue
		}
		if !strings.HasPrefix(evt.ID, "evt_") {
			t.Errorf("line %d: event ID doesn't start with evt_: %s", i, evt.ID)
		}
		if !strings.HasPrefix(evt.Type, "prizm.") {
			t.Errorf("line %d: event type doesn't start with prizm.: %s", i, evt.Type)
		}
		if evt.CorrelationID == "" {
			t.Errorf("line %d: event has no correlation_id", i)
		}
	}

	// Verify summary.json exists
	summaryPath := filepath.Join(tmpDir, "runs", result.RunID, "summary.json")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("failed to read summary.json: %v", err)
	}

	var summary event.Summary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("failed to parse summary.json: %v", err)
	}
	if summary.Status != "completed" {
		t.Errorf("expected summary status 'completed', got %s", summary.Status)
	}
	if summary.RunID != result.RunID {
		t.Errorf("summary run_id mismatch: %s != %s", summary.RunID, result.RunID)
	}
}

func TestV1LifecycleWithMemoryFailure(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test with memory failure",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: true,
		RequireMemory: false, // Should continue even if memory fails
		MemoryURL:     unavailableMemoryURL(t),
		RunDir:        filepath.Join(tmpDir, "runs"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V1 run with memory failure should not error: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", result.Status)
	}
	// Should have memory.context_requested + memory.context_failed events
	if result.EventCount < 8 {
		t.Errorf("expected at least 8 events (incl. memory events), got %d", result.EventCount)
	}
}

func TestV1LifecycleRequireMemoryFailure(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test require-memory failure",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: true,
		RequireMemory: true, // Should fail if memory is unavailable
		MemoryURL:     unavailableMemoryURL(t),
		RunDir:        filepath.Join(tmpDir, "runs"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err == nil {
		t.Fatal("expected error when require-memory=true and memory is unavailable")
	}
	if result == nil {
		t.Fatal("expected non-nil result even on failure")
	}
	if result.Status != "failed" {
		t.Errorf("expected status 'failed', got %s", result.Status)
	}
}

func TestV1EventCorrelationID(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test correlation ID propagation",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()
	if err != nil {
		t.Fatalf("V1 run failed: %v", err)
	}

	// Read events and verify all have the same correlation_id
	eventsPath := filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("failed to read events.jsonl: %v", err)
	}

	var correlationIDs []string
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		var evt event.Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		correlationIDs = append(correlationIDs, evt.CorrelationID)
	}

	if len(correlationIDs) == 0 {
		t.Fatal("no events found")
	}

	firstCorrID := correlationIDs[0]
	for i, cid := range correlationIDs {
		if cid != firstCorrID {
			t.Errorf("event %d: correlation_id mismatch: %s != %s", i, cid, firstCorrID)
		}
	}
}

func TestV1EventTypesInOrder(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test event order",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()
	if err != nil {
		t.Fatalf("V1 run failed: %v", err)
	}

	// Read events and verify they appear in expected order
	eventsPath := filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("failed to read events.jsonl: %v", err)
	}

	var types []string
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		var evt event.Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		types = append(types, evt.Type)
	}

	// Expected order for V2 no-memory run (provider replaced agent output/completed)
	expected := []string{
		event.V1EventTypes.TaskCreated,
		event.V1EventTypes.TaskStarted,
		event.V1EventTypes.AgentStarted,
		event.V2EventTypes.LLMRequested,
		event.V2EventTypes.LLMCompleted,
		event.V1EventTypes.AgentCompleted,
		event.V2EventTypes.OutputWritten,
		event.V1EventTypes.TaskCompleted,
	}

	if len(types) < len(expected) {
		t.Fatalf("expected at least %d events, got %d", len(expected), len(types))
	}

	for i, exp := range expected {
		if types[i] != exp {
			t.Errorf("event %d: expected type %s, got %s", i, exp, types[i])
		}
	}
}

func TestV1NATSPublishAndSubscribe(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	// Set up a subscriber BEFORE the run to capture events
	nc, err := nats.Connect(busURL)
	if err != nil {
		t.Fatalf("failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("failed to get JetStream context: %v", err)
	}

	// Create stream
	_, _ = js.AddStream(&nats.StreamConfig{
		Name:      "PRIZM",
		Subjects:  []string{"prizm.>"},
		Retention: nats.LimitsPolicy,
		MaxMsgs:   1000000,
		Storage:   nats.MemoryStorage,
	})

	// Subscribe to all events
	received := make([]event.Event, 0)
	var receivedMu sync.Mutex
	sub, err := js.Subscribe("prizm.>", func(msg *nats.Msg) {
		var evt event.Event
		if err := json.Unmarshal(msg.Data, &evt); err == nil {
			receivedMu.Lock()
			received = append(received, evt)
			receivedMu.Unlock()
		}
		msg.Ack()
	}, nats.Durable("test-sub"), nats.ManualAck())
	if err != nil {
		t.Fatalf("failed to subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Give subscriber time to start
	time.Sleep(100 * time.Millisecond)

	// Run the lifecycle
	tmpDir := t.TempDir()
	cfg := run.RunConfig{
		Task:          "Test NATS pub/sub",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
	}

	runner := run.NewRunner(cfg)
	_, err = runner.Run()
	if err != nil {
		t.Fatalf("V1 run failed: %v", err)
	}

	// Give events time to arrive
	time.Sleep(500 * time.Millisecond)

	// Should have received at least 6 events
	receivedMu.Lock()
	defer receivedMu.Unlock()
	if len(received) < 6 {
		t.Errorf("expected at least 6 events received via NATS, got %d", len(received))
	}
}

func TestV1Remembrance404(t *testing.T) {
	// Start a test HTTP server that returns 404 for the context-build endpoint
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	// Simple HTTP server returning 404
	httpServer := &http.Server{Addr: "127.0.0.1:0"}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go httpServer.Serve(listener)
	defer httpServer.Close()

	memoryURL := fmt.Sprintf("http://%s", listener.Addr().String())

	tmpDir := t.TempDir()
	cfg := run.RunConfig{
		Task:          "Test remembrance 404",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: true,
		RequireMemory: false,
		MemoryURL:     memoryURL,
		RunDir:        filepath.Join(tmpDir, "runs"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V1 run with 404 memory should not error: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", result.Status)
	}

	// Read events and verify memory.context_failed was emitted
	eventsPath := filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("failed to read events.jsonl: %v", err)
	}

	foundContextRequested := false
	foundContextFailed := false
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		var evt event.Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		if evt.Type == "prizm.memory.context_requested" {
			foundContextRequested = true
		}
		if evt.Type == "prizm.memory.context_failed" {
			foundContextFailed = true
		}
	}

	if !foundContextRequested {
		t.Error("expected prizm.memory.context_requested event")
	}
	if !foundContextFailed {
		t.Error("expected prizm.memory.context_failed event for 404 response")
	}
}

func TestV1ParentIDInNATS(t *testing.T) {
	// Verify that events published to NATS include parent_id
	// This tests the fix for the emitWithParent bug
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()
	cfg := run.RunConfig{
		Task:          "Test parent_id in NATS",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()
	if err != nil {
		t.Fatalf("V1 run failed: %v", err)
	}

	// Read events.jsonl and verify parent_ids are set
	eventsPath := filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("failed to read events.jsonl: %v", err)
	}

	var events []event.Event
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		var evt event.Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		events = append(events, evt)
	}

	// Build a map of event types to their parent IDs
	parentIDs := make(map[string]string)
	for _, evt := range events {
		parentIDs[evt.Type] = evt.ParentID
	}

	// task.started should have parent = task.created
	taskCreatedID := ""
	for _, evt := range events {
		if evt.Type == "prizm.task.created" {
			taskCreatedID = evt.ID
		}
	}

	if taskCreatedID == "" {
		t.Fatal("task.created event not found")
	}

	// task.started should be parented to task.created
	if parentIDs["prizm.task.started"] != taskCreatedID {
		t.Errorf("task.started parent_id = %q, want %q (task.created ID)", parentIDs["prizm.task.started"], taskCreatedID)
	}

	// agent.started should be parented to task.started
	taskStartedID := ""
	for _, evt := range events {
		if evt.Type == "prizm.task.started" {
			taskStartedID = evt.ID
		}
	}
	if parentIDs["prizm.agent.started"] != taskStartedID {
		t.Errorf("agent.started parent_id = %q, want %q (task.started ID)", parentIDs["prizm.agent.started"], taskStartedID)
	}
}

// parseEventsFile reads events.jsonl and returns parsed events
func parseEventsFile(t *testing.T, eventsPath string) []event.Event {
	t.Helper()
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("failed to read events.jsonl: %v", err)
	}
	var events []event.Event
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		var evt event.Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		events = append(events, evt)
	}
	return events
}

// eventTypes returns the ordered list of event types
func eventTypes(events []event.Event) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return types
}

// assertContainsTypes verifies that expectedTypes appear in order within actualTypes.
// Not necessarily consecutively — just in the right relative order.
func assertContainsTypes(t *testing.T, actualTypes []string, expectedTypes []string) {
	t.Helper()
	ei := 0
	for _, at := range actualTypes {
		if ei < len(expectedTypes) && at == expectedTypes[ei] {
			ei++
		}
	}
	if ei != len(expectedTypes) {
		t.Errorf("expected event types in order %v, but not all found in %v", expectedTypes, actualTypes)
	}
}

// ============================================================================
// V2 Tests
// ============================================================================

func TestV2MockProviderSuccess(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test V2 event lifecycle",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.New(),
		Model:         "mock-model",
		Temperature:   0.2,
		MaxTokens:     2048,
		Timeout:       60 * time.Second,
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V2 run failed: %v", err)
	}

	if result.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", result.Status)
	}
	if result.RunID == "" {
		t.Error("expected non-empty run_id")
	}

	// Verify events.jsonl
	events := parseEventsFile(t, filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl"))
	types := eventTypes(events)

	// Expected V2 lifecycle: task.created, task.started, agent.started,
	// llm.requested, llm.completed, agent.completed, output.written, task.completed
	expected := []string{
		event.V1EventTypes.TaskCreated,
		event.V1EventTypes.TaskStarted,
		event.V1EventTypes.AgentStarted,
		event.V2EventTypes.LLMRequested,
		event.V2EventTypes.LLMCompleted,
		event.V1EventTypes.AgentCompleted,
		event.V2EventTypes.OutputWritten,
		event.V1EventTypes.TaskCompleted,
	}

	if len(events) < len(expected) {
		t.Fatalf("expected at least %d events, got %d", len(expected), len(events))
	}

	assertContainsTypes(t, types, expected)

	// Verify parent_id chains (V2 causal chain):
	// task.started -> task.created
	// agent.started -> task.started
	// llm.requested -> agent.started
	// llm.completed -> llm.requested
	// agent.completed -> llm.completed
	// output.written -> agent.completed
	// task.completed -> agent.completed
	parentMap := make(map[string]string)
	idMap := make(map[string]string)
	for _, evt := range events {
		parentMap[evt.Type] = evt.ParentID
		idMap[evt.Type] = evt.ID
	}

	if parentMap[event.V1EventTypes.TaskStarted] != idMap[event.V1EventTypes.TaskCreated] {
		t.Error("task.started parent should be task.created")
	}
	if parentMap[event.V1EventTypes.AgentStarted] != idMap[event.V1EventTypes.TaskStarted] {
		t.Error("agent.started parent should be task.started")
	}
	if parentMap[event.V2EventTypes.LLMRequested] != idMap[event.V1EventTypes.AgentStarted] {
		t.Error("llm.requested parent should be agent.started")
	}
	if parentMap[event.V2EventTypes.LLMCompleted] != idMap[event.V2EventTypes.LLMRequested] {
		t.Error("llm.completed parent should be llm.requested")
	}
	if parentMap[event.V1EventTypes.AgentCompleted] != idMap[event.V2EventTypes.LLMCompleted] {
		t.Error("agent.completed parent should be llm.completed")
	}
	if parentMap[event.V2EventTypes.OutputWritten] != idMap[event.V1EventTypes.AgentCompleted] {
		t.Error("output.written parent should be agent.completed")
	}
	if parentMap[event.V1EventTypes.TaskCompleted] != idMap[event.V1EventTypes.AgentCompleted] {
		t.Error("task.completed parent should be agent.completed")
	}

	// Verify correlation_id propagation
	var corrID string
	for _, evt := range events {
		if corrID == "" {
			corrID = evt.CorrelationID
		}
		if evt.CorrelationID != corrID {
			t.Errorf("event %s has different correlation_id: %s != %s", evt.Type, evt.CorrelationID, corrID)
		}
	}

	// Verify summary.json has V2 fields
	summaryPath := filepath.Join(tmpDir, "runs", result.RunID, "summary.json")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("failed to read summary.json: %v", err)
	}

	var summary event.Summary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("failed to parse summary.json: %v", err)
	}
	if summary.Provider == "" {
		t.Error("summary missing Provider field")
	}
	if summary.Model == "" {
		t.Error("summary missing Model field")
	}
	if summary.OutputPath == "" {
		t.Error("summary missing OutputPath field")
	}
	if summary.PromptPath == "" {
		t.Error("summary missing PromptPath field")
	}
	if summary.MemoryStatus != "none" {
		t.Errorf("expected MemoryStatus 'none', got '%s'", summary.MemoryStatus)
	}

	// Verify prompt.md and output.md exist
	promptPath := filepath.Join(tmpDir, "runs", result.RunID, "prompt.md")
	if _, err := os.Stat(promptPath); os.IsNotExist(err) {
		t.Error("prompt.md does not exist")
	}
	outputPath := filepath.Join(tmpDir, "runs", result.RunID, "output.md")
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Error("output.md does not exist")
	}

	// Verify output.md has content
	outputData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("failed to read output.md: %v", err)
	}
	if !strings.Contains(string(outputData), "Mock response") {
		t.Error("output.md does not contain mock response")
	}
}

func TestV2MockProviderWithMemory(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	// Start a mock remembrance server
	mockMemory := startMockMemoryServer(t)
	defer mockMemory.Close()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test V2 with memory",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: true,
		RequireMemory: false,
		MemoryURL:     mockMemory.URL,
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.New(),
		Model:         "mock-model",
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V2 run with memory failed: %v", err)
	}

	if result.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", result.Status)
	}

	// Verify events contain V2 context events
	events := parseEventsFile(t, filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl"))
	types := eventTypes(events)

	expected := []string{
		event.V1EventTypes.TaskCreated,
		event.V1EventTypes.TaskStarted,
		event.V1EventTypes.MemoryContextRequested,
		event.V2EventTypes.ContextRequested,
		event.V1EventTypes.MemoryContextBuilt,
		event.V2EventTypes.ContextInjected,
		event.V1EventTypes.AgentStarted,
		event.V2EventTypes.LLMRequested,
		event.V2EventTypes.LLMCompleted,
		event.V1EventTypes.AgentCompleted,
		event.V2EventTypes.OutputWritten,
		event.V1EventTypes.TaskCompleted,
	}

	assertContainsTypes(t, types, expected)

	// Verify V2 context events have correct parent chains
	// context.requested -> task.started
	// context.injected -> task.started
	var taskStartedID string
	for _, evt := range events {
		if evt.Type == event.V1EventTypes.TaskStarted {
			taskStartedID = evt.ID
			break
		}
	}
	for _, evt := range events {
		if evt.Type == event.V2EventTypes.ContextRequested {
			if evt.ParentID != taskStartedID {
				t.Errorf("context.requested parent should be task.started, got %s", evt.ParentID)
			}
		}
		if evt.Type == event.V2EventTypes.ContextInjected {
			if evt.ParentID != taskStartedID {
				t.Errorf("context.injected parent should be task.started, got %s", evt.ParentID)
			}
		}
	}

	// Verify summary MemoryStatus is "injected"
	summaryPath := filepath.Join(tmpDir, "runs", result.RunID, "summary.json")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("failed to read summary.json: %v", err)
	}
	var summary event.Summary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("failed to parse summary.json: %v", err)
	}
	if summary.MemoryStatus != "injected" {
		t.Errorf("expected MemoryStatus 'injected', got '%s'", summary.MemoryStatus)
	}
	if !summary.MemoryUsed {
		t.Error("expected MemoryUsed to be true")
	}

	// Verify prompt.md includes context section
	promptPath := filepath.Join(tmpDir, "runs", result.RunID, "prompt.md")
	promptData, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("failed to read prompt.md: %v", err)
	}
	if !strings.Contains(string(promptData), "## Retrieved Context") {
		t.Error("prompt.md should contain context section")
	}
}

func TestV2LLMFailure(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test V2 LLM failure",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.NewFailing(),
		Model:         "mock-model",
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err == nil {
		t.Fatal("expected error when provider fails")
	}
	if result == nil {
		t.Fatal("expected non-nil result even on failure")
	}
	if result.Status != "failed" {
		t.Errorf("expected status 'failed', got %s", result.Status)
	}

	// Verify event sequence: llm.failed -> agent.failed -> task.failed
	events := parseEventsFile(t, filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl"))
	types := eventTypes(events)

	expected := []string{
		event.V1EventTypes.TaskCreated,
		event.V1EventTypes.TaskStarted,
		event.V1EventTypes.AgentStarted,
		event.V2EventTypes.LLMRequested,
		event.V2EventTypes.LLMFailed,
		event.V2EventTypes.AgentFailed,
		event.V1EventTypes.TaskFailed,
	}

	assertContainsTypes(t, types, expected)

	// Verify parent_id chain for failure path:
	// task.started -> task.created
	// agent.started -> task.started
	// llm.requested -> agent.started
	// llm.failed -> llm.requested
	// agent.failed -> llm.failed
	// task.failed -> task.started
	parentMap := make(map[string]string)
	idMap := make(map[string]string)
	for _, evt := range events {
		parentMap[evt.Type] = evt.ParentID
		idMap[evt.Type] = evt.ID
	}
	if parentMap[event.V2EventTypes.AgentFailed] != idMap[event.V2EventTypes.LLMFailed] {
		t.Error("agent.failed parent should be llm.failed")
	}
	if parentMap[event.V2EventTypes.LLMFailed] != idMap[event.V2EventTypes.LLMRequested] {
		t.Error("llm.failed parent should be llm.requested")
	}

	// Verify llm.failed has error in payload
	for _, evt := range events {
		if evt.Type == event.V2EventTypes.LLMFailed {
			errMsg, ok := evt.Payload["error"].(string)
			if !ok || errMsg == "" {
				t.Error("llm.failed event missing error in payload")
			}
		}
	}

	// Verify summary has LLMError
	summaryPath := filepath.Join(tmpDir, "runs", result.RunID, "summary.json")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("failed to read summary.json: %v", err)
	}
	var summary event.Summary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("failed to parse summary.json: %v", err)
	}
	if summary.LLMError == "" {
		t.Error("summary should have LLMError field set")
	}
}

func TestV2DryRunPrompt(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test dry run",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.New(),
		Model:         "mock-model",
		DryRunPrompt:  true,
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected dry-run status 'completed', got %s", result.Status)
	}

	// Verify events: task.created, task.started, agent.started, agent.completed, task.completed
	// BUT no llm events, no output.written
	events := parseEventsFile(t, filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl"))
	types := eventTypes(events)

	// Should NOT contain LLM events
	for _, evt := range events {
		if evt.Type == event.V2EventTypes.LLMRequested ||
			evt.Type == event.V2EventTypes.LLMCompleted ||
			evt.Type == event.V2EventTypes.LLMFailed ||
			evt.Type == event.V2EventTypes.OutputWritten {
			t.Errorf("dry run should not have %s event", evt.Type)
		}
	}

	expected := []string{
		event.V1EventTypes.TaskCreated,
		event.V1EventTypes.TaskStarted,
		event.V1EventTypes.AgentStarted,
		event.V1EventTypes.AgentCompleted,
		event.V1EventTypes.TaskCompleted,
	}
	assertContainsTypes(t, types, expected)

	// Verify prompt.md exists (that's the whole point of dry-run)
	promptPath := filepath.Join(tmpDir, "runs", result.RunID, "prompt.md")
	if _, err := os.Stat(promptPath); os.IsNotExist(err) {
		t.Error("dry-run: prompt.md should exist")
	}

	// Verify output.md does NOT exist
	outputPath := filepath.Join(tmpDir, "runs", result.RunID, "output.md")
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Error("dry-run: output.md should NOT exist")
	}

	// Verify summary shows dry-run provider
	summaryPath := filepath.Join(tmpDir, "runs", result.RunID, "summary.json")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("failed to read summary.json: %v", err)
	}
	var summary event.Summary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("failed to parse summary.json: %v", err)
	}
	if summary.Provider != "dry-run" {
		t.Errorf("expected dry-run provider 'dry-run', got '%s'", summary.Provider)
	}
	if summary.PromptPath == "" {
		t.Error("dry-run summary should have PromptPath")
	}
}

func TestV2OutputArtifacts(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test output artifacts",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.New(),
		Model:         "mock-model",
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V2 run failed: %v", err)
	}

	runDir := filepath.Join(tmpDir, "runs", result.RunID)

	// Verify events.jsonl exists
	eventsPath := filepath.Join(runDir, "events.jsonl")
	if _, err := os.Stat(eventsPath); os.IsNotExist(err) {
		t.Error("events.jsonl does not exist")
	}

	// Verify summary.json exists
	summaryPath := filepath.Join(runDir, "summary.json")
	if _, err := os.Stat(summaryPath); os.IsNotExist(err) {
		t.Error("summary.json does not exist")
	}

	// Verify prompt.md exists and has content
	promptPath := filepath.Join(runDir, "prompt.md")
	promptData, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("failed to read prompt.md: %v", err)
	}
	if !strings.Contains(string(promptData), "# Prizm Agent Task") {
		t.Error("prompt.md missing expected content")
	}
	if !strings.Contains(string(promptData), "Test output artifacts") {
		t.Error("prompt.md missing task text")
	}

	// Verify output.md exists and has content
	outputPath := filepath.Join(runDir, "output.md")
	outputData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("failed to read output.md: %v", err)
	}
	if len(outputData) == 0 {
		t.Error("output.md is empty")
	}
}

func TestV2ContextInjectionInPrompt(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	mockMemory := startMockMemoryServer(t)
	defer mockMemory.Close()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test context injection",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: true,
		RequireMemory: false,
		MemoryURL:     mockMemory.URL,
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.New(),
		Model:         "mock-model",
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V2 run with memory failed: %v", err)
	}

	// Verify prompt.md includes the context content
	promptPath := filepath.Join(tmpDir, "runs", result.RunID, "prompt.md")
	promptData, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("failed to read prompt.md: %v", err)
	}

	promptStr := string(promptData)
	if !strings.Contains(promptStr, "Relevant Context") {
		t.Error("prompt.md missing Relevant Context section")
	}
	if !strings.Contains(promptStr, "test snippet one") {
		t.Error("prompt.md missing injected context content")
	}

	// Verify agent.started is parented to task.started when no context,
	// but in this case it should come after context.injected
	events := parseEventsFile(t, filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl"))

	var taskStartedIdx, contextInjectedIdx, agentStartedIdx int
	for i, evt := range events {
		switch evt.Type {
		case event.V1EventTypes.TaskStarted:
			taskStartedIdx = i
		case event.V2EventTypes.ContextInjected:
			contextInjectedIdx = i
		case event.V1EventTypes.AgentStarted:
			agentStartedIdx = i
		}
	}

	if taskStartedIdx >= agentStartedIdx {
		t.Error("task.started should appear before agent.started")
	}
	if contextInjectedIdx >= agentStartedIdx {
		t.Error("context.injected should appear before agent.started")
	}
}

func TestV2MemoryFailureGraceful(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test memory failure graceful",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: true,
		RequireMemory: false, // graceful: continue even if memory fails
		MemoryURL:     unavailableMemoryURL(t),
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.New(),
		Model:         "mock-model",
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("graceful memory failure should not error: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", result.Status)
	}

	// Verify events include both V1 and V2 context failure events
	events := parseEventsFile(t, filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl"))
	types := eventTypes(events)

	expected := []string{
		event.V1EventTypes.MemoryContextRequested,
		event.V2EventTypes.ContextRequested,
		event.V1EventTypes.MemoryContextFailed,
		event.V2EventTypes.ContextFailed,
		event.V2EventTypes.LLMCompleted,
		event.V1EventTypes.TaskCompleted,
	}

	assertContainsTypes(t, types, expected)

	// Verify summary MemoryStatus is "failed"
	summaryPath := filepath.Join(tmpDir, "runs", result.RunID, "summary.json")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("failed to read summary.json: %v", err)
	}
	var summary event.Summary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("failed to parse summary.json: %v", err)
	}
	if summary.MemoryStatus != "failed" {
		t.Errorf("expected MemoryStatus 'failed', got '%s'", summary.MemoryStatus)
	}
	if summary.MemoryUsed {
		t.Error("expected MemoryUsed to be false on failure")
	}
}

func TestV2MemoryFailureStrict(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test memory failure strict",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: true,
		RequireMemory: true, // strict: fail if memory unavailable
		MemoryURL:     unavailableMemoryURL(t),
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.New(),
		Model:         "mock-model",
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err == nil {
		t.Fatal("expected error when require-memory=true and memory is unavailable")
	}
	if result == nil {
		t.Fatal("expected non-nil result even on failure")
	}
	if result.Status != "failed" {
		t.Errorf("expected status 'failed', got %s", result.Status)
	}

	// Verify events: context.requested, context.failed, task.failed
	// Should NOT have agent.started, llm events, etc.
	events := parseEventsFile(t, filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl"))
	types := eventTypes(events)

	expected := []string{
		event.V1EventTypes.TaskCreated,
		event.V1EventTypes.TaskStarted,
		event.V1EventTypes.MemoryContextRequested,
		event.V2EventTypes.ContextRequested,
		event.V1EventTypes.MemoryContextFailed,
		event.V2EventTypes.ContextFailed,
		event.V1EventTypes.TaskFailed,
	}

	assertContainsTypes(t, types, expected)

	// Verify NO agent.started or LLM events exist
	for _, evt := range events {
		if evt.Type == event.V1EventTypes.AgentStarted ||
			evt.Type == event.V2EventTypes.LLMRequested ||
			evt.Type == event.V2EventTypes.LLMCompleted {
			t.Errorf("strict failure should not have %s event", evt.Type)
		}
	}
}

// ============================================================================
// Mock Remembrance Server for V2 tests
// ============================================================================

// mockMemoryServer is a simple HTTP server that returns a fake context.
type mockMemoryServer struct {
	URL      string
	listener net.Listener
	server   *http.Server
}

func (m *mockMemoryServer) Close() {
	m.server.Close()
}

func startMockMemoryServer(t *testing.T) *mockMemoryServer {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/context/build", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := `{"project_id": "prizm", "agent_id": "lumi", "task": "test", "selected_memories": ["test-doc-1", "test-doc-2"], "context_markdown": "# Retrieved Remembrance Context\n\n## Task\ntest\n\n## Relevant Context\n- **Test context for unit test** \u2014 test snippet one\n- **Another test** \u2014 test snippet two\n", "context_json": {"project_id": "prizm", "agent_id": "lumi", "task": "test", "selected_memories": [{"memory_id": "test-doc-1", "title": "Test context for unit test", "summary": "test snippet one", "score": 0.95, "reason": "test"}, {"memory_id": "test-doc-2", "title": "Another test", "summary": "test snippet two", "score": 0.85, "reason": "test"}], "total_memories": 2}, "token_count": 150}`
		w.Write([]byte(resp))
	})
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "ok"}`))
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	server := &http.Server{Handler: mux}
	go server.Serve(listener)

	return &mockMemoryServer{
		URL:      fmt.Sprintf("http://%s", listener.Addr().String()),
		listener: listener,
		server:   server,
	}
}

// ============================================================================
// V3 Tests — Tool Execution
// ============================================================================

func TestV3ToolCallSummaryInSummary(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	// Set up tool registry and executor
	registry := tool.NewRegistry()
	tool.RegisterBuiltins(registry, tmpDir, 1024*1024)
	policyConfig := tool.PolicyConfig{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	executor := tool.NewExecutor(registry, &policyConfig)

	cfg := run.RunConfig{
		Task:          "Test V3 tool call",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.NewToolRequest("read_file", map[string]any{"path": "test.txt"}),
		ProviderName:  "mock",
		Model:         "mock-model",
		ToolExecutor:  executor,
		ToolPolicy:    policyConfig,
	}

	// Create a test file in the workspace
	os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("hello world"), 0644)

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V3 run failed: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", result.Status)
	}

	// Verify summary.json has tool_calls
	summaryPath := filepath.Join(tmpDir, "runs", result.RunID, "summary.json")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("failed to read summary.json: %v", err)
	}

	var summary event.Summary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("failed to parse summary.json: %v", err)
	}

	if len(summary.ToolCalls) == 0 {
		t.Error("expected at least 1 tool call in summary")
	} else {
		tc := summary.ToolCalls[0]
		if tc.ToolName != "read_file" {
			t.Errorf("expected tool_name 'read_file', got %s", tc.ToolName)
		}
		if tc.PolicyDecision != "approved" {
			t.Errorf("expected policy_decision 'approved', got %s", tc.PolicyDecision)
		}
		if tc.Status != "completed" {
			t.Errorf("expected status 'completed', got %s", tc.Status)
		}
	}
}

func TestV3ToolResultFileWritten(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	registry := tool.NewRegistry()
	tool.RegisterBuiltins(registry, tmpDir, 1024*1024)
	policyConfig := tool.PolicyConfig{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	executor := tool.NewExecutor(registry, &policyConfig)

	// Create test file
	os.WriteFile(filepath.Join(tmpDir, "hello.txt"), []byte("hello from prizm"), 0644)

	cfg := run.RunConfig{
		Task:          "Test V3 tool result file",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.NewToolRequest("read_file", map[string]any{"path": "hello.txt"}),
		ProviderName:  "mock",
		Model:         "mock-model",
		ToolExecutor:  executor,
		ToolPolicy:    policyConfig,
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V3 run failed: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", result.Status)
	}

	// Verify tool_result.json exists
	toolResultPath := filepath.Join(tmpDir, "runs", result.RunID, "tool_result.json")
	if _, err := os.Stat(toolResultPath); os.IsNotExist(err) {
		t.Error("tool_result.json should exist after tool execution")
	}

	toolData, err := os.ReadFile(toolResultPath)
	if err != nil {
		t.Fatalf("failed to read tool_result.json: %v", err)
	}

	var toolResult tool.ToolResult
	if err := json.Unmarshal(toolData, &toolResult); err != nil {
		t.Fatalf("failed to parse tool_result.json: %v", err)
	}
	if !toolResult.Success {
		t.Errorf("expected tool_result.Success=true, got false: %s", toolResult.Error)
	}
	if toolResult.Output["content"] != "hello from prizm" {
		t.Errorf("expected content 'hello from prizm', got %v", toolResult.Output["content"])
	}
}

func TestV3ToolEventsInEventLog(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	registry := tool.NewRegistry()
	tool.RegisterBuiltins(registry, tmpDir, 1024*1024)
	policyConfig := tool.PolicyConfig{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	executor := tool.NewExecutor(registry, &policyConfig)

	cfg := run.RunConfig{
		Task:          "Test V3 tool events",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.NewToolRequest("echo", map[string]any{"text": "hello"}),
		ProviderName:  "mock",
		Model:         "mock-model",
		ToolExecutor:  executor,
		ToolPolicy:    policyConfig,
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V3 run failed: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", result.Status)
	}

	// Verify events.jsonl includes tool events
	events := parseEventsFile(t, filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl"))

	// Find tool events
	toolEventTypes := map[string]bool{
		event.V3EventTypes.ToolRequested: false,
		event.V3EventTypes.ToolApproved:  false,
		event.V3EventTypes.ToolStarted:   false,
		event.V3EventTypes.ToolCompleted: false,
	}

	for _, evt := range events {
		if _, ok := toolEventTypes[evt.Type]; ok {
			toolEventTypes[evt.Type] = true
		}
	}

	for eventType, found := range toolEventTypes {
		if !found {
			t.Errorf("expected tool event %s in event log, not found", eventType)
		}
	}

	// Verify all events share the same correlation_id
	if len(events) > 0 {
		corrID := events[0].CorrelationID
		for _, evt := range events {
			if evt.CorrelationID != corrID {
				t.Errorf("event %s has different correlation_id: %s != %s", evt.Type, evt.CorrelationID, corrID)
			}
		}
	}
}

func TestV3ToolDeniedPolicy(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	registry := tool.NewRegistry()
	tool.RegisterBuiltins(registry, tmpDir, 1024*1024)
	policyConfig := tool.PolicyConfig{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	executor := tool.NewExecutor(registry, &policyConfig)

	// Request a tool that should be denied (nonexistent tool)
	// V60: Shell tool is now a valid tool that requires approval.
	// Use a truly unknown tool name to test the denied path.
	cfg := run.RunConfig{
		Task:          "Test V3 tool denied",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.NewToolRequest("nonexistent_tool", map[string]any{}),
		ProviderName:  "mock",
		Model:         "mock-model",
		ToolExecutor:  executor,
		ToolPolicy:    policyConfig,
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V3 run with denied tool should not error: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected status 'completed' (run completes, tool denied), got %s", result.Status)
	}

	// Verify events include tool.denied
	events := parseEventsFile(t, filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl"))

	foundDenied := false
	for _, evt := range events {
		if evt.Type == event.V3EventTypes.ToolDenied {
			foundDenied = true
			if evt.Payload["policy_decision"] != "denied" {
				t.Errorf("expected policy_decision 'denied', got %v", evt.Payload["policy_decision"])
			}
		}
	}
	if !foundDenied {
		t.Error("expected tool.denied event in event log")
	}

	// Verify summary has tool_call with denied status
	summaryPath := filepath.Join(tmpDir, "runs", result.RunID, "summary.json")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("failed to read summary.json: %v", err)
	}
	var summary event.Summary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("failed to parse summary.json: %v", err)
	}
	if len(summary.ToolCalls) == 0 {
		t.Fatal("expected at least 1 tool call in summary")
	}
	if summary.ToolCalls[0].PolicyDecision != "denied" {
		t.Errorf("expected policy_decision 'denied', got %s", summary.ToolCalls[0].PolicyDecision)
	}
}

func TestV3NoToolExecutor(t *testing.T) {
	// When ToolExecutor is nil, the run should complete normally (V2 behavior)
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:          "Test V3 no tool executor",
		Project:       "prizm",
		Agent:         "lumi",
		BusURL:        busURL,
		MemoryEnabled: false,
		RunDir:        filepath.Join(tmpDir, "runs"),
		Provider:      mockpkg.New(),
		ProviderName:  "mock",
		Model:         "mock-model",
		// ToolExecutor is nil — should behave like V2
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()

	if err != nil {
		t.Fatalf("V3 run without tool executor should not error: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", result.Status)
	}

	// Verify no tool events in the event log
	events := parseEventsFile(t, filepath.Join(tmpDir, "runs", result.RunID, "events.jsonl"))
	for _, evt := range events {
		if strings.HasPrefix(evt.Type, "prizm.tool.") {
			t.Errorf("expected no tool events when ToolExecutor is nil, found %s", evt.Type)
		}
	}

	// Verify no tool_result.json exists
	toolResultPath := filepath.Join(tmpDir, "runs", result.RunID, "tool_result.json")
	if _, err := os.Stat(toolResultPath); !os.IsNotExist(err) {
		t.Error("tool_result.json should NOT exist when no tool executor")
	}

	// Verify summary has no tool_calls
	summaryPath := filepath.Join(tmpDir, "runs", result.RunID, "summary.json")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("failed to read summary.json: %v", err)
	}
	var summary event.Summary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("failed to parse summary.json: %v", err)
	}
	if len(summary.ToolCalls) > 0 {
		t.Errorf("expected no tool_calls in summary, got %d", len(summary.ToolCalls))
	}
}

// ── V5: Validation and Review Runner Tests ──────────────────────────────────

func TestV5RunValidation(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:               "V5 validation test",
		Project:            "prizm",
		Agent:              "lumi",
		BusURL:             busURL,
		RunDir:             tmpDir,
		Provider:           mockpkg.New(),
		ProviderName:       "mock",
		Model:              "mock-model",
		ValidationRegistry: validation.NewRegistry(),
		Reviewer:           review.NewReviewer("lumi-deterministic"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	runDir := filepath.Join(tmpDir, result.RunID)

	// Run a fast validation profile
	valResult, err := runner.RunValidation(runDir, "echo_test")
	if err != nil {
		t.Fatalf("validation run failed: %v", err)
	}

	if valResult == nil {
		t.Fatal("expected validation result")
	}
	if valResult.Profile != "echo_test" {
		t.Errorf("expected profile echo_test, got %s", valResult.Profile)
	}

	// Check that validation artifacts were written
	valDir := filepath.Join(runDir, "validation")
	if _, err := os.Stat(filepath.Join(valDir, "echo_test.json")); err != nil {
		t.Errorf("expected echo_test.json artifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(valDir, "echo_test.stdout.txt")); err != nil {
		t.Errorf("expected stdout artifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(valDir, "echo_test.stderr.txt")); err != nil {
		t.Errorf("expected stderr artifact: %v", err)
	}
}

func TestV5RunReview(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:               "V5 review test",
		Project:            "prizm",
		Agent:              "lumi",
		BusURL:             busURL,
		RunDir:             tmpDir,
		Provider:           mockpkg.New(),
		ProviderName:       "mock",
		Model:              "mock-model",
		ValidationRegistry: validation.NewRegistry(),
		Reviewer:           review.NewReviewer("lumi-deterministic"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	runDir := filepath.Join(tmpDir, result.RunID)

	// First run validation
	valResult, err := runner.RunValidation(runDir, "echo_test")
	if err != nil {
		t.Fatalf("validation run failed: %v", err)
	}

	// Then run review
	reviewResult, artifactPath, err := runner.RunReview(
		runDir,
		"applied",
		[]string{"main.go"},
		[]validation.Result{*valResult},
	)
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}

	if reviewResult == nil {
		t.Fatal("expected review result")
	}
	if reviewResult.Recommendation != review.RecommendationApproved {
		t.Errorf("expected approved_for_human_review, got %s", reviewResult.Recommendation)
	}

	// Check review.md artifact
	if _, err := os.Stat(artifactPath); err != nil {
		t.Errorf("expected review.md artifact: %v", err)
	}

	// Check summary was updated with V5 metadata
	summaryPath := filepath.Join(runDir, "summary.json")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("failed to read summary: %v", err)
	}
	var summary event.Summary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("failed to parse summary: %v", err)
	}
	if len(summary.Validations) == 0 {
		t.Error("expected validations in summary")
	}
	if len(summary.Reviews) == 0 {
		t.Error("expected reviews in summary")
	}
}

func TestV5ValidationEvents(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:               "V5 events test",
		Project:            "prizm",
		Agent:              "lumi",
		BusURL:             busURL,
		RunDir:             tmpDir,
		Provider:           mockpkg.New(),
		ProviderName:       "mock",
		Model:              "mock-model",
		ValidationRegistry: validation.NewRegistry(),
		Reviewer:           review.NewReviewer("lumi-deterministic"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	runDir := filepath.Join(tmpDir, result.RunID)

	// Run validation — this should emit validation.* events
	valResult, err := runner.RunValidation(runDir, "echo_test")
	if err != nil {
		t.Fatalf("validation run failed: %v", err)
	}

	// Run review — this should emit review.* events
	reviewResult, _, err := runner.RunReview(
		runDir,
		"applied",
		[]string{"test.go"},
		[]validation.Result{*valResult},
	)
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}

	// Read the events.jsonl to verify events were recorded
	eventsPath := filepath.Join(runDir, "events.jsonl")
	eventsData, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("failed to read events: %v", err)
	}

	eventStr := string(eventsData)

	// Check for validation events
	validationEvents := []string{
		"prizm.validation.requested",
		"prizm.validation.started",
	}
	for _, evtType := range validationEvents {
		if !strings.Contains(eventStr, evtType) {
			t.Errorf("expected event %q in events.jsonl", evtType)
		}
	}

	// Check for review events
	reviewEvents := []string{
		"prizm.review.requested",
		"prizm.review.started",
		"prizm.review.completed",
	}
	for _, evtType := range reviewEvents {
		if !strings.Contains(eventStr, evtType) {
			t.Errorf("expected event %q in events.jsonl", evtType)
		}
	}

	// Verify the validation completed or failed event exists
	hasCompletion := strings.Contains(eventStr, "prizm.validation.completed") ||
		strings.Contains(eventStr, "prizm.validation.failed")
	if !hasCompletion {
		t.Error("expected validation completed or failed event")
	}

	_ = reviewResult
}

func TestV5ReviewWithFailedValidation(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:               "V5 failed validation review test",
		Project:            "prizm",
		Agent:              "lumi",
		BusURL:             busURL,
		RunDir:             tmpDir,
		Provider:           mockpkg.New(),
		ProviderName:       "mock",
		Model:              "mock-model",
		ValidationRegistry: validation.NewRegistry(),
		Reviewer:           review.NewReviewer("lumi-deterministic"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	runDir := filepath.Join(tmpDir, result.RunID)

	// Simulate a failed validation (without actually running a profile)
	failedResult := validation.Result{
		Profile:    "echo_test",
		Status:     "failed",
		ExitCode:   1,
		DurationMs: 500,
	}

	// Run review with failed validation
	reviewResult, _, err := runner.RunReview(
		runDir,
		"applied",
		[]string{"broken.go"},
		[]validation.Result{failedResult},
	)
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}

	if reviewResult.Recommendation != review.RecommendationValidationFailed {
		t.Errorf("expected validation_failed, got %s", reviewResult.Recommendation)
	}
}

func TestV5ApprovalApproveWithValidate(t *testing.T) {
	// Test the full V5 approve+validate+review pipeline by exercising each component.
	// This tests that ApproveWithValidation correctly chains: approve → apply → validate → review.
	tmpDir := t.TempDir()
	workspaceDir := t.TempDir()

	// Create the approval structure manually
	runID := event.NewRunID()
	runDir := filepath.Join(tmpDir, runID)
	approvalDir := filepath.Join(runDir, "approvals")
	if err := os.MkdirAll(approvalDir, 0755); err != nil {
		t.Fatalf("failed to create approval dir: %v", err)
	}

	approvalID := "appr_" + event.NewID()
	targetPath := "test_output.txt"

	approvalData, _ := json.Marshal(map[string]any{
		"approval_id":    approvalID,
		"run_id":         runID,
		"correlation_id": event.NewCorrelationID(),
		"status":         "pending",
		"requested_by":   "lumi",
		"project":        "prizm",
		"mutation_type":  "write_file",
		"target_path":    targetPath,
		"content":        "Hello V5!",
		"preview":        "Hello V5!",
		"created_at":     time.Now().UTC().Format(time.RFC3339Nano),
		"policy": map[string]any{
			"decision": "requires_approval",
			"reason":   "file writes require explicit approval",
		},
	})
	os.WriteFile(filepath.Join(approvalDir, approvalID+".json"), append(approvalData, '\n'), 0644)

	// Create summary.json
	summaryData, _ := json.Marshal(map[string]any{
		"run_id":  runID,
		"status":  "pending_approval",
		"project": "prizm",
		"agent":   "lumi",
	})
	os.WriteFile(filepath.Join(runDir, "summary.json"), append(summaryData, '\n'), 0644)

	// Use a test registry with a portable smoke profile.
	testRegistry := validation.NewEmptyRegistry()
	testRegistry.Register(validation.Profile{
		Name:             "echo_test",
		Description:      "Quick Go toolchain smoke test",
		Command:          "go",
		Args:             []string{"version"},
		WorkingDir:       ".",
		TimeoutSeconds:   5,
		AllowedExitCodes: []int{0},
	})

	// Test individual components without the full runner pipeline
	// (which requires NATS). The full integration is tested by the CLI.

	// 1. Test mutation executor (approve + apply)
	store := approval.NewStore(tmpDir)
	mutExec := mutation.NewExecutor(workspaceDir, store)

	mutResult, err := mutExec.ApplyWithRun(context.Background(), runID, approvalID, "ema")
	if err != nil {
		t.Fatalf("mutation apply failed: %v", err)
	}
	if mutResult == nil {
		t.Fatal("expected mutation result, got nil")
	}
	if !mutResult.Success {
		t.Fatalf("expected mutation to succeed, got: success=%v message=%q", mutResult.Success, mutResult.Message)
	}

	// Verify file was written
	writtenPath := filepath.Join(workspaceDir, "test_output.txt")
	data, err := os.ReadFile(writtenPath)
	if err != nil {
		t.Fatalf("expected file to be written: %v", err)
	}
	if strings.TrimSpace(string(data)) != "Hello V5!" {
		t.Errorf("expected 'Hello V5!', got %q", strings.TrimSpace(string(data)))
	}

	// 2. Test validation executor
	valExec := validation.NewExecutor(testRegistry, ".", runDir)
	valResult, err := valExec.Run(context.Background(), "echo_test", "test-corr-id")
	if err != nil {
		t.Fatalf("validation run failed: %v", err)
	}
	if valResult.Status != "passed" {
		t.Errorf("expected validation status 'passed', got %q", valResult.Status)
	}

	// Verify validation artifacts
	if _, err := os.Stat(filepath.Join(runDir, "validation", "echo_test.json")); err != nil {
		t.Errorf("expected validation result artifact: %v", err)
	}

	// 3. Test review generation
	reviewer := review.NewReviewer("lumi-deterministic")
	reviewResult, err := reviewer.Generate(
		runID,
		"test-corr-id",
		"applied",
		[]string{targetPath},
		[]review.ValidationInfo{{
			Profile:    valResult.Profile,
			Status:     valResult.Status,
			ExitCode:   valResult.ExitCode,
			DurationMs: valResult.DurationMs,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("review generation failed: %v", err)
	}
	if reviewResult.Recommendation != review.RecommendationApproved {
		t.Errorf("expected approved_for_human_review, got %s", reviewResult.Recommendation)
	}

	// Write review artifact
	artifactPath, err := review.WriteReviewArtifact(runDir, reviewResult)
	if err != nil {
		t.Fatalf("failed to write review artifact: %v", err)
	}
	if _, err := os.Stat(artifactPath); err != nil {
		t.Errorf("expected review.md artifact: %v", err)
	}
}

func TestV5ValidationUnknownProfile(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:               "V5 unknown profile test",
		Project:            "prizm",
		Agent:              "lumi",
		BusURL:             busURL,
		RunDir:             tmpDir,
		Provider:           mockpkg.New(),
		ProviderName:       "mock",
		Model:              "mock-model",
		ValidationRegistry: validation.NewRegistry(),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	runDir := filepath.Join(tmpDir, result.RunID)

	_, err = runner.RunValidation(runDir, "nonexistent_profile")
	if err == nil {
		t.Error("expected error for unknown profile")
	}
}

func TestV5ValidationTimeoutRequirement(t *testing.T) {
	// Verify all built-in profiles have timeouts
	registry := validation.NewRegistry()
	for _, name := range registry.List() {
		p, err := registry.Resolve(name)
		if err != nil {
			t.Fatalf("failed to resolve %s: %v", name, err)
		}
		if p.TimeoutSeconds <= 0 {
			t.Errorf("profile %s has no timeout (timeout_seconds=%d)", name, p.TimeoutSeconds)
		}
	}
}

func TestV5ReviewSummaryMetadata(t *testing.T) {
	s, busURL := startTestServer(t)
	defer s.Shutdown()

	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:               "V5 summary metadata test",
		Project:            "prizm",
		Agent:              "lumi",
		BusURL:             busURL,
		RunDir:             tmpDir,
		Provider:           mockpkg.New(),
		ProviderName:       "mock",
		Model:              "mock-model",
		ValidationRegistry: validation.NewRegistry(),
		Reviewer:           review.NewReviewer("lumi-deterministic"),
	}

	runner := run.NewRunner(cfg)
	result, err := runner.Run()
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	runDir := filepath.Join(tmpDir, result.RunID)

	// Run validation and review
	valResult, _ := runner.RunValidation(runDir, "echo_test")
	_, _, err = runner.RunReview(runDir, "applied", []string{"test.go"}, []validation.Result{*valResult})
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}

	// Read summary and verify V5 metadata
	summaryPath := filepath.Join(runDir, "summary.json")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("failed to read summary: %v", err)
	}
	var summary event.Summary
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		t.Fatalf("failed to parse summary: %v", err)
	}

	// Verify validations array
	if len(summary.Validations) != 1 {
		t.Errorf("expected 1 validation, got %d", len(summary.Validations))
	} else {
		v := summary.Validations[0]
		if v.Profile != "echo_test" {
			t.Errorf("expected profile echo_test, got %s", v.Profile)
		}
		if v.Status != "passed" {
			t.Errorf("expected status passed, got %s", v.Status)
		}
		if v.ResultPath == "" {
			t.Error("expected result_path")
		}
	}

	// Verify reviews array
	if len(summary.Reviews) != 1 {
		t.Errorf("expected 1 review, got %d", len(summary.Reviews))
	} else {
		rv := summary.Reviews[0]
		if rv.Reviewer != "lumi-deterministic" {
			t.Errorf("expected reviewer lumi-deterministic, got %s", rv.Reviewer)
		}
		if rv.Status != "completed" {
			t.Errorf("expected status completed, got %s", rv.Status)
		}
	}
}

func TestV5ReviewCannotApproveOrApply(t *testing.T) {
	// At compile time, the Reviewer struct has no Approve() or Apply() methods.
	// This test verifies that a review does not trigger side effects.
	r := review.NewReviewer("test-reviewer")

	var emitted []map[string]any
	r.SetEmitter(func(eventType, source string, payload map[string]any) {
		emitted = append(emitted, payload)
	})

	reviewResult, err := r.Generate("run_1", "corr_1", "pending", nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The review result should NOT say the mutation was applied
	if reviewResult.MutationStatus == "applied" {
		t.Error("reviewer should not change mutation status to 'applied'")
	}

	// Verify no approval or mutation events were emitted
	for _, p := range emitted {
		if et, ok := p["event_type"].(string); ok {
			if strings.Contains(et, "approval") || strings.Contains(et, "mutation") {
				t.Errorf("reviewer emitted approval/mutation event: %s", et)
			}
		}
	}
}

func TestV5PublishEventWithoutNATS(t *testing.T) {
	// Verify that publishEvent does not panic when r.js is nil.
	// This is critical for CLI commands like ApproveWithValidation that
	// create a Runner without calling Run() (which sets up NATS).
	tmpDir := t.TempDir()

	cfg := run.RunConfig{
		Task:         "V5 nil NATS test",
		Project:      "prizm",
		Agent:        "lumi",
		BusURL:       "",
		RunDir:       tmpDir,
		Provider:     mockpkg.New(),
		ProviderName: "mock",
		Model:        "mock-model",
	}

	runner := run.NewRunner(cfg)

	// publishEvent should not panic even though js is nil
	evt := event.Event{
		ID:        event.NewID(),
		Type:      "prizm.validation.completed",
		Source:    "prizm-test",
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}

	// This must not panic
	runner.PublishEvent(evt)
}
