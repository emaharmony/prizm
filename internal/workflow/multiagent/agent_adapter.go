package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/validation"
)

// AgentRoleRunner adapts Prism's bounded agent execution seam to RoleRunner.
type AgentRoleRunner struct {
	profiles   AgentProfileResolver
	executor   AgentExecutor
	workspaces WorkspaceResolver
	approvals  ApprovalChecker
	validation ValidationRunner
	proposals  ProposalResolver
	events     EventSink
	now        func() time.Time
}

// SetEventSink connects validation lifecycle events to the durable runtime's
// canonical outbox. It is called during runtime composition.
func (r *AgentRoleRunner) SetEventSink(sink EventSink) { r.events = sink }

// AgentRoleRunnerOptions supplies the existing governance and workspace
// authorities used by the adapter.
type AgentRoleRunnerOptions struct {
	Profiles   AgentProfileResolver
	Executor   AgentExecutor
	Workspaces WorkspaceResolver
	Approvals  ApprovalChecker
	Validation ValidationRunner
	Proposals  ProposalResolver
	Clock      func() time.Time
}

// NewAgentRoleRunner creates the real Prism agent adapter.
func NewAgentRoleRunner(options AgentRoleRunnerOptions) (*AgentRoleRunner, error) {
	if options.Profiles == nil {
		return nil, errors.New("multiagent: agent profile resolver is required")
	}
	if options.Executor == nil {
		return nil, errors.New("multiagent: agent executor is required")
	}
	if options.Workspaces == nil {
		return nil, errors.New("multiagent: run workspace resolver is required")
	}
	now := options.Clock
	if now == nil {
		now = time.Now
	}
	return &AgentRoleRunner{
		profiles:   options.Profiles,
		executor:   options.Executor,
		workspaces: options.Workspaces,
		approvals:  options.Approvals,
		validation: options.Validation,
		proposals:  options.Proposals,
		now:        now,
	}, nil
}

