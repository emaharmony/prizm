package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var errStrategyExperimentScope = errors.New("strategy experiment scope mismatch")

type fakeFreewillStrategyExperiments struct {
	record    FreewillStrategyExperimentRecord
	finalized int
	reads     int
}

func (f *fakeFreewillStrategyExperiments) Finalize(_ context.Context, owner, project, workspace, agent string, taskIDs []string) (FreewillStrategyExperimentRecord, error) {
	if owner != f.record.OwnerID || project != f.record.ProjectID || workspace != "/synthetic/workspace" || agent != f.record.AgentID {
		return FreewillStrategyExperimentRecord{}, errStrategyExperimentScope
	}
	if len(taskIDs) != 2 || taskIDs[0] != "task-a" || taskIDs[1] != "task-b" {
		return FreewillStrategyExperimentRecord{}, errStrategyExperimentScope
	}
	f.finalized++
	return f.record, nil
}

func (f *fakeFreewillStrategyExperiments) ReadStrategyExperiment(_ context.Context, owner, project, workspace, agent string, taskIDs []string) (FreewillStrategyExperimentRecord, error) {
	f.reads++
	if owner != f.record.OwnerID || project != f.record.ProjectID || workspace != "/synthetic/workspace" || agent != f.record.AgentID {
		return FreewillStrategyExperimentRecord{}, errStrategyExperimentScope
	}
	return f.record, nil
}

func TestFreewillStrategyExperimentRoutesRequireBearerAndKeepFixedScope(t *testing.T) {
	controller := &fakeFreewillStrategyExperiments{record: FreewillStrategyExperimentRecord{ExperimentID: "fwexp-synthetic", OwnerID: "owner-a", ProjectID: "project-a", WorkspaceID: "workspace-a", AgentID: "agent-a", Runs: []FreewillStrategyExperimentRun{{TaskID: "task-a", StrategyID: "baseline"}, {TaskID: "task-b", StrategyID: "alternative"}}, Comparison: "Host receipt classifications matched; two synthetic runs cannot establish a strategy preference.", Uncertainty: "one run per fixed variant"}}
	s := NewServer(Config{Addr: ":0", AuthToken: "synthetic-token", FreewillStrategyExperiments: controller})
	body := `{"owner_id":"owner-a","project_id":"project-a","workspace":"/synthetic/workspace","agent_id":"agent-a","task_ids":["task-a","task-b"]}`
	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/strategy-experiments/finalize", strings.NewReader(body))
	unauthorizedResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized finalize status = %d, want %d", unauthorizedResponse.Code, http.StatusUnauthorized)
	}
	readURL := "/api/v1/freewill/strategy-experiments/current?owner_id=owner-a&project_id=project-a&workspace=%2Fsynthetic%2Fworkspace&agent_id=agent-a&task_id=task-a&task_id=task-b"
	unauthorizedRead := httptest.NewRequest(http.MethodGet, readURL, nil)
	unauthorizedReadResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauthorizedReadResponse, unauthorizedRead)
	wrongBearerRead := httptest.NewRequest(http.MethodGet, readURL, nil)
	wrongBearerRead.Header.Set("Authorization", "Bearer wrong-token")
	wrongBearerReadResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(wrongBearerReadResponse, wrongBearerRead)
	if unauthorizedReadResponse.Code != http.StatusUnauthorized || wrongBearerReadResponse.Code != http.StatusUnauthorized || controller.reads != 0 {
		t.Fatalf("unauthorized reads missing=%d wrong=%d controller_reads=%d", unauthorizedReadResponse.Code, wrongBearerReadResponse.Code, controller.reads)
	}
	finalize := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/strategy-experiments/finalize", strings.NewReader(body))
	finalize.Header.Set("Authorization", "Bearer synthetic-token")
	finalizeResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(finalizeResponse, finalize)
	if finalizeResponse.Code != http.StatusOK || controller.finalized != 1 || !strings.Contains(finalizeResponse.Body.String(), `"strategy_id":"baseline"`) {
		t.Fatalf("finalize status=%d calls=%d body=%s", finalizeResponse.Code, controller.finalized, finalizeResponse.Body.String())
	}
	read := httptest.NewRequest(http.MethodGet, readURL, nil)
	read.Header.Set("Authorization", "Bearer synthetic-token")
	readResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusOK || controller.reads != 1 || !strings.Contains(readResponse.Body.String(), `"experiment_id":"fwexp-synthetic"`) {
		t.Fatalf("read status=%d controller_reads=%d body=%s", readResponse.Code, controller.reads, readResponse.Body.String())
	}
	wrongScope := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/strategy-experiments/finalize", strings.NewReader(strings.Replace(body, "owner-a", "owner-b", 1)))
	wrongScope.Header.Set("Authorization", "Bearer synthetic-token")
	wrongScopeResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(wrongScopeResponse, wrongScope)
	if wrongScopeResponse.Code != http.StatusConflict {
		t.Fatalf("wrong scope status = %d, want %d", wrongScopeResponse.Code, http.StatusConflict)
	}
}

