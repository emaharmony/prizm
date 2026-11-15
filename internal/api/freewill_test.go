package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

type fakeFreewillControl struct {
	state multiagent.FreewillOperatorControlState
	sets  int
}

func (f *fakeFreewillControl) Status(_ context.Context, agentID string, scope freewill.Scope) (multiagent.FreewillOperatorControlState, error) {
	f.state.AgentID, f.state.Scope = agentID, scope
	return f.state, nil
}
func (f *fakeFreewillControl) SetHalted(_ context.Context, agentID string, scope freewill.Scope, halted bool, actor string) (multiagent.FreewillOperatorControlState, error) {
	f.sets++
	f.state = multiagent.FreewillOperatorControlState{AgentID: agentID, Scope: scope, Halted: halted, Actor: actor}
	return f.state, nil
}

func TestFreewillOperatorControlRequiresAuthAndOnlyExplicitPostChangesHalt(t *testing.T) {
	controller := &fakeFreewillControl{}
	s := NewServer(Config{Addr: ":0", AuthToken: "secret", FreewillControl: controller})
	h := s.authMiddleware(s.mux)
	body := `{"agent_id":"agent-a","project_id":"project-a","workspace":"workspace-a","halted":true}`
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest(http.MethodPost, "/api/v1/freewill/control", strings.NewReader(body)))
	if unauth.Code != http.StatusUnauthorized || controller.sets != 0 {
		t.Fatalf("unauthenticated mutation status=%d sets=%d", unauth.Code, controller.sets)
	}
	post := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/control", strings.NewReader(body))
	post.Header.Set("Authorization", "Bearer secret")
	posted := httptest.NewRecorder()
	h.ServeHTTP(posted, post)
	if posted.Code != http.StatusOK || controller.sets != 1 || !controller.state.Halted || controller.state.Actor != "api:operator" {
		t.Fatalf("halt status=%d sets=%d state=%+v body=%s", posted.Code, controller.sets, controller.state, posted.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/freewill/control?agent_id=agent-a&project_id=project-a&workspace=workspace-a", nil)
	get.Header.Set("Authorization", "Bearer secret")
	got := httptest.NewRecorder()
	h.ServeHTTP(got, get)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"halted":true`) || controller.sets != 1 {
		t.Fatalf("status GET=%d sets=%d body=%s", got.Code, controller.sets, got.Body.String())
	}
	for _, invalid := range []string{`{"agent_id":"agent-a","project_id":"project-a","workspace":"workspace-a","halted":false,"approved":true}`, body + ` {}`, `{"agent_id":"agent-a","project_id":"project-a","workspace":"workspace-a"}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/control", strings.NewReader(invalid))
		req.Header.Set("Authorization", "Bearer secret")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("invalid control body=%q status=%d", invalid, res.Code)
		}
	}
	resume := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/control", strings.NewReader(`{"agent_id":"agent-a","project_id":"project-a","workspace":"workspace-a","halted":false}`))
	resume.Header.Set("Authorization", "Bearer secret")
	resumed := httptest.NewRecorder()
	h.ServeHTTP(resumed, resume)
	if resumed.Code != http.StatusOK || controller.sets != 2 || controller.state.Halted {
		t.Fatalf("explicit resume status=%d sets=%d state=%+v", resumed.Code, controller.sets, controller.state)
	}
}

type fakeFreewillApprovals struct {
	status                             multiagent.FreewillApprovalStatus
	decision, runID, approvalID, actor string
	decisionDisabled                   bool
}

func (f *fakeFreewillApprovals) Status(_ context.Context, runID string) (multiagent.FreewillApprovalStatus, error) {
	f.runID = runID
	return f.status, nil
}
func (f *fakeFreewillApprovals) DecideStatus(_ context.Context, runID, approvalID, decision, actor, _ string) (multiagent.FreewillApprovalStatus, error) {
	f.runID, f.approvalID, f.decision, f.actor = runID, approvalID, decision, actor
	if f.decisionDisabled {
		return multiagent.FreewillApprovalStatus{}, errors.New("read-only status controller")
	}
	return f.status, nil
}