// RunRole resolves the configured Prism agent, executes bounded local work,
// strictly decodes its role output, applies governance results, and returns a
// typed supervisor result.
func (r *AgentRoleRunner) RunRole(
	ctx context.Context,
	request RoleRunRequest,
) (RoleRunResult, error) {
	if err := ctx.Err(); err != nil {
		return RoleRunResult{}, err
	}

	profile, err := r.profiles.ResolveAgent(request.RoleConfig.AgentRef)
	if err != nil {
		return RoleRunResult{}, fmt.Errorf("resolve agent profile %q: %w", request.RoleConfig.AgentRef, err)
	}
	if err := requireCapabilities(profile, request.RoleConfig.Capabilities); err != nil {
		return RoleRunResult{}, err
	}
	workspace, err := r.workspaces.ResolveWorkspace(ctx, request.Run.RunID)
	if err != nil {
		return RoleRunResult{}, fmt.Errorf("resolve run workspace: %w", err)
	}
	if strings.TrimSpace(workspace.ID) == "" || strings.TrimSpace(workspace.Path) == "" {
		return RoleRunResult{}, errors.New("multiagent: run workspace requires id and path")
	}
	if expected := strings.TrimSpace(request.Run.WorkspaceID); expected != "" && workspace.ID != expected {
		return RoleRunResult{}, &GovernanceError{
			Kind: "workspace",
			Reason: fmt.Sprintf(
				"resolved workspace %q does not match persisted workspace %q",
				workspace.ID, expected,
			),
		}
	}

	approvalStatus, err := r.checkApproval(ctx, request, profile)
	if err != nil {
		return RoleRunResult{}, err
	}

	prompt, err := BuildRolePrompt(request)
	if err != nil {
		return RoleRunResult{}, err
	}
	startedAt := r.now().UTC()
	executionContext := ctx
	cancelExecution := func() {}
	if request.RoleConfig.TimeBudget != UnlimitedDuration {
		executionContext, cancelExecution = context.WithTimeout(ctx, request.RoleConfig.TimeBudget)
	}
	defer cancelExecution()
	executionRequest := AgentExecutionRequest{
		RunID:                request.Run.RunID,
		TaskID:               request.Run.Task.ID,
		ExecutionKey:         request.Run.ExecutionKey,
		Role:                 request.Run.CurrentRole,
		Visit:                request.Run.Visit,
		Profile:              profile,
		Prompt:               prompt,
		Workspace:            workspace,
		AllowedTools:         append([]string(nil), request.RoleConfig.AllowedTools...),
		RequiredCapabilities: append([]string(nil), request.RoleConfig.Capabilities...),
		MaxIterations:        request.RoleConfig.MaxLocalIterations,
		MaxTokens:            request.RoleConfig.TokenBudget,
		Deadline:             deadline(startedAt, request.RoleConfig.TimeBudget),
	}
	execution, err := r.executor.ExecuteAgent(executionContext, executionRequest)
	if err != nil {
		return RoleRunResult{}, err
	}
	var proposals []ProposalReference
	correctiveRetries := 0
	if r.proposals != nil {
		proposals, err = r.proposals.ResolveProposals(ctx, ProposalQuery{
			RunID: request.Run.RunID, ExecutionKey: request.Run.ExecutionKey, AgentID: profile.ID,
		})
		if err != nil {
			return RoleRunResult{}, fmt.Errorf("resolve execution proposals: %w", err)
		}
	}
	if request.Run.CurrentRole == RoleDeveloper && r.proposals != nil && len(proposals) == 0 {
		// A mutation-bearing developer may not complete with prose/structured
		// output alone. Give the provider one bounded correction opportunity to
		// create the proposal through the authorized tool boundary. The second
		// turn receives the first turn's output and shares its deadline; its
		// iteration and token ceilings are the remaining role budget.
		if err := executionContext.Err(); err != nil {
			return RoleRunResult{}, fmt.Errorf("proposal corrective turn: %w", err)
		}
		remainingIterations, ok := remainingLimit(request.RoleConfig.MaxLocalIterations, execution.LocalIterations)
		if !ok {
			return RoleRunResult{}, &GovernanceError{Kind: "proposal", Reason: "developer exhausted local iteration budget without persisting a mutation proposal"}
		}
		if remainingIterations != Unlimited && remainingIterations < 2 {
			return RoleRunResult{}, &GovernanceError{Kind: "proposal", Reason: "developer lacks the two remaining iterations required for proposal tool result and final role JSON"}
		}
		remainingTokens, ok := remainingLimit(request.RoleConfig.TokenBudget, execution.Usage.TotalTokens)
		if !ok {
			return RoleRunResult{}, &GovernanceError{Kind: "proposal", Reason: "developer exhausted token budget without persisting a mutation proposal"}
		}
		executionRequest.MaxIterations = remainingIterations
		executionRequest.MaxTokens = remainingTokens
		executionRequest.Prompt += fmt.Sprintf("\n\nCORRECTION: Your previous response did not persist a mutation proposal. Previous output follows:\n%s\n\nYour NEXT response must be ONLY one JSON tool_request for write_file_proposal. Do not include the developer role JSON in that response. After the tool result, return ONLY the required developer role JSON. This is your only corrective turn.", execution.Output)
		correctiveRetries = 1
		corrected, correctErr := r.executor.ExecuteAgent(executionContext, executionRequest)
		if correctErr != nil {
			return RoleRunResult{}, fmt.Errorf("proposal corrective turn: %w", correctErr)
		}
		execution.Usage.PromptTokens += corrected.Usage.PromptTokens
		execution.Usage.CompletionTokens += corrected.Usage.CompletionTokens
		execution.Usage.TotalTokens += corrected.Usage.TotalTokens
		execution.Usage.EstimatedCostUsd += corrected.Usage.EstimatedCostUsd
		execution.LocalIterations += corrected.LocalIterations
		execution.ToolCalls += corrected.ToolCalls
		execution.DeniedToolCalls += corrected.DeniedToolCalls
		execution.Output = corrected.Output
		execution.Artifacts = append(execution.Artifacts, corrected.Artifacts...)
		proposals, err = r.proposals.ResolveProposals(ctx, ProposalQuery{
			RunID: request.Run.RunID, ExecutionKey: request.Run.ExecutionKey, AgentID: profile.ID,
		})
		if err != nil {
			return RoleRunResult{}, fmt.Errorf("resolve corrected execution proposals: %w", err)
		}
		if len(proposals) == 0 {
			return RoleRunResult{}, &GovernanceError{Kind: "proposal", Reason: "developer completed without persisting a mutation proposal after one corrective turn"}
		}
	}

	var decoded decodedRoleOutput
	if request.Run.CurrentRole == RoleDeveloper && len(proposals) > 0 {
		decoded, err = decodeDeveloperOutputForProposals(execution.Output, proposals)
	} else {
		decoded, err = decodeRoleOutput(request.Run.CurrentRole, execution.Output)
	}
	if err != nil {
		return RoleRunResult{}, err
	}
	finishedAt := r.now().UTC()
	validationStatus := "not_required"
	// A developer proposal has not changed the workspace yet. Validation must
	// run after the durable approval/application transition, never against the
	// pre-change worktree.
	deferValidation := request.Run.CurrentRole == RoleDeveloper && len(proposals) > 0 && len(request.RoleConfig.ValidationProfiles) > 0
	if deferValidation {
		if r.validation == nil {
			return RoleRunResult{}, &GovernanceError{Kind: "validation", Reason: "mutation-bearing developer requires workspace-aware post-apply validation"}
		}
		if _, ok := r.validation.(WorkspaceValidationRunner); !ok {
			return RoleRunResult{}, &GovernanceError{Kind: "validation", Reason: "mutation-bearing developer requires a workspace-aware validation runner"}
		}
	}
	if !deferValidation {
		validationResults, status, validationErr := r.runValidations(ctx, request)
		if validationErr != nil {
			return RoleRunResult{}, validationErr
		}
		validationStatus = status
		if decoded.Handoff != nil {
			decoded.Handoff.ValidationResults = append([]validation.Result(nil), validationResults...)
		}
		if validationStatus == "failed" && request.Run.CurrentRole == RoleTester {
			decoded.Outcome = OutcomeTestsFailed
			if decoded.Handoff == nil {
				return RoleRunResult{}, &StructuredOutputError{Role: RoleTester, Cause: errors.New("validation failure requires a tester handoff")}
			}
			decoded.Handoff.Reason = "allowlisted validation failed"
		}
	} else {
		validationStatus = "deferred_until_applied"
	}
	if decoded.Handoff != nil {
		decoded.Handoff.Artifacts = mergeArtifacts(decoded.Handoff.Artifacts, execution.Artifacts)
	}

	localIterations := execution.LocalIterations
	if localIterations <= 0 {
		localIterations = 1
	}
	return RoleRunResult{
		Outcome:         decoded.Outcome,
		OutgoingHandoff: decoded.Handoff,
		TokenUsage:      execution.Usage,
		LocalIterations: localIterations,
		Retries:         correctiveRetries,
		Metadata: ExecutionMetadata{
			AgentRef:         profile.ID,
			Provider:         profile.Provider,
			Model:            profile.Model,
			WorkspaceID:      workspace.ID,
			ToolCalls:        execution.ToolCalls,
			DeniedToolCalls:  execution.DeniedToolCalls,
			ValidationStatus: validationStatus,
			ApprovalStatus:   string(approvalStatus),
			Attempt:          request.Run.Visit,
			StartedAt:        startedAt,
			FinishedAt:       finishedAt,
		},
		Proposals: proposals,
		FanOut:    cloneFanOutPlan(decoded.FanOut),
	}, nil
}

