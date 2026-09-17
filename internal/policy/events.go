package policy

// V8 Policy Event Types
//
// Policy events are emitted during evaluation and persist to events.jsonl.

const (
	EventTypePolicyRequested        = "prizm.policy.requested"
	EventTypePolicyEvaluated        = "prizm.policy.evaluated"
	EventTypePolicyAllowed          = "prizm.policy.allowed"
	EventTypePolicyDenied           = "prizm.policy.denied"
	EventTypePolicyApprovalRequired = "prizm.policy.approval_required"
	EventTypePolicyFailed           = "prizm.policy.failed"
)
