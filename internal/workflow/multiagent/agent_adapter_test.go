package multiagent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prism/internal/agent"
	"github.com/emaharmony/prism/internal/cost"
	"github.com/emaharmony/prism/internal/event"
	"github.com/emaharmony/prism/internal/validation"
)

type profileResolverFunc func(string) (AgentProfile, error)

func (f profileResolverFunc) ResolveAgent(ref string) (AgentProfile, error) {
	return f(ref)
}

type agentExecutorFunc func(context.Context, AgentExecutionRequest) (AgentExecutionResult, error)

func (f agentExecutorFunc) ExecuteAgent(
	ctx context.Context,
	request AgentExecutionRequest,
) (AgentExecutionResult, error) {
	return f(ctx, request)
}

func TestAgentRoleRunnerBuildsGovernedExecutionRequest(t *testing.T) {
	var captured AgentExecutionRequest
	runner := newAdapterForTest(t, agentExecutorFunc(func(
		_ context.Context,
		request AgentExecutionRequest,
	) (AgentExecutionResult, error) {
		captured = request
		return AgentExecutionResult{
			Output: `{
				"schema_version": 1,
				"understanding": "understood",
				"implementation_plan": ["implement"],
				"task_breakdown": ["adapter"],
				"acceptance_criteria": ["tests pass"],
				"handoff": {"objective": "implement", "reason": "plan ready"}
			}`,
			Usage:           cost.TokenUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10},
			LocalIterations: 2,
			ToolCalls:       1,
		}, nil
	}), nil, nil)

	request := adapterRoleRequest(RolePlanner)
	result, err := runner.RunRole(context.Background(), request)
	if err != nil {
		t.Fatalf("run role: %v", err)
	}
	if captured.Profile.ID != "planner-agent" || captured.Role != RolePlanner {
		t.Errorf("profile/role = %q/%q", captured.Profile.ID, captured.Role)
	}
	if captured.Workspace.ID != "workspace-run-phase1-test" ||
		captured.Workspace.Path != "/worktrees/run-phase1-test" {
		t.Errorf("workspace = %#v", captured.Workspace)
	}
	if len(captured.AllowedTools) != 1 || captured.AllowedTools[0] != "read_file" {
		t.Errorf("allowed tools = %v", captured.AllowedTools)
	}
	if captured.MaxIterations != 3 || captured.MaxTokens != 1_000 {
		t.Errorf("execution limits = iterations %d tokens %d", captured.MaxIterations, captured.MaxTokens)
	}
	if captured.Deadline.IsZero() {
		t.Error("finite role time budget did not produce a deadline")
	}
	if !strings.Contains(captured.Prompt, "INCOMING HANDOFF JSON:") ||
		!strings.Contains(captured.Prompt, "Return exactly one JSON object") {
		t.Errorf("prompt missing structured contract: %s", captured.Prompt)
	}
	if result.Outcome != OutcomePlanReady || result.TokenUsage.TotalTokens != 10 {
		t.Errorf("result = %#v", result)
	}
	if result.Metadata.ToolCalls != 1 ||
		result.Metadata.WorkspaceID != captured.Workspace.ID ||
		result.Metadata.Model != "fake-model" {
		t.Errorf("metadata = %#v", result.Metadata)
	}
}

