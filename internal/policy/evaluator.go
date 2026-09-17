package policy

import (
	"fmt"

	"github.com/emaharmony/prizm/internal/event"
)

// Evaluator evaluates policy requests against registered rules.
//
// Rules are evaluated in registration order (first match wins).
// If no rule matches, the default decision is applied.
type Evaluator struct {
	registry        *Registry
	defaultDecision Decision
	defaultReason   string
	emitter         EventEmitter
}

// EventEmitter is the interface for emitting policy events.
type EventEmitter interface {
	Emit(eventType, source string, payload map[string]any)
}

// NewEvaluator creates a policy evaluator with the given registry.
// Default decision when no rule matches: denied.
func NewEvaluator(registry *Registry) *Evaluator {
	return &Evaluator{
		registry:        registry,
		defaultDecision: DecisionDenied,
		defaultReason:   "No matching policy rule; default deny.",
	}
}

// SetDefault sets the default decision when no rule matches.
func (e *Evaluator) SetDefault(decision Decision, reason string) {
	e.defaultDecision = decision
	e.defaultReason = reason
}

// SetEmitter sets the event emitter for policy events.
func (e *Evaluator) SetEmitter(emitter EventEmitter) {
	e.emitter = emitter
}

// Evaluate evaluates a policy request against all registered rules.
// Returns a PolicyDecision with the result.
//
// Evaluation order: first matching rule wins.
// If no rule matches, the default decision is applied.
func (e *Evaluator) Evaluate(req PolicyRequest) PolicyDecision {
	e.emit("prizm.policy.requested", map[string]any{
		"action":   req.Action,
		"resource": req.Resource,
		"subject":  req.Subject,
		"context":  req.Context,
	})

	decision := e.evaluateRules(req)

	e.emit("prizm.policy.evaluated", map[string]any{
		"action":        req.Action,
		"decision":      string(decision.Decision),
		"rule_id":       decision.RuleID,
		"evaluation_id": decision.EvaluationID,
	})

	// Emit specific outcome event
	switch decision.Decision {
	case DecisionAllowed:
		e.emit("prizm.policy.allowed", map[string]any{
			"action":  req.Action,
			"rule_id": decision.RuleID,
			"reason":  decision.Reason,
		})
	case DecisionDenied:
		e.emit("prizm.policy.denied", map[string]any{
			"action":  req.Action,
			"rule_id": decision.RuleID,
			"reason":  decision.Reason,
		})
	case DecisionRequiresApproval:
		e.emit("prizm.policy.approval_required", map[string]any{
			"action":  req.Action,
			"rule_id": decision.RuleID,
			"reason":  decision.Reason,
		})
	}

	return decision
}

func (e *Evaluator) evaluateRules(req PolicyRequest) PolicyDecision {
	evalID := event.NewRunID()

	rules := e.registry.Rules()
	for i, rule := range rules {
		if rule.Match.Matches(req) {
			decision := PolicyDecision{
				Decision:         rule.Decision,
				Reason:           rule.Reason,
				RuleID:           rule.ID,
				Severity:         rule.Severity,
				RequiresApproval: rule.Decision == DecisionRequiresApproval,
				Metadata:         map[string]any{},
				EvaluationID:     evalID,
				MatchedRuleIndex: i,
			}
			if decision.Severity == "" {
				decision.Severity = SeverityInfo
			}
			return decision
		}
	}

	// No rule matched — apply default
	severity := SeverityWarning
	if e.defaultDecision == DecisionDenied {
		severity = SeverityWarning
	}

	return PolicyDecision{
		Decision:         e.defaultDecision,
		Reason:           e.defaultReason,
		RuleID:           "default",
		Severity:         severity,
		RequiresApproval: e.defaultDecision == DecisionRequiresApproval,
		Metadata:         map[string]any{},
		EvaluationID:     evalID,
	}
}

// Explain returns a human-readable explanation of what policy would decide for a request.
func (e *Evaluator) Explain(req PolicyRequest) string {
	decision := e.evaluateRules(req)
	if decision.RuleID == "default" {
		return fmt.Sprintf("No matching rule for action=%q resource=%q. Default: %s. %s",
			req.Action, req.Resource.Name, decision.Decision, decision.Reason)
	}
	return fmt.Sprintf("Rule %q matches action=%q resource=%q → %s. %s",
		decision.RuleID, req.Action, req.Resource.Name, decision.Decision, decision.Reason)
}

func (e *Evaluator) emit(eventType string, payload map[string]any) {
	if e.emitter != nil {
		e.emitter.Emit(eventType, "prizm-policy", payload)
	}
}
