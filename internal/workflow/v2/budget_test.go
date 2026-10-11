package v2

import (
	"context"
	"strings"
	"testing"
	"time"
)

type captureEmitter struct{ events []string }

func (c *captureEmitter) Emit(t string, _ map[string]any) { c.events = append(c.events, t) }
func (c *captureEmitter) count(t string) int {
	n := 0
	for _, e := range c.events {
		if e == t {
			n++
		}
	}
	return n
}
func (c *captureEmitter) has(t string) bool {
	for _, e := range c.events {
		if e == t {
			return true
		}
	}
	return false
}

// A phase whose gate can never pass (no assumptions declared) would iterate to
// its budget; with a token ceiling set, the run must stop early and finalize
// gracefully rather than burn the full iteration budget.
func TestDriveStopsOnTokenBudget(t *testing.T) {
	cfg := &WorkflowConfig{
		Name: "budget", Version: 1,
		Global: GlobalConfig{MaxTotalIterations: 50, MaxTotalTokens: 5},
		Phases: []PhaseConfig{{
			Name: "PROBE", Type: "probe", MaxIterations: 20,
			Gate: GateConfig{Type: "assumption_threshold", Threshold: 2.0},
		}},
	}
	em := &captureEmitter{}
	engine := NewEngine(cfg, em, nil)
	llm := func(_ context.Context, _ []Message) (string, int, int, error) {
		return `{"type":"final","content":"x"}`, 3, 3, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := engine.Drive(ctx, llm, noopTool, DriveOptions{SystemPrompt: "s", UserPrompt: "u"})
	if err != nil {
		t.Fatalf("Drive returned error: %v", err)
	}
	if !em.has("workflow.budget_exhausted") {
		t.Fatalf("expected workflow.budget_exhausted event, got %v", em.events)
	}
	if got := em.count("workflow.budget_exhausted"); got != 1 {
		t.Fatalf("workflow.budget_exhausted emitted %d times, want 1 (%v)", got, em.events)
	}
	if used := state.GetTotalPromptTokens() + state.GetTotalCompletionTokens(); used < 5 {
		t.Fatalf("expected at least the budget in tokens, got %d", used)
	}
	if state.Status != StatusBudgetExhausted {
		t.Fatalf("budget-exhausted run should finalize as %s, got %s", StatusBudgetExhausted, state.Status)
	}
}

// A phase with its own token cap must stop iterating (phase.budget_exhausted)
// while the RUN continues to later phases — per-phase caps are softer than the
// run-wide ceiling.
func TestDrivePhaseTokenBudgetStopsPhaseNotRun(t *testing.T) {
	cfg := &WorkflowConfig{
		Name: "phase-budget", Version: 1,
		Global: GlobalConfig{MaxTotalIterations: 50},
		Phases: []PhaseConfig{
			{
				Name: "PROBE", Type: "probe", MaxIterations: 20, MaxTokens: 5,
				Gate: GateConfig{Type: "assumption_threshold", Threshold: 2.0},
			},
			{
				// Second phase with a trivially-passing gate proves the run survives.
				Name: "EXECUTION", Type: "execution", MaxIterations: 5,
				Gate: GateConfig{Type: "task_completion", Mode: "partial_allowed"},
			},
		},
	}
	em := &captureEmitter{}
	engine := NewEngine(cfg, em, nil)
	engine.GetState().Plan = &PlanGraph{}
	llm := func(_ context.Context, _ []Message) (string, int, int, error) {
		return `{"type":"final","content":"x"}`, 3, 3, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := engine.Drive(ctx, llm, noopTool, DriveOptions{SystemPrompt: "s", UserPrompt: "u", SkipPushRequirement: true})
	if err != nil {
		t.Fatalf("Drive returned error: %v", err)
	}
	if !em.has("phase.budget_exhausted") {
		t.Fatalf("expected phase.budget_exhausted event, got %v", em.events)
	}
	if em.has("workflow.budget_exhausted") {
		t.Fatal("per-phase cap must not trip the run-wide budget event")
	}
	if got := state.GetPhaseTokens("PROBE"); got < 5 {
		t.Fatalf("PROBE should have accumulated at least its cap, got %d", got)
	}
	// The run continued past the capped phase.
	if ps := state.PhaseStates["EXECUTION"]; ps == nil || ps.Iterations == 0 {
		t.Fatalf("EXECUTION should have run after PROBE hit its cap: %+v", ps)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("run should finalize as completed, got %s", state.Status)
	}
}

func TestValidateConfigRejectsNegativePhaseMaxTokens(t *testing.T) {
	cfg := execVerifyConfig(false)
	cfg.Phases[0].MaxTokens = -1
	errs := ValidateConfig(cfg)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "max_tokens must be >= 0") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected max_tokens validation error, got %v", errs)
	}
}

