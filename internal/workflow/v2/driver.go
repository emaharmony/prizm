package v2

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/retry"
)

// driver.go wires the Natural Gates phase/gate abstractions to a live LLM and
// tool executor. Engine.Run() deliberately contains no LLM calls (it expects an
// external caller to drive ProcessLLMResponse); Drive() is that caller, made
// reusable so any trigger (scheduled wake, interactive prompt, API) shares one
// implementation instead of re-coding the loop.

// Message is a single chat message exchanged with the model.
type Message struct {
	Role    string // system | user | assistant
	Content string
}

// LLMFunc calls the model with the running transcript and returns the response
// text plus token usage. The caller owns provider/model selection.
type LLMFunc func(ctx context.Context, messages []Message) (text string, promptTokens, completionTokens int, err error)

// MarkLocalUsage encodes token usage from a loopback Ollama fallback for the
// legacy LLMFunc signature. Values are offset so a zero-usage response remains
// distinguishable from normal provider usage and can still be estimated.
func MarkLocalUsage(promptTokens, completionTokens int) (int, int) {
	return -(promptTokens + 1), -(completionTokens + 1)
}

func decodeLocalUsage(promptTokens, completionTokens int) (prompt, completion int, local bool) {
	if promptTokens >= 0 && completionTokens >= 0 {
		return promptTokens, completionTokens, false
	}
	if promptTokens < 0 {
		prompt = -promptTokens - 1
	} else {
		prompt = promptTokens
	}
	if completionTokens < 0 {
		completion = -completionTokens - 1
	} else {
		completion = completionTokens
	}
	return prompt, completion, true
}

// ToolFunc executes a tool request for the given phase and returns a result
// string to feed back to the model. A non-nil error means the tool failed.
type ToolFunc func(ctx context.Context, phase string, req *ToolRequest) (result string, err error)

// VerificationOutcome is the result of running a phase's verification profile.
// Ran distinguishes "the profile actually executed" from "could not be run"
// (unknown profile, executor error). A profile that could not run never blocks a
// phase — the loop should not deadlock because verification is misconfigured.
type VerificationOutcome struct {
	Ran      bool
	Passed   bool
	ExitCode int
	Summary  string // short, human-readable result (test failures, build errors)
}

// VerificationFunc runs a named validation profile and reports the outcome. The
// caller (wake handler) binds this to the V5 validation executor, which only runs
// allowlisted commands, so the model never controls what executes.
type VerificationFunc func(ctx context.Context, profile string) VerificationOutcome

// DriveOptions configure a single Drive run.
type DriveOptions struct {
	SystemPrompt        string
	UserPrompt          string
	StateDir            string        // where to persist workflow state (autosave + pauses)
	RepoPath            string        // repo path, surfaced to reviewers in feedback events
	ProjectID           string        // project id, surfaced to reviewers/dashboard events
	Channel             string        // notification channel for feedback events
	GetBranch           func() string // current git branch for EXECUTION branch protection; nil disables
	SkipPushRequirement bool          // true for local repos without a configured remote
}

// codeMutationTools are blocked while on a protected branch during EXECUTION.
var codeMutationTools = map[string]bool{
	"write_file": true, "create_directory": true, "git_add": true, "git_commit": true, "git_push": true,
}

// idempotentTools are read-only/side-effect-free, so a transient failure can be
// safely retried without risk of duplicating an effect. Mutations are deliberately
// excluded — retrying a git_commit could double-commit.
var idempotentTools = map[string]bool{
	"read_file": true, "list_dir": true, "search_files": true,
	"git_status": true, "git_log": true, "git_diff": true, "git_branch_list": true,
	"project_overview": true, "web_search": true, "memory_search": true,
}

// idempotentRetryConfig bounds transient-failure retries for read-only tools.
var idempotentRetryConfig = retry.RetryConfig{MaxRetries: 2, BaseDelay: 300 * time.Millisecond, MaxDelay: 3 * time.Second}

// executeTool runs a tool, retrying transient failures (timeout, connection reset,
// 503, …) for idempotent read-only tools only. Mutations and unknown tools run
// exactly once. The retry is transparent to the caller; each retry emits tool.retry.
func (e *Engine) executeTool(ctx context.Context, phaseName string, req *ToolRequest, tool ToolFunc) (string, error) {
	if !idempotentTools[req.Tool] {
		return tool(ctx, phaseName, req)
	}
	return retry.RetryWithBackoff(ctx, idempotentRetryConfig, func(attempt int) (string, error) {
		if attempt > 0 {
			e.emitEvent("tool.retry", map[string]any{"phase": phaseName, "tool": req.Tool, "attempt": attempt})
		}
		return tool(ctx, phaseName, req)
	})
}

