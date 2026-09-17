package tool

import (
	"context"
	"os"
	"testing"

	"github.com/emaharmony/prizm/internal/policy"
)

// ─── V8 Policy Integration Tests ───────────────────────────────────────────────

func TestPolicyIntegrationAllowsReadFile(t *testing.T) {
	// V8 policy allows read_file
	policyReg := policy.NewRegistry()
	policyReg.Register(policy.PolicyRule{
		ID:       "allow_read_file",
		Match:    policy.MatchSpec{Action: "tool.execute", ResourceName: "read_file"},
		Decision: policy.DecisionAllowed,
		Reason:   "read_file is allowlisted; local validator must still enforce workspace path.",
	})
	policyEval := policy.NewEvaluator(policyReg)

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	repoRoot = repoRoot + "/../.."

	// Tool executor with V8 policy
	toolReg := NewRegistry()
	RegisterBuiltinsV4(toolReg, repoRoot, 1024*1024, "")
	toolExec := NewExecutor(toolReg, &PolicyConfig{WorkspaceRoot: repoRoot, MaxFileSize: 1024 * 1024})
	toolExec.SetPolicyEvaluator(func(action string, resource policy.Resource, context policy.Context) policy.PolicyDecision {
		return policyEval.Evaluate(policy.PolicyRequest{
			Action:   action,
			Resource: resource,
			Context:  context,
		})
	})

	result, err := toolExec.ExecuteWithPolicy(context.Background(), "read_file", "test-agent", "prizm", "test-run", map[string]any{
		"path": "go.mod",
	})
	if err != nil {
		t.Fatalf("ExecuteWithPolicy error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected read_file to succeed, got error: %s", result.Error)
	}
}

func TestPolicyIntegrationDeniesRunCommand(t *testing.T) {
	// V8 policy denies run_command
	policyReg := policy.NewRegistry()
	policyReg.Register(policy.PolicyRule{
		ID:       "deny_shell_execution",
		Match:    policy.MatchSpec{Action: "tool.execute", ResourceName: "run_command"},
		Decision: policy.DecisionDenied,
		Reason:   "Shell execution is not supported by policy.",
		Severity: policy.SeverityCritical,
	})
	policyEval := policy.NewEvaluator(policyReg)

	toolReg := NewRegistry()
	RegisterBuiltinsV4(toolReg, ".", 1024*1024, "")
	toolExec := NewExecutor(toolReg, &PolicyConfig{WorkspaceRoot: "."})
	toolExec.SetPolicyEvaluator(func(action string, resource policy.Resource, context policy.Context) policy.PolicyDecision {
		return policyEval.Evaluate(policy.PolicyRequest{
			Action:   action,
			Resource: resource,
			Context:  context,
		})
	})

	result, err := toolExec.ExecuteWithPolicy(context.Background(), "run_command", "test-agent", "prizm", "test-run", map[string]any{
		"command": "ls",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected run_command to be denied by V8 policy")
	}
}

func TestPolicyIntegrationAllowsButLocalValidatorBlocks(t *testing.T) {
	// V8 policy allows read_file, but local path validator should still block traversal
	policyReg := policy.NewRegistry()
	policyReg.Register(policy.PolicyRule{
		ID:       "allow_read_file",
		Match:    policy.MatchSpec{Action: "tool.execute", ResourceName: "read_file"},
		Decision: policy.DecisionAllowed,
		Reason:   "read_file is allowlisted; local validator must still enforce workspace path.",
	})
	policyEval := policy.NewEvaluator(policyReg)

	toolReg := NewRegistry()
	RegisterBuiltinsV4(toolReg, ".", 1024*1024, "")
	toolExec := NewExecutor(toolReg, &PolicyConfig{WorkspaceRoot: "."})
	toolExec.SetPolicyEvaluator(func(action string, resource policy.Resource, context policy.Context) policy.PolicyDecision {
		return policyEval.Evaluate(policy.PolicyRequest{
			Action:   action,
			Resource: resource,
			Context:  context,
		})
	})

	// Policy allows read_file, but path traversal should still be blocked
	result, err := toolExec.ExecuteWithPolicy(context.Background(), "read_file", "test-agent", "prizm", "test-run", map[string]any{
		"path": "../../etc/passwd",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected path traversal to be blocked by local validator even when V8 policy allows read_file")
	}
}

func TestPolicyIntegrationNoEvaluatorBackwardCompatible(t *testing.T) {
	// Without V8 policy evaluator, local tool policy should work as before
	toolReg := NewRegistry()
	RegisterBuiltinsV4(toolReg, ".", 1024*1024, "")
	toolExec := NewExecutor(toolReg, &PolicyConfig{WorkspaceRoot: ".", MaxFileSize: 1024 * 1024})

	result, err := toolExec.ExecuteWithPolicy(context.Background(), "echo", "test-agent", "prizm", "test-run", map[string]any{
		"text": "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected echo to succeed without V8 policy, got: %s", result.Error)
	}
}

func TestPolicyIntegrationDenyOverridesLocalAllow(t *testing.T) {
	// V8 policy denies echo (which local policy allows)
	policyReg := policy.NewRegistry()
	policyReg.Register(policy.PolicyRule{
		ID:       "deny_echo",
		Match:    policy.MatchSpec{Action: "tool.execute", ResourceName: "echo"},
		Decision: policy.DecisionDenied,
		Reason:   "Echo is temporarily disabled by policy.",
	})
	policyEval := policy.NewEvaluator(policyReg)

	toolReg := NewRegistry()
	RegisterBuiltinsV4(toolReg, ".", 1024*1024, "")
	toolExec := NewExecutor(toolReg, &PolicyConfig{WorkspaceRoot: "."})
	toolExec.SetPolicyEvaluator(func(action string, resource policy.Resource, context policy.Context) policy.PolicyDecision {
		return policyEval.Evaluate(policy.PolicyRequest{
			Action:   action,
			Resource: resource,
			Context:  context,
		})
	})

	result, err := toolExec.ExecuteWithPolicy(context.Background(), "echo", "test-agent", "prizm", "test-run", map[string]any{
		"text": "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected V8 policy deny to block echo even though local policy allows it")
	}
}
