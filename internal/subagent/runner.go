package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	backend                 Backend
	maxIterations           int
	maxTokens               int
	systemPrompt            func(packet v2.TaskPacket, runtime AgentRuntime) string
	scope                   ToolScope // nil = no per-agent tool scoping
	finalReplyReserveTokens int
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
	// FinalReplyReserveTokens opts into a finalization phase that reserves
	// aggregate budget for one last schema-conforming model response.
	FinalReplyReserveTokens int
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
		backend:                 cfg.Backend,
		maxIterations:           cfg.MaxIterations,
		maxTokens:               cfg.MaxTokens,
		systemPrompt:            sp,
		scope:                   cfg.Scope,
		finalReplyReserveTokens: max(0, cfg.FinalReplyReserveTokens),
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
	finalInstruction := finalizationInstruction(packet.FinalResponseContract)
	prerequisitePhaseInstruction := prerequisiteInstruction(packet.FinalizationPrerequisiteTool)
	futureGrowthReserve := futureToolTurnGrowthReserve(finalInstruction, prerequisitePhaseInstruction)

	artifacts := newArtifactSet()
	maxTokens := r.maxTokens
	if packet.MaxTokens != 0 {
		maxTokens = packet.MaxTokens
	}
	if maxTokens < v2.UnlimitedTokens {
		maxTokens = r.maxTokens
	}
	finalReplyReserveTokens := r.finalReplyReserveTokens
	prerequisiteRequired := strings.TrimSpace(packet.FinalizationPrerequisiteTool) != ""
	if prerequisiteRequired && maxTokens > 0 && finalReplyReserveTokens > 0 && maxTokens < 2*finalReplyReserveTokens {
		log.Printf("[SUBAGENT] task=%s finalization=false reason=two_reply_reserve_exceeds_task_budget task_budget=%d reserve=%d",
			packet.TaskID, maxTokens, finalReplyReserveTokens)
		finalReplyReserveTokens = 0
	} else if maxTokens > 0 && maxTokens <= finalReplyReserveTokens {
		log.Printf("[SUBAGENT] task=%s finalization=false reason=reserve_exceeds_task_budget task_budget=%d reserve=%d",
			packet.TaskID, maxTokens, finalReplyReserveTokens)
		finalReplyReserveTokens = 0
	}
	totalPrompt, totalCompletion := 0, 0
	iterations, toolCalls, deniedToolCalls := 0, 0, 0
	finalOnly := false
	prerequisiteOnly := false
	prerequisiteSatisfied := !prerequisiteRequired
	lastPromptTokens, lastCompletionTokens := 0, 0
	finalPromptGrowthTokens := 0
	// usage snapshots the accumulated token counts onto any RunResult (success or
	// failure) so delegated spend is never lost from the parent budget.
	usage := func(r RunResult) RunResult {
		r.PromptTokens, r.CompletionTokens = totalPrompt, totalCompletion
		r.Iterations, r.ToolCalls, r.DeniedToolCalls = iterations, toolCalls, deniedToolCalls
		return r
	}
	armFinalization := func(turn, used, promptTokens, completionTokens, promptGrowthTokens int) {
		finalOnly = true
		finalPromptGrowthTokens = promptGrowthTokens
		log.Printf("[SUBAGENT] task=%s turn=%d finalization=true remaining_tokens=%d estimated_final_tokens=%d",
			packet.TaskID, turn, max(0, maxTokens-used), finalReplyEstimate(promptTokens, completionTokens, finalReplyReserveTokens, promptGrowthTokens))
	}
	armPrerequisite := func(turn, used, promptTokens, completionTokens, promptGrowthTokens int) {
		prerequisiteOnly = true
		finalPromptGrowthTokens = promptGrowthTokens
		log.Printf("[SUBAGENT] task=%s turn=%d prerequisite_finalization=true remaining_tokens=%d estimated_prerequisite_and_final_tokens=%d tool=%q",
			packet.TaskID, turn, max(0, maxTokens-used), prerequisiteAndFinalEstimate(promptTokens, completionTokens, finalReplyReserveTokens, promptGrowthTokens, futureGrowthReserve), packet.FinalizationPrerequisiteTool)
	}
	maxIterations := r.maxIterations
	if runtime.MaxIterations > 0 && runtime.MaxIterations < maxIterations {
		maxIterations = runtime.MaxIterations
	}
	considerFinalization := func(turn, used, promptTokens, completionTokens, promptGrowthTokens int) (string, error) {
		if !prerequisiteSatisfied {
			remainingCalls := maxIterations - turn
			if remainingCalls < 2 {
				return "", fmt.Errorf("task %q lacks two remaining iterations for required tool %q and its final response", packet.TaskID, packet.FinalizationPrerequisiteTool)
			}
			if remainingCalls == 2 || shouldEnterPrerequisite(maxTokens, used, promptTokens, completionTokens, finalReplyReserveTokens, promptGrowthTokens, futureGrowthReserve) {
				armPrerequisite(turn, used, promptTokens, completionTokens, promptGrowthTokens)
				return prerequisitePhaseInstruction, nil
			}
			return "", nil
		}
		if shouldEnterFinalization(maxTokens, used, promptTokens, completionTokens, finalReplyReserveTokens, promptGrowthTokens) {
			armFinalization(turn, used, promptTokens, completionTokens, promptGrowthTokens)
			return finalInstruction, nil
		}
		return "", nil
	}

	if !prerequisiteSatisfied {
		if maxIterations < 2 {
			return usage(RunResult{}), fmt.Errorf("task %q lacks two iterations for required tool %q and its final response", packet.TaskID, packet.FinalizationPrerequisiteTool)
		}
		if maxIterations == 2 {
			armPrerequisite(0, 0, 0, 0, 0)
			messages = append(messages, v2.Message{Role: "user", Content: prerequisitePhaseInstruction})
		}
	}

	for i := 0; i < maxIterations; i++ {
		if err := ctx.Err(); err != nil {
			return usage(RunResult{}), err
		}
		if finalOnly && !canAffordFinalReply(maxTokens, totalPrompt+totalCompletion, lastPromptTokens, lastCompletionTokens, finalReplyReserveTokens, finalPromptGrowthTokens) {
			return usage(RunResult{Artifacts: artifacts.result()}), fmt.Errorf("task %q lacks budget for its reserved final response", packet.TaskID)
		}
		if prerequisiteOnly && !canAffordPrerequisiteAndFinal(maxTokens, totalPrompt+totalCompletion, lastPromptTokens, lastCompletionTokens, finalReplyReserveTokens, finalPromptGrowthTokens, futureGrowthReserve) {
			return usage(RunResult{Artifacts: artifacts.result()}), fmt.Errorf("task %q lacks budget for required tool %q and its reserved final response", packet.TaskID, packet.FinalizationPrerequisiteTool)
		}

		turn, err := llm(ctx, messages)
		if err != nil {
			return usage(RunResult{}), fmt.Errorf("model turn %d: %w", i+1, err)
		}
		iterations = i + 1
		totalPrompt += turn.PromptTokens
		totalCompletion += turn.CompletionTokens
		lastPromptTokens, lastCompletionTokens = turn.PromptTokens, turn.CompletionTokens
		messages = append(messages, v2.Message{Role: "assistant", Content: turn.Text})
		usedTokens := totalPrompt + totalCompletion
		if maxTokens > 0 && usedTokens >= maxTokens {
			summary := fmt.Sprintf("token budget %d exhausted after %d turns", maxTokens, i+1)
			return usage(RunResult{Summary: summary, Artifacts: artifacts.result()}), fmt.Errorf("%s", summary)
		}

		action := parse(turn.Text)
		log.Printf("[SUBAGENT] task=%s turn=%d response_bytes=%d response_json_valid=%t action_content_bytes=%d prompt_tokens=%d completion_tokens=%d action=%s tool=%q",
			packet.TaskID, i+1, len(turn.Text), json.Valid([]byte(strings.TrimSpace(turn.Text))), len(action.Content), turn.PromptTokens, turn.CompletionTokens, classifyAction(turn.Text, action), action.Tool)
		if action.Final {
			if !prerequisiteSatisfied {
				return usage(RunResult{Artifacts: artifacts.result()}), fmt.Errorf("task %q returned a final answer before required tool %q", packet.TaskID, packet.FinalizationPrerequisiteTool)
			}
			return usage(RunResult{Summary: strings.TrimSpace(action.Content), Artifacts: artifacts.result()}), nil
		}
		if finalOnly {
			return usage(RunResult{Artifacts: artifacts.result()}), fmt.Errorf("task %q did not return a final answer during its reserved finalization turn", packet.TaskID)
		}

		if action.Tool == "" {
			if prerequisiteOnly {
				return usage(RunResult{Artifacts: artifacts.result()}), fmt.Errorf("task %q did not request required tool %q during prerequisite finalization", packet.TaskID, packet.FinalizationPrerequisiteTool)
			}
			// Neither final nor a tool call — nudge and continue.
			messages = append(messages, v2.Message{Role: "user", Content: "Respond with a tool call or a final answer."})
			growth := len(finalInstruction)
			instruction, finalizationErr := considerFinalization(i+1, usedTokens, turn.PromptTokens, turn.CompletionTokens, growth)
			if finalizationErr != nil {
				return usage(RunResult{Artifacts: artifacts.result()}), finalizationErr
			}
			if instruction != "" {
				messages[len(messages)-1].Content = instruction
			}
			continue
		}
		toolCalls++
		if prerequisiteOnly && action.Tool != packet.FinalizationPrerequisiteTool {
			return usage(RunResult{Artifacts: artifacts.result()}), fmt.Errorf("task %q requested tool %q instead of required tool %q during prerequisite finalization", packet.TaskID, action.Tool, packet.FinalizationPrerequisiteTool)
		}

		// Per-agent tool scoping: deny out-of-role tools before execution and
		// feed the denial back so the agent adapts (not fatal).
		if r.scope != nil && !r.scope.Allowed(runtime, action.Tool) {
			if prerequisiteOnly {
				return usage(RunResult{Artifacts: artifacts.result()}), fmt.Errorf("task %q required tool %q was denied by role scope", packet.TaskID, action.Tool)
			}
			deniedToolCalls++
			messages = append(messages, v2.Message{Role: "user", Content: withBudgetNotice(fmt.Sprintf("Tool %q is not permitted for agent %q (role scope). Use a tool within your role or give your final answer.", action.Tool, runtime.AgentID), maxIterations-i-1, remainingTokens(maxTokens, usedTokens))})
			growth := len(messages[len(messages)-1].Content) + len(finalInstruction)
			instruction, finalizationErr := considerFinalization(i+1, usedTokens, turn.PromptTokens, turn.CompletionTokens, growth)
			if finalizationErr != nil {
				return usage(RunResult{Artifacts: artifacts.result()}), finalizationErr
			}
			if instruction != "" {
				messages = append(messages, v2.Message{Role: "user", Content: instruction})
			}
			continue
		}

		artifacts.record(action.Tool, action.Input)
		result, terr := exec(ctx, action.Tool, action.Input)
		log.Printf("[SUBAGENT] task=%s turn=%d tool=%q execution_error=%t result_bytes=%d",
			packet.TaskID, i+1, action.Tool, terr != nil, len(result))
		if terr != nil {
			if prerequisiteOnly {
				return usage(RunResult{Artifacts: artifacts.result()}), fmt.Errorf("task %q required tool %q failed: %w", packet.TaskID, action.Tool, terr)
			}
			// A tool failure is fed back so the agent can adapt, not fatal.
			messages = append(messages, v2.Message{Role: "user", Content: withBudgetNotice(fmt.Sprintf("Tool %q failed: %v. Try another approach or give your final answer.", action.Tool, terr), maxIterations-i-1, remainingTokens(maxTokens, usedTokens))})
		} else {
			messages = append(messages, v2.Message{Role: "user", Content: withBudgetNotice(truncate(fmt.Sprintf("Tool %q result:\n%s", action.Tool, result), maxToolFeedbackBytes), maxIterations-i-1, remainingTokens(maxTokens, usedTokens))})
		}
		growth := len(messages[len(messages)-1].Content) + len(finalInstruction)
		if prerequisiteOnly {
			prerequisiteOnly = false
			prerequisiteSatisfied = true
			armFinalization(i+1, usedTokens, turn.PromptTokens, turn.CompletionTokens, growth)
			messages = append(messages, v2.Message{Role: "user", Content: finalInstruction})
		} else if action.Tool == packet.FinalizationPrerequisiteTool && terr == nil {
			prerequisiteSatisfied = true
		}
		if !finalOnly {
			instruction, finalizationErr := considerFinalization(i+1, usedTokens, turn.PromptTokens, turn.CompletionTokens, growth)
			if finalizationErr != nil {
				return usage(RunResult{Artifacts: artifacts.result()}), finalizationErr
			}
			if instruction != "" {
				messages = append(messages, v2.Message{Role: "user", Content: instruction})
			}
		}

	}

	return usage(RunResult{Artifacts: artifacts.result()}), fmt.Errorf("task %q did not complete within %d iterations", packet.TaskID, maxIterations)
}

