package multiagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"

	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/provider"
)

// freewillSelectionProposal is the small, untrusted model-facing DTO. Identity,
// origin, source, scope, and idempotency fields are always bound by the host.
type freewillSelectionProposal struct {
	Candidates        []freewillCandidateProposal `json:"candidates"`
	ResumeCandidateID string                      `json:"resume_candidate_id"`
	ReasonSummary     string                      `json:"reason_summary"`
	SleepReason       string                      `json:"sleep_reason"`
}

type freewillCandidateProposal struct {
	Title                 string                   `json:"title"`
	Goal                  string                   `json:"goal"`
	ReasonSummary         string                   `json:"reason_summary"`
	RequestedCapabilities []string                 `json:"requested_capabilities"`
	Budget                *freewill.ResourceBudget `json:"budget"`
	Score                 *int                     `json:"score"`
	ScoreFactors          []string                 `json:"score_factors"`
	ScoreRationale        string                   `json:"score_rationale"`
}

const freewillSelectionOutputSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["candidates", "resume_candidate_id", "reason_summary", "sleep_reason"],
  "properties": {
    "candidates": {
      "type": "array",
      "minItems": 3,
      "maxItems": 3,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["title", "goal", "reason_summary", "requested_capabilities", "budget", "score", "score_factors", "score_rationale"],
        "properties": {
          "title": {"type": "string", "minLength": 1},
          "goal": {"type": "string", "minLength": 1},
          "reason_summary": {"type": "string", "minLength": 1},
          "requested_capabilities": {"type": "array", "minItems": 1, "items": {"type": "string", "minLength": 1}},
          "budget": {
            "type": ["object", "null"],
            "additionalProperties": false,
            "required": ["runtime_minutes", "disk_mb", "max_model_calls", "max_tokens"],
            "properties": {
              "runtime_minutes": {"type": ["integer", "null"], "minimum": 1},
              "disk_mb": {"type": ["integer", "null"], "minimum": 1},
              "max_model_calls": {"type": ["integer", "null"], "minimum": 1},
              "max_tokens": {"type": ["integer", "null"], "minimum": 1}
            }
          },
          "score": {"type": "integer", "minimum": 0, "maximum": 100},
          "score_factors": {
            "type": "array",
            "minItems": 1,
            "items": {"type": "string", "enum": ["scope_fit", "feasibility", "impact", "novelty", "readiness"]}
          },
          "score_rationale": {"type": "string", "minLength": 1}
        }
      }
    },
    "resume_candidate_id": {"type": "string"},
    "reason_summary": {"type": "string"},
    "sleep_reason": {"type": "string"}
  }
}`

// FreewillSelectionOutputSchema returns the strict untrusted proposal schema.
// Callers receive a copy so provider setup cannot mutate the domain contract.
func FreewillSelectionOutputSchema() []byte {
	return append([]byte(nil), freewillSelectionOutputSchema...)
}

const freewillSelectionSystemPrompt = `Return one JSON object matching the supplied native schema. Include three fresh candidates for new, resume, or sleep outcomes; use only verified resume IDs and the appropriate outcome fields. Add no prose or extra keys and request no tools. The host binds identity and scope; a choice is only a proposal, not approval or execution.`

const maxFreewillSelectionResponseBytes = 64 << 10
const freewillSelectionMaxOutputTokens = 4096

const freewillChoiceSystemPrompt = `Choose one supplied candidate ID using its short host-provided label. Reply on a final line as exactly "choice: <candidate ID>" or "choice: NONE". Do not explain, add JSON, paths, budgets, tools, or authority.`
const freewillChoiceMaxOutputTokens = 1024
const freewillIdeaSystemPrompt = `Return exactly three short task ideas for a read-only decision list. End your answer with exactly three lines, each in this form: IDEA: <short objective>. Use plain text only. Do not mention tools, commands, files, paths, network access, secrets, or execution. If three safe ideas are impossible, end with NONE.`
const freewillIdeaMaxOutputTokens = 1024

func freewillMapperDiagnostic(stage string, response provider.ChatGenerateResponse, model string, code string, ideaLines int, retry int, glmPlain bool) string {
	doneReason, _ := response.Raw["done_reason"].(string)
	thinkLen, _ := response.Raw["thinking_len"].(int)
	return fmt.Sprintf("stage=%s model=%s content_len=%d think_len=%d tool_calls=%d done_reason=%s idea_lines=%d parser_code=%s glm_plain=%t retry=%d", stage, model, len(response.Content), thinkLen, len(response.ToolCalls), doneReason, ideaLines, code, glmPlain, retry)
}

func freewillIdeaMarkerCount(content string) int { return strings.Count(strings.ToUpper(content), "IDEA:") }

func ideaRetryNumber(feedback string) int { if strings.TrimSpace(feedback) == "" { return 0 }; return 1 }

// FreewillCandidateChoiceInput is a host-owned allowlist for the small
// decision phase. Candidate IDs are already bound by the host; the provider
// cannot create or modify an intention here.
type FreewillCandidateChoiceInput struct {
	RunID         string
	CorrelationID string
	AgentID       string
	CandidateIDs  []string
	CandidateLabels []string
}

// FreewillCandidateChoice is the typed result of the ID/NONE protocol. The
// host supplies the candidate object and policy data after this result; the
// provider response never carries those fields.
type FreewillCandidateChoice struct {
	ChoiceID string
	Sleep    bool
}

func parseFreewillIdeaLines(content string) ([]string, bool, error) {
	if len(content) == 0 || len(content) > 2048 {
		return nil, false, errors.New("multiagent: Freewill idea response is empty or exceeds limit")
	}
	value := strings.TrimSpace(content)
	if idx := strings.LastIndex(strings.ToLower(value), "</think>"); idx >= 0 {
		value = strings.TrimSpace(value[idx+len("</think>"):])
	}
	if value == "NONE" {
		return nil, true, nil
	}
	if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") || strings.Contains(value, "```") {
		return nil, false, errors.New("multiagent: Freewill ideas must be bounded plain lines")
	}
	banned := []string{"execute", "shell", "command", "tool", "network", "http", "secret", "credential", "password", "token", "write file", "delete", "install", "ignore policy", "bypass policy", "override policy", "approve this action", "reject this action", "run shell"}
	seen := make(map[string]struct{})
	ideas := make([]string, 0, 3)
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		marker := strings.Index(line, "IDEA:")
		if marker < 0 {
			continue
		}
		if marker > 0 {
			prefix := strings.ToLower(strings.TrimSpace(line[:marker]))
			if strings.Contains(prefix, "example") || strings.Contains(prefix, "candidate") || strings.Contains(prefix, "sample") {
				return nil, false, errors.New("multiagent: Freewill idea response contains a labeled example or candidate block")
			}
		}
		line = strings.TrimSpace(line[marker:])
		idea := strings.TrimSpace(strings.TrimPrefix(line, "IDEA:"))
		if idea == "" || len(idea) > 160 || strings.ContainsAny(idea, "\r\n{}[]") || strings.Contains(idea, "\\") || strings.Contains(idea, "://") || strings.Contains(idea, "..") {
			return nil, false, errors.New("multiagent: Freewill idea is malformed or contains a path")
		}
		lower := strings.ToLower(idea)
		for _, word := range banned {
			if strings.Contains(lower, word) {
				return nil, false, errors.New("multiagent: Freewill idea requests a forbidden authority")
			}
		}
		key := strings.ToLower(strings.Join(strings.Fields(idea), " "))
		if _, exists := seen[key]; exists {
			return nil, false, errors.New("multiagent: Freewill ideas must be unique")
		}
		seen[key] = struct{}{}
		ideas = append(ideas, idea)
		if len(ideas) > 3 {
			return nil, false, errors.New("multiagent: Freewill returned more than three ideas")
		}
	}
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.Contains(lower, "candidate:") || strings.Contains(lower, "example:") || strings.Contains(lower, "sample:") {
			return nil, false, errors.New("multiagent: Freewill idea response contains a labeled example or candidate block")
		}
	}
	if len(ideas) == 0 {
		return nil, false, errors.New("multiagent: Freewill returned no ideas")
	}
	return ideas, false, nil
}

