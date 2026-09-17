package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/policy"
	"github.com/emaharmony/prizm/internal/safety"
)

// Executor runs a tool through the full lifecycle: policy check → event
// emission → execution → event emission. It records every decision in the
// event log for auditability.
type Executor struct {
	Registry        *Registry
	Policy          *PolicyConfig
	PolicyEvaluator PolicyEvaluatorFunc // V8: central policy engine (optional)
	ApprovalStore   ApprovalStorer      // Optional persistence for approval-gated tools.
	// Emit is called to record events. If nil, events are silently dropped.
	Emit func(eventType, source string, payload map[string]any)
}

// ApprovalStorer is the persistence interface used for approval-gated tools.
type ApprovalStorer interface {
	Save(a *approval.Approval) error
}

// PolicyEvaluatorFunc evaluates a V8 policy request and returns a decision.
// If nil, V8 policy evaluation is skipped (backward compatible).
type PolicyEvaluatorFunc func(action string, resource policy.Resource, context policy.Context) policy.PolicyDecision

// NewExecutor creates an executor with the given registry and policy config.
func NewExecutor(registry *Registry, policy *PolicyConfig) *Executor {
	return &Executor{
		Registry: registry,
		Policy:   policy,
	}
}

// SetEmitter sets the event emission callback.
func (e *Executor) SetEmitter(emit func(eventType, source string, payload map[string]any)) {
	e.Emit = emit
}

// SetPolicyEvaluator sets the V8 central policy evaluator.
// When set, policy evaluation runs before local tool policy.
func (e *Executor) SetPolicyEvaluator(fn PolicyEvaluatorFunc) {
	e.PolicyEvaluator = fn
}

// SetApprovalStore enables durable approval artifacts for tools whose policy
// decision is requires_approval.
func (e *Executor) SetApprovalStore(store ApprovalStorer) {
	e.ApprovalStore = store
}