// ValidateApprovedRole validates a saved developer result after its exact
// approved proposal is applied. It is intentionally separate from RunRole so
// the durable runtime can checkpoint the pre-apply result before pausing.
func (r *AgentRoleRunner) ValidateApprovedRole(ctx context.Context, request RoleRunRequest, result RoleRunResult) (RoleRunResult, error) {
	if request.Run.CurrentRole != RoleDeveloper || len(result.Proposals) == 0 || len(request.RoleConfig.ValidationProfiles) == 0 {
		return result, nil
	}
	validationResults, validationStatus, err := r.runValidations(ctx, request)
	if result.OutgoingHandoff != nil {
		result.OutgoingHandoff.ValidationResults = append([]validation.Result(nil), validationResults...)
	}
	result.Metadata.ValidationStatus = validationStatus
	if err != nil {
		return result, err
	}
	return result, nil
}

// Reflect runs the dedicated reflector role through the same profile,
// workspace, approval, timeout, and bounded executor seams as normal roles.
func (r *AgentRoleRunner) Reflect(ctx context.Context, request ReflectionRequest) (ReflectionResult, error) {
	if err := ctx.Err(); err != nil {
		return ReflectionResult{}, err
	}
	profile, err := r.profiles.ResolveAgent(request.RoleConfig.AgentRef)
	if err != nil {
		return ReflectionResult{}, fmt.Errorf("resolve reflection agent profile %q: %w", request.RoleConfig.AgentRef, err)
	}
	if err := requireCapabilities(profile, request.RoleConfig.Capabilities); err != nil {
		return ReflectionResult{}, err
	}
	workspace, err := r.workspaces.ResolveWorkspace(ctx, request.Run.RunID)
	if err != nil {
		return ReflectionResult{}, fmt.Errorf("resolve reflection workspace: %w", err)
	}
	if strings.TrimSpace(workspace.ID) == "" || strings.TrimSpace(workspace.Path) == "" {
		return ReflectionResult{}, errors.New("multiagent: reflection workspace requires id and path")
	}
	roleRequest := RoleRunRequest{Run: request.Run, RoleConfig: request.RoleConfig}
	roleRequest.Run.CurrentRole = RoleReflector
	if _, err := r.checkApproval(ctx, roleRequest, profile); err != nil {
		return ReflectionResult{}, err
	}
	prompt, err := BuildReflectionPrompt(request)
	if err != nil {
		return ReflectionResult{}, err
	}
	startedAt := r.now().UTC()
	executionContext := ctx
	cancelExecution := func() {}
	if request.RoleConfig.TimeBudget != UnlimitedDuration {
		executionContext, cancelExecution = context.WithTimeout(ctx, request.RoleConfig.TimeBudget)
	}
	defer cancelExecution()
	execution, err := r.executor.ExecuteAgent(executionContext, AgentExecutionRequest{
		RunID: request.Run.RunID, TaskID: request.Run.Task.ID, ExecutionKey: request.Run.ExecutionKey,
		Role: RoleReflector, Visit: request.Run.Visit, Profile: profile, Prompt: prompt,
		Workspace: workspace, AllowedTools: append([]string(nil), request.RoleConfig.AllowedTools...),
		RequiredCapabilities: append([]string(nil), request.RoleConfig.Capabilities...),
		MaxIterations:        request.RoleConfig.MaxLocalIterations, MaxTokens: request.RoleConfig.TokenBudget,
		Deadline: deadline(startedAt, request.RoleConfig.TimeBudget),
	})
	if err != nil {
		return ReflectionResult{}, err
	}
	if _, _, err := r.runValidations(ctx, roleRequest); err != nil {
		return ReflectionResult{}, err
	}
	return decodeReflectionOutput(execution.Output)
}

