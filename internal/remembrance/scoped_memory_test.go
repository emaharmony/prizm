package remembrance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/memory"
)

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
