package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/freewill"
)

const (
	diaryOwnerPrincipal     = "synthetic-owner-a"
	diaryRecipientPrincipal = "synthetic-recipient-b"
)

type principalDiaryAnnotations struct {
	entry   FreewillDiaryAnnotationEntry
	reads   int
	appends int
}

func (f *principalDiaryAnnotations) ReadDiaryAnnotations(_ context.Context, owner, project, workspace, agent, intention, parent, entryID string) (FreewillDiaryAnnotationEntry, error) {
	f.reads++
	if owner != diaryOwnerPrincipal || project != f.entry.ProjectID || workspace != f.entry.WorkspaceID || agent != f.entry.AgentID || intention != f.entry.IntentionID || parent != f.entry.ParentRunID || entryID != f.entry.EntryID {
		return FreewillDiaryAnnotationEntry{}, errStrategyExperimentScope
	}
	return cloneDiaryAnnotationEntry(f.entry)
}

func (f *principalDiaryAnnotations) AppendDiaryAnnotation(_ context.Context, owner, project, workspace, agent, intention, parent, entryID string, revision int, text string) (FreewillDiaryAnnotationEntry, error) {
	f.appends++
	if owner != diaryOwnerPrincipal || project != f.entry.ProjectID || workspace != f.entry.WorkspaceID || agent != f.entry.AgentID || intention != f.entry.IntentionID || parent != f.entry.ParentRunID || entryID != f.entry.EntryID || revision != f.entry.Revision || strings.TrimSpace(text) == "" {
		return FreewillDiaryAnnotationEntry{}, errStrategyExperimentScope
	}
	f.entry.Revision++
	f.entry.Annotations = append(f.entry.Annotations, FreewillDiaryAnnotation{Revision: f.entry.Revision, Previous: revision, Text: text})
	return cloneDiaryAnnotationEntry(f.entry)
}

func cloneDiaryAnnotationEntry(entry FreewillDiaryAnnotationEntry) (FreewillDiaryAnnotationEntry, error) {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return FreewillDiaryAnnotationEntry{}, err
	}
	var clone FreewillDiaryAnnotationEntry
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return FreewillDiaryAnnotationEntry{}, err
	}
	return clone, nil
}

type principalDiaryShareState struct {
	grant   FreewillDiaryShareGrant
	entry   FreewillDiaryAnnotationEntry
	revoked bool
}

type principalDiaryShareAudit struct {
	principal, action, grantID, entryID, decision string
	revision                                      int
}

type principalDiaryConsent struct {
	owner     string
	grants    map[string]principalDiaryShareState
	replies   []FreewillDiaryShareReply
	audits    []principalDiaryShareAudit
	sequence  int
	failAudit bool
}

func (f *principalDiaryConsent) CreateDiaryShare(_ context.Context, actor, recipient string, entry FreewillDiaryAnnotationEntry, expires, now time.Time) (FreewillDiaryShareGrant, error) {
	if f.failAudit {
		return FreewillDiaryShareGrant{}, freewill.ErrDiaryConsentAuditUnavailable
	}
	if actor != f.owner || recipient != diaryRecipientPrincipal || !expires.After(now) || expires.After(now.Add(24*time.Hour)) {
		f.record(actor, "share.grant", "", entry.EntryID, entry.Revision, "denied")
		return FreewillDiaryShareGrant{}, fmt.Errorf("denied")
	}
	f.sequence++
	encoded, _ := json.Marshal(entry)
	digest := sha256.Sum256(encoded)
	grant := FreewillDiaryShareGrant{GrantID: fmt.Sprintf("grant-%d", f.sequence), RecipientPrincipalID: recipient, EntryID: entry.EntryID,
		Revision: entry.Revision, ProjectID: entry.ProjectID, WorkspaceID: entry.WorkspaceID, AgentID: entry.AgentID,
		IntentionID: entry.IntentionID, ParentRunID: entry.ParentRunID, EntrySHA256: hex.EncodeToString(digest[:]), GrantedAt: now.UTC(), ExpiresAt: expires.UTC()}
	snapshot, _ := cloneDiaryAnnotationEntry(entry)
	f.grants[grant.GrantID] = principalDiaryShareState{grant: grant, entry: snapshot}
	f.record(actor, "share.grant", grant.GrantID, entry.EntryID, entry.Revision, "allowed")
	return grant, nil
}

