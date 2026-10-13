// Package freewill defines policy-only contracts for agent-originated
// intentions. It has no access to policy decisions, approvals, persistence,
// transport, scheduling, or execution.
package freewill

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Origin identifies who initiated a request. FW-001 accepts agent proposals;
// a user decision belongs to the existing authenticated approval domain.
type Origin string

const OriginAgent Origin = "agent"

type CandidateSource string

const (
	SourceNew        CandidateSource = "new"
	SourceResume     CandidateSource = "resume"
	SourceReflection CandidateSource = "reflection"
)

// Scope is a requested scope, not an authorization grant.
type Scope struct {
	ProjectID string `json:"project_id"`
	Workspace string `json:"workspace"`
}

// ResourceBudget is a proposal. Nil values mean unspecified; validation does
// not choose defaults, reserve resources, or spend a budget.
type ResourceBudget struct {
	RuntimeMinutes *int `json:"runtime_minutes,omitempty"`
	DiskMB         *int `json:"disk_mb,omitempty"`
	MaxModelCalls  *int `json:"max_model_calls,omitempty"`
	MaxTokens      *int `json:"max_tokens,omitempty"`
}

// IntentionRequest is an untrusted agent proposal. Reason is a short,
// inspectable summary and must never be treated as authority or chain of thought.
type IntentionRequest struct {
	RequestID             string          `json:"request_id"`
	AgentID               string          `json:"agent_id"`
	Origin                Origin          `json:"origin"`
	CandidateSource       CandidateSource `json:"candidate_source"`
	Title                 string          `json:"title"`
	Goal                  string          `json:"goal"`
	ReasonSummary         string          `json:"reason_summary"`
	RequestedCapabilities []string        `json:"requested_capabilities"`
	Scope                 Scope           `json:"scope"`
	IdempotencyKey        string          `json:"idempotency_key"`
	CorrelationID         string          `json:"correlation_id"`
	Budget                ResourceBudget  `json:"budget"`
}

// CandidateIntention is the selection-facing wrapper around a validated
// request. It does not express approval, permission, or execution readiness.
type CandidateIntention struct {
	CandidateID string           `json:"candidate_id"`
	Request     IntentionRequest `json:"request"`
}

// Validate checks the candidate envelope and its underlying proposal. It does
// not select, approve, or schedule the candidate.
func (c CandidateIntention) Validate() error {
	if strings.TrimSpace(c.CandidateID) == "" {
		return errors.New("candidate_id is required")
	}
	if len(c.CandidateID) > 128 {
		return errors.New("candidate_id must be bounded")
	}
	return c.Request.Validate()
}

// DecodeIntentionRequest strictly decodes one JSON object. Unknown fields,
// trailing values, and malformed input are rejected rather than ignored.
func DecodeIntentionRequest(data []byte) (IntentionRequest, error) {
	var request IntentionRequest
	if err := decodeStrictJSON(data, &request); err != nil {
		return IntentionRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return IntentionRequest{}, err
	}
	return request, nil
}

// DecodeCandidateIntention strictly decodes and validates a candidate envelope.
func DecodeCandidateIntention(data []byte) (CandidateIntention, error) {
	var candidate CandidateIntention
	if err := decodeStrictJSON(data, &candidate); err != nil {
		return CandidateIntention{}, err
	}
	if err := candidate.Validate(); err != nil {
		return CandidateIntention{}, err
	}
	return candidate, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("decode Freewill contract: invalid JSON, field, or value")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("decode Freewill contract: trailing or invalid data")
	}
	return nil
}

// Validate checks structural and bounded-field requirements only. It never
// decides whether requested capabilities or scope are permitted.
func (r IntentionRequest) Validate() error {
	if strings.TrimSpace(r.RequestID) == "" || strings.TrimSpace(r.AgentID) == "" {
		return errors.New("request_id and agent_id are required")
	}
	if len(r.RequestID) > 128 || len(r.AgentID) > 128 {
		return errors.New("request_id and agent_id must be bounded")
	}
	if r.Origin != OriginAgent {
		return errors.New("origin must be agent")
	}
	if r.CandidateSource != SourceNew && r.CandidateSource != SourceResume && r.CandidateSource != SourceReflection {
		return errors.New("candidate_source is invalid")
	}
	if strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.Goal) == "" {
		return errors.New("title and goal are required")
	}
	if strings.TrimSpace(r.ReasonSummary) == "" {
		return errors.New("reason_summary is required")
	}
	if len(r.Title) > 200 || len(r.Goal) > 4000 || len(r.ReasonSummary) > 1000 {
		return errors.New("title, goal, or reason_summary exceeds its limit")
	}
	if strings.TrimSpace(r.Scope.ProjectID) == "" || strings.TrimSpace(r.Scope.Workspace) == "" {
		return errors.New("scope.project_id and scope.workspace are required")
	}
	if len(r.Scope.ProjectID) > 128 || len(r.Scope.Workspace) > 512 {
		return errors.New("scope.project_id and scope.workspace must be bounded")
	}
	if strings.TrimSpace(r.IdempotencyKey) == "" || strings.TrimSpace(r.CorrelationID) == "" {
		return errors.New("idempotency_key and correlation_id are required")
	}
	if len(r.IdempotencyKey) > 256 || len(r.CorrelationID) > 128 {
		return errors.New("idempotency_key and correlation_id must be bounded")
	}
	if len(r.RequestedCapabilities) == 0 {
		return errors.New("at least one requested capability is required")
	}
	if len(r.RequestedCapabilities) > 64 {
		return errors.New("requested_capabilities exceeds its limit")
	}
	seen := make(map[string]struct{}, len(r.RequestedCapabilities))
	for _, capability := range r.RequestedCapabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" || len(capability) > 128 {
			return errors.New("requested capabilities must be non-empty and bounded")
		}
		if _, exists := seen[capability]; exists {
			return errors.New("requested capabilities must be unique")
		}
		seen[capability] = struct{}{}
	}
	for name, value := range map[string]*int{
		"runtime_minutes": r.Budget.RuntimeMinutes,
		"disk_mb":         r.Budget.DiskMB,
		"max_model_calls": r.Budget.MaxModelCalls,
		"max_tokens":      r.Budget.MaxTokens,
	} {
		if value != nil && *value <= 0 {
			return fmt.Errorf("budget.%s must be positive when specified", name)
		}
	}
	return nil
}
