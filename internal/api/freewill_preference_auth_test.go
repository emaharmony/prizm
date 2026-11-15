package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type preferenceAccessController struct {
	evidenceReads  int
	challengeReads int
	challengePosts int
}

func (c *preferenceAccessController) ChallengePreference(context.Context, string, string, string, string, string, string, string, string, string, time.Time) (json.RawMessage, json.RawMessage, error) {
	c.challengePosts++
	return json.RawMessage(`{"id":"challenge-a"}`), json.RawMessage(`{"revision":"revision-a"}`), nil
}

func (c *preferenceAccessController) ReadPreferenceChallenges(context.Context, string, string, string, string, string) ([]json.RawMessage, error) {
	c.challengeReads++
	return []json.RawMessage{json.RawMessage(`{"id":"challenge-a"}`)}, nil
}

func (c *preferenceAccessController) ReadPreferenceEvidence(context.Context, string, string, string, string, string) (any, error) {
	c.evidenceReads++
	return map[string]string{"hypothesis_id": "hypothesis-a", "evidence": "synthetic"}, nil
}

func (*preferenceAccessController) WithdrawPreferenceEvidence(context.Context, string, string, string, string, time.Time) (string, error) {
	return "complete", nil
}

func TestFreewillPreferenceReadRoutesRequireServiceBearer(t *testing.T) {
	const serviceToken = "synthetic-service-bearer"
	controller := &preferenceAccessController{}
	server := NewServer(Config{
		Addr:                         "127.0.0.1:0",
		AuthToken:                    serviceToken,
		FreewillPreferenceChallenges: controller,
		// No local principal auth is configured: the service bearer is the
		// required boundary before owner_id is accepted by these read routes.
	})
	handler := server.Handler()

	query := url.Values{
		"owner_id":   {"owner-a"},
		"project_id": {"project-a"},
		"workspace":  {"/synthetic/workspace"},
		"agent_id":   {"agent-a"},
	}
	for _, tc := range []struct {
		name      string
		path      string
		wantBody  string
		readCount func() int
	}{
		{
			name:      "evidence",
			path:      "/api/v1/freewill/preferences/hypotheses/hypothesis-a/evidence",
			wantBody:  `"evidence":"synthetic"`,
			readCount: func() int { return controller.evidenceReads },
		},
		{
			name:      "challenges",
			path:      "/api/v1/freewill/preferences/hypotheses/hypothesis-a/challenges",
			wantBody:  `"id":"challenge-a"`,
			readCount: func() int { return controller.challengeReads },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.path+"?"+query.Encode(), nil)
			unauthenticated := httptest.NewRecorder()
			handler.ServeHTTP(unauthenticated, request)
			if unauthenticated.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated GET status=%d want=401 body=%s", unauthenticated.Code, unauthenticated.Body.String())
			}
			if got := tc.readCount(); got != 0 {
				t.Fatalf("unauthenticated GET reached preference controller: reads=%d", got)
			}

			authenticatedRequest := httptest.NewRequest(http.MethodGet, tc.path+"?"+query.Encode(), nil)
			authenticatedRequest.Header.Set("Authorization", "Bearer "+serviceToken)
			authenticated := httptest.NewRecorder()
			handler.ServeHTTP(authenticated, authenticatedRequest)
			if authenticated.Code != http.StatusOK || !strings.Contains(authenticated.Body.String(), tc.wantBody) {
				t.Fatalf("authenticated GET status=%d body=%s", authenticated.Code, authenticated.Body.String())
			}
			if got := tc.readCount(); got != 1 {
				t.Fatalf("authenticated GET controller reads=%d want=1", got)
			}
		})
	}

	postPath := "/api/v1/freewill/preferences/hypotheses/hypothesis-a/challenges"
	postBody := `{"owner_id":"owner-a","project_id":"project-a","workspace":"/synthetic/workspace","agent_id":"agent-a","hypothesis_id":"hypothesis-a","expected_revision":"revision-a","challenge_id":"challenge-a","source_id":"source-a","rationale":"synthetic rationale"}`
	unauthenticatedPost := httptest.NewRequest(http.MethodPost, postPath, strings.NewReader(postBody))
	unauthenticatedPostResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedPostResponse, unauthenticatedPost)
	if unauthenticatedPostResponse.Code != http.StatusUnauthorized || controller.challengePosts != 0 {
		t.Fatalf("unauthenticated POST status=%d writes=%d want=401/0", unauthenticatedPostResponse.Code, controller.challengePosts)
	}
	authenticatedPost := httptest.NewRequest(http.MethodPost, postPath, strings.NewReader(postBody))
	authenticatedPost.Header.Set("Authorization", "Bearer "+serviceToken)
	authenticatedPostResponse := httptest.NewRecorder()
	handler.ServeHTTP(authenticatedPostResponse, authenticatedPost)
	if authenticatedPostResponse.Code != http.StatusOK || controller.challengePosts != 1 {
		t.Fatalf("authenticated POST status=%d writes=%d body=%s", authenticatedPostResponse.Code, controller.challengePosts, authenticatedPostResponse.Body.String())
	}
}
