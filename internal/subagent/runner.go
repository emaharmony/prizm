package subagent

import (
	"context"
	"fmt"
	"strings"

	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

// runner.go implements a production TaskRunner: a bounded single-agent tool
// loop. It drives one delegated task to completion by alternating model turns
// with tool executions until the agent emits a final answer or hits its
// iteration/token budget. The model and tool specifics are injected via a
// Backend, so the loop control (budgeting, done-detection, artifact capture) is
// unit-testable without a live provider — mirroring how v2.Drive splits loop
// control from the injected llm/tool callbacks.

// Turn is one model response plus its token usage.
type Turn struct {
	Text             string
	PromptTokens     int
	CompletionTokens int
}

// LLMFunc calls the agent's model with the running transcript.
type LLMFunc func(ctx context.Context, messages []v2.Message) (Turn, error)

// Action is the parsed intent of a model turn: either a final answer or a tool
// call. This matches the two outcomes v2's phase parser produces.
type Action struct {
	Final   bool
	Content string
	Tool    string
	Input   map[string]any
}

// Parser turns raw model text into an Action (final vs tool call).
type Parser func(text string) Action

// ToolExec runs a tool and returns a result string to feed back to the model.
type ToolExec func(ctx context.Context, tool string, input map[string]any) (string, error)

// Backend binds a resolved agent to its per-run execution callbacks. serve
// supplies one backed by the provider registry + tool executor; tests supply a
// mock. Returning an error fails the task closed.
type Backend interface {
	Bind(runtime AgentRuntime) (LLMFunc, Parser, ToolExec, error)
}

// LoopRunner is a TaskRunner that runs a bounded tool loop per task.
type LoopRunner struct {
	backend       Backend
	maxIterations int
	maxTokens     int
	systemPrompt  func(packet v2.TaskPacket, runtime AgentRuntime) string
	scope         ToolScope // nil = no per-agent tool scoping
}

// LoopRunnerConfig configures a LoopRunner. Only Backend is required.
type LoopRunnerConfig struct {
	Backend       Backend
	MaxIterations int // 0 → DefaultMaxIterations
	MaxTokens     int // 0 -> DefaultSubAgentTokenBudget, -1 -> unlimited
	// SystemPrompt overrides the default task charter builder.
	SystemPrompt func(packet v2.TaskPacket, runtime AgentRuntime) string
	// Scope enforces per-agent tool scoping. nil disables it (any registered
	// tool the policy engine permits may run).
	Scope ToolScope
}

// DefaultMaxIterations bounds a single delegated task's tool-loop turns.
const DefaultMaxIterations = 25

// DefaultSubAgentTokenBudget bounds aggregate prompt+completion tokens for one delegated task.
const DefaultSubAgentTokenBudget = 100_000

// DefaultMaxTokens is kept as a compatibility alias for older callers.
const DefaultMaxTokens = DefaultSubAgentTokenBudget

// NewLoopRunner builds a LoopRunner from config.
func NewLoopRunner(cfg LoopRunnerConfig) *LoopRunner {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = DefaultMaxIterations
	}
	if cfg.MaxTokens == 0 || cfg.MaxTokens < v2.UnlimitedTokens {
		cfg.MaxTokens = DefaultSubAgentTokenBudget
	}
	sp := cfg.SystemPrompt
	if sp == nil {
		sp = defaultSystemPrompt
	}
	return &LoopRunner{
		backend:       cfg.Backend,
		maxIterations: cfg.MaxIterations,
		maxTokens:     cfg.MaxTokens,
		systemPrompt:  sp,
		scope:         cfg.Scope,
	}
}

