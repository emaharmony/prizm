package subagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

// --- mocks -------------------------------------------------------------------

type mapResolver map[string]AgentRuntime

func (m mapResolver) Resolve(id string) (AgentRuntime, bool) {
	rt, ok := m[id]
	return rt, ok
}

type funcRunner func(ctx context.Context, p v2.TaskPacket, rt AgentRuntime) (RunResult, error)

func (f funcRunner) Run(ctx context.Context, p v2.TaskPacket, rt AgentRuntime) (RunResult, error) {
	return f(ctx, p, rt)
}

type capturePublisher struct{ last *v2.TaskCompletion }

func (c *capturePublisher) PublishCompletion(comp v2.TaskCompletion) error {
	c.last = &comp
	return nil
}

func packet(agent, taskID string) v2.TaskPacket {
	return v2.TaskPacket{Type: "task_delegation", TargetAgent: agent, TaskID: taskID, Description: "do the thing"}
}

// --- tests -------------------------------------------------------------------

func TestHandle_Success(t *testing.T) {
	res := mapResolver{"scout": {AgentID: "scout", Provider: "ollama", Model: "qwen3.5:9b"}}
	runner := funcRunner(func(_ context.Context, p v2.TaskPacket, rt AgentRuntime) (RunResult, error) {
		if rt.AgentID != "scout" {
			t.Errorf("runner got wrong runtime: %+v", rt)
		}
		return RunResult{Summary: "found 5 references", Artifacts: v2.CompletionArtifacts{FilePaths: []string{"references/goblin.png"}}}, nil
	})
	w := NewWorker(res, runner, 0)

	c := w.Handle(context.Background(), packet("scout", "T1"))
	if c.Status != "completed" {
		t.Fatalf("status = %q, want completed", c.Status)
	}
	if c.OutputSummary != "found 5 references" {
		t.Errorf("summary = %q", c.OutputSummary)
	}
	if len(c.Artifacts.FilePaths) != 1 {
		t.Errorf("artifacts not propagated: %+v", c.Artifacts)
	}
	if c.TaskID != "T1" {
		t.Errorf("task id = %q", c.TaskID)
	}
}

func TestHandle_UnknownAgentFailsClosed(t *testing.T) {
	w := NewWorker(mapResolver{}, funcRunner(func(context.Context, v2.TaskPacket, AgentRuntime) (RunResult, error) {
		t.Fatal("runner must not run for an unknown agent")
		return RunResult{}, nil
	}), 0)

	c := w.Handle(context.Background(), packet("ghost", "T2"))
	if c.Status != "failed" {
		t.Fatalf("status = %q, want failed", c.Status)
	}
	if c.OutputSummary == "" {
		t.Error("expected a reason for the failure")
	}
}

func TestHandle_MissingRequiredCapabilityFailsClosed(t *testing.T) {
	res := mapResolver{"scout": {AgentID: "scout", Capabilities: []string{"search", "report"}}}
	w := NewWorker(res, funcRunner(func(context.Context, v2.TaskPacket, AgentRuntime) (RunResult, error) {
		t.Fatal("runner must not run when the agent lacks the required capability")
		return RunResult{}, nil
	}), 0)

	p := packet("scout", "TC")
	p.RequiredCapability = "code" // scout has no "code"
	c := w.Handle(context.Background(), p)
	if c.Status != "failed" {
		t.Fatalf("status = %q, want failed", c.Status)
	}
	if !strings.Contains(c.OutputSummary, "capability") {
		t.Errorf("reason should mention capability: %q", c.OutputSummary)
	}
}

