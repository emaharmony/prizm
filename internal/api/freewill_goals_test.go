package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeFreewillGoals struct {
	ownerID   string
	proposal  FreewillGoalProposal
	record    FreewillGoalRecord
	decision  FreewillGoalDecision
	proposes  int
	reads     int
	decides   int
	activates int
	pauses    int
	resumes   int
	checkins  int
	decided   bool
}

func (f *fakeFreewillGoals) expectedOwner() string {
	if f.ownerID != "" {
		return f.ownerID
	}
	return "owner-a"
}

func (f *fakeFreewillGoals) Propose(_ context.Context, owner, project, agent string, tasks []string) (FreewillGoalProposal, error) {
	f.proposes++
	if owner != f.expectedOwner() || project != "project-a" || agent != "agent-a" || len(tasks) != 2 {
		return FreewillGoalProposal{}, errors.New("scope mismatch")
	}
	return f.proposal, nil
}
func (f *fakeFreewillGoals) Read(_ context.Context, owner, project, agent, id string) (FreewillGoalRecord, error) {
	f.reads++
	if owner != f.expectedOwner() || project != "project-a" || agent != "agent-a" || id != f.proposal.ProposalID {
		return FreewillGoalRecord{}, errors.New("scope mismatch")
	}
	return f.record, nil
}
func (f *fakeFreewillGoals) Decide(_ context.Context, owner, project, agent, id, decision string) (FreewillGoalDecision, error) {
	f.decides++
	if owner != f.expectedOwner() || project != "project-a" || agent != "agent-a" || id != f.proposal.ProposalID || (decision != "accept" && decision != "reject") || f.decided {
		return FreewillGoalDecision{}, errors.New("scope or immutable decision conflict")
	}
	f.decided = true
	f.decision.Decision = decision
	f.record.Decision = &f.decision
	return f.decision, nil
}

func (f *fakeFreewillGoals) Activate(_ context.Context, owner, project, agent, id string) (FreewillGoalRecord, error) {
	f.activates++
	if owner != f.expectedOwner() || project != "project-a" || agent != "agent-a" || id != f.proposal.ProposalID ||
		f.record.Decision == nil || f.record.Decision.Decision != "accept" {
		return FreewillGoalRecord{}, errors.New("scope or explicit acceptance required")
	}
	if f.record.Goal != nil {
		return f.record, nil
	}
	f.record.Goal = &FreewillActiveGoal{GoalID: "active-goal-a", ProposalID: id, OwnerID: owner, ProjectID: project, WorkspaceID: "workspace-digest", AgentID: agent, State: "active", Revision: 1}
	f.record.GoalHistory = []FreewillGoalStateRevision{{RecordID: "revision-1", Revision: 1, State: "active", RecordedAt: "2026-11-11T00:00:00Z"}}
	return f.record, nil
}

func (f *fakeFreewillGoals) Pause(_ context.Context, owner, project, agent, id, reason string) (FreewillGoalRecord, error) {
	f.pauses++
	if owner != f.expectedOwner() || project != "project-a" || agent != "agent-a" || id != f.proposal.ProposalID || f.record.Goal == nil || reason == "" {
		return FreewillGoalRecord{}, errors.New("scope or active goal required")
	}
	if f.record.Goal.State == "paused" {
		return f.record, nil
	}
	f.record.Goal.State, f.record.Goal.Revision = "paused", 2
	f.record.GoalHistory = append(f.record.GoalHistory, FreewillGoalStateRevision{RecordID: "revision-2", Revision: 2, State: "paused", Reason: reason, RecordedAt: "2026-11-11T00:01:00Z"})
	return f.record, nil
}

func (f *fakeFreewillGoals) Resume(_ context.Context, owner, project, agent, id, reason string) (FreewillGoalRecord, error) {
	f.resumes++
	if owner != f.expectedOwner() || project != "project-a" || agent != "agent-a" || id != f.proposal.ProposalID || f.record.Goal == nil || f.record.Goal.State != "paused" || reason == "" {
		return FreewillGoalRecord{}, errors.New("scope or paused goal required")
	}
	f.record.Goal.State, f.record.Goal.Revision = "active", 3
	f.record.GoalHistory = append(f.record.GoalHistory, FreewillGoalStateRevision{RecordID: "revision-3", PreviousRecordID: "revision-2", Revision: 3, State: "active", Reason: reason, RecordedAt: "2026-11-11T00:02:00Z"})
	return f.record, nil
}