// parseFreewillCandidateChoice accepts one exact allowlisted ID or NONE, or one
// explicit final marker. Ordinary prose mentions are never interpreted.
func parseFreewillCandidateChoice(content string, allowedIDs []string) (FreewillCandidateChoice, error) {
	if len(content) == 0 || len(content) > 2048 {
		return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice response is empty or exceeds limit")
	}
	for _, r := range content {
		if r < 0x20 && r != '\r' && r != '\n' && r != '\t' {
			return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice contains control characters")
		}
	}
	allowed := make(map[string]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		id = strings.TrimSpace(id)
		if id == "" || strings.ContainsAny(id, " \t\r\n{}[]\"'") {
			return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice allowlist contains invalid candidate ID")
		}
		if _, exists := allowed[id]; exists {
			return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice allowlist contains duplicate candidate ID")
		}
		allowed[id] = struct{}{}
	}
	value := strings.TrimSpace(content)
	// GLM may append a private-reasoning boundary without an opening tag in
	// the public content. Treat that measured delimiter as a line boundary;
	// it never grants a value or changes the allowlist.
	value = strings.ReplaceAll(value, "</think>", "\n")
	value = strings.ReplaceAll(value, "</THINK>", "\n")
	if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
		return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice must not be JSON")
	}
	if strings.HasPrefix(value, "```") && strings.HasSuffix(value, "```") {
		lines := strings.Split(value, "\n")
		if len(lines) < 3 || strings.TrimSpace(lines[len(lines)-1]) != "```" {
			return FreewillCandidateChoice{}, errors.New("multiagent: malformed Freewill choice code fence")
		}
		value = strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
	}
	if _, ok := allowed[value]; ok || value == "NONE" {
		if value == "NONE" {
			return FreewillCandidateChoice{Sleep: true}, nil
		}
		return FreewillCandidateChoice{ChoiceID: value}, nil
	}
	var marked string
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		prefix := ""
		for _, candidate := range []string{"choice:", "reply exactly:", "answer:"} {
			if strings.HasPrefix(lower, candidate) {
				prefix = candidate
				break
			}
		}
		if prefix == "" {
			continue
		}
		item := strings.TrimSpace(line[len(prefix):])
		if strings.Contains(item, "choice:") {
			parts := strings.Split(item, "choice:")
			if len(parts) > 1 {
				first := strings.TrimSpace(parts[0])
				consistent := first != ""
				for _, part := range parts[1:] {
					if strings.TrimSpace(part) != first {
						consistent = false
					}
				}
				if consistent {
					item = first
				}
			}
		}
		if item == "" || strings.ContainsAny(item, " \t\r\n{}[]\"'") {
			return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice marker is not one token")
		}
		if marked != "" && marked != item {
			return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice contains conflicting markers")
		}
		marked = item
	}
	if marked == "NONE" {
		return FreewillCandidateChoice{Sleep: true}, nil
	}
	if _, ok := allowed[marked]; !ok || marked == "" {
		return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice marker is not allowlisted")
	}
	return FreewillCandidateChoice{ChoiceID: marked}, nil
}