// Drive runs the workflow end-to-end against a live LLM and tool executor.
// It honours phase gates, feedback pauses, iterate/loop-back via NextPhase, and
// EXECUTION safety enforcement (branch protection, read budget, commit/push).
func (e *Engine) Drive(ctx context.Context, llm LLMFunc, tool ToolFunc, opts DriveOptions) (*WorkflowState, error) {
	e.state.Status = StatusInProgress
	if e.state.StartedAt == "" {
		e.state.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	e.emitEvent("workflow.started", map[string]any{"workflow": e.config.Name})

	messages := []Message{
		{Role: "system", Content: opts.SystemPrompt},
		{Role: "user", Content: opts.UserPrompt},
	}

	maxTotal := e.config.Global.MaxTotalIterations
	if maxTotal <= 0 {
		maxTotal = 60
	}
	totalIter := 0

	// Wall-clock budget. An unparseable or empty MaxTotalTime means "no deadline".
	var deadline time.Time
	if e.config.Global.MaxTotalTime != "" {
		if d, derr := time.ParseDuration(e.config.Global.MaxTotalTime); derr == nil && d > 0 {
			deadline = time.Now().Add(d)
		} else if derr != nil {
			log.Printf("[V2] ignoring invalid max_total_time %q: %v", e.config.Global.MaxTotalTime, derr)
		}
	}

	maxRepeat := e.config.Global.MaxRepeatedToolCalls
	if maxRepeat <= 0 {
		maxRepeat = 6
	}

	// EXECUTION enforcement state.
	var hasWritten, hasCommitted, hasPushed bool
	readsInPhase := 0
	// repeats counts identical tool calls within the current phase to detect a
	// stuck loop (the model retrying the same action with no progress).
	repeats := map[string]int{}

	// runBudgetHit records the reason the run-wide ceiling (tokens or wall-clock)
	// tripped, if it did. It drives the terminal status (StatusBudgetExhausted) and
	// ensures a budget-killed phase isn't relabeled as a gate pass or re-emitted.
	var runBudgetHit string

	for e.state.CurrentPhaseIdx >= 0 && e.state.CurrentPhaseIdx < len(e.phases) {
		if reason := e.budgetExceeded(deadline); reason != "" {
			runBudgetHit = reason
			e.emitEvent("workflow.budget_exhausted", map[string]any{"phase": e.phases[e.state.CurrentPhaseIdx].Name(), "reason": reason})
			log.Printf("[V2] budget exhausted before phase: %s", reason)
			break
		}
		phase := e.phases[e.state.CurrentPhaseIdx]
		phaseName := phase.Name()

		e.emitEvent("phase.entered", map[string]any{"phase": phaseName})
		e.state.SetPhaseStatus(phaseName, PhaseStatusInProgress)
		if err := phase.Enter(ctx, e.state); err != nil {
			return e.state, fmt.Errorf("phase %s enter: %w", phaseName, err)
		}
		readsInPhase = 0
		for k := range repeats {
			delete(repeats, k)
		}
		if phaseName == "EXECUTION" {
			// Re-entry (after changes requested) must re-commit/re-push fresh work.
			hasCommitted, hasPushed = false, false
		}

		// Auto-approve: skip feedback gates entirely. The phase still enters and
		// exits (for state tracking) but never pauses for external input.
		if e.config.Global.AutoApprove && (phaseName == "FEEDBACK_PRE" || phaseName == "FEEDBACK_POST") {
			log.Printf("[V2] auto-approve: skipping %s gate", phaseName)
			e.state.Status = StatusInProgress
			e.state.PauseReason = ""
			e.recordGate(phaseName)
			if err := phase.Exit(ctx, e.state); err != nil {
				log.Printf("[V2] phase %s exit: %v", phaseName, err)
			}
			e.emitEvent("phase.exited", map[string]any{"phase": phaseName})
			if e.advance(phaseName) {
				continue
			}
			break
		}

		// Feedback phases pause for human/sub-agent input instead of calling the LLM.
		if e.state.Status == StatusPaused {
			pausePayload := map[string]any{
				"phase":     phaseName,
				"reason":    e.state.PauseReason,
				"run_id":    e.state.RunID,
				"repo_path": opts.RepoPath,
				"project":   opts.ProjectID,
				"channel":   opts.Channel,
			}
			var approvers, reviewers []string
			if cfg := e.config.GetPhase(phaseName); cfg != nil {
				approvers = cfg.Gate.Approvers
				reviewers = cfg.Gate.RequiredReviewers
				if len(approvers) > 0 {
					pausePayload["approvers"] = approvers
				}
				if len(reviewers) > 0 {
					pausePayload["required_reviewers"] = reviewers
				}
			}
			// Include the formatted plan/review package so a sub-agent reviewer
			// has the full context without re-deriving it. Approver/reviewer names
			// come from config (not hardcoded) so the gate adapts to any roster.
			switch phaseName {
			case "FEEDBACK_PRE":
				pausePayload["package"] = FormatPlanForApproval(e.state, approvers)
			case "FEEDBACK_POST":
				pausePayload["package"] = FormatReviewPackage(e.state, "", reviewers)
			}
			e.emitEvent("workflow.paused", pausePayload)
			// A separate feedback.requested event lets Discord/dashboard/sub-agent
			// reviewers know to act and reply on prizm.workflow.feedback.response.
			e.emitEvent("feedback.requested", pausePayload)
			if opts.StateDir != "" {
				_ = SaveWorkflowState(e.state, opts.StateDir)
				_ = SaveCurrentWorkflowState(e.state, opts.StateDir)
			}
			e.WaitForResume(ctx)
			if e.state.Status == StatusBlocked {
				return e.state, fmt.Errorf("workflow blocked at %s: %s", phaseName, e.state.PauseReason)
			}
			e.recordGate(phaseName)
			if err := phase.Exit(ctx, e.state); err != nil {
				log.Printf("[V2] phase %s exit: %v", phaseName, err)
			}
			e.emitEvent("phase.exited", map[string]any{"phase": phaseName})
			if e.advance(phaseName) {
				continue
			}
			break
		}

		messages = append(messages, Message{Role: "system", Content: e.phaseGuidance(phase)})

		completed := false
		// phaseBudgetHit records that the per-phase token cap stopped this phase
		// (soft: the run continues to the next phase). Reset each phase.
		phaseBudgetHit := false
	iterLoop:
		for iter := 0; iter < phase.MaxIterations() && totalIter < maxTotal; iter++ {
			if reason := e.budgetExceeded(deadline); reason != "" {
				runBudgetHit = reason
				e.emitEvent("workflow.budget_exhausted", map[string]any{"phase": phaseName, "reason": reason})
				log.Printf("[V2] budget exhausted in %s: %s", phaseName, reason)
				break iterLoop
			}
			// Per-phase token cap: softer than the run-wide budget — the phase
			// stops iterating and falls through to its fallback handling while
			// the run continues.
			if max := e.phaseMaxTokens(phaseName); max > 0 && e.state.GetPhaseTokens(phaseName) >= max {
				phaseBudgetHit = true
				e.emitEvent("phase.budget_exhausted", map[string]any{
					"phase": phaseName, "tokens": e.state.GetPhaseTokens(phaseName), "max": max,
				})
				log.Printf("[V2] phase %s token budget exhausted (%d >= %d)", phaseName, e.state.GetPhaseTokens(phaseName), max)
				break iterLoop
			}
			totalIter++
			e.state.IncrementPhaseIteration(phaseName)

			// Drain any external events (e.g. agent status) without blocking.
			select {
			case evt := <-e.externalEvent:
				e.handleExternalEvent(evt, phaseName)
			default:
			}

			// Handle delegations that blew their deadline: re-dispatch while retries
			// remain, otherwise mark failed so a dead sub-agent can't hold the gate.
			if e.delegation != nil {
				if timedOut := e.delegation.CheckTimeouts(e.state); len(timedOut) > 0 {
					var retried, failed []string
					for _, tid := range timedOut {
						if pkt, ok := e.delegation.RetryDelegation(tid, e.state); ok {
							if e.publishTask != nil {
								_ = e.publishTask(pkt)
							}
							retried = append(retried, tid)
						} else {
							e.state.FailDelegatedTask(tid)
							failed = append(failed, tid)
						}
					}
					if len(retried) > 0 {
						e.emitEvent("delegation.retry", map[string]any{"phase": phaseName, "tasks": retried})
					}
					if len(failed) > 0 {
						e.emitEvent("delegation.timeout", map[string]any{"phase": phaseName, "tasks": failed})
						messages = append(messages, Message{Role: "system", Content: fmt.Sprintf(
							"DELEGATION TIMEOUT: task(s) %v exhausted their retries and are now marked failed. Handle them yourself or revise the plan.", failed)})
					}
				}
			}

			// Inject pending gate guidance.
			for _, msg := range e.state.GetSystemMessages(phaseName) {
				messages = append(messages, Message{Role: "system", Content: msg})
			}

			text, pt, ct, err := llm(ctx, messages)
			pt, ct, localUsage := decodeLocalUsage(pt, ct)
			// Some providers/paths (e.g. certain Ollama/streaming responses) return
			// no usage. Estimate from message/response length (~4 chars/token) so a
			// 0-usage provider can't silently defeat the token ceiling.
			if pt == 0 && ct == 0 {
				ep, ec := estimatePromptTokens(messages), estimateTokens(text)
				if ep > 0 || ec > 0 {
					pt, ct = ep, ec
					log.Printf("[V2] %s: provider reported no token usage; estimated %d prompt + %d completion", phaseName, pt, ct)
				}
			}
			if localUsage {
				e.state.AddLocalTokens(pt, ct)
			} else {
				e.state.AddTokens(pt, ct)
				e.state.AddPhaseTokens(phaseName, pt, ct)
			}
			if err != nil {
				return e.state, fmt.Errorf("llm call in %s: %w", phaseName, err)
			}
			// Per-iteration token telemetry so live observers (e.g. `prizm watch`)
			// can render a budget burn-down. Purely observational; emitted only for
			// a successful call.
			e.emitEvent("phase.tokens", map[string]any{
				"phase":       phaseName,
				"prompt":      e.state.GetTotalPromptTokens(),
				"completion":  e.state.GetTotalCompletionTokens(),
				"total":       e.state.GetTotalTokens(),
				"max":         e.config.Global.MaxTotalTokens,
				"phase_total": e.state.GetPhaseTokens(phaseName),
				"phase_max":   e.phaseMaxTokens(phaseName),
				"local_usage": localUsage,
			})
			messages = append(messages, Message{Role: "assistant", Content: text})

			action, _ := phase.RunIteration(ctx, e.state, text)

			switch action.Type {
			case ActionToolCall:
				req := action.ToolRequest
				// Check enforcement BEFORE counting repeats — denied tools
				// should not trigger stuck-loop detection.
				if deny := e.enforceExecution(phaseName, req, opts, &hasWritten, &readsInPhase); deny != "" {
					messages = append(messages, Message{Role: "user", Content: deny})
					continue
				}
				// Stuck-loop detection: the same tool+input repeated with no progress
				// burns the budget. Nudge at the halfway mark, abort the phase at the cap.
				sig := toolSignature(req)
				repeats[sig]++
				if repeats[sig] >= maxRepeat {
					e.emitEvent("phase.stuck", map[string]any{"phase": phaseName, "tool": req.Tool, "repeats": repeats[sig]})
					log.Printf("[V2] phase %s stuck: %q repeated %d times; aborting phase", phaseName, req.Tool, repeats[sig])
					messages = append(messages, Message{Role: "system", Content: fmt.Sprintf(
						"STUCK: the identical action %q has been attempted %d times with no progress. Aborting this phase.", req.Tool, repeats[sig])})
					break iterLoop
				}
				if maxRepeat >= 4 && repeats[sig] == maxRepeat/2 {
					messages = append(messages, Message{Role: "system", Content: fmt.Sprintf(
						"NO PROGRESS: you have requested the identical action %q %d times. Change your approach — use a different tool or inputs, or finish.", req.Tool, repeats[sig])})
				}
				// run_validation lets the model self-check (build/test) on demand using
				// the same allowlisted runner as the verification gate, so it can catch
				// failures before committing instead of burning a commit→verify→fix cycle.
				if req.Tool == "run_validation" {
					messages = append(messages, Message{Role: "user", Content: e.handleRunValidation(ctx, phaseName, req)})
					continue
				}
				// delegate hands a planned task to another agent via the delegation
				// manager; the result is collected asynchronously as a task_complete
				// external event and reflected in the task_completion gate.
				if req.Tool == "delegate" {
					messages = append(messages, Message{Role: "user", Content: e.handleDelegate(ctx, req)})
					continue
				}
				result, terr := e.executeTool(ctx, phaseName, req, tool)
				log.Printf("[V2] tool %s in %s: err=%v result_len=%d hasWritten=%v hasCommitted=%v", req.Tool, phaseName, terr != nil, len(result), hasWritten, hasCommitted)
				switch req.Tool {
				case "write_file", "create_directory":
					if terr == nil {
						hasWritten = true
						log.Printf("[V2] hasWritten=true after %s", req.Tool)
					}
				case "git_commit":
					if terr == nil {
						hasCommitted = true
					}
				case "git_push":
					if terr == nil {
						hasPushed = true
					}
				}
				if terr != nil {
					messages = append(messages, Message{Role: "user", Content: fmt.Sprintf("Tool %q failed: %v. Try a different approach.", req.Tool, terr)})
					continue
				}
				toolMsg := truncate(fmt.Sprintf("Tool %q result:\n%s", req.Tool, result), 3000)
				// Mid-EXECUTION commit nudge: if files were written but not committed
				// after 8 tool calls, remind the model to commit.
				if phaseName == "EXECUTION" && hasWritten && !hasCommitted && totalIter >= 8 && totalIter%4 == 0 {
					toolMsg += "\n\n⚠️ **COMMIT REMINDER**: You have written files but have not committed. Call git_commit NOW, then git_push. Then continue working or emit EXECUTION_COMPLETE."
				}
				if phaseName == "EXECUTION" && req.Tool == "git_checkout" && !hasWritten {
					toolMsg += "\n\n**NEXT STEP: You MUST call write_file now to implement your changes. Do not read more files. Write the code, then git_add, git_commit, git_push, then EXECUTION_COMPLETE.**"
				}
				messages = append(messages, Message{Role: "user", Content: toolMsg})

			case ActionFinal, ActionPhaseComplete:
				log.Printf("[V2] %s phase action=%d in %s hasWritten=%v hasCommitted=%v hasPushed=%v GetBranch=%v", phaseName, action.Type, phaseName, hasWritten, hasCommitted, hasPushed, opts.GetBranch != nil)
				// Block completion if no writes were made during EXECUTION
				if phaseName == "EXECUTION" && !hasWritten && opts.GetBranch != nil {
					messages = append(messages, Message{Role: "system", Content: "WRITE REQUIRED: You MUST call write_file NOW. Respond with PURE JSON:\n{\"type\":\"tool_request\",\"tool\":\"write_file\",\"input\":{\"path\":\"<filepath>\",\"content\":\"<file content>\"}}\nAfter writing, call git_add, git_commit, git_push with the same JSON format. Then include EXECUTION_COMPLETE."})
					iter-- // don't burn budget
					continue
				}
				if reject := e.enforceCommitPush(phaseName, hasWritten, hasCommitted, hasPushed, opts.SkipPushRequirement); reject != "" {
					// Try auto-commit: if files were written and git_add was called
					// (staged changes exist), run git_commit + git_push automatically.
					if hasWritten && !hasCommitted {
						log.Printf("[V2] auto-commit: writing was detected but no commit. Attempting auto-commit...")
						commitResult, commitErr := e.executeTool(ctx, phaseName, &ToolRequest{
							Tool:  "git_commit",
							Input: map[string]any{"message": "auto: " + fmt.Sprintf("autonomous loop work (%s)", e.state.RunID)},
						}, tool)
						if commitErr == nil && commitResult != "" {
							hasCommitted = true
							messages = append(messages, Message{Role: "user", Content: fmt.Sprintf("Auto-committed: %s", truncate(commitResult, 500))})
							// Now try push
							if !hasPushed && !opts.SkipPushRequirement {
								pushResult, pushErr := e.executeTool(ctx, phaseName, &ToolRequest{
									Tool:  "git_push",
									Input: map[string]any{},
								}, tool)
								if pushErr == nil {
									hasPushed = true
									messages = append(messages, Message{Role: "user", Content: fmt.Sprintf("Auto-pushed: %s", truncate(pushResult, 500))})
								}
							}
							// Re-check enforcement after auto-commit
							if reject2 := e.enforceCommitPush(phaseName, hasWritten, hasCommitted, hasPushed, opts.SkipPushRequirement); reject2 == "" {
								// Enforcement passed, proceed to verification
								break
							}
						}
					}
					messages = append(messages, Message{Role: "system", Content: reject})
					iter-- // don't burn the budget on an enforcement bounce
					continue
				}
				// Objective build/test verification gate. A blocking failure feeds
				// the failure back to the model and forces a fresh commit/push of the
				// fix, consuming budget so a perpetually-failing build can't loop
				// forever (it falls through to the phase fallback instead).
				if e.runVerification(ctx, phaseName, &messages) {
					// V57: stop burning budget once verification has failed
					// MaxVerificationAttempts times — roll the run back instead.
					if e.verificationAttemptsExhausted() {
						e.doRollback(ctx, fmt.Sprintf("blocking verification failed %d times in %s", e.state.Verification.Attempts, phaseName))
						e.state.SetPhaseStatus(phaseName, PhaseStatusFallback)
						break iterLoop
					}
					hasCommitted, hasPushed = false, false
					continue
				}
				if e.gatePasses(phaseName) {
					completed = true
				} else {
					e.injectGateReason(phaseName, &messages)
				}
				if completed {
					break iterLoop
				}

			case ActionContinue:
				// Opportunistic gate check — PROBE/RESEARCH/PLAN parse state each turn.
				// In EXECUTION, block gate pass until code is written AND committed.
				if phaseName == "EXECUTION" {
					if !hasWritten {
						messages = append(messages, Message{Role: "system", Content: "WRITE REQUIRED: You MUST call write_file NOW. Respond with PURE JSON:\n{\"type\":\"tool_request\",\"tool\":\"write_file\",\"input\":{\"path\":\"<filepath>\",\"content\":\"<file content>\"}}\nAfter writing, call git_add, git_commit, git_push with the same JSON format. Then include EXECUTION_COMPLETE."})
						continue
					}
					if hasWritten && !hasCommitted {
						messages = append(messages, Message{Role: "system", Content: "COMMIT REQUIRED: You wrote files but have not committed. Respond with PURE JSON:\n{\"type\":\"tool_request\",\"tool\":\"git_commit\",\"input\":{\"message\":\"<commit message>\"}}\nThen call git_push with JSON. Then include EXECUTION_COMPLETE."})
						continue
					}
				}
				if e.gatePasses(phaseName) {
					completed = true
					break iterLoop
				}
			}
		}

		// Run-wide ceiling cut this phase off mid-flight: end the run here without
		// masking it as a gate pass / iteration exhaustion, and without re-checking
		// (and re-emitting) the budget event at the top of the outer loop.
		if runBudgetHit != "" {
			if !e.state.RolledBack() {
				e.state.SetPhaseStatus(phaseName, PhaseStatusFallback)
			}
			if err := phase.Exit(ctx, e.state); err != nil {
				log.Printf("[V2] phase %s exit: %v", phaseName, err)
			}
			e.emitEvent("phase.exited", map[string]any{"phase": phaseName})
			break
		}

		if !completed && !e.state.RolledBack() {
			if e.gatePasses(phaseName) {
				completed = true
			} else {
				reason := "max_iterations_reached"
				if phaseBudgetHit {
					reason = "phase_token_budget_exhausted"
				}
				e.emitEvent("phase.fallback", map[string]any{"phase": phaseName, "reason": reason})
				e.state.SetPhaseStatus(phaseName, PhaseStatusFallback)
				if cfg := e.config.GetPhase(phaseName); cfg != nil && cfg.Fallback.Blocks {
					// V57: a blocking-phase fallback ends the run — discard its
					// work first when auto-rollback is on.
					if e.config.Global.AutoRollback {
						e.doRollback(ctx, fmt.Sprintf("blocking phase %s did not pass its gate", phaseName))
					}
					e.state.Status = StatusBlocked
					e.emitEvent("workflow.blocked", map[string]any{"phase": phaseName})
					if opts.StateDir != "" {
						_ = SaveWorkflowState(e.state, opts.StateDir)
						_ = SaveCurrentWorkflowState(e.state, opts.StateDir)
					}
					return e.state, fmt.Errorf("blocking phase %s did not pass its gate", phaseName)
				}
			}
		}

		if err := phase.Exit(ctx, e.state); err != nil {
			log.Printf("[V2] phase %s exit: %v", phaseName, err)
		}
		e.emitEvent("phase.exited", map[string]any{"phase": phaseName})

		// V57: a rolled-back run must not keep executing later phases. Re-stamp
		// the phase as fallback — ExecutionPhase.Exit unconditionally marks it
		// completed, which would misreport a discarded run.
		if e.state.RolledBack() {
			e.state.SetPhaseStatus(phaseName, PhaseStatusFallback)
			break
		}
		if !e.advance(phaseName) {
			break
		}
		if totalIter >= maxTotal {
			log.Printf("[V2] total iteration budget (%d) exhausted", maxTotal)
			break
		}
	}

	// V57: a run that ends in a failing state (budget exhausted or loop ended
	// with a blocking verification still red) is discarded, not shipped.
	if e.config.Global.AutoRollback && !e.state.RolledBack() {
		if v := e.state.Verification; v != nil && !v.Passed {
			e.doRollback(ctx, "run ended with failing verification")
		}
	}

	switch {
	case e.state.RolledBack():
		e.state.Status = StatusRolledBack
	case runBudgetHit != "":
		e.state.Status = StatusBudgetExhausted
	default:
		e.state.Status = StatusCompleted
	}
	e.state.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	e.emitEvent("workflow.completed", map[string]any{"workflow": e.config.Name, "status": string(e.state.Status)})
	if opts.StateDir != "" {
		_ = SaveWorkflowState(e.state, opts.StateDir)
		_ = SaveCurrentWorkflowState(e.state, opts.StateDir)
	}
	return e.state, nil
}

// advance moves to the next phase, honouring NextPhase loop-back (iterate).
// Returns false when there is no further phase to run.
func (e *Engine) advance(fromPhase string) bool {
	if next := e.state.NextPhase; next != "" {
		if idx, ok := e.phaseMap[next]; ok {
			e.state.NextPhase = ""
			e.emitEvent("workflow.iteration", map[string]any{"from": fromPhase, "to": next})
			e.state.CurrentPhaseIdx = idx
			return true
		}
		e.state.NextPhase = ""
	}
	e.state.CurrentPhaseIdx++
	return e.state.CurrentPhaseIdx < len(e.phases)
}

// gatePasses evaluates the registered gate for a phase and records the result.
func (e *Engine) gatePasses(phaseName string) bool {
	gate, ok := e.gates[phaseName]
	if !ok {
		return true // no gate → phase completes on its own signal
	}
	result := gate.Evaluate(e.state)
	e.emitEvent("phase.gate_check", map[string]any{
		"phase": phaseName, "passed": result.Passed, "score": result.Score, "reason": result.Reason,
	})
	if result.Passed {
		e.state.SetPhaseStatus(phaseName, PhaseStatusCompleted)
		e.state.SetPhaseGateResult(phaseName, result)
	}
	return result.Passed
}

// recordGate evaluates and records a gate result without changing phase status
// (used after a feedback pause resumes).
func (e *Engine) recordGate(phaseName string) {
	if gate, ok := e.gates[phaseName]; ok {
		result := gate.Evaluate(e.state)
		e.state.SetPhaseGateResult(phaseName, result)
		e.emitEvent("phase.gate_check", map[string]any{
			"phase": phaseName, "passed": result.Passed, "score": result.Score, "reason": result.Reason,
		})
	}
}

// injectGateReason queues the gate's reason as guidance for the next iteration.
func (e *Engine) injectGateReason(phaseName string, messages *[]Message) {
	if gate, ok := e.gates[phaseName]; ok {
		if result := gate.Evaluate(e.state); result.Reason != "" {
			*messages = append(*messages, Message{Role: "system", Content: "GATE NOT MET: " + result.Reason})
		}
	}
}

// remainingRunTokens returns the delegated task ceiling inherited from the parent
// run. -1 means unlimited, 0 means no budget remains, positive is remaining tokens.
func (e *Engine) remainingRunTokens() int {
	if e == nil || e.config == nil || e.state == nil {
		return 0
	}
	max := e.config.Global.MaxTotalTokens
	if max == UnlimitedTokens {
		return UnlimitedTokens
	}
	if max <= 0 {
		return 0
	}
	used := e.state.GetTotalTokens()
	if used >= max {
		return 0
	}
	return max - used
}

// phaseMaxTokens returns a phase's configured token cap (0 = none).
func (e *Engine) phaseMaxTokens(phaseName string) int {
	if cfg := e.config.GetPhase(phaseName); cfg != nil {
		return cfg.MaxTokens
	}
	return 0
}

// estimateTokens approximates a token count from character length (~4 chars/token).
// Used only as a fallback when a provider reports no usage, so the budget stays
// meaningful rather than silently no-op.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// estimatePromptTokens approximates prompt tokens from all message content.
func estimatePromptTokens(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
	}
	return (n + 3) / 4
}