func (f *principalDiaryConsent) RevokeDiaryShare(_ context.Context, actor, grantID string, now time.Time) (FreewillDiaryShareGrant, error) {
	if f.failAudit {
		return FreewillDiaryShareGrant{}, freewill.ErrDiaryConsentAuditUnavailable
	}
	state, ok := f.grants[grantID]
	if !ok || actor != f.owner || state.revoked {
		f.record(actor, "share.revoke", grantID, "", 0, "denied")
		return FreewillDiaryShareGrant{}, fmt.Errorf("denied")
	}
	state.revoked = true
	revoked := now.UTC()
	state.grant.RevokedAt = &revoked
	f.grants[grantID] = state
	f.record(actor, "share.revoke", grantID, state.grant.EntryID, state.grant.Revision, "allowed")
	return state.grant, nil
}

func (f *principalDiaryConsent) ReadDiaryShare(_ context.Context, actor, grantID string, now time.Time) (FreewillDiaryShareGrant, FreewillDiaryAnnotationEntry, error) {
	if f.failAudit {
		return FreewillDiaryShareGrant{}, FreewillDiaryAnnotationEntry{}, freewill.ErrDiaryConsentAuditUnavailable
	}
	state, ok := f.grants[grantID]
	if !ok || actor != diaryRecipientPrincipal || state.revoked || !now.Before(state.grant.ExpiresAt) {
		f.record(actor, "share.read", grantID, "", 0, "denied")
		return FreewillDiaryShareGrant{}, FreewillDiaryAnnotationEntry{}, fmt.Errorf("denied")
	}
	f.record(actor, "share.read", grantID, state.grant.EntryID, state.grant.Revision, "allowed")
	entry, err := cloneDiaryAnnotationEntry(state.entry)
	return state.grant, entry, err
}

func (f *principalDiaryConsent) RecordDiaryConsentDecision(_ context.Context, principal, action, grantID, entryID string, revision int, decision string, _ time.Time) error {
	if f.failAudit {
		return errors.New("synthetic audit unavailable")
	}
	f.record(principal, action, grantID, entryID, revision, decision)
	return nil
}

func (f *principalDiaryConsent) CreateDiaryShareReply(_ context.Context, actor string, expected FreewillDiaryShareGrant, requestID, text string, now time.Time) (FreewillDiaryShareReply, error) {
	if f.failAudit {
		return FreewillDiaryShareReply{}, freewill.ErrDiaryConsentAuditUnavailable
	}
	state, ok := f.grants[expected.GrantID]
	grant := state.grant
	if !ok || actor != grant.RecipientPrincipalID || state.revoked || !now.Before(grant.ExpiresAt) ||
		grant.EntryID != expected.EntryID || grant.Revision != expected.Revision || grant.EntrySHA256 != expected.EntrySHA256 || grant.ProjectID != expected.ProjectID ||
		grant.WorkspaceID != expected.WorkspaceID || grant.AgentID != expected.AgentID || grant.IntentionID != expected.IntentionID || grant.ParentRunID != expected.ParentRunID ||
		requestID == "" || len(requestID) > 128 || strings.TrimSpace(text) == "" || len(text) > freewill.DiaryShareReplyMaxBytes {
		f.record(actor, "share.reply", expected.GrantID, expected.EntryID, expected.Revision, "denied")
		return FreewillDiaryShareReply{}, errors.New("denied")
	}
	for _, prior := range f.replies {
		if prior.GrantID == expected.GrantID && prior.RequestID == requestID {
			f.record(actor, "share.reply", expected.GrantID, expected.EntryID, expected.Revision, "denied")
			return FreewillDiaryShareReply{}, errors.New("replay")
		}
	}
	reply := FreewillDiaryShareReply{ReplyID: fmt.Sprintf("reply-%d", len(f.replies)+1), RequestID: requestID, GrantID: grant.GrantID,
		RecipientPrincipalID: actor, EntryID: grant.EntryID, Revision: grant.Revision, EntrySHA256: grant.EntrySHA256,
		ProjectID: grant.ProjectID, WorkspaceID: grant.WorkspaceID, AgentID: grant.AgentID, IntentionID: grant.IntentionID, ParentRunID: grant.ParentRunID,
		Text: text, CreatedAt: now.UTC()}
	f.replies = append(f.replies, reply)
	f.record(actor, "share.reply", expected.GrantID, expected.EntryID, expected.Revision, "allowed")
	return reply, nil
}

