package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emaharmony/prizm/internal/adapter"
)

func TestDiscordAdapter_Name(t *testing.T) {
	d := New("https://discord.com/api/webhooks/test")
	if d.Name() != "discord" {
		t.Errorf("Name() = %q, want discord", d.Name())
	}
}

func TestDiscordAdapter_Version(t *testing.T) {
	d := New("https://discord.com/api/webhooks/test")
	if d.Version() != "1.0.0" {
		t.Errorf("Version() = %q, want 1.0.0", d.Version())
	}
}

func TestDiscordAdapter_Capabilities(t *testing.T) {
	d := New("https://discord.com/api/webhooks/test")
	caps := d.Capabilities()
	if len(caps) != 3 {
		t.Fatalf("Capabilities() returned %d, want 3", len(caps))
	}
	actions := map[string]bool{
		"post_message":     false,
		"post_run_summary": false,
		"post_alert":       false,
	}
	for _, c := range caps {
		actions[c.Action] = true
	}
	for action, found := range actions {
		if !found {
			t.Errorf("missing capability: %s", action)
		}
	}
}

func TestDiscordAdapter_Health_WithWebhook(t *testing.T) {
	d := New("https://discord.com/api/webhooks/test")
	health, err := d.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if !health.Ready {
		t.Error("Health() should report ready when webhook URL is set")
	}
}

func TestDiscordAdapter_Health_NoWebhook(t *testing.T) {
	d := New("")
	health, err := d.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if health.Ready {
		t.Error("Health() should not report ready when webhook URL is empty")
	}
}

func TestDiscordAdapter_PostMessage(t *testing.T) {
	var receivedPayload discordWebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method = %q, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	d := NewWithClient(server.URL, server.Client())
	result, err := d.Execute(context.Background(), "post_message", map[string]any{
		"content": "Hello from Prizm!",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Output["sent"] != true {
		t.Error("post_message should return sent=true")
	}
	if receivedPayload.Content != "Hello from Prizm!" {
		t.Errorf("Content = %q, want Hello from Prizm!", receivedPayload.Content)
	}
}

func TestDiscordAdapter_PostRunSummary(t *testing.T) {
	var receivedPayload discordWebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	d := NewWithClient(server.URL, server.Client())
	result, err := d.Execute(context.Background(), "post_run_summary", map[string]any{
		"run_id":      "run_abc123",
		"status":      "completed",
		"agent":       "lumi",
		"project":     "prizm",
		"task":        "Implement V14e",
		"duration_ms": 15000,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Output["sent"] != true {
		t.Error("post_run_summary should return sent=true")
	}
	if len(receivedPayload.Embeds) != 1 {
		t.Fatalf("Embeds count = %d, want 1", len(receivedPayload.Embeds))
	}
	embed := receivedPayload.Embeds[0]
	if embed.Title != "Run completed" {
		t.Errorf("Title = %q, want Run completed", embed.Title)
	}
	if embed.Color != colorGreen {
		t.Errorf("Color = %d, want green (%d)", embed.Color, colorGreen)
	}
}

func TestDiscordAdapter_PostRunSummary_Failed(t *testing.T) {
	var receivedPayload discordWebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	d := NewWithClient(server.URL, server.Client())
	d.Execute(context.Background(), "post_run_summary", map[string]any{
		"run_id":      "run_fail",
		"status":      "failed",
		"agent":       "lumi",
		"project":     "prizm",
		"task":        "Fix the bug",
		"duration_ms": 5000,
	})

	if len(receivedPayload.Embeds) != 1 {
		t.Fatalf("Embeds count = %d, want 1", len(receivedPayload.Embeds))
	}
	if receivedPayload.Embeds[0].Color != colorRed {
		t.Errorf("Color = %d, want red (%d) for failed run", receivedPayload.Embeds[0].Color, colorRed)
	}
}

func TestDiscordAdapter_PostAlert(t *testing.T) {
	var receivedPayload discordWebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	d := NewWithClient(server.URL, server.Client())
	result, err := d.Execute(context.Background(), "post_alert", map[string]any{
		"run_id":   "run_alert",
		"message":  "Run exceeded token limit",
		"severity": "error",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Output["sent"] != true {
		t.Error("post_alert should return sent=true")
	}
	if len(receivedPayload.Embeds) != 1 {
		t.Fatalf("Embeds count = %d, want 1", len(receivedPayload.Embeds))
	}
}

func TestDiscordAdapter_UnknownAction(t *testing.T) {
	d := New("https://discord.com/api/webhooks/test")
	_, err := d.Execute(context.Background(), "unknown_action", nil)
	if err == nil {
		t.Error("unknown action should return error")
	}
}

func TestDiscordAdapter_PostMessage_NoContent(t *testing.T) {
	d := New("https://discord.com/api/webhooks/test")
	_, err := d.Execute(context.Background(), "post_message", map[string]any{})
	if err == nil {
		t.Error("expected error for missing content")
	}
}

func TestDiscordAdapter_AdapterInterface(t *testing.T) {
	// Verify DiscordAdapter implements the adapter.Adapter interface
	var _ adapter.Adapter = New("https://discord.com/api/webhooks/test")
}

func TestDiscordAdapter_ServerError(t *testing.T) {
	var receivedPayload discordWebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	d := NewWithClient(server.URL, server.Client())
	_, err := d.Execute(context.Background(), "post_message", map[string]any{
		"content": "test",
	})
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestDiscordAdapter_BadGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	d := NewWithClient(server.URL, server.Client())
	_, err := d.Execute(context.Background(), "post_message", map[string]any{
		"content": "test",
	})
	if err == nil {
		t.Fatal("expected error for 502")
	}
}

func TestDiscordAdapter_EmptyWebhookURL(t *testing.T) {
	d := New("")
	_, err := d.Execute(context.Background(), "post_message", map[string]any{
		"content": "test",
	})
	if err == nil {
		t.Fatal("expected error for empty webhook URL")
	}
}
