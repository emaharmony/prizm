// scenario_runner.go is the execution half of PR5's scenario-based testing
// framework (scenario_types.go is the data half): ScriptedRoleRunner
// promotes the scripted-runner test-double pattern already used internally
// by this package's own tests (e.g. supervisor_test.go's scriptedRunner)
// from test-only code into production code, so `prism graph test` can run a
// Scenario's script against a real Supervisor with zero model calls.
package multiagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/emaharmony/prism/internal/cost"
	"github.com/emaharmony/prism/internal/event"
)

// ScriptedRoleRunner implements RoleRunner by consuming a Scenario's Script
// in order — one entry per RunRole call — instead of invoking any real
// model/provider. It asserts the role the supervisor actually requests
// matches the next scripted entry's Role, returning a clear error (not a
// panic) on mismatch: a scenario author's script falling out of sync with
// the workflow's actual routing behavior is a real, expected failure mode
// that `prism graph test` must surface plainly, not crash on.
type ScriptedRoleRunner struct {
	mu     sync.Mutex
	script []ScriptedRoleResult
	index  int

	// graph is used only to tell whether a scripted (role, outcome) pair
	// resolves to a terminal transition, which determines whether RunRole
	// must attach an outgoing handoff (Supervisor.Run requires one on every
	// non-terminal transition and forbids one on a terminal transition — see
	// supervisor.go's Run loop). Wired via SetGraph rather than threaded
	// through NewScriptedRoleRunner's constructor: RunScenario (the only
	// production caller) always has a graph in hand at construction time,
	// so this keeps the constructor's signature matching the plan's sketch
	// exactly. A nil graph degrades gracefully: every scripted transition is
	// then treated as non-terminal (a handoff is always attached), which is
	// wrong only for a scenario's final, terminal-reaching step run without
	// SetGraph having been called — RunScenario always calls it, so this
	// only matters for a caller that builds a ScriptedRoleRunner directly.
	graph *CompiledGraph

	// cancel is the CancelFunc for the context RunScenario passes to
	// Supervisor.Run, wired via SetCancelFunc. See RunRole's Cancel handling
	// for why this, rather than a returned error, is the only mechanism
	// available to make a scripted step behave like a real cancellation.
	cancel context.CancelFunc
}

// NewScriptedRoleRunner constructs a ScriptedRoleRunner that will consume
// script in order, one entry per RunRole call.
func NewScriptedRoleRunner(script []ScriptedRoleResult) *ScriptedRoleRunner {
	return &ScriptedRoleRunner{script: script}
}

// SetGraph wires the compiled graph the scripted scenario runs against. See
// the graph field's doc for why this exists as a setter rather than a
// constructor parameter.
func (r *ScriptedRoleRunner) SetGraph(graph *CompiledGraph) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.graph = graph
}

// SetCancelFunc wires the context.CancelFunc RunScenario derives from the
// context it passes to Supervisor.Run. Supervisor.Run distinguishes a
// cancelled run from a merely-failed one purely by checking ctx.Err()
// immediately after RunRole returns (see supervisor.go's Run loop) — it
// never inspects the error RunRole itself returned to decide this — so
// actually calling the context's own CancelFunc is the only way a
// RoleRunner can make a scripted step produce RunStatusCancelled rather
// than RunStatusFailed.
func (r *ScriptedRoleRunner) SetCancelFunc(cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancel = cancel
}