func TestFreewillStatusIsAuthenticatedAndShowsReviewableRequest(t *testing.T) {
	controller := &fakeFreewillApprovals{status: multiagent.FreewillApprovalStatus{
		RunID: "run-fw", Lifecycle: freewill.StateRunning, ApprovalID: "appr-fw", ExecutionDisabled: true,
		ExecutionStatus: "completed", ChildRunID: "run-child", ChildStatus: "completed", ExecutionReportRef: "multiagent_report.json",
		ExecutionReportSHA256: strings.Repeat("a", 64), ValidationSummary: "passed",
		Selected:     &multiagent.FreewillPublicCandidate{CandidateID: "candidate-1", Title: "Bounded task", Goal: "Inspect project tests", Reason: "Useful next action", Scope: freewill.Scope{ProjectID: "project-a", Workspace: "workspace-a"}, Capabilities: []string{"read_only"}},
		Alternatives: []multiagent.FreewillPublicCandidate{{CandidateID: "candidate-2", Title: "Alternative", Goal: "Another bounded task", Reason: "Less urgent"}},
	}}
	s := NewServer(Config{Addr: ":0", AuthToken: "secret", FreewillApprovals: controller})
	h := s.authMiddleware(s.mux)
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/api/v1/freewill/runs/run-fw", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauth.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/freewill/runs/run-fw", nil)
	req.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Inspect project tests") || !strings.Contains(response.Body.String(), "execution_disabled") ||
		!strings.Contains(response.Body.String(), `"execution_status":"completed"`) || !strings.Contains(response.Body.String(), `"child_run_id":"run-child"`) ||
		!strings.Contains(response.Body.String(), `"execution_report_ref":"multiagent_report.json"`) ||
		!strings.Contains(response.Body.String(), `"execution_report_sha256":"`+strings.Repeat("a", 64)+`"`) ||
		!strings.Contains(response.Body.String(), `"validation_summary":"passed"`) || controller.runID != "run-fw" {
		t.Fatalf("status response=%d body=%s run=%s", response.Code, response.Body.String(), controller.runID)
	}
}

func TestFreewillReadOnlyStatusAllowsHistoryButRejectsDecision(t *testing.T) {
	controller := &fakeFreewillApprovals{decisionDisabled: true, status: multiagent.FreewillApprovalStatus{
		RunID: "run-old", Lifecycle: freewill.StatePaused, ApprovalID: "appr-old", ApprovalStatus: "approved", ExecutionDisabled: true,
	}}
	s := NewServer(Config{Addr: ":0", AuthToken: "secret", FreewillApprovals: controller})
	h := s.authMiddleware(s.mux)
	get := httptest.NewRequest(http.MethodGet, "/api/v1/freewill/runs/run-old", nil)
	get.Header.Set("Authorization", "Bearer secret")
	got := httptest.NewRecorder()
	h.ServeHTTP(got, get)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"approval_status":"approved"`) {
		t.Fatalf("historical status=%d body=%s", got.Code, got.Body.String())
	}
	controller.status = multiagent.FreewillApprovalStatus{
		RunID: "run-old", Lifecycle: freewill.StateDenied, ApprovalID: "appr-old",
		ApprovalStatus: "denied", ExecutionDisabled: true, PauseReason: "human_denied",
	}
	deniedHistory := httptest.NewRequest(http.MethodGet, "/api/v1/freewill/runs/run-old", nil)
	deniedHistory.Header.Set("Authorization", "Bearer secret")
	deniedResult := httptest.NewRecorder()
	h.ServeHTTP(deniedResult, deniedHistory)
	if deniedResult.Code != http.StatusOK || !strings.Contains(deniedResult.Body.String(), `"approval_status":"denied"`) ||
		!strings.Contains(deniedResult.Body.String(), `"pause_reason":"human_denied"`) {
		t.Fatalf("historical denied status=%d body=%s", deniedResult.Code, deniedResult.Body.String())
	}
	post := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/runs/run-old/approvals/appr-old/decision", strings.NewReader(`{"decision":"deny"}`))
	post.Header.Set("Authorization", "Bearer secret")
	denied := httptest.NewRecorder()
	h.ServeHTTP(denied, post)
	if denied.Code != http.StatusConflict || controller.decision != "deny" {
		t.Fatalf("disabled decision status=%d body=%s decision=%s", denied.Code, denied.Body.String(), controller.decision)
	}
}

func TestFreewillDecisionEndpointRejectsUnknownAuthorityAndUsesAuthenticatedActor(t *testing.T) {
	controller := &fakeFreewillApprovals{status: multiagent.FreewillApprovalStatus{RunID: "run-fw", Lifecycle: freewill.StatePaused, ExecutionDisabled: true}}
	s := NewServer(Config{Addr: ":0", AuthToken: "secret", FreewillApprovals: controller})
	h := s.authMiddleware(s.mux)
	for _, body := range []string{`{"decision":"grant","approved":true}`, `{"decision":"grant"} {}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/runs/run-fw/approvals/appr-fw/decision", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%q status=%d", body, response.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/runs/run-fw/approvals/appr-fw/decision?by=operator", strings.NewReader(`{"decision":"grant"}`))
	req.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != http.StatusOK || controller.runID != "run-fw" || controller.approvalID != "appr-fw" || controller.decision != "grant" || controller.actor != "operator" {
		t.Fatalf("decision response=%d run=%s approval=%s decision=%s actor=%s", response.Code, controller.runID, controller.approvalID, controller.decision, controller.actor)
	}
}

