package crossprizm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/task"
	"github.com/nats-io/nats.go"
)

func TestMessageSignVerify(t *testing.T) {
	msg := NewMessage("lumi-ceo", "astraea-manager", TypeTaskRequest)
	msg.CorrelationID = "corr-1"
	msg.Request = map[string]any{"task": "review factory status"}

	if err := msg.Sign("secret"); err != nil {
		t.Fatalf("sign failed: %v", err)
	}
	if msg.Signature == "" {
		t.Fatal("expected signature")
	}
	if err := msg.Verify("secret", 5*time.Minute); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

func TestCoordinatorAcceptsGenericTask(t *testing.T) {
	store, err := task.NewStore(t.TempDir() + "/tasks.db")
	if err != nil {
		t.Fatalf("task store: %v", err)
	}
	defer store.Close()

	coord := NewCoordinator(CoordinatorConfig{
		InstanceID:             "astraea-manager",
		Store:                  store,
		ConfidenceThreshold:    0.75,
		MaxClarificationRounds: 1,
		TargetProfiles: []TargetProfile{
			{Name: "generic", InstanceID: "astraea-manager", Adapter: "generic"},
		},
	})

	req := NewMessage("lumi-ceo", "astraea-manager", TypeTaskRequest)
	req.CorrelationID = "corr-generic"
	req.Thread = map[string]any{"leader": "lumi-ceo", "turn": 0}
	req.Request = map[string]any{
		"task":            "review project status and report blockers",
		"target_profile":  "generic",
		"leader_instance": "lumi-ceo",
	}

	resp, err := coord.Handle(context.Background(), SubjectTaskRequest, req)
	if err != nil {
		t.Fatalf("handle task request: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.MessageType != TypeTaskAccept {
		t.Fatalf("message type = %q, want %q", resp.MessageType, TypeTaskAccept)
	}
	taskID, _ := resp.Response["task_id"].(string)
	if taskID == "" {
		t.Fatal("expected task_id in response")
	}
	got, err := store.Get(taskID)
	if err != nil {
		t.Fatalf("stored task: %v", err)
	}
	if got.DelegatedBy != "lumi-ceo" || got.DelegatedTo != "astraea-manager" {
		t.Fatalf("delegation = %s -> %s", got.DelegatedBy, got.DelegatedTo)
	}
}

func TestCoordinatorClarifiesLowConfidenceTask(t *testing.T) {
	coord := NewCoordinator(CoordinatorConfig{
		InstanceID:             "astraea-manager",
		ConfidenceThreshold:    0.75,
		MaxClarificationRounds: 1,
	})

	req := NewMessage("lumi-ceo", "astraea-manager", TypeTaskRequest)
	req.CorrelationID = "corr-clarify"
	req.Thread = map[string]any{"leader": "lumi-ceo", "turn": 0}
	req.Request = map[string]any{"task": "check it"}

	resp, err := coord.Handle(context.Background(), SubjectTaskRequest, req)
	if err != nil {
		t.Fatalf("handle task request: %v", err)
	}
	if resp == nil {
		t.Fatal("expected clarification response")
	}
	if resp.MessageType != TypeClarificationReq {
		t.Fatalf("message type = %q, want %q", resp.MessageType, TypeClarificationReq)
	}
	if resp.Response["needs_human_input"] != true {
		t.Fatalf("expected needs_human_input=true, got %v", resp.Response["needs_human_input"])
	}
}

type fakeTaskAdapter struct {
	called bool
	resp   *Message
}

func (f *fakeTaskAdapter) HandleCrossPrizmTask(ctx context.Context, msg Message) (*Message, error) {
	f.called = true
	return f.resp, nil
}

func TestCoordinatorRoutesCodexProfileToAdapter(t *testing.T) {
	adapter := &fakeTaskAdapter{resp: &Message{
		MessageType: TypeTaskResult,
		Response: map[string]any{
			"status":  "completed",
			"task_id": "codex-task",
			"summary": "done",
		},
	}}
	coord := NewCoordinator(CoordinatorConfig{
		InstanceID:             "astraea-manager",
		CodexAdapter:           adapter,
		ConfidenceThreshold:    0.75,
		MaxClarificationRounds: 1,
		TargetProfiles: []TargetProfile{
			{Name: "codex", InstanceID: "astraea-manager", Adapter: "codex"},
		},
	})

	req := NewMessage("lumi-ceo", "astraea-manager", TypeTaskRequest)
	req.CorrelationID = "corr-codex"
	req.Thread = map[string]any{"leader": "lumi-ceo", "turn": 0}
	req.Request = map[string]any{
		"task":            "implement the feature",
		"target_profile":  "codex",
		"leader_instance": "lumi-ceo",
	}

	resp, err := coord.Handle(context.Background(), SubjectTaskRequest, req)
	if err != nil {
		t.Fatalf("handle task request: %v", err)
	}
	if !adapter.called {
		t.Fatal("expected adapter to be called")
	}
	if resp.MessageType != TypeTaskResult {
		t.Fatalf("message type = %q", resp.MessageType)
	}
	if resp.Response["target_profile"] != "codex" {
		t.Fatalf("target_profile = %v", resp.Response["target_profile"])
	}
}

func TestMessageVerifyRejectsTamper(t *testing.T) {
	msg := NewMessage("lumi-ceo", "astraea-manager", TypeTaskRequest)
	msg.Request = map[string]any{"task": "original"}
	if err := msg.Sign("secret"); err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	msg.Request["task"] = "changed"
	if err := msg.Verify("secret", 5*time.Minute); err == nil {
		t.Fatal("expected tampered message to fail verification")
	}
}

func TestMessageVerifyRejectsOldTimestamp(t *testing.T) {
	msg := NewMessage("lumi-ceo", "astraea-manager", TypeTaskRequest)
	msg.Timestamp = time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
	if err := msg.Sign("secret"); err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	if err := msg.Verify("secret", time.Minute); err == nil {
		t.Fatal("expected stale message to fail verification")
	}
}

func TestServiceTaskRequestRoundTrip(t *testing.T) {
	url, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("start bus: %v", err)
	}
	defer cleanup()

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect bus: %v", err)
	}
	defer nc.Close()

	received := make(chan Message, 1)
	if _, err := nc.Subscribe(SubjectTaskResponse, func(m *nats.Msg) {
		msg, err := Decode(m.Data)
		if err != nil {
			t.Errorf("decode response: %v", err)
			return
		}
		received <- msg
	}); err != nil {
		t.Fatalf("subscribe response: %v", err)
	}

	svc, err := NewService(nc, ServiceConfig{
		InstanceID:      "astraea-manager",
		Secret:          "secret",
		AllowedSubjects: []string{SubjectTaskRequest},
	}, func(ctx context.Context, subject string, msg Message) (*Message, error) {
		return &Message{
			MessageType: TypeTaskResponse,
			Response: map[string]any{
				"status": "accepted",
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start service: %v", err)
	}

	req := NewMessage("lumi-ceo", "astraea-manager", TypeTaskRequest)
	req.CorrelationID = "corr-1"
	req.Request = map[string]any{"task": "factory smoke"}
	if err := req.Sign("secret"); err != nil {
		t.Fatalf("sign request: %v", err)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := nc.Publish(SubjectTaskRequest, payload); err != nil {
		t.Fatalf("publish request: %v", err)
	}
	nc.Flush()

	select {
	case resp := <-received:
		if err := resp.Verify("secret", time.Minute); err != nil {
			t.Fatalf("verify response: %v", err)
		}
		if resp.To != "lumi-ceo" {
			t.Fatalf("response To = %q, want lumi-ceo", resp.To)
		}
		if resp.Response["status"] != "accepted" {
			t.Fatalf("response status = %v", resp.Response["status"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}