// BuildRolePrompt renders only the current task, limited run accounting,
// incoming structured handoff, and the role's strict JSON contract.
func BuildRolePrompt(request RoleRunRequest) (string, error) {
	handoffJSON := "null"
	if request.Run.IncomingHandoff != nil {
		data, err := json.Marshal(request.Run.IncomingHandoff)
		if err != nil {
			return "", fmt.Errorf("marshal incoming handoff: %w", err)
		}
		handoffJSON = string(data)
	}
	reflectionJSON := "null"
	if request.Run.LatestReflection != nil {
		data, err := json.Marshal(request.Run.LatestReflection)
		if err != nil {
			return "", fmt.Errorf("marshal latest reflection: %w", err)
		}
		reflectionJSON = string(data)
	}
	schema, err := roleSchemaInstruction(request.Run.CurrentRole)
	if err != nil {
		return "", err
	}

	prompt := fmt.Sprintf(
		"ROLE: %s\nTASK ID: %s\nTASK: %s\nVISIT: %d\nTRANSITIONS USED: %d\n"+
			"INCOMING HANDOFF JSON: %s\nLATEST REFLECTION JSON: %s\n\n%s\n"+
			"Return exactly one JSON object. Do not wrap it in Markdown and do not add prose.",
		request.Run.CurrentRole,
		request.Run.Task.ID,
		request.Run.Task.Description,
		request.Run.Visit,
		request.Run.TransitionCount,
		handoffJSON,
		reflectionJSON,
		schema,
	)
	if request.Run.CurrentRole == RoleDeveloper {
		prompt += "\n\nDEVELOPER TOOL PROTOCOL: Before returning the developer role-schema JSON, emit exactly one separate JSON tool_request for write_file_proposal. Do not combine the tool request with the role-schema JSON. After Prizm returns the tool result, return only the developer role-schema JSON."
	}
	return prompt, nil
}

