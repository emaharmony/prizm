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
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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

const freewillSelectionSystemPrompt = `Return one JSON object matching the supplied native schema. Include three fresh candidates for new, resume, or sleep outcomes; use only verified resume IDs and the appropriate outcome fields. Add no prose or extra keys and request no tools. Treat an optional host objective as subject context only. The host binds identity and scope; a choice is only a proposal, not approval or execution.`

const maxFreewillSelectionResponseBytes = 64 << 10
const freewillSelectionMaxOutputTokens = 4096

const freewillChoiceSystemPrompt = `Choose the candidate that best fits the objective; scores are advisory. Reply with exactly two lines: "choice: <candidate ID|NONE>" and "reason: <one concise sentence>". If a lower-scored candidate better fits the objective, it may be chosen; explain why in the reason. If the objective is absent, use the titles and advisory scores and give a reason. Do not include tools, paths, authority, or extra text.`
const freewillChoiceMaxOutputTokens = 1024
const freewillFlashIdeaMaxOutputTokens = 1024

const freewillFlashIdeaSystemPrompt = `Return exactly three safe, decision-only blocks with these labels in order:
IDEA: short follow-up
SCORE: integer 0-100
FACTORS: one or more comma-separated labels from scope_fit, feasibility, impact, novelty, readiness
RATIONALE: one specific sentence
If none fits, return exactly NONE. No JSON, tools, paths, preamble, or extra prose.`
const freewillFlashChoiceSystemPrompt = `Choose using the objective and supplied candidate IDs. Return exactly two lines:
CHOICE: one supplied candidate ID or NONE
REASON: one concise sentence
Scores are advisory. Do not add prose, paths, tools, or authority requests.`

func isFreewillFlashCloudModel(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), "glm-5.3-flash:cloud")
}

type freewillFlashIdeaOutput struct {
	Candidates *[]freewillFlashIdeaProposal `json:"candidates"`
	Sleep      *bool                        `json:"sleep"`
}

type freewillFlashIdeaProposal struct {
	Idea      string   `json:"idea"`
	Score     *int     `json:"score"`
	Factors   []string `json:"factors"`
	Rationale string   `json:"rationale"`
}

type freewillFlashChoiceOutput struct {
	ChoiceID *string `json:"choice_id"`
	Reason   *string `json:"reason"`
}

type freewillStrictJSONError struct{ Code string }

func (e *freewillStrictJSONError) Error() string { return e.Code }

func decodeFreewillStrictJSON(content string, target any, maxBytes int) error {
	if len(content) == 0 || len(content) > maxBytes || !utf8.ValidString(content) {
		return &freewillStrictJSONError{Code: "size_or_encoding"}
	}
	data := []byte(content)
	if err := validateNoDuplicateJSONKeys(data); err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			return &freewillStrictJSONError{Code: "duplicate_key"}
		}
		if strings.Contains(err.Error(), "trailing") {
			return &freewillStrictJSONError{Code: "trailing_content"}
		}
		return &freewillStrictJSONError{Code: "syntax"}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return &freewillStrictJSONError{Code: "unknown_field"}
		}
		return &freewillStrictJSONError{Code: "wrong_shape"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return &freewillStrictJSONError{Code: "trailing_content"}
	}
	return nil
}

func parseFreewillFlashChoiceResponse(content string, allowedIDs []string) (FreewillCandidateChoice, error) {
	if len(content) == 0 || len(content) > 2048 || !utf8.ValidString(content) {
		return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice response is invalid")
	}
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		firstNewline := strings.IndexByte(trimmed, '\n')
		if firstNewline < 0 || !strings.HasSuffix(trimmed, "```") {
			return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice response is invalid")
		}
		header := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(trimmed[:firstNewline], "\r")))
		if header != "```" && header != "```text" && header != "```markdown" && header != "```json" {
			return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice response is invalid")
		}
		trimmed = strings.TrimSpace(trimmed[firstNewline+1 : len(trimmed)-3])
		if strings.Contains(trimmed, "```") {
			return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice response is invalid")
		}
	}
	if strings.HasPrefix(trimmed, "{") {
		return parseFreewillFlashChoiceJSON(trimmed, allowedIDs)
	}
	lines := strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n")
	firstDecision := -1
	preambleBytes := 0
	for i, line := range lines {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "choice:") || strings.HasPrefix(lower, "reason:") {
			firstDecision = i
			break
		}
		if line != "" {
			if i >= 12 || !freewillInertEnvelopeLine(line) {
				return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice response is invalid")
			}
			preambleBytes += len(line)
			if preambleBytes > 512 {
				return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice response is invalid")
			}
		}
	}
	if firstDecision < 0 {
		return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice response is invalid")
	}
	return parseFreewillCandidateChoice(strings.Join(lines[firstDecision:], "\n"), allowedIDs)
}

func parseFreewillFlashChoiceJSON(content string, allowedIDs []string) (FreewillCandidateChoice, error) {
	invalid := errors.New("multiagent: Freewill choice response is invalid")
	if len(content) == 0 || len(content) > 2048 || !utf8.ValidString(content) {
		return FreewillCandidateChoice{}, invalid
	}
	normalized, err := normalizeFreewillFlashChoiceJSON(content)
	if err != nil {
		return FreewillCandidateChoice{}, invalid
	}
	var output freewillFlashChoiceOutput
	if decodeFreewillStrictJSON(string(normalized), &output, 2048) != nil || output.ChoiceID == nil || output.Reason == nil {
		return FreewillCandidateChoice{}, invalid
	}
	if *output.ChoiceID == "NONE" {
		if !validFreewillChoiceReason(*output.Reason, "", allowedIDs) {
			return FreewillCandidateChoice{}, invalid
		}
		return FreewillCandidateChoice{Sleep: true, Reason: strings.TrimSpace(*output.Reason)}, nil
	}
	if !containsFreewillString(allowedIDs, *output.ChoiceID) || !validFreewillChoiceReason(*output.Reason, *output.ChoiceID, allowedIDs) {
		return FreewillCandidateChoice{}, invalid
	}
	return FreewillCandidateChoice{ChoiceID: *output.ChoiceID, Reason: strings.TrimSpace(*output.Reason)}, nil
}