func TestHandle_RequiredCapabilitySatisfiedRuns(t *testing.T) {
	res := mapResolver{"atlas": {AgentID: "atlas", Capabilities: []string{"code", "report"}}}
	ran := false
	w := NewWorker(res, funcRunner(func(context.Context, v2.TaskPacket, AgentRuntime) (RunResult, error) {
		ran = true
		return RunResult{Summary: "did it"}, nil
	}), 0)

	p := packet("atlas", "TC2")
	p.RequiredCapability = "code"
	c := w.Handle(context.Background(), p)
	if !ran {
		t.Fatal("runner should run when capability is satisfied")
	}
	if c.Status != "completed" {
		t.Fatalf("status = %q, want completed", c.Status)
	}
}

func TestHandle_RunnerErrorFailsClosed(t *testing.T) {
	res := mapResolver{"atlas": {AgentID: "atlas"}}
	w := NewWorker(res, funcRunner(func(context.Context, v2.TaskPacket, AgentRuntime) (RunResult, error) {
		return RunResult{}, errors.New("factory queue unreachable")
	}), 0)

	c := w.Handle(context.Background(), packet("atlas", "T3"))
	if c.Status != "failed" {
		t.Fatalf("status = %q, want failed", c.Status)
	}
	if c.ReviewNotes == "" {
		t.Error("expected review notes describing the failure")
	}
}

func TestHandle_PanicFailsClosed(t *testing.T) {
	res := mapResolver{"chisel": {AgentID: "chisel"}}
	w := NewWorker(res, funcRunner(func(context.Context, v2.TaskPacket, AgentRuntime) (RunResult, error) {
		panic("blender exploded")
	}), 0)

	c := w.Handle(context.Background(), packet("chisel", "T4"))
	if c.Status != "failed" {
		t.Fatalf("status = %q, want failed", c.Status)
	}
}

func TestHandle_TimeoutFailsClosed(t *testing.T) {
	res := mapResolver{"muse": {AgentID: "muse"}}
	runner := funcRunner(func(ctx context.Context, _ v2.TaskPacket, _ AgentRuntime) (RunResult, error) {
		<-ctx.Done() // block until the worker's deadline fires
		return RunResult{}, ctx.Err()
	})
	w := NewWorker(res, runner, 50*time.Millisecond)

	start := time.Now()
	c := w.Handle(context.Background(), packet("muse", "T5"))
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout did not bound the run")
	}
	if c.Status != "failed" {
		t.Fatalf("status = %q, want failed", c.Status)
	}
}

func TestDeadlineFor_ClampsToMaxRun(t *testing.T) {
	w := NewWorker(mapResolver{}, funcRunner(nil), 10*time.Minute)
	// A far-future deadline exceeds maxRun → clamp to maxRun.
	far := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	if got := w.deadlineFor(v2.TaskPacket{Deadline: far}); got != 10*time.Minute {
		t.Errorf("far deadline = %v, want maxRun 10m", got)
	}
	// A near deadline is honored.
	near := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	if got := w.deadlineFor(v2.TaskPacket{Deadline: near}); got > 10*time.Minute || got <= 0 {
		t.Errorf("near deadline = %v, want <=10m and >0", got)
	}
	// Missing/garbage deadline → maxRun.
	if got := w.deadlineFor(v2.TaskPacket{Deadline: "not-a-time"}); got != 10*time.Minute {
		t.Errorf("bad deadline = %v, want maxRun", got)
	}
}

func TestHandleAndPublish(t *testing.T) {
	res := mapResolver{"scout": {AgentID: "scout"}}
	w := NewWorker(res, funcRunner(func(context.Context, v2.TaskPacket, AgentRuntime) (RunResult, error) {
		return RunResult{Summary: "ok"}, nil
	}), 0)
	pub := &capturePublisher{}

	c, err := w.HandleAndPublish(context.Background(), packet("scout", "T6"), pub)
	if err != nil {
		t.Fatalf("publish error: %v", err)
	}
	if pub.last == nil || pub.last.TaskID != "T6" || pub.last.Status != "completed" {
		t.Fatalf("completion not published correctly: %+v", pub.last)
	}
	if c.Status != "completed" {
		t.Errorf("returned completion status = %q", c.Status)
	}
}
