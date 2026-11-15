package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeFreewillPromises struct {
	calls                                       int
	owner, project, workspace, agent, promiseID string
}

func (f *fakeFreewillPromises) ReadPromise(_ context.Context, owner, project, workspace, agent, promiseID string) (FreewillPromiseConfirmation, error) {
	f.calls++
	f.owner, f.project, f.workspace, f.agent, f.promiseID = owner, project, workspace, agent, promiseID
	return FreewillPromiseConfirmation{Current: FreewillPromiseRevision{PromiseID: "promise-a", Revision: 1, Scope: "current"}}, nil
}
func (f *fakeFreewillPromises) ProposePromiseRevision(_ context.Context, owner, project, workspace, agent, promiseID, _ string, _ int, _, _, _ string) (FreewillPromiseConfirmation, error) {
	f.calls++
	f.owner, f.project, f.workspace, f.agent, f.promiseID = owner, project, workspace, agent, promiseID
	return FreewillPromiseConfirmation{Current: FreewillPromiseRevision{PromiseID: "promise-a", Revision: 1}, Proposals: []FreewillPromiseProposal{{ProposalID: "proposal-a", Status: "pending"}}}, nil
}
func (f *fakeFreewillPromises) DecidePromiseRevision(_ context.Context, owner, project, workspace, agent, promiseID, _, _ string) (FreewillPromiseConfirmation, error) {
	f.calls++
	f.owner, f.project, f.workspace, f.agent, f.promiseID = owner, project, workspace, agent, promiseID
	return FreewillPromiseConfirmation{}, nil
}