type fakeFreewillChildApprovals struct {
	listedParent, listedChild, parent, child, approvalID, decision, actor, reason string
	historyParent, historyChild                                                   string
}

func (f *fakeFreewillChildApprovals) ListChildApprovals(_ context.Context, parent, child string) ([]FreewillChildApproval, error) {
	f.listedParent, f.listedChild = parent, child
	return []FreewillChildApproval{{ParentRunID: parent, ChildRunID: child, ApprovalID: "patch-1", ProposalID: "proposal-1", Status: "pending", TargetPath: "approved.txt", Patch: "diff --git a/approved.txt b/approved.txt",
		SandboxBinding: FreewillChildSandboxBinding{TaskID: "task-1", ExecutionKey: "exec-1", AgentID: "lumi", WorkspaceID: "workspace-1", WorkspacePath: "C:/sandbox", BaseSHA: "base-sha", BaseTree: "base-tree", ArtifactDigest: "sha256:artifact"},
		PatchPlan:      FreewillChildPatchPlan{BaseSHA: "base-sha", BaseTree: "base-tree", ExpectedTree: "expected-tree", PatchSHA256: "patch-sha", Paths: []string{"approved.txt"}}}}, nil
}

func (f *fakeFreewillChildApprovals) DecideChildApproval(_ context.Context, parent, child, approvalID, decision, actor, reason string) (FreewillChildApproval, error) {
	f.parent, f.child, f.approvalID, f.decision, f.actor, f.reason = parent, child, approvalID, decision, actor, reason
	return FreewillChildApproval{ParentRunID: parent, ChildRunID: child, ApprovalID: approvalID, Status: decision + "ed"}, nil
}

func (f *fakeFreewillChildApprovals) ListChildApprovalHistory(_ context.Context, parent, child string) ([]FreewillChildApprovalDecision, error) {
	f.historyParent, f.historyChild = parent, child
	decisions := make([]FreewillChildApprovalDecision, 25)
	for i := range decisions {
		decision, status, actor, reason := "grant", "approved", "reviewer", "approved after review"
		if i%2 == 1 {
			decision, status, actor, reason = "deny", "denied", "reviewer-2", "changes exceeded agreed scope"
		}
		decisions[i] = FreewillChildApprovalDecision{ParentRunID: parent, ChildRunID: child,
			ApprovalID: "approval-" + strconv.Itoa(i), ProposalID: "proposal-1", Decision: decision, Status: status,
			Actor: actor, Reason: reason, DecidedAt: time.Date(2026, 11, 9, 12, 0, i, 0, time.UTC),
			Paths: []string{"approved.txt"}, PatchSHA256: "aabbcc"}
	}
	return decisions, nil
}

