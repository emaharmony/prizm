// Package policy provides a central policy engine for Prizm.
//
// V8 introduces the Core Policy Engine — a reusable, declarative policy layer
// that answers: Is this action allowed? Denied? Does it require approval? Why?
//
// Policy does NOT replace local safety validators. Policy decides permission;
// local validators still enforce input safety (path traversal, malformed input, etc).
package policy

// Decision represents a policy decision outcome.
type Decision string

const (
	DecisionAllowed          Decision = "allowed"
	DecisionDenied           Decision = "denied"
	DecisionRequiresApproval Decision = "requires_approval"
)

// Severity represents the severity level of a policy decision.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// PolicyDecision is the result of evaluating a policy request.
type PolicyDecision struct {
	Decision         Decision       `json:"decision"`
	Reason           string         `json:"reason"`
	RuleID           string         `json:"rule_id"`
	Severity         Severity       `json:"severity"`
	RequiresApproval bool           `json:"requires_approval"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	EvaluationID     string         `json:"evaluation_id"`
	MatchedRuleIndex int            `json:"matched_rule_index,omitempty"`
}