func TestFreewillPromiseConfirmationRoutesRequireBearer(t *testing.T) {
	c := &fakeFreewillPromises{}
	s := NewServer(Config{Addr: ":0", AuthToken: "synthetic-token", FreewillPromises: c})
	base := "/api/v1/freewill/promises/promise-a"
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		r := httptest.NewRequest(method, base+"?owner_id=o&project_id=p&workspace=%2Ftmp%2Fw&agent_id=a", strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized %s=%d", method, w.Code)
		}
	}
	if c.calls != 0 {
		t.Fatalf("unauthorized request reached controller %d times", c.calls)
	}
	r := httptest.NewRequest(http.MethodGet, base+"?owner_id=o&project_id=p&workspace=%2Ftmp%2Fw&agent_id=a", nil)
	r.Header.Set("Authorization", "Bearer synthetic-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"revision":1`) {
		t.Fatalf("read status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestFreewillPromisePrincipalModeBindsOwnerAndExactRequestScope(t *testing.T) {
	c := &fakeFreewillPromises{}
	s := NewServer(Config{Addr: "127.0.0.1:0", AuthToken: "service-wide-token", FreewillPromises: c,
		FreewillDiaryAnnotations: &principalDiaryAnnotations{entry: FreewillDiaryAnnotationEntry{ProjectID: "project-exact", WorkspaceID: "/synthetic/workspace", AgentID: "agent-exact"}},
		FreewillDiaryConsent:     &principalDiaryConsent{owner: diaryOwnerPrincipal, grants: make(map[string]principalDiaryShareState)},
		LocalDiaryPrincipalAuth: &LocalFreewillDiaryPrincipalAuth{Enabled: true, OwnerPrincipalID: diaryOwnerPrincipal, Credentials: []LocalFreewillDiaryPrincipalCredential{
			{PrincipalID: diaryOwnerPrincipal, TokenSHA256: diaryTokenDigest("owner-token")},
			{PrincipalID: diaryRecipientPrincipal, TokenSHA256: diaryTokenDigest("recipient-token")},
		}},
	})
	base := "/api/v1/freewill/promises/promise-exact"
	request := func(method, url, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	query := "?project_id=project-exact&workspace=%2Fsynthetic%2Fworkspace&agent_id=agent-exact"
	if w := request(http.MethodGet, base+query, "", "service-wide-token"); w.Code != http.StatusUnauthorized || c.calls != 0 {
		t.Fatalf("service bearer status=%d calls=%d", w.Code, c.calls)
	}
	if w := request(http.MethodGet, base+query, "", "recipient-token"); w.Code != http.StatusForbidden || c.calls != 0 {
		t.Fatalf("recipient read status=%d calls=%d", w.Code, c.calls)
	}
	if w := request(http.MethodGet, base+query+"&owner_id=other", "", "owner-token"); w.Code != http.StatusForbidden || c.calls != 0 {
		t.Fatalf("spoofed read status=%d calls=%d", w.Code, c.calls)
	}
	if w := request(http.MethodGet, base+query+"&workspace=%2Fother", "", "owner-token"); w.Code != http.StatusBadRequest || c.calls != 0 {
		t.Fatalf("duplicate workspace status=%d calls=%d", w.Code, c.calls)
	}
	if w := request(http.MethodGet, base+query, "", "owner-token"); w.Code != http.StatusOK || c.calls != 1 || c.owner != diaryOwnerPrincipal || c.project != "project-exact" || c.workspace != "/synthetic/workspace" || c.agent != "agent-exact" || c.promiseID != "promise-exact" {
		t.Fatalf("derived-owner read status=%d scope=%q/%q/%q/%q/%q calls=%d", w.Code, c.owner, c.project, c.workspace, c.agent, c.promiseID, c.calls)
	}
	body := `{"project_id":"project-exact","workspace":"/synthetic/workspace","agent_id":"agent-exact","request_id":"request-exact","expected_revision":1,"scope":"bounded","deadline":"2026-12-01","impact_hypothesis":"synthetic"}`
	if w := request(http.MethodPost, base+"/proposals", body, "owner-token"); w.Code != http.StatusOK || c.calls != 2 || c.owner != diaryOwnerPrincipal || c.project != "project-exact" || c.workspace != "/synthetic/workspace" || c.agent != "agent-exact" || c.promiseID != "promise-exact" {
		t.Fatalf("derived-owner proposal status=%d scope=%q/%q/%q/%q/%q calls=%d body=%s", w.Code, c.owner, c.project, c.workspace, c.agent, c.promiseID, c.calls, w.Body.String())
	}
	spoofed := `{"owner_id":"other","project_id":"project-exact","workspace":"/synthetic/workspace","agent_id":"agent-exact","request_id":"request-exact","expected_revision":1,"scope":"bounded","deadline":"2026-12-01","impact_hypothesis":"synthetic"}`
	if w := request(http.MethodPost, base+"/proposals", spoofed, "owner-token"); w.Code != http.StatusForbidden || c.calls != 2 {
		t.Fatalf("spoofed proposal status=%d calls=%d", w.Code, c.calls)
	}
	decision := `{"project_id":"project-exact","workspace":"/synthetic/workspace","agent_id":"agent-exact","proposal_id":"proposal-a","decision":"accept"}`
	if w := request(http.MethodPost, base+"/decisions/proposal-a", decision, "owner-token"); w.Code != http.StatusOK || c.calls != 3 || c.owner != diaryOwnerPrincipal || c.project != "project-exact" || c.workspace != "/synthetic/workspace" || c.agent != "agent-exact" || c.promiseID != "promise-exact" {
		t.Fatalf("derived-owner decision status=%d scope=%q/%q/%q/%q/%q calls=%d body=%s", w.Code, c.owner, c.project, c.workspace, c.agent, c.promiseID, c.calls, w.Body.String())
	}
	spoofedDecision := strings.Replace(decision, `"project_id"`, `"owner_id":"other","project_id"`, 1)
	if w := request(http.MethodPost, base+"/decisions/proposal-a", spoofedDecision, "owner-token"); w.Code != http.StatusForbidden || c.calls != 3 {
		t.Fatalf("spoofed decision status=%d calls=%d", w.Code, c.calls)
	}
}