func (f *principalDiaryConsent) ReadDiaryShareReplies(_ context.Context, actor, grantID string, _ time.Time) ([]FreewillDiaryShareReply, error) {
	if f.failAudit {
		return nil, freewill.ErrDiaryConsentAuditUnavailable
	}
	state, ok := f.grants[grantID]
	if !ok || actor != f.owner {
		f.record(actor, "share.replies.read", grantID, "", 0, "denied")
		return nil, errors.New("denied")
	}
	var result []FreewillDiaryShareReply
	for _, reply := range f.replies {
		if reply.GrantID == grantID {
			result = append(result, reply)
		}
	}
	f.record(actor, "share.replies.read", grantID, state.grant.EntryID, state.grant.Revision, "allowed")
	return result, nil
}

func (f *principalDiaryConsent) record(principal, action, grantID, entryID string, revision int, decision string) {
	f.audits = append(f.audits, principalDiaryShareAudit{principal: principal, action: action, grantID: grantID, entryID: entryID, revision: revision, decision: decision})
}

func TestFreewillDiaryPrincipalModeBindsOwnerAndFrozenShare(t *testing.T) {
	ownerToken := "synthetic-owner-token-opaque"
	recipientToken := "synthetic-recipient-token-opaque"
	serviceToken := "service-wide-bearer-opaque"
	annotations := &principalDiaryAnnotations{entry: FreewillDiaryAnnotationEntry{
		EntryID: "entry-a", Revision: 2, ProjectID: "project-a", WorkspaceID: "/synthetic/workspace", AgentID: "agent-a",
		IntentionID: "intention-a", ParentRunID: "parent-a", OriginalText: "synthetic diary text",
		Annotations: []FreewillDiaryAnnotation{{Revision: 2, Previous: 1, Text: "synthetic owner note"}},
	}}
	consent := &principalDiaryConsent{owner: diaryOwnerPrincipal, grants: make(map[string]principalDiaryShareState)}
	s := NewServer(Config{Addr: "127.0.0.1:0", AuthToken: serviceToken, FreewillDiaryAnnotations: annotations, FreewillDiaryConsent: consent,
		LocalDiaryPrincipalAuth: &LocalFreewillDiaryPrincipalAuth{Enabled: true, OwnerPrincipalID: diaryOwnerPrincipal, Credentials: []LocalFreewillDiaryPrincipalCredential{
			{PrincipalID: diaryOwnerPrincipal, TokenSHA256: diaryTokenDigest(ownerToken)},
			{PrincipalID: diaryRecipientPrincipal, TokenSHA256: diaryTokenDigest(recipientToken)},
		}},
	})
	h := s.Handler()
	base := "/api/v1/freewill/diary/entries/entry-a"
	query := url.Values{"project_id": {"project-a"}, "workspace": {"/synthetic/workspace"}, "agent_id": {"agent-a"}, "intention_id": {"intention-a"}, "parent_run_id": {"parent-a"}}
	ownerReadURL := base + "?" + query.Encode()

	for _, tc := range []struct {
		name  string
		token string
		query string
		want  int
	}{
		{name: "service bearer does not impersonate principal", token: serviceToken, want: http.StatusUnauthorized},
		{name: "recipient cannot read live owner entry", token: recipientToken, want: http.StatusForbidden},
		{name: "recipient cannot spoof owner in query", token: recipientToken, query: "&owner_id=" + diaryOwnerPrincipal, want: http.StatusForbidden},
		{name: "header principal cannot combine query token", token: ownerToken, query: "&token=" + serviceToken, want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, ownerReadURL+tc.query, nil)
			request.Header.Set("Authorization", "Bearer "+tc.token)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.want, response.Body.String())
			}
		})
	}
	if annotations.reads != 0 {
		t.Fatalf("unauthorized/live recipient reads reached diary controller: %d", annotations.reads)
	}
	queryOnly := httptest.NewRequest(http.MethodGet, ownerReadURL+"&token="+url.QueryEscape(serviceToken), nil)
	queryOnlyResponse := httptest.NewRecorder()
	h.ServeHTTP(queryOnlyResponse, queryOnly)
	if queryOnlyResponse.Code != http.StatusUnauthorized {
		t.Fatalf("query-only service bearer status=%d want=401", queryOnlyResponse.Code)
	}
	multipleHeaders := httptest.NewRequest(http.MethodGet, ownerReadURL, nil)
	multipleHeaders.Header.Add("Authorization", "Bearer "+ownerToken)
	multipleHeaders.Header.Add("Authorization", "Bearer "+recipientToken)
	multipleHeadersResponse := httptest.NewRecorder()
	h.ServeHTTP(multipleHeadersResponse, multipleHeaders)
	if multipleHeadersResponse.Code != http.StatusUnauthorized {
		t.Fatalf("multiple Authorization headers status=%d want=401", multipleHeadersResponse.Code)
	}

	ownerRead := httptest.NewRequest(http.MethodGet, ownerReadURL, nil)
	ownerRead.Header.Set("Authorization", "Bearer "+ownerToken)
	ownerReadResponse := httptest.NewRecorder()
	h.ServeHTTP(ownerReadResponse, ownerRead)
	if ownerReadResponse.Code != http.StatusOK || !strings.Contains(ownerReadResponse.Body.String(), "synthetic diary text") {
		t.Fatalf("owner read status=%d body=%s", ownerReadResponse.Code, ownerReadResponse.Body.String())
	}

	grantBody := fmt.Sprintf(`{"recipient_principal_id":%q,"project_id":"project-a","workspace":"/synthetic/workspace","workspace_id":"/synthetic/workspace","agent_id":"agent-a","intention_id":"intention-a","parent_run_id":"parent-a","entry_id":"entry-a","expected_revision":2,"expires_at":%q}`,
		diaryRecipientPrincipal, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	grantRequest := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/diary/shares", strings.NewReader(grantBody))
	grantRequest.Header.Set("Authorization", "Bearer "+ownerToken)
	grantResponse := httptest.NewRecorder()
	h.ServeHTTP(grantResponse, grantRequest)
	if grantResponse.Code != http.StatusOK {
		t.Fatalf("grant status=%d body=%s", grantResponse.Code, grantResponse.Body.String())
	}
	grantID := "grant-1"
	sharedPath := "/api/v1/freewill/diary/shares/" + grantID
	grant := consent.grants[grantID].grant
	replyBody, _ := json.Marshal(map[string]any{"request_id": "reply-request-1", "grant_id": grant.GrantID, "entry_id": grant.EntryID,
		"revision": grant.Revision, "entry_sha256": grant.EntrySHA256, "project_id": grant.ProjectID, "workspace_id": grant.WorkspaceID,
		"agent_id": grant.AgentID, "intention_id": grant.IntentionID, "parent_run_id": grant.ParentRunID, "text": "Unverified recipient account of the frozen entry."})
	replyPath := sharedPath + "/replies"
	createReply := func(token string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, replyPath, strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		return response
	}
	replyResponse := createReply(recipientToken, replyBody)
	if replyResponse.Code != http.StatusOK || !strings.Contains(replyResponse.Body.String(), `"revision":2`) || !strings.Contains(replyResponse.Body.String(), "Unverified recipient account") {
		t.Fatalf("reply create status=%d body=%s", replyResponse.Code, replyResponse.Body.String())
	}
	if replay := createReply(recipientToken, replyBody); replay.Code != http.StatusConflict {
		t.Fatalf("same request replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	if ownerReply := createReply(ownerToken, replyBody); ownerReply.Code != http.StatusConflict {
		t.Fatalf("owner-as-recipient reply status=%d", ownerReply.Code)
	}
	staleBody := append([]byte(nil), replyBody...)
	var stale map[string]any
	_ = json.Unmarshal(staleBody, &stale)
	stale["request_id"], stale["revision"] = "reply-request-stale", float64(3)
	staleBody, _ = json.Marshal(stale)
	if staleResponse := createReply(recipientToken, staleBody); staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale revision status=%d body=%s", staleResponse.Code, staleResponse.Body.String())
	}
	for _, tc := range []struct {
		name, token string
		want        int
	}{{"recipient cannot read owner history", recipientToken, http.StatusNotFound}, {"service bearer cannot reply", serviceToken, http.StatusUnauthorized}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, replyPath, nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("history status=%d want=%d body=%s", res.Code, tc.want, res.Body.String())
			}
		})
	}
	ownerHistory := httptest.NewRequest(http.MethodGet, replyPath, nil)
	ownerHistory.Header.Set("Authorization", "Bearer "+ownerToken)
	ownerHistoryResponse := httptest.NewRecorder()
	h.ServeHTTP(ownerHistoryResponse, ownerHistory)
	if ownerHistoryResponse.Code != http.StatusOK || !strings.Contains(ownerHistoryResponse.Body.String(), "Unverified recipient account") {
		t.Fatalf("owner history status=%d body=%s", ownerHistoryResponse.Code, ownerHistoryResponse.Body.String())
	}
	recipientRead := httptest.NewRequest(http.MethodGet, sharedPath, nil)
	recipientRead.Header.Set("Authorization", "Bearer "+recipientToken)
	recipientResponse := httptest.NewRecorder()
	h.ServeHTTP(recipientResponse, recipientRead)
	if recipientResponse.Code != http.StatusOK || !strings.Contains(recipientResponse.Body.String(), `"revision":2`) || !strings.Contains(recipientResponse.Body.String(), "synthetic owner note") {
		t.Fatalf("frozen recipient read status=%d body=%s", recipientResponse.Code, recipientResponse.Body.String())
	}

	appendBody := `{"owner_id":"synthetic-owner-a","project_id":"project-a","workspace":"/synthetic/workspace","agent_id":"agent-a","intention_id":"intention-a","parent_run_id":"parent-a","expected_revision":2,"text":"new owner revision"}`
	appendRequest := httptest.NewRequest(http.MethodPost, base, strings.NewReader(appendBody))
	appendRequest.Header.Set("Authorization", "Bearer "+ownerToken)
	appendResponse := httptest.NewRecorder()
	h.ServeHTTP(appendResponse, appendRequest)
	if appendResponse.Code != http.StatusOK || !strings.Contains(appendResponse.Body.String(), `"revision":3`) {
		t.Fatalf("owner append status=%d body=%s", appendResponse.Code, appendResponse.Body.String())
	}
	recipientRead = httptest.NewRequest(http.MethodGet, sharedPath, nil)
	recipientRead.Header.Set("Authorization", "Bearer "+recipientToken)
	recipientResponse = httptest.NewRecorder()
	h.ServeHTTP(recipientResponse, recipientRead)
	if recipientResponse.Code != http.StatusOK || !strings.Contains(recipientResponse.Body.String(), `"revision":2`) || strings.Contains(recipientResponse.Body.String(), "new owner revision") {
		t.Fatalf("share changed after live revision status=%d body=%s", recipientResponse.Code, recipientResponse.Body.String())
	}

	recipientWrite := httptest.NewRequest(http.MethodPost, base, strings.NewReader(appendBody))
	recipientWrite.Header.Set("Authorization", "Bearer "+recipientToken)
	recipientWriteResponse := httptest.NewRecorder()
	h.ServeHTTP(recipientWriteResponse, recipientWrite)
	if recipientWriteResponse.Code != http.StatusForbidden || annotations.appends != 1 {
		t.Fatalf("recipient write status=%d appends=%d", recipientWriteResponse.Code, annotations.appends)
	}
	spoofBody := strings.Replace(appendBody, `"owner_id":"synthetic-owner-a"`, `"owner_id":"synthetic-other"`, 1)
	spoof := httptest.NewRequest(http.MethodPost, base, strings.NewReader(spoofBody))
	spoof.Header.Set("Authorization", "Bearer "+ownerToken)
	spoofResponse := httptest.NewRecorder()
	h.ServeHTTP(spoofResponse, spoof)
	if spoofResponse.Code != http.StatusForbidden || annotations.appends != 1 {
		t.Fatalf("owner field spoof status=%d appends=%d", spoofResponse.Code, annotations.appends)
	}

	revoke := httptest.NewRequest(http.MethodDelete, sharedPath, nil)
	revoke.Header.Set("Authorization", "Bearer "+ownerToken)
	revokeResponse := httptest.NewRecorder()
	h.ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", revokeResponse.Code, revokeResponse.Body.String())
	}
	recipientRead = httptest.NewRequest(http.MethodGet, sharedPath, nil)
	recipientRead.Header.Set("Authorization", "Bearer "+recipientToken)
	recipientResponse = httptest.NewRecorder()
	h.ServeHTTP(recipientResponse, recipientRead)
	if recipientResponse.Code != http.StatusNotFound {
		t.Fatalf("revoked share read status=%d want=404", recipientResponse.Code)
	}
	if afterRevoke := createReply(recipientToken, []byte(strings.ReplaceAll(string(replyBody), "reply-request-1", "reply-after-revoke"))); afterRevoke.Code != http.StatusConflict {
		t.Fatalf("reply after revoke status=%d body=%s", afterRevoke.Code, afterRevoke.Body.String())
	}
	ownerHistory = httptest.NewRequest(http.MethodGet, replyPath, nil)
	ownerHistory.Header.Set("Authorization", "Bearer "+ownerToken)
	ownerHistoryResponse = httptest.NewRecorder()
	h.ServeHTTP(ownerHistoryResponse, ownerHistory)
	if ownerHistoryResponse.Code != http.StatusOK || !strings.Contains(ownerHistoryResponse.Body.String(), "Unverified recipient account") {
		t.Fatalf("owner history after revoke status=%d body=%s", ownerHistoryResponse.Code, ownerHistoryResponse.Body.String())
	}
	if len(consent.audits) < 7 {
		t.Fatalf("expected metadata-only decisions for reads, grant, writes, and revoke; got %d", len(consent.audits))
	}
	for _, audit := range consent.audits {
		if strings.Contains(fmt.Sprint(audit), "synthetic diary text") || strings.Contains(fmt.Sprint(audit), "synthetic owner note") || strings.Contains(fmt.Sprint(audit), "Unverified recipient account") || strings.Contains(fmt.Sprint(audit), ownerToken) || strings.Contains(fmt.Sprint(audit), recipientToken) {
			t.Fatalf("audit contained content or credential metadata: %#v", audit)
		}
	}
}