// RunRole implements RoleRunner.
func (r *ScriptedRoleRunner) RunRole(_ context.Context, req RoleRunRequest) (RoleRunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stepIndex := r.index
	if stepIndex >= len(r.script) {
		return RoleRunResult{}, fmt.Errorf(
			"multiagent: scenario script exhausted: role %q was requested at step %d, but the script has only %d entries",
			req.Run.CurrentRole, stepIndex, len(r.script),
		)
	}
	step := r.script[stepIndex]
	r.index++

	if string(req.Run.CurrentRole) != step.Role {
		return RoleRunResult{}, fmt.Errorf(
			"multiagent: scenario script mismatch at step %d: script expects role %q to run next, but the supervisor requested role %q (the scripted scenario has fallen out of sync with the workflow's actual routing)",
			stepIndex, step.Role, req.Run.CurrentRole,
		)
	}

	if step.Cancel {
		if r.cancel != nil {
			r.cancel()
		}
		return RoleRunResult{}, context.Canceled
	}
	if step.FailWith != "" {
		return RoleRunResult{}, errors.New(step.FailWith)
	}

	// RoleRunResult.LocalIterations must be positive (validateRoleRunResult
	// in supervisor.go rejects <= 0), so an unscripted (zero) value defaults
	// to 1 rather than being passed through as an invalid result.
	localIterations := step.LocalIterations
	if localIterations <= 0 {
		localIterations = 1
	}

	result := RoleRunResult{
		Outcome:         TransitionOutcome(step.Outcome),
		LocalIterations: localIterations,
		Metadata: ExecutionMetadata{
			ValidationStatus: step.ValidationStatus,
			ApprovalStatus:   step.ApprovalStatus,
		},
	}
	if step.TokenUsage != nil {
		result.TokenUsage = cost.TokenUsage{
			PromptTokens:     step.TokenUsage.Prompt,
			CompletionTokens: step.TokenUsage.Completion,
			TotalTokens:      step.TokenUsage.Prompt + step.TokenUsage.Completion,
		}
	}

	terminal := false
	if r.graph != nil {
		if t, err := r.graph.Resolve(Role(step.Role), result.Outcome); err == nil {
			terminal = t.Terminal != ""
		}
	}
	if !terminal {
		result.OutgoingHandoff = &HandoffDraft{
			Objective: fmt.Sprintf("scripted scenario step %d (%s -> %s)", stepIndex, step.Role, step.Outcome),
			Reason:    "scenario script",
		}
	}
	return result, nil
}

// scenarioEventSink is a minimal in-memory EventSink used to run a scenario
// against a Supervisor without any real event bus. It intentionally
// duplicates supervisor_test.go's captureEventSink test double rather than
// reusing it: that type is defined in a _test.go file and therefore cannot
// be imported from this (non-test, `prism graph test`-reachable) file.
type scenarioEventSink struct {
	mu     sync.Mutex
	events []event.Event
}

func (s *scenarioEventSink) Emit(evt event.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, evt)
}

func (s *scenarioEventSink) snapshot() []event.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event.Event(nil), s.events...)
}

// ScenarioResult is the outcome of running one Scenario: whether every
// assertion in its Expect held, and a specific, human-readable message per
// mismatch when it didn't.
type ScenarioResult struct {
	Passed   bool
	Failures []string
}

