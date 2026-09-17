// Package tts provides text-to-speech integration with Voicebox.
//
// Voicebox is a local-first AI voice studio running at http://localhost:17493.
// This package calls the Voicebox REST API to generate speech from text
// and returns the audio file path for Discord delivery.
//
// Configuration is in prizm.yaml under the `tts` key. Per-channel
// overrides are in the channel_roles section with a `tts: true/false` field.
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Config holds TTS settings from prizm.yaml.
type Config struct {
	Enabled     bool   `yaml:"enabled"`
	ProfileID   string `yaml:"profile_id"`
	Engine      string `yaml:"engine"`       // kokoro, qwen, chatterbox, etc.
	VoiceboxURL string `yaml:"voicebox_url"` // default: http://localhost:17493
	MaxChars    int    `yaml:"max_chars"`    // don't voice messages longer than this
}

// DefaultConfig returns conservative defaults — TTS off by default.
func DefaultConfig() Config {
	return Config{
		Enabled:     false,
		Engine:      "kokoro",
		VoiceboxURL: "http://localhost:17493",
		MaxChars:    500,
	}
}

// ShouldVoice determines whether a message should be voiced based on
// global config, channel override, and message length.
func ShouldVoice(cfg Config, channelTTS bool, messageLength int) bool {
	if !cfg.Enabled {
		return false
	}
	if !channelTTS {
		return false
	}
	if cfg.MaxChars > 0 && messageLength > cfg.MaxChars {
		return false
	}
	return true
}

// GenerateRequest is the Voicebox /generate payload.
type GenerateRequest struct {
	ProfileID string `json:"profile_id"`
	Text      string `json:"text"`
	Engine    string `json:"engine"`
	Language  string `json:"language"`
}

// GenerateResponse is the Voicebox /generate response.
type GenerateResponse struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	AudioPath string  `json:"audio_path"`
	Duration  float64 `json:"duration"`
	Error     string  `json:"error"`
}

// Client calls the Voicebox REST API.
type Client struct {
	URL  string
	HTTP *http.Client
}

// NewClient creates a Voicebox client.
func NewClient(url string) *Client {
	if url == "" {
		url = "http://localhost:17493"
	}
	return &Client{
		URL:  url,
		HTTP: &http.Client{Timeout: 120 * time.Second},
	}
}

// Generate calls Voicebox to synthesize speech from text.
// Returns the generation ID. Caller should then call GetAudio to
// download the WAV file.
func (c *Client) Generate(ctx context.Context, profileID, text, engine string) (string, error) {
	if profileID == "" {
		return "", fmt.Errorf("tts: profile_id is required")
	}
	if text == "" {
		return "", fmt.Errorf("tts: text is required")
	}
	if engine == "" {
		engine = "kokoro"
	}

	body, _ := json.Marshal(GenerateRequest{
		ProfileID: profileID,
		Text:      text,
		Engine:    engine,
		Language:  "en",
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("tts: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("tts: voicebox request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("tts: voicebox error %d: %s", resp.StatusCode, string(b))
	}

	var genResp GenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&genResp); err != nil {
		return "", fmt.Errorf("tts: parse response: %w", err)
	}

	return genResp.ID, nil
}

// WaitForGeneration polls the generation status until completed or timeout.
func (c *Client) WaitForGeneration(ctx context.Context, genID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/generate/"+genID+"/status", nil)
		if err != nil {
			return fmt.Errorf("tts: build request: %w", err)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("tts: generation wait cancelled: %w", ctx.Err())
			}
			time.Sleep(2 * time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		status := string(body)
		if bytes.Contains(body, []byte("completed")) {
			return nil
		}
		if bytes.Contains(body, []byte("error")) {
			return fmt.Errorf("tts: generation failed: %s", status)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("tts: generation wait cancelled: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("tts: generation timeout after %v", timeout)
}

// GetAudio downloads the generated audio as a WAV file.
func (c *Client) GetAudio(ctx context.Context, genID string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/audio/"+genID, nil)
	if err != nil {
		return nil, fmt.Errorf("tts: build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts: download audio: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tts: audio not ready (HTTP %d)", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tts: read audio: %w", err)
	}

	if len(data) < 100 {
		return nil, fmt.Errorf("tts: audio too small (%d bytes)", len(data))
	}

	return data, nil
}

// GenerateAndWait is a convenience function that generates, waits,
// and downloads the audio in one call.
func (c *Client) GenerateAndWait(ctx context.Context, profileID, text, engine string) ([]byte, error) {
	genID, err := c.Generate(ctx, profileID, text, engine)
	if err != nil {
		return nil, err
	}

	if err := c.WaitForGeneration(ctx, genID, 60*time.Second); err != nil {
		return nil, err
	}

	return c.GetAudio(ctx, genID)
}
