package multiagent

import (
	"context"
	"testing"

	"github.com/emaharmony/prism/internal/subagent"
	v2 "github.com/emaharmony/prism/internal/workflow/v2"
)

type subagentTaskRunnerFunc func(
	context.Context,
	v2.TaskPacket,
	subagent.AgentRuntime,
) (subagent.RunResult, error)

func (f subagentTaskRunnerFunc) Run(
	ctx context.Context,
	packet v2.TaskPacket,
	runtime subagent.AgentRuntime,
) (subagent.RunResult, error) {
	return f(ctx, packet, runtime)
}

func TestSubagentExecutorMapsRuntimeAndResult(t *testing.T) {
	executor := SubagentExecutor{Runner: subagentTaskRunnerFunc(func(
		_ context.Context,
		packet v2.TaskPacket,
		runtime subagent.AgentRuntime,
	) (subagent.RunResult, error) {
		if packet.TargetAgent != "developer-agent" ||
			packet.TaskID != "run-1-developer-2" {
			t.Errorf("packet = %#v", packet)
		}
		if runtime.WorkDir != "/workspace/run-1" ||
			!runtime.EnforceAllowedTools ||
			len(runtime.AllowedTools) != 1 ||
			runtime.MaxIterations != 4 {
			t.Errorf("runtime = %#v", runtime)
		}
		return subagent.RunResult{
			Summary: `{"schema_version":1}`,
			Artifacts: v2.CompletionArtifacts{
				FilePaths:    []string{"internal/adapter.go"},
				CommitHashes: []string{"abc123"},
				PRURLs:       []string{"https://example.test/pr/1"},
			},
			PromptTokens:     8,
			CompletionTokens: 2,
			Iterations:       3,
			ToolCalls:        2,
			DeniedToolCalls:  1,
		}, nil
	})}

	result, err := executor.ExecuteAgent(context.Background(), AgentExecutionRequest{
		RunID:  "run-1",
		TaskID: "task-1",
		Role:   RoleDeveloper,
		Visit:  2,
		Profile: AgentProfile{
			ID:           "developer-agent",
			Provider:     "mock",
			Model:        "mock-model",
			Capabilities: []string{"code"},
		},
		Workspace:     Workspace{ID: "workspace-run-1", Path: "/workspace/run-1"},
		AllowedTools:  []string{"read_file"},
		MaxIterations: 4,
		MaxTokens:     100,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Usage.TotalTokens != 10 || result.LocalIterations != 3 ||
		result.ToolCalls != 2 || result.DeniedToolCalls != 1 {
		t.Errorf("result = %#v", result)
	}
	if len(result.Artifacts) != 3 {
		t.Errorf("artifacts = %v", result.Artifacts)
	}
}
