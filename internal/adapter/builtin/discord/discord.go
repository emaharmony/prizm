// Package discord implements the Discord adapter for Prizm (V14e).
//
// The Discord adapter is Prizm's second real adapter. It posts run results,
// alerts, and summaries to Discord channels via webhooks.
//
// When a `prizm.run.completed` or `prizm.run.failed` event fires, the Discord
// adapter posts a rich embed with the run summary, status, and duration.
// This IS the thesis in action: state changes trigger actions automatically.
//
// Webhook-based, not bot-based. No gateway intent needed. Just a webhook URL.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/emaharmony/prizm/internal/adapter"
)

// DiscordAdapter posts run results to Discord channels via webhooks.
type DiscordAdapter struct {
	webhookURL string
	httpClient *http.Client
}

// New creates a new DiscordAdapter with the given webhook URL.
func New(webhookURL string) *DiscordAdapter {
	return &DiscordAdapter{
		webhookURL: webhookURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// NewWithClient creates a DiscordAdapter with a custom HTTP client (for testing).
func NewWithClient(webhookURL string, client *http.Client) *DiscordAdapter {
	return &DiscordAdapter{
		webhookURL: webhookURL,
		httpClient: client,
	}
}

// Name returns the adapter name.
func (d *DiscordAdapter) Name() string {
	return "discord"
}

// Version returns the adapter version.
func (d *DiscordAdapter) Version() string {
	return "1.0.0"
}

// Capabilities returns what this adapter can do.
func (d *DiscordAdapter) Capabilities() []adapter.Capability {
	return []adapter.Capability{
		{Action: "post_message", Description: "Post a plain text message to a Discord channel"},
		{Action: "post_run_summary", Description: "Post a rich embed with run summary, status, and duration"},
		{Action: "post_alert", Description: "Post an alert embed for failed or error runs"},
	}
}

// Execute runs a Discord adapter action.
// Supported actions: post_message, post_run_summary, post_alert.
func (d *DiscordAdapter) Execute(ctx context.Context, action string, input map[string]any) (*adapter.Result, error) {
	switch action {
	case "post_message":
		return d.postMessage(input)
	case "post_run_summary":
		return d.postRunSummary(input)
	case "post_alert":
		return d.postAlert(input)
	default:
		return nil, fmt.Errorf("discord: unknown action %q", action)
	}
}

// Health checks if the adapter is ready.
// The Discord adapter is always ready — it just needs a webhook URL.
func (d *DiscordAdapter) Health(ctx context.Context) (*adapter.HealthResult, error) {
	if d.webhookURL == "" {
		return &adapter.HealthResult{
			Ready:   false,
			Message: "discord: webhook URL not configured",
		}, nil
	}
	return &adapter.HealthResult{
		Ready:   true,
		Message: "discord adapter is healthy",
	}, nil
}

// discordWebhookPayload represents a Discord webhook message.
type discordWebhookPayload struct {
	Content string         `json:"content,omitempty"`
	Embeds  []discordEmbed `json:"embeds,omitempty"`
}

// discordEmbed represents a Discord rich embed.
type discordEmbed struct {
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color,omitempty"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
	Footer      *discordEmbedFooter `json:"footer,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
}

// discordEmbedField represents a field in a Discord embed.
type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// discordEmbedFooter represents a footer in a Discord embed.
type discordEmbedFooter struct {
	Text string `json:"text"`
}

const (
	colorGreen  = 0x00FF00 // success
	colorRed    = 0xFF0000 // failure
	colorYellow = 0xFFFF00 // warning
	colorBlue   = 0x0000FF // info
)

// postMessage posts a plain text message to Discord.
func (d *DiscordAdapter) postMessage(input map[string]any) (*adapter.Result, error) {
	content, _ := input["content"].(string)
	if content == "" {
		return nil, fmt.Errorf("discord: content is required for post_message")
	}

	payload := discordWebhookPayload{Content: content}
	return d.sendWebhook(payload)
}

// postRunSummary posts a rich embed with run summary information.
func (d *DiscordAdapter) postRunSummary(input map[string]any) (*adapter.Result, error) {
	runID, _ := input["run_id"].(string)
	status, _ := input["status"].(string)
	agent, _ := input["agent"].(string)
	project, _ := input["project"].(string)
	task, _ := input["task"].(string)
	duration, _ := input["duration_ms"].(float64) // JSON numbers are float64

	// Determine color based on status
	color := colorBlue
	switch status {
	case "completed":
		color = colorGreen
	case "failed":
		color = colorRed
	case "requires_approval":
		color = colorYellow
	}

	// Build duration string
	durationStr := fmt.Sprintf("%.1fs", duration/1000)

	embed := discordEmbed{
		Title:       fmt.Sprintf("Run %s", status),
		Description: task,
		Color:       color,
		Fields: []discordEmbedField{
			{Name: "Run ID", Value: runID, Inline: true},
			{Name: "Agent", Value: agent, Inline: true},
			{Name: "Project", Value: project, Inline: true},
			{Name: "Duration", Value: durationStr, Inline: true},
		},
		Footer:    &discordEmbedFooter{Text: "Prizm"},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	payload := discordWebhookPayload{Embeds: []discordEmbed{embed}}
	return d.sendWebhook(payload)
}

// postAlert posts an alert embed for failed runs.
func (d *DiscordAdapter) postAlert(input map[string]any) (*adapter.Result, error) {
	runID, _ := input["run_id"].(string)
	message, _ := input["message"].(string)
	severity, _ := input["severity"].(string)
	if severity == "" {
		severity = "error"
	}

	color := colorRed
	if severity == "warning" {
		color = colorYellow
	}

	embed := discordEmbed{
		Title:       fmt.Sprintf("🚨 Alert: %s", severity),
		Description: message,
		Color:       color,
		Fields: []discordEmbedField{
			{Name: "Run ID", Value: runID, Inline: true},
			{Name: "Severity", Value: severity, Inline: true},
		},
		Footer:    &discordEmbedFooter{Text: "Prizm Alert"},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	payload := discordWebhookPayload{Embeds: []discordEmbed{embed}}
	return d.sendWebhook(payload)
}

// sendWebhook sends a payload to the Discord webhook URL.
func (d *DiscordAdapter) sendWebhook(payload discordWebhookPayload) (*adapter.Result, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("discord: marshal payload: %w", err)
	}

	resp, err := d.httpClient.Post(d.webhookURL, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("discord: send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discord: webhook returned %d", resp.StatusCode)
	}

	return &adapter.Result{
		Output: map[string]any{
			"sent":   true,
			"status": resp.Status,
			"embeds": len(payload.Embeds),
		},
	}, nil
}