// ExecuteWithPolicy evaluates policy for a tool call, emits events for the
// decision, and executes the tool if approved.
//
// V8 adds a central policy evaluation BEFORE local tool policy.
// Policy decides permission; local validators still enforce input safety.
// If V8 policy denies, local policy is never reached.
// If V8 policy allows, local policy still enforces path safety, etc.
// If V8 policy is not configured, local policy runs as before (backward compatible).
func (e *Executor) ExecuteWithPolicy(ctx context.Context, toolName, agent, project, correlationID string, input map[string]any) (ToolResult, error) {
	runID := metadataString(input, "_run_id")
	channelID := metadataString(input, "_channel_id")
	execInput := stripMetadata(input)

	// Emit tool.requested
	e.emitEvent("prizm.tool.requested", map[string]any{
		"tool_name":      toolName,
		"agent":          agent,
		"project":        project,
		"correlation_id": correlationID,
		"run_id":         runID,
		"input":          execInput,
	})

	// V8: Evaluate central policy first (if configured)
	if e.PolicyEvaluator != nil {
		v8Decision := e.PolicyEvaluator(
			"tool.execute",
			policy.Resource{Type: "tool", Name: toolName},
			policy.Context{Project: project},
		)

		e.emitEvent("prizm.policy.checked", map[string]any{
			"tool_name":       toolName,
			"policy_decision": string(v8Decision.Decision),
			"policy_rule_id":  v8Decision.RuleID,
			"policy_reason":   v8Decision.Reason,
		})

		switch v8Decision.Decision {
		case policy.DecisionDenied:
			e.emitEvent("prizm.tool.denied", map[string]any{
				"tool_name":       toolName,
				"agent":           agent,
				"project":         project,
				"correlation_id":  correlationID,
				"policy_decision": string(v8Decision.Decision),
				"policy_reason":   v8Decision.Reason,
				"policy_source":   "v8",
			})
			return ToolResult{
				Success: false,
				Output:  nil,
				Error:   fmt.Sprintf("tool call denied by policy: %s", v8Decision.Reason),
			}, nil
		case policy.DecisionRequiresApproval:
			// V8 says requires_approval — fall through to local policy
			// which will handle the approval workflow
		default:
			// V8 says allowed — local policy still validates safety
		}
	}

	// Evaluate policy
	policyResult := EvaluatePolicyForAgent(*e.Policy, toolName, agent, execInput)

	// Handle requires_approval — this is not a denial, it's a request for human approval
	if policyResult.Decision == PolicyRequiresApproval {
		e.emitEvent("prizm.tool.approved", map[string]any{
			"tool_name":       toolName,
			"agent":           agent,
			"project":         project,
			"correlation_id":  correlationID,
			"policy_decision": string(policyResult.Decision),
			"policy_reason":   policyResult.Reason,
		})

		// write_file_proposal/create_directory_proposal are safe to actually
		// invoke here — they only build a preview/proposal object, with no
		// real side effect (the real write happens later, in
		// mutation.Executor.ApplyWithRun, once a human approves). Every
		// other approval-gated tool (shell, git_*, mcp_*) has no such
		// dry-run mode — its Execute() performs the real, irreversible
		// action immediately — so it must NOT be invoked here. persistApproval
		// (via describeToolCall) derives everything it needs from the raw
		// input alone, and the real invocation is deferred to approval time.
		var result ToolResult
		switch toolName {
		case "write_file_proposal", "create_directory_proposal":
			var err error
			result, err = e.Registry.Execute(ctx, toolName, execInput)
			if err != nil {
				return ToolResult{
					Success: false,
					Output:  nil,
					Error:   err.Error(),
				}, err
			}
		default:
			result = ToolResult{Success: true, Output: map[string]any{}}
		}
		if result.Output == nil {
			result.Output = map[string]any{}
		}

		// Mark as pending_approval status
		result.Output["policy_decision"] = string(PolicyRequiresApproval)
		result.Output["policy_reason"] = policyResult.Reason
		if err := e.persistApproval(toolName, agent, project, correlationID, runID, channelID, execInput, policyResult, &result); err != nil {
			e.emitEvent("prizm.approval.persist_failed", map[string]any{
				"tool_name":      toolName,
				"agent":          agent,
				"project":        project,
				"correlation_id": correlationID,
				"run_id":         runID,
				"error":          err.Error(),
			})
			return ToolResult{
				Success: false,
				Output:  nil,
				Error:   fmt.Sprintf("approval request could not be saved: %v", err),
			}, nil
		}

		return result, nil
	}

	// Emit denied event
	if policyResult.Decision == PolicyDenied {
		e.emitEvent("prizm.tool.denied", map[string]any{
			"tool_name":       toolName,
			"agent":           agent,
			"project":         project,
			"correlation_id":  correlationID,
			"policy_decision": string(policyResult.Decision),
			"policy_reason":   policyResult.Reason,
		})
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   fmt.Sprintf("tool call denied: %s", policyResult.Reason),
		}, nil
	}

	e.emitEvent("prizm.tool.approved", map[string]any{
		"tool_name":       toolName,
		"agent":           agent,
		"project":         project,
		"correlation_id":  correlationID,
		"policy_decision": string(policyResult.Decision),
		"policy_reason":   policyResult.Reason,
	})

	// Emit tool.started
	e.emitEvent("prizm.tool.started", map[string]any{
		"tool_name":      toolName,
		"agent":          agent,
		"project":        project,
		"correlation_id": correlationID,
	})

	// Sanitize tool inputs before execution (command injection prevention)
	sanitizedInput := safety.SanitizeToolInput(toolName, execInput)

	// Resolve and execute the tool
	result, err := e.Registry.Execute(ctx, toolName, sanitizedInput)
	if err != nil {
		e.emitEvent("prizm.tool.failed", map[string]any{
			"tool_name":      toolName,
			"agent":          agent,
			"project":        project,
			"correlation_id": correlationID,
			"error":          err.Error(),
		})
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   err.Error(),
		}, err
	}

	// Auto-approve write: when write_file_proposal is auto-approved, actually write the file to disk.
	// The WriteFileProposal tool only creates a proposal; when AutoApproveMutations is true,
	// we need to perform the actual file write ourselves.
	if toolName == "write_file_proposal" && e.Policy != nil && e.Policy.AutoApproveMutations && result.Success {
		filePath, _ := sanitizedInput["path"].(string)
		fileContent, _ := sanitizedInput["content"].(string)
		if filePath != "" {
			roots := append([]string{}, e.Policy.WorkspaceRoot)
			roots = append(roots, e.Policy.ReadRoots...)
			roots = append(roots, e.Policy.WriteRoots...)
			resolvedPath, err := safety.ResolveAndContainMulti(roots, filePath)
			if err != nil {
				result.Error = fmt.Sprintf("auto-approve write: path resolution failed: %v", err)
				result.Success = false
			} else {
				if err := os.MkdirAll(filepath.Dir(resolvedPath), 0755); err != nil {
					result.Error = fmt.Sprintf("auto-approve write: mkdir failed: %v", err)
					result.Success = false
				} else if err := os.WriteFile(resolvedPath, []byte(fileContent), 0644); err != nil {
					result.Error = fmt.Sprintf("auto-approve write: write failed: %v", err)
					result.Success = false
				} else {
					result.Output["status"] = "written"
					result.Output["written_path"] = resolvedPath
					result.Output["auto_approved"] = true
					e.emitEvent("prizm.mutation.applied", map[string]any{
						"tool_name":      toolName,
						"agent":           agent,
						"project":         project,
						"correlation_id":  correlationID,
						"target_path":     resolvedPath,
						"mutation_type":   "write_file",
						"auto_approved":   true,
					})
				}
			}
		}
	}

	// Emit tool.completed
	e.emitEvent("prizm.tool.completed", map[string]any{
		"tool_name":      toolName,
		"agent":          agent,
		"project":        project,
		"correlation_id": correlationID,
		"success":        result.Success,
	})

	return result, nil
}

