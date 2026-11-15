package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/freewill"
)

type relationshipCleanupPendingController struct{}

func (relationshipCleanupPendingController) ProposeRelationship(context.Context, string, string, string, string, time.Time) (string, error) {
	return "", freewill.ErrRelationshipUnavailable
}
func (relationshipCleanupPendingController) ConsentRelationship(context.Context, string, string, time.Time) error {
	return freewill.ErrRelationshipPurgePending
}
func (relationshipCleanupPendingController) WriteRelationshipHypothesis(context.Context, string, string, string, string, string, time.Time) error {
	return freewill.ErrRelationshipPurgePending
}
func (relationshipCleanupPendingController) ReadRelationshipHypothesis(context.Context, string, string, time.Time) (FreewillRelationshipObservation, error) {
	return FreewillRelationshipObservation{}, freewill.ErrRelationshipPurgePending
}
func (relationshipCleanupPendingController) WithdrawRelationship(context.Context, string, string, time.Time) error {
	return freewill.ErrRelationshipPurgePending
}

type diaryCleanupPendingController struct{}

func (diaryCleanupPendingController) CreateDiaryShare(context.Context, string, string, FreewillDiaryAnnotationEntry, time.Time, time.Time) (FreewillDiaryShareGrant, error) {
	return FreewillDiaryShareGrant{}, nil
}
func (diaryCleanupPendingController) RevokeDiaryShare(context.Context, string, string, time.Time) (FreewillDiaryShareGrant, error) {
	return FreewillDiaryShareGrant{GrantID: "grant-test", RelationshipCleanupPending: true}, nil
}
func (diaryCleanupPendingController) ReadDiaryShare(context.Context, string, string, time.Time) (FreewillDiaryShareGrant, FreewillDiaryAnnotationEntry, error) {
	return FreewillDiaryShareGrant{}, FreewillDiaryAnnotationEntry{}, nil
}
func (diaryCleanupPendingController) RecordDiaryConsentDecision(context.Context, string, string, string, string, int, string, time.Time) error {
	return nil
}
func (diaryCleanupPendingController) CreateDiaryShareReply(context.Context, string, FreewillDiaryShareGrant, string, string, time.Time) (FreewillDiaryShareReply, error) {
	return FreewillDiaryShareReply{}, nil
}
func (diaryCleanupPendingController) ReadDiaryShareReplies(context.Context, string, string, time.Time) ([]FreewillDiaryShareReply, error) {
	return nil, nil
}

func TestRelationshipWithdrawalSurfacesPendingPhysicalCleanup(t *testing.T) {
	s := NewServer(Config{FreewillRelationships: relationshipCleanupPendingController{}})
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/freewill/relationships/rel-test", nil)
	w := httptest.NewRecorder()
	s.handleFreewillRelationship(w, r)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusAccepted || body["cleanup_pending"] != true || body["status"] != "withdrawn" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDiaryGrantRevokeSurfacesPendingRelationshipCleanup(t *testing.T) {
	s := NewServer(Config{FreewillDiaryConsent: diaryCleanupPendingController{}, LocalDiaryPrincipalAuth: &LocalFreewillDiaryPrincipalAuth{Enabled: true, OwnerPrincipalID: "synthetic-owner-a"}})
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/freewill/diary/shares/grant-test", nil)
	r = r.WithContext(context.WithValue(r.Context(), freewillDiaryPrincipalContextKey{}, "synthetic-owner-a"))
	w := httptest.NewRecorder()
	s.handleFreewillDiaryShare(w, r)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "\"relationship_cleanup_pending\":true") || !strings.Contains(w.Body.String(), "\"cleanup_pending\":true") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