// doRollback fires the injected V57 rollback runner (if any), emits the
// workflow.rollback event, and records the outcome on the state. Idempotent:
// a second call is a no-op once a rollback has been recorded.
func (e *Engine) doRollback(ctx context.Context, reason string) {
	if e.rollback == nil || e.state.Rollback != nil {
		return
	}
	e.emitEvent("workflow.rollback", map[string]any{"reason": reason})
	log.Printf("[V2] auto-rollback: %s", reason)
	if err := e.rollback(ctx, reason); err != nil {
		log.Printf("[V2] rollback FAILED (branch may still hold bad commits): %v", err)
		e.state.SetRollback(reason, err.Error())
		return
	}
	e.state.SetRollback(reason, "")
}

// verificationAttemptsExhausted reports whether auto-rollback should fire
// because a blocking verification has failed MaxVerificationAttempts times.
func (e *Engine) verificationAttemptsExhausted() bool {
	if !e.config.Global.AutoRollback {
		return false
	}
	max := e.config.Global.MaxVerificationAttempts
	if max <= 0 {
		max = 3
	}
	v := e.state.Verification
	return v != nil && !v.Passed && v.Attempts >= max
}

// runVerification runs the phase's configured verification profile (if any) and
// records the outcome on the state. It returns true only when verification ran,
// failed, and the phase configured it as blocking — signalling the caller to keep
// iterating (so the model fixes the failure) rather than complete the phase.
//
// It is a no-op (returns false) when the phase has no verification configured, no
// runner is wired, or the profile could not be executed — verification never
// deadlocks the loop on misconfiguration.
func (e *Engine) runVerification(ctx context.Context, phaseName string, messages *[]Message) bool {
	cfg := e.config.GetPhase(phaseName)
	if cfg == nil || cfg.Verification == nil || strings.TrimSpace(cfg.Verification.Profile) == "" {
		return false
	}
	if e.verify == nil {
		return false
	}
	profile := cfg.Verification.Profile
	outcome := e.verify(ctx, profile)
	if !outcome.Ran {
		log.Printf("[V2] verification profile %q could not run in %s; skipping", profile, phaseName)
		return false
	}
	e.state.SetVerification(profile, outcome.Passed, outcome.ExitCode, outcome.Summary)
	e.emitEvent("phase.verification", map[string]any{
		"phase": phaseName, "profile": profile, "passed": outcome.Passed,
		"exit_code": outcome.ExitCode,
	})
	if outcome.Passed {
		return false
	}
	*messages = append(*messages, Message{Role: "system", Content: fmt.Sprintf(
		"VERIFICATION FAILED: profile %q exited %d. The code you committed does not pass. Fix the failure below, then commit and push the fix.\n%s",
		profile, outcome.ExitCode, truncate(outcome.Summary, 2000))})
	return cfg.Verification.Blocking
}

