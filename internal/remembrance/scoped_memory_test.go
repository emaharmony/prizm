package remembrance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/memory"
)

func TestScopedMemoryBackendCaptureHonorsCancellation(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer server.Close()
	backend := ScopedMemoryBackend{Client: NewClient(server.URL)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := backend.CaptureIdempotent(ctx, memory.Memory{ID: "local", Content: "x", Summary: "x", ProjectID: "p", TaskID: "t"}, "key")
		errCh <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("capture did not honor cancellation")
	}
}

func TestScopedMemoryBackendCaptureIdempotentSendsScopeAndKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/memory/ingest" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["idempotency_key"] != "sync-key" || body["owner_id"] != "user-a" || body["project_id"] != "project-a" || body["task_id"] != "task-a" || body["agent_id"] != "agent-a" {
			t.Fatalf("bad scope/key: %#v", body)
		}
		_, _ = w.Write([]byte(`{"id":"remote-original"}`))
	}))
	defer server.Close()
	backend := ScopedMemoryBackend{Client: NewClient(server.URL)}
	mem := memory.Memory{ID: "local-id", Content: "durable", Summary: "durable", UserID: "user-a", ProjectID: "project-a", TaskID: "task-a", AgentID: "agent-a"}
	id, err := backend.CaptureIdempotent(context.Background(), mem, "sync-key")
	if err != nil || id != "remote-original" {
		t.Fatalf("capture = %q, %v", id, err)
	}
}

func TestScopedMemorySearchSendsScopeAndRejectsIncompleteMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project_id") != "project-a" || r.URL.Query().Get("task_id") != "task-a" || r.URL.Query().Get("owner_id") != "user-a" {
			t.Fatalf("missing exact scope: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"results":[{"id":"good","project_id":"project-a","task_id":"task-a","owner_id":"user-a","content":"keep","topics":["r2"]},{"id":"missing","content":"reject"},{"id":"other","project_id":"project-b","task_id":"task-a","owner_id":"user-a","content":"reject"}]}`))
	}))
	defer server.Close()
	backend := ScopedMemoryBackend{Client: NewClient(server.URL)}
	results, err := backend.Search(context.Background(), memory.SearchRequest{Query: "scope", Limit: 5, Scope: memory.Scope{ProjectID: "project-a", TaskID: "task-a", UserID: "user-a", IncludeUserScope: true, CorrelationID: "corr-test"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "good" || !strings.Contains(strings.Join(results[0].KeyTopics, ","), "r2") {
		t.Fatalf("results = %#v", results)
	}
}
