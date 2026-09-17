// Package run implements the lifecycle orchestrator for Prizm — the central engine
// that takes a task, runs the full V1→V5 pipeline, and produces an event log.
//
// What is Prizm? An event-native AI agent platform. Every user request becomes a
// "run" that flows through a deterministic lifecycle of events published to NATS.
//
// The V1→V5 versioning represents the platform's evolution, not API versions:
//
//	V1 — Mock agent + event emission (the skeleton)
//	V2 — Real LLM integration (Ollama provider, dry-run mode, context injection)
//	V3 — Controlled tool execution (the agent can call tools; policy decides what runs)
//	V4 — Approval-gated mutations (tools that modify files require human approval)
//	V5 — Validation + deterministic review (run tests, lint, get a verdict)
//
// A single Runner instance represents one invocation. It emits ~15+ events in
// sequence, each with a parent chain for tracing. Everything is persisted as
// JSONL (events), JSON (summary), and Markdown (prompt/output/review artifacts).
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/prompt"
	"github.com/emaharmony/prizm/internal/provider"
	mockpkg "github.com/emaharmony/prizm/internal/provider/mock"
	"github.com/emaharmony/prizm/internal/remembrance"
	"github.com/emaharmony/prizm/internal/review"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/validation"

	agentpkg "github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/mutation"
)

// RunConfig holds the configuration for a run.
type RunConfig struct {
	Task string
	// WorkspaceContext is pre-built workspace context (V19 --context flags)
	// injected into the prompt ahead of any Remembrance context.
	WorkspaceContext string
	Project          string
	Agent            string
	BusURL           string
	MemoryEnabled    bool
	RequireMemory    bool
	MemoryURL        string
	RunDir           string // Base directory for run outputs (default: ./runs)

	// V2 LLM provider configuration
	Provider     provider.Provider
	ProviderName string // Human-readable provider name for output/summaries
	Model        string
	Temperature  float64
	MaxTokens    int
	Timeout      time.Duration
	DryRunPrompt bool // If true, build prompt and artifacts but skip LLM call

	// V3 tool execution configuration
	ToolRegistry *tool.Registry
	ToolPolicy   tool.PolicyConfig
	ToolExecutor *tool.Executor

	// V4 approval-gated mutation configuration
	ApprovalStoreDir     string // directory for approval artifacts (default: derived from RunDir)
	MutationAllowedPaths []string

	// V5 validation and review configuration
	ValidationRegistry *validation.Registry
	Reviewer           *review.Reviewer
}