// ChatFreewillSelector is a read-only provider adapter. It sends no tools and
// accepts only the strict Freewill selection DTO; runtime validation supplies
// trusted identity, scope, and resume eligibility afterward.
type ChatFreewillSelector struct {
	Provider        provider.ChatProvider
	AgentID         string
	Model           string
	ReasoningEffort string
}

// SelectCandidateChoice performs the optional second, decision-only phase.
// It deliberately uses no native schema: GLM cloud models have been observed
// to emit schema-disclaiming prose. The host parser remains authoritative.
func (s ChatFreewillSelector) SelectCandidateChoice(ctx context.Context, input FreewillCandidateChoiceInput) (FreewillCandidateChoice, FreewillSelectionEvidence, error) {
	if s.Provider == nil || strings.TrimSpace(s.AgentID) == "" || strings.TrimSpace(s.Model) == "" || input.AgentID != s.AgentID {
		return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice provider configuration is incomplete")
	}
	if strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.CorrelationID) == "" || len(input.CandidateIDs) > 16 || (len(input.CandidateLabels) != 0 && len(input.CandidateLabels) != len(input.CandidateIDs)) {
		return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice input is invalid")
	}
	if len(input.CandidateIDs) == 0 {
		return FreewillCandidateChoice{Sleep: true}, FreewillSelectionEvidence{}, nil
	}
	ids := make([]string, len(input.CandidateIDs))
	copy(ids, input.CandidateIDs)
	if _, err := parseFreewillCandidateChoice("NONE", ids); err != nil {
		return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, err
	}
	labels := make([]string, len(input.CandidateLabels))
	for i, label := range input.CandidateLabels {
		label = strings.TrimSpace(label)
		if label == "" || len(label) > 160 || strings.ContainsAny(label, "\r\n{}[]") || strings.Contains(label, "\\") || strings.Contains(label, "://") {
			return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice label is invalid")
		}
		labels[i] = label
	}
	user, err := json.Marshal(struct {
		CandidateIDs   []string `json:"candidate_ids"`
		CandidateLabels []string `json:"candidate_labels,omitempty"`
	}{ids, labels})
	if err != nil {
		return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice context could not be encoded")
	}
	think := false
	response, err := s.Provider.ChatGenerate(ctx, provider.ChatGenerateRequest{
		RunID: input.RunID, CorrelationID: input.CorrelationID, Agent: s.AgentID, Model: s.Model,
		Messages:  []provider.ChatMessage{{Role: "system", Content: freewillChoiceSystemPrompt}, {Role: "user", Content: string(user)}},
		MaxTokens: freewillChoiceMaxOutputTokens, Think: &think,
	})
	if err != nil {
		return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, err
	}
	if len(response.ToolCalls) != 0 {
		return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice provider requested a tool")
	}
	choice, err := parseFreewillCandidateChoice(response.Content, ids)
	if err != nil {
		log.Printf("[FREEWILL] mapper diagnostic %s", freewillMapperDiagnostic("choice", response, s.Model, "invalid_choice", 0, 0, true))
		return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, &SchemaCorrectionError{Role: RolePlanner, Code: "invalid_choice"}
	}
	providerName, model := response.Provider, response.Model
	if providerName == "" {
		providerName = "configured"
	}
	if model == "" {
		model = s.Model
	}
	usage := cost.TokenUsage{PromptTokens: response.PromptTokens, CompletionTokens: response.OutputTokens, TotalTokens: response.PromptTokens + response.OutputTokens}
	return choice, FreewillSelectionEvidence{Provider: providerName, Model: model, Usage: usage}, nil
}