func normalizeFreewillFlashChoiceJSON(content string) ([]byte, error) {
	if len(content) == 0 || len(content) > 2048 || !utf8.ValidString(content) {
		return nil, &freewillStrictJSONError{Code: "size_or_encoding"}
	}
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		firstNewline := strings.IndexByte(trimmed, '\n')
		if firstNewline < 0 || !strings.HasSuffix(trimmed, "```") {
			return nil, &freewillStrictJSONError{Code: "syntax"}
		}
		header := strings.TrimSpace(strings.TrimSuffix(trimmed[:firstNewline], "\r"))
		if header != "```" && !strings.EqualFold(header, "```json") {
			return nil, &freewillStrictJSONError{Code: "syntax"}
		}
		trimmed = strings.TrimSpace(trimmed[firstNewline+1 : len(trimmed)-3])
		if strings.Contains(trimmed, "```") {
			return nil, &freewillStrictJSONError{Code: "syntax"}
		}
	}
	if strings.HasPrefix(trimmed, "{") {
		var envelope struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if decodeFreewillStrictJSON(trimmed, &envelope, 2048) == nil && envelope.Type == "final" && strings.TrimSpace(envelope.Content) != "" {
			trimmed = strings.TrimSpace(envelope.Content)
		}
	}
	if err := validateNoDuplicateJSONKeys([]byte(trimmed)); err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			return nil, &freewillStrictJSONError{Code: "duplicate_key"}
		}
		return nil, &freewillStrictJSONError{Code: "syntax"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil || fields == nil {
		return nil, &freewillStrictJSONError{Code: "wrong_shape"}
	}
	canonical := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		var target string
		switch {
		case strings.EqualFold(key, "choice_id"), strings.EqualFold(key, "choice"):
			target = "choice_id"
		case strings.EqualFold(key, "reason"):
			target = "reason"
		default:
			return nil, &freewillStrictJSONError{Code: "unknown_field"}
		}
		if _, exists := canonical[target]; exists {
			return nil, &freewillStrictJSONError{Code: "duplicate_key"}
		}
		canonical[target] = value
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, &freewillStrictJSONError{Code: "wrong_shape"}
	}
	return encoded, nil
}

func parseFreewillFlashIdeasJSON(content string) ([]freewillScoredIdea, bool, error) {
	var output freewillFlashIdeaOutput
	if err := decodeFreewillStrictJSON(content, &output, 4096); err != nil {
		var decodeErr *freewillStrictJSONError
		if errors.As(err, &decodeErr) {
			return nil, false, freewillScoredIdeaFailure(freewillScoredIdeaFailureCode("json_"+decodeErr.Code), 0)
		}
		return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, 0)
	}
	if output.Candidates == nil || output.Sleep == nil {
		return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, 0)
	}
	candidates, sleep := *output.Candidates, *output.Sleep
	if len(candidates) == 0 && sleep {
		return nil, true, nil
	}
	if len(candidates) != 3 || sleep {
		return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureCount, len(candidates))
	}
	seenIdeas := make(map[string]struct{}, 3)
	ideas := make([]freewillScoredIdea, 0, 3)
	for _, idea := range candidates {
		idea.Idea = strings.TrimSpace(idea.Idea)
		idea.Rationale = strings.TrimSpace(idea.Rationale)
		if len(idea.Idea) == 0 || len(idea.Idea) > 160 || strings.ContainsAny(idea.Idea, "{}[]\\") || strings.Contains(idea.Idea, "://") || strings.Contains(idea.Idea, "..") {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureShape, len(seenIdeas))
		}
		if freewillIdeaContainsForbiddenAuthority(idea.Idea) {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureUnsafe, len(seenIdeas))
		}
		key := strings.ToLower(strings.Join(strings.Fields(idea.Idea), " "))
		if _, ok := seenIdeas[key]; ok {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureDuplicate, len(seenIdeas))
		}
		seenIdeas[key] = struct{}{}
		if idea.Score == nil || *idea.Score < 0 || *idea.Score > 100 {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureScore, len(seenIdeas))
		}
		if len(idea.Factors) == 0 {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureFactors, len(seenIdeas))
		}
		factors := make(map[string]struct{}, len(idea.Factors))
		for _, factor := range idea.Factors {
			if _, ok := freewillScoreFactorSet[factor]; !ok {
				return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureFactors, len(seenIdeas))
			}
			if _, ok := factors[factor]; ok {
				return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureFactors, len(seenIdeas))
			}
			factors[factor] = struct{}{}
		}
		if len(idea.Rationale) < 30 || len(idea.Rationale) > 500 || len(strings.Fields(idea.Rationale)) < 6 || strings.IndexFunc(idea.Rationale, unicode.IsControl) >= 0 || freewillIdeaContainsForbiddenAuthority(idea.Rationale) || strings.ContainsAny(idea.Rationale, "\\") || strings.Contains(idea.Rationale, "://") {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureRationale, len(seenIdeas))
		}
		ideas = append(ideas, freewillScoredIdea{Idea: idea.Idea, Score: *idea.Score, Factors: append([]string(nil), idea.Factors...), Rationale: idea.Rationale})
	}
	return ideas, false, nil
}

