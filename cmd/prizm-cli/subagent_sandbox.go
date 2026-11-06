package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/sandbox"
	"github.com/emaharmony/prizm/internal/subagent"
	"github.com/emaharmony/prizm/internal/tool"
)

const subAgentSandboxToolName = "sandbox_exec"
const subAgentSandboxProposalToolName = "sandbox_propose_artifact"

// subAgentSandboxTool is the only effectful tool exposed to code-capable
// agents in the opt-in OCI lane. It never writes the source worktree: results
// are exported as a review archive outside the disposable sandbox.
type subAgentSandboxTool struct {
	provider   *sandbox.Provider
	sourceRoot string
	identity   *sandboxRuntimeIdentity

	mu             sync.Mutex
	session        subAgentSandboxSession
	proposalDigest string
}

type sandboxRuntimeIdentity struct {
	runID, taskID, executionKey, agentID, workspaceID, workspacePath string
}

func newSandboxRuntimeIdentity(rt subagent.AgentRuntime) (sandboxRuntimeIdentity, error) {
	path, err := filepath.Abs(filepath.Clean(rt.WorkDir))
	if err != nil || rt.RunID == "" || rt.TaskID == "" || rt.ExecutionKey == "" || rt.AgentID == "" || rt.WorkspaceID == "" {
		return sandboxRuntimeIdentity{}, fmt.Errorf("sandbox runtime identity is incomplete")
	}
	return sandboxRuntimeIdentity{runID: rt.RunID, taskID: rt.TaskID, executionKey: rt.ExecutionKey, agentID: rt.AgentID, workspaceID: rt.WorkspaceID, workspacePath: path}, nil
}

func (i sandboxRuntimeIdentity) matches(rt subagent.AgentRuntime) bool {
	path, err := filepath.Abs(filepath.Clean(rt.WorkDir))
	return err == nil && i.runID == rt.RunID && i.taskID == rt.TaskID && i.executionKey == rt.ExecutionKey &&
		i.agentID == rt.AgentID && i.workspaceID == rt.WorkspaceID && i.workspacePath == path
}

type subAgentSandboxSession interface {
	Execute(context.Context, string) (string, int, error)
	ExportArtifact() (string, error)
	ReviewPatch(context.Context) (string, string, string, string, error)
	Close() error
}

func (t *subAgentSandboxTool) Name() string { return subAgentSandboxToolName }

func (t *subAgentSandboxTool) Description() string {
	return "Run one bounded command in the isolated network-disabled code sandbox; changes are returned as a review-only archive and are never applied to the host repository."
}

func (t *subAgentSandboxTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{
		Input: map[string]tool.ParamSpec{
			"command": {Type: "string", Description: "A bounded shell command executed inside the isolated workspace.", Required: true},
		},
		Output: tool.ParamSpec{Type: "object", Description: "Bounded command output, exit code, and review archive path."},
	}
}

func (t *subAgentSandboxTool) Execute(ctx context.Context, input map[string]any) (tool.ToolResult, error) {
	command, ok := input["command"].(string)
	if !ok {
		return tool.ToolResult{Success: false, Error: "sandbox command must be text"}, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.proposalDigest != "" {
		return tool.ToolResult{Success: false, Error: "sandbox session is frozen after artifact proposal"}, nil
	}
	if t.session == nil {
		if t.provider == nil || t.sourceRoot == "" {
			return tool.ToolResult{Success: false, Error: "sandbox is not configured for this task"}, nil
		}
		session, err := t.provider.Open(ctx, t.sourceRoot)
		if err != nil {
			return tool.ToolResult{Success: false, Error: "isolated sandbox is unavailable; command was not run"}, nil
		}
		t.session = session
		context.AfterFunc(ctx, func() { _ = t.close() })
	}
	stdout, exitCode, err := t.session.Execute(ctx, command)
	if err != nil {
		return tool.ToolResult{Success: false, Error: "isolated sandbox command failed; no host fallback was attempted"}, nil
	}
	artifact, err := t.session.ExportArtifact()
	if err != nil {
		return tool.ToolResult{Success: false, Error: "sandbox result could not be exported"}, nil
	}
	return tool.ToolResult{Success: true, Output: map[string]any{
		"stdout":          stdout,
		"exit_code":       exitCode,
		"review_artifact": artifact,
	}}, nil
}

func (t *subAgentSandboxTool) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.session == nil {
		return nil
	}
	err := t.session.Close()
	t.session = nil
	return err
}

// subAgentSandboxProposalTool has no model-controlled inputs. Its session,
// workspace identity and approval executor are captured by the host.
type subAgentSandboxProposalTool struct {
	sandbox      *subAgentSandboxTool
	approvalExec *tool.Executor
	runtime      subagent.AgentRuntime
	project      string
}

