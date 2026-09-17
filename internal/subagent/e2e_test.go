package subagent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/bus"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
	"github.com/nats-io/nats.go"
)

// TestE2E_DelegationOverNATS exercises the full autonomous path over a real
// embedded NATS server: a task packet published to the delegation subject is
// consumed by the worker, run through the LoopRunner, and its completion is
// published back on the completion subject. This is the smoke test for the
// complete delegate → execute → report loop (mock backend keeps it
// deterministic; no live model).
func TestE2E_DelegationOverNATS(t *testing.T) {
	const (
		delegationSubject = "prizm.agent.openclaw"
		completionSubject = "prizm.workflow.task.complete"
	)

	url, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("start embedded bus: %v", err)
	}
	defer cleanup()

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	// Worker: resolves one agent, runs a scripted final-answer turn.
	backend := &scriptBackend{parse: lineParser, turns: []Turn{{Text: "FINAL: task done"}}}
	worker := NewWorker(
		mapResolver{"scout": {AgentID: "scout", Capabilities: []string{"search", "report"}}},
		NewLoopRunner(LoopRunnerConfig{Backend: backend, Scope: DefaultToolScope()}),
		0,
	)
	pub := &natsCompletionPublisher{nc: nc, subject: completionSubject}

	// Consumer: mirror the serve subscription (filter task packets, run, publish).
	_, err = nc.Subscribe(delegationSubject, func(msg *nats.Msg) {
		var p v2.TaskPacket
		if json.Unmarshal(msg.Data, &p) != nil || p.Type != "task_delegation" {
			return
		}
		go worker.HandleAndPublish(context.Background(), p, pub)
	})
	if err != nil {
		t.Fatalf("subscribe delegation: %v", err)
	}

	// Capture completions.
	completions := make(chan v2.TaskCompletion, 1)
	_, err = nc.Subscribe(completionSubject, func(msg *nats.Msg) {
		var c v2.TaskCompletion
		if json.Unmarshal(msg.Data, &c) == nil {
			completions <- c
		}
	})
	if err != nil {
		t.Fatalf("subscribe completion: %v", err)
	}

	// Publish a delegation packet.
	packet := v2.TaskPacket{Type: "task_delegation", TargetAgent: "scout", TaskID: "E2E-1", Description: "find refs"}
	data, _ := json.Marshal(packet)
	if err := nc.Publish(delegationSubject, data); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case c := <-completions:
		if c.TaskID != "E2E-1" {
			t.Errorf("completion task id = %q", c.TaskID)
		}
		if c.Status != "completed" {
			t.Fatalf("status = %q, want completed", c.Status)
		}
		if c.OutputSummary != "task done" {
			t.Errorf("summary = %q", c.OutputSummary)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for completion over NATS")
	}
}

// TestE2E_UnknownAgentReportsFailure proves a mis-routed packet still gets a
// definitive completion over the wire (fail-closed end to end).
func TestE2E_UnknownAgentReportsFailure(t *testing.T) {
	url, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatalf("start embedded bus: %v", err)
	}
	defer cleanup()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	worker := NewWorker(mapResolver{}, NewLoopRunner(LoopRunnerConfig{
		Backend: &scriptBackend{parse: lineParser, turns: []Turn{{Text: "FINAL: x"}}},
	}), 0)
	pub := &natsCompletionPublisher{nc: nc, subject: "c"}

	nc.Subscribe("d", func(msg *nats.Msg) {
		var p v2.TaskPacket
		if json.Unmarshal(msg.Data, &p) == nil {
			go worker.HandleAndPublish(context.Background(), p, pub)
		}
	})
	got := make(chan v2.TaskCompletion, 1)
	nc.Subscribe("c", func(msg *nats.Msg) {
		var c v2.TaskCompletion
		if json.Unmarshal(msg.Data, &c) == nil {
			got <- c
		}
	})

	data, _ := json.Marshal(v2.TaskPacket{Type: "task_delegation", TargetAgent: "ghost", TaskID: "E2E-2"})
	nc.Publish("d", data)

	select {
	case c := <-got:
		if c.Status != "failed" {
			t.Fatalf("status = %q, want failed", c.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

type natsCompletionPublisher struct {
	nc      *nats.Conn
	subject string
}

func (p *natsCompletionPublisher) PublishCompletion(c v2.TaskCompletion) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return p.nc.Publish(p.subject, data)
}