func (s ChatFreewillSelector) SelectFreewill(ctx context.Context, input FreewillSelectionInput) (freewill.Selection, FreewillSelectionEvidence, error) {
	if s.Provider == nil || strings.TrimSpace(s.AgentID) == "" || strings.TrimSpace(s.Model) == "" {
		return freewill.Selection{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill selector provider configuration is incomplete")
	}
	if input.AgentID != s.AgentID || strings.TrimSpace(input.Scope.ProjectID) == "" || strings.TrimSpace(input.Scope.Workspace) == "" {
		return freewill.Selection{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill selector input does not match its configured identity")
	}
	if strings.Contains(strings.ToLower(s.Model), "glm-5.3-flash") {
		return s.selectFreewillFlashIdeas(ctx, input)
	}
	resumeJSON, err := json.Marshal(summarizeFreewillResumeCandidates(input.ResumeCandidates))
	if err != nil {
		return freewill.Selection{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill resume context could not be encoded")
	}
	user, err := json.Marshal(struct {
		AgentID          string                  `json:"agent_id"`
		Scope            freewill.Scope          `json:"scope"`
		ResumeCandidates json.RawMessage         `json:"verified_resume_candidates"`
		Budget           freewill.ResourceBudget `json:"requested_budget"`
		Correction       string                  `json:"correction,omitempty"`
	}{input.AgentID, input.Scope, resumeJSON, input.Budget, input.RecoveryFeedback})
	if err != nil {
		return freewill.Selection{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill selector context could not be encoded")
	}
	var outputSchema map[string]any
	if err := json.Unmarshal(FreewillSelectionOutputSchema(), &outputSchema); err != nil {
		return freewill.Selection{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill selector output schema is invalid")
	}
	response, err := s.Provider.ChatGenerate(ctx, provider.ChatGenerateRequest{
		RunID: input.RunID, CorrelationID: input.CorrelationID, Agent: s.AgentID,
		Model: s.Model, ReasoningEffort: s.ReasoningEffort, OutputSchema: outputSchema,
		Messages: []provider.ChatMessage{
			{Role: "system", Content: freewillSelectionSystemPrompt},
			{Role: "user", Content: string(user)},
		},
		// Selection has no action channel. Keep each provider response bounded.
		MaxTokens: freewillSelectionMaxOutputTokens,
	})
	if err != nil {
		return freewill.Selection{}, FreewillSelectionEvidence{}, err
	}
	if len(response.ToolCalls) != 0 {
		return freewill.Selection{}, FreewillSelectionEvidence{}, &SchemaCorrectionError{Role: RolePlanner, Code: "selection_tool_request"}
	}
	if strings.TrimSpace(response.Content) == "" {
		return freewill.Selection{}, FreewillSelectionEvidence{}, &SchemaCorrectionError{Role: RolePlanner, Code: "empty_selection_response"}
	}
	proposalContent, err := normalizeFreewillSelectionResponse(response.Content)
	if err != nil {
		code := "invalid_selection_json"
		if strings.Contains(err.Error(), "exactly three candidates") {
			code = "selection_candidate_count"
		}
		return freewill.Selection{}, FreewillSelectionEvidence{}, &SchemaCorrectionError{Role: RolePlanner, Code: code}
	}
	proposal, err := decodeFreewillSelectionProposal(proposalContent)
	if err != nil {
		code := "invalid_selection_json"
		if strings.Contains(err.Error(), "unknown field") {
			code = "unknown_selection_field"
		}
		return freewill.Selection{}, FreewillSelectionEvidence{}, &SchemaCorrectionError{Role: RolePlanner, Code: code}
	}
	selection, err := bindFreewillSelectionProposal(input, proposal)
	if err != nil {
		code := "selection_contract_invalid"
		if len(proposal.Candidates) != 3 {
			code = "selection_candidate_count"
		}
		return freewill.Selection{}, FreewillSelectionEvidence{}, &SchemaCorrectionError{Role: RolePlanner, Code: code}
	}
	// GLM-5.3 cloud has repeatedly returned schema-disclaiming prose for the
	// large proposal object. Once the host has bound and validated candidate
	// IDs, use the smaller ID/NONE protocol for fresh choices only. Resume and
	// explicit sleep remain governed by the existing proposal contract.
	var choiceEvidence *FreewillSelectionEvidence
	if strings.Contains(strings.ToLower(s.Model), "glm-5.3-flash") && proposal.ResumeCandidateID == "" && strings.TrimSpace(proposal.SleepReason) == "" {
		ids := make([]string, 0, len(selection.Candidates))
		for _, candidate := range selection.Candidates {
			ids = append(ids, candidate.CandidateID)
		}
		choice, choiceMeta, choiceErr := s.SelectCandidateChoice(ctx, FreewillCandidateChoiceInput{
			RunID: input.RunID, CorrelationID: input.CorrelationID, AgentID: input.AgentID, CandidateIDs: ids,
		})
		if choiceErr != nil {
			return freewill.Selection{}, FreewillSelectionEvidence{}, choiceErr
		}
		choiceEvidence = &choiceMeta
		selection.ReasonSummary = "Host accepted the allowlisted Freewill choice."
		if choice.Sleep {
			selection.ChoiceID = ""
			selection.ReasonSummary = ""
			selection.SleepReason = "No eligible candidate was selected by the bounded choice phase."
		} else {
			selection.ChoiceID = choice.ChoiceID
		}
	}
	providerName, model := response.Provider, response.Model
	if providerName == "" {
		providerName = "configured"
	}
	if model == "" {
		model = s.Model
	}
	usage := cost.TokenUsage{PromptTokens: response.PromptTokens, CompletionTokens: response.OutputTokens, TotalTokens: response.PromptTokens + response.OutputTokens}
	if choiceEvidence != nil {
		usage.PromptTokens += choiceEvidence.Usage.PromptTokens
		usage.CompletionTokens += choiceEvidence.Usage.CompletionTokens
		usage.TotalTokens += choiceEvidence.Usage.TotalTokens
	}
	return selection, FreewillSelectionEvidence{Provider: providerName, Model: model, Usage: usage}, nil
}

func (s ChatFreewillSelector) selectFreewillFlashIdeas(ctx context.Context, input FreewillSelectionInput) (freewill.Selection, FreewillSelectionEvidence, error) {
	think := false
	user, err := json.Marshal(struct {
		Mode       string `json:"mode"`
		MaxIdeas   int    `json:"max_ideas"`
		Correction string `json:"correction,omitempty"`
	}{Mode: "decision_only", MaxIdeas: 3, Correction: input.RecoveryFeedback})
	if err != nil {
		return freewill.Selection{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill idea context could not be encoded")
	}
	response, err := s.Provider.ChatGenerate(ctx, provider.ChatGenerateRequest{
		RunID: input.RunID, CorrelationID: input.CorrelationID, Agent: s.AgentID, Model: s.Model,
		Messages:  []provider.ChatMessage{{Role: "system", Content: freewillIdeaSystemPrompt}, {Role: "user", Content: string(user)}},
		MaxTokens: freewillIdeaMaxOutputTokens, Think: &think,
	})
	if err != nil {
		return freewill.Selection{}, FreewillSelectionEvidence{}, err
	}
	if len(response.ToolCalls) != 0 {
		log.Printf("[FREEWILL] mapper diagnostic %s", freewillMapperDiagnostic("idea", response, s.Model, "idea_tool_request", freewillIdeaMarkerCount(response.Content), ideaRetryNumber(input.RecoveryFeedback), true))
		return freewill.Selection{}, FreewillSelectionEvidence{}, &SchemaCorrectionError{Role: RolePlanner, Code: "idea_tool_request"}
	}
	ideas, none, err := parseFreewillIdeaLines(response.Content)
	if err != nil {
		log.Printf("[FREEWILL] mapper diagnostic %s", freewillMapperDiagnostic("idea", response, s.Model, "invalid_idea_lines", freewillIdeaMarkerCount(response.Content), ideaRetryNumber(input.RecoveryFeedback), true))
		return freewill.Selection{}, FreewillSelectionEvidence{}, &SchemaCorrectionError{Role: RolePlanner, Code: "invalid_idea_lines"}
	}
	providerName, model := response.Provider, response.Model
	if providerName == "" {
		providerName = "configured"
	}
	if model == "" {
		model = s.Model
	}
	usage := cost.TokenUsage{PromptTokens: response.PromptTokens, CompletionTokens: response.OutputTokens, TotalTokens: response.PromptTokens + response.OutputTokens}
	if none || len(ideas) < 3 {
		return freewill.Selection{SleepReason: "Host found no complete set of bounded decision-only ideas."}, FreewillSelectionEvidence{Provider: providerName, Model: model, Usage: usage}, nil
	}
	selection := freewill.Selection{ReasonSummary: "Host bounded the decision-only ideas."}
	for i, idea := range ideas {
		candidateID := fmt.Sprintf("candidate-%s-%d", input.RunID, i+1)
		request := freewill.IntentionRequest{
			RequestID: "request-" + input.RunID + fmt.Sprintf("-%d", i+1), AgentID: input.AgentID,
			Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: idea, Goal: idea,
			ReasonSummary: "Host-bounded decision-only candidate.", RequestedCapabilities: []string{"decision_only"},
			Scope: input.Scope, IdempotencyKey: "freewill-" + input.RunID + fmt.Sprintf("-%d", i+1), CorrelationID: input.CorrelationID, Budget: input.Budget,
		}
		selection.Candidates = append(selection.Candidates, freewill.CandidateIntention{CandidateID: candidateID, Request: request, Score: 0, ScoreFactors: []string{"scope_fit"}, ScoreRationale: "Host-bounded scope fit."})
	}
	ids := []string{selection.Candidates[0].CandidateID, selection.Candidates[1].CandidateID, selection.Candidates[2].CandidateID}
	labels := make([]string, len(selection.Candidates))
	for i, candidate := range selection.Candidates { labels[i] = candidate.Request.Title }
	choice, choiceMeta, err := s.SelectCandidateChoice(ctx, FreewillCandidateChoiceInput{RunID: input.RunID, CorrelationID: input.CorrelationID, AgentID: input.AgentID, CandidateIDs: ids, CandidateLabels: labels})
	if err != nil {
		var schemaErr *SchemaCorrectionError
		if errors.As(err, &schemaErr) && schemaErr.Code == "invalid_choice" {
			selection.ChoiceID = deterministicFreewillChoiceID(selection.Candidates)
			if selection.ChoiceID == "" {
				selection.ReasonSummary = "No eligible candidate remained after bounded choice validation."
				selection.SleepReason = "No eligible candidate remained after bounded choice validation."
			} else {
				selection.ReasonSummary = "Host deterministic ranking selected after bounded model choice validation failed."
			}
			return selection, FreewillSelectionEvidence{Provider: providerName, Model: model, Usage: usage}, nil
		}
		return freewill.Selection{}, FreewillSelectionEvidence{}, err
	}
	usage.PromptTokens += choiceMeta.Usage.PromptTokens
	usage.CompletionTokens += choiceMeta.Usage.CompletionTokens
	usage.TotalTokens += choiceMeta.Usage.TotalTokens
	if choice.Sleep {
		selection.ReasonSummary = ""
		selection.SleepReason = "No eligible candidate was selected by the bounded choice phase."
	} else {
		selection.ChoiceID = choice.ChoiceID
	}
	return selection, FreewillSelectionEvidence{Provider: providerName, Model: model, Usage: usage}, nil
}

func deterministicFreewillChoiceID(candidates []freewill.CandidateIntention) string {
	if len(candidates) == 0 { return "" }
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.Score > best.Score || (candidate.Score == best.Score && candidate.CandidateID < best.CandidateID) {
			best = candidate
		}
	}
	return best.CandidateID
}

// normalizeFreewillSelectionResponse accepts bounded JSON and known harmless
// wrappers, removes non-authoritative extras, and constructs a canonical proposal
// from model-authored judgments. It never creates candidate goals, reasons, scores,
// authority, or resource budgets.
func normalizeFreewillSelectionResponse(content string) ([]byte, error) {
	if len(content) > maxFreewillSelectionResponseBytes {
		return nil, errors.New("multiagent: Freewill selection response exceeds limit")
	}
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		firstNewline := strings.IndexByte(content, '\n')
		if firstNewline < 0 || !strings.HasSuffix(content, "```") {
			return nil, errors.New("multiagent: malformed Freewill selection fence")
		}
		header := strings.TrimSuffix(content[:firstNewline], "\r")
		if header != "```" && !strings.EqualFold(header, "```json") {
			return nil, errors.New("multiagent: unsupported Freewill selection fence")
		}
		content = strings.TrimSpace(content[firstNewline+1 : len(content)-3])
		if strings.Contains(content, "```") {
			return nil, errors.New("multiagent: nested Freewill selection fence")
		}
	}
	data := []byte(content)
	if err := validateNoDuplicateJSONKeys(data); err != nil {
		return nil, errors.New("multiagent: duplicate Freewill selection JSON key")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) == nil {
		if _, hasEnvelopeType := envelope["type"]; hasEnvelopeType && len(envelope) != 2 {
			return nil, errors.New("multiagent: invalid Freewill final envelope shape")
		}
	}
	if json.Unmarshal(data, &envelope) == nil && len(envelope) == 2 {
		var kind string
		var text string
		if raw, ok := envelope["type"]; ok && json.Unmarshal(raw, &kind) == nil && kind == "final" {
			if raw, ok := envelope["content"]; ok && json.Unmarshal(raw, &text) == nil {
				text = strings.TrimSpace(text)
				if len(text) > maxFreewillSelectionResponseBytes || text == "" {
					return nil, errors.New("multiagent: invalid Freewill final envelope content")
				}
				data = []byte(text)
				if err := validateNoDuplicateJSONKeys(data); err != nil {
					return nil, errors.New("multiagent: duplicate Freewill selection JSON key")
				}
			}
		}
	}
	if !json.Valid(data) {
		return nil, errors.New("multiagent: Freewill selection response is not JSON")
	}
	return stripHarmlessFreewillSelectionFields(data)
}

func validateNoDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid JSON object key")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate JSON object key")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			closeToken, err := decoder.Token()
			if err != nil || closeToken != json.Delim('}') {
				return errors.New("invalid JSON object")
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			closeToken, err := decoder.Token()
			if err != nil || closeToken != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON content")
	}
	return nil
}

func stripHarmlessFreewillSelectionFields(data []byte) ([]byte, error) {
	var proposal map[string]json.RawMessage
	if err := json.Unmarshal(data, &proposal); err != nil || proposal == nil {
		return nil, errors.New("multiagent: Freewill selection must be a JSON object")
	}
	topRequired := []string{"candidates"}
	topAllowed := map[string]struct{}{"candidates": {}, "resume_candidate_id": {}, "reason_summary": {}, "sleep_reason": {}}
	if err := requireFreewillJSONFields(proposal, topRequired); err != nil {
		return nil, err
	}
	for key := range map[string]struct{}{"resume_candidate_id": {}, "sleep_reason": {}} {
		if _, present := proposal[key]; !present {
			proposal[key] = json.RawMessage(`""`)
		}
	}
	candidates, err := normalizeFreewillSelectionCandidates(proposal["candidates"])
	if err != nil {
		return nil, err
	}
	if _, present := proposal["reason_summary"]; !present {
		proposal["reason_summary"], err = highestScoringCandidateReason(candidates)
		if err != nil {
			return nil, err
		}
	}
	proposal["candidates"], err = json.Marshal(candidates)
	if err != nil {
		return nil, errors.New("multiagent: Freewill candidate list normalization failed")
	}
	for key := range proposal {
		if _, allowed := topAllowed[key]; !allowed {
			delete(proposal, key)
		}
	}
	cleaned, err := json.Marshal(proposal)
	if err != nil {
		return nil, errors.New("multiagent: Freewill selection normalization failed")
	}
	return cleaned, nil
}

func normalizeFreewillSelectionCandidates(raw json.RawMessage) ([]json.RawMessage, error) {
	var candidates []json.RawMessage
	if err := json.Unmarshal(raw, &candidates); err != nil || len(candidates) != 3 {
		return nil, errors.New("multiagent: Freewill selection requires exactly three candidates")
	}
	candidateRequired := []string{"title", "goal", "reason_summary", "requested_capabilities", "score", "score_factors", "score_rationale"}
	candidateAllowed := make(map[string]struct{}, len(candidateRequired)+1)
	for _, key := range candidateRequired {
		candidateAllowed[key] = struct{}{}
	}
	candidateAllowed["budget"] = struct{}{}
	for i, candidateData := range candidates {
		var candidate map[string]json.RawMessage
		if err := json.Unmarshal(candidateData, &candidate); err != nil || candidate == nil {
			return nil, errors.New("multiagent: Freewill candidate must be an object")
		}
		if err := requireFreewillJSONFields(candidate, candidateRequired); err != nil {
			return nil, err
		}
		factors, err := normalizeFreewillScoreFactors(candidate["score_factors"])
		if err != nil {
			return nil, err
		}
		candidate["score_factors"] = factors
		if _, present := candidate["budget"]; !present {
			candidate["budget"] = json.RawMessage("null")
		}
		if err := validateFreewillBudgetShape(candidate["budget"]); err != nil {
			candidate["budget"] = json.RawMessage("null")
		}
		for key := range candidate {
			if _, allowed := candidateAllowed[key]; !allowed {
				delete(candidate, key)
			}
		}
		candidates[i], err = json.Marshal(candidate)
		if err != nil {
			return nil, errors.New("multiagent: Freewill candidate normalization failed")
		}
	}
	return candidates, nil
}

func normalizeFreewillScoreFactors(raw json.RawMessage) (json.RawMessage, error) {
	var factors []string
	if json.Unmarshal(raw, &factors) == nil && factors != nil {
		return raw, nil
	}
	var weights map[string]json.RawMessage
	if json.Unmarshal(raw, &weights) != nil || len(weights) == 0 {
		return nil, errors.New("multiagent: invalid Freewill score factors")
	}
	allowed := map[string]struct{}{"scope_fit": {}, "feasibility": {}, "impact": {}, "novelty": {}, "readiness": {}}
	factors = make([]string, 0, len(weights))
	for key, value := range weights {
		if _, ok := allowed[key]; !ok {
			return nil, errors.New("multiagent: unsupported Freewill score factor")
		}
		var numeric float64
		if err := json.Unmarshal(value, &numeric); err != nil {
			return nil, errors.New("multiagent: invalid Freewill score factor value")
		}
		factors = append(factors, key)
	}
	sort.Strings(factors)
	encoded, err := json.Marshal(factors)
	if err != nil {
		return nil, errors.New("multiagent: Freewill score factor normalization failed")
	}
	return encoded, nil
}

func highestScoringCandidateReason(candidates []json.RawMessage) (json.RawMessage, error) {
	bestScore := -1
	var bestReason json.RawMessage
	for _, raw := range candidates {
		var candidate map[string]json.RawMessage
		if err := json.Unmarshal(raw, &candidate); err != nil {
			return nil, errors.New("multiagent: invalid Freewill candidate")
		}
		var score int
		var reason string
		if json.Unmarshal(candidate["score"], &score) != nil || json.Unmarshal(candidate["reason_summary"], &reason) != nil || strings.TrimSpace(reason) == "" {
			return nil, errors.New("multiagent: cannot derive selection summary from candidate")
		}
		if score > bestScore {
			bestScore = score
			bestReason = append(json.RawMessage(nil), candidate["reason_summary"]...)
		}
	}
	if len(bestReason) == 0 {
		return nil, errors.New("multiagent: cannot derive selection summary from candidates")
	}
	return bestReason, nil
}

func requireFreewillJSONFields(object map[string]json.RawMessage, required []string) error {
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return fmt.Errorf("multiagent: required Freewill field %q is missing", key)
		}
	}
	return nil
}

