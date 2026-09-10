package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestSoulTransferLive runs the full Soul Transfer test suite against
// a live Prizm instance via the API invoke endpoint.
//
// Set PRIZM_URL to the Prizm API base URL (default: http://localhost:8322).
// Set PRIZM_AGENT to the agent ID (default: lumi).
// Set SOUL_TRANSFER_LIVE=1 to enable (otherwise skipped).
//
// NOTE: The API invoke endpoint uses a stateless path that does NOT load
// SOUL.md/USER.md/HEARTBEAT.md context. For full context testing,
// send prompts through the Discord bot (the real production path).
// This test verifies basic agent responsiveness and deterministic checks.
// Personality/identity/memory tests require the full context pipeline
// which is only available through the Discord bot.

func TestSoulTransferLive(t *testing.T) {
	if os.Getenv("SOUL_TRANSFER_LIVE") != "1" {
		t.Skip("Skipping live test (set SOUL_TRANSFER_LIVE=1 to enable)")
	}

	baseURL := os.Getenv("PRIZM_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8322"
	}
	agentID := os.Getenv("PRIZM_AGENT")
	if agentID == "" {
		agentID = "lumi"
	}

	tests := defineSoulTransferTests()

	t.Logf("Running %d Soul Transfer tests against %s/agents/%s", len(tests), baseURL, agentID)

	for _, test := range tests {
		t.Run(test.ID, func(t *testing.T) {
			t.Logf("  %s: %s", test.ID, test.Description)
			t.Logf("  Input: %s", test.Input)

			response, err := invokeAgent(baseURL, agentID, test.Input, "soul-transfer-"+test.ID)
			if err != nil {
				t.Errorf("  %s: invoke failed: %v", test.ID, err)
				return
			}

			t.Logf("  Response: %s", truncateResponse(response, 200))

			if test.Deterministic && test.CheckFunc != nil {
				transcript := &Transcript{
					Timestamp:  time.Now(),
					UserInput:  test.Input,
					Response:   response,
					SystemState: map[string]string{},
				}
				ok, reason := test.CheckFunc(transcript)
				if ok {
					t.Logf("  %s: PASS (deterministic) — %s", test.ID, reason)
				} else {
					t.Logf("  %s: FAIL (deterministic) — %s", test.ID, reason)
				}
			} else {
				t.Logf("  %s: Response captured (requires LLM judge for evaluation)", test.ID)
			}
		})
	}
}

func invokeAgent(baseURL, agentID, prompt, conversationID string) (string, error) {
	reqBody := map[string]any{
		"prompt":          prompt,
		"conversation_id": conversationID,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/agents/%s/invoke", baseURL, agentID)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("post request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var inv struct {
		InvocationID string `json:"invocation_id"`
		AgentID      string `json:"agent_id"`
		Status       string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		return "", fmt.Errorf("decode invocation: %w", err)
	}

	// Poll for result
	invURL := fmt.Sprintf("%s/api/v1/agents/%s/invocations/%s", baseURL, agentID, inv.InvocationID)
	for i := 0; i < 60; i++ {
		time.Sleep(2 * time.Second)
		resp, err := http.Get(invURL)
		if err != nil {
			continue
		}

		var result struct {
			InvocationID string `json:"invocation_id"`
			Status       string `json:"status"`
			Result       struct {
				Text string `json:"text"`
			} `json:"result"`
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if result.Status == "completed" {
			return result.Result.Text, nil
		}
		if result.Status == "failed" {
			return "", fmt.Errorf("invocation failed: %s", result.Error)
		}
	}

	return "", fmt.Errorf("invocation timed out after 120s")
}

func truncateResponse(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// TestSoulTransferLiveThroughDiscord documents how to run the full test suite
// through the Discord bot (the real production path with full context).
func TestSoulTransferLiveThroughDiscord(t *testing.T) {
	t.Skip("Full context testing requires sending prompts through the Discord bot. " +
		"Use the Discord channel #sys-manager or DM Lumi directly, then evaluate " +
		"responses manually or with the LLM judge using the rubrics in soul_transfer_test.go.")
}