// emitEvent calls the configured emitter if it exists.
func (e *Executor) emitEvent(eventType string, payload map[string]any) {
	if e.Emit != nil {
		e.Emit(eventType, "prizm-tool-executor", payload)
	}
}

func (e *Executor) persistApproval(toolName, agent, project, correlationID, runID, channelID string, input map[string]any, policyResult PolicyResult, result *ToolResult) error {
	if e.ApprovalStore == nil {
		return nil
	}
	if runID == "" {
		runID = correlationID
	}
	if runID == "" {
		return fmt.Errorf("run_id is required for approval persistence")
	}
	if result.Output == nil {
		result.Output = map[string]any{}
	}

	approvalID, _ := result.Output["approval_id"].(string)
	if approvalID == "" {
		approvalID = approval.NewApprovalID()
		result.Output["approval_id"] = approvalID
	}

	// write_file_proposal/create_directory_proposal are safe, side-effect-free
	// tools that already build their own preview and set mutation_type/
	// target_path in Output — use that. Every other approval-gated tool
	// (shell, git_*, mcp_*) has no proposal variant; its Execute() was never
	// called for this request (see ExecuteWithPolicy's PolicyRequiresApproval
	// branch), so derive target/preview generically from the original input
	// and persist it as a MutationToolCall for later re-invocation.
	var targetPath, content, preview string
	mutationType, _ := result.Output["mutation_type"].(string)
	if mutationType != "" {
		targetPath, _ = result.Output["target_path"].(string)
		if targetPath == "" {
			targetPath, _ = input["path"].(string)
		}
		content, _ = input["content"].(string)
		preview = content
		if preview == "" {
			preview, _ = result.Output["preview"].(string)
		}
	} else {
		mutationType = approval.MutationToolCall
		targetPath, preview = describeToolCall(toolName, input)
	}
	if targetPath == "" {
		return fmt.Errorf("target_path is required for approval persistence")
	}
	if len(preview) > 500 {
		preview = preview[:500] + "..."
	}

	a := &approval.Approval{
		ApprovalID:    approvalID,
		RunID:         runID,
		CorrelationID: correlationID,
		Status:        approval.StatusPending,
		RequestedBy:   agent,
		Project:       project,
		MutationType:  mutationType,
		TargetPath:    targetPath,
		Content:       content,
		Preview:       preview,
		ToolName:      toolName,
		Input:         input,
		CreatedAt:     time.Now().UTC(),
		Policy: approval.PolicyDecision{
			Decision: approval.DecisionRequiresApproval,
			Reason:   policyResult.Reason,
		},
	}
	if err := e.ApprovalStore.Save(a); err != nil {
		return err
	}

	result.Output["run_id"] = runID
	result.Output["correlation_id"] = correlationID
	result.Output["status"] = "pending_approval"
	result.Output["instruction"] = fmt.Sprintf("Use 'prizm approval approve %s --run %s --by <name>' or 'prizm approval deny %s --run %s --by <name>' to proceed.", approvalID, runID, approvalID, runID)

	// Emit event for Discord notification (approval card with buttons)
	e.emitEvent("prizm.approval.file_requested", map[string]any{
		"approval_id":    approvalID,
		"run_id":         runID,
		"agent":          agent,
		"project":        project,
		"target_path":    targetPath,
		"mutation_type":  mutationType,
		"tool_name":      toolName,
		"preview":        preview,
		"content_length": len(content),
		"_channel_id":    channelID,
	})

	return nil
}

