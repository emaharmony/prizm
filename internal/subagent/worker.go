// Package subagent turns a delegated TaskPacket into a TaskCompletion by
// resolving the target agent, running a bounded execution, and reporting the
// result. It is the consumer half of the V37/V58 delegation path — the piece
// that makes sub-agents genuinely autonomous instead of orchestrator-routed.
//
// The package is transport- and execution-agnostic: NATS wiring and the real
// agent runner are injected as interfaces, so the core logic (resolve → run →
// report, with fail-closed error/timeout handling) is unit-testable in
// isolation. See docs/design/V58-FULL-AUTONOMY-DESIGN.md.
package subagent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	v2 "github.com/emaharmony/prism/internal/workflow/v2"
)

// AgentRuntime is the resolved execution surface for one agent: enough for a
// TaskRunner to run it. Kept minimal so the resolver can be backed by the
// orchestrator config without importing it here.
type AgentRuntime struct {
	AgentID             string
	Provider            string
	Model               string
	Capabilities        []string
	RunID               string
	TaskID              string
	AllowedTools        []string
	EnforceAllowedTools bool
	MaxIterations       int
	// WorkDir is the isolated working directory for this task's run (set by the
	// Worker from its WorktreeProvider). Empty = the shared/default root. The
	// backend points file/git tools at it.
	WorkDir string
}

// AgentResolver resolves an agent id to its runtime. ok is false when the id is
// unknown (→ the worker fails the task closed rather than guessing).
type AgentResolver interface {
	Resolve(agentID string) (AgentRuntime, bool)
}

// RunResult is what a TaskRunner produces for a completed task.
type RunResult struct {
	Summary   string
	Artifacts v2.CompletionArtifacts
	// PromptTokens/CompletionTokens are the sub-agent's LLM usage for this task.
	// They are carried on both success and failure returns so the orchestrator can
	// roll delegated spend into the parent run's token budget.
	PromptTokens     int
	CompletionTokens int
	Iterations       int
	ToolCalls        int
	DeniedToolCalls  int
}

// TaskRunner executes one delegated task for a resolved agent. Implementations
// range from a mock (tests) to a bounded tool-loop / gated-loop slice
// (production). A returned error becomes a failed completion.
type TaskRunner interface {
	Run(ctx context.Context, packet v2.TaskPacket, runtime AgentRuntime) (RunResult, error)
}

// Publisher sends a completion back to the orchestrator (NATS in serve; a
// capture in tests).
type Publisher interface {
	PublishCompletion(c v2.TaskCompletion) error
}

// Worker consumes task packets and produces/publishes completions.
type Worker struct {
	resolver AgentResolver
	runner   TaskRunner
	// maxRun caps a single task's wall-clock even if the packet Deadline is
	// missing or absurd. 0 → DefaultMaxRun.
	maxRun time.Duration
	// worktrees, when set, gives code-capable tasks an isolated working dir.
	worktrees WorktreeProvider
}

// SetWorktrees enables per-task worktree isolation for code-capable agents.
func (w *Worker) SetWorktrees(p WorktreeProvider) { w.worktrees = p }

// DefaultMaxRun bounds a single delegated task when no usable deadline is given.
const DefaultMaxRun = 30 * time.Minute

// NewWorker builds a worker. resolver and runner are required.
func NewWorker(resolver AgentResolver, runner TaskRunner, maxRun time.Duration) *Worker {
	if maxRun <= 0 {
		maxRun = DefaultMaxRun
	}
	return &Worker{resolver: resolver, runner: runner, maxRun: maxRun}
}