// handleRunValidation services a model-issued run_validation tool call by running
// a validation profile through the same injected runner the verification gate
// uses, recording the result on the state, and returning a model-readable summary.
// The profile comes from the call's "profile" input or falls back to the phase's
// configured verification profile.
func (e *Engine) handleRunValidation(ctx context.Context, phaseName string, req *ToolRequest) string {
	if e.verify == nil {
		return "run_validation is not available in this environment."
	}
	profile := ""
	if req != nil {
		if p, ok := req.Input["profile"].(string); ok {
			profile = strings.TrimSpace(p)
		}
	}
	if profile == "" {
		if cfg := e.config.GetPhase(phaseName); cfg != nil && cfg.Verification != nil {
			profile = cfg.Verification.Profile
		}
	}
	if profile == "" {
		return "run_validation: specify a \"profile\" (the phase has no default verification profile)."
	}
	outcome := e.verify(ctx, profile)
	if !outcome.Ran {
		return fmt.Sprintf("run_validation: profile %q could not be run here.", profile)
	}
	e.state.SetVerification(profile, outcome.Passed, outcome.ExitCode, outcome.Summary)
	e.emitEvent("phase.verification", map[string]any{
		"phase": phaseName, "profile": profile, "passed": outcome.Passed,
		"exit_code": outcome.ExitCode, "source": "tool",
	})
	status := "PASSED"
	if !outcome.Passed {
		status = "FAILED"
	}
	return fmt.Sprintf("run_validation %q: %s (exit %d).\n%s", profile, status, outcome.ExitCode, truncate(outcome.Summary, 2000))
}