func (f *fakeFreewillGoals) CheckIn(_ context.Context, owner, project, agent, id string, revision int, goalID, requestID, childRunID, selfReport string) (FreewillGoalRecord, error) {
	f.checkins++
	if owner != f.expectedOwner() || project != "project-a" || agent != "agent-a" || id != f.proposal.ProposalID || f.record.Goal == nil || f.record.Goal.State != "active" || revision != f.record.Goal.Revision || goalID != f.record.Goal.GoalID || requestID == "" || childRunID == "" || len(selfReport) > 320 {
		return FreewillGoalRecord{}, errors.New("scope or active goal required")
	}
	return f.record, nil
}

func TestFreewillGoalPrincipalModeDerivesOwnerAndRejectsSpoofing(t *testing.T) {
	controller := &fakeFreewillGoals{ownerID: diaryOwnerPrincipal, proposal: FreewillGoalProposal{ProposalID: "goal_0123456789abcdef0123456789abcdef", OwnerID: diaryOwnerPrincipal, ProjectID: "project-a", AgentID: "agent-a"}}
	s := NewServer(Config{Addr: "127.0.0.1:0", AuthToken: "service-wide-token", FreewillGoals: controller,
		FreewillDiaryAnnotations: &principalDiaryAnnotations{entry: FreewillDiaryAnnotationEntry{ProjectID: "project-a", WorkspaceID: "/synthetic/workspace", AgentID: "agent-a"}},
		FreewillDiaryConsent:     &principalDiaryConsent{owner: diaryOwnerPrincipal, grants: make(map[string]principalDiaryShareState)},
		LocalDiaryPrincipalAuth: &LocalFreewillDiaryPrincipalAuth{Enabled: true, OwnerPrincipalID: diaryOwnerPrincipal, Credentials: []LocalFreewillDiaryPrincipalCredential{
			{PrincipalID: diaryOwnerPrincipal, TokenSHA256: diaryTokenDigest("owner-token")},
			{PrincipalID: diaryRecipientPrincipal, TokenSHA256: diaryTokenDigest("recipient-token")},
		}},
	})
	path := "/api/v1/freewill/goals/proposals"
	serve := func(token, body string, duplicate bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if duplicate {
			req.Header.Add("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	validBody := `{"project_id":"project-a","agent_id":"agent-a","child_run_ids":["task-a","task-b"]}`
	if w := serve("service-wide-token", validBody, false); w.Code != http.StatusUnauthorized || controller.proposes != 0 {
		t.Fatalf("service bearer status=%d calls=%d", w.Code, controller.proposes)
	}
	queryTokenRequest := httptest.NewRequest(http.MethodPost, path+"?token=owner-token", strings.NewReader(validBody))
	queryTokenRequest.Header.Set("Authorization", "Bearer owner-token")
	queryTokenResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(queryTokenResponse, queryTokenRequest)
	if queryTokenResponse.Code != http.StatusUnauthorized || controller.proposes != 0 {
		t.Fatalf("query token status=%d calls=%d", queryTokenResponse.Code, controller.proposes)
	}
	if w := serve("owner-token", validBody, true); w.Code != http.StatusUnauthorized || controller.proposes != 0 {
		t.Fatalf("duplicate principal header status=%d calls=%d", w.Code, controller.proposes)
	}
	if w := serve("recipient-token", validBody, false); w.Code != http.StatusForbidden || controller.proposes != 0 {
		t.Fatalf("recipient proposal status=%d calls=%d", w.Code, controller.proposes)
	}
	spoofed := `{"owner_id":"synthetic-other-owner","project_id":"project-a","agent_id":"agent-a","child_run_ids":["task-a","task-b"]}`
	if w := serve("owner-token", spoofed, false); w.Code != http.StatusForbidden || controller.proposes != 0 {
		t.Fatalf("spoofed proposal status=%d calls=%d", w.Code, controller.proposes)
	}
	if w := serve("owner-token", validBody, false); w.Code != http.StatusOK || controller.proposes != 1 {
		t.Fatalf("owner proposal status=%d calls=%d body=%s", w.Code, controller.proposes, w.Body.String())
	}
	readURL := "/api/v1/freewill/goals/proposals/" + controller.proposal.ProposalID + "?project_id=project-a&agent_id=agent-a"
	req := httptest.NewRequest(http.MethodGet, readURL, nil)
	req.Header.Set("Authorization", "Bearer owner-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || controller.reads != 1 {
		t.Fatalf("derived-owner read status=%d calls=%d body=%s", w.Code, controller.reads, w.Body.String())
	}
	spoofedRead := httptest.NewRequest(http.MethodGet, readURL+"&owner_id=other-owner", nil)
	spoofedRead.Header.Set("Authorization", "Bearer owner-token")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, spoofedRead)
	if w.Code != http.StatusForbidden || controller.reads != 1 {
		t.Fatalf("spoofed read status=%d calls=%d", w.Code, controller.reads)
	}
	runList := httptest.NewRequest(http.MethodGet, "/api/v1/freewill/runs", nil)
	runList.Header.Set("Authorization", "Bearer owner-token")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, runList)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("principal credential unexpectedly reached runs API: status=%d", w.Code)
	}
}

