package v2

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/bus"
	prizmevent "github.com/emaharmony/prizm/internal/event"
	"github.com/nats-io/nats.go"
)

// TestTaskPacketMarshaling verifies that TaskPacket serializes correctly
// for NATS publishing. This ensures the JSON structure matches what
// agents expect when receiving task delegation packets.
func TestTaskPacketMarshaling(t *testing.T) {
	packet := TaskPacket{
		Type:        "task_delegation",
		TargetAgent: "mango",
		TaskID:      "T-001",
		Description: "Implement the NATS delegation wiring for Phase 3",
		Context: TaskContext{
			Files:       []string{"delegation.go", "engine.go"},
			Decisions:   []string{"Use nats.go library", "Follow existing patterns"},
			Constraints: []string{"Must use existing NATS connection if available"},
		},
		ExpectedDeliverable: "nats_publisher.go with NATSPublisher and NATSListener",
		ValidationChecklist: []string{
			"PublishTaskPacket works",
			"PublishFeedbackRequest works",
			"PublishReviewRequest works",
			"PublishEvent works",
			"Listen subscribes to all 3 subjects",
		},
		Priority: "high",
		Deadline: "2026-06-24T00:00:00Z",
	}

	data, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("failed to marshal TaskPacket: %v", err)
	}

	// Verify it can be unmarshaled back
	var decoded TaskPacket
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal TaskPacket: %v", err)
	}

	// Verify key fields survived round-trip
	if decoded.Type != packet.Type {
		t.Errorf("Type mismatch: got %q, want %q", decoded.Type, packet.Type)
	}
	if decoded.TargetAgent != packet.TargetAgent {
		t.Errorf("TargetAgent mismatch: got %q, want %q", decoded.TargetAgent, packet.TargetAgent)
	}
	if decoded.TaskID != packet.TaskID {
		t.Errorf("TaskID mismatch: got %q, want %q", decoded.TaskID, packet.TaskID)
	}
	if decoded.Description != packet.Description {
		t.Errorf("Description mismatch: got %q, want %q", decoded.Description, packet.Description)
	}
	if decoded.Priority != packet.Priority {
		t.Errorf("Priority mismatch: got %q, want %q", decoded.Priority, packet.Priority)
	}
	if decoded.Deadline != packet.Deadline {
		t.Errorf("Deadline mismatch: got %q, want %q", decoded.Deadline, packet.Deadline)
	}
	if len(decoded.Context.Files) != 2 {
		t.Errorf("Context.Files length mismatch: got %d, want 2", len(decoded.Context.Files))
	}
	if len(decoded.ValidationChecklist) != 5 {
		t.Errorf("ValidationChecklist length mismatch: got %d, want 5", len(decoded.ValidationChecklist))
	}
}