func BuildReflectionPrompt(request ReflectionRequest) (string, error) {
	input, err := json.Marshal(request.Input)
	if err != nil {
		return "", fmt.Errorf("marshal reflection input: %w", err)
	}
	schema, err := roleSchemaInstruction(RoleReflector)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ROLE: reflector\nTASK ID: %s\nTASK: %s\nREFLECTION INPUT JSON: %s\n\n%s\nReturn exactly one JSON object. Do not wrap it in Markdown and do not add prose.", request.Run.Task.ID, request.Run.Task.Description, input, schema), nil
}

func (r *AgentRoleRunner) checkApproval(
	ctx context.Context,
	request RoleRunRequest,
	profile AgentProfile,
) (ApprovalStatus, error) {
	if !request.RoleConfig.Approval.Required {
		return ApprovalNotRequired, nil
	}
	check := ApprovalCheck{
		RunID:     request.Run.RunID,
		Role:      request.Run.CurrentRole,
		AgentID:   profile.ID,
		Approvers: append([]Role(nil), request.RoleConfig.Approval.Approvers...),
		Visit:     request.Run.Visit,
	}
	if r.approvals == nil {
		return ApprovalPending, &ApprovalRequiredError{Check: check, Status: ApprovalPending}
	}
	status, err := r.approvals.CheckApproval(ctx, check)
	if err != nil {
		return status, fmt.Errorf("check approval: %w", err)
	}
	switch status {
	case ApprovalApproved:
		return status, nil
	case ApprovalPending, ApprovalDenied:
		return status, &ApprovalRequiredError{Check: check, Status: status}
	default:
		return status, &GovernanceError{Kind: "approval", Reason: "unknown approval status"}
	}
}

func (r *AgentRoleRunner) runValidations(
	ctx context.Context, request RoleRunRequest,
) ([]validation.Result, string, error) {
	if len(request.RoleConfig.ValidationProfiles) == 0 {
		return nil, "not_required", nil
	}
	if r.validation == nil {
		return nil, "unavailable", &GovernanceError{
			Kind:   "validation",
			Reason: "validation profiles configured but no validation runner is available",
		}
	}
	results := make([]validation.Result, 0, len(request.RoleConfig.ValidationProfiles))
	status := "passed"
	for _, profile := range request.RoleConfig.ValidationProfiles {
		r.emitValidationEvent(event.EventValidationStarted, request, profile, "started", "")
		var result *validation.Result
		var err error
		if workspaceRunner, ok := r.validation.(WorkspaceValidationRunner); ok {
			workspace, resolveErr := r.workspaces.ResolveWorkspace(ctx, request.Run.RunID)
			if resolveErr != nil {
				return results, "failed", fmt.Errorf("resolve validation workspace: %w", resolveErr)
			}
			result, err = workspaceRunner.RunValidationInWorkspace(ctx, profile, request.Run.RunID, workspace)
		} else {
			result, err = r.validation.RunValidation(ctx, profile, request.Run.RunID)
		}
		if result != nil {
			results = append(results, *result)
			if result.Status != "passed" {
				status = "failed"
			}
		}
		if err != nil && request.Run.CurrentRole != RoleTester {
			r.emitValidationEvent(event.EventValidationFailed, request, profile, "failed", err.Error())
			return results, "failed", fmt.Errorf("validation profile %q: %w", profile, err)
		}
		if err != nil {
			status = "failed"
			r.emitValidationEvent(event.EventValidationFailed, request, profile, "failed", err.Error())
		} else if result != nil && result.Status != "passed" {
			r.emitValidationEvent(event.EventValidationFailed, request, profile, result.Status, "validation result did not pass")
		} else {
			r.emitValidationEvent(event.EventValidationCompleted, request, profile, "passed", "")
		}
	}
	if status == "failed" && request.Run.CurrentRole != RoleTester {
		return results, status, &GovernanceError{
			Kind:   "validation",
			Reason: "allowlisted validation failed",
		}
	}
	return results, status, nil
}

func (r *AgentRoleRunner) emitValidationEvent(typ string, request RoleRunRequest, profile, status, message string) {
	if r.events == nil {
		return
	}
	payload := map[string]any{"run_id": request.Run.RunID, "workflow_id": request.Run.WorkflowID,
		"role": request.Run.CurrentRole, "profile": profile, "status": status}
	if message != "" {
		payload["error"] = message
	}
	r.events.Emit(event.NewEvent(typ, "prizm-multi-agent-validation", payload).WithCorrelationID(request.Run.RunID))
}

