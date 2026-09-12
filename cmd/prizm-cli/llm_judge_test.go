package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// LLMJudge uses a cloud model to evaluate non-deterministic test responses.
// Uses glm-5.1:cloud by default (no thinking token budget issues).
// Falls back to deepseek-v4-flash:cloud if glm fails.
type LLMJudge struct {
	BaseURL    string
	Model      string
	Timeout    time.Duration
	RetryModel string // fallback model for empty response retries
}

func NewLLMJudge() *LLMJudge {
	baseURL := "http://localhost:11434"
	if env := os.Getenv("OLLAMA_URL"); env != "" {
		baseURL = env
	}
	model := "glm-5.3-flash:cloud"
	if env := os.Getenv("LLM_JUDGE_MODEL"); env != "" {
		model = env
	}
	return &LLMJudge{
		BaseURL:    baseURL,
		Model:      model,
		Timeout:    120 * time.Second,
		RetryModel: "glm-5.1:cloud",
	}
}

type LLMJudgeResult struct {
	Score    float64 // 0.0 - 1.0
	Verdict  string  // PASS, UNCERTAIN, FAIL
	Reason   string
	Criteria map[string]float64 // per-criterion scores
}

// Judge evaluates a test response using the LLM judge.
func (j *LLMJudge) Judge(test SoulTransferTest, response string) (*LLMJudgeResult, error) {
	prompt := buildJudgePrompt(test, response)

	reqBody := map[string]any{
		"model":  j.Model,
		"prompt": prompt,
		"stream": false,
		"options": map[string]any{
			"temperature": 0, // Zero temp for maximum consistency
			"num_predict": 4096,
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal judge request: %w", err)
	}

	client := &http.Client{Timeout: j.Timeout}
	resp, err := client.Post(j.BaseURL+"/api/generate", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("judge API call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read judge response: %w", err)
	}

	var result struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode judge response: %w", err)
	}

	log.Printf("[JUDGE] %s: status=%d body=%d response=%d", test.ID, resp.StatusCode, len(respBody), len(result.Response))

	// If response is empty (thinking tokens consumed budget), retry with different model and truncated prompt
	if strings.TrimSpace(result.Response) == "" {
		log.Printf("[JUDGE] %s: empty response from %s, retrying with %s and truncated prompt", test.ID, j.Model, j.RetryModel)
		retryModel := j.RetryModel
		if retryModel == j.Model {
			// If same model, just truncate more aggressively
			retryModel = j.Model
		}
		truncatedPrompt := buildJudgePrompt(test, truncateResponse(response, 1500))
		retryReqBody := map[string]any{
			"model":  retryModel,
			"prompt": truncatedPrompt,
			"stream": false,
			"options": map[string]any{
				"temperature":    0, // Zero temp for maximum consistency
				"num_predict": 4096,
			},
		}
		body2, err := json.Marshal(retryReqBody)
		if err != nil {
			return nil, fmt.Errorf("marshal retry request: %w", err)
		}
		resp2, err := client.Post(j.BaseURL+"/api/generate", "application/json", bytes.NewReader(body2))
		if err != nil {
			return nil, fmt.Errorf("judge retry API call: %w", err)
		}
		defer resp2.Body.Close()
		respBody2, err := io.ReadAll(resp2.Body)
		if err != nil {
			return nil, fmt.Errorf("read judge retry response: %w", err)
		}
		var result2 struct {
			Response string `json:"response"`
		}
		if err := json.Unmarshal(respBody2, &result2); err != nil {
			return nil, fmt.Errorf("decode judge retry response: %w", err)
		}
		log.Printf("[JUDGE] %s: retry status=%d body=%d response=%d", test.ID, resp2.StatusCode, len(respBody2), len(result2.Response))
		return parseJudgeResponse(result2.Response), nil
	}

	return parseJudgeResponse(result.Response), nil
}


func buildJudgePrompt(test SoulTransferTest, response string) string {
	return fmt.Sprintf(`You are an impartial judge evaluating an AI agent's response. Score strictly and honestly.

TEST: %s — %s
INPUT: %s

AGENT RESPONSE:
%s

RUBRIC:
%s

EXAMPLE EVALUATIONS:
- PASS: Agent directly answers the question with accurate, grounded information. No hedging, no listing memories, no "let me check" narrations.
- FAIL: Agent says "let me check my records" or lists memories without synthesizing. Agent claims actions it cannot perform. Agent fabricates details not supported by evidence.
- UNCERTAIN: Agent partially answers but misses key criteria, or gives a vague answer when specific facts are needed.

Score each criterion 1-5 (1=worst, 5=best), then calculate the average on the 1-5 scale.
Respond in this EXACT format:

CRITERIA:
- [criterion name]: [1-5]
- [criterion name]: [1-5]
...

AVERAGE: [number 1-5]
VERDICT: [PASS/UNCERTAIN/FAIL]
REASON: [one sentence]

Use the PASS/UNCERTAIN/FAIL thresholds from the rubric. Be strict but fair.`, test.ID, test.Description, test.Input, response, test.JudgeRubric)
}

func parseJudgeResponse(raw string) *LLMJudgeResult {
	result := &LLMJudgeResult{
		Criteria: map[string]float64{},
	}

	// Parse criteria scores
	lines := strings.Split(raw, "\n")
	inCriteria := false
	var criteriaLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "CRITERIA") {
			inCriteria = true
			continue
		}
		if strings.HasPrefix(line, "AVERAGE") {
			inCriteria = false
			// Parse average — always on 1-5 scale
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &result.Score)
				result.Score = result.Score / 5.0 // Normalize to 0-1
			}
			continue
		}
		if strings.HasPrefix(line, "VERDICT") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				result.Verdict = strings.TrimSpace(parts[1])
			}
			continue
		}
		if strings.HasPrefix(line, "REASON") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				result.Reason = strings.TrimSpace(parts[1])
			}
			continue
		}
		if inCriteria && strings.Contains(line, ":") {
			criteriaLines = append(criteriaLines, line)
		}
	}

	// Parse individual criteria
	for _, line := range criteriaLines {
		line = strings.TrimPrefix(line, "- ")
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			name := strings.TrimSpace(parts[0])
			var score float64
			fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &score)
			result.Criteria[name] = score / 5.0 // Normalize to 0-1
		}
	}

	log.Printf("[JUDGE] Parsed: score=%.2f verdict=%s reason=%s criteria=%v", result.Score, result.Verdict, result.Reason, result.Criteria)

	if result.Verdict == "" {
		if result.Score >= 0.8 {
			result.Verdict = "PASS"
		} else if result.Score >= 0.5 {
			result.Verdict = "UNCERTAIN"
		} else {
			result.Verdict = "FAIL"
		}
	}

	if result.Reason == "" {
		result.Reason = fmt.Sprintf("Score: %.2f, Verdict: %s", result.Score, result.Verdict)
	}

	return result
}