// Run executes the delegated task. It returns an error (→ failed completion at
// the Worker) when the agent cannot be bound, a tool fails hard, or the task
// does not complete within its iteration/token budget. Context cancellation
// (the Worker's deadline) is honored between turns.
func (r *LoopRunner) Run(ctx context.Context, packet v2.TaskPacket, runtime AgentRuntime) (RunResult, error) {
	llm, parse, exec, err := r.backend.Bind(runtime)
	if err != nil {
		return RunResult{}, fmt.Errorf("bind agent %q: %w", runtime.AgentID, err)
	}

	messages := []v2.Message{
		{Role: "system", Content: r.systemPrompt(packet, runtime)},
		{Role: "user", Content: taskUserPrompt(packet)},
	}

	artifacts := newArtifactSet()
	maxTokens := r.maxTokens
	if packet.MaxTokens != 0 {
		maxTokens = packet.MaxTokens
	}
	if maxTokens < v2.UnlimitedTokens {
		maxTokens = r.maxTokens
	}
	totalPrompt, totalCompletion := 0, 0
	iterations, toolCalls, deniedToolCalls := 0, 0, 0
	// usage snapshots the accumulated token counts onto any RunResult (success or
	// failure) so delegated spend is never lost from the parent budget.
	usage := func(r RunResult) RunResult {
		r.PromptTokens, r.CompletionTokens = totalPrompt, totalCompletion
		r.Iterations, r.ToolCalls, r.DeniedToolCalls = iterations, toolCalls, deniedToolCalls
		return r
	}

	maxIterations := r.maxIterations
	if runtime.MaxIterations > 0 && runtime.MaxIterations < maxIterations {
		maxIterations = runtime.MaxIterations
	}

	for i := 0; i < maxIterations; i++ {
		if err := ctx.Err(); err != nil {
			return usage(RunResult{}), err
		}

		turn, err := llm(ctx, messages)
		if err != nil {
			return usage(RunResult{}), fmt.Errorf("model turn %d: %w", i+1, err)
		}
		iterations = i + 1
		totalPrompt += turn.PromptTokens
		totalCompletion += turn.CompletionTokens
		messages = append(messages, v2.Message{Role: "assistant", Content: turn.Text})
		usedTokens := totalPrompt + totalCompletion
		if maxTokens > 0 && usedTokens >= maxTokens {
			summary := fmt.Sprintf("token budget %d exhausted after %d turns", maxTokens, i+1)
			return usage(RunResult{Summary: summary, Artifacts: artifacts.result()}), fmt.Errorf("%s", summary)
		}

		action := parse(turn.Text)
		if action.Final {
			return usage(RunResult{Summary: strings.TrimSpace(action.Content), Artifacts: artifacts.result()}), nil
		}

		if action.Tool == "" {
			// Neither final nor a tool call — nudge and continue.
			messages = append(messages, v2.Message{Role: "user", Content: "Respond with a tool call or a final answer."})
			continue
		}
		toolCalls++

		// Per-agent tool scoping: deny out-of-role tools before execution and
		// feed the denial back so the agent adapts (not fatal).
		if r.scope != nil && !r.scope.Allowed(runtime, action.Tool) {
			deniedToolCalls++
			messages = append(messages, v2.Message{Role: "user", Content: fmt.Sprintf("Tool %q is not permitted for agent %q (role scope). Use a tool within your role or give your final answer.", action.Tool, runtime.AgentID)})
			continue
		}

		artifacts.record(action.Tool, action.Input)
		result, terr := exec(ctx, action.Tool, action.Input)
		if terr != nil {
			// A tool failure is fed back so the agent can adapt, not fatal.
			messages = append(messages, v2.Message{Role: "user", Content: fmt.Sprintf("Tool %q failed: %v. Try another approach or give your final answer.", action.Tool, terr)})
		} else {
			messages = append(messages, v2.Message{Role: "user", Content: truncate(fmt.Sprintf("Tool %q result:\n%s", action.Tool, result), 3000)})
		}

	}

	return usage(RunResult{Artifacts: artifacts.result()}), fmt.Errorf("task %q did not complete within %d iterations", packet.TaskID, maxIterations)
}

// artifactSet dedupes file paths touched during a run so the completion reports
// what the sub-agent actually produced.
type artifactSet struct {
	seen  map[string]bool
	paths []string
}

func newArtifactSet() *artifactSet { return &artifactSet{seen: map[string]bool{}} }

// record captures a path-like output from a tool call. Read-only tools carry no
// path and are ignored.
func (a *artifactSet) record(tool string, input map[string]any) {
	for _, key := range []string{"path", "file_path", "filename", "output_path"} {
		if p, ok := input[key].(string); ok && strings.TrimSpace(p) != "" {
			if !a.seen[p] {
				a.seen[p] = true
				a.paths = append(a.paths, p)
			}
		}
	}
}

func (a *artifactSet) result() v2.CompletionArtifacts {
	return v2.CompletionArtifacts{FilePaths: a.paths}
}

// defaultSystemPrompt builds the sub-agent's operating charter for a task.
func defaultSystemPrompt(packet v2.TaskPacket, runtime AgentRuntime) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are agent %q running a single delegated task autonomously.\n", runtime.AgentID)
	if len(runtime.Capabilities) > 0 {
		fmt.Fprintf(&b, "Your capabilities: %s.\n", strings.Join(runtime.Capabilities, ", "))
	}
	b.WriteString("Use your available tools to complete the task, then give a concise final answer describing what you did and where the results are. ")
	b.WriteString("Do only the delegated task; do not expand scope. If you cannot complete it, say so plainly in your final answer.")
	return b.String()
}

// taskUserPrompt renders the packet as the task instruction.
func taskUserPrompt(packet v2.TaskPacket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TASK: %s\n", packet.Description)
	if packet.ExpectedDeliverable != "" {
		fmt.Fprintf(&b, "EXPECTED DELIVERABLE: %s\n", packet.ExpectedDeliverable)
	}
	if len(packet.ValidationChecklist) > 0 {
		fmt.Fprintf(&b, "DONE WHEN:\n- %s\n", strings.Join(packet.ValidationChecklist, "\n- "))
	}
	if len(packet.Context.Constraints) > 0 {
		fmt.Fprintf(&b, "CONSTRAINTS:\n- %s\n", strings.Join(packet.Context.Constraints, "\n- "))
	}
	if len(packet.Context.Files) > 0 {
		fmt.Fprintf(&b, "RELEVANT FILES: %s\n", strings.Join(packet.Context.Files, ", "))
	}
	return b.String()
}

// truncate shortens s to max runes with an ellipsis marker.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...(truncated)"
}