func validateFreewillBudgetShape(raw json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var budget map[string]json.RawMessage
	if err := json.Unmarshal(raw, &budget); err != nil || budget == nil {
		return errors.New("multiagent: Freewill budget must be null or an object")
	}
	required := []string{"runtime_minutes", "disk_mb", "max_model_calls", "max_tokens"}
	if err := requireFreewillJSONFields(budget, required); err != nil {
		return err
	}
	allowed := make(map[string]struct{}, len(required))
	for _, key := range required {
		allowed[key] = struct{}{}
	}
	for key := range budget {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown field: unsupported budget field")
		}
	}
	return nil
}

func decodeFreewillSelectionProposal(data []byte) (freewillSelectionProposal, error) {
	var proposal freewillSelectionProposal
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proposal); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return freewillSelectionProposal{}, errors.New("unknown field")
		}
		return freewillSelectionProposal{}, errors.New("invalid Freewill selection proposal")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return freewillSelectionProposal{}, errors.New("invalid Freewill selection proposal")
	}
	return proposal, nil
}

func bindFreewillSelectionProposal(input FreewillSelectionInput, proposal freewillSelectionProposal) (freewill.Selection, error) {
	if len(proposal.Candidates) != 3 || len(input.RunID) == 0 || len(input.RunID) > 80 || len(input.CorrelationID) == 0 || len(input.CorrelationID) > 128 {
		return freewill.Selection{}, errors.New("invalid Freewill selection proposal")
	}
	selection := freewill.Selection{ReasonSummary: proposal.ReasonSummary, SleepReason: proposal.SleepReason}
	seenProposals := make(map[string]struct{}, len(proposal.Candidates))
	for i, candidate := range proposal.Candidates {
		if candidate.Score == nil || *candidate.Score < 0 || *candidate.Score > 100 || strings.TrimSpace(candidate.ScoreRationale) == "" || len(candidate.ScoreFactors) == 0 {
			return freewill.Selection{}, errors.New("invalid Freewill candidate evaluation")
		}
		proposalKey := strings.ToLower(strings.Join(strings.Fields(candidate.Title), " ")) + "\x00" + strings.ToLower(strings.Join(strings.Fields(candidate.Goal), " "))
		if _, duplicate := seenProposals[proposalKey]; duplicate {
			return freewill.Selection{}, errors.New("duplicate Freewill candidate proposal")
		}
		seenProposals[proposalKey] = struct{}{}
		candidateID := fmt.Sprintf("candidate-%s-%d", input.RunID, i+1)
		var budget freewill.ResourceBudget
		if candidate.Budget != nil {
			budget = *candidate.Budget
		}
		request := freewill.IntentionRequest{
			RequestID: "request-" + input.RunID + fmt.Sprintf("-%d", i+1), AgentID: input.AgentID,
			Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew,
			Title: candidate.Title, Goal: candidate.Goal, ReasonSummary: candidate.ReasonSummary,
			RequestedCapabilities: candidate.RequestedCapabilities, Scope: input.Scope,
			IdempotencyKey: "freewill-" + input.RunID + fmt.Sprintf("-%d", i+1),
			CorrelationID:  input.CorrelationID, Budget: budget,
		}
		bound := freewill.CandidateIntention{
			CandidateID: candidateID, Request: request, Score: *candidate.Score,
			ScoreFactors: candidate.ScoreFactors, ScoreRationale: candidate.ScoreRationale,
		}
		if err := bound.Validate(); err != nil {
			return freewill.Selection{}, errors.New("invalid Freewill candidate evaluation")
		}
		selection.Candidates = append(selection.Candidates, bound)
	}
	if proposal.ResumeCandidateID != "" {
		if strings.TrimSpace(proposal.ReasonSummary) == "" || strings.TrimSpace(proposal.SleepReason) != "" {
			return freewill.Selection{}, errors.New("invalid Freewill selection proposal")
		}
		for _, resume := range input.ResumeCandidates {
			if resume.CandidateID == proposal.ResumeCandidateID && resume.Request.CandidateSource == freewill.SourceResume {
				selection.Candidates = append(selection.Candidates, resume)
				selection.ChoiceID = resume.CandidateID
				break
			}
		}
		if selection.ChoiceID == "" {
			return freewill.Selection{}, errors.New("invalid Freewill selection proposal")
		}
	} else if strings.TrimSpace(proposal.SleepReason) != "" {
		if strings.TrimSpace(proposal.ReasonSummary) != "" {
			return freewill.Selection{}, errors.New("invalid Freewill selection proposal")
		}
	} else {
		if strings.TrimSpace(proposal.ReasonSummary) == "" {
			return freewill.Selection{}, errors.New("invalid Freewill selection proposal")
		}
		best := 0
		for i := 1; i < len(selection.Candidates); i++ {
			if selection.Candidates[i].Score > selection.Candidates[best].Score {
				best = i
			}
		}
		selection.ChoiceID = selection.Candidates[best].CandidateID
	}
	return selection, nil
}

func summarizeFreewillResumeCandidates(candidates []freewill.CandidateIntention) []map[string]any {
	result := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, map[string]any{
			"candidate_id": candidate.CandidateID, "title": candidate.Request.Title,
			"goal": candidate.Request.Goal, "reason_summary": candidate.Request.ReasonSummary,
			"requested_capabilities": candidate.Request.RequestedCapabilities,
		})
	}
	return result
}