func TestFreewillDiaryPrincipalConfigFailsClosedAndRejectsServiceTokenAlias(t *testing.T) {
	ownerToken, recipientToken := "owner-opaque", "recipient-opaque"
	annotations := &principalDiaryAnnotations{}
	consent := &principalDiaryConsent{owner: diaryOwnerPrincipal, grants: map[string]principalDiaryShareState{}}
	config := LocalFreewillDiaryPrincipalAuth{Enabled: true, OwnerPrincipalID: diaryOwnerPrincipal, Credentials: []LocalFreewillDiaryPrincipalCredential{
		{PrincipalID: diaryOwnerPrincipal, TokenSHA256: diaryTokenDigest(ownerToken)},
		{PrincipalID: diaryRecipientPrincipal, TokenSHA256: diaryTokenDigest(recipientToken)},
	}}
	badBind := NewServer(Config{Addr: "0.0.0.0:0", AuthToken: "service", FreewillDiaryAnnotations: annotations, FreewillDiaryConsent: consent, LocalDiaryPrincipalAuth: &config})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/freewill/diary/entries/entry-a", nil)
	request.Header.Set("Authorization", "Bearer "+ownerToken)
	response := httptest.NewRecorder()
	badBind.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || badBind.Start() == nil {
		t.Fatalf("non-loopback config must fail closed: handler=%d startErr=%v", response.Code, badBind.localDiaryPrincipalAuthErr)
	}

	alias := config
	alias.Credentials = append([]LocalFreewillDiaryPrincipalCredential(nil), config.Credentials...)
	alias.Credentials[0].TokenSHA256 = strings.ToUpper(alias.Credentials[0].TokenSHA256)
	alias.Credentials[1].TokenSHA256 = diaryTokenDigest(recipientToken)
	duplicate := NewServer(Config{Addr: "127.0.0.1:0", AuthToken: ownerToken, FreewillDiaryAnnotations: annotations, FreewillDiaryConsent: consent, LocalDiaryPrincipalAuth: &alias})
	if duplicate.localDiaryPrincipalAuthErr == nil {
		t.Fatal("service-wide bearer alias to a principal credential was accepted")
	}
	caseAlias := config
	caseAlias.Credentials = append([]LocalFreewillDiaryPrincipalCredential(nil), config.Credentials...)
	caseAlias.Credentials[1].TokenSHA256 = strings.ToUpper(caseAlias.Credentials[0].TokenSHA256)
	duplicate = NewServer(Config{Addr: "127.0.0.1:0", AuthToken: "unrelated-service-token", FreewillDiaryAnnotations: annotations, FreewillDiaryConsent: consent, LocalDiaryPrincipalAuth: &caseAlias})
	if duplicate.localDiaryPrincipalAuthErr == nil {
		t.Fatal("case-aliased duplicate credential hash was accepted")
	}
}