// handleDelegate services a model-issued delegate tool call: it resolves the named
// plan task, records a delegation (marking the task in_progress), publishes the
// task packet if a transport is wired, and returns a model-readable acknowledgement.
// The delegated result arrives later as a task_complete external event.
func (e *Engine) handleDelegate(ctx context.Context, req *ToolRequest) string {
	if e.delegation == nil {
		return "delegate is not available in this environment."
	}
	if e.state.Plan == nil {
		return "delegate: no plan exists yet; create tasks before delegating."
	}
	taskID := ""
	if req != nil {
		if t, ok := req.Input["task_id"].(string); ok {
			taskID = strings.TrimSpace(t)
		}
	}
	if taskID == "" {
		return "delegate: specify the \"task_id\" of a planned task to delegate."
	}
	var task *PlanTask
	e.state.mu.RLock()
	for i := range e.state.Plan.Tasks {
		if e.state.Plan.Tasks[i].ID == taskID {
			t := e.state.Plan.Tasks[i]
			task = &t
			break
		}
	}
	e.state.mu.RUnlock()
	if task == nil {
		return fmt.Sprintf("delegate: no task %q in the plan.", taskID)
	}
	if task.Agent == "" {
		return fmt.Sprintf("delegate: task %q has no agent assigned; set an agent in the plan first.", taskID)
	}
	// When the workflow knows its agent roster, validate the target up front so we
	// don't delegate into the void. When the roster is empty (agents resolved
	// dynamically from the running config), skip the check and proceed.
	if known := e.state.RegisteredAgents(); len(known) > 0 {
		info, ok := e.state.LookupAgent(task.Agent)
		if !ok {
			return fmt.Sprintf("delegate: agent %q is not registered. Known agents: %s.", task.Agent, strings.Join(known, ", "))
		}
		if info.Availability == "offline" {
			return fmt.Sprintf("delegate: agent %q is offline. Assign an available agent or handle the task yourself.", task.Agent)
		}
	}
	remainingTokens := e.remainingRunTokens()
	if remainingTokens == 0 {
		return fmt.Sprintf("delegate: token budget exhausted before task %q could be delegated.", taskID)
	}
	del, packet, err := e.delegation.DelegateTask(ctx, *task, e.state)
	if err != nil {
		return fmt.Sprintf("delegate: failed to delegate %q: %v", taskID, err)
	}
	packet.MaxTokens = remainingTokens
	published := "recorded (no transport configured)"
	if e.publishTask != nil {
		if perr := e.publishTask(packet); perr != nil {
			return fmt.Sprintf("delegate: recorded delegation %s but publish failed: %v", del.DelegationID, perr)
		}
		published = "dispatched"
	}
	e.emitEvent("task.delegated", map[string]any{
		"task_id": taskID, "agent": task.Agent, "delegation_id": del.DelegationID,
	})
	return fmt.Sprintf("delegate: task %q %s to agent %q (deadline %s). Continue with other work; its result will arrive as a completion.",
		taskID, published, task.Agent, packet.Deadline)
}