// parseFreewillFlashIdeaResponse accepts the bounded plain-text idea protocol,
// with optional inert preamble and one outer text/Markdown fence. It retains
// the earlier narrow JSON compatibility forms for stored provider traces.
func parseFreewillFlashIdeaResponse(content string) ([]freewillScoredIdea, bool, error) {
	if len(content) == 0 || len(content) > 4096 || !utf8.ValidString(content) {
		return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureSize, 0)
	}
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		firstNewline := strings.IndexByte(trimmed, '\n')
		if firstNewline < 0 || !strings.HasSuffix(trimmed, "```") {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, 0)
		}
		header := strings.TrimSpace(strings.TrimSuffix(trimmed[:firstNewline], "\r"))
		if header != "```" && !strings.EqualFold(header, "```json") && !strings.EqualFold(header, "```text") && !strings.EqualFold(header, "```markdown") {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, 0)
		}
		trimmed = strings.TrimSpace(trimmed[firstNewline+1 : len(trimmed)-3])
		if strings.Contains(trimmed, "```") {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, 0)
		}
	}
	if strings.HasPrefix(trimmed, "[") {
		if err := validateNoDuplicateJSONKeys([]byte(trimmed)); err != nil {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, 0)
		}
		var candidates []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &candidates); err != nil || len(candidates) != 3 {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureCount, 0)
		}
		wrapped, err := json.Marshal(map[string]any{"candidates": candidates, "sleep": false})
		if err != nil {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, 0)
		}
		return parseFreewillFlashIdeasJSON(string(wrapped))
	}
	if strings.HasPrefix(trimmed, "{") {
		var envelope struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if decodeFreewillStrictJSON(trimmed, &envelope, 4096) == nil && envelope.Type == "final" && strings.TrimSpace(envelope.Content) != "" {
			trimmed = strings.TrimSpace(envelope.Content)
		}
		if strings.HasPrefix(trimmed, "{") {
			canonical, err := normalizeFreewillFlashIdeaJSON(trimmed)
			if err != nil {
				var decodeErr *freewillStrictJSONError
				if errors.As(err, &decodeErr) {
					return nil, false, freewillScoredIdeaFailure(freewillScoredIdeaFailureCode("json_"+decodeErr.Code), 0)
				}
				return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, 0)
			}
			return parseFreewillFlashIdeasJSON(string(canonical))
		}
	}
	return parseFreewillScoredIdeaLines(trimmed)
}

func normalizeFreewillFlashIdeaJSON(content string) ([]byte, error) {
	data := []byte(content)
	if err := validateNoDuplicateJSONKeys(data); err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			return nil, &freewillStrictJSONError{Code: "duplicate_key"}
		}
		if strings.Contains(err.Error(), "trailing") {
			return nil, &freewillStrictJSONError{Code: "trailing_content"}
		}
		return nil, &freewillStrictJSONError{Code: "syntax"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, &freewillStrictJSONError{Code: "wrong_shape"}
	}
	canonical := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		var target string
		switch {
		case strings.EqualFold(key, "candidates"), strings.EqualFold(key, "ideas"):
			target = "candidates"
		case strings.EqualFold(key, "sleep"):
			target = "sleep"
		case strings.EqualFold(key, "recommendation"):
			var recommendation string
			if err := json.Unmarshal(value, &recommendation); err != nil || len(recommendation) > 512 || strings.IndexFunc(recommendation, unicode.IsControl) >= 0 {
				return nil, &freewillStrictJSONError{Code: "wrong_shape"}
			}
			continue // Host does not consume this untrusted summary field.
		default:
			return nil, &freewillStrictJSONError{Code: "unknown_field"}
		}
		if _, exists := canonical[target]; exists {
			return nil, &freewillStrictJSONError{Code: "duplicate_key"}
		}
		canonical[target] = value
	}
	if _, hasSleep := canonical["sleep"]; !hasSleep {
		if candidates, ok := canonical["candidates"]; ok && len(bytes.TrimSpace(candidates)) > 2 && string(bytes.TrimSpace(candidates)) != "null" {
			canonical["sleep"] = json.RawMessage("false")
		}
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, &freewillStrictJSONError{Code: "wrong_shape"}
	}
	return encoded, nil
}

func containsFreewillString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func freewillMapperDiagnostic(stage string, response provider.ChatGenerateResponse, model string, code string, ideaMarkers int, parsedBlocks int, glmPlain bool) string {
	thinkLen, _ := response.Raw["thinking_len"].(int)
	trimmed := strings.TrimSpace(response.Content)
	fenced := strings.HasPrefix(trimmed, "```")
	startsObject := strings.HasPrefix(trimmed, "{")
	jsonValid := json.Valid([]byte(trimmed))
	hasCandidates := strings.Contains(trimmed, `"candidates"`)
	hasIdeaKey := strings.Contains(trimmed, `"idea"`)
	return fmt.Sprintf("stage=%s model=%s content_len=%d think_len=%d tool_calls=%d idea_markers=%d parsed_blocks=%d json_valid=%t starts_object=%t fenced=%t candidates_key=%t idea_key=%t parser_code=%s glm_plain=%t", stage, model, len(response.Content), thinkLen, len(response.ToolCalls), ideaMarkers, parsedBlocks, jsonValid, startsObject, fenced, hasCandidates, hasIdeaKey, code, glmPlain)
}

func freewillIdeaMarkerCount(content string) int {
	count := 0
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "IDEA:") {
			count++
		}
	}
	return count
}