func TestBudgetExceededTimeAndZero(t *testing.T) {
	e := NewEngine(execVerifyConfig(false), nil, nil)
	e.config.Global.MaxTotalTime = "1h"
	if r := e.budgetExceeded(time.Now().Add(-time.Second)); r == "" {
		t.Fatal("expected a past deadline to trip the time budget")
	}
	if r := e.budgetExceeded(time.Time{}); r != "" {
		t.Fatalf("a zero deadline should never trip: %q", r)
	}
}

func TestBudgetExceededTokensOnly(t *testing.T) {
	e := NewEngine(execVerifyConfig(false), nil, nil)
	e.config.Global.MaxTotalTokens = 10
	e.state.AddTokens(6, 6) // 12 >= 10
	if r := e.budgetExceeded(time.Time{}); r == "" {
		t.Fatal("expected token ceiling to trip")
	}
	cfg := execVerifyConfig(false)
	cfg.Global.MaxTotalTokens = 0
	e2 := NewEngine(cfg, nil, nil)
	if e2.config.Global.MaxTotalTokens != UnlimitedTokens {
		t.Fatalf("NewEngine should leave an omitted token ceiling unlimited, got %d", e2.config.Global.MaxTotalTokens)
	}
	e2.state.AddTokens(10_000_000, 0)
	if r := e2.budgetExceeded(time.Time{}); r != "" {
		t.Fatalf("omitted token ceiling unexpectedly tripped: %s", r)
	}
}

func TestNewEngineRejectsInvalidNegativeGlobalTokenBudget(t *testing.T) {
	cfg := execVerifyConfig(false)
	cfg.Global.MaxTotalTokens = -2
	engine := NewEngine(cfg, nil, nil)
	if cfg.Global.MaxTotalTokens != -2 {
		t.Fatalf("NewEngine rewrote invalid token ceiling to %d", cfg.Global.MaxTotalTokens)
	}

	if _, err := engine.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "must be -1") {
		t.Fatalf("Run() error = %v, want invalid token budget error", err)
	}
	llmCalled := false
	if _, err := engine.Drive(context.Background(), func(context.Context, []Message) (string, int, int, error) {
		llmCalled = true
		return `{"type":"final","content":"done"}`, 1, 1, nil
	}, noopTool, DriveOptions{}); err == nil || !strings.Contains(err.Error(), "must be -1") {
		t.Fatalf("Drive() error = %v, want invalid token budget error", err)
	}
	if llmCalled {
		t.Fatal("Drive() called the model with an invalid token budget")
	}
	if _, err := engine.ProcessLLMResponse(`{"type":"final","content":"done"}`); err == nil || !strings.Contains(err.Error(), "must be -1") {
		t.Fatalf("ProcessLLMResponse() error = %v, want invalid token budget error", err)
	}
}

// -1 remains the persisted explicit "unlimited" sentinel.
func TestLocalFallbackUsageIsReportedButDoesNotConsumePaidBudget(t *testing.T) {
	cfg := &WorkflowConfig{
		Name: "local-fallback", Version: 2,
		Global: GlobalConfig{MaxTotalIterations: 2, MaxTotalTokens: 1},
		Phases: []PhaseConfig{{Name: "PROBE", Type: "probe", MaxIterations: 1, Gate: GateConfig{Type: "assumption_threshold", Threshold: 2.0}}},
	}
	engine := NewEngine(cfg, nil, nil)
	llm := func(_ context.Context, _ []Message) (string, int, int, error) {
		pt, ct := MarkLocalUsage(4, 3)
		return `{"type":"final","content":"ok"}`, pt, ct, nil
	}
	state, err := engine.Drive(context.Background(), llm, noopTool, DriveOptions{SystemPrompt: "s", UserPrompt: "u"})
	if err != nil {
		t.Fatalf("Drive returned error: %v", err)
	}
	if paid := state.GetTotalTokens(); paid != 0 {
		t.Fatalf("paid total = %d, want 0", paid)
	}
	if got := state.LocalPromptTokens + state.LocalCompletionTokens; got != 7 {
		t.Fatalf("local total = %d, want 7", got)
	}
}

func TestBudgetUnlimitedNeverTrips(t *testing.T) {
	cfg := execVerifyConfig(false)
	cfg.Global.MaxTotalTokens = UnlimitedTokens
	e := NewEngine(cfg, nil, nil)
	if e.config.Global.MaxTotalTokens != UnlimitedTokens {
		t.Fatalf("NewEngine must preserve the unlimited sentinel, got %d", e.config.Global.MaxTotalTokens)
	}
	e.state.AddTokens(DefaultMaxTotalTokens*3, 0)
	if r := e.budgetExceeded(time.Time{}); r != "" {
		t.Fatalf("unlimited ceiling must never trip, got %q", r)
	}
}

// A delegated sub-agent's token spend must roll up into the parent run's budget so
// it counts against global.max_total_tokens (previously it was invisible).
func TestHandleTaskCompletionRollsUpTokens(t *testing.T) {
	st := NewWorkflowState(&WorkflowConfig{Name: "x", Version: 2, Phases: []PhaseConfig{{Name: "EXECUTION"}}})
	dm := &DelegationManager{}
	before := st.GetTotalTokens()
	dm.HandleTaskCompletion(TaskCompletion{
		TaskID: "T1", Status: "completed", PromptTokens: 120, CompletionTokens: 80,
	}, st)
	if got := st.GetTotalTokens() - before; got != 200 {
		t.Fatalf("sub-agent tokens not rolled up: parent gained %d, want 200", got)
	}
}