func TestRegistryProfileResolverUsesExistingAgentRegistry(t *testing.T) {
	registry := agent.NewRegistry()
	if err := registry.Register(&agent.Agent{
		Name:         "planner-agent",
		Version:      "1.0.0",
		Role:         "planning",
		ProviderName: "mock",
		Model:        "mock-model",
		Capabilities: []agent.AgentCapability{
			{Action: "plan", Description: "Plan work"},
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	profile, err := (RegistryProfileResolver{Registry: registry}).ResolveAgent("planner-agent")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if profile.ID != "planner-agent" || profile.Provider != "mock" ||
		profile.Model != "mock-model" {
		t.Errorf("profile = %#v", profile)
	}
	if len(profile.Capabilities) != 1 || profile.Capabilities[0] != "plan" {
		t.Errorf("capabilities = %v", profile.Capabilities)
	}
}

func TestAgentRoleRunnerGovernanceFailures(t *testing.T) {
	tests := []struct {
		name     string
		executor AgentExecutor
		mutate   func(*RoleRunRequest)
		wantKind string
	}{
		{
			name: "policy rejection",
			executor: agentExecutorFunc(func(
				context.Context,
				AgentExecutionRequest,
			) (AgentExecutionResult, error) {
				return AgentExecutionResult{}, &GovernanceError{
					Kind:   "policy",
					Reason: "tool execution denied",
				}
			}),
			wantKind: "policy",
		},
		{
			name: "tool authorization failure",
			executor: agentExecutorFunc(func(
				context.Context,
				AgentExecutionRequest,
			) (AgentExecutionResult, error) {
				return AgentExecutionResult{}, &GovernanceError{
					Kind:   "tool_authorization",
					Reason: "write_file is outside the role allowlist",
				}
			}),
			wantKind: "tool_authorization",
		},
		{
			name: "capability rejection before execution",
			executor: agentExecutorFunc(func(
				context.Context,
				AgentExecutionRequest,
			) (AgentExecutionResult, error) {
				t.Fatal("executor must not run after capability rejection")
				return AgentExecutionResult{}, nil
			}),
			mutate: func(request *RoleRunRequest) {
				request.RoleConfig.Capabilities = []string{"admin"}
			},
			wantKind: "capability",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := newAdapterForTest(t, test.executor, nil, nil)
			request := adapterRoleRequest(RolePlanner)
			if test.mutate != nil {
				test.mutate(&request)
			}
			_, err := runner.RunRole(context.Background(), request)
			var governanceErr *GovernanceError
			if !errors.As(err, &governanceErr) {
				t.Fatalf("expected GovernanceError, got %T: %v", err, err)
			}
			if governanceErr.Kind != test.wantKind {
				t.Errorf("kind = %q, want %q", governanceErr.Kind, test.wantKind)
			}
		})
	}
}

func TestAgentRoleRunnerApprovalRequired(t *testing.T) {
	executed := false
	runner := newAdapterForTest(t, agentExecutorFunc(func(
		context.Context,
		AgentExecutionRequest,
	) (AgentExecutionResult, error) {
		executed = true
		return AgentExecutionResult{}, nil
	}), nil, nil)
	request := adapterRoleRequest(RoleDeveloper)
	request.RoleConfig.Approval = ApprovalRequirement{
		Required:  true,
		Approvers: []Role{RoleReviewer},
	}

	_, err := runner.RunRole(context.Background(), request)
	var approvalErr *ApprovalRequiredError
	if !errors.As(err, &approvalErr) {
		t.Fatalf("expected ApprovalRequiredError, got %T: %v", err, err)
	}
	if executed {
		t.Fatal("agent executed before required approval")
	}
}

func TestAgentRoleRunnerValidationFailureSelectsTypedTesterFailure(t *testing.T) {
	validator := ValidationRunnerFunc(func(
		context.Context,
		string,
		string,
	) (*validation.Result, error) {
		return &validation.Result{
			Profile:  "go_test_all",
			Status:   "failed",
			ExitCode: 1,
		}, nil
	})
	runner := newAdapterForTest(t, agentExecutorFunc(func(
		context.Context,
		AgentExecutionRequest,
	) (AgentExecutionResult, error) {
		return AgentExecutionResult{Output: testerPassedJSON(), LocalIterations: 1}, nil
	}), validator, nil)
	request := adapterRoleRequest(RoleTester)
	request.RoleConfig.ValidationProfiles = []string{"go_test_all"}

	result, err := runner.RunRole(context.Background(), request)
	if err != nil {
		t.Fatalf("run role: %v", err)
	}
	if result.Outcome != OutcomeTestsFailed {
		t.Errorf("outcome = %q, want %q", result.Outcome, OutcomeTestsFailed)
	}
	if result.OutgoingHandoff == nil ||
		len(result.OutgoingHandoff.ValidationResults) != 1 {
		t.Fatalf("validation results = %#v", result.OutgoingHandoff)
	}
	if result.Metadata.ValidationStatus != "failed" {
		t.Errorf("validation status = %q", result.Metadata.ValidationStatus)
	}
}

func TestAgentRoleRunnerCancellationPropagation(t *testing.T) {
	runner := newAdapterForTest(t, agentExecutorFunc(func(
		ctx context.Context,
		_ AgentExecutionRequest,
	) (AgentExecutionResult, error) {
		<-ctx.Done()
		return AgentExecutionResult{}, ctx.Err()
	}), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.RunRole(ctx, adapterRoleRequest(RolePlanner))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestAgentRoleRunnerEnforcesRoleTimeBudget(t *testing.T) {
	runner := newAdapterForTest(t, agentExecutorFunc(func(
		ctx context.Context,
		_ AgentExecutionRequest,
	) (AgentExecutionResult, error) {
		<-ctx.Done()
		return AgentExecutionResult{}, ctx.Err()
	}), nil, nil)
	request := adapterRoleRequest(RolePlanner)
	request.RoleConfig.TimeBudget = 10 * time.Millisecond

	started := time.Now()
	_, err := runner.RunRole(context.Background(), request)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected role deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("role deadline was not enforced promptly: %s", elapsed)
	}
}

func TestSupervisorWithRealAgentAdapterBoundary(t *testing.T) {
	var mu sync.Mutex
	roleVisits := map[Role]int{}
	executor := agentExecutorFunc(func(
		_ context.Context,
		request AgentExecutionRequest,
	) (AgentExecutionResult, error) {
		mu.Lock()
		roleVisits[request.Role]++
		mu.Unlock()
		outputs := map[Role]string{
			RolePlanner:   `{"schema_version":1,"understanding":"task","implementation_plan":["implement"],"task_breakdown":["adapter"],"acceptance_criteria":["green"],"handoff":{"objective":"implement","reason":"ready"}}`,
			RoleDeveloper: `{"schema_version":1,"summary":"done","changed_artifacts":[{"kind":"file","uri":"internal/adapter.go"}],"handoff":{"objective":"test","reason":"implemented"}}`,
			RoleTester:    testerPassedJSON(),
			RoleReviewer:  `{"schema_version":1,"decision":"approved","findings":[],"required_corrections":[],"evidence":[]}`,
		}
		return AgentExecutionResult{
			Output:          outputs[request.Role],
			Usage:           cost.TokenUsage{PromptTokens: 4, CompletionTokens: 1, TotalTokens: 5},
			LocalIterations: 1,
			ToolCalls:       1,
		}, nil
	})
	validator := ValidationRunnerFunc(func(
		context.Context,
		string,
		string,
	) (*validation.Result, error) {
		return &validation.Result{Profile: "go_test_all", Status: "passed"}, nil
	})
	approval := ApprovalCheckerFunc(func(
		context.Context,
		ApprovalCheck,
	) (ApprovalStatus, error) {
		return ApprovalApproved, nil
	})
	adapter := newAdapterForTest(t, executor, validator, approval)
	definition := validDefinition()
	sink := &captureEventSink{}
	supervisor := newTestSupervisor(t, definition, adapter, sink)

	state, err := supervisor.Run(context.Background(), testRunRequest())
	if err != nil {
		t.Fatalf("supervisor run: %v", err)
	}
	if state.Status != RunStatusCompleted || state.TransitionCount != 4 {
		t.Errorf("state = status %q transitions %d", state.Status, state.TransitionCount)
	}
	if state.BudgetUsage.Tokens.TotalTokens != 20 {
		t.Errorf("tokens = %d, want 20", state.BudgetUsage.Tokens.TotalTokens)
	}
	for _, role := range Phase1Roles() {
		if roleVisits[role] != 1 {
			t.Errorf("%s visits = %d, want 1", role, roleVisits[role])
		}
	}

	foundTelemetry := false
	for _, evt := range sink.snapshot() {
		if evt.Type != event.EventMultiAgentRoleCompleted {
			continue
		}
		if evt.Payload["agent_ref"] == "astraea" &&
			evt.Payload["model"] == "fake-model" &&
			evt.Payload["tool_calls"] == 1 {
			foundTelemetry = true
		}
	}
	if !foundTelemetry {
		t.Fatal("role completion telemetry not emitted")
	}
}

func newAdapterForTest(
	t *testing.T,
	executor AgentExecutor,
	validator ValidationRunner,
	approval ApprovalChecker,
) *AgentRoleRunner {
	t.Helper()
	profiles := profileResolverFunc(func(ref string) (AgentProfile, error) {
		role := strings.TrimSuffix(ref, "-agent")
		return AgentProfile{
			ID:           ref,
			Provider:     "mock",
			Model:        "fake-model",
			Capabilities: []string{role, "plan", "code", "test", "review"},
		}, nil
	})
	workspaces := WorkspaceResolverFunc(func(
		_ context.Context,
		runID string,
	) (Workspace, error) {
		return Workspace{
			ID:   "workspace-" + runID,
			Path: "/worktrees/" + runID,
		}, nil
	})
	runner, err := NewAgentRoleRunner(AgentRoleRunnerOptions{
		Profiles:   profiles,
		Executor:   executor,
		Workspaces: workspaces,
		Approvals:  approval,
		Validation: validator,
		Clock: func() time.Time {
			return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	return runner
}

func adapterRoleRequest(role Role) RoleRunRequest {
	config := RoleConfig{
		Role:               role,
		AgentRef:           string(role) + "-agent",
		AllowedTools:       []string{"read_file"},
		Capabilities:       []string{string(role)},
		MaxLocalIterations: 3,
		TokenBudget:        1_000,
		TimeBudget:         time.Minute,
	}
	return RoleRunRequest{
		Run: RunView{
			RunID:       "run-phase1-test",
			WorkflowID:  "phase1-reference",
			Task:        TaskReference{ID: "task-phase1-test", Description: "test adapter"},
			CurrentRole: role,
			Visit:       1,
		},
		RoleConfig: config,
	}
}

func testerPassedJSON() string {
	return `{
		"schema_version": 1,
		"result": "passed",
		"tests_executed": [{"name": "go test", "status": "passed"}],
		"handoff": {"objective": "review", "reason": "tests passed"}
	}`
}