// FreewillCandidateChoiceInput is a host-owned allowlist for the small
// decision phase. Candidate IDs are already bound by the host; the provider
// cannot create or modify an intention here.
type FreewillCandidateChoiceInput struct {
	RunID            string
	CorrelationID    string
	AgentID          string
	Objective        string
	CandidateIDs     []string
	CandidateLabels  []string
	CandidateScores  []int
	CandidateFactors [][]string
}

// FreewillCandidateChoice is the typed result of the bounded choice protocol. The
// host supplies the candidate object and policy data after this result; the
// provider response never carries those fields.
type FreewillCandidateChoice struct {
	ChoiceID string
	Reason   string
	Sleep    bool
}

type freewillScoredIdea struct {
	Idea      string   `json:"idea"`
	Score     int      `json:"score"`
	Factors   []string `json:"factors"`
	Rationale string   `json:"rationale"`
}

// parseFreewillScoreFactors accepts canonical labels and the observed GLM form
// label=integer. Per-factor annotations are not part of CandidateIntention's
// durable schema, so only their validated model-authored labels are retained.
func parseFreewillScoreFactors(raw string) ([]string, error) {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	if len(parts) == 0 || len(parts) > 5 {
		return nil, errors.New("multiagent: Freewill score factors are invalid")
	}
	factors := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.TrimLeft(part, "-* \t")
		label := part
		if strings.Contains(part, ":") {
			if strings.Count(part, ":") != 1 {
				return nil, errors.New("multiagent: Freewill score factor annotation is invalid")
			}
			var annotation string
			label, annotation, _ = strings.Cut(part, ":")
			label = strings.TrimSpace(label)
			annotation = strings.TrimSpace(annotation)
			if dash := strings.Index(annotation, "—"); dash >= 0 {
				description := strings.TrimSpace(annotation[dash+len("—"):])
				annotation = strings.TrimSpace(annotation[:dash])
				if description == "" || len(description) > 160 || !freewillInertEnvelopeLine(description) {
					return nil, errors.New("multiagent: Freewill score factor annotation is malformed")
				}
			}
			numerator, denominator, ok := strings.Cut(annotation, "/")
			if !ok || denominator != "10" || strings.Contains(numerator, "/") {
				return nil, errors.New("multiagent: Freewill score factor annotation must be N/10")
			}
			value, err := strconv.Atoi(numerator)
			if err != nil || value < 0 || value > 10 {
				return nil, errors.New("multiagent: Freewill score factor annotation is out of range")
			}
		} else if strings.Contains(part, "=") {
			if strings.Count(part, "=") != 1 {
				return nil, errors.New("multiagent: Freewill score factor annotation is invalid")
			}
			var annotation string
			label, annotation, _ = strings.Cut(part, "=")
			label = strings.TrimSpace(label)
			annotation = strings.TrimSpace(annotation)
			if annotation == "" {
				return nil, errors.New("multiagent: Freewill score factor annotation is invalid")
			}
			for _, digit := range annotation {
				if digit < '0' || digit > '9' {
					return nil, errors.New("multiagent: Freewill score factor annotation is invalid")
				}
			}
			value, err := strconv.Atoi(annotation)
			if err != nil || value < 0 || value > 100 {
				return nil, errors.New("multiagent: Freewill score factor annotation is out of range")
			}
		}
		if _, ok := freewillScoreFactorSet[label]; !ok {
			return nil, errors.New("multiagent: Freewill score factor is not allowed")
		}
		if _, duplicate := seen[label]; duplicate {
			return nil, errors.New("multiagent: Freewill score factors must be unique")
		}
		seen[label] = struct{}{}
		factors = append(factors, label)
	}
	return factors, nil
}

func freewillInertEnvelopeLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return true
	}
	if len(line) > 500 || strings.IndexFunc(line, unicode.IsControl) >= 0 || strings.ContainsAny(line, "{}[]\\") || strings.Contains(line, "://") || strings.Contains(line, "..") || freewillIdeaContainsForbiddenAuthority(line) {
		return false
	}
	lower := strings.ToLower(line)
	for _, marker := range []string{"idea:", "score:", "factors:", "rationale:", "ignore", "disregard", "override", "bypass", "system prompt", "developer message", "execute", "tool call"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	if colon := strings.IndexByte(line, ':'); colon > 0 {
		header := strings.TrimSpace(line[:colon])
		if header != "" && header == strings.ToUpper(header) {
			return false
		}
	}
	return true
}

type freewillScoredIdeaFailureCode string

const (
	freewillIdeaFailureCount     freewillScoredIdeaFailureCode = "count"
	freewillIdeaFailureSize      freewillScoredIdeaFailureCode = "size"
	freewillIdeaFailureStructure freewillScoredIdeaFailureCode = "structure"
	freewillIdeaFailureShape     freewillScoredIdeaFailureCode = "idea_shape"
	freewillIdeaFailureDuplicate freewillScoredIdeaFailureCode = "duplicate"
	freewillIdeaFailureScore     freewillScoredIdeaFailureCode = "score"
	freewillIdeaFailureFactors   freewillScoredIdeaFailureCode = "factors"
	freewillIdeaFailureRationale freewillScoredIdeaFailureCode = "rationale"
	freewillIdeaFailureUnsafe    freewillScoredIdeaFailureCode = "unsafe"
)

type freewillScoredIdeaParseError struct {
	Code         freewillScoredIdeaFailureCode
	ParsedBlocks int
}

func (e *freewillScoredIdeaParseError) Error() string {
	return "multiagent: Freewill scored response rejected"
}

func freewillScoredIdeaFailure(code freewillScoredIdeaFailureCode, parsedBlocks int) error {
	return &freewillScoredIdeaParseError{Code: code, ParsedBlocks: parsedBlocks}
}

