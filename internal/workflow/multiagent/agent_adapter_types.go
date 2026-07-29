package multiagent

import (
	"context"
	"fmt"
	"time"

	"github.com/emaharmony/prism/internal/agent"
	"github.com/emaharmony/prism/internal/cost"
	"github.com/emaharmony/prism/internal/validation"
)

// AgentProfile is the execution identity resolved from an existing Prism agent
// registry or configuration source.
type AgentProfile struct {
	ID           string
	Provider     string
	Model        string
	Capabilities []string
}

// AgentProfileResolver resolves RoleConfig.AgentRef without giving the adapter
// access to the orchestration configuration.
type AgentProfileResolver interface {
	ResolveAgent(string) (AgentProfile, error)
}

// AgentRegistry is the narrow part of agent.Registry consumed by the adapter.
type AgentRegistry interface {
	Resolve(string) (*agent.Agent, error)
}

// RegistryProfileResolver adapts Prism's existing agent registry.
type RegistryProfileResolver struct {
	Registry AgentRegistry
}

// ResolveAgent resolves a registered Prism agent into execution metadata.
func (r RegistryProfileResolver) ResolveAgent(ref string) (AgentProfile, error) {
	if r.Registry == nil {
		return AgentProfile{}, fmt.Errorf("multiagent: agent registry is required")
	}
	resolved, err := r.Registry.Resolve(ref)
	if err != nil {
		return AgentProfile{}, err
	}
	capabilities := make([]string, 0, len(resolved.Capabilities))
	for _, capability := range resolved.Capabilities {
		capabilities = append(capabilities, capability.Action)
	}
	return AgentProfile{
		ID:           resolved.Name,
		Provider:     resolved.ProviderName,
		Model:        resolved.Model,
		Capabilities: capabilities,
	}, nil
}

// Workspace identifies the run-scoped directory shared by all roles.
// Lifecycle ownership remains with the composition layer that acquires it.
type Workspace struct {
	ID   string
	Path string
}

// WorkspaceResolver returns the already-acquired workspace for a run.
type WorkspaceResolver interface {
	ResolveWorkspace(context.Context, string) (Workspace, error)
}

// WorkspaceResolverFunc adapts a function to WorkspaceResolver.
type WorkspaceResolverFunc func(context.Context, string) (Workspace, error)

// ResolveWorkspace implements WorkspaceResolver.
func (f WorkspaceResolverFunc) ResolveWorkspace(ctx context.Context, runID string) (Workspace, error) {
	return f(ctx, runID)
}

// ApprovalStatus is the result of consulting Prism's approval authority.
type ApprovalStatus string

const (
	ApprovalNotRequired ApprovalStatus = "not_required"
	ApprovalPending     ApprovalStatus = "pending"
	ApprovalApproved    ApprovalStatus = "approved"
	ApprovalDenied      ApprovalStatus = "denied"
)

// ApprovalCheck is the workflow-level approval question for one role visit.
type ApprovalCheck struct {
	RunID     string
	Role      Role
	AgentID   string
	Approvers []Role
	Visit     int
}

// ApprovalChecker consults the existing approval authority. It does not create
// or mutate approval records.
type ApprovalChecker interface {
	CheckApproval(context.Context, ApprovalCheck) (ApprovalStatus, error)
}

// ApprovalCheckerFunc adapts a function to ApprovalChecker.
type ApprovalCheckerFunc func(context.Context, ApprovalCheck) (ApprovalStatus, error)

// CheckApproval implements ApprovalChecker.
func (f ApprovalCheckerFunc) CheckApproval(
	ctx context.Context,
	check ApprovalCheck,
) (ApprovalStatus, error) {
	return f(ctx, check)
}

// ValidationRunner invokes an existing allowlisted validation profile.
type ValidationRunner interface {
	RunValidation(context.Context, string, string) (*validation.Result, error)
}

// ValidationRunnerFunc adapts validation.Executor.Run to ValidationRunner.
type ValidationRunnerFunc func(context.Context, string, string) (*validation.Result, error)

// RunValidation implements ValidationRunner.
func (f ValidationRunnerFunc) RunValidation(
	ctx context.Context,
	profile string,
	correlationID string,
) (*validation.Result, error) {
	return f(ctx, profile, correlationID)
}

// AgentExecutionRequest is the narrow request sent to Prism's real bounded
// agent execution seam.
type AgentExecutionRequest struct {
	RunID                string
	TaskID               string
	ExecutionKey         string
	Role                 Role
	Visit                int
	Profile              AgentProfile
	Prompt               string
	Workspace            Workspace
	AllowedTools         []string
	RequiredCapabilities []string
	MaxIterations        Limit
	MaxTokens            Limit
	Deadline             time.Time
}

// AgentExecutionResult is the transport-neutral result returned by the real
// agent execution seam.
type AgentExecutionResult struct {
	Output          string
	Artifacts       []ArtifactRef
	Usage           cost.TokenUsage
	LocalIterations int
	ToolCalls       int
	DeniedToolCalls int
}

// AgentExecutor runs one configured Prism agent. Production uses the existing
// bounded sub-agent TaskRunner; tests use controlled fakes.
type AgentExecutor interface {
	ExecuteAgent(context.Context, AgentExecutionRequest) (AgentExecutionResult, error)
}

// GovernanceError identifies a fail-closed governance decision made below the
// adapter, such as a policy denial or tool-scope rejection.
type GovernanceError struct {
	Kind   string
	Reason string
}

func (e *GovernanceError) Error() string {
	return fmt.Sprintf("multiagent: governance %s: %s", e.Kind, e.Reason)
}

// ApprovalRequiredError reports that a role cannot complete until the existing
// approval authority records a decision.
type ApprovalRequiredError struct {
	Check  ApprovalCheck
	Status ApprovalStatus
}

func (e *ApprovalRequiredError) Error() string {
	return fmt.Sprintf(
		"multiagent: approval for role %q visit %d is %s",
		e.Check.Role,
		e.Check.Visit,
		e.Status,
	)
}

// StructuredOutputError reports a malformed or semantically invalid role
// result. The adapter never guesses a successful outcome.
type StructuredOutputError struct {
	Role  Role
	Cause error
}

func (e *StructuredOutputError) Error() string {
	return fmt.Sprintf("multiagent: invalid structured output for role %q: %v", e.Role, e.Cause)
}

// Unwrap exposes the decoding or validation error.
func (e *StructuredOutputError) Unwrap() error {
	return e.Cause
}