// toolSignature is a stable key for a tool call. fmt's %v renders map keys in
// sorted order, so identical (tool, input) pairs hash to the same string
// regardless of input map ordering.
func toolSignature(req *ToolRequest) string {
	if req == nil {
		return ""
	}
	return fmt.Sprintf("%s|%v", req.Tool, req.Input)
}

// budgetExceeded reports a non-empty reason when the run has hit its wall-clock or
// token ceiling. Both are soft stops: the loop finalizes gracefully (after emitting
// workflow.budget_exhausted) rather than running unbounded — the safeguard an
// autonomous loop needs so a stuck or expensive run can't burn time or tokens
// without limit. A zero deadline disables the time dimension; zero MaxTotalTokens
// is normalized to the default cap when engines are constructed.
func (e *Engine) budgetExceeded(deadline time.Time) string {
	if !deadline.IsZero() && time.Now().After(deadline) {
		return fmt.Sprintf("max_total_time exceeded (%s)", e.config.Global.MaxTotalTime)
	}
	if max := e.config.Global.MaxTotalTokens; max > 0 {
		if used := e.state.GetTotalTokens(); used >= max {
			return fmt.Sprintf("max_total_tokens exceeded (%d/%d)", used, max)
		}
	}
	return ""
}

// enforceExecution applies EXECUTION-phase safety (branch protection + read budget).
// Returns a non-empty denial message when the tool call must be blocked.
func (e *Engine) enforceExecution(phaseName string, req *ToolRequest, opts DriveOptions, hasWritten *bool, readsInPhase *int) string {
	if phaseName != "EXECUTION" {
		return ""
	}
	if codeMutationTools[req.Tool] && opts.GetBranch != nil {
		branch := opts.GetBranch()
		if branch == "main" || branch == "master" {
			return fmt.Sprintf("BRANCH PROTECTION: you are on %q. Create a feature branch (git_checkout create=true) before writing or committing.", branch)
		}
	}
	if req.Tool == "read_file" || req.Tool == "list_dir" || req.Tool == "search_files" || req.Tool == "git_diff" || req.Tool == "git_status" || req.Tool == "git_log" || req.Tool == "git_branch_list" {
		*readsInPhase++
		if *readsInPhase > 3 && !*hasWritten {
			return "READ BUDGET EXCEEDED: You have read enough. You MUST call write_file NOW. Respond with PURE JSON:\n{\"type\":\"tool_request\",\"tool\":\"write_file\",\"input\":{\"path\":\"<filepath>\",\"content\":\"<file content>\"}}\nAfter writing, call git_add, git_commit, git_push with the same JSON format. Then include EXECUTION_COMPLETE."
		}
	}
	return ""
}