// Handle turns a packet into a completion. It never returns an error: every
// failure mode (unknown agent, runner error, timeout, panic) produces a
// status:"failed" completion so a delegated task always gets a definitive
// answer and can never silently strand the orchestrator's gate.
func (w *Worker) Handle(ctx context.Context, packet v2.TaskPacket) v2.TaskCompletion {
	runtime, ok := w.resolver.Resolve(packet.TargetAgent)
	if !ok {
		return failed(packet.TaskID, fmt.Sprintf("unknown agent %q", packet.TargetAgent))
	}

	// Capability-aware routing: a task delegated to an agent that lacks the
	// required capability fails closed rather than running on the wrong agent.
	if packet.RequiredCapability != "" && !hasCapability(runtime, packet.RequiredCapability) {
		return failed(packet.TaskID, fmt.Sprintf("agent %q lacks required capability %q", packet.TargetAgent, packet.RequiredCapability))
	}

	// Worktree isolation: code-capable (file-mutating) agents run in their own
	// git worktree so parallel runs don't collide. Non-mutating agents skip it.
	if w.worktrees != nil && hasCapability(runtime, "code") {
		dir, release, err := w.worktrees.Acquire(ctx, packet.TaskID)
		if err != nil {
			return failed(packet.TaskID, fmt.Sprintf("could not isolate worktree for agent %q: %v", packet.TargetAgent, err))
		}
		defer release()
		runtime.WorkDir = dir
	}

	runCtx, cancel := context.WithTimeout(ctx, w.deadlineFor(packet))
	defer cancel()

	result, err := w.runGuarded(runCtx, packet, runtime)
	if err != nil {
		reason := fmt.Sprintf("agent %q failed: %v", packet.TargetAgent, err)
		if runCtx.Err() == context.DeadlineExceeded {
			reason = fmt.Sprintf("task %q timed out for agent %q", packet.TaskID, packet.TargetAgent)
		}
		fc := failed(packet.TaskID, reason)
		if strings.TrimSpace(result.Summary) != "" {
			fc.OutputSummary = strings.TrimSpace(result.Summary)
		}
		fc.Artifacts = result.Artifacts
		// Even a failed/timed-out sub-agent spent tokens, so surface them to the parent budget.
		fc.PromptTokens, fc.CompletionTokens = result.PromptTokens, result.CompletionTokens
		fc.Iterations = result.Iterations
		fc.ToolCalls = result.ToolCalls
		fc.DeniedToolCalls = result.DeniedToolCalls
		return fc
	}

	return v2.TaskCompletion{
		TaskID:           packet.TaskID,
		Status:           "completed",
		OutputSummary:    result.Summary,
		Artifacts:        result.Artifacts,
		PromptTokens:     result.PromptTokens,
		CompletionTokens: result.CompletionTokens,
		Iterations:       result.Iterations,
		ToolCalls:        result.ToolCalls,
		DeniedToolCalls:  result.DeniedToolCalls,
	}
}

// HandleAndPublish runs Handle and publishes the completion. A publish failure
// is logged and returned (the completion itself is always well-formed).
func (w *Worker) HandleAndPublish(ctx context.Context, packet v2.TaskPacket, pub Publisher) (v2.TaskCompletion, error) {
	completion := w.Handle(ctx, packet)
	if pub == nil {
		return completion, nil
	}
	if err := pub.PublishCompletion(completion); err != nil {
		log.Printf("[SUBAGENT] failed to publish completion for task %s: %v", packet.TaskID, err)
		return completion, err
	}
	return completion, nil
}

// runGuarded runs the runner, converting a panic into an error so one bad
// sub-agent run can't take down the worker.
func (w *Worker) runGuarded(ctx context.Context, packet v2.TaskPacket, runtime AgentRuntime) (result RunResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in task runner: %v", r)
		}
	}()
	return w.runner.Run(ctx, packet, runtime)
}

// deadlineFor derives the run budget from the packet Deadline, clamped to
// (0, maxRun]. An unparseable/past/absent deadline falls back to maxRun.
func (w *Worker) deadlineFor(packet v2.TaskPacket) time.Duration {
	if packet.Deadline != "" {
		if t, err := time.Parse(time.RFC3339, packet.Deadline); err == nil {
			if d := time.Until(t); d > 0 && d <= w.maxRun {
				return d
			}
		}
	}
	return w.maxRun
}

func failed(taskID, reason string) v2.TaskCompletion {
	return v2.TaskCompletion{
		TaskID:        taskID,
		Status:        "failed",
		OutputSummary: reason,
		ReviewNotes:   reason,
	}
}