// RunResult holds the result of a completed run.
type RunResult struct {
	RunID       string `json:"run_id"`
	Status      string `json:"status"`
	EventCount  int    `json:"event_count"`
	EventsPath  string `json:"events_path"`
	SummaryPath string `json:"summary_path"`
	Error       string `json:"error,omitempty"`

	// V2 fields for CLI output
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	PromptPath string `json:"prompt_path,omitempty"`
	OutputPath string `json:"output_path,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	DryRun     bool   `json:"dry_run,omitempty"`

	// V3 tool execution result
	ToolCallResult *tool.ToolResult `json:"tool_result,omitempty"`

	// V5 validation and review results
	ValidationResults  []validation.Result `json:"validation_results,omitempty"`
	Review             *review.Review      `json:"review,omitempty"`
	ReviewArtifactPath string              `json:"review_artifact_path,omitempty"`
}

// Runner orchestrates a single run's complete lifecycle.
// It owns the event slice, NATS connection, and all sub-components needed
// to take a task from creation through to persisted artifacts.
type Runner struct {
	config        RunConfig
	runID         string
	correlationID string
	sessionID     string
	events        []event.Event
	nc            *nats.Conn
	js            nats.JetStreamContext
	memClient     *remembrance.Client
	startedAt     string
	taskStartedID string // ID of the task.started event for linking failure events
}

// NewRunner creates a new lifecycle runner.
func NewRunner(config RunConfig) *Runner {
	return &Runner{
		config: config,
		events: make([]event.Event, 0),
	}
}

// Run executes the complete lifecycle pipeline. Here's the 15-step overview:
//
//  1. Connect to NATS (event bus)
//  2. Emit task.created
//  3. Emit task.started (parent: task.created)
//  4. Optional: Fetch Remembrance context (memory), emit V1+V2 context events
//  5. Emit agent.started
//  6. Build & write prompt.md artifact
//  7. [dry-run gate] If --dry-run-prompt, stop here and complete
//  8. Emit llm.requested
//  9. Call the LLM provider with timeout
//  10. Emit llm.completed (or llm.failed → abort)
//     10b. V3: Parse LLM output for tool requests, execute if present
//     10c. V4: If tool requires approval, emit mutation.proposed + approval.requested
//  11. Emit agent.completed (V1 backward compat)
//  12. Write output.md artifact
//  13. Determine run status (completed or pending_approval)
//  14. Emit task.completed
//  15. Persist event log (events.jsonl) and summary (summary.json)
//
// Returns a RunResult and an error if the lifecycle failed.
func (r *Runner) Run() (*RunResult, error) {
	// Generate IDs
	r.runID = event.NewRunID()
	r.correlationID = event.NewCorrelationID()
	r.sessionID = event.NewSessionID()
	r.startedAt = time.Now().UTC().Format(time.RFC3339Nano)

	log.Printf("prizm: starting run %s", r.runID)

	// Default provider for V1 backward compat
	if r.config.Provider == nil {
		r.config.Provider = mockpkg.New()
		if r.config.ProviderName == "" {
			r.config.ProviderName = "mock"
		}
	}
	if r.config.Model == "" {
		r.config.Model = "mock-model"
	}
	if r.config.Temperature == 0 {
		r.config.Temperature = 0.2
	}
	if r.config.MaxTokens == 0 {
		r.config.MaxTokens = 2048
	}
	if r.config.Timeout == 0 {
		r.config.Timeout = 20 * time.Minute
	}

	// V3: Wire tool executor event emitter into the runner's event system
	if r.config.ToolExecutor != nil {
		r.config.ToolExecutor.SetEmitter(func(eventType, source string, payload map[string]any) {
			r.emit(eventType, source, payload)
		})
	}

	// 1. Connect to NATS
	nc, err := nats.Connect(r.config.BusURL, nats.Name(fmt.Sprintf("prizm-run-%s", r.runID)))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}
	r.nc = nc
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("failed to init JetStream: %w", err)
	}
	r.js = js

	// Ensure the PRIZM stream exists
	r.ensureStream()

	// 2. Emit task.created
	evt := r.emit(event.V1EventTypes.TaskCreated, "prizm-cli", map[string]any{
		"task":    r.config.Task,
		"project": r.config.Project,
		"agent":   r.config.Agent,
	})

	// 3. Emit task.started
	evt = r.emitWithParent(event.V1EventTypes.TaskStarted, "prizm-cli", map[string]any{
		"task":    r.config.Task,
		"project": r.config.Project,
		"agent":   r.config.Agent,
	}, evt.ID)
	r.taskStartedID = evt.ID

	// 4. Optional: Remembrance context hook
	// Remembrance is Prizm's memory service — it fetches relevant past context
	// for the current task. When enabled, we emit BOTH V1 and V2 context events.
	// Why duplicate? V1 (`prizm.memory.context_requested`) exists for backward
	// compatibility with early consumers. V2 (`prizm.context.requested`) is the
	// canonical new-style event. New consumers should listen to V2 events only.
	var contextStr string
	memoryStatus := "none" // "none", "injected", "failed"

	if r.config.MemoryEnabled {
		r.memClient = remembrance.NewClient(r.config.MemoryURL)

		// Emit V1 memory.context_requested (backward compat)
		r.emitWithParent(event.V1EventTypes.MemoryContextRequested, "prizm-cli", map[string]any{
			"task":    r.config.Task,
			"project": r.config.Project,
			"agent":   r.config.Agent,
		}, evt.ID)

		// Emit V2 context.requested
		r.emitWithParent(event.V2EventTypes.ContextRequested, "prizm-cli", map[string]any{
			"task":    r.config.Task,
			"project": r.config.Project,
			"agent":   r.config.Agent,
		}, evt.ID)

		ctxResp, err := r.memClient.BuildContext(r.config.Task, r.config.Project, r.config.Agent, remembrance.DefaultContextMaxTokens)
		if err != nil {
			// Memory failed
			log.Printf("prizm: remembrance context failed: %v", err)
			memoryStatus = "failed"

			// V1 backward compat
			r.emitWithParent(event.V1EventTypes.MemoryContextFailed, "prizm-cli", map[string]any{
				"task":  r.config.Task,
				"error": err.Error(),
			}, evt.ID)

			// V2 context.failed
			r.emitWithParent(event.V2EventTypes.ContextFailed, "prizm-cli", map[string]any{
				"task":  r.config.Task,
				"error": err.Error(),
			}, evt.ID)

			if r.config.RequireMemory {
				return r.fail(fmt.Sprintf("remembrance context required but failed: %v", err))
			}
			// Continue without context
			contextStr = ""
		} else if ctxResp != nil {
			// Memory succeeded
			// ContextPackResponse has ContextMarkdown (ready-to-inject)
			// and ContextJSON with structured memory data
			if ctxResp.ContextMarkdown != "" {
				contextStr = ctxResp.ContextMarkdown
				memoryStatus = "injected"
				log.Printf("prizm: remembrance context built (%d sources, markdown)", len(ctxResp.SelectedMemories))
			} else if ctxResp.ContextJSON != nil && len(ctxResp.ContextJSON.Memories) > 0 {
				var contextParts []string
				for _, mem := range ctxResp.ContextJSON.Memories {
					if mem.Summary != "" {
						contextParts = append(contextParts, mem.Summary)
					}
				}
				contextStr = strings.Join(contextParts, "\n\n")
				memoryStatus = "injected"
			}

			// V1 backward compat
			r.emitWithParent(event.V1EventTypes.MemoryContextBuilt, "prizm-cli", map[string]any{
				"task":          r.config.Task,
				"sources_count": len(ctxResp.SelectedMemories),
			}, evt.ID)

			// V2 context.injected
			r.emitWithParent(event.V2EventTypes.ContextInjected, "prizm-cli", map[string]any{
				"task":          r.config.Task,
				"sources_count": len(ctxResp.SelectedMemories),
			}, evt.ID)
		} else {
			// No context available (404)
			log.Printf("prizm: no remembrance context available")
			memoryStatus = "failed"

			r.emitWithParent(event.V1EventTypes.MemoryContextFailed, "prizm-cli", map[string]any{
				"task":  r.config.Task,
				"error": "no context available",
			}, evt.ID)

			r.emitWithParent(event.V2EventTypes.ContextFailed, "prizm-cli", map[string]any{
				"task":  r.config.Task,
				"error": "no context available",
			}, evt.ID)

			if r.config.RequireMemory {
				return r.fail("remembrance context required but none available")
			}
		}
	}

	// 5. Emit agent.started
	agentEvt := r.emitWithParent(event.V1EventTypes.AgentStarted, "prizm-cli", map[string]any{
		"agent":   r.config.Agent,
		"task":    r.config.Task,
		"project": r.config.Project,
	}, evt.ID)

	// 6. Build and write prompt
	// Workspace context (V19 --context flags) is injected ahead of any
	// Remembrance context so project knowledge frames the memories.
	if r.config.WorkspaceContext != "" {
		if contextStr != "" {
			contextStr = r.config.WorkspaceContext + "\n\n" + contextStr
		} else {
			contextStr = r.config.WorkspaceContext
		}
	}
	promptContent := prompt.BuildPrompt(r.config.Agent, r.config.Project, r.config.Task, contextStr)
	runDir := filepath.Join(r.config.RunDir, r.runID)
	if err := prompt.WritePrompt(runDir, promptContent); err != nil {
		log.Printf("prizm: failed to write prompt.md: %v", err)
		return r.fail(fmt.Sprintf("failed to write prompt: %v", err))
	}

	// 7. Dry run prompt — skip LLM, complete task
	if r.config.DryRunPrompt {
		log.Printf("prizm: dry-run mode — prompt written, skipping LLM call")
		r.emitWithParent(event.V1EventTypes.AgentCompleted, "prizm-cli", map[string]any{
			"agent":     r.config.Agent,
			"dry_run":   true,
			"prompt.md": true,
		}, agentEvt.ID)

		// Must find the original task.started event to use as parent for task.completed
		var taskStartedID string
		for _, evt := range r.events {
			if evt.Type == event.V1EventTypes.TaskStarted {
				taskStartedID = evt.ID
				break
			}
		}
		r.emitWithParent(event.V1EventTypes.TaskCompleted, "prizm-cli", map[string]any{
			"task":    r.config.Task,
			"project": r.config.Project,
			"agent":   r.config.Agent,
			"status":  "completed",
			"dry_run": true,
		}, taskStartedID)

		completedAt := time.Now().UTC().Format(time.RFC3339Nano)
		durationMs := r.calculateDuration()

		summary := event.Summary{
			RunID:         r.runID,
			CorrelationID: r.correlationID,
			Status:        "completed",
			EventCount:    len(r.events),
			StartedAt:     r.startedAt,
			CompletedAt:   completedAt,
			DurationMs:    durationMs,
			MemoryUsed:    memoryStatus == "injected",
			Agent:         r.config.Agent,
			Project:       r.config.Project,
			Task:          r.config.Task,
			Provider:      "dry-run",
			Model:         r.config.Model,
			PromptPath:    filepath.Join(runDir, "prompt.md"),
			MemoryStatus:  memoryStatus,
		}

		eventsPath, summaryPath, err := r.persist(summary)
		if err != nil {
			return nil, fmt.Errorf("failed to persist run data: %w", err)
		}

		log.Printf("prizm: dry-run %s completed (%d events, %dms)", r.runID, len(r.events), durationMs)
		return &RunResult{
			RunID:       r.runID,
			Status:      "completed",
			EventCount:  len(r.events),
			EventsPath:  eventsPath,
			SummaryPath: summaryPath,
			Provider:    r.config.ProviderName,
			Model:       r.config.Model,
			PromptPath:  filepath.Join(runDir, "prompt.md"),
			DurationMs:  durationMs,
			DryRun:      true,
		}, nil
	}

	// 8. Emit llm.requested
	llmReqEvt := r.emitWithParent(event.V2EventTypes.LLMRequested, "prizm-cli", map[string]any{
		"model":       r.config.Model,
		"temperature": r.config.Temperature,
		"max_tokens":  r.config.MaxTokens,
	}, agentEvt.ID)

	// 9. Call the provider with timeout
	ctx, cancel := context.WithTimeout(context.Background(), r.config.Timeout)
	defer cancel()

	genReq := provider.GenerateRequest{
		RunID:         r.runID,
		CorrelationID: r.correlationID,
		Agent:         r.config.Agent,
		Project:       r.config.Project,
		Task:          r.config.Task,
		Prompt:        promptContent,
		Model:         r.config.Model,
		Temperature:   r.config.Temperature,
		MaxTokens:     r.config.MaxTokens,
	}

	genResp, err := r.config.Provider.Generate(ctx, genReq)

	if err != nil {
		// LLM failed
		log.Printf("prizm: LLM generate failed: %v", err)

		// llm.failed
		llmFailedEvt := r.emitWithParent(event.V2EventTypes.LLMFailed, "prizm-cli", map[string]any{
			"error":        err.Error(),
			"context_done": errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled),
		}, llmReqEvt.ID)

		// agent.failed (V2) — parent is llm.failed (causal chain: llm.requested → llm.failed → agent.failed)
		r.emitWithParent(event.V2EventTypes.AgentFailed, "prizm-cli", map[string]any{
			"agent": r.config.Agent,
			"error": err.Error(),
		}, llmFailedEvt.ID)

		return r.failWithLLMError(fmt.Sprintf("LLM generation failed: %v", err), err.Error())
	}

	// 10. LLM succeeded — emit llm.completed
	llmCompletedEvt := r.emitWithParent(event.V2EventTypes.LLMCompleted, "prizm-cli", map[string]any{
		"model":         genResp.Model,
		"provider":      genResp.Provider,
		"latency_ms":    genResp.LatencyMS,
		"prompt_tokens": genResp.PromptTokens,
		"output_tokens": genResp.OutputTokens,
		"text_length":   len(genResp.Text),
	}, llmReqEvt.ID)

	// 10b. V3: Parse LLM output for tool requests
	// The LLM's raw output is parsed for a structured JSON block that tells
	// Prizm which tool to call. The expected format is a JSON object with:
	//   {"type": "tool_request", "tool_name": "...", "tool_input": {...}}
	// If the LLM just provides a plain text answer (no tool request), we use
	// the raw output directly as the final response.
	var toolCallSummaries []event.ToolCallSummary
	var approvalSummaries []event.ApprovalSummary
	var mutationSummaries []event.MutationSummary
	var toolResult *tool.ToolResult
	outputText := genResp.Text // default: use raw LLM output

	// pendingApproval gates the run status. When a tool's policy result is
	// PolicyRequiresApproval, this flips to true. It changes the run from
	// a "completed" flow to a "pending_approval" flow — the run pauses
	// waiting for a human to approve or deny via the CLI.
	pendingApproval := false // V4: whether the run is pending approval

	if r.config.ToolExecutor != nil {
		agentResp := agentpkg.ParseAgentOutput(genResp.Text)

		if agentResp.Type == agentpkg.ResponseToolRequest {
			log.Printf("prizm: LLM requested tool %q", agentResp.ToolName)

			toolResultVal, execErr := r.config.ToolExecutor.ExecuteWithPolicy(
				context.Background(),
				agentResp.ToolName,
				r.config.Agent,
				r.config.Project,
				r.correlationID,
				agentResp.ToolInput,
			)
			if execErr != nil {
				log.Printf("prizm: tool execution failed: %v", execErr)
			}
			toolResult = &toolResultVal
			err = execErr

			policyResult := tool.EvaluatePolicy(r.config.ToolPolicy, agentResp.ToolName, agentResp.ToolInput)

			// V4: Check if this is pending approval
			// The approval workflow: agent proposes a mutation → Prizm emits
			// mutation.proposed + approval.requested → a human reviews and
			// runs `prizm approval approve <id>` or `prizm approval deny <id>`.
			// Until the human acts, the run stays in pending_approval status.
			if policyResult.Decision == tool.PolicyRequiresApproval {
				pendingApproval = true

				// Capture the proposed content for the approval artifact.
				// This is the actual file content the agent wants to write.
				proposalContent := ""
				if c, ok := agentResp.ToolInput["content"]; ok {
					if cs, ok := c.(string); ok {
						proposalContent = cs
					}
				}

				// Emit mutation.proposed
				r.emit(event.V4EventTypes.MutationProposed, "prizm-cli", map[string]any{
					"tool_name":       agentResp.ToolName,
					"mutation_type":   "write_file",
					"target_path":     agentResp.ToolInput["path"],
					"correlation_id":  r.correlationID,
					"policy_decision": string(policyResult.Decision),
					"policy_reason":   policyResult.Reason,
				})

				// Emit approval.requested
				r.emit(event.V4EventTypes.ApprovalRequested, "prizm-cli", map[string]any{
					"tool_name":       agentResp.ToolName,
					"correlation_id":  r.correlationID,
					"policy_decision": string(policyResult.Decision),
					"policy_reason":   policyResult.Reason,
				})

				// Build approval summary
				if toolResult.Output != nil {
					approvalID, _ := toolResult.Output["approval_id"].(string)
					targetPath, _ := toolResult.Output["target_path"].(string)

					// Store content under _proposal_content for later artifact writing.
					// This is an internal key — not returned to the LLM. It's extracted when
					// writing the approval.json artifact so the full proposed content is persisted
					// even if the tool result output is truncated or reformatted.
					// This key is NOT part of the public API — it's an internal marker
					// that bridges the gap between tool execution and approval artifact
					// persistence. The underscore prefix signals "internal, don't display".
					// Lifecycle: set here → read during persist (step 15) → written to
					// the approval.json artifact → never shown to the user directly.
					toolResult.Output["_proposal_content"] = proposalContent

					approvalSummaries = append(approvalSummaries, event.ApprovalSummary{
						ApprovalID:     approvalID,
						MutationType:   "write_file",
						TargetPath:     targetPath,
						Status:         "pending",
						RequestedBy:    r.config.Agent,
						PolicyDecision: string(policyResult.Decision),
					})
				}
			}

			// Build tool call summary
			toolCallSummaries = append(toolCallSummaries, event.ToolCallSummary{
				ToolName:       agentResp.ToolName,
				PolicyDecision: string(policyResult.Decision),
				Status:         toolResultStatus(toolResult, err),
			})

			// Write tool_result.json
			toolResultPath := filepath.Join(runDir, "tool_result.json")
			if toolData, jsonErr := json.MarshalIndent(toolResult, "", "  "); jsonErr == nil {
				if writeErr := os.WriteFile(toolResultPath, append(toolData, '\n'), 0644); writeErr != nil {
					log.Printf("prizm: failed to write tool_result.json: %v", writeErr)
				}
			}

			// Build output text combining LLM response and tool result
			var outputBuilder strings.Builder
			outputBuilder.WriteString(genResp.Text)
			outputBuilder.WriteString("\n\n---\n**Tool Result:**\n")
			if toolResult.Success {
				outputBuilder.WriteString(fmt.Sprintf("Tool `%s` executed successfully.\n", agentResp.ToolName))
				for k, v := range toolResult.Output {
					outputBuilder.WriteString(fmt.Sprintf("- %s: %v\n", k, v))
				}
			} else {
				outputBuilder.WriteString(fmt.Sprintf("Tool `%s` failed: %s\n", agentResp.ToolName, toolResult.Error))
			}
			outputText = outputBuilder.String()
		}
	}

	// 11. Emit agent.completed (V1 backward compat)
	// Parent is llm.completed (V2 causal chain: agent started → llm requested → llm completed → agent completed)
	//
	// The summary is truncated to 200 characters. Why 200? It's a balance:
	// enough characters to give a meaningful preview of the agent's output
	// (roughly 2-3 sentences), but short enough to keep event payloads compact
	// in NATS. Events are the communication backbone — bloated events slow
	// everything downstream. The full output lives in output.md.
	agentCompletedEvt := r.emitWithParent(event.V1EventTypes.AgentCompleted, "prizm-cli", map[string]any{
		"agent":   r.config.Agent,
		"status":  "completed",
		"summary": genResp.Text[:min(len(genResp.Text), 200)], // Truncate to 200 chars — this is for the event payload only, not the full output. The complete text is saved in output.md.
	}, llmCompletedEvt.ID)

	// 12. Write output.md
	outputPath := filepath.Join(runDir, "output.md")
	if err := prompt.WriteOutput(runDir, outputText); err != nil {
		log.Printf("prizm: failed to write output.md: %v", err)
		return r.fail(fmt.Sprintf("failed to write output: %v", err))
	}

	// Emit output.written
	r.emitWithParent(event.V2EventTypes.OutputWritten, "prizm-cli", map[string]any{
		"path":         outputPath,
		"agent":        r.config.Agent,
		"output_bytes": len(outputText),
	}, agentCompletedEvt.ID)

	// 13. Determine run status (V4: pending_approval if mutation requires approval)
	runStatus := "completed"
	if pendingApproval {
		runStatus = "pending_approval"
	}

	// 14. Emit task.completed
	taskCompletedEvt := r.emitWithParent(event.V1EventTypes.TaskCompleted, "prizm-cli", map[string]any{
		"task":    r.config.Task,
		"project": r.config.Project,
		"agent":   r.config.Agent,
		"status":  runStatus,
	}, agentCompletedEvt.ID)
	_ = taskCompletedEvt

	// Emit approval.expired if applicable (when approval has expiration)
	if pendingApproval && toolResult != nil {
		// For now, no expiration in basic V4
	}

	// 15. Persist event log and summary
	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	durationMs := r.calculateDuration()

	// Write approval artifact if one was created
	if pendingApproval && toolResult != nil && toolResult.Output != nil {
		// Write approval.json alongside the run artifacts
		if approvalID, ok := toolResult.Output["approval_id"].(string); ok {
			approvalDir := filepath.Join(runDir, "approvals")
			os.MkdirAll(approvalDir, 0755)
			approvalPath := filepath.Join(approvalDir, fmt.Sprintf("%s.json", approvalID))
			approvalData, _ := json.MarshalIndent(map[string]any{
				"approval_id":    approvalID,
				"run_id":         r.runID,
				"correlation_id": r.correlationID,
				"status":         "pending",
				"requested_by":   r.config.Agent,
				"project":        r.config.Project,
				"mutation_type":  toolResult.Output["mutation_type"],
				"target_path":    toolResult.Output["target_path"],
				"content":        toolResult.Output["_proposal_content"],
				"preview":        toolResult.Output["preview"],
				"created_at":     time.Now().UTC().Format(time.RFC3339Nano),
				"policy": map[string]any{
					"decision": "requires_approval",
					"reason":   "file writes require explicit approval",
				},
			}, "", "  ")
			os.WriteFile(approvalPath, append(approvalData, '\n'), 0644)
			log.Printf("prizm: approval artifact written to %s", approvalPath)
		}
	}

	summary := event.Summary{
		RunID:         r.runID,
		CorrelationID: r.correlationID,
		Status:        runStatus,
		EventCount:    len(r.events),
		StartedAt:     r.startedAt,
		CompletedAt:   completedAt,
		DurationMs:    durationMs,
		MemoryUsed:    memoryStatus == "injected",
		Agent:         r.config.Agent,
		Project:       r.config.Project,
		Task:          r.config.Task,
		// V2 fields
		Provider:     genResp.Provider,
		Model:        genResp.Model,
		OutputPath:   outputPath,
		PromptPath:   filepath.Join(runDir, "prompt.md"),
		MemoryStatus: memoryStatus,
		LLMLatencyMs: genResp.LatencyMS,
		ToolCalls:    toolCallSummaries,
		// V4 fields
		Approvals: approvalSummaries,
		Mutations: mutationSummaries,
	}

	eventsPath, summaryPath, err := r.persist(summary)
	if err != nil {
		return nil, fmt.Errorf("failed to persist run data: %w", err)
	}

	log.Printf("prizm: run %s completed (%d events, %dms)", r.runID, len(r.events), durationMs)

	return &RunResult{
		RunID:          r.runID,
		Status:         "completed",
		EventCount:     len(r.events),
		EventsPath:     eventsPath,
		SummaryPath:    summaryPath,
		Provider:       r.config.ProviderName,
		Model:          r.config.Model,
		PromptPath:     filepath.Join(runDir, "prompt.md"),
		OutputPath:     outputPath,
		DurationMs:     durationMs,
		DryRun:         false,
		ToolCallResult: toolResult,
	}, nil
}

// fail emits a task.failed event (linked to task.started) and returns an error result.
func (r *Runner) fail(msg string) (*RunResult, error) {
	return r.failWithLLMError(msg, "")
}

// failWithLLMError emits a task.failed event and returns an error result,
// optionally with an LLM error in the summary.
func (r *Runner) failWithLLMError(msg string, llmError string) (*RunResult, error) {
	// Use emitWithParent so task.failed links to task.started in both
	// the in-memory slice and the NATS-published event.
	parentID := r.taskStartedID
	if parentID == "" {
		// Fallback: if task.started was never emitted (extreme edge case),
		// use plain emit so we still log the failure.
		r.emit(event.V1EventTypes.TaskFailed, "prizm-cli", map[string]any{
			"task":    r.config.Task,
			"project": r.config.Project,
			"error":   msg,
		})
	} else {
		r.emitWithParent(event.V1EventTypes.TaskFailed, "prizm-cli", map[string]any{
			"task":    r.config.Task,
			"project": r.config.Project,
			"error":   msg,
		}, parentID)
	}

	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	durationMs := r.calculateDuration()

	summary := event.Summary{
		RunID:         r.runID,
		CorrelationID: r.correlationID,
		Status:        "failed",
		EventCount:    len(r.events),
		StartedAt:     r.startedAt,
		CompletedAt:   completedAt,
		DurationMs:    durationMs,
		MemoryUsed:    false,
		Agent:         r.config.Agent,
		Project:       r.config.Project,
		Task:          r.config.Task,
		ErrorMessage:  msg,
		Model:         r.config.Model,
		LLMError:      llmError,
	}

	eventsPath, summaryPath, _ := r.persist(summary)

	result := &RunResult{
		RunID:       r.runID,
		Status:      "failed",
		EventCount:  len(r.events),
		EventsPath:  eventsPath,
		SummaryPath: summaryPath,
		Error:       msg,
		Provider:    r.config.ProviderName,
		Model:       r.config.Model,
		DurationMs:  durationMs,
	}

	return result, fmt.Errorf("run failed: %s", msg)
}

// buildEvent creates an event, sets correlation_id and metadata, and appends
// it to the in-memory r.events slice. It does NOT publish to NATS — call
// publishEvent after building to publish.
func (r *Runner) buildEvent(eventType, source string, payload map[string]any) event.Event {
	evt := event.NewEvent(eventType, source, payload)
	evt.CorrelationID = r.correlationID
	evt.Metadata = event.EventMetadata{
		RunID:     r.runID,
		SessionID: r.sessionID,
		Project:   r.config.Project,
		Agent:     r.config.Agent,
	}
	r.events = append(r.events, evt)
	return evt
}

// publishEvent publishes a fully-built event to NATS (best effort for V1).
// If NATS is not connected (r.js is nil), the event is still recorded in-memory
// but not published to the bus. This allows CLI commands like ApproveWithValidation
// to work without requiring a NATS connection.
func (r *Runner) publishEvent(evt event.Event) {
	data, err := evt.ToJSON()
	if err != nil {
		log.Printf("prizm: failed to marshal event %s: %v", evt.ID, err)
		return
	}
	if r.js == nil {
		log.Printf("prizm: event %s not published (no NATS connection)", evt.ID[:24])
		return
	}
	if _, err := r.js.Publish(evt.Type, data); err != nil {
		log.Printf("prizm: failed to publish event %s: %v", evt.ID, err)
	} else {
		log.Printf("  💎 [%s] id=%s", evt.Type, evt.ID[:24])
	}
}

// PublishEvent publishes an event via NATS. If NATS is not connected,
// the event is logged but not published. This is safe to call even when
// the runner has not established a NATS connection (e.g., CLI-only usage).
func (r *Runner) PublishEvent(evt event.Event) {
	r.publishEvent(evt)
}

// emit builds and publishes an event to NATS.
func (r *Runner) emit(eventType, source string, payload map[string]any) event.Event {
	evt := r.buildEvent(eventType, source, payload)
	r.publishEvent(evt)
	return evt
}

// emitWithParent builds an event with a parent_id and publishes to NATS.
//
// The parent chain pattern: every event links to the event that caused it.
// This creates a causal DAG (directed acyclic graph) tracing the entire run.
// For example: task.created → task.started → agent.started → llm.requested →
// llm.completed → agent.completed → task.completed. If any step fails,
// the failure events still link to their parent so you can trace what broke.
//
// The parent_id is set BEFORE publishing, ensuring both the in-memory slice
// AND the NATS-published event include it. Old versions set it after publish,
// which meant NATS consumers couldn't see the parent relationship.
func (r *Runner) emitWithParent(eventType, source string, payload map[string]any, parentID string) event.Event {
	evt := r.buildEvent(eventType, source, payload)
	evt.ParentID = parentID
	r.events[len(r.events)-1] = evt
	r.publishEvent(evt)
	return evt
}

// ensureStream creates the PRIZM stream if it doesn't exist.
func (r *Runner) ensureStream() {
	_, err := r.js.AddStream(&nats.StreamConfig{
		Name:      "PRIZM",
		Subjects:  []string{"prizm.>"},
		Retention: nats.LimitsPolicy,
		MaxMsgs:   1000000,
		MaxBytes:  1024 * 1024 * 1024,
		MaxAge:    7 * 24 * time.Hour,
		Storage:   nats.FileStorage,
	})
	if err != nil {
		// "stream name already in use" is expected on re-creation; log at info level.
		// Any other error is unexpected and logged at warning level.
		if errors.Is(err, nats.ErrStreamNameAlreadyInUse) ||
			strings.Contains(err.Error(), "already in use") {
			log.Printf("prizm: stream PRIZM already exists (reusing)")
		} else {
			log.Printf("prizm: WARNING: failed to ensure stream PRIZM: %v", err)
		}
	}
}

// persist writes the event log and summary to disk.
func (r *Runner) persist(summary event.Summary) (eventsPath, summaryPath string, err error) {
	runDir := filepath.Join(r.config.RunDir, r.runID)
	if err = os.MkdirAll(runDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create run directory: %w", err)
	}

	// Write events.jsonl (one compact JSON object per line)
	eventsPath = filepath.Join(runDir, "events.jsonl")
	f, err := os.Create(eventsPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to create events file: %w", err)
	}
	defer f.Close()

	for _, evt := range r.events {
		data, err := evt.ToJSON()
		if err != nil {
			log.Printf("prizm: failed to marshal event %s: %v", evt.ID, err)
			continue
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			log.Printf("prizm: failed to write event %s: %v", evt.ID, err)
		}
	}

	// Write summary.json
	summaryPath = filepath.Join(runDir, "summary.json")
	sf, err := os.Create(summaryPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to create summary file: %w", err)
	}
	defer sf.Close()

	summaryData, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal summary: %w", err)
	}
	if _, err := sf.Write(append(summaryData, '\n')); err != nil {
		return "", "", fmt.Errorf("failed to write summary: %w", err)
	}

	log.Printf("prizm: persisted %d events to %s", len(r.events), eventsPath)
	log.Printf("prizm: summary written to %s", summaryPath)

	return eventsPath, summaryPath, nil
}

// calculateDuration computes milliseconds between startedAt and now.
func (r *Runner) calculateDuration() int64 {
	start, err := time.Parse(time.RFC3339Nano, r.startedAt)
	if err != nil {
		return 0
	}
	return time.Since(start).Milliseconds()
}

// toolResultStatus returns a human-readable status from a tool result and error.
func toolResultStatus(result *tool.ToolResult, err error) string {
	if err != nil {
		return "failed"
	}
	if result == nil {
		return "unknown"
	}
	if result.Success {
		return "completed"
	}
	return "failed"
}

// ── V5: Validation and Review Integration ──────────────────────────────────

// RunValidation executes a validation profile and emits V5 events.
// runDir should be the path to runs/<run_id>
func (r *Runner) RunValidation(runDir, profileName string) (*validation.Result, error) {
	if r.config.ValidationRegistry == nil {
		return nil, fmt.Errorf("validation registry not configured")
	}

	executor := validation.NewExecutor(r.config.ValidationRegistry, ".", runDir)
	executor.SetEmitter(func(eventType, source string, payload map[string]any) {
		r.emit(eventType, source, payload)
	})

	ctx := context.Background()
	result, err := executor.Run(ctx, profileName, r.correlationID)
	if err != nil {
		return nil, fmt.Errorf("validation %q failed: %w", profileName, err)
	}

	// Persist events to the events.jsonl file
	r.appendEventsToFile(runDir)

	return result, nil
}

// RunReview generates a deterministic review from mutation and validation data.
// runDir should be the path to runs/<run_id>
func (r *Runner) RunReview(runDir, mutationStatus string, filesChanged []string, validationResults []validation.Result) (*review.Review, string, error) {
	if r.config.Reviewer == nil {
		// Create a default reviewer if none configured
		r.config.Reviewer = review.NewReviewer("lumi-deterministic")
	}

	r.config.Reviewer.SetEmitter(func(eventType, source string, payload map[string]any) {
		r.emit(eventType, source, payload)
	})

	// Convert validation results to ValidationInfo
	var validationInfos []review.ValidationInfo
	var validSummaries []event.ValidationSummary
	for _, vr := range validationResults {
		validationInfos = append(validationInfos, review.ValidationInfo{
			Profile:    vr.Profile,
			Status:     vr.Status,
			ExitCode:   vr.ExitCode,
			DurationMs: vr.DurationMs,
		})
		validSummaries = append(validSummaries, event.ValidationSummary{
			Profile:    vr.Profile,
			Status:     vr.Status,
			ExitCode:   vr.ExitCode,
			DurationMs: vr.DurationMs,
			ResultPath: fmt.Sprintf("validation/%s.json", vr.Profile),
		})
	}

	// Pass empty mutation summaries for V5 (review works from mutationStatus string)
	reviewResult, err := r.config.Reviewer.Generate(
		r.runID,
		r.correlationID,
		mutationStatus,
		filesChanged,
		validationInfos,
		nil,
	)
	if err != nil {
		return nil, "", fmt.Errorf("review generation failed: %w", err)
	}

	// Write review.md artifact
	artifactPath, err := review.WriteReviewArtifact(runDir, reviewResult)
	if err != nil {
		return nil, "", fmt.Errorf("failed to write review artifact: %w", err)
	}

	// Persist review events to the events.jsonl file
	r.appendEventsToFile(runDir)

	// Update summary.json with validation and review metadata
	if err := r.updateSummaryWithV5(runDir, validSummaries, reviewResult, artifactPath); err != nil {
		log.Printf("prizm: failed to update summary with V5 metadata: %v", err)
		// Non-fatal: review and validation artifacts are already written
	}

	return reviewResult, artifactPath, nil
}

// ApproveWithValidation approves a mutation, applies it, runs validation, and generates a review.
// This is the full V5 pipeline: approve → apply → validate → review.
func (r *Runner) ApproveWithValidation(runDir, approvalID, approvedBy, workspaceDir string) (*RunResult, error) {
	// Derive the run ID from the run directory path
	runID := filepath.Base(runDir)

	// Load the approval store
	store := approval.NewStore(filepath.Dir(runDir))

	// Create the mutation executor
	mutExec := mutation.NewExecutor(workspaceDir, store, r.config.MutationAllowedPaths...)

	// Emit events for approval
	mutExec.SetEmitter(func(eventType, source string, payload map[string]any) {
		r.emit(eventType, source, payload)
	})

	// Apply the mutation
	mutResult, err := mutExec.ApplyWithRun(context.Background(), runID, approvalID, approvedBy)
	if err != nil {
		return nil, fmt.Errorf("mutation apply failed: %w", err)
	}

	mutationStatus := "none"
	var filesChanged []string
	if mutResult != nil && mutResult.Success {
		mutationStatus = "applied"
		filesChanged = []string{mutResult.TargetPath}
	} else if mutResult != nil {
		mutationStatus = "failed"
	}

	// Run validation
	var validationResults []validation.Result
	if r.config.ValidationRegistry != nil {
		profiles := r.config.ValidationRegistry.List()
		for _, profileName := range profiles {
			result, valErr := r.RunValidation(runDir, profileName)
			if valErr != nil {
				log.Printf("prizm: validation %q error: %v", profileName, valErr)
				validationResults = append(validationResults, validation.Result{
					Profile: profileName,
					Status:  "error",
					Error:   valErr.Error(),
				})
				continue
			}
			if result != nil {
				validationResults = append(validationResults, *result)
			}
		}
	}

	// Generate review
	reviewResult, reviewArtifactPath, reviewErr := r.RunReview(runDir, mutationStatus, filesChanged, validationResults)
	if reviewErr != nil {
		log.Printf("prizm: review generation failed: %v", reviewErr)
		// Non-fatal: validation artifacts are already written
	}

	// Read the summary to get current values
	summaryPath := filepath.Join(runDir, "summary.json")
	var summary event.Summary
	if data, err := os.ReadFile(summaryPath); err == nil {
		json.Unmarshal(data, &summary)
	}

	summary.Status = "completed"
	if mutationStatus == "applied" {
		summary.Status = "completed"
	}

	// Persist updated summary
	summaryData, _ := json.MarshalIndent(summary, "", "  ")
	os.WriteFile(summaryPath, append(summaryData, '\n'), 0644)

	return &RunResult{
		RunID:              r.runID,
		Status:             summary.Status,
		EventCount:         len(r.events),
		SummaryPath:        summaryPath,
		Provider:           r.config.ProviderName,
		Model:              r.config.Model,
		ValidationResults:  validationResults,
		Review:             reviewResult,
		ReviewArtifactPath: reviewArtifactPath,
	}, nil
}

// updateSummaryWithV5 adds validation and review metadata to the run summary.
func (r *Runner) updateSummaryWithV5(runDir string, validSummaries []event.ValidationSummary, reviewResult *review.Review, reviewArtifactPath string) error {
	summaryPath := filepath.Join(runDir, "summary.json")

	// Read existing summary
	var summary event.Summary
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		return fmt.Errorf("cannot read summary: %w", err)
	}
	if err := json.Unmarshal(data, &summary); err != nil {
		return fmt.Errorf("cannot parse summary: %w", err)
	}

	// Add validation data
	summary.Validations = validSummaries

	// Add review data
	if reviewResult != nil {
		artifactRelPath := "review.md"
		if reviewArtifactPath != "" {
			artifactRelPath = filepath.Base(reviewArtifactPath)
		}
		summary.Reviews = []event.ReviewSummary{
			{
				Reviewer:       reviewResult.Reviewer,
				Status:         "completed",
				Recommendation: string(reviewResult.Recommendation),
				ArtifactPath:   artifactRelPath,
			},
		}
	}

	// Write updated summary
	summaryData, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal updated summary: %w", err)
	}
	if err := os.WriteFile(summaryPath, append(summaryData, '\n'), 0644); err != nil {
		return fmt.Errorf("failed to write updated summary: %w", err)
	}

	return nil
}

// appendEventsToFile appends events from the in-memory slice to the events.jsonl file.
// This is used by post-run operations (validation, review) that emit events after
// the initial persistEvents call during Run().
func (r *Runner) appendEventsToFile(runDir string) {
	eventsPath := filepath.Join(runDir, "events.jsonl")

	// Read existing events from the file
	existingData, err := os.ReadFile(eventsPath)
	if err != nil {
		log.Printf("prizm: failed to read existing events for append: %v", err)
		return
	}

	// Count existing events (one per line)
	lines := strings.Split(strings.TrimSpace(string(existingData)), "\n")
	existingCount := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			existingCount++
		}
	}

	// Append only new events
	if existingCount < len(r.events) {
		f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("prizm: failed to open events file for append: %v", err)
			return
		}
		defer f.Close()

		for _, evt := range r.events[existingCount:] {
			data, err := evt.ToJSON()
			if err != nil {
				log.Printf("prizm: failed to marshal event %s: %v", evt.ID, err)
				continue
			}
			if _, err := f.Write(append(data, '\n')); err != nil {
				log.Printf("prizm: failed to write event %s: %v", evt.ID, err)
			}
		}
	}
}