func requireCapabilities(profile AgentProfile, required []string) error {
	available := make(map[string]struct{}, len(profile.Capabilities))
	for _, capability := range profile.Capabilities {
		available[capability] = struct{}{}
	}
	for _, capability := range required {
		if _, ok := available[capability]; !ok {
			return &GovernanceError{
				Kind:   "capability",
				Reason: fmt.Sprintf("agent %q lacks required capability %q", profile.ID, capability),
			}
		}
	}
	return nil
}

func deadline(start time.Time, limit time.Duration) time.Time {
	if limit == UnlimitedDuration {
		return time.Time{}
	}
	return start.Add(limit)
}

// remainingLimit derives the allocation for the one permitted corrective
// provider turn. A correction never expands a configured role budget.
func remainingLimit(limit Limit, used int) (Limit, bool) {
	if limit == Unlimited {
		return Unlimited, true
	}
	remaining := int(limit) - used
	if remaining <= 0 {
		return 0, false
	}
	return Limit(remaining), true
}

func roleSchemaInstruction(role Role) (string, error) {
	switch role {
	case RolePlanner:
		return `JSON schema: {"schema_version":1,"understanding":"...","implementation_plan":["..."],"task_breakdown":["..."],"acceptance_criteria":["..."],"risks":["..."],"assumptions":["..."],"handoff":{"objective":"...","reason":"...","evidence":[{"kind":"file","uri":"path"}],"unresolved_issues":[{"id":"issue-1","summary":"...","blocking":false}],"notes":"..."}}. Use [] when there is no evidence or unresolved issue.`, nil
	case RoleDeveloper:
		return `JSON schema: {"schema_version":1,"summary":"...","commands_executed":["..."],"known_limitations":["..."],"handoff":{"objective":"...","reason":"...","evidence":[{"kind":"file","uri":"path"}],"unresolved_issues":[{"id":"issue-1","summary":"...","blocking":false}],"notes":"..."}}. After a write proposal, omit changed_artifacts: Prizm derives that field from the persisted proposal. Use [] when there is no evidence or unresolved issue.`, nil
	case RoleTester:
		return `JSON schema: {"schema_version":1,"result":"passed|failed","tests_executed":[{"name":"...","status":"passed|failed|timeout|error","evidence":[{"kind":"validation","uri":"path"}]}],"failure_evidence":[],"reproduction":["..."],"handoff":{"objective":"...","reason":"...","evidence":[{"kind":"validation","uri":"path"}],"unresolved_issues":[{"id":"issue-1","summary":"...","blocking":false}],"notes":"..."}}. You must execute at least one allowed validation and include it in the non-empty tests_executed array. If validation cannot run, report it as an error or timeout; never omit tests_executed. Use [] when there is no evidence or unresolved issue.`, nil
	case RoleReviewer:
		return `JSON schema: {"schema_version":1,"decision":"approved|changes_requested","findings":[{"severity":"info|low|medium|high|critical","summary":"...","evidence":[]}],"required_corrections":["..."],"evidence":[],"handoff":{"objective":"...","reason":"...","evidence":[],"unresolved_issues":[],"notes":"..."}}. Omit handoff when approved.`, nil
	case RoleReflector:
		return `JSON schema: {"schema_version":1,"verdict":"success|partial|failure|uncertain","confidence":0.0,"failure_class":"none|policy|tool|environment|model|verification|unknown","evidence":[],"lesson_candidate":{"summary":"...","content":"...","category":"decision|feedback|project|reference","topics":[]},"replan_requested":false,"replan_reason":"..."}`, nil
	default:
		return "", fmt.Errorf("multiagent: unsupported role %q", role)
	}
}

func mergeArtifacts(primary []ArtifactRef, additional []ArtifactRef) []ArtifactRef {
	merged := append([]ArtifactRef(nil), primary...)
	seen := make(map[string]struct{}, len(merged))
	for _, artifact := range merged {
		seen[string(artifact.Kind)+"\x00"+artifact.URI] = struct{}{}
	}
	for _, artifact := range additional {
		key := string(artifact.Kind) + "\x00" + artifact.URI
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, artifact)
	}
	return merged
}