// RunScenario compiles nothing itself — the caller passes an
// already-compiled graph, so a whole ScenarioFile's scenarios can share one
// compile — runs the Supervisor with a ScriptedRoleRunner driven by
// scenario.Script and an in-memory EventSink, and diffs the resulting
// RunState (plus the emitted event stream, for transition/edge-traversal
// assertions) against scenario.Expect.
//
// The returned error is reserved for setup failures RunScenario itself
// cannot recover from (a nil graph, or Supervisor construction failing) —
// NOT for an assertion mismatch or even a "the run failed/was
// cancelled/exhausted a budget" outcome, all of which are legitimate
// scenario outcomes a scenario's Expect may be asserting *for*. Those are
// reported through the returned ScenarioResult instead.
func RunScenario(ctx context.Context, graph *CompiledGraph, scenario Scenario) (ScenarioResult, error) {
	if graph == nil {
		return ScenarioResult{}, errors.New("multiagent: RunScenario requires a non-nil compiled graph")
	}

	runner := NewScriptedRoleRunner(scenario.Script)
	runner.SetGraph(graph)
	sink := &scenarioEventSink{}

	supervisor, err := NewSupervisor(graph, runner, sink, SupervisorOptions{})
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("multiagent: construct supervisor for scenario %q: %w", scenario.Name, err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runner.SetCancelFunc(cancel)

	taskID := strings.TrimSpace(scenario.Input.ID)
	if taskID == "" {
		taskID = "scenario-task"
	}
	runID := "scenario-run"
	if name := strings.TrimSpace(scenario.Name); name != "" {
		runID = "scenario-" + name
	}

	state, runErr := supervisor.Run(runCtx, RunRequest{
		RunID: runID,
		Task:  TaskReference{ID: taskID, Description: scenario.Input.Description},
	})

	return diffScenarioExpectation(scenario.Expect, state, runErr, sink.snapshot()), nil
}

// diffScenarioExpectation compares one run's outcome against expect,
// returning a ScenarioResult with one specific failure message per
// mismatched dimension. See ScenarioExpectation's doc comment
// (scenario_types.go) for exactly which real signal each field is compared
// against.
func diffScenarioExpectation(expect ScenarioExpectation, state RunState, runErr error, events []event.Event) ScenarioResult {
	var failures []string

	if expect.FinalStatus != "" && string(state.Status) != expect.FinalStatus {
		failures = append(failures, fmt.Sprintf("expected finalStatus %q, got %q", expect.FinalStatus, state.Status))
	}

	if expect.TerminalNode != "" {
		got := ""
		if state.TerminalOutcome != nil {
			got = terminalNodeID(state.TerminalOutcome.Condition)
		}
		if got != expect.TerminalNode {
			failures = append(failures, fmt.Sprintf("expected terminalNode %q, got %q", expect.TerminalNode, got))
		}
	}

	if len(expect.Transitions) > 0 {
		got := transitionEdgeSequence(events)
		if !equalStringSlices(got, expect.Transitions) {
			failures = append(failures, fmt.Sprintf("expected transitions %v, got %v", expect.Transitions, got))
		}
	}

	for role, want := range expect.NodeVisits {
		got := state.RoleStates[Role(role)].Visits
		if got != want {
			failures = append(failures, fmt.Sprintf("expected node %q visits = %d, got %d", role, want, got))
		}
	}

	if len(expect.EdgeTraversals) > 0 {
		traversals, _ := scanTransitionEvents(events)
		for edgeID, want := range expect.EdgeTraversals {
			got := traversals[edgeID]
			if got != want {
				failures = append(failures, fmt.Sprintf("expected edge %q traversals = %d, got %d", edgeID, want, got))
			}
		}
	}

	if expect.BudgetExhausted != "" {
		var budgetErr *BudgetExceededError
		if !errors.As(runErr, &budgetErr) {
			failures = append(failures, fmt.Sprintf("expected budgetExhausted %q, but the run did not return a *BudgetExceededError (err: %v)", expect.BudgetExhausted, runErr))
		} else if budgetErr.Budget != expect.BudgetExhausted {
			failures = append(failures, fmt.Sprintf("expected budgetExhausted %q, got %q", expect.BudgetExhausted, budgetErr.Budget))
		}
	}

	if expect.LoopExhausted != "" {
		var budgetErr *BudgetExceededError
		if !errors.As(runErr, &budgetErr) {
			failures = append(failures, fmt.Sprintf("expected loopExhausted %q, but the run did not return a *BudgetExceededError (err: %v)", expect.LoopExhausted, runErr))
		} else if budgetErr.Budget != expect.LoopExhausted {
			failures = append(failures, fmt.Sprintf("expected loopExhausted %q, got budget %q", expect.LoopExhausted, budgetErr.Budget))
		}
	}

	if expect.InvalidOutcome {
		var invalidErr *InvalidTransitionError
		if !errors.As(runErr, &invalidErr) {
			failures = append(failures, fmt.Sprintf("expected invalidOutcome (an *InvalidTransitionError), got %T: %v", runErr, runErr))
		}
	}

	if expect.ApprovalWaiting && !anyRoleStatus(state, RoleStatusWaiting) {
		failures = append(failures, "expected approvalWaiting, but no role state has status \"waiting\" (Supervisor.Run executes a role fully in-memory and never pauses mid-role for approval; approval waits are a DurableRuntime-only concept RunScenario's in-memory Supervisor path cannot produce — see ScenarioExpectation's doc comment)")
	}

	if expect.ValidationFailed && !anyRoleValidationFailed(state) {
		failures = append(failures, "expected validationFailed, but no role state has validation_status \"failed\"")
	}

	return ScenarioResult{Passed: len(failures) == 0, Failures: failures}
}

// transitionEdgeSequence extracts the stable edge ID of every
// transition.selected event, in emission order.
func transitionEdgeSequence(events []event.Event) []string {
	var seq []string
	for _, evt := range events {
		if evt.Type != event.EventMultiAgentTransitionSelected {
			continue
		}
		source := payloadString(evt.Payload, "source_role")
		outcome := payloadString(evt.Payload, "outcome")
		if source == "" || outcome == "" {
			continue
		}
		seq = append(seq, edgeID(Role(source), TransitionOutcome(outcome)))
	}
	return seq
}

func anyRoleStatus(state RunState, status RoleExecutionStatus) bool {
	for _, roleState := range state.RoleStates {
		if roleState.Status == status {
			return true
		}
	}
	return false
}

func anyRoleValidationFailed(state RunState) bool {
	for _, roleState := range state.RoleStates {
		if roleState.ValidationStatus == "failed" {
			return true
		}
	}
	return false
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
