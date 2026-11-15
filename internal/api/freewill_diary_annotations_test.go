package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type fakeFreewillDiaryAnnotations struct {
	reads   int
	appends int
	owner   string
	entryID string
}

func (f *fakeFreewillDiaryAnnotations) ReadDiaryAnnotations(_ context.Context, owner, project, workspace, agent, intention, parent, entryID string) (FreewillDiaryAnnotationEntry, error) {
	f.reads++
	if owner != f.owner || project != "project-a" || workspace != "/synthetic/workspace" || agent != "agent-a" || intention != "intention-a" || parent != "parent-a" || entryID != f.entryID {
		return FreewillDiaryAnnotationEntry{}, errStrategyExperimentScope
	}
	return FreewillDiaryAnnotationEntry{EntryID: entryID, Revision: 2, OriginalText: "original", Annotations: []FreewillDiaryAnnotation{{Revision: 2, Previous: 1, Text: "owner note"}}}, nil
}

func (f *fakeFreewillDiaryAnnotations) AppendDiaryAnnotation(_ context.Context, owner, project, workspace, agent, intention, parent, entryID string, revision int, text string) (FreewillDiaryAnnotationEntry, error) {
	f.appends++
	if owner != f.owner || project != "project-a" || workspace != "/synthetic/workspace" || agent != "agent-a" || intention != "intention-a" || parent != "parent-a" || entryID != f.entryID || revision != 1 || text != "owner note" {
		return FreewillDiaryAnnotationEntry{}, errStrategyExperimentScope
	}
	return FreewillDiaryAnnotationEntry{EntryID: entryID, Revision: 2, OriginalText: "original", Annotations: []FreewillDiaryAnnotation{{Revision: 2, Previous: 1, Text: text}}}, nil
}

func TestFreewillDiaryAnnotationRoutesRequireBearerAndExactScope(t *testing.T) {
	controller := &fakeFreewillDiaryAnnotations{owner: "owner-a", entryID: "entry-a"}
	s := NewServer(Config{Addr: ":0", AuthToken: "synthetic-token", FreewillDiaryAnnotations: controller})
	base := "/api/v1/freewill/diary/entries/entry-a"
	query := url.Values{"owner_id": {"owner-a"}, "project_id": {"project-a"}, "workspace": {"/synthetic/workspace"}, "agent_id": {"agent-a"}, "intention_id": {"intention-a"}, "parent_run_id": {"parent-a"}}
	readURL := base + "?" + query.Encode()
	for _, token := range []string{"", "Bearer wrong-token"} {
		request := httptest.NewRequest(http.MethodGet, readURL, nil)
		if token != "" {
			request.Header.Set("Authorization", token)
		}
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized read status=%d want=%d", response.Code, http.StatusUnauthorized)
		}
	}
	if controller.reads != 0 {
		t.Fatalf("unauthorized reads reached controller %d times", controller.reads)
	}
	read := httptest.NewRequest(http.MethodGet, readURL, nil)
	read.Header.Set("Authorization", "Bearer synthetic-token")
	readResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusOK || !strings.Contains(readResponse.Body.String(), `"original_text":"original"`) || !strings.Contains(readResponse.Body.String(), `"text":"owner note"`) {
		t.Fatalf("authorized read status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}
	body := `{"owner_id":"owner-a","project_id":"project-a","workspace":"/synthetic/workspace","agent_id":"agent-a","intention_id":"intention-a","parent_run_id":"parent-a","expected_revision":1,"text":"owner note"}`
	post := httptest.NewRequest(http.MethodPost, base, strings.NewReader(body))
	post.Header.Set("Authorization", "Bearer synthetic-token")
	postResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusOK || controller.appends != 1 || !strings.Contains(postResponse.Body.String(), `"revision":2`) {
		t.Fatalf("authorized append status=%d calls=%d body=%s", postResponse.Code, controller.appends, postResponse.Body.String())
	}
	wrong := strings.Replace(body, "owner-a", "owner-b", 1)
	wrongRequest := httptest.NewRequest(http.MethodPost, base, strings.NewReader(wrong))
	wrongRequest.Header.Set("Authorization", "Bearer synthetic-token")
	wrongResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(wrongResponse, wrongRequest)
	if wrongResponse.Code != http.StatusConflict {
		t.Fatalf("wrong-owner append status=%d want=%d", wrongResponse.Code, http.StatusConflict)
	}
}

func TestFreewillDiaryAnnotationRouteDisabledWithoutController(t *testing.T) {
	s := NewServer(Config{Addr: ":0", AuthToken: "synthetic-token"})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/freewill/diary/entries/entry-a", nil)
	request.Header.Set("Authorization", "Bearer synthetic-token")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled route status=%d want=%d", response.Code, http.StatusServiceUnavailable)
	}
}