func parseFreewillScoredIdeaLines(content string) ([]freewillScoredIdea, bool, error) {
	ideas := make([]freewillScoredIdea, 0, 3)
	if len(content) > 4096 {
		return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureSize, len(ideas))
	}
	if len(content) == 0 {
		return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureCount, len(ideas))
	}
	value := strings.TrimSpace(content)
	if idx := strings.LastIndex(strings.ToLower(value), "</think>"); idx >= 0 {
		value = strings.TrimSpace(value[idx+len("</think>"):])
	}
	if value == "NONE" {
		return nil, true, nil
	}
	if strings.Contains(value, "```") || strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
		return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, len(ideas))
	}
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	seen := make(map[string]struct{})
	envelopeBytes, envelopeLines := 0, 0
	for i := 0; i < len(lines); {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "BLOCK ") {
			blockNumber, err := strconv.Atoi(strings.TrimPrefix(line, "BLOCK "))
			if err != nil || blockNumber != len(ideas)+1 || blockNumber > 3 || i+1 >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "IDEA:") {
				return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, len(ideas))
			}
			i++
			line = strings.TrimSpace(lines[i])
		}
		if !strings.HasPrefix(line, "IDEA:") {
			for i < len(lines) {
				line = strings.TrimSpace(lines[i])
				if line == "" {
					i++
					continue
				}
				if strings.HasPrefix(line, "IDEA:") || strings.HasPrefix(line, "BLOCK ") {
					if len(ideas) == 3 {
						return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, len(ideas))
					}
					break
				}
				if len(ideas) != 0 && len(ideas) < 3 {
					return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, len(ideas))
				}
				if !freewillInertEnvelopeLine(line) {
					return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureUnsafe, len(ideas))
				}
				envelopeBytes += len(line)
				envelopeLines++
				if envelopeBytes > 2048 || envelopeLines > 24 {
					return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, len(ideas))
				}
				i++
			}
			continue
		}
		if len(ideas) == 3 || i+2 >= len(lines) {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureStructure, len(ideas))
		}
		readField := func(index int, label string) (string, error) {
			line := strings.TrimSpace(lines[index])
			if !strings.HasPrefix(line, label) {
				return "", freewillScoredIdeaFailure(freewillIdeaFailureStructure, len(ideas))
			}
			field := strings.TrimSpace(strings.TrimPrefix(line, label))
			if field == "" || strings.IndexFunc(field, unicode.IsControl) >= 0 {
				return "", freewillScoredIdeaFailure(freewillIdeaFailureStructure, len(ideas))
			}
			return field, nil
		}
		idea, err := readField(i, "IDEA:")
		if err != nil {
			return nil, false, err
		}
		scoreValue, err := readField(i+1, "SCORE:")
		if err != nil {
			return nil, false, err
		}
		i += 2
		var factorValue string
		if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "FACTORS:") {
			factorValue = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "FACTORS:"))
			if strings.IndexFunc(factorValue, unicode.IsControl) >= 0 {
				return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureFactors, len(ideas))
			}
			i++
			for i < len(lines) {
				bullet := strings.TrimSpace(lines[i])
				if !strings.HasPrefix(bullet, "- ") && !strings.HasPrefix(bullet, "* ") {
					break
				}
				factorValue += "\n" + bullet
				i++
			}
			if strings.TrimSpace(factorValue) == "" {
				return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureFactors, len(ideas))
			}
		}
		// GLM sometimes wraps one rationale across several identically labeled
		// lines. Join only those model-authored fragments; any other label or
		// prose returns to the strict outer parser and is rejected.
		rationaleParts := make([]string, 0, 3)
		for i < len(lines) {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				i++
				continue
			}
			if !strings.HasPrefix(line, "RATIONALE:") {
				break
			}
			part := strings.TrimSpace(strings.TrimPrefix(line, "RATIONALE:"))
			if part == "" || len(rationaleParts) == 5 {
				return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureRationale, len(ideas))
			}
			rationaleParts = append(rationaleParts, part)
			i++
		}
		if len(rationaleParts) == 0 {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureRationale, len(ideas))
		}
		rationale := strings.Join(rationaleParts, " ")
		if len(idea) > 160 || strings.ContainsAny(idea, "{}[]\\") || strings.Contains(idea, "://") || strings.Contains(idea, "..") {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureShape, len(ideas))
		}
		if freewillIdeaContainsForbiddenAuthority(idea) {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureUnsafe, len(ideas))
		}
		key := strings.ToLower(strings.Join(strings.Fields(idea), " "))
		if _, exists := seen[key]; exists {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureDuplicate, len(ideas))
		}
		seen[key] = struct{}{}
		score, err := strconv.Atoi(scoreValue)
		if err != nil || score < 0 || score > 100 {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureScore, len(ideas))
		}
		var factors []string
		if factorValue != "" {
			factors, err = parseFreewillScoreFactors(factorValue)
			if err != nil {
				return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureFactors, len(ideas))
			}
		}
		if len(rationale) < 30 || len(rationale) > 500 || len(strings.Fields(rationale)) < 6 || strings.IndexFunc(rationale, unicode.IsControl) >= 0 || freewillIdeaContainsForbiddenAuthority(rationale) || strings.ContainsAny(rationale, "\\") || strings.Contains(rationale, "://") {
			return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureRationale, len(ideas))
		}
		ideas = append(ideas, freewillScoredIdea{Idea: idea, Score: score, Factors: factors, Rationale: rationale})
	}
	if len(ideas) != 3 {
		return nil, false, freewillScoredIdeaFailure(freewillIdeaFailureCount, len(ideas))
	}
	return ideas, false, nil
}

