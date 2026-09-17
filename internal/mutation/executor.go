// Package mutation implements Prizm V4's approval-gated mutation execution.
// After an approval is granted, the mutation executor applies the approved
// file change, with comprehensive safety checks before writing any file.
package mutation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/safety"
	"github.com/emaharmony/prizm/internal/tool"
)

// MaxContentSize is the maximum allowed content size (1MB).
const MaxContentSize = 1024 * 1024

// EmitFunc is a callback for emitting events.
type EmitFunc func(eventType, source string, payload map[string]any)

// Executor applies approved file mutations with safety checks.
type Executor struct {
	workspaceRoot string
	allowedPaths  []string
	approvalStore *approval.Store
	emit          EmitFunc
	shellTool     *tool.ShellTool // V62: optional — elevated (tier_3) shell tool for applying MutationToolCall approvals whose ToolName is "shell"
	registry      *tool.Registry  // V62: optional — enables applying MutationToolCall approvals for any other tool (git_*, mcp_*) by re-invoking it via the registry
}

// NewExecutor creates a new mutation executor.
func NewExecutor(workspaceRoot string, approvalStore *approval.Store, allowedPaths ...string) *Executor {
	return &Executor{
		workspaceRoot: workspaceRoot,
		allowedPaths:  allowedPaths,
		approvalStore: approvalStore,
	}
}

// SetEmitter sets the event emission callback.
func (e *Executor) SetEmitter(emit EmitFunc) {
	e.emit = emit
}

// SetShellTool enables this executor to apply MutationToolCall approvals
// whose ToolName is "shell", by re-running the approved command through the
// given (typically tier_3) shell tool. Without this, shell approvals fail
// with a clear "not configured" error instead of silently doing nothing.
func (e *Executor) SetShellTool(t *tool.ShellTool) {
	e.shellTool = t
}

// SetRegistry enables this executor to apply MutationToolCall approvals for
// any tool other than "shell" (git_checkout, git_add, git_commit, git_push,
// create_pr, mcp_* tools) by re-invoking the exact same tool with the exact
// same input through the given registry. Without this, those approvals fail
// with a clear "not configured" error.
func (e *Executor) SetRegistry(r *tool.Registry) {
	e.registry = r
}

// MutationResult captures the result of applying a mutation.
type MutationResult struct {
	Success    bool   `json:"success"`
	ApprovalID string `json:"approval_id"`
	TargetPath string `json:"target_path"`
	Message    string `json:"message,omitempty"`
}

// Apply executes the mutation for a granted approval.
// It performs comprehensive safety checks before any file write.
func (e *Executor) Apply(ctx context.Context, approvalID string, approvedBy string) (*MutationResult, error) {
	// We can't load without runID yet — we'll scan for it or the caller will
	// provide it. For now we need the runID to look up the approval.
	// Since the CLI provides the runID context, let's accept it differently.
	// Actually, the approval ID is a ULID, we should have the runID too.
	// Let's require the runID to be derivable or passed in.
	// For now, return an error requiring runID context.
	return nil, fmt.Errorf("runID required — use ApplyWithRun")
}