// describeToolCall derives a human-readable target label and preview for an
// approval-gated tool call that has no dedicated "_proposal" variant (i.e.
// nothing in result.Output already describes it). Used to persist a
// meaningful MutationToolCall approval record and to render the Discord
// approval card, without ever having executed the tool.
func describeToolCall(toolName string, input map[string]any) (target, preview string) {
	str := func(key string) string {
		v, _ := input[key].(string)
		return v
	}
	switch toolName {
	case "update_context":
		var parts []string
		for _, k := range []string{"branch", "last_action", "pr", "notes"} {
			if v := str(k); v != "" {
				parts = append(parts, fmt.Sprintf("%s=%q", k, v))
			}
		}
		label := strings.Join(parts, ", ")
		if label == "" {
			label = "update_context"
		}
		return label, label
	case "record_decision":
		decision := str("decision")
		return decision, fmt.Sprintf("record decision: %q", decision)
	case "add_blocked":
		item := str("item")
		waitingOn := str("waiting_on")
		return item, fmt.Sprintf("blocked: %q (waiting on %s)", item, waitingOn)
	case "unblock":
		id := str("id")
		return id, fmt.Sprintf("unblock %s", id)
	case "shell":
		cmd := str("command")
		return cmd, cmd
	case "git_checkout":
		branch := str("branch")
		return branch, fmt.Sprintf("git checkout %s", branch)
	case "git_add":
		path := str("path")
		return path, fmt.Sprintf("git add %s", path)
	case "git_commit":
		msg := str("message")
		return msg, fmt.Sprintf("git commit -m %q", msg)
	case "git_push":
		remote, branch := str("remote"), str("branch")
		if remote == "" {
			remote = "origin"
		}
		label := remote
		if branch != "" {
			label = remote + "/" + branch
		}
		return label, fmt.Sprintf("git push %s %s", remote, branch)
	case "create_pr":
		title := str("title")
		return title, fmt.Sprintf("gh pr create --title %q", title)
	}
	if strings.HasPrefix(toolName, "mcp_") {
		return toolName, fmt.Sprintf("%s(%v)", toolName, input)
	}
	// Generic fallback for any other tool: try common field names, then
	// fall back to a raw summary so target is never empty.
	if path := str("path"); path != "" {
		return path, path
	}
	if cmd := str("command"); cmd != "" {
		return cmd, cmd
	}
	return toolName, fmt.Sprintf("%s(%v)", toolName, input)
}

func metadataString(input map[string]any, key string) string {
	if input == nil {
		return ""
	}
	v, _ := input[key].(string)
	return v
}

func stripMetadata(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	cleaned := make(map[string]any, len(input))
	for k, v := range input {
		if len(k) > 0 && k[0] == '_' {
			continue
		}
		cleaned[k] = v
	}
	return cleaned
}
