package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNoModelAvailable is returned when all models in the fallback chain fail.
var ErrNoModelAvailable = errors.New("no model available in fallback chain")

// GateExtractor decides whether a conversation turn is worth remembering
// and extracts structured memory from it using local Ollama models.
type GateExtractor struct {
	Models        []string // Ordered fallback chain: e.g. ["nemotron-3-nano:4b", "qwen3.5:4b"]
	OllamaURL     string   // Base URL for Ollama API (default http://localhost:11434)
	FallbackModel string   // Session model name (not called from here — just metadata)
	HTTPClient    *http.Client
}

// NewGateExtractor creates a GateExtractor with the given model fallback chain.
func NewGateExtractor(models []string, ollamaURL string, fallbackModel string) *GateExtractor {
	if strings.TrimSpace(ollamaURL) == "" {
		ollamaURL = "http://localhost:11434"
	}
	return &GateExtractor{
		Models:        models,
		OllamaURL:     strings.TrimRight(ollamaURL, "/"),
		FallbackModel: fallbackModel,
		HTTPClient:     &http.Client{Timeout: 30 * time.Second},
	}
}

// GateResult is the output of the gate decision.
type GateResult struct {
	ShouldRemember bool
	Reasoning      string
}

// Gate decides whether a conversation turn is worth persisting as a memory.
// It calls local Ollama models in fallback chain order.
func (g *GateExtractor) Gate(ctx context.Context, conversationTurn string) (*GateResult, error) {
	prompt := fmt.Sprintf(gatePrompt, conversationTurn)
	resp, err := g.callModel(ctx, prompt)
	if err != nil {
		return nil, err
	}

	lines := strings.SplitN(strings.TrimSpace(resp), "\n", 2)
	firstLine := strings.ToUpper(strings.TrimSpace(lines[0]))
	shouldRemember := strings.Contains(firstLine, "YES")

	reasoning := ""
	if len(lines) > 1 {
		reasoning = strings.TrimSpace(lines[1])
	}

	return &GateResult{
		ShouldRemember: shouldRemember,
		Reasoning:      reasoning,
	}, nil
}

// ExtractResult is the structured output of memory extraction.
type ExtractResult struct {
	Category  string   // user, feedback, project, reference, emotional
	Tier      string   // ephemeral, active, persist
	Summary   string   // one-line summary
	KeyTopics []string // comma-separated topics
	Content   string   // detailed content
}

// Extract produces a structured memory from a conversation turn.
func (g *GateExtractor) Extract(ctx context.Context, conversationTurn string) (*ExtractResult, error) {
	prompt := fmt.Sprintf(extractPrompt, conversationTurn)
	resp, err := g.callModel(ctx, prompt)
	if err != nil {
		return nil, err
	}

	result := parseExtractResponse(resp)
	return result, nil
}

// callModel tries each model in the fallback chain until one succeeds.
func (g *GateExtractor) callModel(ctx context.Context, prompt string) (string, error) {
	var lastErr error
	for _, model := range g.Models {
		resp, err := g.callOllama(ctx, model, prompt)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("%w: last error: %v", ErrNoModelAvailable, lastErr)
}

// callOllama makes a single request to the Ollama /api/generate endpoint.
func (g *GateExtractor) callOllama(ctx context.Context, model, prompt string) (string, error) {
	body := ollamaGenerateRequest{
		Model:  model,
		Prompt: prompt,
		Stream: false,
		Options: ollamaOptions{
			Temperature: 0.1,
			NumPredict:  256,
		},
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.OllamaURL+"/api/generate", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call model %s: %w", model, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("model %s returned status %d: %s", model, resp.StatusCode, string(body))
	}

	var result ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response from %s: %w", model, err)
	}

	return strings.TrimSpace(result.Response), nil
}

// parseExtractResponse parses the structured extraction output.
func parseExtractResponse(raw string) *ExtractResult {
	result := &ExtractResult{
		Category: "reference",
		Tier:     "active",
	}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := afterPrefix(line, "CATEGORY:"); ok {
			result.Category = normalizeCategory(after)
		} else if after, ok := afterPrefix(line, "TIER:"); ok {
			result.Tier = normalizeTier(after)
		} else if after, ok := afterPrefix(line, "SUMMARY:"); ok {
			result.Summary = strings.TrimSpace(after)
		} else if after, ok := afterPrefix(line, "TOPICS:"); ok {
			topics := strings.Split(after, ",")
			for i, t := range topics {
				topics[i] = strings.TrimSpace(t)
			}
			result.KeyTopics = topics
		} else if after, ok := afterPrefix(line, "CONTENT:"); ok {
			result.Content = strings.TrimSpace(after)
		}
	}

	// If no CONTENT line, use everything after TOPICS as content
	if result.Content == "" && result.Summary != "" {
		result.Content = result.Summary
	}

	return result
}

func afterPrefix(line, prefix string) (string, bool) {
	upper := strings.ToUpper(line)
	if strings.HasPrefix(upper, prefix) {
		return strings.TrimSpace(line[len(prefix):]), true
	}
	return "", false
}

func normalizeCategory(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// V76: 5-type taxonomy (user/feedback/project/reference/emotional)
	// Order matters: check "prefer" before "reference" (preference contains "reference")
	switch {
	case strings.Contains(s, "user"):
		return "user"
	case strings.Contains(s, "feedback"):
		return "feedback"
	case strings.Contains(s, "project"):
		return "project"
	case strings.Contains(s, "emotional"):
		return "emotional"
	case strings.Contains(s, "prefer"):
		return "user"
	case strings.Contains(s, "decision"):
		return "project"
	case strings.Contains(s, "observ"):
		return "feedback"
	case strings.Contains(s, "reference"):
		return "reference"
	case strings.Contains(s, "fact"):
		return "reference"
	default:
		return "reference"
	}
}

func normalizeTier(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(s, "ephemeral") || strings.Contains(s, "cold"):
		return "ephemeral"
	case strings.Contains(s, "persist") || strings.Contains(s, "stable") || strings.Contains(s, "permanent"):
		return "persist"
	default:
		return "active"
	}
}

// --- Ollama API types (local, no external dep) ---

type ollamaGenerateRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Stream  bool           `json:"stream"`
	Options ollamaOptions  `json:"options,omitempty"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Model    string `json:"model"`
	Done     bool   `json:"done"`
}

// --- Prompts ---

const gatePrompt = `Is this conversation turn worth remembering as a long-term memory? Consider whether it contains decisions, preferences, facts, or important context that would be useful later.

Reply YES or NO on the first line, then explain why on the second line.

Conversation turn:
%s`

const extractPrompt = `Extract key information from this conversation as a structured memory. Reply in this exact format:

CATEGORY: <user/feedback/project/reference>
TIER: <ephemeral/active/persist>
SUMMARY: <one line summary>
TOPICS: <comma separated topics>
CONTENT: <detailed content>

Category types:
- user: user preferences, traits, communication style, habits, skill level
- feedback: corrections, lessons learned, things to avoid, improvements noted
- project: project state, architecture decisions, task progress, blockers
- reference: facts, techniques, environment info, tool quirks, workarounds
- emotional: qualitative state, enjoyment level, what felt good/off, identity reflections

Do NOT save:
- Trivial/obvious info ("user asked about Python")
- Easily re-discovered facts ("Python 3.12 supports f-string nesting")
- Raw data dumps (large code blocks, log files, data tables)
- Session-specific ephemera (temporary file paths, one-off debugging context)

Note: memory may drift over time — prefer concrete, actionable entries over vague observations.

Conversation turn:
%s`