// ApplyWithRun executes the mutation for a granted approval with explicit runID.
func (e *Executor) ApplyWithRun(ctx context.Context, runID, approvalID, approvedBy string) (*MutationResult, error) {
	// 1. Load the approval
	a, err := e.approvalStore.Load(runID, approvalID)
	if err != nil {
		e.emitEvent("prizm.mutation.failed", map[string]any{
			"approval_id": approvalID,
			"run_id":      runID,
			"error":       "approval not found: " + err.Error(),
		})
		return &MutationResult{Success: false, ApprovalID: approvalID, Message: "approval not found"}, nil
	}

	// 2. Status check: must be pending (we'll approve first) or already approved
	if a.Status != approval.StatusPending && a.Status != approval.StatusApproved {
		e.emitEvent("prizm.mutation.failed", map[string]any{
			"approval_id":     approvalID,
			"run_id":          runID,
			"mutation_type":   a.MutationType,
			"target_path":     a.TargetPath,
			"correlation_id":  a.CorrelationID,
			"approval_status": a.Status,
			"error":           fmt.Sprintf("approval status is %q, not pending or approved", a.Status),
		})
		return &MutationResult{Success: false, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: fmt.Sprintf("approval status is %q", a.Status)}, nil
	}

	// 3. Safety checks
	safetyErr := e.validateSafety(a)
	if safetyErr != nil {
		e.emitEvent("prizm.mutation.failed", map[string]any{
			"approval_id":    approvalID,
			"run_id":         runID,
			"mutation_type":  a.MutationType,
			"target_path":    a.TargetPath,
			"correlation_id": a.CorrelationID,
			"error":          safetyErr.Error(),
		})
		return &MutationResult{Success: false, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: safetyErr.Error()}, nil
	}

	// 4. Approve if still pending
	if a.Status == approval.StatusPending {
		if err := a.Approve(approvedBy); err != nil {
			e.emitEvent("prizm.mutation.failed", map[string]any{
				"approval_id": approvalID,
				"error":       "failed to approve: " + err.Error(),
			})
			return &MutationResult{Success: false, ApprovalID: approvalID, Message: "failed to approve"}, nil
		}
		// Persist the updated approval
		if err := e.approvalStore.Save(a); err != nil {
			return nil, fmt.Errorf("failed to save updated approval: %w", err)
		}
	}

	// 5. Emit approval.granted
	e.emitEvent("prizm.approval.granted", map[string]any{
		"approval_id":     approvalID,
		"run_id":          runID,
		"mutation_type":   a.MutationType,
		"target_path":     a.TargetPath,
		"correlation_id":  a.CorrelationID,
		"approved_by":     approvedBy,
		"policy_decision": a.Policy.Decision,
		"policy_reason":   a.Policy.Reason,
	})

	// 6. Emit mutation.validated
	e.emitEvent("prizm.mutation.validated", map[string]any{
		"approval_id":    approvalID,
		"run_id":         runID,
		"mutation_type":  a.MutationType,
		"target_path":    a.TargetPath,
		"correlation_id": a.CorrelationID,
	})

	// 7. Apply the mutation
	// V62: MutationToolCall's target path is a human-readable label (a shell
	// command, a git branch/message, etc.), not a filesystem path —
	// resolveTargetPath (containment against workspace roots) doesn't apply
	// to it. The actual tool is re-invoked with the preserved a.Input below.
	var absPath string
	if a.MutationType != approval.MutationToolCall {
		var resolveErr error
		absPath, resolveErr = e.resolveTargetPath(a.TargetPath)
		if resolveErr != nil {
			e.emitEvent("prizm.mutation.failed", map[string]any{
				"approval_id":    approvalID,
				"mutation_type":  a.MutationType,
				"target_path":    a.TargetPath,
				"correlation_id": a.CorrelationID,
				"error":          resolveErr.Error(),
			})
			return &MutationResult{Success: false, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: resolveErr.Error()}, nil
		}
	}

	switch a.MutationType {
	case approval.MutationToolCall:
		var result tool.ToolResult
		var err error
		if a.ToolName == "shell" {
			if e.shellTool == nil {
				e.emitEvent("prizm.mutation.failed", map[string]any{
					"approval_id":    approvalID,
					"mutation_type":  a.MutationType,
					"target_path":    a.TargetPath,
					"correlation_id": a.CorrelationID,
					"error":          "shell execution not configured for this approval executor",
				})
				return &MutationResult{Success: false, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: "shell execution not configured for this approval executor"}, nil
			}
			result, err = e.shellTool.Execute(ctx, a.Input)
		} else {
			if e.registry == nil {
				e.emitEvent("prizm.mutation.failed", map[string]any{
					"approval_id":    approvalID,
					"mutation_type":  a.MutationType,
					"target_path":    a.TargetPath,
					"correlation_id": a.CorrelationID,
					"error":          "tool registry not configured for this approval executor",
				})
				return &MutationResult{Success: false, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: "tool registry not configured for this approval executor"}, nil
			}
			result, err = e.registry.Execute(ctx, a.ToolName, a.Input)
		}
		if err != nil {
			e.emitEvent("prizm.mutation.failed", map[string]any{
				"approval_id":    approvalID,
				"mutation_type":  a.MutationType,
				"target_path":    a.TargetPath,
				"correlation_id": a.CorrelationID,
				"error":          err.Error(),
			})
			return &MutationResult{Success: false, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: err.Error()}, nil
		}
		if !result.Success {
			e.emitEvent("prizm.mutation.failed", map[string]any{
				"approval_id":    approvalID,
				"mutation_type":  a.MutationType,
				"target_path":    a.TargetPath,
				"correlation_id": a.CorrelationID,
				"error":          result.Error,
			})
			return &MutationResult{Success: false, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: result.Error}, nil
		}
		message := fmt.Sprintf("%v", result.Output)
		if stdout, ok := result.Output["stdout"].(string); ok {
			message = stdout
		}
		e.emitEvent("prizm.mutation.applied", map[string]any{
			"approval_id":    approvalID,
			"run_id":         runID,
			"mutation_type":  a.MutationType,
			"tool_name":      a.ToolName,
			"target_path":    a.TargetPath,
			"correlation_id": a.CorrelationID,
			"applied_by":     approvedBy,
		})
		return &MutationResult{Success: true, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: message}, nil
	case approval.MutationCreateDirectory:
		if err := os.MkdirAll(absPath, 0755); err != nil {
			e.emitEvent("prizm.mutation.failed", map[string]any{
				"approval_id":    approvalID,
				"mutation_type":  a.MutationType,
				"target_path":    a.TargetPath,
				"correlation_id": a.CorrelationID,
				"error":          "directory creation failed: " + err.Error(),
			})
			return &MutationResult{Success: false, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: "directory creation failed: " + err.Error()}, nil
		}
	case approval.MutationWriteFile:
		// Ensure parent directory exists
		parentDir := filepath.Dir(absPath)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			e.emitEvent("prizm.mutation.failed", map[string]any{
				"approval_id":    approvalID,
				"mutation_type":  a.MutationType,
				"target_path":    a.TargetPath,
				"correlation_id": a.CorrelationID,
				"error":          "failed to create parent directory: " + err.Error(),
			})
			return &MutationResult{Success: false, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: "failed to create parent directory"}, nil
		}
		if err := os.WriteFile(absPath, []byte(a.Content), 0644); err != nil {
			e.emitEvent("prizm.mutation.failed", map[string]any{
				"approval_id":    approvalID,
				"mutation_type":  a.MutationType,
				"target_path":    a.TargetPath,
				"correlation_id": a.CorrelationID,
				"error":          "file write failed: " + err.Error(),
			})
			return &MutationResult{Success: false, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: "file write failed: " + err.Error()}, nil
		}
	default:
		e.emitEvent("prizm.mutation.failed", map[string]any{
			"approval_id":    approvalID,
			"mutation_type":  a.MutationType,
			"target_path":    a.TargetPath,
			"correlation_id": a.CorrelationID,
			"error":          "unsupported mutation type",
		})
		return &MutationResult{Success: false, ApprovalID: approvalID, TargetPath: a.TargetPath, Message: "unsupported mutation type: " + a.MutationType}, nil
	}

	// 8. Emit mutation.applied
	e.emitEvent("prizm.mutation.applied", map[string]any{
		"approval_id":    approvalID,
		"run_id":         runID,
		"mutation_type":  a.MutationType,
		"target_path":    a.TargetPath,
		"correlation_id": a.CorrelationID,
		"content_size":   len(a.Content),
		"applied_by":     approvedBy,
	})

	return &MutationResult{
		Success:    true,
		ApprovalID: approvalID,
		TargetPath: a.TargetPath,
		Message:    "mutation applied successfully",
	}, nil
}

