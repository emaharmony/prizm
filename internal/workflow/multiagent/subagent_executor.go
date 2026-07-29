package multiagent

import (
	"context"
	"fmt"
	"time"

	"github.com/emaharmony/prism/internal/cost"
	"github.com/emaharmony/prism/internal/subagent"
	v2 "github.com/emaharmony/prism/internal/workflow/v2"
)

// SubagentExecutor adapts the existing bounded sub-agent TaskRunner. The
// production LoopRunner backend continues to own provider and governed tool
// execution.
type SubagentExecutor struct {
	Runner subagent.TaskRunner
}

// ExecuteAgent implements AgentExecutor.
func (e SubagentExecutor) ExecuteAgent(
	ctx context.Context,
	request AgentExecutionRequest,
) (AgentExecutionResult, error) {
	if e.Runner == nil {
		return AgentExecutionResult{}, fmt.Errorf("multiagent: sub-agent task runner is required")
	}

	deadlineValue := ""
	if !request.Deadline.IsZero() {
		deadlineValue = request.Deadline.UTC().Format(time.RFC3339)
	}
	maxTokens := int(request.MaxTokens)
	result, err := e.Runner.Run(ctx, v2.TaskPacket{
		Type:                "task_delegation",
		TargetAgent:         request.Profile.ID,
		TaskID:              executionTaskID(request),
		Description:         request.Prompt,
		ExpectedDeliverable: "one strict JSON object matching the role schema",
		Deadline:            deadlineValue,
		MaxTokens:           maxTokens,
	}, subagent.AgentRuntime{
		AgentID:             request.Profile.ID,
		Provider:            request.Profile.Provider,
		Model:               request.Profile.Model,
		Capabilities:        append([]string(nil), request.Profile.Capabilities...),
		WorkDir:             request.Workspace.Path,
		RunID:               request.RunID,
		TaskID:              request.TaskID,
		ExecutionKey:        request.ExecutionKey,
		AllowedTools:        append([]string(nil), request.AllowedTools...),
		EnforceAllowedTools: true,
		MaxIterations:       int(request.MaxIterations),
	})

	executionResult := AgentExecutionResult{
		Output:    result.Summary,
		Artifacts: completionArtifacts(result.Artifacts),
		Usage: cost.TokenUsage{
			PromptTokens:     result.PromptTokens,
			CompletionTokens: result.CompletionTokens,
			TotalTokens:      result.PromptTokens + result.CompletionTokens,
		},
		LocalIterations: result.Iterations,
		ToolCalls:       result.ToolCalls,
		DeniedToolCalls: result.DeniedToolCalls,
	}
	if err != nil {
		return executionResult, err
	}
	return executionResult, nil
}

func executionTaskID(request AgentExecutionRequest) string {
	if request.ExecutionKey != "" {
		return request.ExecutionKey
	}
	return fmt.Sprintf("%s-%s-%d", request.RunID, request.Role, request.Visit)
}

func completionArtifacts(artifacts v2.CompletionArtifacts) []ArtifactRef {
	refs := make([]ArtifactRef, 0, len(artifacts.FilePaths)+len(artifacts.CommitHashes)+len(artifacts.PRURLs))
	for _, path := range artifacts.FilePaths {
		refs = append(refs, ArtifactRef{Kind: ArtifactFile, URI: path})
	}
	for _, hash := range artifacts.CommitHashes {
		refs = append(refs, ArtifactRef{Kind: ArtifactCommit, URI: hash})
	}
	for _, url := range artifacts.PRURLs {
		refs = append(refs, ArtifactRef{Kind: ArtifactPullRequest, URI: url})
	}
	return refs
}
