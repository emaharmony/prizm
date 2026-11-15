package freewill

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDiaryConsentPersistsFrozenGrantRevocationExpiryAndMetadataAudit(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "consent-store")
	if err := os.MkdirAll(directory, 0777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "consent.db")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0777); err != nil {
			t.Fatal(err)
		}
	}
	store, err := OpenDiaryConsentStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(directory)
		if err != nil || dirInfo.Mode().Perm() != 0700 {
			t.Fatalf("consent directory permissions=%v err=%v", dirInfo.Mode().Perm(), err)
		}
		dbInfo, err := os.Stat(path)
		if err != nil || dbInfo.Mode().Perm() != 0600 {
			t.Fatalf("consent database permissions=%v err=%v", dbInfo.Mode().Perm(), err)
		}
	}

	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)
	share := testDiaryShare("entry-a", 2, now.Add(time.Hour))
	created, err := store.Create(ctx, share.OwnerPrincipalID, share, now)
	if err != nil || created.GrantID == "" || created.EntrySHA256 == "" {
		t.Fatalf("create share=%#v err=%v", created, err)
	}
	if _, err := store.Read(ctx, share.OwnerPrincipalID, created.GrantID, now); err == nil {
		t.Fatal("owner was accepted as the named recipient")
	}
	if _, err := store.Revoke(ctx, share.RecipientPrincipalID, created.GrantID, now); err == nil {
		t.Fatal("recipient was allowed to revoke the owner's grant")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenDiaryConsentStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Read(ctx, share.RecipientPrincipalID, created.GrantID, now.Add(time.Minute))
	if err != nil || read.EntrySHA256 != created.EntrySHA256 || string(read.FrozenView) != string(share.FrozenView) {
		t.Fatalf("reopened grant read=%#v err=%v", read, err)
	}
	if _, err := store.Revoke(ctx, share.OwnerPrincipalID, created.GrantID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDiaryConsentStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, share.RecipientPrincipalID, created.GrantID, now.Add(3*time.Minute)); err == nil {
		t.Fatal("revoked grant became readable after store reopen")
	}

	expiring := testDiaryShare("entry-expiring", 1, now.Add(30*time.Minute))
	expiring.FrozenView = frozenDiaryView(expiring)
	expiringGrant, err := store.Create(ctx, expiring.OwnerPrincipalID, expiring, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDiaryConsentStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, expiring.RecipientPrincipalID, expiringGrant.GrantID, expiring.ExpiresAt.Add(time.Nanosecond)); err == nil {
		t.Fatal("expired grant was readable after store reopen")
	}
	tooLong := testDiaryShare("entry-too-long", 1, now.Add(25*time.Hour))
	if _, err := store.Create(ctx, tooLong.OwnerPrincipalID, tooLong, now); err == nil {
		t.Fatal("share beyond the 24-hour time limit was accepted")
	}
	mismatched := testDiaryShare("entry-mismatch", 2, now.Add(time.Hour))
	var view map[string]any
	if err := json.Unmarshal(mismatched.FrozenView, &view); err != nil {
		t.Fatal(err)
	}
	view["project_id"] = "other-project"
	mismatched.FrozenView, _ = json.Marshal(view)
	if _, err := store.Create(ctx, mismatched.OwnerPrincipalID, mismatched, now); err == nil {
		t.Fatal("snapshot for an unrelated scope was accepted")
	}

	rows, err := store.db.QueryContext(ctx, `SELECT principal_id, action, decision, grant_id, entry_id, entry_revision FROM freewill_diary_consent_audit`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var auditDump strings.Builder
	var audits int
	for rows.Next() {
		var principal, action, decision, grantID, entryID string
		var revision int
		if err := rows.Scan(&principal, &action, &decision, &grantID, &entryID, &revision); err != nil {
			t.Fatal(err)
		}
		audits++
		auditDump.WriteString(strings.Join([]string{principal, action, decision, grantID, entryID}, " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if audits < 8 {
		t.Fatalf("metadata decision audit count=%d want at least 8", audits)
	}
	if strings.Contains(auditDump.String(), "synthetic original diary content") || strings.Contains(auditDump.String(), "annotation text") {
		t.Fatalf("audit table contained diary content: %q", auditDump.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, share.OwnerPrincipalID, share, now); !errors.Is(err, ErrDiaryConsentAuditUnavailable) {
		t.Fatalf("grant with unavailable audit returned %v", err)
	}
}

func TestDiaryShareRepliesAreBoundAppendOnlyAndOwnerReadable(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "consent-replies")
	store, err := OpenDiaryConsentStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)
	share := testDiaryShare("entry-reply", 3, now.Add(time.Hour))
	grant, err := store.Create(ctx, share.OwnerPrincipalID, share, now)
	if err != nil {
		t.Fatal(err)
	}
	expected := grant
	expected.FrozenView = nil
	reply, err := store.CreateReply(ctx, share.RecipientPrincipalID, expected, "reply-once", "Synthetic recipient comment", now.Add(time.Minute))
	if err != nil || reply.ReplyID == "" || reply.EntrySHA256 != grant.EntrySHA256 || reply.Revision != 3 {
		t.Fatalf("reply=%#v err=%v", reply, err)
	}
	if _, err := store.CreateReply(ctx, share.RecipientPrincipalID, expected, "reply-once", "Synthetic recipient comment", now.Add(2*time.Minute)); err == nil {
		t.Fatal("same-request replay was accepted")
	}
	if _, err := store.CreateReply(ctx, share.OwnerPrincipalID, expected, "owner-reply", "owner cannot submit as recipient", now.Add(2*time.Minute)); err == nil {
		t.Fatal("owner was allowed to submit a recipient reply")
	}
	stale := expected
	stale.Revision++
	if _, err := store.CreateReply(ctx, share.RecipientPrincipalID, stale, "reply-stale", "stale snapshot", now.Add(2*time.Minute)); err == nil {
		t.Fatal("stale revision binding was accepted")
	}
	wrongDigest := expected
	wrongDigest.EntrySHA256 = strings.Repeat("0", 64)
	if _, err := store.CreateReply(ctx, share.RecipientPrincipalID, wrongDigest, "reply-digest", "wrong digest", now.Add(2*time.Minute)); err == nil {
		t.Fatal("wrong content digest was accepted")
	}
	second, err := store.CreateReply(ctx, share.RecipientPrincipalID, expected, "reply-second", "A distinct append-only follow-up", now.Add(2*time.Minute))
	if err != nil || second.ReplyID == reply.ReplyID {
		t.Fatalf("second reply=%#v err=%v", second, err)
	}
	if _, err := store.ReadReplies(ctx, share.RecipientPrincipalID, grant.GrantID, now.Add(2*time.Minute)); err == nil {
		t.Fatal("recipient was allowed to read owner reply history")
	}
	got, err := store.ReadReplies(ctx, share.OwnerPrincipalID, grant.GrantID, now.Add(2*time.Minute))
	if err != nil || len(got) != 2 || got[0].Text != "Synthetic recipient comment" || got[0].GrantID != grant.GrantID || got[1].Text != "A distinct append-only follow-up" {
		t.Fatalf("owner replies=%#v err=%v", got, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE freewill_diary_share_replies SET reply_text = 'edited' WHERE reply_id = ?`, reply.ReplyID); err == nil {
		t.Fatal("reply row update bypassed append-only protection")
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM freewill_diary_share_replies WHERE reply_id = ?`, reply.ReplyID); err == nil {
		t.Fatal("reply row deletion bypassed append-only protection")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDiaryConsentStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Revoke(ctx, share.OwnerPrincipalID, grant.GrantID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateReply(ctx, share.RecipientPrincipalID, expected, "reply-after-revoke", "not allowed", now.Add(4*time.Minute)); err == nil {
		t.Fatal("reply to revoked grant was accepted")
	}
	got, err = store.ReadReplies(ctx, share.OwnerPrincipalID, grant.GrantID, now.Add(5*time.Minute))
	if err != nil || len(got) != 2 || got[0].Text != "Synthetic recipient comment" || got[1].Text != "A distinct append-only follow-up" {
		t.Fatalf("owner history after revoke=%#v err=%v", got, err)
	}
	expiresSoon := testDiaryShare("entry-reply-expiring", 1, now.Add(5*time.Minute))
	expiringGrant, err := store.Create(ctx, expiresSoon.OwnerPrincipalID, expiresSoon, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateReply(ctx, expiresSoon.RecipientPrincipalID, expiringGrant, "reply-expired", "expired grant", expiresSoon.ExpiresAt.Add(time.Nanosecond)); err == nil {
		t.Fatal("reply to expired grant was accepted")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT principal_id, action, decision, grant_id, entry_id, entry_revision FROM freewill_diary_consent_audit`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var auditDump strings.Builder
	for rows.Next() {
		var principal, action, decision, grantID, entryID string
		var revision int
		if err := rows.Scan(&principal, &action, &decision, &grantID, &entryID, &revision); err != nil {
			t.Fatal(err)
		}
		auditDump.WriteString(strings.Join([]string{principal, action, decision, grantID, entryID}, " "))
	}
	if strings.Contains(auditDump.String(), "Synthetic recipient comment") || strings.Contains(auditDump.String(), "stale snapshot") || strings.Contains(auditDump.String(), "wrong digest") {
		t.Fatalf("audit contained reply text: %q", auditDump.String())
	}
}

func TestDiaryConsentWindowsRejectsPreexistingExplicitGroupACL(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows ACL behavior only")
	}
	directory := filepath.Join(t.TempDir(), "unsafe-consent-directory")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("icacls", directory, "/grant", "*S-1-1-0:(OI)(CI)F")
	if err := cmd.Run(); err != nil {
		t.Fatalf("prepare synthetic unsafe test ACL: %v", err)
	}
	if err := restrictDiaryConsentPermissions(directory, true); err == nil {
		t.Fatal("preexisting explicit Everyone access was accepted")
	}
}

func testDiaryShare(entryID string, revision int, expires time.Time) DiaryShare {
	share := DiaryShare{OwnerPrincipalID: "synthetic-owner-a", RecipientPrincipalID: "synthetic-recipient-b", EntryID: entryID, Revision: revision,
		ProjectID: "project-a", WorkspaceID: "/synthetic/workspace", AgentID: "agent-a", IntentionID: "intention-a", ParentRunID: "parent-a", ExpiresAt: expires}
	share.FrozenView = frozenDiaryView(share)
	return share
}

func frozenDiaryView(share DiaryShare) []byte {
	view := map[string]any{"entry_id": share.EntryID, "revision": share.Revision, "project_id": share.ProjectID, "workspace_id": share.WorkspaceID,
		"agent_id": share.AgentID, "intention_id": share.IntentionID, "parent_run_id": share.ParentRunID, "original_text": "synthetic original diary content",
		"annotations": []any{map[string]any{"text": "synthetic annotation text"}}}
	encoded, _ := json.Marshal(view)
	return encoded
}
