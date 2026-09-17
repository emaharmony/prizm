package remembrance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewClientUsesDefaultTimeout(t *testing.T) {
	client := NewClient("http://localhost:8788")
	if client.HTTPClient.Timeout != DefaultTimeout {
		t.Errorf("NewClient timeout = %v, want %v", client.HTTPClient.Timeout, DefaultTimeout)
	}
}

func TestNewClientWithTimeout(t *testing.T) {
	client := NewClientWithTimeout("http://localhost:8788/", 90*time.Second)
	if client.BaseURL != "http://localhost:8788" {
		t.Errorf("BaseURL = %q, want %q", client.BaseURL, "http://localhost:8788")
	}
	if client.HTTPClient.Timeout != 90*time.Second {
		t.Errorf("NewClientWithTimeout timeout = %v, want 90s", client.HTTPClient.Timeout)
	}
}

func TestBuildContextWithOptionsSendsOwnerAndLocalHints(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/context/build" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"project_id":"prizm","owner_id":"owner-1","agent_id":"lumi","task":"hello","selected_memories":[],"context_markdown":"","token_count":0}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.BuildContextWithOptions(BuildContextRequest{
		OwnerID:            "owner-1",
		AgentID:            "lumi",
		ProjectID:          "prizm",
		Task:               "hello",
		LocalRecentSummary: "recent exact context",
		ChannelContext:     "discord channel",
		MaxTokens:          123,
	})
	if err != nil {
		t.Fatalf("BuildContextWithOptions: %v", err)
	}
	if resp.OwnerID != "owner-1" {
		t.Fatalf("owner_id response = %q", resp.OwnerID)
	}
	for key, want := range map[string]any{
		"owner_id":             "owner-1",
		"agent_id":             "lumi",
		"project_id":           "prizm",
		"task":                 "hello",
		"local_recent_summary": "recent exact context",
		"channel_context":      "discord channel",
	} {
		if got[key] != want {
			t.Fatalf("%s = %#v, want %#v", key, got[key], want)
		}
	}
	if got["max_tokens"] != float64(123) {
		t.Fatalf("max_tokens = %#v", got["max_tokens"])
	}
}

func TestCaptureWithMetadataSendsSourceFields(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/memory/ingest" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"mem-1","decision":"PERSIST"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.CaptureWithMetadata(CaptureRequest{
		OwnerID:         "owner-1",
		AgentID:         "lumi",
		SessionID:       "sess-1",
		MessageIDs:      []string{"msg-1"},
		Scope:           "user",
		Category:        "conversation_summary",
		Summary:         "summary",
		SourceRef:       "run-1",
		ImportanceScore: 0.8,
		ProjectID:       "prizm",
		Title:           "title",
		Content:         "content",
		SourceType:      "agent",
		SourceAgent:     "prizm:lumi",
	})
	if err != nil {
		t.Fatalf("CaptureWithMetadata: %v", err)
	}
	if got["owner_id"] != "owner-1" || got["session_id"] != "sess-1" || got["source_ref"] != "run-1" {
		t.Fatalf("metadata not sent: %#v", got)
	}
	if got["importance_score"] != 0.8 {
		t.Fatalf("importance_score = %#v", got["importance_score"])
	}
}