func TestFreewillStrategyExperimentRouteDisabledWithoutController(t *testing.T) {
	s := NewServer(Config{Addr: ":0", AuthToken: "synthetic-token"})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/strategy-experiments/finalize", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer synthetic-token")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled route status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestFreewillStrategyExperimentBindsOwnerInLocalPrincipalMode(t *testing.T) {
	controller := &fakeFreewillStrategyExperiments{record: FreewillStrategyExperimentRecord{ExperimentID: "fwexp-synthetic", OwnerID: diaryOwnerPrincipal, ProjectID: "project-a", AgentID: "agent-a"}}
	s := NewServer(Config{Addr: "127.0.0.1:0", AuthToken: "service-wide-token", FreewillStrategyExperiments: controller,
		FreewillDiaryAnnotations: &principalDiaryAnnotations{entry: FreewillDiaryAnnotationEntry{ProjectID: "project-a", WorkspaceID: "/synthetic/workspace", AgentID: "agent-a"}},
		FreewillDiaryConsent:     &principalDiaryConsent{owner: diaryOwnerPrincipal, grants: make(map[string]principalDiaryShareState)},
		LocalDiaryPrincipalAuth: &LocalFreewillDiaryPrincipalAuth{Enabled: true, OwnerPrincipalID: diaryOwnerPrincipal, Credentials: []LocalFreewillDiaryPrincipalCredential{
			{PrincipalID: diaryOwnerPrincipal, TokenSHA256: diaryTokenDigest("owner-token")},
			{PrincipalID: diaryRecipientPrincipal, TokenSHA256: diaryTokenDigest("recipient-token")},
		}},
	})
	path := "/api/v1/freewill/strategy-experiments/current?project_id=project-a&workspace=%2Fsynthetic%2Fworkspace&agent_id=agent-a&task_id=task-a&task_id=task-b"
	serviceRequest := httptest.NewRequest(http.MethodGet, path, nil)
	serviceRequest.Header.Set("Authorization", "Bearer service-wide-token")
	serviceResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(serviceResponse, serviceRequest)
	if serviceResponse.Code != http.StatusUnauthorized || controller.reads != 0 {
		t.Fatalf("service bearer strategy read status=%d reads=%d", serviceResponse.Code, controller.reads)
	}
	ownerRequest := httptest.NewRequest(http.MethodGet, path, nil)
	ownerRequest.Header.Set("Authorization", "Bearer owner-token")
	ownerResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(ownerResponse, ownerRequest)
	if ownerResponse.Code != http.StatusOK || controller.reads != 1 || !strings.Contains(ownerResponse.Body.String(), `"experiment_id":"fwexp-synthetic"`) {
		t.Fatalf("derived-owner strategy read status=%d reads=%d body=%s", ownerResponse.Code, controller.reads, ownerResponse.Body.String())
	}
	spoofed := httptest.NewRequest(http.MethodGet, path+"&owner_id=other-owner", nil)
	spoofed.Header.Set("Authorization", "Bearer owner-token")
	spoofedResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(spoofedResponse, spoofed)
	if spoofedResponse.Code != http.StatusForbidden || controller.reads != 1 {
		t.Fatalf("spoofed strategy read status=%d reads=%d", spoofedResponse.Code, controller.reads)
	}
	finalizeBody := `{"project_id":"project-a","workspace":"/synthetic/workspace","agent_id":"agent-a","task_ids":["task-a","task-b"]}`
	finalizeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/strategy-experiments/finalize", strings.NewReader(finalizeBody))
	finalizeRequest.Header.Set("Authorization", "Bearer owner-token")
	finalizeResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(finalizeResponse, finalizeRequest)
	if finalizeResponse.Code != http.StatusOK || controller.finalized != 1 {
		t.Fatalf("derived-owner strategy finalize status=%d finalized=%d body=%s", finalizeResponse.Code, controller.finalized, finalizeResponse.Body.String())
	}
	spoofedFinalizeBody := strings.Replace(finalizeBody, `"project_id"`, `"owner_id":"other-owner","project_id"`, 1)
	spoofedFinalize := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/strategy-experiments/finalize", strings.NewReader(spoofedFinalizeBody))
	spoofedFinalize.Header.Set("Authorization", "Bearer owner-token")
	spoofedFinalizeResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(spoofedFinalizeResponse, spoofedFinalize)
	if spoofedFinalizeResponse.Code != http.StatusForbidden || controller.finalized != 1 {
		t.Fatalf("spoofed strategy finalize status=%d finalized=%d", spoofedFinalizeResponse.Code, controller.finalized)
	}
}
