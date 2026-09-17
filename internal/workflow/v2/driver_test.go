package v2

import (
	"context"
	"strings"
	"testing"
	"time"
)

// signalEmitter notifies a channel whenever the workflow pauses, so tests can
// release feedback gates without racing on shared state.
type signalEmitter struct {
	paused chan string
}

func (e *signalEmitter) Emit(eventType string, payload map[string]any) {
	if eventType == "workflow.paused" {
		phase, _ := payload["phase"].(string)
		select {
		case e.paused <- phase:
		default:
		}
	}
}

// testConfig is a compact 3-phase loop: PLAN → FEEDBACK_PRE → REPORT.
func testConfig() *WorkflowConfig {
	return &WorkflowConfig{
		Name:    "test-loop",
		Version: 1,
		Global:  GlobalConfig{MaxTotalIterations: 30},
		Phases: []PhaseConfig{
			{
				Name: "PLAN", Type: "plan", MaxIterations: 3,
				AllowedTools: []string{"read_file"},
				Gate: GateConfig{Type: "plan_completeness", Threshold: 0.5,
					Weights: map[string]float64{"tasks_identified": 0.5, "resources_assigned": 0.5}},
			},
			{
				Name: "FEEDBACK_PRE", Type: "feedback_pre", MaxIterations: 1,
				Gate: GateConfig{Type: "approval", Approvers: []string{"ema"}, Mode: "require_any"},
			},
			{
				Name: "REPORT", Type: "report", MaxIterations: 3,
				Gate: GateConfig{Type: "report_completeness"},
			},
		},
	}
}

func planResponse() string {
	return "TASK: T1 | description: do the thing | agent: prizm | success: it works\n" +
		"PLAN_COMPLETE\n" + `{"type":"final","content":"plan ready"}`
}

func reportResponse() string {
	return "## Change Summary\n## Proof of Work\n## Impact\n## Next Steps\n## Learnings\n" +
		"REPORT_COMPLETE\n" + `{"type":"final","content":"all done"}`
}

func noopTool(_ context.Context, _ string, _ *ToolRequest) (string, error) { return "ok", nil }

func TestDriveHappyPath(t *testing.T) {
	emitter := &signalEmitter{paused: make(chan string, 4)}
	engine := NewEngine(testConfig(), emitter, nil)

	llm := func(_ context.Context, _ []Message) (string, int, int, error) {
		switch engine.GetState().CurrentPhase() {
		case "PLAN":
			return planResponse(), 1, 1, nil
		case "REPORT":
			return reportResponse(), 1, 1, nil
		default:
			return `{"type":"final","content":"noop"}`, 1, 1, nil
		}
	}

	// Approve the single pre-execution pause.
	go func() {
		<-emitter.paused
		engine.GetExternalEventChannel() <- ExternalEvent{
			Type: "approval", Source: "test",
			Data: map[string]any{"decision": "approved"},
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := engine.Drive(ctx, llm, noopTool, DriveOptions{SystemPrompt: "s", UserPrompt: "u"})
	if err != nil {
		t.Fatalf("Drive returned error: %v", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("expected completed, got %s", state.Status)
	}
	if ps := state.PhaseStates["REPORT"]; ps == nil || ps.Status != PhaseStatusCompleted {
		t.Fatalf("REPORT did not complete: %+v", ps)
	}
	if state.Feedback == nil || state.Feedback.PreExecution == nil || state.Feedback.PreExecution.Status != "approved" {
		t.Fatalf("pre-execution feedback not approved: %+v", state.Feedback)
	}
}

func TestDriveIterateOnChangesRequested(t *testing.T) {
	emitter := &signalEmitter{paused: make(chan string, 4)}
	engine := NewEngine(testConfig(), emitter, nil)

	planCalls := 0
	llm := func(_ context.Context, _ []Message) (string, int, int, error) {
		switch engine.GetState().CurrentPhase() {
		case "PLAN":
			planCalls++
			return planResponse(), 1, 1, nil
		case "REPORT":
			return reportResponse(), 1, 1, nil
		default:
			return `{"type":"final","content":"noop"}`, 1, 1, nil
		}
	}

	// First pause: request changes (loops back to PLAN). Second pause: approve.
	go func() {
		ch := engine.GetExternalEventChannel()
		<-emitter.paused
		ch <- ExternalEvent{Type: "approval", Source: "test", Data: map[string]any{"decision": "changes_requested"}}
		<-emitter.paused
		ch <- ExternalEvent{Type: "approval", Source: "test", Data: map[string]any{"decision": "approved"}}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := engine.Drive(ctx, llm, noopTool, DriveOptions{SystemPrompt: "s", UserPrompt: "u"})
	if err != nil {
		t.Fatalf("Drive returned error: %v", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("expected completed, got %s", state.Status)
	}
	if planCalls < 2 {
		t.Fatalf("expected PLAN to run at least twice (iterate), ran %d", planCalls)
	}
	if !strings.Contains(string(state.Status), "completed") {
		t.Fatalf("unexpected final status %s", state.Status)
	}
}

func TestEnforceCommitPushSkipsPushWhenNoRemote(t *testing.T) {
	engine := NewEngine(testConfig(), nil, nil)

	reject := engine.enforceCommitPush("EXECUTION", true, true, false, true)
	if reject != "" {
		t.Fatalf("expected no rejection when push requirement is skipped, got %q", reject)
	}
}

func TestEnforceCommitPushRequiresPushByDefault(t *testing.T) {
	engine := NewEngine(testConfig(), nil, nil)

	reject := engine.enforceCommitPush("EXECUTION", true, true, false, false)
	if !strings.Contains(reject, "PUSH REQUIRED") {
		t.Fatalf("expected push rejection, got %q", reject)
	}
}