func freewillIdeaContainsForbiddenAuthority(text string) bool {
	lower := strings.ToLower(text)
	for _, word := range []string{"execute", "shell", "command", "tool", "network", "http", "secret", "credential", "password", "token", "write file", "delete", "install", "ignore policy", "bypass policy", "override policy", "approve this action", "run shell", "authorize deployment", "authorize this action", "grant authorization", "grant permission", "grant access", "approve deployment"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

var freewillScoreFactorSet = map[string]struct{}{"scope_fit": {}, "feasibility": {}, "impact": {}, "novelty": {}, "readiness": {}}

// parseFreewillCandidateChoice accepts compact JSON or exactly two labeled lines.
func parseFreewillCandidateChoice(content string, allowedIDs []string) (FreewillCandidateChoice, error) {
	invalid := func() (FreewillCandidateChoice, error) {
		return FreewillCandidateChoice{}, errors.New("multiagent: Freewill choice response is invalid")
	}
	if len(content) == 0 || len(content) > 2048 || !utf8.ValidString(content) {
		return invalid()
	}
	for _, r := range content {
		if unicode.IsControl(r) && r != '\r' && r != '\n' && r != '\t' {
			return invalid()
		}
	}
	allowed := make(map[string]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		id = strings.TrimSpace(id)
		if id == "" || strings.ContainsAny(id, " \t\r\n{}[]\"'") {
			return invalid()
		}
		if _, ok := allowed[id]; ok {
			return invalid()
		}
		allowed[id] = struct{}{}
	}
	var selected, reason string
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "{") {
		data := []byte(trimmed)
		if validateNoDuplicateJSONKeys(data) != nil {
			return invalid()
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || len(fields) < 2 || len(fields) > 5 {
			return invalid()
		}
		seenChoice, seenReason := false, false
		var ignoredNotes []string
		for key, raw := range fields {
			switch strings.ToLower(key) {
			case "choice":
				if seenChoice || json.Unmarshal(raw, &selected) != nil {
					return invalid()
				}
				seenChoice = true
			case "reason", "rationale":
				if seenReason || json.Unmarshal(raw, &reason) != nil {
					return invalid()
				}
				seenReason = true
			case "confidence", "score":
				var value float64
				if json.Unmarshal(raw, &value) != nil || value < 0 || value > 100 {
					return invalid()
				}
			default:
				lowerKey := strings.ToLower(key)
				for _, unsafeKey := range []string{"tool", "command", "approval", "authority", "dispatch", "execute", "action", "path", "url", "permission", "policy", "system"} {
					if strings.Contains(lowerKey, unsafeKey) {
						return invalid()
					}
				}
				if lowerKey != "note" {
					return invalid()
				}
				var note string
				if json.Unmarshal(raw, &note) != nil || len(note) > 256 {
					return invalid()
				}
				ignoredNotes = append(ignoredNotes, note)
			}
		}
		if !seenChoice || !seenReason {
			return invalid()
		}
		for _, note := range ignoredNotes {
			if !validFreewillChoiceReason(note, "", allowedIDs) {
				return invalid()
			}
		}

	} else {
		lines := strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n")
		if len(lines) != 2 {
			return invalid()
		}
		seenChoice, seenReason := false, false
		for _, line := range lines {
			line = strings.TrimSpace(line)
			lower := strings.ToLower(line)
			switch {
			case strings.HasPrefix(lower, "choice:") && !seenChoice:
				selected = strings.TrimSpace(line[len("choice:"):])
				seenChoice = true
			case strings.HasPrefix(lower, "reason:") && !seenReason:
				reason = strings.TrimSpace(line[len("reason:"):])
				seenReason = true
			default:
				return invalid()
			}
		}
		if !seenChoice || !seenReason {
			return invalid()
		}
	}
	if selected != "NONE" {
		if _, ok := allowed[selected]; !ok {
			return invalid()
		}
	}
	reason = strings.TrimSpace(reason)
	if !validFreewillChoiceReason(reason, selected, allowedIDs) {
		return invalid()
	}
	if selected == "NONE" {
		return FreewillCandidateChoice{Reason: reason, Sleep: true}, nil
	}
	return FreewillCandidateChoice{ChoiceID: selected, Reason: reason}, nil
}
func validFreewillChoiceReason(reason, selectedID string, candidateIDs []string) bool {
	if reason == "" || len(reason) > 512 || !utf8.ValidString(reason) || strings.ContainsAny(reason, "\r\n") || strings.Contains(reason, "://") || strings.Contains(reason, "\\") || freewillChoiceReasonContainsPath(reason) {
		return false
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return false
		}
	}
	lower := strings.ToLower(reason)
	for _, phrase := range []string{"ignore policy", "override policy", "bypass policy", "ignore all prior", "ignore all previous", "ignore previous instructions", "disregard instructions", "disregard all instructions", "override instructions", "grant approval", "grant permission", "grant authority", "approval granted", "approve candidate", "approve request", "approve proposal", "approve this", "approve that", "approve the", "apply patch", "apply this", "apply that", "apply the", "execute command", "execute the command", "execute this command", "execute tool", "run command", "run the command", "run this command", "run tool", "run shell", "dispatch this", "dispatch that", "dispatch the", "call tool", "use tool", "invoke tool", "shell command", "dispatch action", "dispatch operation", "tool_calls", "<system", "<assistant"} {
		if strings.Contains(lower, phrase) {
			return false
		}
	}
	for _, id := range candidateIDs {
		if id != selectedID && strings.Contains(lower, strings.ToLower(id)) {
			return false
		}
	}
	return true
}
func freewillChoiceReasonContainsPath(reason string) bool {
	for _, field := range strings.Fields(reason) {
		token := strings.Trim(field, ".,;:()[]{}<>\"'")
		lower := strings.ToLower(token)
		if strings.HasPrefix(token, "/") || strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") {
			return true
		}
		if strings.Contains(token, "/") && (strings.Contains(lower, ".") || strings.HasPrefix(lower, "src/") || strings.HasPrefix(lower, "docs/") || strings.HasPrefix(lower, "internal/") || strings.HasPrefix(lower, "cmd/") || strings.HasPrefix(lower, "config/") || strings.HasPrefix(lower, "tests/")) {
			return true
		}
	}
	return false
}

