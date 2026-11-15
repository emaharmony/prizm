package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

// This opt-in test helper seeds a synthetic local diary into the same SQLite
// and Markdown paths used by `prizm serve`, without adding a production seed
// route or invoking a model/provider. It is used only for the bounded served
// acceptance harness when E34_STANDALONE_SEED_DIR is set.
func TestE34SeedStandaloneDiaryFixture(t *testing.T) {
	dataDir := strings.TrimSpace(os.Getenv("E34_STANDALONE_SEED_DIR"))
	if dataDir == "" {
		t.Skip("standalone acceptance fixture is opt-in")
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(dataDir, "synthetic-project")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	const project, agent, intention, parent = "synthetic-project", "synthetic-agent", "synthetic-intention", "synthetic-parent"
	const owner = "synthetic-owner-a"
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "freewill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, project, workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	fact := multiagent.FreewillTerminalFact{ProjectID: project, Workspace: workspace, AgentID: agent, IntentionID: intention, ParentRunID: parent,
		ChildRunID: "synthetic-child", Status: "completed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64), ValidationSummary: "passed",
		RecordedAt: time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)}
	if err := facts.CaptureFreewillTerminalFact(context.Background(), fact); err != nil {
		t.Fatal(err)
	}
	diary, err := newFreewillTerminalDiarySink(facts, &freewillRetrospectiveFake{narrative: "synthetic standalone fixture", provider: "local-fake", model: "synthetic", version: "v1"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := diary.CaptureFreewillTerminalRetrospective(context.Background(), fact); err != nil {
		t.Fatal(err)
	}
	entries, err := diary.ReadOwnerEntries(context.Background(), owner, project, workspace, intention, agent, parent)
	if err != nil || len(entries) != 1 {
		t.Fatalf("synthetic fixture entries=%d err=%v", len(entries), err)
	}
	annotationSink, err := newFreewillDiaryAnnotationSink(facts, owner, project, workspace, agent)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := annotationSink.ReadDiaryAnnotations(context.Background(), owner, project, workspace, agent, intention, parent, entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	metadata := struct {
		EntryID     string `json:"entry_id"`
		WorkspaceID string `json:"workspace_id"`
		ProjectID   string `json:"project_id"`
		Workspace   string `json:"workspace"`
		AgentID     string `json:"agent_id"`
		IntentionID string `json:"intention_id"`
		ParentRunID string `json:"parent_run_id"`
	}{entries[0].ID, entry.WorkspaceID, project, workspace, agent, intention, parent}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "e34-fixture-metadata.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestFreewillDiaryConsentProductionAPICompositionReopensFrozenShare(t *testing.T) {
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	const project, agent, intention, parent = "project-a", "agent-a", "intention-a", "parent-a"
	const owner, recipient = "synthetic-owner-a", "synthetic-recipient-b"
	facts, err := newFreewillTerminalFactMemorySink(dataDir, project, workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	fact := multiagent.FreewillTerminalFact{ProjectID: project, Workspace: workspace, AgentID: agent, IntentionID: intention, ParentRunID: parent,
		ChildRunID: "child-a", Status: "completed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64), ValidationSummary: "passed",
		RecordedAt: time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)}
	if err := facts.CaptureFreewillTerminalFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	diary, err := newFreewillTerminalDiarySink(facts, &freewillRetrospectiveFake{narrative: "Synthetic local diary narrative", provider: "local-fake", model: "synthetic", version: "v1"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := diary.CaptureFreewillTerminalRetrospective(ctx, fact); err != nil {
		t.Fatal(err)
	}
	entries, err := diary.ReadOwnerEntries(ctx, owner, project, workspace, intention, agent, parent)
	if err != nil || len(entries) != 1 {
		t.Fatalf("source diary entries=%d err=%v", len(entries), err)
	}
	entryID := entries[0].ID
	annotations, err := newFreewillDiaryAnnotationSink(facts, owner, project, workspace, agent)
	if err != nil {
		t.Fatal(err)
	}
	ownerEntry, err := annotations.ReadDiaryAnnotations(ctx, owner, project, workspace, agent, intention, parent, entryID)
	if err != nil {
		t.Fatal(err)
	}
	consentDirectory := filepath.Join(dataDir, "freewill_diary_consent")
	consent, err := newFreewillDiaryConsentController(consentDirectory, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if consent != nil && consent.store != nil {
			_ = consent.store.Close()
		}
	}()
	ownerToken, recipientToken, serviceToken := "synthetic-owner-opaque", "synthetic-recipient-opaque", "service-wide-opaque"
	localConfig := orchestrator.LocalDiaryPrincipalAuthConfig{Enabled: true, OwnerPrincipalID: owner, Credentials: []orchestrator.LocalDiaryPrincipalCredentialConfig{
		{PrincipalID: owner, TokenSHA256: diaryCredentialDigest(ownerToken)},
		{PrincipalID: recipient, TokenSHA256: diaryCredentialDigest(recipientToken)},
	}}
	newHandler := func(controller *freewillDiaryConsentController) http.Handler {
		server, err := api.NewServerChecked(api.Config{Addr: "127.0.0.1:0", AuthToken: serviceToken,
			FreewillDiaryAnnotations: annotations, FreewillDiaryConsent: controller,
			LocalDiaryPrincipalAuth: freewillLocalDiaryPrincipalAuthFromConfig(localConfig),
		})
		if err != nil {
			t.Fatal(err)
		}
		return server.Handler()
	}
	handler := newHandler(consent)
	grantRequestBody := fmt.Sprintf(`{"recipient_principal_id":%q,"project_id":%q,"workspace":%q,"workspace_id":%q,"agent_id":%q,"intention_id":%q,"parent_run_id":%q,"entry_id":%q,"expected_revision":1,"expires_at":%q}`,
		recipient, project, workspace, ownerEntry.WorkspaceID, agent, intention, parent, entryID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	grantRequest := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/diary/shares", strings.NewReader(grantRequestBody))
	grantRequest.Header.Set("Authorization", "Bearer "+ownerToken)
	grantResponse := httptest.NewRecorder()
	handler.ServeHTTP(grantResponse, grantRequest)
	if grantResponse.Code != http.StatusOK {
		t.Fatalf("share grant status=%d body=%s", grantResponse.Code, grantResponse.Body.String())
	}
	var grantResponseBody struct {
		Grant api.FreewillDiaryShareGrant `json:"grant"`
	}
	if err := json.Unmarshal(grantResponse.Body.Bytes(), &grantResponseBody); err != nil || grantResponseBody.Grant.GrantID == "" || grantResponseBody.Grant.Revision != 1 {
		t.Fatalf("share grant response=%s err=%v", grantResponse.Body.String(), err)
	}
	sharedPath := "/api/v1/freewill/diary/shares/" + grantResponseBody.Grant.GrantID
	ownerAnnotation := httptest.NewRequest(http.MethodPost, "/api/v1/freewill/diary/entries/"+entryID,
		strings.NewReader(fmt.Sprintf(`{"owner_id":%q,"project_id":%q,"workspace":%q,"agent_id":%q,"intention_id":%q,"parent_run_id":%q,"expected_revision":1,"text":"later synthetic annotation"}`,
			owner, project, workspace, agent, intention, parent)))
	ownerAnnotation.Header.Set("Authorization", "Bearer "+ownerToken)
	ownerAnnotationResponse := httptest.NewRecorder()
	handler.ServeHTTP(ownerAnnotationResponse, ownerAnnotation)
	if ownerAnnotationResponse.Code != http.StatusOK || !strings.Contains(ownerAnnotationResponse.Body.String(), `"revision":2`) {
		t.Fatalf("later owner revision status=%d body=%s", ownerAnnotationResponse.Code, ownerAnnotationResponse.Body.String())
	}

	consent.store.Close()
	consent, err = newFreewillDiaryConsentController(consentDirectory, owner)
	if err != nil {
		t.Fatal(err)
	}
	handler = newHandler(consent)
	recipientRead := httptest.NewRequest(http.MethodGet, sharedPath, nil)
	recipientRead.Header.Set("Authorization", "Bearer "+recipientToken)
	recipientResponse := httptest.NewRecorder()
	handler.ServeHTTP(recipientResponse, recipientRead)
	if recipientResponse.Code != http.StatusOK || !strings.Contains(recipientResponse.Body.String(), `"revision":1`) || strings.Contains(recipientResponse.Body.String(), "later synthetic annotation") {
		t.Fatalf("reopened frozen share status=%d body=%s", recipientResponse.Code, recipientResponse.Body.String())
	}
	revoke := httptest.NewRequest(http.MethodDelete, sharedPath, nil)
	revoke.Header.Set("Authorization", "Bearer "+ownerToken)
	revokeResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusOK {
		t.Fatalf("share revoke status=%d body=%s", revokeResponse.Code, revokeResponse.Body.String())
	}
	consent.store.Close()
	consent, err = newFreewillDiaryConsentController(consentDirectory, owner)
	if err != nil {
		t.Fatal(err)
	}
	handler = newHandler(consent)
	recipientRead = httptest.NewRequest(http.MethodGet, sharedPath, nil)
	recipientRead.Header.Set("Authorization", "Bearer "+recipientToken)
	recipientResponse = httptest.NewRecorder()
	handler.ServeHTTP(recipientResponse, recipientRead)
	if recipientResponse.Code != http.StatusNotFound {
		t.Fatalf("reopened revoked share status=%d want=404", recipientResponse.Code)
	}
}

func diaryCredentialDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