func TestDurableTaskPacketPreservesLegacyWireShape(t *testing.T) {
	url, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	msgCh := make(chan *nats.Msg, 1)
	sub, err := nc.ChanSubscribe("tasks", msgCh)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()
	nc.Flush()
	box, err := prizmevent.NewSQLiteOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	pub := NewNATSPublisherFromConn(nc)
	pub.UseOutbox(box)
	packet := TaskPacket{Type: "task_delegation", TargetAgent: "coder", TaskID: "T1", RunID: "run-1", CorrelationID: "corr-1", DelegationID: "del-1", DeliveryKey: "del-1:0", Deadline: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
	if err := pub.PublishTaskPacket("tasks", packet); err != nil {
		t.Fatal(err)
	}
	if err := pub.PublishTaskPacket("tasks", packet); err != nil {
		t.Fatalf("duplicate publish: %v", err)
	}
	select {
	case msg := <-msgCh:
		var got TaskPacket
		if err := json.Unmarshal(msg.Data, &got); err != nil {
			t.Fatal(err)
		}
		if got.DeliveryKey != packet.DeliveryKey || got.TaskID != packet.TaskID {
			t.Fatalf("wire packet=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no task packet")
	}
	select {
	case msg := <-msgCh:
		t.Fatalf("duplicate wire delivery: %s", string(msg.Data))
	case <-time.After(100 * time.Millisecond):
	}
	report, err := box.Report(t.Context(), packet.RunID)
	if err != nil || len(report) != 1 || report[0].State != prizmevent.DeliveryDelivered {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

// TestTaskCompletionMarshaling verifies that TaskCompletion serializes correctly.
func TestTaskCompletionMarshaling(t *testing.T) {
	completion := TaskCompletion{
		TaskID:        "T-001",
		Status:        "completed",
		OutputSummary: "Successfully implemented NATS delegation wiring",
		Artifacts: CompletionArtifacts{
			FilePaths:    []string{"nats_publisher.go", "nats_publisher_test.go"},
			CommitHashes: []string{"abc123def"},
			PRURLs:       []string{"https://github.com/example/pr/42"},
		},
		ReviewNotes: "Looks good, ready for merge",
	}

	data, err := json.Marshal(completion)
	if err != nil {
		t.Fatalf("failed to marshal TaskCompletion: %v", err)
	}

	var decoded TaskCompletion
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal TaskCompletion: %v", err)
	}

	if decoded.TaskID != completion.TaskID {
		t.Errorf("TaskID mismatch: got %q, want %q", decoded.TaskID, completion.TaskID)
	}
	if decoded.Status != completion.Status {
		t.Errorf("Status mismatch: got %q, want %q", decoded.Status, completion.Status)
	}
	if decoded.OutputSummary != completion.OutputSummary {
		t.Errorf("OutputSummary mismatch: got %q, want %q", decoded.OutputSummary, completion.OutputSummary)
	}
	if len(decoded.Artifacts.FilePaths) != 2 {
		t.Errorf("Artifacts.FilePaths length mismatch: got %d, want 2", len(decoded.Artifacts.FilePaths))
	}
	if len(decoded.Artifacts.CommitHashes) != 1 {
		t.Errorf("Artifacts.CommitHashes length mismatch: got %d, want 1", len(decoded.Artifacts.CommitHashes))
	}
	if len(decoded.Artifacts.PRURLs) != 1 {
		t.Errorf("Artifacts.PRURLs length mismatch: got %d, want 1", len(decoded.Artifacts.PRURLs))
	}
}

// TestFeedbackRequestMarshaling verifies the feedback request payload structure.
func TestFeedbackRequestMarshaling(t *testing.T) {
	payload := map[string]any{
		"type":        "feedback_request",
		"workflow_id": "ng-12345",
		"phase":       "FEEDBACK_PRE",
		"plan":        `{"tasks":[{"id":"T-001","agent":"mango","description":"Implement NATS wiring"}]}`,
		"timestamp":   "1234567890",
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal feedback request: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal feedback request: %v", err)
	}

	if decoded["type"] != "feedback_request" {
		t.Errorf("type mismatch: got %v, want feedback_request", decoded["type"])
	}
	if decoded["workflow_id"] != "ng-12345" {
		t.Errorf("workflow_id mismatch: got %v, want ng-12345", decoded["workflow_id"])
	}
	if decoded["phase"] != "FEEDBACK_PRE" {
		t.Errorf("phase mismatch: got %v, want FEEDBACK_PRE", decoded["phase"])
	}
}

// TestReviewRequestMarshaling verifies the review request payload structure.
func TestReviewRequestMarshaling(t *testing.T) {
	payload := map[string]any{
		"type":               "review_request",
		"workflow_id":        "ng-12345",
		"phase":              "FEEDBACK_POST",
		"review_package":     "diff: nats_publisher.go (+200 lines)",
		"required_reviewers": []string{"lumi", "mango"},
		"timestamp":          "1234567890",
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal review request: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal review request: %v", err)
	}

	if decoded["type"] != "review_request" {
		t.Errorf("type mismatch: got %v, want review_request", decoded["type"])
	}

	reviewers, ok := decoded["required_reviewers"].([]any)
	if !ok {
		t.Fatalf("required_reviewers is not an array: %T", decoded["required_reviewers"])
	}
	if len(reviewers) != 2 {
		t.Errorf("required_reviewers length mismatch: got %d, want 2", len(reviewers))
	}
}

// TestExternalEventMarshaling verifies that ExternalEvent serializes correctly.
// This is important because NATSListener converts NATS messages into ExternalEvent
// structs that the engine consumes.
func TestExternalEventMarshaling(t *testing.T) {
	evt := ExternalEvent{
		Type:          "approval",
		CorrelationID: "ng-12345",
		Source:        "nats",
		Data: map[string]any{
			"decision": "approved",
			"notes":    "Plan looks solid, proceed",
		},
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("failed to marshal ExternalEvent: %v", err)
	}

	var decoded ExternalEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal ExternalEvent: %v", err)
	}

	if decoded.Type != evt.Type {
		t.Errorf("Type mismatch: got %q, want %q", decoded.Type, evt.Type)
	}
	if decoded.CorrelationID != evt.CorrelationID {
		t.Errorf("CorrelationID mismatch: got %q, want %q", decoded.CorrelationID, evt.CorrelationID)
	}
	if decoded.Source != evt.Source {
		t.Errorf("Source mismatch: got %q, want %q", decoded.Source, evt.Source)
	}
	if decoded.Data["decision"] != "approved" {
		t.Errorf("Data.decision mismatch: got %v, want approved", decoded.Data["decision"])
	}
}

// TestEventPayloadMarshaling verifies the generic event payload structure
// used by PublishEvent.
func TestEventPayloadMarshaling(t *testing.T) {
	event := map[string]any{
		"type": "workflow.phase.entered",
		"payload": map[string]any{
			"phase":    "PROBE",
			"workflow": "bassbook-chapter-1",
			"run_id":   "ng-12345",
		},
		"timestamp": "1234567890",
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal event: %v", err)
	}

	if decoded["type"] != "workflow.phase.entered" {
		t.Errorf("type mismatch: got %v", decoded["type"])
	}

	payload, ok := decoded["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload is not a map: %T", decoded["payload"])
	}
	if payload["phase"] != "PROBE" {
		t.Errorf("payload.phase mismatch: got %v, want PROBE", payload["phase"])
	}
}

// TestNewNATSPublisherFromConn verifies the constructor works with a nil conn
// (graceful handling — the methods check for nil before publishing).
func TestNewNATSPublisherFromConn(t *testing.T) {
	pub := NewNATSPublisherFromConn(nil)
	if pub == nil {
		t.Fatal("NewNATSPublisherFromConn returned nil")
	}
	if pub.conn != nil {
		t.Error("expected nil conn")
	}

	// Verify methods return errors gracefully with nil conn
	err := pub.PublishTaskPacket("test.subject", TaskPacket{TaskID: "T-001"})
	if err == nil {
		t.Error("expected error from PublishTaskPacket with nil conn")
	}

	err = pub.PublishFeedbackRequest("test.subject", "wf-1", "{}")
	if err == nil {
		t.Error("expected error from PublishFeedbackRequest with nil conn")
	}

	err = pub.PublishReviewRequest("test.subject", "wf-1", "diff")
	if err == nil {
		t.Error("expected error from PublishReviewRequest with nil conn")
	}

	err = pub.PublishEvent("test.subject", "test.event", nil)
	if err == nil {
		t.Error("expected error from PublishEvent with nil conn")
	}
}

// TestNewNATSListenerFromConn verifies the constructor works with a nil conn.
func TestNewNATSListenerFromConn(t *testing.T) {
	listener := NewNATSListenerFromConn(nil)
	if listener == nil {
		t.Fatal("NewNATSListenerFromConn returned nil")
	}
	if listener.conn != nil {
		t.Error("expected nil conn")
	}

	// Verify Listen returns error with nil conn
	err := listener.Listen(nil)
	if err == nil {
		t.Error("expected error from Listen with nil conn")
	}
}