const (
	genericFinalizationInstruction = "The tool phase is now closed to preserve the remaining task budget. Return the required final answer using the task's exact final schema. Do not request another tool."
	maxToolFeedbackBytes           = 3000
	budgetNoticeGrowthReserve      = 512
)

func finalizationInstruction(contract string) string {
	instruction := genericFinalizationInstruction
	if contract = strings.TrimSpace(contract); contract != "" {
		instruction += "\n" + contract + "\nReturn exactly one JSON object. Do not wrap it in Markdown and do not add prose."
	}
	return instruction
}

func futureToolTurnGrowthReserve(finalInstruction, prerequisitePhaseInstruction string) int {
	return maxToolFeedbackBytes + budgetNoticeGrowthReserve + len(finalInstruction) + len(prerequisitePhaseInstruction)
}

func prerequisiteInstruction(tool string) string {
	if tool == "apply_patch_proposal" {
		return "The remaining task budget is reserved for the required governance action and final response. Request exactly the \"apply_patch_proposal\" tool now. Return exactly one JSON object with this envelope and no Markdown or prose: {\"type\":\"tool_request\",\"tool\":\"apply_patch_proposal\",\"input\":{\"patch\":\"<complete unified diff>\",\"base_sha\":\"<exact current git HEAD SHA>\"}}. The input object must contain exactly the non-empty string fields \"patch\" and \"base_sha\". Do not request another tool or return the final answer yet."
	}
	return fmt.Sprintf("The remaining task budget is reserved for the required governance action and final response. Request exactly the %q tool now. Do not request another tool or return the final answer yet.", tool)
}

