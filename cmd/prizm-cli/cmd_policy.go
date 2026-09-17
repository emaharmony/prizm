// cmd_policy.go implements the `prizm policy` subcommands (V8).
//
// The policy engine is Prizm's security brain. Before any action runs,
// the policy engine evaluates it against a set of declarative rules loaded
// from YAML files. Rules can allow, deny, or require approval for any action.
//
// Policy decisions are deterministic — no LLM involved. This means Prizm
// is safe even if the model is compromised. A clever prompt can't trick
// the policy engine into allowing a denied action.
//
// Commands:
//
//	prizm policy list                   — Show all loaded policy rules
//	prizm policy evaluate --input <file> — Evaluate a specific policy request
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emaharmony/prizm/internal/policy"
)

// newPolicyEvaluator loads policy rules from YAML files and creates an
// evaluator. Rules are loaded from the --policy-dir (default: policies/).
// If loading fails partially, we continue with whatever rules we have.
func newPolicyEvaluator(policyDir string) *policy.Evaluator {
	reg := policy.NewRegistry()
	reg.LoadFromDir(policyDir) //nolint:errcheck // best-effort loading
	eval := policy.NewEvaluator(reg)
	return eval
}

// executePolicyList shows all policy rules loaded from YAML files.
// Each rule shows its decision (allowed/denied/requires_approval), ID,
// and description. Icons make it easy to scan: ✅ allowed, ❌ denied, ⚠️ requires approval.
func executePolicyList() {
	reg := policy.NewRegistry()
	count, err := reg.LoadFromDir("policies")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	rules := reg.Rules()

	fmt.Println("📋 Prizm V8 Policy Rules")
	fmt.Println("═══════════════════════════════════════════")
	if len(rules) == 0 {
		fmt.Println("  (no policies loaded)")
	}
	for _, rule := range rules {
		decisionIcon := "✅"
		switch rule.Decision {
		case policy.DecisionDenied:
			decisionIcon = "❌"
		case policy.DecisionRequiresApproval:
			decisionIcon = "⚠️"
		}
		fmt.Printf("  %s %-35s %s\n", decisionIcon, rule.ID, rule.Decision)
		fmt.Printf("     %s\n", rule.Description)
	}
	fmt.Println("═══════════════════════════════════════════")
	if count > 0 {
		fmt.Printf("  %d rules loaded\n", count)
	}
}

// executePolicyEvaluate takes a JSON policy request and evaluates it
// against the loaded policy rules. The input file must contain a valid
// PolicyRequest with action, resource, subject, and optional context.
//
// Output shows the decision, the rule that matched, and the reason.
// If the decision is "denied", it writes a policy artifact and exits with
// code 1 so CI pipelines can fail on policy violations.
func executePolicyEvaluate(inputFile, policyDir string) {
	data, err := os.ReadFile(inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to read input file: %v\n", err)
		os.Exit(1)
	}

	var req policy.PolicyRequest
	if err := json.Unmarshal(data, &req); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid JSON input: %v\n", err)
		os.Exit(1)
	}

	evaluator := newPolicyEvaluator(policyDir)

	fmt.Printf("🔍 Policy Evaluation\n")
	fmt.Printf("  Action:   %s\n", req.Action)
	fmt.Printf("  Resource: %s/%s\n", req.Resource.Type, req.Resource.Name)
	fmt.Printf("  Subject:  %s/%s\n", req.Subject.Type, req.Subject.ID)
	if req.Context.Mode != "" {
		fmt.Printf("  Mode:     %s\n", req.Context.Mode)
	}
	fmt.Println()

	decision := evaluator.Evaluate(req)

	switch decision.Decision {
	case policy.DecisionAllowed:
		fmt.Println("  ✅ ALLOWED")
	case policy.DecisionDenied:
		fmt.Println("  ❌ DENIED")
	case policy.DecisionRequiresApproval:
		fmt.Println("  ⚠️  REQUIRES APPROVAL")
	default:
		fmt.Printf("  ❓ %s\n", decision.Decision)
	}
	fmt.Println()
	fmt.Printf("  Rule:    %s\n", decision.RuleID)
	fmt.Printf("  Reason:  %s\n", decision.Reason)
	if decision.Severity != "" {
		fmt.Printf("  Severity: %s\n", decision.Severity)
	}
	fmt.Println()

	// Write a policy artifact so the decision is auditable
	artifactDir := filepath.Join("runs", "policy")
	if err := policy.WritePolicyArtifact(artifactDir, decision); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write policy artifact: %v\n", err)
	} else {
		fmt.Printf("  Artifact: %s/%s.json\n", artifactDir, decision.EvaluationID)
	}

	// Exit with code 1 if denied — CI pipelines can fail on policy violations
	if decision.Decision == policy.DecisionDenied {
		os.Exit(1)
	}
}