// validateSafety performs all required safety checks before any mutation is applied.
func (e *Executor) validateSafety(a *approval.Approval) error {
	// Check: target path must not be empty
	if a.TargetPath == "" {
		return fmt.Errorf("target path is empty")
	}

	// V62: MutationToolCall's "target path" is a human-readable label (shell
	// command, git branch/message, etc.), not a filesystem path — the checks
	// below (path traversal, containment, symlink/dir checks) don't apply.
	// For shell specifically, re-check the hard blocklist against the actual
	// command as the safety floor that must survive even a stale/tampered
	// approval record — tier is set to tier_3 so only the hard blocklist
	// (always enforced first, regardless of tier) is consulted here; the
	// actual execution tier is whatever the configured shellTool.Policy
	// allows. Other tools (git_*, mcp_*) have no equivalent blocklist
	// concept — their safety already came from policy.go's checks (frozen
	// paths, CanAgentProposeWrites, etc.) at propose time, the same trust
	// model as file mutations.
	if a.MutationType == approval.MutationToolCall {
		if a.ToolName == "shell" {
			command, _ := a.Input["command"].(string)
			if result := tool.EvaluateShellPolicy(tool.ShellPolicy{Tier: "tier_3"}, command); !result.Allowed {
				return fmt.Errorf("command blocked: %s", result.Reason)
			}
		}
		return nil
	}

	// Check: no path traversal
	if strings.Contains(a.TargetPath, "..") {
		return fmt.Errorf("path traversal with '..' is blocked: %q", a.TargetPath)
	}

	resolvedTargetPath, resolveErr := e.resolveTargetPath(a.TargetPath)
	if resolveErr != nil {
		return resolveErr
	}
	if a.MutationType == approval.MutationCreateDirectory {
		if info, statErr := os.Lstat(resolvedTargetPath); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("target path %q is a symlink, not a directory", a.TargetPath)
			}
			if !info.IsDir() {
				return fmt.Errorf("target path %q exists and is not a directory", a.TargetPath)
			}
		}
		return nil
	}
	if a.MutationType != approval.MutationWriteFile {
		return fmt.Errorf("unsupported mutation type %q", a.MutationType)
	}
	if len(a.Content) > MaxContentSize {
		return fmt.Errorf("content size %d bytes exceeds maximum %d bytes", len(a.Content), MaxContentSize)
	}
	if info, statErr := os.Lstat(resolvedTargetPath); statErr == nil {
		if info.IsDir() {
			return fmt.Errorf("target path %q is a directory, not a file", a.TargetPath)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target path %q is a symlink, not a regular file", a.TargetPath)
		}
	}

	// Containment against all write roots — including parent-symlink-escape
	// resolution — is enforced by resolveTargetPath -> safety.ResolveAndContainMulti
	// above. (The previously-duplicated workspace-root-only checks here were
	// dead code and weaker for multi-root configs; removed in favor of the
	// single canonical safety implementation.)
	return nil
}