func prerequisiteAndFinalEstimate(lastPromptTokens, lastCompletionTokens, replyReserveTokens, promptGrowthTokens, futureGrowthReserve int) int {
	nextPrompt := lastPromptTokens + lastCompletionTokens + promptGrowthTokens
	// The prerequisite reply is billed once as completion and then appears in
	// the final call's growing prompt. The final reply consumes the third reserve.
	return 2*nextPrompt + 3*replyReserveTokens + futureGrowthReserve
}

func canAffordPrerequisiteAndFinal(maxTokens, usedTokens, lastPromptTokens, lastCompletionTokens, replyReserveTokens, promptGrowthTokens, futureGrowthReserve int) bool {
	if maxTokens <= 0 || replyReserveTokens <= 0 {
		return true
	}
	return maxTokens-usedTokens >= prerequisiteAndFinalEstimate(lastPromptTokens, lastCompletionTokens, replyReserveTokens, promptGrowthTokens, futureGrowthReserve)
}

func shouldEnterPrerequisite(maxTokens, usedTokens, lastPromptTokens, lastCompletionTokens, replyReserveTokens, promptGrowthTokens, futureGrowthReserve int) bool {
	if maxTokens <= 0 || replyReserveTokens <= 0 {
		return false
	}
	remaining := maxTokens - usedTokens
	return remaining <= ordinaryPrerequisiteAndFinalEstimate(lastPromptTokens, lastCompletionTokens, replyReserveTokens, promptGrowthTokens, futureGrowthReserve)
}