func (t *subAgentSandboxProposalTool) Name() string { return subAgentSandboxProposalToolName }
func (t *subAgentSandboxProposalTool) Description() string {
	return "Submit the current isolated sandbox changes as a normal pending human approval. This tool takes no paths or arguments and never applies changes."
}
func (t *subAgentSandboxProposalTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Input: map[string]tool.ParamSpec{}, Output: tool.ParamSpec{Type: "object", Description: "Pending approval identity for the current sandbox changes"}}
}
func (t *subAgentSandboxProposalTool) Execute(ctx context.Context, input map[string]any) (tool.ToolResult, error) {
	if len(input) != 0 {
		return tool.ToolResult{Success: false, Error: "sandbox proposal tool accepts no input"}, nil
	}
	rt := t.runtime
	if t.sandbox == nil || t.approvalExec == nil || rt.RunID == "" || rt.TaskID == "" || rt.ExecutionKey == "" || rt.AgentID == "" || rt.WorkspaceID == "" || rt.WorkDir == "" {
		return tool.ToolResult{Success: false, Error: "sandbox proposal identity is incomplete"}, nil
	}
	if t.sandbox.identity == nil || !t.sandbox.identity.matches(rt) {
		return tool.ToolResult{Success: false, Error: "sandbox session belongs to a different trusted runtime identity"}, nil
	}
	workDir, err := filepath.Abs(filepath.Clean(rt.WorkDir))
	if err != nil {
		return tool.ToolResult{Success: false, Error: "canonical sandbox workspace is unavailable"}, nil
	}
	sourceRoot, err := filepath.Abs(filepath.Clean(t.sandbox.sourceRoot))
	workInfo, workErr := os.Stat(workDir)
	sourceInfo, sourceErr := os.Stat(sourceRoot)
	if err != nil || workErr != nil || sourceErr != nil || !os.SameFile(workInfo, sourceInfo) {
		return tool.ToolResult{Success: false, Error: "sandbox session is not bound to the canonical run workspace"}, nil
	}
	t.sandbox.mu.Lock()
	defer t.sandbox.mu.Unlock()
	if t.sandbox.session == nil {
		return tool.ToolResult{Success: false, Error: "sandbox has no current session to propose"}, nil
	}
	patch, baseSHA, baseTree, changeDigest, err := t.sandbox.session.ReviewPatch(ctx)
	if err != nil {
		return tool.ToolResult{Success: false, Error: "sandbox changes could not be bound to the clean run workspace"}, nil
	}
	if _, err := t.sandbox.session.ExportArtifact(); err != nil {
		return tool.ToolResult{Success: false, Error: "sandbox review artifact could not be validated"}, nil
	}
	key, err := sandboxProposalIdempotencyKey(rt.RunID, rt.TaskID, rt.ExecutionKey, rt.AgentID, rt.WorkspaceID)
	if err != nil {
		return tool.ToolResult{Success: false, Error: "sandbox proposal identity could not be serialized"}, nil
	}
	if t.sandbox.proposalDigest != "" && t.sandbox.proposalDigest != changeDigest {
		return tool.ToolResult{Success: false, Error: "sandbox session artifact changed after proposal"}, nil
	}
	t.sandbox.proposalDigest = changeDigest
	if err := rejectDifferentExecutionArtifact(t.approvalExec.ApprovalStore, rt.RunID, rt.ExecutionKey, rt.AgentID, key); err != nil {
		return tool.ToolResult{Success: false, Error: "this task execution already has a different or unbound patch proposal"}, nil
	}
	binding := approval.SandboxProposalBinding{TaskID: rt.TaskID, ExecutionKey: rt.ExecutionKey, AgentID: rt.AgentID, WorkspaceID: rt.WorkspaceID, WorkspacePath: workDir, BaseSHA: baseSHA, BaseTree: baseTree, ArtifactDigest: changeDigest}
	hostCtx := tool.WithApprovalSandboxBinding(tool.WithApprovalIdempotencyKey(ctx, key), binding)
	inputForApproval := map[string]any{"patch": patch, "base_sha": baseSHA, "_run_id": rt.RunID}
	correlationID := rt.ExecutionKey
	if t.project == "" {
		t.project = "multiagent"
	}
	result, err := t.approvalExec.ExecuteWithPolicy(hostCtx, "apply_patch_proposal", rt.AgentID, t.project, correlationID, inputForApproval)
	if err != nil || !result.Success {
		return result, err
	}
	if result.Output == nil || result.Output["approval_id"] == "" || result.Output["proposal_id"] == "" {
		return tool.ToolResult{Success: false, Error: "patch proposal did not persist through the approval store", FailureEffect: tool.FailureEffectUnknown}, nil
	}
	status, _ := result.Output["status"].(string)
	if status != "pending_approval" && status != "approved" && status != "denied" && status != "expired" {
		return tool.ToolResult{Success: false, Error: "patch proposal has no durable approval decision", FailureEffect: tool.FailureEffectUnknown}, nil
	}
	return result, nil
}