func TestFreewillGoalRouteRequiresBearerAndRecordsSeparateDecision(t *testing.T) {
	controller := &fakeFreewillGoals{proposal: FreewillGoalProposal{ProposalID: "goal_0123456789abcdef0123456789abcdef", OwnerID: "owner-a", ProjectID: "project-a", AgentID: "agent-a", Hypothesis: "synthetic hypothesis", SourceIDs: []string{"outcome-a", "appraisal-a"}, Provider: "local-fake", Model: "fixture"}}
	controller.record.Proposal = controller.proposal
	s := NewServer(Config{Addr: ":0", AuthToken: "synthetic-token", FreewillGoals: controller})
	h := s.Handler()
	postPath := "/api/v1/freewill/goals/proposals"
	body := `{"owner_id":"owner-a","project_id":"project-a","agent_id":"agent-a","child_run_ids":["task-a","task-b"]}`
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest(http.MethodPost, postPath, strings.NewReader(body)))
	if unauth.Code != http.StatusUnauthorized || controller.proposes != 0 {
		t.Fatalf("unauthenticated proposal status=%d calls=%d", unauth.Code, controller.proposes)
	}
	authorized := httptest.NewRequest(http.MethodPost, postPath, strings.NewReader(body))
	authorized.Header.Set("Authorization", "Bearer synthetic-token")
	posted := httptest.NewRecorder()
	h.ServeHTTP(posted, authorized)
	if posted.Code != http.StatusOK || controller.proposes != 1 || !strings.Contains(posted.Body.String(), controller.proposal.ProposalID) {
		t.Fatalf("proposal status=%d calls=%d body=%s", posted.Code, controller.proposes, posted.Body.String())
	}
	readPath := "/api/v1/freewill/goals/proposals/" + controller.proposal.ProposalID + "?owner_id=owner-a&project_id=project-a&agent_id=agent-a"
	unauthRead := httptest.NewRecorder()
	h.ServeHTTP(unauthRead, httptest.NewRequest(http.MethodGet, readPath, nil))
	if unauthRead.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated read status=%d", unauthRead.Code)
	}
	read := httptest.NewRequest(http.MethodGet, readPath, nil)
	read.Header.Set("Authorization", "Bearer synthetic-token")
	readResponse := httptest.NewRecorder()
	h.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusOK || controller.reads != 1 {
		t.Fatalf("read status=%d calls=%d body=%s", readResponse.Code, controller.reads, readResponse.Body.String())
	}
	decisionPath := "/api/v1/freewill/goals/proposals/" + controller.proposal.ProposalID + "/decision"
	decision := httptest.NewRequest(http.MethodPost, decisionPath, strings.NewReader(`{"owner_id":"owner-a","project_id":"project-a","agent_id":"agent-a","decision":"accept"}`))
	decision.Header.Set("Authorization", "Bearer synthetic-token")
	decisionResponse := httptest.NewRecorder()
	h.ServeHTTP(decisionResponse, decision)
	if decisionResponse.Code != http.StatusOK || controller.decides != 1 || !strings.Contains(decisionResponse.Body.String(), `"decision":"accept"`) {
		t.Fatalf("decision status=%d calls=%d body=%s", decisionResponse.Code, controller.decides, decisionResponse.Body.String())
	}
	conflict := httptest.NewRequest(http.MethodPost, decisionPath, strings.NewReader(`{"owner_id":"owner-a","project_id":"project-a","agent_id":"agent-a","decision":"reject"}`))
	conflict.Header.Set("Authorization", "Bearer synthetic-token")
	conflictResponse := httptest.NewRecorder()
	h.ServeHTTP(conflictResponse, conflict)
	if conflictResponse.Code != http.StatusConflict || controller.decides != 2 {
		t.Fatalf("conflicting decision status=%d calls=%d", conflictResponse.Code, controller.decides)
	}

	activationPath := "/api/v1/freewill/goals/proposals/" + controller.proposal.ProposalID + "/activate"
	unauthActivation := httptest.NewRecorder()
	h.ServeHTTP(unauthActivation, httptest.NewRequest(http.MethodPost, activationPath, strings.NewReader(`{"owner_id":"owner-a","project_id":"project-a","agent_id":"agent-a"}`)))
	if unauthActivation.Code != http.StatusUnauthorized || controller.activates != 0 {
		t.Fatalf("unauthenticated activation status=%d calls=%d", unauthActivation.Code, controller.activates)
	}
	activation := httptest.NewRequest(http.MethodPost, activationPath, strings.NewReader(`{"owner_id":"owner-a","project_id":"project-a","agent_id":"agent-a"}`))
	activation.Header.Set("Authorization", "Bearer synthetic-token")
	activationResponse := httptest.NewRecorder()
	h.ServeHTTP(activationResponse, activation)
	if activationResponse.Code != http.StatusOK || controller.activates != 1 || !strings.Contains(activationResponse.Body.String(), `"state":"active"`) {
		t.Fatalf("activation status=%d calls=%d body=%s", activationResponse.Code, controller.activates, activationResponse.Body.String())
	}
	pausePath := "/api/v1/freewill/goals/proposals/" + controller.proposal.ProposalID + "/pause"
	unauthPause := httptest.NewRecorder()
	h.ServeHTTP(unauthPause, httptest.NewRequest(http.MethodPost, pausePath, strings.NewReader(`{"owner_id":"owner-a","project_id":"project-a","agent_id":"agent-a","reason":"synthetic pause"}`)))
	if unauthPause.Code != http.StatusUnauthorized || controller.pauses != 0 {
		t.Fatalf("unauthenticated pause status=%d calls=%d", unauthPause.Code, controller.pauses)
	}
	pause := httptest.NewRequest(http.MethodPost, pausePath, strings.NewReader(`{"owner_id":"owner-a","project_id":"project-a","agent_id":"agent-a","reason":"synthetic pause"}`))
	pause.Header.Set("Authorization", "Bearer synthetic-token")
	pauseResponse := httptest.NewRecorder()
	h.ServeHTTP(pauseResponse, pause)
	if pauseResponse.Code != http.StatusOK || controller.pauses != 1 || !strings.Contains(pauseResponse.Body.String(), `"state":"paused"`) {
		t.Fatalf("pause status=%d calls=%d body=%s", pauseResponse.Code, controller.pauses, pauseResponse.Body.String())
	}
	resumePath := "/api/v1/freewill/goals/proposals/" + controller.proposal.ProposalID + "/resume"
	unauthResume := httptest.NewRecorder()
	h.ServeHTTP(unauthResume, httptest.NewRequest(http.MethodPost, resumePath, strings.NewReader(`{"owner_id":"owner-a","project_id":"project-a","agent_id":"agent-a","reason":"synthetic resume"}`)))
	if unauthResume.Code != http.StatusUnauthorized || controller.resumes != 0 {
		t.Fatalf("unauthenticated resume status=%d calls=%d", unauthResume.Code, controller.resumes)
	}
	resume := httptest.NewRequest(http.MethodPost, resumePath, strings.NewReader(`{"owner_id":"owner-a","project_id":"project-a","agent_id":"agent-a","reason":"synthetic resume"}`))
	resume.Header.Set("Authorization", "Bearer synthetic-token")
	resumeResponse := httptest.NewRecorder()
	h.ServeHTTP(resumeResponse, resume)
	if resumeResponse.Code != http.StatusOK || controller.resumes != 1 || !strings.Contains(resumeResponse.Body.String(), `"state":"active"`) || !strings.Contains(resumeResponse.Body.String(), `"revision":3`) {
		t.Fatalf("resume status=%d calls=%d body=%s", resumeResponse.Code, controller.resumes, resumeResponse.Body.String())
	}
	progressPath := "/api/v1/freewill/goals/proposals/" + controller.proposal.ProposalID + "/progress"
	progressBody := `{"owner_id":"owner-a","project_id":"project-a","agent_id":"agent-a","goal_id":"active-goal-a","goal_revision":3,"request_id":"checkin-3","child_run_id":"child-a","owner_self_report":"tentative"}`
	unauthProgress := httptest.NewRecorder()
	h.ServeHTTP(unauthProgress, httptest.NewRequest(http.MethodPost, progressPath, strings.NewReader(progressBody)))
	if unauthProgress.Code != http.StatusUnauthorized || controller.checkins != 0 {
		t.Fatalf("unauthenticated progress status=%d calls=%d", unauthProgress.Code, controller.checkins)
	}
	progress := httptest.NewRequest(http.MethodPost, progressPath, strings.NewReader(progressBody))
	progress.Header.Set("Authorization", "Bearer synthetic-token")
	progressResponse := httptest.NewRecorder()
	h.ServeHTTP(progressResponse, progress)
	if progressResponse.Code != http.StatusOK || controller.checkins != 1 {
		t.Fatalf("progress status=%d calls=%d body=%s", progressResponse.Code, controller.checkins, progressResponse.Body.String())
	}
}

func TestFreewillGoalRouteDisabledWhenControllerAbsent(t *testing.T) {
	s := NewServer(Config{Addr: ":0", AuthToken: "synthetic-token"})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/goals/proposals", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer synthetic-token")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled goal route status=%d body=%s", response.Code, response.Body.String())
	}
}