func ordinaryPrerequisiteAndFinalEstimate(lastPromptTokens, lastCompletionTokens, replyReserveTokens, promptGrowthTokens, futureGrowthReserve int) int {
	nextPrompt := lastPromptTokens + lastCompletionTokens + promptGrowthTokens
	nextCompletion := max(lastCompletionTokens, replyReserveTokens)
	return nextPrompt + nextCompletion + prerequisiteAndFinalEstimate(
		nextPrompt,
		nextCompletion,
		replyReserveTokens,
		futureGrowthReserve,
		futureGrowthReserve,
	)
}

func finalReplyEstimate(lastPromptTokens, lastCompletionTokens, replyReserveTokens, promptGrowthTokens int) int {
	// The preceding completion becomes part of the next growing prompt; the
	// reserved completion is separately billed for the final call.
	return lastPromptTokens + lastCompletionTokens + promptGrowthTokens + replyReserveTokens
}

func canAffordFinalReply(maxTokens, usedTokens, lastPromptTokens, lastCompletionTokens, replyReserveTokens, promptGrowthTokens int) bool {
	if maxTokens <= 0 || replyReserveTokens <= 0 {
		return true
	}
	return maxTokens-usedTokens >= finalReplyEstimate(lastPromptTokens, lastCompletionTokens, replyReserveTokens, promptGrowthTokens)
}

