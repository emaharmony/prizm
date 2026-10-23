package multiagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/subagent"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
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
	if basis := request.TrustedTreeBasis; basis != nil {
		if basis.RunID != request.RunID || basis.WorkspaceID != request.Workspace.ID || basis.Role != string(RoleDeveloper) || request.Role != RoleDeveloper {
			return AgentExecutionResult{}, fmt.Errorf("multiagent: trusted patch basis scope does not match role execution")
		}
		ctx = gitx.ContextWithTrustedTreeBasis(ctx, *basis)
	}

	deadlineValue := ""
	if !request.Deadline.IsZero() {
		deadlineValue = request.Deadline.UTC().Format(time.RFC3339)
	}
	maxTokens := int(request.MaxTokens)
	expectedDeliverable := "one strict JSON object matching the role schema"
	finalizationPrerequisiteTool := strings.TrimSpace(request.FinalizationPrerequisiteTool)
	if finalizationPrerequisiteTool != "" {
		expectedDeliverable = "first emit one apply_patch_proposal tool_request JSON containing the complete unified diff and exact base_sha; after its result, emit one strict developer role-schema JSON"
	}
	if finalizationPrerequisiteTool != "" {
		allowed := false
		for _, toolName := range request.AllowedTools {
			if toolName == finalizationPrerequisiteTool {
				allowed = true
				break
			}
		}
		if !allowed {
			return AgentExecutionResult{}, fmt.Errorf("multiagent: finalization prerequisite tool %q is not allowed for role %q", finalizationPrerequisiteTool, request.Role)
		}
	}
	result, err := e.Runner.Run(ctx, v2.TaskPacket{
		Type:                         "task_delegation",
		TargetAgent:                  request.Profile.ID,
		TaskID:                       executionTaskID(request),
		Description:                  request.Prompt,
		ExpectedDeliverable:          expectedDeliverable,
		FinalizationPrerequisiteTool: finalizationPrerequisiteTool,
		FinalResponseContract:        strings.TrimSpace(request.FinalResponseContract),
		ExploreReplyMaxTokens:        request.ExploreReplyMaxTokens,
		PrerequisiteReplyMaxTokens:   request.PrerequisiteReplyMaxTokens,
		FinalReplyMaxTokens:          request.FinalReplyMaxTokens,
		Deadline:                     deadlineValue,
		MaxTokens:                    maxTokens,
	}, subagent.AgentRuntime{
		AgentID:             request.Profile.ID,
		Provider:            request.Profile.Provider,
		Model:               request.Profile.Model,
		ReasoningEffort:     request.Profile.ReasoningEffort,
		Capabilities:        append([]string(nil), request.Profile.Capabilities...),
		WorkDir:             request.Workspace.Path,
		RunID:               request.RunID,
		TaskID:              request.TaskID,
		ExecutionKey:        request.ExecutionKey,
		AllowedTools:        append([]string(nil), request.AllowedTools...),
		EnforceAllowedTools: true,
		MaxIterations:       int(request.MaxIterations),
		Role:                string(request.Role),
		RetryHooks:          subagentRetryHooks{handle: request.RetryHandle},
		ToolEventSink:       subagentToolSink{sink: request.ToolEventSink}.emit,
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
		FailureClass:    result.LastRecoveryClass,
		FailureCode:     result.LastRecoveryCode,
		FailureOrigin:   FailureOrigin(result.LastRecoveryOrigin),
	}
	if err != nil {
		if errors.Is(err, subagent.ErrRecoveryBudgetExhausted) {
			return executionResult, ErrRetryBudgetExhausted
		}
		return executionResult, err
	}
	return executionResult, nil
}

type subagentRetryHooks struct{ handle RoleVisitRetryHandle }

func (h subagentRetryHooks) CurrentAttempt() subagent.RetryAttempt {
	if h.handle == nil {
		return subagent.RetryAttempt{}
	}
	a := h.handle.CurrentAttempt()
	return subagent.RetryAttempt{VisitID: a.VisitID, AttemptID: a.AttemptID, Number: a.Number, RecoveryNumber: a.RecoveryNumber, Remaining: a.Remaining, NextAt: a.NextAt}
}
func (h subagentRetryHooks) NoProgressCount(id, hash string) (int, error) {
	if h.handle == nil {
		return 0, nil
	}
	return h.handle.NoProgressCount(id, hash)
}
func (h subagentRetryHooks) ConsumeRecovery(id, code string) (subagent.RetryAttempt, error) {
	if h.handle == nil {
		return subagent.RetryAttempt{}, nil
	}
	a, err := h.handle.ConsumeRecovery(id, code)
	return subagent.RetryAttempt{VisitID: a.VisitID, AttemptID: a.AttemptID, Number: a.Number, RecoveryNumber: a.RecoveryNumber, Remaining: a.Remaining, NextAt: a.NextAt}, err
}
func (h subagentRetryHooks) RecordProgress(id, kind, ref, hash string) error {
	if h.handle == nil {
		return nil
	}
	return h.handle.RecordProgress(id, kind, ref, hash)
}
func (h subagentRetryHooks) RecordCorrection(id, class, code string) error {
	if h.handle == nil {
		return nil
	}
	return h.handle.RecordCorrection(id, class, code)
}
func (h subagentRetryHooks) RecordCorrectionFeedback(id, class, code, feedback string) error {
	if h.handle == nil {
		return nil
	}
	return h.handle.RecordCorrectionFeedback(id, class, code, feedback)
}
func (h subagentRetryHooks) RecordCorrectionWithOrigin(id, class, code, origin, feedback string) error {
	if h.handle == nil {
		return nil
	}
	if recorder, ok := h.handle.(interface {
		RecordCorrectionWithOrigin(string, string, string, string, string) error
	}); ok {
		return recorder.RecordCorrectionWithOrigin(id, class, code, origin, feedback)
	}
	if feedback != "" {
		return h.handle.RecordCorrectionFeedback(id, class, code, feedback)
	}
	return h.handle.RecordCorrection(id, class, code)
}
func (h subagentRetryHooks) ConfirmNoEffects(id, mutationID string) error {
	if h.handle == nil {
		return nil
	}
	return h.handle.ConfirmNoEffects(id, mutationID)
}

type subagentToolSink struct{ sink ToolEventSink }

func (s subagentToolSink) emit(d subagent.ToolDiagnostic) error {
	if s.sink == nil {
		return nil
	}
	return s.sink(ToolDiagnostic{RunID: d.RunID, TaskID: d.TaskID, Role: Role(d.Role), ExecutionKey: d.ExecutionKey, AttemptID: d.AttemptID, MutationID: d.MutationID, NoProgressEligible: d.NoProgressEligible, Tool: d.Tool, Status: d.Status, ErrorCode: d.ErrorCode, Sequence: d.Sequence, Duration: d.Duration, SafeRef: d.SafeRef, ObservationHash: d.ObservationHash, MutationPossible: d.MutationPossible, NoEffectsProven: d.NoEffectsProven})
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