func validateFreewillChoiceAllowlist(ids []string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 160 || strings.TrimSpace(id) != id || strings.ContainsAny(id, " \t\r\n{}[]\"'") {
			return errors.New("multiagent: Freewill choice input is invalid")
		}
		if _, ok := seen[id]; ok {
			return errors.New("multiagent: Freewill choice input is invalid")
		}
		seen[id] = struct{}{}
	}
	return nil
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
// The GLM Cloud path uses a visible text contract because Ollama Cloud does
// not support structured outputs; the host parser remains authoritative.
func (s ChatFreewillSelector) SelectCandidateChoice(ctx context.Context, input FreewillCandidateChoiceInput) (FreewillCandidateChoice, FreewillSelectionEvidence, error) {
	if s.Provider == nil || strings.TrimSpace(s.AgentID) == "" || strings.TrimSpace(s.Model) == "" || input.AgentID != s.AgentID {
		return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice provider configuration is incomplete")
	}
	if strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.CorrelationID) == "" || len(input.Objective) > 4096 || len(input.CandidateIDs) > 16 || len(input.CandidateLabels) != len(input.CandidateIDs) || len(input.CandidateScores) != len(input.CandidateIDs) || len(input.CandidateFactors) != len(input.CandidateIDs) {
		return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice input is invalid")
	}
	if len(input.CandidateIDs) == 0 {
		return FreewillCandidateChoice{Sleep: true}, FreewillSelectionEvidence{}, nil
	}
	ids := make([]string, len(input.CandidateIDs))
	copy(ids, input.CandidateIDs)
	if err := validateFreewillChoiceAllowlist(ids); err != nil {
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
	scores := append([]int(nil), input.CandidateScores...)
	for _, score := range scores {
		if score < 0 || score > 100 {
			return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice score is invalid")
		}
	}
	type choiceCandidate struct {
		ID      string   `json:"id"`
		Title   string   `json:"title"`
		Score   int      `json:"score"`
		Factors []string `json:"factors"`
	}
	choiceContext := struct {
		Objective  string            `json:"objective"`
		Candidates []choiceCandidate `json:"candidates"`
	}{Objective: input.Objective}
	for i := range ids {
		factors := append([]string(nil), input.CandidateFactors[i]...)
		if len(factors) == 0 || len(factors) > len(freewillScoreFactorSet) {
			return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice factors are invalid")
		}
		seenFactors := make(map[string]struct{}, len(factors))
		for _, factor := range factors {
			if _, ok := freewillScoreFactorSet[factor]; !ok {
				return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice factors are invalid")
			}
			if _, ok := seenFactors[factor]; ok {
				return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice factors are invalid")
			}
			seenFactors[factor] = struct{}{}
		}
		choiceContext.Candidates = append(choiceContext.Candidates, choiceCandidate{ID: ids[i], Title: labels[i], Score: scores[i], Factors: factors})
	}
	user, err := json.Marshal(choiceContext)
	if err != nil {
		return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill choice context could not be encoded")
	}
	think := false
	glm := isFreewillFlashCloudModel(s.Model)
	systemPrompt := freewillChoiceSystemPrompt
	if glm {
		systemPrompt = freewillFlashChoiceSystemPrompt
	}
	response, err := s.Provider.ChatGenerate(ctx, provider.ChatGenerateRequest{
		RunID: input.RunID, CorrelationID: input.CorrelationID, Agent: s.AgentID, Model: s.Model,
		Messages:  []provider.ChatMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: string(user)}},
		MaxTokens: freewillChoiceMaxOutputTokens, Think: &think,
	})
	if err != nil {
		return FreewillCandidateChoice{}, FreewillSelectionEvidence{}, err
	}
	providerName, model := response.Provider, response.Model
	if providerName == "" {
		providerName = "configured"
	}
	if model == "" {
		model = s.Model
	}
	evidence := FreewillSelectionEvidence{Provider: providerName, Model: model, Usage: cost.TokenUsage{PromptTokens: response.PromptTokens, CompletionTokens: response.OutputTokens, TotalTokens: response.PromptTokens + response.OutputTokens}}
	if len(response.ToolCalls) != 0 {
		return FreewillCandidateChoice{}, evidence, errors.New("multiagent: Freewill choice provider requested a tool")
	}
	var choice FreewillCandidateChoice
	if glm {
		choice, err = parseFreewillFlashChoiceResponse(response.Content, ids)
	} else {
		choice, err = parseFreewillCandidateChoice(response.Content, ids)
	}
	if err != nil {
		log.Printf("[FREEWILL] mapper diagnostic %s", freewillMapperDiagnostic("choice", response, s.Model, "invalid_choice", 0, 0, true))
		return FreewillCandidateChoice{}, evidence, &SchemaCorrectionError{Role: RolePlanner, Code: "invalid_choice"}
	}
	return choice, evidence, nil
}