func shouldEnterFinalization(maxTokens, usedTokens, lastPromptTokens, lastCompletionTokens, replyReserveTokens, promptGrowthTokens int) bool {
	if maxTokens <= 0 || replyReserveTokens <= 0 {
		return false
	}
	remaining := maxTokens - usedTokens
	// Reserve the next call for final output when one more ordinary turn plus
	// the final response would consume the remaining aggregate budget.
	return remaining <= lastPromptTokens+lastCompletionTokens+finalReplyEstimate(lastPromptTokens, lastCompletionTokens, replyReserveTokens, promptGrowthTokens)
}

func remainingTokens(maxTokens, usedTokens int) int {
	if maxTokens <= 0 {
		return -1
	}
	return max(0, maxTokens-usedTokens)
}

func withBudgetNotice(message string, remainingTurns, remainingTokenBudget int) string {
	if remainingTurns <= 0 {
		return message + "\nNo model turns remain after this response."
	}
	if remainingTokenBudget >= 0 {
		return fmt.Sprintf("%s\n%d model turns and %d aggregate tokens remain; finish as soon as you have enough evidence.", message, remainingTurns, remainingTokenBudget)
	}
	return fmt.Sprintf("%s\n%d model turns remain; finish as soon as you have enough evidence.", message, remainingTurns)
}

func classifyAction(text string, action Action) string {
	if action.Final {
		return "final"
	}
	if action.Tool != "" {
		return "tool"
	}
	if strings.TrimSpace(text) == "" {
		return "empty"
	}
	return "unrecognized"
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