func TestFreewillChildPatchApprovalIsAuthenticatedAndBoundToChild(t *testing.T) {
	controller := &fakeFreewillChildApprovals{}
	s := NewServer(Config{Addr: ":0", AuthToken: "secret", FreewillChildApprovals: controller})
	h := s.authMiddleware(s.mux)
	path := "/api/v1/freewill/runs/parent-1/children/child-1/approvals"
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, path, nil))
	if unauth.Code != http.StatusUnauthorized || controller.listedChild != "" {
		t.Fatalf("unauthorized list status=%d controller=%+v", unauth.Code, controller)
	}
	get := httptest.NewRequest(http.MethodGet, path, nil)
	get.Header.Set("Authorization", "Bearer secret")
	listed := httptest.NewRecorder()
	h.ServeHTTP(listed, get)
	if listed.Code != http.StatusOK || controller.listedParent != "parent-1" || controller.listedChild != "child-1" ||
		!strings.Contains(listed.Body.String(), "diff --git a/approved.txt") ||
		!strings.Contains(listed.Body.String(), `"artifact_digest":"sha256:artifact"`) ||
		!strings.Contains(listed.Body.String(), `"patch_sha256":"patch-sha"`) ||
		!strings.Contains(listed.Body.String(), `"expected_tree":"expected-tree"`) {
		t.Fatalf("list status=%d controller=%+v body=%s", listed.Code, controller, listed.Body.String())
	}
	decisionPath := path + "/patch-1/decision?by=owner"
	unauthPost := httptest.NewRecorder()
	h.ServeHTTP(unauthPost, httptest.NewRequest(http.MethodPost, decisionPath, strings.NewReader(`{"decision":"grant"}`)))
	if unauthPost.Code != http.StatusUnauthorized || controller.decision != "" {
		t.Fatalf("unauthorized decision status=%d controller=%+v", unauthPost.Code, controller)
	}
	post := httptest.NewRequest(http.MethodPost, decisionPath, strings.NewReader(`{"decision":"grant","reason":"reviewed"}`))
	post.Header.Set("Authorization", "Bearer secret")
	decided := httptest.NewRecorder()
	h.ServeHTTP(decided, post)
	if decided.Code != http.StatusOK || controller.parent != "parent-1" || controller.child != "child-1" || controller.approvalID != "patch-1" || controller.decision != "grant" || controller.actor != "owner" || controller.reason != "reviewed" {
		t.Fatalf("decision status=%d controller=%+v body=%s", decided.Code, controller, decided.Body.String())
	}
}

func TestFreewillChildApprovalHistoryIsAuthenticatedBoundedAndReplayable(t *testing.T) {
	controller := &fakeFreewillChildApprovals{}
	s := NewServer(Config{Addr: ":0", AuthToken: "secret", FreewillChildApprovals: controller})
	h := s.authMiddleware(s.mux)
	path := "/api/v1/freewill/runs/parent-1/children/child-1/approvals/history"
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, path, nil))
	if unauth.Code != http.StatusUnauthorized || controller.historyParent != "" || controller.historyChild != "" {
		t.Fatalf("unauthorized history status=%d controller=%+v", unauth.Code, controller)
	}
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		return response
	}
	first := request()
	if first.Code != http.StatusOK || controller.historyParent != "parent-1" || controller.historyChild != "child-1" {
		t.Fatalf("history status=%d controller=%+v body=%s", first.Code, controller, first.Body.String())
	}
	var body struct {
		Decisions []FreewillChildApprovalDecision `json:"decisions"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Decisions) != maxFreewillChildApprovalHistory {
		t.Fatalf("history length=%d want cap=%d", len(body.Decisions), maxFreewillChildApprovalHistory)
	}
	if body.Decisions[0].Decision != "grant" || body.Decisions[0].Actor != "reviewer" || body.Decisions[0].Reason != "approved after review" ||
		body.Decisions[1].Decision != "deny" || body.Decisions[1].Actor != "reviewer-2" || body.Decisions[1].Reason != "changes exceeded agreed scope" {
		t.Fatalf("history omitted final grant/deny receipt fields: %+v", body.Decisions[:2])
	}
	second := request()
	if second.Code != http.StatusOK || second.Body.String() != first.Body.String() {
		t.Fatalf("history replay changed response: first=%s second=%s", first.Body.String(), second.Body.String())
	}
}