// A cap below the unlimited sentinel (e.g. a typo like -100) is invalid and must be
// rejected on the load path, not silently disable the budget.
func TestLoadConfigRejectsInvalidNegativeTokens(t *testing.T) {
	bad := `{"name":"bad","version":2,"global":{"max_total_tokens":-100},"phases":[{"name":"REPORT","type":"report","max_iterations":1,"gate":{"type":"report_completeness"}}]}`
	if _, err := parseConfigBytes([]byte(bad), "bad.json"); err == nil {
		t.Fatal("expected max_total_tokens < -1 to be rejected on load")
	}
	// -1 (unlimited) and 0 (default) must still load cleanly.
	for _, ok := range []string{
		`{"name":"u","version":2,"global":{"max_total_tokens":-1},"phases":[{"name":"REPORT","type":"report","max_iterations":1,"gate":{"type":"report_completeness"}}]}`,
		`{"name":"z","version":2,"global":{"max_total_tokens":0},"phases":[{"name":"REPORT","type":"report","max_iterations":1,"gate":{"type":"report_completeness"}}]}`,
	} {
		if _, err := parseConfigBytes([]byte(ok), "ok.json"); err != nil {
			t.Fatalf("valid token cap rejected: %v", err)
		}
	}
}

func TestDriveEstimatesZeroUsageProviderForBudget(t *testing.T) {
	cfg := &WorkflowConfig{
		Name: "zero-usage", Version: 2,
		Global: GlobalConfig{MaxTotalIterations: 10, MaxTotalTokens: 10},
		Phases: []PhaseConfig{{Name: "PROBE", Type: "probe", MaxIterations: 5, Gate: GateConfig{Type: "assumption_threshold", Threshold: 2.0}}},
	}
	em := &captureEmitter{}
	engine := NewEngine(cfg, em, nil)
	llm := func(_ context.Context, _ []Message) (string, int, int, error) {
		return strings.Repeat("x", 200), 0, 0, nil
	}
	state, err := engine.Drive(context.Background(), llm, noopTool, DriveOptions{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("Drive returned error: %v", err)
	}
	if state.GetTotalTokens() == 0 {
		t.Fatal("expected estimated tokens to be recorded")
	}
	if state.Status != StatusBudgetExhausted {
		t.Fatalf("zero-usage provider should still trip budget, got %s", state.Status)
	}
}

func TestHandleDelegatePassesRemainingTokenBudget(t *testing.T) {
	cfg := &WorkflowConfig{
		Name: "delegate-budget", Version: 2,
		Global: GlobalConfig{MaxTotalTokens: 100},
		Phases: []PhaseConfig{{Name: "EXECUTION", Type: "execution", MaxIterations: 1}},
	}
	dm := NewDelegationManager("tasks", "complete")
	engine := NewEngine(cfg, nil, dm)
	engine.GetState().Plan = &PlanGraph{Tasks: []PlanTask{{ID: "T1", Agent: "coder", Description: "do it"}}}
	engine.GetState().AddTokens(30, 20)
	var published TaskPacket
	engine.SetTaskPublisher(func(p TaskPacket) error { published = p; return nil })
	msg := engine.handleDelegate(context.Background(), &ToolRequest{Input: map[string]any{"task_id": "T1"}})
	if !strings.Contains(msg, "dispatched") {
		t.Fatalf("expected delegation dispatch, got %q", msg)
	}
	if published.MaxTokens != 50 {
		t.Fatalf("delegated max_tokens = %d, want remaining budget 50", published.MaxTokens)
	}
}

func TestHandleDelegateBlocksWhenNoRunBudgetRemains(t *testing.T) {
	cfg := &WorkflowConfig{
		Name: "delegate-budget", Version: 2,
		Global: GlobalConfig{MaxTotalTokens: 100},
		Phases: []PhaseConfig{{Name: "EXECUTION", Type: "execution", MaxIterations: 1}},
	}
	engine := NewEngine(cfg, nil, NewDelegationManager("tasks", "complete"))
	engine.GetState().Plan = &PlanGraph{Tasks: []PlanTask{{ID: "T1", Agent: "coder", Description: "do it"}}}
	engine.GetState().AddTokens(100, 0)
	published := false
	engine.SetTaskPublisher(func(p TaskPacket) error { published = true; return nil })
	msg := engine.handleDelegate(context.Background(), &ToolRequest{Input: map[string]any{"task_id": "T1"}})
	if !strings.Contains(msg, "token budget exhausted") {
		t.Fatalf("expected budget exhaustion message, got %q", msg)
	}
	if published {
		t.Fatal("delegation should not publish when no run budget remains")
	}
}