func (e *Executor) resolveTargetPath(targetPath string) (string, error) {
	roots := append([]string{e.workspaceRoot}, e.allowedPaths...)
	absPath, err := safety.ResolveAndContainMulti(roots, targetPath)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absPath), nil
}

// DenyApproval marks an approval as denied without applying the mutation.
func (e *Executor) DenyApproval(runID, approvalID, deniedBy, reason string) error {
	a, err := e.approvalStore.Load(runID, approvalID)
	if err != nil {
		return fmt.Errorf("approval not found: %w", err)
	}

	if err := a.Deny(deniedBy, reason); err != nil {
		return err
	}

	if err := e.approvalStore.Save(a); err != nil {
		return fmt.Errorf("failed to save denied approval: %w", err)
	}

	e.emitEvent("prizm.approval.denied", map[string]any{
		"approval_id":     approvalID,
		"run_id":          runID,
		"mutation_type":   a.MutationType,
		"target_path":     a.TargetPath,
		"correlation_id":  a.CorrelationID,
		"denied_by":       deniedBy,
		"denial_reason":   reason,
		"policy_decision": a.Policy.Decision,
	})

	return nil
}

// emitEvent calls the configured emitter if it exists.
func (e *Executor) emitEvent(eventType string, payload map[string]any) {
	if e.emit != nil {
		e.emit(eventType, "prizm-mutation-executor", payload)
	}
}
