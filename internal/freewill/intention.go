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
	CandidateID    string           `json:"candidate_id"`
	Request        IntentionRequest `json:"request"`
	Score          int              `json:"score,omitempty"`
	ScoreFactors   []string         `json:"score_factors,omitempty"`
	ScoreRationale string           `json:"score_rationale,omitempty"`
}

var allowedScoreFactors = map[string]struct{}{
	"scope_fit": {}, "feasibility": {}, "impact": {}, "novelty": {}, "readiness": {},
}

// Selection is untrusted selector output. A non-empty ChoiceID is a proposal
// for approval, never an authorization to execute. Resume candidates must be
// supplied in the server-verified allowlist.
type Selection struct {
	Candidates    []CandidateIntention `json:"candidates"`
	ChoiceID      string               `json:"choice_id,omitempty"`
	ReasonSummary string               `json:"reason_summary,omitempty"`
	SleepReason   string               `json:"sleep_reason,omitempty"`
}

func (s Selection) Validate(agentID string, scope Scope, verifiedResumeIDs map[string]struct{}) error {
	if len(s.Candidates) == 0 && s.ChoiceID == "" && strings.TrimSpace(s.SleepReason) != "" && strings.TrimSpace(s.ReasonSummary) == "" {
		if len(s.SleepReason) > 1000 {
			return errors.New("sleep reason exceeds its limit")
		}
		return nil
	}
	if len(s.Candidates) < 3 || len(s.Candidates) > 4 {
		return errors.New("selection requires three fresh candidates and at most one verified resume")
	}
	if s.ChoiceID == "" {
		if strings.TrimSpace(s.SleepReason) == "" || len(s.SleepReason) > 1000 || strings.TrimSpace(s.ReasonSummary) != "" {
			return errors.New("sleep selection requires a bounded reason and no choice summary")
		}
	} else if strings.TrimSpace(s.ReasonSummary) == "" || len(s.ReasonSummary) > 1000 || strings.TrimSpace(s.SleepReason) != "" {
		return errors.New("chosen selection requires a bounded reason and no sleep reason")
	}
	seenIDs := make(map[string]struct{}, len(s.Candidates))
	seenRequests := make(map[string]struct{}, len(s.Candidates))
	fresh, resumes := 0, 0
	choiceFound := false
	for _, candidate := range s.Candidates {
		if err := candidate.Validate(); err != nil {
			return errors.New("selection contains an invalid candidate")
		}
		if _, exists := seenIDs[candidate.CandidateID]; exists {
			return errors.New("selection candidate IDs must be unique")
		}
		seenIDs[candidate.CandidateID] = struct{}{}
		if _, exists := seenRequests[candidate.Request.RequestID]; exists {
			return errors.New("selection request IDs must be unique")
		}
		seenRequests[candidate.Request.RequestID] = struct{}{}
		if candidate.CandidateID == s.ChoiceID {
			choiceFound = true
		}
		if candidate.Request.AgentID != agentID || candidate.Request.Scope != scope {
			return errors.New("selection candidate is outside the trusted agent or scope")
		}
		switch candidate.Request.CandidateSource {
		case SourceNew:
			fresh++
		case SourceResume:
			resumes++
			if _, ok := verifiedResumeIDs[candidate.CandidateID]; !ok {
				return errors.New("resume candidate is not server-verified")
			}
		default:
			return errors.New("selection candidate source is not allowed")
		}
	}
	if fresh != 3 || resumes > 1 {
		return errors.New("selection must contain exactly three fresh candidates and at most one verified resume")
	}
	if s.ChoiceID != "" && !choiceFound {
		return errors.New("choice must reference a candidate in this selection")
	}
	return nil
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
	if c.Score < 0 || c.Score > 100 || len(c.ScoreFactors) > len(allowedScoreFactors) || len(c.ScoreRationale) > 500 {
		return errors.New("candidate score evidence is invalid or exceeds its limit")
	}
	seenFactors := make(map[string]struct{}, len(c.ScoreFactors))
	for _, factor := range c.ScoreFactors {
		if _, allowed := allowedScoreFactors[factor]; !allowed {
			return errors.New("candidate score factor is not allowed")
		}
		if _, duplicate := seenFactors[factor]; duplicate {
			return errors.New("candidate score factors must be unique")
		}
		seenFactors[factor] = struct{}{}
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

// DecodeSelection strictly decodes the provider's proposal-only selection.
// Semantic checks that require trusted host context remain in Selection.Validate.
func DecodeSelection(data []byte) (Selection, error) {
	var selection Selection
	if err := decodeStrictJSON(data, &selection); err != nil {
		return Selection{}, err
	}
	return selection, nil
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