// enforceCommitPush rejects a premature final/complete in EXECUTION when work
// has been written but not committed and pushed.
func (e *Engine) enforceCommitPush(phaseName string, hasWritten, hasCommitted, hasPushed bool, skipPushRequirement bool) string {
	if phaseName != "EXECUTION" {
		return ""
	}
	if hasWritten && !hasCommitted {
		return "COMMIT REQUIRED: you wrote files but have not committed. Use git_add then git_commit before finishing."
	}
	if hasCommitted && !hasPushed && !skipPushRequirement {
		return "PUSH REQUIRED: you committed but have not pushed. Use git_push before finishing."
	}
	return ""
}

// phaseGuidance builds the per-phase system instruction injected on entry.
func (e *Engine) phaseGuidance(phase Phase) string {
	signal := map[string]string{
		"PROBE":     "PROBE_COMPLETE",
		"RESEARCH":  "RESEARCH_COMPLETE",
		"PLAN":      "PLAN_COMPLETE",
		"EXECUTION": "You MUST write code now. Call write_file to implement your changes. Do not read more files. After writing, call git_add, git_commit, and git_push. Then emit EXECUTION_COMPLETE.",
		"REPORT":    "REPORT_COMPLETE",
	}[phase.Name()]

	var b strings.Builder
	fmt.Fprintf(&b, "[PHASE: %s] %s\n", phase.Name(), phase.Description())
	if tools := phase.AllowedTools(); len(tools) > 0 {
		fmt.Fprintf(&b, "Allowed tools: %s\n", strings.Join(tools, ", "))
	}
	b.WriteString("Respond with PURE JSON: a tool request {\"type\":\"tool_request\",\"tool\":\"...\",\"input\":{...}} or {\"type\":\"final\",\"content\":\"...\"}.\n")
	if signal != "" {
		fmt.Fprintf(&b, "When this phase is done, include the token %s in your response.\n", signal)
	}
	switch phase.Name() {
	case "PROBE":
		b.WriteString("Declare assumptions as: ASSUMPTION: {statement} | confidence: {0.0-1.0} | criticality: {blocker|high|medium|low}\n")
	case "RESEARCH":
		b.WriteString("Declare confidence as: CONFIDENCE: {domain} | {0.0-1.0} | reason: {why}\n")
	case "PLAN":
		b.WriteString("Declare tasks as: TASK: {id} | description: {what} | agent: {who} | success: {criteria}\n")
	case "EXECUTION":
		b.WriteString("Before declaring done, call run_validation to build/test your changes; fix any failures, then commit and push.\n")
	case "REPORT":
		b.WriteString("Include sections: ## Change Summary, ## Proof of Work, ## Impact, ## Next Steps, ## Learnings\n")
	}
	return b.String()
}

// truncate shortens long strings for transcript injection.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-200] + fmt.Sprintf("\n... [truncated %d chars] ...\n", len(s)-max) + s[len(s)-200:]
}