func TestFreewillDiaryPrincipalModeFailsClosedWhenDirectAuditFails(t *testing.T) {
	ownerToken := "synthetic-audit-failure-owner-token"
	annotations := &principalDiaryAnnotations{entry: FreewillDiaryAnnotationEntry{
		EntryID: "entry-a", Revision: 1, ProjectID: "project-a", WorkspaceID: "/synthetic/workspace", AgentID: "agent-a",
		IntentionID: "intention-a", ParentRunID: "parent-a", OriginalText: "synthetic private entry",
	}}
	consent := &principalDiaryConsent{owner: diaryOwnerPrincipal, grants: make(map[string]principalDiaryShareState), failAudit: true}
	s := NewServer(Config{Addr: "127.0.0.1:0", AuthToken: "service-bearer", FreewillDiaryAnnotations: annotations, FreewillDiaryConsent: consent,
		LocalDiaryPrincipalAuth: &LocalFreewillDiaryPrincipalAuth{Enabled: true, OwnerPrincipalID: diaryOwnerPrincipal, Credentials: []LocalFreewillDiaryPrincipalCredential{
			{PrincipalID: diaryOwnerPrincipal, TokenSHA256: diaryTokenDigest(ownerToken)},
			{PrincipalID: diaryRecipientPrincipal, TokenSHA256: diaryTokenDigest("synthetic-audit-failure-recipient-token")},
		}},
	})
	h := s.Handler()
	query := url.Values{"project_id": {"project-a"}, "workspace": {"/synthetic/workspace"}, "agent_id": {"agent-a"}, "intention_id": {"intention-a"}, "parent_run_id": {"parent-a"}}
	path := "/api/v1/freewill/diary/entries/entry-a?" + query.Encode()
	read := httptest.NewRequest(http.MethodGet, path, nil)
	read.Header.Set("Authorization", "Bearer "+ownerToken)
	readResponse := httptest.NewRecorder()
	h.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusServiceUnavailable || strings.Contains(readResponse.Body.String(), "synthetic private entry") {
		t.Fatalf("audit-failed read leaked entry: status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}
	write := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/diary/entries/entry-a", strings.NewReader(`{"owner_id":"synthetic-owner-a","project_id":"project-a","workspace":"/synthetic/workspace","agent_id":"agent-a","intention_id":"intention-a","parent_run_id":"parent-a","expected_revision":1,"text":"attempted annotation"}`))
	write.Header.Set("Authorization", "Bearer "+ownerToken)
	writeResponse := httptest.NewRecorder()
	h.ServeHTTP(writeResponse, write)
	if writeResponse.Code != http.StatusServiceUnavailable || annotations.appends != 0 {
		t.Fatalf("audit-failed write status=%d appends=%d", writeResponse.Code, annotations.appends)
	}
	malformed := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/diary/entries/entry-a", strings.NewReader("{"))
	malformed.Header.Set("Authorization", "Bearer "+ownerToken)
	malformedResponse := httptest.NewRecorder()
	h.ServeHTTP(malformedResponse, malformed)
	if malformedResponse.Code != http.StatusServiceUnavailable || annotations.appends != 0 {
		t.Fatalf("malformed owner request without durable audit status=%d appends=%d", malformedResponse.Code, annotations.appends)
	}
}

func TestFreewillDiaryShareRequiresExactWorkspaceIDAndDurableGrantAudit(t *testing.T) {
	ownerToken := "synthetic-exact-workspace-owner-token"
	annotations := &principalDiaryAnnotations{entry: FreewillDiaryAnnotationEntry{EntryID: "entry-a", Revision: 1, ProjectID: "project-a", WorkspaceID: "/synthetic/workspace", AgentID: "agent-a", IntentionID: "intention-a", ParentRunID: "parent-a", OriginalText: "synthetic diary"}}
	consent := &principalDiaryConsent{owner: diaryOwnerPrincipal, grants: make(map[string]principalDiaryShareState), failAudit: true}
	s := NewServer(Config{Addr: "127.0.0.1:0", AuthToken: "service-token", FreewillDiaryAnnotations: annotations, FreewillDiaryConsent: consent,
		LocalDiaryPrincipalAuth: &LocalFreewillDiaryPrincipalAuth{Enabled: true, OwnerPrincipalID: diaryOwnerPrincipal, Credentials: []LocalFreewillDiaryPrincipalCredential{
			{PrincipalID: diaryOwnerPrincipal, TokenSHA256: diaryTokenDigest(ownerToken)},
			{PrincipalID: diaryRecipientPrincipal, TokenSHA256: diaryTokenDigest("synthetic-exact-workspace-recipient-token")},
		}},
	})
	h := s.Handler()
	body := func(workspaceID string) string {
		return fmt.Sprintf(`{"recipient_principal_id":%q,"project_id":"project-a","workspace":"/synthetic/workspace","workspace_id":%q,"agent_id":"agent-a","intention_id":"intention-a","parent_run_id":"parent-a","entry_id":"entry-a","expected_revision":1,"expires_at":%q}`,
			diaryRecipientPrincipal, workspaceID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	}
	for _, tc := range []struct {
		name        string
		workspaceID string
		failAudit   bool
		want        int
	}{
		{name: "wrong workspace scope is denied", workspaceID: "other-workspace", want: http.StatusConflict},
		{name: "grant audit failure returns unavailable", workspaceID: "/synthetic/workspace", failAudit: true, want: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			consent.failAudit = tc.failAudit
			request := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/diary/shares", strings.NewReader(body(tc.workspaceID)))
			request.Header.Set("Authorization", "Bearer "+ownerToken)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != tc.want || len(consent.grants) != 0 {
				t.Fatalf("status=%d grants=%d body=%s", response.Code, len(consent.grants), response.Body.String())
			}
		})
	}
}

func diaryTokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