func sandboxProposalIdempotencyKey(runID, taskID, executionKey, agentID, workspaceID string) (string, error) {
	values := []string{runID, taskID, executionKey, agentID, workspaceID}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("sandbox proposal identity is incomplete")
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func rejectDifferentExecutionArtifact(store tool.ApprovalStorer, runID, executionKey, agentID, key string) error {
	lister, ok := store.(interface {
		List(string) ([]*approval.Approval, error)
	})
	if !ok {
		return fmt.Errorf("approval store cannot inspect existing execution proposals")
	}
	items, err := lister.List(runID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.CorrelationID != executionKey || item.RequestedBy != agentID || item.MutationType != approval.MutationApplyPatch {
			continue
		}
		if item.IdempotencyKey != key {
			return fmt.Errorf("execution already has a different patch proposal")
		}
	}
	return nil
}

func sandboxToolInfos() []tool.ToolInfo {
	t := &subAgentSandboxTool{}
	return []tool.ToolInfo{{Name: t.Name(), Description: t.Description(), Schema: t.Schema()}}
}

func sandboxApprovalToolInfos() []tool.ToolInfo {
	infos := sandboxToolInfos()
	proposal := &subAgentSandboxProposalTool{}
	infos = append(infos, tool.ToolInfo{Name: proposal.Name(), Description: proposal.Description(), Schema: proposal.Schema()})
	return infos
}

func sandboxApprovalPromptToolInfos(infos []tool.ToolInfo) []tool.ToolInfo {
	out := make([]tool.ToolInfo, 0, len(infos))
	for _, info := range infos {
		if info.Name == "apply_patch_proposal" {
			continue
		}
		out = append(out, info)
	}
	return out
}

func hasSubAgentCapability(runtime subagent.AgentRuntime, capability string) bool {
	for _, candidate := range runtime.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

var _ tool.Tool = (*subAgentSandboxTool)(nil)

func (b *subAgentBackend) sandboxExecutor(sourceRoot string) (*tool.Executor, error) {
	sandboxTool, err := newSubAgentSandboxTool(sourceRoot)
	if err != nil {
		return nil, err
	}
	reg := tool.NewRegistry()
	if err := reg.Register(sandboxTool); err != nil {
		return nil, err
	}
	executor := tool.NewExecutor(reg, b.exec.Policy)
	executor.PolicyEvaluator = b.exec.PolicyEvaluator
	executor.Emit = b.exec.Emit
	return executor, nil
}

func (b *subAgentBackend) sandboxApprovalExecutor(rt subagent.AgentRuntime) (*tool.Executor, error) {
	if strings.TrimSpace(rt.WorkDir) == "" || rt.WorkspaceID == "" || rt.RunID == "" || rt.TaskID == "" || rt.ExecutionKey == "" || rt.AgentID == "" {
		return nil, fmt.Errorf("sandbox approval bridge requires trusted run, task, execution, agent and workspace identity")
	}
	if b.exec == nil || b.exec.ApprovalStore == nil {
		return nil, fmt.Errorf("sandbox approval bridge requires the existing durable approval store")
	}
	if _, ok := b.exec.ApprovalStore.(interface {
		SaveIdempotent(*approval.Approval) (*approval.Approval, bool, error)
	}); !ok {
		return nil, fmt.Errorf("sandbox approval bridge requires idempotent approval persistence")
	}
	// Reuse the exact host-owned session tool instance so the pathless submit
	// operation can only inspect the session opened by sandbox_exec.
	sandboxTool, err := newSubAgentSandboxTool(rt.WorkDir)
	if err != nil {
		return nil, err
	}
	identity, err := newSandboxRuntimeIdentity(rt)
	if err != nil {
		return nil, err
	}
	sandboxTool.identity = &identity
	registry := tool.NewRegistry()
	if err := registry.Register(sandboxTool); err != nil {
		return nil, err
	}
	if err := registry.Register(&subAgentSandboxProposalTool{sandbox: sandboxTool, approvalExec: b.exec, runtime: rt, project: "multiagent"}); err != nil {
		return nil, err
	}
	executor := tool.NewExecutor(registry, b.exec.Policy)
	executor.PolicyEvaluator = b.exec.PolicyEvaluator
	executor.Emit = b.exec.Emit
	return executor, nil
}

func newSubAgentSandboxTool(sourceRoot string) (*subAgentSandboxTool, error) {
	if strings.TrimSpace(sourceRoot) == "" {
		return nil, fmt.Errorf("sandbox code lane requires an isolated task worktree")
	}
	t := &subAgentSandboxTool{provider: sandbox.NewProvider(sandbox.Config{
		DockerPath: os.Getenv("PRIZM_SUBAGENT_SANDBOX_DOCKER"), Image: os.Getenv("PRIZM_SUBAGENT_SANDBOX_IMAGE"),
		WorkRoot: os.Getenv("PRIZM_SUBAGENT_SANDBOX_WORK_ROOT"), ArtifactRoot: os.Getenv("PRIZM_SUBAGENT_SANDBOX_ARTIFACT_ROOT"),
	}), sourceRoot: sourceRoot}
	return t, nil
}
