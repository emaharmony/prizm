package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prism/internal/validation"
)

// AgentRoleRunner adapts Prism's bounded agent execution seam to RoleRunner.
type AgentRoleRunner struct {
	profiles   AgentProfileResolver
	executor   AgentExecutor
	workspaces WorkspaceResolver
	approvals  ApprovalChecker
	validation ValidationRunner
	now        func() time.Time
}

// AgentRoleRunnerOptions supplies the existing governance and workspace
// authorities used by the adapter.
type AgentRoleRunnerOptions struct {
	Profiles   AgentProfileResolver
	Executor   AgentExecutor
	Workspaces WorkspaceResolver
	Approvals  ApprovalChecker
	Validation ValidationRunner
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
	execution, err := r.executor.ExecuteAgent(executionContext, AgentExecutionRequest{
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
	})
	finishedAt := r.now().UTC()
	if err != nil {
		return RoleRunResult{}, err
	}

	decoded, err := decodeRoleOutput(request.Run.CurrentRole, execution.Output)
	if err != nil {
		return RoleRunResult{}, err
	}
	validationResults, validationStatus, err := r.runValidations(ctx, request)
	if err != nil {
		return RoleRunResult{}, err
	}
	if decoded.Handoff != nil {
		decoded.Handoff.Artifacts = mergeArtifacts(decoded.Handoff.Artifacts, execution.Artifacts)
		decoded.Handoff.ValidationResults = append(
			[]validation.Result(nil),
			validationResults...,
		)
	}
	if validationStatus == "failed" && request.Run.CurrentRole == RoleTester {
		decoded.Outcome = OutcomeTestsFailed
		if decoded.Handoff == nil {
			return RoleRunResult{}, &StructuredOutputError{
				Role:  RoleTester,
				Cause: errors.New("validation failure requires a tester handoff"),
			}
		}
		decoded.Handoff.Reason = "allowlisted validation failed"
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
	}, nil
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
	schema, err := roleSchemaInstruction(request.Run.CurrentRole)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"ROLE: %s\nTASK ID: %s\nTASK: %s\nVISIT: %d\nTRANSITIONS USED: %d\n"+
			"INCOMING HANDOFF JSON: %s\n\n%s\n"+
			"Return exactly one JSON object. Do not wrap it in Markdown and do not add prose.",
		request.Run.CurrentRole,
		request.Run.Task.ID,
		request.Run.Task.Description,
		request.Run.Visit,
		request.Run.TransitionCount,
		handoffJSON,
		schema,
	), nil
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
		result, err := r.validation.RunValidation(ctx, profile, request.Run.RunID)
		if result != nil {
			results = append(results, *result)
			if result.Status != "passed" {
				status = "failed"
			}
		}
		if err != nil && request.Run.CurrentRole != RoleTester {
			return results, "failed", fmt.Errorf("validation profile %q: %w", profile, err)
		}
		if err != nil {
			status = "failed"
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

func roleSchemaInstruction(role Role) (string, error) {
	switch role {
	case RolePlanner:
		return `JSON schema: {"schema_version":1,"understanding":"...","implementation_plan":["..."],"task_breakdown":["..."],"acceptance_criteria":["..."],"risks":["..."],"assumptions":["..."],"handoff":{"objective":"...","reason":"...","evidence":[],"unresolved_issues":[],"notes":"..."}}`, nil
	case RoleDeveloper:
		return `JSON schema: {"schema_version":1,"summary":"...","changed_artifacts":[{"kind":"file","uri":"..."}],"commands_executed":["..."],"known_limitations":["..."],"handoff":{"objective":"...","reason":"...","evidence":[],"unresolved_issues":[],"notes":"..."}}`, nil
	case RoleTester:
		return `JSON schema: {"schema_version":1,"result":"passed|failed","tests_executed":[{"name":"...","status":"passed|failed|timeout|error","evidence":[]}],"failure_evidence":[],"reproduction":["..."],"handoff":{"objective":"...","reason":"...","evidence":[],"unresolved_issues":[],"notes":"..."}}`, nil
	case RoleReviewer:
		return `JSON schema: {"schema_version":1,"decision":"approved|changes_requested","findings":[{"severity":"info|low|medium|high|critical","summary":"...","evidence":[]}],"required_corrections":["..."],"evidence":[],"handoff":{"objective":"...","reason":"...","evidence":[],"unresolved_issues":[],"notes":"..."}}. Omit handoff when approved.`, nil
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
