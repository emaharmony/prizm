package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

type freewillApprovalStub struct{}

func (freewillApprovalStub) Status(context.Context, string) (multiagent.FreewillApprovalStatus, error) {
	return multiagent.FreewillApprovalStatus{}, nil
}

func (freewillApprovalStub) DecideStatus(context.Context, string, string, string, string, string) (multiagent.FreewillApprovalStatus, error) {
	return multiagent.FreewillApprovalStatus{}, nil
}

type fakeFreewillLister struct {
	freewillApprovalStub
	lists int
}

func (f *fakeFreewillLister) List(_ context.Context, pageSize int) ([]multiagent.FreewillApprovalStatus, error) {
	if pageSize <= 0 || pageSize > 500 {
		return nil, errors.New("page bounds")
	}
	f.lists++
	return []multiagent.FreewillApprovalStatus{{
		RunID:          "freewill-test-1",
		ProjectID:      "synthetic-event-board",
		ProjectSources: []multiagent.FreewillPublicProjectSource{{ID: "project:readme", Path: "README.md", SHA256: strings.Repeat("a", 64)}},
		Lifecycle:      "approval_pending",
		ApprovalID:     "appr_test",
		ApprovalStatus: "pending",
		Selected: &multiagent.FreewillPublicCandidate{CandidateID: "candidate-1", Title: "Document the event schedule",
			Reason: "This follows the host roadmap action.", BasisRefs: []string{"project:readme"},
			Uncertainty: "The synthetic README may be incomplete.", ProjectActionID: "project:roadmap:M1"},
	}}, nil
}

func TestFreewillRunsListEndpointReturnsRuns(t *testing.T) {
	controller := &fakeFreewillLister{}
	s := NewServer(Config{Addr: ":0", FreewillApprovals: controller})

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/freewill/runs", nil))
	if rec.Code != http.StatusOK || controller.lists != 1 {
		t.Fatalf("list status=%d lists=%d body=%s", rec.Code, controller.lists, rec.Body.String())
	}
	var payload struct {
		Runs []multiagent.FreewillApprovalStatus `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(payload.Runs) != 1 || payload.Runs[0].RunID != "freewill-test-1" || payload.Runs[0].ApprovalStatus != "pending" {
		t.Fatalf("runs=%+v", payload.Runs)
	}
	run := payload.Runs[0]
	if run.ProjectID != "synthetic-event-board" || len(run.ProjectSources) != 1 || run.ProjectSources[0].Path != "README.md" ||
		run.Selected == nil || len(run.Selected.BasisRefs) != 1 || run.Selected.BasisRefs[0] != "project:readme" ||
		run.Selected.Uncertainty == "" || run.Selected.ProjectActionID != "project:roadmap:M1" {
		t.Fatalf("run evidence projection=%+v", run)
	}

	post := httptest.NewRecorder()
	s.mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/v1/freewill/runs", strings.NewReader(`{}`)))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST list status=%d", post.Code)
	}
}

func TestFreewillRunsListUnavailableWithoutController(t *testing.T) {
	s := NewServer(Config{Addr: ":0"})
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/freewill/runs", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-controller status=%d", rec.Code)
	}
}

func TestFreewillRunsListRejectedForNonListerController(t *testing.T) {
	s := NewServer(Config{Addr: ":0", FreewillApprovals: freewillApprovalStub{}})
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/freewill/runs", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-lister status=%d", rec.Code)
	}
}