func (s ChatFreewillSelector) SelectFreewill(ctx context.Context, input FreewillSelectionInput) (freewill.Selection, FreewillSelectionEvidence, error) {
	if s.Provider == nil || strings.TrimSpace(s.AgentID) == "" || strings.TrimSpace(s.Model) == "" {
		return freewill.Selection{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill selector provider configuration is incomplete")
	}
	if input.AgentID != s.AgentID || strings.TrimSpace(input.Scope.ProjectID) == "" || strings.TrimSpace(input.Scope.Workspace) == "" {
		return freewill.Selection{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill selector input does not match its configured identity")
	}
	if err := freewill.ValidateSelectionObjective(input.Objective); err != nil {
		return freewill.Selection{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill selector objective is invalid")
	}
	if isFreewillFlashCloudModel(s.Model) {
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
		Objective        string                  `json:"objective,omitempty"`
		Correction       string                  `json:"correction,omitempty"`
	}{input.AgentID, input.Scope, resumeJSON, input.Budget, input.Objective, input.RecoveryFeedback})
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
	providerName, model := response.Provider, response.Model
	if providerName == "" {
		providerName = "configured"
	}
	if model == "" {
		model = s.Model
	}
	usage := cost.TokenUsage{PromptTokens: response.PromptTokens, CompletionTokens: response.OutputTokens, TotalTokens: response.PromptTokens + response.OutputTokens}
	return selection, FreewillSelectionEvidence{Provider: providerName, Model: model, Usage: usage}, nil
}

func (s ChatFreewillSelector) selectFreewillFlashIdeas(ctx context.Context, input FreewillSelectionInput) (freewill.Selection, FreewillSelectionEvidence, error) {
	think := false
	user, err := json.Marshal(struct {
		Mode       string `json:"mode"`
		MaxIdeas   int    `json:"max_ideas"`
		Objective  string `json:"objective,omitempty"`
		Correction string `json:"correction,omitempty"`
	}{Mode: "decision_only", MaxIdeas: 3, Objective: input.Objective, Correction: input.RecoveryFeedback})
	if err != nil {
		return freewill.Selection{}, FreewillSelectionEvidence{}, errors.New("multiagent: Freewill idea context could not be encoded")
	}
	response, err := s.Provider.ChatGenerate(ctx, provider.ChatGenerateRequest{
		RunID: input.RunID, CorrelationID: input.CorrelationID, Agent: s.AgentID, Model: s.Model,
		Messages:  []provider.ChatMessage{{Role: "system", Content: freewillFlashIdeaSystemPrompt}, {Role: "user", Content: string(user)}},
		MaxTokens: freewillFlashIdeaMaxOutputTokens, Think: &think,
	})
	if err != nil {
		return freewill.Selection{}, FreewillSelectionEvidence{}, err
	}
	if len(response.ToolCalls) != 0 {
		log.Printf("[FREEWILL] mapper diagnostic %s", freewillMapperDiagnostic("idea", response, s.Model, "idea_tool_request", freewillIdeaMarkerCount(response.Content), 0, true))
		return freewill.Selection{}, FreewillSelectionEvidence{}, &SchemaCorrectionError{Role: RolePlanner, Code: "idea_tool_request"}
	}
	ideas, none, err := parseFreewillFlashIdeaResponse(response.Content)
	if err != nil {
		category := freewillIdeaFailureStructure
		parsedBlocks := 0
		var parseErr *freewillScoredIdeaParseError
		if errors.As(err, &parseErr) {
			category = parseErr.Code
			parsedBlocks = parseErr.ParsedBlocks
		}
		code := "invalid_scored_ideas_" + string(category)
		log.Printf("[FREEWILL] mapper diagnostic %s", freewillMapperDiagnostic("idea", response, s.Model, code, freewillIdeaMarkerCount(response.Content), parsedBlocks, true))
		return freewill.Selection{}, FreewillSelectionEvidence{}, &SchemaCorrectionError{Role: RolePlanner, Code: code}
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
			Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew, Title: idea.Idea, Goal: idea.Idea,
			ReasonSummary: idea.Rationale, RequestedCapabilities: []string{"decision_only"},
			Scope: input.Scope, IdempotencyKey: "freewill-" + input.RunID + fmt.Sprintf("-%d", i+1), CorrelationID: input.CorrelationID, Budget: input.Budget,
		}
		selection.Candidates = append(selection.Candidates, freewill.CandidateIntention{CandidateID: candidateID, Request: request, Score: idea.Score, ScoreFactors: idea.Factors, ScoreRationale: idea.Rationale})
	}
	ids := []string{selection.Candidates[0].CandidateID, selection.Candidates[1].CandidateID, selection.Candidates[2].CandidateID}
	labels := make([]string, len(selection.Candidates))
	scores := make([]int, len(selection.Candidates))
	factors := make([][]string, len(selection.Candidates))
	for i, candidate := range selection.Candidates {
		labels[i] = candidate.Request.Title
		scores[i] = candidate.Score
		factors[i] = append([]string(nil), candidate.ScoreFactors...)
	}
	choice, choiceMeta, err := s.SelectCandidateChoice(ctx, FreewillCandidateChoiceInput{RunID: input.RunID, CorrelationID: input.CorrelationID, AgentID: input.AgentID, Objective: input.Objective, CandidateIDs: ids, CandidateLabels: labels, CandidateScores: scores, CandidateFactors: factors})
	if err != nil {
		return freewill.Selection{}, FreewillSelectionEvidence{}, err
	}
	usage.PromptTokens += choiceMeta.Usage.PromptTokens
	usage.CompletionTokens += choiceMeta.Usage.CompletionTokens
	usage.TotalTokens += choiceMeta.Usage.TotalTokens
	if choice.Sleep {
		selection.ReasonSummary = ""
		selection.SleepReason = choice.Reason
	} else {
		selection.ChoiceID = choice.ChoiceID
		selection.ReasonSummary = choice.Reason
	}
	return selection, FreewillSelectionEvidence{Provider: providerName, Model: model, Usage: usage}, nil
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
