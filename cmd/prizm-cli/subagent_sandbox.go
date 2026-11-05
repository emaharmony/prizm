package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/emaharmony/prizm/internal/sandbox"
	"github.com/emaharmony/prizm/internal/subagent"
	"github.com/emaharmony/prizm/internal/tool"
)

const subAgentSandboxToolName = "sandbox_exec"

// subAgentSandboxTool is the only effectful tool exposed to code-capable
// agents in the opt-in OCI lane. It never writes the source worktree: results
// are exported as a review archive outside the disposable sandbox.
type subAgentSandboxTool struct {
	provider   *sandbox.Provider
	sourceRoot string

	mu      sync.Mutex
	session *sandbox.Session
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

func sandboxToolInfos() []tool.ToolInfo {
	t := &subAgentSandboxTool{}
	return []tool.ToolInfo{{Name: t.Name(), Description: t.Description(), Schema: t.Schema()}}
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
	if strings.TrimSpace(sourceRoot) == "" {
		return nil, fmt.Errorf("sandbox code lane requires an isolated task worktree")
	}
	reg := tool.NewRegistry()
	if err := reg.Register(&subAgentSandboxTool{
		provider: sandbox.NewProvider(sandbox.Config{
			DockerPath:   os.Getenv("PRIZM_SUBAGENT_SANDBOX_DOCKER"),
			Image:        os.Getenv("PRIZM_SUBAGENT_SANDBOX_IMAGE"),
			WorkRoot:     os.Getenv("PRIZM_SUBAGENT_SANDBOX_WORK_ROOT"),
			ArtifactRoot: os.Getenv("PRIZM_SUBAGENT_SANDBOX_ARTIFACT_ROOT"),
		}),
		sourceRoot: sourceRoot,
	}); err != nil {
		return nil, err
	}
	executor := tool.NewExecutor(reg, b.exec.Policy)
	executor.PolicyEvaluator = b.exec.PolicyEvaluator
	executor.Emit = b.exec.Emit
	return executor, nil
}
